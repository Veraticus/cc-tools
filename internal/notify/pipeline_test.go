package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readDecisionLog reads every JSON line in path as a DecisionRecord.
func readDecisionLog(t *testing.T, path string) []DecisionRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading decision log %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	recs := make([]DecisionRecord, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec DecisionRecord
		if unmarshalErr := json.Unmarshal([]byte(line), &rec); unmarshalErr != nil {
			t.Fatalf("unmarshaling decision record %q: %v", line, unmarshalErr)
		}
		recs = append(recs, rec)
	}
	return recs
}

// newTestPipeline builds a DryRun Pipeline pointed at a fresh temp state base,
// with judgeBin as the stub claude binary and present as the injected
// presence check.
func newTestPipeline(
	t *testing.T,
	stdout *bytes.Buffer,
	judgeBin string,
	present func([]string, time.Time) bool,
) (Pipeline, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	p := Pipeline{
		DryRun:  true,
		Judge:   Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:     DecisionLog{Path: logPath},
		Stdout:  stdout,
		Host:    "testhost",
		Present: present,
	}
	return p, logPath
}

func neverPresent(_ []string, _ time.Time) bool { return false }

// goalIncidentDaemonCondition is the exact goal condition text baked into the
// goal_incident_daemon.jsonl fixture's goal_status attachment: it reproduces
// the July 5 grailquest incident, where a live background-Bash daemon parked
// under an armed goal caused Claude Code's built-in /goal evaluator to skip
// re-evaluating the goal forever (it defers whenever a Stop finds a live
// background task). At 235 bytes it exceeds maxGoalConditionLen, so tests
// keying off it also exercise the block-reason truncation path.
const goalIncidentDaemonCondition = "Get the grailquest content daemon (grailquestd-play) running stably on " +
	"127.0.0.1:8080 serving real content, with the health check passing three times in a row before you stop; " +
	"if it crashes, restart it and keep going without asking me."

// newGoalTestPipeline builds a Pipeline pointed at a fresh temp state base
// with DryRun:false (unlike newTestPipeline, which fixes DryRun:true) —
// tests needing the real, non-DryRun notification-send path that DryRun:true
// can never reach use this instead. Watchdog is left nil: tests that need to
// observe a real arm attempt inject a *fakeWatchdog explicitly.
func newGoalTestPipeline(
	t *testing.T,
	stdout *bytes.Buffer,
	judgeBin string,
	present func([]string, time.Time) bool,
) (Pipeline, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	p := Pipeline{
		DryRun:  false,
		Judge:   Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:     DecisionLog{Path: logPath},
		Stdout:  stdout,
		SelfBin: judgeBin,
		Present: present,
	}
	return p, logPath
}

// memDedupeState adapts a fresh MemoryState to DedupeState for tests that
// need to seed or inspect Pipeline dedupe state directly, without a live
// daemon event loop: safe because these tests only ever drive Pipeline.Run
// from a single goroutine at a time, so MemoryState's loop-confinement
// requirement (see its doc comment) is trivially satisfied here.
type memDedupeState struct {
	m *MemoryState
}

func newMemDedupeState() memDedupeState {
	return memDedupeState{m: NewMemoryState()}
}

func (d memDedupeState) SinceLastNotify(_ context.Context, sessionID string, now time.Time) time.Duration {
	return d.m.SinceLastNotify(sessionID, now)
}

func (d memDedupeState) SinceLastNotifySame(
	_ context.Context, sessionID string, now time.Time, message string,
) time.Duration {
	return d.m.SinceLastNotifySame(sessionID, now, message)
}

func (d memDedupeState) MarkNotified(_ context.Context, sessionID string, t time.Time, message string) error {
	return d.m.MarkNotified(sessionID, t, message)
}

func (d memDedupeState) ClaimSend(
	_ context.Context, sessionID string, now time.Time, message string, window time.Duration, dryRun bool,
) (bool, time.Duration) {
	return d.m.ClaimSend(sessionID, now, message, window, dryRun)
}

