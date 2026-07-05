package debug

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLogger(t *testing.T) {
	ctx := context.Background()

	// writeConfig redirects $HOME (and on darwin $XDG-style fallbacks) to a
	// tempdir so that NewManager() — called inside NewLogger — sees the
	// fixture's debug-config.json rather than the developer's real ~/.claude.
	writeConfig := func(t *testing.T, enabled map[string]bool) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		m := NewManager()
		m.config = &Config{EnabledDirs: enabled}
		if err := m.Save(ctx); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	tests := []struct {
		name        string
		setupFunc   func(t *testing.T, workDir string)
		workDir     string
		wantEnabled bool
		wantErr     bool
	}{
		{
			name: "creates enabled logger when debug is on",
			setupFunc: func(t *testing.T, workDir string) {
				t.Helper()
				absDir, _ := filepath.Abs(workDir)
				writeConfig(t, map[string]bool{absDir: true})
			},
			workDir:     ".",
			wantEnabled: true,
		},
		{
			name: "creates disabled logger when debug is off",
			setupFunc: func(t *testing.T, _ string) {
				t.Helper()
				writeConfig(t, map[string]bool{})
			},
			workDir:     ".",
			wantEnabled: false,
		},
		{
			name: "creates enabled logger when parent directory is enabled",
			setupFunc: func(t *testing.T, workDir string) {
				t.Helper()
				parentDir, _ := filepath.Abs(filepath.Dir(workDir))
				writeConfig(t, map[string]bool{parentDir: true})
			},
			workDir:     "./subdir",
			wantEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := tt.workDir
			if tt.setupFunc != nil {
				tt.setupFunc(t, workDir)
			}

			logger, err := NewLogger(ctx, workDir)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewLogger() error = %v, wantErr %v", err, tt.wantErr)
			}

			if logger == nil {
				t.Fatal("NewLogger() returned nil")
			}

			if logger.enabled != tt.wantEnabled {
				t.Errorf("logger.enabled = %v, want %v", logger.enabled, tt.wantEnabled)
			}

			if tt.wantEnabled && logger.file == nil {
				t.Error("Enabled logger should have file handle")
			}

			if tt.wantEnabled && logger.filePath == "" {
				t.Error("Enabled logger should have file path")
			}

			// Cleanup
			if logger.file != nil {
				logger.Close()
			}
		})
	}
}

func TestLoggerLog(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		format    string
		args      []any
		checkFunc func(t *testing.T, content string)
	}{
		{
			name:    "logs message when enabled",
			enabled: true,
			format:  "Test message: %s",
			args:    []any{"value"},
			checkFunc: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "Test message: value") {
					t.Error("Log content should contain the message")
				}

				// Check timestamp format
				if !strings.Contains(content, "[20") { // Year starts with 20
					t.Error("Log should contain timestamp")
				}

				if !strings.Contains(content, "]") {
					t.Error("Log should have closing bracket for timestamp")
				}
			},
		},
		{
			name:    "does not log when disabled",
			enabled: false,
			format:  "Should not appear",
			args:    []any{},
			checkFunc: func(t *testing.T, content string) {
				t.Helper()
				if content != "" {
					t.Error("Disabled logger should not write anything")
				}
			},
		},
		{
			name:    "handles multiple arguments",
			enabled: true,
			format:  "Values: %d, %s, %v",
			args:    []any{42, "test", true},
			checkFunc: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "Values: 42, test, true") {
					t.Error("Should format multiple arguments correctly")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "test.log")

			var logger *Logger
			if tt.enabled {
				file, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				logger = &Logger{
					file:     file,
					filePath: tmpFile,
					enabled:  true,
				}
			} else {
				logger = &Logger{
					enabled: false,
				}
			}

			logger.Logf(tt.format, tt.args...)

			// Close to flush
			if logger.file != nil {
				logger.file.Close()
			}

			// Read content
			content, _ := os.ReadFile(tmpFile)

			if tt.checkFunc != nil {
				tt.checkFunc(t, string(content))
			}
		})
	}
}

