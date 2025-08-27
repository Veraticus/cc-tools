package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ServerDependencies holds all dependencies for the server.
type ServerDependencies struct {
	LintRunner  LintRunner
	TestRunner  TestRunner
	Statusline  StatuslineGenerator
	LockManager LockManager
	Logger      Logger
}

// LockManager manages resource locks.
type LockManager interface {
	Acquire(key, holder string) bool
	Release(key string)
}

// Logger provides logging functionality.
type Logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

// Server represents the improved server implementation.
type Server struct {
	socketPath string
	listener   net.Listener

	// Graceful shutdown
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Dependencies
	deps *ServerDependencies

	// Stats
	stats *ServerStats
}

// ServerStats tracks server statistics.
type ServerStats struct {
	mu           sync.RWMutex
	requestCount int64
	errorCount   int64
	activeConns  int32
	startTime    time.Time
}

// NewServer creates a new server with injected dependencies.
func NewServer(socketPath string, deps *ServerDependencies) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		socketPath: socketPath,
		ctx:        ctx,
		cancel:     cancel,
		deps:       deps,
		stats:      &ServerStats{startTime: time.Now()},
	}
}

// Run starts the server and blocks until shutdown.
func (s *Server) Run() error {
	// Ensure socket directory exists
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	// Remove old socket if exists
	os.Remove(s.socketPath)

	// Listen on socket
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	s.listener = listener

	// Set socket permissions (owner only)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		s.deps.Logger.Println("Shutting down server...")
		s.Shutdown()
	}()

	s.deps.Logger.Printf("Server listening on %s", s.socketPath)

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return nil // Clean shutdown
			default:
				s.deps.Logger.Printf("Accept error: %v", err)
				continue
			}
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection processes a client connection.
func (s *Server) handleConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Track connection stats
	s.stats.mu.Lock()
	s.stats.activeConns++
	s.stats.mu.Unlock()

	defer func() {
		s.stats.mu.Lock()
		s.stats.activeConns--
		s.stats.mu.Unlock()
	}()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		// Check for shutdown
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		// Set read deadline
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// Read request
		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err.Error() == "EOF" || os.IsTimeout(err) {
				return
			}
			// Send parse error
			encoder.Encode(NewErrorResponse(RequestID{}, ParseError, "Parse error"))
			return
		}

		// Update stats
		s.stats.mu.Lock()
		s.stats.requestCount++
		s.stats.mu.Unlock()

		// Process request
		resp := s.processRequest(req)

		// Send response
		if err := encoder.Encode(resp); err != nil {
			return
		}
	}
}

// processRequest handles a single request.
func (s *Server) processRequest(req Request) Response {
	// Log the request
	s.deps.Logger.Printf("[SERVER] Processing %s request (ID: %s)", req.Method, req.ID.value)
	
	// Validate JSON-RPC version
	if req.JSONRPC != "2.0" {
		return NewErrorResponse(req.ID, InvalidRequest, "Invalid Request")
	}

	// Route to handler based on method
	var resp Response
	start := time.Now()
	
	switch req.Method {
	case "statusline":
		resp = s.handleStatusline(req)
	case "lint":
		resp = s.handleLint(req)
	case "test":
		resp = s.handleTest(req)
	case "stats":
		resp = s.handleStats(req)
	default:
		resp = NewErrorResponse(req.ID, MethodNotFound, fmt.Sprintf("Method not found: %s", req.Method))
	}
	
	// Log completion
	duration := time.Since(start)
	if resp.Error != nil {
		s.deps.Logger.Printf("[SERVER] %s failed in %v: %s", req.Method, duration, resp.Error.Message)
	} else {
		s.deps.Logger.Printf("[SERVER] %s completed in %v", req.Method, duration)
	}
	
	return resp
}

