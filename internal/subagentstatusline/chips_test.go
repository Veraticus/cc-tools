package subagentstatusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mapEnvReader is a tiny EnvReader stub for tests that exercise the
// SnapshotEnv code path; chip-level tests now take string args
// directly.
type mapEnvReader map[string]string

func (m mapEnvReader) Get(key string) string { return m[key] }

// --- Agent name chip tests ---

func ptr(s string) *string { return &s }

func TestRenderAgentNameChip_UsesNameWhenSet(t *testing.T) {
	task := Task{Name: ptr("auditor"), Type: "local_agent", Status: "running"}
	chip := renderAgentNameChip(task)
	if !strings.Contains(chip.Body, "auditor") {
		t.Errorf("body should contain Name 'auditor', got %q", chip.Body)
	}
	if chip.Color != ColorPeach {
		t.Errorf("running → ColorPeach, got %v", chip.Color)
	}
}

func TestRenderAgentNameChip_FallsBackToTypeLabel(t *testing.T) {
	cases := []struct {
		taskType string
		want     string
	}{
		{"local_agent", "agent"},
		{"local_bash", "bash"},
		{"local_workflow", "workflow"},
		{"monitor_mcp", "mcp"},
		{"mcp_task", "mcp"},
		{"in_process_teammate", "teammate"},
		{"remote_agent", "teammate"},
		{"dream", "dream"},
		{"unknown_type", "task"},
	}
	for _, c := range cases {
		chip := renderAgentNameChip(Task{Type: c.taskType, Status: "running"})
		if !strings.Contains(chip.Body, c.want) {
			t.Errorf("Type=%q: body should contain %q, got %q", c.taskType, c.want, chip.Body)
		}
	}
}

func TestRenderAgentNameChip_EmptyNameFallsBackToTypeLabel(t *testing.T) {
	empty := ""
	chip := renderAgentNameChip(Task{Name: &empty, Type: "local_workflow", Status: "running"})
	if !strings.Contains(chip.Body, "workflow") {
		t.Errorf("empty Name should fall back to type label 'workflow', got %q", chip.Body)
	}
}

