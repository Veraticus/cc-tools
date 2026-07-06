package cost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Scanner tuning: transcript lines can run multi-hundred-KB (a single
// tool_result can embed a large file), so lines are streamed one at a time
// with a generous max size rather than ever reading a whole file into
// memory.
const (
	initialScanBufferSize = 64 * 1024
	maxLineBufferSize     = 10 * 1024 * 1024
)

// transcriptTypeAssistant is the only top-level "type" value billing ever
// counts: user, queue-operation, attachment, etc. records are never
// billable.
const transcriptTypeAssistant = "assistant"

// assistantTypeMarker is the compact-JSON byte sequence maybeAssistantRow
// uses as a cheap pre-unmarshal gate: every real assistant row's "type"
// field is emitted in this exact compact form. Most of a transcript's
// bytes are huge non-billable tool_result/user rows, so skipping
// json.Unmarshal for lines that plainly can't be an assistant row keeps a
// cold-cache scan of a large corpus well under the statusline's render
// budget. A row whose CONTENT happens to contain this literal (e.g. a user
// message quoting it) is a rare false positive that just pays for the
// unmarshal — isBillableRow (the authoritative check, run after unmarshal)
// still rejects it correctly.
const assistantTypeMarker = `"type":"assistant"`

// syntheticModel is the message.model value Claude Code emits for
// synthetic/non-billed assistant records; such rows are never counted even
// though they otherwise look like a normal assistant turn with usage.
const syntheticModel = "<synthetic>"

// bedrockMsgIDPrefix identifies a bedrock-backed message by its
// message.id: the transcript reports the same plain claude-* model id on
// both backends, so the id prefix is the only reliable backend signal.
const bedrockMsgIDPrefix = "msg_bdrk"

// noRequestIDKey stands in for a missing or null requestId when building a
// dedup key, so that "no requestId" is a single stable bucket rather than
// silently colliding with (or being indistinguishable from) an empty
// string requestId.
const noRequestIDKey = "no-req"

// dateLayout is the local-date bucketing key used to decide whether a row
// belongs to "today".
const dateLayout = "2006-01-02"

// transcriptRow is the subset of one transcript JSONL line's shape that
// billing cares about. Unknown fields are ignored by encoding/json.
type transcriptRow struct {
	Type      string       `json:"type"`
	Timestamp string       `json:"timestamp"`
	RequestID string       `json:"requestId"`
	Message   *messageInfo `json:"message"`
}

// messageInfo is the message payload of a billed row.
type messageInfo struct {
	ID    string     `json:"id"`
	Model string     `json:"model"`
	Usage *usageInfo `json:"usage"`
}

// usageInfo is the token-usage payload of a billed row.
type usageInfo struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// dateFilter restricts scanRows to rows whose timestamp falls on one local
// date. A nil *dateFilter (Session's case) disables date filtering
// entirely: every row that passes the row filter and dedup is counted
// regardless of its timestamp.
type dateFilter struct {
	loc   *time.Location
	today string
}

// newTodayFilter builds a dateFilter selecting now's own local date, in
// now's own time.Location. Using now.Location() rather than time.Local
// keeps date bucketing independent of the process's global time.Local,
// which a test cannot reliably repoint (TZ env changes don't re-init an
// already-loaded time.Local).
func newTodayFilter(now time.Time) *dateFilter {
	return &dateFilter{loc: now.Location(), today: now.Format(dateLayout)}
}

// includes reports whether timestamp (an RFC3339 string) falls on the
// filter's target local date. An unparseable timestamp is never included:
// Daily can't bucket what it can't date.
func (f *dateFilter) includes(timestamp string) bool {
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false
	}
	return t.In(f.loc).Format(dateLayout) == f.today
}

