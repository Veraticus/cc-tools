package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner:  &mockLintRunner{},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	if srv.socketPath != "/tmp/test.sock" {
		t.Errorf("Expected socket path /tmp/test.sock, got %s", srv.socketPath)
	}

	if srv.deps != deps {
		t.Error("Dependencies not properly set")
	}

	if srv.shutdown == nil {
		t.Error("Shutdown channel not initialized")
	}

	if srv.stats == nil || srv.stats.startTime.IsZero() {
		t.Error("Stats not properly initialized")
	}
}

func TestServer_processRequest(t *testing.T) {
	tests := []struct {
		name         string
		request      Request
		setupMocks   func(*ServerDependencies)
		wantError    bool
		wantErrorMsg string
		wantMethod   string
	}{
		{
			name: "invalid json-rpc version",
			request: Request{
				JSONRPC: "1.0",
				ID:      RequestID{value: "1"},
				Method:  "lint",
			},
			wantError:    true,
			wantErrorMsg: "Invalid Request",
		},
		{
			name: "method not found",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "1"},
				Method:  "unknown",
			},
			wantError:    true,
			wantErrorMsg: "Method not found: unknown",
		},
		{
			name: "successful lint request",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "1"},
				Method:  "lint",
				Params:  json.RawMessage(`{"input": "test"}`),
			},
			setupMocks: func(deps *ServerDependencies) {
				lint, ok := deps.LintRunner.(*mockLintRunner)
				if !ok {
					t.Fatal("LintRunner is not a *mockLintRunner")
				}
				lint.runFunc = func(_ context.Context, _ io.Reader) (io.Reader, error) {
					return strings.NewReader("lint success"), nil
				}
			},
			wantMethod: "lint",
		},
		{
			name: "successful test request",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "2"},
				Method:  "test",
				Params:  json.RawMessage(`{"input": "test"}`),
			},
			setupMocks: func(deps *ServerDependencies) {
				test, ok := deps.TestRunner.(*mockTestRunner)
				if !ok {
					t.Fatal("TestRunner is not a *mockTestRunner")
				}
				test.runFunc = func(_ context.Context, _ io.Reader) (io.Reader, error) {
					return strings.NewReader("test success"), nil
				}
			},
			wantMethod: "test",
		},
		{
			name: "stats request",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "3"},
				Method:  "stats",
			},
			wantMethod: "stats",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &ServerDependencies{
				LintRunner:  &mockLintRunner{},
				TestRunner:  &mockTestRunner{},
				LockManager: newMockLockManager(),
				Logger:      newMockLogger(),
			}

			if tt.setupMocks != nil {
				tt.setupMocks(deps)
			}

			srv := NewServer("/tmp/test.sock", deps)
			resp := srv.processRequest(tt.request)

			if tt.wantError {
				if resp.Error == nil {
					t.Errorf("Expected error, got nil")
				} else if !strings.Contains(resp.Error.Message, tt.wantErrorMsg) {
					t.Errorf("Expected error message containing %q, got %q",
						tt.wantErrorMsg, resp.Error.Message)
				}
			} else {
				if resp.Error != nil {
					t.Errorf("Expected no error, got %v", resp.Error)
				}
			}

			// Check that logger was called
			logger, ok := deps.Logger.(*mockLogger)
			if !ok {
				t.Fatal("Logger is not a *mockLogger")
			}
			messages := logger.getMessages()
			if len(messages) == 0 {
				t.Error("Expected log messages, got none")
			}
		})
	}
}

func TestServer_handleConnection(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner: &mockLintRunner{
			runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
				return strings.NewReader("success"), nil
			},
		},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	// Create a request
	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "1"},
		Method:  "lint",
		Params:  json.RawMessage(`{"input": "test"}`),
	}

	reqData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Create mock connection
	var responseBuffer bytes.Buffer
	conn := &mockConn{
		reader: bytes.NewReader(reqData),
		writer: &responseBuffer,
	}

	// Handle connection (simulate server calling it)
	srv.wg.Add(1)
	srv.handleConnection(conn)

	// Parse response
	var resp Response
	if unmarshalErr := json.Unmarshal(responseBuffer.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Failed to parse response: %v", unmarshalErr)
	}

	if resp.Error != nil {
		t.Errorf("Expected successful response, got error: %v", resp.Error)
	}

	// Verify stats were updated
	if srv.stats.requestCount != 1 {
		t.Errorf("Expected request count 1, got %d", srv.stats.requestCount)
	}
}

