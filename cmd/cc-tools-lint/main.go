// Package main implements the cc-tools-lint CLI application.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

func main() {
	// Check if directory should be skipped
	if shouldSkip() {
		os.Exit(0)
	}

	timeoutSecs, cooldownSecs := loadLintConfig()
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs, nil)
	os.Exit(exitCode)
}

func shouldSkip() bool {
	// Read input to get the file path
	var input map[string]interface{}
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&input); err != nil {
		// If we can't decode input, don't skip
		return false
	}

	// Get file path from input
	var filePath string
	if toolInput, ok := input["tool_input"].(map[string]interface{}); ok {
		if fp, ok := toolInput["file_path"].(string); ok {
			filePath = fp
		}
	}

	if filePath == "" {
		// No file path, don't skip
		return false
	}

	// Get directory from file path
	dir := filepath.Dir(filePath)

	// Convert to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}

	// Check skip registry
	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	isSkipped, err := registry.IsSkipped(ctx, skipregistry.DirectoryPath(absDir), skipregistry.SkipTypeLint)
	if err != nil {
		// If there's an error checking, don't skip
		return false
	}

	if isSkipped && os.Getenv("CLAUDE_HOOKS_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "Skipping lint for directory: %s\n", absDir)
	}

	return isSkipped
}

func loadLintConfig() (int, int) {
	timeoutSecs := 30
	cooldownSecs := 2

	// Load configuration
	cfg, _ := config.Load()
	if cfg != nil {
		if cfg.Hooks.Lint.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Lint.TimeoutSeconds
		}
		if cfg.Hooks.Lint.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Lint.CooldownSeconds
		}
	}

	// Support legacy environment variables for backward compatibility
	if envTimeout := os.Getenv("CLAUDE_HOOKS_LINT_TIMEOUT"); envTimeout != "" {
		if val, err := strconv.Atoi(envTimeout); err == nil && val > 0 {
			timeoutSecs = val
		}
	}
	if envCooldown := os.Getenv("CLAUDE_HOOKS_LINT_COOLDOWN"); envCooldown != "" {
		if val, err := strconv.Atoi(envCooldown); err == nil && val >= 0 {
			cooldownSecs = val
		}
	}

	return timeoutSecs, cooldownSecs
}
