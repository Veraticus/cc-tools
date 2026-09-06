package statusline

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"time"

	"github.com/joshsymonds/steward/internal/cost"
)

// costCacheKeyPrefix distinguishes transcript-cost cache entries from
// the git-status cache entries sharing the same cache root/subdir.
// costSessionCacheKeyPrefix and costDailyCacheKeyPrefix further split
// the session and daily entries under two different keys — session
// keyed by transcriptPath, daily keyed by projectsDir — so that
// concurrent sessions in the same project share one daily-scan cache
// entry instead of each paying for their own.
const (
	costSessionCacheKeyPrefix = "cost-session-"
	costDailyCacheKeyPrefix   = "cost-daily-"
)

// costState is the combined session+daily result returned to
// statusline.go's caller.
type costState struct {
	SessionUSD float64 `json:"session_usd"`
	DailyUSD   float64 `json:"daily_usd"`
}

// sessionCacheEntry is the on-disk shape of the session-keyed cache
// entry.
type sessionCacheEntry struct {
	SessionUSD float64 `json:"session_usd"`
}

// dailyCacheEntry is the on-disk shape of the projectsDir-keyed cache
// entry.
type dailyCacheEntry struct {
	DailyUSD float64 `json:"daily_usd"`
}

// transcriptCosts returns transcript-derived costs for transcriptPath,
// cached under cacheDir with ttl (same machinery as the git chip).
// ok=false on any failure — caller falls back to stdin cost.
//
// transcriptPath is expected in the shape
// "<profile>/projects/<slug>/<session>.jsonl": projectsDir is its
// grandparent directory.
//
// subscribed is the caller's ground truth for whether anthropic-backend
// rows price at $0 (see statusline.go's costSource, which derives it from
// whether stdin's rate_limits data is available) — it is folded into the
// cache key (see costSessionCacheName/costDailyCacheName) so a cache entry
// computed under one value is never served back under the other.
//
// The session and daily figures are cached under two separate keys —
// session by transcriptPath, daily by projectsDir — rather than one
// combined entry: a hit on both skips the scan entirely, but a miss on
// either recomputes both together through cost.Costs' single-pass scan
// (there is no cheaper way to refresh just one side, since the
// session-file scan feeds the daily total too) and (re)writes both
// entries.
func transcriptCosts(
	cacheDir string, ttl time.Duration, transcriptPath string, now time.Time, subscribed bool,
) (costState, bool) {
	projectsDir := filepath.Dir(filepath.Dir(transcriptPath))

	if cacheDir == "" {
		return computeTranscriptCosts(transcriptPath, projectsDir, now, subscribed)
	}

	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		return computeTranscriptCosts(transcriptPath, projectsDir, now, subscribed)
	}
	defer func() { _ = root.Close() }()

	sessionName := costSessionCacheName(transcriptPath, subscribed)
	dailyName := costDailyCacheName(projectsDir, subscribed)

	var cachedSession sessionCacheEntry
	var cachedDaily dailyCacheEntry
	sessionHit := readFreshCache(root, sessionName, ttl, now, &cachedSession)
	dailyHit := readFreshCache(root, dailyName, ttl, now, &cachedDaily)
	if sessionHit && dailyHit {
		return costState{SessionUSD: cachedSession.SessionUSD, DailyUSD: cachedDaily.DailyUSD}, true
	}

	state, ok := computeTranscriptCosts(transcriptPath, projectsDir, now, subscribed)
	if ok {
		writeCache(root, sessionName, sessionCacheEntry{SessionUSD: state.SessionUSD})
		writeCache(root, dailyName, dailyCacheEntry{DailyUSD: state.DailyUSD})
	}
	return state, ok
}

// computeTranscriptCosts computes costState fresh (no cache) from
// transcriptPath and its already-derived projectsDir: cost.Costs
// computes both the session and daily totals in a single pass under
// the caller-supplied subscribed flag. A scan failure is treated as a
// total failure (ok=false) — a partial session-only or daily-only
// result would be confusing on a status bar.
func computeTranscriptCosts(transcriptPath, projectsDir string, now time.Time, subscribed bool) (costState, bool) {
	sessionUSD, dailyUSD, err := cost.Costs(transcriptPath, projectsDir, now, subscribed)
	if err != nil {
		return costState{}, false
	}

	return costState{SessionUSD: sessionUSD, DailyUSD: dailyUSD}, true
}

// cacheKeyBytes appends a one-byte subscribed marker to key, so that
// costSessionCacheName/costDailyCacheName hash subscribed=true and
// subscribed=false inputs to different cache files even when key
// itself (transcriptPath or projectsDir) is identical.
func cacheKeyBytes(key string, subscribed bool) []byte {
	b := []byte(key)
	if subscribed {
		return append(b, 1)
	}
	return append(b, 0)
}

// costSessionCacheName returns the cache file name for transcriptPath's
// session entry under subscribed:
// "cost-session-<hex sha256 prefix of transcriptPath+subscribed>.json".
func costSessionCacheName(transcriptPath string, subscribed bool) string {
	sum := sha256.Sum256(cacheKeyBytes(transcriptPath, subscribed))
	return costSessionCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}

// costDailyCacheName returns the cache file name for projectsDir's daily
// entry under subscribed:
// "cost-daily-<hex sha256 prefix of projectsDir+subscribed>.json". Keying
// by projectsDir (rather than transcriptPath) is what lets concurrent
// sessions within the same project share one daily-scan cache entry.
func costDailyCacheName(projectsDir string, subscribed bool) string {
	sum := sha256.Sum256(cacheKeyBytes(projectsDir, subscribed))
	return costDailyCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}
