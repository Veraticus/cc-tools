package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

// mockSkipStorage implements skipregistry.Storage for testing.
type mockSkipStorage struct {
	data skipregistry.RegistryData
	err  error
}

func (m *mockSkipStorage) Load(_ context.Context) (skipregistry.RegistryData, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.data == nil {
		return make(skipregistry.RegistryData), nil
	}
	return m.data, nil
}

func (m *mockSkipStorage) Save(_ context.Context, data skipregistry.RegistryData) error {
	if m.err != nil {
		return m.err
	}
	m.data = data
	return nil
}

func TestExecuteValidationsWithSkip(t *testing.T) { //nolint:cyclop // Table-driven test
	tests := []struct {
		name       string
		skipConfig *SkipConfig
		setupMocks func(*TestDependencies)
		wantBoth   bool
	}{
		{
			name:       "no skip config runs both",
			skipConfig: nil,
			setupMocks: func(td *TestDependencies) {
				// Setup file system for project root detection
				td.MockFS.statFunc = func(name string) (os.FileInfo, error) {
					if name == "/project/.git" || name == "/project/go.mod" || name == "/project/Makefile" {
						return &mockFileInfo{isDir: true}, nil
					}
					return nil, fmt.Errorf("not found")
				}

				// Setup runner for command discovery and execution
				td.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					// Handle make dry run for discovery
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil
						}
						if args[len(args)-1] == "test" {
							return &CommandOutput{Stdout: []byte("go test")}, nil
						}
					}
					// Handle actual execution
					if name == "make" && len(args) == 1 {
						return &CommandOutput{Stdout: []byte("Success")}, nil
					}
					return nil, fmt.Errorf("unexpected command")
				}
				td.MockRunner.lookPathFunc = func(file string) (string, error) {
					if file == "make" {
						return "/usr/bin/make", nil
					}
					return "", fmt.Errorf("not found")
				}
			},
			wantBoth: true,
		},
		{
			name: "skip lint only runs test",
			skipConfig: &SkipConfig{
				SkipLint: true,
				SkipTest: false,
			},
			setupMocks: func(td *TestDependencies) {
				td.MockFS.statFunc = func(name string) (os.FileInfo, error) {
					if name == "/project/.git" || name == "/project/go.mod" || name == "/project/Makefile" {
						return &mockFileInfo{isDir: true}, nil
					}
					return nil, fmt.Errorf("not found")
				}

				// Only test command should be discovered and run (lint is skipped)
				td.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "test" {
							return &CommandOutput{Stdout: []byte("go test")}, nil
						}
					}
					if name == "make" && len(args) == 1 && args[0] == "test" {
						return &CommandOutput{Stdout: []byte("Test Success")}, nil
					}
					return nil, fmt.Errorf("unexpected command")
				}
				td.MockRunner.lookPathFunc = func(file string) (string, error) {
					if file == "make" {
						return "/usr/bin/make", nil
					}
					return "", fmt.Errorf("not found")
				}
			},
			wantBoth: true,
		},
		{
			name: "skip test only runs lint",
			skipConfig: &SkipConfig{
				SkipLint: false,
				SkipTest: true,
			},
			setupMocks: func(td *TestDependencies) {
				td.MockFS.statFunc = func(name string) (os.FileInfo, error) {
					if name == "/project/.git" || name == "/project/go.mod" || name == "/project/Makefile" {
						return &mockFileInfo{isDir: true}, nil
					}
					return nil, fmt.Errorf("not found")
				}

				// Only lint command should be discovered and run (test is skipped)
				td.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil
						}
					}
					if name == "make" && len(args) == 1 && args[0] == "lint" {
						return &CommandOutput{Stdout: []byte("Lint Success")}, nil
					}
					return nil, fmt.Errorf("unexpected command")
				}
				td.MockRunner.lookPathFunc = func(file string) (string, error) {
					if file == "make" {
						return "/usr/bin/make", nil
					}
					return "", fmt.Errorf("not found")
				}
			},
			wantBoth: true,
		},
		{
			name: "skip both returns success without running",
			skipConfig: &SkipConfig{
				SkipLint: true,
				SkipTest: true,
			},
			setupMocks: func(td *TestDependencies) {
				td.MockFS.statFunc = func(name string) (os.FileInfo, error) {
					if name == "/project/.git" || name == "/project/go.mod" {
						return &mockFileInfo{isDir: true}, nil
					}
					return nil, fmt.Errorf("not found")
				}
				// No commands should be executed when both are skipped
				td.MockRunner.runContextFunc = func(_ context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
					return nil, fmt.Errorf("no commands should be run when both are skipped")
				}
			},
			wantBoth: true,
		},
		{
			name: "lint fails when not skipped",
			skipConfig: &SkipConfig{
				SkipLint: false,
				SkipTest: true,
			},
			setupMocks: func(td *TestDependencies) {
				td.MockFS.statFunc = func(name string) (os.FileInfo, error) {
					if name == "/project/.git" || name == "/project/go.mod" || name == "/project/Makefile" {
						return &mockFileInfo{isDir: true}, nil
					}
					return nil, fmt.Errorf("not found")
				}

				td.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
					if name == "make" && len(args) >= 3 && args[len(args)-2] == "-n" {
						if args[len(args)-1] == "lint" {
							return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil
						}
					}
					if name == "make" && len(args) == 1 && args[0] == "lint" {
						// Lint fails
						return &CommandOutput{Stderr: []byte("lint errors")}, fmt.Errorf("exit status 1")
					}
					return nil, fmt.Errorf("unexpected command")
				}
				td.MockRunner.lookPathFunc = func(file string) (string, error) {
					if file == "make" {
						return "/usr/bin/make", nil
					}
					return "", fmt.Errorf("not found")
				}
			},
			wantBoth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDeps := createTestDependencies()
			tt.setupMocks(testDeps)

			executor := NewParallelValidateExecutor(
				"/project",
				10,
				false,
				tt.skipConfig,
				testDeps.Dependencies,
			)

			result, err := executor.ExecuteValidations(
				context.Background(),
				"/project",
				"/project/src",
			)

			if err != nil {
				t.Fatalf("ExecuteValidations() error = %v", err)
			}

			if result.BothPassed != tt.wantBoth {
				t.Errorf("ExecuteValidations() BothPassed = %v, want %v", result.BothPassed, tt.wantBoth)
			}

			// Check that skipped commands weren't executed
			if tt.skipConfig != nil {
				if tt.skipConfig.SkipLint && result.LintResult != nil {
					t.Errorf("Lint was supposed to be skipped but got result: %+v", result.LintResult)
				}
				if tt.skipConfig.SkipTest && result.TestResult != nil {
					t.Errorf("Test was supposed to be skipped but got result: %+v", result.TestResult)
				}
			}
		})
	}
}

