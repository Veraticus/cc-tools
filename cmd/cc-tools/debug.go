package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Veraticus/cc-tools/internal/debug"
)

const minDebugArgs = 3

func runDebugCommand() {
	if len(os.Args) < minDebugArgs {
		printDebugUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	manager := debug.NewManager()

	switch os.Args[2] {
	case "enable":
		if err := enableDebug(ctx, manager); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "disable":
		if err := disableDebug(ctx, manager); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := showDebugStatus(ctx, manager); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := listDebugDirs(ctx, manager); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown debug subcommand: %s\n", os.Args[2])
		printDebugUsage()
		os.Exit(1)
	}
}

func printDebugUsage() {
	fmt.Fprintf(os.Stderr, `Usage: cc-tools debug <subcommand>

Subcommands:
  enable    Enable debug logging for the current directory
  disable   Disable debug logging for the current directory
  status    Show debug status for the current directory
  list      Show all directories with debug logging enabled

Examples:
  cc-tools debug enable     # Enable debug logging in current directory
  cc-tools debug disable    # Disable debug logging in current directory
  cc-tools debug status     # Check if debug logging is enabled
  cc-tools debug list       # List all directories with debug enabled
`)
}

func enableDebug(ctx context.Context, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	logFile, err := manager.Enable(ctx, dir)
	if err != nil {
		return fmt.Errorf("enable debug: %w", err)
	}

	fmt.Printf("✓ Debug logging enabled for %s\n", dir)                     //nolint:forbidigo // CLI output
	fmt.Printf("  Log file: %s\n", logFile)                                 //nolint:forbidigo // CLI output
	fmt.Printf("\ncc-tools-validate will write debug logs to this file.\n") //nolint:forbidigo // CLI output

	return nil
}

func disableDebug(ctx context.Context, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if disableErr := manager.Disable(ctx, dir); disableErr != nil {
		return fmt.Errorf("disable debug: %w", disableErr)
	}

	fmt.Printf("✓ Debug logging disabled for %s\n", dir) //nolint:forbidigo // CLI output

	return nil
}

func showDebugStatus(ctx context.Context, manager *debug.Manager) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	enabled, err := manager.IsEnabled(ctx, dir)
	if err != nil {
		return fmt.Errorf("check debug status: %w", err)
	}

	if enabled {
		logFile := debug.GetLogFilePath(dir)
		fmt.Printf("Debug status for %s:\n", dir) //nolint:forbidigo // CLI output
		fmt.Printf("  Status: ENABLED\n")         //nolint:forbidigo // CLI output
		fmt.Printf("  Log file: %s\n", logFile)   //nolint:forbidigo // CLI output
	} else {
		fmt.Printf("Debug status for %s:\n", dir) //nolint:forbidigo // CLI output
		fmt.Printf("  Status: DISABLED\n")        //nolint:forbidigo // CLI output
	}

	return nil
}

func listDebugDirs(ctx context.Context, manager *debug.Manager) error {
	dirs, err := manager.GetEnabledDirs(ctx)
	if err != nil {
		return fmt.Errorf("list debug directories: %w", err)
	}

	if len(dirs) == 0 {
		fmt.Println("No directories have debug logging enabled") //nolint:forbidigo // CLI output
		return nil
	}

	sort.Strings(dirs)

	fmt.Println("Directories with debug logging enabled:") //nolint:forbidigo // CLI output
	for _, dir := range dirs {
		logFile := debug.GetLogFilePath(dir)
		fmt.Printf("  %s\n", dir)              //nolint:forbidigo // CLI output
		fmt.Printf("    → Log: %s\n", logFile) //nolint:forbidigo // CLI output
	}

	return nil
}
