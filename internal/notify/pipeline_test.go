package notify

import (
	"bytes"
	"context"
	"encoding/json"
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
		Present:   present,
	}
	return p, logPath
}

func neverPresent(_ []string, _ time.Time) bool { return false }

func TestPipeline_SessionEnd_ReapsExistingStateDir(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t), neverPresent)

	sessionID := "sess-end-1"
	state := SessionState{Dir: filepath.Join(p.StateBase, sessionID)}
	if err := state.MarkNotified(time.Now()); err != nil {
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
	if recs[0].Outcome != "silent" {
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

	if !strings.Contains(stdout.String(), "session idle") {
		t.Errorf("stdout = %q, want fallback session idle line", stdout.String())
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
	if len(recs) != 1 || recs[0].Outcome != "silent" {
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
	if len(recs) != 1 || recs[0].Outcome != "silent" {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "suppressed: user present") {
		t.Errorf("Reason = %q, want suppressed reason", recs[0].Reason)
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
	if err := state.MarkNotified(time.Now().Add(-30 * time.Second)); err != nil {
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
	if len(recs) != 1 || recs[0].Outcome != "silent" {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "dedupe") {
		t.Errorf("Reason = %q, want dedupe reason", recs[0].Reason)
	}
}
