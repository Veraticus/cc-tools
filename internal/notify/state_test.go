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
	if err := s.MarkNotified(markedAt, "hello"); err != nil {
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

func TestSessionState_SinceLastNotifySame_NeverIsNegative(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-same-never-notified")}
	if got := s.SinceLastNotifySame(time.Now(), "hello"); got >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative when never notified", got)
	}
}

func TestSessionState_SinceLastNotifySame_MatchingMessageReturnsElapsed(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-same-1")}
	markedAt := time.Now()
	if err := s.MarkNotified(markedAt, "identical body"); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	elapsedWant := 5 * time.Second
	got := s.SinceLastNotifySame(markedAt.Add(elapsedWant), "identical body")
	diff := got - elapsedWant
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("SinceLastNotifySame() = %v, want ~%v (diff %v exceeds tolerance)", got, elapsedWant, diff)
	}
}

func TestSessionState_SinceLastNotifySame_DifferentMessageIsNegative(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-same-2")}
	if err := s.MarkNotified(time.Now(), "original body"); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	if got := s.SinceLastNotifySame(time.Now(), "a different body"); got >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative for a different message", got)
	}
}

func TestSessionState_SinceLastNotifySame_MissingFileIsNegative(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-same-missing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	s := SessionState{Dir: dir}
	if got := s.SinceLastNotifySame(time.Now(), "hello"); got >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative when last-notify-msg is missing", got)
	}
}

func TestSessionState_SinceLastNotifySame_CorruptFileIsNegative(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-same-corrupt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, lastNotifyMsgFile), []byte("not-a-hash"), 0o644); err != nil {
		t.Fatalf("writing corrupt state file: %v", err)
	}

	s := SessionState{Dir: dir}
	if got := s.SinceLastNotifySame(time.Now(), "hello"); got >= 0 {
		t.Errorf("SinceLastNotifySame() = %v, want negative on corrupt file", got)
	}
}

func TestSessionState_MarkNotified_RoundTripsBothFiles(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-roundtrip")}
	markedAt := time.Now()
	if err := s.MarkNotified(markedAt, "the body"); err != nil {
		t.Fatalf("MarkNotified() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(s.Dir, lastNotifyFile)); err != nil {
		t.Errorf("last-notify file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, lastNotifyMsgFile)); err != nil {
		t.Errorf("last-notify-msg file missing: %v", err)
	}
	if since := s.SinceLastNotify(markedAt); since < 0 {
		t.Errorf("SinceLastNotify() = %v, want non-negative", since)
	}
	if since := s.SinceLastNotifySame(markedAt, "the body"); since < 0 {
		t.Errorf("SinceLastNotifySame() = %v, want non-negative for the same body", since)
	}
}

func TestSessionState_ReapTwiceNoError(t *testing.T) {
	s := SessionState{Dir: filepath.Join(t.TempDir(), "session-reap")}
	if err := s.MarkNotified(time.Now(), "hello"); err != nil {
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
