// Package notify parses Claude Code session transcripts (JSONL) into goal
// state and live background task information. It is the ground-truth layer
// a notification pipeline builds on: deterministic gates, digests, and a
// watchdog all consume ScanTranscript's output.
package notify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Scanner tuning: real transcript lines can run multi-hundred-KB and files
// reach tens of MB, so we stream line-by-line with a generous max line size
// rather than loading the whole file.
const (
	initialScanBufferSize = 64 * 1024
	maxLineBufferSize     = 10 * 1024 * 1024
	maxDetailLen          = 200
)

var (
	bgIDPattern    = regexp.MustCompile(`Command running in background with ID: ([a-z0-9]+)`)
	agentIDPattern = regexp.MustCompile(`agentId: ([a-z0-9]{6,20})`)
	taskIDPattern  = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

	// bashOutputFilePattern and agentOutputFilePattern extract the on-disk
	// output path from a launch acknowledgment. Real transcript text (not
	// the pattern name) is ground truth for these shapes: a bg-bash ack
	// reads "...Output is being written to: /tmp/claude-1000/<proj>/<session>/
	// tasks/<id>.output. You will be notified..." and an async-agent ack
	// carries a standalone "output_file: /tmp/claude-1000/<proj>/<session>/
	// tasks/<id>.output" line. \S+ is deliberate over a charset like
	// [^\s.]+: real paths contain dots (session UUIDs, the .output suffix)
	// and dashes (anonymized project segments), so excluding "." would
	// truncate the match. The only punctuation to strip is the sentence-
	// final period the bash form appends after ".output" — see
	// extractOutputFile.
	bashOutputFilePattern  = regexp.MustCompile(`Output is being written to: (\S+)`)
	agentOutputFilePattern = regexp.MustCompile(`output_file: (\S+)`)
)

// asyncAgentAckMarker is the first sentence of every genuine background-agent
// launch acknowledgment. It is the decisive gate for a TaskAgent launch: a
// synchronous Agent call's final report also embeds "agentId: ..." (as part
// of its SendMessage-to-continue text), so the agentId pattern and kind/
// run_in_background pairing alone are not enough to distinguish a real async
// launch from an already-finished synchronous report. Only the ack text
// marks a launch.
const asyncAgentAckMarker = "Async agent launched successfully"

// GoalStatus represents the current state of a /goal condition as recorded
// in a session transcript.
type GoalStatus int

const (
	// GoalNone means no goal_status record was found in the transcript.
	GoalNone GoalStatus = iota
	// GoalActive means a goal is set and has not yet been met, cleared, or failed.
	GoalActive
	// GoalMet means the goal condition was satisfied.
	GoalMet
	// GoalCleared means the user cleared the goal before it was met.
	GoalCleared
	// GoalFailed means goal evaluation gave up without the condition being met.
	GoalFailed
)

// GoalState captures the most recent goal_status verdict found in a
// transcript. The last matching record in the transcript wins.
type GoalState struct {
	Status     GoalStatus
	Condition  string
	Iterations int
}

// TaskKind identifies whether a LiveTask is a background Bash command or an
// async Agent launch.
type TaskKind string

const (
	// TaskBash is a background Bash command launch.
	TaskBash TaskKind = "bash"
	// TaskAgent is an async Agent launch.
	TaskAgent TaskKind = "agent"
)

// LiveTask describes a background bash command or agent launch that has not
// yet received a matching completion notification in the transcript.
type LiveTask struct {
	ID          string
	Kind        TaskKind
	Description string
	Detail      string
	LaunchedAt  time.Time
	// OutputFile is the on-disk path the launch acknowledgment said output
	// is being written to, taken verbatim from that ack text (never derived
	// from cwd or session ID: a real bg-bash task has been observed writing
	// under a different session ID than the transcript's own). Empty when
	// the ack carried no path; this never affects liveness.
	OutputFile string
}

// transcriptRecord is the subset of a transcript JSONL line's shape that
// ScanTranscript cares about. Unknown fields are ignored by encoding/json.
type transcriptRecord struct {
	Type       string          `json:"type"`
	Timestamp  string          `json:"timestamp"`
	Content    string          `json:"content"`
	Message    *rawMessage     `json:"message"`
	Attachment json.RawMessage `json:"attachment"`
}

// rawMessage defers decoding of Content: it is a plain string for
// human-typed messages and system reminders, and an array of contentBlock
// for assistant tool_use turns and user tool_result turns.
type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock covers tool_use, tool_result, and text block shapes found in
// message.content arrays. Its own Content field defers decoding the same
// way: a tool_result's content is a plain string for Bash results and an
// array of text blocks for Agent results.
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

type bashInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	// RunInBackground defaults to false (foreground) when absent, which is
	// the Bash tool's own default.
	RunInBackground bool `json:"run_in_background"`
}

type agentInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	// RunInBackground is a pointer because absent must resolve to true
	// (background is the Agent tool's default), unlike Bash's false default.
	RunInBackground *bool `json:"run_in_background"`
}

// attachmentEnvelope is decoded first to route to the right richer struct,
// since "attachment" holds either a goal_status or a queued_command payload.
type attachmentEnvelope struct {
	Type string `json:"type"`
}

type goalAttachment struct {
	Type       string `json:"type"`
	Met        bool   `json:"met"`
	Sentinel   bool   `json:"sentinel"`
	Failed     bool   `json:"failed"`
	Condition  string `json:"condition"`
	Iterations int    `json:"iterations"`
}

type queuedCommandAttachment struct {
	Type   string `json:"type"`
	Prompt string `json:"prompt"`
}

// toolUseInfo is what we remember about a Bash or Agent tool_use record
// until its matching tool_result announces the launch's background/agent ID.
// name and runInBackground are what let processUserMessage kind-check a
// pairing instead of trusting pattern text alone: name must match the
// pattern being registered (Bash for a background-bash ID, Agent for an
// agent ID), and runInBackground must resolve true, before a launch fires.
type toolUseInfo struct {
	description     string
	detail          string
	name            string
	runInBackground bool
}

// scanState is the mutable state threaded through a single streaming pass
// over a transcript.
type scanState struct {
	goal     GoalState
	toolUses map[string]toolUseInfo
	live     map[string]*LiveTask
	order    []string
}

func newScanState() *scanState {
	return &scanState{
		goal:     GoalState{Status: GoalNone},
		toolUses: make(map[string]toolUseInfo),
		live:     make(map[string]*LiveTask),
	}
}

// ScanTranscript reads a Claude Code session transcript in JSONL format and
// returns the most recent goal state along with any background bash or
// agent tasks that have not yet received a completion notification.
// Malformed or unparseable lines are skipped rather than treated as fatal;
// an empty transcript yields a zero-value GoalState and no tasks.
func ScanTranscript(r io.Reader) (GoalState, []LiveTask, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxLineBufferSize)

	state := newScanState()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		state.processLine(line)
	}
	if err := scanner.Err(); err != nil {
		return state.goal, nil, fmt.Errorf("scanning transcript: %w", err)
	}

	return state.goal, state.tasks(), nil
}

func (s *scanState) processLine(line []byte) {
	var rec transcriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}

	switch rec.Type {
	case "attachment":
		s.processAttachment(rec.Attachment)
	case "queue-operation":
		s.extractCompletions(rec.Content)
	}

	if rec.Message == nil {
		return
	}
	switch rec.Message.Role {
	case "assistant":
		s.processAssistantMessage(rec.Message.Content)
	case "user":
		s.processUserMessage(rec.Message.Content, rec.Timestamp)
	}
}

// processAttachment handles the top-level "attachment" field, which carries
// either a goal_status verdict or a queued_command task-notification.
func (s *scanState) processAttachment(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var envelope attachmentEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	switch envelope.Type {
	case "goal_status":
		var att goalAttachment
		if err := json.Unmarshal(raw, &att); err == nil {
			s.applyGoalStatus(att)
		}
	case "queued_command":
		var qc queuedCommandAttachment
		if err := json.Unmarshal(raw, &qc); err == nil {
			s.extractCompletions(qc.Prompt)
		}
	}
}

// applyGoalStatus fully replaces the goal state from the fields on the
// current record: the last matching goal_status record in the transcript
// wins outright, it is not merged with whatever came before.
func (s *scanState) applyGoalStatus(att goalAttachment) {
	s.goal.Condition = att.Condition
	s.goal.Iterations = att.Iterations
	switch {
	case att.Sentinel && !att.Met:
		s.goal.Status = GoalActive
	case att.Sentinel && att.Met:
		s.goal.Status = GoalCleared
	case !att.Met && att.Failed:
		s.goal.Status = GoalFailed
	case !att.Met:
		s.goal.Status = GoalActive
	default:
		s.goal.Status = GoalMet
	}
}

// extractCompletions removes any live task whose ID appears in a
// <task-id>...</task-id> wrapper within text. text must come from a record
// already known structurally to be a task-notification (a queue-operation's
// content, or a queued_command attachment's prompt) — never from raw line
// bytes — so that ordinary prose mentioning "<task-id>" (e.g. a transcript
// where the assistant is discussing this very format) can never be mistaken
// for a real completion.
func (s *scanState) extractCompletions(text string) {
	for _, m := range taskIDPattern.FindAllStringSubmatch(text, -1) {
		delete(s.live, m[1])
	}
}

