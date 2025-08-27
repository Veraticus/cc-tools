// Package statusline provides terminal statusline rendering functionality.
package statusline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultFileReader implements FileReader using the OS.
type DefaultFileReader struct{}

// ReadFile reads a file from the filesystem.
func (f *DefaultFileReader) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", path, err)
	}
	return content, nil
}

// Exists checks if a file exists.
func (f *DefaultFileReader) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ModTime returns the modification time of a file.
func (f *DefaultFileReader) ModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("stat file %s: %w", path, err)
	}
	return info.ModTime(), nil
}

// DefaultCommandRunner implements CommandRunner using exec.
type DefaultCommandRunner struct{}

// Run executes a command with arguments.
func (c *DefaultCommandRunner) Run(command string, args ...string) ([]byte, error) {
	const commandTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running command %s: %w", command, err)
	}
	return output, nil
}

// DefaultEnvReader implements EnvReader using os.Getenv.
type DefaultEnvReader struct{}

// Get retrieves an environment variable.
func (e *DefaultEnvReader) Get(key string) string {
	return os.Getenv(key)
}
