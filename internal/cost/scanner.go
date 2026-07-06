package cost

import (
	"bufio"
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
// rows to one local date (Daily's "today only" rule); Session passes nil.
// Malformed JSON lines are skipped silently: transcripts are appended to
// live, so a partially-written final line is expected, not an error.
func scanRows(r io.Reader, subscribed bool, dedup map[string]bool, filter *dateFilter) (float64, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxLineBufferSize)

	var total float64
	for scanner.Scan() {
		var row transcriptRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if !isBillableRow(row) {
			continue
		}
		if isDuplicate(row, dedup) {
			continue
		}
		if filter != nil && !filter.includes(row.Timestamp) {
			continue
		}
		total += rowCost(row, subscribed)
	}
	if err := scanner.Err(); err != nil {
		return total, fmt.Errorf("scanning transcript: %w", err)
	}
	return total, nil
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
