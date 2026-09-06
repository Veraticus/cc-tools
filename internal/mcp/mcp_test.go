package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshsymonds/steward/internal/output"
)

// MockCommandExecutor is a mock implementation of CommandExecutor for testing.
type MockCommandExecutor struct {
	capturedCmd  string
	capturedArgs []string
	mockOutput   string
	shouldFail   bool
	// For more complex tests that need varying responses
	commandHandler func(name string, args []string) *exec.Cmd
}

// CommandContext captures the command and returns a mock exec.Cmd.
func (m *MockCommandExecutor) CommandContext(_ context.Context, name string, args ...string) *exec.Cmd {
	m.capturedCmd = name
	m.capturedArgs = args

	// If a custom handler is set, use it
	if m.commandHandler != nil {
		return m.commandHandler(name, args)
	}

	if m.shouldFail {
		if m.mockOutput != "" {
			// Create a command that outputs our mock message and fails
			return exec.Command("sh", "-c", "echo '"+m.mockOutput+"' && false")
		}
		return exec.Command("false")
	}
	if m.mockOutput != "" {
		return exec.Command("echo", m.mockOutput)
	}
	return exec.Command("echo", "success")
}

func TestNewManager(t *testing.T) {
	out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
	m := NewManager(out)

	if m.settingsPath == "" {
		t.Error("settingsPath should not be empty")
	}

	if !strings.Contains(m.settingsPath, "settings.json") {
		t.Errorf("settingsPath should contain settings.json, got %s", m.settingsPath)
	}

	if m.output == nil {
		t.Error("output should be set")
	}

	if m.executor == nil {
		t.Error("executor should be set")
	}
}

func TestNewManagerWithExecutor(t *testing.T) {
	out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
	mockExecutor := &MockCommandExecutor{}
	m := NewManagerWithExecutor(out, mockExecutor)

	if m.executor != mockExecutor {
		t.Error("custom executor should be set")
	}
}

func TestLoadSettings(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(t *testing.T, settingsPath string)
		checkFunc func(t *testing.T, settings *Settings)
		wantErr   bool
	}{
		{
			name: "loads valid settings",
			setupFunc: func(_ *testing.T, settingsPath string) {
				settings := &Settings{
					MCPServers: map[string]MCPServer{
						"targetprocess": {
							Type:    "local",
							Command: "node",
							Args:    []string{"server.js"},
							Env: map[string]any{
								"API_KEY": "test-key",
							},
						},
						"jira": {
							Type:    "local",
							Command: "python",
							Args:    []string{"jira_mcp.py"},
						},
					},
				}
				data, _ := json.MarshalIndent(settings, "", "  ")
				os.MkdirAll(filepath.Dir(settingsPath), 0755)
				os.WriteFile(settingsPath, data, 0600)
			},
			checkFunc: func(t *testing.T, settings *Settings) {
				t.Helper()
				if len(settings.MCPServers) != 2 {
					t.Errorf("expected 2 MCP servers, got %d", len(settings.MCPServers))
				}

				if tp, ok := settings.MCPServers["targetprocess"]; !ok {
					t.Error("targetprocess server should exist")
				} else {
					if tp.Type != "local" {
						t.Errorf("targetprocess type = %s, want local", tp.Type)
					}
					if tp.Command != "node" {
						t.Errorf("targetprocess command = %s, want node", tp.Command)
					}
					if len(tp.Args) != 1 || tp.Args[0] != "server.js" {
						t.Errorf("targetprocess args = %v, want [server.js]", tp.Args)
					}
				}
			},
		},
		{
			name:    "handles missing file",
			wantErr: true,
		},
		{
			name: "handles corrupt JSON",
			setupFunc: func(_ *testing.T, settingsPath string) {
				os.MkdirAll(filepath.Dir(settingsPath), 0755)
				os.WriteFile(settingsPath, []byte("{invalid json}"), 0600)
			},
			wantErr: true,
		},
		{
			name: "handles empty MCP servers",
			setupFunc: func(_ *testing.T, settingsPath string) {
				settings := &Settings{
					MCPServers: map[string]MCPServer{},
				}
				data, _ := json.MarshalIndent(settings, "", "  ")
				os.MkdirAll(filepath.Dir(settingsPath), 0755)
				os.WriteFile(settingsPath, data, 0600)
			},
			checkFunc: func(t *testing.T, settings *Settings) {
				t.Helper()
				if settings.MCPServers == nil {
					t.Error("MCPServers should be initialized")
				}
				if len(settings.MCPServers) != 0 {
					t.Error("MCPServers should be empty")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")

			if tt.setupFunc != nil {
				tt.setupFunc(t, settingsPath)
			}

			m := &Manager{
				settingsPath: settingsPath,
			}

			settings, err := m.loadSettings()
			if (err != nil) != tt.wantErr {
				t.Errorf("loadSettings() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, settings)
			}
		})
	}
}

func TestFindMCPByName(t *testing.T) {
	settings := &Settings{
		MCPServers: map[string]MCPServer{
			"targetprocess": {
				Type:    "local",
				Command: "node",
			},
			"jira-mcp": {
				Type:    "local",
				Command: "python",
			},
			"GitHub": {
				Type:    "local",
				Command: "gh",
			},
		},
	}

	tests := []struct {
		name       string
		searchName string
		wantKey    string
		wantFound  bool
	}{
		{
			name:       "exact match",
			searchName: "targetprocess",
			wantKey:    "targetprocess",
			wantFound:  true,
		},
		{
			name:       "case insensitive exact match",
			searchName: "TargetProcess",
			wantKey:    "targetprocess",
			wantFound:  true,
		},
		{
			name:       "partial match - target",
			searchName: "target",
			wantKey:    "targetprocess",
			wantFound:  true,
		},
		{
			name:       "partial match - target-process",
			searchName: "target-process",
			wantKey:    "targetprocess",
			wantFound:  true,
		},
		{
			name:       "partial match - jira",
			searchName: "jira",
			wantKey:    "jira-mcp",
			wantFound:  true,
		},
		{
			name:       "case insensitive partial match",
			searchName: "github",
			wantKey:    "GitHub",
			wantFound:  true,
		},
		{
			name:       "not found",
			searchName: "nonexistent",
			wantFound:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{}

			key, server, err := m.findMCPByName(settings, tt.searchName)

			if !tt.wantFound {
				if err == nil {
					t.Error("findMCPByName() should return error for not found")
					return
				}
				if !strings.Contains(err.Error(), "not found") {
					t.Errorf("error should mention 'not found', got %v", err)
				}
				return
			}

			if err != nil {
				t.Errorf("findMCPByName() error = %v, want found", err)
			}
			if key != tt.wantKey {
				t.Errorf("findMCPByName() key = %s, want %s", key, tt.wantKey)
			}
			if server == nil {
				t.Error("findMCPByName() server should not be nil")
			}
		})
	}
}

