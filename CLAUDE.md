# cc-tools Development Guide

This document provides guidance for Claude when working on the cc-tools codebase.

## Project Overview

cc-tools is a high-performance Go implementation of Claude Code hooks and utilities. It provides smart linting, testing, and statusline generation that runs directly in the user's shell environment.

## Architecture Principles

### Direct Execution Model

The project runs commands directly in the user's current shell context to ensure access to:

1. **Nix shell environments** - nix develop, nix-shell
2. **Virtual environments** - Python venv, node_modules/.bin, Ruby gems
3. **Dynamic PATH** - direnv, asdf, and other environment managers
4. **Project variables** - Environment-specific configurations

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
├── config/         # Configuration management
│   └── config.go       # Config loading and structures
├── hooks/          # Core hook logic
│   ├── discovery.go    # Command discovery
│   ├── executor.go     # Command execution
│   ├── lock.go         # PID-based locking
│   └── input.go        # JSON input parsing
├── statusline/     # Statusline generation
│   ├── statusline.go   # Main statusline logic
│   ├── render.go       # Rendering logic
│   └── colors.go       # Color schemes
└── shared/         # Shared utilities
    ├── project.go      # Project detection
    └── colors.go       # Color utilities
```

### Key Interfaces

```go
// CommandDiscoverer finds commands in project
type CommandDiscoverer interface {
    Discover(startDir string, commandType string) *DiscoveredCommand
}

// LockManager manages resource locks
type LockManager interface {
    TryAcquire(key string) (bool, string, error)
    Release(key string) error
}

// Statusline generates status lines
type Statusline interface {
    Generate(reader io.Reader) (string, error)
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

### Adding Configuration Options

Edit `internal/config/config.go` to add new configuration fields:

```go
type Config struct {
    Hooks HooksConfig `mapstructure:"hooks"`
    // Add new sections here
}
```

## Performance Considerations

### Startup Time

- **Direct execution**: ~20-30ms (Go startup + execution)
- **No daemon overhead**: Commands run immediately

### Memory Usage

- **Minimal footprint**: ~5MB during execution
- **No persistent processes**: Memory freed after command completes

### Optimization Techniques

1. **Cache project roots** - Discovery is expensive
2. **Reuse compiled patterns** - Compile regexes once
3. **Timeout appropriately** - Don't wait forever for slow commands
4. **Stream output** - Don't buffer large outputs

## Debugging

### Enable Debug Logging

```bash
# Environment variable
export CLAUDE_HOOKS_DEBUG=1

# Check debug logs
tail -f /tmp/cc-tools.debug
```

### Common Issues

1. **Command not found**: Check PATH is set correctly
2. **Timeouts**: Increase timeout or optimize commands
3. **Lock contention**: Check for stale lock files
4. **Wrong environment**: Ensure running in correct shell context

### Testing Hooks Locally

```bash
# Test lint hook
echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "main.go"}}' | cc-tools lint

# Test with debug
CLAUDE_HOOKS_DEBUG=1 echo '{"file_path": "main.go"}' | cc-tools lint

# Test statusline
echo '{"cwd": "$(pwd)"}' | cc-tools statusline
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
4. **Tag release**: `git tag v1.0.0`

## Troubleshooting Production Issues

### Lock File Issues

```bash
# Find stale locks
ls -la /tmp/claude-hook-*.lock

# Check lock contents
cat /tmp/claude-hook-lint-*.lock

# Verify PID exists
ps -p $(head -1 /tmp/claude-hook-lint-*.lock)
```

### Performance Degradation

```bash
# Check execution time
time echo '{"file_path": "main.go"}' | cc-tools lint

# Monitor system resources during execution
top -p $(pgrep cc-tools)

# Enable debug logging
CLAUDE_HOOKS_DEBUG=1 cc-tools lint
```

## Best Practices

1. **Keep hooks fast** - Under 5 seconds ideal, 10 seconds max
2. **Fail clearly** - Error messages should be actionable
3. **Test thoroughly** - Unit tests, integration tests, manual testing
4. **Document changes** - Update this guide when adding features
5. **Maintain compatibility** - Don't break existing configurations