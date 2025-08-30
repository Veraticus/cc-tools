package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Veraticus/cc-tools/internal/mcp"
)

const (
	mcpMinArgs    = 3
	mcpSubCmdArgs = 4
	mcpTimeout    = 30 * time.Second
)

func runMCPCommand() {
	if len(os.Args) < mcpMinArgs {
		printMCPUsage()
		os.Exit(1)
	}

	manager := mcp.NewManager()

	subcommand := os.Args[2]

	// Handle help separately to avoid defer issues
	if subcommand == "help" || subcommand == "-h" || subcommand == "--help" {
		printMCPUsage()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), mcpTimeout)
	defer cancel()

	switch subcommand {
	case "list":
		if err := manager.List(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "enable":
		if len(os.Args) < mcpSubCmdArgs {
			fmt.Fprintf(os.Stderr, "Error: 'enable' requires an MCP name\n")
			printMCPUsage()
			os.Exit(1)
		}
		if err := manager.Enable(ctx, os.Args[3]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "disable":
		if len(os.Args) < mcpSubCmdArgs {
			fmt.Fprintf(os.Stderr, "Error: 'disable' requires an MCP name\n")
			printMCPUsage()
			os.Exit(1)
		}
		if err := manager.Disable(ctx, os.Args[3]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "enable-all":
		if err := manager.EnableAll(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "disable-all":
		if err := manager.DisableAll(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		printMCPUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown MCP subcommand: %s\n", subcommand)
		printMCPUsage()
		os.Exit(1)
	}
}

func printMCPUsage() {
	fmt.Fprintf(os.Stderr, `cc-tools mcp - Manage Claude MCP servers

Usage:
  cc-tools mcp <subcommand> [arguments]

Subcommands:
  list          Show all MCP servers and their status
  enable        Enable an MCP server (e.g., 'jira', 'playwright')
  disable       Disable an MCP server
  enable-all    Enable all MCP servers from settings
  disable-all   Disable all MCP servers

Examples:
  cc-tools mcp list                  # Show current MCP status
  cc-tools mcp enable jira           # Enable the Jira MCP
  cc-tools mcp disable playwright    # Disable the Playwright MCP
  cc-tools mcp disable-all           # Disable all MCPs

Notes:
  - MCP configurations are read from ~/.claude/settings.json
  - Names support flexible matching (e.g., 'target' matches 'targetprocess')
`)
}
