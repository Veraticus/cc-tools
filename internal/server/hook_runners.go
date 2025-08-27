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

	// Check exit code
	if exitCode != 0 {
		output := outputWriter.String()
		if output != "" {
			return nil, fmt.Errorf("lint failed: %s", output)
		}
		return nil, fmt.Errorf("lint failed with exit code %d", exitCode)
	}

	// Return output as reader
	return bytes.NewReader([]byte(outputWriter.String())), nil
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

	// Check exit code
	if exitCode != 0 {
		output := outputWriter.String()
		if output != "" {
			return nil, fmt.Errorf("test failed: %s", output)
		}
		return nil, fmt.Errorf("test failed with exit code %d", exitCode)
	}

	// Return output as reader
	return bytes.NewReader([]byte(outputWriter.String())), nil
}
