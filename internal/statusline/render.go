package statusline

import (
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Render renders the statusline with lipgloss styling and guaranteed fixed width
func (s *Statusline) Render(data *CachedData) string {
	termWidth := data.TermWidth
	if termWidth == 0 {
		termWidth = 200
	}

	// Initialize colors
	s.colors = CatppuccinMocha{}

	// Select random model icon
	icons := []rune(ModelIcons)
	modelIcon := string(icons[rand.Intn(len(icons))])

	// Format directory path
	dirPath := formatPath(data.CurrentDir)

	// Determine if we're in compact mode
	isCompact := data.ContextLength >= 128000

	// Calculate spacer widths first
	leftSpacerWidth := 0
	if s.config.LeftSpacerWidth > 0 {
		leftSpacerWidth = s.config.LeftSpacerWidth
	}

	rightSpacerWidth := 0
	// Only add right spacer when not in compact mode
	if !isCompact && s.config.RightSpacerWidth > 0 {
		rightSpacerWidth = s.config.RightSpacerWidth
	}

	// Calculate effective width after spacers and compact mode
	effectiveWidth := termWidth - leftSpacerWidth - rightSpacerWidth
	if isCompact {
		// In compact mode, also reserve 41 chars for "     Context left until auto-compact: XX%"
		effectiveWidth = effectiveWidth - 41
	}

	// Content width is now the same as effective width (since spacers are already accounted for)
	contentWidth := effectiveWidth

	// Ensure we have at least some width for content
	if contentWidth < 20 {
		// Extreme case: spacers are taking up too much space
		// Force a minimum content width and reduce spacers if necessary
		contentWidth = 20
		totalSpacerBudget := effectiveWidth - contentWidth
		if totalSpacerBudget < leftSpacerWidth+rightSpacerWidth {
			// Scale down spacers proportionally
			if totalSpacerBudget > 0 {
				leftSpacerWidth = totalSpacerBudget * leftSpacerWidth / (leftSpacerWidth + rightSpacerWidth)
				rightSpacerWidth = totalSpacerBudget - leftSpacerWidth
			} else {
				leftSpacerWidth = 0
				rightSpacerWidth = 0
			}
		}
	}

	// Debug terminal width
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "Render: termWidth=%d, isCompact=%v, effectiveWidth=%d, spacers(L:%d,R:%d), contentWidth=%d\n",
			data.TermWidth, isCompact, effectiveWidth, leftSpacerWidth, rightSpacerWidth, contentWidth)
	}

	// Build components with proper sizing that accounts for spacers
	leftSection := s.buildLeftSection(dirPath, data.ModelDisplay, modelIcon, data, isCompact, contentWidth)
	rightSection := s.buildRightSection(data, isCompact, contentWidth)

	// Spacers are width constraints, not visible spaces
	// Calculate actual widths (stripping ANSI) without adding spacer widths
	leftWidth := runewidth.StringWidth(stripAnsi(leftSection))
	rightWidth := runewidth.StringWidth(stripAnsi(rightSection))

	// Calculate middle section width using the effective width
	middleWidth := effectiveWidth - leftWidth - rightWidth
	if middleWidth < 0 {
		middleWidth = 0
	}

	// Debug
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "effectiveWidth=%d, leftWidth=%d, rightWidth=%d, middleWidth=%d\n",
			effectiveWidth, leftWidth, rightWidth, middleWidth)
	}

	// Create middle section (context bar or spacing)
	middleSection := s.buildMiddleSection(data, middleWidth, isCompact)

	// Combine all sections (no visible spacers - they're just width constraints)
	// Start with a color reset to ensure clean state regardless of what Claude Code has done
	result := s.colors.NC() + leftSection + middleSection + rightSection

	// Debug each section
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "Final section widths: left=%d, middle=%d, right=%d, total=%d (contentWidth=%d)\n",
			runewidth.StringWidth(stripAnsi(leftSection)),
			runewidth.StringWidth(stripAnsi(middleSection)),
			runewidth.StringWidth(stripAnsi(rightSection)),
			runewidth.StringWidth(stripAnsi(leftSection))+runewidth.StringWidth(stripAnsi(middleSection))+runewidth.StringWidth(stripAnsi(rightSection)),
			contentWidth)
	}

	// Ensure exact width by padding
	// The statusline should be shorter than terminal width by the spacer amounts
	// In compact mode, we already calculated effectiveWidth with spacers
	targetWidth := effectiveWidth

	actualWidth := runewidth.StringWidth(stripAnsi(result))
	if actualWidth < targetWidth {
		if os.Getenv("DEBUG_WIDTH") == "1" {
			fmt.Fprintf(os.Stderr, "Adding padding: actualWidth=%d, targetWidth=%d, padding=%d\n",
				actualWidth, targetWidth, targetWidth-actualWidth)
		}
		result += strings.Repeat(" ", targetWidth-actualWidth)
	}

	return result
}

