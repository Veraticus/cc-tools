// Package mcp provides MCP server management functionality for cc-tools.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MCPServer represents an MCP server configuration.
type MCPServer struct {
	Type    string         `json:"type"`
	Command string         `json:"command"`
	Args    []string       `json:"args"`
	Env     map[string]any `json:"env"`
}

// Settings represents the structure of ~/.claude/settings.json.
type Settings struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// Manager handles MCP server operations.
type Manager struct {
	settingsPath string
	output       io.Writer
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	homeDir, _ := os.UserHomeDir()
	return &Manager{
		settingsPath: filepath.Join(homeDir, ".claude", "settings.json"),
		output:       os.Stdout,
	}
}

// loadSettings reads the settings.json file.
func (m *Manager) loadSettings() (*Settings, error) {
	data, err := os.ReadFile(m.settingsPath)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}

	var settings Settings
	if unmarshalErr := json.Unmarshal(data, &settings); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing settings: %w", unmarshalErr)
	}

	return &settings, nil
}

// findMCPByName finds an MCP server by name with flexible matching.
func (m *Manager) findMCPByName(settings *Settings, name string) (string, *MCPServer, error) {
	name = strings.ToLower(name)

	// Try exact match first
	for key, server := range settings.MCPServers {
		if strings.ToLower(key) == name {
			return key, &server, nil
		}
	}

	// Try partial matches
	for key, server := range settings.MCPServers {
		lowerKey := strings.ToLower(key)

		// Handle targetprocess variations
		if name == "target" && lowerKey == "targetprocess" {
			return key, &server, nil
		}
		if name == "target-process" && lowerKey == "targetprocess" {
			return key, &server, nil
		}

		// Handle partial matches
		if strings.Contains(lowerKey, name) || strings.Contains(name, lowerKey) {
			return key, &server, nil
		}
	}

	return "", nil, fmt.Errorf("MCP server '%s' not found in settings", name)
}

// List shows all available MCP servers and their status.
func (m *Manager) List(ctx context.Context) error {
	// Just run claude mcp list
	cmd := exec.CommandContext(ctx, "claude", "mcp", "list")
	cmd.Stdout = m.output
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("listing MCPs: %w", err)
	}
	return nil
}

// Enable adds an MCP server from settings.
func (m *Manager) Enable(ctx context.Context, name string) error {
	settings, err := m.loadSettings()
	if err != nil {
		return err
	}

	actualName, server, err := m.findMCPByName(settings, name)
	if err != nil {
		return err
	}

	// Build the claude mcp add command
	args := []string{"mcp", "add"}

	// Add the name
	args = append(args, actualName)

	// Add the command (expand ~ to home directory)
	command := server.Command
	if strings.HasPrefix(command, "~/") {
		homeDir, _ := os.UserHomeDir()
		command = filepath.Join(homeDir, command[2:])
	}
	args = append(args, command)

	// Add any additional args
	args = append(args, server.Args...)

	_, _ = fmt.Fprintf(m.output, "Enabling MCP server '%s'...\n", actualName)

	cmd := exec.CommandContext(ctx, "claude", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's already enabled
		if strings.Contains(string(output), "already exists") {
			_, _ = fmt.Fprintf(m.output, "MCP server '%s' is already enabled\n", actualName)
			return nil
		}
		return fmt.Errorf("enabling MCP: %w\nOutput: %s", err, output)
	}

	_, _ = fmt.Fprintf(m.output, "✓ Enabled MCP server '%s'\n", actualName)
	return nil
}

// Disable removes an MCP server.
func (m *Manager) Disable(ctx context.Context, name string) error {
	settings, err := m.loadSettings()
	if err != nil {
		// If we can't load settings, try to remove anyway with the provided name
		return m.removeMCP(ctx, name)
	}

	// Try to find the actual name from settings
	actualName, _, err := m.findMCPByName(settings, name)
	if err != nil {
		// If not found in settings, try with the provided name anyway
		return m.removeMCP(ctx, name)
	}

	return m.removeMCP(ctx, actualName)
}

// removeMCP runs the claude mcp remove command.
func (m *Manager) removeMCP(ctx context.Context, name string) error {
	_, _ = fmt.Fprintf(m.output, "Disabling MCP server '%s'...\n", name)

	cmd := exec.CommandContext(ctx, "claude", "mcp", "remove", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it doesn't exist
		if strings.Contains(string(output), "not found") {
			_, _ = fmt.Fprintf(m.output, "MCP server '%s' is not enabled\n", name)
			return nil
		}
		return fmt.Errorf("disabling MCP: %w\nOutput: %s", err, output)
	}

	_, _ = fmt.Fprintf(m.output, "✓ Disabled MCP server '%s'\n", name)
	return nil
}

// EnableAll enables all MCP servers from settings.
func (m *Manager) EnableAll(ctx context.Context) error {
	settings, err := m.loadSettings()
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(m.output, "Enabling all %d MCP servers...\n",
		len(settings.MCPServers))

	hasError := false
	for name := range settings.MCPServers {
		if enableErr := m.Enable(ctx, name); enableErr != nil {
			fmt.Fprintf(os.Stderr, "Error enabling %s: %v\n", name, enableErr)
			hasError = true
		}
	}

	if hasError {
		return fmt.Errorf("some MCP servers failed to enable")
	}

	_, _ = fmt.Fprintln(m.output, "✓ All MCP servers enabled")
	return nil
}

// DisableAll disables all MCP servers.
func (m *Manager) DisableAll(ctx context.Context) error {
	// Get current list of enabled MCPs
	cmd := exec.CommandContext(ctx, "claude", "mcp", "list")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("listing MCPs: %w", err)
	}

	// Parse the output to find enabled MCPs
	lines := strings.Split(string(output), "\n")
	mcpNames := []string{}

	for _, line := range lines {
		// Look for lines with MCP names (they start with a name followed by a colon)
		if strings.Contains(line, ":") && !strings.Contains(line, "Checking") {
			parts := strings.Split(line, ":")
			if len(parts) > 0 {
				name := strings.TrimSpace(parts[0])
				if name != "" {
					mcpNames = append(mcpNames, name)
				}
			}
		}
	}

	if len(mcpNames) == 0 {
		_, _ = fmt.Fprintln(m.output, "No MCP servers are currently enabled")
		return nil
	}

	_, _ = fmt.Fprintf(m.output, "Disabling %d MCP servers...\n", len(mcpNames))

	hasError := false
	for _, name := range mcpNames {
		if disableErr := m.removeMCP(ctx, name); disableErr != nil {
			fmt.Fprintf(os.Stderr, "Error disabling %s: %v\n", name, disableErr)
			hasError = true
		}
	}

	if hasError {
		return fmt.Errorf("some MCP servers failed to disable")
	}

	_, _ = fmt.Fprintln(m.output, "✓ All MCP servers disabled")
	return nil
}
