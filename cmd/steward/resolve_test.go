package main

import "testing"

// TestExitUsageError_IsBSDExUsageNotClaudeCodeBlockingExit asserts
// exitUsageError is 64 (BSD EX_USAGE), never 2. Claude Code reserves exit
// code 2 as the Stop-hook blocking-error channel: stderr on exit 2 is fed
// back to Claude and the stop is blocked. steward notify runs as a Stop
// hook on every session, so a usage error (e.g. a typo'd flag) must never
// exit 2 — that would force-continue every session on every host.
func TestExitUsageError_IsBSDExUsageNotClaudeCodeBlockingExit(t *testing.T) {
	const wantExitUsageError = 64
	if exitUsageError != wantExitUsageError {
		t.Errorf(
			"exitUsageError = %d, want %d (BSD EX_USAGE); exit 2 is reserved for Claude Code's Stop-hook blocking-error channel",
			exitUsageError,
			wantExitUsageError,
		)
	}
}
