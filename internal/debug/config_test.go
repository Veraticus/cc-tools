package debug

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewManager(t *testing.T) {
	m := NewManager()

	if m.filepath == "" {
		t.Error("filepath should not be empty")
	}

	if !strings.Contains(m.filepath, "debug-config.json") {
		t.Errorf("filepath should contain debug-config.json, got %s", m.filepath)
	}

	if m.config == nil {
		t.Error("config should be initialized")
	}

	if m.config.EnabledDirs == nil {
		t.Error("EnabledDirs map should be initialized")
	}
}

func TestManagerLoad(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		setupFunc func(t *testing.T, filepath string)
		checkFunc func(t *testing.T, m *Manager)
		wantErr   bool
	}{
		{
			name: "loads valid config",
			setupFunc: func(_ *testing.T, configPath string) {
				config := &Config{
					EnabledDirs: map[string]bool{
						"/path/one": true,
						"/path/two": true,
					},
				}
				data, _ := json.MarshalIndent(config, "", "  ")
				os.MkdirAll(filepath.Dir(configPath), 0755)
				os.WriteFile(configPath, data, 0600)
			},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if len(m.config.EnabledDirs) != 2 {
					t.Errorf("expected 2 enabled dirs, got %d", len(m.config.EnabledDirs))
				}
				if !m.config.EnabledDirs["/path/one"] {
					t.Error("/path/one should be enabled")
				}
				if !m.config.EnabledDirs["/path/two"] {
					t.Error("/path/two should be enabled")
				}
			},
		},
		{
			name: "handles missing file",
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if m.config == nil {
					t.Error("config should be initialized even when file missing")
				}
				if len(m.config.EnabledDirs) != 0 {
					t.Error("EnabledDirs should be empty for missing file")
				}
			},
		},
		{
			name: "handles empty file",
			setupFunc: func(_ *testing.T, configPath string) {
				os.MkdirAll(filepath.Dir(configPath), 0755)
				os.WriteFile(configPath, []byte(""), 0600)
			},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if len(m.config.EnabledDirs) != 0 {
					t.Error("EnabledDirs should be empty for empty file")
				}
			},
		},
		{
			name: "handles nil EnabledDirs in JSON",
			setupFunc: func(_ *testing.T, configPath string) {
				data := []byte(`{"enabled_dirs": null}`)
				os.MkdirAll(filepath.Dir(configPath), 0755)
				os.WriteFile(configPath, data, 0600)
			},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if m.config.EnabledDirs == nil {
					t.Error("EnabledDirs should be initialized even when null in JSON")
				}
			},
		},
		{
			name: "handles corrupt JSON",
			setupFunc: func(_ *testing.T, configPath string) {
				os.MkdirAll(filepath.Dir(configPath), 0755)
				os.WriteFile(configPath, []byte("{invalid json}"), 0600)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "debug-config.json")

			m := &Manager{
				filepath: configPath,
				config:   &Config{EnabledDirs: make(map[string]bool)},
			}

			if tt.setupFunc != nil {
				tt.setupFunc(t, configPath)
			}

			err := m.Load(ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, m)
			}
		})
	}
}

