package subagentstatusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Veraticus/cc-tools/internal/statusline"
)

func TestRender_HappyPath(t *testing.T) {
	in := `{
		"columns": 200,
		"tasks": [
			{"id": "t1", "tokenCount": 100000, "cwd": "/tmp"},
			{"id": "t2", "tokenCount": 500000, "cwd": "/tmp"}
		]
	}`
	var out bytes.Buffer
	if err := Render(strings.NewReader(in), &out, 1_000_000, mapEnvReader{}, ""); err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output has %d lines, want 2:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var d Decoration
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Errorf("line %d not parseable: %v (raw=%q)", i, err, line)
		}
		if d.Content == "" {
			t.Errorf("line %d Content is empty", i)
		}
	}
}

func TestRender_EmptyTasks(t *testing.T) {
	in := `{"columns": 200, "tasks": []}`
	var out bytes.Buffer
	if err := Render(strings.NewReader(in), &out, 1_000_000, mapEnvReader{}, ""); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("empty tasks should produce no output, got %q", out.String())
	}
}

func TestRender_MalformedInput(t *testing.T) {
	var out bytes.Buffer
	err := Render(strings.NewReader(`{not json`), &out, 1_000_000, mapEnvReader{}, "")
	if err == nil {
		t.Fatalf("Render: want parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q should mention parse", err)
	}
	if out.Len() != 0 {
		t.Errorf("malformed input should produce no output, got %q", out.String())
	}
}

func TestRender_ContentContainsLeftCurve(t *testing.T) {
	in := `{"columns": 200, "tasks": [{"id": "t1", "cwd": "/tmp"}]}`
	var out bytes.Buffer
	if err := Render(strings.NewReader(in), &out, 1_000_000, mapEnvReader{}, ""); err != nil {
		t.Fatalf("Render: %v", err)
	}
	var d Decoration
	if err := json.Unmarshal(bytes.TrimRight(out.Bytes(), "\n"), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(d.Content, statusline.LeftCurve) {
		t.Errorf("Content missing LeftCurve glyph: %q", d.Content)
	}
	if !strings.Contains(d.Content, statusline.RightCurve) {
		t.Errorf("Content missing RightCurve glyph: %q", d.Content)
	}
}

func TestRender_WritesAgentsState(t *testing.T) {
	dir := t.TempDir()
	in := `{"session_id":"abc-123","columns":120,"tasks":[
		{"id":"t1","type":"local_agent","status":"running","model":"chatgpt/sol","effort":"xhigh","cwd":"/tmp"},
		{"id":"t2","type":"local_agent","status":"completed","model":"claude-opus-5[1m]","cwd":"/tmp"},
		{"id":"t3","type":"local_agent","status":"running","cwd":"/tmp"}]}`
	var out bytes.Buffer
	if err := Render(strings.NewReader(in), &out, 1_000_000, mapEnvReader{}, dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cc-tools-agents-abc-123.json"))
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var st struct {
		Running []string `json:"running"`
	}
	if unmarshalErr := json.Unmarshal(raw, &st); unmarshalErr != nil {
		t.Fatalf("state file not JSON: %v", unmarshalErr)
	}
	// Running tasks only, in order: the sol rung with effort, then the
	// inherited-model task as an empty label. The completed O5 task
	// must not appear.
	want := []string{"sol ×XH", ""}
	if len(st.Running) != len(want) {
		t.Fatalf("running = %v, want %v", st.Running, want)
	}
	for i := range want {
		if st.Running[i] != want[i] {
			t.Errorf("running[%d] = %q, want %q", i, st.Running[i], want[i])
		}
	}
}

func TestRender_EmptyCacheDirSkipsState(t *testing.T) {
	in := `{"session_id":"abc-123","tasks":[{"id":"t1","type":"local_agent","status":"running","cwd":"/tmp"}]}`
	var out bytes.Buffer
	if err := Render(strings.NewReader(in), &out, 1_000_000, mapEnvReader{}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
