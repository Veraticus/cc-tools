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
		name         string
		setup        func(*MockEnvReader)
		expectedText string
	}{
		{
			name: "with override",
			setup: func(er *MockEnvReader) {
				er.vars["CLAUDE_STATUSLINE_DEVSPACE"] = "saturn"
			},
			expectedText: "● saturn",
		},
		// Names are truncated to 3 chars in the chip so the symbol does
		// the heavy lifting and the word just confirms the planet.
		{
			name: "mercury from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "mercury"
			},
			expectedText: "☿ mer",
		},
		{
			name: "venus from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "venus"
			},
			expectedText: "♀ ven",
		},
		{
			name: "earth from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "earth"
			},
			expectedText: "♁ ear",
		},
		{
			name: "mars from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "mars"
			},
			expectedText: "♂ mar",
		},
		{
			name: "jupiter from TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "jupiter"
			},
			expectedText: "♃ jup",
		},
		{
			name: "arbitrary devspace under 3 chars stays full",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "qa"
			},
			expectedText: "● qa",
		},
		{
			name: "arbitrary devspace keeps full name (not truncated)",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "project-dev"
			},
			expectedText: "● project-dev",
		},
		{
			name: "empty when -TMUX_DEVSPACE",
			setup: func(er *MockEnvReader) {
				er.vars["TMUX_DEVSPACE"] = "-TMUX_DEVSPACE"
			},
			expectedText: "",
		},
		{
			name: "empty when not set",
			setup: func(_ *MockEnvReader) {
				// No TMUX_DEVSPACE set
			},
			expectedText: "",
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
			text := s.getDevspace()

			if text != tt.expectedText {
				t.Errorf("Expected devspace text %q, got %q", tt.expectedText, text)
			}
		})
	}
}
