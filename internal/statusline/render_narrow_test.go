package statusline

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// depsFor builds a Dependencies suitable for narrow-mode unit tests.
// Reuses the package's canonical MockEnvReader so the env reader
// stays consistent with the rest of the test suite.
func depsFor(t *testing.T, awsProfile string) *Dependencies {
	t.Helper()
	env := NewMockEnvReader()
	if awsProfile != "" {
		env.vars["AWS_PROFILE"] = awsProfile
	}
	return &Dependencies{
		EnvReader: env,
		Resolver:  newTestResolver(t, ""),
	}
}

func TestNarrowWidthThreshold_Value(t *testing.T) {
	if narrowWidthThreshold != 80 {
		t.Errorf("narrowWidthThreshold = %d, want 80 (pinned by epic R1)", narrowWidthThreshold)
	}
}

func TestContextColor_Thresholds(t *testing.T) {
	cases := []struct {
		pct  int
		want string
	}{
		{0, "green"},
		{39, "green"},
		{40, "yellow"},
		{59, "yellow"},
		{60, "peach"},
		{79, "peach"},
		{80, "red"},
		{100, "red"},
	}
	for _, c := range cases {
		got := contextColor(c.pct)
		if got != c.want {
			t.Errorf("contextColor(%d) = %q, want %q", c.pct, got, c.want)
		}
	}
}

func TestBuildNarrowContextBody_QuintileCounts(t *testing.T) {
	// Round-half-up: 0-9 → 0 filled, 10-29 → 1, 30-49 → 2, 50-69 → 3, 70-89 → 4, 90+ → 5.
	cases := []struct {
		pct          int
		wantFilled   int
		wantContains string
	}{
		{0, 0, " 0%"},
		{15, 1, " 15%"},
		{42, 2, " 42%"},
		{60, 3, " 60%"},
		{75, 4, " 75%"},
		{100, 5, " 100%"},
	}
	for _, c := range cases {
		got := buildNarrowContextBody(c.pct)
		filled := strings.Count(got, "▰")
		empty := strings.Count(got, "▱")
		if filled != c.wantFilled {
			t.Errorf(
				"buildNarrowContextBody(%d): filled=%d, want %d (body=%q)",
				c.pct,
				filled,
				c.wantFilled,
				got,
			)
		}
		if filled+empty != narrowContextMaxQuintiles {
			t.Errorf(
				"buildNarrowContextBody(%d): filled+empty = %d, want %d",
				c.pct,
				filled+empty,
				narrowContextMaxQuintiles,
			)
		}
		if !strings.Contains(got, c.wantContains) {
			t.Errorf(
				"buildNarrowContextBody(%d) = %q, want to contain %q",
				c.pct,
				got,
				c.wantContains,
			)
		}
		if strings.Contains(got, ".") {
			t.Errorf("buildNarrowContextBody(%d) contains decimal: %q", c.pct, got)
		}
	}
}

func TestStripNarrowControl_RemovesEscapes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"main\x1b[31m", "main[31m"}, // ESC stripped; the [31m remains as plain printable text
		{"foo\x00bar", "foobar"},
		{"x\x07y", "xy"},
		{"normal name", "normal name"}, // SP kept (≥ 0x20)
		{"tab\there", "tabhere"},
		{"del\x7fchar", "delchar"},
	}
	for _, c := range cases {
		got := stripNarrowControl(c.in)
		if got != c.want {
			t.Errorf("stripNarrowControl(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGatherNarrowChips_DirOnly(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/fakehome/x"}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 2 {
		t.Fatalf("want 2 chips (dir+ctx) when no git/env, got %d: %+v", len(chips), chips)
	}
	if chips[0].Kind != kindDir {
		t.Errorf("chip[0].Kind = %q, want %q", chips[0].Kind, kindDir)
	}
	if chips[1].Kind != kindContext {
		t.Errorf("chip[1].Kind = %q, want %q", chips[1].Kind, kindContext)
	}
}

func TestGatherNarrowChips_WithGit(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", GitBranch: "main", UsedPercentage: 25}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 3 {
		t.Fatalf("want 3 chips (dir+ctx+branch), got %d: %+v", len(chips), chips)
	}
	if chips[2].Kind != kindBranch {
		t.Errorf("chip[2].Kind = %q, want %q", chips[2].Kind, kindBranch)
	}
	if !strings.Contains(chips[2].Body, "main") {
		t.Errorf("branch chip body should contain 'main', got %q", chips[2].Body)
	}
}

