package statusline

import (
	"bytes"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatuslineStress(t *testing.T) {
	// Setup dependencies with mock filesystem
	fileReadCount := int64(0)
	fileExistsCount := int64(0)

	mockReader := &MockFileReader{}
	// Wrap the mock to count operations
	countingReader := &countingFileReader{
		wrapped:     mockReader,
		readCount:   &fileReadCount,
		existsCount: &fileExistsCount,
	}

	deps := &Dependencies{
		FileReader:    countingReader,
		CommandRunner: &MockCommandRunner{},
		EnvReader: &MockEnvReader{vars: map[string]string{
			"HOME":        "/home/user",
			"AWS_PROFILE": "dev",
			"HOSTNAME":    "testhost",
		}},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	s := CreateStatusline(deps)

	// Prepare a typical input
	input := &Input{
		Model: struct {
			ID          string `json:"id"`
			Provider    string `json:"provider"`
			DisplayName string `json:"display_name"`
		}{
			DisplayName: "Claude 3 Opus",
		},
		Cost: struct {
			TotalCostUSD     float64 `json:"total_cost_usd"`
			InputTokens      int     `json:"input_tokens"`
			OutputTokens     int     `json:"output_tokens"`
			CacheReadTokens  int     `json:"cache_read_input_tokens"`
			CacheWriteTokens int     `json:"cache_creation_input_tokens"`
		}{
			InputTokens:  50000,
			OutputTokens: 2000,
		},
		GitInfo: struct {
			Branch       string `json:"branch"`
			IsGitRepo    bool   `json:"is_git_repo"`
			HasUntracked bool   `json:"has_untracked"`
			HasModified  bool   `json:"has_modified"`
		}{
			Branch:      "main",
			IsGitRepo:   true,
			HasModified: true,
		},
		Workspace: struct {
			ProjectDir string `json:"project_dir"`
			CurrentDir string `json:"current_dir"`
			CWD        string `json:"cwd"`
		}{
			ProjectDir: "/home/user/project",
		},
		TranscriptPath: "/tmp/transcript.jsonl",
	}

	jsonData, _ := json.Marshal(input)

	t.Run("filesystem operations per render", func(t *testing.T) {
		// Reset counters
		atomic.StoreInt64(&fileReadCount, 0)
		atomic.StoreInt64(&fileExistsCount, 0)

		// Single render
		reader := bytes.NewReader(jsonData)
		s.Generate(reader)

		reads := atomic.LoadInt64(&fileReadCount)
		exists := atomic.LoadInt64(&fileExistsCount)

		t.Logf("Filesystem operations per render:")
		t.Logf("  File reads: %d", reads)
		t.Logf("  File exists checks: %d", exists)
		t.Logf("  Total FS operations: %d", reads+exists)
	})

	t.Run("rapid continuous rendering", func(t *testing.T) {
		// Reset counters
		atomic.StoreInt64(&fileReadCount, 0)
		atomic.StoreInt64(&fileExistsCount, 0)

		// Simulate rapid rendering (e.g., every 100ms for 1 second)
		duration := 1 * time.Second
		interval := 100 * time.Millisecond
		renders := int(duration / interval)

		start := time.Now()
		for range renders {
			reader := bytes.NewReader(jsonData)
			s.Generate(reader)
			time.Sleep(interval)
		}
		elapsed := time.Since(start)

		reads := atomic.LoadInt64(&fileReadCount)
		exists := atomic.LoadInt64(&fileExistsCount)

		t.Logf("Rapid rendering (every %v for %v):", interval, duration)
		t.Logf("  Total renders: %d", renders)
		t.Logf("  Total file reads: %d (%.1f per render)", reads, float64(reads)/float64(renders))
		t.Logf("  Total exists checks: %d (%.1f per render)", exists, float64(exists)/float64(renders))
		t.Logf("  Total FS operations: %d", reads+exists)
		t.Logf("  Time elapsed: %v", elapsed)
	})

	t.Run("concurrent rendering stress", func(t *testing.T) {
		// Reset counters
		atomic.StoreInt64(&fileReadCount, 0)
		atomic.StoreInt64(&fileExistsCount, 0)

		// Simulate multiple concurrent renders
		concurrency := 10
		rendersPerGoroutine := 100

		var wg sync.WaitGroup
		start := time.Now()

		for range concurrency {
			wg.Add(1)
			go func() {
				defer wg.Done()
				localS := CreateStatusline(deps) // Each goroutine gets its own instance
				for range rendersPerGoroutine {
					reader := bytes.NewReader(jsonData)
					localS.Generate(reader)
				}
			}()
		}

		wg.Wait()
		elapsed := time.Since(start)

		totalRenders := concurrency * rendersPerGoroutine
		reads := atomic.LoadInt64(&fileReadCount)
		exists := atomic.LoadInt64(&fileExistsCount)

		t.Logf("Concurrent stress test:")
		t.Logf("  Goroutines: %d", concurrency)
		t.Logf("  Total renders: %d", totalRenders)
		t.Logf("  Total time: %v", elapsed)
		t.Logf("  Throughput: %.0f renders/sec", float64(totalRenders)/elapsed.Seconds())
		t.Logf("  Total file reads: %d (%.1f per render)", reads, float64(reads)/float64(totalRenders))
		t.Logf("  Total exists checks: %d (%.1f per render)", exists, float64(exists)/float64(totalRenders))
		t.Logf("  FS ops/sec: %.0f", float64(reads+exists)/elapsed.Seconds())
	})

	t.Run("CPU usage measurement", func(t *testing.T) {
		// Measure CPU usage during sustained rendering
		runtime.GC() // Clean slate

		var memStatsBefore, memStatsAfter runtime.MemStats
		runtime.ReadMemStats(&memStatsBefore)

		// Render continuously for a short period
		stopChan := make(chan bool)
		renderCount := int64(0)

		go func() {
			for {
				select {
				case <-stopChan:
					return
				default:
					reader := bytes.NewReader(jsonData)
					s.Generate(reader)
					atomic.AddInt64(&renderCount, 1)
				}
			}
		}()

		// Let it run for 100ms
		time.Sleep(100 * time.Millisecond)
		close(stopChan)

		runtime.ReadMemStats(&memStatsAfter)
		renders := atomic.LoadInt64(&renderCount)

		t.Logf("Sustained rendering for 100ms:")
		t.Logf("  Renders completed: %d", renders)
		t.Logf("  Rate: ~%.0f renders/sec", float64(renders)*10)
		t.Logf("  Memory allocated: %d KB", (memStatsAfter.Alloc-memStatsBefore.Alloc)/1024)
		t.Logf("  GC runs: %d", memStatsAfter.NumGC-memStatsBefore.NumGC)
	})
}

// countingFileReader wraps a FileReader and counts operations.
type countingFileReader struct {
	wrapped     FileReader
	readCount   *int64
	existsCount *int64
}

func (c *countingFileReader) ReadFile(path string) ([]byte, error) {
	atomic.AddInt64(c.readCount, 1)
	return c.wrapped.ReadFile(path)
}

func (c *countingFileReader) Exists(path string) bool {
	atomic.AddInt64(c.existsCount, 1)
	return c.wrapped.Exists(path)
}

func (c *countingFileReader) ModTime(path string) (time.Time, error) {
	// Count as a read since it accesses file metadata
	atomic.AddInt64(c.readCount, 1)
	return c.wrapped.ModTime(path)
}
