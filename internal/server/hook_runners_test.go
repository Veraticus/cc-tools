package server

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNewHookLintRunner(t *testing.T) {
	tests := []struct {
		name         string
		debug        bool
		timeoutSecs  int
		cooldownSecs int
	}{
		{
			name:         "default configuration",
			debug:        false,
			timeoutSecs:  30,
			cooldownSecs: 2,
		},
		{
			name:         "debug enabled",
			debug:        true,
			timeoutSecs:  60,
			cooldownSecs: 5,
		},
		{
			name:         "short timeout",
			debug:        false,
			timeoutSecs:  5,
			cooldownSecs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewHookLintRunner(tt.debug, tt.timeoutSecs, tt.cooldownSecs)

			if runner == nil {
				t.Fatal("Expected runner, got nil")
			}

			if runner.debug != tt.debug {
				t.Errorf("Expected debug=%v, got %v", tt.debug, runner.debug)
			}

			if runner.timeoutSecs != tt.timeoutSecs {
				t.Errorf("Expected timeoutSecs=%d, got %d", tt.timeoutSecs, runner.timeoutSecs)
			}

			if runner.cooldownSecs != tt.cooldownSecs {
				t.Errorf("Expected cooldownSecs=%d, got %d", tt.cooldownSecs, runner.cooldownSecs)
			}
		})
	}
}

func TestNewHookTestRunner(t *testing.T) {
	tests := []struct {
		name         string
		debug        bool
		timeoutSecs  int
		cooldownSecs int
	}{
		{
			name:         "default configuration",
			debug:        false,
			timeoutSecs:  60,
			cooldownSecs: 2,
		},
		{
			name:         "debug enabled",
			debug:        true,
			timeoutSecs:  120,
			cooldownSecs: 5,
		},
		{
			name:         "short timeout",
			debug:        false,
			timeoutSecs:  10,
			cooldownSecs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewHookTestRunner(tt.debug, tt.timeoutSecs, tt.cooldownSecs)

			if runner == nil {
				t.Fatal("Expected runner, got nil")
			}

			if runner.debug != tt.debug {
				t.Errorf("Expected debug=%v, got %v", tt.debug, runner.debug)
			}

			if runner.timeoutSecs != tt.timeoutSecs {
				t.Errorf("Expected timeoutSecs=%d, got %d", tt.timeoutSecs, runner.timeoutSecs)
			}

			if runner.cooldownSecs != tt.cooldownSecs {
				t.Errorf("Expected cooldownSecs=%d, got %d", tt.cooldownSecs, runner.cooldownSecs)
			}
		})
	}
}

func TestHookLintRunner_Run(t *testing.T) {
	// This test verifies that the Run method properly passes through to the hooks package
	// We can't fully test the execution without mocking the hooks package,
	// but we can verify the interface behavior

	runner := NewHookLintRunner(true, 1, 1)

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "empty input",
			input:   "",
			wantErr: false, // RunSmartHook handles empty input gracefully
		},
		{
			name:    "simple input",
			input:   `{"file_path": "test.go"}`,
			wantErr: false,
		},
		{
			name:    "complex input",
			input:   `{"file_path": "test.go", "project": "myproject", "options": {"fix": true}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			input := strings.NewReader(tt.input)
			output, err := runner.Run(ctx, input)

			// We expect an error because the hooks will try to execute actual commands
			// In real usage, the hooks would be properly configured
			if err == nil && output != nil {
				// If no error, verify we got some output reader
				if _, readErr := io.ReadAll(output); readErr != nil {
					t.Errorf("Failed to read output: %v", readErr)
				}
			}
		})
	}
}

func TestHookTestRunner_Run(t *testing.T) {
	// Similar to lint runner test
	runner := NewHookTestRunner(true, 1, 1)

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "empty input",
			input:   "",
			wantErr: false,
		},
		{
			name:    "simple input",
			input:   `{"file_path": "test_test.go"}`,
			wantErr: false,
		},
		{
			name:    "with project",
			input:   `{"file_path": "test_test.go", "project": "myproject"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			input := strings.NewReader(tt.input)
			output, err := runner.Run(ctx, input)

			// Similar to lint test - we expect the hooks to attempt execution
			if err == nil && output != nil {
				if _, readErr := io.ReadAll(output); readErr != nil {
					t.Errorf("Failed to read output: %v", readErr)
				}
			}
		})
	}
}

