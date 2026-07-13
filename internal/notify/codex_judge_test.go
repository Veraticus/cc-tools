package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const codexJudgeStubScript = `#!/bin/sh
empty=true
for entry in ./* ./.[!.]* ./..?*; do
  if [ -e "$entry" ] || [ -L "$entry" ]; then
    empty=false
    break
  fi
done

if mode=$(stat -c '%a' . 2>/dev/null); then
  :
elif mode=$(stat -f '%Lp' . 2>/dev/null); then
  :
else
  mode=unknown
fi

if [ -n "$STUB_MODE_LOG" ]; then
  printf '%s|%s\n' "$PWD" "$mode" >> "$STUB_MODE_LOG"
fi

if [ -n "$STUB_CALL_LOG" ]; then
  printf '%s\n' 'call' >> "$STUB_CALL_LOG"
fi

if [ -n "$STUB_CWD_LOG" ]; then
  printf '%s\n' "$PWD" >> "$STUB_CWD_LOG"
fi

if [ -n "$STUB_EMPTY_LOG" ]; then
  printf '%s|%s\n' "$PWD" "$empty" >> "$STUB_EMPTY_LOG"
fi

if [ -n "$STUB_ARGS_FILE" ]; then
  : > "$STUB_ARGS_FILE"
  for arg in "$@"; do
    printf '%s\n' "$arg" >> "$STUB_ARGS_FILE"
  done
fi

if [ -n "$STUB_ENV_FILE" ]; then
  env > "$STUB_ENV_FILE"
fi

if [ -n "$STUB_STDIN_FILE" ]; then
  cat > "$STUB_STDIN_FILE"
else
  cat > /dev/null
fi

if [ -n "$STUB_SLEEP" ]; then
  exec sleep "$STUB_SLEEP"
fi

if [ -n "$STUB_STDERR" ]; then
  printf '%s' "$STUB_STDERR" >&2
fi

if [ -n "$STUB_STDOUT" ]; then
  printf '%s' "$STUB_STDOUT"
fi

if [ -n "$STUB_LOCK_TEMP_BASE" ]; then
  chmod 0500 "$TMPDIR"
fi

exit "${STUB_EXIT:-0}"
`

const validCodexVerdict = `{"notify":true,"urgency":"done","task":"tests complete","body":"The requested checks now pass.","reason":"work delivered"}`

func writeStubCodex(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(codexJudgeStubScript), 0o755); err != nil {
		t.Fatalf("writing Codex stub: %v", err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func useTempBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	return base
}

func assertTempBaseEmpty(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("reading temporary base: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary base contains %v after Evaluate(), want no leaked workdir", entries)
	}
}

