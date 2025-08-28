package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Veraticus/cc-tools/internal/shared"
)

// ValidationResult represents the result of a single validation (lint or test).
type ValidationResult struct {
	Type     CommandType
	Success  bool
	ExitCode int
	Message  string
	Command  *DiscoveredCommand
	Error    error
}

// ValidateExecutor executes parallel validation commands.
type ValidateExecutor interface {
	ExecuteValidations(ctx context.Context, projectRoot, fileDir string) (*ValidateResult, error)
}

// ValidateResult contains the combined results of lint and test validation.
type ValidateResult struct {
	LintResult *ValidationResult
	TestResult *ValidationResult
	BothPassed bool
}

// FormatMessage returns the appropriate user message based on validation results.
func (vr *ValidateResult) FormatMessage() string {
	// Both passed
	if vr.BothPassed {
		return shared.RawWarningStyle.Render("👉 Validations pass. Continue with your task.")
	}

	// Determine what failed
	lintFailed := vr.LintResult != nil && !vr.LintResult.Success
	testFailed := vr.TestResult != nil && !vr.TestResult.Success

	// Both failed
	if lintFailed && testFailed {
		lintCmd := vr.LintResult.Command.String()
		testCmd := vr.TestResult.Command.String()
		return shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Lint and test failures. Run 'cd %s && %s' and '%s'",
				vr.LintResult.Command.WorkingDir, lintCmd, testCmd))
	}

	// Only lint failed
	if lintFailed {
		cmdStr := vr.LintResult.Command.String()
		return shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix lint failures",
				vr.LintResult.Command.WorkingDir, cmdStr))
	}

	// Only test failed
	if testFailed {
		cmdStr := vr.TestResult.Command.String()
		return shared.RawErrorStyle.Render(
			fmt.Sprintf("⛔ BLOCKING: Run 'cd %s && %s' to fix test failures",
				vr.TestResult.Command.WorkingDir, cmdStr))
	}

	// Neither command was found (both nil results)
	return ""
}

// ParallelValidateExecutor implements ValidateExecutor with parallel execution.
type ParallelValidateExecutor struct {
	discovery *CommandDiscovery
	executor  *CommandExecutor
	timeout   int
	debug     bool
}

// NewParallelValidateExecutor creates a new parallel validate executor.
func NewParallelValidateExecutor(
	projectRoot string,
	timeout int,
	debug bool,
	deps *Dependencies,
) *ParallelValidateExecutor {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &ParallelValidateExecutor{
		discovery: NewCommandDiscovery(projectRoot, timeout, deps),
		executor:  NewCommandExecutor(timeout, debug, deps),
		timeout:   timeout,
		debug:     debug,
	}
}

// ExecuteValidations discovers and runs lint and test commands in parallel.
func (pve *ParallelValidateExecutor) ExecuteValidations(
	ctx context.Context,
	_, fileDir string,
) (*ValidateResult, error) {
	// Discover commands first (sequential, fast)
	lintCmd, _ := pve.discovery.DiscoverCommand(ctx, CommandTypeLint, fileDir)
	testCmd, _ := pve.discovery.DiscoverCommand(ctx, CommandTypeTest, fileDir)

	// If neither command found, return empty result
	if lintCmd == nil && testCmd == nil {
		return &ValidateResult{BothPassed: true}, nil
	}

	// Execute commands in parallel
	var wg sync.WaitGroup
	result := &ValidateResult{}

	// Launch lint if available
	if lintCmd != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result.LintResult = pve.executeCommand(ctx, lintCmd, CommandTypeLint)
		}()
	}

	// Launch test if available
	if testCmd != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result.TestResult = pve.executeCommand(ctx, testCmd, CommandTypeTest)
		}()
	}

	// Wait for both to complete
	wg.Wait()

	// Determine overall success
	lintPassed := result.LintResult == nil || result.LintResult.Success
	testPassed := result.TestResult == nil || result.TestResult.Success
	result.BothPassed = lintPassed && testPassed

	return result, nil
}

// executeCommand runs a single command and returns its validation result.
func (pve *ParallelValidateExecutor) executeCommand(
	ctx context.Context,
	cmd *DiscoveredCommand,
	cmdType CommandType,
) *ValidationResult {
	execResult := pve.executor.Execute(ctx, cmd)

	return &ValidationResult{
		Type:     cmdType,
		Success:  execResult.Success,
		ExitCode: execResult.ExitCode,
		Command:  cmd,
		Error:    execResult.Error,
	}
}

// RunValidateHook is the main entry point for the validate hook.
func RunValidateHook(
	ctx context.Context,
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

	// Acquire lock for validate
	lockMgr := NewLockManager(projectRoot, "validate", cooldownSecs, deps)
	if !acquireLock(lockMgr, debug, deps.Stderr) {
		return 0
	}
	defer func() {
		_ = lockMgr.Release()
	}()

	// Execute validations in parallel
	validateExecutor := NewParallelValidateExecutor(projectRoot, timeoutSecs, debug, deps)
	result, err := validateExecutor.ExecuteValidations(ctx, projectRoot, fileDir)
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(deps.Stderr, "Error executing validations: %v\n", err)
		}
		return 0
	}

	// Format and display message
	message := result.FormatMessage()
	if message != "" {
		_, _ = fmt.Fprintln(deps.Stderr, message)
		return ExitCodeShowMessage
	}

	return 0
}
