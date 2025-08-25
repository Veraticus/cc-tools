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

// ExecutorResult represents the result of executing a command.
type ExecutorResult struct {
	Success  bool
	ExitCode int
	Output   string
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
func NewCommandExecutor(timeoutSecs int, debug bool) *CommandExecutor {
	return NewCommandExecutorWithDeps(timeoutSecs, debug, nil)
}

// NewCommandExecutorWithDeps creates a new command executor with explicit dependencies.
func NewCommandExecutorWithDeps(timeoutSecs int, debug bool, deps *Dependencies) *CommandExecutor {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &CommandExecutor{
		timeout: time.Duration(timeoutSecs) * time.Second,
		debug:   debug,
		deps:    deps,
	}
}

// Execute runs the discovered command with timeout.
func (ce *CommandExecutor) Execute(cmd *DiscoveredCommand) *ExecutorResult {
	if cmd == nil {
		return &ExecutorResult{
			Success: false,
			Error:   fmt.Errorf("no command to execute"),
		}
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), ce.timeout)
	defer cancel()

	// Run the command through dependencies
	output, err := ce.deps.Runner.RunContext(ctx, cmd.WorkingDir, cmd.Command, cmd.Args...)

	// Check if context timed out
	if ctx.Err() == context.DeadlineExceeded {
		return &ExecutorResult{
			Success:  false,
			ExitCode: -1,
			Output:   string(output),
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

	return &ExecutorResult{
		Success:  err == nil,
		ExitCode: exitCode,
		Output:   string(output),
		Error:    err,
		TimedOut: false,
	}
}

// ExecuteForHook executes a command and formats output for hook response.
func (ce *CommandExecutor) ExecuteForHook(cmd *DiscoveredCommand, hookType CommandType) (int, string) {
	result := ce.Execute(cmd)

	if result.TimedOut {
		message := shared.ErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Command timed out after %v", ce.timeout))
		return 2, message //nolint:mnd // Exit code 2 signals blocking error to Claude
	}

	if result.Success {
		// Command succeeded
		if ce.debug {
			var message string
			switch hookType {
			case CommandTypeLint:
				message = shared.WarningStyle.Render("👉 Lints pass. Continue with your task.")
			case CommandTypeTest:
				message = shared.WarningStyle.Render("👉 Tests pass. Continue with your task.")
			default:
				message = shared.SuccessStyle.Render("✓ Command succeeded")
			}
			return 2, message //nolint:mnd // Exit code 2 shows message to Claude
		}
		return 0, "" // Silent success when not in debug mode
	}

	// Command failed - format error message
	cmdStr := cmd.String()
	var message string
	switch hookType {
	case CommandTypeLint:
		message = shared.ErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix lint failures",
				cmd.WorkingDir, cmdStr))
	case CommandTypeTest:
		message = shared.ErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix test failures",
				cmd.WorkingDir, cmdStr))
	default:
		message = shared.ErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Command failed: %s", cmdStr))
	}

	return 2, message //nolint:mnd // Exit code 2 signals blocking error to Claude
}

// RunSmartHook is the main entry point for smart-lint and smart-test hooks.
func RunSmartHook(hookType CommandType, debug bool, timeoutSecs int, cooldownSecs int) int {
	return RunSmartHookWithDeps(hookType, debug, timeoutSecs, cooldownSecs, nil)
}

// RunSmartHookWithDeps runs the smart hook with explicit dependencies.
func RunSmartHookWithDeps(hookType CommandType, debug bool, timeoutSecs int, cooldownSecs int, deps *Dependencies) int {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	// Read and validate input
	input, err := ReadHookInputWithDeps(deps.Input)
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
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Skipping file: %s\n", filePath)
		}
		return 0
	}

	// Find project root
	fileDir := filepath.Dir(filePath)
	projectRoot, err := shared.FindProjectRoot(fileDir)
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error finding project root: %v\n", err)
		}
		return 0
	}

	// Acquire lock
	lockMgr := NewLockManagerWithDeps(projectRoot, string(hookType), cooldownSecs, deps)
	if !acquireLock(lockMgr, debug, deps.Stderr) {
		return 0
	}
	defer func() {
		_ = lockMgr.Release()
	}()

	// Discover and execute command
	return discoverAndExecute(projectRoot, fileDir, hookType, timeoutSecs, debug, deps)
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
	projectRoot, fileDir string,
	hookType CommandType,
	timeoutSecs int,
	debug bool,
	deps *Dependencies,
) int {
	// Discover command
	discovery := NewCommandDiscovery(projectRoot, timeoutSecs, deps)
	cmd, err := discovery.DiscoverCommand(hookType, fileDir)
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
	executor := NewCommandExecutorWithDeps(timeoutSecs, debug, deps)
	exitCode, message := executor.ExecuteForHook(cmd, hookType)

	if message != "" {
		_, _ = fmt.Fprintln(deps.Stderr, message)
	}

	return exitCode
}
