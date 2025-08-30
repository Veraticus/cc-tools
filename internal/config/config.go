// Package config manages application configuration.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config represents the application configuration.
type Config struct {
	Hooks         HooksConfig         `json:"hooks"`
	Notifications NotificationsConfig `json:"notifications"`
}

// HooksConfig represents hook-related settings.
type HooksConfig struct {
	Validate ValidateConfig `json:"validate"`
}

// ValidateConfig represents validate hook settings.
type ValidateConfig struct {
	CooldownSeconds int `json:"cooldown_seconds"`
	TimeoutSeconds  int `json:"timeout_seconds"`
}

// NotificationsConfig represents notification settings.
type NotificationsConfig struct {
	NtfyTopic string `json:"ntfy_topic"`
}

const (
	defaultCooldownSeconds = 5
	defaultTimeoutSeconds  = 60
)

// Load loads configuration from the config file.
// It reads from ~/.config/cc-tools/config.json.
func Load() (*Config, error) {
	// Set defaults
	cfg := &Config{
		Hooks: HooksConfig{
			Validate: ValidateConfig{
				CooldownSeconds: defaultCooldownSeconds,
				TimeoutSeconds:  defaultTimeoutSeconds,
			},
		},
	}

	// Try to read config file
	configPath := getConfigPath()
	data, err := os.ReadFile(configPath) //nolint:gosec // Path is controlled by our code
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config file: %w", err)
		}
		// Config file not found is OK, we'll use defaults
		return cfg, nil
	}

	// Parse the JSON config
	var fileConfig map[string]any
	if unmarshalErr := json.Unmarshal(data, &fileConfig); unmarshalErr != nil {
		return nil, fmt.Errorf("parse config file: %w", unmarshalErr)
	}

	// Extract validate settings if they exist
	if validate, validateOk := fileConfig["validate"].(map[string]any); validateOk {
		if timeout, timeoutOk := validate["timeout"].(float64); timeoutOk {
			cfg.Hooks.Validate.TimeoutSeconds = int(timeout)
		}
		if cooldown, cooldownOk := validate["cooldown"].(float64); cooldownOk {
			cfg.Hooks.Validate.CooldownSeconds = int(cooldown)
		}
	}

	// Extract notification settings if they exist
	if notifications, notifOk := fileConfig["notifications"].(map[string]any); notifOk {
		if topic, topicOk := notifications["ntfy_topic"].(string); topicOk {
			cfg.Notifications.NtfyTopic = topic
		}
	}

	return cfg, nil
}

// LoadFromManager loads configuration using the Manager for consistent config access.
// This is equivalent to Load() but ensures the config file exists.
func LoadFromManager(ctx context.Context) (*Config, error) {
	manager := NewManager()

	// Ensure config exists
	if err := manager.EnsureConfig(ctx); err != nil {
		return nil, fmt.Errorf("ensure config: %w", err)
	}

	// Now just use the regular Load function which reads from the same file
	return Load()
}

// getConfigPath returns the path to the configuration file.
func getConfigPath() string {
	// Check XDG_CONFIG_HOME first
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "cc-tools", "config.json")
	}

	// Default to ~/.config/cc-tools/config.json
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if we can't get home
		return "config.json"
	}

	return filepath.Join(homeDir, ".config", "cc-tools", "config.json")
}
