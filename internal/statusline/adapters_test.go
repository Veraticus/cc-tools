package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultCommandRunner_RunContext_TimeoutReapsChild proves the
// no-zombie guarantee at the DefaultCommandRunner level: a real `sleep`
// subprocess (far longer than the context budget) is killed and
// reaped promptly when its context expires. cmd.Output() always calls
// cmd.Wait() after Start(), on every return path -- including when
// ctx's deadline fires and the default Cancel (Process.Kill) runs --
// so RunContext returning at all here proves Wait() completed and the
// child was reaped; if it hadn't, Output() itself would still be
// blocked inside the process.
func TestDefaultCommandRunner_RunContext_TimeoutReapsChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available on PATH")
	}

	r := &DefaultCommandRunner{}
	const budget = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	_, err := r.RunContext(ctx, "sleep", "5")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the command is killed by context timeout")
	}
	const generousUpperBound = 2 * time.Second
	if elapsed > generousUpperBound {
		t.Fatalf("RunContext should return promptly after context timeout, took %v", elapsed)
	}
}

func TestDefaultEnvReader_OldStateFileEnvIgnored(t *testing.T) {
	oldState := filepath.Join(t.TempDir(), "old-state.json")
	if err := os.WriteFile(oldState, []byte(`{"aws_profile":"old-profile"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_TOOLS_STATE_FILE", oldState)
	t.Setenv("STEWARD_STATE_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("AWS_PROFILE", "canonical-process-env")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "canonical-process-env" {
		t.Errorf("old state env must be ignored: got %q", got)
	}
}

func TestDefaultEnvReader_AwsProfileFromFile(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"aws_profile":"foo-prod"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEWARD_STATE_FILE", state)
	t.Setenv("AWS_PROFILE", "other-profile")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "foo-prod" {
		t.Errorf("file should override env: got %q, want foo-prod", got)
	}
}

func TestDefaultEnvReader_AwsProfileEmptyStringFromFile(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"aws_profile":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEWARD_STATE_FILE", state)
	t.Setenv("AWS_PROFILE", "fallback-shouldnt-win")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "" {
		t.Errorf("empty string in file should be authoritative: got %q, want empty", got)
	}
}

func TestDefaultEnvReader_AwsProfileFileMissing(t *testing.T) {
	t.Setenv("STEWARD_STATE_FILE", "/nonexistent/state.json")
	t.Setenv("AWS_PROFILE", "from-env")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "from-env" {
		t.Errorf("missing file should fall through to env: got %q, want from-env", got)
	}
}

func TestDefaultEnvReader_AwsProfileMalformedFile(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`not json {`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEWARD_STATE_FILE", state)
	t.Setenv("AWS_PROFILE", "from-env")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "from-env" {
		t.Errorf("malformed file should not crash, should fall through: got %q, want from-env", got)
	}
}

func TestDefaultEnvReader_AwsProfileKeyAbsentInFile(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"something_else":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEWARD_STATE_FILE", state)
	t.Setenv("AWS_PROFILE", "from-env")

	r := &DefaultEnvReader{}
	if got := r.Get("AWS_PROFILE"); got != "from-env" {
		t.Errorf("file without aws_profile key should fall through: got %q, want from-env", got)
	}
}

func TestDefaultEnvReader_NonAwsProfileUnchanged(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"aws_profile":"foo-prod"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEWARD_STATE_FILE", state)
	t.Setenv("KUBECONFIG", "/some/path")

	r := &DefaultEnvReader{}
	if got := r.Get("KUBECONFIG"); got != "/some/path" {
		t.Errorf("non-AWS keys should pass through to env: got %q, want /some/path", got)
	}
}