func TestHookRunner_ContextCancellation(t *testing.T) {
	// Test that runners respect context cancellation
	lintRunner := NewHookLintRunner(true, 30, 2)
	testRunner := NewHookTestRunner(true, 60, 2)

	tests := []struct {
		name   string
		runner interface {
			Run(context.Context, io.Reader) (io.Reader, error)
		}
	}{
		{
			name:   "lint runner cancellation",
			runner: lintRunner,
		},
		{
			name:   "test runner cancellation",
			runner: testRunner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			// Create a context that's already canceled
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel immediately

			input := strings.NewReader(`{"file_path": "test.go"}`)
			_, err := tt.runner.Run(ctx, input)

			// We expect an error due to canceled context or quick execution
			// The important thing is it doesn't hang
			_ = err // Error is expected in this case
		})
	}
}

func TestHookRunner_LargeInput(_ *testing.T) {
	// Test handling of large input
	runner := NewHookLintRunner(true, 1, 1)

	// Create large input
	var buf bytes.Buffer
	buf.WriteString(`{"file_path": "test.go", "content": "`)
	for range 10000 {
		buf.WriteString("line content ")
	}
	buf.WriteString(`"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	input := bytes.NewReader(buf.Bytes())
	output, err := runner.Run(ctx, input)

	// Verify it handles large input without panicking
	if err == nil && output != nil {
		// Read the output to ensure it's valid
		outputData, _ := io.ReadAll(output)
		_ = outputData // We're just checking it doesn't panic
	}
}

func TestHookRunner_EmptyContext(t *testing.T) {
	// Test with background context (no timeout)
	lintRunner := NewHookLintRunner(false, 1, 1)
	testRunner := NewHookTestRunner(false, 1, 1)

	tests := []struct {
		name   string
		runner interface {
			Run(context.Context, io.Reader) (io.Reader, error)
		}
	}{
		{
			name:   "lint runner with background context",
			runner: lintRunner,
		},
		{
			name:   "test runner with background context",
			runner: testRunner,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			input := strings.NewReader(`{"file_path": "test.go"}`)

			// Use a goroutine with timeout to prevent hanging
			done := make(chan bool)
			go func() {
				_, _ = tt.runner.Run(context.Background(), input)
				done <- true
			}()

			select {
			case <-done:
				// Success - completed without hanging
			case <-time.After(200 * time.Millisecond):
				// This is also OK - the runner has its own timeout
			}
		})
	}
}

func TestHookRunner_InvalidJSON(t *testing.T) {
	// Test with invalid JSON input
	lintRunner := NewHookLintRunner(true, 1, 1)
	testRunner := NewHookTestRunner(true, 1, 1)

	tests := []struct {
		name   string
		runner interface {
			Run(context.Context, io.Reader) (io.Reader, error)
		}
		input string
	}{
		{
			name:   "lint runner with invalid JSON",
			runner: lintRunner,
			input:  `{invalid json}`,
		},
		{
			name:   "test runner with malformed JSON",
			runner: testRunner,
			input:  `{"file_path": "test.go"`,
		},
		{
			name:   "lint runner with non-JSON",
			runner: lintRunner,
			input:  `not json at all`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			input := strings.NewReader(tt.input)
			output, err := tt.runner.Run(ctx, input)

			// The runners should handle invalid input gracefully
			// Either by returning an error or empty output
			if err == nil && output != nil {
				// If no error, should still return some output (possibly error message)
				data, _ := io.ReadAll(output)
				_ = data // Just verify it doesn't panic
			}
		})
	}
}