func TestEnable(t *testing.T) {
	tests := []struct {
		name         string
		mcpName      string
		settings     *Settings
		mockOutput   string
		shouldFail   bool
		wantErr      bool
		checkCommand func(t *testing.T, cmd string, args []string)
	}{
		{
			name:    "enables MCP successfully",
			mcpName: "targetprocess",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"targetprocess": {
						Type:    "local",
						Command: "node",
						Args:    []string{"server.js"},
					},
				},
			},
			mockOutput: "",
			checkCommand: func(t *testing.T, cmd string, args []string) {
				t.Helper()
				if cmd != "claude" {
					t.Errorf("command = %s, want claude", cmd)
				}
				expectedArgs := []string{"mcp", "add", "targetprocess", "node", "server.js"}
				if !slicesEqual(args, expectedArgs) {
					t.Errorf("args = %v, want %v", args, expectedArgs)
				}
			},
		},
		{
			name:    "expands home directory in command",
			mcpName: "test",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"test": {
						Command: "~/bin/mcp",
						Args:    []string{"--port", "3000"},
					},
				},
			},
			checkCommand: func(t *testing.T, _ string, args []string) {
				t.Helper()
				// args is [mcp, add, name, command, ...serverArgs]; the
				// expanded command is at index 3.
				if len(args) < 4 {
					t.Fatalf("Not enough args: %v", args)
				}
				commandPath := args[3]
				if strings.Contains(commandPath, "~") {
					t.Error("Command path should have ~ expanded")
				}
				if !strings.Contains(commandPath, "/bin/mcp") {
					t.Error("Command path should contain /bin/mcp")
				}
			},
		},
		{
			name:    "handles already enabled",
			mcpName: "jira",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"jira": {Command: "jira-mcp"},
				},
			},
			mockOutput: "MCP server 'jira' already exists",
			shouldFail: true,
			wantErr:    false, // Should not error when already enabled
		},
		{
			name:    "MCP not in settings",
			mcpName: "nonexistent",
			settings: &Settings{
				MCPServers: map[string]MCPServer{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")

			// Write settings
			data, _ := json.MarshalIndent(tt.settings, "", "  ")
			os.WriteFile(settingsPath, data, 0600)

			// Create mock executor
			mockExecutor := &MockCommandExecutor{
				mockOutput: tt.mockOutput,
				shouldFail: tt.shouldFail,
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				settingsPath: settingsPath,
				output:       out,
				executor:     mockExecutor,
			}

			err := m.Enable(context.Background(), tt.mcpName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Enable() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.checkCommand != nil && !tt.wantErr {
				tt.checkCommand(t, mockExecutor.capturedCmd, mockExecutor.capturedArgs)
			}
		})
	}
}

