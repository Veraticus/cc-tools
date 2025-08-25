// Package main implements the smart-lint CLI tool for Claude Code.
package main

import (
	"os"

	"github.com/Veraticus/cc-tools/internal/hooks"
)

func main() {
	// Configuration - these can be made into flags later if needed
	const (
		debug        = true // Show messages for passed checks
		timeoutSecs  = 30   // Command execution timeout
		cooldownSecs = 2    // Cooldown period between runs
	)

	// Run the smart lint hook
	exitCode := hooks.RunSmartHook(hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs)
	os.Exit(exitCode)
}
