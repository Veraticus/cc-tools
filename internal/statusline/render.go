package statusline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Render renders the statusline with lipgloss styling and guaranteed fixed width.
func (s *Statusline) Render(data *CachedData) string {
	termWidth := s.getTermWidth(data)
	s.colors = CatppuccinMocha{}
	modelIcon := s.selectModelIcon()
	dirPath := formatPath(data.CurrentDir)
	isCompact := s.isCompactMode(data.ContextLength)

	// Calculate widths
	leftSpacerWidth, rightSpacerWidth, contentWidth := s.calculateWidths(termWidth, isCompact)
	effectiveWidth := termWidth - leftSpacerWidth - rightSpacerWidth

	// Debug terminal width
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(
			os.Stderr,
			"Render: termWidth=%d, isCompact=%v, effectiveWidth=%d, spacers(L:%d,R:%d), contentWidth=%d\n",
			data.TermWidth,
			isCompact,
			effectiveWidth,
			leftSpacerWidth,
			rightSpacerWidth,
			contentWidth,
		)
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

	// Combine all sections (spacers are width constraints, not visible spaces)
	// Start with a color reset to ensure clean state regardless of what Claude Code has done
	result := s.colors.NC() + leftSection + middleSection + rightSection

	// Debug each section
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(
			os.Stderr,
			"Final section widths: left=%d, middle=%d, right=%d, total=%d (contentWidth=%d)\n",
			runewidth.StringWidth(stripAnsi(leftSection)),
			runewidth.StringWidth(stripAnsi(middleSection)),
			runewidth.StringWidth(stripAnsi(rightSection)),
			runewidth.StringWidth(
				stripAnsi(leftSection),
			)+runewidth.StringWidth(
				stripAnsi(middleSection),
			)+runewidth.StringWidth(
				stripAnsi(rightSection),
			),
			contentWidth,
		)
	}

	// Don't pad - the spacers are meant to make the statusline shorter
	// Just return the result as-is
	if os.Getenv("DEBUG_WIDTH") == "1" {
		actualWidth := runewidth.StringWidth(stripAnsi(result))
		fmt.Fprintf(os.Stderr, "Final width: actualWidth=%d, effectiveWidth=%d\n",
			actualWidth, effectiveWidth)
	}

	return result
}

func (s *Statusline) buildLeftSection(
	dirPath, modelDisplay, modelIcon string,
	data *CachedData,
	isCompact bool,
	availableWidth int,
) string {
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
	dirMaxLen, modelMaxLen = s.calculateTextLengths(
		availableForText, overhead,
		dirMaxLen, modelMaxLen,
		minDirLen, minModelLen,
	)

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
	maxLengths := s.getRightSectionMaxLengths(isCompact)
	awsProfile := s.deps.EnvReader.Get("AWS_PROFILE")
	componentCount := s.countRightComponents(data, awsProfile)

	if componentCount > 0 {
		maxLengths = s.adjustComponentMaxLengths(maxLengths, componentCount, availableWidth)
	}

	components := s.collectRightComponents(data, awsProfile, maxLengths)
	return s.renderComponents(components)
}

type componentMaxLengths struct {
	hostname int
	branch   int
	aws      int
	k8s      int
	devspace int
}

func (s *Statusline) getRightSectionMaxLengths(isCompact bool) componentMaxLengths {
	const (
		compactHostname = 20
		compactBranch   = 25
		compactAWS      = 20
		compactK8s      = 20
		compactDevspace = 15
		normalHostname  = 35
		normalBranch    = 40
		normalAWS       = 35
		normalK8s       = 35
		normalDevspace  = 30
	)

	if isCompact {
		return componentMaxLengths{
			hostname: compactHostname,
			branch:   compactBranch,
			aws:      compactAWS,
			k8s:      compactK8s,
			devspace: compactDevspace,
		}
	}
	return componentMaxLengths{
		hostname: normalHostname,
		branch:   normalBranch,
		aws:      normalAWS,
		k8s:      normalK8s,
		devspace: normalDevspace,
	}
}

