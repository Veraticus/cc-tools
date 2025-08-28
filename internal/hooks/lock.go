package hooks

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strconv"
)

const lockFileMode = 0600 // Read/write for owner only

// LockManager handles process locking to prevent concurrent hook execution.
type LockManager struct {
	lockFile      string
	pid           int
	cooldownSecs  int
	cleanupOnExit bool
	deps          *Dependencies
}

// NewLockManager creates a new lock manager for the given workspace.
func NewLockManager(workspaceDir, hookName string, cooldownSecs int, deps *Dependencies) *LockManager {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	// Generate a unique lock file name based on workspace and hook
	hash := sha256.Sum256([]byte(workspaceDir))
	lockFileName := fmt.Sprintf("claude-hook-%s-%x.lock", hookName, hash[:8])
	// Use /tmp directly for lock files as they are temporary runtime state
	lockFile := filepath.Join("/tmp", lockFileName)

	return &LockManager{
		lockFile:      lockFile,
		pid:           deps.Process.GetPID(),
		cooldownSecs:  cooldownSecs,
		cleanupOnExit: true,
		deps:          deps,
	}
}

// isAnotherProcessRunning checks if another process holds the lock.
func (l *LockManager) isAnotherProcessRunning(lines []string) bool {
	if len(lines) < 1 || lines[0] == "" {
		return false
	}

	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return false
	}

	return l.deps.Process.ProcessExists(pid)
}

// isInCooldownPeriod checks if the lock is in cooldown period.
func (l *LockManager) isInCooldownPeriod(lines []string) bool {
	if len(lines) < 2 || lines[1] == "" {
		return false
	}

	completionTime, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil {
		return false
	}

	timeSinceCompletion := l.deps.Clock.Now().Unix() - completionTime
	return timeSinceCompletion < int64(l.cooldownSecs)
}

// TryAcquire attempts to acquire the lock atomically.
// Returns true if lock acquired, false if another process has it or cooldown active.
func (l *LockManager) TryAcquire() (bool, error) {
	// First, try to atomically create the lock file
	// CreateExclusive uses O_EXCL to ensure this fails if the file already exists
	content := fmt.Sprintf("%d\n", l.pid)
	err := l.deps.FS.CreateExclusive(l.lockFile, []byte(content), lockFileMode)
	if err == nil {
		// We created the file atomically!
		return true, nil
	}

	// File exists - check if it's a stale lock or in cooldown
	data, readErr := l.deps.FS.ReadFile(l.lockFile)
	if readErr != nil {
		// Couldn't read the existing file - perhaps it was just deleted
		// Not an error - just couldn't acquire lock
		return false, nil //nolint:nilerr // Intentionally returning nil - not an error condition
	}

	lines := splitLines(string(data))

	// Check if another process is running
	if l.isAnotherProcessRunning(lines) {
		return false, nil
	}

	// Check if in cooldown period
	if l.isInCooldownPeriod(lines) {
		return false, nil
	}

	// Lock is stale - try to remove it and acquire atomically
	if removeErr := l.deps.FS.Remove(l.lockFile); removeErr != nil {
		// Another process might have just acquired it
		// Not an error - just couldn't acquire lock
		return false, nil //nolint:nilerr // Intentionally returning nil - not an error condition
	}

	// Try to create again atomically
	if createErr := l.deps.FS.CreateExclusive(l.lockFile, []byte(content), lockFileMode); createErr != nil {
		// Another process beat us to it
		// Not an error - just couldn't acquire lock
		return false, nil //nolint:nilerr // Intentionally returning nil - not an error condition
	}

	return true, nil
}

// Release releases the lock and starts the cooldown period.
func (l *LockManager) Release() error {
	if !l.cleanupOnExit {
		return nil
	}

	// Write empty PID and completion timestamp
	content := fmt.Sprintf("\n%d\n", l.deps.Clock.Now().Unix())
	if err := l.deps.FS.WriteFile(l.lockFile, []byte(content), lockFileMode); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}
	return nil
}

// splitLines splits a string into lines, handling both \n and \r\n.
func splitLines(s string) []string {
	var lines []string
	var current []byte

	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, string(current))
			current = nil
		} else if s[i] != '\r' {
			current = append(current, s[i])
		}
	}

	if len(current) > 0 {
		lines = append(lines, string(current))
	}

	return lines
}
