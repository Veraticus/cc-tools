package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateResult_FormatMessage(t *testing.T) {
	tests := []struct {
		name         string
		result       *ValidateResult
		wantEmpty    bool
		wantContains []string
	}{
		{
			name: "both passed",
			result: &ValidateResult{
				BothPassed: true,
			},
			wantContains: []string{"Validations pass"},
		},
		{
			name: "lint failed only",
			result: &ValidateResult{
				LintResult: &ValidationResult{
					Success: false,
					Command: &DiscoveredCommand{
						Command:    "make",
						Args:       []string{"lint"},
						WorkingDir: "/project",
					},
				},
				TestResult: &ValidationResult{
					Success: true,
					Command: &DiscoveredCommand{
						Command: "make",
						Args:    []string{"test"},
					},
				},
				BothPassed: false,
			},
			wantContains: []string{"BLOCKING", "lint failures", "make lint"},
		},
		{
			name: "test failed only",
			result: &ValidateResult{
				LintResult: &ValidationResult{
					Success: true,
					Command: &DiscoveredCommand{
						Command: "make",
						Args:    []string{"lint"},
					},
				},
				TestResult: &ValidationResult{
					Success: false,
					Command: &DiscoveredCommand{
						Command:    "make",
						Args:       []string{"test"},
						WorkingDir: "/project",
					},
				},
				BothPassed: false,
			},
			wantContains: []string{"BLOCKING", "test failures", "make test"},
		},
		{
			name: "both failed",
			result: &ValidateResult{
				LintResult: &ValidationResult{
					Success: false,
					Command: &DiscoveredCommand{
						Command:    "make",
						Args:       []string{"lint"},
						WorkingDir: "/project",
					},
				},
				TestResult: &ValidationResult{
					Success: false,
					Command: &DiscoveredCommand{
						Command:    "make",
						Args:       []string{"test"},
						WorkingDir: "/project",
					},
				},
				BothPassed: false,
			},
			wantContains: []string{"BLOCKING", "Lint and test failures", "make lint", "make test"},
		},
		{
			name: "no commands found",
			result: &ValidateResult{
				BothPassed: true,
			},
			wantContains: []string{"Validations pass"},
		},
		{
			name: "only lint found and passed",
			result: &ValidateResult{
				LintResult: &ValidationResult{
					Success: true,
					Command: &DiscoveredCommand{
						Command: "make",
						Args:    []string{"lint"},
					},
				},
				TestResult: nil,
				BothPassed: true,
			},
			wantContains: []string{"Validations pass"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.result.FormatMessage()

			// Remove ANSI color codes for testing
			message = strings.ReplaceAll(message, "\033[0;33m", "")
			message = strings.ReplaceAll(message, "\033[0;31m", "")
			message = strings.ReplaceAll(message, "\033[0;32m", "")
			message = strings.ReplaceAll(message, "\033[0m", "")

			if tt.wantEmpty && message != "" {
				t.Errorf("FormatMessage() = %q, want empty", message)
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(message, want) {
					t.Errorf("FormatMessage() = %q, want to contain %q", message, want)
				}
			}
		})
	}
}

