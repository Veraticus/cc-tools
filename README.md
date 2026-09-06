# cc-tools

High-performance Go utilities for terminal coding agents. Claude Code has the
richest integration, and Codex CLI is supported for external turn-complete
notifications. The historical `cc-tools` and `cc-tools-statusline` names are
kept for compatibility.

## Features

### 📊 Rich Statusline
- **Model & cost tracking** - Current model, token usage, running costs
- **Git awareness** - Branch, dirty status, uncommitted file count
- **Environment context** - Kubernetes cluster, AWS profile, custom workspace
- **Visual indicators** - Token usage bars, color-coded states
- **Performance** - Cached results with 20-second refresh

### 🎛️ Development Controls
- **MCP management** - Enable/disable context servers per-project
- **Debug logging** - Detailed execution logs for troubleshooting
- **No daemon required** - Direct execution, no background processes

### 🔔 Turn Notifications

- **Deterministic root delivery** - Claude Stop and Codex/Pi TurnComplete events
  always deliver with done urgency; child and internal events stay silent
- **Immediate input delivery** - Claude permission, elicitation, and
  agent-needs-input events deliver with blocked urgency without model latency
- **Daemon-only Pi composition** - `notifyd` may improve a completion body using
  the configured Pi helper, but composition cannot veto delivery or change urgency
- **Provider-neutral configuration** - `CC_TOOLS_NTFY_*` environment variables,
  with the original `CLAUDE_HOOKS_NTFY_*` names retained as aliases

## Agent compatibility

| Capability | Claude Code | Codex CLI |
|---|---|---|
| External notification command | Yes, JSON on stdin | Yes, JSON in one argument |
| Root turn-complete ntfy delivery | Yes | Yes |
| Permission/external approval delivery | Yes | Use Codex's built-in TUI notifications |
| Daemon-side Pi body composition | Yes, using reliable transcript identity | Yes, using native turn identity |
| Replace the in-app status line with `cc-tools-statusline` | Yes | No; Codex accepts built-in footer item identifiers only |

## Installation

### Claude Code Hooks

cc-tools provides the statusline hook that you can use in Claude Code itself.

- **`cc-tools-statusline`** - Generates the rich statusline

### Download Pre-built Binaries

