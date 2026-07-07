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

// retryStubScript stands in for the claude binary in the invalid-model
// retry tests below. It logs each invocation's full argv (one line per
// call) to STUB_CALL_LOG, so a test can assert both the call count and
// whether a given invocation carried --model. STUB_RETRY_MODE selects the
// behavior:
//   - "invalid_then_ok": fails with the observed invalid-model-identifier
//     stdout when --model is present, succeeds (JudgeVerdict-shaped JSON)
//     when it is absent.
//   - "invalid_then_ok_goal": same, but the success payload is
//     GoalVerdict-shaped JSON, for EvaluateGoal's retry test.
//   - "invalid_always": always fails with the invalid-model-identifier
//     stdout, regardless of --model — proves the retry runs exactly once
//     and does not loop.
//   - "unrelated": always fails with an unrelated stderr message,
//     regardless of --model — proves non-model errors never retry.
const retryStubScript = `#!/bin/sh
input=$(cat)

argv_line=""
for a in "$@"; do
  argv_line="$argv_line$a "
done

if [ -n "$STUB_CALL_LOG" ]; then
  printf '%s\n' "$argv_line" >> "$STUB_CALL_LOG"
fi

has_model=0
case "$argv_line" in
  *"--model"*) has_model=1 ;;
esac

case "$STUB_RETRY_MODE" in
  invalid_then_ok)
    if [ "$has_model" = "1" ]; then
      printf '%s' 'API Error (claude-haiku-4-5): 400 The provided model identifier is invalid.. Run --model to pick a different model.'
      exit 1
    fi
    printf '%s' '{"notify":true,"urgency":"done","task":"recovered","body":"ok without model","reason":"r"}'
    exit 0
    ;;
  invalid_then_ok_goal)
    if [ "$has_model" = "1" ]; then
      printf '%s' 'API Error (claude-haiku-4-5): 400 The provided model identifier is invalid.. Run --model to pick a different model.'
      exit 1
    fi
    printf '%s' '{"tasks":"parked","goal_met":true,"reason":"recovered"}'
    exit 0
    ;;
  invalid_always)
    printf '%s' 'API Error (claude-haiku-4-5): 400 The provided model identifier is invalid.. Run --model to pick a different model.'
    exit 1
    ;;
  unrelated)
    printf '%s' 'boom: something else broke' >&2
    exit 1
    ;;
esac

exit 0
`

// writeRetryStubClaude writes retryStubScript to a temp dir and returns its path.
func writeRetryStubClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte(retryStubScript), 0o755); err != nil {
		t.Fatalf("writing retry stub: %v", err)
	}
	return path
}

// readCallLog reads a STUB_CALL_LOG file and returns its non-empty lines,
// one per subprocess invocation.
func readCallLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading call log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
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

func TestEvaluate_RetriesWithoutModelOnInvalidModelError(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_then_ok")

	j := Judge{Bin: writeRetryStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err != nil {
		t.Fatalf("Evaluate() error = %v, want recovery via retry", err)
	}
	if !got.RetriedWithoutModel {
		t.Errorf("Evaluate() RetriedWithoutModel = false, want true")
	}
	if got.Task != "recovered" {
		t.Errorf("Evaluate() Task = %q, want the retry's recovered verdict", got.Task)
	}

	calls := readCallLog(t, callLog)
	if len(calls) != 2 {
		t.Fatalf("call count = %d, want 2 (initial + retry)", len(calls))
	}
	if !strings.Contains(calls[0], "--model") {
		t.Errorf("first call argv = %q, want it to contain --model", calls[0])
	}
	if strings.Contains(calls[1], "--model") {
		t.Errorf("second call argv = %q, want it to omit --model", calls[1])
	}
}

func TestEvaluate_UnrelatedErrorDoesNotRetry(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "unrelated")

	j := Judge{Bin: writeRetryStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want unrelated error surfaced")
	}
	if !strings.Contains(err.Error(), "boom: something else broke") {
		t.Errorf("Evaluate() error = %q, want it to include the stderr snippet", err.Error())
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}

	calls := readCallLog(t, callLog)
	if len(calls) != 1 {
		t.Fatalf("call count = %d, want 1 (no retry for a non-model error)", len(calls))
	}
}

func TestEvaluate_DoubleInvalidModelErrorRunsExactlyTwice(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_always")

	j := Judge{Bin: writeRetryStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want invalid-model error after a failed retry")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value on error", got)
	}

	calls := readCallLog(t, callLog)
	if len(calls) != 2 {
		t.Fatalf("call count = %d, want exactly 2 (no further retries)", len(calls))
	}
}

func TestEvaluateGoal_RetriesWithoutModelOnInvalidModelError(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_then_ok_goal")

	j := Judge{Bin: writeRetryStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.EvaluateGoal(context.Background(), "digest")
	if err != nil {
		t.Fatalf("EvaluateGoal() error = %v, want recovery via retry", err)
	}
	if !got.RetriedWithoutModel {
		t.Errorf("EvaluateGoal() RetriedWithoutModel = false, want true")
	}
	if got.Reason != "recovered" {
		t.Errorf("EvaluateGoal() Reason = %q, want the retry's recovered verdict", got.Reason)
	}

	calls := readCallLog(t, callLog)
	if len(calls) != 2 {
		t.Fatalf("call count = %d, want 2 (initial + retry)", len(calls))
	}
	if !strings.Contains(calls[0], "--model") {
		t.Errorf("first call argv = %q, want it to contain --model", calls[0])
	}
	if strings.Contains(calls[1], "--model") {
		t.Errorf("second call argv = %q, want it to omit --model", calls[1])
	}
}

func TestResolveJudgeModel_PrefersSmallFastModelEnv(t *testing.T) {
	environ := []string{"OTHER=1", "ANTHROPIC_SMALL_FAST_MODEL=claude-haiku-9000"}
	got := ResolveJudgeModel(environ, "claude-haiku-4-5")
	if got != "claude-haiku-9000" {
		t.Errorf("ResolveJudgeModel() = %q, want claude-haiku-9000", got)
	}
}

func TestResolveJudgeModel_DefaultsWhenAbsent(t *testing.T) {
	got := ResolveJudgeModel([]string{"OTHER=1"}, "claude-haiku-4-5")
	if got != "claude-haiku-4-5" {
		t.Errorf("ResolveJudgeModel() = %q, want fallback default", got)
	}
}

func TestResolveJudgeModel_DefaultsWhenEnvValueEmpty(t *testing.T) {
	got := ResolveJudgeModel([]string{"ANTHROPIC_SMALL_FAST_MODEL="}, "claude-haiku-4-5")
	if got != "claude-haiku-4-5" {
		t.Errorf("ResolveJudgeModel() = %q, want fallback default when env value is empty", got)
	}
}
