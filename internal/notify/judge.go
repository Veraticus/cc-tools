package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultJudgeTimeout applies when Judge.Timeout is zero.
const defaultJudgeTimeout = 10 * time.Second

// maxErrSnippetBytes bounds the stdout/stderr excerpt embedded in an error
// message: enough to see what went wrong, never enough to blow up a log line
// with a full transcript.
const maxErrSnippetBytes = 200

// maxTaskBytes and maxBodyBytes are the JudgeVerdict field caps after
// clamping (see truncate in transcript.go). Task becomes "<project> · <task>"
// downstream, so 60 bytes leaves room for the project prefix; Body is the
// notification's full text.
const (
	maxTaskBytes = 60
	maxBodyBytes = 200
)

// judgeRubric is the fixed instruction body sent ahead of the mode line and
// digest on every judge call. It never decides fallback behavior for the
// caller — Evaluate's only contract is verdict-or-error — but it is what
// teaches the model the notify/blocked/done judgment itself.
const judgeRubric = `You are judging whether a Claude Code coding session's turn just ended in a way that should notify the user away from their terminal, and if so writing that notification.

Rules:
- "blocked": the user's input gates ALL further progress right now — a direct question, a decision needed, or a failure with no path forward. A task still running elsewhere does NOT unblock a direct question; if the session is waiting on the user AND something is still running, it is still blocked.
- "done": a deliverable is ready and nothing still running will extend it further. A parked service (a dev server, a file watcher, anything whose output loops steadily rather than converging toward a result) is not pending work — the session is done, not waiting.
- Silence (notify=false): one or more still-running tasks (builds, tests, subagents) are genuine pending work the session is awaiting. Their completion revives the session and a later evaluation covers it — do not notify now.
- Tie-break, done vs. silent: unsure whether the session is truly done or should stay silent and wait — choose silent, a later check will run.
- Tie-break, blocked vs. done: unsure whether the session is blocked on the user or simply done — choose blocked, a missed question is worse than an unneeded ping.

Write:
- task: at most 6 words naming what the session is doing right now. Never "Claude finished" or similar filler.
- body: one sentence, at most 180 characters, in your own words — state what the session is waiting for (if blocked) or what was delivered (if done), and why. Never quote or copy text from the digest or transcript — write your own summary, not an excerpt.
- reason: one line explaining the call. This is for a decision log only — it is never shown to the user.

Output contract: respond with a single raw JSON object and nothing else — no markdown code fences, no commentary before or after. It must have exactly these keys: notify (bool), urgency (one of "blocked", "done", "info"), task (string), body (string), reason (string).`

// judgeModeCompose is appended when the send is already decided: the judge
// only writes better text for a notification that is going out regardless.
const judgeModeCompose = `Mode: compose-only — the decision to notify has already been made for you. Always set notify=true. Your only job is writing task, body, urgency, and reason.`

// judgeModeDecide is appended when the judge must choose notify-or-silence
// itself (e.g. distinguishing a parked dev server from pending work).
const judgeModeDecide = `Mode: decide — choose whether to notify at all. Set notify to true or false per the rules above, and if true, write task/body/urgency/reason.`

// goalRubric is the fixed instruction body for the goal-continuation judge
// mode: given a digest with live tasks, classify those tasks and, only when
// they are parked, judge whether the GOAL section's condition already holds.
// It shares no output keys with judgeRubric, so it stands alone rather than
// being appended as a mode line.
const goalRubric = `You are judging whether a Claude Code coding session's goal condition has already been met, given a digest showing the session's current GOAL, its live (still-running) tasks, and the text of the turn that just ended.

Rules:
- "pending": at least one live task will exit on its own and produce a result — a build, a test run, a subagent. Genuine pending work is still awaited; do not judge the goal yet.
- "parked": ALL live tasks are non-converging processes — a dev server, a file watcher, anything whose output loops steadily rather than converging toward a result. None of them will ever finish on their own, so waiting further is pointless.
- Only when tasks is "parked", judge whether the GOAL section's condition is already satisfied by what the digest shows. When tasks is "pending", goal_met must be false — the goal cannot be judged met while genuine pending work is still awaited.

Write:
- reason: one line. For pending, name what is being awaited. For parked and not yet met, name what remains to meet the goal. For parked and met, say why it is met.

Output contract: respond with a single raw JSON object and nothing else — no markdown code fences, no commentary before or after. It must have exactly these keys: tasks (one of "pending", "parked"), goal_met (bool), reason (string).`

