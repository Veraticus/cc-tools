package notify

import (
	"context"
	"encoding/json"
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

// maxBlockGoalEchoLen bounds the goal-condition echo inside a Stop-hook
// block reason. The echo is a parenthetical reminder, not the message; the
// judge's directive carries the instruction.
const maxBlockGoalEchoLen = 120

// goalBlockCap bounds how many consecutive Stop-hook blocks the pipeline
// will emit for one goal condition before it gives up blocking and sends a
// "goal stalled" notification instead: 8, mirroring Claude Code's own
// built-in stop-hook override cap.
const goalBlockCap = 8

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

// goalBlockMinInterval paces consecutive Stop-hook blocks for the same goal
// condition: a parked-unmet verdict within this long of the previous block
// resolves silent (watchdog armed, count untouched) instead of blocking
// again — the fix for a goal-judge block re-prodding a session every
// evaluation cycle instead of giving it room to act on the previous block's
// directive.
const goalBlockMinInterval = 5 * time.Minute

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
	Environ   []string
	Stdout    io.Writer
	SelfBin   string
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

	env := Env{
		UserPresent:     p.Present(p.Environ, now),
		SinceLastNotify: state.SinceLastNotify(now),
		Broadcast:       p.broadcastFacts(in, now),
	}
	d := Decide(in, res, env)

	switch d.Outcome {
	case OutcomeSilent:
		//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
		p.handleSilent(state, in, res, now, project, host, d, reasonSuffix)
	case OutcomeSend:
		p.handleSend(ctx, state, in, now, project, locus, host, d, reasonSuffix)
	case OutcomeJudge:
		p.handleJudge(ctx, state, in, res, env, now, project, locus, host, d, reasonSuffix)
	case OutcomeGoalJudge:
		p.handleGoalJudge(ctx, state, in, res, now, project, host, d, reasonSuffix)
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
	state SessionState,
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
		_ = state.MarkNotified(now)
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
		_ = state.MarkNotified(now)
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
// logged as the distinct "judge error" outcome (matching
// handleGoalJudgeError's convention) so a suppressed repeat stays
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
// retried without --model, as a decision-log Reason suffix, mirroring
// stopHookActiveSuffix's append-only shape — so an operator reading the log
// can see when the no-model retry path ran.
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
			_ = state.MarkNotified(now)
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
		_ = state.MarkNotified(now)
	}
	p.logJudged(
		in, now, OutcomeSend.String(),
		d.Reason+reasonSuffix+sendSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
		n, JudgeModeDecide, nil, digest, judgeMs,
	)
}

// handleGoalJudge implements the OutcomeGoalJudge branch: build the digest,
// call the goal-continuation judge, and route on verdict.Tasks FIRST — a
// "pending" verdict is inert regardless of GoalMet (the goal cannot be
// judged met while genuine pending work is still awaited, per goalRubric),
// so branching on Tasks before ever looking at GoalMet is what keeps that
// guarantee even if the judge ever violated its own rubric. A judge error
// fails toward the pre-epic behavior: silent and arm the watchdog, never a
// block.
func (p Pipeline) handleGoalJudge(
	ctx context.Context, state SessionState, in HookInput, res ScanResult, now time.Time,
	project, host string, d Decision, reasonSuffix string,
) {
	tasks := EnrichTasks(res.LiveTasks, now)
	digest := BuildDigest(DigestMeta{
		Project: project, Host: host, Event: in.HookEventName, LastAssistantMessage: in.LastAssistantMessage,
	}, res, tasks, now)

	start := time.Now()
	verdict, jerr := p.Judge.EvaluateGoal(ctx, digest)
	judgeMs := time.Since(start).Milliseconds()

	if jerr != nil {
		//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
		p.handleGoalJudgeError(state, in, res, now, project, host, d, jerr, digest, judgeMs, reasonSuffix)
		return
	}

	switch verdict.Tasks {
	case "pending":
		//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
		p.handleGoalPending(state, in, res, now, project, host, verdict, digest, judgeMs, reasonSuffix)
	case "parked":
		if verdict.GoalMet {
			p.handleGoalParkedMet(ctx, state, in, now, project, res.Goal, verdict, digest, judgeMs, reasonSuffix)
		} else {
			p.handleGoalParkedUnmet(
				ctx, state, in, res, now, project, host, res.Goal, verdict, digest, judgeMs, reasonSuffix,
			)
		}
	}
}