func TestGatherNarrowChips_WithAWS(t *testing.T) {
	deps := depsFor(t, "staging")
	data := &CachedData{CurrentDir: "/tmp/x", UsedPercentage: 30}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 3 {
		t.Fatalf("want 3 chips (dir+ctx+env), got %d: %+v", len(chips), chips)
	}
	if chips[2].Kind != kindEnv {
		t.Errorf("chip[2].Kind = %q, want %q", chips[2].Kind, kindEnv)
	}
	if !strings.Contains(chips[2].Body, "staging") {
		t.Errorf("AWS env chip body should contain profile 'staging', got %q", chips[2].Body)
	}
}

func TestGatherNarrowChips_AllChips(t *testing.T) {
	deps := depsFor(t, "staging")
	data := &CachedData{CurrentDir: "/tmp/x", GitBranch: "main", UsedPercentage: 25}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 4 {
		t.Fatalf("want 4 chips (dir+ctx+branch+env), got %d: %+v", len(chips), chips)
	}
	kinds := []string{chips[0].Kind, chips[1].Kind, chips[2].Kind, chips[3].Kind}
	want := []string{kindDir, kindContext, kindBranch, kindEnv}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("chip[%d].Kind = %q, want %q", i, kinds[i], k)
		}
	}
}

func TestGatherNarrowChips_AWSPrioritizedOverGcloud(t *testing.T) {
	deps := depsFor(t, "staging")
	data := &CachedData{CurrentDir: "/tmp/x", GcloudProject: "my-project"}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 3 {
		t.Fatalf("want 3 chips (dir+ctx+aws), got %d: %+v", len(chips), chips)
	}
	envBody := chips[2].Body
	if !strings.Contains(envBody, "staging") {
		t.Errorf("env chip should be AWS (staging), got body %q", envBody)
	}
	if strings.Contains(envBody, "my-project") {
		t.Errorf("env chip should NOT include gcloud project; got %q", envBody)
	}
}

func TestGatherNarrowChips_GcloudWhenNoAWS(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", GcloudProject: "my-project"}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 3 {
		t.Fatalf("want 3 chips (dir+ctx+gcloud), got %d: %+v", len(chips), chips)
	}
	if !strings.Contains(chips[2].Body, "my-project") {
		t.Errorf("env chip should contain gcloud project, got %q", chips[2].Body)
	}
}

func TestGatherNarrowChips_K8sWhenNoAWSorGcloud(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", K8sContext: "my-cluster"}
	chips := gatherNarrowChips(deps, data)
	if len(chips) != 3 {
		t.Fatalf("want 3 chips (dir+ctx+k8s), got %d: %+v", len(chips), chips)
	}
	if !strings.Contains(chips[2].Body, "my-cluster") {
		t.Errorf("env chip should contain k8s context, got %q", chips[2].Body)
	}
}

func TestGatherNarrowChips_ContextPercentTruncates(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", UsedPercentage: 42.7}
	chips := gatherNarrowChips(deps, data)
	if len(chips) < 2 || chips[1].Kind != kindContext {
		t.Fatalf("expected context chip at index 1, got %+v", chips)
	}
	if !strings.Contains(chips[1].Body, "42%") {
		t.Errorf("context body should contain '42%%' (int truncation), got %q", chips[1].Body)
	}
	if strings.Contains(chips[1].Body, "43%") {
		t.Errorf("context body should NOT contain '43%%' (no half-up), got %q", chips[1].Body)
	}
	if strings.Contains(chips[1].Body, ".") {
		t.Errorf("context body should not contain decimal, got %q", chips[1].Body)
	}
}

// --- Security regression tests for ANSI injection (S1) ---

