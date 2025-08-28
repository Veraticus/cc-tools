package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestLoadWithTOML(t *testing.T) {
	// Create a temporary directory for test config
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config.toml")

	// Write test TOML config
	tomlContent := `
[notifications]
ntfy_topic = "test-topic-from-toml"
`
	if err := os.WriteFile(configFile, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Create Viper instance for testing
	v := viper.New()
	v.SetConfigFile(configFile)

	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	cfg, err := LoadWithViper(v)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Notifications.NtfyTopic != "test-topic-from-toml" {
		t.Errorf("Expected ntfy_topic to be 'test-topic-from-toml', got '%s'", cfg.Notifications.NtfyTopic)
	}
}

func TestLoadWithYAML(t *testing.T) {
	// Create a temporary directory for test config
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config.yaml")

	// Write test YAML config
	yamlContent := `
notifications:
  ntfy_topic: test-topic-from-yaml
`
	if err := os.WriteFile(configFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Create Viper instance for testing
	v := viper.New()
	v.SetConfigFile(configFile)

	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	cfg, err := LoadWithViper(v)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Notifications.NtfyTopic != "test-topic-from-yaml" {
		t.Errorf("Expected ntfy_topic to be 'test-topic-from-yaml', got '%s'", cfg.Notifications.NtfyTopic)
	}
}

func TestLoadWithEnvironmentVariables(t *testing.T) {
	// Set environment variable
	envKey := "CC_TOOLS_NOTIFICATIONS_NTFY_TOPIC"
	envValue := "test-topic-from-env"

	t.Setenv(envKey, envValue)

	// Create Viper instance with env support
	v := viper.New()
	v.SetEnvPrefix("CC_TOOLS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind the env var to the config key
	v.BindEnv("notifications.ntfy_topic")

	cfg, err := LoadWithViper(v)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Notifications.NtfyTopic != envValue {
		t.Errorf("Expected ntfy_topic to be '%s' from env, got '%s'", envValue, cfg.Notifications.NtfyTopic)
	}
}

func TestLoadWithTOMLAndEnvOverride(t *testing.T) {
	// Create a temporary directory for test config
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config.toml")

	// Write test TOML config
	tomlContent := `
[notifications]
ntfy_topic = "topic-from-toml"
`
	if err := os.WriteFile(configFile, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Set environment variable to override TOML value
	envKey := "CC_TOOLS_NOTIFICATIONS_NTFY_TOPIC"
	envValue := "topic-from-env-override"

	t.Setenv(envKey, envValue)

	// Create Viper instance
	v := viper.New()
	v.SetConfigFile(configFile)
	v.SetEnvPrefix("CC_TOOLS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	cfg, err := LoadWithViper(v)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Environment variable should override TOML value
	if cfg.Notifications.NtfyTopic != envValue {
		t.Errorf("Expected ntfy_topic to be '%s' from env override, got '%s'", envValue, cfg.Notifications.NtfyTopic)
	}
}

func TestLoadWithNoConfig(t *testing.T) {
	// Create Viper instance with no config file
	v := viper.New()
	v.SetEnvPrefix("CC_TOOLS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg, err := LoadWithViper(v)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Should load successfully with empty values
	if cfg.Notifications.NtfyTopic != "" {
		t.Errorf("Expected empty ntfy_topic, got '%s'", cfg.Notifications.NtfyTopic)
	}
}

func TestGetXDGConfigPath(t *testing.T) {
	tests := []struct {
		name         string
		xdgConfig    string
		wantContains string
	}{
		{
			name:         "with XDG_CONFIG_HOME set",
			xdgConfig:    "/custom/config",
			wantContains: "/custom/config/cc-tools",
		},
		{
			name:         "without XDG_CONFIG_HOME",
			xdgConfig:    "",
			wantContains: ".config/cc-tools",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test value
			if tt.xdgConfig != "" {
				t.Setenv("XDG_CONFIG_HOME", tt.xdgConfig)
			} else {
				t.Setenv("XDG_CONFIG_HOME", "")
			}

			path := getXDGConfigPath()
			if !filepath.IsAbs(path) && tt.xdgConfig == "" {
				// If XDG_CONFIG_HOME is not set and we can't get home dir,
				// it should return "."
				if path != "." {
					t.Errorf("Expected '.', got '%s'", path)
				}
			} else if !strings.Contains(path, tt.wantContains) {
				t.Errorf("Expected path to contain '%s', got '%s'", tt.wantContains, path)
			}
		})
	}
}
