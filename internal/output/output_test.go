package output

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestNewTerminal(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	term := NewTerminal(stdout, stderr)

	if term == nil {
		t.Fatal("NewTerminal() returned nil")
	}

	if term.stdout != stdout {
		t.Error("stdout not set correctly")
	}

	if term.stderr != stderr {
		t.Error("stderr not set correctly")
	}

	if term.styles == nil {
		t.Error("styles should be initialized")
	}

	// Check that all levels have styles
	for level := Info; level <= Debug; level++ {
		if _, ok := term.styles[level]; !ok {
			t.Errorf("Missing style for level %d", level)
		}
	}
}

func TestDefaultStyles(t *testing.T) {
	styles := defaultStyles()

	// Check that all levels have styles
	expectedLevels := []Level{Info, Success, Warning, Error, Debug}

	for _, level := range expectedLevels {
		style, ok := styles[level]
		if !ok {
			t.Errorf("Missing style for level %d", level)
		}

		// Verify it's a valid lipgloss style
		// Try to render something to ensure the style is valid
		rendered := style.Render("test")
		if rendered == "" {
			t.Errorf("Style for level %d rendered empty string", level)
		}
	}

	// Verify we have exactly the expected number of styles
	if len(styles) != len(expectedLevels) {
		t.Errorf("Expected %d styles, got %d", len(expectedLevels), len(styles))
	}
}

func TestTerminalWrite(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantErr bool
	}{
		{
			name:    "writes simple message",
			message: "Hello, World!",
		},
		{
			name:    "writes empty message",
			message: "",
		},
		{
			name:    "writes message with newlines",
			message: "Line 1\nLine 2",
		},
		{
			name:    "writes message with special characters",
			message: "Special: !@#$%^&*()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			err := term.Write(tt.message)
			if (err != nil) != tt.wantErr {
				t.Errorf("Write() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				// Check message was written to stdout with newline
				expected := tt.message + "\n"
				if stdout.String() != expected {
					t.Errorf("stdout = %q, want %q", stdout.String(), expected)
				}

				// Check nothing was written to stderr
				if stderr.Len() > 0 {
					t.Errorf("stderr should be empty, got %q", stderr.String())
				}
			}
		})
	}
}

func TestTerminalWriteError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantErr bool
	}{
		{
			name:    "writes error message",
			message: "Error occurred",
		},
		{
			name:    "writes empty error",
			message: "",
		},
		{
			name:    "writes multiline error",
			message: "Error:\nDetails here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			err := term.WriteError(tt.message)
			if (err != nil) != tt.wantErr {
				t.Errorf("WriteError() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				// Check message was written to stderr with newline
				expected := tt.message + "\n"
				if stderr.String() != expected {
					t.Errorf("stderr = %q, want %q", stderr.String(), expected)
				}

				// Check nothing was written to stdout
				if stdout.Len() > 0 {
					t.Errorf("stdout should be empty, got %q", stdout.String())
				}
			}
		})
	}
}

func TestTerminalPrint(t *testing.T) {
	tests := []struct {
		name   string
		level  Level
		format string
		args   []any
	}{
		{
			name:   "prints info message",
			level:  Info,
			format: "Info: %s",
			args:   []any{"test"},
		},
		{
			name:   "prints success message",
			level:  Success,
			format: "Success: %d items",
			args:   []any{42},
		},
		{
			name:   "prints warning message",
			level:  Warning,
			format: "Warning: %v",
			args:   []any{true},
		},
		{
			name:   "prints with no args",
			level:  Info,
			format: "Simple message",
			args:   []any{},
		},
		{
			name:   "prints with multiple args",
			level:  Info,
			format: "%s: %d of %d",
			args:   []any{"Progress", 5, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			// Print should not panic
			term.Printf(tt.level, tt.format, tt.args...)

			// Check something was written to stdout
			if stdout.Len() == 0 {
				t.Error("Nothing written to stdout")
			}

			// Check the formatted message is in the output
			expectedMsg := formatMessage(tt.format, tt.args...)
			if !strings.Contains(stdout.String(), expectedMsg) {
				t.Errorf("stdout should contain %q, got %q", expectedMsg, stdout.String())
			}

			// Check nothing was written to stderr
			if stderr.Len() > 0 {
				t.Errorf("stderr should be empty, got %q", stderr.String())
			}
		})
	}
}

