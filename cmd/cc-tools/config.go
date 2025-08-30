package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/output"
	"github.com/charmbracelet/lipgloss"
)

const (
	minConfigArgs = 3
	minGetArgs    = 4
	minSetArgs    = 5
)

func runConfigCommand() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	if len(os.Args) < minConfigArgs {
		printConfigUsage(out)
		os.Exit(1)
	}

	ctx := context.Background()
	manager := config.NewManager()

	switch os.Args[2] {
	case "get":
		if len(os.Args) < minGetArgs {
			out.Error("Error: 'get' requires a key")
			printConfigUsage(out)
			os.Exit(1)
		}
		if err := handleConfigGet(ctx, out, manager, os.Args[3]); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "set":
		if len(os.Args) < minSetArgs {
			out.Error("Error: 'set' requires a key and value")
			printConfigUsage(out)
			os.Exit(1)
		}
		if err := handleConfigSet(ctx, out, manager, os.Args[3], os.Args[4]); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "list", "show":
		if err := handleConfigList(ctx, out, manager); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	case "reset":
		var key string
		if len(os.Args) >= minGetArgs {
			key = os.Args[3]
		}
		if err := handleConfigReset(ctx, out, manager, key); err != nil {
			out.Error("Error: %v", err)
			os.Exit(1)
		}
	default:
		out.Error("Unknown config subcommand: %s", os.Args[2])
		printConfigUsage(out)
		os.Exit(1)
	}
}

func printConfigUsage(out *output.Terminal) {
	out.RawError(`Usage: cc-tools config <subcommand> [arguments]

Subcommands:
  get <key>         Get a configuration value
  set <key> <value> Set a configuration value
  list              Show all configuration with defaults and overrides
  show              Alias for list
  reset [key]       Reset configuration to defaults (all or specific key)

Configuration Keys:
  validate.timeout    Timeout for validation commands (seconds)
  validate.cooldown   Cooldown between validation runs (seconds)
  statusline.workspace    Custom workspace label
  statusline.cache_dir    Cache directory path
  statusline.cache_seconds    Cache duration

Examples:
  cc-tools config set validate.timeout 90
  cc-tools config get validate.timeout
  cc-tools config list
  cc-tools config reset validate.timeout
  cc-tools config reset              # Reset all to defaults
`)
}

func handleConfigGet(ctx context.Context, out *output.Terminal, manager *config.Manager, key string) error {
	// Ensure config exists with defaults
	if err := manager.EnsureConfig(ctx); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}

	value, exists, err := manager.GetValue(ctx, key)
	if err != nil {
		return fmt.Errorf("get config value: %w", err)
	}

	if !exists {
		out.Error("Key '%s' not found", key)
		out.Info("Available keys:")
		keys, _ := manager.GetAllKeys(ctx)
		for _, k := range keys {
			out.Info("  %s", k)
		}
		return fmt.Errorf("key not found")
	}

	out.Raw(fmt.Sprintf("%v\n", value))
	return nil
}

func handleConfigSet(ctx context.Context, out *output.Terminal, manager *config.Manager, key, value string) error {
	// Ensure config exists with defaults
	if err := manager.EnsureConfig(ctx); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}

	if err := manager.Set(ctx, key, value); err != nil {
		return fmt.Errorf("set config value: %w", err)
	}

	out.Success("✓ Set %s = %s", key, value)
	return nil
}

func handleConfigList(ctx context.Context, out *output.Terminal, manager *config.Manager) error {
	// Ensure config exists with defaults
	if err := manager.EnsureConfig(ctx); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}

	settings, err := manager.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("get all config: %w", err)
	}

	// Sort keys for consistent display
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Create table
	table := output.NewTable(
		[]string{"Setting", "Value", "Status"},
		[]int{30, 25, 10},
	)

	// Create styles for status column
	defaultStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Gray for defaults
	customStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("220"))  // Yellow for custom

	for _, key := range keys {
		info := settings[key]
		var status string
		if info.IsDefault {
			status = defaultStyle.Render("default")
		} else {
			status = customStyle.Render("custom")
		}

		// Handle empty values display
		value := info.Value
		if value == "" {
			value = "(empty)"
		}

		table.AddRow([]string{key, value, status})
	}

	out.Info("Configuration Settings")
	_ = out.Write(table.Render())

	// Show config file location
	configPath := manager.GetConfigPath()
	out.Info("\nConfig file: %s", configPath)

	return nil
}

func handleConfigReset(ctx context.Context, out *output.Terminal, manager *config.Manager, key string) error {
	if key == "" {
		// Reset all
		if err := manager.ResetAll(ctx); err != nil {
			return fmt.Errorf("reset all config: %w", err)
		}
		out.Success("✓ Reset all configuration to defaults")
	} else {
		// Reset specific key
		if err := manager.Reset(ctx, key); err != nil {
			return fmt.Errorf("reset config key: %w", err)
		}
		out.Success("✓ Reset %s to default value", key)
	}

	return nil
}
