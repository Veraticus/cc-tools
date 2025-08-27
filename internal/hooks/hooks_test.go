package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Veraticus/cc-tools/internal/shared"
)

// TestHookInputParsing tests parsing of hook input JSON.
func TestHookInputParsing(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		expectEvent string
		expectTool  string
	}{
		{
			name: "valid PostToolUse Edit input",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {
					"file_path": "/path/to/file.go"
				}
			}`,
			expectError: false,
			expectEvent: "PostToolUse",
			expectTool:  "Edit",
		},
		{
			name: "valid PostToolUse MultiEdit input",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "MultiEdit",
				"tool_input": {
					"file_path": "/path/to/file.py"
				}
			}`,
			expectError: false,
			expectEvent: "PostToolUse",
			expectTool:  "MultiEdit",
		},
		{
			name:        "invalid JSON",
			input:       `{invalid json}`,
			expectError: true,
		},
		{
			name:        "empty input",
			input:       ``,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input HookInput
			err := json.Unmarshal([]byte(tt.input), &input)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if input.HookEventName != tt.expectEvent {
				t.Errorf("HookEventName = %v, want %v", input.HookEventName, tt.expectEvent)
			}

			if input.ToolName != tt.expectTool {
				t.Errorf("ToolName = %v, want %v", input.ToolName, tt.expectTool)
			}
		})
	}
}

// TestGetFilePathOld tests extracting file path from tool input.
func TestGetFilePathOld(t *testing.T) {
	tests := []struct {
		name       string
		input      *HookInput
		expectPath string
	}{
		{
			name: "Edit tool file path",
			input: &HookInput{
				ToolName: "Edit",
				ToolInput: mustMarshalJSON(map[string]any{
					"file_path": "/path/to/file.go",
				}),
			},
			expectPath: "/path/to/file.go",
		},
		{
			name: "NotebookEdit tool notebook path",
			input: &HookInput{
				ToolName: "NotebookEdit",
				ToolInput: mustMarshalJSON(map[string]any{
					"notebook_path": "/path/to/notebook.ipynb",
				}),
			},
			expectPath: "/path/to/notebook.ipynb",
		},
		{
			name: "nil tool input",
			input: &HookInput{
				ToolName:  "Edit",
				ToolInput: nil,
			},
			expectPath: "",
		},
		{
			name: "empty file paths",
			input: &HookInput{
				ToolName:  "Edit",
				ToolInput: mustMarshalJSON(map[string]any{}),
			},
			expectPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.input.GetFilePath()
			if path != tt.expectPath {
				t.Errorf("GetFilePath() = %v, want %v", path, tt.expectPath)
			}
		})
	}
}

// TestIsEditToolOld tests the logic for determining if a tool is an edit tool.
func TestIsEditToolOld(t *testing.T) {
	tests := []struct {
		name       string
		input      *HookInput
		expectEdit bool
	}{
		{
			name: "Edit is an edit tool",
			input: &HookInput{
				ToolName: "Edit",
			},
			expectEdit: true,
		},
		{
			name: "MultiEdit is an edit tool",
			input: &HookInput{
				ToolName: "MultiEdit",
			},
			expectEdit: true,
		},
		{
			name: "Write is an edit tool",
			input: &HookInput{
				ToolName: "Write",
			},
			expectEdit: true,
		},
		{
			name: "NotebookEdit is an edit tool",
			input: &HookInput{
				ToolName: "NotebookEdit",
			},
			expectEdit: true,
		},
		{
			name: "Bash is not an edit tool",
			input: &HookInput{
				ToolName: "Bash",
			},
			expectEdit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isEdit := tt.input.IsEditTool()
			if isEdit != tt.expectEdit {
				t.Errorf("IsEditTool() = %v, want %v", isEdit, tt.expectEdit)
			}
		})
	}
}

