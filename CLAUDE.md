# cc-tools Development Guide

This document provides guidance for Claude when working on the cc-tools codebase.

## Project Overview

cc-tools is a high-performance Go implementation of Claude Code hooks and utilities. It replaces the original bash implementation with a client-server architecture for improved performance and reliability.

## Architecture Principles

### Client-Server Design

The project uses a daemon architecture to eliminate startup overhead:

1. **Server runs continuously** - Started via systemd or manually with `cc-tools serve`
2. **Clients are lightweight** - Connect via Unix socket, send request, receive response
3. **Protocol is JSON-RPC** - Simple, standard protocol for IPC
4. **Socket at** `/run/user/$(id -u)/cc-tools.sock`

### Hook Philosophy

Hooks follow these principles:

1. **Discover, don't configure** - Find existing project commands automatically
2. **Walk up, not down** - Start from edited file, walk up to find project root
3. **Lock properly** - Use PID-based locking to prevent concurrent runs
4. **Exit codes matter** - 0 = silent success, 2 = show message (success or failure)

## Code Organization

### Package Structure

```
internal/
├── hooks/          # Core hook logic
│   ├── discovery.go    # Command discovery
│   ├── executor.go     # Command execution
│   ├── lock.go         # PID-based locking
│   └── input.go        # JSON input parsing
├── server/         # Client-server implementation
│   ├── server.go       # Server main logic
│   ├── client.go       # Client implementation
│   ├── protocol.go     # JSON-RPC protocol
│   └── hook_runners.go # Hook runner implementations
├── statusline/     # Statusline generation
│   ├── statusline.go   # Main statusline logic
│   ├── render.go       # Rendering logic
│   └── colors.go       # Color schemes
└── shared/         # Shared utilities
    ├── project.go      # Project detection
    └── dependencies.go # Dependency injection
```

### Key Interfaces

```go
// LintRunner runs lint commands
type LintRunner interface {
    Run(ctx context.Context, input string) (string, error)
}

// TestRunner runs test commands  
type TestRunner interface {
    Run(ctx context.Context, input string) (string, error)
}

// LockManager manages resource locks
type LockManager interface {
    Acquire(key, holder string) bool
    Release(key string)
}
```

## Development Standards

### Go Patterns

1. **Concrete types over interfaces** - Interfaces hide bugs
2. **Early returns** - Keep code flat and readable
3. **Context everywhere** - Pass context.Context for cancellation
4. **Explicit errors** - Return errors, don't panic
5. **Table-driven tests** - Easy to add test cases

### Error Handling

```go
// Good: Wrap errors with context
if err := cmd.Run(); err != nil {
    return fmt.Errorf("execute lint command: %w", err)
}

// Bad: Generic error
if err := cmd.Run(); err != nil {
    return err
}
```

### Testing

```go
// Use table-driven tests
func TestDiscovery(t *testing.T) {
    tests := []struct {
        name     string
        setup    func(*testing.T, string)
        want     *DiscoveredCommand
        wantErr  bool
    }{
        {
            name: "finds make lint",
            setup: func(t *testing.T, dir string) {
                createMakefile(t, dir, "lint:\n\techo lint")
            },
            want: &DiscoveredCommand{
                Type: "make",
                Args: []string{"lint"},
            },
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation
        })
    }
}
```

## Adding Features

### Adding a New Hook

1. Create runner in `internal/server/hook_runners.go`:

```go
type MyHookRunner struct {
    enabled bool
    timeout time.Duration
}

func (r *MyHookRunner) Run(ctx context.Context, input string) (string, error) {
    // Implementation
}
```

2. Add to server dependencies:

```go
deps := &ServerDependencies{
    MyRunner: NewMyHookRunner(),
    // ...
}
```

3. Add command in `cmd/cc-tools/main.go`:

```go
case "myhook":
    runMyHookWithServer()
```

### Adding Command Discovery

Edit `internal/hooks/discovery.go`:

