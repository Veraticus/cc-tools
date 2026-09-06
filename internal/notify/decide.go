package notify

import "fmt"

// Outcome is the deterministic action selected for one normalized hook event.
type Outcome int

const (
	// OutcomeSilent means the event must not notify.
	OutcomeSilent Outcome = iota
	// OutcomeSend means the event must notify.
	OutcomeSend
)

func (outcome Outcome) String() string {
	switch outcome {
	case OutcomeSilent:
		return "silent"
	case OutcomeSend:
		return "send"
	default:
		return "unknown"
	}
}

// Urgency controls ntfy priority and sound selection.
type Urgency string

const (
	// UrgencyBlocked marks a request requiring immediate user input.
	UrgencyBlocked Urgency = "blocked"
	// UrgencyDone marks a completed root turn.
	UrgencyDone Urgency = "done"
	// UrgencyInfo is retained for the sender's general informational tier.
	UrgencyInfo Urgency = "info"
)

// Decision is the complete deterministic result of Decide.
type Decision struct {
	Outcome Outcome
	Urgency Urgency
	Message string
	Reason  string
}

const notifTypeAgentNeedsInput = "agent_needs_input"

// Decide applies only structural event facts. Completion prose, task lists,
// teammate activity, elapsed time, and prior notification text never affect
// eligibility or urgency.
func Decide(in HookInput, scan ScanResult) Decision {
	if in.AgentID != "" || in.AgentType != "" {
		return Decision{Outcome: OutcomeSilent, Reason: "agent context"}
	}

	switch in.HookEventName {
	case eventSessionEnd:
		return Decision{Outcome: OutcomeSilent, Reason: "session end"}
	case eventStop:
		if (in.Harness == "" || in.Harness == harnessClaude) && scan.Goal.Status == GoalActive {
			return Decision{Outcome: OutcomeSilent, Reason: "goal active: deferring to built-in /goal"}
		}
		return Decision{Outcome: OutcomeSend, Urgency: UrgencyDone, Reason: "root completion"}
	case eventTurnComplete:
		return Decision{Outcome: OutcomeSend, Urgency: UrgencyDone, Reason: "root completion"}
	case eventNotification:
		return decideNotification(in)
	default:
		return Decision{Outcome: OutcomeSilent, Reason: fmt.Sprintf("unhandled event: %s", in.HookEventName)}
	}
}

func decideNotification(in HookInput) Decision {
	switch in.NotificationType {
	case "permission_prompt":
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "permission prompt",
		}
	case "elicitation_dialog", "elicitation_url_dialog":
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "elicitation dialog",
		}
	case notifTypeAgentNeedsInput:
		return Decision{
			Outcome: OutcomeSend, Urgency: UrgencyBlocked,
			Message: in.Message, Reason: "background session needs input",
		}
	default:
		return Decision{
			Outcome: OutcomeSilent,
			Reason:  fmt.Sprintf("unhandled notification type: %s", in.NotificationType),
		}
	}
}
