package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// maxNotificationTailLen bounds the fallback notification body built from
// HookInput.LastAssistantMessage when the judge errors: the tail (via
// truncateHead) is what carries the meaningful content, matching the
// head/tail convention established in transcript.go/digest.go.
const maxNotificationTailLen = 160

// dryRunWouldArmWatchdogSuffix is appended to a decision's Reason whenever a
// dry run reaches an arm-the-watchdog branch without actually arming it.
const dryRunWouldArmWatchdogSuffix = " (would arm watchdog)"

// failOpenWindow rate-limits a compose/decide-mode judge-error fallback send
// to at most one per session within this long of its last notification: a
// run of judge failures must not each re-ping — the reliability invariant
// (an LLM failure may never lose a genuine ping) only requires that the
// FIRST fail-open after a quiet period gets through; repeats within the
// window are the same failure already surfaced.
const failOpenWindow = 10 * time.Minute

// DedupeState is Pipeline's interface onto per-session notify dedupe
// (last-notify time and message hash) and broadcast-claim coordination.
// Pipeline reads and writes it instead of talking to SessionState files
// directly, so where that bookkeeping actually lives is a swappable
// concern: on disk, one file tree per session (FileState — the default,
// preserving notify's historical single-shot-hook behavior), entirely in
// memory and confined to one goroutine (MemoryState — notifyd's dedupe
// store), or nowhere at all (NopState — the hook client's inline
// fallback). sessionID keys every session-scoped method because one
// DedupeState instance is shared across every session a process handles —
// unlike SessionState, whose Dir already bakes in a single session.
type DedupeState interface {
	// SinceLastNotify returns how long before now sessionID last notified;
	// see SessionState.SinceLastNotify for the "never notified" sentinel
	// contract every implementation must honor. ctx carries no deadline
	// today — it exists so the daemon's loopState implementation can bail
	// out of its loop round trip on shutdown rather than storing ctx on
	// itself.
	SinceLastNotify(ctx context.Context, sessionID string, now time.Time) time.Duration
	// SinceLastNotifySame returns how long before now sessionID last sent
	// message verbatim; see SessionState.SinceLastNotifySame.
	SinceLastNotifySame(ctx context.Context, sessionID string, now time.Time, message string) time.Duration
	// MarkNotified records t/message as sessionID's last notification.
	MarkNotified(ctx context.Context, sessionID string, t time.Time, message string) error
	// ClaimBroadcast atomically claims key for a window starting at now,
	// reporting whether this call won; see claimBroadcast's first-claimant
	// contract. dryRun observes without claiming.
	ClaimBroadcast(ctx context.Context, key string, window time.Duration, now time.Time, dryRun bool) bool
}

// Pipeline is the top-level orchestrator for a single hook invocation: it
// scans the transcript, computes the deterministic Decide() gates, and
// routes to a plain send, silence, or the LLM judge — then delivers,
// records session state, and appends a decision-log entry. It never
// returns an error for a condition it can itself recover from: a failing
// transcript scan, judge call, or notification send all degrade to a
// documented fallback rather than losing the hook's exit code 0 contract.
type Pipeline struct {
	StateBase string
	DryRun    bool
	Judge     Judge
	Sender    Sender
	Log       DecisionLog
	// State is where dedupe/broadcast-claim bookkeeping lives. The zero
	// value (nil) defaults to FileState{Base: StateBase} — the historical
	// file-backed behavior every single-shot hook invocation still relies
	// on — so only callers that want different semantics (notifyd's
	// MemoryState, the hook client fallback's NopState) need to set it.
	State   DedupeState
	Environ []string
	Stdout  io.Writer
	SelfBin string
	// Workspace is the tmux session name this hook's pane lives in
	// (WorkspaceName), or "" outside tmux — background jobs included.
	// Notification titles use it as the where-to-go segment, falling back
	// to Host.
	Workspace string
	// Host is the short hostname for titles and falls back to
	// ShortHostname() when empty; tests inject a fixed value.
	Host string
	// Present reports whether the user is at the terminal right now.
	// Production wires UserPresent+RunCommand; tests inject a canned func.
	Present func(environ []string, now time.Time) bool
}

// dedupeState returns p.State, or a FileState rooted at p.StateBase when
// State is unset — see the State field's doc comment.
//
//nolint:ireturn // DedupeState is intentionally swappable (file/memory/nop); this is the field's single accessor.
func (p Pipeline) dedupeState() DedupeState {
	if p.State != nil {
		return p.State
	}
	return FileState{Base: p.StateBase}
}

