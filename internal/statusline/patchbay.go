package statusline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	patchbayURLEnv            = "CC_TOOLS_PATCHBAY_URL"
	patchbayCallerKeyFileEnv  = "PATCHBAY_CALLER_KEY_FILE"
	defaultPatchbayURL        = "http://127.0.0.1:4100"
	patchbayUsageSummaryPath  = "/_patchbay/usage/summary"
	patchbayCallerKeyHeader   = "X-Patchbay-Key"
	patchbayTimeout           = 500 * time.Millisecond
	patchbayCacheKeyPrefix    = "patchbay-usage-"
	patchbayMaxResponseBytes  = 1 << 20
	patchbayResponseReadLimit = patchbayMaxResponseBytes + 1
)

type patchbayStatus uint8

const (
	patchbayUnconfigured patchbayStatus = iota
	patchbayAvailable
	patchbayUnavailable
	patchbayError
)

// patchbaySummary is the portion of the usage summary needed to render the
// daily cost chip. Money remains integer nano-USD until formatting.
type patchbaySummary struct {
	KnownCostNanoUSD int64 `json:"known_cost_nano_usd"`
	UnknownCostRows  int64 `json:"unknown_cost_rows"`
}

type patchbayResult struct {
	Status  patchbayStatus  `json:"status"`
	Summary patchbaySummary `json:"summary"`
}

type patchbayConfig struct {
	URL     string
	KeyFile string
}

// patchbayConfigFromEnv resolves the optional Patchbay configuration. A caller
// key alone enables Patchbay using its local loopback default; both values empty
// leave the statusline on its transcript-cost path.
func patchbayConfigFromEnv(env EnvReader) patchbayConfig {
	if env == nil {
		return patchbayConfig{}
	}
	config := patchbayConfig{
		URL:     strings.TrimSpace(env.Get(patchbayURLEnv)),
		KeyFile: strings.TrimSpace(env.Get(patchbayCallerKeyFileEnv)),
	}
	if config.URL == "" && config.KeyFile != "" {
		config.URL = defaultPatchbayURL
	}
	return config
}

// patchbayCost fetches today's final Patchbay cost. It caches successful and
// transport-unavailable results with the regular statusline TTL, while keeping
// malformed responses and authentication failures visible on every render.
func patchbayCost(
	cacheDir string,
	ttl time.Duration,
	now time.Time,
	env EnvReader,
	client *http.Client,
) patchbayResult {
	config := patchbayConfigFromEnv(env)
	if config.URL == "" && config.KeyFile == "" {
		return patchbayResult{Status: patchbayUnconfigured}
	}
	if config.KeyFile == "" || !validPatchbayBaseURL(config.URL) {
		return patchbayResult{Status: patchbayError}
	}
	key, err := os.ReadFile(config.KeyFile)
	if err != nil {
		return patchbayResult{Status: patchbayError}
	}
	callerKey := strings.TrimSpace(string(key))
	if callerKey == "" {
		return patchbayResult{Status: patchbayError}
	}
	keyInfo, err := os.Stat(config.KeyFile)
	if err != nil {
		return patchbayResult{Status: patchbayError}
	}

	windowStart := localMidnight(now)
	if cacheDir == "" {
		return fetchPatchbayCost(config.URL, callerKey, windowStart, now, client)
	}

	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		return fetchPatchbayCost(config.URL, callerKey, windowStart, now, client)
	}
	defer func() { _ = root.Close() }()

	cacheName := patchbayCacheName(config.URL, config.KeyFile, keyInfo, windowStart)
	var cached patchbayResult
	if readFreshCache(root, cacheName, ttl, now, &cached) && patchbayCacheable(cached) {
		return cached
	}

	var stale patchbayResult
	hasStale := readCache(root, cacheName, &stale) && patchbayCacheable(stale)
	lock := tryCacheRefillLock(root, cacheName)
	switch lock.status {
	case cacheRefillLockHeld:
		if hasStale {
			return stale
		}
		return fetchAndCachePatchbayCost(root, cacheName, config.URL, callerKey, windowStart, now, client)
	case cacheRefillLockUnavailable:
		return fetchAndCachePatchbayCost(root, cacheName, config.URL, callerKey, windowStart, now, client)
	case cacheRefillLockAcquired:
		defer releaseCacheRefillLock(lock.file)

		// Another renderer may have refreshed the cache while this one waited to
		// acquire the advisory lock.
		if readFreshCache(root, cacheName, ttl, now, &cached) && patchbayCacheable(cached) {
			return cached
		}
		return fetchAndCachePatchbayCost(root, cacheName, config.URL, callerKey, windowStart, now, client)
	default:
		return fetchAndCachePatchbayCost(root, cacheName, config.URL, callerKey, windowStart, now, client)
	}
}

