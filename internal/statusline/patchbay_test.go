package statusline

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func writePatchbayKey(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/caller.key"
	if err := os.WriteFile(path, []byte(" caller-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func patchbayEnv(url, keyFile string) *MockEnvReader {
	env := NewMockEnvReader()
	env.vars[patchbayURLEnv] = url
	env.vars[patchbayCallerKeyFileEnv] = keyFile
	return env
}

func patchbaySummaryJSON(knownCostNanoUSD, unknownCostRows int64) string {
	return `{
		"since":"2026-08-21T00:00:00Z",
		"until":"2026-08-21T01:00:00Z",
		"context":"",
		"summary":{
			"requests":{"messages":1,"count_tokens":0,"other":0},
			"messages":{
				"finalized":1,"pending":0,
				"known_cost_nano_usd":` + int64String(knownCostNanoUSD) + `,
				"unknown_cost_rows":` + int64String(unknownCostRows) + `,
				"input_tokens":1,"output_tokens":2,"cache_read_tokens":3,"cache_creation_total":4,
				"by_billing_class":{},"by_cost_basis":{}
			}
		}
	}`
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestPatchbayCost_HappyPathSendsKeyAndWindow(t *testing.T) {
	now := time.Date(2026, time.August, 21, 14, 30, 0, 0, time.FixedZone("local", -7*60*60))
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(patchbayCallerKeyHeader); got != "caller-key" {
			t.Errorf("X-Patchbay-Key = %q, want caller-key", got)
		}
		if got := r.URL.Path; got != patchbayUsageSummaryPath {
			t.Errorf("path = %q, want %q", got, patchbayUsageSummaryPath)
		}
		if got, want := r.URL.Query().Get("since"), localMidnight(now).Format(time.RFC3339); got != want {
			t.Errorf("since = %q, want %q", got, want)
		}
		if got, want := r.URL.Query().Get("until"), now.Format(time.RFC3339); got != want {
			t.Errorf("until = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(patchbaySummaryJSON(12_345_000_000, 2)))
	}))
	defer server.Close()

	result := patchbayCost("", time.Minute, now, patchbayEnv(server.URL, keyFile), server.Client())
	if result.Status != patchbayAvailable {
		t.Fatalf("status = %v, want available", result.Status)
	}
	if result.Summary.KnownCostNanoUSD != 12_345_000_000 || result.Summary.UnknownCostRows != 2 {
		t.Errorf("summary = %+v, want known=12345000000 unknown=2", result.Summary)
	}
}

func TestPatchbayCost_ParsesNanoUSDWithoutFloatLoss(t *testing.T) {
	const knownCost = int64(9_007_199_254_740_993)
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(patchbaySummaryJSON(knownCost, 0)))
	}))
	defer server.Close()

	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(server.URL, keyFile), server.Client())
	if result.Status != patchbayAvailable {
		t.Fatalf("status = %v, want available", result.Status)
	}
	if result.Summary.KnownCostNanoUSD != knownCost {
		t.Errorf("known_cost_nano_usd = %d, want %d", result.Summary.KnownCostNanoUSD, knownCost)
	}
}

