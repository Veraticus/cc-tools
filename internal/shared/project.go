package shared

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectHelper provides project-related functions with dependency injection.
type ProjectHelper struct {
	deps *Dependencies
}

// NewProjectHelper creates a new project helper with dependencies.
func NewProjectHelper(deps *Dependencies) *ProjectHelper {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	return &ProjectHelper{deps: deps}
}

// FindProjectRoot walks up from the current directory to find the project root.
// It looks for common project markers like .git, go.mod, package.json, etc.
func FindProjectRoot(startDir string, deps *Dependencies) (string, error) {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	dir := startDir
	if dir == "" {
		var err error
		dir, err = deps.FS.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Convert to absolute path
	absDir, err := deps.FS.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("getting absolute path: %w", err)
	}

	for {
		// Check for project root markers
		markers := []string{
			".git",
			"go.mod",
			"package.json",
			"Cargo.toml",
			"setup.py",
			"pyproject.toml",
			"Makefile",
			"justfile",
			"Justfile",
		}

		for _, marker := range markers {
			path := filepath.Join(absDir, marker)
			if _, statErr := deps.FS.Stat(path); statErr == nil {
				return absDir, nil
			}
		}

		// Move up one directory
		parent := filepath.Dir(absDir)
		if parent == absDir {
			// Reached root of filesystem
			break
		}
		absDir = parent
	}

	// No project root found, return original directory
	return dir, nil
}

// DetectProjectType analyzes the project directory to determine its type.
func DetectProjectType(projectDir string, deps *Dependencies) []string {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	types := []string{}

	// Go project
	if fileExists(filepath.Join(projectDir, "go.mod"), deps) ||
		fileExists(filepath.Join(projectDir, "go.sum"), deps) {
		types = append(types, "go")
	}

	// Python project
	if fileExists(filepath.Join(projectDir, "pyproject.toml"), deps) ||
		fileExists(filepath.Join(projectDir, "setup.py"), deps) ||
		fileExists(filepath.Join(projectDir, "requirements.txt"), deps) {
		types = append(types, "python")
	}

	// JavaScript/TypeScript project
	if fileExists(filepath.Join(projectDir, "package.json"), deps) ||
		fileExists(filepath.Join(projectDir, "tsconfig.json"), deps) {
		types = append(types, "javascript")
	}

	// Rust project
	if fileExists(filepath.Join(projectDir, "Cargo.toml"), deps) {
		types = append(types, "rust")
	}

	// Nix project
	if fileExists(filepath.Join(projectDir, "flake.nix"), deps) ||
		fileExists(filepath.Join(projectDir, "default.nix"), deps) ||
		fileExists(filepath.Join(projectDir, "shell.nix"), deps) {
		types = append(types, "nix")
	}

	if len(types) == 0 {
		types = append(types, "unknown")
	}

	return types
}

// GetPackageManager detects the package manager for JavaScript projects.
func GetPackageManager(projectDir string, deps *Dependencies) string {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	if fileExists(filepath.Join(projectDir, "yarn.lock"), deps) {
		return "yarn"
	}
	if fileExists(filepath.Join(projectDir, "pnpm-lock.yaml"), deps) {
		return "pnpm"
	}
	if fileExists(filepath.Join(projectDir, "bun.lockb"), deps) {
		return "bun"
	}
	// Default to npm
	return "npm"
}

// fileExists checks if a file exists using dependencies.
func fileExists(path string, deps *Dependencies) bool {
	if deps == nil {
		deps = NewDefaultDependencies()
	}
	_, err := deps.FS.Stat(path)
	return err == nil
}

// ShouldSkipFile determines if a file should be skipped based on common patterns.
// This function doesn't need dependency injection as it only does string manipulation.
func ShouldSkipFile(filePath string) bool {
	// Built-in patterns to always skip
	skipPatterns := []string{
		"/vendor/",
		"/node_modules/",
		"/build/",
		"/.git/",
		"/dist/",
		"/__pycache__/",
		"/.cache/",
		"/target/", // Rust
		"/.next/",  // Next.js
	}

	for _, pattern := range skipPatterns {
		if strings.Contains(filePath, pattern) {
			return true
		}
	}

	// Skip test files for linting/formatting hooks
	testSuffixes := []string{
		"_test.go",
		"_test.py",
		".test.js",
		".test.ts",
		".spec.js",
		".spec.ts",
		".test.jsx",
		".test.tsx",
		".spec.jsx",
		".spec.tsx",
	}

	for _, suffix := range testSuffixes {
		if strings.HasSuffix(filePath, suffix) {
			return true
		}
	}

	// Skip generated files
	generatedSuffixes := []string{
		".generated.go",
		".pb.go",
		".gen.go",
		"_gen.go",
	}

	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(filePath, suffix) {
			return true
		}
	}

	return false
}
