package statusline

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentsNow is a fixed reference time for every agents-state test so
// TTL math never depends on the wall clock.
func agentsNow() time.Time { return time.Unix(1_787_200_000, 0) }

const agentsTestSession = "39fdc97d-5534-4e4b-af38-8e1ae9d77939"

func TestAgentsState_WriteReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	labels := []string{"O5", "O5", "sol ×XH"}
	if err := WriteAgentsState(dir, agentsTestSession, labels, nil, agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow())
	want := "2×O5 · sol ×XH"
	if got != want {
		t.Errorf("roundtrip = %q, want %q", got, want)
	}
}

func TestAgentsState_EmptyLabelsClearTheSwap(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAgentsState(dir, agentsTestSession, []string{"O5"}, nil, agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteAgentsState(dir, agentsTestSession, nil, nil, agentsNow()); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "" {
		t.Errorf("no running agents should read as empty, got %q", got)
	}
}

func TestReadAgentsDisplay_StaleFileIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAgentsState(dir, agentsTestSession, []string{"O5"}, nil, agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	later := agentsNow().Add(agentsStateTTL + time.Second)
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", later); got != "" {
		t.Errorf("stale state must be ignored, got %q", got)
	}
}

func TestReadAgentsDisplay_FutureTimestampIgnored(t *testing.T) {
	dir := t.TempDir()
	future := agentsNow().Add(agentsStateTTL + time.Minute)
	if err := WriteAgentsState(dir, agentsTestSession, []string{"O5"}, nil, future); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "" {
		t.Errorf("far-future timestamp must be ignored, got %q", got)
	}
}

func TestReadAgentsDisplay_MissingFile(t *testing.T) {
	if got := ReadAgentsDisplay(NewMockFileReader(), "/cache", agentsTestSession, "F5", agentsNow()); got != "" {
		t.Errorf("missing file → empty, got %q", got)
	}
}

func TestReadAgentsDisplay_GarbageFile(t *testing.T) {
	fr := NewMockFileReader()
	path, _ := agentsStatePath("/cache", agentsTestSession)
	fr.files[path] = []byte("{not json")
	if got := ReadAgentsDisplay(fr, "/cache", agentsTestSession, "F5", agentsNow()); got != "" {
		t.Errorf("garbage file → empty, got %q", got)
	}
}

func TestReadAgentsDisplay_InheritedModelFallsBackToSessionModel(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAgentsState(dir, agentsTestSession, []string{"", ""}, nil, agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "2×F5" {
		t.Errorf("inherited-model tasks should group under session model, got %q", got)
	}
}

func TestReadAgentsDisplay_GroupCapAndOverflow(t *testing.T) {
	dir := t.TempDir()
	labels := []string{"O5", "sol", "H4.5", "S5", "glm-4.6", "glm-4.6"}
	if err := WriteAgentsState(dir, agentsTestSession, labels, nil, agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow())
	want := "O5 · sol · H4.5 · +3"
	if got != want {
		t.Errorf("overflow rendering = %q, want %q", got, want)
	}
}

func TestReadAgentsDisplay_SanitizesLabels(t *testing.T) {
	fr := NewMockFileReader()
	path, _ := agentsStatePath("/cache", agentsTestSession)
	fr.files[path] = []byte(`{"updated":1787200000,"running":["O\u001b[31m5"]}`)
	got := ReadAgentsDisplay(fr, "/cache", agentsTestSession, "F5", agentsNow())
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("control bytes must be stripped, got %q", got)
	}
	if got != "O[31m5" {
		t.Errorf("sanitized label = %q, want %q", got, "O[31m5")
	}
}

func TestAgentsStatePath_RejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "../evil", "a/b", "id with space", strings.Repeat("a", 65)} {
		if _, ok := agentsStatePath("/cache", id); ok {
			t.Errorf("session id %q should be rejected", id)
		}
	}
	if _, ok := agentsStatePath("", agentsTestSession); ok {
		t.Error("empty cache dir should be rejected")
	}
}