// maxGoalReasonBytes bounds the GoalVerdict.Reason field after clamping (see
// truncate in transcript.go). Reason is decision-log-only, same as
// JudgeVerdict.Reason, so it gets a generous but bounded cap.
const maxGoalReasonBytes = 200

// GoalVerdict is the LLM judge's validated answer for the goal-continuation
// question. Reason is decision-log-only and never reaches the user.
type GoalVerdict struct {
	Tasks   string `json:"tasks"`
	GoalMet bool   `json:"goal_met"`
	Reason  string `json:"reason"`
}

// JudgeVerdict is the LLM judge's validated answer. Task and Body are
// already byte-clamped (maxTaskBytes/maxBodyBytes) by the time Evaluate
// returns them. Reason is decision-log-only and never reaches the user.
type JudgeVerdict struct {
	Notify  bool    `json:"notify"`
	Urgency Urgency `json:"urgency"`
	Task    string  `json:"task"`
	Body    string  `json:"body"`
	Reason  string  `json:"reason"`
}

// Judge runs the LLM judge over a rendered digest via a headless `claude -p`
// call. Judge never decides fallback behavior itself: every failure path
// returns a wrapped error and a zero-value JudgeVerdict. Mapping a judge
// failure to a deterministic send is the caller's job (a later pipeline
// stage), not this type's.
type Judge struct {
	// Bin is the claude binary to invoke — "claude" in production, a stub
	// script path in tests.
	Bin string
	// Model is passed as --model, e.g. "claude-haiku-4-5".
	Model string
	// Timeout bounds the subprocess call. Zero applies defaultJudgeTimeout.
	Timeout time.Duration
}

// Evaluate runs the judge over digest for the given mode and returns a
// validated verdict or an error. It never guesses on failure — timeout,
// nonzero exit, empty output, malformed JSON, and invalid urgency are all
// returned as errors with a bounded snippet of whatever the subprocess said,
// never papered over with a default verdict.
func (j Judge) Evaluate(ctx context.Context, digest string, mode JudgeMode) (JudgeVerdict, error) {
	prompt, err := buildJudgePrompt(digest, mode)
	if err != nil {
		return JudgeVerdict{}, err
	}

	timeout := j.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, runErr := j.run(runCtx, prompt)
	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return JudgeVerdict{}, fmt.Errorf("judge: claude -p timed out after %s: %w", timeout, runErr)
		}
		return JudgeVerdict{}, fmt.Errorf("judge: claude -p exited with error: %w (stderr: %s)",
			runErr, snippet(stderr))
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return JudgeVerdict{}, errors.New("judge: claude -p returned empty stdout")
	}

	return parseVerdict(out)
}

// EvaluateGoal runs the goal-continuation judge over digest and returns a
// validated verdict or an error. Like Evaluate, it never guesses on
// failure — timeout, nonzero exit, empty output, malformed JSON, and an
// invalid tasks value are all returned as errors with a bounded snippet of
// whatever the subprocess said, never papered over with a default verdict.
func (j Judge) EvaluateGoal(ctx context.Context, digest string) (GoalVerdict, error) {
	prompt := buildGoalPrompt(digest)

	timeout := j.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, runErr := j.run(runCtx, prompt)
	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return GoalVerdict{}, fmt.Errorf("judge: claude -p timed out after %s: %w", timeout, runErr)
		}
		return GoalVerdict{}, fmt.Errorf("judge: claude -p exited with error: %w (stderr: %s)",
			runErr, snippet(stderr))
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return GoalVerdict{}, errors.New("judge: claude -p returned empty stdout")
	}

	return parseGoalVerdict(out)
}

// buildGoalPrompt joins the fixed goal rubric and the digest into the single
// STDIN payload for `claude -p`, mirroring buildJudgePrompt.
func buildGoalPrompt(digest string) string {
	return fmt.Sprintf("%s\n\nDIGEST\n%s\n", goalRubric, digest)
}

// buildJudgePrompt joins the fixed rubric, the mode-specific instruction
// line, and the digest into the single STDIN payload for `claude -p`. The
// prompt goes on STDIN rather than argv specifically to avoid argv-length
// limits on a digest that can grow with task/tail-line content.
func buildJudgePrompt(digest string, mode JudgeMode) (string, error) {
	var modeLine string
	switch mode {
	case JudgeModeCompose:
		modeLine = judgeModeCompose
	case JudgeModeDecide:
		modeLine = judgeModeDecide
	case JudgeModeNone:
		return "", errors.New("judge: buildJudgePrompt called with JudgeModeNone")
	default:
		return "", fmt.Errorf("judge: unknown judge mode %d", mode)
	}
	return fmt.Sprintf("%s\n\n%s\n\nDIGEST\n%s\n", judgeRubric, modeLine, digest), nil
}