//nolint:cyclop // table-driven test with multiple scenarios
func TestParallelValidateExecutor_ExecuteValidations(t *testing.T) {
	tests := []struct {
		name          string
		setupDeps     func(*TestDependencies)
		wantBothPass  bool
		wantLintFound bool
		wantTestFound bool
	}{
		{
			name: "both commands succeed",
			setupDeps: func(deps *TestDependencies) {
				// Setup filesystem to find Makefile
				deps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, "Makefile") {
						return mockFileInfo{name: "Makefile"}, nil
					}
					return nil, os.ErrNotExist
				}

				// Setup runner for discovery and execution
				deps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					// Handle make dry run for discovery
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("echo lint")}, nil
						}
						if args[len(args)-1] == "test" {
							return &CommandOutput{Stdout: []byte("echo test")}, nil
						}
					}
					// Handle actual execution
					if name == "make" && len(args) == 1 {
						if args[0] == "lint" {
							return &CommandOutput{Stdout: []byte("Linting...")}, nil
						}
						if args[0] == "test" {
							return &CommandOutput{Stdout: []byte("Testing...")}, nil
						}
					}
					return nil, fmt.Errorf("command failed")
				}
			},
			wantBothPass:  true,
			wantLintFound: true,
			wantTestFound: true,
		},
		{
			name: "lint fails test passes",
			setupDeps: func(deps *TestDependencies) {
				// Setup filesystem
				deps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, "Makefile") {
						return mockFileInfo{name: "Makefile"}, nil
					}
					return nil, os.ErrNotExist
				}

				// Setup runner
				deps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					// Handle make dry run for discovery
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("echo lint")}, nil
						}
						if args[len(args)-1] == "test" {
							return &CommandOutput{Stdout: []byte("echo test")}, nil
						}
					}
					// Handle actual execution
					if name == "make" && len(args) == 1 {
						if args[0] == "lint" {
							return &CommandOutput{Stderr: []byte("Lint error")}, fmt.Errorf("exit status 1")
						}
						if args[0] == "test" {
							return &CommandOutput{Stdout: []byte("Tests pass")}, nil
						}
					}
					return nil, fmt.Errorf("command failed")
				}
			},
			wantBothPass:  false,
			wantLintFound: true,
			wantTestFound: true,
		},
		{
			name: "no commands found",
			setupDeps: func(deps *TestDependencies) {
				// No project files
				deps.MockFS.statFunc = func(_ string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				}
				deps.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
					return nil, fmt.Errorf("command failed")
				}
			},
			wantBothPass:  true,
			wantLintFound: false,
			wantTestFound: false,
		},
		{
			name: "only lint command found",
			setupDeps: func(deps *TestDependencies) {
				// Setup filesystem
				deps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, "Makefile") {
						return mockFileInfo{name: "Makefile"}, nil
					}
					return nil, os.ErrNotExist
				}

				// Setup runner - only lint available
				deps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("echo lint")}, nil
						}
						if args[len(args)-1] == "test" {
							return nil, fmt.Errorf("target not found")
						}
					}
					if name == "make" && len(args) == 1 && args[0] == "lint" {
						return &CommandOutput{Stdout: []byte("Linting...")}, nil
					}
					return nil, fmt.Errorf("command failed")
				}
			},
			wantBothPass:  true,
			wantLintFound: true,
			wantTestFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDeps := createTestDependencies()
			tt.setupDeps(testDeps)

			executor := NewParallelValidateExecutor("/project", 10, false, nil, testDeps.Dependencies)
			result, err := executor.ExecuteValidations(context.Background(), "/project", "/project")

			if err != nil {
				t.Fatalf("ExecuteValidations() error = %v", err)
			}

			if result.BothPassed != tt.wantBothPass {
				t.Errorf("BothPassed = %v, want %v", result.BothPassed, tt.wantBothPass)
			}

			if (result.LintResult != nil) != tt.wantLintFound {
				t.Errorf("LintResult found = %v, want %v", result.LintResult != nil, tt.wantLintFound)
			}

			if (result.TestResult != nil) != tt.wantTestFound {
				t.Errorf("TestResult found = %v, want %v", result.TestResult != nil, tt.wantTestFound)
			}
		})
	}
}

