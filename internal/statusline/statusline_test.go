package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// MockFileReader implements FileReader for testing.
type MockFileReader struct {
	files map[string][]byte
}

func NewMockFileReader() *MockFileReader {
	return &MockFileReader{
		files: make(map[string][]byte),
	}
}

func (m *MockFileReader) ReadFile(path string) ([]byte, error) {
	if content, ok := m.files[path]; ok {
		return content, nil
	}
	return nil, nil
}

func (m *MockFileReader) Exists(path string) bool {
	_, ok := m.files[path]
	return ok
}

// MockCommandRunner implements CommandRunner for testing.
type MockCommandRunner struct {
	responses map[string][]byte
	errors    map[string]error
}

func NewMockCommandRunner() *MockCommandRunner {
	return &MockCommandRunner{
		responses: make(map[string][]byte),
		errors:    make(map[string]error),
	}
}

func (m *MockCommandRunner) Run(command string, args ...string) ([]byte, error) {
	key := command
	if len(args) > 0 {
		key = command + " " + strings.Join(args, " ")
	}

	// Check for configured error first
	if m.errors != nil {
		if err, hasError := m.errors[key]; hasError {
			return nil, err
		}
	}

	// Then check for response
	if response, ok := m.responses[key]; ok {
		return response, nil
	}

	// Default: return empty with no error
	return []byte(""), nil
}

// RunContext ignores ctx and delegates to Run: none of the existing
// MockCommandRunner-based tests need per-call context/timeout
// behavior, and Run's key-based response/error lookup already covers
// what they configure.
func (m *MockCommandRunner) RunContext(_ context.Context, command string, args ...string) ([]byte, error) {
	return m.Run(command, args...)
}

// MockEnvReader implements EnvReader for testing.
type MockEnvReader struct {
	vars map[string]string
}

func NewMockEnvReader() *MockEnvReader {
	return &MockEnvReader{
		vars: make(map[string]string),
	}
}

func (m *MockEnvReader) Get(key string) string {
	return m.vars[key]
}

// MockTerminalWidth implements TerminalWidth for testing.
type MockTerminalWidth struct {
	width int
}

func (m *MockTerminalWidth) GetWidth() int {
	if m.width == 0 {
		return 210
	}
	return m.width
}

func TestCachedData_UsedPercentage(t *testing.T) {
	data := &CachedData{
		UsedPercentage: 67.5,
	}

	if data.UsedPercentage != 67.5 {
		t.Errorf("Expected UsedPercentage 67.5, got %f", data.UsedPercentage)
	}
}

func TestInput_ContextWindow(t *testing.T) {
	jsonInput := `{
		"model": {"id": "claude-sonnet-4-5", "display_name": "Claude"},
		"context_window": {
			"used_percentage": 45.5,
			"context_window_size": 200000
		}
	}`

	var input Input
	err := json.Unmarshal([]byte(jsonInput), &input)
	if err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if input.ContextWindow.UsedPercentage != 45.5 {
		t.Errorf("Expected UsedPercentage 45.5, got %f", input.ContextWindow.UsedPercentage)
	}
	if input.ContextWindow.ContextWindowSize != 200000 {
		t.Errorf("Expected ContextWindowSize 200000, got %d", input.ContextWindow.ContextWindowSize)
	}
}

func TestComputeData_InputColumnsOverrideTerminalWidth(t *testing.T) {
	deps := &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 210},
	}
	sl := CreateStatusline(deps)
	if err := sl.parseInput(strings.NewReader(`{"cwd":"/tmp","columns":80}`)); err != nil {
		t.Fatalf("parseInput() error = %v", err)
	}

	if got := sl.computeData("/tmp").TermWidth; got != 80 {
		t.Fatalf("TermWidth = %d, want caller-supplied width 80", got)
	}
}