// handleGoalJudgeError implements disposition 4: EvaluateGoal errored, so
// the pipeline fails toward the pre-epic behavior (silent, arm the
// watchdog) rather than ever guessing a verdict. The decision log's Outcome
// is the distinct string "judge error" (not "silent") so this fallback path
// stays distinguishable in the log from a genuine pending verdict; Reason
// falls back to d.Reason (the Decide-level reason) since there is no
// verdict to quote, mirroring handleDecideVerdict's own jerr!=nil branch.
func (p Pipeline) handleGoalJudgeError(
	state SessionState, in HookInput, res ScanResult, now time.Time, project, host string,
	d Decision, jerr error, digest string, judgeMs int64, reasonSuffix string,
) {
	reason := d.Reason + reasonSuffix + stopHookActiveSuffix(in.StopHookActive)
	if p.DryRun {
		reason += dryRunWouldArmWatchdogSuffix
	} else {
		p.arm(state, in, res, now, project, host)
		_ = state.SetGoalBlockCount(res.Goal.Condition, 0)
	}
	p.logGoalJudged(
		in, now, "judge error", reason,
		Notification{}, jerr, digest, judgeMs,
	)
}

// handleGoalPending implements disposition 1: at least one live task is
// still genuine pending work, so the goal cannot be judged yet — same
// effect as the pre-split goal-active outcome (silent, arm the watchdog),
// reusing the existing arm path exactly.
func (p Pipeline) handleGoalPending(
	state SessionState, in HookInput, res ScanResult, now time.Time, project, host string,
	verdict GoalVerdict, digest string, judgeMs int64, reasonSuffix string,
) {
	reason := verdict.Reason + reasonSuffix + stopHookActiveSuffix(in.StopHookActive) +
		retriedWithoutModelSuffix(verdict.RetriedWithoutModel)
	if p.DryRun {
		reason += dryRunWouldArmWatchdogSuffix
	} else {
		p.arm(state, in, res, now, project, host)
		_ = state.SetGoalBlockCount(res.Goal.Condition, 0)
	}
	p.logGoalJudged(in, now, OutcomeSilent.String(), reason, Notification{}, nil, digest, judgeMs)
}

// handleGoalParkedUnmet implements disposition 2 (block, under cap) and the
// cap-hit fallback: all live tasks are parked and the goal is not yet met.
// Under goalBlockCap consecutive blocks for this exact goal condition, it
// writes the Stop-hook block control message; at or past the cap, it gives
// up blocking and sends a deterministic "goal stalled" notification
// instead. The count is reset whenever a block is NOT emitted (cap-hit),
// matching every other non-block disposition.
func (p Pipeline) handleGoalParkedUnmet(
	ctx context.Context, state SessionState, in HookInput, res ScanResult, now time.Time, project, host string,
	goal GoalState, verdict GoalVerdict, digest string, judgeMs int64, reasonSuffix string,
) {
	count := state.GoalBlockCount(goal.Condition)
	reason := verdict.Reason + reasonSuffix + stopHookActiveSuffix(in.StopHookActive) +
		retriedWithoutModelSuffix(verdict.RetriedWithoutModel)

	if count >= goalBlockCap {
		n := Notification{Title: project + " · goal stalled", Body: verdict.Reason, Urgency: UrgencyBlocked}
		sendSuffix := p.deliver(ctx, n)
		if !p.DryRun {
			_ = state.MarkNotified(now)
			_ = state.SetGoalBlockCount(goal.Condition, 0)
		}
		p.logGoalJudged(in, now, OutcomeSend.String(), reason+sendSuffix, n, nil, digest, judgeMs)
		return
	}

	if last := state.LastGoalBlockAt(goal.Condition); !last.IsZero() && now.Sub(last) < goalBlockMinInterval {
		pacedReason := reason + fmt.Sprintf(" (paced: blocked %s ago)", humanDuration(now.Sub(last)))
		if p.DryRun {
			pacedReason += dryRunWouldArmWatchdogSuffix
		} else {
			//nolint:contextcheck // arming spawns a detached child that must outlive this hook's ctx; see SpawnRecheck
			p.arm(state, in, res, now, project, host)
		}
		p.logGoalJudged(in, now, OutcomeSilent.String(), pacedReason, Notification{}, nil, digest, judgeMs)
		return
	}

	// The judge's directive leads — it is what the resumed session acts on
	// and what the user reads in the terminal's block line. The goal echo
	// is demoted to a parenthetical: insurance against post-compaction
	// amnesia, never the sentence's subject.
	blockReason := verdict.Reason + " (goal: " + truncateWords(goal.Condition, maxBlockGoalEchoLen) + ")"
	if p.DryRun {
		_, _ = fmt.Fprintf(p.Stdout, "DRY RUN: would block (goal continuation) — %s\n", blockReason)
	} else {
		reason += writeBlockDecision(p.Stdout, blockReason)
		_ = state.SetGoalBlockCount(goal.Condition, count+1)
		_ = state.MarkGoalBlocked(goal.Condition, now)
	}
	p.logGoalJudged(in, now, "block", reason, Notification{}, nil, digest, judgeMs)
}

