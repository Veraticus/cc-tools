package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- fakes ---

// fakeClock is a fully injected clock/sleeper: Sleep records the requested
// duration and advances now by it, returning true, until maxSleeps calls
// have succeeded — the call after that returns false without advancing the
// clock, simulating ctx cancellation.
type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	sleeps    []time.Duration
	maxSleeps int // -1 = unlimited
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxSleeps >= 0 && len(c.sleeps) >= c.maxSleeps {
		return false
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return true
}

func (c *fakeClock) sleepDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.sleeps))
	copy(out, c.sleeps)
	return out
}

// fakeProc is a counting fake for ProcAlive/Kill.
type fakeProc struct {
	mu     sync.Mutex
	alive  map[int]bool
	killed []int
}

func newFakeProc() *fakeProc {
	return &fakeProc{alive: make(map[int]bool)}
}

func (f *fakeProc) ProcAlive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeProc) Kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	f.alive[pid] = false
	return nil
}

func (f *fakeProc) killedPIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.killed))
	copy(out, f.killed)
	return out
}

// judgeResult is one scripted answer for fakeJudge.
type judgeResult struct {
	verdict JudgeVerdict
	err     error
}

// fakeJudge returns scripted results in order, repeating the last entry once
// the script is exhausted, and counts every call plus the mode it was
// invoked with.
type fakeJudge struct {
	mu     sync.Mutex
	script []judgeResult
	calls  int
	modes  []JudgeMode
}

func (f *fakeJudge) Evaluate(_ context.Context, _ string, mode JudgeMode) (JudgeVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	f.calls++
	f.modes = append(f.modes, mode)
	if idx >= len(f.script) {
		idx = len(f.script) - 1
	}
	r := f.script[idx]
	return r.verdict, r.err
}

func (f *fakeJudge) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSender counts every notification it is asked to send.
type fakeSender struct {
	mu   sync.Mutex
	sent []Notification
}

func (f *fakeSender) Send(_ context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return nil
}

func (f *fakeSender) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) notifications() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Notification, len(f.sent))
	copy(out, f.sent)
	return out
}

// fakeLog counts every decision record appended.
type fakeLog struct {
	mu   sync.Mutex
	recs []DecisionRecord
}

func (f *fakeLog) Log(rec DecisionRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, rec)
}

func (f *fakeLog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recs)
}

// --- test helpers ---

const (
	testSelfPID   = 4242
	testParentPID = 1000
	testOtherPID  = 9999
)

// testHarness bundles the fakes behind a WatchdogDeps for one test.
type testHarness struct {
	clock *fakeClock
	proc  *fakeProc
	judge *fakeJudge
	send  *fakeSender
	log   *fakeLog
	deps  WatchdogDeps
}

func newTestHarness(now time.Time, maxSleeps int, script []judgeResult) *testHarness {
	h := &testHarness{
		clock: &fakeClock{now: now, maxSleeps: maxSleeps},
		proc:  newFakeProc(),
		judge: &fakeJudge{script: script},
		send:  &fakeSender{},
		log:   &fakeLog{},
	}
	h.proc.alive[testParentPID] = true
	h.deps = WatchdogDeps{
		Now:       h.clock.Now,
		Sleep:     h.clock.Sleep,
		ProcAlive: h.proc.ProcAlive,
		Kill:      h.proc.Kill,
		SelfPID:   func() int { return testSelfPID },
		Judge:     h.judge.Evaluate,
		Send:      h.send.Send,
		Log:       h.log.Log,
	}
	return h
}

// copyFixture copies a testdata fixture into t.TempDir and returns its path,
// so a test can freely append to or mutate its own copy (e.g. for revival).
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if writeErr := os.WriteFile(dst, data, 0o600); writeErr != nil {
		t.Fatalf("writing fixture copy %s: %v", name, writeErr)
	}
	return dst
}

// scannedBytes returns ScanResult.BytesScanned for path, for constructing a
// lock's Offset that matches the fixture's current content exactly (no
// spurious revival).
func scannedBytes(t *testing.T, path string) int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("scanning %s: %v", path, err)
	}
	return res.BytesScanned
}

