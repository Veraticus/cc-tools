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
