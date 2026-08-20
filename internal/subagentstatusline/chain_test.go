package subagentstatusline

import (
	"strings"
	"testing"

	"github.com/Veraticus/cc-tools/internal/statusline"
)

func TestVisibleWidth_PlainASCII(t *testing.T) {
	if got := visibleWidth("hello"); got != 5 {
		t.Errorf("visibleWidth('hello') = %d, want 5", got)
	}
}

func TestVisibleWidth_WithCSIEscape(t *testing.T) {
	s := "\033[38;2;1;2;3mhello\033[0m"
	if got := visibleWidth(s); got != 5 {
		t.Errorf("visibleWidth with CSI = %d, want 5", got)
	}
}

func TestVisibleWidth_MultipleSequences(t *testing.T) {
	s := "\033[48;2;1;1;1ma\033[38;2;2;2;2mb\033[0m"
	if got := visibleWidth(s); got != 2 {
		t.Errorf("multi-CSI visibleWidth = %d, want 2", got)
	}
}

func TestVisibleWidth_PowerlineGlyph(t *testing.T) {
	// LeftChevron and LeftCurve are single-cell in any NerdFont.
	if got := visibleWidth(statusline.LeftChevron); got != 1 {
		t.Errorf("LeftChevron visibleWidth = %d, want 1", got)
	}
	if got := visibleWidth(statusline.LeftCurve); got != 1 {
		t.Errorf("LeftCurve visibleWidth = %d, want 1", got)
	}
}

func TestVisibleWidth_BlockChars(t *testing.T) {
	if got := visibleWidth("▰▰▱▱"); got != 4 {
		t.Errorf("block-char visibleWidth = %d, want 4", got)
	}
}

func TestAssembleChain_Empty(t *testing.T) {
	if got := assembleChain(nil); got != "" {
		t.Errorf("empty chain = %q, want ''", got)
	}
	if got := assembleChain([]Chip{}); got != "" {
		t.Errorf("empty slice chain = %q, want ''", got)
	}
}

func TestAssembleChain_SingleChip(t *testing.T) {
	chips := []Chip{{Color: ColorLavender, Body: " ~/x "}}
	got := assembleChain(chips)
	if !strings.Contains(got, statusline.LeftCurve) {
		t.Errorf("missing LeftCurve in %q", got)
	}
	if !strings.Contains(got, statusline.RightCurve) {
		t.Errorf("missing RightCurve in %q", got)
	}
	if strings.Contains(got, statusline.LeftChevron) {
		t.Errorf("single chip should have NO LeftChevron, got %q", got)
	}
	if !strings.Contains(got, "~/x") {
		t.Errorf("missing chip body in %q", got)
	}
	if !strings.HasSuffix(got, "\033[0m") {
		t.Errorf("chain should end with reset escape, got %q", got)
	}
}

func TestAssembleChain_TwoChips(t *testing.T) {
	chips := []Chip{
		{Color: ColorLavender, Body: " a "},
		{Color: ColorPink, Body: " b "},
	}
	got := assembleChain(chips)
	if strings.Count(got, statusline.LeftChevron) != 1 {
		t.Errorf("two chips should have exactly 1 LeftChevron, got %q", got)
	}
}

func TestAssembleChain_ThreeChips(t *testing.T) {
	chips := []Chip{
		{Color: ColorLavender, Body: " a "},
		{Color: ColorGreen, Body: " b "},
		{Color: ColorPink, Body: " c "},
	}
	got := assembleChain(chips)
	if strings.Count(got, statusline.LeftChevron) != 2 {
		t.Errorf("three chips should have exactly 2 LeftChevrons, got %q", got)
	}
}

func TestBuildContent_Minimal(t *testing.T) {
	// Use a temp dir with no .git and no env — only dir + context chips.
	cwd := t.TempDir()
	task := Task{ID: "t1", TokenCount: 100_000, CWD: cwd}
	got := BuildContent(task, 200, 1_000_000, EnvSnapshot{})

	// Directory chip body must appear (raw cwd, since not under fake home).
	if !strings.Contains(got, cwd) {
		t.Errorf("BuildContent missing cwd %q in output %q", cwd, got)
	}
	// Context bar appears.
	if !strings.Contains(got, "10%") {
		t.Errorf("BuildContent missing context %% in output %q", got)
	}
	// No branch chip (no .git).
	if strings.Contains(got, statusline.GitIcon) {
		t.Errorf("BuildContent should NOT have branch chip when no .git, got %q", got)
	}
}