func TestLoggerLogSection(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.log")
	file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

	logger := &Logger{
		file:     file,
		filePath: tmpFile,
		enabled:  true,
	}

	logger.LogSection("TEST SECTION")
	file.Close()

	content, _ := os.ReadFile(tmpFile)

	if !strings.Contains(string(content), "========== TEST SECTION ==========") {
		t.Error("Section header should be formatted with separators")
	}
}

func TestLoggerLogError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		context   string
		wantInLog string
	}{
		{
			name:      "logs error with context",
			err:       os.ErrNotExist,
			context:   "file operation",
			wantInLog: "ERROR in file operation:",
		},
		{
			name:      "handles nil error",
			err:       nil,
			context:   "operation",
			wantInLog: "", // Should not log anything
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "test.log")
			file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

			logger := &Logger{
				file:     file,
				filePath: tmpFile,
				enabled:  true,
			}

			logger.LogError(tt.err, tt.context)
			file.Close()

			content, _ := os.ReadFile(tmpFile)

			if tt.wantInLog != "" {
				if !strings.Contains(string(content), tt.wantInLog) {
					t.Errorf("Log should contain %q, got %s", tt.wantInLog, content)
				}
			} else {
				if len(content) > 0 {
					t.Error("Should not log anything for nil error")
				}
			}
		})
	}
}

func TestLoggerLogCommand(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.log")
	file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

	logger := &Logger{
		file:     file,
		filePath: tmpFile,
		enabled:  true,
	}

	logger.LogCommand("make", []string{"test", "-v"}, "/project/dir")
	file.Close()

	content, _ := os.ReadFile(tmpFile)
	contentStr := string(content)

	if !strings.Contains(contentStr, "Executing command: make") {
		t.Error("Should log command name")
	}

	if !strings.Contains(contentStr, "Args: [test -v]") {
		t.Error("Should log command arguments")
	}

	if !strings.Contains(contentStr, "Working dir: /project/dir") {
		t.Error("Should log working directory")
	}
}

func TestLoggerLogDiscovery(t *testing.T) {
	tests := []struct {
		name        string
		commandType string
		result      string
		workDir     string
		checkFunc   func(t *testing.T, content string)
	}{
		{
			name:        "logs successful discovery",
			commandType: "lint",
			result:      "make lint",
			workDir:     "/project",
			checkFunc: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "Discovery for lint in /project") {
					t.Error("Should log discovery context")
				}
				if !strings.Contains(content, "Found: make lint") {
					t.Error("Should log found command")
				}
			},
		},
		{
			name:        "logs failed discovery",
			commandType: "test",
			result:      "",
			workDir:     "/project",
			checkFunc: func(t *testing.T, content string) {
				t.Helper()
				if !strings.Contains(content, "Discovery for test in /project") {
					t.Error("Should log discovery context")
				}
				if !strings.Contains(content, "Not found") {
					t.Error("Should log not found")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "test.log")
			file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

			logger := &Logger{
				file:     file,
				filePath: tmpFile,
				enabled:  true,
			}

			logger.LogDiscovery(tt.commandType, tt.result, tt.workDir)
			file.Close()

			content, _ := os.ReadFile(tmpFile)

			if tt.checkFunc != nil {
				tt.checkFunc(t, string(content))
			}
		})
	}
}

func TestLoggerClose(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func() *Logger
		wantErr   bool
	}{
		{
			name: "closes file successfully",
			setupFunc: func() *Logger {
				tmpFile := filepath.Join(t.TempDir(), "test.log")
				file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				return &Logger{
					file:     file,
					filePath: tmpFile,
					enabled:  true,
				}
			},
		},
		{
			name: "handles nil file",
			setupFunc: func() *Logger {
				return &Logger{
					enabled: false,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := tt.setupFunc()

			err := logger.Close()
			if (err != nil) != tt.wantErr {
				t.Errorf("Close() error = %v, wantErr %v", err, tt.wantErr)
			}

			if logger.file != nil {
				t.Error("File handle should be nil after close")
			}

			// Try to close again - should not panic
			err2 := logger.Close()
			if err2 != nil {
				t.Error("Second close should not return error")
			}
		})
	}
}