Download the latest release from [GitHub Releases](https://github.com/Veraticus/cc-tools/releases):

```bash
# Download and extract binaries
wget https://github.com/Veraticus/cc-tools/releases/latest/download/cc-tools-linux-amd64.tar.gz
tar -xzf cc-tools-linux-amd64.tar.gz

# Move to ~/.claude/bin/ (or any directory in your PATH)
mkdir -p ~/.claude/bin
mv cc-tools-statusline ~/.claude/bin/
chmod +x ~/.claude/bin/cc-tools-*
```

### Build from Source (NixOS)

```bash
# Build with Nix
nix-build

# Copy the required binaries
cp ./result/bin/cc-tools-statusline ~/.claude/bin/
```

### Build from Source (Go)

```bash
# Build all binaries
make build

# Copy the required binaries
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
  }
}
```

### Codex notifications

Codex's external notification command is a user-level setting. Add this at the
top level of `~/.codex/config.toml` (use the absolute path to your installed
binary):

```toml
notify = ["/home/you/bin/cc-tools", "notify"]
```

Configure ntfy in the environment inherited by Codex:

```bash
export CC_TOOLS_NTFY_URL="https://ntfy.sh/your-topic"
# Optional for an authenticated topic:
export CC_TOOLS_NTFY_TOKEN="your-token"
```

The `_FILE` variants are supported for secrets (`CC_TOOLS_NTFY_URL_FILE` and
`CC_TOOLS_NTFY_TOKEN_FILE`). Set `CC_TOOLS_NTFY_DISABLED=true` to suppress
delivery. The old `CLAUDE_HOOKS_NTFY_*` variables continue to work.

Root completion delivery is deterministic. When `notifyd` receives an eligible
completion with a usable native identity pair (session ID and completion ID),
it calls `steward-pi-helper` exactly once with the latest user and assistant
text. The helper may supply only a validated
plain-text body; it cannot suppress the notification or change its done urgency.
Claude Stop uses the transcript's reliable final assistant UUID, then
`message.id`; an unavailable or unreliable transcript never reuses a stale
supplied ID. Active Claude `/goal` state remains a structural silence gate.

Pi configuration is centralized in the daemon environment:

```bash
export STEWARD_HELPER_BIN="steward-pi-helper"
export STEWARD_MODEL_PROVIDER="openai-codex"
export STEWARD_MODEL_ID="gpt-5.6-luna"
export STEWARD_MODEL_THINKING="low"
```

Each default applies only when its setting is absent. An explicitly invalid
setting, unavailable helper, authentication failure, malformed response, or
timeout does not disable completion delivery: the decision log records only a
safe error category and ntfy receives a deterministic plain-text tail of the
final response. Fallback bodies are valid UTF-8, at most 160 bytes, and use
`turn complete` when empty. Generated bodies are bounded to 200 bytes. Titles
retain the cwd project name and tmux workspace locator (or host fallback).

If `notifyd` is unavailable, `cc-tools notify` uses the same model-free fallback
inline. Dry runs are model-free as well; neither path starts the helper or
attempts model authentication. Notification frames retain workspace routing but
never copy the caller's whole environment into daemon IPC.

The hook prepares each event once and waits up to 250 ms for a strict daemon
acknowledgement. `accepted` and `duplicate` acknowledgements suppress inline
work; a rejection, timeout, disconnect, or malformed acknowledgement runs one
inline fallback from the same prepared snapshot, without rescanning a Claude
transcript. Completion duplicate suppression is daemon-local and non-durable:
it covers in-flight and successful IDs for 24 hours (up to 10,000 claims), but a
daemon restart starts empty. Consequently an acknowledgement lost in transit
can produce a duplicate, while a daemon crash after an observed acceptance can
lose that notification. See [the notify protocol](docs/notify-protocol.md) for
the exact framing, bounds, and outage semantics.

You can exercise the Codex adapter without sending anything:

```bash
cc-tools notify --dry-run '{
  "type":"agent-turn-complete",
  "thread-id":"demo-thread",
  "turn-id":"demo-turn",
  "cwd":"/tmp/project",
  "input-messages":["demo"],
  "last-assistant-message":"The requested work is complete."
}'
```

Codex currently invokes external `notify` commands for `agent-turn-complete`.
Its built-in TUI notifications additionally understand events such as approval
requests:

```toml
[tui]
notifications = ["agent-turn-complete", "approval-requested"]
notification_condition = "unfocused" # or "always"
notification_method = "auto"         # or "osc9" / "bel"
```

### Codex status line

Codex cannot invoke `cc-tools-statusline` as its footer renderer. Unlike Claude
Code's `statusLine.type = "command"` contract, Codex's `tui.status_line` is an
ordered list of native item identifiers. A close native configuration is:

```toml
[tui]
status_line = [
  "model-with-reasoning",
  "current-dir",
  "git-branch",
  "context-remaining",
  "five-hour-limit",
  "weekly-limit",
]
status_line_use_colors = true
```

This covers the core model/directory/git/context/rate-limit information, but
the custom AWS, Google Cloud, Kubernetes, transcript-cost, and powerline-chip
rendering in this repository cannot be injected into Codex's footer today.

## Control Commands

The `cc-tools` binary provides control commands for managing your development workflow:

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

### Statusline

Generates a rich statusline for Claude Code prompts:

```bash
echo '{"cwd": "/path/to/project", "model": {"display_name": "Claude 3.5"}, "cost": {"input_tokens": 1000}}' | cc-tools statusline
```

Example output: image

The provider-neutral cache variables are
`CC_TOOLS_STATUSLINE_CACHE_DIR` and `CC_TOOLS_STATUSLINE_CACHE_SECONDS`.
Their original `CLAUDE_STATUSLINE_*` aliases remain supported.

When `PATCHBAY_CALLER_KEY_FILE` is set, the statusline asks Patchbay's
`/_patchbay/usage/summary` API for the local-midnight-to-now daily total. Set
`CC_TOOLS_PATCHBAY_URL` to Patchbay's root URL, or omit it to use
`http://127.0.0.1:4100`; leave both variables unset to retain transcript-based
cost display. The URL must use `https`, except `http` is allowed for
`127.0.0.0/8`, `::1`, and `localhost`; URL userinfo is rejected. Costs stay integer nano-USD through half-even cent rounding. A
trailing `~` means Patchbay was unreachable and the chip fell back wholly to
legacy transcript data. `ERR` means the configured API, caller key, or response
is broken, so no cost number is shown. A Patchbay-backed rate-limit alarm carries
no dollar figure because its subscription-capacity signal has no shared monetary
basis with the day summary.

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

# View the raw JSON config file
cc-tools config show

# Get a specific value
cc-tools config get statusline.cache_seconds
```

### Setting Configuration

```bash
# Set custom workspace label for statusline
cc-tools config set statusline.workspace "my-project"

# Set cache directory (e.g., for systems without /dev/shm)
cc-tools config set statusline.cache_dir "/tmp"
```

### Resetting to Defaults

```bash
# Reset a specific setting to its default
cc-tools config reset statusline.cache_seconds

# Reset all settings to defaults
cc-tools config reset
```

### Available Settings

| Setting | Default | Description |
|---------|---------|-------------|
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
go test ./internal/statusline/...

# Verbose output
go test -v ./...
```

## License

MIT

## Author

Josh Symonds ([@Veraticus](https://github.com/Veraticus))
