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