// Run executes the pipeline for one hook payload. It always returns nil:
// every failure mode (transcript scan, judge call, watchdog arm, delivery)
// is a documented fallback or a swallowed best-effort operation, never a
// reason to fail the hook itself.
func (p Pipeline) Run(ctx context.Context, in HookInput) error {
	now := time.Now()
	state := SessionState{Dir: filepath.Join(p.StateBase, in.SessionID)}
	project := filepath.Base(in.CWD)
	host := p.Host
	if host == "" {
		host = ShortHostname()
	}
	// The where-to-go segment of generic notification titles: the tmux
	// workspace when the session lives in one, the machine otherwise. A
	// ping's urgency already rides the ntfy sound/priority and its content
	// rides the body, so a generic label ("needs input") would waste the
	// title slot on what the ping itself already says.
	locus := p.Workspace
	if locus == "" {
		locus = host
	}

	if in.HookEventName == "SessionEnd" {
		deps := DefaultWatchdogDeps(p.Judge, p.Sender, p.Log)
		_ = ReapWatchdog(state, deps)
		_ = state.Reap()
		p.logRecord(in, now, DecisionRecord{Outcome: OutcomeSilent.String(), Reason: "session end"})
		return nil
	}

	res, scanErr := p.scanTranscript(in.TranscriptPath)
	reasonSuffix := ""
	if scanErr != nil {
		reasonSuffix = fmt.Sprintf(" (transcript error: %s)", scanErr)
	}

	// SinceLastNotifySame costs a state-file read plus a sha256 of the
	// message, and only the blocked-tier Notification gates ever read it —
	// so the dominant Stop path (every assistant-turn end) skips it, keeping
	// the sentinel that means "never/differs" instead.
	sinceSame := neverNotifiedDuration
	if in.HookEventName == eventNotification {
		sinceSame = p.dedupeState().SinceLastNotifySame(ctx, in.SessionID, now, in.Message)
	}
	env := Env{
		UserPresent:         p.Present(p.Environ, now),
		SinceLastNotify:     p.dedupeState().SinceLastNotify(ctx, in.SessionID, now),
		SinceLastNotifySame: sinceSame,
		Broadcast:           p.broadcastFacts(ctx, in, now),
	}
	d := Decide(in, res, env)

	switch d.Outcome {
	case OutcomeSilent:
		//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
		p.handleSilent(state, in, res, now, project, host, d, reasonSuffix)
	case OutcomeSend:
		p.handleSend(ctx, in, now, project, locus, host, d, reasonSuffix)
	case OutcomeJudge:
		p.handleJudge(ctx, state, in, res, env, now, project, locus, host, d, reasonSuffix)
	}
	return nil
}

// scanTranscript opens and scans path, returning a zero ScanResult on any
// failure (open or scan) so the caller can fall back to a pass-through
// decision rather than losing the hook event entirely.
func (p Pipeline) scanTranscript(path string) (ScanResult, error) {
	f, err := os.Open(path) //nolint:gosec // Path comes from the hook's own payload
	if err != nil {
		return ScanResult{}, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	res, err := ScanTranscript(f)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scanning transcript: %w", err)
	}
	return res, nil
}

// handleSilent implements the OutcomeSilent branch: log the silence, and
// either arm the watchdog (real run) or note that it would have (dry run).
func (p Pipeline) handleSilent(
	state SessionState,
	in HookInput,
	res ScanResult,
	now time.Time,
	project, host string,
	d Decision,
	reasonSuffix string,
) {
	reason := d.Reason + reasonSuffix
	if d.ArmWatchdog {
		if p.DryRun {
			reason += dryRunWouldArmWatchdogSuffix
		} else {
			p.arm(state, in, res, now, project, host)
		}
	}
	p.logRecord(in, now, DecisionRecord{Outcome: OutcomeSilent.String(), Reason: reason})
}

// handleSend implements the OutcomeSend branch: a deterministic send with
// no judge involved. The title's second segment is where to go, not what
// happened — the ping's sound/priority and body already carry the what. A
// broadcast is about a headless job, so the receiving session's workspace
// would be misleading; those use the host.
func (p Pipeline) handleSend(
	ctx context.Context,
	in HookInput,
	now time.Time,
	project, locus, host string,
	d Decision,
	reasonSuffix string,
) {
	body := d.Message
	if body == "" {
		body = sendLabel(in.NotificationType)
	}
	if d.ProjectOverride != "" {
		project = d.ProjectOverride
	}
	where := locus
	if in.NotificationType == notifTypeAgentNeedsInput || in.NotificationType == notifTypeAgentCompleted {
		where = host
	}
	n := Notification{Title: project + " · " + where, Body: body, Urgency: d.Urgency}

	sendSuffix := p.deliver(ctx, n)
	if !p.DryRun {
		_ = p.dedupeState().MarkNotified(ctx, in.SessionID, now, n.Body)
	}
	p.logRecord(in, now, DecisionRecord{
		Outcome: OutcomeSend.String(), Reason: d.Reason + reasonSuffix + sendSuffix,
		Urgency: n.Urgency, Title: n.Title, Body: n.Body,
	})
}

