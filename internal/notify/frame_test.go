package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validTestPreparedEvent() PreparedEvent {
	return PreparedEvent{
		Version: 1, Harness: harnessPi, SessionID: "session-1", Kind: eventKindCompletion,
		SourceEvent: eventTurnComplete, CWD: "/work/project", CompletionID: "completion-1",
		User: "user", Assistant: "assistant",
	}
}

func frameWithRawScalar(t *testing.T, topLevel bool, field string, raw json.RawMessage) []byte {
	t.Helper()
	wire, err := json.Marshal(Frame{Event: validTestPreparedEvent(), Workspace: "earth:3", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if topLevel {
		fields[field] = raw
	} else {
		var eventFields map[string]json.RawMessage
		if err = json.Unmarshal(fields["event"], &eventFields); err != nil {
			t.Fatal(err)
		}
		eventFields[field] = raw
		fields["event"], err = json.Marshal(eventFields)
		if err != nil {
			t.Fatal(err)
		}
	}
	wire, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return append(wire, '\n')
}

func replaceFrameWireField(t *testing.T, frame Frame, old, replacement string) []byte {
	t.Helper()
	wire, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(wire), old, replacement, 1)
	if updated == string(wire) {
		t.Fatalf("frame wire does not contain %q: %s", old, wire)
	}
	return append([]byte(updated), '\n')
}

func TestFrameStrictRoundTrip(t *testing.T) {
	frame := Frame{Event: validTestPreparedEvent(), Workspace: "earth:3", DryRun: true}
	var wire bytes.Buffer
	if err := EncodeFrame(&wire, frame); err != nil {
		t.Fatal(err)
	}
	if wire.Len() > maximumFrameBytes || wire.Bytes()[wire.Len()-1] != '\n' {
		t.Fatalf("encoded frame length/terminator = %d/%q", wire.Len(), wire.Bytes()[wire.Len()-1:])
	}
	for _, forbidden := range []string{"hook_input", "transcript_path", "environ", "parent_pid", "SECRET"} {
		if strings.Contains(wire.String(), forbidden) {
			t.Fatalf("wire contains forbidden field %q: %s", forbidden, wire.String())
		}
	}
	got, err := DecodeFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, frame) {
		t.Errorf("round trip = %+v, want %+v", got, frame)
	}
}

