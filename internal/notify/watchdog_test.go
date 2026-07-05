package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeProcStartTicks is a scripted PID->ticks table for WatchdogDeps.
// ProcStartTicks. An unset PID reports (0, false), matching the
// "unavailable" fallback production sees on non-Linux or a vanished /proc
// entry — so every existing test that never calls set() exercises that
// fallback (Kill still called) by default.
type fakeProcStartTicks struct {
	mu    sync.Mutex
	ticks map[int]int64
}

func newFakeProcStartTicks() *fakeProcStartTicks {
	return &fakeProcStartTicks{ticks: make(map[int]int64)}
}

func (f *fakeProcStartTicks) set(pid int, ticks int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticks[pid] = ticks
}

func (f *fakeProcStartTicks) get(pid int) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.ticks[pid]
	return t, ok
}

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
	clock      *fakeClock
	proc       *fakeProc
	startTicks *fakeProcStartTicks
	judge      *fakeJudge
	send       *fakeSender
	log        *fakeLog
	deps       WatchdogDeps
}

func newTestHarness(now time.Time, maxSleeps int, script []judgeResult) *testHarness {
	h := &testHarness{
		clock:      &fakeClock{now: now, maxSleeps: maxSleeps},
		proc:       newFakeProc(),
		startTicks: newFakeProcStartTicks(),
		judge:      &fakeJudge{script: script},
		send:       &fakeSender{},
		log:        &fakeLog{},
	}
	h.proc.alive[testParentPID] = true
	h.deps = WatchdogDeps{
		Now:            h.clock.Now,
		Sleep:          h.clock.Sleep,
		ProcAlive:      h.proc.ProcAlive,
		Kill:           h.proc.Kill,
		SelfPID:        func() int { return testSelfPID },
		ProcStartTicks: h.startTicks.get,
		Judge:          h.judge.Evaluate,
		Send:           h.send.Send,
		Log:            h.log.Log,
	}
	return h
}

// splitFixtureLines reads a testdata fixture and returns its lines with line
// terminators intact (each element ends in "\n"), for tests that need to
// arm a watchdog on a goal-active prefix and append a later transition line
// (the realistic offset a production arming would actually produce, versus
// one computed over the whole fixture including the transition itself).
func splitFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	split := bytes.Split(data, []byte("\n"))
	lines := make([][]byte, 0, len(split))
	for _, line := range split {
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append(append([]byte{}, line...), '\n'))
	}
	return lines
}

// activeOnlyTranscript writes fixture's goal-active line(s) (every line but
// the last) to a fresh temp file and returns its path alongside the
// transition line (the fixture's last line) still unwritten, so a test can
// record the offset over the active-only content before appending the
// transition — the realistic offset a production arming actually has,
// versus one computed over the whole fixture.
func activeOnlyTranscript(t *testing.T, fixture string) (string, []byte) {
	t.Helper()
	lines := splitFixtureLines(t, fixture)
	if len(lines) < 2 {
		t.Fatalf("fixture %s has %d line(s), want at least 2 (active + transition)", fixture, len(lines))
	}
	dst := filepath.Join(t.TempDir(), fixture)
	var active []byte
	for _, l := range lines[:len(lines)-1] {
		active = append(active, l...)
	}
	if err := os.WriteFile(dst, active, 0o600); err != nil {
		t.Fatalf("writing active-only transcript %s: %v", dst, err)
	}
	return dst, lines[len(lines)-1]
}

