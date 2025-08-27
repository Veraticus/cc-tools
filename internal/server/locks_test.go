package server

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSimpleLockManager_Acquire(t *testing.T) {
	manager := NewSimpleLockManager()

	tests := []struct {
		name           string
		key            string
		holder         string
		expectAcquired bool
	}{
		{
			name:           "first acquisition",
			key:            "resource1",
			holder:         "holder1",
			expectAcquired: true,
		},
		{
			name:           "different resource",
			key:            "resource2",
			holder:         "holder1",
			expectAcquired: true,
		},
		{
			name:           "already locked resource",
			key:            "resource1",
			holder:         "holder2",
			expectAcquired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acquired := manager.Acquire(tt.key, tt.holder)
			if acquired != tt.expectAcquired {
				t.Errorf("Expected acquired=%v, got %v", tt.expectAcquired, acquired)
			}
		})
	}

	// Verify locks are held
	if len(manager.locks) != 2 {
		t.Errorf("Expected 2 locks held, got %d", len(manager.locks))
	}

	// Check specific locks
	if lock, exists := manager.locks["resource1"]; !exists || lock.Holder != "holder1" {
		t.Error("resource1 should be locked by holder1")
	}

	if lock, exists := manager.locks["resource2"]; !exists || lock.Holder != "holder1" {
		t.Error("resource2 should be locked by holder1")
	}
}

func TestSimpleLockManager_Release(t *testing.T) {
	manager := NewSimpleLockManager()

	// Acquire some locks
	manager.Acquire("resource1", "holder1")
	manager.Acquire("resource2", "holder2")

	// Release resource1
	manager.Release("resource1")

	// Verify resource1 is released
	if _, exists := manager.locks["resource1"]; exists {
		t.Error("resource1 should be released")
	}

	// Verify resource2 is still locked
	if _, exists := manager.locks["resource2"]; !exists {
		t.Error("resource2 should still be locked")
	}

	// Try to acquire resource1 again
	if !manager.Acquire("resource1", "holder3") {
		t.Error("Should be able to acquire released resource")
	}

	// Release non-existent lock should not panic
	manager.Release("non-existent")
}

func TestSimpleLockManager_ConcurrentAccess(t *testing.T) {
	manager := NewSimpleLockManager()
	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	successCounts := make(map[int]int)
	var countMu sync.Mutex

	// Multiple goroutines trying to acquire the same lock
	for i := range numGoroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			successCount := 0

			for range numOperations {
				if manager.Acquire("shared-resource", string(rune(id))) {
					successCount++
					// Hold lock briefly
					time.Sleep(time.Microsecond)
					manager.Release("shared-resource")
				}
				// Brief pause between attempts
				time.Sleep(time.Microsecond)
			}

			countMu.Lock()
			successCounts[id] = successCount
			countMu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify that all operations succeeded across all goroutines
	totalSuccess := 0
	for _, count := range successCounts {
		totalSuccess += count
	}

	// Some goroutines should have acquired the lock
	if totalSuccess == 0 {
		t.Error("No goroutine acquired the lock")
	}

	// Lock should be released at the end
	if len(manager.locks) != 0 {
		t.Errorf("Expected all locks to be released, but %d locks remain", len(manager.locks))
	}
}

func TestSimpleLockManager_MultipleResources(t *testing.T) {
	manager := NewSimpleLockManager()

	// Acquire locks on different resources
	resources := []string{"resource1", "resource2", "resource3"}
	for _, resource := range resources {
		if !manager.Acquire(resource, "holder") {
			t.Errorf("Failed to acquire lock on %s", resource)
		}
	}

	// Verify all locks are held
	if len(manager.locks) != len(resources) {
		t.Errorf("Expected %d locks, got %d", len(resources), len(manager.locks))
	}

	// Release all locks
	for _, resource := range resources {
		manager.Release(resource)
	}

	// Verify all locks are released
	if len(manager.locks) != 0 {
		t.Errorf("Expected 0 locks after release, got %d", len(manager.locks))
	}
}

func TestSimpleLockManager_LockTimeout(t *testing.T) {
	manager := NewSimpleLockManager()

	// Acquire a lock
	if !manager.Acquire("resource", "holder1") {
		t.Fatal("Failed to acquire initial lock")
	}

	// Store the lock time
	initialLock := manager.locks["resource"]
	initialTime := initialLock.AcquiredAt

	// Try to acquire again (should fail)
	if manager.Acquire("resource", "holder2") {
		t.Error("Should not be able to acquire locked resource")
	}

	// Verify lock time hasn't changed
	currentLock := manager.locks["resource"]
	if currentLock.AcquiredAt != initialTime {
		t.Error("Lock time should not change on failed acquisition")
	}

	// Verify holder hasn't changed
	if currentLock.Holder != "holder1" {
		t.Errorf("Lock holder changed from holder1 to %s", currentLock.Holder)
	}
}

func TestStandardLogger_Printf(t *testing.T) {
	var buf bytes.Buffer
	// Create a test logger that writes to our buffer
	testLogger := slog.New(slog.NewTextHandler(&buf, nil))
	logger := &StandardLogger{logger: testLogger}

	tests := []struct {
		format string
		args   []any
		expect string
	}{
		{
			format: "Test message %s",
			args:   []any{"hello"},
			expect: "Test message hello",
		},
		{
			format: "Number: %d, String: %s",
			args:   []any{42, "test"},
			expect: "Number: 42, String: test",
		},
		{
			format: "No args",
			args:   []any{},
			expect: "No args",
		},
	}

	for _, tt := range tests {
		buf.Reset()
		logger.Printf(tt.format, tt.args...)
		output := buf.String()

		if !strings.Contains(output, tt.expect) {
			t.Errorf("Expected output to contain %q, got %q", tt.expect, output)
		}
	}
}

func TestStandardLogger_Println(t *testing.T) {
	var buf bytes.Buffer
	// Create a test logger that writes to our buffer
	testLogger := slog.New(slog.NewTextHandler(&buf, nil))
	logger := &StandardLogger{logger: testLogger}

	tests := []struct {
		args   []any
		expect string
	}{
		{
			args:   []any{"Test", "message"},
			expect: "Test message",
		},
		{
			args:   []any{"Single"},
			expect: "Single",
		},
		{
			args:   []any{42, "mixed", true},
			expect: "42 mixed true",
		},
		{
			args:   []any{},
			expect: "",
		},
	}

	for _, tt := range tests {
		buf.Reset()
		logger.Println(tt.args...)
		output := buf.String()

		if tt.expect != "" && !strings.Contains(output, tt.expect) {
			t.Errorf("Expected output to contain %q, got %q", tt.expect, output)
		}
	}
}

func TestStandardLogger_ConcurrentUse(t *testing.T) {
	var buf bytes.Buffer
	// Create a test logger that writes to our buffer
	testLogger := slog.New(slog.NewTextHandler(&buf, nil))
	logger := &StandardLogger{logger: testLogger}

	var wg sync.WaitGroup
	const numGoroutines = 10
	const numLogs = 10

	for i := range numGoroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range numLogs {
				logger.Printf("Goroutine %d, log %d", id, j)
				logger.Println("Line from", id)
			}
		}(i)
	}

	wg.Wait()

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	expectedLines := numGoroutines * numLogs * 2 // Printf and Println for each iteration
	if len(lines) != expectedLines {
		t.Errorf("Expected %d log lines, got %d", expectedLines, len(lines))
	}
}
