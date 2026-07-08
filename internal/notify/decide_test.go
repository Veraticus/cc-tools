package notify

import (
	"testing"
	"time"
)

// neverNotified is the sentinel Env.SinceLastNotify value meaning this
// session has not sent a notification yet.
const neverNotified = -1 * time.Second

func TestDecide(t *testing.T) {
	liveTasks := []LiveTask{{ID: "t1", Kind: TaskBash, Description: "run tests"}}

	tests := []struct {
		name string
		in   HookInput
		scan ScanResult
		env  Env
		want Decision
	}{
		// --- Gate 1: agent context, all events ---
		{
			name: "agent context silences any event",
			in:   HookInput{HookEventName: "Notification", NotificationType: "agent_completed", AgentID: "agent-1"},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "agent context"},
		},

		// --- Gate 2: SessionEnd ---
		{
			name: "session end reaps watchdog",
			in:   HookInput{HookEventName: "SessionEnd"},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "session end", ReapWatchdog: true},
		},

		// --- Gate 3: Stop ---
		{
			name: "stop with goal active arms watchdog",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{Goal: GoalState{Status: GoalActive}},
			env:  Env{},
			want: Decision{
				Outcome: OutcomeSilent, Reason: "goal active: deferring to built-in /goal", ArmWatchdog: true,
			},
		},
		{
			name: "stop with live tasks judges decide mode",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{LiveTasks: liveTasks},
			env:  Env{},
			want: Decision{
				Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
				Reason: "live tasks: parked vs pending", ArmWatchdog: true,
			},
		},
		{
			name: "stop with goal active and live tasks still defers silently",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{Goal: GoalState{Status: GoalActive}, LiveTasks: liveTasks},
			env:  Env{},
			want: Decision{
				Outcome: OutcomeSilent, Reason: "goal active: deferring to built-in /goal", ArmWatchdog: true,
			},
		},
		{
			name: "stop with teammates judges decide mode",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{Teammates: []TeammateActivity{{Name: "worker-wire"}}},
			env:  Env{},
			want: Decision{
				Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
				Reason: "teammates active: parked vs pending", ArmWatchdog: true,
			},
		},
		{
			// ArmWatchdog: true here is the epic's presence-watchdog rule:
			// with the broadcastCoveredWindow heuristic deleted (ownership is
			// now structural, not time-window-based), a Stop that resolves
			// silent solely because the user is present must arm the
			// watchdog itself to preserve the walked-away-while-blocked
			// coverage the presence-blind broadcast backstop used to provide.
			name: "stop with user present and no work is silent and arms watchdog",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{},
			env:  Env{UserPresent: true},
			want: Decision{Outcome: OutcomeSilent, Reason: "user present at focused pane", ArmWatchdog: true},
		},
		{
			name: "stop with no goal, no tasks, no presence composes",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "turn ended, composing"},
		},

		// --- Gate 4: Notification ---
		{
			name: "permission prompt always sends blocked",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "permission_prompt",
				Message: "allow this?",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: neverNotified},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "allow this?", Reason: "permission prompt",
			},
		},
		{
			name: "idle prompt within dedupe window is silent",
			in:   HookInput{HookEventName: "Notification", NotificationType: "idle_prompt"},
			scan: ScanResult{},
			env:  Env{SinceLastNotify: 30 * time.Second},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: notified 30s ago"},
		},
		{
			name: "idle prompt with goal active is silent",
			in:   HookInput{HookEventName: "Notification", NotificationType: "idle_prompt"},
			scan: ScanResult{Goal: GoalState{Status: GoalActive}},
			env:  Env{SinceLastNotify: neverNotified},
			want: Decision{Outcome: OutcomeSilent, Reason: "goal active"},
		},
		{
			name: "idle prompt with user present is silent",
			in:   HookInput{HookEventName: "Notification", NotificationType: "idle_prompt"},
			scan: ScanResult{},
			env:  Env{SinceLastNotify: neverNotified, UserPresent: true},
			want: Decision{Outcome: OutcomeSilent, Reason: "user present"},
		},
		{
			name: "idle prompt otherwise composes as backstop",
			in:   HookInput{HookEventName: "Notification", NotificationType: "idle_prompt"},
			scan: ScanResult{},
			env:  Env{SinceLastNotify: 5 * time.Minute},
			want: Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "idle backstop"},
		},
		{
			name: "agent needs input always sends blocked",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{UserPresent: true, SinceLastNotify: 1 * time.Second, SinceLastNotifySame: neverNotified},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "need a decision", Reason: "background session needs input",
			},
		},
		{
			name: "agent completed with user present is silent",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_completed",
				Message: "finished the run",
			},
			scan: ScanResult{},
			env:  Env{UserPresent: true},
			want: Decision{Outcome: OutcomeSilent, Reason: "user present"},
		},
		{
			name: "agent completed without presence sends done",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_completed",
				Message: "finished the run",
			},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyDone,
				Message: "finished the run", Reason: "background session completed",
			},
		},
		{
			name: "agent needs input claimed elsewhere is silent",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{Broadcast: &BroadcastFacts{Duplicate: true}},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: broadcast claimed by another session"},
		},
		{
			// Ownership is structural, not time-window-based: any broadcast
			// that resolves to a local job always defers to the source
			// session, regardless of claim state and regardless of whether
			// the source has sent yet (the epic's 18s-judge-race scenario —
			// the source's own pipeline fails open on judge errors, so no
			// ping is lost by deferring unconditionally).
			// broadcastCoveredWindow and Covered/CoveredAgo are deleted;
			// Local replaces them.
			name: "agent needs input resolved to local job is silent, deferred to source",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{Broadcast: &BroadcastFacts{Local: true}},
			want: Decision{Outcome: OutcomeSilent, Reason: "deferred to source session"},
		},
		{
			// Local defers even when a claim was already lost elsewhere: the
			// two suppressions are independent, and Local is checked first
			// (see suppressBroadcast), so this must never fall through to
			// the duplicate-claim reason.
			name: "agent needs input resolved to local job defers even if also claimed elsewhere",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{Broadcast: &BroadcastFacts{Local: true, Duplicate: true}},
			want: Decision{Outcome: OutcomeSilent, Reason: "deferred to source session"},
		},
		{
			name: "agent completed claimed elsewhere is silent even without presence",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_completed",
				Message: "finished the run",
			},
			scan: ScanResult{},
			env:  Env{Broadcast: &BroadcastFacts{Duplicate: true}},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: broadcast claimed by another session"},
		},

		// --- blocked-tier identical-repeat dedupe (blockedRepeatWindow) ---
		{
			name: "permission prompt identical repeat within window is silent",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "permission_prompt",
				Message: "allow this?",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: 3 * time.Minute},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: identical ping 3m0s ago"},
		},
		{
			name: "permission prompt different message sends despite recent identical elsewhere",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "permission_prompt",
				Message: "allow this other thing?",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: neverNotified},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "allow this other thing?", Reason: "permission prompt",
			},
		},
		{
			name: "permission prompt identical repeat past window sends",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "permission_prompt",
				Message: "allow this?",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: blockedRepeatWindow},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "allow this?", Reason: "permission prompt",
			},
		},
		{
			name: "agent needs input identical repeat within window is silent",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: 2 * time.Minute},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: identical ping 2m0s ago"},
		},
		{
			name: "agent needs input different message sends",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a different decision",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: neverNotified},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "need a different decision", Reason: "background session needs input",
			},
		},
		{
			name: "agent needs input identical repeat past window sends",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env:  Env{SinceLastNotifySame: blockedRepeatWindow},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "need a decision", Reason: "background session needs input",
			},
		},
		{
			name: "agent needs input broadcast claim takes precedence over identical-repeat window",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "agent_needs_input",
				Message: "need a decision",
			},
			scan: ScanResult{},
			env: Env{
				Broadcast:           &BroadcastFacts{Duplicate: true},
				SinceLastNotifySame: 1 * time.Minute,
			},
			want: Decision{Outcome: OutcomeSilent, Reason: "dedupe: broadcast claimed by another session"},
		},
		{
			name: "unhandled notification type is silent",
			in:   HookInput{HookEventName: "Notification", NotificationType: "auth_success"},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "unhandled notification type: auth_success"},
		},

		// --- Gate 5: any other event ---
		{
			name: "unhandled event is silent",
			in:   HookInput{HookEventName: "PreToolUse"},
			scan: ScanResult{},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "unhandled event: PreToolUse"},
		},

		// --- Precedence rows ---
		{
			name: "agent context wins over stop with goal active",
			in:   HookInput{HookEventName: "Stop", AgentID: "agent-1"},
			scan: ScanResult{Goal: GoalState{Status: GoalActive}},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "agent context"},
		},
		{
			name: "agent context wins over stop with goal active and live tasks",
			in:   HookInput{HookEventName: "Stop", AgentID: "agent-1"},
			scan: ScanResult{Goal: GoalState{Status: GoalActive}, LiveTasks: liveTasks},
			env:  Env{},
			want: Decision{Outcome: OutcomeSilent, Reason: "agent context"},
		},
		{
			name: "permission prompt sends even with user present and recent notify",
			in: HookInput{
				HookEventName: "Notification", NotificationType: "permission_prompt",
				Message: "allow this?",
			},
			scan: ScanResult{},
			env:  Env{UserPresent: true, SinceLastNotify: 1 * time.Second, SinceLastNotifySame: neverNotified},
			want: Decision{
				Outcome: OutcomeSend, Urgency: UrgencyBlocked,
				Message: "allow this?", Reason: "permission prompt",
			},
		},
		{
			name: "idle prompt never notified with no goal and not present still judges",
			in:   HookInput{HookEventName: "Notification", NotificationType: "idle_prompt"},
			scan: ScanResult{},
			env:  Env{SinceLastNotify: neverNotified},
			want: Decision{Outcome: OutcomeJudge, JudgeMode: JudgeModeCompose, Reason: "idle backstop"},
		},
		{
			name: "stop with live tasks still judges even when user present",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{LiveTasks: liveTasks},
			env:  Env{UserPresent: true},
			want: Decision{
				Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
				Reason: "live tasks: parked vs pending", ArmWatchdog: true,
			},
		},
		{
			name: "stop with teammates still judges even when user present",
			in:   HookInput{HookEventName: "Stop"},
			scan: ScanResult{Teammates: []TeammateActivity{{Name: "worker-wire"}}},
			env:  Env{UserPresent: true},
			want: Decision{
				Outcome: OutcomeJudge, JudgeMode: JudgeModeDecide,
				Reason: "teammates active: parked vs pending", ArmWatchdog: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.in, tt.scan, tt.env)
			if got != tt.want {
				t.Errorf("Decide() mismatch\ngot:  %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestOutcomeString(t *testing.T) {
	tests := []struct {
		o    Outcome
		want string
	}{
		{OutcomeSilent, "silent"},
		{OutcomeSend, "send"},
		{OutcomeJudge, "judge"},
	}
	for _, tt := range tests {
		if got := tt.o.String(); got != tt.want {
			t.Errorf("Outcome(%d).String() = %q, want %q", tt.o, got, tt.want)
		}
	}
}
