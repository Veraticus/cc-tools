package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// watchdogLockFile is the name, within SessionState.Dir, of the watchdog
// lockfile.
const watchdogLockFile = "watchdog.lock"

// initialStaleBudget is how many decide-mode judge calls a single
// RunWatchdog lifetime may spend on staleness checks (step e). Spent on
// every actual judge call, success or failure — never on a notify=true call,
// since that path exits immediately anyway.
const initialStaleBudget = 2

// staleThreshold is how long a live task's output can go untouched (or, for
// a task with no output file at all, how long since it launched) before it
// counts as stale for step e.
const staleThreshold = 15 * time.Minute

// watchdogCeiling is the hard lifetime limit measured from
// WatchdogLock.ArmedAt; past this a watchdog gives up waiting and sends a
// deterministic "still parked" notification (step f).
const watchdogCeiling = 4 * time.Hour

// WatchdogLock is the on-disk record of which process currently owns the
// watchdog for a session. Written by WriteWatchdogLock, read and validated
// by RunWatchdog on every wake, removed by RunWatchdog's exit paths and by
// ReapWatchdog.
type WatchdogLock struct {
	PID int `json:"pid"`
	// ParentPID is the claude process observed at arming; RunWatchdog exits
	// silently once this process is gone.
	ParentPID int `json:"parent_pid"`
	// Transcript is the session transcript path scanned on every recheck.
	Transcript string `json:"transcript"`
	// Offset is ScanResult.BytesScanned at arming: growth past this means
	// the session revived on its own.
	Offset  int64     `json:"offset"`
	ArmedAt time.Time `json:"armed_at"`
}

// WatchdogDeps is every external dependency RunWatchdog, WriteWatchdogLock,
// and ReapWatchdog need, fully injected so tests can run the whole loop
// against fake clocks, fake processes, and fake judge/sender/log calls.
type WatchdogDeps struct {
	Now func() time.Time
	// Sleep waits for d, returning false if ctx was canceled first instead
	// of the wait completing.
	Sleep     func(ctx context.Context, d time.Duration) bool
	ProcAlive func(pid int) bool
	Kill      func(pid int) error
	SelfPID   func() int
	Judge     func(ctx context.Context, digest string, mode JudgeMode) (JudgeVerdict, error)
	Send      func(ctx context.Context, n Notification) error
	// Log records a decision. Implementations must swallow their own
	// errors — logging must never be able to kill the watchdog loop.
	Log func(rec DecisionRecord)
}

// DefaultWatchdogDeps wires WatchdogDeps for production use: a real clock, a
// timer+ctx select for Sleep, signal-0 for ProcAlive, and SIGTERM for Kill.
func DefaultWatchdogDeps(j Judge, s Sender, l DecisionLog) WatchdogDeps {
	return WatchdogDeps{
		Now:       time.Now,
		Sleep:     sleepOrCancel,
		ProcAlive: procAlive,
		Kill:      killProcess,
		SelfPID:   os.Getpid,
		Judge:     j.Evaluate,
		Send:      s.Send,
		Log: func(rec DecisionRecord) {
			_ = l.Append(rec)
		},
	}
}

// sleepOrCancel waits for d, or until ctx is canceled, whichever comes
// first.
func sleepOrCancel(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// procAlive reports whether pid is alive via a signal-0 probe, which checks
// existence/permission without actually signaling the process.
func procAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// killProcess sends SIGTERM to pid.
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("notify: finding process %d: %w", pid, err)
	}
	if sigErr := proc.Signal(syscall.SIGTERM); sigErr != nil {
		return fmt.Errorf("notify: sending SIGTERM to pid %d: %w", pid, sigErr)
	}
	return nil
}

// WriteWatchdogLock claims watchdog ownership for the caller: if a live
// prior owner holds the lock, deps.Kill is sent to it first, so there is
// never more than one watcher for a session. It then (re)writes the lock
// unconditionally with lk.
func WriteWatchdogLock(st SessionState, lk WatchdogLock, deps WatchdogDeps) error {
	path := filepath.Join(st.Dir, watchdogLockFile)

	//nolint:gosec // Path comes from trusted caller-composed session dir
	if data, err := os.ReadFile(path); err == nil {
		var existing WatchdogLock
		if json.Unmarshal(data, &existing) == nil && deps.ProcAlive(existing.PID) {
			if killErr := deps.Kill(existing.PID); killErr != nil {
				return fmt.Errorf("notify: killing prior watchdog owner: %w", killErr)
			}
		}
	}

	if err := os.MkdirAll(st.Dir, 0o750); err != nil {
		return fmt.Errorf("notify: creating session state dir: %w", err)
	}
	data, err := json.Marshal(lk)
	if err != nil {
		return fmt.Errorf("notify: marshaling watchdog lock: %w", err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		return fmt.Errorf("notify: writing watchdog lock: %w", writeErr)
	}
	return nil
}

// ReapWatchdog kills the lock's owner if it is alive and removes the
// lockfile. Idempotent: no lock, a dead owner, and an already-removed lock
// are all success. Called on SessionEnd.
func ReapWatchdog(st SessionState, deps WatchdogDeps) error {
	path := filepath.Join(st.Dir, watchdogLockFile)

	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("notify: reading watchdog lock: %w", err)
	}

	var lock WatchdogLock
	if json.Unmarshal(data, &lock) == nil && deps.ProcAlive(lock.PID) {
		if killErr := deps.Kill(lock.PID); killErr != nil {
			return fmt.Errorf("notify: killing watchdog owner: %w", killErr)
		}
	}

	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return fmt.Errorf("notify: removing watchdog lock: %w", removeErr)
	}
	return nil
}

