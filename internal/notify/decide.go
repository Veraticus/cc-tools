package notify

import (
	"fmt"
	"time"
)

// dedupeWindow suppresses a second non-blocked notification sent within this
// long of the previous one for the same session. It gates two places: the
// idle_prompt pre-judge check here (killing the Stop→idle_prompt double-ping
// for one idle turn), and every judged/watchdog send's pre-delivery
// ClaimSend claim. Originally 90s, widened to 5 minutes after an observed
// double ping 115s apart (Stop send, then an idle_prompt re-announcing the
// same finished work) and a watchdog goal-met send 4m16s after an idle
// backstop ping — one ping per session per five quiet minutes is the
// contract, with blocked-tier permission/needs-input events exempt as ever.
const dedupeWindow = 5 * time.Minute

// blockedRepeatWindow suppresses a second permission_prompt or
// agent_needs_input notification within this long of an identical previous
// one for the same session. Blocked-tier events are otherwise dedupe-blind
// by design (a blocked session needs attention regardless of pane focus), so
// this is scoped to content identity only: a session asking the exact same
// thing every poll cycle gets pinged once, but a DIFFERENT message always
// sends immediately.
const blockedRepeatWindow = 5 * time.Minute

// Outcome is what Decide resolved to do with a hook event.
type Outcome int

const (
	// OutcomeSilent means no notification and no judge call.
	OutcomeSilent Outcome = iota
	// OutcomeSend means send a notification deterministically, with no LLM
	// involved.
	OutcomeSend
	// OutcomeJudge means hand off to the LLM judge; JudgeMode says why.
	OutcomeJudge
)

// String renders an Outcome as the lowercase word the decision log shows.
func (o Outcome) String() string {
	switch o {
	case OutcomeSilent:
		return "silent"
	case OutcomeSend:
		return "send"
	case OutcomeJudge:
		return "judge"
	default:
		return "unknown"
	}
}

// JudgeMode distinguishes why the judge runs. Both modes fail-open to a
// deterministic SEND on judge failure — the reliability invariant is that an
// LLM failure may never lose a genuine ping. In compose mode the judge only
// writes better text for a send that is already decided; in decide mode the
// judge chooses notify-or-silent (live tasks: parked vs pending work), and
// on failure the fallback is to send.
type JudgeMode int

const (
	// JudgeModeNone applies when Outcome is not OutcomeJudge.
	JudgeModeNone JudgeMode = iota
	// JudgeModeCompose means the send is already decided; the judge only
	// composes the notification text. Failure falls back to a deterministic
	// send with default text.
	JudgeModeCompose
	// JudgeModeDecide means the judge chooses notify-or-silent. Failure
	// falls back to a deterministic send.
	JudgeModeDecide
)

// Urgency classifies a sent notification for the caller's delivery
// mechanism (e.g. sound, urgency hint).
type Urgency string

const (
	// UrgencyBlocked means the session cannot proceed without the user.
	UrgencyBlocked Urgency = "blocked"
	// UrgencyDone means a background session finished its work.
	UrgencyDone Urgency = "done"
	// UrgencyInfo is a general informational notification.
	UrgencyInfo Urgency = "info"
)

// Decision is the full result of a single Decide call.
type Decision struct {
	Outcome Outcome
	// JudgeMode is set when Outcome == OutcomeJudge.
	JudgeMode JudgeMode
	// Urgency is set when Outcome == OutcomeSend (judge outcomes carry their
	// own urgency, decided later).
	Urgency Urgency
	// Message is set when Outcome == OutcomeSend and the event carries
	// pass-through text (Notification .message).
	Message string
	// Reason is always set, distinct per gate, and feeds the decision log.
	Reason string
	// ArmWatchdog is true when a watchdog is wanted because the FINAL
	// outcome is silence and there is live/active work (goal active or
	// pending tasks) to keep checking on.
	ArmWatchdog bool
}

// Env carries environment facts the caller computed, since Decide itself
// does no I/O: how long ago this session last sent a notification (negative
// means never), and — for broadcast-type Notification events — the
// cross-session BroadcastFacts (nil otherwise).
type Env struct {
	SinceLastNotify time.Duration
	// SinceLastNotifySame is how long ago this session last sent the
	// current Notification event's message verbatim (negative when never,
	// or the last send differed) — computed by the pipeline via
	// DedupeState.SinceLastNotifySame against the raw incoming message.
	// Consumed by the blocked-tier identical-repeat gates.
	SinceLastNotifySame time.Duration
	Broadcast           *BroadcastFacts
}

// Decide is the pure decision core for the notification hook: given the raw
// hook payload, a transcript scan, and environment facts, it returns exactly
// one Decision. Gates are evaluated in priority order below, each an early
// return; the first that matches wins.
func Decide(in HookInput, scan ScanResult, env Env) Decision {
	// Gate 1: subagent/teammate context, checked before anything else and
	// applying to every event — a background agent's own hook events must
	// never surface a notification meant for the top-level session.
	if in.AgentID != "" {
		return Decision{Outcome: OutcomeSilent, Reason: "agent context"}
	}

	switch in.HookEventName {
	case "SessionEnd":
		// Watchdog reaping on SessionEnd is Pipeline.Run's own concern (it
		// intercepts SessionEnd and calls Watchdog.Reap before Decide is ever
		// reached) — this case exists only so Decide, called directly, still
		// resolves SessionEnd to a sensible silent decision.
		return Decision{Outcome: OutcomeSilent, Reason: "session end"}
	case "Stop":
		return decideStop(scan)
	case eventNotification:
		return decideNotification(in, scan, env)
	default:
		return Decision{Outcome: OutcomeSilent, Reason: fmt.Sprintf("unhandled event: %s", in.HookEventName)}
	}
}

