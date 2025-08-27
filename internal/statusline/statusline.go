package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Input represents the JSON input from stdin.
type Input struct {
	Model struct {
		ID          string `json:"id"`
		Provider    string `json:"provider"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Cost struct {
		TotalCostUSD     float64 `json:"total_cost_usd"`
		InputTokens      int     `json:"input_tokens"`
		OutputTokens     int     `json:"output_tokens"`
		CacheReadTokens  int     `json:"cache_read_input_tokens"`
		CacheWriteTokens int     `json:"cache_creation_input_tokens"`
	} `json:"cost"`
	GitInfo struct {
		Branch       string `json:"branch"`
		IsGitRepo    bool   `json:"is_git_repo"`
		HasUntracked bool   `json:"has_untracked"`
		HasModified  bool   `json:"has_modified"`
	} `json:"git_info"`
	Workspace struct {
		ProjectDir string `json:"project_dir"`
		CurrentDir string `json:"current_dir"`
		CWD        string `json:"cwd"`
	} `json:"workspace"`
	TranscriptPath string `json:"transcript_path"`
}

// TokenMetrics holds token usage information.
type TokenMetrics struct {
	InputTokens   int
	OutputTokens  int
	CachedTokens  int
	ContextLength int
}

// CachedData represents cached statusline data.
type CachedData struct {
	ModelDisplay   string
	CurrentDir     string
	TranscriptPath string
	GitBranch      string
	GitStatus      string
	K8sContext     string
	InputTokens    int
	OutputTokens   int
	ContextLength  int
	Hostname       string
	Devspace       string
	DevspaceSymbol string
	TermWidth      int
}

// Dependencies contains all external dependencies.
type Dependencies struct {
	FileReader    FileReader
	CommandRunner CommandRunner
	EnvReader     EnvReader
	TerminalWidth TerminalWidth
	CacheDir      string
	CacheDuration time.Duration
}

// FileReader interface for reading files.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	Exists(path string) bool
	ModTime(path string) (time.Time, error)
}

// CommandRunner interface for executing commands.
type CommandRunner interface {
	Run(command string, args ...string) ([]byte, error)
}

// EnvReader interface for reading environment variables.
type EnvReader interface {
	Get(key string) string
}

// TerminalWidth interface for getting terminal width.
type TerminalWidth interface {
	GetWidth() int
}

// Config contains configuration for the statusline.
type Config struct {
	// LeftSpacerWidth is the width of the left spacer (default: 2)
	LeftSpacerWidth int
	// RightSpacerWidth is the width of the right spacer (default: 2, only shown when not in compact mode)
	RightSpacerWidth int
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	const (
		defaultLeftSpacerWidth  = 2
		defaultRightSpacerWidth = 4
	)
	return &Config{
		LeftSpacerWidth:  defaultLeftSpacerWidth,
		RightSpacerWidth: defaultRightSpacerWidth,
	}
}

// Statusline is the main statusline generator.
type Statusline struct {
	deps   *Dependencies
	colors CatppuccinMocha
	input  *Input
	config *Config
}

// CreateStatusline creates a new Statusline instance.
func CreateStatusline(deps *Dependencies) *Statusline {
	return NewWithConfig(deps, DefaultConfig())
}

// NewWithConfig creates a new Statusline instance with custom configuration.
func NewWithConfig(deps *Dependencies, config *Config) *Statusline {
	if config == nil {
		config = DefaultConfig()
	}
	return &Statusline{
		deps:   deps,
		config: config,
		colors: CatppuccinMocha{},
	}
}

// Generate generates the statusline from JSON input.
func (s *Statusline) Generate(reader io.Reader) (string, error) {
	// Read and parse JSON input
	if err := s.parseInput(reader); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	// Get current directory
	currentDir := s.getCurrentDir()

	// Always compute data fresh (no caching)
	data := s.computeData(currentDir)

	// Build and return the statusline with guaranteed fixed width
	return s.Render(data), nil
}

func (s *Statusline) parseInput(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	s.input = &Input{}
	if err := decoder.Decode(s.input); err != nil {
		return fmt.Errorf("decoding JSON: %w", err)
	}
	return nil
}

func (s *Statusline) getCurrentDir() string {
	if s.input.Workspace.ProjectDir != "" {
		return s.input.Workspace.ProjectDir
	}
	if s.input.Workspace.CurrentDir != "" {
		return s.input.Workspace.CurrentDir
	}
	if s.input.Workspace.CWD != "" {
		return s.input.Workspace.CWD
	}
	return "~"
}

func (s *Statusline) computeData(currentDir string) *CachedData {
	data := &CachedData{
		CurrentDir:     currentDir,
		TranscriptPath: s.input.TranscriptPath,
		TermWidth:      s.deps.TerminalWidth.GetWidth(),
	}

	// Model display name
	if s.input.Model.DisplayName != "" {
		data.ModelDisplay = s.input.Model.DisplayName
	} else {
		data.ModelDisplay = "Claude"
	}

	// Git information
	gitInfo := s.getGitInfo(currentDir)
	data.GitBranch = gitInfo.Branch
	data.GitStatus = gitInfo.Status

	// Kubernetes context
	data.K8sContext = s.getK8sContext()

	// Token metrics
	if data.TranscriptPath != "" && s.deps.FileReader.Exists(data.TranscriptPath) {
		metrics := s.getTokenMetrics(data.TranscriptPath)
		data.InputTokens = metrics.InputTokens
		data.OutputTokens = metrics.OutputTokens
		data.ContextLength = metrics.ContextLength

		// Debug
		if os.Getenv("DEBUG_CONTEXT") == "1" {
			debug := fmt.Sprintf(
				"DEBUG computeData: TranscriptPath=%s, InputTokens=%d, OutputTokens=%d, ContextLength=%d\n",
				data.TranscriptPath,
				data.InputTokens,
				data.OutputTokens,
				data.ContextLength,
			)
			const debugFileMode = 0600
			if err := os.WriteFile("/tmp/compute_debug.txt", []byte(debug), debugFileMode); err != nil { //nolint:gosec // Debug file
				// Debug write failed - continue silently
				_ = err
			}
		}
	}

	// Hostname
	data.Hostname = s.getHostname()

	// Devspace
	data.Devspace, data.DevspaceSymbol = s.getDevspace()

	return data
}

func (s *Statusline) getGitInfo(dir string) GitInfo {
	// Walk up the directory tree to find .git
	current := dir
	for current != "/" && current != "." {
		gitPath := filepath.Join(current, ".git")
		if s.deps.FileReader.Exists(gitPath) {
			// Check if it's a directory or file (worktree)
			if content, err := s.deps.FileReader.ReadFile(gitPath); err == nil {
				// It's a file (worktree) - extract actual git dir
				contentStr := string(content)
				if strings.HasPrefix(contentStr, "gitdir:") {
					gitDir := strings.TrimSpace(strings.TrimPrefix(contentStr, "gitdir:"))
					return s.readGitInfo(gitDir)
				}
			}
			// Assume it's a directory
			return s.readGitInfo(gitPath)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return GitInfo{}
}

func (s *Statusline) readGitInfo(gitDir string) GitInfo {
	info := GitInfo{}

	// Read HEAD file for branch
	headPath := filepath.Join(gitDir, "HEAD")
	if content, err := s.deps.FileReader.ReadFile(headPath); err == nil {
		head := strings.TrimSpace(string(content))
		if strings.HasPrefix(head, "ref: refs/heads/") {
			info.Branch = strings.TrimPrefix(head, "ref: refs/heads/")
		} else if len(head) >= 7 {
			// Detached HEAD - show short hash
			info.Branch = head[:7]
		}
	}

	// Check for uncommitted changes
	indexPath := filepath.Join(gitDir, "index")
	if modTime, err := s.deps.FileReader.ModTime(indexPath); err == nil {
		// If index was modified in last 60 seconds, likely have changes
		const recentChangeWindow = 60 * time.Second
		if time.Since(modTime) < recentChangeWindow {
			info.Status = "!"
		}
	}

	// Check for merge/rebase states
	if s.deps.FileReader.Exists(filepath.Join(gitDir, "MERGE_HEAD")) ||
		s.deps.FileReader.Exists(filepath.Join(gitDir, "rebase-merge")) ||
		s.deps.FileReader.Exists(filepath.Join(gitDir, "rebase-apply")) {
		info.Status = "!"
	}

	return info
}

func (s *Statusline) getK8sContext() string {
	// Check for test override
	if override := s.deps.EnvReader.Get("CLAUDE_STATUSLINE_KUBECONFIG"); override != "" {
		if override == "/dev/null" {
			return ""
		}
	}

	kubeconfig := s.deps.EnvReader.Get("KUBECONFIG")
	if kubeconfig == "" {
		home := s.deps.EnvReader.Get("HOME")
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	if !s.deps.FileReader.Exists(kubeconfig) {
		return ""
	}

	content, err := s.deps.FileReader.ReadFile(kubeconfig)
	if err != nil {
		return ""
	}

	// Extract current-context from YAML
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "current-context:") {
			context := strings.TrimSpace(strings.TrimPrefix(line, "current-context:"))
			context = strings.Trim(context, "\"")
			return context
		}
	}

	return ""
}

func (s *Statusline) getTokenMetrics(transcriptPath string) TokenMetrics {
	content, err := s.deps.FileReader.ReadFile(transcriptPath)
	if err != nil {
		return TokenMetrics{}
	}

	// Parse JSONL transcript file
	lines := strings.Split(string(content), "\n")
	metrics := TokenMetrics{}

	for _, line := range lines {
		if line == "" {
			continue
		}

		var msg struct {
			Message struct {
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}

		unmarshalErr := json.Unmarshal([]byte(line), &msg)
		if unmarshalErr == nil && msg.Message.Usage.InputTokens > 0 {
			metrics.InputTokens += msg.Message.Usage.InputTokens
			metrics.OutputTokens += msg.Message.Usage.OutputTokens
			metrics.CachedTokens += msg.Message.Usage.CacheReadInputTokens
		}
	}

	// Context length is the total of all input and output tokens
	metrics.ContextLength = metrics.InputTokens + metrics.OutputTokens

	return metrics
}

func (s *Statusline) getHostname() string {
	// Check for test override
	if override := s.deps.EnvReader.Get("CLAUDE_STATUSLINE_HOSTNAME"); override != "" {
		return override
	}

	if hostname := s.deps.EnvReader.Get("HOSTNAME"); hostname != "" {
		return hostname
	}

	// Try to get hostname from command
	output, err := s.deps.CommandRunner.Run("hostname", "-s")
	if err == nil && len(output) > 0 {
		if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
			return trimmed
		}
	}

	output, err = s.deps.CommandRunner.Run("hostname")
	if err == nil && len(output) > 0 {
		if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
			return trimmed
		}
	}

	return "unknown"
}

func (s *Statusline) getDevspace() (string, string) {
	// Check for test override
	var tmuxDevspace string
	if override := s.deps.EnvReader.Get("CLAUDE_STATUSLINE_DEVSPACE"); override != "" {
		tmuxDevspace = override
	} else {
		tmuxDevspace = s.deps.EnvReader.Get("TMUX_DEVSPACE")
	}

	if tmuxDevspace == "" || tmuxDevspace == "-TMUX_DEVSPACE" {
		return "", ""
	}

	var symbol string
	switch tmuxDevspace {
	case "mercury":
		symbol = "☿"
	case "venus":
		symbol = "♀"
	case "earth":
		symbol = "♁"
	case "mars":
		symbol = "♂"
	case "jupiter":
		symbol = "♃"
	default:
		symbol = "●"
	}

	return symbol + " " + tmuxDevspace, symbol
}

func (s *Statusline) getColorBG(color string) string {
	switch color {
	case "mauve":
		return s.colors.MauveBG()
	case "rosewater":
		return s.colors.RosewaterBG()
	case "sky":
		return s.colors.SkyBG()
	case "peach":
		return s.colors.PeachBG()
	case "teal":
		return s.colors.TealBG()
	default:
		return ""
	}
}

func (s *Statusline) getColorFG(color string) string {
	switch color {
	case "mauve":
		return s.colors.MauveFG()
	case "rosewater":
		return s.colors.RosewaterFG()
	case "sky":
		return s.colors.SkyFG()
	case "peach":
		return s.colors.PeachFG()
	case "teal":
		return s.colors.TealFG()
	default:
		return ""
	}
}

// GitInfo contains git repository information.
type GitInfo struct {
	Branch string
	Status string
}

// Component represents a statusline component.
type Component struct {
	Color string
	Text  string
}
