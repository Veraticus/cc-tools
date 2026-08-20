// Package subagentstatusline renders cc-tools' per-row chip chain for
// Claude Code's `subagentStatusLine` hook. Invoked once per tick by
// Claude with the full task array on stdin; emits one JSON line per
// task on stdout in the schema `{"id": string, "content": string}`.
package subagentstatusline

import (
	"encoding/json"
	"fmt"
	"io"
)

// maxInputBytes bounds how much we'll read from stdin. Claude's typical
// subagentStatusLine payload is a few KB even for 50 active rows;
// 1 MB is generous insurance against a misbehaving parent process
// driving memory growth.
const maxInputBytes = 1 << 20 // 1 MiB

// Input is the top-level JSON blob Claude pipes on stdin.
//
// Only the fields cc-tools renders against are listed; the JSON
// decoder silently drops everything else (`session_id`, `agent_id`,
// `transcript_path`, etc.).
type Input struct {
	// Columns is the agent view's terminal width at this tick. Used
	// for width-pressure decisions when assembling chip chains.
	Columns int `json:"columns"`

	// Tasks is the full list of active subagent rows. Parse normalizes
	// this to a non-nil empty slice when the key is missing or null
	// so downstream renderers can range over it unconditionally.
	Tasks []Task `json:"tasks"`
}

// Task is one row in the agent view. The JSON includes more fields
// than we use (`tokenSamples`, `startTime`, `label`, `description`);
// they're decoder-ignored to keep this struct focused on rendering
// inputs.
type Task struct {
	// ID matches the corresponding output Decoration.ID — Claude uses
	// it to line up decorations with rows.
	ID string `json:"id"`

	// Name is the user-assigned display name; null for unnamed agents.
	Name *string `json:"name"`

	// Type is one of "local_agent", "local_bash", "monitor_mcp",
	// "mcp_task", "local_workflow", "in_process_teammate",
	// "remote_agent", "dream" (per Claude 2.1.145).
	Type string `json:"type"`

	// Status is one of "running", "completed", "failed", "killed",
	// plus a few intermediate states.
	Status string `json:"status"`

	// Description is the agent's current prompt or summary.
	Description string `json:"description"`

	// Label is Claude's pre-computed short label; falls back to
	// Description if no override is set.
	Label string `json:"label"`

	// TokenCount is the running token total used for the context bar.
	TokenCount int `json:"tokenCount"`

	// Model is the model id or alias configured for this task
	// ("claude-fable-5[1m]", "chatgpt/sol", "sonnet"). Empty when
	// Claude didn't report one; the model chip is omitted then.
	Model string `json:"model"`

	// Effort is the reasoning-effort level for this task (low |
	// medium | high | xhigh | max). Rendered as a suffix inside the
	// model chip, mirroring the main statusline's "×H" convention.
	Effort EffortLevel `json:"effort"`

	// ContextWindowSize is Model's context window in tokens, as
	// computed by Claude per task. 0 when absent (Claude only sends
	// it when Model is set); the renderer then falls back to the
	// invocation-level context window.
	ContextWindowSize int `json:"contextWindowSize"`

	// CWD is the working directory of the agent. Used for the
	// directory chip and as the starting point for git branch lookup.
	CWD string `json:"cwd"`
}

// EffortLevel is a task's reasoning-effort level. Claude 2.1.234 sends
// it as a bare string; the unmarshaller also tolerates the main
// statusline's `{"level": "..."}` object shape, and maps anything else
// to "" rather than failing the whole payload — a malformed effort
// value should cost one suffix, not every row.
type EffortLevel string

// UnmarshalJSON accepts `"high"`, `{"level": "high"}`, or anything
// else (→ ""). Never returns an error by design; see EffortLevel.
func (e *EffortLevel) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*e = EffortLevel(s)
		return nil
	}
	var obj struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		*e = EffortLevel(obj.Level)
		return nil
	}
	*e = ""
	return nil
}

// Parse reads a subagentStatusLine JSON blob and decodes it into an
// Input. Tasks is normalized to a non-nil empty slice if the key is
// missing or null. Errors are wrapped with `parse:` for easy
// identification at the caller.
//
// Streams through json.Decoder rather than ReadAll-then-Unmarshal so
// peak memory is one parsed Input, not raw bytes + parsed struct.
// Bounded by maxInputBytes via io.LimitReader so a misbehaving parent
// can't drive the daemon into the OOM killer.
func Parse(r io.Reader) (*Input, error) {
	dec := json.NewDecoder(io.LimitReader(r, maxInputBytes))
	var in Input
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if in.Tasks == nil {
		in.Tasks = []Task{}
	}
	return &in, nil
}
