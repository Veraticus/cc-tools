package notify

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

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
// WatchdogArmRequest.ArmedAt; past this a watchdog gives up waiting and
// sends a deterministic "still parked" notification (step f).
const watchdogCeiling = 4 * time.Hour

// WatchdogArmRequest is what Pipeline.arm hands to Watchdog.Arm to start (or
// supersede) coverage for a session: everything RunWatchdog's core needs to
// run, with no lock file or on-disk session directory involved. Offset is
// ScanResult.BytesScanned at arming — growth past it means the session
// revived on its own.
type WatchdogArmRequest struct {
	SessionID string
	// Transcript is the session transcript path scanned on every recheck.
	Transcript string
	Offset     int64
	// ParentPID is the claude process observed at arming; RunWatchdog exits
	// silently once this process is gone. Zero disables the check entirely
	// (see parentAlive) rather than ever being probed as alive or dead.
	ParentPID int
	// Workspace is the arming session's tmux locator (Pipeline.Workspace),
	// carried so a watchdog notification's body can say where the session
	// lives — the watchdog fires long after the hook that armed it, with no
	// process context of its own to resolve one from.
	Workspace string
	Meta      DigestMeta
	ArmedAt   time.Time
	// GoalArmed marks a watchdog armed while the session's goal was active
	// (ScanResult.Goal.Status == GoalActive at arming). The /goal evaluator's
	// met/failed verdict lands in the transcript AFTER the Stop-hook scan
	// that arms this watchdog — hook ordering is undocumented, verified in
	// production 2026-07-08, with the verdict landing at roughly Stop+seconds
	// while the standard schedule's first wake is +5m — so a goal-armed
	// watchdog's first wakes are the delivery path for the goal-complete
	// ping and must be seconds, not minutes. See scheduleInterval.
	GoalArmed bool
}

// Watchdog is Pipeline's interface onto arming and reaping the in-daemon
// watchdog for a session: Arm starts (or, for a session already covered,
// supersedes) one watchdog goroutine; Reap cancels it on SessionEnd. A nil
// Pipeline.Watchdog makes Pipeline.arm a no-op — the hook client's inline
// fallback's documented degraded mode: when notifyd is unreachable, that
// single invocation has no long-lived goroutine to arm one on, so it runs
// with no watchdog coverage rather than spawning a detached process.
type Watchdog interface {
	Arm(req WatchdogArmRequest)
	Reap(sessionID string)
}

// WatchdogDeps is every external dependency RunWatchdog needs, fully
// injected so tests can run the whole loop against fake clocks, fake
// processes, and fake judge/sender/log calls.
type WatchdogDeps struct {
	Now func() time.Time
	// Sleep waits for d, returning false if ctx was canceled first instead
	// of the wait completing.
	Sleep     func(ctx context.Context, d time.Duration) bool
	ProcAlive func(pid int) bool
	Judge     func(ctx context.Context, digest string, mode JudgeMode) (JudgeVerdict, error)
	Send      func(ctx context.Context, n Notification) error
	// Log records a decision. Implementations must swallow their own
	// errors — logging must never be able to kill the watchdog loop.
	Log func(rec DecisionRecord)
	// ClaimSend atomically resolves whether the watchdog's one send may
	// proceed at now, recording it as the session's last notification when
	// it wins — the same check-and-mark the pipeline's judged sends run
	// (DedupeState.ClaimSend), so a watchdog can never re-ping a session
	// some hook event already pinged within the dedupe window. The returned
	// duration is how long before now the session last notified. nil means
	// always allowed (no shared state to consult).
	ClaimSend func(ctx context.Context, sessionID string, now time.Time, message string) (bool, time.Duration)
}

