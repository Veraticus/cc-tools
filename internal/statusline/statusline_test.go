package statusline

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// MockFileReader implements FileReader for testing.
type MockFileReader struct {
	files map[string][]byte
	times map[string]time.Time
}

func NewMockFileReader() *MockFileReader {
	return &MockFileReader{
		files: make(map[string][]byte),
		times: make(map[string]time.Time),
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

func (m *MockFileReader) ModTime(path string) (time.Time, error) {
	if t, ok := m.times[path]; ok {
		return t, nil
	}
	return time.Now(), nil
}

// MockCommandRunner implements CommandRunner for testing.
type MockCommandRunner struct {
	responses map[string][]byte
}

func NewMockCommandRunner() *MockCommandRunner {
	return &MockCommandRunner{
		responses: make(map[string][]byte),
	}
}

func (m *MockCommandRunner) Run(command string, args ...string) ([]byte, error) {
	key := command + " " + strings.Join(args, " ")
	if response, ok := m.responses[key]; ok {
		return response, nil
	}
	return []byte(""), nil
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
				fr.files["/home/user/project/.git/index"] = []byte("index")
				fr.times["/home/user/project/.git/index"] = time.Now() // Recent modification
			},
			contains: []string{
				"main", // Git branch
				"!",    // Modified status
			},
		},
		{
			name: "statusline with tokens",
			input: `{
				"model": {"display_name": "Claude"},
				"workspace": {"project_dir": "/home/user/project"},
				"transcript_path": "/tmp/transcript.jsonl"
			}`,
			setup: func(deps *Dependencies) {
				if envReader, ok := deps.EnvReader.(*MockEnvReader); ok {
					envReader.vars["HOME"] = "/home/user"
				}
				fr, _ := deps.FileReader.(*MockFileReader)
				// Add transcript with token usage
				fr.files["/tmp/transcript.jsonl"] = []byte(
					`{"message": {"usage": {"input_tokens": 1500, "output_tokens": 300}}}`,
				)
			},
			contains: []string{
				"↑1.5k", // Input tokens
				"↓300",  // Output tokens
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
				CacheDir:      "/tmp",
				CacheDuration: 0, // Disable cache for tests
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
		{"/home/user/a/b/c/d", "/home/user", "~/c/d"},
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

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		count    int
		expected string
	}{
		{100, "100"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatTokens(tt.count)
			if result != tt.expected {
				t.Errorf("formatTokens(%d) = %q, want %q", tt.count, result, tt.expected)
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
		CacheDir:      "/tmp",
		CacheDuration: 0,
	}

	sl := CreateStatusline(deps)

	tests := []struct {
		contextLength int
		barWidth      int
		shouldContain []string
	}{
		{
			contextLength: 32000, // 20% - green
			barWidth:      50,
			shouldContain: []string{"20.0%", "Context"},
		},
		{
			contextLength: 80000, // 50% - yellow
			barWidth:      50,
			shouldContain: []string{"50.0%", "Context"},
		},
		{
			contextLength: 120000, // 75% - peach
			barWidth:      50,
			shouldContain: []string{"75.0%", "Context"},
		},
		{
			contextLength: 160000, // 100% - red
			barWidth:      50,
			shouldContain: []string{"100.0%", "Context"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.shouldContain[0], func(t *testing.T) {
			result := sl.createContextBar(tt.contextLength, tt.barWidth)

			for _, expected := range tt.shouldContain {
				if !strings.Contains(result, expected) {
					t.Errorf("Context bar doesn't contain %q\nGot: %q", expected, result)
				}
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

	// Test with recent index modification
	fr.files["/project/.git/index"] = []byte("index")
	fr.times["/project/.git/index"] = time.Now()
	info = sl.getGitInfo("/project")
	if info.Status != "!" {
		t.Errorf("Expected status '!', got %q", info.Status)
	}
}

func TestDevspace(t *testing.T) {
	env := NewMockEnvReader()
	deps := &Dependencies{
		EnvReader: env,
	}
	sl := CreateStatusline(deps)

	tests := []struct {
		devspace       string
		expectedText   string
		expectedSymbol string
	}{
		{"mercury", "☿ mercury", "☿"},
		{"venus", "♀ venus", "♀"},
		{"earth", "♁ earth", "♁"},
		{"mars", "♂ mars", "♂"},
		{"jupiter", "♃ jupiter", "♃"},
		{"custom", "● custom", "●"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.devspace, func(t *testing.T) {
			env.vars["TMUX_DEVSPACE"] = tt.devspace
			text, symbol := sl.getDevspace()
			if text != tt.expectedText {
				t.Errorf("Expected text %q, got %q", tt.expectedText, text)
			}
			if symbol != tt.expectedSymbol {
				t.Errorf("Expected symbol %q, got %q", tt.expectedSymbol, symbol)
			}
		})
	}
}