func TestAgentStatusColor_AllStatuses(t *testing.T) {
	cases := []struct {
		status string
		want   Color
	}{
		{"running", ColorPeach},
		{"completed", ColorGreen},
		{"failed", ColorRed},
		{"killed", ColorRed},
		{"pending", ColorYellow},
		{"queued", ColorYellow},
		{"", ColorLavender},
		{"unrecognized", ColorLavender},
	}
	for _, c := range cases {
		got := agentStatusColor(c.status)
		if got != c.want {
			t.Errorf("agentStatusColor(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestRenderAgentNameChip_StripsControlBytes(t *testing.T) {
	// A poisoned Name (e.g. via attacker-controlled task creation)
	// must not inject terminal escapes into the chip body.
	task := Task{Name: ptr("evil\x1b]0;PWN\x07"), Type: "local_agent", Status: "running"}
	chip := renderAgentNameChip(task)
	for _, r := range chip.Body {
		if r < 0x20 || r == 0x7f {
			t.Errorf("chip body contains control byte 0x%x: %q", r, chip.Body)
		}
	}
}

// --- Agent description chip tests ---

func TestRenderAgentDescriptionChip_WhenNameAndDescriptionDiffer(t *testing.T) {
	task := Task{
		Name:        ptr("auditor"),
		Type:        "local_agent",
		Description: "Reviewing config",
		Status:      "running",
	}
	chip, ok := renderAgentDescriptionChip(task)
	if !ok {
		t.Fatalf("description chip should render when Description differs from Name")
	}
	if !strings.Contains(chip.Body, "Reviewing config") {
		t.Errorf("body should contain Description, got %q", chip.Body)
	}
	if chip.Color != ColorSapphire {
		t.Errorf("description chip color = %v, want ColorSapphire", chip.Color)
	}
}

func TestRenderAgentDescriptionChip_SkipsWhenSameAsName(t *testing.T) {
	// Common Task-tool case: Name nil, Label = Description, so the
	// name chip already shows Description. Description chip would
	// duplicate it.
	task := Task{
		Type:        "local_agent",
		Label:       "Audit code",
		Description: "Audit code",
		Status:      "running",
	}
	if _, ok := renderAgentDescriptionChip(task); ok {
		t.Errorf("description chip should be omitted when text matches the name chip")
	}
}

func TestRenderAgentDescriptionChip_FallsBackToLabel(t *testing.T) {
	// Description empty but Label set → use Label as the secondary
	// text (still useful info).
	task := Task{
		Name:   ptr("auditor"),
		Type:   "local_agent",
		Label:  "Short label",
		Status: "running",
	}
	chip, ok := renderAgentDescriptionChip(task)
	if !ok {
		t.Fatalf("description chip should render with Label fallback")
	}
	if !strings.Contains(chip.Body, "Short label") {
		t.Errorf("body should contain Label fallback, got %q", chip.Body)
	}
}

func TestRenderAgentDescriptionChip_SkipsWhenEmpty(t *testing.T) {
	task := Task{Name: ptr("auditor"), Type: "local_agent", Status: "running"}
	if _, ok := renderAgentDescriptionChip(task); ok {
		t.Errorf("description chip should be omitted when both Description and Label are empty")
	}
}

func TestRenderAgentDescriptionChip_TruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 50)
	task := Task{
		Name:        ptr("auditor"),
		Type:        "local_agent",
		Description: long,
		Status:      "running",
	}
	chip, _ := renderAgentDescriptionChip(task)
	// Body includes padding (" " + text + " "). Strip the padding
	// for the rune count comparison.
	body := strings.TrimSpace(chip.Body)
	if len([]rune(body)) > descriptionMaxRunes {
		t.Errorf("description text not truncated: %d runes (max %d), body=%q",
			len([]rune(body)), descriptionMaxRunes, body)
	}
	if !strings.HasSuffix(body, "…") {
		t.Errorf("truncated description should end with ellipsis, got %q", body)
	}
}

func TestRenderAgentDescriptionChip_StripsControlBytes(t *testing.T) {
	task := Task{
		Name:        ptr("auditor"),
		Type:        "local_agent",
		Description: "evil\x1b]0;PWN\x07",
		Status:      "running",
	}
	chip, _ := renderAgentDescriptionChip(task)
	for _, r := range chip.Body {
		if r < 0x20 || r == 0x7f {
			t.Errorf("description chip body contains control byte 0x%x: %q", r, chip.Body)
		}
	}
}

func TestRenderAgentNameChip_BodyHasPaddingSpaces(t *testing.T) {
	chip := renderAgentNameChip(Task{Name: ptr("x"), Type: "local_agent", Status: "running"})
	if !strings.HasPrefix(chip.Body, " ") || !strings.HasSuffix(chip.Body, " ") {
		t.Errorf("body should be wrapped with single-space padding, got %q", chip.Body)
	}
}

func TestRenderDirectoryChip_EmptyCWD(t *testing.T) {
	// When cwd is empty, we fall back to the process's working dir.
	// Body should NOT contain "?" because os.Getwd succeeds in tests.
	got := renderDirectoryChip("")
	if strings.Contains(got.Body, "?") {
		t.Errorf("empty cwd should fall back to os.Getwd (not '?'), got Body=%q", got.Body)
	}
	if got.Color != ColorLavender {
		t.Errorf("color = %v, want ColorLavender", got.Color)
	}
}

func TestRenderDirectoryChip_HomeItself(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := renderDirectoryChip("/tmp/fakehome")
	// "~" exactly — not "~/" (no trailing slash for home itself)
	if !strings.Contains(got.Body, "~") {
		t.Errorf("home cwd should render with ~, got Body=%q", got.Body)
	}
	if strings.Contains(got.Body, "~/") {
		t.Errorf("home cwd should NOT contain ~/ (trailing slash), got Body=%q", got.Body)
	}
}

func TestRenderDirectoryChip_UnderHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := renderDirectoryChip("/tmp/fakehome/projects/steward")
	if !strings.Contains(got.Body, "~/projects/steward") {
		t.Errorf("under-home cwd should render as ~/relative, got Body=%q", got.Body)
	}
}

