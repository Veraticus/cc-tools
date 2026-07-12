package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// eventNotification is the HookEventName of a Notification event, named
// because three call sites branch on it (Decide's event switch, the
// broadcast-facts gate, and the pipeline's lazy SinceLastNotifySame
// computation). Stop and SessionEnd appear at most twice each and stay
// inline literals in Decide's switch.
const eventNotification = "Notification"

// eventTurnComplete is the provider-neutral event emitted by adapters whose
// notification API reports only that an agent turn finished. Codex's
// agent-turn-complete notification maps here; unlike Claude's Stop hook it
// carries no transcript path to inspect, so Decide handles it without the
// Claude-specific transcript/judge pipeline.
const eventTurnComplete = "TurnComplete"

// HookInput is the notifier's canonical provider-neutral event. Claude's
// stdin hook shape already matches most fields; ParseHookInput adapts Codex's
// argv notification shape into the same type. Field meaning varies by
// HookEventName: NotificationType/Message apply to Notification events,
// LastAssistantMessage applies to Stop/TurnComplete, and AgentID/AgentType
// are set only inside a Claude subagent/teammate context.
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
	// AgentID and AgentType are set only inside a subagent/teammate context.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// codexNotifyInput is the JSON object Codex passes as the notify command's
// single argv value. Codex deliberately uses kebab-case field names here,
// unlike Claude's snake_case hook payload.
type codexNotifyInput struct {
	Type                 string   `json:"type"`
	ThreadID             string   `json:"thread-id"`
	TurnID               string   `json:"turn-id"`
	CWD                  string   `json:"cwd"`
	InputMessages        []string `json:"input-messages"`
	LastAssistantMessage string   `json:"last-assistant-message"`
}

// ParseHookInput decodes exactly one provider payload from r and normalizes
// it into HookInput. Claude hook objects use snake_case fields and lifecycle
// event names; Codex passes a kebab-case agent-turn-complete object. Unknown
// fields are ignored. Empty input or malformed JSON is an error rather than
// a zero-value HookInput: garbage input must surface as a visible failure the
// caller logs, not silently decide as if nothing were wrong. When r contains
// more than one JSON value, only the first is decoded — the first object wins.
func ParseHookInput(r io.Reader) (HookInput, error) {
	dec := json.NewDecoder(r)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return HookInput{}, fmt.Errorf("parsing hook input: empty input")
		}
		return HookInput{}, fmt.Errorf("parsing hook input: %w", err)
	}

	// Probe only the discriminator fields first. Keeping detection separate
	// from normalization makes future provider adapters additive rather than
	// forcing their wire fields into the canonical HookInput type.
	var probe struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		Type          string `json:"type"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&probe); err != nil {
		return HookInput{}, fmt.Errorf("parsing hook input: %w", err)
	}

	var in HookInput
	switch {
	case probe.HookEventName != "" || probe.SessionID != "":
		if err := json.Unmarshal(raw, &in); err != nil {
			return HookInput{}, fmt.Errorf("parsing hook input: %w", err)
		}
	case probe.Type == "agent-turn-complete":
		var codex codexNotifyInput
		if err := json.Unmarshal(raw, &codex); err != nil {
			return HookInput{}, fmt.Errorf("parsing hook input: %w", err)
		}
		in = HookInput{
			SessionID:            codex.ThreadID,
			CWD:                  codex.CWD,
			HookEventName:        eventTurnComplete,
			NotificationType:     codex.Type,
			Message:              strings.Join(codex.InputMessages, "\n"),
			LastAssistantMessage: codex.LastAssistantMessage,
		}
	default:
		return HookInput{}, fmt.Errorf("parsing hook input: unsupported notification type %q", probe.Type)
	}

	// SessionID keys the daemon's in-memory dedupe state (MemoryState.sessions)
	// and is written verbatim into every decision-log record. Reject anything
	// that could carry a path separator or traversal sequence, rather than
	// let a malformed or hostile session_id ever reach a future call site
	// that joins it onto a filesystem path.
	if !validSessionID(in.SessionID) {
		return HookInput{}, fmt.Errorf("parsing hook input: invalid session_id %q", in.SessionID)
	}

	return in, nil
}

// validSessionID reports whether id is safe to use as a map key and to join
// onto a filesystem path, should some future call site need to. An empty id
// or "." would collapse filepath.Join(base, id) to base itself; a path
// separator or ".." could steer it outside base.
func validSessionID(id string) bool {
	return id != "" && id != "." &&
		!strings.ContainsRune(id, filepath.Separator) &&
		!strings.Contains(id, "..")
}
