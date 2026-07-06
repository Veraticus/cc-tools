package statusline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Veraticus/cc-tools/internal/cost"
)

// credentialsFileName is the OAuth credentials file that sits at the
// profile root (the parent of the "projects" directory transcripts
// live under). Its .claudeAiOauth.subscriptionType field is the only
// signal used to decide anthropic-backend pricing (see readSubscribed).
const credentialsFileName = ".credentials.json"

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
// grandparent directory, and profileDir (projectsDir's parent) is
// where the OAuth credentials file lives.
//
// The session and daily figures are cached under two separate keys —
// session by transcriptPath, daily by projectsDir — rather than one
// combined entry: a hit on both skips the scan entirely, but a miss on
// either recomputes both together through cost.Costs' single-pass scan
// (there is no cheaper way to refresh just one side, since the
// session-file scan feeds the daily total too) and (re)writes both
// entries.
func transcriptCosts(cacheDir string, ttl time.Duration, transcriptPath string, now time.Time) (costState, bool) {
	projectsDir := filepath.Dir(filepath.Dir(transcriptPath))

	if cacheDir == "" {
		return computeTranscriptCosts(transcriptPath, projectsDir, now)
	}

	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		return computeTranscriptCosts(transcriptPath, projectsDir, now)
	}
	defer func() { _ = root.Close() }()

	sessionName := costSessionCacheName(transcriptPath)
	dailyName := costDailyCacheName(projectsDir)

	var cachedSession sessionCacheEntry
	var cachedDaily dailyCacheEntry
	sessionHit := readFreshCache(root, sessionName, ttl, now, &cachedSession)
	dailyHit := readFreshCache(root, dailyName, ttl, now, &cachedDaily)
	if sessionHit && dailyHit {
		return costState{SessionUSD: cachedSession.SessionUSD, DailyUSD: cachedDaily.DailyUSD}, true
	}

	state, ok := computeTranscriptCosts(transcriptPath, projectsDir, now)
	if ok {
		writeCache(root, sessionName, sessionCacheEntry{SessionUSD: state.SessionUSD})
		writeCache(root, dailyName, dailyCacheEntry{DailyUSD: state.DailyUSD})
	}
	return state, ok
}

// computeTranscriptCosts computes costState fresh (no cache) from
// transcriptPath and its already-derived projectsDir: subscription
// status is read from the profile's credentials file, then
// cost.Costs computes both the session and daily totals in a single
// pass under that subscribed flag. A scan failure is treated as a
// total failure (ok=false) — a partial session-only or daily-only
// result would be confusing on a status bar.
func computeTranscriptCosts(transcriptPath, projectsDir string, now time.Time) (costState, bool) {
	profileDir := filepath.Dir(projectsDir)
	subscribed := readSubscribed(profileDir)

	sessionUSD, dailyUSD, err := cost.Costs(transcriptPath, projectsDir, now, subscribed)
	if err != nil {
		return costState{}, false
	}

	return costState{SessionUSD: sessionUSD, DailyUSD: dailyUSD}, true
}

// readSubscribed reports whether profileDir's credentials file
// carries a non-empty .claudeAiOauth.subscriptionType, meaning the
// caller is on a Claude subscription (anthropic-backend rows are
// free). Any failure to find or parse the file — missing, unreadable,
// malformed — is treated as "not subscribed": conservative for a cost
// display, since it means list-rate dollars are shown rather than
// silently zeroed.
func readSubscribed(profileDir string) bool {
	root, err := os.OpenRoot(profileDir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(credentialsFileName)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}

	var creds struct {
		ClaudeAiOauth struct {
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if unmarshalErr := json.Unmarshal(data, &creds); unmarshalErr != nil {
		return false
	}
	return creds.ClaudeAiOauth.SubscriptionType != ""
}

// costSessionCacheName returns the cache file name for transcriptPath's
// session entry: "cost-session-<hex sha256 prefix of transcriptPath>.json".
func costSessionCacheName(transcriptPath string) string {
	sum := sha256.Sum256([]byte(transcriptPath))
	return costSessionCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}

// costDailyCacheName returns the cache file name for projectsDir's daily
// entry: "cost-daily-<hex sha256 prefix of projectsDir>.json". Keying by
// projectsDir (rather than transcriptPath) is what lets concurrent
// sessions within the same project share one daily-scan cache entry.
func costDailyCacheName(projectsDir string) string {
	sum := sha256.Sum256([]byte(projectsDir))
	return costDailyCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}
