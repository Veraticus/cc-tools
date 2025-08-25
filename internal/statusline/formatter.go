package statusline

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/shared"
	"github.com/charmbracelet/lipgloss"
)

// Formatter handles statusline formatting.
type Formatter struct {
	input *hooks.StatusLineInput
	git   *GitInfo
}

// NewFormatter creates a new statusline formatter.
func NewFormatter(input *hooks.StatusLineInput) *Formatter {
	return &Formatter{
		input: input,
		git:   GetGitInfo(),
	}
}

// Format generates the formatted statusline.
func (f *Formatter) Format() string {
	segments := []string{}

	// Model segment
	modelSegment := f.formatModelSegment()
	if modelSegment != "" {
		segments = append(segments, modelSegment)
	}

	// Directory segment
	dirSegment := f.formatDirectorySegment()
	if dirSegment != "" {
		segments = append(segments, dirSegment)
	}

	// Git segment
	gitSegment := f.formatGitSegment()
	if gitSegment != "" {
		segments = append(segments, gitSegment)
	}

	// Cost segment (if significant)
	costSegment := f.formatCostSegment()
	if costSegment != "" {
		segments = append(segments, costSegment)
	}

	// Join segments with separators
	return f.joinSegments(segments)
}

// formatModelSegment formats the model display.
func (f *Formatter) formatModelSegment() string {
	if f.input == nil || f.input.Model.DisplayName == "" {
		return ""
	}

	// Choose color based on model
	var style lipgloss.Style
	modelName := strings.ToLower(f.input.Model.DisplayName)
	switch {
	case strings.Contains(modelName, "opus"):
		style = shared.MauveSegment
	case strings.Contains(modelName, "sonnet"):
		style = shared.SkySegment
	case strings.Contains(modelName, "haiku"):
		style = shared.TealSegment
	default:
		style = shared.LavenderSegment
	}

	return style.Render(fmt.Sprintf(" %s ", f.input.Model.DisplayName))
}

// formatDirectorySegment formats the current directory.
func (f *Formatter) formatDirectorySegment() string {
	if f.input == nil || f.input.Workspace.CurrentDir == "" {
		return ""
	}

	dir := filepath.Base(f.input.Workspace.CurrentDir)
	if dir == "/" || dir == "." {
		dir = f.input.Workspace.CurrentDir
	}

	// Truncate if too long
	const maxDirLength = 30
	if len(dir) > maxDirLength {
		dir = "..." + dir[len(dir)-maxDirLength+3:]
	}

	style := lipgloss.NewStyle().
		Background(shared.Base).
		Foreground(shared.Lavender).
		Padding(0, 1)

	return style.Render(fmt.Sprintf("📁 %s", dir))
}

// formatGitSegment formats git information.
func (f *Formatter) formatGitSegment() string {
	if f.git == nil || !f.git.IsGitRepo || f.git.Branch == "" {
		return ""
	}

	symbol := f.git.GetGitSymbol()

	// Choose color based on git status
	var color lipgloss.Color
	switch {
	case f.git.HasStaged:
		color = shared.Yellow
	case f.git.HasModified || f.git.HasUntracked:
		color = shared.Peach
	default:
		color = shared.Green
	}

	style := lipgloss.NewStyle().
		Background(shared.Base).
		Foreground(color).
		Padding(0, 1)

	return style.Render(fmt.Sprintf("%s %s", symbol, f.git.Branch))
}

// formatCostSegment formats cost information if significant.
func (f *Formatter) formatCostSegment() string {
	if f.input == nil || f.input.Cost.TotalCostUSD < 0.01 {
		return ""
	}

	style := lipgloss.NewStyle().
		Background(shared.Base).
		Foreground(shared.Rosewater).
		Padding(0, 1)

	// Format cost with appropriate precision
	var costStr string
	const smallCostThreshold = 0.10 // Threshold for cost display precision
	if f.input.Cost.TotalCostUSD < smallCostThreshold {
		costStr = fmt.Sprintf("$%.3f", f.input.Cost.TotalCostUSD)
	} else {
		costStr = fmt.Sprintf("$%.2f", f.input.Cost.TotalCostUSD)
	}

	return style.Render(costStr)
}

// joinSegments joins segments with powerline separators.
func (f *Formatter) joinSegments(segments []string) string {
	if len(segments) == 0 {
		return ""
	}

	result := segments[0]
	for i := 1; i < len(segments); i++ {
		// Add separator between segments
		separator := lipgloss.NewStyle().
			Foreground(shared.Base).
			Render(shared.RightChevron)
		result += separator + segments[i]
	}

	return result
}
