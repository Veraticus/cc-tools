package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// CommandType represents the type of command to discover.
type CommandType string

const (
	// CommandTypeLint represents lint commands (used internally by validate).
	CommandTypeLint CommandType = "lint"
	// CommandTypeTest represents test commands (used internally by validate).
	CommandTypeTest CommandType = "test"
)

// DiscoveredCommand represents a discovered command.
type DiscoveredCommand struct {
	Type       CommandType
	Command    string
	Args       []string
	WorkingDir string
	Source     string // Where it was found (e.g., "Makefile", "package.json")
}

// CommandDiscovery handles discovering project commands with injected dependencies.
type CommandDiscovery struct {
	projectRoot string
	timeout     int
	deps        *Dependencies
}

// NewCommandDiscovery creates a new command discovery instance with dependencies.
func NewCommandDiscovery(projectRoot string, timeoutSecs int, deps *Dependencies) *CommandDiscovery {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &CommandDiscovery{
		projectRoot: projectRoot,
		timeout:     timeoutSecs,
		deps:        deps,
	}
}

// DiscoverCommand searches for and returns a command of the specified type.
func (cd *CommandDiscovery) DiscoverCommand(
	ctx context.Context,
	cmdType CommandType,
	startDir string,
) (*DiscoveredCommand, error) {
	currentDir := startDir
	if currentDir == "" {
		currentDir = cd.projectRoot
	}

	// Walk up from current directory to project root
	for {
		// Check for Makefile
		if cmd := cd.checkMakefile(ctx, currentDir, cmdType); cmd != nil {
			return cmd, nil
		}

		// Check for justfile
		if cmd := cd.checkJustfile(ctx, currentDir, cmdType); cmd != nil {
			return cmd, nil
		}

		// Check for package.json (Node.js)
		if cmd := cd.checkPackageJSON(ctx, currentDir, cmdType); cmd != nil {
			return cmd, nil
		}

		// Check for scripts directory
		if cmd := cd.checkScriptsDir(ctx, currentDir, cmdType); cmd != nil {
			return cmd, nil
		}

		// Check for language-specific tools
		if cmd := cd.checkLanguageSpecific(ctx, currentDir, cmdType); cmd != nil {
			return cmd, nil
		}

		// Stop at project root or filesystem root
		if currentDir == cd.projectRoot || currentDir == "/" {
			break
		}

		// Move up one directory
		parent := filepath.Dir(currentDir)
		if parent == currentDir {
			break
		}
		currentDir = parent
	}

	return nil, fmt.Errorf("no command found for type %s", cmdType)
}

// checkMakefile checks for Makefile targets.
func (cd *CommandDiscovery) checkMakefile(
	ctx context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	makefiles := []string{"Makefile", "makefile"}

	for _, makefile := range makefiles {
		path := filepath.Join(dir, makefile)
		if _, err := cd.deps.FS.Stat(path); err != nil {
			continue
		}

		target := string(cmdType)
		// Check if target exists using make -n (dry run)
		timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(cd.timeout)*time.Second)
		_, err := cd.deps.Runner.RunContext(timeoutCtx, dir, "make", "-f", path, "-n", target)
		cancel()
		if err == nil {
			return &DiscoveredCommand{
				Type:       cmdType,
				Command:    "make",
				Args:       []string{target},
				WorkingDir: dir,
				Source:     makefile,
			}
		}
	}

	return nil
}

// checkJustfile checks for justfile recipes.
func (cd *CommandDiscovery) checkJustfile(
	ctx context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	justfiles := []string{"justfile", "Justfile", ".justfile"}

	for _, justfile := range justfiles {
		path := filepath.Join(dir, justfile)
		if _, err := cd.deps.FS.Stat(path); err != nil {
			continue
		}

		recipe := string(cmdType)
		// Check if recipe exists using just --show
		timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(cd.timeout)*time.Second)
		_, err := cd.deps.Runner.RunContext(timeoutCtx, dir, "just", "--justfile", path, "--show", recipe)
		cancel()
		if err == nil {
			return &DiscoveredCommand{
				Type:       cmdType,
				Command:    "just",
				Args:       []string{recipe},
				WorkingDir: dir,
				Source:     justfile,
			}
		}
	}

	return nil
}

// checkPackageJSON checks for npm/yarn/pnpm scripts.
func (cd *CommandDiscovery) checkPackageJSON(
	ctx context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	packagePath := filepath.Join(dir, "package.json")
	if _, err := cd.deps.FS.Stat(packagePath); err != nil {
		return nil
	}

	// Use jq to check if script exists
	script := string(cmdType)
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(cd.timeout)*time.Second)
	defer cancel()

	if _, err := cd.deps.Runner.RunContext(timeoutCtx, dir, "jq", "-e",
		fmt.Sprintf(".scripts.\"%s\"", script), packagePath); err != nil {
		return nil
	}

	// Detect package manager
	pm := cd.detectPackageManager(dir)

	return &DiscoveredCommand{
		Type:       cmdType,
		Command:    pm,
		Args:       []string{"run", script},
		WorkingDir: dir,
		Source:     "package.json",
	}
}

// checkScriptsDir checks for executable scripts in scripts/ directory.
func (cd *CommandDiscovery) checkScriptsDir(
	_ context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	scriptPath := filepath.Join(dir, "scripts", string(cmdType))

	info, err := cd.deps.FS.Stat(scriptPath)
	if err != nil {
		return nil
	}

	// Check if it's executable
	if info.Mode()&0111 == 0 {
		return nil
	}

	return &DiscoveredCommand{
		Type:       cmdType,
		Command:    "./scripts/" + string(cmdType),
		Args:       []string{},
		WorkingDir: dir,
		Source:     "scripts/",
	}
}