func (d memDedupeState) ClaimBroadcast(
	_ context.Context, key string, window time.Duration, now time.Time, dryRun bool,
) bool {
	return d.m.ClaimBroadcast(key, window, now, dryRun)
}

func (d memDedupeState) DeleteSession(_ context.Context, sessionID string) {
	d.m.DeleteSession(sessionID)
}

// fakeWatchdog is a spy Pipeline.Watchdog for tests that need to observe
// arm/reap calls without a live daemon event loop.
type fakeWatchdog struct {
	armed  []WatchdogArmRequest
	reaped []string
}

func (f *fakeWatchdog) Arm(req WatchdogArmRequest) { f.armed = append(f.armed, req) }
func (f *fakeWatchdog) Reap(sessionID string)      { f.reaped = append(f.reaped, sessionID) }

// capturedRequest is what stubSenderRecording's round tripper records for
// one Sender.Send call: everything a real ntfy POST would have carried.
type capturedRequest struct {
	Title    string
	Body     string
	Priority string
	Tags     string
}

// roundTripFunc adapts a plain function to http.RoundTripper, so
// stubSenderRecording can inject its capture logic without a real network
// call.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubSenderRecording returns a Sender whose Send calls are captured into
// *captured and always succeed with 200 OK, never touching the network:
// Pipeline.Sender is a concrete Sender struct (not an interface), so tests
// intercept delivery via Sender.Client's Transport rather than a fake
// implementation.
func stubSenderRecording(captured *[]capturedRequest) Sender {
	return Sender{
		URL: "http://stub.invalid/publish",
		Client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				*captured = append(*captured, capturedRequest{
					Title:    req.Header.Get("Title"),
					Body:     string(body),
					Priority: req.Header.Get("Priority"),
					Tags:     req.Header.Get("Tags"),
				})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
			}),
		},
	}
}

func TestPipeline_SessionEnd_ReapsWatchdog(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	wd := &fakeWatchdog{}
	p.Watchdog = wd

	sessionID := "sess-end-1"
	in := HookInput{SessionID: sessionID, HookEventName: "SessionEnd"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.reaped) != 1 || wd.reaped[0] != sessionID {
		t.Errorf("reaped = %v, want [%s]", wd.reaped, sessionID)
	}
}

// TestPipeline_SessionEnd_NilWatchdog_NeverPanics proves the SessionEnd
// branch's Watchdog.Reap call is properly guarded: a Pipeline with no
// Watchdog configured (the hook client's inline fallback shape) must not
// panic on SessionEnd.
func TestPipeline_SessionEnd_NilWatchdog_NeverPanics(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)

	in := HookInput{SessionID: "sess-end-nil-watchdog", HookEventName: "SessionEnd"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPipeline_Stop_GoalActive_DryRun_WouldArmWatchdog(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-1", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent outcome writes no DRY RUN line)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1", len(recs))
	}
	if recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("Outcome = %q, want silent", recs[0].Outcome)
	}
	if !strings.Contains(recs[0].Reason, "would arm watchdog") {
		t.Errorf("Reason = %q, want it to mention would arm watchdog", recs[0].Reason)
	}
}

func TestPipeline_PermissionPrompt_BadTranscript_StillDelivers(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)

	in := HookInput{
		SessionID:        "sess-2",
		CWD:              "/home/user/project",
		TranscriptPath:   "/nonexistent/path.jsonl",
		HookEventName:    "Notification",
		NotificationType: "permission_prompt",
		Message:          "Claude needs permission to run rm",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [blocked]") {
		t.Errorf("stdout = %q, want DRY RUN blocked line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Claude needs permission to run rm") {
		t.Errorf("stdout = %q, want it to contain the message", stdout.String())
	}
}

func TestPipeline_Stop_Clean_ComposeVerdict_DryRunLine(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"ship it","body":"all tests green","reason":"turn ended cleanly"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin, neverPresent)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-3", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "ship it") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

