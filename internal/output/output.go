// Package output provides a unified interface for terminal output in cc-tools.
package output

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Level represents the severity/type of output message.
type Level int

const (
	// Info is for general information messages.
	Info Level = iota
	// Success indicates successful operations.
	Success
	// Warning indicates non-critical issues or important notices.
	Warning
	// Error indicates failures or problems.
	Error
	// Debug is for debugging information.
	Debug
)

// Writer is the core interface for output destinations.
type Writer interface {
	Write(message string) error
	WriteError(message string) error
}

// Terminal provides beautiful terminal output using lipgloss.
type Terminal struct {
	stdout io.Writer
	stderr io.Writer
	styles map[Level]lipgloss.Style
}

// NewTerminal creates a new Terminal with default styling.
func NewTerminal(stdout, stderr io.Writer) *Terminal {
	return &Terminal{
		stdout: stdout,
		stderr: stderr,
		styles: defaultStyles(),
	}
}

// defaultStyles returns the default lipgloss styles for each level.
func defaultStyles() map[Level]lipgloss.Style {
	return map[Level]lipgloss.Style{
		Info:    lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb")), // Sky blue
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")), // Green
		Warning: lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")), // Yellow
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")), // Red
		Debug:   lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")), // Teal
	}
}

// Write writes a plain message to stdout.
func (t *Terminal) Write(message string) error {
	_, err := fmt.Fprintln(t.stdout, message)
	if err != nil {
		return fmt.Errorf("write to stdout: %w", err)
	}
	return nil
}

// WriteError writes a plain message to stderr.
func (t *Terminal) WriteError(message string) error {
	_, err := fmt.Fprintln(t.stderr, message)
	if err != nil {
		return fmt.Errorf("write to stderr: %w", err)
	}
	return nil
}

// Print writes a formatted message at the given level to stdout.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) Print(level Level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	styled := t.styles[level].Render(msg)
	if err := t.Write(styled); err != nil {
		// If we can't write output, exit immediately
		os.Exit(1)
	}
}

// PrintError writes a formatted message at the given level to stderr.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) PrintError(level Level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	styled := t.styles[level].Render(msg)
	if err := t.WriteError(styled); err != nil {
		// If we can't write errors, exit immediately
		os.Exit(1)
	}
}

// Info writes an info message to stdout.
func (t *Terminal) Info(format string, args ...any) {
	t.Print(Info, format, args...)
}

// Success writes a success message to stdout.
func (t *Terminal) Success(format string, args ...any) {
	t.Print(Success, format, args...)
}

// Warning writes a warning message to stdout.
func (t *Terminal) Warning(format string, args ...any) {
	t.Print(Warning, format, args...)
}

// Error writes an error message to stderr.
func (t *Terminal) Error(format string, args ...any) {
	t.PrintError(Error, format, args...)
}

// Debug writes a debug message to stderr.
func (t *Terminal) Debug(format string, args ...any) {
	t.PrintError(Debug, format, args...)
}

// Raw writes a raw string without any formatting to stdout.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) Raw(s string) {
	if _, err := fmt.Fprint(t.stdout, s); err != nil {
		os.Exit(1)
	}
}

// RawError writes a raw string without any formatting to stderr.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) RawError(s string) {
	if _, err := fmt.Fprint(t.stderr, s); err != nil {
		os.Exit(1)
	}
}

// Style returns a styled string at the given level without writing it.
func (t *Terminal) Style(level Level, format string, args ...any) string {
	msg := fmt.Sprintf(format, args...)
	return t.styles[level].Render(msg)
}
