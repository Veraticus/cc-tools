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
	t.Setenv("STUB_STDOUT", "```json\n"+`{"notify":false,"urgency":"info","task":"t","body":"b","reason":"r"}`+"\n```")

	j := Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"}
	got, err := j.Evaluate(context.Background(), "digest", JudgeModeDecide)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Notify || got.Urgency != UrgencyInfo || got.Task != "t" {
		t.Errorf("Evaluate() = %+v, fences not stripped correctly", got)
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
