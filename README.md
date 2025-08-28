# cc-tools

High-performance Go implementation of Claude Code hooks and utilities. Provides smart linting, testing, and statusline generation with minimal overhead.

## Features

### 🚀 Smart Hooks
- **Automatic validation** - Finds and runs lint and test commands in parallel
- **Project-aware execution** - Walks up directory tree to find project root
- **Intelligent concurrency control** - PID-based locking prevents duplicate runs
- **Clear feedback** - Shows consolidated success messages or blocking errors in Claude

### ⚡ Performance Optimized
- **Fast startup** - Pure Go implementation with minimal dependencies
- **Efficient caching** - Reuses project discovery across requests
- **Smart command execution** - Runs in user's current shell environment

### 📊 Rich Statusline
- **Model information** - Shows current Claude model
- **Git integration** - Branch, status, and uncommitted changes
- **Token metrics** - Input/output/cached token counts with visual bars
- **Context awareness** - Kubernetes context, AWS profile, custom workspace
- **Cost tracking** - Running cost total for the session

## Installation

### NixOS (Recommended)

The project is designed for NixOS and can be built with Nix:

```bash
# Build the binary
nix-build

# Or add to your system packages
```

### Manual Installation

```bash
# Build the binary
go build -o cc-tools cmd/cc-tools/main.go

# Use the tools
echo '{"file_path": "main.go"}' | cc-tools lint
echo '{"file_path": "main_test.go"}' | cc-tools test
echo '{"cwd": "/path/to/project"}' | cc-tools statusline
```

## Usage

### Smart Linting & Testing Behavior

The lint and test hooks follow a precise workflow designed for seamless Claude Code integration:

#### Execution Flow

1. **Project Root Discovery**: Finds the top-level directory by looking for markers like `.git`, `Makefile`, `Justfile`, `package.json`, `go.mod`, etc.

2. **Lock Acquisition**: Attempts to acquire an exclusive lock from `/tmp/claude-hook-{lint|test}-<workspace-hash>.lock`

3. **Exit Codes and Messages**:
   - **Lock unavailable**: Exit code `0`, no output (silent failure)
   - **Command succeeds**: Exit code `2`, displays `👉 Lints/Tests pass. Continue with your task.`
   - **Command fails**: Exit code `2`, displays `⛔ BLOCKING: Run 'cd <dir> && <command>' to fix failures`
   - **Command timeout**: Exit code `2`, displays `⛔ BLOCKING: Command timed out after <timeout>`

4. **Lock Release**: Writes timestamp to lock file for cooldown enforcement

#### Important Notes

- **Always exit code 2** when displaying messages (both success and failure)
- **Silent exit 0** only when lock cannot be acquired
- **Error messages include** the exact command that was run and the directory to run it from
- **Stdout and stderr** are captured separately but not displayed to reduce noise

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

### Example Hook Output

#### Successful Lint
```bash
$ echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "/project/src/main.go"}}' | cc-tools lint
👉 Lints pass. Continue with your task.
$ echo $?
2
```

#### Failed Lint
```bash
$ echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "/project/src/main.go"}}' | cc-tools lint
⛔ BLOCKING: Run 'cd /project && make lint' to fix lint failures
$ echo $?
2
```

#### Lock Unavailable (Another Instance Running)
```bash
$ echo '{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": {"file_path": "/project/src/main.go"}}' | cc-tools lint
$ echo $?
0
```

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

### Direct Execution Model

cc-tools runs commands directly in the user's current shell environment, ensuring access to:
- Nix shell environments (nix develop)
- Virtual environments (Python venv, node_modules/.bin, Ruby gems)
- Dynamic PATH modifications (direnv, asdf, etc.)
- Project-specific environment variables

### Lock Management

Uses PID-based file locking to prevent concurrent executions:

1. **Lock file**: `/tmp/claude-hook-{lint|test}-<workspace-hash>.lock`
2. **Contents**: PID (line 1), timestamp (line 2)
3. **Behavior**: Skips if running or within cooldown period

### Performance Characteristics

- **Startup time**: ~20-30ms (Go binary startup + execution)
- **Command discovery**: Cached per project root
- **Memory usage**: Minimal (~5MB during execution)

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
│   ├── config/         # Configuration management
│   ├── hooks/          # Smart lint/test discovery and execution
│   ├── statusline/     # Statusline generation
│   └── shared/         # Shared utilities
└── test_fixtures/      # Test data
```

## Troubleshooting

### Commands Not Found

Enable debug mode to see discovery process:

```bash
export CLAUDE_HOOKS_DEBUG=1
echo '{"file_path": "main.go"}' | cc-tools lint
```

### Performance Issues

Check debug logs:

```bash
tail -f /tmp/cc-tools.debug
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