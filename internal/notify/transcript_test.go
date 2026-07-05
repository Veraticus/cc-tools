package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("opening fixture %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = f.Close()
	})
	return f
}

func TestScanTranscriptGoalState(t *testing.T) {
	cases := []struct {
		fixture       string
		wantStatus    GoalStatus
		wantCondition string
		wantIters     int
	}{
		{
			fixture:       "goal_none.jsonl",
			wantStatus:    GoalNone,
			wantCondition: "",
			wantIters:     0,
		},
		{
			fixture:       "goal_active_set.jsonl",
			wantStatus:    GoalActive,
			wantCondition: "Continue using gambit:executing-plans until we run into a decision point you need my input on. Do not pose false choices; take the next task and iterate on it unless there is a meaningful blockage.",
			wantIters:     0,
		},
		{
			fixture:       "goal_active_iterating.jsonl",
			wantStatus:    GoalActive,
			wantCondition: "Continue using gambit:executing-plans until we run into a decision point you need my input on. Do not pose false choices; take the next task and iterate on it unless there is a meaningful blockage.",
			wantIters:     2,
		},
		{
			fixture:       "goal_met.jsonl",
			wantStatus:    GoalMet,
			wantCondition: "Continue using gambit:executing-plans until we run into a decision point you need my input on. Do not pose false choices; take the next task and iterate on it unless there is a meaningful blockage.",
			wantIters:     3,
		},
		{
			fixture:       "goal_cleared.jsonl",
			wantStatus:    GoalCleared,
			wantCondition: "Continue executing plan until ALL components of the epic are complete. Do not stop until ALL components of the epic are complete robustly and completely.",
			wantIters:     0,
		},
		{
			fixture:       "goal_failed.jsonl",
			wantStatus:    GoalFailed,
			wantCondition: "Continue executing plan until ALL components of the epic are complete. Do not stop until ALL components of the epic are complete robustly and completely.",
			wantIters:     0,
		},
	}

	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			f := openFixture(t, c.fixture)
			res, err := ScanTranscript(f)
			if err != nil {
				t.Fatalf("ScanTranscript(%s) returned error: %v", c.fixture, err)
			}
			if res.Goal.Status != c.wantStatus {
				t.Errorf("Status = %v, want %v", res.Goal.Status, c.wantStatus)
			}
			if res.Goal.Condition != c.wantCondition {
				t.Errorf("Condition = %q, want %q", res.Goal.Condition, c.wantCondition)
			}
			if res.Goal.Iterations != c.wantIters {
				t.Errorf("Iterations = %d, want %d", res.Goal.Iterations, c.wantIters)
			}
			if len(res.LiveTasks) != 0 {
				t.Errorf("tasks = %v, want none", res.LiveTasks)
			}
		})
	}
}

// assertLiveTask checks every field of a LiveTask against its expected
// value in one call, keeping per-field branching out of the calling test
// function (which otherwise trips cyclop's complexity ceiling once several
// full-field assertions accumulate across subtests).
func assertLiveTask(
	t *testing.T,
	task LiveTask,
	wantID string,
	wantKind TaskKind,
	wantDescription string,
	wantDetail string,
	wantLaunchedAt time.Time,
	wantOutputFile string,
) {
	t.Helper()
	if task.ID != wantID {
		t.Errorf("ID = %q, want %q", task.ID, wantID)
	}
	if task.Kind != wantKind {
		t.Errorf("Kind = %v, want %v", task.Kind, wantKind)
	}
	if task.Description != wantDescription {
		t.Errorf("Description = %q, want %q", task.Description, wantDescription)
	}
	if task.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", task.Detail, wantDetail)
	}
	if !task.LaunchedAt.Equal(wantLaunchedAt) {
		t.Errorf("LaunchedAt = %v, want %v", task.LaunchedAt, wantLaunchedAt)
	}
	if task.OutputFile != wantOutputFile {
		t.Errorf("OutputFile = %q, want %q", task.OutputFile, wantOutputFile)
	}
}

func parseTestTimestamp(t *testing.T, ts string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("parsing test timestamp %q: %v", ts, err)
	}
	return parsed
}

