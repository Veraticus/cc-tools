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