// run execs the claude binary and returns its stdout/stderr. Flags chosen,
// each for a specific reason confirmed against `claude -p --help` on the
// installed version:
//   - "-p": headless, non-interactive — required for a scripted call.
//   - "--model <Model>": pin the judge model explicitly rather than trust
//     whatever model the invoking user's session happens to be configured
//     for.
//   - "--output-format text": the model's raw text reply on stdout, no JSON
//     envelope to unwrap first — we already ask the model itself to emit
//     JSON as that text.
//   - "--strict-mcp-config" + `--mcp-config {"mcpServers":{}}`: load zero MCP
//     servers, ignoring any project/user-level MCP configuration — the judge
//     is a pure text-in/text-out call with no tool access. The bare "{}"
//     form is rejected by this installed CLI version ("mcpServers: Invalid
//     input: expected record, received undefined"); the config must name the
//     (empty) mcpServers record explicitly.
//
// "--max-turns 1" is deliberately omitted: it does not exist in this
// installed `claude -p --help` output.
//
// Env is os.Environ() plus CLAUDE_HOOKS_NTFY_DISABLED=true. This is a
// recursion guard, not a preference: the `-p` session this spawns fires its
// own Stop hook, which (post-cutover) is this very notifier binary — without
// the guard, a judge call would trigger a nested judge call. The env var is
// that notifier's kill switch, read at the top of the hook path, so the
// judge can never recurse into itself.
func (j Judge) run(ctx context.Context, prompt string) (string, string, error) {
	args := []string{
		"-p",
		"--model", j.Model,
		"--output-format", "text",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	}
	//nolint:gosec // j.Bin is caller-controlled config ("claude" in production, a test stub path), not external input
	cmd := exec.CommandContext(ctx, j.Bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "CLAUDE_HOOKS_NTFY_DISABLED=true")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// parseVerdict strips any markdown fences, unmarshals the JSON verdict,
// validates urgency, and clamps Task/Body to their byte caps.
func parseVerdict(raw string) (JudgeVerdict, error) {
	cleaned := stripFences(raw)

	var v JudgeVerdict
	if err := json.Unmarshal([]byte(cleaned), &v); err != nil {
		return JudgeVerdict{}, fmt.Errorf("judge: malformed verdict JSON: %w (stdout: %s)", err, snippet(raw))
	}

	switch v.Urgency {
	case UrgencyBlocked, UrgencyDone, UrgencyInfo:
		// valid
	default:
		return JudgeVerdict{}, fmt.Errorf("judge: invalid urgency %q (stdout: %s)", v.Urgency, snippet(raw))
	}

	v.Task = truncate(v.Task, maxTaskBytes)
	v.Body = truncate(v.Body, maxBodyBytes)
	return v, nil
}

// parseGoalVerdict strips any markdown fences, unmarshals the JSON verdict,
// validates tasks, and clamps Reason to its byte cap. It mirrors
// parseVerdict's pattern exactly.
func parseGoalVerdict(raw string) (GoalVerdict, error) {
	cleaned := stripFences(raw)

	var v GoalVerdict
	if err := json.Unmarshal([]byte(cleaned), &v); err != nil {
		return GoalVerdict{}, fmt.Errorf("judge: malformed goal verdict JSON: %w (stdout: %s)", err, snippet(raw))
	}

	switch v.Tasks {
	case "pending", "parked":
		// valid
	default:
		return GoalVerdict{}, fmt.Errorf("judge: invalid tasks %q (stdout: %s)", v.Tasks, snippet(raw))
	}

	v.Reason = truncate(v.Reason, maxGoalReasonBytes)
	return v, nil
}

// stripFences removes a leading ```json/``` and trailing ``` if the model
// wrapped its JSON in a markdown code fence despite the output contract
// asking it not to — cheap insurance against the one deviation models
// reliably make.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// snippet bounds s to maxErrSnippetBytes for embedding in an error message.
func snippet(s string) string {
	return truncate(strings.TrimSpace(s), maxErrSnippetBytes)
}
