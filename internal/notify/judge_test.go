package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubScript is a minimal /bin/sh program standing in for the claude binary
// in every test below: it always drains stdin first (a real pipe blocks the
// writer if the reader never consumes it), optionally dumps stdin+env to
// STUB_DUMP_FILE for a test to inspect, then answers per env vars the test
// sets before invoking Judge.Evaluate — STUB_SLEEP to simulate a hang past
// Timeout, STUB_STDOUT/STUB_STDERR for output, STUB_EXIT for exit status.
const stubScript = `#!/bin/sh
input=$(cat)

if [ -n "$STUB_DUMP_FILE" ]; then
  {
    echo "STDIN:"
    printf '%s' "$input"
    echo
    echo "ENV:"
    env
    echo "ARGV:"
    for a in "$@"; do printf '%s\n' "$a"; done
  } > "$STUB_DUMP_FILE"
fi

if [ -n "$STUB_SLEEP" ]; then
  sleep "$STUB_SLEEP"
fi

if [ -n "$STUB_STDERR" ]; then
  printf '%s' "$STUB_STDERR" >&2
fi

if [ -n "$STUB_STDOUT" ]; then
  printf '%s' "$STUB_STDOUT"
fi

exit "${STUB_EXIT:-0}"
`

// writeStubClaude writes stubScript to a temp dir and returns its path.
func writeStubClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("writing stub: %v", err)
	}
	return path
}

