package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Additional tests to cover remaining edge cases

func TestExecutorEdgeCases(t *testing.T) {
	t.Run("Execute with nil command", func(t *testing.T) {
		testDeps := createTestDependencies()
		executor := NewCommandExecutorWithDeps(5, false, testDeps.Dependencies)

		result := executor.Execute(nil)
		if result.Success {
			t.Error("Expected failure for nil command")
		}
		if result.Error == nil {
			t.Error("Expected error for nil command")
		}
	})

	t.Run("ExecuteForHook with timeout", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Mock command that times out
		testDeps.MockRunner.runContextFunc = func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, context.DeadlineExceeded
		}

		executor := NewCommandExecutorWithDeps(1, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandTypeLint,
			Command:    "sleep",
			Args:       []string{"10"},
			WorkingDir: "/project",
		}

		exitCode, message := executor.ExecuteForHook(cmd, CommandTypeLint)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}
		if !strings.Contains(message, "timed out") {
			t.Errorf("Expected timeout message, got: %s", message)
		}
	})

	t.Run("ExecuteForHook with unknown command type", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
			return nil, &exec.ExitError{}
		}

		executor := NewCommandExecutorWithDeps(5, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandType("unknown"),
			Command:    "test",
			Args:       []string{},
			WorkingDir: "/project",
		}

		exitCode, message := executor.ExecuteForHook(cmd, CommandType("unknown"))
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}
		if !strings.Contains(message, "BLOCKING") {
			t.Errorf("Expected BLOCKING message, got: %s", message)
		}
	})

	t.Run("ExecuteForHook success with debug", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
			return []byte("success"), nil
		}

		executor := NewCommandExecutorWithDeps(5, true, testDeps.Dependencies) // debug=true
		cmd := &DiscoveredCommand{
			Type:       CommandType("unknown"), // Use unknown type to test default case
			Command:    "echo",
			Args:       []string{"test"},
			WorkingDir: "/project",
		}

		exitCode, message := executor.ExecuteForHook(cmd, CommandType("unknown"))
		if exitCode != 2 {
			t.Errorf("Expected exit code 2 with debug, got %d", exitCode)
		}
		if !strings.Contains(message, "✓") {
			t.Errorf("Expected success message with checkmark, got: %s", message)
		}
	})
}

func TestDiscoveryEdgeCases(t *testing.T) {
	t.Run("checkPackageJSON with jq error", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "package.json") {
				return mockFileInfo{name: "package.json"}, nil
			}
			return nil, os.ErrNotExist
		}

		// jq fails
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
			return nil, fmt.Errorf("jq error")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(CommandTypeLint, "/project")

		if err == nil {
			t.Fatal("Expected error when package.json script not found")
		}
		if cmd != nil {
			t.Fatal("Expected no command when jq fails")
		}
	})

	t.Run("detects bun from lock file", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "package.json") || strings.HasSuffix(path, "bun.lockb") {
				return mockFileInfo{name: path}, nil
			}
			return nil, os.ErrNotExist
		}

		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, _ ...string) ([]byte, error) {
			if name == "jq" {
				return []byte(`"test script"`), nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd.Command != "bun" {
			t.Errorf("Expected bun, got %s", cmd.Command)
		}
	})

	t.Run("Python pylint fallback", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "setup.py") {
				return mockFileInfo{name: "setup.py"}, nil
			}
			return nil, os.ErrNotExist
		}

		testDeps.MockRunner.lookPathFunc = func(file string) (string, error) {
			if file == "pylint" {
				return "/usr/local/bin/pylint", nil
			}
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd.Command != "pylint" {
			t.Errorf("Expected pylint, got %s", cmd.Command)
		}
	})

	t.Run("no Python linters available", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "requirements.txt") {
				return mockFileInfo{name: "requirements.txt"}, nil
			}
			return nil, os.ErrNotExist
		}

		testDeps.MockRunner.lookPathFunc = func(_ string) (string, error) {
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(CommandTypeLint, "/project")

		// Should return nil when no linters are found
		if err == nil {
			t.Fatal("Expected error when no Python linters found")
		}
		if cmd != nil {
			t.Fatal("Expected no command when no linters available")
		}
	})

	t.Run("stops at filesystem root", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.statFunc = func(_ string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(CommandTypeLint, "/")

		if err == nil {
			t.Fatal("Expected error at filesystem root")
		}
		if cmd != nil {
			t.Fatal("Expected no command at filesystem root")
		}
	})
}

