package notify

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionState_SinceLastNotify_NeverIsNegative(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-never-notified")}
	if got := s.SinceLastNotify(time.Now()); got >= 0 {
		t.Errorf("SinceLastNotify() = %v, want negative when never notified", got)
	}
}

func TestSessionState_MarkThenReadIsApproximatelyElapsed(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-1")}
	markedAt := time.Now()
	if err := s.MarkNotified(markedAt); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	elapsedWant := 5 * time.Second
	got := s.SinceLastNotify(markedAt.Add(elapsedWant))
	diff := got - elapsedWant
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("SinceLastNotify() = %v, want ~%v (diff %v exceeds tolerance)", got, elapsedWant, diff)
	}
}

func TestSessionState_CorruptFileIsNegative(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last-notify"), []byte("not-a-timestamp"), 0o644); err != nil {
		t.Fatalf("writing corrupt state file: %v", err)
	}

	s := SessionState{Dir: dir}
	if got := s.SinceLastNotify(time.Now()); got >= 0 {
		t.Errorf("SinceLastNotify() = %v, want negative on corrupt file", got)
	}
}

func TestSessionState_GoalBlockCount_ZeroWhenNeverSet(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-unset")}
	if got := s.GoalBlockCount("finish the epic"); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want 0 when never set", got)
	}
}

func TestSessionState_GoalBlockCount_SetThenReadRoundTrips(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-1")}
	condition := "finish the epic"

	if err := s.SetGoalBlockCount(condition, 3); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	if got := s.GoalBlockCount(condition); got != 3 {
		t.Errorf("GoalBlockCount() = %d, want 3", got)
	}

	if err := s.SetGoalBlockCount(condition, 4); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	if got := s.GoalBlockCount(condition); got != 4 {
		t.Errorf("GoalBlockCount() = %d, want 4 after overwrite", got)
	}
}

func TestSessionState_GoalBlockCount_DistinctConditionsDoNotCollide(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-2")}

	if err := s.SetGoalBlockCount("condition A", 2); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	if err := s.SetGoalBlockCount("condition B", 5); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}

	if got := s.GoalBlockCount("condition A"); got != 2 {
		t.Errorf("GoalBlockCount(A) = %d, want 2", got)
	}
	if got := s.GoalBlockCount("condition B"); got != 5 {
		t.Errorf("GoalBlockCount(B) = %d, want 5", got)
	}
}

func TestSessionState_GoalBlockCount_CorruptFileIsZero(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-goal-block-corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	s := SessionState{Dir: dir}
	if err := s.SetGoalBlockCount("cond", 1); err != nil {
		t.Fatalf("SetGoalBlockCount() error = %v", err)
	}
	corruptPath := filepath.Join(dir, "goal-block-"+goalBlockKey("cond"))
	if err := os.WriteFile(corruptPath, []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("writing corrupt goal block file: %v", err)
	}

	if got := s.GoalBlockCount("cond"); got != 0 {
		t.Errorf("GoalBlockCount() = %d, want 0 on corrupt file", got)
	}
}

func TestSessionState_LastGoalBlockAt_ZeroWhenNeverSet(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-at-unset")}
	if got := s.LastGoalBlockAt("finish the epic"); !got.IsZero() {
		t.Errorf("LastGoalBlockAt() = %v, want zero time when never set", got)
	}
}

func TestSessionState_LastGoalBlockAt_SetThenReadRoundTrips(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-at-1")}
	condition := "finish the epic"
	blockedAt := time.Now()

	if err := s.MarkGoalBlocked(condition, blockedAt); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}
	got := s.LastGoalBlockAt(condition)
	if !got.Equal(blockedAt) {
		t.Errorf("LastGoalBlockAt() = %v, want %v", got, blockedAt)
	}

	secondBlockedAt := blockedAt.Add(6 * time.Minute)
	if err := s.MarkGoalBlocked(condition, secondBlockedAt); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}
	got = s.LastGoalBlockAt(condition)
	if !got.Equal(secondBlockedAt) {
		t.Errorf("LastGoalBlockAt() = %v, want %v after overwrite", got, secondBlockedAt)
	}
}

func TestSessionState_LastGoalBlockAt_DistinctConditionsDoNotCollide(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-goal-block-at-2")}
	atA := time.Now()
	atB := atA.Add(time.Hour)

	if err := s.MarkGoalBlocked("condition A", atA); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}
	if err := s.MarkGoalBlocked("condition B", atB); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}

	if got := s.LastGoalBlockAt("condition A"); !got.Equal(atA) {
		t.Errorf("LastGoalBlockAt(A) = %v, want %v", got, atA)
	}
	if got := s.LastGoalBlockAt("condition B"); !got.Equal(atB) {
		t.Errorf("LastGoalBlockAt(B) = %v, want %v", got, atB)
	}
}

func TestSessionState_LastGoalBlockAt_CorruptFileIsZero(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-goal-block-at-corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	s := SessionState{Dir: dir}
	if err := s.MarkGoalBlocked("cond", time.Now()); err != nil {
		t.Fatalf("MarkGoalBlocked() error = %v", err)
	}
	corruptPath := filepath.Join(dir, "goal-block-at-"+goalBlockKey("cond"))
	if err := os.WriteFile(corruptPath, []byte("not-a-timestamp"), 0o644); err != nil {
		t.Fatalf("writing corrupt goal block time file: %v", err)
	}

	if got := s.LastGoalBlockAt("cond"); !got.IsZero() {
		t.Errorf("LastGoalBlockAt() = %v, want zero time on corrupt file", got)
	}
}

func TestSessionState_ReapTwiceNoError(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-reap")}
	if err := s.MarkNotified(time.Now()); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	if err := s.Reap(); err != nil {
		t.Fatalf("Reap() error = %v", err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists after Reap(): err = %v", err)
	}
	if err := s.Reap(); err != nil {
		t.Fatalf("Reap() second call error = %v", err)
	}
}