func TestScanTranscriptLiveTasks(t *testing.T) {
	wantLaunchedAt := func(ts string) time.Time {
		return parseTestTimestamp(t, ts)
	}

	t.Run("tasks_live.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_live.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if res.Goal.Status != GoalNone {
			t.Errorf("Status = %v, want GoalNone", res.Goal.Status)
		}
		if len(res.LiveTasks) != 2 {
			t.Fatalf("len(tasks) = %d, want 2: %+v", len(res.LiveTasks), res.LiveTasks)
		}

		wantPrompt := "Investigate the current notification hook setup and summarize how Stop and Notification hooks interact with subagents, citing official docs."
		assertLiveTask(
			t, res.LiveTasks[0],
			"agenttestid01", TaskAgent, "Research task placeholder", wantPrompt,
			wantLaunchedAt("2026-07-05T05:25:54.323Z"),
			"/tmp/claude-1000/-home-user-project/anon-session-0001/tasks/agenttestid01.output",
		)

		assertLiveTask(
			t, res.LiveTasks[1],
			"bgtaskid001", TaskBash, "Run full build in background", "long-running-build.sh --target all --verbose",
			wantLaunchedAt("2026-07-05T05:47:58.730Z"),
			"/tmp/claude-1000/-home-user-project/anon-session-0099/tasks/bgtaskid001.output",
		)
	})

	t.Run("tasks_completed.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_completed.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 0 {
			t.Errorf("tasks = %+v, want none (both launches completed)", res.LiveTasks)
		}
	})

	t.Run("tasks_mixed.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_mixed.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 1 {
			t.Fatalf(
				"len(tasks) = %d, want 1 (bash still live, agent completed): %+v",
				len(res.LiveTasks), res.LiveTasks,
			)
		}
		if res.LiveTasks[0].ID != "bgtaskid001" {
			t.Errorf("live task ID = %q, want bgtaskid001", res.LiveTasks[0].ID)
		}
		if res.LiveTasks[0].Kind != TaskBash {
			t.Errorf("live task Kind = %v, want TaskBash", res.LiveTasks[0].Kind)
		}
		wantOutputFile := "/tmp/claude-1000/-home-user-project/anon-session-0099/tasks/bgtaskid001.output"
		if res.LiveTasks[0].OutputFile != wantOutputFile {
			t.Errorf("live task OutputFile = %q, want %q", res.LiveTasks[0].OutputFile, wantOutputFile)
		}
	})

	// tasks_prose_mention.jsonl is a real false-positive case: a plain
	// (non-background) Bash tool_use whose own grep output happens to quote
	// both the "Command running in background with ID:" and "agentId:"
	// patterns literally. Without structural pairing to a backgrounded
	// tool_use, neither pattern should register a launch.
	t.Run("tasks_prose_mention.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_prose_mention.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 0 {
			t.Errorf("tasks = %+v, want none (prose mention of launch patterns is not a launch)", res.LiveTasks)
		}
	})

	// tasks_sync_agent.jsonl is a synchronous Agent call (run_in_background:
	// false) whose final report embeds "agentId: ..." as part of the
	// SendMessage-to-continue text. The task already finished inline; it
	// must not register as a live agent task.
	t.Run("tasks_sync_agent.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_sync_agent.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 0 {
			t.Errorf("tasks = %+v, want none (synchronous agent report is not a live launch)", res.LiveTasks)
		}
	})

	// tasks_sync_agent_no_flag.jsonl is a synchronous Agent call with NO
	// run_in_background key at all (absent, not false) — real halmasuit
	// sessions dispatch review/research agents this way. Its tool_result is
	// a short final report ending in the SendMessage-to-continue text, with
	// no "Async agent launched successfully" acknowledgment anywhere. Absent
	// run_in_background resolves to true (Agent's own default), so kind
	// pairing and run_in_background alone would misclassify this as a live
	// launch; only the missing async marker correctly excludes it.
	t.Run("tasks_sync_agent_no_flag.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_sync_agent_no_flag.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 0 {
			t.Errorf(
				"tasks = %+v, want none (synchronous agent report with run_in_background absent is not a live launch)",
				res.LiveTasks,
			)
		}
	})

	// tasks_readd_after_completion.jsonl exercises a real background launch
	// that completes via a queue-operation task-notification, followed by a
	// later prose mention (an unrelated Bash tool_use's own grep output)
	// that quotes the same ID's launch pattern again. The ID must not come
	// back to life, and specifically must not be emitted twice.
	t.Run("tasks_readd_after_completion.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_readd_after_completion.jsonl")
		res, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(res.LiveTasks) != 0 {
			t.Errorf("tasks = %+v, want none (completed task must not be re-added or duplicated)", res.LiveTasks)
		}
	})
}