func TestRunSmartHookEdgeCases(t *testing.T) {
	t.Run("handles error finding project root", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/nonexistent/file.go"}
			}`), nil
		}

		// No .git directory found anywhere
		testDeps.MockFS.statFunc = func(_ string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}

		exitCode := RunSmartHookWithDeps(CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("handles lock acquisition error", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") {
				return mockFileInfo{isDir: true}, nil
			}
			return nil, os.ErrNotExist
		}

		// Lock file operations fail
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(path string) ([]byte, error) {
			if strings.Contains(path, "lock") {
				return nil, fmt.Errorf("permission denied")
			}
			return nil, os.ErrNotExist
		}
		testDeps.MockFS.writeFileFunc = func(path string, _ []byte, _ os.FileMode) error {
			if strings.Contains(path, "lock") {
				return fmt.Errorf("permission denied")
			}
			return nil
		}

		exitCode := RunSmartHookWithDeps(CommandTypeLint, true, 20, 5, testDeps.Dependencies) // debug=true
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("handles missing file path in input", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {}
			}`), nil
		}

		exitCode := RunSmartHookWithDeps(CommandTypeLint, true, 20, 5, testDeps.Dependencies) // debug=true
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}

		// Check debug message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "No file path") {
			t.Errorf("Expected 'No file path' in debug output, got: %s", output)
		}
	})

	t.Run("handles discovery error with debug", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") {
				return mockFileInfo{isDir: true}, nil
			}
			// All other files don't exist
			return nil, os.ErrNotExist
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, os.ErrNotExist
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }

		exitCode := RunSmartHookWithDeps(CommandTypeLint, true, 20, 5, testDeps.Dependencies) // debug=true
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}

		// Check debug message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "Error discovering command") {
			t.Errorf("Expected 'Error discovering command' in debug output, got: %s", output)
		}
	})
}

func TestHandleInputError(t *testing.T) {
	t.Run("logs non-ErrNoInput errors in debug mode", func(t *testing.T) {
		stderr := &mockOutputWriter{}
		err := fmt.Errorf("unexpected error")

		handleInputError(err, true, stderr)

		output := stderr.String()
		if !strings.Contains(output, "Error reading input") {
			t.Errorf("Expected error log in debug mode, got: %s", output)
		}
	})

	t.Run("silent for ErrNoInput even in debug mode", func(t *testing.T) {
		stderr := &mockOutputWriter{}

		handleInputError(ErrNoInput, true, stderr)

		output := stderr.String()
		if output != "" {
			t.Errorf("Expected no output for ErrNoInput, got: %s", output)
		}
	})

	t.Run("silent when not in debug mode", func(t *testing.T) {
		stderr := &mockOutputWriter{}
		err := fmt.Errorf("some error")

		handleInputError(err, false, stderr)

		output := stderr.String()
		if output != "" {
			t.Errorf("Expected no output when not in debug, got: %s", output)
		}
	})
}

func TestReadStatusLineInputEdgeCases(t *testing.T) {
	t.Run("handles read error", func(t *testing.T) {
		reader := &mockInputReader{
			readAllFunc: func() ([]byte, error) {
				return nil, fmt.Errorf("read error")
			},
		}

		input, err := ReadStatusLineInputWithDeps(reader)
		if err == nil {
			t.Fatal("Expected error for read failure")
		}
		if input != nil {
			t.Error("Expected nil input on error")
		}
		if !strings.Contains(err.Error(), "reading stdin") {
			t.Errorf("Expected 'reading stdin' in error, got: %v", err)
		}
	})
}

func TestLockManagerCleanupOnExit(t *testing.T) {
	t.Run("Release respects cleanupOnExit flag", func(t *testing.T) {
		testDeps := createTestDependencies()

		var writeCount int
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			writeCount++
			return nil
		}

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)
		lm.cleanupOnExit = false // Disable cleanup

		err := lm.Release()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if writeCount != 0 {
			t.Error("Expected no write when cleanupOnExit is false")
		}
	})

	t.Run("Release handles write error", func(t *testing.T) {
		testDeps := createTestDependencies()

		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return fmt.Errorf("disk full")
		}
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

		err := lm.Release()
		if err == nil {
			t.Fatal("Expected error on write failure")
		}
		if !strings.Contains(err.Error(), "disk full") {
			t.Errorf("Expected 'disk full' in error, got: %v", err)
		}
	})
}
