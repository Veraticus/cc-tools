package main

import (
	"context"
	"os"
	"time"

	"github.com/joshsymonds/steward/internal/mcp"
	"github.com/joshsymonds/steward/internal/output"
)

const (
	mcpMinArgs    = 3
	mcpSubCmdArgs = 4
	mcpTimeout    = 30 * time.Second
)

func runMCPCommand() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	if len(os.Args) < mcpMinArgs {
		printMCPUsage(out)
		os.Exit(1)
	}

	subcommand := os.Args[2]

	// Handle help separately to avoid defer issues
	if subcommand == "help" || subcommand == "-h" || subcommand == "--help" {
		printMCPUsage(out)
		return
	}

	// Run the actual command and get exit code
	exitCode := executeMCPCommand(out, subcommand)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func executeMCPCommand(out *output.Terminal, subcommand string) int {
	manager := mcp.NewManager(out)
	ctx, cancel := context.WithTimeout(context.Background(), mcpTimeout)
	defer cancel()

	switch subcommand {
	case "list":
		return handleMCPList(ctx, out, manager)
	case "enable":
		return handleMCPEnable(ctx, out, manager)
	case "disable":
		return handleMCPDisable(ctx, out, manager)
	case "enable-all":
		return handleMCPEnableAll(ctx, out, manager)
	case "disable-all":
		return handleMCPDisableAll(ctx, out, manager)
	default:
		out.Errorf("Unknown MCP subcommand: %s", subcommand)
		printMCPUsage(out)
		return 1
	}
}

func handleMCPList(ctx context.Context, out *output.Terminal, manager *mcp.Manager) int {
	if err := manager.List(ctx); err != nil {
		out.Errorf("Error: %v", err)
		return 1
	}
	return 0
}

func handleMCPEnable(ctx context.Context, out *output.Terminal, manager *mcp.Manager) int {
	if len(os.Args) < mcpSubCmdArgs {
		out.Errorf("Error: 'enable' requires an MCP name")
		printMCPUsage(out)
		return 1
	}
	if err := manager.Enable(ctx, os.Args[3]); err != nil {
		out.Errorf("Error: %v", err)
		return 1
	}
	return 0
}

func handleMCPDisable(ctx context.Context, out *output.Terminal, manager *mcp.Manager) int {
	if len(os.Args) < mcpSubCmdArgs {
		out.Errorf("Error: 'disable' requires an MCP name")
		printMCPUsage(out)
		return 1
	}
	if err := manager.Disable(ctx, os.Args[3]); err != nil {
		out.Errorf("Error: %v", err)
		return 1
	}
	return 0
}

func handleMCPEnableAll(ctx context.Context, out *output.Terminal, manager *mcp.Manager) int {
	if err := manager.EnableAll(ctx); err != nil {
		out.Errorf("Error: %v", err)
		return 1
	}
	return 0
}

func handleMCPDisableAll(ctx context.Context, out *output.Terminal, manager *mcp.Manager) int {
	if err := manager.DisableAll(ctx); err != nil {
		out.Errorf("Error: %v", err)
		return 1
	}
	return 0
}

func printMCPUsage(out *output.Terminal) {
	out.RawError(`steward mcp - Manage Claude MCP servers

Usage:
  steward mcp <subcommand> [arguments]

Subcommands:
  list          Show all MCP servers and their status
  enable        Enable an MCP server (e.g., 'jira', 'playwright')
  disable       Disable an MCP server
  enable-all    Enable all MCP servers from settings
  disable-all   Disable all MCP servers

Examples:
  steward mcp list                  # Show current MCP status
  steward mcp enable jira           # Enable the Jira MCP
  steward mcp disable playwright    # Disable the Playwright MCP
  steward mcp disable-all           # Disable all MCPs

Notes:
  - MCP configurations are read from ~/.claude/settings.json
  - Names support flexible matching (e.g., 'target' matches 'targetprocess')
`)
}
