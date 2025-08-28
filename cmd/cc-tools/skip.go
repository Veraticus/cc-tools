package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

// runSkipCommand handles the skip command and its subcommands.
func runSkipCommand() {
	if len(os.Args) < 3 {
		printSkipUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	switch os.Args[2] {
	case "lint":
		if err := addSkip(ctx, registry, skipregistry.SkipTypeLint); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "test":
		if err := addSkip(ctx, registry, skipregistry.SkipTypeTest); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "all":
		if err := addSkip(ctx, registry, skipregistry.SkipTypeAll); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := listSkips(ctx, registry); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := showStatus(ctx, registry); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown skip subcommand: %s\n", os.Args[2])
		printSkipUsage()
		os.Exit(1)
	}
}

// runUnskipCommand handles the unskip command.
func runUnskipCommand() {
	if len(os.Args) < 3 {
		printUnskipUsage()
		os.Exit(1)
	}

	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	switch os.Args[2] {
	case "lint":
		if err := removeSkip(ctx, registry, skipregistry.SkipTypeLint); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "test":
		if err := removeSkip(ctx, registry, skipregistry.SkipTypeTest); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "all":
		if err := clearSkips(ctx, registry); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		// If no argument, default to "all"
		if err := clearSkips(ctx, registry); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

func printSkipUsage() {
	fmt.Fprintf(os.Stderr, `Usage: cc-tools skip <subcommand>

Subcommands:
  lint      Skip linting in the current directory
  test      Skip testing in the current directory  
  all       Skip both linting and testing in the current directory
  list      Show all directories with skip configurations
  status    Show skip status for the current directory

Examples:
  cc-tools skip lint        # Skip linting in current directory
  cc-tools skip all         # Skip both lint and test in current directory
  cc-tools skip list        # List all skip configurations
  cc-tools skip status      # Show skip status for current directory
`)
}

func printUnskipUsage() {
	fmt.Fprintf(os.Stderr, `Usage: cc-tools unskip [<type>]

Types:
  lint      Remove skip for linting in the current directory
  test      Remove skip for testing in the current directory  
  all       Remove all skips for the current directory (default)

Examples:
  cc-tools unskip           # Remove all skips from current directory
  cc-tools unskip lint      # Remove lint skip from current directory
  cc-tools unskip test      # Remove test skip from current directory
  cc-tools unskip all       # Remove all skips from current directory
`)
}

func addSkip(ctx context.Context, registry skipregistry.Registry, skipType skipregistry.SkipType) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if err := registry.AddSkip(ctx, skipregistry.DirectoryPath(dir), skipType); err != nil {
		return fmt.Errorf("add skip: %w", err)
	}

	// Print confirmation
	switch skipType {
	case skipregistry.SkipTypeLint:
		fmt.Printf("✓ Linting will be skipped in %s\n", dir) //nolint:forbidigo // CLI output
	case skipregistry.SkipTypeTest:
		fmt.Printf("✓ Testing will be skipped in %s\n", dir) //nolint:forbidigo // CLI output
	case skipregistry.SkipTypeAll:
		fmt.Printf("✓ Linting and testing will be skipped in %s\n", dir) //nolint:forbidigo // CLI output
	}

	return nil
}

func removeSkip(ctx context.Context, registry skipregistry.Registry, skipType skipregistry.SkipType) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if err := registry.RemoveSkip(ctx, skipregistry.DirectoryPath(dir), skipType); err != nil {
		return fmt.Errorf("remove skip: %w", err)
	}

	// Print confirmation
	switch skipType {
	case skipregistry.SkipTypeLint:
		fmt.Printf("✓ Linting will no longer be skipped in %s\n", dir) //nolint:forbidigo // CLI output
	case skipregistry.SkipTypeTest:
		fmt.Printf("✓ Testing will no longer be skipped in %s\n", dir) //nolint:forbidigo // CLI output
	}

	return nil
}

func clearSkips(ctx context.Context, registry skipregistry.Registry) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if err := registry.Clear(ctx, skipregistry.DirectoryPath(dir)); err != nil {
		return fmt.Errorf("clear skips: %w", err)
	}

	fmt.Printf("✓ All skips removed from %s\n", dir) //nolint:forbidigo // CLI output
	return nil
}

func listSkips(ctx context.Context, registry skipregistry.Registry) error {
	entries, err := registry.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list all: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No directories have skip configurations") //nolint:forbidigo // CLI output
		return nil
	}

	// Sort entries by path for consistent output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path.String() < entries[j].Path.String()
	})

	fmt.Println("Skip configurations:") //nolint:forbidigo // CLI output
	for _, entry := range entries {
		var typeStrs []string
		for _, t := range entry.Types {
			typeStrs = append(typeStrs, string(t))
		}
		fmt.Printf("  %s: %v\n", entry.Path, typeStrs) //nolint:forbidigo // CLI output
	}

	return nil
}

func showStatus(ctx context.Context, registry skipregistry.Registry) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	types, err := registry.GetSkipTypes(ctx, skipregistry.DirectoryPath(dir))
	if err != nil {
		return fmt.Errorf("get skip types: %w", err)
	}

	if len(types) == 0 {
		fmt.Printf("No skips configured for %s\n", dir) //nolint:forbidigo // CLI output
		return nil
	}

	fmt.Printf("Skip status for %s:\n", dir) //nolint:forbidigo // CLI output
	for _, t := range types {
		switch t {
		case skipregistry.SkipTypeLint:
			fmt.Println("  - Linting: SKIPPED") //nolint:forbidigo // CLI output
		case skipregistry.SkipTypeTest:
			fmt.Println("  - Testing: SKIPPED") //nolint:forbidigo // CLI output
		}
	}

	return nil
}
