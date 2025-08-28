// Package main implements the cc-tools-lint CLI application.
package main

import (
	"context"
	"os"
	"strconv"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
)

func main() {
	timeoutSecs, cooldownSecs := loadLintConfig()
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs, nil)
	os.Exit(exitCode)
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
