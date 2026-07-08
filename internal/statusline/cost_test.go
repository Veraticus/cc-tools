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

// plainRowMsgID is the fixed message.id every plainRow fixture uses: no
// "msg_bdrk" prefix, so internal/cost's row-cost logic treats it as an
// anthropic-backend row — priced only when the subscribed flag passed to
// transcriptCosts/computeTranscriptCosts is false.
const plainRowMsgID = "msg_01plain"

// plainRowInputTokens is the fixed input-token count every plainRow
// fixture uses; only the amount's positivity (when priced) is asserted by
// these tests, not its exact value.
const plainRowInputTokens = 1000

// plainRow renders one minimal billable anthropic-backend transcript
// JSONL row (no "msg_bdrk" prefix): its cost is nonzero when subscribed
// is false and exactly zero when subscribed is true. timestamp must be
// an RFC3339 string.
func plainRow(timestamp string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"requestId":%q,`+
			`"message":{"id":%q,"model":"claude-fable-5",`+
			`"usage":{"input_tokens":%d,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		timestamp, plainRowMsgID, plainRowMsgID, plainRowInputTokens,
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
// and returns the transcript path.
func setupTranscriptFixture(t *testing.T, rows ...string) string {
	t.Helper()
	tmp := t.TempDir()
	profileDir := filepath.Join(tmp, "profile")
	projectDir := filepath.Join(profileDir, "projects", "slug")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(projectDir, "session.jsonl")
	writeTranscript(t, transcriptPath, rows...)
	return transcriptPath
}

// --- path derivation + end-to-end compute --------------------------------

func TestTranscriptCosts_ComputesFromRealFixture(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	state, ok := transcriptCosts("", 0, transcriptPath, now, false)
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

	// No transcript file written at all.
	transcriptPath := filepath.Join(projectDir, "session.jsonl")

	_, ok := transcriptCosts("", 0, transcriptPath, time.Now(), false)
	if ok {
		t.Error("expected ok=false when the transcript file doesn't exist")
	}
}

// --- subscribed pricing ---------------------------------------------------

// TestComputeTranscriptCosts_SubscribedPricesBedrockZeroesPlainRows reuses
// internal/cost/testdata/real_shape.jsonl (two msg_bdrk rows plus one
// plain msg_ row) to prove the subscribed flag now threaded through from
// stdin's rate_limits presence, not a credentials file, is what decides
// pricing: with subscribed=false every row (including the plain one)
// prices, so the total is higher than with subscribed=true, where only
// the two msg_bdrk rows still price.
func TestComputeTranscriptCosts_SubscribedPricesBedrockZeroesPlainRows(t *testing.T) {
	now := time.Now()
	transcriptPath := filepath.Join("..", "cost", "testdata", "real_shape.jsonl")

	unsubscribed, ok := transcriptCosts("", 0, transcriptPath, now, false)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=false")
	}
	if unsubscribed.SessionUSD <= 0 {
		t.Errorf("expected positive SessionUSD under subscribed=false, got %v", unsubscribed.SessionUSD)
	}

	subscribed, ok := transcriptCosts("", 0, transcriptPath, now, true)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=true")
	}
	if subscribed.SessionUSD <= 0 {
		t.Errorf("expected msg_bdrk rows to remain priced under subscribed=true, got %v", subscribed.SessionUSD)
	}
	if subscribed.SessionUSD >= unsubscribed.SessionUSD {
		t.Errorf(
			"expected subscribed=true to zero the plain msg_ row's contribution, lowering session cost: "+
				"subscribed=%v unsubscribed=%v",
			subscribed.SessionUSD, unsubscribed.SessionUSD,
		)
	}
}

// TestComputeTranscriptCosts_SubscribedZeroesPlainRow isolates the plain-row
// case with its own single-row fixture: subscribed=true must price it at
// exactly $0, not merely "less than before".
func TestComputeTranscriptCosts_SubscribedZeroesPlainRow(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, plainRow(timestamp))

	unsubscribed, ok := transcriptCosts("", 0, transcriptPath, now, false)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=false")
	}
	if unsubscribed.SessionUSD <= 0 {
		t.Errorf("expected positive SessionUSD under subscribed=false, got %v", unsubscribed.SessionUSD)
	}

	subscribed, ok := transcriptCosts("", 0, transcriptPath, now, true)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=true")
	}
	if subscribed.SessionUSD != 0 {
		t.Errorf(
			"expected exactly $0 SessionUSD for a plain-row transcript under subscribed=true, got %v",
			subscribed.SessionUSD,
		)
	}
}

// --- caching --------------------------------------------------------------

