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
			"stop_hook_active": true,
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
			StopHookActive:       true,
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

	// SessionID composes a state directory (StateBase/SessionID) that gets
	// os.RemoveAll'd on SessionEnd. An empty, separator-carrying, or
	// traversal-carrying session_id must never reach that point: an empty
	// id collapses the directory to the state base itself (wiping every
	// session's state on the next SessionEnd), and a separator or ".."
	// could point the RemoveAll outside the intended per-session directory
	// entirely. These three forms must all error rather than decode.
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

	// "." carries no separator and no "..", but filepath.Join(StateBase, ".")
	// collapses to StateBase itself, so SessionEnd's reap would RemoveAll the
	// entire state base. It must be rejected explicitly.
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
