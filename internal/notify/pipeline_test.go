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
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	p := Pipeline{
		StateBase: stateBase,
		DryRun:    true,
		Judge:     Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:       DecisionLog{Path: logPath},
		Stdout:    stdout,
		Host:      "testhost",
		Present:   present,
	}
	return p, logPath
}

func neverPresent(_ []string, _ time.Time) bool { return false }

// goalJudgeTestCondition is the exact goal condition text baked into the
// goal_active_live_tasks.jsonl fixture's goal_status attachment: tests key
// state.GoalBlockCount lookups off this literal string, the same way the
// pipeline itself keys off res.Goal.Condition.
const goalJudgeTestCondition = "Continue using gambit:executing-plans until we run into a decision point " +
	"you need my input on. Do not pose false choices; take the next task and iterate on it unless there is " +
	"a meaningful blockage."

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

// newGoalTestPipeline builds a Pipeline pointed at a fresh temp state base,
// with the given dryRun setting (unlike newTestPipeline, which fixes
// DryRun:true) — goal-judge disposition tests need to exercise the real,
// non-DryRun block-emission and notification-send paths that DryRun:true
// can never reach. SelfBin is the same stub claude binary used as the
// judge: it is a valid, quick-exiting executable, which is all SpawnRecheck
// needs to succeed and write a watchdog lock.
func newGoalTestPipeline(
	t *testing.T,
	stdout *bytes.Buffer,
	judgeBin string,
	present func([]string, time.Time) bool,
	dryRun bool,
) (Pipeline, string) {
	t.Helper()
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	p := Pipeline{
		StateBase: stateBase,
		DryRun:    dryRun,
		Judge:     Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:       DecisionLog{Path: logPath},
		Stdout:    stdout,
		SelfBin:   judgeBin,
		Present:   present,
	}
	return p, logPath
}

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

