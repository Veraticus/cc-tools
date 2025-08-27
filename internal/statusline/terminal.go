package statusline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// DefaultTerminalWidth provides terminal width detection.
type DefaultTerminalWidth struct{}

// GetWidth returns the current terminal width.
func (t *DefaultTerminalWidth) GetWidth() int {
	// Try various methods in priority order
	widthMethods := []func() int{
		t.getTestOverride,
		t.getColumnsEnv,
		t.getTmuxIfAvailable,
		t.getFromStderr,
		t.getFromStdout,
		t.getFromStdin,
		t.getFromTTY,
		t.getSSHWidth,
		getTputWidth,
		getSttyWidth,
	}

	for _, method := range widthMethods {
		if width := method(); width > 0 {
			return width
		}
	}

	// Default fallback
	return t.getDefault()
}

func (t *DefaultTerminalWidth) getColumnsEnv() int {
	if columns := os.Getenv("COLUMNS"); columns != "" {
		if width, err := strconv.Atoi(columns); err == nil && width > 0 {
			return width
		}
	}
	return 0
}

func (t *DefaultTerminalWidth) getTmuxIfAvailable() int {
	if tmux := os.Getenv("TMUX"); tmux != "" {
		return getTmuxWidth()
	}
	return 0
}

func (t *DefaultTerminalWidth) getFromStderr() int {
	if width, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && width > 0 {
		return width
	}
	return 0
}

func (t *DefaultTerminalWidth) getFromStdout() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return 0
}

func (t *DefaultTerminalWidth) getFromStdin() int {
	if width, _, err := term.GetSize(int(os.Stdin.Fd())); err == nil && width > 0 {
		return width
	}
	return 0
}

func (t *DefaultTerminalWidth) getFromTTY() int {
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer func() { _ = tty.Close() }()
		if width, _, sizeErr := term.GetSize(int(tty.Fd())); sizeErr == nil && width > 0 {
			return width
		}
	}
	return 0
}

func (t *DefaultTerminalWidth) getDefault() int {
	const defaultWidth = 200
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "Using default width: %d\n", defaultWidth)
	}
	return defaultWidth
}

func (t *DefaultTerminalWidth) getTestOverride() int {
	if testWidth := os.Getenv("CLAUDE_STATUSLINE_WIDTH"); testWidth != "" {
		if width, err := strconv.Atoi(testWidth); err == nil && width > 0 {
			if os.Getenv("DEBUG_WIDTH") == "1" {
				fmt.Fprintf(os.Stderr, "Using CLAUDE_STATUSLINE_WIDTH: %d\n", width)
			}
			return width
		}
	}
	return 0
}

func (t *DefaultTerminalWidth) getSSHWidth() int {
	if sshTty := os.Getenv("SSH_TTY"); sshTty != "" {
		if file, err := os.Open(sshTty); err == nil { //nolint:gosec // SSH_TTY is a trusted env var
			defer func() { _ = file.Close() }()
			if width, _, sizeErr := term.GetSize(int(file.Fd())); sizeErr == nil && width > 0 {
				return width
			}
		}
	}
	return 0
}

func getTmuxWidth() int {
	const commandTimeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{window_width}")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	width, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}

	return width
}

func getTputWidth() int {
	const commandTimeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tput", "cols")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	width, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}

	return width
}

func getSttyWidth() int {
	const commandTimeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "stty", "size")
	cmd.Stdin = os.Stdin // Important for stty to work
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	// stty size returns "rows cols"
	const expectedParts = 2
	parts := strings.Fields(string(output))
	if len(parts) != expectedParts {
		return 0
	}

	width, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}

	return width
}
