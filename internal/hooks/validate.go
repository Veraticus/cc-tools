package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Veraticus/cc-tools/internal/shared"
)

// SkipConfig represents which validations should be skipped.
type SkipConfig struct {
	SkipLint bool
	SkipTest bool
}

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
	discovery  *CommandDiscovery
	executor   *CommandExecutor
	timeout    int
	debug      bool
	skipConfig *SkipConfig
}

// NewParallelValidateExecutor creates a new parallel validate executor.
func NewParallelValidateExecutor(
	projectRoot string,
	timeout int,
	debug bool,
	skipConfig *SkipConfig,
	deps *Dependencies,
) *ParallelValidateExecutor {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &ParallelValidateExecutor{
		discovery:  NewCommandDiscovery(projectRoot, timeout, deps),
		executor:   NewCommandExecutor(timeout, debug, deps),
		timeout:    timeout,
		debug:      debug,
		skipConfig: skipConfig,
	}
}

// ExecuteValidations discovers and runs lint and test commands in parallel.
func (pve *ParallelValidateExecutor) ExecuteValidations(
	ctx context.Context,
	_, fileDir string,
) (*ValidateResult, error) {
	// Discover commands
	lintCmd, testCmd := pve.discoverCommands(ctx, fileDir)

	// If neither command found, return empty result
	if lintCmd == nil && testCmd == nil {
		return &ValidateResult{BothPassed: true}, nil
	}

	// Execute commands in parallel
	result := pve.executeParallel(ctx, lintCmd, testCmd)

	// Determine overall success
	result.BothPassed = pve.checkSuccess(result)

	return result, nil
}

// discoverCommands discovers lint and test commands based on skip configuration.
func (pve *ParallelValidateExecutor) discoverCommands(
	ctx context.Context,
	fileDir string,
) (*DiscoveredCommand, *DiscoveredCommand) {
	skipLint := pve.skipConfig != nil && pve.skipConfig.SkipLint
	skipTest := pve.skipConfig != nil && pve.skipConfig.SkipTest

	var lintCmd, testCmd *DiscoveredCommand
	if !skipLint {
		lintCmd, _ = pve.discovery.DiscoverCommand(ctx, CommandTypeLint, fileDir)
	}
	if !skipTest {
		testCmd, _ = pve.discovery.DiscoverCommand(ctx, CommandTypeTest, fileDir)
	}

	return lintCmd, testCmd
}

// executeParallel runs lint and test commands in parallel.
func (pve *ParallelValidateExecutor) executeParallel(
	ctx context.Context,
	lintCmd, testCmd *DiscoveredCommand,
) *ValidateResult {
	var wg sync.WaitGroup
	result := &ValidateResult{}

	skipLint := pve.skipConfig != nil && pve.skipConfig.SkipLint
	skipTest := pve.skipConfig != nil && pve.skipConfig.SkipTest

	// Launch lint if available and not skipped
	if lintCmd != nil && !skipLint {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result.LintResult = pve.executeCommand(ctx, lintCmd, CommandTypeLint)
		}()
	}

	// Launch test if available and not skipped
	if testCmd != nil && !skipTest {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result.TestResult = pve.executeCommand(ctx, testCmd, CommandTypeTest)
		}()
	}

	wg.Wait()
	return result
}

// checkSuccess determines if both lint and test passed.
func (pve *ParallelValidateExecutor) checkSuccess(result *ValidateResult) bool {
	skipLint := pve.skipConfig != nil && pve.skipConfig.SkipLint
	skipTest := pve.skipConfig != nil && pve.skipConfig.SkipTest

	lintPassed := result.LintResult == nil || result.LintResult.Success || skipLint
	testPassed := result.TestResult == nil || result.TestResult.Success || skipTest

	return lintPassed && testPassed
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

// RunValidateHookWithSkip is the main entry point for the validate hook with skip configuration.
func RunValidateHookWithSkip(
	ctx context.Context,
	debug bool,
	timeoutSecs int,
	cooldownSecs int,
	skipConfig *SkipConfig,
	deps *Dependencies,
) int {
	return runValidateHookInternal(ctx, debug, timeoutSecs, cooldownSecs, skipConfig, deps)
}

// RunValidateHook is the main entry point for the validate hook.
func RunValidateHook(
	ctx context.Context,
	debug bool,
	timeoutSecs int,
	cooldownSecs int,
	deps *Dependencies,
) int {
	return runValidateHookInternal(ctx, debug, timeoutSecs, cooldownSecs, nil, deps)
}

// runValidateHookInternal contains the shared logic for running validation.
func runValidateHookInternal(
	ctx context.Context,
	debug bool,
	timeoutSecs int,
	cooldownSecs int,
	skipConfig *SkipConfig,
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
	if !acquireLock(lockMgr, debug, deps.Stderr, nil) {
		return 0
	}
	defer func() {
		_ = lockMgr.Release()
	}()

	// Execute validations in parallel with optional skip configuration
	validateExecutor := NewParallelValidateExecutor(projectRoot, timeoutSecs, debug, skipConfig, deps)
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