func TestGatherNarrowChips_StripsControlBytesFromBranch(t *testing.T) {
	// A poisoned .git/HEAD value like `main\x1b]0;PWN\x07` should
	// either be rejected by validNarrowBranchName or have its
	// control bytes stripped before reaching the chip body.
	deps := depsFor(t, "")
	data := &CachedData{
		CurrentDir: "/tmp/x",
		GitBranch:  "main\x1b]0;PWN\x07",
	}
	chips := gatherNarrowChips(deps, data)
	for _, c := range chips {
		for _, r := range c.Body {
			if r < 0x20 || r == 0x7f {
				t.Errorf("chip body %q contains control byte 0x%x — sanitization failed", c.Body, r)
			}
		}
	}
}

func TestGatherNarrowChips_StripsControlBytesFromAWSProfile(t *testing.T) {
	deps := depsFor(t, "prof\x1b]0;x\x07")
	data := &CachedData{CurrentDir: "/tmp/x"}
	chips := gatherNarrowChips(deps, data)
	for _, c := range chips {
		for _, r := range c.Body {
			if r < 0x20 || r == 0x7f {
				t.Errorf("chip body %q contains control byte 0x%x — sanitization failed", c.Body, r)
			}
		}
	}
}

func TestGatherNarrowChips_StripsControlBytesFromGcloud(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", GcloudProject: "proj\x1bevil"}
	chips := gatherNarrowChips(deps, data)
	for _, c := range chips {
		if strings.ContainsRune(c.Body, '\x1b') {
			t.Errorf("chip body still contains ESC after stripping: %q", c.Body)
		}
	}
}

func TestGatherNarrowChips_RejectsInvalidBranchName(t *testing.T) {
	deps := depsFor(t, "")
	data := &CachedData{CurrentDir: "/tmp/x", GitBranch: "  spaces-start"}
	chips := gatherNarrowChips(deps, data)
	for _, c := range chips {
		if c.Kind == kindBranch {
			t.Errorf("branch chip should be rejected for invalid name, got %+v", c)
		}
	}
}

// --- Composition tests ---

func TestComposeNarrowChain_Empty(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	if got := s.composeNarrowChain(nil); got != "" {
		t.Errorf("empty chain = %q, want ''", got)
	}
	if got := s.composeNarrowChain([]narrowChip{}); got != "" {
		t.Errorf("empty slice chain = %q, want ''", got)
	}
}

func TestComposeNarrowChain_DirOnly(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{{Color: "lavender", Body: "~/x", Kind: kindDir}}
	got := s.composeNarrowChain(chips)
	if !strings.Contains(got, LeftCurve) {
		t.Errorf("missing LeftCurve in %q", got)
	}
	if !strings.Contains(got, RightCurve) {
		t.Errorf("missing RightCurve in %q", got)
	}
	if strings.Contains(got, LeftChevron) || strings.Contains(got, RightChevron) {
		t.Errorf("single chip should have NO chevrons, got %q", got)
	}
}

func TestComposeNarrowChain_DirAndContext_AllForwardChevrons(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
	}
	got := s.composeNarrowChain(chips)
	if strings.Count(got, LeftChevron) != 1 {
		t.Errorf("want 1 forward chevron, got %d in %q", strings.Count(got, LeftChevron), got)
	}
	if strings.Count(got, RightChevron) != 0 {
		t.Errorf("want 0 backward chevrons (context is last, no pivot), got %d",
			strings.Count(got, RightChevron))
	}
}

func TestComposeNarrowChain_DirContextBranch_PivotAtBranch(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
	}
	got := s.composeNarrowChain(chips)
	if strings.Count(got, LeftChevron) != 1 {
		t.Errorf("want exactly 1 forward chevron, got %d", strings.Count(got, LeftChevron))
	}
	if strings.Count(got, RightChevron) != 1 {
		t.Errorf("want exactly 1 backward chevron, got %d", strings.Count(got, RightChevron))
	}
}

