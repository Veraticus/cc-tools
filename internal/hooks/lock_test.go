package hooks

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestLockManagerWithDeps(t *testing.T) {
	t.Run("successful lock acquisition", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("file not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

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
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("12345\n"), nil // Lock file with PID
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(pid int) bool {
			return pid == 12345 // Process 12345 is running
		}

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

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

		var writeCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("12345\n"), nil // Lock file with PID
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			writeCallCount++
			return nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockProcess.processExistsFunc = func(_ int) bool {
			return false // Process 12345 is not running
		}
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock when holding process is dead")
		}
		if writeCallCount != 1 {
			t.Errorf("Expected 1 write call, got %d", writeCallCount)
		}
	})

	t.Run("respects cooldown period", func(t *testing.T) {
		testDeps := createTestDependencies()

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			// Lock file with empty PID and recent timestamp
			return []byte("\n1700000099\n"), nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time {
			return time.Unix(1700000100, 0) // 1 second after completion
		}

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies) // 5 second cooldown

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

		var writeCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			// Lock file with empty PID and old timestamp
			return []byte("\n1700000094\n"), nil
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			writeCallCount++
			return nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time {
			return time.Unix(1700000100, 0) // 6 seconds after completion
		}

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies) // 5 second cooldown

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock after cooldown expires")
		}
		if writeCallCount != 1 {
			t.Errorf("Expected 1 write call, got %d", writeCallCount)
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

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

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
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return nil, fmt.Errorf("file not found")
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			return fmt.Errorf("permission denied")
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err == nil {
			t.Fatal("Expected error on write failure")
		}
		if acquired {
			t.Fatal("Should not acquire lock on write failure")
		}
	})

	t.Run("handles malformed lock file", func(t *testing.T) {
		testDeps := createTestDependencies()

		var writeCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("not-a-number\n"), nil // Malformed PID
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			writeCallCount++
			return nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock with malformed PID")
		}
		if writeCallCount != 1 {
			t.Errorf("Expected 1 write call, got %d", writeCallCount)
		}
	})

	t.Run("handles malformed timestamp", func(t *testing.T) {
		testDeps := createTestDependencies()

		var writeCallCount int

		// Setup mocks
		testDeps.MockFS.tempDirFunc = func() string { return "/tmp" }
		testDeps.MockFS.readFileFunc = func(_ string) ([]byte, error) {
			return []byte("\nnot-a-timestamp\n"), nil // Malformed timestamp
		}
		testDeps.MockFS.writeFileFunc = func(_ string, _ []byte, _ os.FileMode) error {
			writeCallCount++
			return nil
		}
		testDeps.MockProcess.getPIDFunc = func() int { return 99999 }
		testDeps.MockClock.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }

		lm := NewLockManagerWithDeps("/project", "test", 5, testDeps.Dependencies)

		acquired, err := lm.TryAcquire()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !acquired {
			t.Fatal("Should acquire lock with malformed timestamp")
		}
		if writeCallCount != 1 {
			t.Errorf("Expected 1 write call, got %d", writeCallCount)
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
