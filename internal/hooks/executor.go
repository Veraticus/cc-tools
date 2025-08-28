package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	debuglog "github.com/Veraticus/cc-tools/internal/debug"
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

// initLogger initializes the debug logger.
func initLogger(ctx context.Context) *debuglog.Logger {
	cwd, _ := os.Getwd()
	logger, _ := debuglog.NewLogger(ctx, cwd)
	return logger
}

// logHookStart logs the start of a hook execution.
func logHookStart(logger *debuglog.Logger, hookType CommandType, timeoutSecs, cooldownSecs int) {
	if logger == nil || !logger.IsEnabled() {
		return
	}
	cwd, _ := os.Getwd()
	logger.LogSection(fmt.Sprintf("Starting %s hook", hookType))
	logger.Log("Working directory: %s", cwd)
	logger.Log("Timeout: %d seconds", timeoutSecs)
	logger.Log("Cooldown: %d seconds", cooldownSecs)
}

// processHookInput reads and validates the hook input.
func processHookInput(deps *Dependencies, logger *debuglog.Logger, debug bool) (*HookInput, string, bool) {
	input, err := ReadHookInput(deps.Input)
	if err != nil {
		if logger != nil && logger.IsEnabled() {
			logger.LogError(err, "reading hook input")
		}
		handleInputError(err, debug, deps.Stderr)
		return nil, "", false
	}

	if logger != nil && logger.IsEnabled() && input != nil {
		logger.Log("Hook event: %s", input.HookEventName)
		logger.Log("Tool name: %s", input.ToolName)
	}

	filePath, shouldProcess := validateHookEvent(input, debug, deps.Stderr)
	if !shouldProcess {
		if logger != nil && logger.IsEnabled() {
			logger.Log("Event validation failed, not processing")
		}
		return input, "", false
	}

	if logger != nil && logger.IsEnabled() {
		logger.Log("Processing file: %s", filePath)
	}

	return input, filePath, true
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

	logger := initLogger(ctx)
	defer func() {
		if logger != nil {
			_ = logger.Close()
		}
	}()

	logHookStart(logger, hookType, timeoutSecs, cooldownSecs)

	// Process input
	_, filePath, shouldProcess := processHookInput(deps, logger, debug)
	if !shouldProcess {
		return 0
	}

	// Check if file should be skipped
	if shared.ShouldSkipFile(filePath) {
		if logger != nil && logger.IsEnabled() {
			logger.Log("File skipped by filter: %s", filePath)
		}
		return 0
	}

	// Find project root
	fileDir := filepath.Dir(filePath)
	projectRoot, err := shared.FindProjectRoot(fileDir, nil)
	if err != nil {
		if logger != nil && logger.IsEnabled() {
			logger.LogError(err, "finding project root")
		}
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error finding project root: %v\n", err)
		}
		return 0
	}

	if logger != nil && logger.IsEnabled() {
		logger.Log("Project root: %s", projectRoot)
	}

	// Acquire lock
	lockMgr := NewLockManager(projectRoot, string(hookType), cooldownSecs, deps)
	if !acquireLock(lockMgr, debug, deps.Stderr, logger) {
		return 0
	}
	defer func() {
		_ = lockMgr.Release()
	}()

	// Discover and execute command
	return discoverAndExecute(ctx, projectRoot, fileDir, hookType, timeoutSecs, debug, deps, logger)
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
func acquireLock(lockMgr *LockManager, debug bool, stderr OutputWriter, logger *debuglog.Logger) bool {
	acquired, err := lockMgr.TryAcquire()
	if err != nil {
		if logger != nil && logger.IsEnabled() {
			logger.LogError(err, "acquiring lock")
		}
		if debug {
			_, _ = fmt.Fprintf(stderr, "Error acquiring lock: %v\n", err)
		}
		return false
	}
	if !acquired {
		if logger != nil && logger.IsEnabled() {
			logger.Log("Another instance is running or in cooldown")
		}
		if debug {
			_, _ = fmt.Fprintf(stderr, "Another instance is running or in cooldown\n")
		}
		return false
	}
	if logger != nil && logger.IsEnabled() {
		logger.Log("Lock acquired successfully")
	}
	return true
}

// discoverCommand handles command discovery with logging.
func discoverCommand(
	ctx context.Context,
	discovery *CommandDiscovery,
	hookType CommandType,
	fileDir string,
	logger *debuglog.Logger,
	debug bool,
	deps *Dependencies,
) *DiscoveredCommand {
	if logger != nil && logger.IsEnabled() {
		logger.LogSection(fmt.Sprintf("Discovering %s command", hookType))
	}

	cmd, err := discovery.DiscoverCommand(ctx, hookType, fileDir)
	if err != nil {
		if logger != nil && logger.IsEnabled() {
			logger.LogError(err, "discovering command")
		}
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error discovering command: %v\n", err)
		}
		return nil
	}

	if cmd == nil {
		if logger != nil && logger.IsEnabled() {
			logger.Log("No %s command found", hookType)
		}
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "No %s command found\n", hookType)
		}
		return nil
	}

	if logger != nil && logger.IsEnabled() {
		logger.LogDiscovery(string(hookType), cmd.String(), cmd.WorkingDir)
	}

	return cmd
}

// executeCommand handles command execution with logging.
func executeCommand(
	ctx context.Context,
	cmd *DiscoveredCommand,
	hookType CommandType,
	timeoutSecs int,
	debug bool,
	deps *Dependencies,
	logger *debuglog.Logger,
) (int, string) {
	if logger != nil && logger.IsEnabled() {
		logger.LogSection("Executing command")
		logger.LogCommand(cmd.Command, cmd.Args, cmd.WorkingDir)
	}

	executor := NewCommandExecutor(timeoutSecs, debug, deps)
	exitCode, message := executor.ExecuteForHook(ctx, cmd, hookType)

	if logger != nil && logger.IsEnabled() {
		logger.Log("Exit code: %d", exitCode)
		if message != "" {
			logger.Log("Message: %s", message)
		}
	}

	return exitCode, message
}

// discoverAndExecute discovers and executes the appropriate command.
func discoverAndExecute(
	ctx context.Context,
	projectRoot, fileDir string,
	hookType CommandType,
	timeoutSecs int,
	debug bool,
	deps *Dependencies,
	logger *debuglog.Logger,
) int {
	discovery := NewCommandDiscovery(projectRoot, timeoutSecs, deps)

	cmd := discoverCommand(ctx, discovery, hookType, fileDir, logger, debug, deps)
	if cmd == nil {
		return 0
	}

	exitCode, message := executeCommand(ctx, cmd, hookType, timeoutSecs, debug, deps, logger)

	if message != "" {
		_, _ = fmt.Fprintln(deps.Stderr, message)
	}

	return exitCode
}
