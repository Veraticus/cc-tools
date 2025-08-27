package statusline

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRealFilesystemImpact(t *testing.T) {
	// Create a temporary directory with mock git structure
	tmpDir, err := ioutil.TempDir("", "statusline-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake .git directory structure
	gitDir := filepath.Join(tmpDir, ".git")
	os.MkdirAll(gitDir, 0755)

	// Create HEAD file
	headContent := []byte("ref: refs/heads/main")
	ioutil.WriteFile(filepath.Join(gitDir, "HEAD"), headContent, 0644)

	// Create index file (git's staging area)
	ioutil.WriteFile(filepath.Join(gitDir, "index"), []byte("fake index"), 0644)

	// Create kubeconfig
	kubeDir := filepath.Join(tmpDir, ".kube")
	os.MkdirAll(kubeDir, 0755)
	kubeconfig := `
current-context: production-cluster
contexts:
- name: production-cluster
`
	ioutil.WriteFile(filepath.Join(kubeDir, "config"), []byte(kubeconfig), 0644)

	// Create transcript file
	transcript := `{"message": {"usage": {"input_tokens": 50000, "output_tokens": 2000}}}`
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	ioutil.WriteFile(transcriptPath, []byte(transcript), 0644)

	// Setup real filesystem reader
	realFS := &RealFileReader{}

	deps := &Dependencies{
		FileReader:    realFS,
		CommandRunner: &MockCommandRunner{},
		EnvReader: &MockEnvReader{vars: map[string]string{
			"HOME":        tmpDir,
			"AWS_PROFILE": "dev",
			"HOSTNAME":    "testhost",
			"KUBECONFIG":  filepath.Join(kubeDir, "config"),
		}},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	s := New(deps)

	// Prepare input pointing to our temp directory
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
			IsGitRepo: true,
		},
		Workspace: struct {
			ProjectDir string `json:"project_dir"`
			CurrentDir string `json:"current_dir"`
			CWD        string `json:"cwd"`
		}{
			ProjectDir: tmpDir,
		},
		TranscriptPath: transcriptPath,
	}

	jsonData, _ := json.Marshal(input)

	t.Run("real filesystem timing", func(t *testing.T) {
		const runs = 100
		var totalDuration time.Duration

		for i := 0; i < runs; i++ {
			reader := bytes.NewReader(jsonData)
			start := time.Now()
			s.Generate(reader)
			totalDuration += time.Since(start)
		}

		avgDuration := totalDuration / runs
		t.Logf("Real filesystem average time over %d runs: %v", runs, avgDuration)
		t.Logf("  Compared to mock FS: ~57µs")
		t.Logf("  Overhead: ~%v", avgDuration-57*time.Microsecond)
	})

	t.Run("filesystem cache impact", func(t *testing.T) {
		// First run - cold cache
		runtime.GC()
		reader := bytes.NewReader(jsonData)
		start := time.Now()
		s.Generate(reader)
		coldTime := time.Since(start)

		// Subsequent runs - warm cache
		var warmTimes []time.Duration
		for i := 0; i < 10; i++ {
			reader = bytes.NewReader(jsonData)
			start = time.Now()
			s.Generate(reader)
			warmTimes = append(warmTimes, time.Since(start))
		}

		var avgWarm time.Duration
		for _, d := range warmTimes {
			avgWarm += d
		}
		avgWarm /= time.Duration(len(warmTimes))

		t.Logf("Cold cache (first run): %v", coldTime)
		t.Logf("Warm cache (avg of 10): %v", avgWarm)
		t.Logf("Cache benefit: %v faster", coldTime-avgWarm)

		// Note: OS filesystem cache makes subsequent reads much faster
		if avgWarm < coldTime {
			t.Logf("OS FS cache is working: %.1fx speedup", float64(coldTime)/float64(avgWarm))
		}
	})

	t.Run("rapid real filesystem access", func(t *testing.T) {
		// Simulate rapid rendering with real FS
		start := time.Now()
		count := 0

		// Render as fast as possible for 100ms
		deadline := start.Add(100 * time.Millisecond)
		for time.Now().Before(deadline) {
			reader := bytes.NewReader(jsonData)
			s.Generate(reader)
			count++
		}

		elapsed := time.Since(start)
		rate := float64(count) / elapsed.Seconds()

		t.Logf("Real FS rapid rendering:")
		t.Logf("  Renders in %v: %d", elapsed, count)
		t.Logf("  Rate: %.0f renders/sec", rate)
		t.Logf("  Average time: %v", elapsed/time.Duration(count))

		// Check if we're hitting filesystem limits
		if rate < 10000 {
			t.Logf("  Note: Rate suggests filesystem is becoming a bottleneck")
		}
	})
}

// RealFileReader implements FileReader using actual filesystem
type RealFileReader struct{}

func (r *RealFileReader) ReadFile(path string) ([]byte, error) {
	return ioutil.ReadFile(path)
}

func (r *RealFileReader) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (r *RealFileReader) ModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
