package statusline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gitStatusTimeout bounds the `git status` subprocess spawned for the
// git chip's dirty/ahead/behind state. Kept well under a second so a
// slow or hung git process never stalls a statusline render; the
// context.WithTimeout deadline it drives guarantees the child is
// killed and reaped (see DefaultCommandRunner.RunContext) even if git
// itself never exits on its own.
const gitStatusTimeout = 500 * time.Millisecond

// abFieldCount is the number of space-separated fields in a
// well-formed "# branch.ab +<ahead> -<behind>" porcelain line.
const abFieldCount = 2

// gitCacheFilePerm is the permission mode for the git-status cache
// file; owner-only since CacheDir may be a shared location like
// /dev/shm.
const gitCacheFilePerm = 0o600

// gitCacheHashBytes is how many bytes of the cwd's sha256 sum are kept
// for the cache filename — enough to make collisions practically
// impossible for the small number of directories any one user renders
// a statusline from.
const gitCacheHashBytes = 8

// gitState is the parsed/cached result of one `git status
// --porcelain=v2 --branch` invocation for a working directory.
//
// Branch is parsed straight from the porcelain "# branch.head" line,
// including the literal "(detached)" token git emits for a detached
// HEAD. Display code does NOT use this field: the git chip's branch
// text always comes from the existing .git/HEAD file-read path
// (readGitInfo), which already derives a short-SHA fallback for
// detached HEAD without needing a subprocess at all. Branch is kept
// here purely so the porcelain parser is independently testable.
type gitState struct {
	Branch     string `json:"branch"`
	DirtyCount int    `json:"dirty_count"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	OK         bool   `json:"ok"`
}

// gitStatus returns cached or freshly-computed dirty/ahead/behind
// state for cwd. It runs `git -C cwd status --porcelain=v2 --branch`
// through runner (bounded by gitStatusTimeout) whenever the cache — a
// JSON file in cacheDir keyed by a hash of cwd — is missing, corrupt,
// or older than ttl relative to now. A failed or timed-out subprocess
// yields gitState{OK: false}, which callers treat as "render the git
// chip branch-only".
//
// cacheDir == "" disables caching entirely (no read, no write; the
// subprocess still runs fresh every call). This keeps callers that
// don't configure a cache directory from writing a stray file at a
// relative path resolved against the process's current working
// directory.
func gitStatus(runner CommandRunner, cacheDir string, ttl time.Duration, cwd string, now time.Time) gitState {
	if cacheDir == "" {
		return runGitStatus(runner, cwd)
	}

	cachePath := gitStatusCachePath(cacheDir, cwd)
	if state, ok := readFreshGitStatusCache(cachePath, ttl, now); ok {
		return state
	}

	state := runGitStatus(runner, cwd)
	if state.OK {
		writeGitStatusCache(cachePath, state)
	}
	return state
}

// gitStatusCachePath returns the cache file path for cwd inside
// cacheDir: "gitstatus-<hex sha256 prefix of cwd>.json". Hashing (vs.
// e.g. sanitizing cwd into a filename) keeps the name filesystem-safe
// regardless of what characters cwd contains.
func gitStatusCachePath(cacheDir, cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(cacheDir, "gitstatus-"+hex.EncodeToString(sum[:gitCacheHashBytes])+".json")
}

// readFreshGitStatusCache reads path and reports (state, true) only if
// it exists, is younger than ttl relative to now, and decodes as valid
// JSON. Any other outcome — missing, stale, or corrupt — is a cache
// miss: (gitState{}, false).
func readFreshGitStatusCache(path string, ttl time.Duration, now time.Time) (gitState, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return gitState{}, false
	}
	if now.Sub(info.ModTime()) >= ttl {
		return gitState{}, false
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is CacheDir + a hash of cwd, not user input
	if err != nil {
		return gitState{}, false
	}

	var state gitState
	if unmarshalErr := json.Unmarshal(data, &state); unmarshalErr != nil {
		return gitState{}, false
	}
	return state, true
}

// writeGitStatusCache best-effort writes state as JSON to path. A
// write failure (cacheDir missing, unwritable, etc.) isn't fatal — the
// freshly computed state is returned to gitStatus's caller regardless
// — so the error is deliberately not propagated any further than this.
func writeGitStatusCache(path string, state gitState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, gitCacheFilePerm)
}

// runGitStatus runs `git -C cwd status --porcelain=v2 --branch` under
// a gitStatusTimeout deadline and parses its output. Any error —
// including the subprocess being killed on timeout — yields
// gitState{OK: false}.
func runGitStatus(runner CommandRunner, cwd string) gitState {
	ctx, cancel := context.WithTimeout(context.Background(), gitStatusTimeout)
	defer cancel()

	output, err := runner.RunContext(ctx, "git", "-C", cwd, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return gitState{OK: false}
	}
	return parseGitPorcelain(string(output))
}

// parseGitPorcelain parses `git status --porcelain=v2 --branch`
// output. Recognized header lines: "# branch.head <name>" (branch
// name, or the literal "(detached)" for a detached HEAD) and
// "# branch.ab +<ahead> -<behind>" (tracking counts vs. upstream).
// Every other line is either an unrecognized "#"-prefixed header
// (ignored — e.g. branch.oid, branch.upstream) or a change/untracked
// entry, counted toward DirtyCount.
func parseGitPorcelain(output string) gitState {
	state := gitState{OK: true}

	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			state.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.ab "):
			parseBranchAb(&state, strings.TrimPrefix(line, "# branch.ab "))
		case strings.HasPrefix(line, "#"):
			// Other header line — not needed for dirty/ahead/behind state.
		default:
			state.DirtyCount++
		}
	}

	return state
}

// parseBranchAb parses the "+<ahead> -<behind>" body of a
// "# branch.ab" line into state.Ahead/state.Behind. Malformed input
// (wrong field count, non-numeric values) leaves the fields at their
// zero value rather than panicking or recording a partial count.
func parseBranchAb(state *gitState, body string) {
	fields := strings.Fields(body)
	if len(fields) != abFieldCount {
		return
	}
	ahead, aheadErr := strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
	behind, behindErr := strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
	if aheadErr != nil || behindErr != nil {
		return
	}
	state.Ahead = ahead
	state.Behind = behind
}