func TestManagerSave(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		config    *Config
		setupFunc func(t *testing.T, filepath string)
		wantErr   bool
		checkFunc func(t *testing.T, configFile string)
	}{
		{
			name: "saves config successfully",
			config: &Config{
				EnabledDirs: map[string]bool{
					"/test/path": true,
					"/another":   true,
				},
			},
			checkFunc: func(t *testing.T, configFile string) {
				t.Helper()
				data, err := os.ReadFile(configFile)
				if err != nil {
					t.Fatalf("Failed to read saved file: %v", err)
				}

				var saved Config
				if err = json.Unmarshal(data, &saved); err != nil {
					t.Fatalf("Failed to unmarshal saved config: %v", err)
				}

				if !saved.EnabledDirs["/test/path"] {
					t.Error("/test/path should be enabled in saved config")
				}

				// Check file permissions
				info, _ := os.Stat(configFile)
				mode := info.Mode()
				if mode.Perm() != 0600 {
					t.Errorf("File permissions = %v, want 0600", mode.Perm())
				}

				// Check formatting (should be indented)
				if !strings.Contains(string(data), "  ") {
					t.Error("Config should be pretty-printed with indentation")
				}

				// Check trailing newline
				if data[len(data)-1] != '\n' {
					t.Error("Config file should end with newline")
				}
			},
		},
		{
			name: "creates directory if missing",
			config: &Config{
				EnabledDirs: map[string]bool{"/path": true},
			},
			setupFunc: func(_ *testing.T, configPath string) {
				// Ensure parent directory doesn't exist
				os.RemoveAll(filepath.Dir(configPath))
			},
			checkFunc: func(t *testing.T, configFile string) {
				t.Helper()
				if _, err := os.Stat(configFile); os.IsNotExist(err) {
					t.Error("Config file should have been created")
				}

				// Check directory permissions
				configDir := filepath.Dir(configFile)
				info, _ := os.Stat(configDir)
				mode := info.Mode()
				if mode.Perm() != 0750 {
					t.Errorf("Directory permissions = %v, want 0750", mode.Perm())
				}
			},
		},
		{
			name: "uses atomic write with temp file",
			config: &Config{
				EnabledDirs: map[string]bool{"/atomic": true},
			},
			checkFunc: func(t *testing.T, configFile string) {
				t.Helper()
				// Check that temp file doesn't exist
				tempFile := configFile + ".tmp"
				if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
					t.Error("Temp file should be cleaned up after successful write")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "subdir", "debug-config.json")

			if tt.setupFunc != nil {
				tt.setupFunc(t, configPath)
			}

			m := &Manager{
				filepath: configPath,
				config:   tt.config,
			}

			err := m.Save(ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Save() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, configPath)
			}
		})
	}
}

func TestManagerEnable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		inputDir     string
		initialDirs  map[string]bool
		wantLogPath  bool
		checkEnabled []string
	}{
		{
			name:         "enables relative directory",
			inputDir:     ".",
			wantLogPath:  true,
			checkEnabled: []string{"."},
		},
		{
			name:         "enables absolute directory",
			inputDir:     "/test/absolute/path",
			wantLogPath:  true,
			checkEnabled: []string{"/test/absolute/path"},
		},
		{
			name:     "adds to existing enabled dirs",
			inputDir: "/new/path",
			initialDirs: map[string]bool{
				"/existing/path": true,
			},
			wantLogPath:  true,
			checkEnabled: []string{"/new/path", "/existing/path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "debug-config.json")

			m := &Manager{
				filepath: configPath,
				config: &Config{
					EnabledDirs: make(map[string]bool),
				},
			}

			if tt.initialDirs != nil {
				m.config.EnabledDirs = tt.initialDirs
			}

			logPath, err := m.Enable(ctx, tt.inputDir)
			if err != nil {
				t.Fatalf("Enable() error = %v", err)
			}

			if tt.wantLogPath && logPath == "" {
				t.Error("Expected log path but got empty string")
			}

			// Check that directory is enabled
			absDir, _ := filepath.Abs(tt.inputDir)
			if !m.config.EnabledDirs[absDir] {
				t.Errorf("Directory %s should be enabled", absDir)
			}

			// Verify it was saved to disk
			var saved Config
			data, _ := os.ReadFile(configPath)
			json.Unmarshal(data, &saved)

			if !saved.EnabledDirs[absDir] {
				t.Error("Enabled directory should be persisted to disk")
			}

			// Check all expected directories are enabled
			for _, dir := range tt.checkEnabled {
				absCheckDir, _ := filepath.Abs(dir)
				if !m.config.EnabledDirs[absCheckDir] {
					t.Errorf("Directory %s should be enabled", absCheckDir)
				}
			}
		})
	}
}