func TestCodexJudgeEvaluate_ValidVerdictAndInvocation(t *testing.T) {
	bin := writeStubCodex(t)
	base := useTempBase(t)
	argsFile := filepath.Join(t.TempDir(), "args")
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	envFile := filepath.Join(t.TempDir(), "env")
	cwdLog := filepath.Join(t.TempDir(), "cwd")
	emptyLog := filepath.Join(t.TempDir(), "empty")
	modeLog := filepath.Join(t.TempDir(), "mode")

	t.Setenv("STUB_ARGS_FILE", argsFile)
	t.Setenv("STUB_STDIN_FILE", stdinFile)
	t.Setenv("STUB_ENV_FILE", envFile)
	t.Setenv("STUB_CWD_LOG", cwdLog)
	t.Setenv("STUB_EMPTY_LOG", emptyLog)
	t.Setenv("STUB_MODE_LOG", modeLog)
	t.Setenv("STUB_STDOUT", validCodexVerdict)
	t.Setenv("CODEX_JUDGE_PARENT_SENTINEL", "inherited")
	t.Setenv("CC_TOOLS_NTFY_DISABLED", "false")

	digest := "FINAL ASSISTANT MESSAGE\nThe checks passed."
	j := CodexJudge{Bin: bin, Model: "luna-test-model"}
	got, err := j.Evaluate(context.Background(), digest)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	want := JudgeVerdict{
		Notify:  true,
		Urgency: UrgencyDone,
		Task:    "tests complete",
		Body:    "The requested checks now pass.",
		Reason:  "work delivered",
	}
	if got != want {
		t.Errorf("Evaluate() = %+v, want %+v", got, want)
	}

	cwds := readLines(t, cwdLog)
	if len(cwds) != 1 {
		t.Fatalf("recorded cwd count = %d, want 1", len(cwds))
	}
	wantArgs := []string{
		"exec",
		"--ignore-user-config",
		"--ignore-rules",
		"--ephemeral",
		"--skip-git-repo-check",
		"--sandbox",
		"read-only",
		"--color",
		"never",
		"-C",
		cwds[0],
		"-m",
		"luna-test-model",
		"-c",
		`model_reasoning_effort="low"`,
		"-",
	}
	gotArgs := readLines(t, argsFile)
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("argv = %#v, want %#v", gotArgs, wantArgs)
	}
	for _, arg := range gotArgs {
		if strings.Contains(arg, digest) {
			t.Errorf("argv contains digest %q; prompt must travel only on stdin", arg)
		}
		if arg == "--output-schema" {
			t.Error("argv contains forbidden --output-schema")
		}
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading stdin capture: %v", err)
	}
	if !strings.HasSuffix(string(stdin), digest) {
		t.Errorf("stdin = %q, want exact digest as its suffix", stdin)
	}
	environ, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("reading environment capture: %v", err)
	}
	if !strings.Contains(string(environ), "CODEX_JUDGE_PARENT_SENTINEL=inherited\n") {
		t.Errorf("child environment did not inherit parent sentinel: %q", environ)
	}
	if !strings.Contains(string(environ), "CC_TOOLS_NTFY_DISABLED=true\n") {
		t.Errorf("child environment lacks recursion guard: %q", environ)
	}
	if got := readLines(t, emptyLog); len(got) != 1 || got[0] != cwds[0]+"|true" {
		t.Errorf("initial workdir state = %#v, want [%q]", got, cwds[0]+"|true")
	}
	if got := readLines(t, modeLog); len(got) != 1 || got[0] != cwds[0]+"|700" {
		t.Errorf("initial workdir mode = %#v, want [%q]", got, cwds[0]+"|700")
	}
	if _, statErr := os.Stat(cwds[0]); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("temporary cwd still exists after Evaluate(): %v", statErr)
	}
	assertTempBaseEmpty(t, base)
}

func TestCodexJudgeEvaluate_ClampsLongVerdictFields(t *testing.T) {
	plainTask := strings.Repeat("Luna ", 30) + "月"
	plainBody := strings.Repeat("完了 ", 80) + "end"
	longTask := "## **" + plainTask + "**"
	longBody := "- `" + plainBody + "`"
	stdout := `{"notify":true,"urgency":"done","task":"` + longTask + `","body":"` + longBody + `","reason":"r"}`
	t.Setenv("STUB_STDOUT", stdout)

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "digest")
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	wantTask := truncateWords(plainTask, maxTaskBytes)
	if got.Task != wantTask {
		t.Errorf(
			"Task = %q, want shared %d-byte clamp %q",
			got.Task, maxTaskBytes, wantTask,
		)
	}
	wantBody := truncateWords(plainBody, maxBodyBytes)
	if got.Body != wantBody {
		t.Errorf(
			"Body = %q, want shared %d-byte clamp %q",
			got.Body, maxBodyBytes, wantBody,
		)
	}
	if len(got.Task) > maxTaskBytes || len(got.Body) > maxBodyBytes {
		t.Errorf("clamped byte lengths = task %d, body %d", len(got.Task), len(got.Body))
	}
	if !utf8.ValidString(got.Task) || !utf8.ValidString(got.Body) {
		t.Errorf("clamped fields are not valid UTF-8: task %q, body %q", got.Task, got.Body)
	}
}

