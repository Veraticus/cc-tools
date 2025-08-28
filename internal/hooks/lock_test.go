package hooks

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestLockManagerWithMocks(t *testing.T) {
	t.Run("successful lock acquisition", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil // Successfully created exclusive file
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Expected to acquire lock")
		}
	})

	t.Run("lock held by running process", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return fmt.Errorf("file exists") // Can't create exclusive - file exists
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("12345\n"), nil // Lock file with PID
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(pid int) bool {
			return pid == 12345 // Process 12345 is running
		}

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if acquired {
			t.Fatal("Should not acquire lock when another process holds it")
		}
	})

	t.Run("lock held by dead process", func(t *testing.T) {
		testDeps := createTestDependencies()

		var createExclusiveCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			createExclusiveCallCount++
			if createExclusiveCallCount == 1 {
				return fmt.Errorf("file exists") // First attempt fails
			}
			return nil // Second attempt succeeds after removing stale lock
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("12345\n"), nil // Lock file with PID
		}
		testDeps.MockFS.removeFunc = func(_ string) error {
			return nil // Successfully remove stale lock
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool {
			return false // Process 12345 is not running
		}
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock when holding process is dead")
		}
		if createExclusiveCallCount != 2 {
			t.Errorf("Expected 2 createExclusive calls, got %d", createExclusiveCallCount)
		}
	})

	t.Run("respects cooldown period", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return fmt.Errorf("file exists") // Can't create exclusive
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			// Lock file with empty PID and recent timestamp
			return []byte("\n1700000099\n"), nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time {
			return time.Unix(1700000100, 0) // 1 second after completion
		}

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies) // 5 second cooldown

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if acquired {
			t.Fatal("Should not acquire lock during cooldown period")
		}
	})

	t.Run("acquires after cooldown expires", func(t *testing.T) {
		testDeps := createTestDependencies()

		var createExclusiveCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			createExclusiveCallCount++
			if createExclusiveCallCount == 1 {
				return fmt.Errorf("file exists") // First attempt fails
			}
			return nil // Second attempt succeeds after removing expired lock
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			// Lock file with empty PID and old timestamp
			return []byte("\n1700000094\n"), nil
		}
		testDeps.MockFS.removeFunc = func(_ string) error {
			return nil // Successfully remove expired lock
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time {
			return time.Unix(1700000100, 0) // 6 seconds after completion
		}

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies) // 5 second cooldown

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock after cooldown expires")
		}
		if createExclusiveCallCount != 2 {
			t.Errorf("Expected 2 createExclusive calls, got %d", createExclusiveCallCount)
		}
	})

	t.Run("release writes timestamp", func(t *testing.T) {
		testDeps := createTestDependencies()

		var writtenData []byte

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.writeFileFunc = func(_ string, data []byte, _ os.FileMode) error {
			writtenData = data
			return nil
		}
		testDeps.MockClock.nowFunc = func() time.Time {
			return time.Unix(1700000200, 0)
		}

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		err := lm.Release()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "\n1700000200\n"
		if string(writtenData) != expected {
			t.Errorf("Expected written data %q, got %q", expected, string(writtenData))
		}
	})

	t.Run("handles write error on acquire", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return fmt.Errorf("permission denied")
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("file not found")
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatal("Should not return error, just fail to acquire")
		}
		if acquired {
			t.Fatal("Should not acquire lock on write failure")
		}
	})

	t.Run("handles malformed lock file", func(t *testing.T) {
		testDeps := createTestDependencies()

		var createExclusiveCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			createExclusiveCallCount++
			if createExclusiveCallCount == 1 {
				return fmt.Errorf("file exists") // First attempt fails
			}
			return nil // Second attempt succeeds after removing malformed lock
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("not-a-number\n"), nil // Malformed PID
		}
		testDeps.MockFS.removeFunc = func(_ string) error {
			return nil // Successfully remove malformed lock
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock with malformed PID")
		}
		if createExclusiveCallCount != 2 {
			t.Errorf("Expected 2 createExclusive calls, got %d", createExclusiveCallCount)
		}
	})

	t.Run("handles malformed timestamp", func(t *testing.T) {
		testDeps := createTestDependencies()

		var createExclusiveCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.createExclusiveFunc = func(_ string, _ []byte, _ os.FileMode) error {
			createExclusiveCallCount++
			if createExclusiveCallCount == 1 {
				return fmt.Errorf("file exists") // First attempt fails
			}
			return nil // Second attempt succeeds after removing malformed lock
		}
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("\nnot-a-timestamp\n"), nil // Malformed timestamp
		}
		testDeps.MockFS.removeFunc = func(_ string) error {
			return nil // Successfully remove malformed lock
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManager("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock with malformed timestamp")
		}
		if createExclusiveCallCount != 2 {
			t.Errorf("Expected 2 createExclusive calls, got %d", createExclusiveCallCount)
		}
	})
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "unix line endings",
			input:    "line1\nline2\nline3",
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "windows line endings",
			input:    "line1\r\nline2\r\nline3",
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "mixed line endings",
			input:    "line1\nline2\r\nline3",
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "empty lines",
			input:    "\n\n",
			expected: []string{"", ""},
		},
		{
			name:     "no newline at end",
			input:    "line1\nline2",
			expected: []string{"line1", "line2"},
		},
		{
			name:     "empty string",
			input:    "",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitLines(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d lines, got %d", len(tt.expected), len(result))
				return
			}
			for i, line := range result {
				if line != tt.expected[i] {
					t.Errorf("Line %d: expected %q, got %q", i, tt.expected[i], line)
				}
			}
		})
	}
}