// appendLine appends line to the file at path.
func appendLine(t *testing.T, path string, line []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening %s for append: %v", path, err)
	}
	if _, writeErr := f.Write(line); writeErr != nil {
		t.Fatalf("appending to %s: %v", path, writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("closing %s: %v", path, closeErr)
	}
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
//
// Every test in this section arms with a REALISTIC offset: the byte count
// over the goal-active line(s) only, computed BEFORE the transition line is
// appended — exactly what a production arming has, since the watchdog only
// ever arms while a goal is ACTIVE. Arming with an offset computed over the
// whole fixture (transition line included) can never occur in production
// and was masking the c1 gap: the revival check was preempting the goal
// transition it exists to detect, because the transition record is itself
// transcript growth past the (unrealistic) full-file offset.

func TestRunWatchdog_GoalMet(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript, metLine := activeOnlyTranscript(t, "goal_met.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	appendLine(t, transcript, metLine)

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
	transcript, metLine := activeOnlyTranscript(t, "goal_met.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	appendLine(t, transcript, metLine)

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
	transcript, failedLine := activeOnlyTranscript(t, "goal_failed.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	appendLine(t, transcript, failedLine)

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
	transcript, clearedLine := activeOnlyTranscript(t, "goal_cleared.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	appendLine(t, transcript, clearedLine)

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

// TestRunWatchdog_GoalStillActive_MidLoopGrowth_RevivesNotTransitions proves
// c1's ordering doesn't break the case it must NOT change: transcript growth
// that is NOT a goal transition (goal stays ACTIVE) must still exit "session
// revived" with zero sends, exactly as before — mid-loop goal iterations
// grow the transcript too, but each iteration's own Stop hook re-arms a
// fresh watchdog, so this (superseded) watchdog correctly treats the growth
// as revival.
func TestRunWatchdog_GoalStillActive_MidLoopGrowth_RevivesNotTransitions(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript, _ := activeOnlyTranscript(t, "goal_met.jsonl") // discard the transition line entirely
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	// Append a plain, non-goal-status line: the goal remains ACTIVE (no
	// applyGoalStatus call), but the transcript has grown past Offset.
	extraLines := splitFixtureLines(t, "goal_none.jsonl")
	appendLine(t, transcript, extraLines[0])

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
	writeLock(t, st, lk)

	h := newTestHarness(now, -1, nil)
	meta := DigestMeta{Project: "proj"}

	got := RunWatchdog(context.Background(), st, meta, h.deps, "sess-1")

	if got != "session revived" {
		t.Errorf("RunWatchdog() = %q, want %q", got, "session revived")
	}
	if h.judge.callCount() != 0 {
		t.Errorf("judge calls = %d, want 0", h.judge.callCount())
	}
	if h.send.sendCount() != 0 {
		t.Errorf("sendCount = %d, want 0", h.send.sendCount())
	}
	if lockExists(st) {
		t.Error("lockfile still exists after revival exit")
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

// --- RunWatchdog: stat-first / cached scan (p2) ---

// TestRunWatchdog_NoGrowthWake_ReusesCachedScan proves the "size ==
// lock.Offset" wake reuses a cached scan rather than re-parsing: after a
// first no-growth wake caches its scan, the transcript is made unreadable
// (chmod 0000) and the clock advanced past the ceiling. A wake that
// mistakenly re-parses would hit the permission error and exit "transcript
// unreadable"; a wake that correctly reuses the cache never opens the file
// again and reaches the ceiling send instead, still reporting the 0 live
// tasks the (untouched, cached) goal_none.jsonl scan produced.
func TestRunWatchdog_NoGrowthWake_ReusesCachedScan(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript) // == full file size: no growth, ever
	armedAt := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	lk := WatchdogLock{
		PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: armedAt,
	}
	writeLock(t, st, lk)
	lockPath := filepath.Join(dir, watchdogLockFile)

	h := newTestHarness(armedAt, -1, nil)
	meta := DigestMeta{Project: "proj"}
	state := &wakeState{budget: initialStaleBudget}

	// Wake 1: well before the ceiling, transcript still fully readable —
	// the "first such wake" that must perform (and cache) a real scan.
	got := runWatchdogWake(context.Background(), st, meta, h.deps, "sess-1", lockPath, state)
	if got != "" {
		t.Fatalf("wake 1 = %q, want \"\" (no exit yet)", got)
	}
	if h.send.sendCount() != 0 {
		t.Fatalf("sendCount after wake 1 = %d, want 0", h.send.sendCount())
	}

	// Advance past the ceiling and strip all read permission from the
	// transcript. The file's size is unchanged, so this wake still lands in
	// the size == lock.Offset case.
	h.clock.mu.Lock()
	h.clock.now = h.clock.now.Add(watchdogCeiling + time.Minute)
	h.clock.mu.Unlock()
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 does not deny root; cache proof needs non-root euid")
	}
	if err := os.Chmod(transcript, 0o000); err != nil {
		t.Fatalf("chmod transcript unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(transcript, 0o600) })

	got = runWatchdogWake(context.Background(), st, meta, h.deps, "sess-1", lockPath, state)
	if got != "ceiling" {
		t.Errorf("wake 2 = %q, want %q (cached scan must be reused, not a fresh parse)", got, "ceiling")
	}
	if h.send.sendCount() != 1 {
		t.Fatalf("sendCount after wake 2 = %d, want 1", h.send.sendCount())
	}
	if n := h.send.notifications()[0]; !contains(n.Body, "0 task(s)") {
		t.Errorf("ceiling body = %q, want it to report 0 tasks (from the cached, no-live-task scan)", n.Body)
	}
}

// TestRunWatchdog_TranscriptTruncated_ExitsUnreadable covers the size <
// lock.Offset case: truncation or rotation is pathological (no coherent
// growth to reason about), so it is treated like an unreadable transcript
// rather than attempting to parse whatever remains.
func TestRunWatchdog_TranscriptTruncated_ExitsUnreadable(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	const truncateBy = 20
	if err := os.Truncate(transcript, offset-truncateBy); err != nil {
		t.Fatalf("truncating transcript: %v", err)
	}

	lk := WatchdogLock{PID: testSelfPID, ParentPID: testParentPID, Transcript: transcript, Offset: offset, ArmedAt: now}
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
		t.Error("lockfile still exists after truncated-transcript exit")
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

// TestWriteWatchdogLock_KillsLivePriorOwner also exercises s3's
// dep-unavailable fallback: h.startTicks never gets a set() call for PID
// 555, so deps.ProcStartTicks reports (0, false) — the same "unavailable"
// answer production sees on a vanished /proc entry — and killIfOwnerMatches
// falls back to the plain probe-only behavior (Kill called).
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

// TestWriteWatchdogLock_MatchingStartTicks_KillsPriorOwner covers s3's
// "matching ticks" case: the prior lock's StartTicks equals what
// ProcStartTicks reports for that PID right now, so the live process really
// is the one that wrote the lock, and Kill proceeds.
func TestWriteWatchdogLock_MatchingStartTicks_KillsPriorOwner(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	prior := WatchdogLock{
		PID: 555, ParentPID: 222, Transcript: "/tmp/old.jsonl", Offset: 1, ArmedAt: time.Now(), StartTicks: 111,
	}
	writeLock(t, st, prior)
	h.proc.alive[555] = true
	h.startTicks.set(555, 111) // matches the recorded fingerprint

	next := WatchdogLock{PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	killed := h.proc.killedPIDs()
	if len(killed) != 1 || killed[0] != 555 {
		t.Errorf("killed = %v, want [555]", killed)
	}
}

// TestWriteWatchdogLock_RecycledPriorOwnerPID_SkipsKill covers s3's
// "recycled PID" case: a process is alive at the prior lock's PID, but its
// current start ticks differ from what was recorded — it is not the
// process that wrote the lock, so Kill must not be sent to it. The lock is
// still overwritten with the new owner.
func TestWriteWatchdogLock_RecycledPriorOwnerPID_SkipsKill(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	prior := WatchdogLock{
		PID: 555, ParentPID: 222, Transcript: "/tmp/old.jsonl", Offset: 1, ArmedAt: time.Now(), StartTicks: 111,
	}
	writeLock(t, st, prior)
	h.proc.alive[555] = true
	h.startTicks.set(555, 222) // a different process now holds PID 555

	next := WatchdogLock{PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	if killed := h.proc.killedPIDs(); len(killed) != 0 {
		t.Errorf("killed = %v, want none (recycled PID must not be killed)", killed)
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
		t.Errorf("written lock PID = %d, want 666 (new owner, lock still overwritten)", got.PID)
	}
}

// TestWriteWatchdogLock_PopulatesStartTicksFromDeps proves the arming side
// of s3: a written lock whose StartTicks was zero gets it filled in from
// deps.ProcStartTicks, so a later Kill of that PID has a fingerprint to
// check against.
func TestWriteWatchdogLock_PopulatesStartTicksFromDeps(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)
	h.startTicks.set(666, 123456)

	next := WatchdogLock{PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now()}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading written lock: %v", err)
	}
	var got WatchdogLock
	if unmarshalErr := json.Unmarshal(data, &got); unmarshalErr != nil {
		t.Fatalf("unmarshaling written lock: %v", unmarshalErr)
	}
	if got.StartTicks != 123456 {
		t.Errorf("written lock StartTicks = %d, want 123456", got.StartTicks)
	}
}

// TestWriteWatchdogLock_PreservesExplicitStartTicks proves WriteWatchdogLock
// only populates StartTicks "when the field is zero": a caller-supplied
// nonzero value is left alone even if deps.ProcStartTicks would report
// something else.
func TestWriteWatchdogLock_PreservesExplicitStartTicks(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)
	h.startTicks.set(666, 999999)

	next := WatchdogLock{
		PID: 666, ParentPID: 222, Transcript: "/tmp/new.jsonl", Offset: 2, ArmedAt: time.Now(), StartTicks: 42,
	}
	if err := WriteWatchdogLock(st, next, h.deps); err != nil {
		t.Fatalf("WriteWatchdogLock() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "watchdog.lock"))
	if err != nil {
		t.Fatalf("reading written lock: %v", err)
	}
	var got WatchdogLock
	if unmarshalErr := json.Unmarshal(data, &got); unmarshalErr != nil {
		t.Fatalf("unmarshaling written lock: %v", unmarshalErr)
	}
	if got.StartTicks != 42 {
		t.Errorf("written lock StartTicks = %d, want 42 (explicit value preserved)", got.StartTicks)
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

// TestReapWatchdog_KillsAliveOwnerAndRemovesLock_IdempotentTwice also
// exercises s3's dep-unavailable fallback (see the analogous comment on
// TestWriteWatchdogLock_KillsLivePriorOwner): h.startTicks never gets a
// set() call for PID 777, so Kill falls back to the plain probe.
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

// TestReapWatchdog_MatchingStartTicks_KillsOwner covers s3's "matching
// ticks" case for ReapWatchdog: the lock's StartTicks matches what
// ProcStartTicks reports for that PID right now, so Kill proceeds.
func TestReapWatchdog_MatchingStartTicks_KillsOwner(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	lk := WatchdogLock{
		PID: 777, ParentPID: 222, Transcript: "/tmp/t.jsonl", Offset: 1, ArmedAt: time.Now(), StartTicks: 111,
	}
	writeLock(t, st, lk)
	h.proc.alive[777] = true
	h.startTicks.set(777, 111)

	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Fatalf("ReapWatchdog() error = %v", err)
	}
	if killed := h.proc.killedPIDs(); len(killed) != 1 || killed[0] != 777 {
		t.Errorf("killed = %v, want [777]", killed)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after ReapWatchdog")
	}
}

// TestReapWatchdog_RecycledOwnerPID_SkipsKillButRemovesLock covers s3's
// "recycled PID" case for ReapWatchdog: a live process sits at the lock's
// PID, but its start ticks don't match what was recorded, so it is not the
// process that wrote the lock and must not be killed. The lockfile is still
// removed (ReapWatchdog's unconditional cleanup).
func TestReapWatchdog_RecycledOwnerPID_SkipsKillButRemovesLock(t *testing.T) {
	dir := t.TempDir()
	st := SessionState{Dir: dir}
	h := newTestHarness(time.Now(), -1, nil)

	lk := WatchdogLock{
		PID: 777, ParentPID: 222, Transcript: "/tmp/t.jsonl", Offset: 1, ArmedAt: time.Now(), StartTicks: 111,
	}
	writeLock(t, st, lk)
	h.proc.alive[777] = true
	h.startTicks.set(777, 999) // a different process now holds PID 777

	if err := ReapWatchdog(st, h.deps); err != nil {
		t.Fatalf("ReapWatchdog() error = %v", err)
	}
	if killed := h.proc.killedPIDs(); len(killed) != 0 {
		t.Errorf("killed = %v, want none (recycled PID must not be killed)", killed)
	}
	if lockExists(st) {
		t.Error("lockfile still exists after ReapWatchdog on a recycled-PID owner")
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
