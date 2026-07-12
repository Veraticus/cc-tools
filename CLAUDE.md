# cc-tools Development Guide

Go implementation of terminal coding-agent utilities. Claude Code provides the
rich statusline/hook surface; Codex CLI is also supported for external
turn-complete notifications.

## Project Structure

```
cmd/
├── cc-tools/             # Main CLI with subcommands (debug, mcp, config)
└── cc-tools-statusline/  # Standalone statusline binary for Claude Code

internal/
├── config/       # JSON config management (~/.config/cc-tools/config.json)
├── debug/        # Debug logging control
├── mcp/          # MCP server enable/disable management
├── output/       # Terminal output formatting and tables
├── statusline/   # Statusline generation with Catppuccin colors
└── shared/       # Project detection, colors, debug paths
```

## Key Design Patterns

### Dependency Injection

All major components use constructor-injected dependencies for testability:

```go
// Statusline uses Dependencies struct
type Dependencies struct {
    FileReader    FileReader
    CommandRunner CommandRunner
    EnvReader     EnvReader
    TerminalWidth TerminalWidth
    CacheDir      string
    CacheDuration time.Duration
}
```

### Exit Code Protocol

Claude Code hooks use specific exit codes:
- `0`: Silent success
- `2`: Show message to user (success or failure with feedback)

## Configuration

Config stored at `~/.config/cc-tools/config.json`:

```json
{
  "statusline": {
    "workspace": "",
    "cache_dir": "/dev/shm",
    "cache_seconds": 20
  }
}
```

## Testing Locally

```bash
# Test statusline
echo '{"cwd": "'$(pwd)'", "model": {"display_name": "Claude"}}' | ./build/cc-tools-statusline

# Enable debug logging
./build/cc-tools debug enable
./build/cc-tools debug filename  # Shows log path
```

## Build Commands

```bash
make build    # Build all binaries to build/
make test     # Run tests with coverage
make lint     # Run gofmt, golangci-lint, deadcode
make install  # Copy binaries to ~/bin
```
