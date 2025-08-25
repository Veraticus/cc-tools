package statusline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Mock implementation of CommandRunner.
type mockCommandRunner struct {
	runFunc    func(_ context.Context, _ string, args ...string) error
	outputFunc func(_ context.Context, _ string, args ...string) ([]byte, error)
}

func (m *mockCommandRunner) RunContext(ctx context.Context, name string, args ...string) error {
	if m.runFunc != nil {
		return m.runFunc(ctx, name, args...)
	}
	return nil
}

func (m *mockCommandRunner) OutputContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.outputFunc != nil {
		return m.outputFunc(ctx, name, args...)
	}
	return []byte{}, nil
}

func TestGetGitInfoWithDeps(t *testing.T) { //nolint:cyclop // table-driven test with many scenarios
	tests := []struct {
		name         string
		mockRunner   *mockCommandRunner
		expectedInfo *GitInfo
	}{
		{
			name: "not a git repo",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, args ...string) error {
					if args[0] == "rev-parse" && args[1] == "--git-dir" {
						return errors.New("not a git repository")
					}
					return nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "",
				IsGitRepo:    false,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo on main branch with clean status",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, args ...string) error {
					if args[0] == "rev-parse" && args[1] == "--git-dir" {
						return nil // is a git repo
					}
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("main\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte(""), nil // clean status
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "main",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with detached HEAD",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil // is a git repo
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte(""), nil // no branch (detached HEAD)
					}
					if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--short" && args[2] == "HEAD" {
						return []byte("abc1234\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte(""), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "abc1234",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with untracked files",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("feature\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte("?? new_file.txt\n"), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "feature",
				IsGitRepo:    true,
				HasUntracked: true,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with modified files",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("develop\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte(" M file1.go\n D file2.go\n"), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "develop",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  true,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with staged files",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("feature/new\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte("M  file1.go\nA  file2.go\n"), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "feature/new",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  true, // M in status means modified
				HasStaged:    true,
			},
		},
		{
			name: "git repo with mixed status",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("main\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte("MM file1.go\n?? new.txt\nA  added.go\n M modified.go\n"), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "main",
				IsGitRepo:    true,
				HasUntracked: true,
				HasModified:  true,
				HasStaged:    true,
			},
		},
		{
			name: "git repo with branch error falls back to HEAD",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return nil, errors.New("branch error")
					}
					if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--short" && args[2] == "HEAD" {
						return []byte("def5678\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte(""), nil
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "def5678",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with all git commands failing except repo check",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, args ...string) error {
					if args[0] == "rev-parse" && args[1] == "--git-dir" {
						return nil
					}
					return errors.New("command failed")
				},
				outputFunc: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
					return nil, errors.New("command failed")
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with status command error",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("main\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return nil, errors.New("status failed")
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "main",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  false,
				HasStaged:    false,
			},
		},
		{
			name: "git repo with short status lines",
			mockRunner: &mockCommandRunner{
				runFunc: func(_ context.Context, _ string, _ ...string) error {
					return nil
				},
				outputFunc: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
						return []byte("main\n"), nil
					}
					if len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
						return []byte("M\n\nX\n M file.go\n"), nil // lines too short should be skipped
					}
					return []byte{}, nil
				},
			},
			expectedInfo: &GitInfo{
				Branch:       "main",
				IsGitRepo:    true,
				HasUntracked: false,
				HasModified:  true,
				HasStaged:    false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deps *Dependencies
			if tt.mockRunner != nil {
				deps = &Dependencies{
					Runner: tt.mockRunner,
				}
			}

			info := GetGitInfoWithDeps(deps)

			if info.Branch != tt.expectedInfo.Branch {
				t.Errorf("Branch = %v, want %v", info.Branch, tt.expectedInfo.Branch)
			}
			if info.IsGitRepo != tt.expectedInfo.IsGitRepo {
				t.Errorf("IsGitRepo = %v, want %v", info.IsGitRepo, tt.expectedInfo.IsGitRepo)
			}
			if info.HasUntracked != tt.expectedInfo.HasUntracked {
				t.Errorf("HasUntracked = %v, want %v", info.HasUntracked, tt.expectedInfo.HasUntracked)
			}
			if info.HasModified != tt.expectedInfo.HasModified {
				t.Errorf("HasModified = %v, want %v", info.HasModified, tt.expectedInfo.HasModified)
			}
			if info.HasStaged != tt.expectedInfo.HasStaged {
				t.Errorf("HasStaged = %v, want %v", info.HasStaged, tt.expectedInfo.HasStaged)
			}
		})
	}
}