func TestRunValidateHook(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		setupDeps    func(*TestDependencies)
		wantExitCode int
	}{
		{
			name: "successful validation",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`,
			setupDeps: func(deps *TestDependencies) {
				// Setup filesystem
				deps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, ".git") || strings.HasSuffix(path, "Makefile") {
						return mockFileInfo{name: filepath.Base(path)}, nil
					}
					return nil, os.ErrNotExist
				}

				// Setup runner
				deps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						return &CommandOutput{Stdout: []byte("echo OK")}, nil
					}
					if name == "make" && len(args) == 1 {
						return &CommandOutput{Stdout: []byte("OK")}, nil
					}
					return nil, fmt.Errorf("command failed")
				}
			},
			wantExitCode: 2, // ExitCodeShowMessage for success
		},
		{
			name: "validation failures",
			input: `{
				"hook_event_name": "PostToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`,
			setupDeps: func(deps *TestDependencies) {
				// Setup filesystem
				deps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, ".git") || strings.HasSuffix(path, "Makefile") {
						return mockFileInfo{name: filepath.Base(path)}, nil
					}
					return nil, os.ErrNotExist
				}

				// Setup runner
				deps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						return &CommandOutput{Stdout: []byte("echo cmd")}, nil
					}
					if name == "make" && len(args) == 1 {
						if args[0] == "lint" {
							return nil, fmt.Errorf("exit status 1")
						}
						return &CommandOutput{Stdout: []byte("OK")}, nil
					}
					return nil, fmt.Errorf("command failed")
				}
			},
			wantExitCode: 2, // ExitCodeShowMessage for failure
		},
		{
			name:         "invalid input",
			input:        "not json",
			setupDeps:    func(_ *TestDependencies) {},
			wantExitCode: 0,
		},
		{
			name: "wrong event type",
			input: `{
				"hook_event_name": "PreToolUse",
				"tool_name": "Edit",
				"tool_input": {"file_path": "/project/main.go"}
			}`,
			setupDeps:    func(_ *TestDependencies) {},
			wantExitCode: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDeps := createTestDependencies()
			tt.setupDeps(testDeps)

			// Setup input
			testDeps.MockInput.readAllFunc = func() ([]byte, error) {
				return []byte(tt.input), nil
			}

			exitCode := RunValidateHook(
				context.Background(),
				false, // debug
				10,    // timeout
				2,     // cooldown
				testDeps.Dependencies,
			)

			if exitCode != tt.wantExitCode {
				t.Errorf("RunValidateHook() = %v, want %v", exitCode, tt.wantExitCode)
			}
		})
	}
}

func TestValidateExecutor_Parallelism(t *testing.T) {
	testDeps := createTestDependencies()

	// Setup filesystem
	testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, "Makefile") {
			return mockFileInfo{name: "Makefile"}, nil
		}
		return nil, os.ErrNotExist
	}

	// Track execution order with mutex for thread safety
	var executionOrder []string
	var mu sync.Mutex
	testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
		fullCmd := fmt.Sprintf("%s %s", name, strings.Join(args, " "))

		// Handle discovery
		if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
			return &CommandOutput{Stdout: []byte("echo cmd")}, nil
		}

		// Handle execution and track order with mutex
		if name == "make" && len(args) == 1 {
			mu.Lock()
			executionOrder = append(executionOrder, fullCmd)
			mu.Unlock()
			return &CommandOutput{Stdout: []byte("OK")}, nil
		}

		return nil, fmt.Errorf("unknown command: %s", fullCmd)
	}

	executor := NewParallelValidateExecutor("/project", 10, false, nil, testDeps.Dependencies)
	result, err := executor.ExecuteValidations(context.Background(), "/project", "/project")

	if err != nil {
		t.Fatalf("ExecuteValidations() error = %v", err)
	}

	if !result.BothPassed {
		t.Error("Expected both validations to pass")
	}

	// Verify both commands were executed
	if len(executionOrder) < 2 {
		t.Errorf("Expected at least 2 commands to be executed, got %d", len(executionOrder))
	}

	// Check that both lint and test were executed (order doesn't matter due to parallelism)
	hasLint := false
	hasTest := false
	for _, cmd := range executionOrder {
		if strings.Contains(cmd, "lint") {
			hasLint = true
		}
		if strings.Contains(cmd, "test") {
			hasTest = true
		}
	}

	if !hasLint || !hasTest {
		t.Errorf("Expected both lint and test to be executed. Commands: %v", executionOrder)
	}
}
