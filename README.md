# cc-tools

High-performance Go implementation of Claude Code hooks and utilities. Provides smart linting, testing, and statusline generation with improved performance through client-server architecture.

## Features

### 🚀 Smart Hooks
- **Automatic lint/test discovery** - Finds and runs your project's existing commands
- **Project-aware execution** - Walks up directory tree to find project root
- **Intelligent concurrency control** - PID-based locking prevents duplicate runs
- **Clear feedback** - Shows success messages or blocking errors in Claude

### ⚡ Performance Optimized
- **Client-server architecture** - Daemon mode eliminates startup overhead
- **Unix socket communication** - Fast IPC between client and server
- **Efficient caching** - Reuses project discovery across requests
- **Minimal dependencies** - Pure Go implementation

### 📊 Rich Statusline
- **Model information** - Shows current Claude model
- **Git integration** - Branch, status, and uncommitted changes
- **Token metrics** - Input/output/cached token counts with visual bars
- **Context awareness** - Kubernetes context, AWS profile, custom workspace
- **Cost tracking** - Running cost total for the session

## Installation

### NixOS (Recommended)

The project is designed for NixOS and integrates with home-manager:

```nix
# In your home-manager configuration
{
  imports = [ ./path-to/cc-tools/nix/module.nix ];
  
  services.cc-tools = {
    enable = true;
    settings = {
      lintEnabled = true;
      testEnabled = true;
      lintTimeout = 30;
      testTimeout = 60;
    };
  };
}
```

### Manual Installation

```bash
# Build the binary
go build -o cc-tools cmd/cc-tools/main.go

# Start the server
cc-tools serve

# Use the tools
echo '{"file_path": "main.go"}' | cc-tools lint
echo '{"file_path": "main_test.go"}' | cc-tools test
echo '{"cwd": "/path/to/project"}' | cc-tools statusline
```

## Usage

### Server Mode

For best performance, run cc-tools as a daemon:

```bash
# Start server (usually done via systemd)
cc-tools serve --socket /run/user/$(id -u)/cc-tools.sock

# Check server status
cc-tools status
```

### Smart Linting

Automatically discovers and runs your project's lint commands:

```bash
# Via Claude Code hook
echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "main.go"}}' | cc-tools lint

# Direct usage
echo '{"file_path": "main.go"}' | cc-tools lint
```

Searches for (in order):
1. `make lint`
2. `just lint`  
3. `npm/yarn/pnpm run lint`
4. `./scripts/lint`
5. Language-specific tools (golangci-lint, ruff, cargo clippy, etc.)

### Smart Testing

Automatically discovers and runs your project's test commands:

```bash
# Via Claude Code hook
echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "main_test.go"}}' | cc-tools test

# Direct usage
echo '{"file_path": "main_test.go"}' | cc-tools test
```

Searches for (in order):
1. `make test`
2. `just test`
3. `npm/yarn/pnpm run test`
4. `./scripts/test`
5. Language-specific tools (go test, pytest, cargo test, etc.)

### Statusline

Generates a rich statusline for Claude Code prompts:

```bash
echo '{"cwd": "/path/to/project", "model": {"display_name": "Claude 3.5"}, "cost": {"input_tokens": 1000}}' | cc-tools statusline
```

Example output:
```
╭─ Claude 3.5 │ ~/project │ main* │ k8s:prod │ $0.42 │ ▁▃█ 15K ─╮
```

## Configuration

### Environment Variables

```bash
# Hook settings
export CLAUDE_HOOKS_LINT_ENABLED=true    # Enable/disable lint hook
export CLAUDE_HOOKS_TEST_ENABLED=true    # Enable/disable test hook
export CLAUDE_HOOKS_LINT_TIMEOUT=30      # Lint timeout in seconds
export CLAUDE_HOOKS_TEST_TIMEOUT=60      # Test timeout in seconds
export CLAUDE_HOOKS_LINT_COOLDOWN=2      # Seconds between lint runs
export CLAUDE_HOOKS_TEST_COOLDOWN=2      # Seconds between test runs
export CLAUDE_HOOKS_DEBUG=1              # Enable debug output

# Server settings
export CC_TOOLS_SOCKET=/run/user/$(id -u)/cc-tools.sock  # Socket path

# Statusline customization
export TMUX_DEVSPACE=myproject           # Custom workspace label
export CLAUDE_STATUSLINE_K8S_ENABLED=false  # Disable k8s context
```