func (s *Statusline) buildLeftSection(dirPath, modelDisplay, modelIcon string, data *CachedData, isCompact bool, availableWidth int) string {
	// Calculate proportional truncation lengths based on available width
	// Default allocations when width is sufficient
	var dirMaxLen, modelMaxLen int

	// Base minimum sizes
	minDirLen := 10
	minModelLen := 10

	if isCompact {
		// In compact mode, use tighter defaults
		dirMaxLen, modelMaxLen = 25, 30
	} else {
		// Normal mode defaults - increased since components get priority
		dirMaxLen, modelMaxLen = 40, 40
	}

	// If available width is very constrained, scale down proportionally
	// Reserve space for: curves(2) + chevrons(2) + spaces(6) + icon(2) + tokens(~10) = ~22 chars overhead
	overhead := 22
	if data.InputTokens > 0 || data.OutputTokens > 0 {
		overhead += 10 // Extra space for token display
	}

	// Don't artificially limit the left section - let it use space it needs
	// Only constrain if we're running out of total space
	availableForText := availableWidth
	if availableForText < overhead+minDirLen+minModelLen {
		// Very constrained - use minimum sizes but ensure we fit
		totalMin := overhead + minDirLen + minModelLen
		if totalMin > availableForText {
			// Even minimums don't fit - scale them down
			scaleRatio := float64(availableForText-overhead) / float64(minDirLen+minModelLen)
			dirMaxLen = int(float64(minDirLen) * scaleRatio)
			modelMaxLen = int(float64(minModelLen) * scaleRatio)
			if dirMaxLen < 5 {
				dirMaxLen = 5
			}
			if modelMaxLen < 5 {
				modelMaxLen = 5
			}
		} else {
			dirMaxLen = minDirLen
			modelMaxLen = minModelLen
		}
	} else if availableForText < overhead+dirMaxLen+modelMaxLen {
		// Scale down proportionally
		textBudget := availableForText - overhead
		dirMaxLen = textBudget * 40 / 100   // 40% for directory
		modelMaxLen = textBudget * 60 / 100 // 60% for model
		if dirMaxLen < minDirLen {
			dirMaxLen = minDirLen
		}
		if modelMaxLen < minModelLen {
			modelMaxLen = minModelLen
		}
	}

	dirPath = truncateText(dirPath, dirMaxLen)
	modelDisplay = truncateText(modelDisplay, modelMaxLen)

	var sb strings.Builder

	// Left curve
	sb.WriteString(s.colors.LavenderFG())
	sb.WriteString(LeftCurve)

	// Directory section
	sb.WriteString(s.colors.LavenderBG())
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(" ")
	sb.WriteString(dirPath)
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	// Chevron to model section
	sb.WriteString(s.colors.SkyBG())
	sb.WriteString(s.colors.LavenderFG())
	sb.WriteString(LeftChevron)
	sb.WriteString(s.colors.NC())

	// Model section
	sb.WriteString(s.colors.SkyBG())
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(" ")
	sb.WriteString(modelIcon)
	sb.WriteString(" ")
	sb.WriteString(modelDisplay)

	// Add tokens if present
	if data.InputTokens > 0 || data.OutputTokens > 0 {
		sb.WriteString(fmt.Sprintf(" ↑%s ↓%s",
			formatTokens(data.InputTokens),
			formatTokens(data.OutputTokens)))
	}

	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	// End chevron
	sb.WriteString(s.colors.SkyFG())
	sb.WriteString(LeftChevron)
	sb.WriteString(s.colors.NC())

	return sb.String()
}