// TestLockManager tests lock acquisition and release.
func TestLockManager(t *testing.T) {
	tmpDir := t.TempDir()

	// Override temp dir for testing
	t.Setenv("TMPDIR", tmpDir)

	t.Run("acquire and release lock", func(t *testing.T) {
		lm := NewLockManager("/test/project", "test", 2, nil)

		// Should acquire lock successfully
		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Error acquiring lock: %v", err)
		}
		if !acquired {
			t.Fatal("Failed to acquire lock")
		}

		// Release lock
		if releaseErr := lm.Release(); releaseErr != nil {
			t.Errorf("Error releasing lock: %v", releaseErr)
		}

		// Check that lock file exists
		if _, statErr := os.Stat(lm.lockFile); os.IsNotExist(statErr) {
			t.Error("Lock file should exist after release")
		}
	})

	t.Run("respects cooldown", func(t *testing.T) {
		lm1 := NewLockManager("/test/project", "cooldown", 2, nil)

		// First process acquires and releases
		acquired, err := lm1.TryAcquire()
		if err != nil || !acquired {
			t.Fatal("Failed to acquire first lock")
		}
		lm1.Release()

		// Second process tries immediately
		lm2 := NewLockManager("/test/project", "cooldown", 2, nil)
		acquired, _ = lm2.TryAcquire()
		if acquired {
			t.Error("Should not acquire lock during cooldown")
		}

		// Wait for cooldown
		time.Sleep(3 * time.Second)

		// Now should acquire
		acquired, _ = lm2.TryAcquire()
		if !acquired {
			t.Error("Should acquire lock after cooldown")
		}
	})
}

