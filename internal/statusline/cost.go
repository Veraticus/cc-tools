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
const costCacheKeyPrefix = "cost-"

// costState is the cached session+daily result for one transcript
// path.
type costState struct {
	SessionUSD float64 `json:"session_usd"`
	DailyUSD   float64 `json:"daily_usd"`
}

// transcriptCosts returns transcript-derived costs for transcriptPath,
// cached under cacheDir with ttl (same machinery as the git chip).
// ok=false on any failure — caller falls back to stdin cost.
//
// transcriptPath is expected in the shape
// "<profile>/projects/<slug>/<session>.jsonl": projectsDir is its
// grandparent directory, and profileDir (projectsDir's parent) is
// where the OAuth credentials file lives.
func transcriptCosts(cacheDir string, ttl time.Duration, transcriptPath string, now time.Time) (costState, bool) {
	if cacheDir == "" {
		return computeTranscriptCosts(transcriptPath, now)
	}

	root, trusted := openCacheRoot(cacheDir)
	if !trusted {
		return computeTranscriptCosts(transcriptPath, now)
	}
	defer func() { _ = root.Close() }()

	name := costCacheName(transcriptPath)
	var cached costState
	if readFreshCache(root, name, ttl, now, &cached) {
		return cached, true
	}

	state, ok := computeTranscriptCosts(transcriptPath, now)
	if ok {
		writeCache(root, name, state)
	}
	return state, ok
}

// computeTranscriptCosts computes costState fresh (no cache) from
// transcriptPath: subscription status is read from the profile's
// credentials file, then cost.Session and cost.Daily are both
// computed under that subscribed flag. Either failing is treated as a
// total failure (ok=false) — a partial session-only or daily-only
// result would be confusing on a status bar.
func computeTranscriptCosts(transcriptPath string, now time.Time) (costState, bool) {
	projectsDir := filepath.Dir(filepath.Dir(transcriptPath))
	profileDir := filepath.Dir(projectsDir)
	subscribed := readSubscribed(profileDir)

	sessionUSD, err := cost.Session(transcriptPath, subscribed)
	if err != nil {
		return costState{}, false
	}
	dailyUSD, err := cost.Daily(projectsDir, now, subscribed)
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

// costCacheName returns the cache file name for transcriptPath:
// "cost-<hex sha256 prefix of transcriptPath>.json". The costCacheKeyPrefix
// keeps this entry's name distinct from gitStatusCacheName's entries,
// which share the same cache root/subdirectory.
func costCacheName(transcriptPath string) string {
	sum := sha256.Sum256([]byte(transcriptPath))
	return costCacheKeyPrefix + hex.EncodeToString(sum[:cacheHashBytes]) + ".json"
}
