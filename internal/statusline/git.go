// Package statusline provides functionality for generating Claude Code status lines.
package statusline

import (
	"context"
	"strings"
	"time"
)

// GitInfo contains git repository information.
type GitInfo struct {
	Branch       string
	IsGitRepo    bool
	HasUntracked bool
	HasModified  bool
	HasStaged    bool
}

// GetGitInfoWithDeps retrieves git information for the current directory.
func GetGitInfoWithDeps(deps *Dependencies) *GitInfo {
	if deps == nil {
		deps = NewDefaultDependencies()
	}

	info := &GitInfo{}

	// Create a context with timeout for all git commands
	const gitTimeout = 2 * time.Second // Reasonable timeout for git operations
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	// Check if we're in a git repo
	if !isGitRepoWithDeps(ctx, deps) {
		return info
	}
	info.IsGitRepo = true

	// Get branch information
	info.Branch = getBranchNameWithDeps(ctx, deps)

	// Get status information
	parseGitStatusWithDeps(ctx, info, deps)

	return info
}

// GetGitInfo is a convenience wrapper that uses default dependencies.
func GetGitInfo() *GitInfo {
	return GetGitInfoWithDeps(nil)
}

// isGitRepoWithDeps checks if the current directory is in a git repository.
func isGitRepoWithDeps(ctx context.Context, deps *Dependencies) bool {
	return deps.Runner.RunContext(ctx, "git", "rev-parse", "--git-dir") == nil
}

// isGitRepo is a convenience wrapper that uses default dependencies.
func isGitRepo(ctx context.Context) bool {
	return isGitRepoWithDeps(ctx, NewDefaultDependencies())
}

// getBranchNameWithDeps gets the current branch name or commit hash.
func getBranchNameWithDeps(ctx context.Context, deps *Dependencies) string {
	// Try to get current branch
	output, err := deps.Runner.OutputContext(ctx, "git", "branch", "--show-current")
	if err == nil {
		branch := strings.TrimSpace(string(output))
		if branch != "" {
			return branch
		}
	}

	// If no branch (detached HEAD), get commit hash
	output, err = deps.Runner.OutputContext(ctx, "git", "rev-parse", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(string(output))
	}

	return ""
}

// getBranchName is a convenience wrapper that uses default dependencies.
func getBranchName(ctx context.Context) string {
	return getBranchNameWithDeps(ctx, NewDefaultDependencies())
}

// parseGitStatusWithDeps parses git status to determine file states.
func parseGitStatusWithDeps(ctx context.Context, info *GitInfo, deps *Dependencies) {
	output, err := deps.Runner.OutputContext(ctx, "git", "status", "--porcelain")
	if err != nil {
		return
	}

	const minStatusLength = 2 // Git status format requires at least 2 chars
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if len(line) < minStatusLength {
			continue
		}
		status := line[:2]
		if strings.Contains(status, "?") {
			info.HasUntracked = true
		}
		if strings.Contains(status, "M") || strings.Contains(status, "D") {
			info.HasModified = true
		}
		if status[0] != ' ' && status[0] != '?' {
			info.HasStaged = true
		}
	}
}

// parseGitStatus is a convenience wrapper that uses default dependencies.
func parseGitStatus(ctx context.Context, info *GitInfo) {
	parseGitStatusWithDeps(ctx, info, NewDefaultDependencies())
}

// GetGitSymbol returns an appropriate symbol for the git status.
func (g *GitInfo) GetGitSymbol() string {
	if !g.IsGitRepo {
		return ""
	}

	symbol := "🌿"
	if g.HasModified || g.HasUntracked {
		symbol = "🔧"
	}
	if g.HasStaged {
		symbol = "📝"
	}

	return symbol
}