func TestRenderDirectoryChip_OutsideHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := renderDirectoryChip("/etc/nixos")
	if !strings.Contains(got.Body, "/etc/nixos") {
		t.Errorf("outside-home cwd should render absolute path, got Body=%q", got.Body)
	}
	if strings.Contains(got.Body, "~") {
		t.Errorf("outside-home cwd should NOT use ~, got Body=%q", got.Body)
	}
}

func TestRenderDirectoryChip_Root(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := renderDirectoryChip("/")
	if !strings.Contains(got.Body, "/") {
		t.Errorf("root cwd should render as /, got Body=%q", got.Body)
	}
}

func TestRenderDirectoryChip_PaddingSpaces(t *testing.T) {
	got := renderDirectoryChip("/tmp/x")
	if !strings.HasPrefix(got.Body, " ") {
		t.Errorf("Body should start with a space, got %q", got.Body)
	}
	if !strings.HasSuffix(got.Body, " ") {
		t.Errorf("Body should end with a space, got %q", got.Body)
	}
}

func TestRenderDirectoryChip_StripsControlChars(t *testing.T) {
	// A poisoned cwd containing ANSI escape bytes must not leak into
	// the chip body.
	got := renderDirectoryChip("/tmp/\x1b[2Jevil")
	if strings.Contains(got.Body, "\x1b") {
		t.Errorf("Body must strip ESC bytes; got %q", got.Body)
	}
}

func TestRenderContextChip_Zero(t *testing.T) {
	got := renderContextChip(0, 1_000_000)
	if got.Color != ColorGreen {
		t.Errorf("0%% color = %v, want ColorGreen", got.Color)
	}
	if !strings.Contains(got.Body, "▱▱▱▱▱") {
		t.Errorf("0%% should have 5 empty blocks, got Body=%q", got.Body)
	}
	if strings.Contains(got.Body, "▰") {
		t.Errorf("0%% should have NO filled blocks, got Body=%q", got.Body)
	}
	if !strings.Contains(got.Body, "0%") {
		t.Errorf("Body should contain percent, got %q", got.Body)
	}
}

func TestRenderContextChip_FortyPercent(t *testing.T) {
	got := renderContextChip(400_000, 1_000_000)
	// 40% with round(40/20)=2 → 2 filled, 3 empty.
	filled := strings.Count(got.Body, "▰")
	empty := strings.Count(got.Body, "▱")
	if filled != 2 || empty != 3 {
		t.Errorf("40%% blocks = %d filled, %d empty; want 2 filled, 3 empty (Body=%q)", filled, empty, got.Body)
	}
	if got.Color != ColorYellow {
		t.Errorf("40%% color = %v, want ColorYellow", got.Color)
	}
}

func TestRenderContextChip_QuintileRounding(t *testing.T) {
	// Epic requirement: quintile count uses ROUND, not floor.
	// 17% → round(0.85) = 1; floor would give 0. Catches the bug
	// that conformance-G1 flagged.
	got := renderContextChip(170_000, 1_000_000)
	filled := strings.Count(got.Body, "▰")
	if filled != 1 {
		t.Errorf("17%% (round) should give 1 filled block, got %d (Body=%q)", filled, got.Body)
	}
	// 35% → round(1.75) = 2; floor would give 1.
	got = renderContextChip(350_000, 1_000_000)
	filled = strings.Count(got.Body, "▰")
	if filled != 2 {
		t.Errorf("35%% (round) should give 2 filled blocks, got %d (Body=%q)", filled, got.Body)
	}
	// 9% → round(0.45) = 0 (under .5). Exercises the rounds-down case.
	got = renderContextChip(90_000, 1_000_000)
	filled = strings.Count(got.Body, "▰")
	if filled != 0 {
		t.Errorf("9%% (round) should give 0 filled blocks, got %d (Body=%q)", filled, got.Body)
	}
}