// wakeState carries the parts of RunWatchdog's loop state that persist
// across wakes: the remaining decide-mode judge budget, and whether the
// next sleep interval owes a doubling from a stale "keep waiting" verdict.
type wakeState struct {
	budget     int
	doubleNext bool
}

// RunWatchdog is the body of the detached recheck process. It sends at most
// one notification in its lifetime, then exits, returning the exit reason
// (also written to the decision log on every exit and every send).
// sessionID is used only for the decision log's SessionID field.
func RunWatchdog(ctx context.Context, st SessionState, meta DigestMeta, deps WatchdogDeps, sessionID string) string {
	lockPath := filepath.Join(st.Dir, watchdogLockFile)
	idx := 0
	next := scheduleInterval(idx)
	state := &wakeState{budget: initialStaleBudget}

	for {
		if !deps.Sleep(ctx, next) {
			return logWatchdogExit(deps, sessionID, "canceled")
		}

		if exit := runWatchdogWake(ctx, st, meta, deps, sessionID, lockPath, state); exit != "" {
			return exit
		}

		idx++
		next = scheduleInterval(idx)
		if state.doubleNext {
			next *= 2
			state.doubleNext = false
		}
	}
}

// runWatchdogWake runs one wake's checks (steps a-f of the recheck loop) and
// returns the exit reason, or "" to keep looping.
func runWatchdogWake(
	ctx context.Context, st SessionState, meta DigestMeta, deps WatchdogDeps,
	sessionID, lockPath string, state *wakeState,
) string {
	lock, ok := readOwnedLock(lockPath, deps)
	if !ok {
		// Missing, unreadable, or owned by a different PID: never touch the
		// lockfile here, it belongs to whoever superseded us.
		return logWatchdogExit(deps, sessionID, "superseded")
	}
	if !deps.ProcAlive(lock.ParentPID) {
		removeLock(lockPath)
		return logWatchdogExit(deps, sessionID, "session process gone")
	}

	res, err := scanTranscriptFile(lock.Transcript)
	if err != nil {
		removeLock(lockPath)
		return logWatchdogExit(deps, sessionID, "transcript unreadable")
	}
	if res.BytesScanned > lock.Offset {
		removeLock(lockPath)
		return logWatchdogExit(deps, sessionID, "session revived")
	}

	now := deps.Now()
	tasks := EnrichTasks(res.LiveTasks, now)

	if exit := handleGoalStatus(ctx, deps, st, lockPath, meta, res, tasks, sessionID, now); exit != "" {
		return exit
	}
	if exit := handleStaleness(ctx, deps, st, lockPath, meta, res, tasks, sessionID, now, state); exit != "" {
		return exit
	}
	return handleCeiling(ctx, deps, st, lockPath, meta, lock, tasks, sessionID, now)
}

// handleGoalStatus implements step d: a goal transition found in the fresh
// scan. GoalNone/GoalActive are not transitions and fall through unchanged.
func handleGoalStatus(
	ctx context.Context, deps WatchdogDeps, st SessionState, lockPath string,
	meta DigestMeta, res ScanResult, tasks []TaskActivity, sessionID string, now time.Time,
) string {
	switch res.Goal.Status {
	case GoalNone, GoalActive:
		return ""
	case GoalMet:
		return finishGoalOutcome(ctx, deps, st, lockPath, meta, res, tasks, sessionID, now,
			"recheck: goal met", meta.Project+" · goal complete",
			truncate(res.Goal.Condition, maxGoalConditionLen), UrgencyDone, "goal met")
	case GoalFailed:
		return finishGoalOutcome(ctx, deps, st, lockPath, meta, res, tasks, sessionID, now,
			"recheck: goal failed", meta.Project+" · goal failed",
			truncate(res.Goal.Condition, maxGoalConditionLen), UrgencyBlocked, "goal failed")
	case GoalCleared:
		removeLock(lockPath)
		return logWatchdogExit(deps, sessionID, "goal cleared")
	default:
		return ""
	}
}

