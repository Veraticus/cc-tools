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

	// Create cc-tools directory
	ccToolsDir := filepath.Join(tempDir, "cc-tools")
	if err := os.MkdirAll(ccToolsDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Write test JSON config
	configPath := filepath.Join(ccToolsDir, "config.json")
	jsonContent := map[string]any{
		"validate": map[string]any{
			"timeout":  90,
			"cooldown": 10,
		},
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
	if cfg.Hooks.Validate.TimeoutSeconds != 90 {
		t.Errorf("Expected timeout to be 90, got %d", cfg.Hooks.Validate.TimeoutSeconds)
	}
	if cfg.Hooks.Validate.CooldownSeconds != 10 {
		t.Errorf("Expected cooldown to be 10, got %d", cfg.Hooks.Validate.CooldownSeconds)
	}
	if cfg.Notifications.NtfyTopic != "test-topic" {
		t.Errorf("Expected ntfy_topic to be 'test-topic', got '%s'", cfg.Notifications.NtfyTopic)
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

	// Check default values
	if cfg.Hooks.Validate.TimeoutSeconds != 60 {
		t.Errorf("Expected default timeout to be 60, got %d", cfg.Hooks.Validate.TimeoutSeconds)
	}
	if cfg.Hooks.Validate.CooldownSeconds != 5 {
		t.Errorf("Expected default cooldown to be 5, got %d", cfg.Hooks.Validate.CooldownSeconds)
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

	if err := manager.Set(ctx, "validate.timeout", "100"); err != nil {
		t.Fatalf("Failed to set timeout: %v", err)
	}

	if err := manager.Set(ctx, "validate.cooldown", "8"); err != nil {
		t.Fatalf("Failed to set cooldown: %v", err)
	}

	// Load config using manager
	cfg, err := LoadFromManager(ctx)
	if err != nil {
		t.Fatalf("Failed to load config from manager: %v", err)
	}

	// Check values
	if cfg.Hooks.Validate.TimeoutSeconds != 100 {
		t.Errorf("Expected timeout to be 100, got %d", cfg.Hooks.Validate.TimeoutSeconds)
	}
	if cfg.Hooks.Validate.CooldownSeconds != 8 {
		t.Errorf("Expected cooldown to be 8, got %d", cfg.Hooks.Validate.CooldownSeconds)
	}
}

func TestGetConfigPath(t *testing.T) {
	// Test with XDG_CONFIG_HOME set
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")

	path := getConfigPath()
	expected := "/custom/config/cc-tools/config.json"
	if path != expected {
		t.Errorf("Expected config path to be %s, got %s", expected, path)
	}

	// Test without XDG_CONFIG_HOME
	os.Unsetenv("XDG_CONFIG_HOME")
	path = getConfigPath()

	// Should contain .config/cc-tools/config.json
	if !filepath.IsAbs(path) {
		t.Errorf("Expected absolute path, got %s", path)
	}
	if filepath.Base(path) != "config.json" {
		t.Errorf("Expected file name to be config.json, got %s", filepath.Base(path))
	}
}