func TestRenderContextChip_EightyPercent(t *testing.T) {
	got := renderContextChip(800_000, 1_000_000)
	filled := strings.Count(got.Body, "▰")
	if filled != 4 {
		t.Errorf("80%% filled = %d, want 4 (Body=%q)", filled, got.Body)
	}
	if got.Color != ColorRed {
		t.Errorf("80%% color = %v, want ColorRed", got.Color)
	}
}

func TestRenderContextChip_FullWindow(t *testing.T) {
	got := renderContextChip(1_000_000, 1_000_000)
	filled := strings.Count(got.Body, "▰")
	if filled != 5 {
		t.Errorf("100%% filled = %d, want 5", filled)
	}
	if !strings.Contains(got.Body, "100%") {
		t.Errorf("100%% body should contain '100%%', got %q", got.Body)
	}
	if got.Color != ColorRed {
		t.Errorf("100%% color = %v, want ColorRed", got.Color)
	}
}

func TestRenderContextChip_OverWindow(t *testing.T) {
	got := renderContextChip(1_500_000, 1_000_000)
	filled := strings.Count(got.Body, "▰")
	if filled != 5 {
		t.Errorf(">100%% filled = %d, want 5 (clamped)", filled)
	}
	if !strings.Contains(got.Body, "100%") {
		t.Errorf(">100%% body should clamp display to 100%%, got %q", got.Body)
	}
	if got.Color != ColorRed {
		t.Errorf(">100%% color = %v, want ColorRed", got.Color)
	}
}

func TestRenderContextChip_NegativeTokens(t *testing.T) {
	got := renderContextChip(-100, 1_000_000)
	filled := strings.Count(got.Body, "▰")
	if filled != 0 {
		t.Errorf("negative tokens filled = %d, want 0 (clamped)", filled)
	}
	if got.Color != ColorGreen {
		t.Errorf("negative tokens color = %v, want ColorGreen (clamped to 0%%)", got.Color)
	}
}

func TestRenderContextChip_ThresholdTransitions(t *testing.T) {
	cases := []struct {
		pct       int
		wantColor Color
	}{
		{0, ColorGreen},
		{39, ColorGreen},
		{40, ColorYellow},
		{59, ColorYellow},
		{60, ColorPeach},
		{79, ColorPeach},
		{80, ColorRed},
		{99, ColorRed},
		{100, ColorRed},
	}
	const window = 1_000_000
	for _, c := range cases {
		tokens := c.pct * window / 100
		got := renderContextChip(tokens, window)
		if got.Color != c.wantColor {
			t.Errorf("%d%% (tokens=%d): color = %v, want %v", c.pct, tokens, got.Color, c.wantColor)
		}
	}
}

func TestRenderContextChip_AlternateWindow(t *testing.T) {
	got := renderContextChip(100_000, 200_000)
	if got.Color != ColorYellow {
		t.Errorf("100k/200k color = %v, want ColorYellow (50%%)", got.Color)
	}
	if !strings.Contains(got.Body, "50%") {
		t.Errorf("Body should contain 50%%, got %q", got.Body)
	}
	// 50% round = 3 (round(2.5) = 3 half-up). Existing test expected
	// 2 (floor). Update: spec mandates round.
	filled := strings.Count(got.Body, "▰")
	if filled != 3 {
		t.Errorf("50%% round filled = %d, want 3", filled)
	}
}

func TestRenderContextChip_ZeroWindow(t *testing.T) {
	got := renderContextChip(100, 0)
	if got.Color != ColorRed {
		t.Errorf("zero-window color = %v, want ColorRed (clamped)", got.Color)
	}
}

