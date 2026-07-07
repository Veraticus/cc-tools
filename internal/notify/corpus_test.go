package notify

// This file replays the goal-defer behavior (see decideStop's GoalActive
// gate in decide.go, and the b5d8723 refactor that removed all block
// machinery) against small, real-derived transcript slices, plus an opt-in
// harness that replays the full multi-MB originals end to end.
//
// The testdata/corpus_*.jsonl fixtures are NOT synthetic: each record's
// substantive content (goal condition text, Bash command/description,
// backgroundTaskId, ack text, assistant turn text, Stop-hook-feedback text,
// non-sentinel verdict reason/iterations) is copied verbatim from one of the
// two real incident sessions, with only cwd/sessionId/uuid fields anonymized
// and noise fields (thinking blocks, usage, requestId, diagnostics) trimmed
// — the same minimal-reconstruction convention the existing testdata/goal_*
// fixtures already use. Source sessions:
//   - grailquest session (197 historical blocks) —
//     corpus_active_parked_grailquest.jsonl, corpus_goal_met.jsonl.
//   - savecraft session (11 historical blocks) —
//     corpus_active_parked_savecraft.jsonl, corpus_goal_cleared.jsonl.
//
// The raw multi-MB originals are intentionally NOT committed; see
// TestCorpus_FullTranscriptReplay_OptIn below for replaying them directly.
//
// No real "failed:true" goal_status record was found in either source
// transcript (both sessions only ever produced met:false/true, sentinel or
// not — never failed:true), so no corpus_goal_failed.jsonl was extracted.
// That shape stays covered by the existing hand-built testdata/goal_failed.jsonl.
//
// corpus_active_parked_*.jsonl each cover the "loop that used to block":
// an active goal with one still-live (parked) background task, the shape
// decideStop's GoalActive gate now defers on unconditionally. Real Stop
// hook invocations are recorded in-transcript as {"type":"system",
// "subtype":"stop_hook_summary",...} records; corpus_active_parked_
// grailquest.jsonl was trimmed to keep two such real turn-boundaries (the
// two "Stop hook feedback" exchanges) so a single fixture already reflects
// >1 real Stop firing against the same parked task. Both active_parked
// tests additionally call Pipeline.Run twice against the same fixture to
// exercise the exact dynamic that produced the historical block counts:
// repeated Stop invocations against an unchanged (or slowly growing)
// parked-task transcript, each of which the old block-based system would
// have independently flagged.

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockDecisionJSON is the literal stdout fragment that must never appear,
// across every corpus replay in this file.
const blockDecisionJSON = `"decision":"block"`

// runCorpusStop drives one Stop hook invocation over fixture through a fresh
// Pipeline built like newGoalTestPipeline (DryRun:false, real watchdog-arm
// path), returning the accumulated stdout, the decision log path, and the
// session's state dir for the caller to inspect.
func runCorpusStop(
	t *testing.T, stdout *bytes.Buffer, sent *[]capturedRequest, fixture, sessionID string,
) (string, string) {
	t.Helper()
	stubBin := writeStubClaude(t)
	p, logPath := newGoalTestPipeline(t, stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(sent)
	transcript := copyFixture(t, fixture)

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(stdout.String(), blockDecisionJSON) {
		t.Fatalf("stdout = %q, must never contain a block decision", stdout.String())
	}
	return logPath, filepath.Join(p.StateBase, sessionID)
}

// TestCorpus_ActiveParkedGrailquest_TwoStops_SilentNoBlock replays the
// grailquest-derived active+parked shape across two Stop invocations against
// the same fixture (see the file-level comment for why two Run calls stand
// in for "≥2 Stops"): both must resolve silent, arm the watchdog, and never
// print a block decision.
func TestCorpus_ActiveParkedGrailquest_TwoStops_SilentNoBlock(t *testing.T) {
	testActiveParkedTwoStops(t, "corpus_active_parked_grailquest.jsonl", "sess-corpus-grailquest")
}

// TestCorpus_ActiveParkedSavecraft_TwoStops_SilentNoBlock is the
// cross-project counterpart of the grailquest test above, sourced from the
// savecraft incident session.
func TestCorpus_ActiveParkedSavecraft_TwoStops_SilentNoBlock(t *testing.T) {
	testActiveParkedTwoStops(t, "corpus_active_parked_savecraft.jsonl", "sess-corpus-savecraft")
}

func testActiveParkedTwoStops(t *testing.T, fixture, sessionID string) {
	t.Helper()
	stubBin := writeStubClaude(t)
	// decideStop's GoalActive gate returns OutcomeSilent before the judge is
	// ever consulted (see decide.go), so this verdict is never read — it is
	// set defensively, shaped like the old goal-judgment stack's answer, to
	// document that even a block-shaped verdict could not surface as a block.
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"still needs the restart script"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, fixture)
	stateDir := filepath.Join(p.StateBase, sessionID)

	for i := 1; i <= 2; i++ {
		in := HookInput{
			SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
		}
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() #%d error = %v", i, err)
		}
		if strings.Contains(stdout.String(), blockDecisionJSON) {
			t.Fatalf("Run() #%d: stdout = %q, must never contain a block decision", i, stdout.String())
		}
		if len(sent) != 0 {
			t.Errorf("Run() #%d: sent = %+v, want no notification (silent defer)", i, sent)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "watchdog.lock")); err != nil {
			t.Errorf("Run() #%d: watchdog.lock missing, want armed: %v", i, err)
		}

		recs := readDecisionLog(t, logPath)
		if len(recs) != i {
			t.Fatalf("Run() #%d: log records = %d, want %d", i, len(recs), i)
		}
		last := recs[len(recs)-1]
		if last.Outcome != OutcomeSilent.String() {
			t.Errorf("Run() #%d: Outcome = %q, want silent", i, last.Outcome)
		}
		if !strings.Contains(last.Reason, "goal active") {
			t.Errorf("Run() #%d: Reason = %q, want it to mention the goal-active defer", i, last.Reason)
		}
	}
}

