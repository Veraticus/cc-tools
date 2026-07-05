package statusline

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
)

// formatPath formats a directory path similar to starship truncation.
func formatPath(path string) string {
	home := os.Getenv("HOME")

	// Replace home with ~
	if home != "" && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}

	// Remove empty parts from splitting
	var parts []string
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}

	// Handle root path
	if path == "/" {
		return "/"
	}

	// If starts with /, add empty part at beginning for absolute path
	if strings.HasPrefix(path, "/") && (len(parts) == 0 || parts[0] != "") {
		parts = append([]string{""}, parts...)
	}

	// Count non-empty parts for truncation decision
	nonEmptyParts := 0
	for _, part := range parts {
		if part != "" {
			nonEmptyParts++
		}
	}

	// If path is longer than 3 directories, truncate with `…/`.
	// We intentionally drop the `~` prefix on truncation so the cc-tools
	// statusline matches starship's directory module byte-for-byte
	// (starship's truncation_length=2 + truncation_symbol="…/" yields the
	// same shape). The slight info loss (you can't tell from the chip
	// alone whether you're under $HOME) is acceptable for line parity.
	const maxDisplayedParts = 3
	if nonEmptyParts > maxDisplayedParts {
		return fmt.Sprintf("…/%s/%s", parts[len(parts)-2], parts[len(parts)-1])
	}

	return path
}

// truncateText truncates text to a maximum display width with ellipsis.
func truncateText(text string, maxWidth int) string {
	// Use runewidth to properly count display width
	width := runewidth.StringWidth(text)
	if width <= maxWidth {
		return text
	}

	// Truncate to fit within maxWidth including ellipsis
	const ellipsisWidth = 1
	return runewidth.Truncate(text, maxWidth-ellipsisWidth, "") + "…"
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(text string) string {
	// Remove all ANSI escape sequences
	var result strings.Builder
	inEscape := false
	for _, r := range text {
		switch {
		case r == '\033':
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}