func TestPipeline_Stop_Clean_ComposeJudgeError_FallbackSessionIdle(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin, neverPresent)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-4", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "All done here.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "project · testhost") {
		t.Errorf("stdout = %q, want fallback title located by host", stdout.String())
	}
	if !strings.Contains(stdout.String(), "All done here.") {
		t.Errorf("stdout = %q, want it to contain last assistant message tail", stdout.String())
	}
}

func TestPipeline_Stop_LiveTasks_DecideNotifyFalse_Silent(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"parked, still building"}`)

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin, neverPresent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-5", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no DRY RUN line)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "parked, still building") {
		t.Errorf("Reason = %q, want verdict reason", recs[0].Reason)
	}
}

func TestPipeline_Stop_LiveTasks_DecideNotifyTrue_UserPresent_Suppressed(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"wrap up","body":"finished a subtask","reason":"r"}`,
	)

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin, func(_ []string, _ time.Time) bool { return true })
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-6", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (suppressed)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "suppressed: user present") {
		t.Errorf("Reason = %q, want suppressed reason", recs[0].Reason)
	}
	// The live-tasks Stop gate sets ArmWatchdog: true (background work is
	// still running even though the user is present and got suppressed) —
	// a silent outcome here must not mean zero coverage of that live work.
	// This pipeline is DryRun, so the arm attempt is logged, not executed.
	if !strings.Contains(recs[0].Reason, "would arm watchdog") {
		t.Errorf("Reason = %q, want it to mention would arm watchdog", recs[0].Reason)
	}
}

// TestPipeline_Stop_LiveTasks_DecideNotifyTrue_NotPresent_Delivers exercises
// the decide-mode delivery branch that TestPipeline_Stop_LiveTasks_
// DecideNotifyTrue_UserPresent_Suppressed does not reach: verdict.Notify
// true, urgency not blocked, but the user is NOT present, so the focus gate
// never fires and the pipeline actually delivers (here: the DryRun line for
// what the skill brief calls the parked-dev-server ping).
func TestPipeline_Stop_LiveTasks_DecideNotifyTrue_NotPresent_Delivers(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"dev server ready","body":"parked and listening on :3000","reason":"r"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin, neverPresent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-8", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [done]") {
		t.Errorf("stdout = %q, want DRY RUN done line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "dev server ready") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

// TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Blocked_PresentStillDelivers
// exercises the other half of the focus gate: verdict.Urgency == blocked
// bypasses the "user present" suppression entirely, even with the user
// sitting right at the pane, since a blocked session needs the user
// regardless of which pane currently has focus.
func TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Blocked_PresentStillDelivers(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"blocked","task":"needs a decision","body":"which approach?","reason":"r"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin, func(_ []string, _ time.Time) bool { return true })
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-9", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [blocked]") {
		t.Errorf("stdout = %q, want DRY RUN blocked line despite user present", stdout.String())
	}
	if !strings.Contains(stdout.String(), "needs a decision") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

// TestPipeline_Stop_Teammates_DecideNotifyFalse_SilentAndArms exercises the
// teammates Stop gate end to end: a transcript with a teammate spawn and a
// recent teammate reply (empty queue, no goal, no live tasks) reaches
// OutcomeSilent through the decide-mode judge (never compose) and arms the
// watchdog, and the digest handed to the judge carries the TEAMMATES section
// the rubric's teammates guidance depends on.
func TestPipeline_Stop_Teammates_DecideNotifyFalse_SilentAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	const judgeReason = "teammate still working, no reply yet"
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	const parentPIDSentinel = 424242
	p.ParentPID = parentPIDSentinel
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "teammates.jsonl")
	wantOffset := scannedBytes(t, transcript)

	sessionID := "sess-teammates"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no DRY RUN line)", stdout.String())
	}

	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Fatalf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}
	// Pins the rest of the arm request's forwarding: a broken ParentPID
	// plumbing path (pipeline.go's arm literal, or the daemon's per-frame
	// copy) would be invisible to a SessionID-only assertion, and
	// parentAlive treats a zero ParentPID as "no probe" — so a silently
	// dropped ParentPID would never surface as a dead-session exit either.
	got := wd.armed[0]
	if got.ParentPID != parentPIDSentinel {
		t.Errorf("armed[0].ParentPID = %d, want %d", got.ParentPID, parentPIDSentinel)
	}
	if got.Transcript != transcript {
		t.Errorf("armed[0].Transcript = %q, want %q", got.Transcript, transcript)
	}
	if got.Offset != wantOffset {
		t.Errorf("armed[0].Offset = %d, want %d", got.Offset, wantOffset)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want the judge's verdict reason", recs[0].Reason)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "TEAMMATES") {
		t.Errorf("stdin did not carry the TEAMMATES digest section: %q", dump)
	}
}

