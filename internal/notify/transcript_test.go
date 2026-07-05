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
			goal, tasks, err := ScanTranscript(f)
			if err != nil {
				t.Fatalf("ScanTranscript(%s) returned error: %v", c.fixture, err)
			}
			if goal.Status != c.wantStatus {
				t.Errorf("Status = %v, want %v", goal.Status, c.wantStatus)
			}
			if goal.Condition != c.wantCondition {
				t.Errorf("Condition = %q, want %q", goal.Condition, c.wantCondition)
			}
			if goal.Iterations != c.wantIters {
				t.Errorf("Iterations = %d, want %d", goal.Iterations, c.wantIters)
			}
			if len(tasks) != 0 {
				t.Errorf("tasks = %v, want none", tasks)
			}
		})
	}
}

func TestScanTranscriptLiveTasks(t *testing.T) {
	wantLaunchedAt := func(ts string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t.Fatalf("parsing test timestamp %q: %v", ts, err)
		}
		return parsed
	}

	t.Run("tasks_live.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_live.jsonl")
		goal, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if goal.Status != GoalNone {
			t.Errorf("Status = %v, want GoalNone", goal.Status)
		}
		if len(tasks) != 2 {
			t.Fatalf("len(tasks) = %d, want 2: %+v", len(tasks), tasks)
		}

		agentTask := tasks[0]
		if agentTask.ID != "agenttestid01" {
			t.Errorf("agentTask.ID = %q, want agenttestid01", agentTask.ID)
		}
		if agentTask.Kind != TaskAgent {
			t.Errorf("agentTask.Kind = %v, want TaskAgent", agentTask.Kind)
		}
		if agentTask.Description != "Research task placeholder" {
			t.Errorf("agentTask.Description = %q, want %q", agentTask.Description, "Research task placeholder")
		}
		wantPrompt := "Investigate the current notification hook setup and summarize how Stop and Notification hooks interact with subagents, citing official docs."
		if agentTask.Detail != wantPrompt {
			t.Errorf("agentTask.Detail = %q, want %q", agentTask.Detail, wantPrompt)
		}
		wantAgentLaunchedAt := wantLaunchedAt("2026-07-05T05:25:54.323Z")
		if !agentTask.LaunchedAt.Equal(wantAgentLaunchedAt) {
			t.Errorf("agentTask.LaunchedAt = %v, want %v", agentTask.LaunchedAt, wantAgentLaunchedAt)
		}

		bashTask := tasks[1]
		if bashTask.ID != "bgtaskid001" {
			t.Errorf("bashTask.ID = %q, want bgtaskid001", bashTask.ID)
		}
		if bashTask.Kind != TaskBash {
			t.Errorf("bashTask.Kind = %v, want TaskBash", bashTask.Kind)
		}
		if bashTask.Description != "Run full build in background" {
			t.Errorf("bashTask.Description = %q, want %q", bashTask.Description, "Run full build in background")
		}
		if bashTask.Detail != "long-running-build.sh --target all --verbose" {
			t.Errorf("bashTask.Detail = %q, want %q", bashTask.Detail, "long-running-build.sh --target all --verbose")
		}
		wantBashLaunchedAt := wantLaunchedAt("2026-07-05T05:47:58.730Z")
		if !bashTask.LaunchedAt.Equal(wantBashLaunchedAt) {
			t.Errorf("bashTask.LaunchedAt = %v, want %v", bashTask.LaunchedAt, wantBashLaunchedAt)
		}
	})

	t.Run("tasks_completed.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_completed.jsonl")
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 0 {
			t.Errorf("tasks = %+v, want none (both launches completed)", tasks)
		}
	})

	t.Run("tasks_mixed.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_mixed.jsonl")
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("len(tasks) = %d, want 1 (bash still live, agent completed): %+v", len(tasks), tasks)
		}
		if tasks[0].ID != "bgtaskid001" {
			t.Errorf("live task ID = %q, want bgtaskid001", tasks[0].ID)
		}
		if tasks[0].Kind != TaskBash {
			t.Errorf("live task Kind = %v, want TaskBash", tasks[0].Kind)
		}
	})

	// tasks_prose_mention.jsonl is a real false-positive case: a plain
	// (non-background) Bash tool_use whose own grep output happens to quote
	// both the "Command running in background with ID:" and "agentId:"
	// patterns literally. Without structural pairing to a backgrounded
	// tool_use, neither pattern should register a launch.
	t.Run("tasks_prose_mention.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_prose_mention.jsonl")
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 0 {
			t.Errorf("tasks = %+v, want none (prose mention of launch patterns is not a launch)", tasks)
		}
	})

	// tasks_sync_agent.jsonl is a synchronous Agent call (run_in_background:
	// false) whose final report embeds "agentId: ..." as part of the
	// SendMessage-to-continue text. The task already finished inline; it
	// must not register as a live agent task.
	t.Run("tasks_sync_agent.jsonl", func(t *testing.T) {
		f := openFixture(t, "tasks_sync_agent.jsonl")
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 0 {
			t.Errorf("tasks = %+v, want none (synchronous agent report is not a live launch)", tasks)
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
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 0 {
			t.Errorf(
				"tasks = %+v, want none (synchronous agent report with run_in_background absent is not a live launch)",
				tasks,
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
		_, tasks, err := ScanTranscript(f)
		if err != nil {
			t.Fatalf("ScanTranscript returned error: %v", err)
		}
		if len(tasks) != 0 {
			t.Errorf("tasks = %+v, want none (completed task must not be re-added or duplicated)", tasks)
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
	_, tasks, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want none", tasks)
	}
}

func TestScanTranscriptMalformedLinesSkipped(t *testing.T) {
	f := openFixture(t, "malformed.jsonl")
	goal, tasks, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}
	if goal.Status != GoalActive {
		t.Errorf("Status = %v, want GoalActive (from the one valid line)", goal.Status)
	}
	if goal.Condition != "Valid record after garbage lines." {
		t.Errorf("Condition = %q, want %q", goal.Condition, "Valid record after garbage lines.")
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none", tasks)
	}
}

func TestScanTranscriptEmptyInput(t *testing.T) {
	goal, tasks, err := ScanTranscript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ScanTranscript(empty) returned error: %v", err)
	}
	if goal.Status != GoalNone {
		t.Errorf("Status = %v, want GoalNone", goal.Status)
	}
	if goal.Condition != "" {
		t.Errorf("Condition = %q, want empty", goal.Condition)
	}
	if goal.Iterations != 0 {
		t.Errorf("Iterations = %d, want 0", goal.Iterations)
	}
	if tasks != nil {
		t.Errorf("tasks = %+v, want nil", tasks)
	}
}