func (s *Statusline) countRightComponents(data *CachedData, awsProfile string) int {
	count := 0
	if data.Devspace != "" {
		count++
	}
	if data.Hostname != "" {
		count++
	}
	if data.GitBranch != "" {
		count++
	}
	if awsProfile != "" {
		count++
	}
	if data.K8sContext != "" {
		count++
	}
	return count
}

func (s *Statusline) adjustComponentMaxLengths(
	maxLengths componentMaxLengths,
	componentCount, availableWidth int,
) componentMaxLengths {
	const (
		minHostnameLen = 8
		minBranchLen   = 10
		minAwsLen      = 8
		minK8sLen      = 8
		minDevspaceLen = 6
	)

	hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen := s.calculateComponentSizes(
		componentCount, availableWidth,
		maxLengths.hostname, maxLengths.branch, maxLengths.aws, maxLengths.k8s, maxLengths.devspace,
		minHostnameLen, minBranchLen, minAwsLen, minK8sLen, minDevspaceLen,
	)

	return componentMaxLengths{
		hostname: hostnameMaxLen,
		branch:   branchMaxLen,
		aws:      awsMaxLen,
		k8s:      k8sMaxLen,
		devspace: devspaceMaxLen,
	}
}

func (s *Statusline) collectRightComponents(
	data *CachedData,
	awsProfile string,
	maxLengths componentMaxLengths,
) []Component {
	var components []Component

	if data.Devspace != "" {
		devspace := truncateText(data.Devspace, maxLengths.devspace)
		components = append(components, Component{"mauve", devspace})
	}

	if data.Hostname != "" {
		hostname := truncateText(data.Hostname, maxLengths.hostname)
		components = append(components, Component{"rosewater", HostnameIcon + hostname})
	}

	if data.GitBranch != "" {
		components = append(components, s.createGitComponent(data, maxLengths.branch))
	}

	if awsProfile != "" {
		components = append(components, s.createAwsComponent(awsProfile, maxLengths.aws))
	}

	if data.K8sContext != "" {
		components = append(components, s.createK8sComponent(data.K8sContext, maxLengths.k8s))
	}

	return components
}

func (s *Statusline) createGitComponent(data *CachedData, maxLen int) Component {
	branch := truncateText(data.GitBranch, maxLen)
	text := GitIcon + branch
	if data.GitStatus != "" {
		text += " " + data.GitStatus
	}
	return Component{"sky", text}
}

func (s *Statusline) createAwsComponent(awsProfile string, maxLen int) Component {
	awsProfile = strings.TrimPrefix(awsProfile, "export AWS_PROFILE=")
	awsProfile = truncateText(awsProfile, maxLen)
	return Component{"peach", AwsIcon + awsProfile}
}

func (s *Statusline) createK8sComponent(k8sContext string, maxLen int) Component {
	k8s := k8sContext
	k8s = strings.TrimPrefix(k8s, "arn:aws:eks:*:*:cluster/")
	k8s = strings.TrimPrefix(k8s, "gke_*_*_")
	k8s = truncateText(k8s, maxLen)
	return Component{"teal", K8sIcon + k8s}
}

