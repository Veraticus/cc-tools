package hooks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCommandDiscovery(t *testing.T) { //nolint:cyclop // table-driven test with many scenarios
	t.Run("discovers Makefile lint target", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "Makefile") {
				return mockFileInfo{name: "Makefile"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - make dry run succeeds for lint
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && len(args) >= 3 && args[len(args)-1] == "lint" && args[len(args)-2] == "-n" {
				return &CommandOutput{Stdout: []byte("golangci-lint run")}, nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "make" || len(cmd.Args) != 1 || cmd.Args[0] != "lint" {
			t.Errorf("Unexpected command: %s %v", cmd.Command, cmd.Args)
		}
		if cmd.Source != "Makefile" {
			t.Errorf("Expected source 'Makefile', got %s", cmd.Source)
		}
	})

	t.Run("discovers justfile recipe", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.Contains(path, "justfile") {
				return mockFileInfo{name: "justfile"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - just --show succeeds for test
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "just" && len(args) >= 2 && args[len(args)-1] == "test" && args[len(args)-2] == "--show" {
				return &CommandOutput{Stdout: []byte("test:\n\tgo test ./...")}, nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "just" || len(cmd.Args) != 1 || cmd.Args[0] != "test" {
			t.Errorf("Unexpected command: %s %v", cmd.Command, cmd.Args)
		}
	})

	t.Run("discovers package.json scripts with npm", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - package.json exists, no lock files
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "package.json") {
				return mockFileInfo{name: "package.json"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - jq finds lint script
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "jq" && len(args) >= 2 {
				// Check if querying for lint script
				if strings.Contains(args[1], "lint") {
					return &CommandOutput{Stdout: []byte(`"eslint ."`)}, nil
				}
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "npm" {
			t.Errorf("Expected npm, got %s", cmd.Command)
		}
		if len(cmd.Args) != 2 || cmd.Args[0] != "run" || cmd.Args[1] != "lint" {
			t.Errorf("Unexpected args: %v", cmd.Args)
		}
	})

	t.Run("detects yarn from lock file", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - package.json and yarn.lock exist
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "package.json") || strings.HasSuffix(path, "yarn.lock") {
				return mockFileInfo{name: path}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "jq" && strings.Contains(args[1], "test") {
				return &CommandOutput{Stdout: []byte(`"jest"`)}, nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "yarn" {
			t.Errorf("Expected yarn, got %s", cmd.Command)
		}
	})

	t.Run("detects pnpm from lock file", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - package.json and pnpm-lock.yaml exist
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "package.json") || strings.HasSuffix(path, "pnpm-lock.yaml") {
				return mockFileInfo{name: path}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, _ ...string) (*CommandOutput, error) {
			if name == "jq" {
				return &CommandOutput{Stdout: []byte(`"test script"`)}, nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd.Command != "pnpm" {
			t.Errorf("Expected pnpm, got %s", cmd.Command)
		}
	})

	t.Run("discovers executable script in scripts directory", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - scripts/lint exists and is executable
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "scripts/lint") {
				return mockFileInfo{
					name: "lint",
					mode: 0755, // executable
				}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "./scripts/lint" {
			t.Errorf("Expected ./scripts/lint, got %s", cmd.Command)
		}
		if cmd.Source != "scripts/" {
			t.Errorf("Expected source 'scripts/', got %s", cmd.Source)
		}
	})

	t.Run("skips non-executable script", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - scripts/test exists but not executable
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "scripts/test") {
				return mockFileInfo{
					name: "test",
					mode: 0644, // not executable
				}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err == nil {
			t.Fatal("Expected error for no command found")
		}
		if cmd != nil {
			t.Fatal("Expected no command for non-executable script")
		}
	})

	t.Run("discovers Go lint with golangci-lint", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - go.mod exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "go.mod") {
				return mockFileInfo{name: "go.mod"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - golangci-lint exists
		testDeps.MockRunner.lookPathFunc = func(file string) (string, error) {
			if file == "golangci-lint" {
				return "/usr/local/bin/golangci-lint", nil
			}
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "golangci-lint" {
			t.Errorf("Expected golangci-lint, got %s", cmd.Command)
		}
		if len(cmd.Args) != 1 || cmd.Args[0] != "run" {
			t.Errorf("Expected args [run], got %v", cmd.Args)
		}
	})

	t.Run("falls back to go vet when golangci-lint not found", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - go.mod exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "go.mod") {
				return mockFileInfo{name: "go.mod"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - golangci-lint doesn't exist
		testDeps.MockRunner.lookPathFunc = func(_ string) (string, error) {
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "go" {
			t.Errorf("Expected go, got %s", cmd.Command)
		}
		if len(cmd.Args) != 2 || cmd.Args[0] != "vet" || cmd.Args[1] != "./..." {
			t.Errorf("Expected args [vet ./...], got %v", cmd.Args)
		}
	})

	t.Run("discovers Go test command", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - go.mod exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "go.mod") {
				return mockFileInfo{name: "go.mod"}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "go" || cmd.Args[0] != "test" || cmd.Args[1] != "./..." {
			t.Errorf("Unexpected command: %s %v", cmd.Command, cmd.Args)
		}
	})

	t.Run("discovers Rust clippy for lint", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - Cargo.toml exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "Cargo.toml") {
				return mockFileInfo{name: "Cargo.toml"}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "cargo" {
			t.Errorf("Expected cargo, got %s", cmd.Command)
		}
		expectedArgs := []string{"clippy", "--", "-D", "warnings"}
		if len(cmd.Args) != len(expectedArgs) {
			t.Errorf("Expected %d args, got %d", len(expectedArgs), len(cmd.Args))
		}
		for i, arg := range expectedArgs {
			if i < len(cmd.Args) && cmd.Args[i] != arg {
				t.Errorf("Arg %d: expected %s, got %s", i, arg, cmd.Args[i])
			}
		}
	})

	t.Run("discovers Rust test command", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - Cargo.toml exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "Cargo.toml") {
				return mockFileInfo{name: "Cargo.toml"}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "cargo" || len(cmd.Args) != 1 || cmd.Args[0] != "test" {
			t.Errorf("Unexpected command: %s %v", cmd.Command, cmd.Args)
		}
	})

	t.Run("discovers Python lint with ruff", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - pyproject.toml exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "pyproject.toml") {
				return mockFileInfo{name: "pyproject.toml"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - ruff exists
		testDeps.MockRunner.lookPathFunc = func(file string) (string, error) {
			if file == "ruff" {
				return "/usr/local/bin/ruff", nil
			}
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "ruff" {
			t.Errorf("Expected ruff, got %s", cmd.Command)
		}
		if len(cmd.Args) != 2 || cmd.Args[0] != "check" || cmd.Args[1] != "." {
			t.Errorf("Expected args [check .], got %v", cmd.Args)
		}
	})

	t.Run("falls back to flake8 when ruff not found", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - setup.py exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "setup.py") {
				return mockFileInfo{name: "setup.py"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - only flake8 exists
		testDeps.MockRunner.lookPathFunc = func(file string) (string, error) {
			if file == "flake8" {
				return "/usr/local/bin/flake8", nil
			}
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "flake8" {
			t.Errorf("Expected flake8, got %s", cmd.Command)
		}
	})

	t.Run("discovers Python test with pytest", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - requirements.txt exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "requirements.txt") {
				return mockFileInfo{name: "requirements.txt"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - pytest exists
		testDeps.MockRunner.lookPathFunc = func(file string) (string, error) {
			if file == "pytest" {
				return "/usr/local/bin/pytest", nil
			}
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "pytest" {
			t.Errorf("Expected pytest, got %s", cmd.Command)
		}
	})

	t.Run("falls back to unittest when pytest not found", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - requirements.txt exists
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "requirements.txt") {
				return mockFileInfo{name: "requirements.txt"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - pytest doesn't exist
		testDeps.MockRunner.lookPathFunc = func(_ string) (string, error) {
			return "", fmt.Errorf("not found")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeTest, "/project")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.Command != "python" {
			t.Errorf("Expected python, got %s", cmd.Command)
		}
		if len(cmd.Args) != 2 || cmd.Args[0] != "-m" || cmd.Args[1] != "unittest" {
			t.Errorf("Expected args [-m unittest], got %v", cmd.Args)
		}
	})

	t.Run("walks up directory tree to find commands", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - Makefile exists at project root
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			// Only Makefile at /project root exists
			if path == "/project/Makefile" {
				return mockFileInfo{name: "Makefile"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner
		testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
			if name == "make" && args[len(args)-1] == "lint" {
				return &CommandOutput{Stdout: []byte("lint")}, nil
			}
			return nil, fmt.Errorf("command failed")
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		// Start from subdirectory
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project/src/subdir")

		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if cmd == nil {
			t.Fatal("Expected to find command")
		}
		if cmd.WorkingDir != "/project" {
			t.Errorf("Expected working dir /project, got %s", cmd.WorkingDir)
		}
	})

	t.Run("stops at project root", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - no build files exist
		testDeps.MockFS.statFunc = func(_ string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project/src")

		if err == nil {
			t.Fatal("Expected error for no command found")
		}
		if cmd != nil {
			t.Fatal("Expected no command")
		}
		if !strings.Contains(err.Error(), "no command found") {
			t.Errorf("Expected 'no command found' error, got: %v", err)
		}
	})

	t.Run("handles timeout during command check", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "Makefile") {
				return mockFileInfo{name: "Makefile"}, nil
			}
			return nil, os.ErrNotExist
		}

		// Setup runner - simulate timeout
		testDeps.MockRunner.runContextFunc = func(ctx context.Context, _, _ string, _ ...string) (*CommandOutput, error) {
			// Block until context is canceled
			<-ctx.Done()
			return nil, ctx.Err()
		}

		// Use very short timeout
		discovery := NewCommandDiscovery("/project", 0, testDeps.Dependencies)
		cmd, err := discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")

		// Should fail to find command due to timeout
		if err == nil {
			t.Fatal("Expected error due to timeout")
		}
		if cmd != nil {
			t.Fatal("Expected no command due to timeout")
		}
	})

	t.Run("detects multiple project types", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup filesystem - both go.mod and package.json exist
		testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "go.mod") || strings.HasSuffix(path, "package.json") {
				return mockFileInfo{name: path}, nil
			}
			return nil, os.ErrNotExist
		}

		discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)
		types := discovery.detectProjectTypes("/project")

		hasGo := false
		hasJS := false
		for _, t := range types {
			if t == "go" {
				hasGo = true
			}
			if t == "javascript" {
				hasJS = true
			}
		}

		if !hasGo {
			t.Error("Expected to detect Go project")
		}
		if !hasJS {
			t.Error("Expected to detect JavaScript project")
		}
	})
}