// openTranscriptFile opens path for reading via os.OpenRoot(dir).Open(base)
// rather than a direct os.Open(path): confining the open to the file's own
// parent directory, resolved through an os.Root handle, is what keeps a
// caller-supplied path from tripping gosec's G304 (potential file
// inclusion via variable) while still allowing an arbitrary transcript
// path, which this package's whole purpose requires.
func openTranscriptFile(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening directory %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(base)
	if err != nil {
		return nil, fmt.Errorf("opening transcript file %s: %w", path, err)
	}
	return f, nil
}

// scanTranscriptFile opens relPath within root and scans it, adding to
// dedup and applying the today-only filter derived from now. Any open
// error is returned to the caller (Daily treats it as "skip this file,
// not fatal").
func scanTranscriptFile(
	root *os.Root,
	relPath string,
	subscribed bool,
	dedup map[string]bool,
	now *time.Time,
) (float64, error) {
	f, err := root.Open(relPath)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", relPath, err)
	}
	defer func() { _ = f.Close() }()

	return scanRows(f, subscribed, dedup, newTodayFilter(*now))
}

// scanRows streams r line by line, summing the USD cost of every billable,
// non-duplicate row. filter, when non-nil, additionally restricts counted
// rows to one local date (the "today only" rule applied to every file
// except the session file itself; see scanSessionRows and Costs' doc
// comment for why the session file never date-filters this way).
func scanRows(r io.Reader, subscribed bool, dedup map[string]bool, filter *dateFilter) (float64, error) {
	var total float64
	err := forEachBillableRow(r, dedup, func(row transcriptRow) {
		if filter != nil && !filter.includes(row.Timestamp) {
			return
		}
		total += rowCost(row, subscribed)
	})
	return total, err
}

// scanSessionRows streams r (the session transcript file) in a single
// pass, returning both its unfiltered total (sessionUSD, no date filter —
// see Costs' doc comment) and the subset of that same total whose rows
// also fall on filter's target date (todayUSD). Scanning the session file
// only once and deriving both figures from the same pass is what lets
// Costs avoid Daily's old double-scan of the largest, live-appended file
// in the corpus.
func scanSessionRows(
	r io.Reader,
	subscribed bool,
	dedup map[string]bool,
	filter *dateFilter,
) (float64, float64, error) {
	var sessionUSD, todayUSD float64
	err := forEachBillableRow(r, dedup, func(row transcriptRow) {
		amount := rowCost(row, subscribed)
		sessionUSD += amount
		if filter.includes(row.Timestamp) {
			todayUSD += amount
		}
	})
	return sessionUSD, todayUSD, err
}

// forEachBillableRow streams r line by line and invokes onRow once for
// every row that decodes as valid JSON, passes isBillableRow, and is not
// a dedup-set duplicate (dedup is read and updated here, so every caller
// shares one dedup rule). Two cheap gates run before the authoritative
// checks:
//
//   - assistantTypeMarker: a byte-level bytes.Contains check run before
//     json.Unmarshal. Most of a transcript's bytes are huge non-billable
//     tool_result/user rows, so skipping the unmarshal for lines that
//     plainly cannot be an assistant row keeps a cold-cache scan of a
//     large (~25MB/day) corpus well under the statusline's render
//     budget. A real assistant row's compact-JSON "type" field always
//     contains this exact literal, so a genuine assistant row can never
//     be filtered out here; a row whose CONTENT happens to also contain
//     the literal (e.g. a user message quoting it) is a rare false
//     positive that just pays for the unmarshal, since isBillableRow —
//     the authoritative check — still rejects it correctly afterward.
//   - the line-skipping split function (see newLineSkippingSplit):
//     transcripts are appended to live, so a partially-written final
//     line is expected and skipped like any other malformed JSON; a
//     line exceeding maxLineBufferSize is presumed non-billable junk and
//     is discarded without unmarshaling, but scanning continues with the
//     next line rather than aborting the whole file (see the task's
//     oversized-line fix — the old bufio.Scanner default surfaced
//     ErrTooLong and the caller undercounted the entire file to 0).
func forEachBillableRow(r io.Reader, dedup map[string]bool, onRow func(transcriptRow)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxLineBufferSize)
	scanner.Split(newLineSkippingSplit(maxLineBufferSize))

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(assistantTypeMarker)) {
			continue
		}
		var row transcriptRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		if !isBillableRow(row) {
			continue
		}
		if isDuplicate(row, dedup) {
			continue
		}
		onRow(row)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning transcript: %w", err)
	}
	return nil
}