// DefaultWatchdogDeps wires WatchdogDeps for production use: a real clock, a
// timer+ctx select for Sleep, and signal-0 for ProcAlive.
func DefaultWatchdogDeps(j Judge, s Sender, l DecisionLog) WatchdogDeps {
	return WatchdogDeps{
		Now:       time.Now,
		Sleep:     sleepOrCancel,
		ProcAlive: procAlive,
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

// parentAlive reports whether parentPID is still alive, per WatchdogDeps.
// ProcAlive — except a zero parentPID (no process observed at arming, or
// not supplied) disables the probe entirely rather than ever answering
// alive or dead: pid 0 is not a real process to signal, and treating it as
// "gone" would exit every such watchdog on its very next wake.
func parentAlive(deps WatchdogDeps, parentPID int) bool {
	if parentPID == 0 {
		return true
	}
	return deps.ProcAlive(parentPID)
}

// wakeState carries the parts of RunWatchdog's loop state that persist
// across wakes: the remaining decide-mode judge budget, and whether the
// next sleep interval owes a doubling from a stale "keep waiting" verdict.
type wakeState struct {
	budget     int
	doubleNext bool

	// cachedRes/cachedSize/haveCache back wakeNoGrowth: a wake whose
	// transcript size matches req.Offset exactly reuses the last scan taken
	// at that same size rather than re-parsing, since live tasks cannot
	// change without transcript growth. haveCache is false until the first
	// such wake performs the one scan it seeds the cache with.
	cachedRes  ScanResult
	cachedSize int64
	haveCache  bool
}

// RunWatchdog is the body of the in-daemon watchdog goroutine. It sends at
// most one notification in its lifetime, then exits, returning the exit
// reason (also written to the decision log on every exit and every send).
// Ownership is entirely ctx-driven: the caller (Watchdog.Arm) cancels ctx on
// SessionEnd or on superseding this session with a fresh Arm, and RunWatchdog
// exits "canceled" the moment that happens — there is no lock file to read
// or remove.
func RunWatchdog(ctx context.Context, req WatchdogArmRequest, deps WatchdogDeps) string {
	idx := 0
	next := scheduleInterval(idx, req.GoalArmed)
	state := &wakeState{budget: initialStaleBudget}

	for {
		if !deps.Sleep(ctx, next) {
			return logWatchdogExit(deps, req.SessionID, "canceled")
		}

		if exit := runWatchdogWake(ctx, req, deps, state); exit != "" {
			return exit
		}

		idx++
		next = scheduleInterval(idx, req.GoalArmed)
		if state.doubleNext {
			next *= 2
			state.doubleNext = false
		}
	}
}

// runWatchdogWake runs one wake's checks (steps a-f of the recheck loop) and
// returns the exit reason, or "" to keep looping.
//
// It stats the transcript before ever parsing it: BytesScanned equals file
// size for a well-formed, newline-terminated JSONL transcript (see
// ScanResult.BytesScanned's doc), so the stat size is a free proxy for "did
// anything land since arming/the last wake" without paying a full parse on
// every wake. Three cases follow: size < req.Offset is truncation or
// rotation — pathological, with no coherent growth to reason about, so it
// is treated like an unreadable transcript (wakeNoGrowth and wakeGrowth
// below cover the other two).
func runWatchdogWake(ctx context.Context, req WatchdogArmRequest, deps WatchdogDeps, state *wakeState) string {
	if !parentAlive(deps, req.ParentPID) {
		return logWatchdogExit(deps, req.SessionID, "session process gone")
	}

	info, statErr := os.Stat(req.Transcript)
	if statErr != nil {
		return logWatchdogExit(deps, req.SessionID, "transcript unreadable")
	}
	size := info.Size()

	switch {
	case size < req.Offset:
		return logWatchdogExit(deps, req.SessionID, "transcript unreadable")
	case size == req.Offset:
		return wakeNoGrowth(ctx, req, deps, size, state)
	default:
		return wakeGrowth(ctx, req, deps, state)
	}
}

// wakeNoGrowth handles a wake whose transcript size matches req.Offset
// exactly: nothing has been appended since arming (or since the previous
// wake), so no goal transition and no revival are possible — both require a
// new record, and there is none. It exists purely to keep staleness/ceiling
// fed with LiveTasks, via cachedScan rather than a fresh parse every wake.
func wakeNoGrowth(ctx context.Context, req WatchdogArmRequest, deps WatchdogDeps, size int64, state *wakeState) string {
	res, err := cachedScan(state, req.Transcript, size)
	if err != nil {
		return logWatchdogExit(deps, req.SessionID, "transcript unreadable")
	}
	now := deps.Now()
	tasks := EnrichTasks(res.LiveTasks, now)
	if exit := handleStaleness(ctx, deps, req, res, tasks, now, state); exit != "" {
		return exit
	}
	return handleCeiling(ctx, deps, req, tasks, now)
}

// wakeGrowth handles a wake whose transcript has grown past req.Offset: a
// fresh scan is required. Goal status is checked BEFORE the revival gate:
// mid-loop goal iterations do grow the transcript, but each iteration's own
// Stop hook re-arms a fresh watchdog (supersession), so treating growth as
// "revived" is correct only when the goal has not just terminally
// transitioned. A terminal transition (met/failed) has no subsequent Stop
// hook — the Stop that produced it was allowed to complete — so this wake
// is the only thing that can ever deliver that ping, and it would otherwise
// be silently preempted by the revival check, since the transition record
// is itself the growth that trips it. GoalCleared exits silently as before;
// GoalNone/GoalActive fall through to the revival check unaffected.
func wakeGrowth(ctx context.Context, req WatchdogArmRequest, deps WatchdogDeps, state *wakeState) string {
	res, err := scanTranscriptFile(req.Transcript)
	if err != nil {
		return logWatchdogExit(deps, req.SessionID, "transcript unreadable")
	}

	now := deps.Now()
	tasks := EnrichTasks(res.LiveTasks, now)

	if exit := handleGoalStatus(ctx, deps, req, res, tasks, now); exit != "" {
		return exit
	}
	if res.BytesScanned > req.Offset {
		return logWatchdogExit(deps, req.SessionID, "session revived")
	}
	if exit := handleStaleness(ctx, deps, req, res, tasks, now, state); exit != "" {
		return exit
	}
	return handleCeiling(ctx, deps, req, tasks, now)
}

// cachedScan returns the ScanResult for a wakeNoGrowth wake at transcript
// size size: reused from state if the last cached scan was taken at this
// same size, otherwise performed fresh and cached for later same-size
// wakes. Live tasks cannot change without transcript growth, so this scan
// only needs to happen once per distinct (unchanging) size.
func cachedScan(state *wakeState, transcript string, size int64) (ScanResult, error) {
	if state.haveCache && state.cachedSize == size {
		return state.cachedRes, nil
	}
	res, err := scanTranscriptFile(transcript)
	if err != nil {
		return ScanResult{}, err
	}
	state.cachedRes = res
	state.cachedSize = size
	state.haveCache = true
	return res, nil
}

// handleGoalStatus implements step d: a goal transition found in the fresh
// scan. GoalNone/GoalActive are not transitions and fall through unchanged.
func handleGoalStatus(
	ctx context.Context, deps WatchdogDeps, req WatchdogArmRequest, res ScanResult, tasks []TaskActivity, now time.Time,
) string {
	switch res.Goal.Status {
	case GoalNone, GoalActive:
		return ""
	case GoalMet:
		return finishGoalOutcome(ctx, deps, req, res, tasks, now,
			"recheck: goal met", req.Meta.Project+" · goal complete",
			truncateWords(res.Goal.Condition, maxGoalConditionLen), UrgencyDone, "goal met")
	case GoalFailed:
		return finishGoalOutcome(ctx, deps, req, res, tasks, now,
			"recheck: goal failed", req.Meta.Project+" · goal failed",
			truncateWords(res.Goal.Condition, maxGoalConditionLen), UrgencyBlocked, "goal failed")
	case GoalCleared:
		return logWatchdogExit(deps, req.SessionID, "goal cleared")
	default:
		return ""
	}
}

// finishGoalOutcome composes the goal-transition notification (judge
// compose, falling back to a deterministic notification on judge error) and
// sends it.
func finishGoalOutcome(
	ctx context.Context, deps WatchdogDeps, req WatchdogArmRequest, res ScanResult, tasks []TaskActivity, now time.Time,
	event, fallbackTitle, fallbackBody string, fallbackUrgency Urgency, exitReason string,
) string {
	n, digest, judgeErr := composeGoalNotification(
		ctx, deps, req.Meta, res, tasks, now, event, fallbackTitle, fallbackBody, fallbackUrgency,
	)
	return finishSend(ctx, deps, req, n, JudgeModeCompose, judgeErr, digest, exitReason)
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

	// This compose call is a deliberately budget-exempt one-shot terminal
	// call, distinct from handleStaleness's initialStaleBudget: that budget
	// (2) caps only decide-mode staleness judgments (step e) and does not
	// apply here. A goal transition (met/failed) reaches this call exactly
	// once per watchdog lifetime, with no subsequent Stop hook able to retry
	// it, so a global cap shared with staleness would let an already-
	// exhausted staleness budget silently swallow the mandatory goal
	// met/failed ping. Worst-case judge calls per watchdog lifetime is
	// therefore 3 (2 staleness + 1 terminal compose); the ≤1-send invariant
	// is unaffected, since RunWatchdog still sends at most once regardless of
	// how many judge calls it took to get there.
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
	ctx context.Context, deps WatchdogDeps, req WatchdogArmRequest, res ScanResult, tasks []TaskActivity,
	now time.Time, state *wakeState,
) string {
	// This budget caps only decide-mode staleness judgments (this function);
	// it does not apply to the goal-transition compose call in
	// composeGoalNotification, which is a deliberately exempt one-shot
	// terminal call — see the comment there for why a shared cap would be
	// wrong. Worst case, a watchdog spends this budget's 2 calls here plus 1
	// more on that terminal compose: 3 judge calls per lifetime, still ≤1
	// send.
	if state.budget <= 0 {
		return ""
	}
	stale, maxAge := staleTasks(tasks, now)
	if !stale {
		return ""
	}

	m := req.Meta
	m.Event = fmt.Sprintf("recheck: task output silent %s", humanDuration(maxAge))
	digest := BuildDigest(m, res, tasks, now)

	verdict, err := deps.Judge(ctx, digest, JudgeModeDecide)
	state.budget--
	switch {
	case err != nil:
		state.doubleNext = true
		return ""
	case verdict.Notify:
		n := Notification{Title: req.Meta.Project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
		return finishSend(ctx, deps, req, n, JudgeModeDecide, nil, digest, "stalled ping")
	default:
		state.doubleNext = true
		return ""
	}
}

// handleCeiling implements step f: past the 4h ceiling from arming, send a
// deterministic "still parked" notification with no judge involved.
func handleCeiling(
	ctx context.Context, deps WatchdogDeps, req WatchdogArmRequest, tasks []TaskActivity, now time.Time,
) string {
	if now.Sub(req.ArmedAt) < watchdogCeiling {
		return ""
	}
	n := Notification{
		Title: req.Meta.Project + " · still parked",
		Body: fmt.Sprintf("%d task(s) running, no session activity for %s",
			len(tasks), humanDuration(now.Sub(req.ArmedAt))),
		Urgency: UrgencyInfo,
	}
	return finishSend(ctx, deps, req, n, JudgeModeNone, nil, "", "ceiling")
}

// finishSend appends the session's locator to n, claims the send against the
// session's dedupe state, then sends, logs the send, and logs+returns
// exitReason. Shared by every path that both sends a notification and exits:
// a Send failure does not change this sequence — RunWatchdog has at most one
// shot at notifying and no error channel to surface a delivery failure
// through. A lost claim (some hook event pinged this session within the
// dedupe window, possibly during this wake's own judge call) skips the send
// and exits with a suppression reason instead: the watchdog is a backstop,
// and a user already summoned to the session does not need it to fire twice.
func finishSend(
	ctx context.Context, deps WatchdogDeps, req WatchdogArmRequest,
	n Notification, mode JudgeMode, judgeErr error, digest string, exitReason string,
) string {
	n.Body += locatorSuffix(req.Workspace, req.Meta.Host)
	if deps.ClaimSend != nil {
		if won, since := deps.ClaimSend(ctx, req.SessionID, deps.Now(), n.Body); !won {
			return logWatchdogExit(deps, req.SessionID,
				fmt.Sprintf("suppressed: notified %s ago", humanDuration(since)))
		}
	}
	_ = deps.Send(ctx, n)
	logWatchdogSend(deps, req.SessionID, n, mode, judgeErr, digest)
	return logWatchdogExit(deps, req.SessionID, exitReason)
}

// eventWatchdog is the DecisionRecord.Event value for any record logged by
// the watchdog path.
const eventWatchdog = "watchdog"

// logWatchdogExit logs the terminal outcome of a RunWatchdog invocation and
// returns reason, so callers can write "return logWatchdogExit(...)".
func logWatchdogExit(deps WatchdogDeps, sessionID, reason string) string {
	deps.Log(DecisionRecord{Time: deps.Now(), SessionID: sessionID, Event: eventWatchdog, Outcome: reason})
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
		Event:     eventWatchdog,
		Outcome:   OutcomeSend.String(),
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

// Wake schedule intervals. The non-goal schedule sleeps watchdogFirstWakeDelay,
// then watchdogSecondWakeDelay, then watchdogThirdWakeDelay, then hourly.
// A goal-armed watchdog (see WatchdogArmRequest.GoalArmed) instead front-loads
// goalArmedPrefixLen fast wakes — watchdogGoalFirstWakeDelay through
// watchdogGoalFourthWakeDelay — ahead of that same tail, so a goal met/failed
// verdict landing seconds after arming is delivered in seconds rather than
// waiting for the standard +5m first wake. watchdogThirdWakeIdx,
// watchdogGoalThirdWakeIdx, and watchdogGoalFourthWakeIdx name indices
// golangci-lint's mnd check otherwise flags as bare magic numbers, mirroring
// the hoursPerDay pattern in digest.go.
const (
	watchdogFirstWakeDelay  = 5 * time.Minute
	watchdogSecondWakeDelay = 15 * time.Minute
	watchdogThirdWakeDelay  = 30 * time.Minute
	watchdogThirdWakeIdx    = 2

	// goalArmedPrefixLen is how many front-loaded wakes precede the standard
	// tail schedule when GoalArmed is true.
	goalArmedPrefixLen = 4

	watchdogGoalFirstWakeDelay  = 2 * time.Second
	watchdogGoalSecondWakeDelay = 5 * time.Second
	watchdogGoalThirdWakeDelay  = 15 * time.Second
	watchdogGoalFourthWakeDelay = 45 * time.Second
	watchdogGoalThirdWakeIdx    = 2
	watchdogGoalFourthWakeIdx   = 3
)

// scheduleInterval is the wake schedule's base interval (before any
// staleness doubling) for the idx-th sleep. goalArmed selects the
// front-loaded schedule: its first goalArmedPrefixLen wakes are the fast
// 2s/5s/15s/45s prefix, after which it falls through to the same tail the
// non-goal schedule uses.
func scheduleInterval(idx int, goalArmed bool) time.Duration {
	if goalArmed {
		switch idx {
		case 0:
			return watchdogGoalFirstWakeDelay
		case 1:
			return watchdogGoalSecondWakeDelay
		case watchdogGoalThirdWakeIdx:
			return watchdogGoalThirdWakeDelay
		case watchdogGoalFourthWakeIdx:
			return watchdogGoalFourthWakeDelay
		default:
			return scheduleInterval(idx-goalArmedPrefixLen, false)
		}
	}
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

// scanTranscriptFile opens path and scans it once.
func scanTranscriptFile(path string) (ScanResult, error) {
	f, err := os.Open(path) //nolint:gosec // Path is the watchdog's own recorded transcript path
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