func TestComposeNarrowChain_AllFourChips_MirrorPattern(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
		{Color: "peach", Body: " staging", Kind: kindEnv},
	}
	got := s.composeNarrowChain(chips)
	if strings.Count(got, LeftChevron) != 1 {
		t.Errorf("want 1 forward chevron, got %d", strings.Count(got, LeftChevron))
	}
	if strings.Count(got, RightChevron) != 2 {
		t.Errorf("want 2 backward chevrons, got %d", strings.Count(got, RightChevron))
	}
}

func TestComposeNarrowChain_PostPivotChevronColorOrder(t *testing.T) {
	// Post-pivot junctions render RightChevron (U+E0B2), a mirrored
	// powerline glyph: the solid triangle must carry the RIGHT
	// (next) chip's color over the LEFT (current) chip's
	// background — bg = chip.Color, fg = next.Color. This is the
	// opposite convention from the pre-pivot LeftChevron
	// (bg = next.Color, fg = chip.Color), because the chevron
	// direction has mirrored but the "which side is which color"
	// rule mirrors with it.
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
		{Color: "peach", Body: " staging", Kind: kindEnv},
	}
	got := s.composeNarrowChain(chips)

	// Junction under test: branch(pink) -> env(peach), the second
	// post-pivot junction, both chips distinctly colored.
	wantCorrect := s.getColorBG("pink") + s.getColorFG("peach") + RightChevron
	wantBuggy := s.getColorBG("peach") + s.getColorFG("pink") + RightChevron

	if !strings.Contains(got, wantCorrect) {
		t.Errorf(
			"post-pivot RightChevron between branch and env should have bg=branch(pink) fg=env(peach); got %q",
			got,
		)
	}
	if strings.Contains(got, wantBuggy) {
		t.Errorf("post-pivot RightChevron has inverted colors (bg=next, fg=current); got %q", got)
	}
}

func TestComposeNarrowChain_StartsWithLeftCurveEndsWithRightCurve(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
	}
	got := s.composeNarrowChain(chips)
	stripped := stripAnsi(got)
	if !strings.HasPrefix(stripped, LeftCurve) {
		t.Errorf("stripped output should start with LeftCurve, got %q", stripped)
	}
	if !strings.HasSuffix(stripped, RightCurve) {
		t.Errorf("stripped output should end with RightCurve, got %q", stripped)
	}
	if !strings.HasSuffix(got, "\033[0m") {
		const tailN = 8
		start := max(0, len(got)-tailN)
		t.Errorf("output should end with NC reset, tail = %q", got[start:])
	}
}

// --- Fit + render tests ---

