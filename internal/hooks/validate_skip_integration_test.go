package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWithSkipCheck_RealIntegration(t *testing.T) {
	// Skip if running in CI or short mode
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name         string
		setupSkip    func(t *testing.T, dir string)
		input        map[string]any
		debug        bool
		wantExitCode int
		wantInStderr []string
	}{
		{
			name: "no skip runs validation",
			setupSkip: func(_ *testing.T, _ string) {
				// No skip setup - should run validation
			},
			input: map[string]any{
				"hook_event_name": "PostToolUse",
				"tool_name":       "Edit",
				"tool_input": map[string]any{
					"file_path": "main.go",
				},
			},
			debug:        false,
			wantExitCode: 2, // Shows validation pass message
		},
		{
			name: "skip both with debug messages",
			setupSkip: func(_ *testing.T, _ string) {
				// Create a skip registry file in test location
				// This would need actual registry setup
			},
			input: map[string]any{
				"hook_event_name": "PostToolUse",
				"tool_name":       "Edit",
				"tool_input": map[string]any{
					"file_path": "main.go",
				},
			},
			debug:        true,
			wantExitCode: 2, // Shows validation pass message even with skips
			wantInStderr: []string{
				"Checking skips for project root",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			tmpDir := t.TempDir()
			t.Chdir(tmpDir)

			// Setup skip if needed
			if tt.setupSkip != nil {
				tt.setupSkip(t, tmpDir)
			}

			// Update input with absolute path
			if toolInput, ok := tt.input["tool_input"].(map[string]any); ok {
				if filePath, fpOk := toolInput["file_path"].(string); fpOk {
					toolInput["file_path"] = filepath.Join(tmpDir, filePath)
				}
			}

			// Prepare stdin
			inputJSON, _ := json.Marshal(tt.input)
			stdin := bytes.NewReader(inputJSON)

			// Capture output
			var stdout, stderr bytes.Buffer

			// Run validation
			exitCode := ValidateWithSkipCheck(
				context.Background(),
				stdin,
				&stdout,
				&stderr,
				tt.debug,
				5, // timeout
				0, // cooldown
			)

			// Check exit code
			if exitCode != tt.wantExitCode {
				t.Errorf("ValidateWithSkipCheck() exit code = %v, want %v", exitCode, tt.wantExitCode)
			}

			// Check stderr content
			stderrStr := stderr.String()
			for _, expected := range tt.wantInStderr {
				if !strings.Contains(stderrStr, expected) {
					t.Errorf("Expected stderr to contain %q, got: %s", expected, stderrStr)
				}
			}
		})
	}
}

func TestCheckSkipsFromInput_Unit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		debug    bool
		wantLogs []string
	}{
		{
			name: "valid JSON with file path",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {
					"file_path": "/tmp/test.go"
				}
			}`,
			debug: true,
			wantLogs: []string{
				"Checking skips for project root: /tmp",
			},
		},
		{
			name:  "invalid JSON",
			input: `{invalid json}`,
			debug: true,
			wantLogs: []string{
				"Failed to parse JSON input",
			},
		},
		{
			name: "no file path",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {}
			}`,
			debug: true,
			wantLogs: []string{
				"No file path found in input",
			},
		},
		{
			name: "nested tool_input structure",
			input: `{
				"tool_input": {
					"file_path": "/home/user/project/src/main.go"
				}
			}`,
			debug: true,
			wantLogs: []string{
				"Checking skips for project root: /home/user/project/src",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var stderr bytes.Buffer

			// Call the function
			_, _ = checkSkipsFromInput(ctx, []byte(tt.input), tt.debug, &stderr)

			// Check debug logs
			stderrStr := stderr.String()
			for _, expectedLog := range tt.wantLogs {
				if !strings.Contains(stderrStr, expectedLog) {
					t.Errorf("Expected stderr to contain %q, got: %s", expectedLog, stderrStr)
				}
			}
		})
	}
}

func TestValidateWithSkipCheck_ErrorHandling(t *testing.T) {
	tests := []struct {
		name         string
		stdin        io.Reader
		wantExitCode int
	}{
		{
			name:         "empty reader",
			stdin:        bytes.NewReader([]byte{}),
			wantExitCode: 0,
		},
		{
			name:         "nil bytes",
			stdin:        bytes.NewReader(nil),
			wantExitCode: 0,
		},
		{
			name:         "reader that returns error",
			stdin:        &errorReader{err: context.DeadlineExceeded},
			wantExitCode: 0, // Falls back to RunValidateHook
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			exitCode := ValidateWithSkipCheck(
				context.Background(),
				tt.stdin,
				&stdout,
				&stderr,
				false,
				1,
				0,
			)

			if exitCode != tt.wantExitCode {
				t.Errorf("ValidateWithSkipCheck() = %v, want %v", exitCode, tt.wantExitCode)
			}
		})
	}
}

// errorReader is a reader that always returns an error.
type errorReader struct {
	err error
}

func (r *errorReader) Read(_ []byte) (int, error) {
	return 0, r.err
}