// checkLanguageSpecific checks for language-specific tools.
func (cd *CommandDiscovery) checkLanguageSpecific(
	ctx context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	// Check for various project markers
	projectTypes := cd.detectProjectTypes(dir)

	for _, projectType := range projectTypes {
		switch projectType {
		case "go":
			if cmd := cd.checkGoCommands(ctx, dir, cmdType); cmd != nil {
				return cmd
			}
		case "rust":
			if cmd := cd.checkRustCommands(ctx, dir, cmdType); cmd != nil {
				return cmd
			}
		case "python":
			if cmd := cd.checkPythonCommands(ctx, dir, cmdType); cmd != nil {
				return cmd
			}
		}
	}

	return nil
}

// checkGoCommands checks for Go-specific commands.
func (cd *CommandDiscovery) checkGoCommands(
	_ context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	// Only check if go.mod exists in this directory
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil
	}

	switch cmdType {
	case CommandTypeLint:
		// Try golangci-lint first
		if _, err := cd.deps.Runner.LookPath("golangci-lint"); err == nil {
			return &DiscoveredCommand{
				Type:       cmdType,
				Command:    "golangci-lint",
				Args:       []string{"run"},
				WorkingDir: dir,
				Source:     "go.mod",
			}
		}
		// Fall back to go vet
		return &DiscoveredCommand{
			Type:       cmdType,
			Command:    "go",
			Args:       []string{"vet", "./..."},
			WorkingDir: dir,
			Source:     "go.mod",
		}
	case CommandTypeTest:
		return &DiscoveredCommand{
			Type:       cmdType,
			Command:    "go",
			Args:       []string{"test", "./..."},
			WorkingDir: dir,
			Source:     "go.mod",
		}
	}

	return nil
}

// checkRustCommands checks for Rust-specific commands.
func (cd *CommandDiscovery) checkRustCommands(
	_ context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	// Only check if Cargo.toml exists in this directory
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "Cargo.toml")); err != nil {
		return nil
	}

	switch cmdType {
	case CommandTypeLint:
		return &DiscoveredCommand{
			Type:       cmdType,
			Command:    "cargo",
			Args:       []string{"clippy", "--", "-D", "warnings"},
			WorkingDir: dir,
			Source:     "Cargo.toml",
		}
	case CommandTypeTest:
		return &DiscoveredCommand{
			Type:       cmdType,
			Command:    "cargo",
			Args:       []string{"test"},
			WorkingDir: dir,
			Source:     "Cargo.toml",
		}
	}

	return nil
}

// checkPythonCommands checks for Python-specific commands.
func (cd *CommandDiscovery) checkPythonCommands(
	_ context.Context,
	dir string,
	cmdType CommandType,
) *DiscoveredCommand {
	// Check if this is a Python project directory
	pythonMarkers := []string{"pyproject.toml", "setup.py", "requirements.txt"}
	hasPython := false
	for _, marker := range pythonMarkers {
		if _, err := cd.deps.FS.Stat(filepath.Join(dir, marker)); err == nil {
			hasPython = true
			break
		}
	}

	if !hasPython {
		return nil
	}

	switch cmdType {
	case CommandTypeLint:
		// Try linters in order of preference
		linters := []struct {
			name string
			args []string
		}{
			{"ruff", []string{"check", "."}},
			{"flake8", []string{"."}},
			{"pylint", []string{"."}},
		}

		for _, linter := range linters {
			if _, err := cd.deps.Runner.LookPath(linter.name); err == nil {
				return &DiscoveredCommand{
					Type:       cmdType,
					Command:    linter.name,
					Args:       linter.args,
					WorkingDir: dir,
					Source:     "Python project",
				}
			}
		}
	case CommandTypeTest:
		// Try test runners in order of preference
		if _, err := cd.deps.Runner.LookPath("pytest"); err == nil {
			return &DiscoveredCommand{
				Type:       cmdType,
				Command:    "pytest",
				Args:       []string{},
				WorkingDir: dir,
				Source:     "Python project",
			}
		}
		// Fall back to unittest
		return &DiscoveredCommand{
			Type:       cmdType,
			Command:    "python",
			Args:       []string{"-m", "unittest"},
			WorkingDir: dir,
			Source:     "Python project",
		}
	}

	return nil
}

// detectPackageManager detects which package manager to use based on lock files.
func (cd *CommandDiscovery) detectPackageManager(dir string) string {
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return "yarn"
	}
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return "pnpm"
	}
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "bun.lockb")); err == nil {
		return "bun"
	}
	return "npm"
}

// detectProjectTypes detects the types of project in the directory.
func (cd *CommandDiscovery) detectProjectTypes(dir string) []string {
	var types []string

	// Go project
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "go.mod")); err == nil {
		types = append(types, "go")
	}

	// Rust project
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		types = append(types, "rust")
	}

	// Python project
	pythonMarkers := []string{"pyproject.toml", "setup.py", "requirements.txt"}
	for _, marker := range pythonMarkers {
		if _, err := cd.deps.FS.Stat(filepath.Join(dir, marker)); err == nil {
			types = append(types, "python")
			break
		}
	}

	// JavaScript/TypeScript project
	if _, err := cd.deps.FS.Stat(filepath.Join(dir, "package.json")); err == nil {
		types = append(types, "javascript")
	}

	return types
}

// String returns a string representation of the discovered command.
func (dc *DiscoveredCommand) String() string {
	if dc == nil {
		return ""
	}
	args := strings.Join(dc.Args, " ")
	if args != "" {
		return fmt.Sprintf("%s %s", dc.Command, args)
	}
	return dc.Command
}