// TestPipeline_Stop_GoalActive_NilWatchdog_NoOpNoPanic proves arm() is a
// safe no-op when Pipeline.Watchdog is unset (the hook client's inline
// fallback shape): a goal-active Stop must still resolve silent and log
// normally, with no "arm failed" record — that outcome no longer exists now
// that arming is an interface call, not a subprocess spawn that can fail.
func TestPipeline_Stop_GoalActive_NilWatchdog_NoOpNoPanic(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	p.DryRun = false
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-nil-watchdog", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "goal active") {
		t.Errorf("Reason = %q, want it to mention the goal-active defer", recs[0].Reason)
	}
}

func TestPipeline_IdlePrompt_DedupeWindow_Silent(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	ds := newMemDedupeState()
	p.State = ds

	sessionID := "sess-7"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-30*time.Second), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: filepath.Join("testdata", "goal_none.jsonl"),
		HookEventName: "Notification", NotificationType: "idle_prompt",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (dedupe silence)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "dedupe") {
		t.Errorf("Reason = %q, want dedupe reason", recs[0].Reason)
	}
}

// TestPipeline_PermissionPrompt_IdenticalRepeat_SecondIsSilent is the
// end-to-end version of the blockedRepeatWindow gate: two identical
// permission_prompt events for the same session, roughly a minute apart,
// must produce exactly one send followed by one silent (dedupe) record —
// the real-world storm being fixed is "Claude needs your permission" firing
// 3x in 9 minutes with identical text.
func TestPipeline_PermissionPrompt_IdenticalRepeat_SecondIsSilent(t *testing.T) {
	stubBin := writeStubClaude(t)
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	ds := newMemDedupeState()
	p.State = ds

	sessionID := "sess-perm-repeat"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt",
		Message: "Claude needs permission to run rm",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification on first ping", sent)
	}

	// Backdate the recorded send to simulate the second identical ping
	// arriving roughly a minute later — well inside blockedRepeatWindow.
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-1*time.Minute), in.Message); err != nil {
		t.Fatalf("backdating MarkNotified: %v", err)
	}

	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Errorf("sent = %+v, want still exactly one notification (second identical ping suppressed)", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 2 {
		t.Fatalf("log records = %d, want 2 (one send, one silent)", len(recs))
	}
	if recs[0].Outcome != OutcomeSend.String() {
		t.Errorf("first record Outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() {
		t.Errorf("second record Outcome = %q, want silent", recs[1].Outcome)
	}
	if !strings.Contains(recs[1].Reason, "dedupe: identical ping") {
		t.Errorf("second record Reason = %q, want identical-ping dedupe reason", recs[1].Reason)
	}
}

// TestPipeline_Regression_ComposePath_NonDryRun_StdoutEmpty pins that a
// non-goal outcome (the existing compose path) writes NOTHING to stdout,
// even outside DryRun — this task is the only one permitted to add stdout
// writes to the pipeline, and this test guards against a future change
// accidentally leaking the new block-JSON write (or anything else) onto an
// unrelated Stop's stdout.
func TestPipeline_Regression_ComposePath_NonDryRun_StdoutEmpty(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"ship it","body":"all tests green","reason":"turn ended cleanly"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-regression-1", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (non-goal outcome must never write to stdout)", stdout.String())
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification (compose path still delivers normally)", sent)
	}
}

