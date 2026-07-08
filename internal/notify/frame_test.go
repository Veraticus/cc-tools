package notify

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestFrame_EncodeDecode_RoundTrip(t *testing.T) {
	f := Frame{
		HookInput: HookInput{
			SessionID: "sess-1", TranscriptPath: "/tmp/t.jsonl", CWD: "/home/user/project",
			HookEventName: "Stop", LastAssistantMessage: "All done here.",
		},
		Workspace: "my-tmux-session",
		Environ:   []string{"TMUX=/tmp/tmux-1000/default,1234,0", "CLAUDE_CONFIG_DIR=/home/user/.claude"},
	}

	var buf bytes.Buffer
	if err := EncodeFrame(&buf, f); err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}

	got, err := DecodeFrame(&buf)
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}
	if !reflect.DeepEqual(got, f) {
		t.Errorf("round trip = %+v, want %+v", got, f)
	}
}

func TestFrame_Decode_IgnoresUnknownFields(t *testing.T) {
	raw := `{"hook_input":{"session_id":"sess-2","hook_event_name":"SessionEnd"},"workspace":"w","environ":["A=1"],"future_field":"ignored"}`

	got, err := DecodeFrame(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}
	want := Frame{
		HookInput: HookInput{SessionID: "sess-2", HookEventName: "SessionEnd"},
		Workspace: "w",
		Environ:   []string{"A=1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode() = %+v, want %+v", got, want)
	}
}

func TestFrame_Decode_MalformedJSON_Errors(t *testing.T) {
	_, err := DecodeFrame(strings.NewReader("not json{"))
	if err == nil {
		t.Fatal("DecodeFrame() error = nil, want error for malformed JSON")
	}
}
