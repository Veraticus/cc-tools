package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name               string
		socketPath         string
		envXDG             string
		expectedSocketPath string
	}{
		{
			name:               "explicit socket path",
			socketPath:         "/tmp/custom.sock",
			expectedSocketPath: "/tmp/custom.sock",
		},
		{
			name:               "default with XDG_RUNTIME_DIR",
			socketPath:         "",
			envXDG:             "/run/user/1000",
			expectedSocketPath: "/run/user/1000/cc-tools/server.sock",
		},
		{
			name:               "default without XDG_RUNTIME_DIR",
			socketPath:         "",
			envXDG:             "",
			expectedSocketPath: fmt.Sprintf("/tmp/cc-tools-%d.sock", os.Getuid()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment
			t.Setenv("XDG_RUNTIME_DIR", tt.envXDG)

			client := NewClient(tt.socketPath)

			if client.socketPath != tt.expectedSocketPath {
				t.Errorf("Expected socket path %s, got %s", tt.expectedSocketPath, client.socketPath)
			}
		})
	}
}

func TestDefaultSocketPath(t *testing.T) {
	tests := []struct {
		name     string
		xdgDir   string
		expected string
	}{
		{
			name:     "with XDG_RUNTIME_DIR",
			xdgDir:   "/run/user/1000",
			expected: "/run/user/1000/cc-tools/server.sock",
		},
		{
			name:     "without XDG_RUNTIME_DIR",
			xdgDir:   "",
			expected: fmt.Sprintf("/tmp/cc-tools-%d.sock", os.Getuid()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", tt.xdgDir)

			path := DefaultSocketPath()
			if path != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, path)
			}
		})
	}
}

func TestClient_Call_SocketNotFound(t *testing.T) {
	client := NewClient("/tmp/non-existent-socket.sock")

	output, metadata, err := client.Call("test", "input")

	if err == nil {
		t.Error("Expected error for non-existent socket, got nil")
	}

	if !strings.Contains(err.Error(), "server not running") {
		t.Errorf("Expected 'server not running' error, got: %v", err)
	}

	if output != "" {
		t.Errorf("Expected empty output, got %q", output)
	}

	if metadata != nil {
		t.Errorf("Expected nil metadata, got %v", metadata)
	}
}

func TestClient_Call_Success(t *testing.T) {
	// Create a temporary socket for testing
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server goroutine
	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()

		// Read request
		decoder := json.NewDecoder(conn)
		var req Request
		if decodeErr := decoder.Decode(&req); decodeErr != nil {
			t.Errorf("Server failed to decode request: %v", decodeErr)
			return
		}

		// Send response
		resp := NewSuccessResponse(req.ID, "test output")
		resp.Result.Meta = map[string]string{"key": "value"}
		encoder := json.NewEncoder(conn)
		if encodeErr := encoder.Encode(resp); encodeErr != nil {
			t.Errorf("Server failed to encode response: %v", encodeErr)
		}
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	// Client call
	client := NewClient(socketPath)
	output, metadata, err := client.Call("test-method", "test input")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if output != "test output" {
		t.Errorf("Expected output 'test output', got %q", output)
	}

	if metadata == nil || metadata["key"] != "value" {
		t.Errorf("Expected metadata with key=value, got %v", metadata)
	}

	// Wait for server to finish
	serverWg.Wait()
}

func TestClient_Call_ErrorResponse(t *testing.T) {
	// Create a temporary socket for testing
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server goroutine
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()

		// Read request
		decoder := json.NewDecoder(conn)
		var req Request
		if decodeErr := decoder.Decode(&req); decodeErr != nil {
			return
		}

		// Send error response
		resp := NewErrorResponse(req.ID, InternalError, "Something went wrong")
		encoder := json.NewEncoder(conn)
		encoder.Encode(resp)
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	// Client call
	client := NewClient(socketPath)
	output, metadata, err := client.Call("test-method", "test input")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "Something went wrong") {
		t.Errorf("Expected error message to contain 'Something went wrong', got: %v", err)
	}

	if output != "" {
		t.Errorf("Expected empty output on error, got %q", output)
	}

	if metadata != nil {
		t.Errorf("Expected nil metadata on error, got %v", metadata)
	}
}