// TestScanTranscriptDuplicateCompletionNoDoubleSubtract exercises the case
// spelled out in the notify package brief: a single task-notification event
// materializes as two separate transcript lines (a queue-operation record
// and an attachment record) that both carry the same <task-id>. Removing a
// completed task twice must not panic or otherwise misbehave, and the task
// must end up absent exactly once.
func TestScanTranscriptDuplicateCompletionNoDoubleSubtract(t *testing.T) {
	f := openFixture(t, "tasks_completed.jsonl")
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}
	if len(res.LiveTasks) != 0 {
		t.Fatalf("tasks = %+v, want none", res.LiveTasks)
	}
}

func TestScanTranscriptMalformedLinesSkipped(t *testing.T) {
	f := openFixture(t, "malformed.jsonl")
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}
	if res.Goal.Status != GoalActive {
		t.Errorf("Status = %v, want GoalActive (from the one valid line)", res.Goal.Status)
	}
	if res.Goal.Condition != "Valid record after garbage lines." {
		t.Errorf("Condition = %q, want %q", res.Goal.Condition, "Valid record after garbage lines.")
	}
	if len(res.LiveTasks) != 0 {
		t.Errorf("tasks = %+v, want none", res.LiveTasks)
	}
}

func TestScanTranscriptEmptyInput(t *testing.T) {
	res, err := ScanTranscript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ScanTranscript(empty) returned error: %v", err)
	}
	if res.Goal.Status != GoalNone {
		t.Errorf("Status = %v, want GoalNone", res.Goal.Status)
	}
	if res.Goal.Condition != "" {
		t.Errorf("Condition = %q, want empty", res.Goal.Condition)
	}
	if res.Goal.Iterations != 0 {
		t.Errorf("Iterations = %d, want 0", res.Goal.Iterations)
	}
	if res.LiveTasks != nil {
		t.Errorf("tasks = %+v, want nil", res.LiveTasks)
	}
	if res.LastUserMessage != "" {
		t.Errorf("LastUserMessage = %q, want empty", res.LastUserMessage)
	}
	if res.LastSubstantiveUser != "" {
		t.Errorf("LastSubstantiveUser = %q, want empty", res.LastSubstantiveUser)
	}
	if res.LastAssistantText != "" {
		t.Errorf("LastAssistantText = %q, want empty", res.LastAssistantText)
	}
	if !res.FirstTimestamp.IsZero() {
		t.Errorf("FirstTimestamp = %v, want zero", res.FirstTimestamp)
	}
	if !res.LastTimestamp.IsZero() {
		t.Errorf("LastTimestamp = %v, want zero", res.LastTimestamp)
	}
	if res.UserTurns != 0 {
		t.Errorf("UserTurns = %d, want 0", res.UserTurns)
	}
	if res.BytesScanned != 0 {
		t.Errorf("BytesScanned = %d, want 0", res.BytesScanned)
	}
}

