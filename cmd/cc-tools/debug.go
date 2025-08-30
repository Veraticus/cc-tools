package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Veraticus/cc-tools/internal/debug"
	"github.com/Veraticus/cc-tools/internal/output"
	"github.com/Veraticus/cc-tools/internal/shared"
)

const (
	minDebugArgs = 3
	listCommand  = "list"
)

func runDebugCommand() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	if len(os.Args) < minDebugArgs {
		printDebugUsage(out)
		os.Exit(1)
	}

	ctx := context.Background()
	manager := debug.NewManager()

	switch os.Args[2] {
	case "enable":
		if err := enableDebug(ctx, out, manager); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "disable":
		if err := disableDebug(ctx, out, manager); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "status":
		if err := showDebugStatus(ctx, out, manager); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case listCommand:
		if err := listDebugDirs(ctx, out, manager); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "filename":
		showDebugFilename(out)
	default:
		out.Error("Unknown debug subcommand: %s", os.Args[2])
		printDebugUsage(out)
		os.Exit(1)
	}
}

func printDebugUsage(out *output.Terminal) {
	out.RawError(`Usage: cc-tools debug <subcommand>

Subcommands:
  enable    Enable debug logging for the current directory
  disable   Disable debug logging for the current directory
  status    Show debug status for the current directory
  list      Show all directories with debug logging enabled
  filename  Print the debug log filename for the current directory

Examples:
  cc-tools debug enable     # Enable debug logging in current directory
  cc-tools debug disable    # Disable debug logging in current directory
  cc-tools debug status     # Check if debug logging is enabled
  cc-tools debug list       # List all directories with debug enabled
  cc-tools debug filename   # Get the debug log file path for current directory
`)
}

func enableDebug(ctx context.Context, out *output.Terminal, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	logFile, err := manager.Enable(ctx, dir)
	if err != nil {
		return fmt.Errorf("enable debug: %w", err)
	}

	out.Success("✓ Debug logging enabled for %s", dir)
	out.Info("  Log file: %s", logFile)
	_ = out.Write("")
	out.Info("cc-tools-validate will write debug logs to this file.")

	return nil
}

func disableDebug(ctx context.Context, out *output.Terminal, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if disableErr := manager.Disable(ctx, dir); disableErr != nil {
		return fmt.Errorf("disable debug: %w", disableErr)
	}

	out.Success("✓ Debug logging disabled for %s", dir)

	return nil
}

func showDebugStatus(ctx context.Context, out *output.Terminal, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	enabled, err := manager.IsEnabled(ctx, dir)
	if err != nil {
		return fmt.Errorf("check debug status: %w", err)
	}

	list := output.NewListRenderer()
	items := map[string]string{}

	if enabled {
		logFile := debug.GetLogFilePath(dir)
		items["Status"] = "ENABLED"
		items["Log file"] = logFile
	} else {
		items["Status"] = "DISABLED"
	}

	_ = out.Write(list.RenderMap(fmt.Sprintf("Debug status for %s:", dir), items))

	return nil
}

func listDebugDirs(ctx context.Context, out *output.Terminal, manager *debug.Manager) error {
	dirs, err := manager.GetEnabledDirs(ctx)
	if err != nil {
		return fmt.Errorf("list debug directories: %w", err)
	}

	if len(dirs) == 0 {
		out.Info("No directories have debug logging enabled")
		return nil
	}

	sort.Strings(dirs)

	list := output.NewListRenderer()
	groups := make(map[string][]string)

	for _, dir := range dirs {
		logFile := debug.GetLogFilePath(dir)
		debugLogFile := shared.GetDebugLogPathForDir(dir)
		groups[dir] = []string{
			fmt.Sprintf("Log: %s", logFile),
			fmt.Sprintf("Debug: %s", debugLogFile),
		}
	}

	_ = out.Write(list.RenderGrouped("Directories with debug logging enabled:", groups))

	return nil
}

func showDebugFilename(out *output.Terminal) {
	// Print the debug log filename for the current directory
	wd, err := os.Getwd()
	if err != nil {
		out.Error("Error getting current directory: %v", err)
		os.Exit(1)
	}
	out.Raw(shared.GetDebugLogPathForDir(wd))
	out.Raw("\n")
}
