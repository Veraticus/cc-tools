package subagentstatusline

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParse_ValidSingleTask(t *testing.T) {
	raw := `{
		"columns": 80,
		"tasks": [
			{
				"id": "t1",
				"name": "explorer",
				"type": "local_agent",
				"status": "running",
				"description": "Looking for foo",
				"label": "Looking for foo",
				"tokenCount": 1234,
				"cwd": "/home/me/repo"
			}
		]
	}`

	got, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: unexpected error %v", err)
	}
	if got.Columns != 80 {
		t.Errorf("Columns = %d, want 80", got.Columns)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("Tasks len = %d, want 1", len(got.Tasks))
	}
	tk := got.Tasks[0]
	if tk.ID != "t1" {
		t.Errorf("ID = %q, want %q", tk.ID, "t1")
	}
	if tk.Name == nil || *tk.Name != "explorer" {
		t.Errorf("Name = %v, want pointer to \"explorer\"", tk.Name)
	}
	if tk.Type != "local_agent" {
		t.Errorf("Type = %q, want local_agent", tk.Type)
	}
	if tk.TokenCount != 1234 {
		t.Errorf("TokenCount = %d, want 1234", tk.TokenCount)
	}
	if tk.CWD != "/home/me/repo" {
		t.Errorf("CWD = %q, want /home/me/repo", tk.CWD)
	}
}

func TestParse_ValidMultipleTasks(t *testing.T) {
	raw := `{
		"columns": 200,
		"tasks": [
			{"id": "t1", "name": null, "type": "local_agent", "tokenCount": 100, "cwd": "/a"},
			{"id": "t2", "name": "named", "type": "local_bash", "tokenCount": 200, "cwd": "/b"},
			{"id": "t3", "name": null, "type": "monitor_mcp", "tokenCount": 300, "cwd": "/c"}
		]
	}`
	got, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Tasks) != 3 {
		t.Fatalf("Tasks len = %d, want 3", len(got.Tasks))
	}
	if got.Tasks[1].Name == nil || *got.Tasks[1].Name != "named" {
		t.Errorf("Tasks[1].Name = %v, want pointer to \"named\"", got.Tasks[1].Name)
	}
}

func TestParse_EmptyTasksArray(t *testing.T) {
	got, err := Parse(strings.NewReader(`{"columns": 80, "tasks": []}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tasks == nil {
		t.Errorf("Tasks is nil; want non-nil empty slice")
	}
	if len(got.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(got.Tasks))
	}
}

func TestParse_MissingTasksKey(t *testing.T) {
	got, err := Parse(strings.NewReader(`{"columns": 80}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tasks == nil {
		t.Errorf("Tasks is nil; Parse must normalize to non-nil empty slice")
	}
	if len(got.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(got.Tasks))
	}
}

func TestParse_NullName(t *testing.T) {
	raw := `{"tasks": [{"id": "t1", "name": null}]}`
	got, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tasks[0].Name != nil {
		t.Errorf("Name = %v, want nil for null JSON value", got.Tasks[0].Name)
	}
}

func TestParse_NonNullName(t *testing.T) {
	raw := `{"tasks": [{"id": "t1", "name": "my-agent"}]}`
	got, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tasks[0].Name == nil {
		t.Fatalf("Name is nil; want pointer")
	}
	if *got.Tasks[0].Name != "my-agent" {
		t.Errorf("*Name = %q, want my-agent", *got.Tasks[0].Name)
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	_, err := Parse(strings.NewReader(`{not json`))
	if err == nil {
		t.Fatalf("Parse: want error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q should be wrapped with 'parse:'", err)
	}
}

func TestParse_EmptyReader(t *testing.T) {
	_, err := Parse(strings.NewReader(""))
	if err == nil {
		t.Fatalf("Parse: want error for empty input, got nil")
	}
}

func TestParse_UnknownFieldsIgnored(t *testing.T) {
	raw := `{
		"columns": 80,
		"session_id": "abc-123",
		"agent_id": "agent-1",
		"effort": {"level": "high"},
		"tasks": [
			{
				"id": "t1",
				"tokenCount": 100,
				"tokenSamples": [1, 2, 3, 4, 5],
				"startTime": 1684567890,
				"cwd": "/x"
			}
		]
	}`
	got, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "t1" {
		t.Errorf("Parse did not survive unknown fields: %+v", got)
	}
}

// failingReader returns an error after returning some bytes — confirms
// Parse wraps io errors distinctly from JSON-format errors.
type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, errors.New("disk on fire")
}

func TestParse_ReadError(t *testing.T) {
	_, err := Parse(failingReader{})
	if err == nil {
		t.Fatalf("Parse: want error from failing reader, got nil")
	}
	if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("error %q should wrap reader error", err)
	}
}

func TestParse_ModelEffortAndWindow(t *testing.T) {
	input := `{"columns": 120, "tasks": [{
		"id": "t1", "type": "local_agent", "status": "running",
		"model": "chatgpt/sol", "effort": "low", "contextWindowSize": 372000
	}]}`
	in, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	task := in.Tasks[0]
	if task.Model != "chatgpt/sol" {
		t.Errorf("Model = %q, want %q", task.Model, "chatgpt/sol")
	}
	if task.Effort != "low" {
		t.Errorf("Effort = %q, want %q", task.Effort, "low")
	}
	if task.ContextWindowSize != 372000 {
		t.Errorf("ContextWindowSize = %d, want 372000", task.ContextWindowSize)
	}
}

func TestParse_EffortObjectShape(t *testing.T) {
	input := `{"tasks": [{"id": "t1", "effort": {"level": "xhigh"}}]}`
	in, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Tasks[0].Effort != "xhigh" {
		t.Errorf("Effort = %q, want %q", in.Tasks[0].Effort, "xhigh")
	}
}

func TestParse_EffortGarbageToleratedAsEmpty(t *testing.T) {
	input := `{"tasks": [{"id": "t1", "model": "sonnet", "effort": 42}]}`
	in, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("malformed effort must not fail the payload: %v", err)
	}
	if in.Tasks[0].Effort != "" {
		t.Errorf("Effort = %q, want empty", in.Tasks[0].Effort)
	}
	if in.Tasks[0].Model != "sonnet" {
		t.Errorf("Model = %q, want %q (rest of task must survive)", in.Tasks[0].Model, "sonnet")
	}
}
