package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

const minDirParts = 2

// GetDebugLogPathForDir returns the debug log path for a specific directory.
func GetDebugLogPathForDir(dir string) string {
	// Create a sanitized version of the directory path for the filename
	// Use last two directory components if possible for readability
	cleanPath := filepath.Clean(dir)
	parts := strings.Split(cleanPath, string(filepath.Separator))

	// Filter out empty parts (e.g., from root "/" which splits to ["", ""])
	var nonEmptyParts []string
	for _, part := range parts {
		if part != "" {
			nonEmptyParts = append(nonEmptyParts, part)
		}
	}

	var namePart string
	switch {
	case len(nonEmptyParts) >= minDirParts:
		// Get last two components
		namePart = strings.Join(nonEmptyParts[len(nonEmptyParts)-2:], "-")
	case len(nonEmptyParts) == 1:
		namePart = nonEmptyParts[0]
	default:
		// Root directory or empty path
		namePart = "root"
	}

	// Clean the name part - replace any remaining slashes or problematic chars
	namePart = strings.ReplaceAll(namePart, "/", "-")
	namePart = strings.ReplaceAll(namePart, " ", "_")

	// Add a short hash of the full path to ensure uniqueness
	hash := sha256.Sum256([]byte(dir))
	hashStr := hex.EncodeToString(hash[:4]) // Just first 4 bytes for brevity

	return fmt.Sprintf("/tmp/cc-tools-%s-%s.debug", namePart, hashStr)
}