func patchbayCacheable(result patchbayResult) bool {
	return result.Status == patchbayAvailable || result.Status == patchbayUnavailable
}

func fetchAndCachePatchbayCost(
	root *os.Root,
	cacheName, baseURL, callerKey string,
	windowStart, now time.Time,
	client *http.Client,
) patchbayResult {
	result := fetchPatchbayCost(baseURL, callerKey, windowStart, now, client)
	if patchbayCacheable(result) {
		writeCache(root, cacheName, result)
	}
	return result
}

func localMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// patchbayCacheName binds an entry to a non-secret caller-key file identity.
// Key contents never enter a cache filename: the file's path, modification time,
// and size invalidate cached summaries after key rotation without making the
// cache name an oracle for secret material.
func patchbayCacheName(baseURL, keyPath string, keyInfo os.FileInfo, windowStart time.Time) string {
	identity := baseURL + "\x00" + keyPath + "\x00" +
		strconv.FormatInt(keyInfo.ModTime().UnixNano(), 10) + "\x00" +
		strconv.FormatInt(keyInfo.Size(), 10) + "\x00" + windowStart.Format(time.RFC3339)
	sum := sha256.Sum256([]byte(identity))
	return patchbayCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}

func fetchPatchbayCost(
	baseURL, key string,
	windowStart, now time.Time,
	client *http.Client,
) patchbayResult {
	endpoint, err := patchbayUsageSummaryURL(baseURL, windowStart, now)
	if err != nil {
		return patchbayResult{Status: patchbayError}
	}

	ctx, cancel := context.WithTimeout(context.Background(), patchbayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return patchbayResult{Status: patchbayError}
	}
	req.Header.Set(patchbayCallerKeyHeader, key)

	requestClient := patchbayHTTPClient(client)
	resp, err := requestClient.Do(req)
	if err != nil {
		if patchbayTransportUnavailable(err) {
			return patchbayResult{Status: patchbayUnavailable}
		}
		return patchbayResult{Status: patchbayError}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return patchbayResult{Status: patchbayError}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, patchbayResponseReadLimit))
	if err != nil || len(body) > patchbayMaxResponseBytes {
		return patchbayResult{Status: patchbayError}
	}
	summary, err := parsePatchbaySummary(body)
	if err != nil {
		return patchbayResult{Status: patchbayError}
	}
	return patchbayResult{Status: patchbayAvailable, Summary: summary}
}

// patchbayTransportUnavailable admits only request failures that clearly mean
// the gateway could not be reached. Request construction, protocol, and header
// failures are configuration errors and must remain visible instead of falling
// back to transcript accounting.
func patchbayTransportUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func patchbayUsageSummaryURL(baseURL string, since, until time.Time) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || !validPatchbayURL(parsed) {
		return "", fmt.Errorf("invalid Patchbay URL")
	}
	parsed.Path = patchbayUsageSummaryPath
	parsed.RawPath = ""
	query := parsed.Query()
	query.Set("since", since.Format(time.RFC3339))
	query.Set("until", until.Format(time.RFC3339))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func validPatchbayBaseURL(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	return err == nil && validPatchbayURL(parsed)
}

func validPatchbayURL(parsed *url.URL) bool {
	if parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func patchbayHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: patchbayTimeout}
	}
	configuredClient := *client
	configuredClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &configuredClient
}

type patchbayUsageSummaryResponse struct {
	Summary *patchbayUsageSummaryWire `json:"summary"`
}

