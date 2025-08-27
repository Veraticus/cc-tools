package statusline

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func BenchmarkStatuslineRender(b *testing.B) {
	// Setup dependencies
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader: &MockEnvReader{vars: map[string]string{
			"HOME":        "/home/user",
			"AWS_PROFILE": "dev",
			"HOSTNAME":    "testhost",
		}},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	s := CreateStatusline(deps)

	// Prepare JSON input
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
	}

	jsonData, _ := json.Marshal(input)

	b.ResetTimer()
	for range b.N {
		reader := bytes.NewReader(jsonData)
		s.Generate(reader)
	}
}

func TestStatuslineRenderTiming(t *testing.T) {
	// Setup dependencies
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader: &MockEnvReader{vars: map[string]string{
			"HOME":        "/home/user",
			"AWS_PROFILE": "dev",
			"HOSTNAME":    "testhost",
		}},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	s := CreateStatusline(deps)

	// Test different scenarios
	scenarios := []struct {
		name          string
		contextLength int
		hasGit        bool
		hasK8s        bool
		termWidth     int
	}{
		{
			name:          "minimal",
			contextLength: 0,
			hasGit:        false,
			hasK8s:        false,
			termWidth:     100,
		},
		{
			name:          "with context bar",
			contextLength: 50000,
			hasGit:        false,
			hasK8s:        false,
			termWidth:     100,
		},
		{
			name:          "full features",
			contextLength: 75000,
			hasGit:        true,
			hasK8s:        true,
			termWidth:     120,
		},
		{
			name:          "compact mode",
			contextLength: 170000,
			hasGit:        true,
			hasK8s:        true,
			termWidth:     100,
		},
		{
			name:          "wide terminal",
			contextLength: 50000,
			hasGit:        true,
			hasK8s:        false,
			termWidth:     200,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			// Update dependencies for scenario
			deps.TerminalWidth = &MockTerminalWidth{width: sc.termWidth}

			// Prepare input
			input := &Input{
				Model: struct {
					ID          string `json:"id"`
					Provider    string `json:"provider"`
					DisplayName string `json:"display_name"`
				}{
					DisplayName: "Claude 3 Opus",
				},
				Workspace: struct {
					ProjectDir string `json:"project_dir"`
					CurrentDir string `json:"current_dir"`
					CWD        string `json:"cwd"`
				}{
					ProjectDir: "/home/user/project/with/long/path",
				},
			}

			if sc.contextLength > 0 {
				input.Cost.InputTokens = sc.contextLength
				input.Cost.OutputTokens = 2000
			}

			if sc.hasGit {
				input.GitInfo.Branch = "feature/long-branch-name"
				input.GitInfo.IsGitRepo = true
				input.GitInfo.HasModified = true
			}

			if sc.hasK8s {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["KUBECONFIG"] = "/home/user/.kube/config"
				}
			}

			jsonData, _ := json.Marshal(input)

			// Run multiple times and measure
			const runs = 100
			var totalDuration time.Duration

			for range runs {
				reader := bytes.NewReader(jsonData)
				start := time.Now()
				_, err := s.Generate(reader)
				duration := time.Since(start)
				totalDuration += duration

				if err != nil {
					t.Fatalf("Generate failed: %v", err)
				}
			}

			avgDuration := totalDuration / runs
			t.Logf("Average time over %d runs: %v", runs, avgDuration)
			t.Logf("  Per-run: %v", avgDuration)
			t.Logf("  Total: %v", totalDuration)

			// Warn if it's taking too long
			if avgDuration > 1*time.Millisecond {
				t.Logf("  WARNING: Rendering is slower than 1ms")
			}
		})
	}

	// Also do a single detailed timing
	t.Run("detailed single run", func(t *testing.T) {
		input := &Input{
			Model: struct {
				ID          string `json:"id"`
				Provider    string `json:"provider"`
				DisplayName string `json:"display_name"`
			}{
				DisplayName: "Claude 3 Opus Ultra Long Name For Testing",
			},
			Cost: struct {
				TotalCostUSD     float64 `json:"total_cost_usd"`
				InputTokens      int     `json:"input_tokens"`
				OutputTokens     int     `json:"output_tokens"`
				CacheReadTokens  int     `json:"cache_read_input_tokens"`
				CacheWriteTokens int     `json:"cache_creation_input_tokens"`
			}{
				InputTokens:      123456,
				OutputTokens:     7890,
				CacheReadTokens:  10000,
				CacheWriteTokens: 5000,
			},
			GitInfo: struct {
				Branch       string `json:"branch"`
				IsGitRepo    bool   `json:"is_git_repo"`
				HasUntracked bool   `json:"has_untracked"`
				HasModified  bool   `json:"has_modified"`
			}{
				Branch:       "feature/very-long-branch-name-with-many-words",
				IsGitRepo:    true,
				HasUntracked: true,
				HasModified:  true,
			},
			Workspace: struct {
				ProjectDir string `json:"project_dir"`
				CurrentDir string `json:"current_dir"`
				CWD        string `json:"cwd"`
			}{
				ProjectDir: "/very/long/nested/directory/path/that/should/be/truncated",
			},
		}

		jsonData, _ := json.Marshal(input)
		reader := bytes.NewReader(jsonData)

		start := time.Now()
		result, err := s.Generate(reader)
		duration := time.Since(start)

		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}

		t.Logf("Single run time: %v", duration)
		t.Logf("Result length: %d chars", len(result))
		t.Logf("Result width: %d", runewidth.StringWidth(stripAnsi(result)))
	})
}
