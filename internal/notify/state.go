package notify

import (
	"fmt"
	"os"
	"path/filepath"
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

// Reap removes the session's entire state directory. It is idempotent:
// removing an already-gone directory is not an error, per os.RemoveAll's
// own contract.
func (s SessionState) Reap() error {
	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("notify: reaping session state dir: %w", err)
	}
	return nil
}