// writeLock writes lk as the watchdog lock for st, bypassing
// WriteWatchdogLock (which is tested separately) so RunWatchdog tests can
// arm a session directly.
func writeLock(t *testing.T, st SessionState, lk WatchdogLock) {
	t.Helper()
	if err := os.MkdirAll(st.Dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(lk)
	if err != nil {
		t.Fatalf("marshaling lock: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(st.Dir, "watchdog.lock"), data, 0o600); writeErr != nil {
		t.Fatalf("writing lock: %v", writeErr)
	}
}

func lockExists(st SessionState) bool {
	_, err := os.Stat(filepath.Join(st.Dir, "watchdog.lock"))
	return err == nil
}

// --- RunWatchdog: ownership/supersession ---

func TestRunWatchdog_Superseded(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testOtherPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now,
	}
	writeLock(t, st, lk)
	before, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading lock before run: %v", err)
	}

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "superseded" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "superseded")
	}
	if h.log.count() != 1 {
		t.Errorf("log entries = %d, want 1 (one exit record, no send)", h.log.count())
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	after, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading lock after run: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("lockfile was modified on superseded path:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRunWatchdog_ParentDead(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, nil)
	h.proc.alive[testParentPID] = false
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "session process gone" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "session process gone")
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after parent-dead exit")
	}
}

func TestRunWatchdog_Revival(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	// Simulate the session reviving: new bytes land in the transcript after
	// arming.
	extra := []byte(
		`{"type":"user","message":{"role":"user","content":"more"},"timestamp":"2026-07-05T12:05:00.000Z"}` + "\n",
	)
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening transcript for append: %v", err)
	}
	if _, writeErr := f.Write(extra); writeErr != nil {
		t.Fatalf("appending to transcript: %v", writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("closing transcript: %v", closeErr)
	}

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "session revived" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "session revived")
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after revival exit")
	}
}

func TestRunWatchdog_TranscriptUnreadable(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID,
		Transcript: filepath.Join(dir, "does-not-exist.jsonl"), Offset: 0, ArmedAt: now,
	}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "transcript unreadable" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "transcript unreadable")
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after transcript-unreadable exit")
	}
}

// --- RunWatchdog: goal transitions ---

func TestRunWatchdog_GoalMet(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_met.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, []judgeResult{
		{verdict: JudgeVerdict{Notify: true, Urgency: UrgencyDone, Task: "ship it", Body: "all done", Reason: "r"}},
	})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "goal met" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "goal met")
	}
	if h.judge.callCount() != 1 {
		t.Errorf("judge calls = %d, want 1", h.judge.callCount())
	}
	if h.judge.modes[0] != JudgeModeCompose {
		t.Errorf("judge mode = %v, want JudgeModeCompose", h.judge.modes[0])
	}
	if h.log.count() != 2 {
		t.Errorf("log entries = %d, want 2 (one send record, one exit record)", h.log.count())
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", h.send.sendCount())
	}
	n := h.send.notifications()[0]
	if n.Urgency != UrgencyDone {
		t.Errorf("Urgency = %v, want UrgencyDone", n.Urgency)
	}
	wantTitle := "proj · ship it"
	if n.Title != wantTitle {
		t.Errorf("Title = %q, want %q", n.Title, wantTitle)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after goal-met exit")
	}
	// The wake that actually notifies happens after the first scheduled
	// sleep (now+5m), so query comfortably after that instant.
	if since := st.SinceLastNotify(now.Add(time.Hour)); since < 0 {
		t.Error("MarkNotified was not called")
	}
}

func TestRunWatchdog_GoalMet_JudgeErrorFallsBack(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_met.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, []judgeResult{{err: errors.New("judge exploded")}})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "goal met" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "goal met")
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", h.send.sendCount())
	}
	n := h.send.notifications()[0]
	if n.Urgency != UrgencyDone {
		t.Errorf("Urgency = %v, want UrgencyDone", n.Urgency)
	}
	if !contains(n.Title, "goal complete") {
		t.Errorf("Title = %q, want it to contain %q", n.Title, "goal complete")
	}
	if lockExists(st) {
		t.Error("lockfile still exists after fallback goal-met exit")
	}
}

func TestRunWatchdog_GoalFailed(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_failed.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, []judgeResult{
		{verdict: JudgeVerdict{Notify: true, Urgency: UrgencyBlocked, Task: "stuck", Body: "gave up", Reason: "r"}},
	})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "goal failed" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "goal failed")
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", h.send.sendCount())
	}
	if n := h.send.notifications()[0]; n.Urgency != UrgencyBlocked {
		t.Errorf("Urgency = %v, want UrgencyBlocked", n.Urgency)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after goal-failed exit")
	}
}