func TestEvaluate_ValidVerdictRoundTrips(t *testing.T) {
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"ship it","body":"tests pass","reason":"deliverable ready"}`,
	)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "DIGEST-MARKER", JudgeModeDecide)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	want := JudgeVerdict{
		Notify:  true,
		Urgency: UrgencyDone,
		Task:    "ship it",
		Body:    "tests pass",
		Reason:  "deliverable ready",
	}
	if got != want {
		t.Errorf("Evaluate() = %+v, want %+v", got, want)
	}
}

func TestEvaluate_ClampsLongTaskAndBody(t *testing.T) {
	longTask := strings.Repeat("a", maxTaskBytes+40)
	longBody := strings.Repeat("b", maxBodyBytes+40)
	stdout := `{"notify":true,"urgency":"info","task":"` + longTask + `","body":"` + longBody + `","reason":"r"}`
	t.Setenv("STUB_STDOUT", stdout)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeCompose)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(got.Task) != maxTaskBytes {
		t.Errorf("Task length = %d, want %d", len(got.Task), maxTaskBytes)
	}
	if len(got.Body) != maxBodyBytes {
		t.Errorf("Body length = %d, want %d", len(got.Body), maxBodyBytes)
	}
}

func TestEvaluate_StripsMarkdownFences(t *testing.T) {
	t.Setenv("STUB_STDOUT", "```json\n"+`{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`+"\n```")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !got.Notify || got.Urgency != UrgencyInfo || got.Task != "t" {
		t.Errorf("Evaluate() = %+v, fences not stripped correctly", got)
	}
}

func TestEvaluate_SilentVerdictWithNullFieldsIsValid(t *testing.T) {
	// A decide-mode judge that answers notify=false legitimately leaves
	// urgency/task/body null — the rubric only requires them when
	// notifying. This must parse as a valid silent verdict, not a judge
	// error: the error path fails open to a send, which is exactly the
	// spurious ping the verdict said not to send.
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":null,"task":null,"body":null,"reason":"parked dev server only"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err != nil {
		t.Fatalf("Evaluate() error = %v, want valid silent verdict", err)
	}
	if got.Notify || got.Reason != "parked dev server only" {
		t.Errorf("Evaluate() = %+v, want silent verdict with reason preserved", got)
	}
}

func TestEvaluate_ComposeModeNotifyFalseErrors(t *testing.T) {
	// Compose mode's contract is notify=true (the send is already
	// decided); a notify=false verdict has no usable text and must surface
	// as an error so the caller's deterministic fallback runs.
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":null,"task":null,"body":null,"reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	if _, err := j.Evaluate(context.Background(), "digest", JudgeModeCompose); err == nil {
		t.Fatal("Evaluate() error = nil, want compose-mode notify=false error")
	}
}

func TestEvaluate_InvalidUrgencyErrors(t *testing.T) {
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"urgent","task":"t","body":"b","reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatalf("Evaluate() error = nil, want invalid urgency error")
	}
	if !strings.Contains(err.Error(), "urgency") {
		t.Errorf("Evaluate() error = %q, want it to mention urgency", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_MalformedJSONErrorsWithStdoutSnippet(t *testing.T) {
	t.Setenv("STUB_STDOUT", "this is not json at all")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatalf("Evaluate() error = nil, want malformed JSON error")
	}
	if !strings.Contains(err.Error(), "this is not json at all") {
		t.Errorf("Evaluate() error = %q, want it to include the stdout snippet", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_NonzeroExitErrorsWithStderrSnippet(t *testing.T) {
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: something broke")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatalf("Evaluate() error = nil, want nonzero exit error")
	}
	if !strings.Contains(err.Error(), "boom: something broke") {
		t.Errorf("Evaluate() error = %q, want it to include the stderr snippet", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_EmptyStdoutErrors(t *testing.T) {
	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatalf("Evaluate() error = nil, want empty stdout error")
	}
	if !strings.Contains(err.Error(), "empty stdout") {
		t.Errorf("Evaluate() error = %q, want it to mention empty stdout", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_TimeoutErrorsOnContextDeadline(t *testing.T) {
	t.Setenv("STUB_SLEEP", "1")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5", Timeout: 100 * time.Millisecond}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatalf("Evaluate() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Evaluate() error = %q, want it to mention timeout/deadline", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_EnvGuardDisablesRecursiveHook(t *testing.T) {
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	if _, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "CLAUDE_HOOKS_NTFY_DISABLED=true") {
		t.Errorf("subprocess env = %q, want CLAUDE_HOOKS_NTFY_DISABLED=true present", dump)
	}
}

func TestEvaluate_StdinCarriesDigestAndComposeRubric(t *testing.T) {
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	if _, err := j.Evaluate(context.Background(), "DIGEST-MARKER-COMPOSE", JudgeModeCompose); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "DIGEST-MARKER-COMPOSE") {
		t.Errorf("stdin did not carry the digest text: %q", dump)
	}
	if !strings.Contains(string(dump), "compose-only") {
		t.Errorf("stdin did not carry the compose-mode rubric line: %q", dump)
	}
}

func TestEvaluate_ArgvPassesValidEmptyMCPConfig(t *testing.T) {
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	if _, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "--strict-mcp-config") {
		t.Errorf("argv = %q, want it to contain --strict-mcp-config", dump)
	}
	if !strings.Contains(string(dump), "--mcp-config\n"+`{"mcpServers":{}}`) {
		t.Errorf(`argv = %q, want it to contain --mcp-config followed by {"mcpServers":{}}`, dump)
	}
}

func TestBuildJudgePrompt_BodyContractDemandsOwnWordsSummary(t *testing.T) {
	got, err := buildJudgePrompt("DIGEST-MARKER", JudgeModeDecide)
	if err != nil {
		t.Fatalf("buildJudgePrompt() error = %v", err)
	}
	for _, phrase := range []string{
		"one sentence",
		"in your own words",
		"what the session is waiting for (if blocked) or what was delivered (if done)",
		"Never quote or copy text from the digest or transcript",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("buildJudgePrompt() = %q, want it to contain %q", got, phrase)
		}
	}
}

func TestBuildGoalPrompt_ContainsRubricAndDigest(t *testing.T) {
	got := buildGoalPrompt("DIGEST-MARKER-GOAL")
	if !strings.Contains(got, goalRubric) {
		t.Errorf("buildGoalPrompt() does not contain goalRubric")
	}
	if !strings.Contains(got, "DIGEST\nDIGEST-MARKER-GOAL") {
		t.Errorf("buildGoalPrompt() = %q, want it to contain the digest under a DIGEST heading", got)
	}
}

func TestParseGoalVerdict_ValidVerdictsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want GoalVerdict
	}{
		{
			name: "pending",
			raw:  `{"tasks":"pending","goal_met":false,"reason":"build running"}`,
			want: GoalVerdict{Tasks: "pending", GoalMet: false, Reason: "build running"},
		},
		{
			name: "parked unmet",
			raw:  `{"tasks":"parked","goal_met":false,"reason":"review not run"}`,
			want: GoalVerdict{Tasks: "parked", GoalMet: false, Reason: "review not run"},
		},
		{
			name: "parked met",
			raw:  `{"tasks":"parked","goal_met":true,"reason":"all criteria shown met"}`,
			want: GoalVerdict{Tasks: "parked", GoalMet: true, Reason: "all criteria shown met"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGoalVerdict(tc.raw)
			if err != nil {
				t.Fatalf("parseGoalVerdict() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("parseGoalVerdict() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseGoalVerdict_StripsMarkdownFences(t *testing.T) {
	raw := "```json\n" + `{"tasks":"parked","goal_met":true,"reason":"met"}` + "\n```"
	got, err := parseGoalVerdict(raw)
	if err != nil {
		t.Fatalf("parseGoalVerdict() error = %v", err)
	}
	if got.Tasks != "parked" || !got.GoalMet || got.Reason != "met" {
		t.Errorf("parseGoalVerdict() = %+v, fences not stripped correctly", got)
	}
}

func TestParseGoalVerdict_ClampsLongReason(t *testing.T) {
	longReason := strings.Repeat("r", maxGoalReasonBytes+40)
	raw := `{"tasks":"pending","goal_met":false,"reason":"` + longReason + `"}`
	got, err := parseGoalVerdict(raw)
	if err != nil {
		t.Fatalf("parseGoalVerdict() error = %v", err)
	}
	if len(got.Reason) != maxGoalReasonBytes {
		t.Errorf("Reason length = %d, want %d", len(got.Reason), maxGoalReasonBytes)
	}
}

func TestParseGoalVerdict_RejectsMissingTasks(t *testing.T) {
	_, err := parseGoalVerdict(`{"goal_met":false,"reason":"r"}`)
	if err == nil {
		t.Fatalf("parseGoalVerdict() error = nil, want error for missing tasks")
	}
}

func TestParseGoalVerdict_RejectsInvalidTasksValue(t *testing.T) {
	_, err := parseGoalVerdict(`{"tasks":"running","goal_met":false,"reason":"r"}`)
	if err == nil {
		t.Fatalf("parseGoalVerdict() error = nil, want error for invalid tasks value")
	}
	if !strings.Contains(err.Error(), "tasks") {
		t.Errorf("parseGoalVerdict() error = %q, want it to mention tasks", err.Error())
	}
}

func TestParseGoalVerdict_RejectsNonJSON(t *testing.T) {
	_, err := parseGoalVerdict("this is not json at all")
	if err == nil {
		t.Fatalf("parseGoalVerdict() error = nil, want malformed JSON error")
	}
}

func TestParseGoalVerdict_RejectsEmptyInput(t *testing.T) {
	_, err := parseGoalVerdict("")
	if err == nil {
		t.Fatalf("parseGoalVerdict() error = nil, want error for empty input")
	}
}

func TestEvaluateGoal_ValidVerdictRoundTrips(t *testing.T) {
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":true,"reason":"all criteria shown met"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.EvaluateGoal(context.Background(), "DIGEST-MARKER")
	if err != nil {
		t.Fatalf("EvaluateGoal() error = %v", err)
	}
	want := GoalVerdict{Tasks: "parked", GoalMet: true, Reason: "all criteria shown met"}
	if got != want {
		t.Errorf("EvaluateGoal() = %+v, want %+v", got, want)
	}
}

func TestEvaluateGoal_TimeoutErrorsOnContextDeadline(t *testing.T) {
	t.Setenv("STUB_SLEEP", "1")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5", Timeout: 100 * time.Millisecond}
	got, err := j.EvaluateGoal(context.Background(), "digest")
	if err == nil {
		t.Fatalf("EvaluateGoal() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("EvaluateGoal() error = %q, want it to mention timeout/deadline", err.Error())
	}
	if got != (GoalVerdict{}) {
		t.Errorf("EvaluateGoal() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluateGoal_NonzeroExitErrorsWithStderrSnippet(t *testing.T) {
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: something broke")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.EvaluateGoal(context.Background(), "digest")
	if err == nil {
		t.Fatalf("EvaluateGoal() error = nil, want nonzero exit error")
	}
	if !strings.Contains(err.Error(), "boom: something broke") {
		t.Errorf("EvaluateGoal() error = %q, want it to include the stderr snippet", err.Error())
	}
	if got != (GoalVerdict{}) {
		t.Errorf("EvaluateGoal() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluateGoal_EmptyStdoutErrors(t *testing.T) {
	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.EvaluateGoal(context.Background(), "digest")
	if err == nil {
		t.Fatalf("EvaluateGoal() error = nil, want empty stdout error")
	}
	if !strings.Contains(err.Error(), "empty stdout") {
		t.Errorf("EvaluateGoal() error = %q, want it to mention empty stdout", err.Error())
	}
	if got != (GoalVerdict{}) {
		t.Errorf("EvaluateGoal() verdict = %+v, want zero value on error", got)
	}
}

func TestEvaluate_StdinCarriesDigestAndDecideRubric(t *testing.T) {
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`)

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	if _, err := j.Evaluate(context.Background(), "DIGEST-MARKER-DECIDE", JudgeModeDecide); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "DIGEST-MARKER-DECIDE") {
		t.Errorf("stdin did not carry the digest text: %q", dump)
	}
	if !strings.Contains(string(dump), "choose") {
		t.Errorf("stdin did not carry the decide-mode rubric line: %q", dump)
	}
}
