package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// FileSystem provides filesystem operations.
type FileSystem interface {
	Stat(name string) (os.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	TempDir() string
	CreateExclusive(name string, data []byte, perm os.FileMode) error
	Remove(name string) error
}

// CommandOutput contains the output from a command execution.
type CommandOutput struct {
	Stdout []byte
	Stderr []byte
}

// CommandRunner executes external commands.
type CommandRunner interface {
	RunContext(ctx context.Context, dir, name string, args ...string) (*CommandOutput, error)
	LookPath(file string) (string, error)
}

// ProcessManager manages system processes.
type ProcessManager interface {
	GetPID() int
	FindProcess(pid int) (*os.Process, error)
	ProcessExists(pid int) bool
}

// Clock provides time operations.
type Clock interface {
	Now() time.Time
}

// InputReader reads input from various sources.
type InputReader interface {
	ReadAll() ([]byte, error)
	IsTerminal() bool
}

// OutputWriter writes output to various destinations.
type OutputWriter interface {
	io.Writer
}

// Dependencies holds all external dependencies.
type Dependencies struct {
	FS      FileSystem
	Runner  CommandRunner
	Process ProcessManager
	Clock   Clock
	Input   InputReader
	Stdout  OutputWriter
	Stderr  OutputWriter
}

// Production implementations

type realFileSystem struct{}

func (r *realFileSystem) Stat(name string) (os.FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	return info, nil
}

func (r *realFileSystem) ReadFile(name string) ([]byte, error) {
	data, err := os.ReadFile(name) // #nosec G304 - file path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", name, err)
	}
	return data, nil
}

func (r *realFileSystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(name, data, perm); err != nil {
		return fmt.Errorf("write file %s: %w", name, err)
	}
	return nil
}

func (r *realFileSystem) TempDir() string {
	return os.TempDir()
}

func (r *realFileSystem) CreateExclusive(name string, data []byte, perm os.FileMode) error {
	// Use O_EXCL to atomically create the file only if it doesn't exist
	// #nosec G304 - file path is from trusted source
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create exclusive %s: %w", name, err)
	}
	defer func() { _ = file.Close() }()

	if _, writeErr := file.Write(data); writeErr != nil {
		// Try to clean up on write failure
		_ = os.Remove(name)
		return fmt.Errorf("write exclusive %s: %w", name, writeErr)
	}
	return nil
}

func (r *realFileSystem) Remove(name string) error {
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

type realCommandRunner struct{}

func (r *realCommandRunner) RunContext(ctx context.Context, dir, name string, args ...string) (*CommandOutput, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	// Capture stdout and stderr separately
	var stdout, stderr []byte
	var err error

	// Get stdout
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	// Get stderr
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}

	// Start the command
	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start command %s: %w", name, startErr)
	}

	// Read outputs
	stdout, _ = io.ReadAll(stdoutPipe)
	stderr, _ = io.ReadAll(stderrPipe)

	// Wait for completion
	err = cmd.Wait()

	output := &CommandOutput{
		Stdout: stdout,
		Stderr: stderr,
	}

	if err != nil {
		return output, fmt.Errorf("run command %s: %w", name, err)
	}

	return output, nil
}

func (r *realCommandRunner) LookPath(file string) (string, error) {
	path, err := exec.LookPath(file)
	if err != nil {
		return "", fmt.Errorf("look path %s: %w", file, err)
	}
	return path, nil
}

type realProcessManager struct{}

func (r *realProcessManager) GetPID() int {
	return os.Getpid()
}

func (r *realProcessManager) FindProcess(pid int) (*os.Process, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("find process %d: %w", pid, err)
	}
	return process, nil
}

func (r *realProcessManager) ProcessExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds, so we need to send signal 0
	err = process.Signal(os.Signal(nil))
	return err == nil
}

type realClock struct{}

func (r *realClock) Now() time.Time {
	return time.Now()
}

type stdinReader struct{}

func (s *stdinReader) ReadAll() ([]byte, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	return data, nil
}

func (s *stdinReader) IsTerminal() bool {
	fileInfo, _ := os.Stdin.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// NewDefaultDependencies creates production dependencies.
func NewDefaultDependencies() *Dependencies {
	return &Dependencies{
		FS:      &realFileSystem{},
		Runner:  &realCommandRunner{},
		Process: &realProcessManager{},
		Clock:   &realClock{},
		Input:   &stdinReader{},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
}
