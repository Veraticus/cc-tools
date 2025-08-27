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
}

// CommandRunner executes external commands.
type CommandRunner interface {
	RunContext(ctx context.Context, dir, name string, args ...string) ([]byte, error)
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

// StringInputReader reads from a string instead of stdin.
type StringInputReader struct {
	content string
}

// NewStringInputReader creates a new StringInputReader.
func NewStringInputReader(content string) *StringInputReader {
	return &StringInputReader{content: content}
}

// ReadAll returns the string content as bytes.
func (s *StringInputReader) ReadAll() ([]byte, error) {
	return []byte(s.content), nil
}

// IsTerminal always returns false for string input.
func (s *StringInputReader) IsTerminal() bool {
	return false
}

// OutputWriter writes output to various destinations.
type OutputWriter interface {
	io.Writer
}

// StringOutputWriter captures output to a string.
type StringOutputWriter struct {
	content []byte
}

// NewStringOutputWriter creates a new StringOutputWriter.
func NewStringOutputWriter() *StringOutputWriter {
	return &StringOutputWriter{}
}

// Write appends to the output buffer.
func (s *StringOutputWriter) Write(p []byte) (int, error) {
	s.content = append(s.content, p...)
	return len(p), nil
}

// String returns the captured output as a string.
func (s *StringOutputWriter) String() string {
	return string(s.content)
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

type realCommandRunner struct{}

func (r *realCommandRunner) RunContext(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
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