func TestDisable(t *testing.T) {
	tests := []struct {
		name         string
		mcpName      string
		settings     *Settings
		mockOutput   string
		shouldFail   bool
		wantErr      bool
		checkCommand func(t *testing.T, cmd string, args []string)
	}{
		{
			name:    "disables MCP successfully",
			mcpName: "targetprocess",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"targetprocess": {Type: "local"},
				},
			},
			checkCommand: func(t *testing.T, cmd string, args []string) {
				t.Helper()
				if cmd != "claude" {
					t.Errorf("command = %s, want claude", cmd)
				}
				expectedArgs := []string{"mcp", "remove", "targetprocess"}
				if !slicesEqual(args, expectedArgs) {
					t.Errorf("args = %v, want %v", args, expectedArgs)
				}
			},
		},
		{
			name:       "handles not found",
			mcpName:    "nonexistent",
			settings:   &Settings{MCPServers: map[string]MCPServer{}},
			mockOutput: "MCP server 'nonexistent' not found",
			shouldFail: true,
			wantErr:    false, // Should not error when not found
		},
		{
			name:    "uses provided name when not in settings",
			mcpName: "custom-mcp",
			settings: &Settings{
				MCPServers: map[string]MCPServer{},
			},
			checkCommand: func(t *testing.T, _ string, args []string) {
				t.Helper()
				// Should still try to remove with the provided name
				expectedArgs := []string{"mcp", "remove", "custom-mcp"}
				if !slicesEqual(args, expectedArgs) {
					t.Errorf("args = %v, want %v", args, expectedArgs)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")

			// Write settings
			data, _ := json.MarshalIndent(tt.settings, "", "  ")
			os.WriteFile(settingsPath, data, 0600)

			// Create mock executor
			mockExecutor := &MockCommandExecutor{
				mockOutput: tt.mockOutput,
				shouldFail: tt.shouldFail,
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				settingsPath: settingsPath,
				output:       out,
				executor:     mockExecutor,
			}

			err := m.Disable(context.Background(), tt.mcpName)
			if (err != nil) != tt.wantErr {
				t.Errorf("Disable() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.checkCommand != nil && !tt.wantErr {
				tt.checkCommand(t, mockExecutor.capturedCmd, mockExecutor.capturedArgs)
			}
		})
	}
}

func TestEnableAll(t *testing.T) {
	tests := []struct {
		name           string
		settings       *Settings
		enabledServers []string
		failServers    []string
		wantErr        bool
	}{
		{
			name: "enables all servers",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"server1": {Command: "cmd1"},
					"server2": {Command: "cmd2"},
					"server3": {Command: "cmd3"},
				},
			},
			enabledServers: []string{"server1", "server2", "server3"},
		},
		{
			name: "handles partial failures",
			settings: &Settings{
				MCPServers: map[string]MCPServer{
					"server1": {Command: "cmd1"},
					"server2": {Command: "cmd2"},
				},
			},
			enabledServers: []string{"server1"},
			failServers:    []string{"server2"},
			wantErr:        true,
		},
		{
			name: "handles empty servers",
			settings: &Settings{
				MCPServers: map[string]MCPServer{},
			},
			enabledServers: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")

			// Write settings
			data, _ := json.MarshalIndent(tt.settings, "", "  ")
			os.WriteFile(settingsPath, data, 0600)

			// Track which servers were attempted to enable
			enabledServers := make(map[string]bool)

			// Create mock executor with custom handler
			mockExecutor := &MockCommandExecutor{
				commandHandler: func(_ string, args []string) *exec.Cmd {
					if len(args) >= 3 && args[0] == "mcp" && args[1] == "add" {
						serverName := args[2]
						enabledServers[serverName] = true

						// Check if this server should fail
						for _, failServer := range tt.failServers {
							if serverName == failServer {
								return exec.Command("false")
							}
						}
					}
					return exec.Command("echo", "success")
				},
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				settingsPath: settingsPath,
				output:       out,
				executor:     mockExecutor,
			}

			err := m.EnableAll(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("EnableAll() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check that all expected servers were attempted
			for _, expected := range tt.enabledServers {
				if !enabledServers[expected] {
					t.Errorf("Server %s was not attempted to enable", expected)
				}
			}
		})
	}
}

