// Package main implements the smart-test CLI tool for Claude Code.
package main

import (
	"os"

	"github.com/Veraticus/cc-tools/internal/hooks"
)

func main() {
	// Configuration - these can be made into flags later if needed
	const (
		debug        = true // Show messages for passed checks
		timeoutSecs  = 60   // Command execution timeout (tests can take longer)
		cooldownSecs = 2    // Cooldown period between runs
	)

	// Run the smart test hook
	exitCode := hooks.RunSmartHook(hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs)
	os.Exit(exitCode)
}