// TestScanTranscript_GoalIncidentDaemon_LiveBackgroundBashTask is the direct
// ScanTranscript-level assertion for the grailquest-incident fixture: a goal
// armed via a sentinel goal_status record, with exactly one live task and no
// non-sentinel goal_status verdict anywhere in the transcript, registered as
// a background Bash launch (not an Agent) — the shape that Claude Code's
// built-in /goal evaluator defers on, which is what stalled the goal in the
// real incident this fixture reproduces.
func TestScanTranscript_GoalIncidentDaemon_LiveBackgroundBashTask(t *testing.T) {
	f := openFixture(t, "goal_incident_daemon.jsonl")
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}

	if res.Goal.Status != GoalActive {
		t.Errorf("Status = %v, want GoalActive", res.Goal.Status)
	}
	if res.Goal.Condition != goalIncidentDaemonCondition {
		t.Errorf("Condition = %q, want %q", res.Goal.Condition, goalIncidentDaemonCondition)
	}

	if len(res.LiveTasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1 (the parked daemon): %+v", len(res.LiveTasks), res.LiveTasks)
	}
	task := res.LiveTasks[0]
	if task.Kind != TaskBash {
		t.Errorf("Kind = %v, want TaskBash (a background daemon, not an Agent)", task.Kind)
	}
	if task.ID != "bgtaskid001" {
		t.Errorf("ID = %q, want bgtaskid001", task.ID)
	}
	wantDetail := "exec /tmp/grailquestd-play --addr 127.0.0.1:8080 --content content"
	if task.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", task.Detail, wantDetail)
	}
}

// TestPipeline_Stop_ComposeJudgeError_SuppressedWithinFailOpenWindow exercises
// the compose-mode fail-open rate limit: a judge error within failOpenWindow
// of the session's last notification must be suppressed (silent, watchdog
// armed) rather than sent again, logged as the distinct "judge error"
// outcome so it stays distinguishable from a genuine silent verdict.
func TestPipeline_Stop_ComposeJudgeError_SuppressedWithinFailOpenWindow(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin, neverPresent)
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "goal_none.jsonl")

	sessionID := "sess-compose-suppressed"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-2*time.Minute), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "All done here.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (suppressed judge error must not send)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "suppressed") {
		t.Errorf("Reason = %q, want it to mention suppressed", recs[0].Reason)
	}
	if !strings.Contains(recs[0].Reason, "would arm watchdog") {
		t.Errorf("Reason = %q, want it to mention would arm watchdog (DryRun)", recs[0].Reason)
	}
}

// TestPipeline_Stop_DecideJudgeError_NeverNotified_SendsFallback pins the
// baseline decide-mode judge-error fallback (never notified before, so
// SinceLastNotify is negative): it must send exactly as before the fail-open
// rate limit existed — the epic's anti-pattern guard is that the FIRST judge
// error may never fail to notify.
func TestPipeline_Stop_DecideJudgeError_NeverNotified_SendsFallback(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-decide-never-notified", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "background build still going",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one fallback notification", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record", recs)
	}
}

