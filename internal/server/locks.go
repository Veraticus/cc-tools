package server

import (
	"log/slog"
	"sync"
	"time"
)

// Lock represents a resource lock.
type Lock struct {
	Resource   string
	Holder     string
	AcquiredAt time.Time
}

// SimpleLockManager implements LockManager with in-memory locks.
type SimpleLockManager struct {
	mu    sync.RWMutex
	locks map[string]*Lock
}

// NewSimpleLockManager creates a new lock manager.
func NewSimpleLockManager() *SimpleLockManager {
	return &SimpleLockManager{
		locks: make(map[string]*Lock),
	}
}

// Acquire attempts to acquire a lock for a resource.
func (m *SimpleLockManager) Acquire(key, holder string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.locks[key]; exists {
		return false // Already locked
	}

	m.locks[key] = &Lock{
		Resource:   key,
		Holder:     holder,
		AcquiredAt: time.Now(),
	}
	return true
}

// Release releases a lock for a resource.
func (m *SimpleLockManager) Release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, key)
}

// StandardLogger implements Logger using the standard log package.
type StandardLogger struct {
	logger *slog.Logger
}

// NewStandardLogger creates a new StandardLogger.
func NewStandardLogger() *StandardLogger {
	return &StandardLogger{
		logger: slog.Default(),
	}
}

// Printf formats and prints to the standard logger.
func (l *StandardLogger) Printf(format string, v ...any) {
	l.logger.Info("log message", "format", format, "args", v)
}

// Println prints to the standard logger.
func (l *StandardLogger) Println(v ...any) {
	l.logger.Info("log message", "args", v)
}
