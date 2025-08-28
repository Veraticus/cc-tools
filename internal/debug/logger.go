// Package debug provides debug logging functionality.
package debug

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Logger handles debug logging to files.
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	enabled  bool
}

// NewLogger creates a new debug logger.
func NewLogger(ctx context.Context, workingDir string) (*Logger, error) {
	manager := NewManager()

	// Check if debug is enabled for this directory
	enabled, _ := manager.IsEnabled(ctx, workingDir)

	if !enabled {
		return &Logger{enabled: false}, nil
	}

	// Get log file path
	logPath := GetLogFilePath(workingDir)

	// Open log file in append mode
	// #nosec G304 - logPath is computed from config
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		// If we can't open the file, create a disabled logger
		return &Logger{enabled: false}, fmt.Errorf("open log file: %w", err)
	}

	return &Logger{
		file:     file,
		filePath: logPath,
		enabled:  true,
	}, nil
}

// Log writes a debug message if logging is enabled.
func (l *Logger) Log(format string, args ...any) {
	if !l.enabled || l.file == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, args...)

	// Write to file with timestamp
	_, _ = fmt.Fprintf(l.file, "[%s] %s\n", timestamp, message)
}

// LogSection writes a section header to the log.
func (l *Logger) LogSection(title string) {
	if !l.enabled || l.file == nil {
		return
	}

	l.Log("========== %s ==========", title)
}

// LogError writes an error to the log.
func (l *Logger) LogError(err error, context string) {
	if !l.enabled || l.file == nil {
		return
	}

	if err == nil {
		return
	}

	l.Log("ERROR in %s: %v", context, err)
}

// LogCommand logs a command execution.
func (l *Logger) LogCommand(cmd string, args []string, workDir string) {
	if !l.enabled || l.file == nil {
		return
	}

	l.Log("Executing command: %s", cmd)
	if len(args) > 0 {
		l.Log("  Args: %v", args)
	}
	l.Log("  Working dir: %s", workDir)
}

// LogDiscovery logs command discovery results.
func (l *Logger) LogDiscovery(commandType string, result string, workDir string) {
	if !l.enabled || l.file == nil {
		return
	}

	l.Log("Discovery for %s in %s", commandType, workDir)
	if result != "" {
		l.Log("  Found: %s", result)
	} else {
		l.Log("  Not found")
	}
}

// Close closes the log file.
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	err := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("close log file: %w", err)
	}
	return nil
}

// IsEnabled returns whether debug logging is enabled.
func (l *Logger) IsEnabled() bool {
	return l.enabled
}