func TestPipeline_SessionEnd_ReapsExistingStateDir(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)

	sessionID := "sess-end-1"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now(), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	if _, err := os.Stat(state.Dir); err != nil {
		t.Fatalf("state dir missing before test: %v", err)
	}

	in := HookInput{SessionID: sessionID, HookEventName: "SessionEnd"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := os.Stat(state.Dir); !os.IsNotExist(err) {
		t.Errorf("state dir still exists after SessionEnd: err = %v", err)
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

func TestPipeline_Stop_GoalActive_ArmFails_LogsDecisionRecord(t *testing.T) {
	var stdout bytes.Buffer
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	p := Pipeline{
		StateBase: stateBase,
		DryRun:    false,
		Judge:     Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
		Log:       DecisionLog{Path: logPath},
		Stdout:    &stdout,
		SelfBin:   filepath.Join(stateBase, "no-such-binary"),
		Present:   neverPresent,
	}
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-arm-fail", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	found := false
	for _, rec := range recs {
		if rec.Event == "watchdog" && rec.Outcome == "arm failed" && strings.Contains(rec.Reason, "spawn recheck") {
			found = true
		}
	}
	if !found {
		t.Errorf("decision log = %+v, want a watchdog/arm failed record mentioning spawn recheck", recs)
	}
}

func TestPipeline_IdlePrompt_DedupeWindow_Silent(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)

	sessionID := "sess-7"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now().Add(-30*time.Second), "prior"); err != nil {
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
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)

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
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now().Add(-1*time.Minute), in.Message); err != nil {
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

// TestPipeline_Stop_GoalJudge_Pending_SilentAndArms exercises disposition 1:
// tasks=="pending" must be silent + arm the watchdog, reusing the existing
// arm path — and must be inert to goal_met regardless of what the judge said
// (the rubric guarantees goal_met is false whenever tasks is pending, but the
// pipeline branches on Tasks first either way).
func TestPipeline_Stop_GoalJudge_Pending_SilentAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "waiting on the research subagent to finish"
	t.Setenv("STUB_STDOUT", `{"tasks":"pending","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-pending"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", StopHookActive: true,
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no stdout write)", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}

	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err != nil {
		t.Errorf("watchdog.lock missing, want armed: %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want 0", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("Outcome = %q, want silent", recs[0].Outcome)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want it to contain the judge's reason", recs[0].Reason)
	}
	if !strings.Contains(recs[0].Reason, "stop_hook_active=true") {
		t.Errorf("Reason = %q, want it to mention stop_hook_active=true", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_ParkedUnmet_UnderCap_EmitsBlockAndIncrements
// exercises disposition 2: a golden byte-exact assertion on the Stop-hook
// block control message, since a stray or malformed write here corrupts
// every Stop on every host.
func TestPipeline_Stop_GoalJudge_ParkedUnmet_UnderCap_EmitsBlockAndIncrements(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "dev server still needs the restart script re-run"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-block-1"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantLine, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{Decision: "block", Reason: judgeReason + " (goal: " + truncateWords(goalJudgeTestCondition, maxBlockGoalEchoLen) + ")"})
	if err != nil {
		t.Fatalf("marshaling expected line: %v", err)
	}
	wantStdout := string(wantLine) + "\n"
	if stdout.String() != wantStdout {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantStdout)
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}

	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if _, statErr := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); !os.IsNotExist(statErr) {
		t.Errorf("watchdog.lock exists (err=%v), want not armed", statErr)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 1 {
		t.Errorf("GoalBlockCount() = %d, want 1", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != "block" {
		t.Fatalf("records = %+v, want one block record", recs)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want judge reason", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_ParkedMet_SendsGoalCompleteAndResets exercises
// disposition 3: goal met sends a "goal complete" notification and resets
// the block count, with no block and no watchdog arm.
func TestPipeline_Stop_GoalJudge_ParkedMet_SendsGoalCompleteAndResets(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "all plan steps finished and tests are green"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":true,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-met-1"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.SetGoalBlockCount(goalJudgeTestCondition, 3); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification", sent)
	}
	if sent[0].Title != "project · goal complete" {
		t.Errorf("Title = %q, want %q", sent[0].Title, "project · goal complete")
	}
	if sent[0].Body != judgeReason {
		t.Errorf("Body = %q, want %q", sent[0].Body, judgeReason)
	}
	if sent[0].Priority != "4" {
		t.Errorf("Priority = %q, want 4 (done)", sent[0].Priority)
	}

	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); !os.IsNotExist(err) {
		t.Errorf("watchdog.lock exists (err=%v), want not armed", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want reset to 0", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record", recs)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want judge reason", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_Error_SilentArmsAndLogsJudgeError exercises
// disposition 4: a judge error fails toward the pre-epic behavior (silent,
// arm the watchdog) and logs a distinct "judge error" outcome, resetting the
// block count like every other non-block disposition.
func TestPipeline_Stop_GoalJudge_Error_SilentArmsAndLogsJudgeError(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-err-1"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.SetGoalBlockCount(goalJudgeTestCondition, 2); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err != nil {
		t.Errorf("watchdog.lock missing, want armed: %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want reset to 0", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "goal active with live tasks: goal continuation") {
		t.Errorf("Reason = %q, want it to contain the Decide-level reason", recs[0].Reason)
	}
	if recs[0].JudgeErr == "" || !strings.Contains(recs[0].JudgeErr, "judge unavailable") {
		t.Errorf("JudgeErr = %q, want it to mention the stub's stderr", recs[0].JudgeErr)
	}
}

// TestPipeline_Stop_GoalJudge_Error_DryRun_WouldArmSuffix exercises
// disposition 4 under DryRun: a judge error must not arm the watchdog or
// write state, but the logged Reason still needs the "(would arm watchdog)"
// suffix every other silent-arm path carries under DryRun, and the Outcome
// must stay the distinct "judge error" string.
func TestPipeline_Stop_GoalJudge_Error_DryRun_WouldArmSuffix(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, true)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-err-dryrun-1"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.SetGoalBlockCount(goalJudgeTestCondition, 2); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (no block JSON)", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err == nil {
		t.Error("watchdog.lock exists, want no lock written under DryRun")
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 2 {
		t.Errorf("GoalBlockCount() = %d, want unchanged at 2 (no state written under DryRun)", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "(would arm watchdog)") {
		t.Errorf("Reason = %q, want it to contain the would-arm-watchdog suffix", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_CapBoundary pins the exact cap edge: a prior
// count of 7 still blocks (the 8th consecutive block), but a prior count of
// 8 gives up blocking and sends a "goal stalled" notification instead.
func TestPipeline_Stop_GoalJudge_CapBoundary(t *testing.T) {
	const judgeReason = "still finishing the final review pass"

	t.Run("count seven still blocks as the eighth", func(t *testing.T) {
		stubBin := writeStubClaude(t)
		t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

		var stdout bytes.Buffer
		var sent []capturedRequest
		p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
		p.Sender = stubSenderRecording(&sent)
		transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

		sessionID := "sess-cap-7"
		state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
		if err := state.SetGoalBlockCount(goalJudgeTestCondition, 7); err != nil {
			t.Fatalf("SetGoalBlockCount() error = %v", err)
		}

		in := HookInput{
			SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
		}
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if !strings.Contains(stdout.String(), `"decision":"block"`) {
			t.Errorf("stdout = %q, want the block JSON (8th consecutive block, still under cap)", stdout.String())
		}
		if len(sent) != 0 {
			t.Errorf("sent = %+v, want no notification", sent)
		}
		if got := state.GoalBlockCount(goalJudgeTestCondition); got != 8 {
			t.Errorf("GoalBlockCount() = %d, want 8", got)
		}
		recs := readDecisionLog(t, logPath)
		if len(recs) != 1 || recs[0].Outcome != "block" {
			t.Fatalf("records = %+v, want one block record", recs)
		}
	})

	t.Run("count eight hits the cap and notifies instead", func(t *testing.T) {
		stubBin := writeStubClaude(t)
		t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

		var stdout bytes.Buffer
		var sent []capturedRequest
		p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
		p.Sender = stubSenderRecording(&sent)
		transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

		sessionID := "sess-cap-8"
		state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
		if err := state.SetGoalBlockCount(goalJudgeTestCondition, 8); err != nil {
			t.Fatalf("SetGoalBlockCount() error = %v", err)
		}

		in := HookInput{
			SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
		}
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if strings.Contains(stdout.String(), `"decision":"block"`) {
			t.Errorf("stdout = %q, want no block JSON once the cap is reached", stdout.String())
		}
		if len(sent) != 1 {
			t.Fatalf("sent = %+v, want one goal-stalled notification", sent)
		}
		if sent[0].Title != "project · goal stalled" {
			t.Errorf("Title = %q, want %q", sent[0].Title, "project · goal stalled")
		}
		if sent[0].Body != judgeReason {
			t.Errorf("Body = %q, want %q", sent[0].Body, judgeReason)
		}
		if sent[0].Priority != "5" {
			t.Errorf("Priority = %q, want 5 (blocked)", sent[0].Priority)
		}
		if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
			t.Errorf("GoalBlockCount() = %d, want reset to 0 after cap-hit", got)
		}
		recs := readDecisionLog(t, logPath)
		if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
			t.Fatalf("records = %+v, want one send record", recs)
		}
	})
}

// TestPipeline_Stop_GoalJudge_TwoRuns_PersistIncrementThenReset exercises
// the state base persisting the count across separate Pipeline.Run calls
// (as separate hook invocations would see it): two consecutive
// parked-and-unmet Stops, spaced past goalBlockMinInterval so the pacing
// gate added for goal-block pacing doesn't suppress the second one, increment
// 1 then 2, and a parked-and-met Stop afterwards resets it to 0.
func TestPipeline_Stop_GoalJudge_TwoRuns_PersistIncrementThenReset(t *testing.T) {
	stubBin := writeStubClaude(t)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	state := SessionState{Dir: filepath.Join(p.StateBase, "sess-two-runs")}

	in := HookInput{
		SessionID: "sess-two-runs", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}

	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"first pass, not there yet"}`)
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() #1 error = %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 1 {
		t.Fatalf("GoalBlockCount() after run 1 = %d, want 1", got)
	}
	// Simulate the two Stops arriving goalBlockMinInterval+ apart, the way
	// separate hook invocations naturally would in production: the test
	// itself runs both calls within microseconds of each other.
	backdated := time.Now().Add(-goalBlockMinInterval - time.Second)
	if err := state.MarkGoalBlocked(goalJudgeTestCondition, backdated); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}

	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"second pass, still not there"}`)
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() #2 error = %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 2 {
		t.Fatalf("GoalBlockCount() after run 2 = %d, want 2", got)
	}

	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":true,"reason":"criteria now satisfied"}`)
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() #3 error = %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
		t.Errorf("GoalBlockCount() after parked-met run = %d, want reset to 0", got)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification (from the final parked-met run)", sent)
	}
	if sent[0].Title != "project · goal complete" {
		t.Errorf("Title = %q, want %q", sent[0].Title, "project · goal complete")
	}
}