// handleGoalParkedMet implements disposition 3: all live tasks are parked
// and the goal condition already holds. Sends a "goal complete"
// notification and resets the block count — no block, no watchdog.
func (p Pipeline) handleGoalParkedMet(
	ctx context.Context, state SessionState, in HookInput, now time.Time, project string,
	goal GoalState, verdict GoalVerdict, digest string, judgeMs int64, reasonSuffix string,
) {
	n := Notification{Title: project + " · goal complete", Body: verdict.Reason, Urgency: UrgencyDone}
	sendSuffix := p.deliver(ctx, n)
	if !p.DryRun {
		_ = state.MarkNotified(now)
		_ = state.SetGoalBlockCount(goal.Condition, 0)
	}
	reason := verdict.Reason + reasonSuffix + stopHookActiveSuffix(in.StopHookActive) + sendSuffix +
		retriedWithoutModelSuffix(verdict.RetriedWithoutModel)
	p.logGoalJudged(in, now, OutcomeSend.String(), reason, n, nil, digest, judgeMs)
}

// stopHookActiveSuffix renders in.StopHookActive as a decision-log Reason
// suffix, folding the parsed field into the log the same way reasonSuffix
// folds a transcript-scan error in: appended text on an existing Reason,
// never a separate control path.
func stopHookActiveSuffix(active bool) string {
	return fmt.Sprintf(" (stop_hook_active=%t)", active)
}

// writeBlockDecision marshals the Stop-hook block control message —
// {"decision":"block","reason":reason} — and writes it, newline-terminated,
// to w. This is the only stdout write in the whole pipeline outside DryRun's
// own reporting lines: Claude Code parses hook stdout as a control message,
// so a marshal or write failure here is folded into the decision log's
// Reason via a returned suffix (mirroring deliver's sendSuffix) rather than
// panicking or retrying — this call must never be able to fail the hook.
func writeBlockDecision(w io.Writer, reason string) string {
	line, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{Decision: "block", Reason: reason})
	if err != nil {
		return fmt.Sprintf(" (block write failed: %s)", err)
	}
	line = append(line, '\n')
	if _, writeErr := w.Write(line); writeErr != nil {
		return fmt.Sprintf(" (block write failed: %s)", writeErr)
	}
	return ""
}

// logGoalJudged builds and appends a decision record for a goal-judge
// evaluation: every goal-judge record carries Digest, JudgeMs, and (if
// non-nil) JudgeErr, with JudgeMode fixed to "goal" — EvaluateGoal shares no
// JudgeMode value with Evaluate's compose/decide modes, so it is not routed
// through judgeModeLabel.
func (p Pipeline) logGoalJudged(
	in HookInput, now time.Time, outcome, reason string, n Notification, jerr error, digest string, judgeMs int64,
) {
	rec := DecisionRecord{
		Outcome: outcome, Reason: reason,
		Urgency: n.Urgency, Title: n.Title, Body: n.Body,
		JudgeMode: "goal", JudgeMs: judgeMs, Digest: digest,
	}
	if jerr != nil {
		rec.JudgeErr = jerr.Error()
	}
	p.logRecord(in, now, rec)
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
