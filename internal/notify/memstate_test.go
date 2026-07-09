package notify

import (
	"testing"
	"time"
)

func TestMemoryState_SinceLastNotify_NeverIsNegative(t *testing.T) {
	m := NewMemoryState()
	if got := m.SinceLastNotify("sess-never", time.Now()); got >= 0 {
		t.Errorf("SinceLastNotify() = %v, want negative when never notified", got)
	}
}

func TestMemoryState_MarkThenReadIsApproximatelyElapsed(t *testing.T) {
	m := NewMemoryState()
	markedAt := time.Now()
	if err := m.MarkNotified("sess-1", markedAt, "hello"); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	elapsedWant := 5 * time.Second
	got := m.SinceLastNotify("sess-1", markedAt.Add(elapsedWant))
	if got != elapsedWant {
		t.Errorf("SinceLastNotify() = %v, want exactly %v", got, elapsedWant)
	}
}

func TestMemoryState_SinceLastNotifySame_MatchingAndDiffering(t *testing.T) {
	m := NewMemoryState()
	markedAt := time.Now()
	if err := m.MarkNotified("sess-2", markedAt, "identical body"); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	sameBody := m.SinceLastNotifySame("sess-2", markedAt.Add(5*time.Second), "identical body")
	if sameBody < 0 {
		t.Errorf("SinceLastNotifySame() = %v, want non-negative for matching body", sameBody)
	}
	if diffBody := m.SinceLastNotifySame("sess-2", markedAt, "a different body"); diffBody >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative for a different body", diffBody)
	}
	if unknownSession := m.SinceLastNotifySame("sess-never-notified", markedAt, "anything"); unknownSession >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative for a session that never notified", unknownSession)
	}
}

func TestMemoryState_SessionsAreIndependent(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	if err := m.MarkNotified("sess-a", now, "a"); err != nil {
		t.Fatalf("MarkNotified(sess-a) error = %v", err)
	}
	if got := m.SinceLastNotify("sess-b", now); got >= 0 {
		t.Errorf("SinceLastNotify(sess-b) = %v, want negative — marking sess-a must not affect sess-b", got)
	}
}

// TestMemoryState_ClaimSend_CheckAndMarkAreAtomic pins ClaimSend's whole
// contract in one sequence: a first claim wins (and records itself, so a
// racing second claim within the window loses), a claim past the window wins
// again, and the winning claim's message is what SinceLastNotifySame now
// compares against.
func TestMemoryState_ClaimSend_CheckAndMarkAreAtomic(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	const sessionID = "sess-claim"
	window := 5 * time.Minute

	won, since := m.ClaimSend(sessionID, now, "first ping", window, false)
	if !won || since >= 0 {
		t.Fatalf("first claim = (%v, %v), want a win with negative (never-notified) duration", won, since)
	}

	// A second claim 10s later — the two-Stops-racing-through-the-judge shape
	// — must lose against the first claim's own mark.
	won, since = m.ClaimSend(sessionID, now.Add(10*time.Second), "second ping", window, false)
	if won {
		t.Fatal("second claim within window = won, want lost")
	}
	if since != 10*time.Second {
		t.Errorf("second claim since = %v, want 10s", since)
	}

	// The losing claim must not have overwritten the winner's record.
	if got := m.SinceLastNotifySame(sessionID, now.Add(time.Minute), "first ping"); got != time.Minute {
		t.Errorf("SinceLastNotifySame(first ping) = %v, want 1m0s (loser must not overwrite)", got)
	}

	// Past the window the session is quiet again and a new claim wins.
	won, _ = m.ClaimSend(sessionID, now.Add(window+time.Second), "third ping", window, false)
	if !won {
		t.Fatal("claim past window = lost, want won")
	}
}

