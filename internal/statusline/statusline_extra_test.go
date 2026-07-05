package statusline

import (
	"errors"
	"testing"
	"time"
)

// TestStatusline_CacheExpiration exercises the git-status cache through
// the production computeData path: a fresh cache serves the previous
// porcelain result without re-running git; once the cache is older than
// CacheDuration (simulated via the injected Now, no sleeping), the next
// render re-runs git and picks up new state.
func TestStatusline_CacheExpiration(t *testing.T) {
	cacheDir := t.TempDir()

	fr := NewMockFileReader()
	fr.files["/test/dir/.git"] = []byte{}
	fr.files["/test/dir/.git/HEAD"] = []byte("ref: refs/heads/main\n")

	const gitStatusKey = "git -C /test/dir status --porcelain=v2 --branch"
	cr := NewMockCommandRunner()
	cr.responses[gitStatusKey] = []byte("# branch.head main\n? file1.go\n")

	baseNow := time.Now()
	currentNow := baseNow

	deps := &Dependencies{
		FileReader:    fr,
		CommandRunner: cr,
		EnvReader:     NewMockEnvReader(),
		TerminalWidth: &MockTerminalWidth{width: 120},
		CacheDir:      cacheDir,
		CacheDuration: time.Hour,
		Now:           func() time.Time { return currentNow },
	}

	s := CreateStatusline(deps)
	// Initialize input to avoid nil pointer
	s.input = &Input{}

	// First call runs git and populates the cache.
	data1 := s.computeData("/test/dir")
	if data1.GitBranch != "main" || data1.GitDirtyCount != 1 {
		t.Fatalf("expected branch main with 1 dirty entry, got branch=%q dirty=%d",
			data1.GitBranch, data1.GitDirtyCount)
	}

	// The repo state changes, but the cache is still fresh: the second
	// call must serve the cached count, not the new porcelain.
	cr.responses[gitStatusKey] = []byte("# branch.head main\n? file1.go\n? file2.go\n")
	data2 := s.computeData("/test/dir")
	if data2.GitDirtyCount != 1 {
		t.Errorf("fresh cache should serve the cached dirty count 1, got %d", data2.GitDirtyCount)
	}

	// Advance the injected clock past CacheDuration: the cache is now
	// stale, so git re-runs and the new state is visible.
	currentNow = baseNow.Add(2 * time.Hour)
	data3 := s.computeData("/test/dir")
	if data3.GitDirtyCount != 2 {
		t.Errorf("expired cache should re-run git and pick up dirty count 2, got %d", data3.GitDirtyCount)
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