func TestBuildContent_AllChips(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n")
	task := Task{ID: "t1", TokenCount: 500_000, CWD: cwd}
	snap := EnvSnapshot{
		AWSProfile:    "staging",
		GCloudProject: "my-project",
		K8sContext:    "my-cluster",
	}
	got := BuildContent(task, 200, 1_000_000, snap)

	for _, want := range []string{"main", "staging", "my-project", "my-cluster", "50%"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildContent missing %q in output:\n%q", want, got)
		}
	}
}

func TestBuildContent_WidthPressureDropsK8sFirst(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n")
	task := Task{ID: "t1", TokenCount: 500_000, CWD: cwd}
	snap := EnvSnapshot{
		AWSProfile:    "staging",
		GCloudProject: "my-project",
		K8sContext:    "my-cluster",
	}

	// 40 cells: tight — should drop k8s first.
	got := BuildContent(task, 40, 1_000_000, snap)
	if strings.Contains(got, "my-cluster") {
		t.Errorf("at 40 cols, k8s chip should be dropped; got %q", got)
	}
	// Directory must still be present.
	if !strings.Contains(got, statusline.LeftCurve) {
		t.Errorf("LeftCurve missing — directory chip should always be present, got %q", got)
	}
}

func TestBuildContent_WidthPressureKeepsOnlyDirectory(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n")
	task := Task{ID: "t1", TokenCount: 500_000, CWD: cwd}
	snap := EnvSnapshot{
		AWSProfile:    "staging",
		GCloudProject: "my-project",
		K8sContext:    "my-cluster",
	}

	// 15 cells is below what dir+context need; only dir survives.
	got := BuildContent(task, 15, 1_000_000, snap)
	if strings.Contains(got, "50%") {
		t.Errorf("at 15 cols, context should be dropped; got %q", got)
	}
	if strings.Contains(got, "main") {
		t.Errorf("at 15 cols, branch should be dropped; got %q", got)
	}
	if strings.Contains(got, "staging") {
		t.Errorf("at 15 cols, AWS should be dropped; got %q", got)
	}
	// Directory chip's curve markers must still be present.
	if !strings.Contains(got, statusline.LeftCurve) {
		t.Errorf("LeftCurve missing — directory should remain, got %q", got)
	}
	if !strings.Contains(got, statusline.RightCurve) {
		t.Errorf("RightCurve missing — chain should still close, got %q", got)
	}
}

func TestBuildContent_DescriptionAdjacentToName_WideTerminal(t *testing.T) {
	// At wide widths, both name AND description chips render and
	// description appears immediately after name (before dir).
	cwd := t.TempDir()
	name := "auditor"
	task := Task{
		ID:          "t1",
		Name:        &name,
		Type:        "local_agent",
		Status:      "running",
		Description: "Reviewing config",
		CWD:         cwd,
	}
	got := BuildContent(task, 200, 1_000_000, EnvSnapshot{})
	idxName := strings.Index(got, "auditor")
	idxDesc := strings.Index(got, "Reviewing config")
	idxDir := strings.Index(got, cwd) // dir chip contains the CWD path
	if idxName < 0 || idxDesc < 0 || idxDir < 0 {
		t.Fatalf("expected all three chips in output, got %q", got)
	}
	if idxName >= idxDesc || idxDesc >= idxDir {
		t.Errorf("display order should be name → description → dir; got name@%d desc@%d dir@%d",
			idxName, idxDesc, idxDir)
	}
}

func TestBuildContent_DescriptionDropsBeforeDir(t *testing.T) {
	// Under tight width pressure, the description chip drops while
	// the dir chip is preserved.
	cwd := t.TempDir()
	name := "auditor"
	task := Task{
		ID:          "t1",
		Name:        &name,
		Type:        "local_agent",
		Status:      "running",
		Description: "A reasonably long description that costs cells",
		CWD:         cwd,
	}
	got := BuildContent(task, 25, 1_000_000, EnvSnapshot{})
	if strings.Contains(got, "reasonably long") {
		t.Errorf("description should drop under tight width; got %q", got)
	}
	if !strings.Contains(got, "auditor") {
		t.Errorf("agent name should survive width pressure; got %q", got)
	}
	if !strings.Contains(got, statusline.RightCurve) {
		t.Errorf("chain should still close with RightCurve; got %q", got)
	}
}

func TestBuildContent_AgentNameSurvivesPressure(t *testing.T) {
	// Under extreme width pressure, agent name AND directory should
	// both remain. Earlier behavior kept only directory; the chain
	// must now also preserve the agent identity chip.
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n")
	name := "auditor"
	task := Task{ID: "t1", Name: &name, Type: "local_agent", Status: "running", CWD: cwd}
	snap := EnvSnapshot{AWSProfile: "staging", GCloudProject: "my-project", K8sContext: "my-cluster"}

	got := BuildContent(task, 15, 1_000_000, snap)
	if !strings.Contains(got, "auditor") {
		t.Errorf("agent name 'auditor' must survive width pressure, got %q", got)
	}
}

