package hooks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Veraticus/cc-tools/internal/shared"
)

const (
	// ExitCodeShowMessage is used to signal that a message should be shown to Claude.
	ExitCodeShowMessage = 2
)

// ExecutorResult represents the result of executing a command.
type ExecutorResult struct {
	Success  bool
	ExitCode int
	Stdout   string
	Stderr   string
	Error    error
	TimedOut bool
}

// CommandExecutor handles executing discovered commands.
type CommandExecutor struct {
	timeout time.Duration
	debug   bool
	deps    *Dependencies
}

// NewCommandExecutor creates a new command executor.
func NewCommandExecutor(timeoutSecs int, debug bool, deps *Dependencies) *CommandExecutor {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &CommandExecutor{
		timeout: time.Duration(timeoutSecs) * time.Second,
		debug:   debug,
		deps:    deps,
	}
}

// Execute runs the discovered command with the given context and timeout.
func (ce *CommandExecutor) Execute(ctx context.Context, cmd *DiscoveredCommand) *ExecutorResult {
	if cmd == nil {
		return &ExecutorResult{
			Success: false,
			Error:   fmt.Errorf("no command to execute"),
		}
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, ce.timeout)
	defer cancel()

	// Run the command through dependencies
	output, err := ce.deps.Runner.RunContext(ctx, cmd.WorkingDir, cmd.Command, cmd.Args...)

	// Check if context timed out
	if ctx.Err() == context.DeadlineExceeded {
		var stdout, stderr string
		if output != nil {
			stdout = string(output.Stdout)
			stderr = string(output.Stderr)
		}
		return &ExecutorResult{
			Success:  false,
			ExitCode: -1,
			Stdout:   stdout,
			Stderr:   stderr,
			Error:    fmt.Errorf("command timed out after %v", ce.timeout),
			TimedOut: true,
		}
	}

	// Get exit code
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	var stdout, stderr string
	if output != nil {
		stdout = string(output.Stdout)
		stderr = string(output.Stderr)
	}

	return &ExecutorResult{
		Success:  err == nil,
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
		Error:    err,
		TimedOut: false,
	}
}

// ExecuteForHook executes a command with context and formats output for hook response.
func (ce *CommandExecutor) ExecuteForHook(
	ctx context.Context,
	cmd *DiscoveredCommand,
	hookType CommandType,
) (int, string) {
	result := ce.Execute(ctx, cmd)

	if result.TimedOut {
		message := shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Command timed out after %v", ce.timeout))
		return ExitCodeShowMessage, message
	}

	if result.Success {
		// Command succeeded - always show success message
		var message string
		switch hookType {
		case CommandTypeLint:
			message = shared.RawWarningStyle.Render("👉 Lints pass. Continue with your task.")
		case CommandTypeTest:
			message = shared.RawWarningStyle.Render("👉 Tests pass. Continue with your task.")
		default:
			message = shared.RawSuccessStyle.Render("✓ Command succeeded")
		}
		return ExitCodeShowMessage, message
	}

	// Command failed - format error message
	cmdStr := cmd.String()
	var message string
	switch hookType {
	case CommandTypeLint:
		message = shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix lint failures",
				cmd.WorkingDir, cmdStr))
	case CommandTypeTest:
		message = shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix test failures",
				cmd.WorkingDir, cmdStr))
	default:
		message = shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Command failed: %s", cmdStr))
	}

	return ExitCodeShowMessage, message
}

// RunSmartHook is the main entry point for smart-lint and smart-test hooks.
func RunSmartHook(
	ctx context.Context,
	hookType CommandType,
	debug bool,
	timeoutSecs int,
	cooldownSecs int,
	deps *Dependencies,
) int {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	// Read and validate input
	input, err := ReadHookInput(deps.Input)
	if err != nil {
		handleInputError(err, debug, deps.Stderr)
		return 0
	}

	// Validate event and get file path
	filePath, shouldProcess := validateHookEvent(input, debug, deps.Stderr)
	if !shouldProcess {
		return 0
	}

	// Check if file should be skipped
	if shared.ShouldSkipFile(filePath) {
		// Only output in debug mode when CLAUDE_HOOKS_DEBUG is set
		// This matches bash behavior where log_debug checks the env var
		return 0
	}

	// Find project root
	fileDir := filepath.Dir(filePath)
	projectRoot, err := shared.FindProjectRoot(fileDir, nil)
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error finding project root: %v\n", err)
		}
		return 0
	}

	// Acquire lock
	lockMgr := NewLockManager(projectRoot, string(hookType), cooldownSecs, deps)
	if !acquireLock(lockMgr, debug, deps.Stderr) {
		return 0
	}
	defer func() {
		_ = lockMgr.Release()
	}()

	// Discover and execute command
	return discoverAndExecute(ctx, projectRoot, fileDir, hookType, timeoutSecs, debug, deps)
}

// handleInputError handles errors from reading hook input.
func handleInputError(err error, debug bool, stderr OutputWriter) {
	if !errors.Is(err, ErrNoInput) && debug {
		// Only log if it's not the expected "no input" error
		_, _ = fmt.Fprintf(stderr, "Error reading input: %v\n", err)
	}
}

// validateHookEvent checks if the event should be processed.
func validateHookEvent(input *HookInput, debug bool, stderr OutputWriter) (string, bool) {
	if input == nil || input.HookEventName != "PostToolUse" || !input.IsEditTool() {
		if debug && input != nil {
			_, _ = fmt.Fprintf(stderr, "Ignoring event: %s, tool: %s\n",
				input.HookEventName, input.ToolName)
		}
		return "", false
	}

	filePath := input.GetFilePath()
	if filePath == "" {
		if debug {
			_, _ = fmt.Fprintf(stderr, "No file path found in input\n")
		}
		return "", false
	}

	return filePath, true
}

// acquireLock tries to acquire the lock for the hook.
func acquireLock(lockMgr *LockManager, debug bool, stderr OutputWriter) bool {
	acquired, err := lockMgr.TryAcquire()
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(stderr, "Error acquiring lock: %v\n", err)
		}
		return false
	}
	if !acquired {
		if debug {
			_, _ = fmt.Fprintf(stderr, "Another instance is running or in cooldown\n")
		}
		return false
	}
	return true
}

// discoverAndExecute discovers and executes the appropriate command.
func discoverAndExecute(
	ctx context.Context,
	projectRoot, fileDir string,
	hookType CommandType,
	timeoutSecs int,
	debug bool,
	deps *Dependencies,
) int {
	// Discover command
	discovery := NewCommandDiscovery(projectRoot, timeoutSecs, deps)
	cmd, err := discovery.DiscoverCommand(ctx, hookType, fileDir)
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error discovering command: %v\n", err)
		}
		return 0
	}
	if cmd == nil {
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "No %s command found\n", hookType)
		}
		return 0
	}

	// Execute command
	executor := NewCommandExecutor(timeoutSecs, debug, deps)
	exitCode, message := executor.ExecuteForHook(ctx, cmd, hookType)

	if message != "" {
		_, _ = fmt.Fprintln(deps.Stderr, message)
	}

	return exitCode
}