func TestTerminalPrintError(t *testing.T) {
	tests := []struct {
		name   string
		level  Level
		format string
		args   []any
	}{
		{
			name:   "prints error level",
			level:  Error,
			format: "Error: %s",
			args:   []any{"failed"},
		},
		{
			name:   "prints debug level",
			level:  Debug,
			format: "Debug: %v",
			args:   []any{map[string]int{"key": 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			// PrintError should not panic
			term.PrintErrorf(tt.level, tt.format, tt.args...)

			// Check something was written to stderr
			if stderr.Len() == 0 {
				t.Error("Nothing written to stderr")
			}

			// Check the formatted message is in the output
			expectedMsg := formatMessage(tt.format, tt.args...)
			if !strings.Contains(stderr.String(), expectedMsg) {
				t.Errorf("stderr should contain %q, got %q", expectedMsg, stderr.String())
			}

			// Check nothing was written to stdout
			if stdout.Len() > 0 {
				t.Errorf("stdout should be empty, got %q", stdout.String())
			}
		})
	}
}

func TestTerminalConvenienceMethods(t *testing.T) {
	tests := []struct {
		name         string
		method       func(*Terminal, string, ...any)
		format       string
		args         []any
		expectStdout bool
		expectStderr bool
	}{
		{
			name:         "Info writes to stdout",
			method:       (*Terminal).Infof,
			format:       "Info: %s",
			args:         []any{"message"},
			expectStdout: true,
			expectStderr: false,
		},
		{
			name:         "Success writes to stdout",
			method:       (*Terminal).Successf,
			format:       "Success: %d",
			args:         []any{100},
			expectStdout: true,
			expectStderr: false,
		},
		{
			name:         "Warning writes to stdout",
			method:       (*Terminal).Warningf,
			format:       "Warning: %v",
			args:         []any{true},
			expectStdout: true,
			expectStderr: false,
		},
		{
			name:         "Error writes to stderr",
			method:       (*Terminal).Errorf,
			format:       "Error: %s",
			args:         []any{"failed"},
			expectStdout: false,
			expectStderr: true,
		},
		{
			name:         "Debug writes to stderr",
			method:       (*Terminal).Debugf,
			format:       "Debug: %d",
			args:         []any{42},
			expectStdout: false,
			expectStderr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			// Call the method
			tt.method(term, tt.format, tt.args...)

			// Check output destinations
			if tt.expectStdout && stdout.Len() == 0 {
				t.Error("Expected output to stdout but got none")
			}
			if !tt.expectStdout && stdout.Len() > 0 {
				t.Errorf("Unexpected stdout output: %q", stdout.String())
			}

			if tt.expectStderr && stderr.Len() == 0 {
				t.Error("Expected output to stderr but got none")
			}
			if !tt.expectStderr && stderr.Len() > 0 {
				t.Errorf("Unexpected stderr output: %q", stderr.String())
			}

			// Check the message is formatted correctly
			expectedMsg := formatMessage(tt.format, tt.args...)
			output := stdout.String() + stderr.String()
			if !strings.Contains(output, expectedMsg) {
				t.Errorf("Output should contain %q, got %q", expectedMsg, output)
			}
		})
	}
}

func TestTerminalRaw(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "writes raw string",
			message: "Raw output",
		},
		{
			name:    "writes empty string",
			message: "",
		},
		{
			name:    "writes string without adding newline",
			message: "No newline",
		},
		{
			name:    "writes string with existing newline",
			message: "Has newline\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			// Raw should not panic
			term.Raw(tt.message)

			// Check exact output (no newline added)
			if stdout.String() != tt.message {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.message)
			}

			// Check nothing written to stderr
			if stderr.Len() > 0 {
				t.Errorf("stderr should be empty, got %q", stderr.String())
			}
		})
	}
}

func TestTerminalRawError(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "writes raw error string",
			message: "Raw error",
		},
		{
			name:    "writes empty error string",
			message: "",
		},
		{
			name:    "writes error without adding newline",
			message: "No newline error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			// RawError should not panic
			term.RawError(tt.message)

			// Check exact output (no newline added)
			if stderr.String() != tt.message {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.message)
			}

			// Check nothing written to stdout
			if stdout.Len() > 0 {
				t.Errorf("stdout should be empty, got %q", stdout.String())
			}
		})
	}
}