func TestPatchbayCost_ReachableFailuresAreErrors(t *testing.T) {
	keyFile := writePatchbayKey(t)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "server error", status: http.StatusInternalServerError},
		{name: "garbage", status: http.StatusOK, body: "not json"},
		{name: "wrong shape", status: http.StatusOK, body: `{"summary":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(server.URL, keyFile), server.Client())
			if result.Status != patchbayError {
				t.Errorf("status = %v, want error", result.Status)
			}
		})
	}
}

func TestPatchbayCost_MissingKeyFileIsError(t *testing.T) {
	result := patchbayCost(
		"", time.Minute, time.Now(), patchbayEnv("http://127.0.0.1:4100", "/missing/caller.key"), nil,
	)
	if result.Status != patchbayError {
		t.Errorf("status = %v, want error", result.Status)
	}
}

func TestPatchbayCost_KeyOnlyUsesDefaultURL(t *testing.T) {
	keyFile := writePatchbayKey(t)
	result := patchbayConfigFromEnv(patchbayEnv("", keyFile))
	if result.URL != defaultPatchbayURL {
		t.Errorf("URL = %q, want %q", result.URL, defaultPatchbayURL)
	}
}

func TestPatchbayCost_CacheHitAvoidsSecondRequest(t *testing.T) {
	var requests atomic.Int32
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(patchbaySummaryJSON(1, 0)))
	}))
	defer server.Close()

	now := time.Now()
	env := patchbayEnv(server.URL, keyFile)
	cacheDir := t.TempDir()
	first := patchbayCost(cacheDir, time.Minute, now, env, server.Client())
	second := patchbayCost(cacheDir, time.Minute, now.Add(time.Second), env, server.Client())
	if first.Status != patchbayAvailable || second.Status != patchbayAvailable {
		t.Fatalf("statuses = %v, %v, want both available", first.Status, second.Status)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 after cache hit", got)
	}
}

func TestPatchbayCost_UnavailableIsNegativeCached(t *testing.T) {
	var requests atomic.Int32
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(patchbaySummaryJSON(1, 0)))
	}))
	defer server.Close()

	now := time.Now()
	env := patchbayEnv(server.URL, keyFile)
	cacheDir := t.TempDir()
	client := &http.Client{Timeout: 5 * time.Millisecond}
	first := patchbayCost(cacheDir, time.Minute, now, env, client)
	second := patchbayCost(cacheDir, time.Minute, now.Add(time.Second), env, client)
	if first.Status != patchbayUnavailable || second.Status != patchbayUnavailable {
		t.Fatalf("statuses = %v, %v, want both unavailable", first.Status, second.Status)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 after negative cache hit", got)
	}
}

func TestPatchbayCost_Unconfigured(t *testing.T) {
	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv("", ""), nil)
	if result.Status != patchbayUnconfigured {
		t.Errorf("status = %v, want unconfigured", result.Status)
	}
}

func TestPatchbayCost_RefusedConnectionIsUnavailable(t *testing.T) {
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(url, keyFile), server.Client())
	if result.Status != patchbayUnavailable {
		t.Errorf("status = %v, want unavailable", result.Status)
	}
}

func TestPatchbayCost_RejectsNegativeValues(t *testing.T) {
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(patchbaySummaryJSON(-1, 0)))
	}))
	defer server.Close()

	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(server.URL, keyFile), server.Client())
	if result.Status != patchbayError {
		t.Errorf("status = %v, want error", result.Status)
	}
}

func TestFormatPatchbayUSD(t *testing.T) {
	cases := []struct {
		nanoUSD int64
		want    string
	}{
		{0, "$0.00"},
		{5_000_000, "$0.00"},
		{15_000_000, "$0.02"},
		{25_000_000, "$0.02"},
		{35_000_000, "$0.04"},
		{1_234_500_000, "$1.23"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.want, "$", "dollars"), func(t *testing.T) {
			if got := formatPatchbayUSD(tc.nanoUSD); got != tc.want {
				t.Errorf("formatPatchbayUSD(%d) = %q, want %q", tc.nanoUSD, got, tc.want)
			}
		})
	}
}

func TestBuildCostChip_PatchbayDayCostAndUnknownRows(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := stripAnsi(s.buildCostChip(&CachedData{
		Patchbay: patchbayResult{
			Status:  patchbayAvailable,
			Summary: patchbaySummary{KnownCostNanoUSD: 1_234_500_000, UnknownCostRows: 2},
		},
	}))
	want := CostIcon + "$1.23 day +2?"
	if !strings.Contains(chip, want) {
		t.Errorf("chip = %q, want body %q", chip, want)
	}
}

func TestMiddleChipKind_PatchbayZeroCostSuppressesChip(t *testing.T) {
	data := &CachedData{Patchbay: patchbayResult{Status: patchbayAvailable}}
	if got := middleChipKind(data); got != chipNone {
		t.Errorf("middleChipKind() = %v, want chipNone", got)
	}
}

func TestBuildCostChip_PatchbayUnavailableMarksLegacyFallback(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := stripAnsi(s.buildCostChip(&CachedData{
		Cost:     CostInput{TotalCostUSD: 4.12},
		Patchbay: patchbayResult{Status: patchbayUnavailable},
	}))
	want := CostIcon + "$4.12~"
	if !strings.Contains(chip, want) {
		t.Errorf("chip = %q, want body %q", chip, want)
	}
}

func TestBuildCostChip_PatchbayErrorShowsNoNumbers(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := stripAnsi(s.buildCostChip(&CachedData{
		Cost:     CostInput{TotalCostUSD: 4.12},
		Patchbay: patchbayResult{Status: patchbayError},
	}))
	want := CostIcon + "ERR"
	if !strings.Contains(chip, want) {
		t.Errorf("chip = %q, want body %q", chip, want)
	}
	if strings.Contains(chip, "$") {
		t.Errorf("error chip must contain no cost figures; got %q", chip)
	}
}

func TestBuildCostChip_UnconfiguredIsByteIdenticalToLegacy(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	legacy := s.buildCostChip(&CachedData{Cost: CostInput{TotalCostUSD: 4.12}})
	unconfigured := s.buildCostChip(&CachedData{
		Cost:     CostInput{TotalCostUSD: 4.12},
		Patchbay: patchbayResult{Status: patchbayUnconfigured},
	})
	if unconfigured != legacy {
		t.Errorf("unconfigured Patchbay output changed legacy chip:\n got %q\nwant %q", unconfigured, legacy)
	}
}

func TestBuildMiddleSection_PatchbayChipDropsContextThenBlanks(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		UsedPercentage: 42,
		Patchbay: patchbayResult{
			Status:  patchbayAvailable,
			Summary: patchbaySummary{KnownCostNanoUSD: 1_234_500_000},
		},
	}
	chipWidth := runewidth.StringWidth(stripAnsi(s.buildCostChip(data)))
	middle := stripAnsi(s.buildMiddleSection(data, chipWidth))
	if !strings.Contains(middle, "$1.23 day") {
		t.Errorf("Patchbay chip should survive after context drops; got %q", middle)
	}
	if strings.Contains(middle, ContextIcon) {
		t.Errorf("context should drop before Patchbay chip; got %q", middle)
	}
	if got := strings.TrimSpace(stripAnsi(s.buildMiddleSection(data, 3))); got != "" {
		t.Errorf("Patchbay chip should blank when it cannot fit; got %q", got)
	}
}

func TestComputeData_ConfiguredPatchbayUsesAPIDayCostForForeignRoute(t *testing.T) {
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(patchbaySummaryJSON(1_234_500_000, 0)))
	}))
	defer server.Close()

	deps := statuslineDeps()
	deps.EnvReader = patchbayEnv(server.URL, keyFile)
	deps.PatchbayClient = server.Client()
	costSourceCalled := false
	deps.CostSource = func(string) (float64, float64, bool) {
		costSourceCalled = true
		return 8.89, 48.27, true
	}

	s := CreateStatusline(deps)
	s.input = &Input{}
	s.input.Model.ID = "chatgpt/sol"
	s.input.Model.DisplayName = "Sol"
	s.input.Workspace.ProjectDir = scenarioProjectDir
	s.input.TranscriptPath = "/ignored/session.jsonl"

	data := s.computeData(s.getCurrentDir())
	if data.Patchbay.Status != patchbayAvailable {
		t.Fatalf("Patchbay status = %v, want available", data.Patchbay.Status)
	}
	if costSourceCalled || data.CostFromTranscript {
		t.Errorf("configured Patchbay must not mix transcript pricing into API cost")
	}
	chip := stripAnsi(s.buildCostChip(data))
	if !strings.Contains(chip, "$1.23 day") || strings.Contains(chip, "8.89") {
		t.Errorf("configured foreign route chip = %q, want Patchbay day cost only", chip)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, fmt.Errorf("read failure") }
func (failingReadCloser) Close() error             { return nil }

func TestPatchbayCost_EmptyKeyIsErrorBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	keyFile := t.TempDir() + "/empty.key"
	if err := os.WriteFile(keyFile, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(server.URL, keyFile), server.Client())
	if result.Status != patchbayError {
		t.Errorf("status = %v, want error", result.Status)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0 for an empty caller key", got)
	}
}

func TestPatchbayCost_TransportConfigurationFailuresAreErrors(t *testing.T) {
	keyFile := writePatchbayKey(t)
	cases := []struct {
		name string
		url  string
		key  string
	}{
		{name: "unsupported scheme", url: "ftp://127.0.0.1:4100", key: "caller-key"},
		{name: "invalid header", url: "http://127.0.0.1:4100", key: "bad\nkey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(keyFile, []byte(tc.key), 0o600); err != nil {
				t.Fatal(err)
			}
			result := patchbayCost("", time.Minute, time.Now(), patchbayEnv(tc.url, keyFile), nil)
			if result.Status != patchbayError {
				t.Errorf("status = %v, want error", result.Status)
			}
		})
	}
}

func TestPatchbayCost_BodyReadFailureIsError(t *testing.T) {
	keyFile := writePatchbayKey(t)
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingReadCloser{}}, nil
	})}
	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv("http://patchbay.test", keyFile), client)
	if result.Status != patchbayError {
		t.Errorf("status = %v, want error", result.Status)
	}
}

func TestPatchbayCost_TruncatedBodyIsError(t *testing.T) {
	keyFile := writePatchbayKey(t)
	body := patchbaySummaryJSON(1, 0) + strings.Repeat(" ", patchbayMaxResponseBytes)
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result := patchbayCost("", time.Minute, time.Now(), patchbayEnv("http://patchbay.test", keyFile), client)
	if result.Status != patchbayError {
		t.Errorf("status = %v, want error", result.Status)
	}
}

func TestPatchbayCost_KeyRotationInvalidatesCache(t *testing.T) {
	var requests atomic.Int32
	keyFile := writePatchbayKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(patchbaySummaryJSON(1, 0)))
	}))
	defer server.Close()

	now := time.Now()
	cacheDir := t.TempDir()
	first := patchbayCost(cacheDir, time.Minute, now, patchbayEnv(server.URL, keyFile), server.Client())
	if first.Status != patchbayAvailable {
		t.Fatalf("first status = %v, want available", first.Status)
	}
	if err := os.WriteFile(keyFile, []byte("rotated-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := patchbayCost(
		cacheDir, time.Minute, now.Add(time.Second), patchbayEnv(server.URL, keyFile), server.Client(),
	)
	if second.Status != patchbayAvailable {
		t.Fatalf("second status = %v, want available", second.Status)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("requests = %d, want 2 after caller-key rotation", got)
	}
}

func TestReadFreshCache_FutureMtimeMisses(t *testing.T) {
	cacheDir := t.TempDir()
	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		t.Fatal("cache root should be trusted")
	}
	const cacheName = "future.json"
	writeCache(root, cacheName, patchbayResult{Status: patchbayAvailable})
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("cc-tools-%d", os.Getuid()), cacheName)
	if err := os.Chtimes(cachePath, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	root, trusted = openCacheRoot(cacheDir)
	if !trusted {
		t.Fatal("cache root should reopen as trusted")
	}
	defer func() { _ = root.Close() }()
	var cached patchbayResult
	if readFreshCache(root, cacheName, time.Hour, now, &cached) {
		t.Error("future-mtime cache entry must be a miss")
	}
}

func TestBuildMiddleSection_PatchbayErrorBeatsRateLimit(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		Cost:     CostInput{TotalCostUSD: 4.12},
		Patchbay: patchbayResult{Status: patchbayError},
		RateLimits: &RateLimitsInput{
			FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: time.Now().Add(time.Hour).Unix()},
		},
	}
	middle := stripAnsi(s.buildMiddleSection(data, 100))
	if !strings.Contains(middle, CostIcon+"ERR") {
		t.Errorf("Patchbay error must be visible ahead of rate limits; got %q", middle)
	}
	if strings.Contains(middle, "5h") {
		t.Errorf("rate-limit chip must not hide Patchbay error; got %q", middle)
	}
}

func TestBuildMiddleSection_PatchbayAlarmOmitsLegacyCost(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		Cost:     CostInput{TotalCostUSD: 4.12},
		Patchbay: patchbayResult{Status: patchbayAvailable, Summary: patchbaySummary{KnownCostNanoUSD: 1_234_500_000}},
		RateLimits: &RateLimitsInput{
			FiveHour: &RateLimitWindow{UsedPercentage: 100, ResetsAt: time.Now().Add(time.Hour).Unix()},
		},
	}
	middle := stripAnsi(s.buildMiddleSection(data, 100))
	if !strings.Contains(middle, AlarmIcon+"EXTRA") {
		t.Errorf("Patchbay alarm should retain its non-monetary alarm body; got %q", middle)
	}
	if strings.Contains(middle, "$") {
		t.Errorf("Patchbay alarm must not blend legacy dollars; got %q", middle)
	}
}

func TestBuildMiddleSection_DegradedSessionSqueezeKeepsMarker(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		CostFromTranscript: true,
		SessionCostUSD:     8.89,
		DailyCostUSD:       48.27,
		Patchbay:           patchbayResult{Status: patchbayUnavailable},
	}
	width := runewidth.StringWidth(stripAnsi(s.buildSessionOnlyCostChip(data)))
	middle := stripAnsi(s.buildMiddleSection(data, width))
	if !strings.Contains(middle, "$8.89~") {
		t.Errorf("squeezed degraded chip must retain ~ marker; got %q", middle)
	}
}

func TestRender_UnconfiguredPatchbayLegacyGolden(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	deps := &Dependencies{
		EnvReader:     NewMockEnvReader(),
		IconIndex:     func(int) int { return 0 },
		TerminalWidth: &MockTerminalWidth{width: 200},
	}
	s := CreateStatusline(deps)
	got := s.Render(&CachedData{
		CurrentDir:         "/home/tester/project",
		ModelDisplay:       "Fable",
		TermWidth:          200,
		CostFromTranscript: true,
		SessionCostUSD:     8.89,
		DailyCostUSD:       48.27,
	})
	const want = "\x1b[0m\x1b[38;2;180;190;254m\x1b[48;2;180;190;254m\x1b[38;2;30;30;46m ~/project \x1b[0m\x1b[48;2;137;220;235m\x1b[38;2;180;190;254m\x1b[0m\x1b[48;2;137;220;235m\x1b[38;2;30;30;46m \U000f06a9 Fable \x1b[0m\x1b[38;2;137;220;235m\x1b[0m                                                                          \x1b[38;2;116;199;236m\x1b[0m\x1b[48;2;116;199;236m\x1b[38;2;30;30;46m \U000f0210 $8.89 ∙ $48.27 day \x1b[0m\x1b[38;2;116;199;236m\x1b[0m                                                                           "
	if got != want {
		t.Errorf("legacy statusline golden mismatch:\n got %q\nwant %q", got, want)
	}
}