func (s *Statusline) renderComponents(components []Component) string {
	if len(components) == 0 {
		return ""
	}

	var sb strings.Builder
	var prevColor string

	for i, comp := range components {
		s.renderComponentSeparator(&sb, i, comp.Color, prevColor)
		s.renderComponentContent(&sb, comp)
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

func (s *Statusline) renderComponentSeparator(sb *strings.Builder, index int, color, prevColor string) {
	if index == 0 {
		sb.WriteString(s.getColorFG(color))
		sb.WriteString(RightChevron)
		sb.WriteString(s.colors.NC())
	} else {
		sb.WriteString(s.getColorBG(prevColor))
		sb.WriteString(s.getColorFG(color))
		sb.WriteString(RightChevron)
		sb.WriteString(s.colors.NC())
	}
}

func (s *Statusline) renderComponentContent(sb *strings.Builder, comp Component) {
	sb.WriteString(s.getColorBG(comp.Color))
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(" ")
	sb.WriteString(comp.Text)
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())
}

func (s *Statusline) buildMiddleSection(data *CachedData, width int, _ bool) string { // isCompact unused
	if width <= 0 {
		return ""
	}

	// Context bar only appears if there's at least 25 chars of space left after components
	// This ensures components get priority for space
	const minContextBarWidth = 25
	if data.ContextLength > 0 && width >= minContextBarWidth {
		return s.createContextBar(data.ContextLength, width)
	}

	// Otherwise just spaces
	return strings.Repeat(" ", width)
}

func (s *Statusline) createContextBar(contextLength, barWidth int) string {
	availableForBar := s.calculateAvailableBarWidth(barWidth)
	const minSensibleBarSize = 15
	if availableForBar < minSensibleBarSize {
		return strings.Repeat(" ", barWidth)
	}

	percentage := s.calculateContextPercentage(contextLength)
	bgColor, fgColor, fgLightBg := s.getContextColors(percentage)

	barInfo := s.prepareContextBarInfo(percentage, availableForBar)
	const minFillWidth = 4
	if barInfo.fillWidth < minFillWidth {
		return strings.Repeat(" ", barWidth)
	}

	s.debugContextBarInfo(barWidth, availableForBar, barInfo)

	progressBar := s.buildProgressBar(barInfo.fillWidth, barInfo.filledWidth, fgColor, fgLightBg)
	return s.assembleContextBar(barInfo, bgColor, fgColor, progressBar, barWidth)
}

type contextBarInfo struct {
	label       string
	percentText string
	textLength  int
	fillWidth   int
	filledWidth int
}

func (s *Statusline) prepareContextBarInfo(percentage float64, availableForBar int) contextBarInfo {
	label := ContextIcon + "Context "
	percentText := fmt.Sprintf(" %.1f%%", percentage)
	textLength := runewidth.StringWidth(label) + runewidth.StringWidth(percentText)

	const curvesWidth = 2
	fillWidth := availableForBar - textLength - curvesWidth
	const percentDivisor = 100.0
	filledWidth := int(float64(fillWidth) * percentage / percentDivisor)

	return contextBarInfo{
		label:       label,
		percentText: percentText,
		textLength:  textLength,
		fillWidth:   fillWidth,
		filledWidth: filledWidth,
	}
}

func (s *Statusline) debugContextBarInfo(barWidth, availableForBar int, info contextBarInfo) {
	if os.Getenv("DEBUG_WIDTH") != "1" {
		return
	}
	fmt.Fprintf(
		os.Stderr,
		"createContextBar: barWidth=%d, availableForBar=%d, label='%s' width=%d, percentText='%s' width=%d, textLength=%d\n",
		barWidth,
		availableForBar,
		info.label,
		runewidth.StringWidth(info.label),
		info.percentText,
		runewidth.StringWidth(info.percentText),
		info.textLength,
	)
	fmt.Fprintf(os.Stderr, "  fillWidth=%d, leftPad=4, rightPad=4\n", info.fillWidth)
}

func (s *Statusline) buildProgressBar(fillWidth, filledWidth int, fgColor, fgLightBg string) string {
	var bar strings.Builder
	for i := range fillWidth {
		char := selectProgressChar(i, fillWidth, filledWidth)
		bar.WriteString(fgLightBg)
		bar.WriteString(fgColor)
		bar.WriteString(char)
		bar.WriteString(s.colors.NC())
	}
	return bar.String()
}

func selectProgressChar(position, fillWidth, filledWidth int) string {
	switch position {
	case 0:
		if filledWidth > 0 {
			return ProgressLeftFull
		}
		return ProgressLeftEmpty
	case fillWidth - 1:
		if position < filledWidth {
			return ProgressRightFull
		}
		return ProgressRightEmpty
	default:
		if position < filledWidth {
			return ProgressMidFull
		}
		return ProgressMidEmpty
	}
}

func (s *Statusline) assembleContextBar(
	info contextBarInfo,
	bgColor, fgColor, progressBar string,
	barWidth int,
) string {
	var result strings.Builder
	const contextBarPadding = 4

	// Left padding
	result.WriteString(strings.Repeat(" ", contextBarPadding))

	// Start curve
	result.WriteString(fgColor)
	result.WriteString(LeftCurve)
	result.WriteString(s.colors.NC())

	// Label
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(info.label)
	result.WriteString(s.colors.NC())

	// Progress bar
	result.WriteString(progressBar)

	// Percentage
	result.WriteString(bgColor)
	result.WriteString(s.colors.BaseFG())
	result.WriteString(info.percentText)
	result.WriteString(s.colors.NC())

	// End curve
	result.WriteString(fgColor)
	result.WriteString(RightCurve)
	result.WriteString(s.colors.NC())

	// Right padding
	result.WriteString(strings.Repeat(" ", contextBarPadding))

	s.debugContextBarResult(result.String(), barWidth)
	return result.String()
}

func (s *Statusline) debugContextBarResult(finalResult string, barWidth int) {
	if os.Getenv("DEBUG_WIDTH") != "1" {
		return
	}
	finalWidth := runewidth.StringWidth(stripAnsi(finalResult))
	fmt.Fprintf(os.Stderr, "  context bar final width=%d (should be %d)\n", finalWidth, barWidth)
	if finalWidth != barWidth {
		fmt.Fprintf(os.Stderr, "  WARNING: Context bar width mismatch!\n")
	}
}

func (s *Statusline) calculateTextLengths(
	availableForText, overhead int,
	dirMaxLen, modelMaxLen int,
	minDirLen, minModelLen int,
) (int, int) {
	if availableForText < overhead+minDirLen+minModelLen {
		return s.handleVeryConstrainedSpace(
			availableForText, overhead,
			minDirLen, minModelLen,
		)
	}

	if availableForText < overhead+dirMaxLen+modelMaxLen {
		return s.scaleDownProportionally(
			availableForText, overhead,
			minDirLen, minModelLen,
		)
	}

	return dirMaxLen, modelMaxLen
}

func (s *Statusline) handleVeryConstrainedSpace(
	availableForText, overhead int,
	minDirLen, minModelLen int,
) (int, int) {
	totalMin := overhead + minDirLen + minModelLen
	if totalMin > availableForText {
		// Even minimums don't fit - scale them down
		scaleRatio := float64(availableForText-overhead) / float64(minDirLen+minModelLen)
		dirMaxLen := int(float64(minDirLen) * scaleRatio)
		modelMaxLen := int(float64(minModelLen) * scaleRatio)
		const absoluteMinLen = 5
		if dirMaxLen < absoluteMinLen {
			dirMaxLen = absoluteMinLen
		}
		if modelMaxLen < absoluteMinLen {
			modelMaxLen = absoluteMinLen
		}
		return dirMaxLen, modelMaxLen
	}
	return minDirLen, minModelLen
}

func (s *Statusline) scaleDownProportionally(
	availableForText, overhead int,
	minDirLen, minModelLen int,
) (int, int) {
	const (
		dirPercent     = 40
		modelPercent   = 60
		percentDivisor = 100
	)
	textBudget := availableForText - overhead
	dirMaxLen := textBudget * dirPercent / percentDivisor
	modelMaxLen := textBudget * modelPercent / percentDivisor
	if dirMaxLen < minDirLen {
		dirMaxLen = minDirLen
	}
	if modelMaxLen < minModelLen {
		modelMaxLen = minModelLen
	}
	return dirMaxLen, modelMaxLen
}

func (s *Statusline) calculateComponentSizes(
	componentCount, availableForRight int,
	hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen int,
	minHostnameLen, minBranchLen, minAwsLen, minK8sLen, minDevspaceLen int,
) (int, int, int, int, int) {
	// Reserve space for separators, curves, spaces, and icons
	const (
		perComponentOverhead = 5
		curvesOverhead       = 4
		minAvailableForText  = 30
	)
	overhead := componentCount*perComponentOverhead + curvesOverhead
	availableForText := availableForRight - overhead

	if availableForText < minAvailableForText {
		// Very constrained - use minimum sizes
		return minHostnameLen, minBranchLen, minAwsLen, minK8sLen, minDevspaceLen
	}

	totalNeeded := hostnameMaxLen + branchMaxLen + awsMaxLen + k8sMaxLen + devspaceMaxLen
	if availableForText < totalNeeded {
		// Scale down proportionally
		perComponent := availableForText / componentCount
		return s.ensureMinimumSizes(
			perComponent, perComponent, perComponent, perComponent, perComponent,
			minHostnameLen, minBranchLen, minAwsLen, minK8sLen, minDevspaceLen,
		)
	}

	return hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen
}

func (s *Statusline) ensureMinimumSizes(
	hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen int,
	minHostnameLen, minBranchLen, minAwsLen, minK8sLen, minDevspaceLen int,
) (int, int, int, int, int) {
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
	return hostnameMaxLen, branchMaxLen, awsMaxLen, k8sMaxLen, devspaceMaxLen
}

func (s *Statusline) getTermWidth(data *CachedData) int {
	const defaultTermWidth = 200
	if data.TermWidth == 0 {
		return defaultTermWidth
	}
	return data.TermWidth
}

func (s *Statusline) selectModelIcon() string {
	icons := []rune(ModelIcons)
	return string(icons[rand.IntN(len(icons))]) //nolint:gosec // Non-cryptographic randomness is fine for UI
}

func (s *Statusline) isCompactMode(contextLength int) bool {
	const maxContextLength = 200000
	const compactThresholdPercent = 85
	compactModeThreshold := (maxContextLength * compactThresholdPercent) / 100
	return contextLength >= compactModeThreshold
}

func (s *Statusline) calculateWidths(termWidth int, isCompact bool) (int, int, int) {
	leftSpacer := 0
	if s.config.LeftSpacerWidth > 0 {
		leftSpacer = s.config.LeftSpacerWidth
	}

	rightSpacer := 0
	if isCompact {
		// In compact mode, reserve 40 chars for auto-compact message
		rightSpacer = 40
	} else if s.config.RightSpacerWidth > 0 {
		rightSpacer = s.config.RightSpacerWidth
	}

	effectiveWidth := termWidth - leftSpacer - rightSpacer
	content := effectiveWidth

	const minContentWidth = 20
	if content < minContentWidth {
		content = minContentWidth
		totalSpacerBudget := effectiveWidth - content
		if totalSpacerBudget < leftSpacer+rightSpacer {
			if totalSpacerBudget > 0 {
				leftSpacer = totalSpacerBudget * leftSpacer / (leftSpacer + rightSpacer)
				rightSpacer = totalSpacerBudget - leftSpacer
			} else {
				leftSpacer = 0
				rightSpacer = 0
			}
		}
	}

	return leftSpacer, rightSpacer, content
}

func (s *Statusline) calculateAvailableBarWidth(barWidth int) int {
	const contextBarPadding = 4
	const paddingMultiplier = 2
	return barWidth - (contextBarPadding * paddingMultiplier)
}

func (s *Statusline) calculateContextPercentage(contextLength int) float64 {
	const autoCompactThreshold = 160000.0
	const maxPercentage = 100.0
	percentage := float64(contextLength) * maxPercentage / autoCompactThreshold
	if percentage > maxPercentage {
		return maxPercentage
	}
	return percentage
}

func (s *Statusline) getContextColors(percentage float64) (string, string, string) {
	const (
		greenThreshold  = 40.0
		yellowThreshold = 60.0
		peachThreshold  = 80.0
	)
	switch {
	case percentage < greenThreshold:
		return s.colors.GreenBG(), s.colors.GreenFG(), s.colors.GreenLightBG()
	case percentage < yellowThreshold:
		return s.colors.YellowBG(), s.colors.YellowFG(), s.colors.YellowLightBG()
	case percentage < peachThreshold:
		return s.colors.PeachBG(), s.colors.PeachFG(), s.colors.PeachLightBG()
	default:
		return s.colors.RedBG(), s.colors.RedFG(), s.colors.RedLightBG()
	}
}
