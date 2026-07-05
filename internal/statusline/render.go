package statusline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/Veraticus/cc-tools/internal/aliases"
	"github.com/mattn/go-runewidth"
)

// Render renders the statusline with lipgloss styling and guaranteed fixed width.
func (s *Statusline) Render(data *CachedData) string {
	termWidth := s.getTermWidth(data)
	s.colors = CatppuccinMocha{}

	// Calculate widths up-front. Both narrow and wide rendering use
	// effectiveWidth (termWidth − spacers) as their actual budget;
	// the spacer convention is honored by both paths.
	leftSpacerWidth, rightSpacerWidth, contentWidth := s.calculateWidths(termWidth)
	effectiveWidth := termWidth - leftSpacerWidth - rightSpacerWidth

	// Narrow-mode dispatch: when the detected terminal width is
	// ≤ narrowWidthThreshold, render the phone-friendly edge-to-edge
	// chip chain instead of the wide chevron-chain layout. Activation
	// is automatic from the width detection; no setting required.
	if termWidth <= narrowWidthThreshold {
		return s.renderNarrow(data, effectiveWidth)
	}

	modelIcon := s.selectModelIcon()
	dirPath := formatPath(data.CurrentDir)

	// Debug terminal width
	if os.Getenv("DEBUG_WIDTH") == "1" {
		fmt.Fprintf(
			os.Stderr,
			"Render: termWidth=%d, effectiveWidth=%d, spacers(L:%d,R:%d), contentWidth=%d\n",
			data.TermWidth,
			effectiveWidth,
			leftSpacerWidth,
			rightSpacerWidth,
			contentWidth,
		)
	}

	// Build components with proper sizing that accounts for spacers
	leftSection := s.buildLeftSection(dirPath, data.ModelDisplay, modelIcon, contentWidth)
	rightSection := s.buildRightSection(data, contentWidth)

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
	middleSection := s.buildMiddleSection(data, middleWidth)

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
	availableWidth int,
) string {
	// Calculate proportional truncation lengths based on available width
	// Default allocations when width is sufficient
	minDirLen := 10
	minModelLen := 10
	dirMaxLen, modelMaxLen := 40, 40

	// If available width is very constrained, scale down proportionally
	// Reserve space for: curves(2) + chevrons(2) + spaces(6) + icon(2) = ~12 chars overhead
	overhead := 12

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
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	// End chevron
	sb.WriteString(s.colors.SkyFG())
	sb.WriteString(LeftChevron)
	sb.WriteString(s.colors.NC())

	return sb.String()
}

// awsProfileFromEnv reads AWS_PROFILE and strips the literal
// "export AWS_PROFILE=" prefix some misconfigured shells include.
// Shared by render.go and render_clouds.go so both paths agree on
// what the "profile value" is.
func awsProfileFromEnv(r EnvReader) string {
	return strings.TrimPrefix(r.Get("AWS_PROFILE"), "export AWS_PROFILE=")
}

func (s *Statusline) buildRightSection(data *CachedData, availableWidth int) string {
	maxLengths := s.getRightSectionMaxLengths()
	awsProfile := awsProfileFromEnv(s.deps.EnvReader)
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
	gcloud   int
	k8s      int
	devspace int
}