func TestDisableAll(t *testing.T) {
	tests := []struct {
		name           string
		listOutput     string
		expectedRemove []string
		wantErr        bool
	}{
		{
			name: "disables all listed servers",
			listOutput: `Checking MCP servers...
targetprocess: Running
jira-mcp: Running
github: Stopped`,
			expectedRemove: []string{"targetprocess", "jira-mcp", "github"},
		},
		{
			name:           "handles no servers",
			listOutput:     "Checking MCP servers...\n",
			expectedRemove: []string{},
		},
		{
			name: "parses various output formats",
			listOutput: `Some header text
server1: Status information here
server2: Different status
Other text that should be ignored
server3: More status`,
			expectedRemove: []string{"server1", "server2", "server3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Track which servers were removed
			removedServers := make(map[string]bool)

			// Create mock executor with custom handler
			mockExecutor := &MockCommandExecutor{
				commandHandler: func(_ string, args []string) *exec.Cmd {
					if len(args) >= 2 && args[0] == "mcp" {
						if args[1] == "list" {
							// Return mock list output
							return exec.Command("echo", tt.listOutput)
						}
						if args[1] == "remove" && len(args) >= 3 {
							serverName := args[2]
							removedServers[serverName] = true
						}
					}
					return exec.Command("echo", "success")
				},
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				output:   out,
				executor: mockExecutor,
			}

			err := m.DisableAll(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("DisableAll() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check that all expected servers were removed
			for _, expected := range tt.expectedRemove {
				if !removedServers[expected] {
					t.Errorf("Server %s was not removed", expected)
				}
			}

			// Check no unexpected servers were removed
			for removed := range removedServers {
				found := false
				for _, expected := range tt.expectedRemove {
					if removed == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Unexpected server %s was removed", removed)
				}
			}
		})
	}
}

func TestList(t *testing.T) {
	tests := []struct {
		name       string
		shouldFail bool
		wantErr    bool
	}{
		{
			name:    "lists successfully",
			wantErr: false,
		},
		{
			name:       "handles command error",
			shouldFail: true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock executor
			mockExecutor := &MockCommandExecutor{
				shouldFail: tt.shouldFail,
				mockOutput: "MCP list output",
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				output:   out,
				executor: mockExecutor,
			}

			err := m.List(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("List() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				if mockExecutor.capturedCmd != "claude" {
					t.Errorf("command = %s, want claude", mockExecutor.capturedCmd)
				}
				expectedArgs := []string{"mcp", "list"}
				if !slicesEqual(mockExecutor.capturedArgs, expectedArgs) {
					t.Errorf("args = %v, want %v", mockExecutor.capturedArgs, expectedArgs)
				}
			}
		})
	}
}

func TestRemoveMCP(t *testing.T) {
	tests := []struct {
		name       string
		mcpName    string
		mockOutput string
		shouldFail bool
		wantErr    bool
	}{
		{
			name:    "removes successfully",
			mcpName: "test-mcp",
		},
		{
			name:       "handles not found",
			mcpName:    "nonexistent",
			mockOutput: "MCP server 'nonexistent' not found",
			shouldFail: true,
			wantErr:    false, // Should not error when not found
		},
		{
			name:       "handles other errors",
			mcpName:    "test-mcp",
			mockOutput: "Some other error",
			shouldFail: true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock executor
			mockExecutor := &MockCommandExecutor{
				mockOutput: tt.mockOutput,
				shouldFail: tt.shouldFail,
			}

			out := output.NewTerminal(&bytes.Buffer{}, &bytes.Buffer{})
			m := &Manager{
				output:   out,
				executor: mockExecutor,
			}

			err := m.removeMCP(context.Background(), tt.mcpName)
			if (err != nil) != tt.wantErr {
				t.Errorf("removeMCP() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHomeDirectoryExpansion(t *testing.T) {
	homeDir, _ := os.UserHomeDir()

	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{
			name:     "expands tilde",
			command:  "~/bin/mcp",
			expected: filepath.Join(homeDir, "bin/mcp"),
		},
		{
			name:     "leaves absolute path unchanged",
			command:  "/usr/local/bin/mcp",
			expected: "/usr/local/bin/mcp",
		},
		{
			name:     "leaves relative path unchanged",
			command:  "./bin/mcp",
			expected: "./bin/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the expansion logic
			command := tt.command
			if strings.HasPrefix(command, "~/") {
				command = filepath.Join(homeDir, command[2:])
			}

			if command != tt.expected {
				t.Errorf("expanded command = %s, want %s", command, tt.expected)
			}
		})
	}
}

// Helper function to compare slices.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
