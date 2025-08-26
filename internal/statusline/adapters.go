package statusline

import (
	"os"
	"os/exec"
	"time"
)

// DefaultFileReader implements FileReader using the OS
type DefaultFileReader struct{}

// ReadFile reads a file from the filesystem
func (f *DefaultFileReader) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// Exists checks if a file exists
func (f *DefaultFileReader) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ModTime returns the modification time of a file
func (f *DefaultFileReader) ModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// DefaultCommandRunner implements CommandRunner using exec
type DefaultCommandRunner struct{}

// Run executes a command with arguments
func (c *DefaultCommandRunner) Run(command string, args ...string) ([]byte, error) {
	cmd := exec.Command(command, args...)
	return cmd.Output()
}

// DefaultEnvReader implements EnvReader using os.Getenv
type DefaultEnvReader struct{}

// Get retrieves an environment variable
func (e *DefaultEnvReader) Get(key string) string {
	return os.Getenv(key)
}