func TestRenderContextChip_PaddingSpaces(t *testing.T) {
	got := renderContextChip(500_000, 1_000_000)
	if !strings.HasPrefix(got.Body, " ") {
		t.Errorf("Body should start with space, got %q", got.Body)
	}
	if !strings.HasSuffix(got.Body, " ") {
		t.Errorf("Body should end with space, got %q", got.Body)
	}
}

func TestColor_FGBG_Roundtrip(t *testing.T) {
	colors := []Color{ColorLavender, ColorGreen, ColorYellow, ColorPeach, ColorRed, ColorPink, ColorTeal}
	for _, c := range colors {
		if c.FG() == "" {
			t.Errorf("%v FG() returned empty", c)
		}
		if c.BG() == "" {
			t.Errorf("%v BG() returned empty", c)
		}
		if c.FG() == c.BG() {
			t.Errorf("%v FG and BG should differ", c)
		}
	}
}

// writeFakeGitHEAD creates a temp dir with .git/HEAD content for branch tests.
func writeFakeGitHEAD(t *testing.T, headContent string) string {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(headContent), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	return dir
}

func TestRenderBranchChip_OnBranch(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n")
	got, ok := renderBranchChip(cwd)
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "main") {
		t.Errorf("Body should contain 'main', got %q", got.Body)
	}
	if got.Color != ColorPink {
		t.Errorf("color = %v, want ColorPink", got.Color)
	}
}

func TestRenderBranchChip_DetachedHEAD(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "abc123def456789012345678901234567890abcd\n")
	got, ok := renderBranchChip(cwd)
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "abc123de") {
		t.Errorf("Body should contain 'abc123de' (short SHA), got %q", got.Body)
	}
	if strings.Contains(got.Body, "abc123def456") {
		t.Errorf("Body should NOT contain full SHA, got %q", got.Body)
	}
}

func TestRenderBranchChip_DetachedHEADRejectsNonHex(t *testing.T) {
	// Defense: if HEAD is a symlink pointed at an arbitrary file
	// (e.g. /etc/hostname), the first 8 chars might not be hex.
	// isHexPrefix should refuse to display them as a fake SHA.
	cwd := writeFakeGitHEAD(t, "vermissian.example.com\n")
	_, ok := renderBranchChip(cwd)
	if ok {
		t.Errorf("expected !ok for non-hex HEAD content")
	}
}

func TestRenderBranchChip_NoGitHEAD(t *testing.T) {
	cwd := t.TempDir()
	_, ok := renderBranchChip(cwd)
	if ok {
		t.Errorf("expected !ok for cwd with no .git, got ok")
	}
}

func TestRenderBranchChip_WorktreePointer(t *testing.T) {
	// .git is a file containing `gitdir: <path>`. Should dereference
	// and read the pointed-to HEAD.
	cwd := t.TempDir()
	gitDir := filepath.Join(cwd, "actual-gitdir")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/wt-branch\n"), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	got, ok := renderBranchChip(cwd)
	if !ok {
		t.Fatalf("expected chip via worktree pointer, got !ok")
	}
	if !strings.Contains(got.Body, "wt-branch") {
		t.Errorf("Body should contain worktree branch name, got %q", got.Body)
	}
}

func TestRenderBranchChip_RefusesSymlinkedGit(t *testing.T) {
	// A symlinked .git is rejected (defense against arbitrary-file
	// reads via planted symlinks).
	cwd := t.TempDir()
	realDir := filepath.Join(cwd, "real.git")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir realDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.Symlink(realDir, filepath.Join(cwd, ".git")); err != nil {
		t.Fatalf("symlink .git: %v", err)
	}
	_, ok := renderBranchChip(cwd)
	if ok {
		t.Errorf("expected !ok for symlinked .git, got ok")
	}
}

func TestRenderBranchChip_EmptyCWD(t *testing.T) {
	_, ok := renderBranchChip("")
	if ok {
		t.Errorf("expected !ok for empty cwd, got ok")
	}
}

func TestRenderBranchChip_TrailingWhitespace(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/main\n\n  ")
	got, ok := renderBranchChip(cwd)
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "main") {
		t.Errorf("Body should contain 'main' (whitespace trimmed), got %q", got.Body)
	}
}

