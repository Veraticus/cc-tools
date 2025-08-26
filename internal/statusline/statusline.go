package statusline

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Input represents the JSON input from stdin
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

// TokenMetrics holds token usage information
type TokenMetrics struct {
	InputTokens   int
	OutputTokens  int
	CachedTokens  int
	ContextLength int
}

// CachedData represents cached statusline data
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

// Dependencies contains all external dependencies
type Dependencies struct {
	FileReader    FileReader
	CommandRunner CommandRunner
	EnvReader     EnvReader
	TerminalWidth TerminalWidth
	CacheDir      string
	CacheDuration time.Duration
}

// FileReader interface for reading files
type FileReader interface {
	ReadFile(path string) ([]byte, error)
	Exists(path string) bool
	ModTime(path string) (time.Time, error)
}

// CommandRunner interface for executing commands
type CommandRunner interface {
	Run(command string, args ...string) ([]byte, error)
}

// EnvReader interface for reading environment variables
type EnvReader interface {
	Get(key string) string
}

// TerminalWidth interface for getting terminal width
type TerminalWidth interface {
	GetWidth() int
}

// Statusline is the main statusline generator
type Statusline struct {
	deps   *Dependencies
	colors CatppuccinMocha
	input  *Input
}

// New creates a new Statusline instance
func New(deps *Dependencies) *Statusline {
	return &Statusline{
		deps: deps,
	}
}

// Generate generates the statusline from JSON input
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
	return decoder.Decode(s.input)
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

