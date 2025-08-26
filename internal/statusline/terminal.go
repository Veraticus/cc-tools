package statusline

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// DefaultTerminalWidth provides terminal width detection
type DefaultTerminalWidth struct{}

// GetWidth returns the current terminal width
func (t *DefaultTerminalWidth) GetWidth() int {
	// Priority 1: Explicit override for testing
	if testWidth := os.Getenv("CLAUDE_STATUSLINE_WIDTH"); testWidth != "" {
		if width, err := strconv.Atoi(testWidth); err == nil && width > 0 {
			return width
		}
	}
	
	// Priority 2: COLUMNS environment variable (commonly set)
	if columns := os.Getenv("COLUMNS"); columns != "" {
		if width, err := strconv.Atoi(columns); err == nil && width > 0 {
			return width
		}
	}
	
	// Priority 3: Check if we're in tmux and get width directly
	if tmux := os.Getenv("TMUX"); tmux != "" {
		if width := getTmuxWidth(); width > 0 {
			return width
		}
	}
	
	// Priority 4: Use golang.org/x/term for proper terminal detection
	// Try stderr first (most likely to be connected to terminal when piping)
	if width, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && width > 0 {
		return width
	}
	
	// Try stdout
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	
	// Try stdin
	if width, _, err := term.GetSize(int(os.Stdin.Fd())); err == nil && width > 0 {
		return width
	}
	
	// Priority 5: Try opening /dev/tty directly
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer tty.Close()
		if width, _, err := term.GetSize(int(tty.Fd())); err == nil && width > 0 {
			return width
		}
	}
	
	// Priority 6: Try SSH_TTY if we're in an SSH session
	if sshTty := os.Getenv("SSH_TTY"); sshTty != "" {
		if file, err := os.Open(sshTty); err == nil {
			defer file.Close()
			if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
				return width
			}
		}
	}
	
	// Priority 7: Try tput command (may work in some edge cases)
	if width := getTputWidth(); width > 0 {
		return width
	}
	
	// Priority 8: Try stty size command
	if width := getSttyWidth(); width > 0 {
		return width
	}
	
	// Default fallback - use a reasonable width that allows context bar
	return 200
}

func getTmuxWidth() int {
	cmd := exec.Command("tmux", "display-message", "-p", "#{window_width}")
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
	cmd := exec.Command("tput", "cols")
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
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin // Important for stty to work
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	
	// stty size returns "rows cols"
	parts := strings.Fields(string(output))
	if len(parts) != 2 {
		return 0
	}
	
	width, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	
	return width
}