// decideStop handles the Stop event's gates: an active goal or live
// background work or active teammates are all reasons a Stop's implication
// (the assistant turn ended) is not yet a signal to notify — there is still
// something for the session to track. Teammates are softer evidence than
// live tasks (a spawn with no reply yet doesn't guarantee more work is
// coming), so they route to the decide-mode judge rather than resolving
// silent on their own. Absent any of those, a Stop always composes: a ping
// means "a stopped session needs the user's response," so nothing here may
// gate that on whether the user happens to be looking at the pane right now.
func decideStop(scan ScanResult) Decision {
	if scan.Goal.Status == GoalActive {
		// Claude Code's built-in /goal evaluator owns goal continuation; a
		// hook decision:block here would override its verdict, and
		// goal_status is officially unstable, so this notifier never
		// competes with it — regardless of whether live tasks are present,
		// it stays silent and arms the watchdog to keep checking.
		return Decision{Outcome: OutcomeSilent, Reason: "goal active: deferring to built-in /goal", ArmWatchdog: true}
	}
	if len(scan.LiveTasks) > 0 {
		return Decision{
			Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
			Reason: "live tasks: parked vs pending", ArmWatchdog: true,
		}
	}
	if len(scan.Teammates) > 0 {
		return Decision{
			Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
			Reason: "teammates active: parked vs pending", ArmWatchdog: true,
		}
	}
	return Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "turn ended, composing"}
}

// decideNotification handles the Notification event's gates, branching on
// NotificationType. permission_prompt and agent_needs_input are
// blocked-tier: they skip idle-dedupe suppression entirely, because a
// blocked session needs the user's attention regardless of which tmux pane
// (if any) currently has focus. They do still gate on blockedRepeatWindow —
// an identical repeat of the exact same blocked ping — since that is a
// content-identity check, not a staleness one.
func decideNotification(in HookInput, scan ScanResult, env Env) Decision {
	switch in.NotificationType {
	case "permission_prompt":
		if s := suppressBlockedRepeat(env); s != nil {
			return *s
		}
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "permission prompt",
		}

	case "idle_prompt":
		// A ping means "a stopped session needs the user's response," so
		// nothing here may gate on whether anyone is looking at the pane.
		// What silences idle_prompt below instead is coverage: the Stop
		// decision already ruled on goal/live-task/teammate state ~60s
		// earlier when this same turn ended, and the watchdog it armed
		// (staleness check plus a 4h ceiling) is the recovery path if that
		// work hangs. Only when none of that coverage applies does
		// idle_prompt fall through to compose its own backstop ping.
		if env.SinceLastNotify >= 0 && env.SinceLastNotify < dedupeWindow {
			return Decision{
				Outcome: OutcomeSilent,
				Reason:  fmt.Sprintf("dedupe: notified %s ago", humanDuration(env.SinceLastNotify)),
			}
		}
		if scan.Goal.Status == GoalActive {
			return Decision{Outcome: OutcomeSilent, Reason: "goal active", ArmWatchdog: true}
		}
		if len(scan.LiveTasks) > 0 || len(scan.Teammates) > 0 {
			return Decision{Outcome: OutcomeSilent, Reason: "live work: watchdog covers", ArmWatchdog: true}
		}
		return Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "idle backstop"}

	case notifTypeAgentNeedsInput:
		if s := suppressBroadcast(env); s != nil {
			return *s
		}
		if s := suppressBlockedRepeat(env); s != nil {
			return *s
		}
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "background session needs input",
		}

	case notifTypeAgentCompleted:
		if s := suppressBroadcast(env); s != nil {
			return *s
		}
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyDone,
			Message: in.Message, Reason: "background session completed",
		}

	default:
		return Decision{
			Outcome: OutcomeSilent,
			Reason:  fmt.Sprintf("unhandled notification type: %s", in.NotificationType),
		}
	}
}

// suppressBlockedRepeat returns the silent Decision a blocked-tier
// notification (permission_prompt, agent_needs_input) resolves to when this
// session already sent this exact message within blockedRepeatWindow — nil
// when the event should proceed to its own send gate. Checked after
// suppressBroadcast for agent_needs_input, so a broadcast-claim suppression
// always wins first.
func suppressBlockedRepeat(env Env) *Decision {
	if env.SinceLastNotifySame >= 0 && env.SinceLastNotifySame < blockedRepeatWindow {
		return &Decision{
			Outcome: OutcomeSilent,
			Reason:  fmt.Sprintf("dedupe: identical ping %s ago", humanDuration(env.SinceLastNotifySame)),
		}
	}
	return nil
}

// suppressBroadcast returns the silent Decision a broadcast event resolves
// to when it belongs to a local job (source-session ownership, checked
// first) or another session already claimed it — nil when the event should
// proceed to its own send gate. Broadcast events are otherwise blocked-tier
// precisely because they are the backstop for a job whose own session went
// silent; these two suppressions are the only ones they get.
func suppressBroadcast(env Env) *Decision {
	b := env.Broadcast
	if b == nil {
		return nil
	}
	if b.Local {
		// Ownership is structural, not a matter of timing (see BroadcastFacts
		// and broadcastFacts): the source session always owns delivery for
		// its own job, regardless of claim state or whether it has sent
		// yet.
		return &Decision{Outcome: OutcomeSilent, Reason: "deferred to source session"}
	}
	if b.Duplicate {
		return &Decision{Outcome: OutcomeSilent, Reason: "dedupe: broadcast claimed by another session"}
	}
	return nil
}
