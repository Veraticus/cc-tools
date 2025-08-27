package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

var errMockNoRunFunc = errors.New("mock: no run function configured")

// mockLintRunner implements LintRunner for testing.
type mockLintRunner struct {
	runFunc func(ctx context.Context, input io.Reader) (io.Reader, error)
	calls   []runCall
	mu      sync.Mutex
}

type runCall struct {
	input string
}

func (m *mockLintRunner) Run(ctx context.Context, input io.Reader) (io.Reader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, _ := io.ReadAll(input)
	m.calls = append(m.calls, runCall{input: string(data)})

	if m.runFunc != nil {
		// Create a new reader with the data for runFunc
		return m.runFunc(ctx, bytes.NewReader(data))
	}
	return nil, errMockNoRunFunc
}

func (m *mockLintRunner) getCalls() []runCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runCall{}, m.calls...)
}

// mockTestRunner implements TestRunner for testing.
type mockTestRunner struct {
	runFunc func(ctx context.Context, input io.Reader) (io.Reader, error)
	calls   []runCall
	mu      sync.Mutex
}

func (m *mockTestRunner) Run(ctx context.Context, input io.Reader) (io.Reader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, _ := io.ReadAll(input)
	m.calls = append(m.calls, runCall{input: string(data)})

	if m.runFunc != nil {
		// Create a new reader with the data for runFunc
		return m.runFunc(ctx, bytes.NewReader(data))
	}
	return nil, errMockNoRunFunc
}

func (m *mockTestRunner) getCalls() []runCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runCall{}, m.calls...)
}

// mockLockManager implements LockManager for testing.
type mockLockManager struct {
	acquireFunc func(key, holder string) bool
	releaseFunc func(key string)
	locks       map[string]string
	mu          sync.Mutex
}

func newMockLockManager() *mockLockManager {
	return &mockLockManager{
		locks: make(map[string]string),
	}
}

func (m *mockLockManager) Acquire(key, holder string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.acquireFunc != nil {
		return m.acquireFunc(key, holder)
	}

	if _, exists := m.locks[key]; exists {
		return false
	}
	m.locks[key] = holder
	return true
}

func (m *mockLockManager) Release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.releaseFunc != nil {
		m.releaseFunc(key)
		return
	}

	delete(m.locks, key)
}

// mockLogger implements Logger for testing.
type mockLogger struct {
	messages []string
	mu       sync.Mutex
}

func newMockLogger() *mockLogger {
	return &mockLogger{
		messages: make([]string, 0),
	}
}

func (m *mockLogger) Printf(format string, v ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, fmt.Sprintf(format, v...))
}

func (m *mockLogger) Println(v ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, fmt.Sprint(v...))
}

func (m *mockLogger) getMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.messages...)
}

// mockConn implements net.Conn for testing.
type mockConn struct {
	reader     io.Reader
	writer     io.Writer
	closeFunc  func() error
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (m *mockConn) Read(b []byte) (int, error) {
	if m.reader != nil {
		return m.reader.Read(b)
	}
	return 0, io.EOF
}

func (m *mockConn) Write(b []byte) (int, error) {
	if m.writer != nil {
		return m.writer.Write(b)
	}
	return len(b), nil
}

func (m *mockConn) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

func (m *mockConn) LocalAddr() net.Addr {
	if m.localAddr != nil {
		return m.localAddr
	}
	return &net.UnixAddr{Name: "mock", Net: "unix"}
}

func (m *mockConn) RemoteAddr() net.Addr {
	if m.remoteAddr != nil {
		return m.remoteAddr
	}
	return &net.UnixAddr{Name: "mock", Net: "unix"}
}

func (m *mockConn) SetDeadline(_ time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(_ time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(_ time.Time) error { return nil }