func TestRunWatchdog_GoalCleared(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_cleared.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "goal cleared" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "goal cleared")
	}
	if h.judge.callCount() != 0 {
		t.Errorf("judge calls = %d, want 0", h.judge.callCount())
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after goal-cleared exit")
	}
}

// --- RunWatchdog: staleness ---

func TestRunWatchdog_StaleNotifyFalseTwice_ThenNoMoreJudgeCalls(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "tasks_live.jsonl")
	offset := scannedBytes(t, transcript)
	// Far past every LaunchedAt in the fixture (2026-07-05T05:xx) so every
	// live task looks stale (no output file exists on this machine either,
	// so staleness is judged off LaunchedAt).
	armedAt := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: armedAt,
	}
	writeLock(t, st, lk)

	h := newTestHarness(armedAt, 4, []judgeResult{
		{verdict: JudgeVerdict{Notify: false, Reason: "still working"}},
		{verdict: JudgeVerdict{Notify: false, Reason: "still working"}},
	})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "canceled" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "canceled")
	}
	if h.judge.callCount() != 2 {
		t.Errorf("judge calls = %d, want 2 (third stale wake must make no call)", h.judge.callCount())
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	wantSleeps := []time.Duration{
		5 * time.Minute,
		30 * time.Minute, // 15m doubled after first notify=false
		60 * time.Minute, // 30m doubled after second notify=false
		60 * time.Minute, // hourly, no more doubling (budget exhausted)
	}
	gotSleeps := h.clock.sleepDurations()
	if len(gotSleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", gotSleeps, wantSleeps)
	}
	for i, want := range wantSleeps {
		if gotSleeps[i] != want {
			t.Errorf("sleep[%d] = %v, want %v (full: %v)", i, gotSleeps[i], want, gotSleeps)
		}
	}
	// "canceled" leaves the lock untouched (we may already be superseded).
	if !lockExists(st) {
		t.Error("lockfile was removed on canceled exit; it must be left alone")
	}
}

func TestRunWatchdog_StaleNotifyTrue(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "tasks_live.jsonl")
	offset := scannedBytes(t, transcript)
	armedAt := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: armedAt,
	}
	writeLock(t, st, lk)

	h := newTestHarness(armedAt, -1, []judgeResult{
		{verdict: JudgeVerdict{
			Notify: true, Urgency: UrgencyInfo, Task: "investigate", Body: "silent a while", Reason: "r",
		}},
	})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "stalled ping" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "stalled ping")
	}
	if h.judge.callCount() != 1 {
		t.Errorf("judge calls = %d, want 1", h.judge.callCount())
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after stalled-ping exit")
	}
}

func TestRunWatchdog_StaleJudgeError_ConsumesBudgetSilently(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "tasks_live.jsonl")
	offset := scannedBytes(t, transcript)
	armedAt := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: armedAt,
	}
	writeLock(t, st, lk)

	h := newTestHarness(armedAt, 3, []judgeResult{
		{err: errors.New("judge down")},
	})
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "canceled" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "canceled")
	}
	// Budget is 2; an erroring judge must still consume it, so exactly 2
	// calls happen (wake1, wake2) and the third stale wake makes none.
	if h.judge.callCount() != 2 {
		t.Errorf("judge calls = %d, want 2 (error must consume budget)", h.judge.callCount())
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
}

// --- RunWatchdog: ceiling ---

func TestRunWatchdog_Ceiling(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	armedAt := now.Add(-5 * time.Hour) // already past the 4h ceiling

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: armedAt,
	}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "ceiling" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "ceiling")
	}
	if h.judge.callCount() != 0 {
		t.Errorf("judge calls = %d, want 0 (ceiling send is deterministic)", h.judge.callCount())
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount = %d, want 1", h.send.sendCount())
	}
	if n := h.send.notifications()[0]; n.Urgency != UrgencyInfo {
		t.Errorf("Urgency = %v, want UrgencyInfo", n.Urgency)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after ceiling exit")
	}
}

// --- RunWatchdog: canceled leaves lock alone ---

func TestRunWatchdog_Canceled_LeavesLockUntouched(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	// maxSleeps: 0 means even the very first Sleep call fails immediately.
	h := newTestHarness(now, 0, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "canceled" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "canceled")
	}
	if !lockExists(st) {
		t.Error("lockfile was removed on canceled exit; it must be left alone")
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
}

