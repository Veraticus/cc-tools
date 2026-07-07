package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// neverNotifiedDuration is what SinceLastNotify returns when there is no
// recorded last-notify time (missing or corrupt state): a negative
// duration, which decide.go's Env.SinceLastNotify already treats as "never
// notified" rather than a real elapsed time.
const neverNotifiedDuration = -1 * time.Second

// lastNotifyFile is the name, within SessionState.Dir, of the last-notify
// timestamp file.
const lastNotifyFile = "last-notify"

// goalBlockFilePrefix names, within SessionState.Dir, the consecutive
// goal-block counter file for a given goal condition: the full filename is
// this prefix plus goalBlockKey(condition).
const goalBlockFilePrefix = "goal-block-"

// goalBlockKeyLen is how many hex characters of the condition's sha256 sum
// goalBlockKey keeps: enough to make collisions practically impossible for a
// single session's goal conditions, short enough to stay a tidy filename.
const goalBlockKeyLen = 16

// goalBlockAtFilePrefix names, within SessionState.Dir, the last-block
// timestamp file for a given goal condition: the full filename is this
// prefix plus goalBlockKey(condition), mirroring goalBlockFilePrefix's
// per-condition keying.
const goalBlockAtFilePrefix = "goal-block-at-"

// goalBlockKey returns the stable, deterministic filename component for a
// goal condition's consecutive-block counter: the hex-encoded sha256 of
// condition, truncated to goalBlockKeyLen characters. Deterministic and
// dependency-free (crypto/sha256 is stdlib), so the same condition always
// resolves to the same counter file across separate pipeline invocations.
func goalBlockKey(condition string) string {
	sum := sha256.Sum256([]byte(condition))
	return hex.EncodeToString(sum[:])[:goalBlockKeyLen]
}

// SessionState is a per-session directory for notify's on-disk bookkeeping:
// today it holds the last-notify timestamp; a watchdog lockfile will live
// here too. Dir is the full <base>/<session-id> path — the caller composes
// it.
type SessionState struct {
	Dir string
}

// MarkNotified records t as the session's last-notify time: it creates Dir
// as needed and writes t (RFC3339Nano) to Dir/last-notify.
func (s SessionState) MarkNotified(t time.Time) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("notify: creating session state dir: %w", err)
	}
	path := filepath.Join(s.Dir, lastNotifyFile)
	if err := os.WriteFile(path, []byte(t.Format(time.RFC3339Nano)), 0o600); err != nil {
		return fmt.Errorf("notify: writing last-notify: %w", err)
	}
	return nil
}

// SinceLastNotify returns how long before now this session last notified.
// A missing state directory/file and a corrupt timestamp are both
// indistinguishable from "never notified" for the caller's dedupe-window
// check, so both return neverNotifiedDuration rather than an error.
func (s SessionState) SinceLastNotify(now time.Time) time.Duration {
	path := filepath.Join(s.Dir, lastNotifyFile)
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		return neverNotifiedDuration
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return neverNotifiedDuration
	}
	return now.Sub(t)
}

// GoalBlockCount returns how many consecutive times this goal condition has
// been blocked in a row. A missing state directory/file and a corrupt count
// are both treated as 0 (never blocked) rather than an error, mirroring
// SinceLastNotify's precedent for unreadable state.
func (s SessionState) GoalBlockCount(condition string) int {
	path := filepath.Join(s.Dir, goalBlockFilePrefix+goalBlockKey(condition))
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// SetGoalBlockCount records n as the consecutive-block count for condition:
// it creates Dir as needed and writes n as decimal text, mirroring
// MarkNotified's write pattern exactly.
func (s SessionState) SetGoalBlockCount(condition string, n int) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("notify: creating session state dir: %w", err)
	}
	path := filepath.Join(s.Dir, goalBlockFilePrefix+goalBlockKey(condition))
	if err := os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600); err != nil {
		return fmt.Errorf("notify: writing goal block count: %w", err)
	}
	return nil
}

// LastGoalBlockAt returns when this goal condition was last blocked. A
// missing state directory/file and a corrupt timestamp are both treated as
// the zero time (never blocked) rather than an error, mirroring
// GoalBlockCount's precedent for unreadable state.
func (s SessionState) LastGoalBlockAt(condition string) time.Time {
	path := filepath.Join(s.Dir, goalBlockAtFilePrefix+goalBlockKey(condition))
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// MarkGoalBlocked records t as the last-block time for condition: it creates
// Dir as needed and writes t (RFC3339Nano), mirroring MarkNotified's write
// pattern exactly.
func (s SessionState) MarkGoalBlocked(condition string, t time.Time) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("notify: creating session state dir: %w", err)
	}
	path := filepath.Join(s.Dir, goalBlockAtFilePrefix+goalBlockKey(condition))
	if err := os.WriteFile(path, []byte(t.Format(time.RFC3339Nano)), 0o600); err != nil {
		return fmt.Errorf("notify: writing goal block time: %w", err)
	}
	return nil
}

// Reap removes the session's entire state directory. It is idempotent:
// removing an already-gone directory is not an error, per os.RemoveAll's
// own contract.
func (s SessionState) Reap() error {
	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("notify: reaping session state dir: %w", err)
	}
	return nil
}
