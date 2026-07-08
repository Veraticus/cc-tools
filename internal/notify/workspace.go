package notify

import (
	"context"
	"os/exec"
	"strings"
)

// WorkspaceName resolves the tmux locator ("workspace") the hook's own pane
// lives in, as session_name:window_index (e.g. "earth:3"): the TMUX_PANE
// entry in environ names the pane, and tmux reports the session and window
// containing it. The window index — not the window name — is the second
// segment because auto-rename makes window names the running command
// ("claude", "zsh"), useless for telling two claude windows in one session
// apart. Sessions outside tmux — and background jobs, whose inherited
// TMUX_PANE is empty — resolve to "", which callers treat as "no workspace"
// and fall back to the host name. The pane must be passed explicitly as -t:
// with an empty target, tmux falls back to the most recently used attached
// session, which is some other workspace entirely.
func WorkspaceName(environ []string, run func(name string, args ...string) (string, error)) string {
	pane := parseEnviron(environ)["TMUX_PANE"]
	if pane == "" {
		return ""
	}
	out, err := run("tmux", "display-message", "-pt", pane, "#{session_name}:#{window_index}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// RunCommand is the production run func for WorkspaceName: it execs name
// with args and returns stdout only (Output(), not CombinedOutput() —
// stderr noise from tmux must never be mistaken for the value on stdout).
func RunCommand(name string, args ...string) (string, error) {
	// context.Background(): this is a quick, bounded tmux query with no
	// ambient context to propagate from.
	//nolint:gosec // G204: only caller is WorkspaceName, which always passes literal "tmux" + fixed subcommand args
	out, err := exec.CommandContext(context.Background(), name, args...).Output()
	return string(out), err
}