func TestCodexJudgeEvaluate_AcceptsSemanticVerdicts(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   JudgeVerdict
	}{
		{
			name:   "silent",
			stdout: `{"notify":false,"urgency":null,"task":null,"body":null,"reason":"routine worker report"}`,
			want:   JudgeVerdict{Notify: false, Reason: "routine worker report"},
		},
		{
			name:   "blocked",
			stdout: `{"notify":true,"urgency":"blocked","task":"choose database","body":"A database decision is needed.","reason":"user decision gates progress"}`,
			want: JudgeVerdict{
				Notify: true, Urgency: UrgencyBlocked, Task: "choose database",
				Body: "A database decision is needed.", Reason: "user decision gates progress",
			},
		},
		{
			name:   "done",
			stdout: validCodexVerdict,
			want: JudgeVerdict{
				Notify: true, Urgency: UrgencyDone, Task: "tests complete",
				Body: "The requested checks now pass.", Reason: "work delivered",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STUB_STDOUT", tt.stdout)
			j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
			got, err := j.Evaluate(context.Background(), "digest")
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Evaluate() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNormalizeCodexPlainText_PreservesLiteralTechnicalTokens(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "dunder filename", input: "__init__.py", want: "__init__.py"},
		{name: "recursive glob", input: "**/*.go", want: "**/*.go"},
		{name: "bare extension glob", input: "*.*", want: "*.*"},
		{name: "bare underscored glob", input: "*_*", want: "*_*"},
		{name: "underscored directory", input: "_private_/file", want: "_private_/file"},
		{name: "path and glob", input: "src/my_project/*.go", want: "src/my_project/*.go"},
		{name: "Unicode technical text", input: "检查 src/模块_一/*.go ✅", want: "检查 src/模块_一/*.go ✅"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCodexPlainText(tt.input); got != tt.want {
				t.Errorf("normalizeCodexPlainText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeCodexPlainText_RemovesActualEmphasis(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "asterisk bold", input: "**bold**", want: "bold"},
		{name: "underscore bold", input: "__bold__", want: "bold"},
		{name: "asterisk italic", input: "*italic*", want: "italic"},
		{name: "underscore italic", input: "_italic_", want: "italic"},
		{name: "strikethrough", input: "~~strike~~", want: "strike"},
		{name: "bold path", input: "**src/foo.go**", want: "src/foo.go"},
		{name: "bold dotted text", input: "**v1.2**", want: "v1.2"},
		{name: "bold glob path", input: "**src/*.go**", want: "src/*.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCodexPlainText(tt.input); got != tt.want {
				t.Errorf("normalizeCodexPlainText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCodexJudgeEvaluate_SourceAuthorityPrompt(t *testing.T) {
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("STUB_STDIN_FILE", stdinFile)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	digest := "USER INPUT\nPlease ship it.\nFINAL ASSISTANT MESSAGE\nI recommend more testing."
	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	if _, err := j.Evaluate(context.Background(), digest); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	promptBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading prompt: %v", err)
	}
	prompt := string(promptBytes)
	for _, phrase := range []string{
		"whether a Codex turn should notify",
		"single raw JSON object",
		"notify=false",
		"routine worker, reviewer, or scout reports",
		"acknowledgements",
		"internal progress",
		"waiting on other work",
		`urgency="blocked"`,
		"answer, decision, approval, recovery action",
		"material blocker or failure",
		"possible intervention",
		`For urgency="blocked", body must state the exact answer, decision, approval, recovery action, or other concrete input the user must provide or take`,
		`urgency="done"`,
		"genuinely useful final deliverable",
		"material completed outcome",
		"at most 6 words",
		"60 UTF-8 bytes",
		"one sentence",
		"at most 180 characters",
		"in your own words",
		"Do not use tools",
		"FINAL ASSISTANT MESSAGE alone",
		"USER INPUT is context for task naming only, not proof",
		"complete digest",
		"plain text",
		"no Markdown, JSON, headings, bullets, backticks, emphasis, links, or fences",
	} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("prompt does not contain required rule %q: %q", phrase, prompt)
		}
	}
}

func TestCodexJudgeEvaluate_ModelOutputFailures(t *testing.T) {
	longStdout := strings.Repeat("O", maxErrSnippetBytes) + "STDOUT-LEAK"
	longStderr := strings.Repeat("E", maxErrSnippetBytes) + "STDERR-LEAK"
	longUrgency := strings.Repeat("U", 4096) + "URGENCY-LEAK"
	tests := []struct {
		name        string
		stdout      string
		stderr      string
		exit        string
		wantError   string
		forbidError []string
		maxErrorLen int
	}{
		{name: "empty stdout", wantError: "empty stdout"},
		{name: "malformed JSON", stdout: "not-json", wantError: "malformed verdict JSON"},
		{
			name:      "invalid urgency",
			stdout:    `{"notify":true,"urgency":"urgent","task":"t","body":"b","reason":"r"}`,
			wantError: "invalid urgency",
		},
		{
			name:        "invalid urgency is bounded",
			stdout:      `{"notify":true,"urgency":"` + longUrgency + `","task":"t","body":"b","reason":"r"}`,
			wantError:   "invalid urgency",
			forbidError: []string{"URGENCY-LEAK"},
			maxErrorLen: len("codex exec: invalid verdict: ") + maxErrSnippetBytes,
		},
		{
			name:      "invalid notified urgency",
			stdout:    `{"notify":true,"urgency":"info","task":"t","body":"b","reason":"r"}`,
			wantError: "invalid notified urgency",
		},
		{
			name:        "nonzero exit has bounded output",
			stdout:      longStdout,
			stderr:      longStderr,
			exit:        "7",
			wantError:   strings.Repeat("O", maxErrSnippetBytes),
			forbidError: []string{"STDOUT-LEAK", "STDERR-LEAK"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := useTempBase(t)
			t.Setenv("STUB_STDOUT", tt.stdout)
			t.Setenv("STUB_STDERR", tt.stderr)
			t.Setenv("STUB_EXIT", tt.exit)

			j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
			got, err := j.Evaluate(context.Background(), "digest")
			if err == nil {
				t.Fatal("Evaluate() error = nil, want model-output error")
			}
			if got != (JudgeVerdict{}) {
				t.Errorf("Evaluate() verdict = %+v, want zero value", got)
			}
			if !strings.Contains(err.Error(), "codex exec") {
				t.Errorf("Evaluate() error = %q, want it to identify codex exec", err)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("Evaluate() error = %q, want it to contain %q", err, tt.wantError)
			}
			for _, forbidden := range tt.forbidError {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("Evaluate() error leaked output beyond snippet bound: %q", err)
				}
			}
			if tt.maxErrorLen > 0 && len(err.Error()) > tt.maxErrorLen {
				t.Errorf("Evaluate() error length = %d, want at most %d: %q", len(err.Error()), tt.maxErrorLen, err)
			}
			assertTempBaseEmpty(t, base)
		})
	}
}

func TestCodexJudgeEvaluate_CleanupFailureReturnsZeroVerdictAndPreservesErrors(t *testing.T) {
	base := useTempBase(t)
	cwdLog := filepath.Join(t.TempDir(), "cwd")
	t.Setenv("STUB_CWD_LOG", cwdLog)
	t.Setenv("STUB_STDERR", "model execution failed")
	t.Setenv("STUB_EXIT", "7")
	t.Setenv("STUB_LOCK_TEMP_BASE", "true")

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "digest")
	if chmodErr := os.Chmod(base, 0o700); chmodErr != nil {
		t.Fatalf("restoring temporary-base permissions: %v", chmodErr)
	}
	workdirs := readLines(t, cwdLog)
	if len(workdirs) != 1 {
		t.Fatalf("recorded cwd count = %d, want 1", len(workdirs))
	}
	t.Cleanup(func() {
		if cleanupErr := os.RemoveAll(workdirs[0]); cleanupErr != nil {
			t.Errorf("cleaning intentionally retained workdir: %v", cleanupErr)
		}
	})

	if err == nil {
		t.Fatal("Evaluate() error = nil, want cleanup failure")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	for _, want := range []string{"codex exec", "removing temporary workdir", "exited with error", "model execution failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Evaluate() error = %q, want it to preserve %q", err, want)
		}
	}
}

func TestCodexJudgeEvaluate_MissingBinary(t *testing.T) {
	base := useTempBase(t)
	j := CodexJudge{Bin: filepath.Join(t.TempDir(), "missing-codex"), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "digest")
	if err == nil {
		t.Fatal("Evaluate() error = nil, want missing binary error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if !strings.Contains(err.Error(), "codex exec") {
		t.Errorf("Evaluate() error = %q, want it to identify codex exec", err)
	}
	assertTempBaseEmpty(t, base)
}

func TestCodexJudgeEvaluate_UsesCallerDeadlineWhenSmaller(t *testing.T) {
	base := useTempBase(t)
	t.Setenv("STUB_SLEEP", "5")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna", Timeout: 2 * time.Second}
	got, err := j.Evaluate(ctx, "digest")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want deadline error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Evaluate() error = %v, want wrapped context deadline exceeded", err)
	}
	if elapsed >= time.Second {
		t.Errorf("Evaluate() took %s, want caller deadline smaller than judge timeout to win", elapsed)
	}
	assertTempBaseEmpty(t, base)
}

func TestCodexJudgeEvaluate_UsesJudgeTimeoutWhenSmaller(t *testing.T) {
	base := useTempBase(t)
	t.Setenv("STUB_SLEEP", "5")
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	start := time.Now()
	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna", Timeout: 50 * time.Millisecond}
	got, err := j.Evaluate(ctx, "digest")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Evaluate() error = nil, want judge timeout error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Evaluate() error = %v, want wrapped context deadline exceeded", err)
	}
	if elapsed >= 400*time.Millisecond {
		t.Errorf("Evaluate() took %s, want judge timeout smaller than caller deadline to win", elapsed)
	}
	assertTempBaseEmpty(t, base)
}