// TestPipeline_Stop_DecideJudgeError_SuppressedWithinFailOpenWindow_NoSend
// exercises the decide-mode fail-open rate limit: within failOpenWindow of
// the last notification, a judge error must resolve silent (watchdog armed,
// last-notify untouched) rather than sending a repeat fallback ping.
func TestPipeline_Stop_DecideJudgeError_SuppressedWithinFailOpenWindow_NoSend(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-suppressed"
	notifiedAt := time.Now().Add(-3 * time.Minute)
	if err := ds.MarkNotified(context.Background(), sessionID, notifiedAt, "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification (suppressed within failOpenWindow)", sent)
	}
	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Errorf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}
	if got := ds.SinceLastNotify(context.Background(), sessionID, time.Now()); got < 2*time.Minute {
		t.Errorf("SinceLastNotify() = %v, want last-notify unchanged (suppressed path must not send)", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "suppressed") {
		t.Errorf("Reason = %q, want it to mention suppressed", recs[0].Reason)
	}
}

// TestPipeline_Stop_DecideJudgeError_AfterFailOpenWindow_SendsAgain pins the
// window's far edge: once SinceLastNotify has cleared failOpenWindow, a
// judge error sends exactly as it would have before the rate limit existed.
func TestPipeline_Stop_DecideJudgeError_AfterFailOpenWindow_SendsAgain(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-after-window"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-11*time.Minute), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one fallback notification (window elapsed)", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record", recs)
	}
}

// TestPipeline_Stop_ComposeVerdict_RetriedWithoutModel_AppendsReasonSuffix
// spot-checks the compose-mode path: a verdict whose RetriedWithoutModel is
// true must have its decision-log Reason carry the retry annotation, so an
// operator reading the log can see when the judge's no-model retry path ran.
func TestPipeline_Stop_ComposeVerdict_RetriedWithoutModel_AppendsReasonSuffix(t *testing.T) {
	stubBin := writeRetryStubClaude(t)
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_then_ok")

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin, neverPresent)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-retry-compose", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if !strings.Contains(recs[0].Reason, "judge retried without --model") {
		t.Errorf("Reason = %q, want it to mention the retry", recs[0].Reason)
	}
}

func TestPipeline_SendTitles_LocusByWorkspaceAndBroadcastByHost(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	p.Workspace = "mercury"

	// A same-session event (permission prompt) locates by workspace.
	in := HookInput{
		SessionID: "sess-loc-1", CWD: "/home/user/grailquest", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "grailquest · mercury") {
		t.Errorf("stdout = %q, want permission title located by workspace", stdout.String())
	}

	// A broadcast is about a headless job, so the receiving session's
	// workspace would mislead: it locates by host instead.
	stdout.Reset()
	in = HookInput{
		SessionID: "sess-loc-2", CWD: "/home/user/grailquest", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "agent_needs_input", Message: "remote job needs your input",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "grailquest · testhost") {
		t.Errorf("stdout = %q, want broadcast title located by host", stdout.String())
	}
}

// TestPipeline_Stop_GoalActive_LiveTasks_DefersSilentNoBlock is the keystone
// regression for the goal-defer epic: an active goal with one parked live
// task (the grailquest-incident daemon shape) must resolve silent and arm
// the watchdog, deferring goal continuation entirely to Claude Code's
// built-in /goal — it must NEVER emit {"decision":"block"}, even when a
// stubbed judge answer is shaped like the old goal-judgment stack's
// "parked, unmet" verdict (which used to trigger exactly that block).
func TestPipeline_Stop_GoalActive_LiveTasks_DefersSilentNoBlock(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"still needs the restart script"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "goal_incident_daemon.jsonl")

	sessionID := "sess-defer-keystone"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Fatalf("stdout = %q, must never contain a block decision", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification (silent defer)", sent)
	}

	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Errorf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "goal active") {
		t.Errorf("Reason = %q, want it to mention the goal-active defer", recs[0].Reason)
	}
}

// TestPackageSource_NeverProducesBlockDecision is the structural invariant
// backing the epic's core requirement: notify must never emit
// {"decision":"block"}. It reads every non-test .go file in this package and
// fails if either the raw JSON literal or the equivalent Go struct-literal
// form (Decision: "block") appears anywhere — this test file is the sole
// permitted exception, since it must name the literal to check for it.
func TestPackageSource_NeverProducesBlockDecision(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}
	const rawJSON = `"decision":"block"`
	const structLiteral = `Decision: "block"`
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		src := string(data)
		if strings.Contains(src, rawJSON) {
			t.Errorf("%s contains the raw block-decision JSON literal %q", name, rawJSON)
		}
		if strings.Contains(src, structLiteral) {
			t.Errorf("%s contains a block-decision struct literal %q", name, structLiteral)
		}
	}
}

