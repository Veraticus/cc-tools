package statusline

import (
	"errors"
	"testing"
	"time"
)

// Test cache functionality.
func TestStatusline_CacheExpiration(t *testing.T) {
	cacheDir := t.TempDir()

	fr := NewMockFileReader()
	fr.files["/test/dir/.git"] = []byte{}
	fr.files["/test/dir/.git/HEAD"] = []byte("ref: refs/heads/main\n")
	fr.files["/test/dir/.git/index"] = []byte("index")
	fr.times["/test/dir/.git/index"] = time.Now().Add(-5 * time.Second)

	cr := NewMockCommandRunner()
	cr.responses["git branch --show-current"] = []byte("main")

	er := NewMockEnvReader()
	er.vars["PWD"] = "/test/dir"

	deps := &Dependencies{
		FileReader:    fr,
		CommandRunner: cr,
		EnvReader:     er,
		TerminalWidth: &MockTerminalWidth{width: 120},
		CacheDir:      cacheDir,
		CacheDuration: 100 * time.Millisecond, // Very short cache
	}

	s := CreateStatusline(deps)
	// Initialize input to avoid nil pointer
	s.input = &Input{}

	// First call should populate cache
	data1 := s.computeData("/test/dir")

	// Immediate second call should use cache
	data2 := s.computeData("/test/dir")

	// These should be the same (from cache)
	if data1.GitBranch != data2.GitBranch {
		t.Error("Expected same data from cache")
	}

	// Wait for cache to expire
	time.Sleep(150 * time.Millisecond)

	// This call should bypass cache
	data3 := s.computeData("/test/dir")

	// Should still get same result (but from fresh computation)
	if data1.GitBranch != data3.GitBranch {
		t.Error("Expected same git branch after cache expiration")
	}
}

// Test hostname retrieval.
func TestStatusline_GetHostname(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*MockEnvReader, *MockCommandRunner)
		expected string
	}{
		{
			name: "from CLAUDE_STATUSLINE_HOSTNAME override",
			setup: func(er *MockEnvReader, _ *MockCommandRunner) {
				er.vars["CLAUDE_STATUSLINE_HOSTNAME"] = "test-host"
			},
			expected: "test-host",
		},
		{
			name: "from HOSTNAME env var",
			setup: func(er *MockEnvReader, _ *MockCommandRunner) {
				er.vars["HOSTNAME"] = "prod-server"
			},
			expected: "prod-server",
		},
		{
			name: "from hostname -s command",
			setup: func(_ *MockEnvReader, cr *MockCommandRunner) {
				cr.responses["hostname -s"] = []byte("dev-box")
			},
			expected: "dev-box",
		},
		{
			name: "from hostname command fallback",
			setup: func(_ *MockEnvReader, cr *MockCommandRunner) {
				// hostname -s fails with error, falls back to hostname
				if cr.errors == nil {
					cr.errors = make(map[string]error)
				}
				cr.errors["hostname -s"] = errors.New("command failed")
				cr.responses["hostname"] = []byte("fallback-host")
			},
			expected: "fallback-host",
		},
		{
			name: "default unknown",
			setup: func(_ *MockEnvReader, _ *MockCommandRunner) {
				// No setup, all methods fail
			},
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			er := NewMockEnvReader()
			cr := NewMockCommandRunner()
			tt.setup(er, cr)

			deps := &Dependencies{
				FileReader:    NewMockFileReader(),
				CommandRunner: cr,
				EnvReader:     er,
				TerminalWidth: &MockTerminalWidth{width: 120},
			}

			s := CreateStatusline(deps)
			hostname := s.getHostname()

			if hostname != tt.expected {
				t.Errorf("Expected hostname %q, got %q", tt.expected, hostname)
			}
		})
	}
}

// Test Kubernetes context retrieval.
func TestStatusline_GetK8sContext(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*MockFileReader, *MockEnvReader)
		expected string
	}{
		{
			name: "disabled via override",
			setup: func(_ *MockFileReader, er *MockEnvReader) {
				er.vars["CLAUDE_STATUSLINE_KUBECONFIG"] = "/dev/null"
			},
			expected: "",
		},
		{
			name: "from KUBECONFIG env var",
			setup: func(fr *MockFileReader, er *MockEnvReader) {
				er.vars["KUBECONFIG"] = "/custom/kube/config"
				fr.files["/custom/kube/config"] = []byte("current-context: custom-cluster\n")
			},
			expected: "custom-cluster",
		},
		{
			name: "from default location",
			setup: func(fr *MockFileReader, er *MockEnvReader) {
				er.vars["HOME"] = "/home/user"
				fr.files["/home/user/.kube/config"] = []byte("current-context: default-cluster\n")
			},
			expected: "default-cluster",
		},
		{
			name: "with quoted context",
			setup: func(fr *MockFileReader, er *MockEnvReader) {
				er.vars["HOME"] = "/home/user"
				fr.files["/home/user/.kube/config"] = []byte(`current-context: "quoted-cluster"` + "\n")
			},
			expected: "quoted-cluster",
		},
		{
			name: "file doesn't exist",
			setup: func(_ *MockFileReader, er *MockEnvReader) {
				er.vars["HOME"] = "/home/user"
				// No kubeconfig file
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := NewMockFileReader()
			er := NewMockEnvReader()
			tt.setup(fr, er)

			deps := &Dependencies{
				FileReader:    fr,
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     er,
				TerminalWidth: &MockTerminalWidth{width: 120},
			}

			s := CreateStatusline(deps)
			context := s.getK8sContext()

			if context != tt.expected {
				t.Errorf("Expected k8s context %q, got %q", tt.expected, context)
			}
		})
	}
}