// TestCorpus_GoalMet_NoBlock replays the grailquest-derived non-sentinel
// goal_met verdict (iterations>0, the built-in /goal evaluator's own
// evaluated-and-satisfied shape, as distinct from a user-cleared sentinel):
// the goal is no longer active, so decideStop proceeds past the defer gate
// to the ordinary compose route — the universal invariant is still zero
// block decisions.
func TestCorpus_GoalMet_NoBlock(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"epic complete","body":"review approved","reason":"goal met"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "corpus_goal_met.jsonl")

	sessionID := "sess-corpus-goal-met"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(stdout.String(), blockDecisionJSON) {
		t.Fatalf("stdout = %q, must never contain a block decision", stdout.String())
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != OutcomeSend.String() {
		t.Errorf("Outcome = %q, want send (goal met, no live tasks, composing)", recs[0].Outcome)
	}
}

// TestCorpus_GoalCleared_NoBlock replays the savecraft-derived sentinel
// clear (met:true, sentinel:true — the goal cleared before the built-in
// evaluator ever ran non-sentinel): same universal invariant. A cleared goal
// with no live tasks/teammates reaches decideStop's compose route, which
// (on judge success) always sends — Notify must be true here, unlike the
// decide-mode stubs elsewhere in this package, since JudgeModeCompose treats
// notify=false as a judge error (see judge.go).
func TestCorpus_GoalCleared_NoBlock(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"epic complete","body":"goal cleared","reason":"goal cleared, nothing to report"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "corpus_goal_cleared.jsonl")

	sessionID := "sess-corpus-goal-cleared"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(stdout.String(), blockDecisionJSON) {
		t.Fatalf("stdout = %q, must never contain a block decision", stdout.String())
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record (compose mode sends on judge success)", recs)
	}
}

// TestCorpus_AllShapes_NeverBlock is the universal invariant sweep: every
// testdata/corpus_*.jsonl fixture, replayed through one Stop invocation each,
// must never print a block decision — regardless of which outcome (send,
// silent, judge-compose, judge-decide) it resolves to. This globs rather
// than naming files individually so a future corpus addition is covered
// automatically.
func TestCorpus_AllShapes_NeverBlock(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "corpus_*.jsonl"))
	if err != nil {
		t.Fatalf("globbing corpus fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no testdata/corpus_*.jsonl fixtures found")
	}

	for _, fixturePath := range fixtures {
		fixture := filepath.Base(fixturePath)
		t.Run(fixture, func(t *testing.T) {
			t.Setenv(
				"STUB_STDOUT",
				`{"notify":true,"urgency":"done","task":"t","body":"b","reason":"universal invariant sweep"}`,
			)

			var stdout bytes.Buffer
			var sent []capturedRequest
			logPath, _ := runCorpusStop(t, &stdout, &sent, fixture, "sess-corpus-sweep-"+fixture)

			recs := readDecisionLog(t, logPath)
			if len(recs) != 1 {
				t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
			}
		})
	}
}

