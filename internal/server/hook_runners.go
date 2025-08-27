package server

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/Veraticus/cc-tools/internal/hooks"
)

// HookLintRunner implements LintRunner using the hooks package.
type HookLintRunner struct {
	debug        bool
	timeoutSecs  int
	cooldownSecs int
}

// NewHookLintRunner creates a new lint runner.
func NewHookLintRunner(debug bool, timeoutSecs, cooldownSecs int) *HookLintRunner {
	return &HookLintRunner{
		debug:        debug,
		timeoutSecs:  timeoutSecs,
		cooldownSecs: cooldownSecs,
	}
}

// Run executes the lint hook with the given input.
func (r *HookLintRunner) Run(ctx context.Context, input io.Reader) (io.Reader, error) {
	// Read input
	inputBytes, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}

	// Create string-based input reader for hooks
	inputReader := hooks.NewStringInputReader(string(inputBytes))
	outputWriter := hooks.NewStringOutputWriter()

	// Create dependencies
	deps := hooks.NewDefaultDependencies()
	deps.Input = inputReader
	deps.Stdout = outputWriter
	deps.Stderr = outputWriter

	// Run the hook
	exitCode := hooks.RunSmartHook(ctx, hooks.CommandTypeLint, r.debug, r.timeoutSecs, r.cooldownSecs, deps)

	// Get the output regardless of exit code
	output := outputWriter.String()

	// Exit code 2 means "show message to user" - this is a successful execution
	// Exit code 0 means success (possibly silent)
	// Any other exit code is an actual infrastructure error
	if exitCode != 0 && exitCode != 2 {
		return nil, fmt.Errorf("lint hook error with exit code %d: %s", exitCode, output)
	}

	// Return output for successful execution (exit codes 0 and 2)
	return bytes.NewReader([]byte(output)), nil
}

// HookTestRunner implements TestRunner using the hooks package.
type HookTestRunner struct {
	debug        bool
	timeoutSecs  int
	cooldownSecs int
}

// NewHookTestRunner creates a new test runner.
func NewHookTestRunner(debug bool, timeoutSecs, cooldownSecs int) *HookTestRunner {
	return &HookTestRunner{
		debug:        debug,
		timeoutSecs:  timeoutSecs,
		cooldownSecs: cooldownSecs,
	}
}

// Run executes the test hook with the given input.
func (r *HookTestRunner) Run(ctx context.Context, input io.Reader) (io.Reader, error) {
	// Read input
	inputBytes, err := io.ReadAll(input)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}

	// Create string-based input reader for hooks
	inputReader := hooks.NewStringInputReader(string(inputBytes))
	outputWriter := hooks.NewStringOutputWriter()

	// Create dependencies
	deps := hooks.NewDefaultDependencies()
	deps.Input = inputReader
	deps.Stdout = outputWriter
	deps.Stderr = outputWriter

	// Run the hook
	exitCode := hooks.RunSmartHook(ctx, hooks.CommandTypeTest, r.debug, r.timeoutSecs, r.cooldownSecs, deps)

	// Get the output regardless of exit code
	output := outputWriter.String()

	// Exit code 2 means "show message to user" - this is a successful execution
	// Exit code 0 means success (possibly silent)
	// Any other exit code is an actual infrastructure error
	if exitCode != 0 && exitCode != 2 {
		return nil, fmt.Errorf("test hook error with exit code %d: %s", exitCode, output)
	}

	// Return output for successful execution (exit codes 0 and 2)
	return bytes.NewReader([]byte(output)), nil
}
