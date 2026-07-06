package cost_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Veraticus/cc-tools/internal/cost"
)

// rowOpts describes one synthetic transcript JSONL line for a test fixture.
// Zero-valued fields take the described defaults; see buildRow.
type rowOpts struct {
	rowType   string // defaults to "assistant"
	msgID     string // omitted from message when ""
	model     string
	requestID string // top-level requestId; omitted from JSON when "" and !nullReqID
	nullReqID bool   // emit `"requestId":null` explicitly
	sidechain bool
	timestamp string
	noMessage bool // emit `"message":null`
	noUsage   bool // omit message.usage entirely

	input, output, cacheRead, cacheWrite int
}

// buildRow renders one transcript JSONL line matching the real Claude Code
// shape described in the task brief, with just enough knobs to exercise the
// row filter, dedup, billing, and date-bucketing rules under test.
func buildRow(o rowOpts) string {
	rowType := o.rowType
	if rowType == "" {
		rowType = "assistant"
	}

	var sb strings.Builder
	sb.WriteString("{")
	fmt.Fprintf(&sb, `"type":%q,`, rowType)
	fmt.Fprintf(&sb, `"isSidechain":%t,`, o.sidechain)
	fmt.Fprintf(&sb, `"timestamp":%q,`, o.timestamp)
	switch {
	case o.nullReqID:
		sb.WriteString(`"requestId":null,`)
	case o.requestID != "":
		fmt.Fprintf(&sb, `"requestId":%q,`, o.requestID)
	}
	sb.WriteString(`"sessionId":"sess1",`)

	if o.noMessage {
		sb.WriteString(`"message":null`)
	} else {
		sb.WriteString(`"message":{`)
		if o.msgID != "" {
			fmt.Fprintf(&sb, `"id":%q,`, o.msgID)
		}
		fmt.Fprintf(&sb, `"model":%q`, o.model)
		if !o.noUsage {
			fmt.Fprintf(
				&sb,
				`,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}`,
				o.input,
				o.output,
				o.cacheRead,
				o.cacheWrite,
			)
		}
		sb.WriteString("}")
	}
	sb.WriteString("}")
	return sb.String()
}

// writeJSONL writes lines (already-built JSONL rows, or raw garbage strings
// for malformed-line tests) to path, one per line, terminated by a trailing
// newline.
func writeJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}

// fableBedrockCost hand-computes, from the bedrock pricing table in the task
// brief, the USD cost of one claude-fable-5 bedrock row with the given token
// counts.
func fableBedrockCost(input, output, cacheRead, cacheWrite int) float64 {
	const (
		in  = 0.000011
		out = 0.000055
		cr  = 0.0000011
		cw  = 0.00001375
	)
	return float64(input)*in + float64(output)*out + float64(cacheRead)*cr + float64(cacheWrite)*cw
}

// fableListCost hand-computes, from the list (anthropic, unsubscribed)
// pricing table in the task brief, the USD cost of one claude-fable-5 row
// with the given token counts.
func fableListCost(input, output, cacheRead, cacheWrite int) float64 {
	const (
		in  = 0.00001
		out = 0.00005
		cr  = 0.000001
		cw  = 0.0000125
	)
	return float64(input)*in + float64(output)*out + float64(cacheRead)*cr + float64(cacheWrite)*cw
}

const floatTolerance = 1e-9

func assertWithinTolerance(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if diff := got - want; diff > floatTolerance || diff < -floatTolerance {
		t.Fatalf("%s: got %v, want %v (diff %v)", msg, got, want, diff)
	}
}

// --- Row filter ---

func TestSessionRowFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	kept := buildRow(rowOpts{
		msgID: "msg_bdrk_kept", model: "claude-fable-5", sidechain: true,
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})
	userRow := buildRow(rowOpts{
		rowType: "user", msgID: "msg_bdrk_user", model: "claude-fable-5",
		timestamp: "2026-07-06T16:00:01.000Z",
		input:     100, output: 100,
	})
	syntheticRow := buildRow(rowOpts{
		msgID: "msg_bdrk_synth", model: "<synthetic>",
		timestamp: "2026-07-06T16:00:02.000Z",
		input:     100, output: 100,
	})
	noUsageRow := buildRow(rowOpts{
		msgID: "msg_bdrk_nousage", model: "claude-fable-5", noUsage: true,
		timestamp: "2026-07-06T16:00:03.000Z",
	})

	writeJSONL(t, path, []string{kept, userRow, syntheticRow, noUsageRow})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "row filter should keep only the billable sidechain row")
}

// --- Dedup ---

func TestSessionDedupSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	row1 := buildRow(rowOpts{
		msgID: "msg_bdrk_dup", model: "claude-fable-5", requestID: "req_1",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})
	// Same message id + requestId, as when a turn's text part and tool_use
	// part are logged as two separate rows.
	row2 := buildRow(rowOpts{
		msgID: "msg_bdrk_dup", model: "claude-fable-5", requestID: "req_1",
		timestamp: "2026-07-06T16:00:00.500Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})

	writeJSONL(t, path, []string{row1, row2})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "duplicate id+requestId within one file should be counted once")
}

func TestDailyDedupAcrossFilesWithNullRequestID(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	row := buildRow(rowOpts{
		msgID: "msg_bdrk_resumed", model: "claude-fable-5", nullReqID: true,
		timestamp: now.UTC().Format(time.RFC3339),
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})

	// Simulate a resumed session: the parent session's row is re-logged into
	// a new file under a different sessionId, but with the same message.id
	// and a null requestId. Both files must be scanned for Daily to find
	// today's rows across all live transcripts, but the shared dedup set
	// must prevent double counting.
	writeJSONL(t, filepath.Join(dir, "session-a.jsonl"), []string{row})
	writeJSONL(t, filepath.Join(dir, "session-b.jsonl"), []string{row})

	got, err := cost.Daily(dir, now, false)
	if err != nil {
		t.Fatalf("Daily: unexpected error: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "cross-file dedup on null requestId should count the resumed row once")
}

func TestDedupNoMessageIDCountsEachRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	// Two rows with no message.id at all: never deduped, so both must count
	// even though they are otherwise identical.
	row := buildRow(rowOpts{
		model:     "claude-fable-5",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})

	writeJSONL(t, path, []string{row, row})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	// No message.id means backend defaults to anthropic (no "msg_bdrk"
	// prefix); subscribed=false so list rates apply, and both rows count.
	want := 2 * fableListCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "rows with no message.id should never be deduped")
}

// --- Billing ---

func TestBillingBedrockRegardlessOfSubscribed(t *testing.T) {
	dir := t.TempDir()

	row := buildRow(rowOpts{
		msgID: "msg_bdrk_01abc", model: "claude-fable-5",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     100, output: 50, cacheRead: 10, cacheWrite: 20,
	})

	pathSubscribed := filepath.Join(dir, "subscribed.jsonl")
	writeJSONL(t, pathSubscribed, []string{row})
	gotSubscribed, err := cost.Session(pathSubscribed, true)
	if err != nil {
		t.Fatalf("Session (subscribed): unexpected error: %v", err)
	}

	pathUnsubscribed := filepath.Join(dir, "unsubscribed.jsonl")
	writeJSONL(t, pathUnsubscribed, []string{row})
	gotUnsubscribed, err := cost.Session(pathUnsubscribed, false)
	if err != nil {
		t.Fatalf("Session (unsubscribed): unexpected error: %v", err)
	}

	want := fableBedrockCost(100, 50, 10, 20)
	assertWithinTolerance(t, gotSubscribed, want, "bedrock row must be priced when subscribed=true")
	assertWithinTolerance(t, gotUnsubscribed, want, "bedrock row must be priced when subscribed=false")
}

func TestBillingAnthropicSubscribedIsFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	row := buildRow(rowOpts{
		msgID: "msg_01xyz", model: "claude-fable-5",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     100, output: 50, cacheRead: 10, cacheWrite: 20,
	})
	writeJSONL(t, path, []string{row})

	got, err := cost.Session(path, true)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	assertWithinTolerance(t, got, 0, "subscribed anthropic row must cost $0")
}

func TestBillingAnthropicUnsubscribedUsesListRates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	row := buildRow(rowOpts{
		msgID: "msg_01xyz", model: "claude-fable-5",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     100, output: 50, cacheRead: 10, cacheWrite: 20,
	})
	writeJSONL(t, path, []string{row})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	want := fableListCost(100, 50, 10, 20)
	assertWithinTolerance(t, got, want, "unsubscribed anthropic row must use list rates")
}

func TestBillingUnknownModelIsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	row := buildRow(rowOpts{
		msgID: "msg_bdrk_unknown", model: "claude-unheard-of-9",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     100, output: 50, cacheRead: 10, cacheWrite: 20,
	})
	writeJSONL(t, path, []string{row})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error: %v", err)
	}
	assertWithinTolerance(t, got, 0, "unknown model id must never be guessed at, must cost $0")
}

// --- Local-date bucketing ---

func TestDailyLocalDateBucketing(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("loading America/Los_Angeles: %v", err)
	}

	// now is 2026-07-05 23:00 in Los Angeles.
	now := time.Date(2026, time.July, 5, 23, 0, 0, 0, la)

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	// This row's timestamp is 2026-07-06T02:00:00Z, which converts to
	// 2026-07-05 19:00 in Los Angeles -- the same local date as now, so it
	// must count for "today".
	inBucket := buildRow(rowOpts{
		msgID: "msg_bdrk_today", model: "claude-fable-5",
		timestamp: "2026-07-06T02:00:00.000Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})
	// This row is a full day earlier in Los Angeles local time and must be
	// excluded, proving the bucketing filter actually excludes something.
	outOfBucket := buildRow(rowOpts{
		msgID: "msg_bdrk_yesterday", model: "claude-fable-5",
		timestamp: "2026-07-04T10:00:00.000Z",
		input:     999, output: 999, cacheRead: 999, cacheWrite: 999,
	})

	writeJSONL(t, path, []string{inBucket, outOfBucket})

	got, err := cost.Daily(dir, now, false)
	if err != nil {
		t.Fatalf("Daily: unexpected error: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "only the row whose local date matches now's local date should count")
}

// --- mtime skip ---

func TestDailyMtimeSkip(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")

	// Content, if scanned, would count -- but the file's mtime predates
	// local midnight of now, so Daily must skip it without ever opening it.
	row := buildRow(rowOpts{
		msgID: "msg_bdrk_old", model: "claude-fable-5",
		timestamp: now.UTC().Format(time.RFC3339),
		input:     100, output: 100, cacheRead: 100, cacheWrite: 100,
	})
	writeJSONL(t, path, []string{row})

	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	oldTime := midnight.Add(-24 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	got, err := cost.Daily(dir, now, false)
	if err != nil {
		t.Fatalf("Daily: unexpected error: %v", err)
	}
	assertWithinTolerance(t, got, 0, "a file with mtime before local midnight must be skipped")
}

// --- Malformed lines / empty files ---

func TestSessionMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	good := buildRow(rowOpts{
		msgID: "msg_bdrk_good", model: "claude-fable-5",
		timestamp: "2026-07-06T16:00:00.000Z",
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})

	writeJSONL(t, path, []string{
		good,
		"not json at all {{{",
		"",
		`{"type":"assistant","message":`, // truncated/partial line
	})

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error on malformed lines: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "malformed lines must be skipped silently")
}

func TestSessionEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	writeJSONL(t, path, nil)

	got, err := cost.Session(path, false)
	if err != nil {
		t.Fatalf("Session: unexpected error on empty file: %v", err)
	}
	assertWithinTolerance(t, got, 0, "empty file must cost 0")
}

func TestDailyEmptyDir(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	got, err := cost.Daily(dir, now, false)
	if err != nil {
		t.Fatalf("Daily: unexpected error on empty dir: %v", err)
	}
	assertWithinTolerance(t, got, 0, "empty projects dir must cost 0")
}

// --- Missing / unreadable paths ---

func TestSessionMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.jsonl")

	if _, err := cost.Session(path, false); err == nil {
		t.Fatal("Session: expected error for missing file, got nil")
	}
}

func TestDailyMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	if _, err := cost.Daily(dir, time.Now(), false); err == nil {
		t.Fatal("Daily: expected error for missing dir, got nil")
	}
}

func TestDailyUnreadableFileAmongReadableOnes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are bypassed, cannot exercise unreadable-file skip")
	}

	dir := t.TempDir()
	now := time.Now()

	goodRow := buildRow(rowOpts{
		msgID: "msg_bdrk_readable", model: "claude-fable-5",
		timestamp: now.UTC().Format(time.RFC3339),
		input:     10, output: 5, cacheRead: 1, cacheWrite: 2,
	})
	badRow := buildRow(rowOpts{
		msgID: "msg_bdrk_unreadable", model: "claude-fable-5",
		timestamp: now.UTC().Format(time.RFC3339),
		input:     999, output: 999, cacheRead: 999, cacheWrite: 999,
	})

	goodPath := filepath.Join(dir, "readable.jsonl")
	badPath := filepath.Join(dir, "unreadable.jsonl")
	writeJSONL(t, goodPath, []string{goodRow})
	writeJSONL(t, badPath, []string{badRow})

	if err := os.Chmod(badPath, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(badPath, 0o600) }()

	got, err := cost.Daily(dir, now, false)
	if err != nil {
		t.Fatalf("Daily: unexpected error: %v", err)
	}
	want := fableBedrockCost(10, 5, 1, 2)
	assertWithinTolerance(t, got, want, "an unreadable file must be skipped, not fatal, among readable ones")
}

// --- End-to-end fixture ---

func TestDailyEndToEndFixture(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ts := now.UTC().Format(time.RFC3339)

	bedrockRow1 := buildRow(rowOpts{
		msgID: "msg_bdrk_e2e1", model: "claude-fable-5", requestID: "req_1",
		timestamp: ts, input: 100, output: 50, cacheRead: 10, cacheWrite: 20,
	})
	bedrockRow2 := buildRow(rowOpts{
		msgID: "msg_bdrk_e2e2", model: "claude-fable-5", requestID: "req_2",
		timestamp: ts, input: 200, output: 60, cacheRead: 0, cacheWrite: 5,
	})
	bedrockRow3 := buildRow(rowOpts{
		msgID: "msg_bdrk_e2e3", model: "claude-fable-5", requestID: "req_3",
		timestamp: ts, input: 50, output: 25, cacheRead: 2, cacheWrite: 0,
	})
	subscribedAnthropicRow := buildRow(rowOpts{
		msgID: "msg_01e2e4", model: "claude-fable-5", requestID: "req_4",
		timestamp: ts, input: 1000, output: 1000, cacheRead: 1000, cacheWrite: 1000,
	})

	path := filepath.Join(dir, "e2e.jsonl")
	writeJSONL(t, path, []string{bedrockRow1, bedrockRow2, bedrockRow3, subscribedAnthropicRow})

	got, err := cost.Daily(dir, now, true)
	if err != nil {
		t.Fatalf("Daily: unexpected error: %v", err)
	}

	want := fableBedrockCost(100, 50, 10, 20) +
		fableBedrockCost(200, 60, 0, 5) +
		fableBedrockCost(50, 25, 2, 0)
	// subscribedAnthropicRow costs $0 (subscribed=true, anthropic backend).
	assertWithinTolerance(t, got, want, "end-to-end fixture: 3 bedrock rows + 1 free subscribed row")
}
