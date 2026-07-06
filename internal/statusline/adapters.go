// Package statusline provides terminal statusline rendering functionality.
package statusline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// DefaultFileReader implements FileReader using the OS.
type DefaultFileReader struct{}

// ReadFile reads a file from the filesystem.
func (f *DefaultFileReader) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", path, err)
	}
	return content, nil
}

// Exists checks if a file exists.
func (f *DefaultFileReader) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DefaultCommandRunner implements CommandRunner using exec.
type DefaultCommandRunner struct{}

// Run executes a command with arguments, bounded by a fixed 5s timeout.
func (c *DefaultCommandRunner) Run(command string, args ...string) ([]byte, error) {
	const commandTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return c.RunContext(ctx, command, args...)
}

// RunContext executes a command bounded by the caller-supplied context
// instead of Run's fixed timeout — used by callers that need a tighter
// budget (e.g. the git-status chip's ≤500ms deadline).
//
// cmd.Output() always calls cmd.Wait() after Start(), on every return
// path — including when ctx's deadline fires and the default Cancel
// (Process.Kill, since Go 1.20) runs — so the child process is
// guaranteed to be reaped here; no zombie process can be left behind.
func (c *DefaultCommandRunner) RunContext(ctx context.Context, command string, args ...string) ([]byte, error) {
	//nolint:gosec // G204: callers always pass a fixed binary name (git, hostname, ...), never external input
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running command %s: %w", command, err)
	}
	return output, nil
}

// DefaultEnvReader implements EnvReader using os.Getenv, with a
// file-backed override for AWS_PROFILE.
//
// The override exists because Claude Code's Bash tool spawns each
// command in a fresh subshell — `export AWS_PROFILE=foo` inside one
// Bash call dies when that subshell exits and never reaches Claude
// Code's process env. The PostToolUse mirror hook (in nix-config) mirrors
// the last seen export to ~/.cache/cc-tools/state.json so cc-tools can
// reflect Claude's intent in the statusline.
//
// The file is authoritative when present, even an empty string (which
// represents an explicit `unset`). When the file is missing,
// unreadable, or malformed, we fall through to the process env.
type DefaultEnvReader struct{}

// Get retrieves an environment variable. For AWS_PROFILE specifically,
// the state file at $CC_TOOLS_STATE_FILE (default
// ~/.cache/cc-tools/state.json) takes precedence when present.
func (e *DefaultEnvReader) Get(key string) string {
	if key == "AWS_PROFILE" {
		if v, ok := readStateFileAwsProfile(); ok {
			return v
		}
	}
	return os.Getenv(key)
}

func readStateFileAwsProfile() (string, bool) {
	path := os.Getenv("CC_TOOLS_STATE_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		path = filepath.Join(home, ".cache", "cc-tools", "state.json")
	}

	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted source
	if err != nil {
		return "", false
	}

	var state struct {
		AwsProfile *string `json:"aws_profile"`
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return "", false
	}
	if state.AwsProfile == nil {
		return "", false
	}
	return *state.AwsProfile, true
}