// TestMemoryState_ClaimSend_OutOfOrderNowLoses pins the out-of-order race
// observed in production 2026-07-08 (two sends 5s apart for one session):
// each claim's now is its own event time, captured before the off-loop judge
// call, so a claim can reach the loop with a now EARLIER than the lastNotify
// a racing claim already recorded. Negative since must read as inside the
// quiet period — only a session with no record at all wins on that path.
func TestMemoryState_ClaimSend_OutOfOrderNowLoses(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	const sessionID = "sess-claim-race"
	window := 5 * time.Minute

	if won, _ := m.ClaimSend(sessionID, now, "later event claims first", window, false); !won {
		t.Fatal("first claim = lost, want won")
	}
	won, since := m.ClaimSend(sessionID, now.Add(-5*time.Second), "earlier event claims second", window, false)
	if won {
		t.Fatal("out-of-order claim (now before recorded lastNotify) = won, want lost")
	}
	if since >= 0 {
		t.Errorf("out-of-order claim since = %v, want negative (now precedes the recorded send)", since)
	}
	// The loser must not have overwritten the winner's record.
	if got := m.SinceLastNotifySame(sessionID, now.Add(time.Minute), "later event claims first"); got != time.Minute {
		t.Errorf("SinceLastNotifySame(winner) = %v, want 1m0s (loser must not overwrite)", got)
	}
}

func TestMemoryState_ClaimSend_DryRunObservesWithoutWriting(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	const sessionID = "sess-claim-dry"
	window := 5 * time.Minute

	if won, _ := m.ClaimSend(sessionID, now, "msg", window, true); !won {
		t.Fatal("dry-run claim on a quiet session = lost, want won")
	}
	// The dry run must not have recorded anything: a real claim still wins.
	if won, _ := m.ClaimSend(sessionID, now, "msg", window, false); !won {
		t.Fatal("real claim after dry run = lost, want won (dry run must not write)")
	}
	// And a dry run against the live record reports the loss.
	if won, _ := m.ClaimSend(sessionID, now.Add(time.Second), "msg", window, true); won {
		t.Fatal("dry-run claim within window = won, want lost")
	}
}

// TestMemoryState_ClaimBroadcast_FirstWinsSecondLosesThenExpires is the
// direct-unit-test analog of TestClaimBroadcast_FirstClaimWinsSecondLoses
// (broadcast.go's file-backed claimBroadcast), pinning the same
// first-claimant/TTL-expiry contract for the in-memory implementation —
// with synthetic `now` values so the TTL edges (broadcastClaimWindow,
// broadcastClaimTTL) can be exercised without a real 30-minute sleep.
func TestMemoryState_ClaimBroadcast_FirstWinsSecondLosesThenExpires(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	const key = "agent_needs_input\nmsg"

	if !m.ClaimBroadcast(key, broadcastClaimWindow, now, false) {
		t.Fatal("first claim = false, want true")
	}
	if m.ClaimBroadcast(key, broadcastClaimWindow, now.Add(time.Second), false) {
		t.Fatal("second claim within window = true, want false (dedupe: broadcast claimed)")
	}
	// Past the window: the claim is stale and reclaimed.
	past := now.Add(broadcastClaimWindow + time.Second)
	if !m.ClaimBroadcast(key, broadcastClaimWindow, past, false) {
		t.Fatal("claim after window expiry = false, want true (stale claim reclaimed)")
	}
}

func TestMemoryState_ClaimBroadcast_DistinctKeysBothWin(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()

	if !m.ClaimBroadcast("agent_needs_input\njob A asks", broadcastClaimWindow, now, false) {
		t.Fatal("claim A = false, want true")
	}
	if !m.ClaimBroadcast("agent_needs_input\njob B asks", broadcastClaimWindow, now, false) {
		t.Fatal("claim B = false, want true")
	}
}

func TestMemoryState_ClaimBroadcast_DryRunObservesWithoutWriting(t *testing.T) {
	m := NewMemoryState()
	now := time.Now()
	const key = "k"

	if !m.ClaimBroadcast(key, broadcastClaimWindow, now, true) {
		t.Fatal("dry-run claim with no live claim = false, want true")
	}
	// The dry run must not have written anything: a subsequent real claim
	// still wins.
	if !m.ClaimBroadcast(key, broadcastClaimWindow, now, false) {
		t.Fatal("real claim after dry run = false, want true (dry run must not write)")
	}
	// And a dry run against a live real claim reports the dedupe.
	if m.ClaimBroadcast(key, broadcastClaimWindow, now.Add(time.Second), true) {
		t.Fatal("dry-run claim against live claim = true, want false")
	}
}
