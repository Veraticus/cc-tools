package statusline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Veraticus/cc-tools/internal/aliases"
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
	ContextWindow struct {
		UsedPercentage    float64 `json:"used_percentage"`
		ContextWindowSize int     `json:"context_window_size"`
	} `json:"context_window"`
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

// CachedData represents cached statusline data.
type CachedData struct {
	ModelID        string
	ModelDisplay   string
	CurrentDir     string
	TranscriptPath string
	GitBranch      string
	GitStatus      string
	K8sContext     string
	GcloudProject  string
	UsedPercentage float64
	Hostname       string
	Devspace       string
	TermWidth      int
}

// Dependencies contains all external dependencies.
type Dependencies struct {
	FileReader    FileReader
	CommandRunner CommandRunner
	EnvReader     EnvReader
	TerminalWidth TerminalWidth
	Resolver      *aliases.Resolver
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
	LeftSpacerWidth  int
	RightSpacerWidth int
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	const (
		defaultLeftSpacerWidth  = 2
		defaultRightSpacerWidth = 2
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
	if deps != nil && deps.Resolver == nil {
		// Zero-value resolver: behaves identically to a missing alias file —
		// raw labels, default env patterns. Keeps test ergonomics simple.
		deps.Resolver, _ = aliases.NewResolver("")
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
		ModelID:        s.input.Model.ID,
	}

	// Model display name (abbreviated to first-letter + version, e.g.
	// "Sonnet 4.6 (1M Context)" → "S4.6").
	data.ModelDisplay = abbreviateModel(s.input.Model.DisplayName)

	// Git information
	gitInfo := s.getGitInfo(currentDir)
	data.GitBranch = gitInfo.Branch
	data.GitStatus = gitInfo.Status

	// Kubernetes context
	data.K8sContext = s.getK8sContext()

	// Gcloud project
	data.GcloudProject = s.getGcloudProject()

	// Context window percentage (directly from input)
	data.UsedPercentage = s.input.ContextWindow.UsedPercentage

	// Hostname
	data.Hostname = s.getHostname()

	// Devspace
	data.Devspace, _ = s.getDevspace()

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

	// Scan line-by-line with early exit. The file is read whole via the
	// FileReader contract, but we avoid allocating a slice of every line
	// via strings.Split — meaningful on hot-path renders with long
	// kubeconfigs.
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "current-context:") {
			context := strings.TrimSpace(strings.TrimPrefix(line, "current-context:"))
			return strings.Trim(context, "\"")
		}
	}

	return ""
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

	// Known planets render with their astronomical glyph and a 3-char
	// label — the glyph carries identity, the text just confirms it.
	// Arbitrary devspace names (e.g. branch names) use the generic ●
	// glyph and keep their full label since the name itself is the only
	// identifier.
	var symbol string
	knownPlanet := true
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
		knownPlanet = false
	}

	label := tmuxDevspace
	if knownPlanet {
		const devspaceLabelMax = 3
		if len(label) > devspaceLabelMax {
			label = label[:devspaceLabelMax]
		}
	}
	return symbol + " " + label, symbol
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
	case "red":
		return s.colors.RedBG()
	case "maroon":
		return s.colors.MaroonBG()
	case "yellow":
		return s.colors.YellowBG()
	case "green":
		return s.colors.GreenBG()
	case "lavender":
		return s.colors.LavenderBG()
	case "pink":
		return s.colors.PinkBG()
	case "sapphire":
		return s.colors.SapphireBG()
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
	case "red":
		return s.colors.RedFG()
	case "maroon":
		return s.colors.MaroonFG()
	case "yellow":
		return s.colors.YellowFG()
	case "green":
		return s.colors.GreenFG()
	case "lavender":
		return s.colors.LavenderFG()
	case "pink":
		return s.colors.PinkFG()
	case "sapphire":
		return s.colors.SapphireFG()
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