func TestDecodeFrameReturnsAtNewlineWithoutWaitingForEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	wire, err := json.Marshal(Frame{Event: validTestPreparedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, decodeErr := DecodeFrame(reader)
		done <- decodeErr
	}()
	if _, err = writer.Write(append(wire, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case decodeErr := <-done:
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("DecodeFrame waited for EOF after complete line")
	}
}

func TestDecodeFrameEnforcesLineSizeIncludingNewline(t *testing.T) {
	base, err := json.Marshal(Frame{Event: validTestPreparedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	atLimit := append(append([]byte{}, base...), bytes.Repeat([]byte(" "), maximumFrameBytes-len(base)-1)...)
	atLimit = append(atLimit, '\n')
	if len(atLimit) != maximumFrameBytes {
		t.Fatal("bad test setup")
	}
	if _, decodeErr := DecodeFrame(bytes.NewReader(atLimit)); decodeErr != nil {
		t.Fatalf("exactly %d bytes rejected: %v", maximumFrameBytes, decodeErr)
	}
	overLimit := append(append([]byte{}, base...), bytes.Repeat([]byte(" "), maximumFrameBytes-len(base))...)
	overLimit = append(overLimit, '\n')
	if _, decodeErr := DecodeFrame(bytes.NewReader(overLimit)); decodeErr == nil {
		t.Fatalf("%d-byte request accepted", len(overLimit))
	}
}

func TestDecodeFrameRejectsUnfinishedAndInvalidUTF8Lines(t *testing.T) {
	valid, err := json.Marshal(Frame{Event: validTestPreparedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	invalidUTF8 := append([]byte{}, valid[:len(valid)-2]...)
	invalidUTF8 = append(invalidUTF8, 0xff, '"', '}', '\n')
	for name, raw := range map[string][]byte{
		"empty EOF":     nil,
		"unfinished":    valid,
		"malformed":     []byte("not json{\n"),
		"invalid UTF-8": invalidUTF8,
	} {
		t.Run(name, func(t *testing.T) {
			if _, decodeErr := DecodeFrame(bytes.NewReader(raw)); decodeErr == nil ||
				(len(raw) > 0 && strings.Contains(decodeErr.Error(), string(raw))) ||
				len(decodeErr.Error()) > maximumAckBytes {
				t.Fatalf("DecodeFrame() error = %v", decodeErr)
			}
		})
	}
}

func TestDecodeFrameRejectsUnknownDuplicateAliasExtraAndInvalidShapes(t *testing.T) {
	valid, err := json.Marshal(Frame{Event: validTestPreparedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	validText := string(valid)
	cases := map[string]string{
		"unknown top-level":      strings.TrimSuffix(validText, "}") + `,"future":true}`,
		"duplicate top-level":    strings.TrimSuffix(validText, "}") + `,"workspace":"again"}`,
		"case alias top-level":   strings.TrimSuffix(validText, "}") + `,"Workspace":"again"}`,
		"unknown event":          strings.Replace(validText, `"version":1`, `"version":1,"future":true`, 1),
		"duplicate event":        strings.Replace(validText, `"version":1`, `"version":1,"version":1`, 1),
		"case alias event":       strings.Replace(validText, `"version":1`, `"version":1,"Version":1`, 1),
		"extra JSON same line":   validText + validText,
		"array frame":            `[]`,
		"null frame":             `null`,
		"array event":            `{"event":[],"workspace":"","dry_run":false}`,
		"null event":             `{"event":null,"workspace":"","dry_run":false}`,
		"wrong workspace type":   strings.Replace(validText, `"workspace":""`, `"workspace":7`, 1),
		"wrong dry type":         strings.TrimSuffix(validText, "}") + `,"dry_run":"false"}`,
		"wrong event field type": strings.Replace(validText, `"version":1`, `"version":"1"`, 1),
		"invalid semantic event": strings.Replace(validText, `"version":1`, `"version":2`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, decodeErr := DecodeFrame(strings.NewReader(raw + "\n")); decodeErr == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
}

func TestDecodeFrameRejectsNullAndWrongTypedScalars(t *testing.T) {
	stringFields := []struct {
		name     string
		topLevel bool
	}{
		{name: "harness"},
		{name: "session_id"},
		{name: "kind"},
		{name: "source_event"},
		{name: "cwd"},
		{name: "completion_id"},
		{name: "user"},
		{name: "assistant"},
		{name: "notification_type"},
		{name: "message"},
		{name: "agent_id"},
		{name: "agent_type"},
		{name: "workspace", topLevel: true},
	}
	for _, field := range stringFields {
		for _, invalid := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "null", raw: json.RawMessage("null")},
			{name: "number", raw: json.RawMessage("7")},
		} {
			t.Run(field.name+" "+invalid.name, func(t *testing.T) {
				if _, err := DecodeFrame(bytes.NewReader(frameWithRawScalar(
					t, field.topLevel, field.name, invalid.raw,
				))); err == nil {
					t.Fatalf("accepted %s %s", field.name, invalid.name)
				}
			})
		}
	}

	for _, field := range []struct {
		name     string
		topLevel bool
	}{
		{name: "goal_active"},
		{name: "dry_run", topLevel: true},
	} {
		for _, invalid := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "null", raw: json.RawMessage("null")},
			{name: "string", raw: json.RawMessage(`"false"`)},
		} {
			t.Run(field.name+" "+invalid.name, func(t *testing.T) {
				if _, err := DecodeFrame(bytes.NewReader(frameWithRawScalar(
					t, field.topLevel, field.name, invalid.raw,
				))); err == nil {
					t.Fatalf("accepted %s %s", field.name, invalid.name)
				}
			})
		}
	}

	for _, invalid := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "null", raw: json.RawMessage("null")},
		{name: "string", raw: json.RawMessage(`"1"`)},
	} {
		t.Run("version "+invalid.name, func(t *testing.T) {
			if _, err := DecodeFrame(bytes.NewReader(frameWithRawScalar(
				t, false, "version", invalid.raw,
			))); err == nil {
				t.Fatalf("accepted version %s", invalid.name)
			}
		})
	}
}

func TestDecodeFrameRejectsUnpairedSurrogatesAndRetainsPairs(t *testing.T) {
	completion := Frame{Event: validTestPreparedEvent(), Workspace: "earth:3"}
	unpaired := []struct {
		name        string
		frame       Frame
		old         string
		replacement string
	}{
		{
			name: "session ID", frame: completion,
			old: `"session_id":"session-1"`, replacement: `"session_id":"\ud800"`,
		},
		{
			name: "completion ID", frame: completion,
			old: `"completion_id":"completion-1"`, replacement: `"completion_id":"\udfff"`,
		},
		{
			name: "agent ID", frame: completion,
			old: `"agent_id":""`, replacement: `"agent_id":"\ud800"`,
		},
		{
			name: "user text", frame: completion,
			old: `"user":"user"`, replacement: `"user":"\ud800"`,
		},
		{
			name: "assistant text", frame: completion,
			old: `"assistant":"assistant"`, replacement: `"assistant":"\udfff"`,
		},
		{
			name: "input message",
			frame: Frame{Event: PreparedEvent{
				Version: 1, Harness: harnessClaude, SessionID: "session-1", Kind: eventKindInput,
				SourceEvent: eventNotification, NotificationType: "permission_prompt", Message: "message",
			}},
			old: `"message":"message"`, replacement: `"message":"\ud800"`,
		},
	}
	for _, tt := range unpaired {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeFrame(bytes.NewReader(replaceFrameWireField(
				t, tt.frame, tt.old, tt.replacement,
			))); err == nil {
				t.Fatal("unpaired surrogate accepted")
			}
		})
	}

	wire := replaceFrameWireField(
		t, completion, `"completion_id":"completion-1"`, `"completion_id":"pair-\ud83d\ude80"`,
	)
	wire = []byte(strings.Replace(
		string(wire), `"assistant":"assistant"`, `"assistant":"text-\ud83d\ude80"`, 1,
	))
	decoded, err := DecodeFrame(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Event.CompletionID != "pair-🚀" || decoded.Event.Assistant != "text-🚀" {
		t.Fatalf("paired surrogates decoded as %+v", decoded.Event)
	}
}

func TestFrameWorkspaceValidationAndEncodeRefusesInvalidFrames(t *testing.T) {
	valid := Frame{Event: validTestPreparedEvent(), Workspace: strings.Repeat("w", maximumWorkspaceBytes)}
	var wire bytes.Buffer
	if err := EncodeFrame(&wire, valid); err != nil {
		t.Fatalf("valid workspace rejected: %v", err)
	}
	for _, workspace := range []string{strings.Repeat("w", maximumWorkspaceBytes+1), "bad\nworkspace", string([]byte{0xff})} {
		frame := valid
		frame.Workspace = workspace
		wire.Reset()
		if err := EncodeFrame(&wire, frame); err == nil || wire.Len() != 0 {
			t.Fatalf("EncodeFrame(%q) error/wire = %v/%q", workspace, err, wire.String())
		}
	}
	invalid := valid
	invalid.Event.Version = 2
	wire.Reset()
	if err := EncodeFrame(&wire, invalid); err == nil || wire.Len() != 0 {
		t.Fatalf("invalid event encoded: error=%v wire=%q", err, wire.String())
	}
}

func TestAckStrictRoundTripAndExactWire(t *testing.T) {
	for _, status := range []string{ackStatusAccepted, ackStatusDuplicate, ackStatusRejected} {
		t.Run(status, func(t *testing.T) {
			var wire bytes.Buffer
			ack := Ack{Version: 1, Status: status}
			if err := EncodeAck(&wire, ack); err != nil {
				t.Fatal(err)
			}
			want := `{"version":1,"status":"` + status + `"}` + "\n"
			if wire.String() != want || wire.Len() > maximumAckBytes {
				t.Fatalf("ack wire = %q, want %q", wire.String(), want)
			}
			got, err := DecodeAck(&wire)
			if err != nil || got != ack {
				t.Fatalf("DecodeAck() = %+v, %v", got, err)
			}
		})
	}
}

func TestDecodeAckAcceptsExactMaximumLineWithoutWaitingForEOF(t *testing.T) {
	base := []byte(`{"version":1,"status":"accepted"}`)
	line := append(append([]byte{}, base...), bytes.Repeat([]byte(" "), maximumAckBytes-len(base)-1)...)
	line = append(line, '\n')
	if len(line) != maximumAckBytes {
		t.Fatal("bad test setup")
	}
	ack, err := DecodeAck(bytes.NewReader(line))
	if err != nil || ack.Status != ackStatusAccepted {
		t.Fatalf("DecodeAck() = %+v, %v", ack, err)
	}
}

func TestAckRejectsAllAmbiguousResponses(t *testing.T) {
	cases := map[string]string{
		"unfinished":             `{"version":1,"status":"accepted"}`,
		"unknown status":         `{"version":1,"status":"future"}` + "\n",
		"unknown version":        `{"version":2,"status":"accepted"}` + "\n",
		"unknown field":          `{"version":1,"status":"accepted","x":1}` + "\n",
		"duplicate field":        `{"version":1,"version":1,"status":"accepted"}` + "\n",
		"case alias":             `{"version":1,"Version":1,"status":"accepted"}` + "\n",
		"extra JSON":             `{"version":1,"status":"accepted"}{"version":1,"status":"accepted"}` + "\n",
		"array":                  "[]\n",
		"null":                   "null\n",
		"wrong version type":     `{"version":"1","status":"accepted"}` + "\n",
		"null version":           `{"version":null,"status":"accepted"}` + "\n",
		"wrong status type":      `{"version":1,"status":7}` + "\n",
		"null status":            `{"version":1,"status":null}` + "\n",
		"unpaired status string": `{"version":1,"status":"\ud800"}` + "\n",
		"truncated":              `{"version":1,` + "\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAck(strings.NewReader(raw)); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	base := []byte(`{"version":1,"status":"accepted"}`)
	oversized := append(append([]byte{}, base...), bytes.Repeat(
		[]byte(" "), maximumAckBytes-len(base),
	)...)
	oversized = append(oversized, '\n')
	if len(oversized) != maximumAckBytes+1 {
		t.Fatal("bad oversized ack setup")
	}
	if _, err := DecodeAck(bytes.NewReader(oversized)); err == nil {
		t.Fatal("oversized valid ack accepted")
	}
}