// assertFullPayload checks every new stdin field parsed from the
// full-payload case of TestInputParse_NewStdinFields.
func assertFullPayload(t *testing.T, in Input) {
	t.Helper()
	if in.RateLimits == nil {
		t.Fatal("RateLimits = nil, want non-nil")
	}
	assertRateLimitWindow(t, "FiveHour", in.RateLimits.FiveHour, 34.5, 1751700000)
	assertRateLimitWindow(t, "SevenDay", in.RateLimits.SevenDay, 61.2, 1752100000)
	if in.Cost.TotalCostUSD != 1.25 {
		t.Errorf("Cost.TotalCostUSD = %f, want 1.25", in.Cost.TotalCostUSD)
	}
	if in.Cost.TotalLinesAdded != 120 {
		t.Errorf("Cost.TotalLinesAdded = %d, want 120", in.Cost.TotalLinesAdded)
	}
	if in.Cost.TotalLinesRemoved != 30 {
		t.Errorf("Cost.TotalLinesRemoved = %d, want 30", in.Cost.TotalLinesRemoved)
	}
	if in.Effort == nil {
		t.Fatal("Effort = nil, want non-nil")
	}
	if in.Effort.Level != "high" {
		t.Errorf("Effort.Level = %q, want %q", in.Effort.Level, "high")
	}
	if in.PR == nil {
		t.Fatal("PR = nil, want non-nil")
	}
	if in.PR.Number != 42 {
		t.Errorf("PR.Number = %d, want 42", in.PR.Number)
	}
	if in.PR.URL != "https://github.com/o/r/pull/42" {
		t.Errorf("PR.URL = %q, want %q", in.PR.URL, "https://github.com/o/r/pull/42")
	}
	if in.PR.ReviewState != "approved" {
		t.Errorf("PR.ReviewState = %q, want %q", in.PR.ReviewState, "approved")
	}
}

// assertRateLimitWindow checks one parsed rate-limit window against the
// expected usage percentage and reset timestamp.
func assertRateLimitWindow(t *testing.T, name string, w *RateLimitWindow, wantPct float64, wantResets int64) {
	t.Helper()
	if w == nil {
		t.Fatalf("RateLimits.%s = nil, want non-nil", name)
	}
	if w.UsedPercentage != wantPct {
		t.Errorf("%s.UsedPercentage = %f, want %f", name, w.UsedPercentage, wantPct)
	}
	if w.ResetsAt != wantResets {
		t.Errorf("%s.ResetsAt = %d, want %d", name, w.ResetsAt, wantResets)
	}
}