// sendLabel names the fallback body for a deterministic OutcomeSend whose
// event carried no message, by NotificationType. Titles never use it: the
// title slot carries where to go, not what happened.
func sendLabel(notificationType string) string {
	switch notificationType {
	case "permission_prompt":
		return "needs permission"
	case notifTypeAgentNeedsInput:
		return "needs input"
	case notifTypeAgentCompleted:
		return "job finished"
	default:
		return "notification"
	}
}

// handleJudge implements the OutcomeJudge branch: build the digest, call
// the judge, and route the verdict (or its absence) per JudgeMode.
func (p Pipeline) handleJudge(
	ctx context.Context, state SessionState, in HookInput, res ScanResult, env Env, now time.Time,
	project, locus, host string, d Decision, reasonSuffix string,
) {
	tasks := EnrichTasks(res.LiveTasks, now)
	digest := BuildDigest(DigestMeta{
		Project: project, Host: host, Event: in.HookEventName, LastAssistantMessage: in.LastAssistantMessage,
	}, res, tasks, now)

	start := time.Now()
	verdict, jerr := p.Judge.Evaluate(ctx, digest, d.JudgeMode)
	judgeMs := time.Since(start).Milliseconds()

	switch d.JudgeMode {
	case JudgeModeCompose:
		p.handleComposeVerdict(
			ctx, state, in, res, env, now, project, locus, host, d, verdict, jerr, digest, judgeMs, reasonSuffix,
		)
	case JudgeModeDecide:
		p.handleDecideVerdict(
			ctx,
			state,
			in,
			res,
			env,
			now,
			project,
			locus,
			host,
			d,
			verdict,
			jerr,
			digest,
			judgeMs,
			reasonSuffix,
		)
	case JudgeModeNone:
		// Decide never returns OutcomeJudge with JudgeModeNone; nothing to do.
	}
}