```go
// Add to command type constants
const (
    CommandTypeMake = "make"
    CommandTypeNPM  = "npm"
    CommandTypeNew  = "new"  // Your new type
)

// Add to discovery logic
func (cd *CommandDiscoverer) discoverInDirectory(dir string) *DiscoveredCommand {
    // Existing checks...
    
    // Add your check
    if cmd := cd.checkNewTool(dir); cmd != nil {
        return cmd
    }
}
```

## Performance Considerations

### Startup Time

- **Client mode**: < 5ms with running server
- **Direct mode**: ~20-30ms (Go startup + execution)
- **Server mode**: One-time startup cost

### Memory Usage

- **Server**: ~10-20MB resident
- **Client**: ~5MB (temporary)
- **Caching**: Project discovery cached in server

### Optimization Techniques

1. **Reuse server connection** - Don't create new connections repeatedly
2. **Cache project roots** - Discovery is expensive
3. **Batch operations** - Send multiple requests in one connection
4. **Timeout appropriately** - Don't wait forever for slow commands

## Debugging

### Enable Debug Logging

```bash
# Environment variable
export CLAUDE_HOOKS_DEBUG=1

# Server verbose mode
cc-tools serve --verbose

# Check server stats
cc-tools status
```

### Common Issues

1. **Socket permission errors**: Ensure socket directory is writable
2. **Command not found**: Check PATH is set correctly
3. **Timeouts**: Increase timeout or optimize commands
4. **Lock contention**: Check for stale lock files

### Testing Hooks Locally

```bash
# Test lint hook
echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "main.go"}}' | cc-tools lint

# Test with debug
CLAUDE_HOOKS_DEBUG=1 echo '{"file_path": "main.go"}' | cc-tools lint

# Test server directly
echo '{"method": "lint", "params": {"file_path": "main.go"}}' | nc -U /run/user/$(id -u)/cc-tools.sock
```

## Integration with Claude Code

### Hook Registration

Hooks are registered in Claude Code settings:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "command": "cc-tools",
        "args": ["lint"],
        "tools": ["Edit", "Write"]
      },
      {
        "command": "cc-tools",
        "args": ["test"],
        "tools": ["Edit", "Write"]
      }
    ]
  }
}
```

### Exit Code Protocol

- **0**: Success, no message (continue silently)
- **2**: Show message to user (both success and failure)
- **Other**: Error, treated as failure

### Message Format

Messages shown to user (exit code 2) should be concise:

```
✅ Lints pass. Continue with your task.
⛔ BLOCKING: Must fix ALL lint failures
```

## Release Process

1. **Update version** in build process
2. **Run full test suite**: `make check`
3. **Test with Claude Code**: Manual integration test
4. **Update NixOS module**: Ensure compatibility
5. **Tag release**: `git tag v1.0.0`

## NixOS Integration

The project includes a NixOS module for easy deployment:

```nix
# nix/module.nix provides:
- systemd service definition
- socket activation support
- environment variable configuration
- wrapper script generation
```

## Troubleshooting Production Issues

### Server Crashes

```bash
# Check systemd logs
journalctl -u cc-tools -n 100

# Check for core dumps
coredumpctl list

# Manual restart
systemctl --user restart cc-tools
```

### Performance Degradation

```bash
# Check server stats
cc-tools status

# Monitor resource usage
htop -p $(pgrep cc-tools)

# Profile if needed
go tool pprof http://localhost:6060/debug/pprof/profile
```

### Lock File Issues

```bash
# Find stale locks
ls -la /tmp/claude-hook-*.lock

# Check lock contents
cat /tmp/claude-hook-lint-*.lock

# Verify PID exists
ps -p $(head -1 /tmp/claude-hook-lint-*.lock)
```

## Best Practices

1. **Keep hooks fast** - Under 5 seconds ideal, 10 seconds max
2. **Fail clearly** - Error messages should be actionable
3. **Test thoroughly** - Unit tests, integration tests, manual testing
4. **Document changes** - Update this guide when adding features
5. **Maintain compatibility** - Don't break existing configurations