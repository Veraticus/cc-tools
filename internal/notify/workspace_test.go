package notify

import (
	"errors"
	"testing"
)

func TestWorkspaceName_ResolvesPaneSession(t *testing.T) {
	var gotArgs []string
	run := func(_ string, args ...string) (string, error) {
		gotArgs = args
		return "mercury:3\n", nil
	}
	got := WorkspaceName([]string{"TMUX_PANE=%42", "TMUX=/tmp/tmux-1000/default,1,0"}, run)
	if got != "mercury:3" {
		t.Errorf("WorkspaceName() = %q, want %q", got, "mercury:3")
	}
	// The pane must be targeted explicitly: an empty -t falls back to the
	// most recently used attached session — some other workspace entirely.
	// The window index rides along because auto-renamed window names can't
	// tell two claude windows in one session apart.
	want := []string{"display-message", "-pt", "%42", "#{session_name}:#{window_index}"}
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