func TestInputParse_NewStdinFields(t *testing.T) {
	tests := []struct {
		name  string
		json  string
		check func(*testing.T, Input)
	}{
		{
			name: "full payload",
			json: `{
				"rate_limits": {
					"five_hour": {"used_percentage": 34.5, "resets_at": 1751700000},
					"seven_day": {"used_percentage": 61.2, "resets_at": 1752100000}
				},
				"cost": {"total_cost_usd": 1.25, "total_lines_added": 120, "total_lines_removed": 30},
				"effort": {"level": "high"},
				"pr": {"number": 42, "url": "https://github.com/o/r/pull/42", "review_state": "approved"}
			}`,
			check: assertFullPayload,
		},
		{
			name: "empty payload",
			json: `{}`,
			check: func(t *testing.T, in Input) {
				t.Helper()
				if in.RateLimits != nil {
					t.Errorf("RateLimits = %+v, want nil", in.RateLimits)
				}
				if in.Effort != nil {
					t.Errorf("Effort = %+v, want nil", in.Effort)
				}
				if in.PR != nil {
					t.Errorf("PR = %+v, want nil", in.PR)
				}
				if in.Cost != (CostInput{}) {
					t.Errorf("Cost = %+v, want zero value", in.Cost)
				}
			},
		},
		{
			name: "rate_limits with only five_hour",
			json: `{"rate_limits": {"five_hour": {"used_percentage": 12, "resets_at": 1751700000}}}`,
			check: func(t *testing.T, in Input) {
				t.Helper()
				if in.RateLimits == nil {
					t.Fatal("RateLimits = nil, want non-nil")
				}
				if in.RateLimits.FiveHour == nil {
					t.Fatal("RateLimits.FiveHour = nil, want non-nil")
				}
				if in.RateLimits.FiveHour.UsedPercentage != 12 {
					t.Errorf("FiveHour.UsedPercentage = %f, want 12", in.RateLimits.FiveHour.UsedPercentage)
				}
				if in.RateLimits.SevenDay != nil {
					t.Errorf("RateLimits.SevenDay = %+v, want nil", in.RateLimits.SevenDay)
				}
			},
		},
		{
			name: "pr without review_state",
			json: `{"pr": {"number": 7, "url": "https://github.com/o/r/pull/7"}}`,
			check: func(t *testing.T, in Input) {
				t.Helper()
				if in.PR == nil {
					t.Fatal("PR = nil, want non-nil")
				}
				if in.PR.Number != 7 {
					t.Errorf("PR.Number = %d, want 7", in.PR.Number)
				}
				if in.PR.ReviewState != "" {
					t.Errorf("PR.ReviewState = %q, want empty", in.PR.ReviewState)
				}
			},
		},
		{
			name: "cost alone",
			json: `{"cost": {"total_cost_usd": 0.42, "total_lines_added": 5, "total_lines_removed": 1}}`,
			check: func(t *testing.T, in Input) {
				t.Helper()
				if in.Cost.TotalCostUSD != 0.42 {
					t.Errorf("Cost.TotalCostUSD = %f, want 0.42", in.Cost.TotalCostUSD)
				}
				if in.Cost.TotalLinesAdded != 5 {
					t.Errorf("Cost.TotalLinesAdded = %d, want 5", in.Cost.TotalLinesAdded)
				}
				if in.Cost.TotalLinesRemoved != 1 {
					t.Errorf("Cost.TotalLinesRemoved = %d, want 1", in.Cost.TotalLinesRemoved)
				}
				if in.RateLimits != nil || in.Effort != nil || in.PR != nil {
					t.Errorf("RateLimits/Effort/PR = %+v/%+v/%+v, want all nil", in.RateLimits, in.Effort, in.PR)
				}
			},
		},
		{
			name: "effort alone",
			json: `{"effort": {"level": "max"}}`,
			check: func(t *testing.T, in Input) {
				t.Helper()
				if in.Effort == nil {
					t.Fatal("Effort = nil, want non-nil")
				}
				if in.Effort.Level != "max" {
					t.Errorf("Effort.Level = %q, want %q", in.Effort.Level, "max")
				}
				if in.RateLimits != nil || in.PR != nil {
					t.Errorf("RateLimits/PR = %+v/%+v, want both nil", in.RateLimits, in.PR)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input Input
			if err := json.Unmarshal([]byte(tt.json), &input); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			tt.check(t, input)
		})
	}
}

func TestComputeData_CarriesNewStdinFields(t *testing.T) {
	deps := &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 210},
	}
	sl := CreateStatusline(deps)

	jsonInput := `{
		"model": {"display_name": "Fable"},
		"workspace": {"project_dir": "/tmp/project"},
		"rate_limits": {"five_hour": {"used_percentage": 50, "resets_at": 1751700000}},
		"cost": {"total_cost_usd": 2.5, "total_lines_added": 10, "total_lines_removed": 3},
		"effort": {"level": "xhigh"},
		"pr": {"number": 9, "url": "https://github.com/o/r/pull/9", "review_state": "pending"}
	}`
	if err := sl.parseInput(bytes.NewReader([]byte(jsonInput))); err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	data := sl.computeData("/tmp/project")

	if data.RateLimits == nil || data.RateLimits.FiveHour == nil {
		t.Fatalf("RateLimits not carried into CachedData: %+v", data.RateLimits)
	}
	if data.RateLimits.FiveHour.UsedPercentage != 50 {
		t.Errorf("RateLimits.FiveHour.UsedPercentage = %f, want 50", data.RateLimits.FiveHour.UsedPercentage)
	}
	if data.Cost.TotalCostUSD != 2.5 {
		t.Errorf("Cost.TotalCostUSD = %f, want 2.5", data.Cost.TotalCostUSD)
	}
	if data.Effort == nil || data.Effort.Level != "xhigh" {
		t.Errorf("Effort = %+v, want level xhigh", data.Effort)
	}
	if data.PR == nil || data.PR.ReviewState != "pending" {
		t.Errorf("PR = %+v, want review_state pending", data.PR)
	}
}

func TestEffortSuffixInModelChip(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		contains    []string
		notContains []string
	}{
		{
			name: "effort high renders ×H suffix",
			input: `{
				"model": {"display_name": "Fable"},
				"workspace": {"project_dir": "/tmp/project"},
				"effort": {"level": "high"}
			}`,
			contains: []string{"Fable ×H"},
		},
		{
			name: "effort absent renders no suffix",
			input: `{
				"model": {"display_name": "Fable"},
				"workspace": {"project_dir": "/tmp/project"}
			}`,
			contains:    []string{"Fable"},
			notContains: []string{"×"},
		},
		{
			name: "effort max renders ×MAX suffix",
			input: `{
				"model": {"display_name": "Fable"},
				"workspace": {"project_dir": "/tmp/project"},
				"effort": {"level": "max"}
			}`,
			contains: []string{"Fable ×MAX"},
		},
		{
			name: "unknown effort level renders no suffix",
			input: `{
				"model": {"display_name": "Fable"},
				"workspace": {"project_dir": "/tmp/project"},
				"effort": {"level": "turbo"}
			}`,
			contains:    []string{"Fable"},
			notContains: []string{"×"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Dependencies{
				FileReader:    NewMockFileReader(),
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     NewMockEnvReader(),
				TerminalWidth: &MockTerminalWidth{width: 210},
			}
			sl := CreateStatusline(deps)

			output, err := sl.Generate(bytes.NewReader([]byte(tt.input)))
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Errorf("Output doesn't contain %q\nGot: %q", want, output)
				}
			}
			for _, unwanted := range tt.notContains {
				if strings.Contains(output, unwanted) {
					t.Errorf("Output contains %q but shouldn't\nGot: %q", unwanted, output)
				}
			}
		})
	}
}

