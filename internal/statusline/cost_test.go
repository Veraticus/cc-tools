package statusline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// --- fixtures -----------------------------------------------------------

// writeCredentials writes a .credentials.json fixture at profileDir with
// the given subscriptionType ("" omits the claudeAiOauth.subscriptionType
// field's meaningful value but still writes a well-formed file).
func writeCredentials(t *testing.T, profileDir, subscriptionType string) {
	t.Helper()
	content := fmt.Sprintf(`{"claudeAiOauth":{"subscriptionType":%q}}`, subscriptionType)
	if err := os.WriteFile(filepath.Join(profileDir, credentialsFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// bedrockRowMsgID is the fixed message.id every bedrockRow fixture uses:
// its "msg_bdrk" prefix is what internal/cost's row-cost logic reads to
// select bedrock (always-priced) rates over anthropic list rates.
const bedrockRowMsgID = "msg_bdrk_1"

// bedrockRowInputTokens is the fixed input-token count every bedrockRow
// fixture uses; only the amount's positivity is asserted by these tests,
// not its exact value.
const bedrockRowInputTokens = 1000

// bedrockRow renders one minimal billable bedrock-backed transcript JSONL
// row: bedrock pricing always applies regardless of the subscribed flag,
// which keeps these fixtures independent of the credentials-file test
// dimension. timestamp must be an RFC3339 string.
func bedrockRow(timestamp string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"requestId":%q,`+
			`"message":{"id":%q,"model":"claude-fable-5",`+
			`"usage":{"input_tokens":%d,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		timestamp, bedrockRowMsgID, bedrockRowMsgID, bedrockRowInputTokens,
	)
}

// writeTranscript writes one JSONL row to path, terminated by a newline.
func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupTranscriptFixture builds <tmp>/profile/projects/slug/session.jsonl
// plus a sibling .credentials.json (subscriptionType "max") at
// <tmp>/profile, and returns the transcript path.
func setupTranscriptFixture(t *testing.T, rows ...string) string {
	t.Helper()
	tmp := t.TempDir()
	profileDir := filepath.Join(tmp, "profile")
	projectDir := filepath.Join(profileDir, "projects", "slug")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredentials(t, profileDir, "max")

	transcriptPath := filepath.Join(projectDir, "session.jsonl")
	writeTranscript(t, transcriptPath, rows...)
	return transcriptPath
}

// --- subscription detection ---------------------------------------------

func TestReadSubscribed_PresentSubscriptionType(t *testing.T) {
	tmp := t.TempDir()
	writeCredentials(t, tmp, "max")
	if !readSubscribed(tmp) {
		t.Error("expected subscribed=true when subscriptionType is set")
	}
}

func TestReadSubscribed_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	if readSubscribed(tmp) {
		t.Error("expected subscribed=false when .credentials.json is missing")
	}
}

func TestReadSubscribed_MalformedFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, credentialsFileName), []byte("not json {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readSubscribed(tmp) {
		t.Error("expected subscribed=false for a malformed credentials file")
	}
}

func TestReadSubscribed_EmptySubscriptionType(t *testing.T) {
	tmp := t.TempDir()
	writeCredentials(t, tmp, "")
	if readSubscribed(tmp) {
		t.Error("expected subscribed=false when subscriptionType is an empty string")
	}
}

// --- path derivation + end-to-end compute --------------------------------

func TestTranscriptCosts_ComputesFromRealFixture(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	state, ok := transcriptCosts("", 0, transcriptPath, now)
	if !ok {
		t.Fatal("expected ok=true for a well-formed fixture")
	}
	if state.SessionUSD <= 0 {
		t.Errorf("expected positive SessionUSD, got %v", state.SessionUSD)
	}
	if state.DailyUSD <= 0 {
		t.Errorf("expected positive DailyUSD, got %v", state.DailyUSD)
	}
}

func TestTranscriptCosts_UnreadableTranscriptFails(t *testing.T) {
	tmp := t.TempDir()
	profileDir := filepath.Join(tmp, "profile")
	projectDir := filepath.Join(profileDir, "projects", "slug")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredentials(t, profileDir, "max")

	// No transcript file written at all.
	transcriptPath := filepath.Join(projectDir, "session.jsonl")

	_, ok := transcriptCosts("", 0, transcriptPath, time.Now())
	if ok {
		t.Error("expected ok=false when the transcript file doesn't exist")
	}
}

// --- caching --------------------------------------------------------------

func TestTranscriptCosts_CacheHitSkipsRecompute(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	first, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now)
	if !ok {
		t.Fatal("expected ok=true on first (cold-cache) call")
	}

	// Delete the transcript file itself: if the second call recomputes
	// rather than reading the cache, it will fail (ok=false) because
	// the file no longer exists.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	second, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(time.Second))
	if !ok {
		t.Fatal("expected a cache hit (ok=true) even though the transcript file was deleted")
	}
	if second != first {
		t.Errorf("cached state should match the original: got %+v, want %+v", second, first)
	}
}

func TestTranscriptCosts_StaleCacheRecomputes(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	if _, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now); !ok {
		t.Fatal("expected ok=true on first call")
	}

	// Delete the transcript file: beyond the TTL, the cache is stale
	// and a recompute is attempted, which must now fail.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	_, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(2*time.Minute))
	if ok {
		t.Error("expected a stale cache to trigger a recompute, which should fail with the transcript gone")
	}
}