// TestPipeline_TwoJudgedStops_SecondLosesClaimAndArms reproduces the
// double-ping the ClaimSend gate exists to kill (two Stop events seconds
// apart, both judged notify=true — observed 2026-07-08 as two "Awaiting …
// decision" pings 10s apart): the first Run's send claims the session, so
// the second Run's identical verdict must resolve silent with a post-judge
// dedupe reason — and still arm the watchdog, since the live work behind the
// suppressed ping is uncovered otherwise.
func TestPipeline_TwoJudgedStops_SecondLosesClaimAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"blocked","task":"needs a decision","body":"which approach?","reason":"r"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	p.State = newMemDedupeState()
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-claim-race"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	for i := 1; i <= 2; i++ {
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() #%d error = %v", i, err)
		}
	}

	if len(sent) != 1 {
		t.Fatalf("sent %d notifications, want exactly 1 (second Stop must lose the claim)", len(sent))
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2", recs)
	}
	if recs[0].Outcome != OutcomeSend.String() {
		t.Errorf("first outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() || !strings.Contains(recs[1].Reason, "post-judge") {
		t.Errorf("second record = %+v, want silent with a post-judge dedupe reason", recs[1])
	}
	// The suppressed decide-mode send must keep watchdog coverage on the
	// live tasks, exactly as a pre-judge silent outcome would.
	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Fatalf("armed = %+v, want exactly one arm for %s (from the suppressed second Stop)", wd.armed, sessionID)
	}
}

// TestPipeline_JudgedSend_BodyCarriesLocator pins the where-did-this-come-from
// trailer on judged sends: their title carries the judge's task label, so the
// body must end with the tmux locator and host.
func TestPipeline_JudgedSend_BodyCarriesLocator(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"dev server ready","body":"parked on :3000","reason":"r"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, stubBin, neverPresent)
	p.Sender = stubSenderRecording(&sent)
	p.Workspace = "earth:3"
	p.Host = "vermissian"
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-locator", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(sent))
	}
	if want := "parked on :3000\n\n— earth:3 @ vermissian"; sent[0].Body != want {
		t.Errorf("Body = %q, want %q", sent[0].Body, want)
	}
}

// TestLocatorSuffix covers the trailer's fallback shapes: both segments,
// workspace only, host only, neither.
func TestLocatorSuffix(t *testing.T) {
	tests := []struct {
		name, workspace, host, want string
	}{
		{name: "workspace and host", workspace: "earth:3", host: "vermissian", want: "\n\n— earth:3 @ vermissian"},
		{name: "workspace only", workspace: "earth:3", want: "\n\n— earth:3"},
		{name: "host only", host: "vermissian", want: "\n\n— vermissian"},
		{name: "neither", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := locatorSuffix(tt.workspace, tt.host); got != tt.want {
				t.Errorf("locatorSuffix(%q, %q) = %q, want %q", tt.workspace, tt.host, got, tt.want)
			}
		})
	}
}

// TestPipeline_Arm_ForwardsWorkspace pins the Workspace plumbing into
// WatchdogArmRequest: the watchdog fires long after the arming hook, so a
// dropped Workspace would silently produce locator-less watchdog pings.
func TestPipeline_Arm_ForwardsWorkspace(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)
	p.DryRun = false
	p.Workspace = "earth:3"
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-arm-ws", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.armed) != 1 || wd.armed[0].Workspace != "earth:3" {
		t.Fatalf("armed = %+v, want one arm carrying Workspace earth:3", wd.armed)
	}
}