### Project Configuration

Create `.claude-hooks-config.sh` in your project root:

```bash
# Custom make targets
export CLAUDE_HOOKS_MAKE_LINT_TARGETS="lint check"
export CLAUDE_HOOKS_MAKE_TEST_TARGETS="test test-unit"

# Custom script names
export CLAUDE_HOOKS_SCRIPT_LINT_NAMES="lint.sh check.sh"
export CLAUDE_HOOKS_SCRIPT_TEST_NAMES="test.sh run-tests.sh"

# Disable hooks for this project
export CLAUDE_HOOKS_LINT_ENABLED=false
export CLAUDE_HOOKS_TEST_ENABLED=false
```

### Ignore Patterns

Create `.claude-hooks-ignore` in your project root:

```
# Ignore generated files
*.pb.go
*_gen.go
generated/

# Ignore vendored code
vendor/
node_modules/

# Ignore test files
*_test.go
*.test.js
```

## Architecture

### Client-Server Model

```
┌─────────────┐     Unix Socket      ┌──────────────┐
│ Claude Code │ ──────────────────>  │   cc-tools   │
│   (client)  │ <──────────────────  │   (server)   │
└─────────────┘     JSON-RPC          └──────────────┘
                                             │
                                             ├── Lint Runner
                                             ├── Test Runner
                                             ├── Lock Manager
                                             └── Status Generator
```

### Lock Management

Uses PID-based file locking to prevent concurrent executions:

1. **Lock file**: `/tmp/claude-hook-{lint|test}-<workspace-hash>.lock`
2. **Contents**: PID (line 1), timestamp (line 2)
3. **Behavior**: Skips if running or within cooldown period

### Performance Characteristics

- **Startup time**: < 5ms (client mode with running server)
- **Command discovery**: Cached per project root
- **Memory usage**: ~10-20MB for server process
- **CPU usage**: Near-zero when idle

## Development

### Building

```bash
# Run tests
make test

# Run lints
make lint

# Build binary
make build

# Run all checks
make check
```

### Testing

```bash
# Unit tests
go test ./...

# With race detection
go test -race ./...

# Specific package
go test ./internal/hooks/...

# Verbose output
go test -v ./...
```

### Project Structure

```
cc-tools/
├── cmd/cc-tools/       # Main CLI application
├── internal/
│   ├── hooks/          # Smart lint/test discovery and execution
│   ├── server/         # Client-server implementation
│   ├── statusline/     # Statusline generation
│   └── shared/         # Shared utilities
├── nix/                # NixOS module and derivation
└── test_fixtures/      # Test data
```

## Troubleshooting

### Server Not Running

```bash
# Check status
cc-tools status

# Start manually
cc-tools serve --verbose

# Check socket exists
ls -la /run/user/$(id -u)/cc-tools.sock
```

### Commands Not Found

Enable debug mode to see discovery process:

```bash
export CLAUDE_HOOKS_DEBUG=1
echo '{"file_path": "main.go"}' | cc-tools lint
```

### Performance Issues

```bash
# Check server stats
cc-tools status

# Monitor server logs
journalctl -u cc-tools --follow  # If using systemd
```

## Migration from Bash Hooks

If migrating from the original bash implementation:

1. The wrapper scripts (`smart-lint-wrapper.sh`, etc.) now call cc-tools
2. All functionality is preserved with better performance
3. Configuration via environment variables remains the same
4. Lock file format is compatible

## Contributing

1. Fork the repository
2. Create a feature branch
3. Write tests for new functionality
4. Ensure `make check` passes
5. Submit a pull request

## License

MIT

## Author

Josh Symonds ([@Veraticus](https://github.com/Veraticus))