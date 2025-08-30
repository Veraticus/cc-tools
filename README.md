# cc-tools

High-performance Go implementation of Claude Code hooks and utilities. Provides smart linting, testing, and statusline generation with minimal overhead.

## Features

### 🚀 Smart Validation Hooks
- **Auto-discovery** - Finds and runs your project's lint/test commands automatically
- **Parallel execution** - Runs linting and testing simultaneously for speed
- **Lock management** - Prevents duplicate runs with PID-based locking
- **Clear feedback** - Success messages or blocking errors right in Claude Code
- **Skip controls** - Temporarily disable per-directory when needed

### 📊 Rich Statusline
- **Model & cost tracking** - Current model, token usage, running costs
- **Git awareness** - Branch, dirty status, uncommitted file count
- **Environment context** - Kubernetes cluster, AWS profile, custom workspace
- **Visual indicators** - Token usage bars, color-coded states
- **Performance** - Cached results with 20-second refresh

### 🎛️ Development Controls
- **MCP management** - Enable/disable context servers per-project
- **Debug logging** - Detailed execution logs for troubleshooting
- **Skip registry** - Pause validation for specific directories
- **No daemon required** - Direct execution, no background processes

### 🔍 Command Discovery
- **Build tools** - Make, Just, NPM, Yarn, PNPM, Cargo
- **Language-specific** - golangci-lint, ruff, pytest, cargo clippy
- **Custom scripts** - Finds `./scripts/lint`, `./scripts/test`
- **Config-aware** - Reads project settings and environment variables
- **Timeout protection** - Configurable limits prevent hanging

## Installation

### Claude Code Hooks

cc-tools provides powerful hooks that you can use in Claude Code itself. Hooks are shipped as separate binaries so that you may
invoke them directly from Claude Code's `settings.json`

- **`cc-tools-validate`** - Runs linting and testing on file changes
- **`cc-tools-statusline`** - Generates the rich statusline

### Download Pre-built Binaries

Download the latest release from [GitHub Releases](https://github.com/Veraticus/cc-tools/releases):

```bash
# Download and extract binaries
wget https://github.com/Veraticus/cc-tools/releases/latest/download/cc-tools-linux-amd64.tar.gz
tar -xzf cc-tools-linux-amd64.tar.gz

# Move to ~/.claude/bin/ (or any directory in your PATH)
mkdir -p ~/.claude/bin
mv cc-tools-validate ~/.claude/bin/
mv cc-tools-statusline ~/.claude/bin/
chmod +x ~/.claude/bin/cc-tools-*
```

### Build from Source (NixOS)

```bash
# Build with Nix
nix-build

# Copy the required binaries
cp ./result/bin/cc-tools-validate ~/.claude/bin/
cp ./result/bin/cc-tools-statusline ~/.claude/bin/
```

### Build from Source (Go)

```bash
# Build all binaries
make build

# Copy the required binaries
cp build/cc-tools-validate ~/.claude/bin/
cp build/cc-tools-statusline ~/.claude/bin/
```

### Claude Code Configuration

Add to your `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/bin/cc-tools-statusline",
    "padding": 0
  },
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "~/.claude/bin/cc-tools-validate"
          }
        ]
      }
    ]
  }
}
```

The `cc-tools-validate` binary automatically runs both linting and testing based on the edited files.

## Control Commands

The `cc-tools` binary provides powerful control commands for managing your development workflow:

### Skip Controls

Temporarily disable linting/testing for specific directories when you need to focus on rapid iteration:

```bash
# Skip both linting and testing in current directory
cc-tools skip all

# Skip only linting
cc-tools skip lint

# Skip only testing  
cc-tools skip test

# View skip status for current directory
cc-tools skip status

# List all directories with skip configurations
cc-tools skip list

# Remove all skips from current directory
cc-tools unskip

# Remove specific skip type
cc-tools unskip lint
cc-tools unskip test
```

### Debug Logging

Enable detailed debug logging to troubleshoot hook behavior:

```bash
# Enable debug logging for current directory
cc-tools debug enable

# Check debug status
cc-tools debug status

# View log file path
cc-tools debug filename

# List all directories with debug enabled
cc-tools debug list

# Disable debug logging
cc-tools debug disable
```

### MCP Server Management

Control which MCP (Model Context Protocol) servers are active per-project:

```bash
# List all MCP servers and their status
cc-tools mcp list

# Enable specific MCP server
cc-tools mcp enable jira
cc-tools mcp enable playwright

# Disable specific MCP server
cc-tools mcp disable targetprocess

# Bulk operations
cc-tools mcp enable-all    # Enable all configured MCPs
cc-tools mcp disable-all   # Disable all MCPs (reduce context)
```

MCP names support flexible matching (e.g., 'target' matches 'targetprocess').

MCP management reads your existing MCP configurations from `~/.claude/settings.json`. Example configuration:

```json
{
  "mcpServers": {
    "playwright": {
      "type": "stdio",
      "command": "~/.claude/playwright-mcp-wrapper.sh",
      "args": [],
      "env": {}
    },
    "targetprocess": {
      "type": "stdio",
      "command": "~/.claude/bin/targetprocess-mcp",
      "args": [],
      "env": {}
    },
    "jira": {
      "type": "stdio",
      "command": "~/.claude/jira-mcp-wrapper.sh",
      "args": [],
      "env": {}
    }
  }
}
```

## Behavior

### Linting & Testing Behavior

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

### Linting

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

### Testing

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

Example output: image

## Configuration

All configuration is managed through the `cc-tools config` command. Settings are stored in `~/.config/cc-tools/config.json` and are automatically created with defaults on first use.

### Viewing Configuration

```bash
# List all settings with current values and defaults
cc-tools config list

# Example output:
# Configuration:
#   statusline:
#     - statusline.cache_dir = /dev/shm (default)
#     - statusline.cache_seconds = 20 (default)
#     - statusline.workspace =  (default)
#   validate:
#     - validate.timeout = 90              # <-- Custom value (no "default" label)
#     - validate.cooldown = 5 (default)

# View the raw JSON config file
cc-tools config show

# Get a specific value
cc-tools config get validate.timeout
```

### Setting Configuration

```bash
# Set validation timeout to 90 seconds
cc-tools config set validate.timeout 90

# Set cooldown between validations to 10 seconds
cc-tools config set validate.cooldown 10

# Set custom workspace label for statusline
cc-tools config set statusline.workspace "my-project"

# Set cache directory (e.g., for systems without /dev/shm)
cc-tools config set statusline.cache_dir "/tmp"
```

### Resetting to Defaults

```bash
# Reset a specific setting to its default
cc-tools config reset validate.timeout

# Reset all settings to defaults
cc-tools config reset
```

### Available Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `validate.timeout` | 60 | Maximum seconds to wait for lint/test commands to complete |
| `validate.cooldown` | 5 | Minimum seconds between validation runs for the same project |
| `statusline.workspace` | "" | Custom label shown in statusline (e.g., project name) |
| `statusline.cache_dir` | /dev/shm | Directory for statusline cache files (fast tmpfs recommended) |
| `statusline.cache_seconds` | 20 | How long to cache statusline data before refreshing |

The `config list` command clearly shows which values are customized vs defaults, making it easy to see what you've changed from the standard configuration.

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

## License

MIT

## Author

Josh Symonds ([@Veraticus](https://github.com/Veraticus))