func TestCodexJudgeEvaluate_CallerCancellation(t *testing.T) {
	base := useTempBase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna", Timeout: time.Second}
	got, err := j.Evaluate(ctx, "digest")
	if err == nil {
		t.Fatal("Evaluate() error = nil, want cancellation error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Evaluate() error = %v, want wrapped context canceled", err)
	}
	if !strings.Contains(err.Error(), "codex exec") {
		t.Errorf("Evaluate() error = %q, want it to identify codex exec", err)
	}
	assertTempBaseEmpty(t, base)
}

func TestCodexJudgeEvaluate_ZeroOrNegativeTimeoutUsesDefault(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Setenv("STUB_STDOUT", validCodexVerdict)
			j := CodexJudge{Bin: writeStubCodex(t), Model: "luna", Timeout: timeout}
			if _, err := j.Evaluate(context.Background(), "digest"); err != nil {
				t.Fatalf("Evaluate() with timeout %s error = %v", timeout, err)
			}
		})
	}
}

func TestCodexJudgeEvaluate_TempDirectoryCreationFailure(t *testing.T) {
	blockedTemp := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedTemp, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("writing TMPDIR blocker: %v", err)
	}
	callLog := filepath.Join(t.TempDir(), "calls")
	t.Setenv("TMPDIR", blockedTemp)
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "digest")
	if err == nil {
		t.Fatal("Evaluate() error = nil, want temp-directory creation error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if !strings.Contains(err.Error(), "codex exec") || !strings.Contains(err.Error(), "temporary workdir") {
		t.Errorf("Evaluate() error = %q, want wrapped codex exec temporary-workdir error", err)
	}
	if _, statErr := os.Stat(callLog); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("stub was spawned despite temp-directory creation failure: %v", statErr)
	}
}