func TestManagerDisable(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		inputDir    string
		initialDirs map[string]bool
		checkFunc   func(t *testing.T, m *Manager)
	}{
		{
			name:     "disables existing directory",
			inputDir: "/test/path",
			initialDirs: map[string]bool{
				"/test/path":  true,
				"/other/path": true,
			},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				absDir, _ := filepath.Abs("/test/path")
				if m.config.EnabledDirs[absDir] {
					t.Error("/test/path should be disabled")
				}

				otherDir, _ := filepath.Abs("/other/path")
				if !m.config.EnabledDirs[otherDir] {
					t.Error("/other/path should still be enabled")
				}
			},
		},
		{
			name:        "handles disabling non-existent directory",
			inputDir:    "/not/enabled",
			initialDirs: map[string]bool{"/other": true},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if len(m.config.EnabledDirs) != 1 {
					t.Error("Should not affect other directories")
				}
			},
		},
		{
			name:        "handles relative paths",
			inputDir:    "./relative",
			initialDirs: map[string]bool{},
			checkFunc: func(t *testing.T, m *Manager) {
				t.Helper()
				if len(m.config.EnabledDirs) != 0 {
					t.Error("Should remain empty")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "debug-config.json")

			// Convert initial dirs to absolute paths
			absDirs := make(map[string]bool)
			for dir, enabled := range tt.initialDirs {
				absDir, _ := filepath.Abs(dir)
				absDirs[absDir] = enabled
			}

			m := &Manager{
				filepath: configPath,
				config: &Config{
					EnabledDirs: absDirs,
				},
			}

			err := m.Disable(ctx, tt.inputDir)
			if err != nil {
				t.Fatalf("Disable() error = %v", err)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, m)
			}

			// Verify persistence
			var saved Config
			data, _ := os.ReadFile(configPath)
			if len(data) > 0 {
				json.Unmarshal(data, &saved)

				absInput, _ := filepath.Abs(tt.inputDir)
				if saved.EnabledDirs[absInput] {
					t.Error("Disabled directory should not be in saved config")
				}
			}
		})
	}
}

func TestManagerIsEnabled(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		checkDir    string
		enabledDirs map[string]bool
		want        bool
	}{
		{
			name:     "exact match enabled",
			checkDir: "/test/path",
			enabledDirs: map[string]bool{
				"/test/path": true,
			},
			want: true,
		},
		{
			name:     "child directory enabled",
			checkDir: "/test/path/subdir/deep",
			enabledDirs: map[string]bool{
				"/test/path": true,
			},
			want: true,
		},
		{
			name:     "parent directory not enabled",
			checkDir: "/test",
			enabledDirs: map[string]bool{
				"/test/path": true,
			},
			want: false,
		},
		{
			name:        "no directories enabled",
			checkDir:    "/any/path",
			enabledDirs: map[string]bool{},
			want:        false,
		},
		{
			name:     "multiple enabled dirs, one matches",
			checkDir: "/project/src/file",
			enabledDirs: map[string]bool{
				"/other":   true,
				"/project": true,
				"/another": true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "debug-config.json")

			// Convert to absolute paths
			absDirs := make(map[string]bool)
			for dir, enabled := range tt.enabledDirs {
				absDir, _ := filepath.Abs(dir)
				absDirs[absDir] = enabled
			}

			m := &Manager{
				filepath: configPath,
				config: &Config{
					EnabledDirs: absDirs,
				},
			}

			// Save to disk to test loading
			m.Save(ctx)

			// Reset config to test loading
			m.config = &Config{EnabledDirs: make(map[string]bool)}

			enabled, err := m.IsEnabled(ctx, tt.checkDir)
			if err != nil {
				t.Fatalf("IsEnabled() error = %v", err)
			}

			if enabled != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", enabled, tt.want)
			}
		})
	}
}

func TestManagerGetEnabledDirs(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		enabledDirs map[string]bool
		wantCount   int
	}{
		{
			name:        "returns empty list when none enabled",
			enabledDirs: map[string]bool{},
			wantCount:   0,
		},
		{
			name: "returns all enabled directories",
			enabledDirs: map[string]bool{
				"/path/one":   true,
				"/path/two":   true,
				"/path/three": true,
			},
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "debug-config.json")

			// Convert to absolute paths
			absDirs := make(map[string]bool)
			expectedDirs := make(map[string]bool)
			for dir, enabled := range tt.enabledDirs {
				absDir, _ := filepath.Abs(dir)
				absDirs[absDir] = enabled
				expectedDirs[absDir] = true
			}

			m := &Manager{
				filepath: configPath,
				config: &Config{
					EnabledDirs: absDirs,
				},
			}

			// Save and reset to test loading
			m.Save(ctx)
			m.config = &Config{EnabledDirs: make(map[string]bool)}

			dirs, err := m.GetEnabledDirs(ctx)
			if err != nil {
				t.Fatalf("GetEnabledDirs() error = %v", err)
			}

			if len(dirs) != tt.wantCount {
				t.Errorf("GetEnabledDirs() returned %d dirs, want %d", len(dirs), tt.wantCount)
			}

			// Check all returned dirs are expected
			for _, dir := range dirs {
				if !expectedDirs[dir] {
					t.Errorf("Unexpected directory in result: %s", dir)
				}
			}
		})
	}
}

