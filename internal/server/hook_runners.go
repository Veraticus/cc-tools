package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/statusline"
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
	exitCode := hooks.RunSmartHookWithDeps(hooks.CommandTypeLint, r.debug, r.timeoutSecs, r.cooldownSecs, deps)

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
	exitCode := hooks.RunSmartHookWithDeps(hooks.CommandTypeTest, r.debug, r.timeoutSecs, r.cooldownSecs, deps)

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

// StatuslineRunner implements StatuslineGenerator using the statusline package.
type StatuslineRunner struct {
	cacheDir      string
	cacheDuration int // seconds
}

// NewStatuslineRunner creates a new statusline generator.
func NewStatuslineRunner(cacheDir string, cacheDuration int) *StatuslineRunner {
	return &StatuslineRunner{
		cacheDir:      cacheDir,
		cacheDuration: cacheDuration,
	}
}

// Generate creates a statusline from the input.
func (r *StatuslineRunner) Generate(ctx context.Context, input io.Reader) (string, error) {
	// Create dependencies
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		CacheDir:      r.cacheDir,
		CacheDuration: time.Duration(r.cacheDuration) * time.Second,
	}

	// Generate statusline
	sl := statusline.New(deps)
	result, err := sl.Generate(input)
	if err != nil {
		return "", fmt.Errorf("generate statusline: %w", err)
	}

	return result, nil
}