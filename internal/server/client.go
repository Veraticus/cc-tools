// Package server provides a JSON-RPC server for cc-tools.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultDialTimeout is the default timeout for connecting to the server.
	DefaultDialTimeout = 5 * time.Second
)

// Client handles communication with the server using concrete types.
type Client struct {
	socketPath  string
	dialTimeout time.Duration
}

// NewClient creates a new client instance with default timeout.
func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return &Client{
		socketPath:  socketPath,
		dialTimeout: DefaultDialTimeout,
	}
}

// NewClientWithTimeout creates a new client instance with custom timeout.
func NewClientWithTimeout(socketPath string, timeout time.Duration) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return &Client{
		socketPath:  socketPath,
		dialTimeout: timeout,
	}
}

// DefaultSocketPath returns the default socket path.
func DefaultSocketPath() string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		return filepath.Join(runtime, "cc-tools.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("cc-tools-%d.sock", os.Getuid()))
}

// Call executes a method on the server and returns the result.
func (c *Client) Call(method string, input string) (string, map[string]string, error) {
	// Check if socket exists
	if _, err := os.Stat(c.socketPath); os.IsNotExist(err) {
		return "", nil, fmt.Errorf("server not running (socket not found: %s)", c.socketPath)
	}

	// Connect to server
	d := &net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(context.Background(), "unix", c.socketPath)
	if err != nil {
		return "", nil, fmt.Errorf("connect to server: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Set read/write deadline based on dial timeout
	deadline := time.Now().Add(c.dialTimeout)
	if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
		return "", nil, fmt.Errorf("set deadline: %w", deadlineErr)
	}

	// Prepare request
	params := MethodParams{
		Input: input,
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return "", nil, fmt.Errorf("marshal params: %w", err)
	}

	req := Request{
		JSONRPC: jsonRPCVersion,
		ID:      RequestID{value: "1"},
		Method:  method,
		Params:  paramsJSON,
	}

	// Send request
	encoder := json.NewEncoder(conn)
	if encErr := encoder.Encode(req); encErr != nil {
		return "", nil, fmt.Errorf("send request: %w", encErr)
	}

	// Read response
	decoder := json.NewDecoder(conn)
	var resp Response
	if decErr := decoder.Decode(&resp); decErr != nil {
		return "", nil, fmt.Errorf("read response: %w", decErr)
	}

	// Check for error
	if resp.Error != nil {
		return "", nil, fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Extract result
	if resp.Result == nil {
		return "", nil, fmt.Errorf("no result in response")
	}

	return resp.Result.Output, resp.Result.Meta, nil
}

// TryCallWithFallback attempts to call the server, falling back to direct execution.
func TryCallWithFallback(method string, directFunc func() (string, error)) (string, error) {
	// Check if server mode is disabled
	if os.Getenv("CC_TOOLS_NO_SERVER") == "1" {
		fmt.Fprintf(os.Stderr, "[CC-TOOLS] ✗ Server disabled, using direct mode for %s\n", method)
		return directFunc()
	}

	// Try custom socket path if specified
	socketPath := os.Getenv("CC_TOOLS_SOCKET")
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}

	client := NewClient(socketPath)

	// Read stdin for input
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	// Try server first
	result, meta, err := client.Call(method, string(input))
	if err == nil {
		// Always show server usage in stderr when successful
		if meta != nil && meta["via"] == "server" {
			fmt.Fprintf(os.Stderr, "[CC-TOOLS] ✓ Using server for %s\n", method)
		}
		return result, nil
	}

	// Always show fallback in stderr with error details for debugging
	fmt.Fprintf(os.Stderr, "[CC-TOOLS] ✗ Server unavailable, using direct mode for %s (error: %v)\n", method, err)

	// Fallback to direct execution
	return directFunc()
}
