package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// HookInput is the JSON payload Claude Code writes to a hook's stdin. Field
// meaning varies by HookEventName: NotificationType/Message apply to
// Notification events, LastAssistantMessage to Stop events, and
// AgentID/AgentType are set only inside a subagent/teammate context (empty
// at the top level).
type HookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	// NotificationType and Message apply to Notification events.
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	// LastAssistantMessage applies to Stop events.
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
	// AgentID and AgentType are set only inside a subagent/teammate context.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// ParseHookInput decodes exactly one JSON object from r into a HookInput.
// Unknown fields are ignored. Empty input or malformed JSON is an error
// rather than a zero-value HookInput: this is a hook, and garbage input must
// surface as a visible failure the caller logs, not silently decide as if
// nothing were wrong. When r contains more than one JSON value, only the
// first is decoded (json.Decoder.Decode reads a single value and leaves the
// rest in its buffer unread) — the first object wins.
func ParseHookInput(r io.Reader) (HookInput, error) {
	dec := json.NewDecoder(r)
	var in HookInput
	if err := dec.Decode(&in); err != nil {
		if errors.Is(err, io.EOF) {
			return HookInput{}, fmt.Errorf("parsing hook input: empty input")
		}
		return HookInput{}, fmt.Errorf("parsing hook input: %w", err)
	}

	// SessionID is used by the caller (Pipeline.Run) to compose a per-session
	// state directory, StateBase/SessionID, that gets os.RemoveAll'd whole on
	// SessionEnd. Reject anything that could steer that path (and its
	// RemoveAll) outside the intended per-session location, rather than let
	// a malformed or hostile session_id ever reach that composition.
	if !validSessionID(in.SessionID) {
		return HookInput{}, fmt.Errorf("parsing hook input: invalid session_id %q", in.SessionID)
	}

	return in, nil
}

// validSessionID reports whether id is safe to join under a state base
// directory. An empty id or "." would collapse filepath.Join(base, id) to
// base itself; a path separator or ".." could steer it outside base.
func validSessionID(id string) bool {
	return id != "" && id != "." &&
		!strings.ContainsRune(id, filepath.Separator) &&
		!strings.Contains(id, "..")
}