// contains reports whether s contains substr (avoids importing strings just
// for this one check spread across a couple of tests).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	n, m := len(s), len(substr)
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == substr {
			return i
		}
	}
	return -1
}

// --- WriteWatchdogLock / ReapWatchdog ---

func TestWriteWatchdogLock_NoPriorOwner_WritesLockNoKill(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	lk := WatchdogLock{PID: 111, ParentPID: 222, Transcript: "/tmp/t.jsonl", Offset: 10, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, lk, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	if len(h.proc.killedPIDs()) != 0 {
		t.Errorf("killed = %v, want none", h.proc.killedPIDs())
	}
	data, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading written lock: %v", err)
	}
	var got WatchdogLock
	if unmarshalErr := json.Unmarshal(data, &got); unmarshalErr != nil {
		t.Fatalf("unmarshaling written lock: %v", unmarshalErr)
	}
	sameLock := got.PID == lk.PID && got.ParentPID == lk.ParentPID &&
		got.Transcript == lk.Transcript && got.Offset == lk.Offset
	if !sameLock {
		t.Errorf("written lock = %+v, want %+v", got, lk)
	}
}

func TestWriteWatchdogLock_KillsLivePriorOwner(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	prior := WatchdogLock{PID: 555, ParentPID: 222, Transcript: "/tmp/old.jsonl", Offset: 1, ArmedAt: time.Now()}
	writeLock(t, st, prior)
	h.proc.alive[555] = true

	next := WatchdogLock{PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	killed := h.proc.killedPIDs()
	if len(killed) != 1 || killed[0] != 555 {
		t.Errorf("killed = %v, want [555]", killed)
	}

	data, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading written lock: %v", err)
	}
	var got WatchdogLock
	if unmarshalErr := json.Unmarshal(data, &got); unmarshalErr != nil {
		t.Fatalf("unmarshaling written lock: %v", unmarshalErr)
	}
	if got.PID != 666 {
		t.Errorf("written lock PID = %d, want 666 (new owner)", got.PID)
	}
}

func TestWriteWatchdogLock_NeverKillsDeadPriorOwner(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	prior := WatchdogLock{PID: 555, ParentPID: 222, Transcript: "/tmp/old.jsonl", Offset: 1, ArmedAt: time.Now()}
	writeLock(t, st, prior)
	h.proc.alive[555] = false // dead

	next := WatchdogLock{PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	if killed := h.proc.killedPIDs(); len(killed) != 0 {
		t.Errorf("killed = %v, want none (prior owner was dead)", killed)
	}
}

func TestReapWatchdog_NoLock_Succeeds(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Errorf("ReapWatchdog() error = %v, want nil", err)
	}
}

func TestReapWatchdog_KillsAliveOwnerAndRemovesLock_IdempotentTwice(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	lk := WatchdogLock{PID: 777, ParentPID: 222, Transcript: "/tmp/t.jsonl", Offset: 1, ArmedAt: time.Now()}
	writeLock(t, st, lk)
	h.proc.alive[777] = true

	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Fatalf("ReapWatchdog() first call error = %v", err)
	}
	if killed := h.proc.killedPIDs(); len(killed) != 1 || killed[0] != 777 {
		t.Errorf("killed = %v, want [777]", killed)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after ReapWatchdog")
	}

	// Idempotent: calling again on the now-lockless dir is still a success,
	// and does not attempt to kill anything again.
	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Fatalf("ReapWatchdog() second call error = %v", err)
	}
	if killed := h.proc.killedPIDs(); len(killed) != 1 {
		t.Errorf("killed after second Reap = %v, want still just [777]", killed)
	}
}

func TestReapWatchdog_DeadOwner_RemovesLockNoKill(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	lk := WatchdogLock{PID: 888, ParentPID: 222, Transcript: "/tmp/t.jsonl", Offset: 1, ArmedAt: time.Now()}
	writeLock(t, st, lk)
	h.proc.alive[888] = false

	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Fatalf("ReapWatchdog() error = %v", err)
	}
	if killed := h.proc.killedPIDs(); len(killed) != 0 {
		t.Errorf("killed = %v, want none", killed)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after ReapWatchdog on dead owner")
	}
}
