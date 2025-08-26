package statusline

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
)

// formatPath formats a directory path similar to starship truncation
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
	
	// If path is longer than 3 directories, truncate with …
	if len(parts) > 3 {
		// Keep first part (~ or /), last 2 parts
		if len(parts) > 0 && parts[0] == "~" {
			return fmt.Sprintf("~/%s/%s", parts[len(parts)-2], parts[len(parts)-1])
		}
		return fmt.Sprintf("…/%s/%s", parts[len(parts)-2], parts[len(parts)-1])
	}
	
	return path
}

// truncateText truncates text to a maximum display width with ellipsis
func truncateText(text string, maxWidth int) string {
	// Use runewidth to properly count display width
	width := runewidth.StringWidth(text)
	if width <= maxWidth {
		return text
	}
	
	// Truncate to fit within maxWidth including ellipsis
	return runewidth.Truncate(text, maxWidth-1, "") + "…"
}

// formatTokens formats token count for display
func formatTokens(count int) string {
	if count >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	}
	if count >= 1000 {
		return fmt.Sprintf("%.1fk", float64(count)/1000)
	}
	return fmt.Sprintf("%d", count)
}

// calculateLeftLength calculates the display width of the left section
func calculateLeftLength(dirPath, tokenInfo, modelIcon string) int {
	leftLength := 1                                        // Left curve (powerline)
	leftLength += 1                                        // Space before directory  
	leftLength += runewidth.StringWidth(dirPath)          // Directory text
	leftLength += 1                                        // Space after directory
	leftLength += 1                                        // Chevron to model section
	leftLength += runewidth.StringWidth(tokenInfo)         // Model section with icon and text
	leftLength += 1                                        // End chevron
	return leftLength
}

// calculateRightLength calculates the display width of the right section
func calculateRightLength(data *CachedData, rightSide string) int {
	// Strip ANSI codes and calculate actual display width
	cleanRight := stripAnsi(rightSide)
	return runewidth.StringWidth(cleanRight)
}

// stripAnsi removes ANSI escape sequences from a string
func stripAnsi(text string) string {
	// Remove all ANSI escape sequences
	var result strings.Builder
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
		} else if inEscape {
			if r == 'm' {
				inEscape = false
			}
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}