package output

import (
	"fmt"
)

// HookFormatter provides output formatting specifically for Claude Code hooks.
// It uses raw ANSI codes to ensure compatibility with Claude Code's expectations.
type HookFormatter struct{}

// NewHookFormatter creates a new hook formatter.
func NewHookFormatter() *HookFormatter {
	return &HookFormatter{}
}

// Raw ANSI escape codes for hook output.
const (
	ansiRed    = "\033[0;31m"
	ansiGreen  = "\033[0;32m"
	ansiYellow = "\033[0;33m"
	ansiReset  = "\033[0m"
)

// FormatSuccess formats a success message with green color.
func (h *HookFormatter) FormatSuccess(message string) string {
	return fmt.Sprintf("%s%s%s", ansiGreen, message, ansiReset)
}

// FormatWarning formats a warning message with yellow color.
func (h *HookFormatter) FormatWarning(message string) string {
	return fmt.Sprintf("%s%s%s", ansiYellow, message, ansiReset)
}

// FormatError formats an error message with red color.
func (h *HookFormatter) FormatError(message string) string {
	return fmt.Sprintf("%s%s%s", ansiRed, message, ansiReset)
}

// FormatBlockingError formats a blocking error message for Claude Code.
func (h *HookFormatter) FormatBlockingError(format string, args ...any) string {
	message := fmt.Sprintf(format, args...)
	return h.FormatError(message)
}

// FormatTestPass formats a test pass message for Claude Code.
func (h *HookFormatter) FormatTestPass() string {
	return h.FormatWarning("👉 Tests pass. Continue with your task.")
}

// FormatLintPass formats a lint pass message for Claude Code.
func (h *HookFormatter) FormatLintPass() string {
	return h.FormatWarning("👉 Lints pass. Continue with your task.")
}

// FormatValidationPass formats a validation pass message for Claude Code.
func (h *HookFormatter) FormatValidationPass() string {
	return h.FormatWarning("👉 Validations pass. Continue with your task.")
}
