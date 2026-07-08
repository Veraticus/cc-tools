package notify

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// presenceActivityWindow is how recently tmux's client_activity must fall
// relative to now for the user to count as present. Chosen to be generous
// enough to survive the tmux query's own latency and clock granularity
// while still meaning "actively looking at this pane right now".
const presenceActivityWindow = 30 * time.Second

// UserPresent reports whether the user is currently looking at the tmux
// pane this session is running in. It is a conservative check: any
// ambiguity or error resolves to false (not present), because a wrong
// "present" answer suppresses a notification the user actually needed —
// eating a real ping is the worse failure mode than sending a spurious one.
//
// All of the following must hold:
//   - environ (os.Environ() form) carries a TMUX entry, any value including
//     empty (see hasTMUXEnv: the key's presence is what counts) — outside
//     tmux entirely, there is no pane to check presence against.
//   - run("tmux", "display-message", "-p",
//     "#{window_active},#{session_attached}") returns exactly "1,1" once
//     trimmed: the session's window is the active one AND a client is
//     attached to the session.
//   - run("tmux", "display-message", "-p", "#{client_activity}") returns an
//     epoch-seconds value within presenceActivityWindow of now: the
//     attached client must have generated activity recently, not merely be
//     attached to an otherwise-idle terminal.
//
// Any run error, unexpected output, or unparseable timestamp makes
// UserPresent return false.
func UserPresent(environ []string, now time.Time, run func(name string, args ...string) (string, error)) bool {
	if !hasTMUXEnv(environ) {
		return false
	}

	activePane, err := run("tmux", "display-message", "-p", "#{window_active},#{session_attached}")
	if err != nil {
		return false
	}
	if strings.TrimSpace(activePane) != "1,1" {
		return false
	}

	activity, err := run("tmux", "display-message", "-p", "#{client_activity}")
	if err != nil {
		return false
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(activity), 10, 64)
	if err != nil {
		return false
	}

	age := now.Sub(time.Unix(epoch, 0))
	if age < 0 {
		age = -age
	}
	return age <= presenceActivityWindow
}

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

// hasTMUXEnv reports whether environ (os.Environ() form) carries a TMUX
// entry at all — "TMUX=" (empty value) still counts as present, since the
// key's presence is what signals "running inside a tmux client session,"
// not the specific value.
func hasTMUXEnv(environ []string) bool {
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if ok && key == "TMUX" {
			return true
		}
	}
	return false
}

// RunCommand is the production run func for UserPresent: it execs name with
// args and returns stdout only (Output(), not CombinedOutput() — stderr
// noise from tmux must never be mistaken for the value on stdout).
func RunCommand(name string, args ...string) (string, error) {
	// context.Background(): this is a quick, bounded tmux query with no
	// ambient context to propagate from — UserPresent's own contract takes
	// no context either.
	//nolint:gosec // G204: only caller is UserPresent, which always passes literal "tmux" + fixed subcommand args
	out, err := exec.CommandContext(context.Background(), name, args...).Output()
	return string(out), err
}
