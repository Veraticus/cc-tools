package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// lastNotifyMsgFile is the name, within SessionState.Dir, of the last-notify
// message-hash file: the hex sha256 of the notification body MarkNotified
// last recorded, letting SinceLastNotifySame detect an identical repeat.
const lastNotifyMsgFile = "last-notify-msg"

// hashMessage returns the hex-encoded sha256 of message: the stable
// content-identity key MarkNotified writes and SinceLastNotifySame compares
// against to detect an identical repeat ping.
func hashMessage(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
}

// SessionState is a per-session directory for notify's on-disk bookkeeping:
// today it holds the last-notify timestamp; a watchdog lockfile will live
// here too. Dir is the full <base>/<session-id> path — the caller composes
// it.
type SessionState struct {
	Dir string
}

// MarkNotified records t as the session's last-notify time and message as
// the last-notify message (as its sha256 hash): it creates Dir as needed and
// writes t (RFC3339Nano) to Dir/last-notify and hashMessage(message) to
// Dir/last-notify-msg.
func (s SessionState) MarkNotified(t time.Time, message string) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("notify: creating session state dir: %w", err)
	}
	path := filepath.Join(s.Dir, lastNotifyFile)
	if err := os.WriteFile(path, []byte(t.Format(time.RFC3339Nano)), 0o600); err != nil {
		return fmt.Errorf("notify: writing last-notify: %w", err)
	}
	msgPath := filepath.Join(s.Dir, lastNotifyMsgFile)
	if err := os.WriteFile(msgPath, []byte(hashMessage(message)), 0o600); err != nil {
		return fmt.Errorf("notify: writing last-notify-msg: %w", err)
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

// SinceLastNotifySame returns how long before now this session last sent
// message verbatim: negative when the session has never notified, the
// last-notify-msg file is missing/corrupt, or the last-recorded message's
// hash does not match message. Callers may treat a non-negative result as
// this exact ping repeating.
func (s SessionState) SinceLastNotifySame(now time.Time, message string) time.Duration {
	path := filepath.Join(s.Dir, lastNotifyMsgFile)
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		return neverNotifiedDuration
	}
	if strings.TrimSpace(string(data)) != hashMessage(message) {
		return neverNotifiedDuration
	}
	return s.SinceLastNotify(now)
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

// FileState is DedupeState's on-disk implementation: the historical
// per-hook-invocation behavior, reading and writing a SessionState file
// tree per session under Base plus the shared broadcast-claims ledger.
// It is what Pipeline.dedupeState falls back to when State is unset, so
// every existing single-shot call site keeps today's file-based semantics
// unchanged.
type FileState struct {
	Base string
}

// session returns the SessionState for sessionID under f.Base.
func (f FileState) session(sessionID string) SessionState {
	return SessionState{Dir: filepath.Join(f.Base, sessionID)}
}

// SinceLastNotify implements DedupeState by delegating to the on-disk
// SessionState for sessionID. ctx is unused: file I/O here is neither
// judge nor sender work, so there is nothing for it to bound or cancel.
func (f FileState) SinceLastNotify(_ context.Context, sessionID string, now time.Time) time.Duration {
	return f.session(sessionID).SinceLastNotify(now)
}

// SinceLastNotifySame implements DedupeState by delegating to the on-disk
// SessionState for sessionID.
func (f FileState) SinceLastNotifySame(
	_ context.Context, sessionID string, now time.Time, message string,
) time.Duration {
	return f.session(sessionID).SinceLastNotifySame(now, message)
}

// MarkNotified implements DedupeState by delegating to the on-disk
// SessionState for sessionID.
func (f FileState) MarkNotified(_ context.Context, sessionID string, t time.Time, message string) error {
	return f.session(sessionID).MarkNotified(t, message)
}

// ClaimBroadcast implements DedupeState via the package-level claimBroadcast,
// rooted at f.Base.
func (f FileState) ClaimBroadcast(
	_ context.Context, key string, window time.Duration, now time.Time, dryRun bool,
) bool {
	return claimBroadcast(f.Base, key, window, now, dryRun)
}

// NopState is DedupeState's no-op implementation: every session reports
// "never notified" and every broadcast claim wins. It is what the hook
// client's inline fallback uses (see cmd/cc-tools/notify.go) when notifyd
// is unreachable — the daemon now holds the real dedupe state in memory
// (see MemoryState), so a single fallback invocation has no shared history
// left to consult on disk, and consulting stale on-disk state would be
// actively wrong once the daemon is the source of truth. Per the epic's
// reliability invariant that a duplicate ping beats a lost one, NopState
// trades a possible duplicate notification for never silently swallowing
// one the daemon never saw.
type NopState struct{}

// SinceLastNotify always reports "never notified".
func (NopState) SinceLastNotify(_ context.Context, _ string, _ time.Time) time.Duration {
	return neverNotifiedDuration
}

// SinceLastNotifySame always reports "never notified".
func (NopState) SinceLastNotifySame(_ context.Context, _ string, _ time.Time, _ string) time.Duration {
	return neverNotifiedDuration
}

// MarkNotified is a no-op: there is nothing to record without a store.
func (NopState) MarkNotified(_ context.Context, _ string, _ time.Time, _ string) error { return nil }

// ClaimBroadcast always reports a win: there is no shared ledger to check.
func (NopState) ClaimBroadcast(_ context.Context, _ string, _ time.Duration, _ time.Time, _ bool) bool {
	return true
}