type patchbayUsageSummaryWire struct {
	Requests *patchbayRequestCountsWire  `json:"requests"`
	Messages *patchbayMessageSummaryWire `json:"messages"`
}

type patchbayRequestCountsWire struct {
	Messages    *int64 `json:"messages"`
	CountTokens *int64 `json:"count_tokens"`
	Other       *int64 `json:"other"`
}

type patchbayMessageSummaryWire struct {
	Finalized          *int64                     `json:"finalized"`
	Pending            *int64                     `json:"pending"`
	KnownCostNanoUSD   *int64                     `json:"known_cost_nano_usd"`
	UnknownCostRows    *int64                     `json:"unknown_cost_rows"`
	InputTokens        *int64                     `json:"input_tokens"`
	OutputTokens       *int64                     `json:"output_tokens"`
	CacheReadTokens    *int64                     `json:"cache_read_tokens"`
	CacheCreationTotal *int64                     `json:"cache_creation_total"`
	ByBillingClass     map[string]json.RawMessage `json:"by_billing_class"`
	ByCostBasis        map[string]json.RawMessage `json:"by_cost_basis"`
}

func parsePatchbaySummary(body []byte) (patchbaySummary, error) {
	var response patchbayUsageSummaryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return patchbaySummary{}, fmt.Errorf("decoding Patchbay summary: %w", err)
	}
	return patchbaySummaryFromResponse(response)
}

func patchbaySummaryFromResponse(response patchbayUsageSummaryResponse) (patchbaySummary, error) {
	if response.Summary == nil || response.Summary.Requests == nil || response.Summary.Messages == nil {
		return patchbaySummary{}, fmt.Errorf("patchbay summary has no messages shape")
	}
	requests := response.Summary.Requests
	messages := response.Summary.Messages
	if !completePatchbaySummary(requests, messages) {
		return patchbaySummary{}, fmt.Errorf("patchbay summary has incomplete messages shape")
	}
	if !nonNegativePatchbayCounts(requests, messages) {
		return patchbaySummary{}, fmt.Errorf("patchbay summary has negative counts")
	}
	return patchbaySummary{
		KnownCostNanoUSD: *messages.KnownCostNanoUSD,
		UnknownCostRows:  *messages.UnknownCostRows,
	}, nil
}

func completePatchbaySummary(requests *patchbayRequestCountsWire, messages *patchbayMessageSummaryWire) bool {
	return requests.Messages != nil && requests.CountTokens != nil && requests.Other != nil &&
		messages.Finalized != nil && messages.Pending != nil &&
		messages.KnownCostNanoUSD != nil && messages.UnknownCostRows != nil &&
		messages.InputTokens != nil && messages.OutputTokens != nil &&
		messages.CacheReadTokens != nil && messages.CacheCreationTotal != nil &&
		messages.ByBillingClass != nil && messages.ByCostBasis != nil
}

func nonNegativePatchbayCounts(requests *patchbayRequestCountsWire, messages *patchbayMessageSummaryWire) bool {
	values := []*int64{
		requests.Messages, requests.CountTokens, requests.Other,
		messages.Finalized, messages.Pending, messages.KnownCostNanoUSD, messages.UnknownCostRows,
		messages.InputTokens, messages.OutputTokens, messages.CacheReadTokens, messages.CacheCreationTotal,
	}
	for _, value := range values {
		if *value < 0 {
			return false
		}
	}
	return true
}

// formatPatchbayUSD rounds nano-USD to cents with bankers' (half-even)
// rounding, then formats the result without a floating-point conversion.
func formatPatchbayUSD(nanoUSD int64) string {
	const (
		nanoUSDPerCent int64 = 10_000_000
		centsPerDollar int64 = 100
		halfDivisor          = 2
	)
	cents := nanoUSD / nanoUSDPerCent
	remainder := nanoUSD % nanoUSDPerCent
	half := nanoUSDPerCent / halfDivisor
	if remainder > half || (remainder == half && cents%halfDivisor != 0) {
		cents++
	}
	return fmt.Sprintf("$%d.%02d", cents/centsPerDollar, cents%centsPerDollar)
}
