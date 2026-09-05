package notify

import (
	"encoding/json"
	"fmt"
	"io"
)

// Frame is the minimal control-socket payload. HookInput contains the native
// source/session/completion identity and Workspace is the only invocation
// context the long-running daemon cannot derive for notification routing.
// The caller's environment and parent process are intentionally never sent.
type Frame struct {
	HookInput HookInput `json:"hook_input"`
	Workspace string    `json:"workspace"`
	// DryRun is a one-way safety override: true rehearses this frame even
	// when the daemon itself is in delivery mode.
	DryRun bool `json:"dry_run,omitempty"`
}

// EncodeFrame writes one JSON control-socket frame.
func EncodeFrame(writer io.Writer, frame Frame) error {
	if err := json.NewEncoder(writer).Encode(frame); err != nil {
		return fmt.Errorf("notify: encoding frame: %w", err)
	}
	return nil
}

// DecodeFrame reads one JSON control-socket frame and ignores unknown fields.
func DecodeFrame(reader io.Reader) (Frame, error) {
	var frame Frame
	if err := json.NewDecoder(reader).Decode(&frame); err != nil {
		return Frame{}, fmt.Errorf("notify: decoding frame: %w", err)
	}
	return frame, nil
}