// handleComposeVerdict implements the compose route: the send is already
// decided, the judge only writes better text. A judge error falls back to
// a deterministic notification titled by locus (where to go — a generic
// "session idle" label would waste the slot) — never silent, per the
// reliability invariant that an LLM failure may never lose a genuine ping.
func (p Pipeline) handleComposeVerdict(
	ctx context.Context, state SessionState, in HookInput, res ScanResult, env Env, now time.Time,
	project, locus, host string,
	d Decision, verdict JudgeVerdict, jerr error, digest string, judgeMs int64, reasonSuffix string,
) {
	if jerr != nil && failOpenSuppressed(env) {
		//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
		p.suppressJudgeError(
			state, in, res, env, now, project, host, JudgeModeCompose, jerr, digest, judgeMs, reasonSuffix,
		)
		return
	}

	var n Notification
	if jerr == nil {
		n = Notification{Title: project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
	} else {
		body := truncateHeadWords(in.LastAssistantMessage, maxNotificationTailLen)
		if body == "" {
			body = "turn ended"
		}
		n = Notification{Title: project + " · " + locus, Body: body, Urgency: UrgencyDone}
	}

	sendSuffix := p.deliver(ctx, n)
	if !p.DryRun {
		_ = p.dedupeState().MarkNotified(ctx, in.SessionID, now, n.Body)
	}
	p.logJudged(
		in, now, OutcomeSend.String(),
		d.Reason+reasonSuffix+sendSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
		n, JudgeModeCompose, jerr, digest, judgeMs,
	)
}

// failOpenSuppressed reports whether a compose/decide-mode judge error
// should be suppressed under failOpenWindow rather than falling back to a
// deterministic send: true only when this session notified within the
// window. SinceLastNotify negative means never notified, which must always
// send — the epic's anti-pattern guard is that the FIRST judge error may
// never fail to notify.
func failOpenSuppressed(env Env) bool {
	return env.SinceLastNotify >= 0 && env.SinceLastNotify < failOpenWindow
}

// suppressJudgeError implements the failOpenWindow rate limit shared by the
// compose- and decide-mode judge-error fallbacks: silent, watchdog armed,
// logged as the distinct "judge error" outcome so a suppressed repeat stays
// distinguishable in the decision log from a genuine silent verdict.
func (p Pipeline) suppressJudgeError(
	state SessionState, in HookInput, res ScanResult, env Env, now time.Time, project, host string,
	mode JudgeMode, jerr error, digest string, judgeMs int64, reasonSuffix string,
) {
	reason := fmt.Sprintf("suppressed: notified %s ago", humanDuration(env.SinceLastNotify)) + reasonSuffix
	if p.DryRun {
		reason += dryRunWouldArmWatchdogSuffix
	} else {
		p.arm(state, in, res, now, project, host)
	}
	p.logJudged(in, now, "judge error", reason, Notification{}, mode, jerr, digest, judgeMs)
}

// retriedWithoutModelSuffix renders whether the judge's runRetrying path
// retried without --model, as a decision-log Reason suffix — so an operator
// reading the log can see when the no-model retry path ran.
func retriedWithoutModelSuffix(retried bool) string {
	if retried {
		return " (judge retried without --model)"
	}
	return ""
}

// handleDecideVerdict implements the decide route: the judge itself chooses
// notify-or-silence. A judge error falls back to a deterministic send
// (documented asymmetry vs. the watchdog's silent decide-error path: the
// watchdog rechecks repeatedly so a silent skip is safe, but this hook path
// has no retry, so it fails open to a send instead of risking a lost ping).
func (p Pipeline) handleDecideVerdict(
	ctx context.Context,
	state SessionState,
	in HookInput,
	res ScanResult,
	env Env,
	now time.Time,
	project, locus, host string,
	d Decision,
	verdict JudgeVerdict,
	jerr error,
	digest string,
	judgeMs int64,
	reasonSuffix string,
) {
	if jerr != nil {
		if failOpenSuppressed(env) {
			//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
			p.suppressJudgeError(
				state, in, res, env, now, project, host, JudgeModeDecide, jerr, digest, judgeMs, reasonSuffix,
			)
			return
		}
		body := truncateHeadWords(in.LastAssistantMessage, maxNotificationTailLen)
		if body == "" {
			body = "session has running background work"
		}
		n := Notification{Title: project + " · " + locus, Body: body, Urgency: UrgencyInfo}
		sendSuffix := p.deliver(ctx, n)
		if !p.DryRun {
			_ = p.dedupeState().MarkNotified(ctx, in.SessionID, now, n.Body)
		}
		p.logJudged(
			in, now, OutcomeSend.String(), d.Reason+reasonSuffix+sendSuffix, n, JudgeModeDecide, jerr, digest, judgeMs,
		)
		return
	}

	if !verdict.Notify {
		if d.ArmWatchdog && !p.DryRun {
			//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
			p.arm(state, in, res, now, project, host)
		}
		p.logJudged(
			in,
			now,
			OutcomeSilent.String(),
			verdict.Reason+reasonSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
			Notification{},
			JudgeModeDecide,
			nil,
			digest,
			judgeMs,
		)
		return
	}

	if verdict.Urgency != UrgencyBlocked && env.UserPresent {
		reason := "suppressed: user present (post-judge)" + reasonSuffix +
			retriedWithoutModelSuffix(verdict.RetriedWithoutModel)
		// This is still a silent outcome with live/pending background work
		// (that's how JudgeModeDecide was reached at all) — the user being
		// present right now doesn't mean they'll stay at this pane, so the
		// watchdog must still arm to keep coverage on that work, exactly as
		// the !verdict.Notify branch above does.
		if d.ArmWatchdog {
			if p.DryRun {
				reason += dryRunWouldArmWatchdogSuffix
			} else {
				//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
				p.arm(state, in, res, now, project, host)
			}
		}
		p.logJudged(in, now, OutcomeSilent.String(), reason, Notification{}, JudgeModeDecide, nil, digest, judgeMs)
		return
	}

	n := Notification{Title: project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
	sendSuffix := p.deliver(ctx, n)
	if !p.DryRun {
		_ = p.dedupeState().MarkNotified(ctx, in.SessionID, now, n.Body)
	}
	p.logJudged(
		in, now, OutcomeSend.String(),
		d.Reason+reasonSuffix+sendSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
		n, JudgeModeDecide, nil, digest, judgeMs,
	)
}

// deliver sends n: in DryRun mode it writes a "DRY RUN: ..." line to Stdout
// (and never fails); otherwise it calls Sender.Send. A send failure never
// propagates as an error — it returns a " (send failed: ...)" suffix for
// the caller to fold into the decision log's Reason field, since delivery
// failure must never fail the hook.
func (p Pipeline) deliver(ctx context.Context, n Notification) string {
	if p.DryRun {
		_, _ = fmt.Fprintf(p.Stdout, "DRY RUN: [%s] %s — %s\n", n.Urgency, n.Title, n.Body)
		return ""
	}
	if err := p.Sender.Send(ctx, n); err != nil {
		return fmt.Sprintf(" (send failed: %s)", err)
	}
	return ""
}

// arm spawns a detached recheck process and writes its watchdog lock. The
// lock is written immediately after the child starts: the child sleeps at
// least watchdogFirstWakeDelay before its first lock read, so this write
// wins any race by hours, and a child that ever reads a lock naming a
// different PID exits "superseded" harmlessly (see watchdog.go). Both
// steps are best-effort: a failure here is swallowed, matching Run's
// contract that no watchdog-arming failure may fail the hook.
func (p Pipeline) arm(state SessionState, in HookInput, res ScanResult, now time.Time, project, host string) {
	pid, err := SpawnRecheck(p.SelfBin, []string{
		"notify", "--recheck",
		"--session", in.SessionID,
		"--state-base", p.StateBase,
		"--project", project,
		"--host", host,
	})
	if err != nil {
		p.logArmFailed(in, now, fmt.Sprintf("spawn recheck: %v", err))
		return
	}
	deps := DefaultWatchdogDeps(p.Judge, p.Sender, p.Log)
	if lockErr := WriteWatchdogLock(state, WatchdogLock{
		PID: pid, ParentPID: os.Getppid(), Transcript: in.TranscriptPath, Offset: res.BytesScanned, ArmedAt: now,
	}, deps); lockErr != nil {
		p.logArmFailed(in, now, fmt.Sprintf("write lock: %v", lockErr))
	}
}

// logArmFailed records a failed watchdog arm attempt: this is the only
// coverage that a "never zero coverage while work is in flight" guarantee
// silently failed, since arm itself must stay non-fatal to the hook. Event
// is fixed to "watchdog" (matching logWatchdogExit's convention) rather
// than in.HookEventName, since this record documents a watchdog-arming
// failure, not the triggering hook event.
func (p Pipeline) logArmFailed(in HookInput, now time.Time, reason string) {
	_ = p.Log.Append(DecisionRecord{
		Time: now, SessionID: in.SessionID, Event: eventWatchdog, Outcome: "arm failed", Reason: reason,
	})
}

// SpawnRecheck starts bin with args as a fully detached process (its own
// session via Setsid, all three stdio to os.DevNull) and returns its PID
// without waiting for it: the caller (Pipeline.arm) is the hook process,
// which must exit immediately, not block on a watchdog that can run for
// hours.
func SpawnRecheck(bin string, args []string) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("notify: opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	// context.Background(), deliberately not the caller's ctx: this child
	// must outlive the hook invocation that spawns it, so it must never be
	// canceled when that invocation's own context expires.
	//nolint:gosec // G204: bin is Pipeline.SelfBin (this tool's own binary), args built internally, not external input
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if startErr := cmd.Start(); startErr != nil {
		return 0, fmt.Errorf("notify: starting recheck process: %w", startErr)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// logRecord appends rec to the decision log with Time/SessionID/Event
// filled from now/in. Append errors are swallowed: logging must never be
// able to fail the hook.
func (p Pipeline) logRecord(in HookInput, now time.Time, rec DecisionRecord) {
	rec.Time = now
	rec.SessionID = in.SessionID
	rec.Event = in.HookEventName
	_ = p.Log.Append(rec)
}

// logJudged builds and appends a decision record for a judged evaluation:
// every judged record carries Digest, JudgeMode, JudgeMs, and (if non-nil)
// JudgeErr, regardless of which route the verdict took.
func (p Pipeline) logJudged(
	in HookInput, now time.Time, outcome, reason string, n Notification, mode JudgeMode, jerr error,
	digest string, judgeMs int64,
) {
	rec := DecisionRecord{
		Outcome: outcome, Reason: reason,
		Urgency: n.Urgency, Title: n.Title, Body: n.Body,
		JudgeMode: judgeModeLabel(mode), JudgeMs: judgeMs, Digest: digest,
	}
	if jerr != nil {
		rec.JudgeErr = jerr.Error()
	}
	p.logRecord(in, now, rec)
}

// ShortHostname returns os.Hostname() with any domain suffix stripped
// (everything from the first '.' onward). Returns "" if os.Hostname()
// errors.
func ShortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	short, _, _ := strings.Cut(h, ".")
	return short
}