func TestFitNarrowChain_AllFitWithSlack(t *testing.T) {
	chips := []narrowChip{
		{Color: "lavender", Body: "~/x", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
	}
	got := fitNarrowChain(chips, 50)
	if len(got) != 3 {
		t.Fatalf("want 3 chips kept, got %d", len(got))
	}
	ctxIdx := -1
	for i, c := range got {
		if c.Kind == kindContext {
			ctxIdx = i
		}
	}
	if ctxIdx == -1 {
		t.Fatal("context chip missing")
	}
	if !strings.Contains(got[ctxIdx].Body, "0%") {
		t.Errorf("context body should still contain '0%%', got %q", got[ctxIdx].Body)
	}
	if got[ctxIdx].Body == "0%" {
		t.Errorf("context body did NOT expand for slack absorption; still %q", got[ctxIdx].Body)
	}
}

func TestFitNarrowChain_DropEnvFirst(t *testing.T) {
	chips := []narrowChip{
		{Color: "lavender", Body: "~/proj", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
		{Color: "peach", Body: " a-really-long-aws-profile-name", Kind: kindEnv},
	}
	got := fitNarrowChain(chips, 30)
	hasEnv := false
	hasBranch := false
	for _, c := range got {
		if c.Kind == kindEnv {
			hasEnv = true
		}
		if c.Kind == kindBranch {
			hasBranch = true
		}
	}
	if hasEnv {
		t.Errorf("env should drop first at budget=30, got chips: %+v", got)
	}
	if !hasBranch {
		t.Errorf("branch should survive; got: %+v", got)
	}
}

func TestFitNarrowChain_DropBranchSecond(t *testing.T) {
	chips := []narrowChip{
		{Color: "lavender", Body: "~/proj", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " feature/very-long-branch-name", Kind: kindBranch},
		{Color: "peach", Body: " prod", Kind: kindEnv},
	}
	got := fitNarrowChain(chips, 25)
	hasBranch := false
	hasEnv := false
	for _, c := range got {
		if c.Kind == kindBranch {
			hasBranch = true
		}
		if c.Kind == kindEnv {
			hasEnv = true
		}
	}
	if hasEnv {
		t.Errorf("env should drop before branch at budget=25, got: %+v", got)
	}
	if hasBranch {
		t.Errorf("branch should ALSO drop at budget=25; got: %+v", got)
	}
}

func TestFitNarrowChain_TruncateDirToLeaf(t *testing.T) {
	chips := []narrowChip{
		{Color: "lavender", Body: "~/Personal/cc-tools/long/path/segment", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
	}
	got := fitNarrowChain(chips, 18)
	if len(got) < 1 {
		t.Fatalf("want at least dir chip, got nothing")
	}
	if got[0].Kind != kindDir {
		t.Fatalf("chips[0] should be dir, got %+v", got[0])
	}
	if strings.Contains(got[0].Body, "/") {
		t.Errorf("dir body should be leaf-only (no slash), got %q", got[0].Body)
	}
	if !strings.Contains(got[0].Body, "segment") {
		t.Errorf("dir body should contain leaf 'segment', got %q", got[0].Body)
	}
}

func TestFitNarrowChain_DirAlwaysSurvives(t *testing.T) {
	chips := []narrowChip{
		{Color: "lavender", Body: "~/proj", Kind: kindDir},
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "pink", Body: " main", Kind: kindBranch},
		{Color: "peach", Body: " prod", Kind: kindEnv},
	}
	got := fitNarrowChain(chips, 10)
	if len(got) == 0 {
		t.Fatalf("dir must always survive; got empty slice")
	}
	if got[0].Kind != kindDir {
		t.Errorf("first surviving chip must be dir; got %+v", got[0])
	}
}

func TestPadContextBody_EvenSlack(t *testing.T) {
	got := padContextBody("42%", 11)
	want := "    42%    " // slack=8, left=4, right=4
	if got != want {
		t.Errorf("padContextBody(42%%, 11) = %q, want %q", got, want)
	}
}

func TestPadContextBody_OddSlack(t *testing.T) {
	got := padContextBody("42%", 10)
	want := "   42%    " // slack=7, left=3, right=4
	if got != want {
		t.Errorf("padContextBody(42%%, 10) = %q, want %q", got, want)
	}
}

func TestPadContextBody_BodyTooBig(t *testing.T) {
	got := padContextBody("42%", 2)
	if got != "42%" {
		t.Errorf("targetWidth < body width should return unchanged, got %q", got)
	}
}

// renderAt is shared with smoketest for end-to-end render checks.
func renderAt(t *testing.T, width int) string {
	t.Helper()
	deps := &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: width},
		Resolver:      newTestResolver(t, ""),
	}
	s := CreateStatusline(deps)
	data := &CachedData{
		CurrentDir:     "/tmp/x",
		ModelDisplay:   "Opus 4.7",
		UsedPercentage: 25,
		TermWidth:      width,
	}
	return s.Render(data)
}

func TestRenderNarrow_AllChipsPresentAtBudget46(t *testing.T) {
	// Direct renderNarrow call at budget=46 (the effective width
	// produced by termWidth=50 with default 2+2 spacers). All four
	// chips should be present, output should hit exactly 46 cells,
	// and the context body should contain quintile blocks.
	s := newTestStatusline(t, newTestResolver(t, ""))
	if er, ok := s.deps.EnvReader.(*MockEnvReader); ok {
		er.vars["AWS_PROFILE"] = "staging"
	}
	data := &CachedData{
		CurrentDir:     "/tmp/x",
		UsedPercentage: 25,
		GitBranch:      "main",
	}
	got := s.renderNarrow(data, 46)
	if w := runewidth.StringWidth(stripAnsi(got)); w != 46 {
		t.Errorf("renderNarrow(46) visible width = %d, want 46", w)
	}
	stripped := stripAnsi(got)
	if !strings.Contains(stripped, LeftCurve) {
		t.Errorf("output missing LeftCurve")
	}
	if !strings.Contains(stripped, RightCurve) {
		t.Errorf("output missing RightCurve")
	}
	if !strings.Contains(stripped, "▰") {
		t.Errorf("output missing quintile block (filled) — context chip not rendering blocks")
	}
	if !strings.Contains(stripped, "▱") {
		t.Errorf("output missing quintile block (empty) — context chip not rendering blocks")
	}
}

