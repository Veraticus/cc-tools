// Package notify parses Claude Code session transcripts (JSONL) into a
// ScanResult: goal state, live background task information, recent
// conversation text, session timing, and bytes consumed. It is the
// ground-truth layer a notification pipeline builds on: deterministic
// gates, digests, and a watchdog all consume ScanTranscript's output.
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
	// substantiveUserMinLen is the trimmed length (in bytes) a human-typed
	// user message must reach to update LastSubstantiveUser rather than only
	// LastUserMessage. Chosen to exclude one-word acks ("yes", "ok", "go
	// ahead") while still capturing short-but-real asks.
	substantiveUserMinLen = 40
	// maxAssistantTextLen bounds LastAssistantText via truncateHead, which
	// keeps the tail: the end of an assistant turn carries the ask or
	// conclusion, unlike the user-message truncation elsewhere in this
	// package which keeps the head.
	maxAssistantTextLen = 2000
)

var (
	bgIDPattern    = regexp.MustCompile(`Command running in background with ID: ([a-z0-9]+)`)
	agentIDPattern = regexp.MustCompile(`agentId: ([a-z0-9]{6,20})`)
	taskIDPattern  = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

	// systemReminderPattern matches an embedded <system-reminder>...</system-reminder>
	// span so it can be stripped from a user message before the human-typed-text
	// checks run: a real prompt often carries one of these appended by the CLI,
	// and a reminder-only record must not be mistaken for typed text. Non-greedy
	// and dot-matches-newline since reminders are frequently multiline.
	systemReminderPattern = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

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
	IsMeta     bool            `json:"isMeta"`
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

// contentBlockTypeText is the contentBlock.Type discriminator for a plain
// text block, as distinct from tool_use/tool_result blocks.
const contentBlockTypeText = "text"

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
// detail is truncated to maxDetailLen at store time (in
// processAssistantMessage) rather than deferred to addLiveTask: a session
// can accumulate far more tool_use records awaiting a result than ever
// become live tasks, so truncating here bounds this map's per-entry memory
// immediately instead of holding a full untruncated command/prompt for the
// entry's whole lifetime.
type toolUseInfo struct {
	description     string
	detail          string
	name            string
	runInBackground bool
}

// Tool names as they appear in a transcript's tool_use blocks.
const (
	toolNameBash  = "Bash"
	toolNameAgent = "Agent"
)

// ScanResult is the full result of a single streaming pass over a session
// transcript: goal state, live background tasks, the most recent
// conversation turns, session timing, and how many bytes were consumed.
// This is the ground-truth layer a notification pipeline builds on:
// deterministic gates, digests, and a watchdog all consume it.
type ScanResult struct {
	Goal      GoalState
	LiveTasks []LiveTask

	// LastUserMessage is the most recently human-typed user text found in
	// the transcript, of any length (e.g. a bare "yes").
	LastUserMessage string
	// LastSubstantiveUser is the most recently human-typed user text whose
	// trimmed/stripped length is at least substantiveUserMinLen; short acks
	// never update it.
	LastSubstantiveUser string
	// LastAssistantText is the most recent non-empty concatenation of an
	// assistant record's text blocks, tail-truncated to maxAssistantTextLen
	// via truncateHead so the end of the message (the ask or conclusion)
	// survives truncation.
	LastAssistantText string

	// FirstTimestamp and LastTimestamp are the first and last top-level
	// "timestamp" values that parsed successfully, across every record type
	// in the transcript.
	FirstTimestamp time.Time
	LastTimestamp  time.Time

	// UserTurns counts human-typed user records (the same records that can
	// update LastUserMessage/LastSubstantiveUser).
	UserTurns int

	// BytesScanned is the number of input bytes consumed, including the line
	// terminator per line: bufio.Scanner strips terminators, so this
	// accumulates len(line)+1 for every line read (blank lines included). A
	// final unterminated line still counts +1 under this convention, which
	// undercounts the true file size by one byte in that one case; ordinary
	// JSONL files end with a trailing newline, so the count is exact for
	// them and remains a monotone growth baseline otherwise (the only use a
	// watchdog needs).
	BytesScanned int64
}

// scanState is the mutable state threaded through a single streaming pass
// over a transcript.
type scanState struct {
	goal     GoalState
	toolUses map[string]toolUseInfo
	live     map[string]*LiveTask
	order    []string

	lastUserMessage     string
	lastSubstantiveUser string
	lastAssistantText   string

	firstTimestamp time.Time
	lastTimestamp  time.Time

	userTurns    int
	bytesScanned int64
}

func newScanState() *scanState {
	return &scanState{
		goal:     GoalState{Status: GoalNone},
		toolUses: make(map[string]toolUseInfo),
		live:     make(map[string]*LiveTask),
	}
}

// ScanTranscript reads a Claude Code session transcript in JSONL format and
// returns a ScanResult combining goal state, live background/agent tasks,
// the most recent conversation turns, session timing, and bytes consumed.
// Malformed or unparseable lines are skipped rather than treated as fatal;
// an empty transcript yields a zero-value ScanResult.
func ScanTranscript(r io.Reader) (ScanResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialScanBufferSize), maxLineBufferSize)

	state := newScanState()
	for scanner.Scan() {
		line := scanner.Bytes()
		state.bytesScanned += int64(len(line)) + 1
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		state.processLine(line)
	}
	if err := scanner.Err(); err != nil {
		return state.result(), fmt.Errorf("scanning transcript: %w", err)
	}

	return state.result(), nil
}