// TestDiscoveredCommandStringOld tests String method.
func TestDiscoveredCommandStringOld(t *testing.T) {
	tests := []struct {
		name     string
		cmd      *DiscoveredCommand
		expected string
	}{
		{
			name:     "nil command",
			cmd:      nil,
			expected: "",
		},
		{
			name: "command without args",
			cmd: &DiscoveredCommand{
				Command: "make",
				Args:    []string{},
			},
			expected: "make",
		},
		{
			name: "command with single arg",
			cmd: &DiscoveredCommand{
				Command: "make",
				Args:    []string{"lint"},
			},
			expected: "make lint",
		},
		{
			name: "command with multiple args",
			cmd: &DiscoveredCommand{
				Command: "cargo",
				Args:    []string{"clippy", "--", "-D", "warnings"},
			},
			expected: "cargo clippy -- -D warnings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cmd.String()
			if result != tt.expected {
				t.Errorf("String() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestCommandExecutorBasic tests basic command execution.
func TestCommandExecutorBasic(t *testing.T) {
	t.Run("execute simple command", func(t *testing.T) {
		executor := NewCommandExecutor(5, false, nil)

		cmd := &DiscoveredCommand{
			Type:       CommandTypeTest,
			Command:    "echo",
			Args:       []string{"hello"},
			WorkingDir: ".",
		}

		result := executor.Execute(context.Background(), cmd)
		if !result.Success {
			t.Errorf("Expected success, got error: %v", result.Error)
		}
	})

	t.Run("handle command timeout", func(t *testing.T) {
		executor := NewCommandExecutor(1, false, nil) // 1 second timeout

		cmd := &DiscoveredCommand{
			Type:       CommandTypeTest,
			Command:    "sleep",
			Args:       []string{"2"}, // Sleep longer than timeout
			WorkingDir: ".",
		}

		result := executor.Execute(context.Background(), cmd)
		if result.Success {
			t.Error("Expected timeout failure")
		}
		if !result.TimedOut {
			t.Error("Expected TimedOut flag to be true")
		}
	})

	t.Run("handle non-existent command", func(t *testing.T) {
		executor := NewCommandExecutor(5, false, nil)

		cmd := &DiscoveredCommand{
			Type:       CommandTypeTest,
			Command:    "nonexistentcommand12345",
			Args:       []string{},
			WorkingDir: ".",
		}

		result := executor.Execute(context.Background(), cmd)
		if result.Success {
			t.Error("Expected failure for non-existent command")
		}
	})
}

// TestRunSmartHookBasic tests the main hook runner function.
func TestRunSmartHookBasic(t *testing.T) {
	t.Run("exits early when disabled", func(t *testing.T) {
		t.Setenv("CLAUDE_HOOKS_LINT_ENABLED", "false")

		code := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 2, nil)
		if code != 0 {
			t.Errorf("Expected exit code 0 when disabled, got %d", code)
		}
	})
}

// TestDiscoveryIntegration tests discovery with real filesystem.
func TestDiscoveryIntegration(t *testing.T) {
	// Create a temporary project structure
	tmpDir := t.TempDir()

	// Create a Makefile with lint target
	makefileContent := `
lint:
	@echo "Running lint"

test:
	@echo "Running tests"
`
	makefilePath := filepath.Join(tmpDir, "Makefile")
	if err := os.WriteFile(makefilePath, []byte(makefileContent), 0644); err != nil {
		t.Fatalf("Failed to create Makefile: %v", err)
	}

	t.Run("discover Makefile targets", func(t *testing.T) {
		discovery := NewCommandDiscovery(tmpDir, 20, nil)

		// Test lint discovery
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, tmpDir)
		if err != nil {
			t.Errorf("Failed to discover lint command: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find lint command")
		}
		if cmd.Command != "make" || len(cmd.Args) != 1 || cmd.Args[0] != "lint" {
			t.Errorf("Unexpected command: %v", cmd.String())
		}

		// Test test discovery
		cmd, err = discovery.DiscoverCommand(context.Background(), CommandTypeTest, tmpDir)
		if err != nil {
			t.Errorf("Failed to discover test command: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find test command")
		}
		if cmd.Command != "make" || len(cmd.Args) != 1 || cmd.Args[0] != "test" {
			t.Errorf("Unexpected command: %v", cmd.String())
		}
	})
}

// TestFileSkipping tests the file skip logic.
func TestFileSkipping(t *testing.T) {
	tests := []struct {
		name       string
		filePath   string
		shouldSkip bool
	}{
		{
			name:       "skip vendor directory",
			filePath:   "/project/vendor/github.com/pkg/file.go",
			shouldSkip: true,
		},
		{
			name:       "skip node_modules",
			filePath:   "/project/node_modules/package/index.js",
			shouldSkip: true,
		},
		{
			name:       "skip test files",
			filePath:   "/project/main_test.go",
			shouldSkip: true,
		},
		{
			name:       "skip generated files",
			filePath:   "/project/api.generated.go",
			shouldSkip: true,
		},
		{
			name:       "process regular files",
			filePath:   "/project/main.go",
			shouldSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldSkip := shared.ShouldSkipFile(tt.filePath)
			if shouldSkip != tt.shouldSkip {
				t.Errorf("ShouldSkipFile(%s) = %v, want %v", tt.filePath, shouldSkip, tt.shouldSkip)
			}
		})
	}
}

// BenchmarkDiscovery benchmarks command discovery.
func BenchmarkDiscovery(b *testing.B) {
	tmpDir := b.TempDir()

	// Create test Makefile
	makefileContent := `lint:
	@echo "lint"
`
	os.WriteFile(filepath.Join(tmpDir, "Makefile"), []byte(makefileContent), 0644)

	discovery := NewCommandDiscovery(tmpDir, 20, nil)

	b.ResetTimer()
	for range b.N {
		discovery.DiscoverCommand(context.Background(), CommandTypeLint, tmpDir)
	}
}

// BenchmarkLockManager benchmarks lock operations.
func BenchmarkLockManager(b *testing.B) {
	tmpDir := b.TempDir()
	b.Setenv("TMPDIR", tmpDir)

	b.ResetTimer()
	for i := range b.N {
		lm := NewLockManager(fmt.Sprintf("/project%d", i), "bench", 0, nil)
		lm.TryAcquire()
		lm.Release()
	}
}
