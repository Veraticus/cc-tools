package notify

import (
	"encoding/json"
	"fmt"
	"io"
)

// Frame is the payload cc-tools notify sends to notifyd over the control
// socket: the parsed hook input plus the process context the daemon cannot
// recover from its own (long-lived, single) environment — the hook
// invocation's tmux workspace (WorkspaceName depends on the calling
// process's TMUX_PANE) and its full environ (UserPresent's TMUX lookup and
// broadcast.go's jobsDir both key off entries — e.g. CLAUDE_CONFIG_DIR —
// that are per-hook-context, not daemon-wide).
type Frame struct {
	HookInput HookInput `json:"hook_input"`
	Workspace string    `json:"workspace"`
	Environ   []string  `json:"environ"`
	// ParentPID is the claude process that invoked the hook (os.Getppid() at
	// the client), forwarded so an armed watchdog's dead-session probe works
	// from the daemon, which has no parent-process relationship of its own
	// to the session.
	ParentPID int `json:"parent_pid"`
}

// EncodeFrame writes f to w as a single JSON object. The client sends
// exactly one frame per connection and then closes it — connection close is
// what signals frame end to DecodeFrame, so no length prefix or delimiter
// is needed.
func EncodeFrame(w io.Writer, f Frame) error {
	if err := json.NewEncoder(w).Encode(f); err != nil {
		return fmt.Errorf("notify: encoding frame: %w", err)
	}
	return nil
}

// DecodeFrame reads exactly one JSON object from r into a Frame. Unknown
// fields are ignored, matching ParseHookInput's contract for the nested
// HookInput.
func DecodeFrame(r io.Reader) (Frame, error) {
	var f Frame
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return Frame{}, fmt.Errorf("notify: decoding frame: %w", err)
	}
	return f, nil
}