func (s *Statusline) getRightSectionMaxLengths() componentMaxLengths {
	const (
		maxHostname = 20
		maxBranch   = 25
		maxAWS      = 20
		maxGcloud   = 20
		maxK8s      = 20
		maxDevspace = 15
	)

	return componentMaxLengths{
		hostname: maxHostname,
		branch:   maxBranch,
		aws:      maxAWS,
		gcloud:   maxGcloud,
		k8s:      maxK8s,
		devspace: maxDevspace,
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
	if data.GcloudProject != "" {
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
		minGcloudLen   = 8
		minK8sLen      = 8
		minDevspaceLen = 6
	)

	sizes := s.calculateComponentSizes(
		componentCount, availableWidth,
		maxLengths, componentMaxLengths{
			hostname: minHostnameLen,
			branch:   minBranchLen,
			aws:      minAwsLen,
			gcloud:   minGcloudLen,
			k8s:      minK8sLen,
			devspace: minDevspaceLen,
		},
	)

	return sizes
}

func (s *Statusline) collectRightComponents(
	data *CachedData,
	awsProfile string,
	maxLengths componentMaxLengths,
) []Component {
	var components []Component

	if data.Devspace != "" {
		devspace := truncateText(data.Devspace, maxLengths.devspace)
		components = append(components, Component{colorMauve, devspace})
	}

	if data.Hostname != "" {
		hostLabel, _ := s.deps.Resolver.Resolve(aliases.KindHost, data.Hostname)
		hostname := truncateText(hostLabel, maxLengths.hostname)
		components = append(components, Component{colorRosewater, HostnameIcon + hostname})
	}

	if data.GitBranch != "" {
		components = append(components, s.createGitComponent(data, maxLengths.branch))
	}

	if awsProfile != "" {
		components = append(components, s.createAwsComponent(awsProfile, maxLengths.aws))
	}

	if data.GcloudProject != "" {
		components = append(components, s.createGcloudComponent(data.GcloudProject, maxLengths.gcloud))
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
	// awsProfile is already cleaned by awsProfileFromEnv in buildRightSection.
	label, env := s.deps.Resolver.Resolve(aliases.KindAWS, awsProfile)
	label = truncateText(label, maxLen)
	return Component{awsBgColor(env), AwsIcon + label}
}

func (s *Statusline) createK8sComponent(k8sContext string, maxLen int) Component {
	label, env := s.deps.Resolver.Resolve(aliases.KindK8s, k8sContext)
	label = truncateText(label, maxLen)
	return Component{k8sBgColor(env), K8sIcon + label}
}

func (s *Statusline) createGcloudComponent(project string, maxLen int) Component {
	label, env := s.deps.Resolver.Resolve(aliases.KindGcloud, project)
	label = truncateText(label, maxLen)
	return Component{gcloudBgColor(env), GcloudIcon + label}
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

func (s *Statusline) buildMiddleSection(data *CachedData, width int) string {
	if width <= 0 {
		return ""
	}

	// Context bar only appears if there's at least 25 chars of space left after components
	// This ensures components get priority for space
	const minContextBarWidth = 25
	if data.UsedPercentage > 0 && width >= minContextBarWidth {
		return s.createContextBarFromPercentage(data.UsedPercentage, width)
	}

	// Otherwise just spaces
	return strings.Repeat(" ", width)
}

func (s *Statusline) createContextBarFromPercentage(percentage float64, barWidth int) string {
	availableForBar := s.calculateAvailableBarWidth(barWidth)
	const minSensibleBarSize = 15
	if availableForBar < minSensibleBarSize {
		return strings.Repeat(" ", barWidth)
	}

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
	label := ContextIcon
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
	maxLengths, minLengths componentMaxLengths,
) componentMaxLengths {
	// Reserve space for separators, curves, spaces, and icons
	const (
		perComponentOverhead = 5
		curvesOverhead       = 4
		minAvailableForText  = 30
	)
	overhead := componentCount*perComponentOverhead + curvesOverhead
	availableForText := availableForRight - overhead

	if availableForText < minAvailableForText {
		return minLengths
	}

	totalNeeded := maxLengths.hostname + maxLengths.branch + maxLengths.aws +
		maxLengths.gcloud + maxLengths.k8s + maxLengths.devspace
	if availableForText < totalNeeded {
		perComponent := availableForText / componentCount
		return s.ensureMinimumSizes(
			componentMaxLengths{
				hostname: perComponent, branch: perComponent, aws: perComponent,
				gcloud: perComponent, k8s: perComponent, devspace: perComponent,
			},
			minLengths,
		)
	}

	return maxLengths
}

func (s *Statusline) ensureMinimumSizes(
	sizes, minLengths componentMaxLengths,
) componentMaxLengths {
	if sizes.hostname < minLengths.hostname {
		sizes.hostname = minLengths.hostname
	}
	if sizes.branch < minLengths.branch {
		sizes.branch = minLengths.branch
	}
	if sizes.aws < minLengths.aws {
		sizes.aws = minLengths.aws
	}
	if sizes.gcloud < minLengths.gcloud {
		sizes.gcloud = minLengths.gcloud
	}
	if sizes.k8s < minLengths.k8s {
		sizes.k8s = minLengths.k8s
	}
	if sizes.devspace < minLengths.devspace {
		sizes.devspace = minLengths.devspace
	}
	return sizes
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

func (s *Statusline) calculateWidths(termWidth int) (int, int, int) {
	leftSpacer := 0
	if s.config.LeftSpacerWidth > 0 {
		leftSpacer = s.config.LeftSpacerWidth
	}

	rightSpacer := s.config.RightSpacerWidth

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
