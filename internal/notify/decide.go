package notify

import (
	"fmt"
	"time"
)

// dedupeWindow suppresses a second idle_prompt notification sent within this
// long of the previous one for the same session. It exists specifically to
// kill the Stop→idle_prompt double-ping: a session going idle can produce a
// composed notification at Stop and then, moments later, an idle_prompt
// Notification event for that same silence — without this window the user
// would get pinged twice for one idle turn.
const dedupeWindow = 90 * time.Second

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
	// OutcomeGoalJudge means a goal is active and live tasks are present:
	// the pipeline calls EvaluateGoal to get a verdict and decides arming
	// from it, rather than Decide arming (or not) up front.
	OutcomeGoalJudge
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
	case OutcomeGoalJudge:
		return "goal-judge"
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
	// ReapWatchdog is true on SessionEnd: kill this session's watchdog.
	ReapWatchdog bool
}

// Env carries environment facts the caller computed, since Decide itself
// does no I/O: whether the user is currently looking at this session's
// focused tmux pane, and how long ago this session last sent a
// notification (negative means never).
type Env struct {
	UserPresent     bool
	SinceLastNotify time.Duration
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
		return Decision{Outcome: OutcomeSilent, Reason: "session end", ReapWatchdog: true}
	case "Stop":
		return decideStop(scan, env)
	case "Notification":
		return decideNotification(in, scan, env)
	default:
		return Decision{Outcome: OutcomeSilent, Reason: fmt.Sprintf("unhandled event: %s", in.HookEventName)}
	}
}

// decideStop handles the Stop event's gates: an active goal or live
// background work always wins over user presence, since a Stop's
// implication (the assistant turn ended) is only a silence signal when
// there is genuinely nothing left for the session to track.
func decideStop(scan ScanResult, env Env) Decision {
	if scan.Goal.Status == GoalActive {
		// Claude Code's built-in /goal evaluator is skipped at any Stop
		// while background tasks are live (verified empirically,
		// undocumented). So when live tasks are present, this notifier is
		// the only place left that owns goal continuation, and it hands
		// off to the goal judge for the pipeline to execute; it must not
		// pre-arm the watchdog, since arming depends on the verdict. When
		// no tasks are live, the built-in /goal evaluator still owns
		// continuation, so the notifier just stays silent and arms the
		// watchdog to keep checking.
		if len(scan.LiveTasks) == 0 {
			return Decision{Outcome: OutcomeSilent, Reason: "goal active", ArmWatchdog: true}
		}
		return Decision{
			Outcome: OutcomeGoalJudge,
			Reason:  "goal active with live tasks: goal continuation",
		}
	}
	if len(scan.LiveTasks) > 0 {
		return Decision{
			Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
			Reason: "live tasks: parked vs pending", ArmWatchdog: true,
		}
	}
	if env.UserPresent {
		return Decision{Outcome: OutcomeSilent, Reason: "user present at focused pane"}
	}
	return Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "turn ended, composing"}
}

// decideNotification handles the Notification event's gates, branching on
// NotificationType. permission_prompt and agent_needs_input are
// blocked-tier: they skip presence and dedupe suppression entirely, because
// a blocked session needs the user's attention regardless of which tmux
// pane (if any) currently has focus.
func decideNotification(in HookInput, scan ScanResult, env Env) Decision {
	switch in.NotificationType {
	case "permission_prompt":
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "permission prompt",
		}

	case "idle_prompt":
		if env.SinceLastNotify >= 0 && env.SinceLastNotify < dedupeWindow {
			return Decision{
				Outcome: OutcomeSilent,
				Reason:  fmt.Sprintf("dedupe: notified %s ago", humanDuration(env.SinceLastNotify)),
			}
		}
		if scan.Goal.Status == GoalActive {
			return Decision{Outcome: OutcomeSilent, Reason: "goal active"}
		}
		if env.UserPresent {
			return Decision{Outcome: OutcomeSilent, Reason: "user present"}
		}
		return Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "idle backstop"}

	case "agent_needs_input":
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "background session needs input",
		}

	case "agent_completed":
		if env.UserPresent {
			return Decision{Outcome: OutcomeSilent, Reason: "user present"}
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
