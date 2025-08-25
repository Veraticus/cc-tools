package statusline

import (
	"strings"
	"testing"

	"github.com/Veraticus/cc-tools/internal/hooks"
)

func TestNewFormatter(t *testing.T) {
	input := &hooks.StatusLineInput{
		Model: hooks.ModelInfo{
			DisplayName: "Claude 3 Opus",
		},
	}

	formatter := NewFormatter(input)

	if formatter == nil {
		t.Fatal("expected non-nil formatter")
	}
	if formatter.input != input {
		t.Error("expected input to be set")
	}
	if formatter.git == nil {
		t.Error("expected git info to be initialized")
	}
}

func TestFormatModelSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    *hooks.StatusLineInput
		expected string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: "",
		},
		{
			name: "empty model name",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "",
				},
			},
			expected: "",
		},
		{
			name: "opus model",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "Claude 3 Opus",
				},
			},
			expected: "Claude 3 Opus", // will be styled with mauve
		},
		{
			name: "sonnet model",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "Claude 3.5 Sonnet",
				},
			},
			expected: "Claude 3.5 Sonnet", // will be styled with sky
		},
		{
			name: "haiku model",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "Claude 3 Haiku",
				},
			},
			expected: "Claude 3 Haiku", // will be styled with teal
		},
		{
			name: "unknown model",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "GPT-4",
				},
			},
			expected: "GPT-4", // will be styled with lavender
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{input: tt.input}
			result := formatter.formatModelSegment()

			if tt.expected == "" {
				if result != "" {
					t.Errorf("expected empty string, got %v", result)
				}
			} else {
				// Check that the model name is in the result (styled)
				if !strings.Contains(result, tt.expected) {
					t.Errorf("expected result to contain %v, got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestFormatDirectorySegment(t *testing.T) {
	tests := []struct {
		name     string
		input    *hooks.StatusLineInput
		contains string
	}{
		{
			name:     "nil input",
			input:    nil,
			contains: "",
		},
		{
			name: "empty directory",
			input: &hooks.StatusLineInput{
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: "",
				},
			},
			contains: "",
		},
		{
			name: "normal directory",
			input: &hooks.StatusLineInput{
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: "/home/user/project",
				},
			},
			contains: "📁 project",
		},
		{
			name: "root directory",
			input: &hooks.StatusLineInput{
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: "/",
				},
			},
			contains: "📁 /",
		},
		{
			name: "current directory",
			input: &hooks.StatusLineInput{
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: ".",
				},
			},
			contains: "📁 .",
		},
		{
			name: "long directory name",
			input: &hooks.StatusLineInput{
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: "/home/user/very/long/path/to/some/deeply/nested/project/directory",
				},
			},
			contains: "📁 directory", // Only the basename is shown (which is "directory")
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{input: tt.input}
			result := formatter.formatDirectorySegment()

			if tt.contains == "" {
				if result != "" {
					t.Errorf("expected empty string, got %v", result)
				}
			} else {
				if !strings.Contains(result, tt.contains) {
					t.Errorf("expected result to contain %v, got %v", tt.contains, result)
				}
			}
		})
	}
}

func TestFormatGitSegment(t *testing.T) {
	tests := []struct {
		name     string
		git      *GitInfo
		contains string
	}{
		{
			name:     "nil git info",
			git:      nil,
			contains: "",
		},
		{
			name: "not a git repo",
			git: &GitInfo{
				IsGitRepo: false,
				Branch:    "main",
			},
			contains: "",
		},
		{
			name: "git repo with no branch",
			git: &GitInfo{
				IsGitRepo: true,
				Branch:    "",
			},
			contains: "",
		},
		{
			name: "clean git repo",
			git: &GitInfo{
				IsGitRepo: true,
				Branch:    "main",
			},
			contains: "🌿 main",
		},
		{
			name: "git repo with staged changes",
			git: &GitInfo{
				IsGitRepo: true,
				Branch:    "feature",
				HasStaged: true,
			},
			contains: "📝 feature",
		},
		{
			name: "git repo with modified files",
			git: &GitInfo{
				IsGitRepo:   true,
				Branch:      "develop",
				HasModified: true,
			},
			contains: "🔧 develop",
		},
		{
			name: "git repo with untracked files",
			git: &GitInfo{
				IsGitRepo:    true,
				Branch:       "test",
				HasUntracked: true,
			},
			contains: "🔧 test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{git: tt.git}
			result := formatter.formatGitSegment()

			if tt.contains == "" {
				if result != "" {
					t.Errorf("expected empty string, got %v", result)
				}
			} else {
				if !strings.Contains(result, tt.contains) {
					t.Errorf("expected result to contain %v, got %v", tt.contains, result)
				}
			}
		})
	}
}

