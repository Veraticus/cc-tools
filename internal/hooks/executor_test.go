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

func TestRunSmartHook(t *testing.T) { //nolint:cyclop // table-driven test with many scenarios
	t.Run("exit code 0 when no input", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup no input
		testDeps.MockInput.isTerminalFunc = func() bool { return true }

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 0 when wrong event type", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input with wrong event
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{"hook_event_name": "PreToolUse", "tool_name": "Edit"}`), nil
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 0 when non-edit tool", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input with non-edit tool
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{"hook_event_name": "PostToolUse", "tool_name": "Bash"}`), nil
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 0 when file should be skipped", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input with test file
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main_test.go"}
			}`), nil
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 0 when lock cannot be acquired", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem for project detection
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") {
				return mockFileInfo{isDir: true}, nil
			}
			return nil, fmt.Errorf("not found")
		}

		// Setup lock already held
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(path string) ([]byte, error) {
			if strings.Contains(path, "lock") {
				return []byte("12345\n"), nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockProcess.processExistsFunc = func(pid int) bool {
			return pid == 12345 // Another process holds lock
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 0 when no command found", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") {
				return mockFileInfo{isDir: true}, nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		// Setup process
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool { return false }

		// Setup runner - no commands available
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
			return nil, fmt.Errorf("command not found")
		}
		testDeps.MockRunner.lookPathFunc = func(_ string) (string, error) {
			return "", fmt.Errorf("not found")
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("exit code 2 on lint failure", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") || strings.Contains(path, "Makefile") {
				return mockFileInfo{isDir: strings.Contains(path, ".git")}, nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		// Setup process
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool { return false }

		// Setup clock
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		// Setup runner - Makefile with lint target that fails
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && len(args) > 0 {
				if args[len(args)-1] == "lint" {
					if len(args) > 1 && args[len(args)-2] == "-n" {
						return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil // dry run succeeds
					}
					// Actual execution fails
					return &CommandOutput{Stderr: []byte("lint errors")}, &exec.ExitError{}
				}
			}
			return nil, fmt.Errorf("command not found")
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}

		// Check error message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "BLOCKING") {
			t.Errorf("Expected BLOCKING message, got: %s", output)
		}
		if !strings.Contains(output, "make lint") {
			t.Errorf("Expected 'make lint' in message, got: %s", output)
		}
	})

	t.Run("exit code 2 on lint success", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") || strings.Contains(path, "Makefile") {
				return mockFileInfo{isDir: strings.Contains(path, ".git")}, nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		// Setup process
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool { return false }

		// Setup clock
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		// Setup runner - Makefile with lint target that succeeds
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && len(args) > 0 && args[len(args)-1] == "lint" {
				return &CommandOutput{Stdout: []byte("lint passed")}, nil
			}
			return nil, fmt.Errorf("command not found")
		}

		exitCode := RunSmartHook(context.Background(), CommandTypeLint, false, 20, 5, testDeps.Dependencies)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}

		// Should show success message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "Lints pass") {
			t.Errorf("Expected 'Lints pass' message, got: %s", output)
		}
	})

	t.Run("exit code 2 on test success", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") || strings.Contains(path, "Makefile") {
				return mockFileInfo{isDir: strings.Contains(path, ".git")}, nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		// Setup process
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool { return false }

		// Setup clock
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		// Setup runner - Makefile with test target that succeeds
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && len(args) > 0 && args[len(args)-1] == "test" {
				return &CommandOutput{Stdout: []byte("test passed")}, nil
			}
			return nil, fmt.Errorf("command not found")
		}

		// Run test hook
		exitCode := RunSmartHook(
			context.Background(),
			CommandTypeTest,
			false, 20, 5,
			testDeps.Dependencies,
		)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}

		// Should show success message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "Tests pass") {
			t.Errorf("Expected 'Tests pass' message, got: %s", output)
		}
	})

	t.Run("command timeout", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup input
		testDeps.MockInput.isTerminalFunc = func() bool { return false }
		testDeps.MockInput.readAllFunc = func() ([]byte, error) {
			return []byte(`{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`), nil
		}

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, ".git") || strings.Contains(path, "Makefile") {
				return mockFileInfo{isDir: strings.Contains(path, ".git")}, nil
			}
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}

		// Setup process
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool { return false }

		// Setup clock
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		// Setup runner - command times out
		testDeps.MockRunner.runContextFunc = func(ctx context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && len(args) > 0 {
				if args[len(args)-1] == "lint" {
					if len(args) > 1 && args[len(args)-2] == "-n" {
						return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil // dry run succeeds
					}
					// Simulate timeout
					<-ctx.Done()
					return nil, context.DeadlineExceeded
				}
			}
			return nil, fmt.Errorf("command not found")
		}

		// Run with 1 second timeout
		exitCode := RunSmartHook(
			context.Background(),
			CommandTypeLint,
			false, 1, 5,
			testDeps.Dependencies,
		)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}

		// Check timeout message
		output := testDeps.MockStderr.String()
		if !strings.Contains(output, "timed out") {
			t.Errorf("Expected timeout message, got: %s", output)
		}
	})
}

func TestCommandExecutor(t *testing.T) {
	t.Run("successful command execution", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Mock successful command execution
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "echo" && len(args) == 1 && args[0] == "hello" {
				return &CommandOutput{Stdout: []byte("hello\n")}, nil
			}
			return nil, fmt.Errorf("unexpected command")
		}

		executor := NewCommandExecutor(5, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandTypeLint,
			Command:    "echo",
			Args:       []string{"hello"},
			WorkingDir: ".",
		}

		result := executor.Execute(context.Background(), cmd)
		if !result.Success {
			t.Errorf("Expected success, got error: %v", result.Error)
		}
		if result.ExitCode != 0 {
			t.Errorf("Expected exit code 0, got %d", result.ExitCode)
		}
		if !strings.Contains(result.Stdout, "hello") {
			t.Errorf("Expected output to contain 'hello', got: %s", result.Stdout)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Mock failed command execution
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, _ ...string) (*CommandOutput, error) {
			if name == "false" {
				// Simulate a command that exits with code 1
				exitErr := &exec.ExitError{}
				return &CommandOutput{}, exitErr
			}
			return nil, fmt.Errorf("unexpected command")
		}

		executor := NewCommandExecutor(5, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandTypeLint,
			Command:    "false",
			Args:       []string{},
			WorkingDir: ".",
		}

		result := executor.Execute(context.Background(), cmd)
		if result.Success {
			t.Error("Expected failure")
		}
		if result.ExitCode == 0 {
			t.Error("Expected non-zero exit code")
		}
	})

	t.Run("ExecuteForHook formats lint failure message", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Mock failed command execution
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
			exitErr := &exec.ExitError{}
			return &CommandOutput{Stderr: []byte("lint errors")}, exitErr
		}

		executor := NewCommandExecutor(5, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandTypeLint,
			Command:    "make",
			Args:       []string{"lint"},
			WorkingDir: "/project",
		}

		exitCode, message := executor.ExecuteForHook(context.Background(), cmd, CommandTypeLint)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}
		if !strings.Contains(message, "BLOCKING") {
			t.Errorf("Expected BLOCKING in message, got: %s", message)
		}
		if !strings.Contains(message, "make lint") {
			t.Errorf("Expected command in message, got: %s", message)
		}
	})

	t.Run("ExecuteForHook formats test failure message", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Mock failed command execution
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
			exitErr := &exec.ExitError{}
			return &CommandOutput{Stderr: []byte("test failures")}, exitErr
		}

		executor := NewCommandExecutor(5, false, testDeps.Dependencies)
		cmd := &DiscoveredCommand{
			Type:       CommandTypeTest,
			Command:    "go",
			Args:       []string{"test", "./..."},
			WorkingDir: "/project",
		}

		exitCode, message := executor.ExecuteForHook(context.Background(), cmd, CommandTypeTest)
		if exitCode != 2 {
			t.Errorf("Expected exit code 2, got %d", exitCode)
		}
		if !strings.Contains(message, "test failures") {
			t.Errorf("Expected 'test failures' in message, got: %s", message)
		}
	})
}

func TestValidateHookEvent(t *testing.T) {
	t.Run("valid PostToolUse Edit event", func(t *testing.T) {
		input := &HookInput{
			HookEventName: "PostToolUse",
			ToolName:      "Edit",
			ToolInput:     mustMarshalJSON(map[string]any{"file_path": "/project/main.go"}),
		}
		stderr := &mockOutputWriter{}

		filePath, shouldProcess := validateHookEvent(input, false, stderr)
		if !shouldProcess {
			t.Error("Expected event to be processed")
		}
		if filePath != "/project/main.go" {
			t.Errorf("Expected file path /project/main.go, got %s", filePath)
		}
	})

	t.Run("wrong event name", func(t *testing.T) {
		input := &HookInput{
			HookEventName: "PreToolUse",
			ToolName:      "Edit",
			ToolInput:     mustMarshalJSON(map[string]any{"file_path": "/project/main.go"}),
		}
		stderr := &mockOutputWriter{}

		_, shouldProcess := validateHookEvent(input, false, stderr)
		if shouldProcess {
			t.Error("Expected event not to be processed")
		}
	})

	t.Run("non-edit tool", func(t *testing.T) {
		input := &HookInput{
			HookEventName: "PostToolUse",
			ToolName:      "Bash",
		}
		stderr := &mockOutputWriter{}

		_, shouldProcess := validateHookEvent(input, false, stderr)
		if shouldProcess {
			t.Error("Expected event not to be processed")
		}
	})

	t.Run("missing file path", func(t *testing.T) {
		input := &HookInput{
			HookEventName: "PostToolUse",
			ToolName:      "Edit",
			ToolInput:     mustMarshalJSON(map[string]any{}),
		}
		stderr := &mockOutputWriter{}

		_, shouldProcess := validateHookEvent(input, false, stderr)
		if shouldProcess {
			t.Error("Expected event not to be processed")
		}
	})

	t.Run("nil input", func(t *testing.T) {
		stderr := &mockOutputWriter{}

		_, shouldProcess := validateHookEvent(nil, false, stderr)
		if shouldProcess {
			t.Error("Expected event not to be processed")
		}
	})

	t.Run("debug output", func(t *testing.T) {
		input := &HookInput{
			HookEventName: "PreToolUse",
			ToolName:      "Bash",
		}
		stderr := &mockOutputWriter{}

		validateHookEvent(input, true, stderr) // debug=true

		output := stderr.String()
		if !strings.Contains(output, "Ignoring event") {
			t.Errorf("Expected debug output, got: %s", output)
		}
	})
}