func TestServer_handleRunner(t *testing.T) {
	tests := []struct {
		name          string
		request       Request
		runner        io.Reader
		runnerError   error
		lockAcquired  bool
		wantError     bool
		wantErrorCode int
	}{
		{
			name: "successful run",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "1"},
				Method:  "lint",
				Params:  json.RawMessage(`{"input": "test"}`),
			},
			runner:    strings.NewReader("success"),
			wantError: false,
		},
		{
			name: "runner error",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "2"},
				Method:  "lint",
				Params:  json.RawMessage(`{"input": "test"}`),
			},
			runnerError:   errors.New("runner failed"),
			wantError:     true,
			wantErrorCode: InternalError,
		},
		{
			name: "lock acquisition failure",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "3"},
				Method:  "lint",
				Params:  json.RawMessage(`{"project": "test-project", "input": "test"}`),
			},
			lockAcquired:  false,
			wantError:     true,
			wantErrorCode: InternalError,
		},
		{
			name: "invalid params",
			request: Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: "4"},
				Method:  "lint",
				Params:  json.RawMessage(`{invalid json}`),
			},
			wantError:     true,
			wantErrorCode: InvalidParams,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockManager := newMockLockManager()
			if !tt.lockAcquired {
				lockManager.acquireFunc = func(_, _ string) bool {
					return false
				}
			}

			deps := &ServerDependencies{
				LintRunner: &mockLintRunner{
					runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
						if tt.runnerError != nil {
							return nil, tt.runnerError
						}
						return tt.runner, nil
					},
				},
				TestRunner:  &mockTestRunner{},
				LockManager: lockManager,
				Logger:      newMockLogger(),
			}

			srv := NewServer("/tmp/test.sock", deps)
			runner := deps.LintRunner

			resp := srv.handleRunner(tt.request, runner, "lint", 30*time.Second)

			if tt.wantError {
				if resp.Error == nil {
					t.Error("Expected error, got nil")
				} else if resp.Error.Code != tt.wantErrorCode {
					t.Errorf("Expected error code %d, got %d", tt.wantErrorCode, resp.Error.Code)
				}
			} else {
				if resp.Error != nil {
					t.Errorf("Expected no error, got %v", resp.Error)
				}
			}
		})
	}
}

func TestServer_handleLint(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner: &mockLintRunner{
			runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
				return strings.NewReader("lint output"), nil
			},
		},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "1"},
		Method:  "lint",
		Params:  json.RawMessage(`{"input": "test code"}`),
	}

	resp := srv.handleLint(req)

	if resp.Error != nil {
		t.Errorf("Expected successful response, got error: %v", resp.Error)
	}

	if resp.Result == nil {
		t.Error("Expected result, got nil")
	}

	// Verify the lint runner was called
	lintRunner, ok := deps.LintRunner.(*mockLintRunner)
	if !ok {
		t.Fatal("LintRunner is not a *mockLintRunner")
	}
	calls := lintRunner.getCalls()
	if len(calls) != 1 {
		t.Errorf("Expected 1 lint runner call, got %d", len(calls))
	}
}

func TestServer_handleTest(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner: &mockLintRunner{},
		TestRunner: &mockTestRunner{
			runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
				return strings.NewReader("test output"), nil
			},
		},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "1"},
		Method:  "test",
		Params:  json.RawMessage(`{"input": "test code"}`),
	}

	resp := srv.handleTest(req)

	if resp.Error != nil {
		t.Errorf("Expected successful response, got error: %v", resp.Error)
	}

	if resp.Result == nil {
		t.Error("Expected result, got nil")
	}

	// Verify the test runner was called
	testRunner, ok := deps.TestRunner.(*mockTestRunner)
	if !ok {
		t.Fatal("TestRunner is not a *mockTestRunner")
	}
	calls := testRunner.getCalls()
	if len(calls) != 1 {
		t.Errorf("Expected 1 test runner call, got %d", len(calls))
	}
}

func TestServer_handleStats(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner:  &mockLintRunner{},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	// Simulate some activity
	srv.stats.requestCount = 10
	srv.stats.errorCount = 2
	srv.stats.activeConns = 3

	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "1"},
		Method:  "stats",
	}

	resp := srv.handleStats(req)

	if resp.Error != nil {
		t.Errorf("Expected successful response, got error: %v", resp.Error)
	}

	if resp.Result == nil {
		t.Error("Expected result, got nil")
	}

	// Stats are returned as plain text, not JSON
	statsOutput := resp.Result.Output

	// Verify stats contain expected fields
	expectedFields := []string{"Uptime:", "Requests:", "Errors:", "Active Connections:", "Socket:"}
	for _, field := range expectedFields {
		if !strings.Contains(statsOutput, field) {
			t.Errorf("Expected stats to contain field %q", field)
		}
	}
}

