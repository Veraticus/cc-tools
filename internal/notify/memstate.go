package notify

import "time"

// sessionMemState is one session's in-memory dedupe record: the last
// notified time and the sha256 of the last message sent.
type sessionMemState struct {
	lastNotify time.Time
	lastHash   string
}

// claimMemState is one broadcast claim's in-memory record: when it was
// claimed.
type claimMemState struct {
	claimedAt time.Time
}

// MemoryState is DedupeState's in-memory implementation, used by notifyd
// (see Daemon). It must only ever be touched by the daemon's single
// event-loop goroutine: it has no locking of its own — the event loop
// draining requests one at a time IS the synchronization (see loopState in
// daemon.go), not a mutex here. A MemoryState shared across goroutines
// without that discipline is a data race. Losing it on daemon restart is
// accepted by the epic: at worst one duplicate ping, never a lost one.
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
// neverNotifiedDuration if it never has.
func (m *MemoryState) SinceLastNotify(sessionID string, now time.Time) time.Duration {
	s, ok := m.sessions[sessionID]
	if !ok {
		return neverNotifiedDuration
	}
	return now.Sub(s.lastNotify)
}

// SinceLastNotifySame returns how long before now sessionID last sent
// message verbatim, or neverNotifiedDuration if it never has or the last
// message differs.
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

// ClaimSend atomically resolves whether a send for sessionID may proceed:
// it loses when the session already notified within window of now, and
// otherwise wins and records now/message as the session's last notification
// in the same step — check and mark are one operation, so two Pipeline runs
// racing through their (off-loop) judge calls can never both win for the
// same quiet period. A negative since only wins when the session truly has
// no record: each claim's now is its own event time, captured before the
// off-loop judge call, so a claim can arrive with a now EARLIER than the
// lastNotify a racing claim just recorded — that is inside the quiet
// period, not before it (observed 2026-07-08 as two sends 5s apart for one
// session). The returned duration is how long before now the session last
// notified (neverNotifiedDuration when no record), for the caller's
// decision-log reason. dryRun observes without recording, mirroring
// ClaimBroadcast.
func (m *MemoryState) ClaimSend(
	sessionID string, now time.Time, message string, window time.Duration, dryRun bool,
) (bool, time.Duration) {
	_, exists := m.sessions[sessionID]
	since := m.SinceLastNotify(sessionID, now)
	won := !exists || since >= window
	if won && !dryRun {
		_ = m.MarkNotified(sessionID, now, message)
	}
	return won, since
}

// ClaimBroadcast atomically claims key for a window starting at now,
// reporting whether this call won: exactly one of N concurrent claims for
// the same key succeeds, and a claim older than window is stale (a
// previous, distinct event) and is replaced. dryRun observes without
// claiming and wins whenever no live claim exists.
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

// DeleteSession removes sessionID's dedupe record, if any — called on
// SessionEnd so the sessions map does not accumulate an entry per session
// for the daemon's entire uptime (see MemoryState's doc comment).
func (m *MemoryState) DeleteSession(sessionID string) {
	delete(m.sessions, sessionID)
}

// sweepClaims removes claims older than broadcastClaimTTL so the claims map
// stays a handful of entries.
func (m *MemoryState) sweepClaims(now time.Time) {
	for k, c := range m.claims {
		if now.Sub(c.claimedAt) > broadcastClaimTTL {
			delete(m.claims, k)
		}
	}
}