// --- Dispatch boundary tests ---

// isNarrowOutput uses a structural signal to distinguish narrow from
// wide rendering: narrow mode emits exactly ONE LeftCurve (the chain
// start cap); wide mode emits multiple (one for the left section,
// another wrapping the context bar in the middle).
func isNarrowOutput(s string) bool {
	return strings.Count(stripAnsi(s), LeftCurve) == 1
}

func TestStatuslineRender_DispatchesToNarrowAtWidth50(t *testing.T) {
	got := renderAt(t, 50)
	if !isNarrowOutput(got) {
		t.Errorf(
			"Render at width=50 should use narrow mode (1 LeftCurve), got %d LeftCurves; stripped=%q",
			strings.Count(stripAnsi(got), LeftCurve),
			stripAnsi(got),
		)
	}
}

func TestStatuslineRender_DispatchesAtExactlyThreshold80(t *testing.T) {
	got := renderAt(t, 80)
	if !isNarrowOutput(got) {
		t.Errorf("Render at width=80 (≤threshold) should use narrow mode; got %d LeftCurves",
			strings.Count(stripAnsi(got), LeftCurve))
	}
}

func TestStatuslineRender_DispatchesToWideAt81(t *testing.T) {
	got := renderAt(t, 81)
	if isNarrowOutput(got) {
		t.Errorf(
			"Render at width=81 (>threshold) should use wide mode; got narrow-style output: %q",
			stripAnsi(got),
		)
	}
}

func TestStatuslineRender_DispatchesToWideAt200(t *testing.T) {
	got := renderAt(t, 200)
	if isNarrowOutput(got) {
		t.Errorf("Render at width=200 should use wide mode; got narrow-style output: %q",
			stripAnsi(got))
	}
	if stripAnsi(got) == "" {
		t.Errorf("wide-mode render at 200 should not be empty")
	}
}

// --- Defense-in-depth: every palette key reachable by narrow chips
// must resolve to non-empty BG and FG escapes (S2).

func TestComposeNarrowChain_ContrastingChevronForMatchingColors(t *testing.T) {
	// When two adjacent chips share a bg color (e.g. context=green
	// next to AWS-dev=green), the default chevron fg equals the bg
	// and the glyph disappears. The renderer must use the dark
	// BaseFG instead so the boundary stays legible.
	s := newTestStatusline(t, newTestResolver(t, ""))
	chips := []narrowChip{
		{Color: "green", Body: "0%", Kind: kindContext},
		{Color: "green", Body: " dev", Kind: kindEnv},
	}
	got := s.composeNarrowChain(chips)
	if !strings.Contains(got, s.colors.BaseFG()) {
		t.Errorf("same-color adjacent chips should use BaseFG for the chevron fg, got %q", got)
	}
}

func TestNarrowChipColorsRoundTrip(t *testing.T) {
	s := newTestStatusline(t, newTestResolver(t, ""))
	keys := []string{
		"lavender", "pink", "peach", "teal", "green", "yellow", "red",
		"maroon", "sapphire", "mauve",
	}
	for _, k := range keys {
		bg := s.getColorBG(k)
		fg := s.getColorFG(k)
		if bg == "" {
			t.Errorf("getColorBG(%q) returned empty escape; chip would render with previous bg", k)
		}
		if fg == "" {
			t.Errorf(
				"getColorFG(%q) returned empty escape; curve/chevron would render uncolored",
				k,
			)
		}
		if bg == fg {
			t.Errorf("getColorBG(%q) == getColorFG(%q); BG and FG should differ", k, k)
		}
	}
}