func TestTerminalStyle(t *testing.T) {
	tests := []struct {
		name   string
		level  Level
		format string
		args   []any
	}{
		{
			name:   "styles info message",
			level:  Info,
			format: "Info: %s",
			args:   []any{"test"},
		},
		{
			name:   "styles success message",
			level:  Success,
			format: "Success!",
			args:   []any{},
		},
		{
			name:   "styles warning message",
			level:  Warning,
			format: "Warning: %d%%",
			args:   []any{75},
		},
		{
			name:   "styles error message",
			level:  Error,
			format: "Error: %v",
			args:   []any{errors.New("test error")},
		},
		{
			name:   "styles debug message",
			level:  Debug,
			format: "Debug: %+v",
			args:   []any{struct{ Name string }{"test"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			term := NewTerminal(stdout, stderr)

			styled := term.Style(tt.level, tt.format, tt.args...)

			// Check that something was returned
			if styled == "" {
				t.Error("Style() returned empty string")
			}

			// Check that the formatted message is in the styled output
			expectedMsg := formatMessage(tt.format, tt.args...)
			if !strings.Contains(styled, expectedMsg) {
				t.Errorf("Styled output should contain %q, got %q", expectedMsg, styled)
			}

			// Verify nothing was written to stdout or stderr
			if stdout.Len() > 0 {
				t.Error("Style() should not write to stdout")
			}
			if stderr.Len() > 0 {
				t.Error("Style() should not write to stderr")
			}
		})
	}
}

func TestWriterInterface(t *testing.T) {
	// Verify Terminal implements Writer interface
	var _ Writer = (*Terminal)(nil)

	// Test using the interface
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	var w Writer = NewTerminal(stdout, stderr)

	// Test Write
	err := w.Write("test message")
	if err != nil {
		t.Errorf("Write() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "test message") {
		t.Error("Write() should write to stdout")
	}

	// Test WriteError
	err = w.WriteError("error message")
	if err != nil {
		t.Errorf("WriteError() error = %v", err)
	}

	if !strings.Contains(stderr.String(), "error message") {
		t.Error("WriteError() should write to stderr")
	}
}

func TestWriteFailureHandling(t *testing.T) {
	// Test behavior when writes fail
	failWriter := &failingWriter{shouldFail: true}

	term := NewTerminal(failWriter, failWriter)

	// Test Write error handling
	err := term.Write("test")
	if err == nil {
		t.Error("Write() should return error when write fails")
	}
	if !strings.Contains(err.Error(), "write to stdout") {
		t.Errorf("Error should mention stdout, got %v", err)
	}

	// Test WriteError error handling
	err = term.WriteError("test")
	if err == nil {
		t.Error("WriteError() should return error when write fails")
	}
	if !strings.Contains(err.Error(), "write to stderr") {
		t.Errorf("Error should mention stderr, got %v", err)
	}
}

func TestColorRendering(t *testing.T) {
	// Force a color profile; lipgloss's default renderer probes stdout for
	// TTY support, and in `go test` stdout is a pipe, which downgrades to
	// plain text and makes every level's "styled" output identical.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	// Test that styles actually apply colors
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	term := NewTerminal(stdout, stderr)

	// Get a styled message
	message := "Test Message"

	// Test each level produces different output
	outputs := make(map[string]bool)

	for level := Info; level <= Debug; level++ {
		styled := term.Style(level, "%s", message)

		// Each level should produce unique output
		if outputs[styled] {
			t.Errorf("Level %d produced duplicate styled output", level)
		}
		outputs[styled] = true

		// The styled output should be different from the plain message
		// (unless lipgloss is in NO_COLOR mode, but we're not testing that)
		// Just verify it contains the message
		if !strings.Contains(styled, message) {
			t.Errorf("Styled output for level %d doesn't contain message", level)
		}
	}
}

func TestConcurrentWrites(t *testing.T) {
	// Test that concurrent writes don't cause issues
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	term := NewTerminal(stdout, stderr)

	done := make(chan bool, 5)

	// Run multiple goroutines writing concurrently
	go func() {
		for i := range 10 {
			term.Infof("Info %d", i)
		}
		done <- true
	}()

	go func() {
		for i := range 10 {
			term.Successf("Success %d", i)
		}
		done <- true
	}()

	go func() {
		for i := range 10 {
			term.Warningf("Warning %d", i)
		}
		done <- true
	}()

	go func() {
		for i := range 10 {
			term.Errorf("Error %d", i)
		}
		done <- true
	}()

	go func() {
		for i := range 10 {
			term.Debugf("Debug %d", i)
		}
		done <- true
	}()

	// Wait for all goroutines
	for range 5 {
		<-done
	}

	// Check that we have output (exact content may be interleaved)
	if stdout.Len() == 0 {
		t.Error("Should have stdout output from concurrent writes")
	}

	if stderr.Len() == 0 {
		t.Error("Should have stderr output from concurrent writes")
	}
}

func TestLipglossIntegration(t *testing.T) {
	// Test that lipgloss styles are properly applied
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	term := NewTerminal(stdout, stderr)

	// Override with known styles for testing
	testStyle := lipgloss.NewStyle().Bold(true)
	term.styles[Info] = testStyle

	message := "Bold Test"
	styled := term.Style(Info, "%s", message)

	// The styled output should be different from the plain message
	// due to the bold styling (unless NO_COLOR is set)
	if styled == message {
		// This might happen in NO_COLOR mode, which is OK
		t.Skip("Lipgloss styling not applied (possibly NO_COLOR mode)")
	}

	// Verify the message is still in the styled output
	if !strings.Contains(styled, message) {
		t.Errorf("Styled output should contain the message, got %q", styled)
	}
}

// Helper types and functions

type failingWriter struct {
	shouldFail bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.shouldFail {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func formatMessage(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
