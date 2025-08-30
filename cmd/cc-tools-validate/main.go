// Package main implements the cc-tools-validate command for running parallel lint and test validations.
package main

import (
	"context"
	"os"
	"strconv"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
)

func main() {
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	timeoutSecs, cooldownSecs := loadValidateConfig()

	exitCode := hooks.ValidateWithSkipCheck(
		context.Background(),
		os.Stdin,
		os.Stdout,
		os.Stderr,
		debug,
		timeoutSecs,
		cooldownSecs,
	)
	os.Exit(exitCode)
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
