package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromJSON(t *testing.T) {
	// Create a temporary directory for test config
	tempDir := t.TempDir()

	// Set XDG_CONFIG_HOME to use our temp directory
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Create steward directory
	stewardDir := filepath.Join(tempDir, "steward")
	if err := os.MkdirAll(stewardDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Write test JSON config
	configPath := filepath.Join(stewardDir, "config.json")
	jsonContent := map[string]any{
		"notifications": map[string]any{
			"ntfy_topic": "test-topic",
		},
	}

	data, err := json.MarshalIndent(jsonContent, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	if writeErr := os.WriteFile(configPath, data, 0644); writeErr != nil {
		t.Fatalf("Failed to write test config: %v", writeErr)
	}

	// Load config
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check values
	if cfg.Notifications.NtfyTopic != "test-topic" {
		t.Errorf("Expected ntfy_topic to be 'test-topic', got '%s'", cfg.Notifications.NtfyTopic)
	}
}

func TestLoadIgnoresOldConfigDirectory(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	oldDir := filepath.Join(tempDir, "cc-tools")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(oldDir, "config.json"),
		[]byte(`{"notifications":{"ntfy_topic":"old-topic"}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notifications.NtfyTopic != "" {
		t.Errorf("old config directory was loaded: topic = %q", cfg.Notifications.NtfyTopic)
	}
}

func TestLoadDefaults(t *testing.T) {
	// Set XDG_CONFIG_HOME to a non-existent directory
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "nonexistent"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check default values - notifications should be empty
	if cfg.Notifications.NtfyTopic != "" {
		t.Errorf("Expected default ntfy_topic to be empty, got %s", cfg.Notifications.NtfyTopic)
	}
}

func TestLoadFromManager(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for test config
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Use manager to set some values
	manager := NewManager()
	if err := manager.EnsureConfig(ctx); err != nil {
		t.Fatalf("Failed to ensure config: %v", err)
	}

	if err := manager.Set(ctx, "statusline.workspace", "test-workspace"); err != nil {
		t.Fatalf("Failed to set workspace: %v", err)
	}

	// Load config using manager
	cfg, err := LoadFromManager(ctx)
	if err != nil {
		t.Fatalf("Failed to load config from manager: %v", err)
	}

	// Check values - Config struct doesn't have statusline, but we can verify it loaded without error
	if cfg == nil {
		t.Error("Expected config to be loaded")
	}
}

func TestGetConfigPath(t *testing.T) {
	// Test with XDG_CONFIG_HOME set
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")

	path := getConfigPath()
	expected := "/custom/config/steward/config.json"
	if path != expected {
		t.Errorf("Expected config path to be %s, got %s", expected, path)
	}

	// Test without XDG_CONFIG_HOME
	os.Unsetenv("XDG_CONFIG_HOME")
	path = getConfigPath()

	// Should contain .config/steward/config.json
	if !filepath.IsAbs(path) {
		t.Errorf("Expected absolute path, got %s", path)
	}
	if filepath.Base(path) != "config.json" {
		t.Errorf("Expected file name to be config.json, got %s", filepath.Base(path))
	}
}
