// Package hooks provides input/output handling and command execution for Claude Code hooks.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNoInput is returned when no input is available on stdin.
var ErrNoInput = errors.New("no input available")

// HookInput represents the JSON input structure from Claude Code.
type HookInput struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
}

// StatusLineInput represents the JSON input for statusline.
type StatusLineInput struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	Model          ModelInfo       `json:"model"`
	Workspace      WorkspaceInfo   `json:"workspace"`
	Version        string          `json:"version"`
	OutputStyle    OutputStyleInfo `json:"output_style"`
	Cost           CostInfo        `json:"cost"`
}

// ModelInfo contains model information.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// WorkspaceInfo contains workspace directories.
type WorkspaceInfo struct {
	CurrentDir string `json:"current_dir"`
	ProjectDir string `json:"project_dir"`
}

// OutputStyleInfo contains output style configuration.
type OutputStyleInfo struct {
	Name string `json:"name"`
}

// CostInfo contains usage and cost metrics.
type CostInfo struct {
	TotalCostUSD       float64 `json:"total_cost_usd"`
	TotalDurationMS    int64   `json:"total_duration_ms"`
	TotalAPIDurationMS int64   `json:"total_api_duration_ms"`
	TotalLinesAdded    int     `json:"total_lines_added"`
	TotalLinesRemoved  int     `json:"total_lines_removed"`
}

// ReadHookInput reads and parses hook input from stdin.
func ReadHookInput() (*HookInput, error) {
	return ReadHookInputWithDeps(&stdinReader{})
}

// ReadHookInputWithDeps reads and parses hook input with explicit dependencies.
func ReadHookInputWithDeps(reader InputReader) (*HookInput, error) {
	// Check if stdin is available (not a terminal)
	if reader.IsTerminal() {
		// No stdin available
		return nil, ErrNoInput
	}

	data, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}

	if len(data) == 0 {
		return nil, ErrNoInput
	}

	var input HookInput
	if unmarshalErr := json.Unmarshal(data, &input); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing JSON: %w", unmarshalErr)
	}

	return &input, nil
}

// ReadStatusLineInput reads and parses statusline input from stdin.
func ReadStatusLineInput() (*StatusLineInput, error) {
	return ReadStatusLineInputWithDeps(&stdinReader{})
}

// ReadStatusLineInputWithDeps reads and parses statusline input with explicit dependencies.
func ReadStatusLineInputWithDeps(reader InputReader) (*StatusLineInput, error) {
	data, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}

	if len(data) == 0 {
		return nil, ErrNoInput
	}

	var input StatusLineInput
	if unmarshalErr := json.Unmarshal(data, &input); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing JSON: %w", unmarshalErr)
	}

	return &input, nil
}

// GetFilePath extracts the file path from tool input based on tool type.
func (h *HookInput) GetFilePath() string {
	if len(h.ToolInput) == 0 {
		return ""
	}

	// Parse the JSON to extract file path
	var toolInput map[string]any
	if err := json.Unmarshal(h.ToolInput, &toolInput); err != nil {
		return ""
	}

	// Handle NotebookEdit specially
	if h.ToolName == "NotebookEdit" {
		if path, ok := toolInput["notebook_path"].(string); ok {
			return path
		}
	}

	// Default to file_path for other tools
	if path, ok := toolInput["file_path"].(string); ok {
		return path
	}

	return ""
}

// IsEditTool returns true if this is an edit-related tool.
func (h *HookInput) IsEditTool() bool {
	switch h.ToolName {
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return true
	default:
		return false
	}
}
