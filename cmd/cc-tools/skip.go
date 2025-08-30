package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Veraticus/cc-tools/internal/output"
	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

const (
	skipLint    = "lint"
	skipTest    = "test"
	skipAll     = "all"
	minSkipArgs = 3
)

// runSkipCommand handles the skip command and its subcommands.
func runSkipCommand() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	if len(os.Args) < minSkipArgs {
		printSkipUsage(out)
		os.Exit(1)
	}

	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	switch os.Args[2] {
	case skipLint:
		if err := addSkip(ctx, out, registry, skipregistry.SkipTypeLint); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case skipTest:
		if err := addSkip(ctx, out, registry, skipregistry.SkipTypeTest); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case skipAll:
		if err := addSkip(ctx, out, registry, skipregistry.SkipTypeAll); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "list":
		if err := listSkips(ctx, out, registry); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "status":
		if err := showStatus(ctx, out, registry); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	default:
		out.Error("Unknown skip subcommand: %s", os.Args[2])
		printSkipUsage(out)
		os.Exit(1)
	}
}

// runUnskipCommand handles the unskip command.
func runUnskipCommand() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	if len(os.Args) < minSkipArgs {
		printUnskipUsage(out)
		os.Exit(1)
	}

	ctx := context.Background()
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	switch os.Args[2] {
	case skipLint:
		if err := removeSkip(ctx, out, registry, skipregistry.SkipTypeLint); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case skipTest:
		if err := removeSkip(ctx, out, registry, skipregistry.SkipTypeTest); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case skipAll:
		if err := clearSkips(ctx, out, registry); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	default:
		// If no argument, default to "all"
		if err := clearSkips(ctx, out, registry); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	}
}

func printSkipUsage(out *output.Terminal) {
	out.RawError(`Usage: cc-tools skip <subcommand>

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

func printUnskipUsage(out *output.Terminal) {
	out.RawError(`Usage: cc-tools unskip [<type>]

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

func addSkip(
	ctx context.Context,
	out *output.Terminal,
	registry skipregistry.Registry,
	skipType skipregistry.SkipType,
) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if addErr := registry.AddSkip(ctx, skipregistry.DirectoryPath(dir), skipType); addErr != nil {
		return fmt.Errorf("add skip: %w", addErr)
	}

	// Print confirmation
	switch skipType {
	case skipregistry.SkipTypeLint:
		out.Success("✓ Linting will be skipped in %s", dir)
	case skipregistry.SkipTypeTest:
		out.Success("✓ Testing will be skipped in %s", dir)
	case skipregistry.SkipTypeAll:
		out.Success("✓ Linting and testing will be skipped in %s", dir)
	}

	return nil
}

func removeSkip(
	ctx context.Context,
	out *output.Terminal,
	registry skipregistry.Registry,
	skipType skipregistry.SkipType,
) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if removeErr := registry.RemoveSkip(ctx, skipregistry.DirectoryPath(dir), skipType); removeErr != nil {
		return fmt.Errorf("remove skip: %w", removeErr)
	}

	// Print confirmation
	switch skipType {
	case skipregistry.SkipTypeLint:
		out.Success("✓ Linting will no longer be skipped in %s", dir)
	case skipregistry.SkipTypeTest:
		out.Success("✓ Testing will no longer be skipped in %s", dir)
	case skipregistry.SkipTypeAll:
		// This case won't occur as we expand SkipTypeAll earlier
	}

	return nil
}

func clearSkips(
	ctx context.Context,
	out *output.Terminal,
	registry skipregistry.Registry,
) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	if clearErr := registry.Clear(ctx, skipregistry.DirectoryPath(dir)); clearErr != nil {
		return fmt.Errorf("clear skips: %w", clearErr)
	}

	out.Success("✓ All skips removed from %s", dir)
	return nil
}

func listSkips(
	ctx context.Context,
	out *output.Terminal,
	registry skipregistry.Registry,
) error {
	entries, err := registry.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list all: %w", err)
	}

	if len(entries) == 0 {
		out.Info("No directories have skip configurations")
		return nil
	}

	// Sort entries by path for consistent output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path.String() < entries[j].Path.String()
	})

	list := output.NewListRenderer()
	groups := make(map[string][]string)

	for _, entry := range entries {
		var typeStrs []string
		for _, t := range entry.Types {
			typeStrs = append(typeStrs, string(t))
		}
		groups[entry.Path.String()] = typeStrs
	}

	_ = out.Write(list.RenderGrouped("Skip configurations:", groups))

	return nil
}

func showStatus(
	ctx context.Context,
	out *output.Terminal,
	registry skipregistry.Registry,
) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}

	types, err := registry.GetSkipTypes(ctx, skipregistry.DirectoryPath(dir))
	if err != nil {
		return fmt.Errorf("get skip types: %w", err)
	}

	if len(types) == 0 {
		out.Info("No skips configured for %s", dir)
		return nil
	}

	list := output.NewListRenderer()
	items := []string{}

	for _, t := range types {
		switch t {
		case skipregistry.SkipTypeLint:
			items = append(items, "Linting: SKIPPED")
		case skipregistry.SkipTypeTest:
			items = append(items, "Testing: SKIPPED")
		case skipregistry.SkipTypeAll:
			// This case won't occur as we don't store SkipTypeAll
		}
	}

	_ = out.Write(list.Render(fmt.Sprintf("Skip status for %s:", dir), items))

	return nil
}