func TestValidateCommandWithSkipRegistry(t *testing.T) {
	// This test simulates the full validate command flow with skip registry
	tests := []struct {
		name         string
		registryData skipregistry.RegistryData
		filePath     string
		wantSkipLint bool
		wantSkipTest bool
	}{
		{
			name: "directory with lint skip",
			registryData: skipregistry.RegistryData{
				"/project/src": []string{"lint"},
			},
			filePath:     "/project/src/main.go",
			wantSkipLint: true,
			wantSkipTest: false,
		},
		{
			name: "directory with test skip",
			registryData: skipregistry.RegistryData{
				"/project/src": []string{"test"},
			},
			filePath:     "/project/src/main.go",
			wantSkipLint: false,
			wantSkipTest: true,
		},
		{
			name: "directory with both skips",
			registryData: skipregistry.RegistryData{
				"/project/src": []string{"lint", "test"},
			},
			filePath:     "/project/src/main.go",
			wantSkipLint: true,
			wantSkipTest: true,
		},
		{
			name:         "directory with no skips",
			registryData: skipregistry.RegistryData{},
			filePath:     "/project/src/main.go",
			wantSkipLint: false,
			wantSkipTest: false,
		},
		{
			name: "different directory not skipped",
			registryData: skipregistry.RegistryData{
				"/other/path": []string{"lint", "test"},
			},
			filePath:     "/project/src/main.go",
			wantSkipLint: false,
			wantSkipTest: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock storage with the test data
			mockStorage := &mockSkipStorage{
				data: tt.registryData,
			}

			// Create a registry with the mock storage
			registry := skipregistry.NewRegistry(mockStorage)

			// Test IsSkipped for lint
			ctx := context.Background()
			dir := filepath.Dir(tt.filePath)
			skipLint, err := registry.IsSkipped(ctx, skipregistry.DirectoryPath(dir), skipregistry.SkipTypeLint)
			if err != nil {
				t.Fatalf("IsSkipped(lint) error = %v", err)
			}
			if skipLint != tt.wantSkipLint {
				t.Errorf("IsSkipped(lint) = %v, want %v", skipLint, tt.wantSkipLint)
			}

			// Test IsSkipped for test
			skipTest, err := registry.IsSkipped(ctx, skipregistry.DirectoryPath(dir), skipregistry.SkipTypeTest)
			if err != nil {
				t.Fatalf("IsSkipped(test) error = %v", err)
			}
			if skipTest != tt.wantSkipTest {
				t.Errorf("IsSkipped(test) = %v, want %v", skipTest, tt.wantSkipTest)
			}
		})
	}
}
