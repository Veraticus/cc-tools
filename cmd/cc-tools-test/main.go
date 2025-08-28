// Package main implements the cc-tools-test CLI application.
package main

import (
	"context"
	"os"
	"strconv"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
)

func main() {
	timeoutSecs, cooldownSecs := loadTestConfig()
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs, nil)
	os.Exit(exitCode)
}

func loadTestConfig() (int, int) {
	timeoutSecs := 60
	cooldownSecs := 2

	// Load configuration
	cfg, _ := config.Load()
	if cfg != nil {
		if cfg.Hooks.Test.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Test.TimeoutSeconds
		}
		if cfg.Hooks.Test.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Test.CooldownSeconds
		}
	}

	// Support legacy environment variables for backward compatibility
	if envTimeout := os.Getenv("CLAUDE_HOOKS_TEST_TIMEOUT"); envTimeout != "" {
		if val, err := strconv.Atoi(envTimeout); err == nil && val > 0 {
			timeoutSecs = val
		}
	}
	if envCooldown := os.Getenv("CLAUDE_HOOKS_TEST_COOLDOWN"); envCooldown != "" {
		if val, err := strconv.Atoi(envCooldown); err == nil && val >= 0 {
			cooldownSecs = val
		}
	}

	return timeoutSecs, cooldownSecs
}