func TestDiscoveredCommandString(t *testing.T) {
	tests := []struct {
		name     string
		cmd      *DiscoveredCommand
		expected string
	}{
		{
			name:     "nil command",
			cmd:      nil,
			expected: "",
		},
		{
			name: "command without args",
			cmd: &DiscoveredCommand{
				Command: "make",
				Args:    []string{},
			},
			expected: "make",
		},
		{
			name: "command with single arg",
			cmd: &DiscoveredCommand{
				Command: "make",
				Args:    []string{"lint"},
			},
			expected: "make lint",
		},
		{
			name: "command with multiple args",
			cmd: &DiscoveredCommand{
				Command: "cargo",
				Args:    []string{"clippy", "--", "-D", "warnings"},
			},
			expected: "cargo clippy -- -D warnings",
		},
		{
			name: "script path",
			cmd: &DiscoveredCommand{
				Command: "./scripts/test",
				Args:    []string{},
			},
			expected: "./scripts/test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cmd.String()
			if result != tt.expected {
				t.Errorf("String() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func BenchmarkCommandDiscovery(b *testing.B) {
	testDeps := createTestDependencies()

	// Setup basic mocks
	testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, "Makefile") {
			return mockFileInfo{name: "Makefile"}, nil
		}
		return nil, os.ErrNotExist
	}

	testDeps.MockRunner.runContextFunc = func(_ context.Context, _, name string, args ...string) (*CommandOutput, error) {
		if name == "make" && args[len(args)-1] == "lint" {
			return &CommandOutput{Stdout: []byte("lint")}, nil
		}
		return nil, fmt.Errorf("command failed")
	}

	discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)

	b.ResetTimer()
	for range b.N {
		discovery.DiscoverCommand(context.Background(), CommandTypeLint, "/project")
	}
}

func BenchmarkDetectPackageManager(b *testing.B) {
	testDeps := createTestDependencies()

	// Vary the lock file that exists
	lockFiles := []string{"yarn.lock", "pnpm-lock.yaml", "bun.lockb", "package-lock.json"}
	currentLock := 0

	testDeps.MockFS.statFunc = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, lockFiles[currentLock%len(lockFiles)]) {
			return mockFileInfo{name: path}, nil
		}
		return nil, os.ErrNotExist
	}

	discovery := NewCommandDiscovery("/project", 20, testDeps.Dependencies)

	b.ResetTimer()
	for i := range b.N {
		currentLock = i
		discovery.detectPackageManager("/project")
	}
}
