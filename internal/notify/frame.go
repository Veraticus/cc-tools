package notify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	maximumFrameBytes     = 64 * 1024
	maximumAckBytes       = 256
	maximumWorkspaceBytes = 256

	ackStatusAccepted  = "accepted"
	ackStatusDuplicate = "duplicate"
	ackStatusRejected  = "rejected"

	frameFieldHarness      = "harness"
	frameFieldSessionID    = "session_id"
	frameFieldKind         = "kind"
	frameFieldSourceEvent  = "source_event"
	frameFieldCompletionID = "completion_id"
	frameFieldUser         = "user"
	frameFieldAssistant    = "assistant"
)

// Frame is the strict one-line control-socket request. Event is already
// prepared and immutable; Workspace is the only invocation context the daemon
// cannot derive itself. DryRun is a one-way per-request safety override.
type Frame struct {
	Event     PreparedEvent `json:"event"`
	Workspace string        `json:"workspace"`
	DryRun    bool          `json:"dry_run,omitempty"`
}

// Ack is notifyd's complete response to one frame.
type Ack struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
}

// EncodeFrame validates and writes one newline-terminated request atomically
// from a bounded temporary buffer.
func EncodeFrame(writer io.Writer, frame Frame) error {
	if err := validateFrame(frame); err != nil {
		return err
	}
	wire, err := json.Marshal(frame)
	if err != nil {
		return invalidFrameError()
	}
	wire = append(wire, '\n')
	if len(wire) > maximumFrameBytes {
		return invalidFrameError()
	}
	return writeStrictLine(writer, wire, invalidFrameError)
}

// DecodeFrame reads exactly one newline-terminated request without waiting
// for EOF, then applies exact-key JSON and semantic validation.
func DecodeFrame(reader io.Reader) (Frame, error) {
	line, err := readStrictLine(reader, maximumFrameBytes, invalidFrameError)
	if err != nil {
		return Frame{}, err
	}
	if !utf8.Valid(line) || !strictObjectKeys(line, frameJSONShape()) ||
		!strictFrameScalars(line) {
		return Frame{}, invalidFrameError()
	}
	var frame Frame
	if json.Unmarshal(line, &frame) != nil || validateFrame(frame) != nil {
		return Frame{}, invalidFrameError()
	}
	return frame, nil
}

// EncodeAck writes exactly one bounded status object and newline.
func EncodeAck(writer io.Writer, ack Ack) error {
	if !validAck(ack) {
		return invalidAckError()
	}
	wire, err := json.Marshal(ack)
	if err != nil {
		return invalidAckError()
	}
	wire = append(wire, '\n')
	if len(wire) > maximumAckBytes {
		return invalidAckError()
	}
	return writeStrictLine(writer, wire, invalidAckError)
}

// DecodeAck reads and strictly validates one complete acknowledgement line.
func DecodeAck(reader io.Reader) (Ack, error) {
	line, err := readStrictLine(reader, maximumAckBytes, invalidAckError)
	if err != nil {
		return Ack{}, err
	}
	if !utf8.Valid(line) || !strictObjectKeys(line, map[string]jsonObjectShape{
		"version": nil,
		"status":  nil,
	}) || !strictAckScalars(line) {
		return Ack{}, invalidAckError()
	}
	var ack Ack
	if json.Unmarshal(line, &ack) != nil || !validAck(ack) {
		return Ack{}, invalidAckError()
	}
	return ack, nil
}

type jsonObjectShape map[string]jsonObjectShape

func frameJSONShape() jsonObjectShape {
	return jsonObjectShape{
		"event": {
			"version": nil, frameFieldHarness: nil, frameFieldSessionID: nil, frameFieldKind: nil,
			frameFieldSourceEvent: nil, "cwd": nil, frameFieldCompletionID: nil, frameFieldUser: nil,
			frameFieldAssistant: nil, "notification_type": nil, "message": nil,
			"agent_id": nil, "agent_type": nil, "goal_active": nil,
		},
		"workspace": nil,
		"dry_run":   nil,
	}
}