// Test devspace retrieval.
func TestStatusline_GetDevspace(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*MockEnvReader)
		expectedText   string
		expectedSymbol string
	}{
		{
			name: "with override",
			setup: func(er *MockEnvReader) {
				er.vars["CLAUDE_STATUSLINE_DEVSPACE"] = "saturn"
			},
			expectedText:   "● saturn",
			expectedSymbol: "●",
		},
		{
			name: "mercury from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "mercury"
			},
			expectedText:   "☿ mercury",
			expectedSymbol: "☿",
		},
		{
			name: "venus from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "venus"
			},
			expectedText:   "♀ venus",
			expectedSymbol: "♀",
		},
		{
			name: "earth from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "earth"
			},
			expectedText:   "♁ earth",
			expectedSymbol: "♁",
		},
		{
			name: "mars from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "mars"
			},
			expectedText:   "♂ mars",
			expectedSymbol: "♂",
		},
		{
			name: "jupiter from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "jupiter"
			},
			expectedText:   "♃ jupiter",
			expectedSymbol: "♃",
		},
		{
			name: "custom devspace",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "project-dev"
			},
			expectedText:   "● project-dev",
			expectedSymbol: "●",
		},
		{
			name: "empty when -TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "-TMUX_DEVSPACE"
			},
			expectedText:   "",
			expectedSymbol: "",
		},
		{
			name: "empty when not set",
			setup: func(_ *MockEnvReader) {
				// No TMUX_DEVSPACE set
			},
			expectedText:   "",
			expectedSymbol: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			er := NewMockEnvReader()
			tt.setup(er)

			deps := &Dependencies{
				FileReader:    NewMockFileReader(),
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     er,
				TerminalWidth: &MockTerminalWidth{width: 120},
			}

			s := CreateStatusline(deps)
			text, symbol := s.getDevspace()

			if text != tt.expectedText {
				t.Errorf("Expected devspace text %q, got %q", tt.expectedText, text)
			}
			if symbol != tt.expectedSymbol {
				t.Errorf("Expected devspace symbol %q, got %q", tt.expectedSymbol, symbol)
			}
		})
	}
}

// Test getTokenMetrics with invalid JSON.
func TestStatusline_GetTokenMetrics_InvalidJSON(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected TokenMetrics
	}{
		{
			name:     "empty file",
			content:  "",
			expected: TokenMetrics{},
		},
		{
			name:     "invalid JSON",
			content:  "not json at all",
			expected: TokenMetrics{},
		},
		{
			name:     "partial JSON line",
			content:  `{"message": {"usage": {"input_tokens":`,
			expected: TokenMetrics{},
		},
		{
			name: "mixed valid and invalid",
			content: `{"message": {"usage": {"input_tokens": 100, "output_tokens": 50}}}
invalid line
{"message": {"usage": {"input_tokens": 200, "output_tokens": 100}}}`,
			expected: TokenMetrics{
				InputTokens:   300,
				OutputTokens:  150,
				ContextLength: 200, // Last main chain entry's input_tokens only
			},
		},
		{
			name:    "with cache tokens",
			content: `{"message": {"usage": {"input_tokens": 100, "output_tokens": 50, "cache_read_input_tokens": 25}}}`,
			expected: TokenMetrics{
				InputTokens:   100,
				OutputTokens:  50,
				CachedTokens:  25,
				ContextLength: 125, // input_tokens (100) + cache_read_input_tokens (25)
			},
		},
		{
			name: "with sidechain entries",
			content: `{"message": {"usage": {"input_tokens": 100, "output_tokens": 50}}}
{"message": {"usage": {"input_tokens": 200, "output_tokens": 100}}, "isSidechain": true}
{"message": {"usage": {"input_tokens": 300, "output_tokens": 150, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 25}}}`,
			expected: TokenMetrics{
				InputTokens:   600, // All entries count for totals
				OutputTokens:  300,
				CachedTokens:  50,
				ContextLength: 375, // Last main chain: 300 + 50 + 25
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := NewMockFileReader()
			fr.files["/tmp/transcript.jsonl"] = []byte(tt.content)

			deps := &Dependencies{
				FileReader:    fr,
				CommandRunner: NewMockCommandRunner(),
				EnvReader:     NewMockEnvReader(),
				TerminalWidth: &MockTerminalWidth{width: 120},
			}

			s := CreateStatusline(deps)
			result := s.getTokenMetrics("/tmp/transcript.jsonl")

			if result.InputTokens != tt.expected.InputTokens {
				t.Errorf("Expected InputTokens %d, got %d",
					tt.expected.InputTokens, result.InputTokens)
			}

			if result.OutputTokens != tt.expected.OutputTokens {
				t.Errorf("Expected OutputTokens %d, got %d",
					tt.expected.OutputTokens, result.OutputTokens)
			}

			if result.CachedTokens != tt.expected.CachedTokens {
				t.Errorf("Expected CachedTokens %d, got %d",
					tt.expected.CachedTokens, result.CachedTokens)
			}

			if result.ContextLength != tt.expected.ContextLength {
				t.Errorf("Expected ContextLength %d, got %d",
					tt.expected.ContextLength, result.ContextLength)
			}
		})
	}
}