func TestCodexJudgeEvaluate_DoesNotRetryWithoutModel(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_ARGS_FILE", argsFile)
	t.Setenv("STUB_STDERR", invalidModelErrText)
	t.Setenv("STUB_EXIT", "1")

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "digest")
	if err == nil {
		t.Fatal("Evaluate() error = nil, want invalid-model error")
	}
	if got != (JudgeVerdict{}) {
		t.Errorf("Evaluate() verdict = %+v, want zero value", got)
	}
	if calls := readLines(t, callLog); len(calls) != 1 {
		t.Errorf("subprocess call count = %d, want exactly 1", len(calls))
	}
	args := readLines(t, argsFile)
	if !strings.Contains(strings.Join(args, "\n"), "-m\nluna") {
		t.Errorf("argv = %#v, want one explicit model selection", args)
	}
}

func TestResolveCodexJudgeModel(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{name: "unset", environ: []string{"OTHER=value"}, want: "fallback"},
		{name: "empty", environ: []string{"CC_TOOLS_CODEX_JUDGE_MODEL="}, want: "fallback"},
		{name: "override", environ: []string{"OTHER=value", "CC_TOOLS_CODEX_JUDGE_MODEL=luna"}, want: "luna"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveCodexJudgeModel(tt.environ, "fallback"); got != tt.want {
				t.Errorf("ResolveCodexJudgeModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexJudgeEvaluate_PreservesLargeSpecialDigestOnStdin(t *testing.T) {
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("STUB_STDIN_FILE", stdinFile)
	t.Setenv("STUB_STDOUT", validCodexVerdict)
	digest := strings.Repeat("line with '$HOME' $(touch nope); `echo no` 雪 ☃ \\\"quotes\\\"\n", 12000) + "FINAL-BYTE"

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	if _, err := j.Evaluate(context.Background(), digest); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading stdin capture: %v", err)
	}
	if !strings.HasSuffix(string(got), digest) {
		t.Errorf("stdin did not preserve the %d-byte special-character digest exactly", len(digest))
	}
}

func TestCodexJudgeEvaluate_ConcurrentCallsUseDistinctCleanWorkdirs(t *testing.T) {
	base := useTempBase(t)
	cwdLog := filepath.Join(t.TempDir(), "cwds")
	emptyLog := filepath.Join(t.TempDir(), "empty")
	t.Setenv("STUB_CWD_LOG", cwdLog)
	t.Setenv("STUB_EMPTY_LOG", emptyLog)
	t.Setenv("STUB_STDOUT", validCodexVerdict)
	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := j.Evaluate(context.Background(), "digest")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Evaluate() error = %v", err)
		}
	}

	cwds := readLines(t, cwdLog)
	if len(cwds) != 2 {
		t.Fatalf("recorded cwd count = %d, want 2", len(cwds))
	}
	if cwds[0] == cwds[1] {
		t.Errorf("concurrent calls used the same cwd %q", cwds[0])
	}
	states := readLines(t, emptyLog)
	if len(states) != 2 {
		t.Fatalf("recorded initial-state count = %d, want 2", len(states))
	}
	for _, cwd := range cwds {
		if !containsLine(states, cwd+"|true") {
			t.Errorf("initial states = %#v, want %q", states, cwd+"|true")
		}
		if _, statErr := os.Stat(cwd); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("temporary cwd %q still exists after Evaluate(): %v", cwd, statErr)
		}
	}
	assertTempBaseEmpty(t, base)
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func TestCodexJudgeEvaluate_EmptyDigestStillInvokesModel(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls")
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_STDIN_FILE", stdinFile)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	j := CodexJudge{Bin: writeStubCodex(t), Model: "luna"}
	got, err := j.Evaluate(context.Background(), "")
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !got.Notify || got.Urgency != UrgencyDone {
		t.Errorf("Evaluate() = %+v, want valid model verdict", got)
	}
	if calls := readLines(t, callLog); len(calls) != 1 {
		t.Errorf("subprocess call count = %d, want 1", len(calls))
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading stdin capture: %v", err)
	}
	if !strings.HasSuffix(string(stdin), "DIGEST\n") {
		t.Errorf("stdin = %q, want empty digest passed after DIGEST marker", stdin)
	}
}