// TestPipeline_Stop_GoalJudge_ParkedUnmet_DryRun_NoBlockJSON exercises the
// DryRun contract for the block disposition: the real Stop-hook block
// control message must NEVER be written in DryRun, but a human-readable
// "DRY RUN: ..." preview line, following the existing dry-run reporting
// pattern, must still appear.
func TestPipeline_Stop_GoalJudge_ParkedUnmet_DryRun_NoBlockJSON(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "still waiting on manual verification"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	p, _ := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, true)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-dryrun-block"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Errorf("stdout = %q, want no block JSON in DryRun", stdout.String())
	}
	if !strings.HasPrefix(stdout.String(), "DRY RUN: would block (goal continuation)") {
		t.Errorf("stdout = %q, want a DRY RUN would-block preview line", stdout.String())
	}
	if !strings.Contains(stdout.String(), judgeReason) {
		t.Errorf("stdout = %q, want it to contain the judge's reason", stdout.String())
	}

	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want 0 (DryRun must not persist state)", got)
	}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); !os.IsNotExist(err) {
		t.Errorf("watchdog.lock exists (err=%v), want not armed", err)
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
	p, _ := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
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

// TestPipeline_Stop_GoalJudge_IncidentDaemon_ParkedUnmet_EmitsBlock is the
// e2e half of the grailquest-incident reproduction: ScanTranscript -> Decide
// -> Pipeline over the real daemon-shaped fixture, asserting the exact
// Stop-hook block control message a stub judge's "parked, unmet" verdict
// must produce, byte for byte, exactly as
// TestPipeline_Stop_GoalJudge_ParkedUnmet_UnderCap_EmitsBlockAndIncrements
// does for the Agent-task fixture.
func TestPipeline_Stop_GoalJudge_IncidentDaemon_ParkedUnmet_EmitsBlock(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "the daemon is still starting up; health check hasn't passed yet"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_incident_daemon.jsonl")

	sessionID := "sess-incident-daemon-block"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantLine, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{Decision: "block", Reason: judgeReason + " (goal: " + truncateWords(goalIncidentDaemonCondition, maxBlockGoalEchoLen) + ")"})
	if err != nil {
		t.Fatalf("marshaling expected line: %v", err)
	}
	wantStdout := string(wantLine) + "\n"
	if stdout.String() != wantStdout {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantStdout)
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}

	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if _, statErr := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); !os.IsNotExist(statErr) {
		t.Errorf("watchdog.lock exists (err=%v), want not armed", statErr)
	}
	if got := state.GoalBlockCount(goalIncidentDaemonCondition); got != 1 {
		t.Errorf("GoalBlockCount() = %d, want 1", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != "block" {
		t.Fatalf("records = %+v, want one block record", recs)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want judge reason", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_IncidentDaemon_Pending_SilentAndArms is the
// pending-disposition variant over the same daemon fixture: tasks=="pending"
// must stay silent and arm the watchdog instead of blocking, exactly as
// TestPipeline_Stop_GoalJudge_Pending_SilentAndArms does for the Agent-task
// fixture.
func TestPipeline_Stop_GoalJudge_IncidentDaemon_Pending_SilentAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "still waiting for the daemon's startup log line"
	t.Setenv("STUB_STDOUT", `{"tasks":"pending","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_incident_daemon.jsonl")

	sessionID := "sess-incident-daemon-pending"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", StopHookActive: true,
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no stdout write)", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}

	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err != nil {
		t.Errorf("watchdog.lock missing, want armed: %v", err)
	}
	if got := state.GoalBlockCount(goalIncidentDaemonCondition); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want 0", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("Outcome = %q, want silent", recs[0].Outcome)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want it to contain the judge's reason", recs[0].Reason)
	}
	if !strings.Contains(recs[0].Reason, "stop_hook_active=true") {
		t.Errorf("Reason = %q, want it to mention stop_hook_active=true", recs[0].Reason)
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
	transcript := copyFixture(t, "goal_none.jsonl")

	sessionID := "sess-compose-suppressed"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now().Add(-2*time.Minute), "prior"); err != nil {
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
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
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
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-suppressed"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	notifiedAt := time.Now().Add(-3 * time.Minute)
	if err := state.MarkNotified(notifiedAt, "prior"); err != nil {
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
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err != nil {
		t.Errorf("watchdog.lock missing, want armed: %v", err)
	}
	if got := state.SinceLastNotify(time.Now()); got < 2*time.Minute {
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
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-after-window"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now().Add(-11*time.Minute), "prior"); err != nil {
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

// TestPipeline_Stop_GoalJudge_ParkedUnmet_WithinPaceInterval_SilentNoIncrement
// exercises the goal-block pacing gate: a parked-unmet verdict within
// goalBlockMinInterval of the previous block resolves silent (watchdog
// armed) instead of blocking again, and the consecutive-block count is left
// untouched — the fix for a goal-judge block re-prodding a session every
// evaluation cycle.
func TestPipeline_Stop_GoalJudge_ParkedUnmet_WithinPaceInterval_SilentNoIncrement(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "still waiting on the restart script"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-pace-1m"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.SetGoalBlockCount(goalJudgeTestCondition, 1); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	if err := state.MarkGoalBlocked(goalJudgeTestCondition, time.Now().Add(-1*time.Minute)); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Errorf("stdout = %q, want no block JSON (paced)", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}
	if _, err := os.Stat(filepath.Join(state.Dir, "watchdog.lock")); err != nil {
		t.Errorf("watchdog.lock missing, want armed: %v", err)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 1 {
		t.Errorf("GoalBlockCount() = %d, want unchanged at 1 (paced, not incremented)", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("Outcome = %q, want silent", recs[0].Outcome)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want judge reason", recs[0].Reason)
	}
}

// TestPipeline_Stop_GoalJudge_ParkedUnmet_AfterPaceInterval_BlocksAgain pins
// the pacing gate's far edge: once goalBlockMinInterval has elapsed since the
// last block, a parked-unmet verdict blocks again and increments the count,
// exactly as it would without the pacing gate.
func TestPipeline_Stop_GoalJudge_ParkedUnmet_AfterPaceInterval_BlocksAgain(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "still waiting on the restart script"
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	sessionID := "sess-goal-pace-6m"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.SetGoalBlockCount(goalJudgeTestCondition, 1); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	if err := state.MarkGoalBlocked(goalJudgeTestCondition, time.Now().Add(-6*time.Minute)); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Errorf("stdout = %q, want the block JSON (past the pace interval)", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification", sent)
	}
	if got := state.GoalBlockCount(goalJudgeTestCondition); got != 2 {
		t.Errorf("GoalBlockCount() = %d, want 2", got)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != "block" {
		t.Fatalf("records = %+v, want one block record", recs)
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

// TestPipeline_Stop_GoalParkedMet_RetriedWithoutModel_AppendsReasonSuffix
// spot-checks a goal path: a GoalVerdict whose RetriedWithoutModel is true
// must carry the same retry annotation on the decision log's Reason.
func TestPipeline_Stop_GoalParkedMet_RetriedWithoutModel_AppendsReasonSuffix(t *testing.T) {
	stubBin := writeRetryStubClaude(t)
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_then_ok_goal")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin, neverPresent, false)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_active_live_tasks.jsonl")

	in := HookInput{
		SessionID: "sess-retry-goal", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification", sent)
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
