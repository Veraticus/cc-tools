package notify

import (
	"fmt"
	"strings"
	"time"
)

// Digest field bounds. Each caps a section's variable text so the rendered
// document stays a bounded-size prompt regardless of how much the session
// produced; truncate keeps the head (the part named after the value, e.g.
// "how it starts"), truncateHead keeps the tail (the part after, e.g. "how
// it ends") — matching the head/tail semantics already established for
// LastAssistantText et al. in transcript.go.
const (
	maxUserAskLen         = 500
	maxRecentReplyLen     = 120
	maxAssistantEndLen    = 1500
	maxTaskDetailLen      = 80
	maxGoalConditionLen   = 200
	maxTeammateSummaryLen = 120

	// hoursPerDay names the 24 used in the days+hours tier of humanDuration,
	// which golangci-lint's mnd check otherwise flags as a bare magic number.
	hoursPerDay = 24
)

// DigestMeta is what the hook layer knows about the current stop event
// without touching the transcript: cwd/hostname context plus the hook's own
// last_assistant_message, which is authoritative for how THIS turn ended
// (scan.LastAssistantText is a transcript-derived fallback for callers that
// don't have it, e.g. a watchdog recheck with no fresh hook payload).
type DigestMeta struct {
	Project              string
	Host                 string
	Event                string
	LastAssistantMessage string
}