// handleStatusline processes statusline requests.
func (s *Server) handleStatusline(req Request) Response {
	// Parse params
	var params MethodParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, InvalidParams, fmt.Sprintf("Invalid params: %v", err))
		}
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	// Generate statusline
	input := bytes.NewReader([]byte(params.Input))
	result, err := s.deps.Statusline.Generate(ctx, input)
	if err != nil {
		s.stats.mu.Lock()
		s.stats.errorCount++
		s.stats.mu.Unlock()
		return NewErrorResponse(req.ID, InternalError, err.Error())
	}

	return NewSuccessResponseWithMeta(req.ID, result, map[string]string{"via": "server"})
}

// handleLint processes lint requests.
func (s *Server) handleLint(req Request) Response {
	// Parse params
	var params MethodParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, InvalidParams, fmt.Sprintf("Invalid params: %v", err))
		}
	}

	// Acquire lock if project specified
	if params.Project != "" {
		lockKey := fmt.Sprintf("%s:lint", params.Project)
		if !s.deps.LockManager.Acquire(lockKey, "server") {
			return NewErrorResponse(req.ID, InternalError, "Resource locked")
		}
		defer s.deps.LockManager.Release(lockKey)
	}

	// Create context with timeout
	timeout := 30 * time.Second
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	// Run lint
	input := bytes.NewReader([]byte(params.Input))
	output, err := s.deps.LintRunner.Run(ctx, input)
	if err != nil {
		s.stats.mu.Lock()
		s.stats.errorCount++
		s.stats.mu.Unlock()
		return NewErrorResponse(req.ID, InternalError, err.Error())
	}

	// Read output
	outputBytes, err := io.ReadAll(output)
	if err != nil {
		return NewErrorResponse(req.ID, InternalError, fmt.Sprintf("Read output: %v", err))
	}

	return NewSuccessResponseWithMeta(req.ID, string(outputBytes), map[string]string{"via": "server"})
}

// handleTest processes test requests.
func (s *Server) handleTest(req Request) Response {
	// Parse params
	var params MethodParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, InvalidParams, fmt.Sprintf("Invalid params: %v", err))
		}
	}

	// Acquire lock if project specified
	if params.Project != "" {
		lockKey := fmt.Sprintf("%s:test", params.Project)
		if !s.deps.LockManager.Acquire(lockKey, "server") {
			return NewErrorResponse(req.ID, InternalError, "Resource locked")
		}
		defer s.deps.LockManager.Release(lockKey)
	}

	// Create context with timeout
	timeout := 60 * time.Second
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	// Run test
	input := bytes.NewReader([]byte(params.Input))
	output, err := s.deps.TestRunner.Run(ctx, input)
	if err != nil {
		s.stats.mu.Lock()
		s.stats.errorCount++
		s.stats.mu.Unlock()
		return NewErrorResponse(req.ID, InternalError, err.Error())
	}

	// Read output
	outputBytes, err := io.ReadAll(output)
	if err != nil {
		return NewErrorResponse(req.ID, InternalError, fmt.Sprintf("Read output: %v", err))
	}

	return NewSuccessResponseWithMeta(req.ID, string(outputBytes), map[string]string{"via": "server"})
}

// handleStats returns server statistics.
func (s *Server) handleStats(req Request) Response {
	s.stats.mu.RLock()
	defer s.stats.mu.RUnlock()
	
	uptime := time.Since(s.stats.startTime).Round(time.Second)
	stats := fmt.Sprintf("Server Stats:\n"+
		"  Uptime: %v\n"+
		"  Requests: %d\n"+
		"  Errors: %d\n"+
		"  Active Connections: %d\n"+
		"  Socket: %s",
		uptime, s.stats.requestCount, s.stats.errorCount, 
		s.stats.activeConns, s.socketPath)
	
	return NewSuccessResponse(req.ID, stats)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown() {
	s.cancel() // Signal shutdown

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	// Wait for active connections
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.deps.Logger.Println("Clean shutdown completed")
	case <-time.After(5 * time.Second):
		s.deps.Logger.Println("Forced shutdown after timeout")
	}

	// Cleanup
	os.Remove(s.socketPath)
}