func TestLoggerIsEnabled(t *testing.T) {
	tests := []struct {
		name   string
		logger *Logger
		want   bool
	}{
		{
			name: "returns true when enabled",
			logger: &Logger{
				enabled: true,
			},
			want: true,
		},
		{
			name: "returns false when disabled",
			logger: &Logger{
				enabled: false,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.logger.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoggerConcurrency(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.log")
	file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

	logger := &Logger{
		file:     file,
		filePath: tmpFile,
		enabled:  true,
	}
	defer logger.Close()

	// Run concurrent log operations
	done := make(chan bool, 4)

	go func() {
		for i := range 100 {
			logger.Logf("Message %d", i)
		}
		done <- true
	}()

	go func() {
		for range 50 {
			logger.LogSection("Section")
		}
		done <- true
	}()

	go func() {
		for range 50 {
			logger.LogError(os.ErrExist, "test")
		}
		done <- true
	}()

	go func() {
		for range 50 {
			logger.LogCommand("cmd", []string{"arg"}, "/dir")
		}
		done <- true
	}()

	// Wait for all goroutines
	for range 4 {
		<-done
	}

	// Should not panic or corrupt the file
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Check that we have content
	if len(content) == 0 {
		t.Error("Log file should have content after concurrent writes")
	}
}

func TestLoggerTimestampFormat(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.log")
	file, _ := os.OpenFile(tmpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)

	logger := &Logger{
		file:     file,
		filePath: tmpFile,
		enabled:  true,
	}

	// Log a message
	beforeLog := time.Now()
	logger.Logf("Test message")
	file.Close()

	content, _ := os.ReadFile(tmpFile)
	contentStr := string(content)

	// Extract timestamp from log
	start := strings.Index(contentStr, "[")
	end := strings.Index(contentStr, "]")

	if start == -1 || end == -1 || start >= end {
		t.Fatal("Could not find timestamp in log")
	}

	timestamp := contentStr[start+1 : end]

	// Parse the timestamp in the logger's local zone (logger.go uses
	// time.Now().Format(...), which is wall-clock in time.Local; bare time.Parse
	// would interpret the same digits as UTC and skew by the local offset).
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05.000", timestamp, time.Local)
	if err != nil {
		t.Errorf("Failed to parse timestamp %q: %v", timestamp, err)
	}

	// Check that timestamp is reasonable (within 1 second of when we logged)
	diff := parsed.Sub(beforeLog).Abs()
	if diff > time.Second {
		t.Errorf("Timestamp seems incorrect, diff = %v", diff)
	}
}

func TestLoggerFilePermissions(t *testing.T) {
	// tmpFile is not used in this test
	// tmpFile := filepath.Join(t.TempDir(), "test.log")

	// Create logger which should create file
	ctx := context.Background()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "debug-config.json")

	workDir, _ := os.Getwd()
	absDir, _ := filepath.Abs(workDir)

	m := &Manager{
		filepath: configPath,
		config: &Config{
			EnabledDirs: map[string]bool{
				absDir: true,
			},
		},
	}
	m.Save(ctx)

	// Note: We can't override getConfigDir, but we're testing
	// the logger behavior with the given Manager

	logger, err := NewLogger(ctx, workDir)
	if err != nil {
		t.Skipf("Could not create logger: %v", err)
	}
	defer logger.Close()

	if !logger.enabled {
		t.Skip("Logger not enabled")
	}

	// Check file permissions
	info, err := os.Stat(logger.filePath)
	if err != nil {
		t.Fatalf("Failed to stat log file: %v", err)
	}

	mode := info.Mode()
	if mode.Perm() != 0600 {
		t.Errorf("Log file permissions = %v, want 0600", mode.Perm())
	}
}

func TestLoggerDisabledOperations(t *testing.T) {
	// Test that all operations are safe on a disabled logger
	logger := &Logger{
		enabled: false,
	}

	// None of these should panic
	logger.Logf("test")
	logger.LogSection("section")
	logger.LogError(os.ErrNotExist, "context")
	logger.LogCommand("cmd", []string{"arg"}, "/dir")
	logger.LogDiscovery("type", "result", "/dir")

	if logger.IsEnabled() {
		t.Error("Disabled logger should report as disabled")
	}

	err := logger.Close()
	if err != nil {
		t.Errorf("Close on disabled logger should not error: %v", err)
	}
}
