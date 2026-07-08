package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// neverNotifiedDuration is what SinceLastNotify returns when there is no
// recorded last-notify time (missing or corrupt state): a negative
// duration, which decide.go's Env.SinceLastNotify already treats as "never
// notified" rather than a real elapsed time.
const neverNotifiedDuration = -1 * time.Second

// hashMessage returns the hex-encoded sha256 of message: the stable
// content-identity key MarkNotified writes and SinceLastNotifySame compares
// against to detect an identical repeat ping.
func hashMessage(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
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
// one the daemon never saw. It is also Pipeline.dedupeState's own default
// when State is left unset, for the same reason.
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

// DeleteSession is a no-op: there is nothing to evict without a store.
func (NopState) DeleteSession(_ context.Context, _ string) {}
