package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"

	"github.com/joshsymonds/steward/internal/statusline"
	"github.com/joshsymonds/steward/internal/subagentstatusline"
)

func runSubagentStatuslineCommand() {
	code := subagentStatuslineMain()
	if code != 0 {
		os.Exit(code)
	}
}

// subagentStatuslineMain is a separate function so its returns happen
// before the outer os.Exit (gocritic exitAfterDefer pattern).
func subagentStatuslineMain() int {
	contextWindow := subagentstatusline.DefaultContextWindow
	if raw := os.Getenv("STEWARD_SUBAGENT_CONTEXT_WINDOW"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			contextWindow = n
		}
	}
	env := &statusline.DefaultEnvReader{}

	// Buffer stdout: WriteDecorations does one Write per decoration;
	// pairing it with a bufio.Writer collapses N writes into one
	// flush syscall regardless of how many tasks Claude passed.
	bw := bufio.NewWriter(os.Stdout)
	if err := subagentstatusline.Render(os.Stdin, bw, contextWindow, env, statusline.ResolveCacheDir()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "subagent-statusline: %v\n", err)
		return 1
	}
	if flushErr := bw.Flush(); flushErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "subagent-statusline: flush: %v\n", flushErr)
		return 1
	}
	return 0
}
