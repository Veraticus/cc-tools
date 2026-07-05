package statusline

import (
	"regexp"
	"strings"
	"unicode"
)

// modelSuffixRegex strips trailing parentheticals like " (1M Context)"
// or " (1M Context, beta)" so they don't bleed into the abbreviated form.
var modelSuffixRegex = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// versionTokenRegex matches a token shaped like "4.6", "4.7", "3.5".
var versionTokenRegex = regexp.MustCompile(`^\d+\.\d+$`)

// abbreviateModel turns "Sonnet 4.6 (1M Context)" → "S4.6", and similar.
//
// Strategy:
//  1. Strip the trailing parenthetical suffix.
//  2. Split into tokens. If a token matches a version pattern (e.g. "4.6"),
//     take the FIRST letter of the most recent name-token plus the version.
//  3. If no recognizable version token, fall back to the cleaned input.
//  4. Empty input falls back to the literal "Claude".
func abbreviateModel(display string) string {
	cleaned := strings.TrimSpace(modelSuffixRegex.ReplaceAllString(display, ""))
	if cleaned == "" {
		return "Claude"
	}

	tokens := strings.Fields(cleaned)
	var version, lastName string
	for _, tok := range tokens {
		switch {
		case versionTokenRegex.MatchString(tok):
			version = tok
		case isAlphaName(tok):
			lastName = tok
		}
	}

	// Need both a name and a version to abbreviate. "Claude" alone
	// (no version) falls through to the cleaned input. "Claude 3.5
	// Sonnet" returns "S3.5" because Sonnet is the last name-token.
	if version != "" && lastName != "" {
		return string(toUpper(lastName[0])) + version
	}

	return cleaned
}

func toUpper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}

func isAlphaName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
