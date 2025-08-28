package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Helper function to create json.RawMessage from a map.
func mustMarshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(data)
}

// Mock implementations for testing

type mockFileSystem struct {
	statFunc            func(string) (os.FileInfo, error)
	readFileFunc        func(string) ([]byte, error)
	writeFileFunc       func(string, []byte, os.FileMode) error
	tempDirFunc         func() string
	createExclusiveFunc func(string, []byte, os.FileMode) error
	removeFunc          func(string) error
}

func (m *mockFileSystem) Stat(name string) (os.FileInfo, error) {
	if m.statFunc != nil {
		return m.statFunc(name)
	}
	return nil, os.ErrNotExist
}

func (m *mockFileSystem) ReadFile(name string) ([]byte, error) {
	if m.readFileFunc != nil {
		return m.readFileFunc(name)
	}
	return nil, os.ErrNotExist
}

func (m *mockFileSystem) WriteFile(name string, data []byte, perm os.FileMode) error {
	if m.writeFileFunc != nil {
		return m.writeFileFunc(name, data, perm)
	}
	return nil
}

func (m *mockFileSystem) TempDir() string {
	if m.tempDirFunc != nil {
		return m.tempDirFunc()
	}
	return "/tmp"
}

func (m *mockFileSystem) CreateExclusive(name string, data []byte, perm os.FileMode) error {
	if m.createExclusiveFunc != nil {
		return m.createExclusiveFunc(name, data, perm)
	}
	return nil
}

func (m *mockFileSystem) Remove(name string) error {
	if m.removeFunc != nil {
		return m.removeFunc(name)
	}
	return nil
}

type mockCommandRunner struct {
	runContextFunc func(ctx context.Context, dir, name string, args ...string) (*CommandOutput, error)
	lookPathFunc   func(file string) (string, error)
}

func (m *mockCommandRunner) RunContext(
	ctx context.Context,
	dir, name string,
	args ...string,
) (*CommandOutput, error) {
	if m.runContextFunc != nil {
		return m.runContextFunc(ctx, dir, name, args...)
	}
	return nil, errors.New("command not found")
}

func (m *mockCommandRunner) LookPath(file string) (string, error) {
	if m.lookPathFunc != nil {
		return m.lookPathFunc(file)
	}
	return "", errors.New("command not found")
}

type mockProcessManager struct {
	getPIDFunc        func() int
	findProcessFunc   func(pid int) (*os.Process, error)
	processExistsFunc func(pid int) bool
}

func (m *mockProcessManager) GetPID() int {
	if m.getPIDFunc != nil {
		return m.getPIDFunc()
	}
	return 12345
}

func (m *mockProcessManager) FindProcess(pid int) (*os.Process, error) {
	if m.findProcessFunc != nil {
		return m.findProcessFunc(pid)
	}
	return nil, errors.New("process not found")
}

func (m *mockProcessManager) ProcessExists(pid int) bool {
	if m.processExistsFunc != nil {
		return m.processExistsFunc(pid)
	}
	return false
}

type mockClock struct {
	nowFunc func() time.Time
}

func (m *mockClock) Now() time.Time {
	if m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Unix(1700000000, 0)
}

type mockInputReader struct {
	readAllFunc    func() ([]byte, error)
	isTerminalFunc func() bool
}

func (m *mockInputReader) ReadAll() ([]byte, error) {
	if m.readAllFunc != nil {
		return m.readAllFunc()
	}
	return nil, io.EOF
}

func (m *mockInputReader) IsTerminal() bool {
	if m.isTerminalFunc != nil {
		return m.isTerminalFunc()
	}
	return false
}

type mockOutputWriter struct {
	writtenData []byte
}

func (m *mockOutputWriter) Write(p []byte) (int, error) {
	m.writtenData = append(m.writtenData, p...)
	return len(p), nil
}

func (m *mockOutputWriter) String() string {
	return string(m.writtenData)
}

// Helper to create test dependencies with mocks
// TestDependencies wraps Dependencies with direct access to mock implementations.
type TestDependencies struct {
	*Dependencies

	MockFS      *mockFileSystem
	MockRunner  *mockCommandRunner
	MockProcess *mockProcessManager
	MockClock   *mockClock
	MockInput   *mockInputReader
	MockStdout  *mockOutputWriter
	MockStderr  *mockOutputWriter
}

func createTestDependencies() *TestDependencies {
	fs := &mockFileSystem{}
	runner := &mockCommandRunner{}
	process := &mockProcessManager{}
	clock := &mockClock{}
	input := &mockInputReader{}
	stdout := &mockOutputWriter{}
	stderr := &mockOutputWriter{}

	return &TestDependencies{
		Dependencies: &Dependencies{
			FS:      fs,
			Runner:  runner,
			Process: process,
			Clock:   clock,
			Input:   input,
			Stdout:  stdout,
			Stderr:  stderr,
		},
		MockFS:      fs,
		MockRunner:  runner,
		MockProcess: process,
		MockClock:   clock,
		MockInput:   input,
		MockStdout:  stdout,
		MockStderr:  stderr,
	}
}

// Mock FileInfo for testing.
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.isDir }

func (m mockFileInfo) Sys() any { return nil }