// String renders a GoalStatus as the lowercase word the GOAL section shows.
// buildGoalSection never calls this for GoalNone (it renders "none" itself,
// not a status= clause at all), but the case is listed explicitly rather
// than folded into default so the exhaustive linter can confirm every
// GoalStatus value is accounted for; an otherwise-unknown value (should not
// occur) still renders as "unknown" rather than panicking or leaking the
// int.
func (s GoalStatus) String() string {
	switch s {
	case GoalNone:
		return "none"
	case GoalActive:
		return "active"
	case GoalMet:
		return "met"
	case GoalCleared:
		return "cleared"
	case GoalFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// BuildDigest renders already-gathered session state into the plain-text
// document an LLM judge reads to decide whether a stop is genuinely idle. It
// is a pure function of its inputs and now: no I/O, no subprocess, safe to
// golden-test byte-for-byte. Sections are joined with a blank line between
// them and the whole document ends in exactly one trailing newline.
func BuildDigest(meta DigestMeta, scan ScanResult, tasks []TaskActivity, now time.Time) string {
	sections := []string{
		buildSessionSection(meta, scan, now),
		buildAskSection(scan),
		buildEndedSection(meta, scan),
		buildTasksSection(tasks, now),
	}
	if teammates := buildTeammatesSection(scan.Teammates, now); teammates != "" {
		sections = append(sections, teammates)
	}
	sections = append(sections, buildGoalSection(scan.Goal))
	return strings.Join(sections, "\n\n") + "\n"
}

// buildSessionSection renders project/host/event on one line (the brief
// allows one or two; one is simplest and loses nothing here) followed by the
// running-duration/user-turns line — omitted entirely when FirstTimestamp is
// zero, since there is no transcript timing to derive a duration from.
func buildSessionSection(meta DigestMeta, scan ScanResult, now time.Time) string {
	lines := []string{
		"SESSION",
		fmt.Sprintf("project=%s host=%s event=%s", meta.Project, meta.Host, meta.Event),
	}
	if !scan.FirstTimestamp.IsZero() {
		lines = append(lines, fmt.Sprintf("session running %s, %d user turns",
			humanDuration(now.Sub(scan.FirstTimestamp)), scan.UserTurns))
	}
	return strings.Join(lines, "\n")
}

// buildAskSection shows the last substantive (non-ack) user ask, plus a
// second line for the literal most-recent reply when it differs — this is
// what lets the judge see a bare "yes, go ahead" that followed a real
// question, instead of losing it under the substantive-length filter applied
// upstream in transcript.go.
func buildAskSection(scan ScanResult) string {
	lines := []string{"WHAT THE USER LAST ASKED"}
	if scan.LastSubstantiveUser == "" && scan.LastUserMessage == "" {
		lines = append(lines, "(none)")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, truncate(scan.LastSubstantiveUser, maxUserAskLen))
	if scan.LastUserMessage != "" && scan.LastUserMessage != scan.LastSubstantiveUser {
		lines = append(lines, fmt.Sprintf("most recent reply: %q", truncate(scan.LastUserMessage, maxRecentReplyLen)))
	}
	return strings.Join(lines, "\n")
}

// buildEndedSection prefers meta.LastAssistantMessage: it is the hook's own
// report of how THIS stop's turn ended, authoritative over whatever the
// transcript scan last saw (which can be stale on a watchdog recheck with no
// fresh hook payload). scan.LastAssistantText is the fallback for callers
// that don't have a hook payload at all.
func buildEndedSection(meta DigestMeta, scan ScanResult) string {
	text := meta.LastAssistantMessage
	if text == "" {
		text = scan.LastAssistantText
	}
	text = truncateHead(text, maxAssistantEndLen)
	if text == "" {
		text = "(no assistant text)"
	}
	return strings.Join([]string{"HOW THE TURN ENDED", text}, "\n")
}

// buildTasksSection lists every still-live task with its age and an
// output-freshness line, so the judge can tell "a bash command started 20s
// ago" (still normal) from "an agent has had no output for 40 minutes"
// (a stall worth flagging).
func buildTasksSection(tasks []TaskActivity, now time.Time) string {
	lines := []string{"STILL RUNNING"}
	if len(tasks) == 0 {
		lines = append(lines, "  (nothing)")
		return strings.Join(lines, "\n")
	}
	for _, t := range tasks {
		age := humanDuration(now.Sub(t.LaunchedAt))
		lines = append(lines, fmt.Sprintf("- %s started %s ago: %q — %s",
			string(t.Kind), age, t.Description, truncate(t.Detail, maxTaskDetailLen)))
		lines = append(lines, taskFreshnessLines(t, now)...)
	}
	return strings.Join(lines, "\n")
}

// taskFreshnessLines renders a task's output-freshness line, plus any tail
// lines (bash only — EnrichTasks leaves TailLines nil for agents, since a
// raw JSONL transcript tail is not useful preview text).
func taskFreshnessLines(t TaskActivity, now time.Time) []string {
	if !t.OutputExists {
		return []string{"  no output file yet"}
	}
	lines := []string{fmt.Sprintf("  output: %d bytes, last write %s ago",
		t.SizeBytes, humanDuration(now.Sub(t.LastWrite)))}
	for _, tl := range t.TailLines {
		lines = append(lines, fmt.Sprintf("  tail: %s", tl))
	}
	return lines
}

// buildTeammatesSection lists every spawned teammate with its spawn age and,
// once it has sent one, the age and summary of its most recent message —
// context for the judge, never a liveness signal (a teammate is never part
// of STILL RUNNING or any liveness gate). Unlike STILL RUNNING, which always
// renders even when empty so the judge can see the deterministic gate found
// nothing, this section is omitted entirely when there are no teammates:
// there is no gate here for an empty render to stand in for.
func buildTeammatesSection(teammates []TeammateActivity, now time.Time) string {
	if len(teammates) == 0 {
		return ""
	}
	lines := []string{"TEAMMATES"}
	for _, tm := range teammates {
		// %q like LastSummary below: Name comes from the constrained
		// teammate_spawned structured field today, but quoting keeps the
		// section uniformly newline-safe against any future source change.
		lines = append(lines, fmt.Sprintf("- %q spawned %s ago", tm.Name, humanDuration(now.Sub(tm.SpawnedAt))))
		if !tm.LastMessageAt.IsZero() {
			lines = append(lines, fmt.Sprintf("  last message %s ago: %q",
				humanDuration(now.Sub(tm.LastMessageAt)), truncate(tm.LastSummary, maxTeammateSummaryLen)))
		}
	}
	return strings.Join(lines, "\n")
}

// buildGoalSection renders "none" outright for GoalNone rather than routing
// it through GoalStatus.String() (which would say "unknown" for it) — GOAL
// has its own dedicated empty-state word, distinct from the status= value
// used for actual goal states.
func buildGoalSection(g GoalState) string {
	lines := []string{"GOAL"}
	if g.Status == GoalNone {
		lines = append(lines, "none")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("condition: %q status=%s iterations=%d",
		truncate(g.Condition, maxGoalConditionLen), g.Status, g.Iterations))
	return strings.Join(lines, "\n")
}

// humanDuration renders d as at most two significant time units with no
// sub-second precision. The tiering is deliberately coarse: this string
// lands in a prompt an LLM judge skims, not a log line an operator measures
// against — "3m20s" and "12m" both read as "recent enough," and the exact
// second count would only add noise a judge has to parse past. Negative
// durations (a clock skew or a stale timestamp) clamp to "0s" rather than
// rendering a nonsensical negative age.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < 10*time.Minute:
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm%ds", m, s)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < hoursPerDay*time.Hour:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d / (hoursPerDay * time.Hour))
		h := int((d % (hoursPerDay * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