func TestBuildContent_AgentNameUsesTypeLabelFallback(t *testing.T) {
	cwd := t.TempDir()
	task := Task{ID: "t1", Type: "local_workflow", Status: "running", CWD: cwd}
	got := BuildContent(task, 200, 1_000_000, EnvSnapshot{})
	if !strings.Contains(got, "workflow") {
		t.Errorf("nil Name should fall back to type label 'workflow', got %q", got)
	}
}

func TestBuildContent_ContrastingChevronForMatchingColors(t *testing.T) {
	// When context (low usage → green) and env (AWS dev → could be
	// configured to green) are adjacent with the same bg color, the
	// chevron between them must still render visibly. Verify by
	// composing a chain with two same-color chips and asserting
	// BaseFG appears as the chevron foreground.
	chips := []Chip{
		{Color: ColorGreen, Body: " a "},
		{Color: ColorGreen, Body: " b "},
	}
	got := assembleChain(chips)
	if !strings.Contains(got, palette.BaseFG()) {
		t.Errorf("same-color adjacent chips should use BaseFG for chevron, got %q", got)
	}
}

func TestBuildContent_DirectoryNeverDropped(t *testing.T) {
	cwd := t.TempDir()
	task := Task{ID: "t1", CWD: cwd}
	// Even at columns=1, we still emit the directory chip (it'll overflow,
	// but Claude renders it).
	got := BuildContent(task, 1, 1_000_000, EnvSnapshot{})
	if got == "" {
		t.Errorf("BuildContent should never return empty when CWD is set, got empty")
	}
	if !strings.Contains(got, statusline.LeftCurve) {
		t.Errorf("LeftCurve missing in narrow-columns output: %q", got)
	}
}

func TestBuildContent_ZeroContextWindow(t *testing.T) {
	cwd := t.TempDir()
	task := Task{ID: "t1", TokenCount: 500_000, CWD: cwd}
	// 0 contextWindow should default to 1_000_000, giving 50%.
	got := BuildContent(task, 200, 0, EnvSnapshot{})
	if !strings.Contains(got, "50%") {
		t.Errorf("BuildContent with contextWindow=0 should default to 1M, expected 50%%, got %q", got)
	}
}

func TestBuildContent_ModelChipRendered(t *testing.T) {
	task := Task{
		ID:     "t1",
		Type:   "local_agent",
		Status: "running",
		Model:  "chatgpt/sol",
		CWD:    "/tmp",
	}
	out := BuildContent(task, 200, DefaultContextWindow, EnvSnapshot{})
	if !strings.Contains(out, "sol") {
		t.Errorf("chain should contain model short name 'sol', got %q", out)
	}
}

func TestBuildContent_ModelChipSurvivesModeratePressure(t *testing.T) {
	// At 80 columns with all env chips present, the right-side chips
	// drop first; the model chip (index 1) must still be there.
	task := Task{
		ID:          "t1",
		Type:        "local_agent",
		Status:      "running",
		Description: "Doing something fairly long here",
		Model:       "claude-fable-5[1m]",
		Effort:      "high",
		CWD:         "/tmp",
	}
	snap := EnvSnapshot{AWSProfile: "dev-profile", GCloudProject: "my-project", K8sContext: "cluster"}
	out := BuildContent(task, 80, DefaultContextWindow, snap)
	if !strings.Contains(out, "F5 ×H") {
		t.Errorf("model chip should survive moderate pressure, got %q", out)
	}
}

func TestBuildContent_TaskWindowOverridesInvocationWindow(t *testing.T) {
	// 186k tokens: 50% of the task's 372k window, but only 19% of the
	// invocation-level 1M fallback. The task's own window must win.
	task := Task{
		ID:                "t1",
		Type:              "local_agent",
		Status:            "running",
		Model:             "chatgpt/sol",
		ContextWindowSize: 372_000,
		TokenCount:        186_000,
		CWD:               "/tmp",
	}
	out := BuildContent(task, 200, DefaultContextWindow, EnvSnapshot{})
	if !strings.Contains(out, "50%") {
		t.Errorf("context bar should use the task's 372k window (50%%), got %q", out)
	}
}

func TestBuildContent_ZeroTaskWindowFallsBack(t *testing.T) {
	task := Task{
		ID:         "t1",
		Type:       "local_agent",
		Status:     "running",
		TokenCount: 500_000,
		CWD:        "/tmp",
	}
	out := BuildContent(task, 200, DefaultContextWindow, EnvSnapshot{})
	if !strings.Contains(out, "50%") {
		t.Errorf("no task window → invocation window (50%% of 1M), got %q", out)
	}
}