func TestFormatCostSegment(t *testing.T) {
	tests := []struct {
		name     string
		input    *hooks.StatusLineInput
		contains string
	}{
		{
			name:     "nil input",
			input:    nil,
			contains: "",
		},
		{
			name: "zero cost",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.0,
				},
			},
			contains: "",
		},
		{
			name: "very small cost",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.005,
				},
			},
			contains: "",
		},
		{
			name: "small cost with 3 decimal places",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.025,
				},
			},
			contains: "$0.025",
		},
		{
			name: "cost below threshold",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.095,
				},
			},
			contains: "$0.095",
		},
		{
			name: "cost at threshold",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.10,
				},
			},
			contains: "$0.10",
		},
		{
			name: "cost above threshold",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 1.234,
				},
			},
			contains: "$1.23",
		},
		{
			name: "large cost",
			input: &hooks.StatusLineInput{
				Cost: hooks.CostInfo{
					TotalCostUSD: 123.456,
				},
			},
			contains: "$123.46",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{input: tt.input}
			result := formatter.formatCostSegment()

			if tt.contains == "" {
				if result != "" {
					t.Errorf("expected empty string, got %v", result)
				}
			} else {
				if !strings.Contains(result, tt.contains) {
					t.Errorf("expected result to contain %v, got %v", tt.contains, result)
				}
			}
		})
	}
}

func TestJoinSegments(t *testing.T) {
	tests := []struct {
		name     string
		segments []string
		expected string
	}{
		{
			name:     "empty segments",
			segments: []string{},
			expected: "",
		},
		{
			name:     "single segment",
			segments: []string{"segment1"},
			expected: "segment1",
		},
		{
			name:     "two segments",
			segments: []string{"segment1", "segment2"},
			expected: "segment1",
		},
		{
			name:     "three segments",
			segments: []string{"segment1", "segment2", "segment3"},
			expected: "segment1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{}
			result := formatter.joinSegments(tt.segments)

			// Handle empty expectation
			if tt.expected == "" {
				if result != "" {
					t.Errorf("expected empty string, got %v", result)
				}
				return
			}

			// Check that first segment is present
			if !strings.Contains(result, tt.expected) {
				t.Errorf("expected result to contain %v, got %v", tt.expected, result)
			}

			// Check for chevron separators when multiple segments
			if len(tt.segments) > 1 && result == tt.segments[0] {
				t.Errorf("expected separators to be added for multiple segments")
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    *hooks.StatusLineInput
		git      *GitInfo
		contains []string
	}{
		{
			name: "full statusline",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "Claude 3 Opus",
				},
				Workspace: hooks.WorkspaceInfo{
					CurrentDir: "/home/user/project",
				},
				Cost: hooks.CostInfo{
					TotalCostUSD: 0.15,
				},
			},
			git: &GitInfo{
				IsGitRepo: true,
				Branch:    "main",
				HasStaged: true,
			},
			contains: []string{"Claude 3 Opus", "project", "main", "$0.15"},
		},
		{
			name: "minimal statusline",
			input: &hooks.StatusLineInput{
				Model: hooks.ModelInfo{
					DisplayName: "Claude",
				},
			},
			git: &GitInfo{
				IsGitRepo: false,
			},
			contains: []string{"Claude"},
		},
		{
			name:     "empty statusline",
			input:    &hooks.StatusLineInput{},
			git:      &GitInfo{},
			contains: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &Formatter{
				input: tt.input,
				git:   tt.git,
			}
			result := formatter.Format()

			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("expected result to contain %v, got %v", expected, result)
				}
			}
		})
	}
}