func TestRenderBranchChip_BranchWithSlashes(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/feature/long-name\n")
	got, ok := renderBranchChip(cwd)
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "feature/long-name") {
		t.Errorf("Body should preserve slashed branch name, got %q", got.Body)
	}
}

func TestRenderBranchChip_RejectsControlCharsInName(t *testing.T) {
	// A poisoned HEAD with ANSI escape in the branch name must be
	// refused — validBranchName rejects control bytes.
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/x\x1b[2Jevil\n")
	_, ok := renderBranchChip(cwd)
	if ok {
		t.Errorf("expected !ok for branch name with control chars")
	}
}

func TestRenderBranchChip_RejectsSpacesInName(t *testing.T) {
	cwd := writeFakeGitHEAD(t, "ref: refs/heads/ a b\n")
	_, ok := renderBranchChip(cwd)
	if ok {
		t.Errorf("expected !ok for branch name with leading space")
	}
}

func TestRenderAWSChip_Unset(t *testing.T) {
	_, ok := renderAWSChip("")
	if ok {
		t.Errorf("expected !ok for empty profile, got ok")
	}
}

func TestRenderAWSChip_ProdLowercase(t *testing.T) {
	got, ok := renderAWSChip("prod-account")
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if got.Color != ColorPeach {
		t.Errorf("'prod-account' color = %v, want ColorPeach", got.Color)
	}
	if !strings.Contains(got.Body, "prod-account") {
		t.Errorf("Body should contain profile, got %q", got.Body)
	}
}

func TestRenderAWSChip_ProdUppercase(t *testing.T) {
	got, _ := renderAWSChip("PRODUCTION")
	if got.Color != ColorPeach {
		t.Errorf("PRODUCTION color = %v, want ColorPeach (case-insensitive 'prod')", got.Color)
	}
}

func TestRenderAWSChip_NonProd(t *testing.T) {
	got, _ := renderAWSChip("staging")
	if got.Color != ColorTeal {
		t.Errorf("staging color = %v, want ColorTeal", got.Color)
	}
}

func TestRenderAWSChip_SubstringProd(t *testing.T) {
	got, _ := renderAWSChip("my-dev-prod")
	if got.Color != ColorPeach {
		t.Errorf("'my-dev-prod' color = %v, want ColorPeach (contains 'prod')", got.Color)
	}
}

func TestRenderAWSChip_StripsControlChars(t *testing.T) {
	got, ok := renderAWSChip("prod\x1b[2Jevil")
	if !ok {
		t.Fatalf("expected chip after stripping, got !ok")
	}
	if strings.Contains(got.Body, "\x1b") {
		t.Errorf("Body must strip ESC bytes; got %q", got.Body)
	}
	// stripControl removes the ESC byte; the literal characters `[2J`
	// remain (they're not control bytes themselves — only the leading
	// 0x1b makes them an ANSI sequence). That's correct: stripping
	// the escape neutralizes the injection without losing data.
	if !strings.Contains(got.Body, "prod[2Jevil") {
		t.Errorf("Body should contain 'prod[2Jevil' (ESC stripped, rest kept), got %q", got.Body)
	}
}

func TestRenderGCloudChip_Unset(t *testing.T) {
	_, ok := renderGCloudChip("")
	if ok {
		t.Errorf("expected !ok for empty project")
	}
}

func TestRenderGCloudChip_Set(t *testing.T) {
	got, ok := renderGCloudChip("my-project")
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "my-project") {
		t.Errorf("Body should contain project, got %q", got.Body)
	}
	if got.Color != ColorPeach {
		t.Errorf("color = %v, want ColorPeach", got.Color)
	}
}

func TestRenderK8sChip_Unset(t *testing.T) {
	_, ok := renderK8sChip("")
	if ok {
		t.Errorf("expected !ok for empty context")
	}
}