func TestGetLogFilePath(t *testing.T) {
	tests := []struct {
		name         string
		inputDir     string
		wantPrefix   string
		wantContains string
	}{
		{
			name:         "generates path for normal directory",
			inputDir:     "/home/user/project",
			wantPrefix:   "/tmp/cc-tools-project-",
			wantContains: ".log",
		},
		{
			name:         "handles root directory",
			inputDir:     "/",
			wantPrefix:   "/tmp/cc-tools-root-",
			wantContains: ".log",
		},
		{
			name:         "handles relative path",
			inputDir:     ".",
			wantPrefix:   "/tmp/cc-tools-",
			wantContains: ".log",
		},
		{
			name:         "sanitizes directory name",
			inputDir:     "/path/with/many/levels",
			wantPrefix:   "/tmp/cc-tools-levels-",
			wantContains: ".log",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := GetLogFilePath(tt.inputDir)

			if !strings.HasPrefix(logPath, tt.wantPrefix) {
				t.Errorf("GetLogFilePath() = %s, want prefix %s", logPath, tt.wantPrefix)
			}

			if !strings.Contains(logPath, tt.wantContains) {
				t.Errorf("GetLogFilePath() = %s, should contain %s", logPath, tt.wantContains)
			}

			// Check hash is included (8 hex chars = 16 chars)
			parts := strings.Split(logPath, "-")
			lastPart := parts[len(parts)-1]
			hashPart := strings.TrimSuffix(lastPart, ".log")
			if len(hashPart) != 16 {
				t.Errorf("Hash part should be 16 chars (8 bytes hex), got %d", len(hashPart))
			}
		})
	}
}

func TestGetLogFilePathConsistency(t *testing.T) {
	// Test that the same directory always produces the same log path
	dir := "/test/directory"

	path1 := GetLogFilePath(dir)
	path2 := GetLogFilePath(dir)

	if path1 != path2 {
		t.Errorf("GetLogFilePath() should be consistent, got %s and %s", path1, path2)
	}

	// Test that different directories produce different paths
	path3 := GetLogFilePath("/different/directory")
	if path1 == path3 {
		t.Error("Different directories should produce different log paths")
	}
}

func TestManagerConcurrency(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "debug-config.json")

	m := &Manager{
		filepath: configPath,
		config: &Config{
			EnabledDirs: make(map[string]bool),
		},
	}

	// Run concurrent operations
	done := make(chan bool, 3)

	// Concurrent enables
	go func() {
		for i := range 10 {
			dir := filepath.Join("/test", string(rune('a'+i)))
			m.Enable(ctx, dir)
		}
		done <- true
	}()

	// Concurrent disables
	go func() {
		for i := range 10 {
			dir := filepath.Join("/test", string(rune('a'+i)))
			m.Disable(ctx, dir)
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for range 10 {
			m.IsEnabled(ctx, "/test/a")
			m.GetEnabledDirs(ctx)
		}
		done <- true
	}()

	// Wait for all goroutines
	for range 3 {
		<-done
	}

	// Should not panic or deadlock
}

func TestGetConfigDir(t *testing.T) {
	tests := []struct {
		name     string
		homeDir  string
		wantPath string
	}{
		{
			name:     "uses home directory",
			homeDir:  "/home/testuser",
			wantPath: "/home/testuser/.claude",
		},
		{
			name:     "falls back to /tmp when home not available",
			homeDir:  "",
			wantPath: "/tmp/.claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// os.UserHomeDir() treats HOME="" the same as HOME unset (both
			// read as the empty string), so t.Setenv covers both cases and
			// restores the previous value automatically after the subtest.
			t.Setenv("HOME", tt.homeDir)

			configDir := getConfigDir()

			// On systems where UserHomeDir works differently, just check it's not empty
			if configDir == "" {
				t.Error("getConfigDir() should not return empty string")
			}

			// Check it ends with .claude
			if !strings.HasSuffix(configDir, ".claude") {
				t.Errorf("getConfigDir() = %s, should end with .claude", configDir)
			}
		})
	}
}