// finishGoalOutcome composes the goal-transition notification (judge
// compose, falling back to a deterministic notification on judge error) and
// sends it.
func finishGoalOutcome(
	ctx context.Context, deps WatchdogDeps, st SessionState, lockPath string,
	meta DigestMeta, res ScanResult, tasks []TaskActivity, sessionID string, now time.Time,
	event, fallbackTitle, fallbackBody string, fallbackUrgency Urgency, exitReason string,
) string {
	n, digest, judgeErr := composeGoalNotification(
		ctx, deps, meta, res, tasks, now, event, fallbackTitle, fallbackBody, fallbackUrgency,
	)
	return finishSend(ctx, deps, st, lockPath, sessionID, now, n, JudgeModeCompose, judgeErr, digest, exitReason)
}

// composeGoalNotification builds the recheck digest for a goal transition
// and returns the notification to send: from the judge on success, or the
// deterministic fallback (with the judge error) on failure.
func composeGoalNotification(
	ctx context.Context, deps WatchdogDeps, meta DigestMeta, res ScanResult, tasks []TaskActivity, now time.Time,
	event, fallbackTitle, fallbackBody string, fallbackUrgency Urgency,
) (Notification, string, error) {
	m := meta
	m.Event = event
	digest := BuildDigest(m, res, tasks, now)

	verdict, err := deps.Judge(ctx, digest, JudgeModeCompose)
	if err != nil {
		return Notification{Title: fallbackTitle, Body: fallbackBody, Urgency: fallbackUrgency}, digest, err
	}
	n := Notification{Title: meta.Project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
	return n, digest, nil
}

// handleStaleness implements step e: when judge budget remains and any live
// task looks stale, a decide-mode judge call decides whether to ping. An
// erroring judge is treated like a notify=false verdict (budget consumed,
// next interval doubled, stay silent) since no send was ever decided for it
// to fall back to — unlike compose mode, where the send is already decided
// and only its text depends on the judge.
func handleStaleness(
	ctx context.Context, deps WatchdogDeps, st SessionState, lockPath string,
	meta DigestMeta, res ScanResult, tasks []TaskActivity, sessionID string, now time.Time, state *wakeState,
) string {
	if state.budget <= 0 {
		return ""
	}
	stale, maxAge := staleTasks(tasks, now)
	if !stale {
		return ""
	}

	m := meta
	m.Event = fmt.Sprintf("recheck: task output silent %s", humanDuration(maxAge))
	digest := BuildDigest(m, res, tasks, now)

	verdict, err := deps.Judge(ctx, digest, JudgeModeDecide)
	state.budget--
	switch {
	case err != nil:
		state.doubleNext = true
		return ""
	case verdict.Notify:
		n := Notification{Title: meta.Project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
		return finishSend(ctx, deps, st, lockPath, sessionID, now, n, JudgeModeDecide, nil, digest, "stalled ping")
	default:
		state.doubleNext = true
		return ""
	}
}

// handleCeiling implements step f: past the 4h ceiling from arming, send a
// deterministic "still parked" notification with no judge involved.
func handleCeiling(
	ctx context.Context, deps WatchdogDeps, st SessionState, lockPath string,
	meta DigestMeta, lock WatchdogLock, tasks []TaskActivity, sessionID string, now time.Time,
) string {
	if now.Sub(lock.ArmedAt) < watchdogCeiling {
		return ""
	}
	n := Notification{
		Title: meta.Project + " · still parked",
		Body: fmt.Sprintf("%d task(s) running, no session activity for %s",
			len(tasks), humanDuration(now.Sub(lock.ArmedAt))),
		Urgency: UrgencyInfo,
	}
	return finishSend(ctx, deps, st, lockPath, sessionID, now, n, JudgeModeNone, nil, "", "ceiling")
}

// finishSend sends n, logs it, marks the session notified, removes the
// lock, and logs+returns exitReason. Shared by every path that both sends a
// notification and exits: a Send failure does not change this sequence —
// RunWatchdog has at most one shot at notifying and no error channel to
// surface a delivery failure through.
func finishSend(
	ctx context.Context, deps WatchdogDeps, st SessionState, lockPath, sessionID string, now time.Time,
	n Notification, mode JudgeMode, judgeErr error, digest string, exitReason string,
) string {
	_ = deps.Send(ctx, n)
	logWatchdogSend(deps, sessionID, n, mode, judgeErr, digest)
	_ = st.MarkNotified(now)
	removeLock(lockPath)
	return logWatchdogExit(deps, sessionID, exitReason)
}

// logWatchdogExit logs the terminal outcome of a RunWatchdog invocation and
// returns reason, so callers can write "return logWatchdogExit(...)".
func logWatchdogExit(deps WatchdogDeps, sessionID, reason string) string {
	deps.Log(DecisionRecord{Time: deps.Now(), SessionID: sessionID, Event: "watchdog", Outcome: reason})
	return reason
}

// logWatchdogSend logs a notification actually dispatched during a
// RunWatchdog invocation, distinct from the exit-reason record
// logWatchdogExit writes for every return.
func logWatchdogSend(
	deps WatchdogDeps, sessionID string, n Notification, mode JudgeMode, judgeErr error, digest string,
) {
	rec := DecisionRecord{
		Time:      deps.Now(),
		SessionID: sessionID,
		Event:     "watchdog",
		Outcome:   "send",
		Urgency:   n.Urgency,
		Title:     n.Title,
		Body:      n.Body,
		Digest:    digest,
	}
	if mode != JudgeModeNone {
		rec.JudgeMode = judgeModeLabel(mode)
	}
	if judgeErr != nil {
		rec.JudgeErr = judgeErr.Error()
	}
	deps.Log(rec)
}

// judgeModeLabel renders a JudgeMode as the lowercase word the decision log
// shows.
func judgeModeLabel(mode JudgeMode) string {
	switch mode {
	case JudgeModeNone:
		return ""
	case JudgeModeCompose:
		return "compose"
	case JudgeModeDecide:
		return "decide"
	default:
		return ""
	}
}

// Wake schedule intervals: sleep watchdogFirstWakeDelay, then
// watchdogSecondWakeDelay, then watchdogThirdWakeDelay, then hourly.
// watchdogThirdWakeIdx names the "2" index golangci-lint's mnd check
// otherwise flags as a bare magic number, mirroring the hoursPerDay pattern
// in digest.go.
const (
	watchdogFirstWakeDelay  = 5 * time.Minute
	watchdogSecondWakeDelay = 15 * time.Minute
	watchdogThirdWakeDelay  = 30 * time.Minute
	watchdogThirdWakeIdx    = 2
)

// scheduleInterval is the wake schedule's base interval (before any
// staleness doubling) for the idx-th sleep.
func scheduleInterval(idx int) time.Duration {
	switch idx {
	case 0:
		return watchdogFirstWakeDelay
	case 1:
		return watchdogSecondWakeDelay
	case watchdogThirdWakeIdx:
		return watchdogThirdWakeDelay
	default:
		return time.Hour
	}
}

// staleTasks reports whether any task in tasks is stale — output written
// more than staleThreshold ago, or (no output file at all) launched more
// than staleThreshold ago — and the longest such silence among the stale
// ones, for the recheck digest's event line.
func staleTasks(tasks []TaskActivity, now time.Time) (bool, time.Duration) {
	var foundStale bool
	var maxAge time.Duration
	for _, t := range tasks {
		age := now.Sub(t.LaunchedAt)
		if t.OutputExists {
			age = now.Sub(t.LastWrite)
		}
		if age <= staleThreshold {
			continue
		}
		foundStale = true
		if age > maxAge {
			maxAge = age
		}
	}
	return foundStale, maxAge
}

// readOwnedLock reads and validates the lock at path: it must exist, parse,
// and carry deps.SelfPID() as its PID, or ok is false.
func readOwnedLock(path string, deps WatchdogDeps) (WatchdogLock, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted caller-composed session dir
	if err != nil {
		return WatchdogLock{}, false
	}
	var lock WatchdogLock
	if unmarshalErr := json.Unmarshal(data, &lock); unmarshalErr != nil {
		return WatchdogLock{}, false
	}
	if lock.PID != deps.SelfPID() {
		return WatchdogLock{}, false
	}
	return lock, true
}

// scanTranscriptFile opens path and scans it once.
func scanTranscriptFile(path string) (ScanResult, error) {
	f, err := os.Open(path) //nolint:gosec // Path is the watchdog lock's own recorded transcript path
	if err != nil {
		return ScanResult{}, fmt.Errorf("notify: opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	res, err := ScanTranscript(f)
	if err != nil {
		return ScanResult{}, fmt.Errorf("notify: scanning transcript: %w", err)
	}
	return res, nil
}

// removeLock deletes the watchdog's own lockfile. This is best-effort
// self-cleanup on RunWatchdog's exit paths (which return only a string,
// with no channel to surface a removal failure); ReapWatchdog is the
// operation that propagates a genuine remove failure to a caller.
func removeLock(path string) {
	_ = os.Remove(path)
}