// result assembles the accumulated scan state into a ScanResult.
func (s *scanState) result() ScanResult {
	return ScanResult{
		Goal:                s.goal,
		LiveTasks:           s.tasks(),
		LastUserMessage:     s.lastUserMessage,
		LastSubstantiveUser: s.lastSubstantiveUser,
		LastAssistantText:   s.lastAssistantText,
		FirstTimestamp:      s.firstTimestamp,
		LastTimestamp:       s.lastTimestamp,
		UserTurns:           s.userTurns,
		BytesScanned:        s.bytesScanned,
	}
}

func (s *scanState) processLine(line []byte) {
	var rec transcriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}

	s.recordTimestamp(rec.Timestamp)

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
		s.captureUserText(rec.Message.Content, rec.IsMeta)
	}
}

// recordTimestamp updates the running first/last timestamps from a
// top-level "timestamp" value, across every record type. Unparseable or
// empty timestamps are ignored rather than resetting the running values.
func (s *scanState) recordTimestamp(ts string) {
	t := parseTimestamp(ts)
	if t.IsZero() {
		return
	}
	if s.firstTimestamp.IsZero() {
		s.firstTimestamp = t
	}
	s.lastTimestamp = t
}

// captureUserText updates LastUserMessage/LastSubstantiveUser/UserTurns from
// a user-role message.content payload, applying each exclusion as its own
// documented check:
//   - isMeta:true records never count, regardless of content.
//   - a payload that isn't a plain string, or an array of blocks including a
//     tool_result, is not human-typed text.
//   - any embedded <system-reminder>...</system-reminder> span is stripped
//     before the remaining checks, so a real prompt with an appended
//     reminder still counts on its typed portion, and a reminder-only
//     record (which strips down to nothing) does not count at all.
//   - text whose trimmed, stripped form starts with "<" (task-notification
//     and local-command wrappers) is excluded.
func (s *scanState) captureUserText(raw json.RawMessage, isMeta bool) {
	if isMeta {
		return
	}
	text, ok := humanTypedText(raw)
	if !ok {
		return
	}
	text = systemReminderPattern.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<") {
		return
	}

	s.userTurns++
	s.lastUserMessage = text
	if len(text) >= substantiveUserMinLen {
		s.lastSubstantiveUser = text
	}
}