func (s *Statusline) buildRightSection(data *CachedData, isCompact bool, availableWidth int) string {
	// Calculate proportional max lengths based on available width
	var hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen int

	// Base minimum sizes
	minHostnameLen := 8
	minBranchLen := 10
	minAwsLen := 8
	minK8sLen := 8
	minDevspaceLen := 6

	if isCompact {
		hostnameMaxLen, branchMaxLen = 20, 25
		awsMaxLen, k8sMaxLen, devspaceMaxLen = 20, 20, 15
	} else {
		// Increased defaults since components get priority over context bar
		hostnameMaxLen, branchMaxLen = 35, 40
		awsMaxLen, k8sMaxLen, devspaceMaxLen = 35, 35, 30
	}

	// Don't artificially limit the right section - let it use space it needs
	availableForRight := availableWidth

	// Count how many components we'll show
	componentCount := 0
	if data.Devspace != "" {
		componentCount++
	}
	if data.Hostname != "" {
		componentCount++
	}
	if data.GitBranch != "" {
		componentCount++
	}
	awsProfile := s.deps.EnvReader.Get("AWS_PROFILE")
	if awsProfile != "" {
		componentCount++
	}
	if data.K8sContext != "" {
		componentCount++
	}

	if componentCount > 0 {
		// Reserve space for separators, curves, spaces, and icons
		overhead := componentCount*5 + 4 // Roughly 5 chars per component for formatting + 4 for curves

		availableForText := availableForRight - overhead
		if availableForText < 30 { // Very constrained
			// Use minimum sizes
			hostnameMaxLen = minHostnameLen
			branchMaxLen = minBranchLen
			awsMaxLen = minAwsLen
			k8sMaxLen = minK8sLen
			devspaceMaxLen = minDevspaceLen
		} else if availableForText < (hostnameMaxLen + branchMaxLen + awsMaxLen + k8sMaxLen + devspaceMaxLen) {
			// Scale down proportionally
			perComponent := availableForText / componentCount
			hostnameMaxLen = perComponent
			branchMaxLen = perComponent
			awsMaxLen = perComponent
			k8sMaxLen = perComponent
			devspaceMaxLen = perComponent

			// Ensure minimums
			if hostnameMaxLen < minHostnameLen {
				hostnameMaxLen = minHostnameLen
			}
			if branchMaxLen < minBranchLen {
				branchMaxLen = minBranchLen
			}
			if awsMaxLen < minAwsLen {
				awsMaxLen = minAwsLen
			}
			if k8sMaxLen < minK8sLen {
				k8sMaxLen = minK8sLen
			}
			if devspaceMaxLen < minDevspaceLen {
				devspaceMaxLen = minDevspaceLen
			}
		}
	}

	var components []Component

	// Add devspace
	if data.Devspace != "" {
		devspace := truncateText(data.Devspace, devspaceMaxLen)
		components = append(components, Component{"mauve", devspace})
	}

	// Add hostname
	if data.Hostname != "" {
		hostname := truncateText(data.Hostname, hostnameMaxLen)
		text := HostnameIcon + hostname
		components = append(components, Component{"rosewater", text})
	}

	// Add git
	if data.GitBranch != "" {
		branch := truncateText(data.GitBranch, branchMaxLen)
		text := GitIcon + branch
		if data.GitStatus != "" {
			text += " " + data.GitStatus
		}
		components = append(components, Component{"sky", text})
	}

	// Add AWS
	awsProfile = strings.TrimPrefix(awsProfile, "export AWS_PROFILE=")
	if awsProfile != "" {
		awsProfile = truncateText(awsProfile, awsMaxLen)
		components = append(components, Component{"peach", AwsIcon + awsProfile})
	}

	// Add K8s
	if data.K8sContext != "" {
		k8s := data.K8sContext
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
	}

	return sb.String()
}

