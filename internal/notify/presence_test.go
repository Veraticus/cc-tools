package notify

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestUserPresent_NoTmuxEnvIsAbsent(t *testing.T) {
	environ := []string{"HOME=/home/josh", "PATH=/usr/bin"}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	run := func(_ string, _ ...string) (string, error) {
		t.Fatal("run should never be called when TMUX is absent")
		return "", nil
	}

	if got := UserPresent(environ, now, run); got {
		t.Errorf("UserPresent() = true, want false (no TMUX in environ)")
	}
}

func TestUserPresent_ActiveAttachedRecentActivity(t *testing.T) {
	environ := []string{"TMUX=/tmp/tmux-1000/default,1234,0"}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	run := func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "display-message" {
			for _, a := range args {
				if a == "#{window_active},#{session_attached}" {
					return "1,1\n", nil
				}
				if a == "#{client_activity}" {
					return strconv.FormatInt(now.Add(-10*time.Second).Unix(), 10) + "\n", nil
				}
			}
		}
		return "", errors.New("unexpected args")
	}

	if got := UserPresent(environ, now, run); !got {
		t.Errorf("UserPresent() = false, want true (active+attached, recent activity)")
	}
}

func TestUserPresent_NotActiveIsAbsent(t *testing.T) {
	environ := []string{"TMUX=/tmp/tmux-1000/default,1234,0"}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	run := func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "display-message" {
			for _, a := range args {
				if a == "#{window_active},#{session_attached}" {
					return "0,1\n", nil
				}
				if a == "#{client_activity}" {
					return strconv.FormatInt(now.Unix(), 10) + "\n", nil
				}
			}
		}
		return "", errors.New("unexpected args")
	}

	if got := UserPresent(environ, now, run); got {
		t.Errorf("UserPresent() = true, want false (window not active)")
	}
}

func TestUserPresent_StaleActivityIsAbsent(t *testing.T) {
	environ := []string{"TMUX=/tmp/tmux-1000/default,1234,0"}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	run := func(_ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "display-message" {
			for _, a := range args {
				if a == "#{window_active},#{session_attached}" {
					return "1,1\n", nil
				}
				if a == "#{client_activity}" {
					return strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10) + "\n", nil
				}
			}
		}
		return "", errors.New("unexpected args")
	}

	if got := UserPresent(environ, now, run); got {
		t.Errorf("UserPresent() = true, want false (client_activity stale)")
	}
}

func TestUserPresent_RunErrorIsAbsent(t *testing.T) {
	environ := []string{"TMUX=/tmp/tmux-1000/default,1234,0"}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	run := func(_ string, _ ...string) (string, error) {
		return "", errors.New("tmux: no server running")
	}

	if got := UserPresent(environ, now, run); got {
		t.Errorf("UserPresent() = true, want false (run error)")
	}
}

func TestWorkspaceName_ResolvesPaneSession(t *testing.T) {
	var gotArgs []string
	run := func(_ string, args ...string) (string, error) {
		gotArgs = args
		return "mercury\n", nil
	}
	got := WorkspaceName([]string{"TMUX_PANE=%42", "TMUX=/tmp/tmux-1000/default,1,0"}, run)
	if got != "mercury" {
		t.Errorf("WorkspaceName() = %q, want %q", got, "mercury")
	}
	// The pane must be targeted explicitly: an empty -t falls back to the
	// most recently used attached session — some other workspace entirely.
	want := []string{"display-message", "-pt", "%42", "#{session_name}"}
	if len(gotArgs) != len(want) {
		t.Fatalf("tmux args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("tmux args = %v, want %v", gotArgs, want)
		}
	}
}

func TestWorkspaceName_EmptyPaneOrErrorResolvesEmpty(t *testing.T) {
	ran := false
	run := func(_ string, _ ...string) (string, error) {
		ran = true
		return "earth", nil
	}
	// Background jobs inherit TMUX_PANE= (empty): no workspace, and tmux
	// must not be queried at all.
	if got := WorkspaceName([]string{"TMUX_PANE=", "TMUX="}, run); got != "" {
		t.Errorf("WorkspaceName(empty pane) = %q, want \"\"", got)
	}
	if got := WorkspaceName([]string{"HOME=/home/u"}, run); got != "" {
		t.Errorf("WorkspaceName(no tmux env) = %q, want \"\"", got)
	}
	if ran {
		t.Error("run was called for an empty TMUX_PANE, want no tmux query")
	}

	failing := func(_ string, _ ...string) (string, error) { return "", errors.New("no server") }
	if got := WorkspaceName([]string{"TMUX_PANE=%1"}, failing); got != "" {
		t.Errorf("WorkspaceName(tmux error) = %q, want \"\"", got)
	}
}
