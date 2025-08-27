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

	// If path is longer than 3 directories, truncate with …
	const maxDisplayedParts = 3
	if nonEmptyParts > maxDisplayedParts {
		// Keep first part (~ or /), last 2 parts
		if len(parts) > 0 && parts[0] == "~" {
			return fmt.Sprintf("~/%s/%s", parts[len(parts)-2], parts[len(parts)-1])
		}
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

// formatTokens formats token count for display.
func formatTokens(count int) string {
	const (
		million  = 1000000
		thousand = 1000
	)
	if count >= million {
		return fmt.Sprintf("%.1fM", float64(count)/float64(million))
	}
	if count >= thousand {
		return fmt.Sprintf("%.1fk", float64(count)/float64(thousand))
	}
	return fmt.Sprintf("%d", count)
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