func (s *Statusline) generateCacheKey(dir string) string {
	h := md5.New()
	h.Write([]byte(dir))
	return hex.EncodeToString(h.Sum(nil))
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
			debug := fmt.Sprintf("DEBUG computeData: TranscriptPath=%s, InputTokens=%d, OutputTokens=%d, ContextLength=%d\n",
				data.TranscriptPath, data.InputTokens, data.OutputTokens, data.ContextLength)
			os.WriteFile("/tmp/compute_debug.txt", []byte(debug), 0644)
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
		if time.Since(modTime) < 60*time.Second {
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

	var lastMessage struct {
		Message struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}

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

		if err := json.Unmarshal([]byte(line), &msg); err == nil && msg.Message.Usage.InputTokens > 0 {
			metrics.InputTokens += msg.Message.Usage.InputTokens
			metrics.OutputTokens += msg.Message.Usage.OutputTokens
			metrics.CachedTokens += msg.Message.Usage.CacheReadInputTokens
			lastMessage = msg
		}
	}

	// Context length from last message
	if lastMessage.Message.Usage.InputTokens > 0 {
		metrics.ContextLength = lastMessage.Message.Usage.InputTokens +
			lastMessage.Message.Usage.CacheReadInputTokens +
			lastMessage.Message.Usage.CacheCreationInputTokens
	}

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
	if err == nil {
		return strings.TrimSpace(string(output))
	}

	output, err = s.deps.CommandRunner.Run("hostname")
	if err == nil {
		return strings.TrimSpace(string(output))
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

func (s *Statusline) buildStatuslineOld(data *CachedData) string {
	var sb strings.Builder

	// Select random model icon
	icons := []rune(ModelIcons)
	modelIcon := string(icons[rand.Intn(len(icons))]) + " "

	// Format directory path
	dirPath := formatPath(data.CurrentDir)

	// Truncation lengths based on context mode
	var dirMaxLen, modelMaxLen, hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen int
	if data.ContextLength >= 128000 {
		// Compact mode
		dirMaxLen, modelMaxLen, hostnameMaxLen = 15, 20, 15
		branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen = 20, 15, 15, 10
	} else {
		// Normal mode
		dirMaxLen, modelMaxLen, hostnameMaxLen = 20, 30, 25
		branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen = 30, 25, 25, 20
	}

	dirPath = truncateText(dirPath, dirMaxLen)
	modelDisplay := truncateText(data.ModelDisplay, modelMaxLen)

	// Build left section
	sb.WriteString(s.colors.NC()) // Reset
	sb.WriteString(s.colors.LavenderFG())
	sb.WriteString(LeftCurve)
	sb.WriteString(s.colors.LavenderBG())
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(" ")
	sb.WriteString(dirPath)
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	// Model/tokens section
	sb.WriteString(s.colors.SkyBG())
	sb.WriteString(s.colors.LavenderFG())
	sb.WriteString(LeftChevron)
	sb.WriteString(s.colors.NC())
	sb.WriteString(s.colors.SkyBG())
	sb.WriteString(s.colors.BaseFG())

	tokenInfo := " " + modelIcon + modelDisplay
	if data.InputTokens > 0 || data.OutputTokens > 0 {
		tokenInfo += fmt.Sprintf(" ↑%s ↓%s",
			formatTokens(data.InputTokens),
			formatTokens(data.OutputTokens))
	}
	sb.WriteString(tokenInfo)
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	// End left section
	sb.WriteString(s.colors.SkyFG())
	sb.WriteString(LeftChevron)
	sb.WriteString(s.colors.NC())

	// Build right side
	rightSide := s.buildRightSide(data, hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen)

	// Calculate lengths for spacing
	leftLength := calculateLeftLength(dirPath, tokenInfo, modelIcon)
	rightLength := calculateRightLength(data, rightSide)

	// Calculate middle section
	termWidth := data.TermWidth
	if termWidth == 0 {
		termWidth = 210 // Default
	}

	compactMessageWidth := 0
	if data.ContextLength >= 128000 {
		compactMessageWidth = 41
	}

	availableWidth := termWidth - compactMessageWidth
	spaceForMiddle := availableWidth - leftLength - rightLength

	// Create middle section (context bar or spacing)
	middleSection := s.createMiddleSection(data, spaceForMiddle)

	// Output final statusline
	sb.WriteString(middleSection)
	sb.WriteString(rightSide)

	return sb.String()
}

func (s *Statusline) buildRightSide(data *CachedData, hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen int) string {
	var components []Component

	// Add components
	if data.Devspace != "" {
		devspace := truncateText(data.Devspace, devspaceMaxLen)
		components = append(components, Component{"mauve", devspace})
	}

	if data.Hostname != "" {
		hostname := truncateText(data.Hostname, hostnameMaxLen)
		text := HostnameIcon + hostname
		components = append(components, Component{"rosewater", text})
	}

	if data.GitBranch != "" {
		branch := truncateText(data.GitBranch, branchMaxLen)
		text := GitIcon + branch
		if data.GitStatus != "" {
			text += " " + data.GitStatus
		}
		components = append(components, Component{"sky", text})
	}

	// AWS Profile
	awsProfile := s.deps.EnvReader.Get("AWS_PROFILE")
	// Remove "export AWS_PROFILE=" prefix if present
	awsProfile = strings.TrimPrefix(awsProfile, "export AWS_PROFILE=")
	if awsProfile != "" {
		awsProfile = truncateText(awsProfile, awsMaxLen)
		components = append(components, Component{"peach", AwsIcon + awsProfile})
	}

	// K8s context
	if data.K8sContext != "" {
		k8s := data.K8sContext
		// Shorten common patterns
		k8s = strings.TrimPrefix(k8s, "arn:aws:eks:*:*:cluster/")
		k8s = strings.TrimPrefix(k8s, "gke_*_*_")
		k8s = truncateText(k8s, k8sMaxLen)
		components = append(components, Component{"teal", K8sIcon + k8s})
	}

	// Build with powerline separators
	var sb strings.Builder
	var prevColor string

	for i, comp := range components {
		// Add separator
		if i == 0 {
			// First component
			sb.WriteString(s.getColorFG(comp.Color))
			sb.WriteString(RightChevron)
			sb.WriteString(s.colors.NC())
		} else {
			// Between components
			sb.WriteString(s.getColorBG(prevColor))
			sb.WriteString(s.getColorFG(comp.Color))
			sb.WriteString(RightChevron)
			sb.WriteString(s.colors.NC())
		}

		// Add content
		sb.WriteString(s.getColorBG(comp.Color))
		sb.WriteString(s.colors.BaseFG())
		sb.WriteString(" ")
		sb.WriteString(comp.Text)
		sb.WriteString(" ")
		sb.WriteString(s.colors.NC())

		prevColor = comp.Color
	}

	// Add end curve
	if prevColor != "" {
		sb.WriteString(s.getColorFG(prevColor))
		sb.WriteString(RightCurve)
		sb.WriteString(s.colors.NC())

		// Add space for compact mode
		if data.ContextLength >= 128000 {
			sb.WriteString(" ")
		}
	}

	return sb.String()
}

func (s *Statusline) createMiddleSection(data *CachedData, spaceForMiddle int) string {
	if data.ContextLength > 0 && spaceForMiddle > 20 {
		// Create context bar
		paddingTotal := 10
		barWidth := spaceForMiddle - paddingTotal

		if barWidth < 20 {
			paddingTotal = 4
			barWidth = spaceForMiddle - paddingTotal
		}

		if barWidth > 0 {
			bar := s.createContextBar(data.ContextLength, barWidth)
			leftPad := paddingTotal / 2
			rightPad := paddingTotal - leftPad
			return fmt.Sprintf("%*s%s%*s", leftPad, "", bar, rightPad, "")
		}
	}

	// Just spacing
	if data.ContextLength == 0 && spaceForMiddle > 0 {
		spaceForMiddle-- // Reduce by 1 to prevent wrapping
	}
	return fmt.Sprintf("%*s", spaceForMiddle, "")
}

func (s *Statusline) createContextBarOld(contextLength, barWidth int) string {
	// Calculate percentage (160k is auto-compact threshold)
	percentage := float64(contextLength) * 100.0 / 160000.0
	if percentage > 100 {
		percentage = 100
	}

	// Choose colors based on percentage
	var bgColor, fgColor, fgLightBg string
	if percentage < 40 {
		bgColor = s.colors.GreenBG()
		fgColor = s.colors.GreenFG()
		fgLightBg = s.colors.GreenLightBG()
	} else if percentage < 60 {
		bgColor = s.colors.YellowBG()
		fgColor = s.colors.YellowFG()
		fgLightBg = s.colors.YellowLightBG()
	} else if percentage < 80 {
		bgColor = s.colors.PeachBG()
		fgColor = s.colors.PeachFG()
		fgLightBg = s.colors.PeachLightBG()
	} else {
		bgColor = s.colors.RedBG()
		fgColor = s.colors.RedFG()
		fgLightBg = s.colors.RedLightBG()
	}

	label := ContextIcon + "Context "
	percentText := fmt.Sprintf(" %.1f%%", percentage)
	textLength := len(label) + len(percentText) + 2 // +2 for icon width and space

	fillWidth := barWidth - textLength - 2 // -2 for curves
	if fillWidth < 4 {
		return ""
	}

	filledWidth := int(float64(fillWidth) * percentage / 100.0)

	// Build bar
	var bar strings.Builder
	for i := 0; i < fillWidth; i++ {
		var char string
		if i == 0 {
			char = ProgressLeftFull
		} else if i == fillWidth-1 {
			if i < filledWidth {
				char = ProgressRightFull
			} else {
				char = ProgressRightEmpty
			}
		} else {
			if i < filledWidth {
				char = ProgressMidFull
			} else {
				char = ProgressMidEmpty
			}
		}
		bar.WriteString(fgLightBg)
		bar.WriteString(fgColor)
		bar.WriteString(char)
		bar.WriteString(s.colors.NC())
	}

	// Build complete bar
	var result strings.Builder
	result.WriteString(fgColor)
	result.WriteString(LeftCurve)
	result.WriteString(s.colors.NC())
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(label)
	result.WriteString(s.colors.NC())
	result.WriteString(bar.String())
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(percentText)
	result.WriteString(" ")
	result.WriteString(s.colors.NC())
	result.WriteString(fgColor)
	result.WriteString(RightCurve)
	result.WriteString(s.colors.NC())

	return result.String()
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

// GitInfo contains git repository information
type GitInfo struct {
	Branch string
	Status string
}

// Component represents a statusline component
type Component struct {
	Color string
	Text  string
}