func (s *Statusline) buildMiddleSection(data *CachedData, width int, isCompact bool) string {
	if width <= 0 {
		return ""
	}

	// Context bar only appears if there's at least 25 chars of space left after components
	// This ensures components get priority for space
	minContextBarWidth := 25
	if data.ContextLength > 0 && width >= minContextBarWidth {
		return s.createContextBar(data.ContextLength, width)
	}

	// Otherwise just spaces
	return strings.Repeat(" ", width)
}

func (s *Statusline) createContextBar(contextLength, barWidth int) string {
	// Always reserve 4 spaces padding on each side
	const contextBarPadding = 4

	// Calculate available width for the actual bar after padding
	availableForBar := barWidth - (contextBarPadding * 2) // 4 spaces left, 4 spaces right
	if availableForBar < 15 {                             // Minimum sensible bar size
		// Not enough space for a meaningful context bar
		return strings.Repeat(" ", barWidth)
	}

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
	textLength := runewidth.StringWidth(label) + runewidth.StringWidth(percentText)

	// Debug context bar calculations
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "createContextBar: barWidth=%d, availableForBar=%d, label='%s' width=%d, percentText='%s' width=%d, textLength=%d\n",
			barWidth, availableForBar, label, runewidth.StringWidth(label), percentText, runewidth.StringWidth(percentText), textLength)
	}

	// Calculate fill width from available bar space
	fillWidth := availableForBar - textLength - 2 // -2 for curves
	if fillWidth < 4 {
		// Not enough space for progress bar
		return strings.Repeat(" ", barWidth)
	}

	// Calculate filled portion
	filledWidth := int(float64(fillWidth) * percentage / 100.0)

	// Build the progress bar with proper characters
	var bar strings.Builder
	for i := 0; i < fillWidth; i++ {
		var char string
		if i == 0 {
			if filledWidth > 0 {
				char = ProgressLeftFull
			} else {
				char = ProgressLeftEmpty
			}
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

		// Apply colors - all progress bar characters use the muted background
		bar.WriteString(fgLightBg)
		bar.WriteString(fgColor)
		bar.WriteString(char)
		bar.WriteString(s.colors.NC())
	}

	// Build complete context bar with fixed padding
	var result strings.Builder

	// Always use exactly 4 spaces of padding on each side
	leftPad := contextBarPadding
	rightPad := contextBarPadding

	// Debug padding calculations
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(os.Stderr, "  fillWidth=%d, leftPad=%d, rightPad=%d\n",
			fillWidth, leftPad, rightPad)
	}

	// Add left padding (always 4 spaces)
	result.WriteString(strings.Repeat(" ", leftPad))

	// Start curve
	result.WriteString(fgColor)
	result.WriteString(LeftCurve)
	result.WriteString(s.colors.NC())

	// Label
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(label)
	result.WriteString(s.colors.NC())

	// Progress bar
	result.WriteString(bar.String())

	// Percentage
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(percentText)
	result.WriteString(s.colors.NC())

	// End curve
	result.WriteString(fgColor)
	result.WriteString(RightCurve)
	result.WriteString(s.colors.NC())

	// Add right padding (always 4 spaces)
	result.WriteString(strings.Repeat(" ", rightPad))

	// Debug final result
	finalResult := result.String()
	if os.Getenv("DEBUG_WIDTH") == "1" {
		finalWidth := runewidth.StringWidth(stripAnsi(finalResult))
		fmt.Fprintf(os.Stderr, "  context bar final width=%d (should be %d)\n", finalWidth, barWidth)
		if finalWidth != barWidth {
			fmt.Fprintf(os.Stderr, "  WARNING: Context bar width mismatch!\n")
		}
	}

	return result.String()
}
