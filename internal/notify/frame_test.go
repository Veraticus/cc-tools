package notify

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestFrameEncodeDecodeRoundTripCarriesOnlyInvocationIdentityAndWorkspace(t *testing.T) {
	frame := Frame{
		HookInput: HookInput{
			SchemaVersion: 1, Harness: harnessClaude, SessionID: "session-1",
			CompletionID: "assistant-uuid", TranscriptPath: "/tmp/transcript.jsonl",
			CWD: "/work/project", HookEventName: eventStop,
			LastAssistantMessage: "finished",
		},
		Workspace: "earth:3",
		DryRun:    true,
	}
	var wire bytes.Buffer
	if err := EncodeFrame(&wire, frame); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "environ") || strings.Contains(wire.String(), "parent_pid") ||
		strings.Contains(wire.String(), "SECRET") {
		t.Fatalf("wire contains removed process context: %s", wire.String())
	}
	got, err := DecodeFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, frame) {
		t.Errorf("round trip = %+v, want %+v", got, frame)
	}
}

func TestFrameDecodeIgnoresLegacyEnvironmentParentAndUnknownFields(t *testing.T) {
	raw := `{"hook_input":{"session_id":"session-2","hook_event_name":"SessionEnd"},` +
		`"workspace":"w","environ":["SECRET=value"],"parent_pid":99,"future":"ignored"}`
	got, err := DecodeFrame(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := Frame{
		HookInput: HookInput{SessionID: "session-2", HookEventName: eventSessionEnd},
		Workspace: "w",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeFrame() = %+v, want %+v", got, want)
	}
}

func TestFrameDecodeMalformedJSONErrors(t *testing.T) {
	if _, err := DecodeFrame(strings.NewReader("not json{")); err == nil {
		t.Fatal("DecodeFrame() error = nil")
	}
}