func TestTranscriptCosts_CacheHitSkipsRecompute(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	first, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now, false)
	if !ok {
		t.Fatal("expected ok=true on first (cold-cache) call")
	}

	// Delete the transcript file itself: if the second call recomputes
	// rather than reading the cache, it will fail (ok=false) because
	// the file no longer exists.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	second, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(time.Second), false)
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

	if _, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now, false); !ok {
		t.Fatal("expected ok=true on first call")
	}

	// Delete the transcript file: beyond the TTL, the cache is stale
	// and a recompute is attempted, which must now fail.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	_, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(2*time.Minute), false)
	if ok {
		t.Error("expected a stale cache to trigger a recompute, which should fail with the transcript gone")
	}
}

// --- split cache keys -------------------------------------------------

func TestTranscriptCosts_SessionAndDailyCachedUnderSeparateKeys(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))
	projectsDir := filepath.Dir(filepath.Dir(transcriptPath))

	if _, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now, false); !ok {
		t.Fatal("expected ok=true on first (cold-cache) call")
	}

	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		t.Fatal("expected the cache root to verify as trusted")
	}
	defer func() { _ = root.Close() }()

	if _, err := root.Stat(costSessionCacheName(transcriptPath, false)); err != nil {
		t.Errorf("expected a session cache entry keyed by transcriptPath: %v", err)
	}
	if _, err := root.Stat(costDailyCacheName(projectsDir, false)); err != nil {
		t.Errorf("expected a daily cache entry keyed by projectsDir: %v", err)
	}
}

func TestTranscriptCosts_ConcurrentSessionSharesDailyCacheEntry(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)

	// Two distinct session transcripts under the SAME project (same
	// projects dir), as two concurrent Claude Code sessions in one
	// project would produce.
	tmp := t.TempDir()
	profileDir := filepath.Join(tmp, "profile")
	projectDir := filepath.Join(profileDir, "projects", "slug")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}

	transcriptA := filepath.Join(projectDir, "session-a.jsonl")
	transcriptB := filepath.Join(projectDir, "session-b.jsonl")
	writeTranscript(t, transcriptA, bedrockRow(timestamp))
	writeTranscript(t, transcriptB, bedrockRow(timestamp))

	if _, ok := transcriptCosts(cacheDir, time.Minute, transcriptA, now, false); !ok {
		t.Fatal("expected ok=true computing session A")
	}

	// Delete session B's own transcript file: session B has never been
	// scanned before (its session-keyed cache entry doesn't exist), so if
	// the shared daily entry weren't reused, this call would need to
	// walk the projects dir itself; deleting transcriptB doesn't affect
	// that walk (transcriptB may or may not still be scanned as part of
	// it), but the point under test is the daily cache FILE's identity —
	// verified directly below via costDailyCacheName equality, which is
	// what actually proves session A and session B share one entry.
	sessionAName := costSessionCacheName(transcriptA, false)
	sessionBName := costSessionCacheName(transcriptB, false)
	dailyNameA := costDailyCacheName(filepath.Dir(filepath.Dir(transcriptA)), false)
	dailyNameB := costDailyCacheName(filepath.Dir(filepath.Dir(transcriptB)), false)

	if sessionAName == sessionBName {
		t.Fatal("test setup invariant broken: distinct transcript paths must not share a session cache key")
	}
	if dailyNameA != dailyNameB {
		t.Fatalf(
			"sessions in the same project must share one daily cache key: got %q and %q",
			dailyNameA, dailyNameB,
		)
	}
}

func TestTranscriptCosts_SessionHitDailyMissRecomputesBoth(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	if _, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now, false); !ok {
		t.Fatal("expected ok=true on first (cold-cache) call")
	}

	// Remove ONLY the daily-keyed entry, leaving the session-keyed entry
	// fresh: a partial hit (session cached, daily missing) must still
	// trigger a full recompute of BOTH figures, not a session-only reuse.
	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		t.Fatal("expected the cache root to verify as trusted")
	}
	projectsDir := filepath.Dir(filepath.Dir(transcriptPath))
	if err := root.Remove(costDailyCacheName(projectsDir, false)); err != nil {
		t.Fatal(err)
	}
	_ = root.Close()

	// Deleting the transcript file proves recompute was actually
	// attempted (and must fail) rather than the session-only cache entry
	// being silently reused for a "good enough" answer.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	_, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(time.Second), false)
	if ok {
		t.Error("expected a daily-cache miss to force a full recompute, which must fail with the transcript gone")
	}
}

// --- subscribed cache isolation --------------------------------------

