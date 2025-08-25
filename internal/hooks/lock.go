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
func NewLockManager(workspaceDir, hookName string, cooldownSecs int) *LockManager {
	return NewLockManagerWithDeps(workspaceDir, hookName, cooldownSecs, nil)
}

// NewLockManagerWithDeps creates a new lock manager with explicit dependencies.
func NewLockManagerWithDeps(workspaceDir, hookName string, cooldownSecs int, deps *Dependencies) *LockManager {
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

// TryAcquire attempts to acquire the lock.
// Returns true if lock acquired, false if another process has it or cooldown active.
func (l *LockManager) TryAcquire() (bool, error) {
	// Check if lock file exists
	data, err := l.deps.FS.ReadFile(l.lockFile)
	if err == nil { //nolint:nestif // Lock file checking requires nested checks
		// Lock file exists, parse it
		lines := splitLines(string(data))
		if len(lines) >= 1 && lines[0] != "" {
			// Check if PID is still running
			pid, pidErr := strconv.Atoi(lines[0])
			if pidErr == nil && l.deps.Process.ProcessExists(pid) {
				// Another instance is running
				return false, nil
			}
		}

		// Check cooldown period
		if len(lines) >= 2 && lines[1] != "" {
			completionTime, parseErr := strconv.ParseInt(lines[1], 10, 64)
			if parseErr == nil {
				timeSinceCompletion := l.deps.Clock.Now().Unix() - completionTime
				if timeSinceCompletion < int64(l.cooldownSecs) {
					// Still in cooldown period
					return false, nil
				}
			}
		}
	}

	// Write our PID to lock file
	content := fmt.Sprintf("%d\n", l.pid)
	if writeErr := l.deps.FS.WriteFile(l.lockFile, []byte(content), lockFileMode); writeErr != nil {
		return false, fmt.Errorf("writing lock file: %w", writeErr)
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