func TestRenderK8sChip_Set(t *testing.T) {
	got, ok := renderK8sChip("eks-prod")
	if !ok {
		t.Fatalf("expected chip, got !ok")
	}
	if !strings.Contains(got.Body, "eks-prod") {
		t.Errorf("Body should contain context, got %q", got.Body)
	}
	if got.Color != ColorTeal {
		t.Errorf("color = %v, want ColorTeal", got.Color)
	}
}

func TestSnapshotEnv_AllKeys(t *testing.T) {
	env := mapEnvReader{
		"AWS_PROFILE":           "prod",
		"CLOUDSDK_CORE_PROJECT": "primary",
		"KUBE_CONTEXT":          "eks",
	}
	snap := SnapshotEnv(env)
	if snap.AWSProfile != "prod" || snap.GCloudProject != "primary" || snap.K8sContext != "eks" {
		t.Errorf("SnapshotEnv: %+v", snap)
	}
}

func TestSnapshotEnv_FallbackKeys(t *testing.T) {
	env := mapEnvReader{
		"GOOGLE_CLOUD_PROJECT": "fallback",
		"KUBERNETES_CONTEXT":   "k8s-fallback",
	}
	snap := SnapshotEnv(env)
	if snap.GCloudProject != "fallback" {
		t.Errorf("expected GOOGLE_CLOUD_PROJECT fallback, got %q", snap.GCloudProject)
	}
	if snap.K8sContext != "k8s-fallback" {
		t.Errorf("expected KUBERNETES_CONTEXT fallback, got %q", snap.K8sContext)
	}
}

func TestSnapshotEnv_PrimaryWins(t *testing.T) {
	env := mapEnvReader{
		"CLOUDSDK_CORE_PROJECT": "primary",
		"GOOGLE_CLOUD_PROJECT":  "fallback",
		"KUBE_CONTEXT":          "primary-k8s",
		"KUBERNETES_CONTEXT":    "fallback-k8s",
	}
	snap := SnapshotEnv(env)
	if snap.GCloudProject != "primary" {
		t.Errorf("CLOUDSDK should win, got %q", snap.GCloudProject)
	}
	if snap.K8sContext != "primary-k8s" {
		t.Errorf("KUBE_CONTEXT should win, got %q", snap.K8sContext)
	}
}

// --- Model chip tests ---