func TestTranscriptCosts_EmptyCacheDirNeverCaches(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	if _, ok := transcriptCosts("", time.Minute, transcriptPath, now); !ok {
		t.Fatal("expected ok=true on first call")
	}

	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	// cacheDir=="" must always compute fresh (mirroring gitStatus's
	// contract) -- with the transcript gone, this must now fail.
	_, ok := transcriptCosts("", time.Minute, transcriptPath, now.Add(time.Second))
	if ok {
		t.Error("expected cacheDir=\"\" to always recompute rather than cache, but got a stale success")
	}
}

// --- fallback + rendering through Generate -------------------------------

func statuslineDeps(width int) *Dependencies {
	return &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: width},
		IconIndex:     func(int) int { return 0 },
	}
}

func TestGenerate_CostSourceFallback_RendersStdinCost(t *testing.T) {
	deps := statuslineDeps(200)
	deps.CostSource = func(string) (float64, float64, bool) { return 0, 0, false }

	input := Input{}
	input.Model.DisplayName = scenarioModelDisplay
	input.Workspace.ProjectDir = scenarioProjectDir
	input.TranscriptPath = "/nonexistent/session.jsonl"
	input.Cost = CostInput{TotalCostUSD: 1.23}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	s := CreateStatusline(deps)
	out, err := s.Generate(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	stripped := stripAnsi(out)
	if !strings.Contains(stripped, "$1.23") {
		t.Errorf("expected fallback to stdin cost '$1.23'; got %q", stripped)
	}
	if strings.Contains(stripped, "day") {
		t.Errorf("fallback body should be the legacy single-figure body, not the two-part one; got %q", stripped)
	}
}

func TestGenerate_CostSourceSuccess_RendersTwoPartBody(t *testing.T) {
	deps := statuslineDeps(200)
	deps.CostSource = func(string) (float64, float64, bool) { return 8.89, 48.27, true }

	input := Input{}
	input.Model.DisplayName = scenarioModelDisplay
	input.Workspace.ProjectDir = scenarioProjectDir
	input.TranscriptPath = "/nonexistent/session.jsonl"

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	s := CreateStatusline(deps)
	out, err := s.Generate(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	stripped := stripAnsi(out)
	want := CostIcon + "$8.89 ∙ $48.27 day"
	if !strings.Contains(stripped, want) {
		t.Errorf("expected two-part body %q; got %q", want, stripped)
	}
}

func TestGenerate_EmptyTranscriptPath_NeverCallsCostSource(t *testing.T) {
	deps := statuslineDeps(200)
	called := false
	deps.CostSource = func(string) (float64, float64, bool) {
		called = true
		return 8.89, 48.27, true
	}

	input := Input{}
	input.Model.DisplayName = scenarioModelDisplay
	input.Workspace.ProjectDir = scenarioProjectDir
	input.Cost = CostInput{TotalCostUSD: 2.50}
	// TranscriptPath left empty.

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	s := CreateStatusline(deps)
	out, err := s.Generate(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("CostSource must not be called when TranscriptPath is empty")
	}
	stripped := stripAnsi(out)
	if !strings.Contains(stripped, "$2.50") {
		t.Errorf("expected legacy stdin cost '$2.50'; got %q", stripped)
	}
}

// --- rendering: exact bodies + width degradation -------------------------

func TestBuildCostChip_LegacyBodyWhenNotFromTranscript(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{Cost: CostInput{TotalCostUSD: 1.23}}
	got := stripAnsi(s.buildCostChip(data))
	want := CostIcon + "$1.23"
	if !strings.Contains(got, want) {
		t.Errorf("expected legacy body %q; got %q", want, got)
	}
}

func TestBuildMiddleSection_CostChipDegradesToSessionOnlyWhenTight(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		CostFromTranscript: true,
		SessionCostUSD:     8.89,
		DailyCostUSD:       48.27,
	}

	fullChip := s.buildCostChip(data)
	fullWidth := runewidth.StringWidth(stripAnsi(fullChip))
	sessionChip := s.buildSessionOnlyCostChip(data)
	sessionWidth := runewidth.StringWidth(stripAnsi(sessionChip))

	// A width that fits the session-only chip but not the full two-part
	// chip: between sessionWidth and fullWidth.
	width := (sessionWidth + fullWidth) / 2
	if width >= fullWidth {
		t.Fatalf("test setup invariant broken: width %d must be < fullWidth %d", width, fullWidth)
	}

	middle := s.buildMiddleSection(data, width)
	stripped := stripAnsi(middle)
	if !strings.Contains(stripped, "$8.89") {
		t.Errorf("expected session-only figure '$8.89' when the full body doesn't fit; got %q", stripped)
	}
	if strings.Contains(stripped, "48.27") {
		t.Errorf("daily figure should be dropped in the degraded body; got %q", stripped)
	}
}

func TestBuildMiddleSection_CostChipBlanksWhenEvenSessionOnlyTooTight(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		CostFromTranscript: true,
		SessionCostUSD:     8.89,
		DailyCostUSD:       48.27,
	}

	const width = 3
	middle := s.buildMiddleSection(data, width)
	if stripped := stripAnsi(middle); strings.TrimSpace(stripped) != "" {
		t.Errorf("expected a blank middle section when even the session-only body can't fit; got %q", stripped)
	}
}