// stopHookSummaryMarker identifies a transcript line as a real recorded Stop
// hook boundary: Claude Code appends a
// {"type":"system","subtype":"stop_hook_summary",...} record to the
// transcript immediately after each Stop hook invocation finishes, so its
// presence marks exactly the point a real Stop event fired against the
// transcript-so-far. Used only by the opt-in full-transcript replay below.
const stopHookSummaryMarker = `"subtype":"stop_hook_summary"`

// replayInstructions documents how to run the opt-in harness against the two
// real incident originals — the ONE sanctioned t.Skip in this epic, since
// those files are multi-MB, uncommitted, and (the grailquest one) known to
// churn out from under a running session.
const replayInstructions = "CCTOOLS_NOTIFY_REPLAY_TRANSCRIPT not set; skipping opt-in full-transcript replay.\n" +
	"To run it against a real session transcript, point the env var at one and re-run:\n" +
	"  CCTOOLS_NOTIFY_REPLAY_TRANSCRIPT=~/.claude/projects/<project-dir>/<session-id>.jsonl \\\n" +
	"    go test ./internal/notify/ -run TestCorpus_FullTranscriptReplay_OptIn -v\n" +
	"(a live session can rewrite its transcript out from under the test; retry if it reports missing)"

// TestCorpus_FullTranscriptReplay_OptIn is the opt-in harness for replaying
// an entire real transcript end to end. It never runs as part of `make
// test`: it requires CCTOOLS_NOTIFY_REPLAY_TRANSCRIPT to name an existing
// file, and skips (never fails) when that's unset or the file is
// momentarily absent — the sole documented skip this epic allows.
//
// For every real Stop hook boundary found in the transcript (see
// stopHookSummaryMarker), it replays Pipeline.Run against the transcript
// prefix up to that point and asserts stdout never carries a block decision.
// The replay is inherently ~O(N²) in transcript size: each boundary re-runs
// Pipeline.Run, which re-scans the whole prefix from byte zero — exactly as
// production re-scans the transcript on every real Stop. The single growing
// prefix file it builds (monotonically, re-pointing TranscriptPath at it) is
// only a constant-factor saving: it avoids re-copying the source per
// boundary, not the per-boundary full scan.
func TestCorpus_FullTranscriptReplay_OptIn(t *testing.T) {
	path := os.Getenv("CCTOOLS_NOTIFY_REPLAY_TRANSCRIPT")
	if path == "" {
		t.Skip(replayInstructions)
	}
	src, err := os.Open(path) // path comes from an operator-set env var, this is an opt-in local harness
	if err != nil {
		t.Skipf("CCTOOLS_NOTIFY_REPLAY_TRANSCRIPT=%s not readable (%v); skipping.\n%s", path, err, replayInstructions)
	}
	defer func() { _ = src.Close() }()

	stubBin := writeStubClaude(t)
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"replay stub"}`)

	tmpPath := filepath.Join(t.TempDir(), "replay-prefix.jsonl")
	tmpFile, err := os.Create(tmpPath) // fixed temp-dir path, not user input
	if err != nil {
		t.Fatalf("creating replay prefix file: %v", err)
	}
	defer func() { _ = tmpFile.Close() }()
	writer := bufio.NewWriter(tmpFile)

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxLineBufferSize)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin, neverPresent)
	sessionID := "sess-replay-full"

	stops := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if _, writeErr := writer.Write(line); writeErr != nil {
			t.Fatalf("writing replay prefix: %v", writeErr)
		}
		if writeErr := writer.WriteByte('\n'); writeErr != nil {
			t.Fatalf("writing replay prefix: %v", writeErr)
		}
		if !bytes.Contains(line, []byte(stopHookSummaryMarker)) {
			continue
		}
		if flushErr := writer.Flush(); flushErr != nil {
			t.Fatalf("flushing replay prefix: %v", flushErr)
		}
		stops++

		in := HookInput{
			SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: tmpPath, HookEventName: "Stop",
		}
		if runErr := p.Run(context.Background(), in); runErr != nil {
			t.Fatalf("Run() error at stop boundary %d: %v", stops, runErr)
		}
		if strings.Contains(stdout.String(), blockDecisionJSON) {
			t.Fatalf("stop boundary %d: stdout contains a block decision: %q", stops, stdout.String())
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatalf("scanning %s: %v", path, scanErr)
	}
	if stops == 0 {
		t.Skipf("no stop_hook_summary boundaries found in %s; nothing to replay", path)
	}
	t.Logf("replayed %d Stop boundaries from %s with zero block decisions", stops, path)
}
