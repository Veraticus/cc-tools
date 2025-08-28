// Package main implements the cc-tools-validate command for running parallel lint and test validations.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

// bytesInputReader implements hooks.InputReader for a byte slice.
type bytesInputReader struct {
	data []byte
}

func (b *bytesInputReader) ReadAll() ([]byte, error) {
	return b.data, nil
}

func (b *bytesInputReader) IsTerminal() bool {
	return false
}

func main() {
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	timeoutSecs, cooldownSecs := loadValidateConfig()

	// Read stdin once
	stdinData, err := io.ReadAll(os.Stdin)
	if err != nil {
		// If we can't read input, run normally without skip checking
		exitCode := hooks.RunValidateHook(context.Background(), debug, timeoutSecs, cooldownSecs, nil)
		os.Exit(exitCode)
	}

	// Check if directory should be skipped
	skipLint, skipTest := checkSkips(stdinData)

	// If both are skipped, exit silently
	if skipLint && skipTest {
		os.Exit(0)
	}

	// Pass skip information to the validate hook
	skipConfig := &hooks.SkipConfig{
		SkipLint: skipLint,
		SkipTest: skipTest,
	}

	// Create dependencies with our input reader
	deps := &hooks.Dependencies{
		Input:   &bytesInputReader{data: stdinData},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		FS:      hooks.NewDefaultDependencies().FS,
		Runner:  hooks.NewDefaultDependencies().Runner,
		Process: hooks.NewDefaultDependencies().Process,
		Clock:   hooks.NewDefaultDependencies().Clock,
	}

	exitCode := hooks.RunValidateHookWithSkip(context.Background(), debug, timeoutSecs, cooldownSecs, skipConfig, deps)
	os.Exit(exitCode)
}

func checkSkips(stdinData []byte) (bool, bool) {
	// Parse the JSON
	var input map[string]any
	if err := json.Unmarshal(stdinData, &input); err != nil {
		// If we can't decode input, don't skip
		return false, false
	}

	// Get file path from input
	var filePath string
	if toolInput, ok := input["tool_input"].(map[string]any); ok {
		if fp, fpOk := toolInput["file_path"].(string); fpOk {
			filePath = fp
		}
	}

	if filePath == "" {
		// No file path, don't skip
		return false, false
	}

	// Get directory from file path
	dir := filepath.Dir(filePath)

	// Convert to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, false
	}

	// Check skip registry
	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	skipLint, _ := registry.IsSkipped(ctx, skipregistry.DirectoryPath(absDir), skipregistry.SkipTypeLint)
	skipTest, _ := registry.IsSkipped(ctx, skipregistry.DirectoryPath(absDir), skipregistry.SkipTypeTest)

	if os.Getenv("CLAUDE_HOOKS_DEBUG") == "1" {
		if skipLint {
			fmt.Fprintf(os.Stderr, "Skipping lint for directory: %s\n", absDir)
		}
		if skipTest {
			fmt.Fprintf(os.Stderr, "Skipping test for directory: %s\n", absDir)
		}
	}

	return skipLint, skipTest
}

func loadValidateConfig() (int, int) {
	timeoutSecs := 60
	cooldownSecs := 5

	// Try to load from config file
	if cfg, err := config.Load(); err == nil {
		// Check if validate config exists
		if cfg.Hooks.Validate.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Validate.TimeoutSeconds
		}
		if cfg.Hooks.Validate.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Validate.CooldownSeconds
		}
	}

	// Environment variables override config
	if timeout := os.Getenv("CC_TOOLS_HOOKS_VALIDATE_TIMEOUT_SECONDS"); timeout != "" {
		if val, err := strconv.Atoi(timeout); err == nil && val > 0 {
			timeoutSecs = val
		}
	}
	if cooldown := os.Getenv("CC_TOOLS_HOOKS_VALIDATE_COOLDOWN_SECONDS"); cooldown != "" {
		if val, err := strconv.Atoi(cooldown); err == nil && val > 0 {
			cooldownSecs = val
		}
	}

	return timeoutSecs, cooldownSecs
}