func TestWriteAgentsState_RejectsBadSessionID(t *testing.T) {
	if err := WriteAgentsState(t.TempDir(), "../evil", []string{"O5"}, nil, agentsNow()); err == nil {
		t.Error("path-traversal session id must error")
	}
}

func TestAgentsStatePath_Shape(t *testing.T) {
	path, ok := agentsStatePath("/dev/shm", "abc-123")
	if !ok {
		t.Fatal("valid inputs rejected")
	}
	if want := filepath.Join("/dev/shm", "cc-tools-agents-abc-123.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// TestRender_AgentsDisplaySwapsModelChip exercises the swap at the
// Render level: with AgentsDisplay set, the left section shows the
// agents summary with the agent icon and no effort suffix; without
// it, the session model renders as before.
func TestRender_AgentsDisplaySwapsModelChip(t *testing.T) {
	deps := &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 200},
	}
	sl := CreateStatusline(deps)
	data := &CachedData{
		ModelDisplay: "F5",
		CurrentDir:   "/tmp",
		TermWidth:    200,
		Effort:       &EffortInput{Level: "high"},
	}

	plain := sl.Render(data)
	if !strings.Contains(plain, "F5 ×H") {
		t.Fatalf("baseline render should show session model with effort, got %q", stripAnsi(plain))
	}

	data.AgentsDisplay = "2×O5 · sol ×XH"
	swapped := sl.Render(data)
	if !strings.Contains(swapped, "2×O5 · sol ×XH") {
		t.Errorf("swap should render agents summary, got %q", stripAnsi(swapped))
	}
	if strings.Contains(swapped, "F5 ×H") {
		t.Errorf("swap should replace the session model text, got %q", stripAnsi(swapped))
	}
	if !strings.Contains(swapped, strings.TrimSpace(AgentIcon)) {
		t.Errorf("swap should use the agent icon, got %q", stripAnsi(swapped))
	}
}

func strPtr(s string) *string { return &s }

func TestReadAgentsDisplay_FocusedWinsOverAggregate(t *testing.T) {
	dir := t.TempDir()
	err := WriteAgentsState(
		dir, agentsTestSession, []string{"O5", "O5"}, strPtr("sol ×XH"), agentsNow())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "sol ×XH" {
		t.Errorf("focused agent should win over aggregate, got %q", got)
	}
}

func TestReadAgentsDisplay_FocusedInheritedModelShowsSessionModel(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAgentsState(dir, agentsTestSession, []string{"O5"}, strPtr(""), agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "F5" {
		t.Errorf("focused inherited-model agent should show session model, got %q", got)
	}
}

func TestReadAgentsDisplay_FocusedWithNoRunningAgents(t *testing.T) {
	// Viewing a completed agent's transcript: no running tasks, but the
	// focused label still labels the screen the user is on.
	dir := t.TempDir()
	if err := WriteAgentsState(dir, agentsTestSession, nil, strPtr("H4.5"), agentsNow()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadAgentsDisplay(&DefaultFileReader{}, dir, agentsTestSession, "F5", agentsNow()); got != "H4.5" {
		t.Errorf("focused completed agent should still label the chip, got %q", got)
	}
}

func TestReadAgentsDisplay_FocusedSanitized(t *testing.T) {
	fr := NewMockFileReader()
	path, _ := agentsStatePath("/cache", agentsTestSession)
	fr.files[path] = []byte(`{"updated":1787200000,"running":[],"focused":"sol\u001b[31m"}`)
	got := ReadAgentsDisplay(fr, "/cache", agentsTestSession, "F5", agentsNow())
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("focused label must be control-stripped, got %q", got)
	}
	if got != "sol[31m" {
		t.Errorf("sanitized focused label = %q, want %q", got, "sol[31m")
	}
}
