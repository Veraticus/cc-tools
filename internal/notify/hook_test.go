package notify

import (
	"strings"
	"testing"
)

func TestParseHookInput(t *testing.T) {
	t.Run("valid full object", func(t *testing.T) {
		in, err := ParseHookInput(strings.NewReader(`{
			"session_id": "sess-1",
			"transcript_path": "/tmp/sess-1.jsonl",
			"cwd": "/home/josh/proj",
			"hook_event_name": "Stop",
			"notification_type": "idle_prompt",
			"message": "hello",
			"last_assistant_message": "all done",
			"agent_id": "agent-1",
			"agent_type": "worker"
		}`))
		if err != nil {
			t.Fatalf("ParseHookInput: unexpected error: %v", err)
		}
		want := HookInput{
			SessionID:            "sess-1",
			TranscriptPath:       "/tmp/sess-1.jsonl",
			CWD:                  "/home/josh/proj",
			HookEventName:        "Stop",
			NotificationType:     "idle_prompt",
			Message:              "hello",
			LastAssistantMessage: "all done",
			AgentID:              "agent-1",
			AgentType:            "worker",
		}
		if in != want {
			t.Errorf("ParseHookInput mismatch\ngot:  %+v\nwant: %+v", in, want)
		}
	})

	t.Run("unknown fields ignored", func(t *testing.T) {
		in, err := ParseHookInput(strings.NewReader(`{
			"session_id": "sess-2",
			"hook_event_name": "SessionEnd",
			"some_future_field": {"nested": true},
			"another_one": [1, 2, 3]
		}`))
		if err != nil {
			t.Fatalf("ParseHookInput: unexpected error: %v", err)
		}
		want := HookInput{SessionID: "sess-2", HookEventName: "SessionEnd"}
		if in != want {
			t.Errorf("ParseHookInput mismatch\ngot:  %+v\nwant: %+v", in, want)
		}
	})

	t.Run("Codex agent turn complete normalizes", func(t *testing.T) {
		in, err := ParseHookInput(strings.NewReader(`{
			"type": "agent-turn-complete",
			"thread-id": "019c-thread-1",
			"turn-id": "turn-7",
			"cwd": "/home/josh/proj",
			"input-messages": ["Please fix it", "and run tests"],
			"last-assistant-message": "Fixed it and all tests pass."
		}`))
		if err != nil {
			t.Fatalf("ParseHookInput: unexpected error: %v", err)
		}
		want := HookInput{
			SessionID:            "019c-thread-1",
			CWD:                  "/home/josh/proj",
			HookEventName:        eventTurnComplete,
			NotificationType:     "agent-turn-complete",
			Message:              "Please fix it\nand run tests",
			LastAssistantMessage: "Fixed it and all tests pass.",
		}
		if in != want {
			t.Errorf("ParseHookInput mismatch\ngot:  %+v\nwant: %+v", in, want)
		}
	})

	t.Run("unsupported provider event errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"type":"future-event","thread-id":"thread-1"}`))
		if err == nil || !strings.Contains(err.Error(), "unsupported notification type") {
			t.Fatalf("ParseHookInput error = %v, want unsupported notification type", err)
		}
	})

	t.Run("empty input errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(""))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for empty input, got nil")
		}
	})

	t.Run("malformed JSON errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": "sess-3",`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for malformed JSON, got nil")
		}
	})

	// SessionID keys the daemon's in-memory dedupe state and is written
	// verbatim into every decision-log record, and validSessionID also
	// guards a future filesystem-path use. An empty, separator-carrying, or
	// traversal-carrying session_id must never reach any of that: these
	// three forms must all error rather than decode.
	t.Run("empty session_id errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": "", "hook_event_name": "Stop"}`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for empty session_id, got nil")
		}
	})

	t.Run("session_id with path separator errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": "sess/../evil", "hook_event_name": "Stop"}`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for session_id with path separator, got nil")
		}
	})

	t.Run("session_id with .. errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": "sess..evil", "hook_event_name": "Stop"}`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for session_id containing .., got nil")
		}
	})

	// "." carries no separator and no "..", but filepath.Join(base, ".")
	// collapses to base itself, so a future path use keyed on session_id
	// could collide across sessions. It must be rejected explicitly.
	t.Run("session_id of dot errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": ".", "hook_event_name": "Stop"}`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for session_id \".\", got nil")
		}
	})

	t.Run("session_id with trailing slash errors", func(t *testing.T) {
		_, err := ParseHookInput(strings.NewReader(`{"session_id": "abc/", "hook_event_name": "Stop"}`))
		if err == nil {
			t.Fatal("ParseHookInput: expected error for session_id with trailing slash, got nil")
		}
	})

	// Two top-level objects in the stream: json.Decoder.Decode reads exactly
	// one object and leaves the rest in the buffer unread, so the first
	// object wins and the second is simply ignored — that is the chosen,
	// documented behavior, not an error case.
	t.Run("two objects input: first object wins", func(t *testing.T) {
		in, err := ParseHookInput(strings.NewReader(
			`{"session_id": "first", "hook_event_name": "Stop"}` +
				`{"session_id": "second", "hook_event_name": "SessionEnd"}`))
		if err != nil {
			t.Fatalf("ParseHookInput: unexpected error: %v", err)
		}
		want := HookInput{SessionID: "first", HookEventName: "Stop"}
		if in != want {
			t.Errorf("ParseHookInput mismatch\ngot:  %+v\nwant: %+v", in, want)
		}
	})
}