func TestShortModelName_Table(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"claude-fable-5[1m]", "F5"},
		{"claude-fable-5", "F5"},
		{"claude-opus-4-8", "O4.8"},
		{"claude-sonnet-5", "S5"},
		{"claude-haiku-4-5-20251001", "H4.5"},
		{"claude-3-5-sonnet-20241022", "S3.5"},
		{"chatgpt/sol", "sol"},
		{"chatgpt/luna", "luna"},
		{"openrouter/glm-4.6", "glm-4.6"},
		{"bedrock/opus", "opus"},
		{"gpt-5.6-sol", "gpt-5.6-sol"},
		{"sonnet", "sonnet"},
		{"opus", "opus"},
		// claude- prefix with no recognizable family/version falls back
		// to the raw (truncated) id.
		{"claude-next", "claude-next"},
		// Provider prefix stripped before claude abbreviation.
		{"anthropic/claude-opus-4-8", "O4.8"},
		// Trailing slash: nothing after it, id passes through.
		{"weird/", "weird/"},
	}
	for _, c := range cases {
		if got := shortModelName(c.in); got != c.want {
			t.Errorf("shortModelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortModelName_TruncatesLongIDs(t *testing.T) {
	got := shortModelName("some-extremely-long-model-identifier-v2")
	if runeCount := len([]rune(got)); runeCount > modelMaxRunes {
		t.Errorf("long id should truncate to ≤%d runes, got %d (%q)", modelMaxRunes, runeCount, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated id should end with ellipsis, got %q", got)
	}
}

func TestShortModelName_StripsControlBytes(t *testing.T) {
	got := shortModelName("chatgpt/s\x1bol")
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("control bytes must be stripped, got %q", got)
	}
	if got != "sol" {
		t.Errorf("shortModelName with embedded ESC = %q, want %q", got, "sol")
	}
}

func TestRenderModelChip_Basic(t *testing.T) {
	chip, ok := renderModelChip(Task{Model: "chatgpt/sol"})
	if !ok {
		t.Fatal("model set → chip expected")
	}
	if chip.Color != ColorTeal {
		t.Errorf("model chip color = %v, want ColorTeal", chip.Color)
	}
	if !strings.Contains(chip.Body, "sol") {
		t.Errorf("body should contain short model name, got %q", chip.Body)
	}
	if !strings.HasPrefix(chip.Body, " ") || !strings.HasSuffix(chip.Body, " ") {
		t.Errorf("body should have padding spaces, got %q", chip.Body)
	}
}

func TestRenderModelChip_EffortSuffix(t *testing.T) {
	cases := []struct {
		effort EffortLevel
		want   string
	}{
		{"low", "×L"},
		{"medium", "×M"},
		{"high", "×H"},
		{"xhigh", "×XH"},
		{"max", "×MAX"},
	}
	for _, c := range cases {
		chip, ok := renderModelChip(Task{Model: "claude-fable-5[1m]", Effort: c.effort})
		if !ok {
			t.Fatalf("effort=%q: chip expected", c.effort)
		}
		if !strings.Contains(chip.Body, "F5 "+c.want) {
			t.Errorf("effort=%q: body should contain %q, got %q", c.effort, "F5 "+c.want, chip.Body)
		}
	}
}

func TestRenderModelChip_UnknownEffortNoSuffix(t *testing.T) {
	chip, ok := renderModelChip(Task{Model: "sonnet", Effort: "ultra"})
	if !ok {
		t.Fatal("model set → chip expected")
	}
	if strings.Contains(chip.Body, "×") {
		t.Errorf("unknown effort should render no suffix, got %q", chip.Body)
	}
}

func TestRenderModelChip_NoModelNoChip(t *testing.T) {
	if _, ok := renderModelChip(Task{Effort: "high"}); ok {
		t.Error("no model → no chip, even with effort set")
	}
}

// --- Agents handoff tests ---

func TestAgentsHandoff_RunningLabelsAndFocus(t *testing.T) {
	tasks := []Task{
		{ID: "a", Status: "running", Model: "claude-opus-5[1m]"},
		{ID: "b", Status: "completed", Model: "claude-haiku-4-5-20251001", Focused: true},
		{ID: "c", Status: "running", Model: "chatgpt/sol", Effort: "xhigh"},
		{ID: "d", Status: "failed", Model: "chatgpt/luna"},
	}
	labels, focused := agentsHandoff(tasks)
	wantLabels := []string{"O5", "sol ×XH"}
	if len(labels) != len(wantLabels) {
		t.Fatalf("labels = %v, want %v", labels, wantLabels)
	}
	for i := range wantLabels {
		if labels[i] != wantLabels[i] {
			t.Errorf("labels[%d] = %q, want %q", i, labels[i], wantLabels[i])
		}
	}
	if focused == nil || *focused != "H4.5" {
		t.Errorf("focused should be the completed-but-viewed task's label H4.5, got %v", focused)
	}
}

func TestAgentsHandoff_NoFocus(t *testing.T) {
	labels, focused := agentsHandoff([]Task{{ID: "a", Status: "running", Model: "sonnet"}})
	if focused != nil {
		t.Errorf("no focused task should give nil, got %q", *focused)
	}
	if len(labels) != 1 || labels[0] != "sonnet" {
		t.Errorf("labels = %v, want [sonnet]", labels)
	}
}

func TestAgentsHandoff_FocusedInheritedModelEmptyLabel(t *testing.T) {
	_, focused := agentsHandoff([]Task{{ID: "a", Status: "running", Focused: true}})
	if focused == nil || *focused != "" {
		t.Errorf("focused inherited-model task should give non-nil empty label, got %v", focused)
	}
}