func TestServer_Shutdown(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner:  &mockLintRunner{},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	// Start a goroutine to simulate active connections
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-srv.shutdown:
			return
		case <-time.After(5 * time.Second):
			t.Error("Shutdown signal not received")
		}
	}()

	// Call shutdown
	srv.Shutdown()

	// Wait for goroutine to complete
	wg.Wait()

	// Verify shutdown channel is closed
	select {
	case <-srv.shutdown:
		// Success
	default:
		t.Error("Shutdown channel not closed")
	}
}

func TestServerStats_ThreadSafety(t *testing.T) {
	stats := &ServerStats{startTime: time.Now()}

	var wg sync.WaitGroup
	numGoroutines := 10
	numOps := 1000

	// Concurrently update stats
	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range numOps {
				stats.mu.Lock()
				stats.requestCount++
				if j%10 == 0 {
					stats.errorCount++
				}
				stats.mu.Unlock()
			}
		}()
	}

	wg.Wait()

	expectedRequests := int64(numGoroutines * numOps)
	expectedErrors := int64(numGoroutines * (numOps / 10))

	if stats.requestCount != expectedRequests {
		t.Errorf("Expected %d requests, got %d", expectedRequests, stats.requestCount)
	}

	if stats.errorCount != expectedErrors {
		t.Errorf("Expected %d errors, got %d", expectedErrors, stats.errorCount)
	}
}

func TestServer_RunnerTimeout(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner: &mockLintRunner{
			runFunc: func(ctx context.Context, _ io.Reader) (io.Reader, error) {
				// Simulate a long-running operation
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return strings.NewReader("should not reach here"), nil
				}
			},
		},
		TestRunner:  &mockTestRunner{},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	req := Request{
		JSONRPC: "2.0",
		ID:      RequestID{value: "1"},
		Method:  "lint",
		Params:  json.RawMessage(`{"input": "test", "timeout": 50}`), // 50ms timeout
	}

	start := time.Now()
	resp := srv.handleRunner(req, deps.LintRunner, "lint", 30*time.Second)
	duration := time.Since(start)

	if resp.Error == nil {
		t.Error("Expected timeout error, got nil")
		return
	}

	if !strings.Contains(resp.Error.Message, "context deadline exceeded") {
		t.Errorf("Expected timeout error message, got: %s", resp.Error.Message)
	}

	// Should timeout quickly (within 100ms plus overhead)
	if duration > 150*time.Millisecond {
		t.Errorf("Runner took too long to timeout: %v", duration)
	}
}

func TestServer_ConcurrentRequests(t *testing.T) {
	deps := &ServerDependencies{
		LintRunner: &mockLintRunner{
			runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
				time.Sleep(10 * time.Millisecond) // Simulate work
				return strings.NewReader("success"), nil
			},
		},
		TestRunner: &mockTestRunner{
			runFunc: func(_ context.Context, _ io.Reader) (io.Reader, error) {
				time.Sleep(10 * time.Millisecond) // Simulate work
				return strings.NewReader("success"), nil
			},
		},
		LockManager: newMockLockManager(),
		Logger:      newMockLogger(),
	}

	srv := NewServer("/tmp/test.sock", deps)

	var wg sync.WaitGroup
	numRequests := 20

	for i := range numRequests {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			method := "lint"
			if id%2 == 0 {
				method = "test"
			}

			req := Request{
				JSONRPC: "2.0",
				ID:      RequestID{value: fmt.Sprintf("%d", id)},
				Method:  method,
				Params:  json.RawMessage(fmt.Sprintf(`{"input": "test %d"}`, id)),
			}

			// Simulate what handleConnection does
			srv.stats.mu.Lock()
			srv.stats.requestCount++
			srv.stats.mu.Unlock()

			resp := srv.processRequest(req)
			if resp.Error != nil {
				srv.stats.mu.Lock()
				srv.stats.errorCount++
				srv.stats.mu.Unlock()
				t.Logf("Request %d failed: %v", id, resp.Error)
			} else {
				t.Logf("Request %d succeeded", id)
			}
		}(i)
	}

	wg.Wait()

	// Verify all requests were processed
	if srv.stats.requestCount != int64(numRequests) {
		t.Errorf("Expected %d requests processed, got %d", numRequests, srv.stats.requestCount)
	}
}
