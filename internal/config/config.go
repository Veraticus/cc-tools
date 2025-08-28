// Package config manages application configuration using Viper.
// Test edit to trigger hooks - checking hook output.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config represents the application configuration.
type Config struct {
	Hooks         HooksConfig         `mapstructure:"hooks"`
	Notifications NotificationsConfig `mapstructure:"notifications"`
}

// HooksConfig represents hook-related settings.
type HooksConfig struct {
	Lint LintConfig `mapstructure:"lint"`
	Test TestConfig `mapstructure:"test"`
}

// LintConfig represents lint hook settings.
type LintConfig struct {
	CooldownSeconds int `mapstructure:"cooldown_seconds"`
	TimeoutSeconds  int `mapstructure:"timeout_seconds"`
}

// TestConfig represents test hook settings.
type TestConfig struct {
	CooldownSeconds int `mapstructure:"cooldown_seconds"`
	TimeoutSeconds  int `mapstructure:"timeout_seconds"`
}

// NotificationsConfig represents notification settings.
type NotificationsConfig struct {
	NtfyTopic string `mapstructure:"ntfy_topic"`
}

// Load loads configuration from files and environment variables.
// It searches for config files in the following order:
// 1. /etc/cc-tools/config.{toml,yaml,yml}
// 2. $XDG_CONFIG_HOME/cc-tools/config.{toml,yaml,yml} (or ~/.config/cc-tools/)
// 3. ./config.{toml,yaml,yml}
//
// Environment variables override file settings using the prefix CC_TOOLS_
// For example: CC_TOOLS_NOTIFICATIONS_NTFY_TOPIC.
func Load() (*Config, error) {
	v := viper.New() //nolint:forbidigo // viper.New is required for configuration

	// Set defaults for hooks
	const (
		defaultCooldownSeconds = 2
		defaultLintTimeout     = 30
		defaultTestTimeout     = 60
	)
	v.SetDefault("hooks.lint.cooldown_seconds", defaultCooldownSeconds)
	v.SetDefault("hooks.lint.timeout_seconds", defaultLintTimeout)
	v.SetDefault("hooks.test.cooldown_seconds", defaultCooldownSeconds)
	v.SetDefault("hooks.test.timeout_seconds", defaultTestTimeout)

	// Set config file name (without extension)
	v.SetConfigName("config")

	// Add config search paths
	v.AddConfigPath("/etc/cc-tools/")
	v.AddConfigPath(getXDGConfigPath())
	v.AddConfigPath(".")

	// Set environment variable prefix
	v.SetEnvPrefix("CC_TOOLS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Try to read config file (it's OK if it doesn't exist)
	if err := v.ReadInConfig(); err != nil {
		// Only return error if it's not a "not found" error
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if !errors.As(err, &configFileNotFoundError) {
			return nil, fmt.Errorf("read config file: %w", err)
		}
		// Config file not found is OK, we'll use defaults and env vars
	}

	// Unmarshal config into struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}

// LoadWithViper loads configuration using a provided Viper instance.
// This is useful for testing or when you want to configure Viper differently.
func LoadWithViper(v *viper.Viper) (*Config, error) {
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

// getXDGConfigPath returns the XDG config directory for cc-tools.
func getXDGConfigPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "cc-tools")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if we can't get home
		return "."
	}

	return filepath.Join(homeDir, ".config", "cc-tools")
}