// newLineSkippingSplit returns a bufio.SplitFunc behaviorally identical to
// bufio.ScanLines, except a token that would exceed maxToken bytes is
// discarded — never returned as a token, never surfaced as an error —
// instead of aborting the whole scan via bufio.ErrTooLong. maxToken must
// equal the max size passed to the same Scanner's Buffer call: that
// equality is what lets this split function's own discard trigger before
// the Scanner's internal "buffer can't grow further" enforcement ever
// engages. The returned func is stateful (closes over a "currently
// discarding an oversized line" flag) and so must be used by exactly one
// Scanner.
func newLineSkippingSplit(maxToken int) bufio.SplitFunc {
	skipping := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if skipping {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				skipping = false
				return i + 1, nil, nil
			}
			if atEOF {
				skipping = false
			}
			return len(data), nil, nil
		}
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, dropCR(data[:i]), nil
		}
		if atEOF {
			if len(data) == 0 {
				return 0, nil, nil
			}
			return len(data), dropCR(data), nil
		}
		if len(data) >= maxToken {
			// No '\n' within maxToken bytes: this line exceeds the cap.
			// Discard everything buffered so far (advance consumes it
			// without producing a token) and keep looking for the
			// terminator across however many further reads it takes.
			skipping = true
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
}

// dropCR drops data's trailing carriage return, if any, matching
// bufio.ScanLines' own CRLF normalization (unexported in the standard
// library, so duplicated here for newLineSkippingSplit's non-skip path).
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}

// isBillableRow reports whether row is a real billed API call: an
// assistant record carrying non-nil usage, whose model is not the
// synthetic placeholder. isSidechain is deliberately not checked here —
// sidechain rows are real billed API calls and must be included.
func isBillableRow(row transcriptRow) bool {
	if row.Type != transcriptTypeAssistant {
		return false
	}
	if row.Message == nil || row.Message.Usage == nil {
		return false
	}
	return row.Message.Model != syntheticModel
}

// isDuplicate reports whether row has already been counted, recording it
// in dedup if not. A row with no message.id is never deduped: such rows
// carry no identity to dedup on (see the task's dedup rule), so each one
// always counts, and none of them are recorded in dedup either — otherwise
// every id-less row would collapse onto the same "" key and only the first
// would ever count.
func isDuplicate(row transcriptRow, dedup map[string]bool) bool {
	if row.Message == nil || row.Message.ID == "" {
		return false
	}
	requestID := row.RequestID
	if requestID == "" {
		requestID = noRequestIDKey
	}
	key := row.Message.ID + ":" + requestID
	if dedup[key] {
		return true
	}
	dedup[key] = true
	return false
}

// rowCost returns row's USD cost under subscribed. Backend is determined
// solely by the message.id prefix (see bedrockMsgIDPrefix): bedrock rows
// are always priced; anthropic rows are priced only when the caller is not
// on a subscription. An unknown model id, on either backend, prices at $0
// rather than falling back to a guessed family rate.
func rowCost(row transcriptRow, subscribed bool) float64 {
	isBedrock := strings.HasPrefix(row.Message.ID, bedrockMsgIDPrefix)
	if !isBedrock && subscribed {
		return 0
	}

	lookup := listRates
	if isBedrock {
		lookup = bedrockRates
	}
	r, ok := lookup(row.Message.Model)
	if !ok {
		return 0
	}

	usage := row.Message.Usage
	return r.cost(usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
}
