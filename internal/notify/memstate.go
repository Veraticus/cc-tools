package notify

import "time"

// sessionMemState is one session's in-memory dedupe record: the daemon's
// analog of a SessionState directory's last-notify file pair.
type sessionMemState struct {
	lastNotify time.Time
	lastHash   string
}

// claimMemState is one broadcast claim's in-memory record: the daemon's
// analog of a claim file's mtime.
type claimMemState struct {
	claimedAt time.Time
}

// MemoryState is DedupeState's in-memory implementation, used by notifyd
// (see Daemon) instead of FileState's on-disk bookkeeping. It must only
// ever be touched by the daemon's single event-loop goroutine: unlike
// FileState, which relies on the filesystem's own atomicity to stay safe
// across concurrent hook processes, MemoryState has no locking of its
// own — the event loop draining requests one at a time IS the
// synchronization (see loopState in daemon.go), not a mutex here. A
// MemoryState shared across goroutines without that discipline is a data
// race. Losing it on daemon restart is accepted by the epic: at worst one
// duplicate ping, never a lost one.
type MemoryState struct {
	sessions map[string]sessionMemState
	claims   map[string]claimMemState
}

// NewMemoryState returns an empty MemoryState ready to use.
func NewMemoryState() *MemoryState {
	return &MemoryState{
		sessions: make(map[string]sessionMemState),
		claims:   make(map[string]claimMemState),
	}
}

// SinceLastNotify returns how long before now sessionID last notified, or
// neverNotifiedDuration if it never has — see SessionState.SinceLastNotify.
func (m *MemoryState) SinceLastNotify(sessionID string, now time.Time) time.Duration {
	s, ok := m.sessions[sessionID]
	if !ok {
		return neverNotifiedDuration
	}
	return now.Sub(s.lastNotify)
}

// SinceLastNotifySame returns how long before now sessionID last sent
// message verbatim, or neverNotifiedDuration if it never has or the last
// message differs — see SessionState.SinceLastNotifySame.
func (m *MemoryState) SinceLastNotifySame(sessionID string, now time.Time, message string) time.Duration {
	s, ok := m.sessions[sessionID]
	if !ok || s.lastHash != hashMessage(message) {
		return neverNotifiedDuration
	}
	return now.Sub(s.lastNotify)
}

// MarkNotified records t/message as sessionID's last notification.
func (m *MemoryState) MarkNotified(sessionID string, t time.Time, message string) error {
	m.sessions[sessionID] = sessionMemState{lastNotify: t, lastHash: hashMessage(message)}
	return nil
}

// ClaimBroadcast atomically claims key for a window starting at now,
// reporting whether this call won — the in-memory analog of
// claimBroadcast's O_EXCL first-claimant contract. dryRun observes without
// claiming, exactly like claimBroadcast's dry-run branch.
func (m *MemoryState) ClaimBroadcast(key string, window time.Duration, now time.Time, dryRun bool) bool {
	c, ok := m.claims[key]
	won := !ok || now.Sub(c.claimedAt) >= window
	if dryRun || !won {
		return won
	}
	m.sweepClaims(now)
	m.claims[key] = claimMemState{claimedAt: now}
	return true
}

// sweepClaims removes claims older than broadcastClaimTTL, the in-memory
// analog of sweepBroadcastClaims's file-ledger hygiene.
func (m *MemoryState) sweepClaims(now time.Time) {
	for k, c := range m.claims {
		if now.Sub(c.claimedAt) > broadcastClaimTTL {
			delete(m.claims, k)
		}
	}
}