// TestScanTranscriptConversationCapture exercises the conversation-capture
// fields (LastUserMessage, LastSubstantiveUser, LastAssistantText,
// FirstTimestamp, LastTimestamp, UserTurns, BytesScanned) against
// testdata/conversation.jsonl, a fixture that interleaves a goal_status
// attachment and a live Bash background-task launch/ack pair (both from the
// existing structural-gating vocabulary) with conversation records:
//
//  1. a typed user string message >= 40 chars
//  2. a short ack ("yes")
//  3. a Bash background launch + its tool_result ack (registers a live task)
//  4. a user message whose real typed text is followed by an appended
//     <system-reminder>...</system-reminder> block (the typed part must win)
//  5. a reminder-ONLY user record (must not count at all)
//  6. an isMeta:true user record carrying real-looking prose (must not count)
//  7. two assistant text messages, the second over 2000 chars (tail-truncated,
//     keeping the END)
func TestScanTranscriptConversationCapture(t *testing.T) {
	const fixtureName = "conversation.jsonl"
	fixturePath := filepath.Join("testdata", fixtureName)

	info, err := os.Stat(fixturePath)
	if err != nil {
		t.Fatalf("stat fixture %s: %v", fixtureName, err)
	}

	f := openFixture(t, fixtureName)
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript(%s) returned error: %v", fixtureName, err)
	}

	// Goal: composes with existing scanning.
	if res.Goal.Status != GoalActive {
		t.Errorf("Goal.Status = %v, want GoalActive", res.Goal.Status)
	}
	wantCondition := "Continue restructuring the transcript scanner until ScanResult lands with conversation capture and tests are green."
	if res.Goal.Condition != wantCondition {
		t.Errorf("Goal.Condition = %q, want %q", res.Goal.Condition, wantCondition)
	}

	// LiveTasks: the Bash background launch/ack pair.
	if len(res.LiveTasks) != 1 {
		t.Fatalf("len(LiveTasks) = %d, want 1: %+v", len(res.LiveTasks), res.LiveTasks)
	}
	assertLiveTask(
		t, res.LiveTasks[0],
		"convtaskid001", TaskBash,
		"Run conversation capture tests in background",
		"go test ./internal/notify/... -run Conversation -v",
		parseTestTimestamp(t, "2026-07-05T06:00:20.000Z"),
		"/tmp/claude-1000/-home-user-project/anon-session-0200/tasks/convtaskid001.output",
	)

	// LastUserMessage / LastSubstantiveUser: the reminder-appended message's
	// typed text wins over both the later reminder-only and isMeta records.
	wantLastUser := "Go ahead and add the byte offset tracking too, we need it for the watchdog."
	if res.LastUserMessage != wantLastUser {
		t.Errorf("LastUserMessage = %q, want %q", res.LastUserMessage, wantLastUser)
	}
	if res.LastSubstantiveUser != wantLastUser {
		t.Errorf("LastSubstantiveUser = %q, want %q", res.LastSubstantiveUser, wantLastUser)
	}

	// UserTurns: the >=40-char typed message, the short "yes" ack, and the
	// reminder-appended message each count once; the reminder-only and
	// isMeta records do not.
	if res.UserTurns != 3 {
		t.Errorf("UserTurns = %d, want 3", res.UserTurns)
	}

	// LastAssistantText: the second (long) assistant message, tail-truncated
	// to exactly 2000 bytes, with the END surviving.
	if len(res.LastAssistantText) != 2000 {
		t.Errorf("len(LastAssistantText) = %d, want 2000", len(res.LastAssistantText))
	}
	wantSuffix := "This is the FINAL-MARKER-END that must survive tail truncation."
	if !strings.HasSuffix(res.LastAssistantText, wantSuffix) {
		t.Errorf("LastAssistantText does not end with %q; got tail %q", wantSuffix, lastN(res.LastAssistantText, 80))
	}
	if strings.Contains(res.LastAssistantText, "On it") {
		t.Errorf(
			"LastAssistantText contains the first (superseded) assistant message: %q",
			lastN(res.LastAssistantText, 80),
		)
	}

	// Timestamps: first and last top-level timestamps across all record
	// types (the goal attachment is first, the final assistant message is
	// last).
	wantFirst := parseTestTimestamp(t, "2026-07-05T06:00:00.000Z")
	wantLast := parseTestTimestamp(t, "2026-07-05T06:10:00.000Z")
	if !res.FirstTimestamp.Equal(wantFirst) {
		t.Errorf("FirstTimestamp = %v, want %v", res.FirstTimestamp, wantFirst)
	}
	if !res.LastTimestamp.Equal(wantLast) {
		t.Errorf("LastTimestamp = %v, want %v", res.LastTimestamp, wantLast)
	}

	// BytesScanned: exact against the fixture's true byte size (computed via
	// os.Stat, never hardcoded).
	if res.BytesScanned != info.Size() {
		t.Errorf("BytesScanned = %d, want %d (fixture size)", res.BytesScanned, info.Size())
	}
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