func TestClient_Call_InvalidResponse(t *testing.T) {
	// Create a temporary socket for testing
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock server that sends invalid JSON
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server goroutine
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()

		// Read request (just read enough to get past the request)
		buf := make([]byte, 1024)
		conn.Read(buf)

		// Send invalid JSON
		conn.Write([]byte("not valid json"))
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	// Client call
	client := NewClient(socketPath)
	output, metadata, err := client.Call("test-method", "test input")

	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}

	if output != "" {
		t.Errorf("Expected empty output on error, got %q", output)
	}

	if metadata != nil {
		t.Errorf("Expected nil metadata on error, got %v", metadata)
	}
}

func TestClient_Call_ConnectionTimeout(t *testing.T) {
	// Create a temporary socket for testing
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock server that never responds
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server goroutine that accepts but never responds
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()

		// Never respond, just sleep
		time.Sleep(1 * time.Second)
	}()

	// Give server time to start
	time.Sleep(5 * time.Millisecond)

	// Client with very short timeout (100ms)
	client := NewClientWithTimeout(socketPath, 100*time.Millisecond)

	start := time.Now()
	_, _, err = client.Call("test-method", "test input")
	duration := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	// Should timeout within ~100ms (plus some overhead)
	if duration > 200*time.Millisecond {
		t.Errorf("Call took too long to timeout: %v", duration)
	}
}

func TestTryCallWithFallback_ServerAvailable(t *testing.T) {
	// Create a temporary socket for testing
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Set socket path environment variable
	t.Setenv("CC_TOOLS_SOCKET", socketPath)

	// Start a mock server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	// Server goroutine
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()

		// Read request
		decoder := json.NewDecoder(conn)
		var req Request
		if decodeErr := decoder.Decode(&req); decodeErr != nil {
			return
		}

		// Send response based on method
		var resp Response
		if req.Method == "lint" {
			resp = NewSuccessResponse(req.ID, "server lint result")
		} else {
			resp = NewErrorResponse(req.ID, MethodNotFound, "Unknown method")
		}

		encoder := json.NewEncoder(conn)
		encoder.Encode(resp)
	}()

	// Give server time to start
	time.Sleep(10 * time.Millisecond)

	// Call with fallback (should use server)
	fallbackCalled := false
	fallbackFunc := func() (string, error) {
		fallbackCalled = true
		return "fallback result", nil
	}

	result, err := TryCallWithFallback("lint", fallbackFunc)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result != "server lint result" {
		t.Errorf("Expected server result, got %q", result)
	}

	if fallbackCalled {
		t.Error("Fallback should not have been called when server is available")
	}
}

func TestTryCallWithFallback_NoServer(t *testing.T) {
	// Set socket path to non-existent location
	t.Setenv("CC_TOOLS_SOCKET", "/tmp/non-existent-socket.sock")

	// Also set NO_SERVER flag to ensure no server is attempted
	t.Setenv("CC_TOOLS_NO_SERVER", "1")

	// Call with fallback (should use fallback)
	fallbackCalled := false
	fallbackFunc := func() (string, error) {
		fallbackCalled = true
		return "fallback result", nil
	}

	result, err := TryCallWithFallback("lint", fallbackFunc)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result != "fallback result" {
		t.Errorf("Expected fallback result, got %q", result)
	}

	if !fallbackCalled {
		t.Error("Fallback should have been called when server is not available")
	}
}

func TestTryCallWithFallback_FallbackError(t *testing.T) {
	// Set NO_SERVER flag to ensure fallback is used
	t.Setenv("CC_TOOLS_NO_SERVER", "1")

	// Call with fallback that returns error
	fallbackFunc := func() (string, error) {
		return "", fmt.Errorf("fallback failed")
	}

	result, err := TryCallWithFallback("lint", fallbackFunc)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "fallback failed") {
		t.Errorf("Expected fallback error, got: %v", err)
	}

	if result != "" {
		t.Errorf("Expected empty result on error, got %q", result)
	}
}