func (s *scanState) processAssistantMessage(raw json.RawMessage) {
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		switch block.Name {
		case "Bash":
			var in bashInput
			if err := json.Unmarshal(block.Input, &in); err == nil {
				s.toolUses[block.ID] = toolUseInfo{
					description:     in.Description,
					detail:          in.Command,
					name:            "Bash",
					runInBackground: in.RunInBackground,
				}
			}
		case "Agent":
			var in agentInput
			if err := json.Unmarshal(block.Input, &in); err == nil {
				runInBackground := true
				if in.RunInBackground != nil {
					runInBackground = *in.RunInBackground
				}
				s.toolUses[block.ID] = toolUseInfo{
					description:     in.Description,
					detail:          in.Prompt,
					name:            "Agent",
					runInBackground: runInBackground,
				}
			}
		}
	}
}

// processUserMessage looks for tool_result blocks announcing a background
// bash or agent launch. Registration requires the block's tool_use_id to
// pair to a remembered tool_use of the matching kind with the right
// run_in_background resolution — see the rationale on addLiveTask. An Agent
// pairing additionally requires the async launch-acknowledgment marker (see
// asyncAgentAckMarker): a synchronous Agent call's tool_result also embeds
// "agentId: ..." as part of its own SendMessage-to-continue text, and with
// run_in_background absent (nil resolves to true, Agent's own default) the
// pairing and run_in_background checks alone would misclassify that
// already-finished report as a live launch. The Bash pairing needs no such
// extra gate — a background Bash launch's tool_result text is exactly the
// "Command running in background with ID: ..." announcement.
func (s *scanState) processUserMessage(raw json.RawMessage, timestamp string) {
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		info, paired := s.toolUses[block.ToolUseID]
		text := extractBlockText(block.Content)
		if m := bgIDPattern.FindStringSubmatch(text); m != nil {
			if paired && info.name == "Bash" && info.runInBackground {
				s.addLiveTask(m[1], TaskBash, timestamp, info, extractOutputFile(bashOutputFilePattern, text))
			}
		}
		if m := agentIDPattern.FindStringSubmatch(text); m != nil {
			if paired && info.name == "Agent" && info.runInBackground && strings.Contains(text, asyncAgentAckMarker) {
				s.addLiveTask(m[1], TaskAgent, timestamp, info, extractOutputFile(agentOutputFilePattern, text))
			}
		}
	}
}

// addLiveTask records a newly-launched task. Callers only reach here after
// structurally confirming the tool_result's tool_use_id pairs to a
// remembered tool_use of the matching kind (Bash/Agent) with
// run_in_background resolving true — never from the bgID/agentID pattern
// text alone. That text can appear in a tool_result for reasons that are
// not a real launch: a plain Bash command whose own output happens to quote
// the pattern (e.g. a grep investigating this very format), or a
// synchronous Agent call's final report, which embeds "agentId: ..." (as
// its SendMessage-to-continue text) but has already returned inline with
// the task over. Requiring the pairing and kind check is what keeps those
// cases from registering a false launch; for Agent specifically, the
// caller also requires the asyncAgentAckMarker text, since a sync report's
// embedded "agentId: ..." alone survives the pairing and kind check
// whenever run_in_background was left absent.
func (s *scanState) addLiveTask(id string, kind TaskKind, timestamp string, info toolUseInfo, outputFile string) {
	if _, exists := s.live[id]; exists {
		return
	}
	s.live[id] = &LiveTask{
		ID:          id,
		Kind:        kind,
		Description: info.description,
		Detail:      truncate(info.detail, maxDetailLen),
		LaunchedAt:  parseTimestamp(timestamp),
		OutputFile:  outputFile,
	}
	s.order = append(s.order, id)
}

// extractOutputFile pulls the on-disk output path out of a launch
// acknowledgment using pattern (bashOutputFilePattern or
// agentOutputFilePattern), returning "" when the ack carries no path. The
// bash sentence form's match includes a trailing "." (the path is
// immediately followed by ". You will be notified..." with no space before
// the period), which TrimSuffix strips; the agent line form has no such
// trailing punctuation, so the trim is a no-op there.
func extractOutputFile(pattern *regexp.Regexp, text string) string {
	m := pattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSuffix(m[1], ".")
}

// tasks returns the still-live tasks in launch order. An ID can appear more
// than once in order (e.g. a completed task whose ID later reappears in a
// paired launch), so seen guards against emitting the same live task twice.
func (s *scanState) tasks() []LiveTask {
	if len(s.order) == 0 {
		return nil
	}
	result := make([]LiveTask, 0, len(s.order))
	seen := make(map[string]bool, len(s.order))
	for _, id := range s.order {
		if seen[id] {
			continue
		}
		seen[id] = true
		if lt, ok := s.live[id]; ok {
			result = append(result, *lt)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// extractBlockText handles a tool_result's content field, which is a plain
// string for Bash results and an array of {"type":"text",...} blocks for
// Agent results.
func extractBlockText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func parseTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}