// TestTranscriptCosts_SubscribedCacheEntryNotServedToUnsubscribedCaller
// proves the cache-key decision documented on transcriptCosts: folding
// subscribed into the cache key (rather than TTL-tolerance) means a
// subscribed=true entry (correctly $0 for a plain-row transcript) is
// never read back by a subsequent subscribed=false call. The transcript
// file is deliberately left in place (not deleted) — if the two
// subscribed values wrongly shared a cache entry, the second call would
// silently return the first's $0 result instead of recomputing.
func TestTranscriptCosts_SubscribedCacheEntryNotServedToUnsubscribedCaller(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, plainRow(timestamp))

	subscribedState, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now, true)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=true")
	}
	if subscribedState.SessionUSD != 0 {
		t.Fatalf("test setup invariant broken: expected $0 under subscribed=true, got %v", subscribedState.SessionUSD)
	}

	unsubscribedState, ok := transcriptCosts(cacheDir, time.Minute, transcriptPath, now.Add(time.Second), false)
	if !ok {
		t.Fatal("expected ok=true computing subscribed=false")
	}
	if unsubscribedState.SessionUSD <= 0 {
		t.Errorf(
			"expected a nonzero session cost under subscribed=false even though a subscribed=true cache entry exists, got %v",
			unsubscribedState.SessionUSD,
		)
	}
}

func TestTranscriptCosts_EmptyCacheDirNeverCaches(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, bedrockRow(timestamp))

	if _, ok := transcriptCosts("", time.Minute, transcriptPath, now, false); !ok {
		t.Fatal("expected ok=true on first call")
	}

	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}

	// cacheDir=="" must always compute fresh (mirroring gitStatus's
	// contract) -- with the transcript gone, this must now fail.
	_, ok := transcriptCosts("", time.Minute, transcriptPath, now.Add(time.Second), false)
	if ok {
		t.Error("expected cacheDir=\"\" to always recompute rather than cache, but got a stale success")
	}
}

// --- fallback + rendering through Generate -------------------------------

// statuslineDepsWidth is the fixed terminal width every statuslineDeps
// fixture uses: wide enough that none of this file's cost-chip tests
// need to reason about width-driven degradation.
const statuslineDepsWidth = 200

func statuslineDeps() *Dependencies {
	return &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: statuslineDepsWidth},
		IconIndex:     func(int) int { return 0 },
	}
}

// TestComputeData_RateLimitsPresence_TogglesTranscriptSubscribedPricing
// drives the real (non-injected) costSource path end to end — no
// Dependencies.CostSource fixture, so this exercises the actual
// RateLimits-derived subscribed flag threaded through transcriptCosts.
// A plain-row transcript prices nonzero when stdin carries no rate_limits
// object and exactly $0 when it does, matching the same subscribed
// semantics TestComputeTranscriptCosts_SubscribedZeroesPlainRow pins at
// the transcriptCosts level.
func TestComputeData_RateLimitsPresence_TogglesTranscriptSubscribedPricing(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	transcriptPath := setupTranscriptFixture(t, plainRow(timestamp))

	deps := statuslineDeps()
	deps.Now = func() time.Time { return now }
	// CacheDir left empty: transcriptCosts always computes fresh, so this
	// test never touches the on-disk cache.

	buildInput := func(withRateLimits bool) *Input {
		input := &Input{}
		input.Model.DisplayName = scenarioModelDisplay
		input.Workspace.ProjectDir = scenarioProjectDir
		input.TranscriptPath = transcriptPath
		if withRateLimits {
			input.RateLimits = &RateLimitsInput{
				FiveHour: &RateLimitWindow{UsedPercentage: 1, ResetsAt: now.Unix()},
			}
		}
		return input
	}

	withoutRL := CreateStatusline(deps)
	withoutRL.input = buildInput(false)
	dataWithoutRL := withoutRL.computeData(withoutRL.getCurrentDir())
	if !dataWithoutRL.CostFromTranscript {
		t.Fatal("expected transcript-derived cost to succeed without rate_limits")
	}
	if dataWithoutRL.SessionCostUSD <= 0 {
		t.Errorf(
			"expected nonzero session cost without rate_limits (subscribed=false), got %v",
			dataWithoutRL.SessionCostUSD,
		)
	}

	withRL := CreateStatusline(deps)
	withRL.input = buildInput(true)
	dataWithRL := withRL.computeData(withRL.getCurrentDir())
	if !dataWithRL.CostFromTranscript {
		t.Fatal("expected transcript-derived cost to succeed with rate_limits present")
	}
	if dataWithRL.SessionCostUSD != 0 {
		t.Errorf(
			"expected exactly $0 session cost with rate_limits present (subscribed=true), got %v",
			dataWithRL.SessionCostUSD,
		)
	}
}

func TestGenerate_CostSourceFallback_RendersStdinCost(t *testing.T) {
	deps := statuslineDeps()
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
	deps := statuslineDeps()
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
	deps := statuslineDeps()
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