func TestGetGitInfo(t *testing.T) {
	// Just ensure the convenience wrapper doesn't panic
	info := GetGitInfo()
	if info == nil {
		t.Error("expected non-nil GitInfo")
	}
}

func TestIsGitRepo(_ *testing.T) {
	ctx := context.Background()
	// Just ensure the convenience wrapper doesn't panic
	_ = isGitRepo(ctx)
}

func TestGetBranchName(_ *testing.T) {
	ctx := context.Background()
	// Just ensure the convenience wrapper doesn't panic
	_ = getBranchName(ctx)
}

func TestParseGitStatus(_ *testing.T) {
	ctx := context.Background()
	info := &GitInfo{}
	// Just ensure the convenience wrapper doesn't panic
	parseGitStatus(ctx, info)
}

func TestGetGitSymbol(t *testing.T) {
	tests := []struct {
		name     string
		info     *GitInfo
		expected string
	}{
		{
			name: "not a git repo",
			info: &GitInfo{
				IsGitRepo: false,
			},
			expected: "",
		},
		{
			name: "clean repo",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  false,
				HasUntracked: false,
				HasStaged:    false,
			},
			expected: "🌿",
		},
		{
			name: "repo with modified files",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  true,
				HasUntracked: false,
				HasStaged:    false,
			},
			expected: "🔧",
		},
		{
			name: "repo with untracked files",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  false,
				HasUntracked: true,
				HasStaged:    false,
			},
			expected: "🔧",
		},
		{
			name: "repo with staged files",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  false,
				HasUntracked: false,
				HasStaged:    true,
			},
			expected: "📝",
		},
		{
			name: "repo with staged and modified files",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  true,
				HasUntracked: false,
				HasStaged:    true,
			},
			expected: "📝", // staged takes precedence
		},
		{
			name: "repo with all types of changes",
			info: &GitInfo{
				IsGitRepo:    true,
				HasModified:  true,
				HasUntracked: true,
				HasStaged:    true,
			},
			expected: "📝", // staged takes precedence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			symbol := tt.info.GetGitSymbol()
			if symbol != tt.expected {
				t.Errorf("GetGitSymbol() = %v, want %v", symbol, tt.expected)
			}
		})
	}
}

func TestGitTimeout(t *testing.T) {
	// Test that operations respect context timeout
	slowRunner := &mockCommandRunner{
		runFunc: func(ctx context.Context, _ string, _ ...string) error {
			select {
			case <-time.After(5 * time.Second):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		outputFunc: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			select {
			case <-time.After(5 * time.Second):
				return []byte{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	deps := &Dependencies{Runner: slowRunner}

	// The function should complete within the 2-second timeout
	start := time.Now()
	info := GetGitInfoWithDeps(deps)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("GetGitInfoWithDeps took too long: %v", elapsed)
	}

	// Should not be detected as a git repo due to timeout
	if info.IsGitRepo {
		t.Error("expected IsGitRepo to be false due to timeout")
	}
}

func TestNewDefaultDependencies(t *testing.T) {
	deps := NewDefaultDependencies()
	if deps == nil {
		t.Fatal("expected non-nil dependencies")
	}
	if deps.Runner == nil {
		t.Fatal("expected non-nil Runner")
	}
}