func TestGetCurrentDir_TopLevelCWD(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "top-level cwd only",
			input: `{"cwd": "/top/level/dir"}`,
			want:  "/top/level/dir",
		},
		{
			name:  "workspace project_dir wins over top-level cwd",
			input: `{"cwd": "/top/level/dir", "workspace": {"project_dir": "/ws/project"}}`,
			want:  "/ws/project",
		},
		{
			name:  "workspace cwd wins over top-level cwd",
			input: `{"cwd": "/top/level/dir", "workspace": {"cwd": "/ws/cwd"}}`,
			want:  "/ws/cwd",
		},
		{
			name:  "empty payload falls back to ~",
			input: `{}`,
			want:  "~",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sl := CreateStatusline(&Dependencies{
				FileReader:    NewMockFileReader(),
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     NewMockEnvReader(),
				TerminalWidth: &MockTerminalWidth{width: 210},
			})
			if err := sl.parseInput(bytes.NewReader([]byte(tt.input))); err != nil {
				t.Fatalf("parseInput: %v", err)
			}
			if got := sl.getCurrentDir(); got != tt.want {
				t.Errorf("getCurrentDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatuslineGenerate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		setup    func(*Dependencies)
		contains []string
	}{
		{
			name: "basic statusline with model",
			input: `{
				"model": {"display_name": "Claude 3 Opus"},
				"workspace": {"project_dir": "/home/user/project"}
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
				// Also set actual HOME env for formatPath
				t.Setenv("HOME", "/home/user")
			},
			contains: []string{
				"~/project",     // Directory
				"Claude 3 Opus", // Model name
			},
		},
		{
			name: "statusline with git info",
			input: `{
				"model": {"display_name": "Claude"},
				"workspace": {"project_dir": "/home/user/project"}
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
				// Also set actual HOME env for formatPath
				t.Setenv("HOME", "/home/user")
				// Add git files
				fr, _ := deps.FileReader.(*MockFileReader)
				fr.files["/home/user/project/.git"] = []byte{} // Make .git exist as a directory
				fr.files["/home/user/project/.git/HEAD"] = []byte("ref: refs/heads/main\n")
				// Canned dirty porcelain response: real dirty-count now
				// drives the "!" suffix, replacing the deleted
				// index-mtime heuristic.
				if cr, ok := deps.CommandRunner.(*MockCommandRunner); ok {
					cr.responses["git -C /home/user/project status --porcelain=v2 --branch"] =
						[]byte("# branch.head main\n1 .M N... 100644 100644 100644 aaa bbb file.go\n")
				}
			},
			contains: []string{
				"main", // Git branch
				"!",    // Modified status
			},
		},
		{
			name: "statusline with context percentage",
			input: `{
				"model": {"display_name": "Claude"},
				"workspace": {"project_dir": "/home/user/project"},
				"context_window": {
					"used_percentage": 25.5,
					"context_window_size": 200000
				}
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
			},
			contains: []string{
				ContextIcon, // Context bar should be visible
			},
		},
		{
			name: "statusline with AWS profile",
			input: `{
				"model": {"display_name": "Claude"},
				"workspace": {"project_dir": "/home/user/project"}
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["AWS_PROFILE"] = "dev-account"
				}
			},
			contains: []string{
				"dev-account", // AWS profile
			},
		},
		{
			name: "statusline with k8s context",
			input: `{
				"model": {"display_name": "Claude"},
				"workspace": {"project_dir": "/home/user/project"}
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
				fr, _ := deps.FileReader.(*MockFileReader)
				// Add kubeconfig with context
				fr.files["/home/user/.kube/config"] = []byte("current-context: production-cluster\n")
			},
			contains: []string{
				"production-cluster", // K8s context
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore HOME env var

			// Create mock dependencies
			deps := &Dependencies{
				FileReader:    NewMockFileReader(),
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     NewMockEnvReader(),
				TerminalWidth: &MockTerminalWidth{width: 210},
				CacheDir:      "", // disable caching: tests must not write to the real filesystem
				CacheDuration: 0,
			}

			// Apply test-specific setup
			if tt.setup != nil {
				tt.setup(deps)
			}

			// Create statusline
			sl := CreateStatusline(deps)

			// Generate output
			reader := bytes.NewReader([]byte(tt.input))
			output, err := sl.Generate(reader)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}

			// Check that output contains expected strings
			for _, expected := range tt.contains {
				if !strings.Contains(output, expected) {
					t.Errorf("Output doesn't contain %q\nGot: %q", expected, output)
				}
			}
		})
	}
}

func TestFormatPath(t *testing.T) {
	tests := []struct {
		input    string
		home     string
		expected string
	}{
		{"/home/user/project", "/home/user", "~/project"},
		// Once truncated, the home indicator drops to `…/` to match
		// starship's directory truncation behavior on the prompt line.
		{"/home/user/a/b/c/d", "/home/user", "…/c/d"},
		{"/usr/local/bin", "/home/user", "/usr/local/bin"},
		{"/a/b/c/d/e/f", "/home/user", "…/e/f"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			// Set HOME env var temporarily
			t.Setenv("HOME", tt.home)

			result := formatPath(tt.input)
			if result != tt.expected {
				t.Errorf("formatPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		text      string
		maxLength int
		expected  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"test", 4, "test"},
		{"testing", 4, "tes…"},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			result := truncateText(tt.text, tt.maxLength)
			if result != tt.expected {
				t.Errorf("truncateText(%q, %d) = %q, want %q", tt.text, tt.maxLength, result, tt.expected)
			}
		})
	}
}

func TestContextBar(t *testing.T) {
	deps := &Dependencies{
		FileReader:    NewMockFileReader(),
		CommandRunner: NewMockCommandRunner(),
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 210},
		CacheDir:      "", // disable caching: tests must not write to the real filesystem
		CacheDuration: 0,
	}

	sl := CreateStatusline(deps)

	tests := []struct {
		percentage    float64
		shouldContain []string
	}{
		{
			percentage:    20.0, // 20% - teal
			shouldContain: []string{"20%", ContextIcon},
		},
		{
			percentage:    50.0, // 50% - teal
			shouldContain: []string{"50%", ContextIcon},
		},
		{
			percentage:    75.0, // 75% - yellow
			shouldContain: []string{"75%", ContextIcon},
		},
		{
			percentage:    100.0, // 100% - red
			shouldContain: []string{"100%", ContextIcon},
		},
	}

	for _, tt := range tests {
		t.Run(tt.shouldContain[0], func(t *testing.T) {
			result := sl.buildContextElement(tt.percentage)

			for _, expected := range tt.shouldContain {
				if !strings.Contains(result, expected) {
					t.Errorf("Context bar doesn't contain %q\nGot: %q", expected, result)
				}
			}

			// No decimal point should ever appear in the percentage.
			if strings.Contains(result, ".") {
				t.Errorf("Context bar should not contain a decimal point\nGot: %q", result)
			}

			// Check that ANSI codes are present
			if !strings.Contains(result, "\033[") {
				t.Error("Context bar doesn't contain ANSI escape codes")
			}
		})
	}
}

func TestGitInfo(t *testing.T) {
	fr := NewMockFileReader()
	deps := &Dependencies{
		FileReader: fr,
	}
	sl := CreateStatusline(deps)

	// Test with main branch
	fr.files["/project/.git"] = []byte{} // Make .git exist as a directory
	fr.files["/project/.git/HEAD"] = []byte("ref: refs/heads/main\n")
	info := sl.getGitInfo("/project")
	if info.Branch != "main" {
		t.Errorf("Expected branch 'main', got %q", info.Branch)
	}

	// Test with detached HEAD
	fr.files["/project/.git/HEAD"] = []byte("a1b2c3d4e5f6789012345678901234567890abcd\n")
	info = sl.getGitInfo("/project")
	if info.Branch != "a1b2c3d" {
		t.Errorf("Expected short hash 'a1b2c3d', got %q", info.Branch)
	}
}

func TestDevspace(t *testing.T) {
	env := NewMockEnvReader()
	deps := &Dependencies{
		EnvReader: env,
	}
	sl := CreateStatusline(deps)

	tests := []struct {
		devspace     string
		expectedText string
	}{
		// Planet labels truncate to 3 chars (glyph carries identity).
		// Arbitrary names keep full text since the name IS the identifier.
		{"mercury", "☿ mer"},
		{"venus", "♀ ven"},
		{"earth", "♁ ear"},
		{"mars", "♂ mar"},
		{"jupiter", "♃ jup"},
		{"custom", "● custom"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.devspace, func(t *testing.T) {
			env.vars["TMUX_DEVSPACE"] = tt.devspace
			text := sl.getDevspace()
			if text != tt.expectedText {
				t.Errorf("Expected text %q, got %q", tt.expectedText, text)
			}
		})
	}
}