// humanTypedText extracts the candidate human-typed text from a user
// message.content payload. ok is false when the payload is structurally not
// human-typed text: an array containing a tool_result block, or neither a
// plain string nor an array of blocks at all. A plain string is always
// human-typed (ok); an array of blocks is human-typed only if it contains at
// least one text block and no tool_result block, with all text blocks
// concatenated.
func humanTypedText(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var sb strings.Builder
	hasText := false
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			return "", false
		case contentBlockTypeText:
			hasText = true
			sb.WriteString(b.Text)
		}
	}
	if !hasText {
		return "", false
	}
	return sb.String(), true
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
	s.captureAssistantText(blocks)
	for _, block := range blocks {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		switch block.Name {
		case toolNameBash:
			var in bashInput
			if err := json.Unmarshal(block.Input, &in); err == nil {
				s.toolUses[block.ID] = toolUseInfo{
					description:     in.Description,
					detail:          truncate(in.Command, maxDetailLen),
					name:            toolNameBash,
					runInBackground: in.RunInBackground,
				}
			}
		case toolNameAgent:
			var in agentInput
			if err := json.Unmarshal(block.Input, &in); err == nil {
				runInBackground := true
				if in.RunInBackground != nil {
					runInBackground = *in.RunInBackground
				}
				s.toolUses[block.ID] = toolUseInfo{
					description:     in.Description,
					detail:          truncate(in.Prompt, maxDetailLen),
					name:            toolNameAgent,
					runInBackground: runInBackground,
				}
			}
		}
	}
}

// captureAssistantText concatenates an assistant record's "text" blocks and,
// if the result is non-empty, replaces LastAssistantText with it
// (tail-truncated). A record with no text blocks (a pure tool_use turn)
// leaves the previous LastAssistantText in place rather than clearing it.
func (s *scanState) captureAssistantText(blocks []contentBlock) {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == contentBlockTypeText {
			sb.WriteString(b.Text)
		}
	}
	text := sb.String()
	if text == "" {
		return
	}
	s.lastAssistantText = truncateHead(text, maxAssistantTextLen)
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
//
// A tool_use_id is deleted from s.toolUses immediately once it successfully
// pairs to a launch: a given tool_use_id is produced once by the assistant
// and consumed at most once by its own matching tool_result (IDs never
// repeat), so the entry has no further use and holding onto it would just
// grow this map for the rest of the scan.
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
			if paired && info.name == toolNameBash && info.runInBackground {
				s.addLiveTask(m[1], TaskBash, timestamp, info, extractOutputFile(bashOutputFilePattern, text))
				delete(s.toolUses, block.ToolUseID)
			}
		}
		if m := agentIDPattern.FindStringSubmatch(text); m != nil {
			if paired && info.name == toolNameAgent && info.runInBackground &&
				strings.Contains(text, asyncAgentAckMarker) {
				s.addLiveTask(m[1], TaskAgent, timestamp, info, extractOutputFile(agentOutputFilePattern, text))
				delete(s.toolUses, block.ToolUseID)
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
		Detail:      info.detail, // already truncated at store time; see toolUseInfo.detail
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

// truncate returns s cut to at most maxLen bytes. The truncated branch
// returns strings.Clone(s[:maxLen]) rather than the bare substring: callers
// (processAssistantMessage's toolUseInfo.detail, and the goal-condition
// fallback text in watchdog.go) store the result long-term, and a plain
// substring slice would still alias — and therefore pin in memory — the
// entire source backing array (a full command or prompt) for as long as the
// truncated copy lives.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return strings.Clone(s[:maxLen])
}

// truncateHead mirrors truncate but keeps the END of s rather than the
// start, for text where the tail carries the meaningful content (an
// assistant turn's ask or conclusion is at the end, not the beginning).
func truncateHead(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[len(s)-maxLen:]
}

func parseTimestamp(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}
