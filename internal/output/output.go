// Package output provides a unified interface for terminal output in cc-tools.
package output

import (
	"fmt"
	"io"
	"os"
	"sync"

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
// Terminal is safe for concurrent use by multiple goroutines: writes to
// stdout and stderr are each serialized by an internal mutex.
type Terminal struct {
	mu     sync.Mutex
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
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := fmt.Fprintln(t.stdout, message)
	if err != nil {
		return fmt.Errorf("write to stdout: %w", err)
	}
	return nil
}

// WriteError writes a plain message to stderr.
func (t *Terminal) WriteError(message string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := fmt.Fprintln(t.stderr, message)
	if err != nil {
		return fmt.Errorf("write to stderr: %w", err)
	}
	return nil
}

// Printf writes a formatted message at the given level to stdout.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) Printf(level Level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	styled := t.styles[level].Render(msg)
	if err := t.Write(styled); err != nil {
		// If we can't write output, exit immediately
		os.Exit(1)
	}
}

// PrintErrorf writes a formatted message at the given level to stderr.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) PrintErrorf(level Level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	styled := t.styles[level].Render(msg)
	if err := t.WriteError(styled); err != nil {
		// If we can't write errors, exit immediately
		os.Exit(1)
	}
}

// Infof writes an info message to stdout.
func (t *Terminal) Infof(format string, args ...any) {
	t.Printf(Info, format, args...)
}

// Successf writes a success message to stdout.
func (t *Terminal) Successf(format string, args ...any) {
	t.Printf(Success, format, args...)
}

// Warningf writes a warning message to stdout.
func (t *Terminal) Warningf(format string, args ...any) {
	t.Printf(Warning, format, args...)
}

// Errorf writes an error message to stderr.
func (t *Terminal) Errorf(format string, args ...any) {
	t.PrintErrorf(Error, format, args...)
}

// Debugf writes a debug message to stderr.
func (t *Terminal) Debugf(format string, args ...any) {
	t.PrintErrorf(Debug, format, args...)
}

// Raw writes a raw string without any formatting to stdout.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) Raw(s string) {
	t.mu.Lock()
	_, err := fmt.Fprint(t.stdout, s)
	t.mu.Unlock()
	if err != nil {
		os.Exit(1)
	}
}

// RawError writes a raw string without any formatting to stderr.
// Following Go's fmt.Print pattern, this exits on write failure.
func (t *Terminal) RawError(s string) {
	t.mu.Lock()
	_, err := fmt.Fprint(t.stderr, s)
	t.mu.Unlock()
	if err != nil {
		os.Exit(1)
	}
}

// Style returns a styled string at the given level without writing it.
func (t *Terminal) Style(level Level, format string, args ...any) string {
	msg := fmt.Sprintf(format, args...)
	return t.styles[level].Render(msg)
}