func strictFrameScalars(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !strictJSONString(fields["workspace"]) {
		return false
	}
	if dryRun, present := fields["dry_run"]; present && !strictJSONBool(dryRun) {
		return false
	}
	var eventFields map[string]json.RawMessage
	if json.Unmarshal(fields["event"], &eventFields) != nil ||
		!strictJSONInt(eventFields["version"]) || !strictJSONBool(eventFields["goal_active"]) {
		return false
	}
	for _, name := range []string{
		frameFieldHarness, frameFieldSessionID, frameFieldKind, frameFieldSourceEvent, "cwd",
		frameFieldCompletionID, frameFieldUser, frameFieldAssistant,
		"notification_type", "message", "agent_id", "agent_type",
	} {
		if !strictJSONString(eventFields[name]) {
			return false
		}
	}
	return true
}

func strictAckScalars(raw []byte) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil &&
		strictJSONInt(fields["version"]) && strictJSONString(fields["status"])
}

func strictJSONString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if !validPiJSONString(raw) {
		return false
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func strictJSONInt(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var value int
	return json.Unmarshal(raw, &value) == nil
}

func strictJSONBool(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false"))
}

// strictObjectKeys rejects unknown, duplicate, omitted, and case-aliased keys
// before encoding/json can apply its permissive case-insensitive matching.
func strictObjectKeys(raw []byte, shape jsonObjectShape) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !strictObjectOpening(decoder) {
		return false
	}
	seen := make(map[string]bool, len(shape))
	for decoder.More() {
		if !decodeStrictObjectEntry(decoder, shape, seen) {
			return false
		}
	}
	return strictObjectClosing(decoder) && strictObjectHasRequiredFields(shape, seen)
}

func strictObjectOpening(decoder *json.Decoder) bool {
	opening, err := decoder.Token()
	return err == nil && opening == json.Delim('{')
}

func decodeStrictObjectEntry(
	decoder *json.Decoder,
	shape jsonObjectShape,
	seen map[string]bool,
) bool {
	keyToken, tokenErr := decoder.Token()
	key, keyOK := keyToken.(string)
	nested, allowed := shape[key]
	if tokenErr != nil || !keyOK || !allowed || seen[key] {
		return false
	}
	seen[key] = true
	var value json.RawMessage
	if decoder.Decode(&value) != nil {
		return false
	}
	return nested == nil || strictObjectKeys(value, nested)
}

func strictObjectClosing(decoder *json.Decoder) bool {
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	var extra json.RawMessage
	return decoder.Decode(&extra) == io.EOF
}

func strictObjectHasRequiredFields(shape jsonObjectShape, seen map[string]bool) bool {
	for key := range shape {
		if key != "dry_run" && !seen[key] {
			return false
		}
	}
	return true
}

func readStrictLine(reader io.Reader, maximum int, invalid func() error) ([]byte, error) {
	limited := io.LimitReader(reader, int64(maximum+1))
	buffered := bufio.NewReaderSize(limited, maximum+1)
	line, err := buffered.ReadBytes('\n')
	if err != nil || len(line) > maximum || len(line) == 0 || line[len(line)-1] != '\n' {
		return nil, invalid()
	}
	return line[:len(line)-1], nil
}

func writeStrictLine(writer io.Writer, wire []byte, invalid func() error) error {
	written, err := writer.Write(wire)
	if err != nil || written != len(wire) {
		return invalid()
	}
	return nil
}

func validateFrame(frame Frame) error {
	if validatePreparedEvent(frame.Event) != nil ||
		!validPreparedMetadata(frame.Workspace, maximumWorkspaceBytes, true) {
		return invalidFrameError()
	}
	return nil
}

func validAck(ack Ack) bool {
	if ack.Version != preparedEventVersion {
		return false
	}
	switch ack.Status {
	case ackStatusAccepted, ackStatusDuplicate, ackStatusRejected:
		return true
	default:
		return false
	}
}

func invalidFrameError() error { return errors.New("notify: invalid frame") }
func invalidAckError() error   { return errors.New("notify: invalid ack") }
