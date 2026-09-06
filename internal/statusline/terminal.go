package statusline

import (
	"os"
	"strconv"
)

// DefaultTerminalWidth reads terminal width from the COLUMNS env var
// that Claude Code 2.1.153+ injects into status-line subprocesses.
type DefaultTerminalWidth struct{}

// GetWidth returns the current terminal width.
//
// Precedence:
//  1. STEWARD_STATUSLINE_WIDTH — test override, used by integration harnesses.
//  2. COLUMNS — populated by Claude Code per the 2.1.153 changelog.
//  3. defaultWidth — fallback for unit tests and any pre-2.1.153 environment.
func (t *DefaultTerminalWidth) GetWidth() int {
	if w := readWidthEnv("STEWARD_STATUSLINE_WIDTH"); w > 0 {
		return w
	}
	if w := readWidthEnv("COLUMNS"); w > 0 {
		return w
	}
	const defaultWidth = 200
	return defaultWidth
}

func readWidthEnv(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	w, err := strconv.Atoi(v)
	if err != nil || w <= 0 {
		return 0
	}
	return w
}
