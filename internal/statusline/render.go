package statusline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

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
	modelDisplay := data.ModelDisplay
	effort := data.Effort
	// Running agents swap the model chip: their models are what the
	// session is actually burning right now, and the agent screen's
	// bottom bar (same statusline — Claude renders one per session)
	// otherwise says nothing about them. Effort is dropped because
	// each agent label carries its own ("sol ×XH"); the session
	// model's effort would mislabel them.
	if data.AgentsDisplay != "" {
		modelIcon = strings.TrimSpace(AgentIcon)
		modelDisplay = data.AgentsDisplay
		effort = nil
	}
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

	// Build components with proper sizing that accounts for spacers.
	// When the extra-usage alarm is active, its chip width is carved
	// out of the budget offered to the left/right sections so their
	// proportional truncation absorbs the squeeze — the alarm is an
	// emergency signal and must never be the piece that loses its
	// space to a long directory or a pile of env chips.
	sectionBudget := contentWidth
	if middleChipKind(data) == chipAlarm {
		alarmWidth := runewidth.StringWidth(stripAnsi(s.buildMiddleChip(chipAlarm, data)))
		if sectionBudget > alarmWidth {
			sectionBudget -= alarmWidth
		}
	}
	leftSection := s.buildLeftSection(dirPath, modelDisplay, modelIcon, effort, sectionBudget)
	rightSection := s.buildRightSection(data, sectionBudget)

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

// effortCode maps an effort level to the compact code shown inside the
// model chip. Unknown or empty levels return "" (no suffix).
func effortCode(effort *EffortInput) string {
	if effort == nil {
		return ""
	}
	switch effort.Level {
	case "low":
		return "L"
	case "medium":
		return "M"
	case "high":
		return "H"
	case "xhigh":
		return "XH"
	case "max":
		return "MAX"
	default:
		return ""
	}
}

func (s *Statusline) buildLeftSection(
	dirPath, modelDisplay, modelIcon string,
	effort *EffortInput,
	availableWidth int,
) string {
	// Effort suffix joins the model text before truncation so the
	// chip's width math accounts for the extra characters.
	if code := effortCode(effort); code != "" {
		modelDisplay += " ×" + code
	}

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

// createGitComponent builds the git chip body: branch, then " !N" when
// dirty, then " ↑A" / " ↓B" for nonzero ahead/behind counts, then a
// review-state-colored PR glyph when data.PR is present.
//
// This is the one chip whose body carries non-BaseFG text: the PR
// glyph gets its own inline foreground color (green/yellow/red/muted
// gray) instead of the BaseFG renderComponentContent applies to every
// other chip. The chip's background stays sky throughout, and the
// branch/dirty/ahead/behind text stays BaseFG (inherited from
// renderComponentContent's preamble) — only the glyph itself switches
// color, and switches back to BaseFG immediately after so nothing
// downstream is left in the glyph's color.
func (s *Statusline) createGitComponent(data *CachedData, maxLen int) Component {
	branch := truncateText(data.GitBranch, maxLen)

	var sb strings.Builder
	sb.WriteString(GitIcon)
	sb.WriteString(branch)
	if data.GitDirtyCount > 0 {
		fmt.Fprintf(&sb, " !%d", data.GitDirtyCount)
	}
	if data.GitAhead > 0 {
		fmt.Fprintf(&sb, " ↑%d", data.GitAhead)
	}
	if data.GitBehind > 0 {
		fmt.Fprintf(&sb, " ↓%d", data.GitBehind)
	}
	if data.PR != nil {
		sb.WriteString(" ")
		sb.WriteString(s.getColorFG(prGlyphColor(data.PR.ReviewState)))
		sb.WriteString(PRIcon)
		sb.WriteString(s.colors.BaseFG())
	}

	return Component{colorSky, sb.String()}
}

// prGlyphColor maps a PR's review_state to the git chip's PR glyph
// foreground color: approved is green, changes_requested is red,
// draft is the muted overlay gray, and pending or an absent/unknown
// state (including "") default to yellow.
func prGlyphColor(reviewState string) string {
	switch reviewState {
	case "approved":
		return colorGreen
	case "changes_requested":
		return colorRed
	case "draft":
		return colorOverlay
	default:
		return colorYellow
	}
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

	var contextEl string
	if data.UsedPercentage > 0 {
		contextEl = s.buildContextElement(data.UsedPercentage)
	}

	kind := middleChipKind(data)
	chip := s.buildMiddleChip(kind, data)

	cluster, clusterWidth := assembleMiddleCluster(contextEl, chip, width)
	if clusterWidth == 0 {
		return strings.Repeat(" ", width)
	}
	if width < clusterWidth {
		return s.buildSqueezedMiddleSection(data, contextEl, kind, width)
	}

	return s.centerElement(cluster, clusterWidth, width)
}

// buildSqueezedMiddleSection handles a chip that remains too wide after
// assembleMiddleCluster has dropped context. Alarms remain visible; a legacy
// two-part cost may degrade to its session figure; every other chip blanks.
func (s *Statusline) buildSqueezedMiddleSection(
	data *CachedData,
	contextEl string,
	kind chipKind,
	width int,
) string {
	if kind == chipAlarm {
		if data.Patchbay.Status == patchbayAvailable {
			return s.buildSqueezedPatchbayAlarmChip(width)
		}
		return s.buildSqueezedAlarmChip(data.Cost, width)
	}
	if kind != chipCost || !data.CostFromTranscript {
		return strings.Repeat(" ", width)
	}

	// The full two-part body doesn't fit — retry with the session-only
	// single-figure body before giving up and blanking, still through
	// assembleMiddleCluster so the context element can also drop.
	sessionOnlyChip := s.buildSessionOnlyCostChip(data)
	sessionCluster, sessionClusterWidth := assembleMiddleCluster(contextEl, sessionOnlyChip, width)
	if sessionClusterWidth > 0 && width >= sessionClusterWidth {
		return s.centerElement(sessionCluster, sessionClusterWidth, width)
	}
	return strings.Repeat(" ", width)
}

// assembleMiddleCluster joins the (optional) context element and the
// (optional) middle chip with one space when both are present, so the
// two render as a single centered cluster. If the combined cluster
// doesn't fit `width`, the context ELEMENT is dropped first and the
// chip alone is returned — mirroring narrow mode's drop priority,
// where the rate-limit/cost/alarm chip is the more urgent signal and
// the context element is comparatively decorative. Pure function of
// pre-rendered ANSI strings; returns the joined string and its
// stripped visible width.
func assembleMiddleCluster(contextEl, chip string, width int) (string, int) {
	joinWidth := func(a, b string) (string, int) {
		var cluster string
		switch {
		case a != "" && b != "":
			cluster = a + " " + b
		case a != "":
			cluster = a
		default:
			cluster = b
		}
		return cluster, runewidth.StringWidth(stripAnsi(cluster))
	}

	cluster, w := joinWidth(contextEl, chip)
	if contextEl == "" || w <= width {
		return cluster, w
	}
	return joinWidth("", chip)
}

// chipKind enumerates which single chip (if any) accompanies the
// context element in the wide-mode middle cluster. Exactly one of
// {alarm, rate-limit, cost} renders alongside the context element —
// never more than one.
type chipKind int

const (
	chipNone chipKind = iota
	chipAlarm
	chipRateLimit
	chipCost
)

// middleChipKind decides which chip (if any) renders in the wide middle
// cluster. A Patchbay error outranks every rate-limit state so broken accounting
// cannot disappear. Otherwise an active 5h alarm wins, then the rate-limit
// chip. Patchbay's available day total hides when both its known and unknown
// counts are zero; an unconfigured Patchbay preserves the original legacy path.
func middleChipKind(data *CachedData) chipKind {
	if data.Patchbay.Status == patchbayError {
		return chipCost
	}
	if data.RateLimits != nil {
		if data.RateLimits.FiveHour != nil && data.RateLimits.FiveHour.UsedPercentage >= 100 {
			return chipAlarm
		}
		return chipRateLimit
	}
	switch data.Patchbay.Status {
	case patchbayUnavailable:
		return chipCost
	case patchbayAvailable:
		if data.Patchbay.Summary.KnownCostNanoUSD > 0 || data.Patchbay.Summary.UnknownCostRows > 0 {
			return chipCost
		}
		return chipNone
	case patchbayUnconfigured:
		return legacyMiddleChipKind(data)
	case patchbayError:
		return chipCost
	}
	return chipNone
}

func legacyMiddleChipKind(data *CachedData) chipKind {
	transcriptCostNonzero := data.CostFromTranscript && (data.SessionCostUSD > 0 || data.DailyCostUSD > 0)
	if data.Cost.TotalCostUSD > 0 || transcriptCostNonzero {
		return chipCost
	}
	return chipNone
}

// buildMiddleChip renders the chip selected by kind, or "" for
// chipNone.
func (s *Statusline) buildMiddleChip(kind chipKind, data *CachedData) string {
	switch kind {
	case chipAlarm:
		if data.Patchbay.Status == patchbayAvailable {
			return s.buildPatchbayAlarmChip()
		}
		return s.buildAlarmChip(data.Cost)
	case chipRateLimit:
		return s.buildRateLimitChip(data.RateLimits, s.now())
	case chipCost:
		return s.buildCostChip(data)
	case chipNone:
		return ""
	default:
		return ""
	}
}

// now returns the current time for rate-limit chip math: deps.Now
// when injected (scenarios/tests), else the real wall clock.
func (s *Statusline) now() time.Time {
	if s.deps != nil && s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// Fixed rolling-window lengths for the rate-limit chip's pace math.
// Claude Code's rate-limit windows are exactly five hours and seven
// days — not derived from the payload.
const (
	rateLimitFiveHourLen = 5 * time.Hour
	rateLimitSevenDayLen = 7 * 24 * time.Hour
	// paceArrowSlackPct is the +/- band (percentage points) around the
	// expected usage at which no pace arrow renders — usage close
	// enough to schedule isn't worth flagging.
	paceArrowSlackPct = 5.0
)

// rlWindowSpec pairs a present rate-limit window with its label and
// fixed window length, so buildRateLimitBody can iterate whichever of
// five_hour/seven_day were actually reported.
type rlWindowSpec struct {
	label  string
	win    *RateLimitWindow
	length time.Duration
}

// buildRateLimitBody renders the rate-limit chip's text content:
// `5h NN%[arrow] · 7d NN%[arrow] (countdown)` for whichever windows
// are present. The countdown in parens is appended once, for the
// reset of whichever present window has the higher used percentage.
func buildRateLimitBody(rl *RateLimitsInput, now time.Time) string {
	const maxRateLimitWindows = 2 // five_hour + seven_day, the only two windows that exist
	windows := make([]rlWindowSpec, 0, maxRateLimitWindows)
	if rl.FiveHour != nil {
		windows = append(windows, rlWindowSpec{"5h", rl.FiveHour, rateLimitFiveHourLen})
	}
	if rl.SevenDay != nil {
		windows = append(windows, rlWindowSpec{"7d", rl.SevenDay, rateLimitSevenDayLen})
	}

	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		arrow := paceArrow(w.win, w.length, now)
		parts = append(parts, fmt.Sprintf("%s %.0f%%%s", w.label, w.win.UsedPercentage, arrow))
	}

	body := RateLimitIcon + strings.Join(parts, " · ")

	if len(windows) > 0 {
		highest := windows[0]
		for _, w := range windows[1:] {
			if w.win.UsedPercentage > highest.win.UsedPercentage {
				highest = w
			}
		}
		body += " (" + formatCountdown(highest.win.ResetsAt, now) + ")"
	}

	return body
}

// paceArrow compares a window's actual used% against the used%
// expected if usage were spread evenly across the window's elapsed
// time, returning "⇡" (ahead of pace), "⇣" (behind pace), or "" when
// within paceArrowSlackPct of expected. A resets_at at or before now
// is a stale payload — the window has (or should have) already reset
// — so no arrow is shown rather than risk a misleading comparison.
func paceArrow(win *RateLimitWindow, windowLen time.Duration, now time.Time) string {
	resetsAt := time.Unix(win.ResetsAt, 0)
	if !resetsAt.After(now) {
		return ""
	}

	remaining := resetsAt.Sub(now)
	elapsed := windowLen - remaining
	const percentDivisor = 100.0
	expectedPct := float64(elapsed) / float64(windowLen) * percentDivisor

	switch {
	case win.UsedPercentage > expectedPct+paceArrowSlackPct:
		return "⇡"
	case win.UsedPercentage < expectedPct-paceArrowSlackPct:
		return "⇣"
	default:
		return ""
	}
}

// formatCountdown renders the time remaining until resetsAt at
// glanceable precision, tiered by magnitude: "2d0h" (days + hours) at
// 24h and above, "3h47m" (hours + minutes) from 1h up to 24h, and
// bare "23m" below an hour. No seconds, no zero-padding. Negative
// remaining time (resetsAt at or before now) clamps to "0m".
func formatCountdown(resetsAt int64, now time.Time) string {
	remaining := time.Unix(resetsAt, 0).Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	const day = 24 * time.Hour
	switch {
	case remaining >= day:
		days := int(remaining / day)
		hours := int((remaining % day) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	case remaining >= time.Hour:
		hours := int(remaining / time.Hour)
		minutes := int((remaining % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", int(remaining/time.Minute))
	}
}

// buildRateLimitChip renders the rate-limit powerline chip on a
// sapphire background.
func (s *Statusline) buildRateLimitChip(rl *RateLimitsInput, now time.Time) string {
	return s.buildPowerlineChip(buildRateLimitBody(rl, now), colorSapphire)
}

// legacyCostChipBody renders the single-figure cost chip body
// (`CostIcon $X.XX`, two decimals always) used both as the
// stdin-cost fallback and as the session-only degraded body when the
// full two-part transcript body doesn't fit.
func legacyCostChipBody(amountUSD float64) string {
	return fmt.Sprintf("%s$%.2f", CostIcon, amountUSD)
}

// transcriptCostChipBody renders the two-part transcript-derived cost
// chip body: `CostIcon $<session> ∙ $<day> day`.
func transcriptCostChipBody(sessionUSD, dailyUSD float64) string {
	return fmt.Sprintf("%s$%.2f ∙ $%.2f day", CostIcon, sessionUSD, dailyUSD)
}

// patchbayCostChipBody renders Patchbay's day-scoped total. Unknown rows are
// deliberately marked instead of estimated because Patchbay does not know
// their cost.
func patchbayCostChipBody(summary patchbaySummary) string {
	body := CostIcon + formatPatchbayUSD(summary.KnownCostNanoUSD) + " day"
	if summary.UnknownCostRows > 0 {
		body += fmt.Sprintf(" +%d?", summary.UnknownCostRows)
	}
	return body
}

// buildCostChip renders the cost powerline chip on a sapphire background. A
// successful Patchbay result is the day-only accounting source. An unreachable
// configured Patchbay retains the legacy basis with a trailing `~`; broken
// authentication, server, or response data renders `ERR` with no cost figures.
func (s *Statusline) buildCostChip(data *CachedData) string {
	var body string
	switch data.Patchbay.Status {
	case patchbayAvailable:
		body = patchbayCostChipBody(data.Patchbay.Summary)
	case patchbayError:
		body = CostIcon + "ERR"
	case patchbayUnconfigured, patchbayUnavailable:
		body = legacyCostChipBody(data.Cost.TotalCostUSD)
		if data.CostFromTranscript {
			body = transcriptCostChipBody(data.SessionCostUSD, data.DailyCostUSD)
		}
		if data.Patchbay.Status == patchbayUnavailable {
			body += "~"
		}
	}
	return s.buildPowerlineChip(body, colorSapphire)
}

// buildSessionOnlyCostChip renders the session-only degraded cost chip
// body (`CostIcon $<session>`) used when the full two-part transcript
// body doesn't fit the available width — see buildMiddleSection's
// width-degradation step.
func (s *Statusline) buildSessionOnlyCostChip(data *CachedData) string {
	body := legacyCostChipBody(data.SessionCostUSD)
	if data.Patchbay.Status == patchbayUnavailable {
		body += "~"
	}
	return s.buildPowerlineChip(body, colorSapphire)
}

// alarmChipBody is the alarm chip's text content, shared by the
// normal and squeezed renderings so the two can never drift.
func alarmChipBody(cost CostInput) string {
	return fmt.Sprintf("%sEXTRA $%.2f", AlarmIcon, cost.TotalCostUSD)
}

// buildAlarmChip renders the extra-usage alarm powerline chip
// (`AlarmIcon EXTRA $X.XX`) on a red background — the same red as the
// context bar's >=80% state. This is an emergency signal: it must
// never be muted, downgraded, or omitted under width pressure.
func (s *Statusline) buildAlarmChip(cost CostInput) string {
	return s.buildPowerlineChip(alarmChipBody(cost), colorRed)
}

// patchbayAlarmChipBody keeps the rate-limit emergency signal visible without
// combining an API-accounted session with the legacy stdin cost field.
func patchbayAlarmChipBody() string {
	return AlarmIcon + "EXTRA"
}

// buildPatchbayAlarmChip renders the Patchbay-accounted alarm without a dollar
// amount. The API summary is day scoped, while the rate-limit alarm is about
// current subscription capacity, so attaching either legacy or day money would
// imply a relationship the data does not establish.
func (s *Statusline) buildPatchbayAlarmChip() string {
	return s.buildPowerlineChip(patchbayAlarmChipBody(), colorRed)
}

// buildSqueezedAlarmChip is the pathological fallback for a middle
// region narrower than the full alarm chip (possible when the
// left/right sections exhaust the row despite Render's alarm-width
// reservation): the chip body is truncated to whatever fits, and when
// not even a curve-wrapped icon fits, the whole region floods red.
// The alarm degrades under width pressure but never blanks.
func (s *Statusline) buildSqueezedAlarmChip(cost CostInput, width int) string {
	return s.buildSqueezedAlarmBody(alarmChipBody(cost), width)
}

func (s *Statusline) buildSqueezedPatchbayAlarmChip(width int) string {
	return s.buildSqueezedAlarmBody(patchbayAlarmChipBody(), width)
}

func (s *Statusline) buildSqueezedAlarmBody(body string, width int) string {
	const chipChromeWidth = 4 // LeftCurve + two body-padding spaces + RightCurve
	bodyBudget := width - chipChromeWidth
	if bodyBudget < runewidth.StringWidth(AlarmIcon) {
		return s.getColorBG(colorRed) + strings.Repeat(" ", width) + s.colors.NC()
	}
	chip := s.buildPowerlineChip(truncateText(body, bodyBudget), colorRed)
	return s.centerElement(chip, runewidth.StringWidth(stripAnsi(chip)), width)
}

// buildPowerlineChip renders a LeftCurve+body+RightCurve powerline
// group for the middle-cluster chips: curve fg = chip color with no
// bg set, body bg = chip color with BaseFG text — the same
// curve/body relationship buildContextElement uses.
func (s *Statusline) buildPowerlineChip(body, colorName string) string {
	fgColor := s.getColorFG(colorName)
	bgColor := s.getColorBG(colorName)

	var sb strings.Builder

	sb.WriteString(fgColor)
	sb.WriteString(LeftCurve)
	sb.WriteString(s.colors.NC())

	sb.WriteString(bgColor)
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(" ")
	sb.WriteString(body)
	sb.WriteString(" ")
	sb.WriteString(s.colors.NC())

	sb.WriteString(fgColor)
	sb.WriteString(RightCurve)
	sb.WriteString(s.colors.NC())

	return sb.String()
}

// centerElement centers a pre-rendered element of elementWidth visible
// cells inside a region of `width` cells, padding the remaining slack
// with plain (uncolored) spaces. Odd slack puts the extra cell on the
// right.
func (s *Statusline) centerElement(element string, elementWidth, width int) string {
	const halfDivisor = 2
	slack := width - elementWidth
	left := slack / halfDivisor
	right := slack - left
	return strings.Repeat(" ", left) + element + strings.Repeat(" ", right)
}

// contextElementWidth is the fixed visible-cell budget for the wide
// middle cluster (curve + icon + progress bar + percentage + curve).
// buildContextElement sizes the bar fill to absorb whatever the
// percentage text needs, so the cluster is exactly this width for any
// 0-100 percentage (a future rate-limit/cost chip can join this
// cluster alongside it without reworking the centering math).
const contextElementWidth = 30

// buildContextElement renders the curve+icon+bar+percentage+curve
// cluster for the wide-mode context indicator. The bar fill width is
// computed to absorb whatever the percentage text needs, so "5%",
// "42%", and "100%" all produce a contextElementWidth-cell element.
func (s *Statusline) buildContextElement(percentage float64) string {
	bgColor, fgColor, fgLightBg := s.getContextColors(percentage)

	percentText := fmt.Sprintf(" %.0f%%", percentage)
	const curvesWidth = 2 // LeftCurve + RightCurve, 1 cell each
	fillWidth := contextElementWidth - curvesWidth -
		runewidth.StringWidth(ContextIcon) - runewidth.StringWidth(percentText)
	if fillWidth < 1 {
		fillWidth = 1
	}
	const percentDivisor = 100.0
	filledWidth := int(float64(fillWidth) * percentage / percentDivisor)

	progressBar := s.buildProgressBar(fillWidth, filledWidth, fgColor, fgLightBg)

	var sb strings.Builder

	sb.WriteString(fgColor)
	sb.WriteString(LeftCurve)
	sb.WriteString(s.colors.NC())

	sb.WriteString(bgColor)
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(ContextIcon)
	sb.WriteString(s.colors.NC())

	sb.WriteString(progressBar)

	sb.WriteString(bgColor)
	sb.WriteString(s.colors.BaseFG())
	sb.WriteString(percentText)
	sb.WriteString(s.colors.NC())

	sb.WriteString(fgColor)
	sb.WriteString(RightCurve)
	sb.WriteString(s.colors.NC())

	return sb.String()
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
	if s.deps.IconIndex != nil {
		return string(icons[s.deps.IconIndex(len(icons))])
	}
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

// contextColor maps a 0-100 context-window usage percentage to the
// palette color name shared by the wide context bar and the narrow
// context chip: teal below 60%, yellow from 60-79%, red at 80% and
// above. This is the single source of truth for both render paths.
func contextColor(usedPct float64) string {
	const (
		yellowThreshold = 60.0
		redThreshold    = 80.0
	)
	switch {
	case usedPct < yellowThreshold:
		return colorTeal
	case usedPct < redThreshold:
		return colorYellow
	default:
		return colorRed
	}
}

func (s *Statusline) getContextColors(percentage float64) (string, string, string) {
	name := contextColor(percentage)
	return s.getColorBG(name), s.getColorFG(name), s.contextLightBG(name)
}

// contextLightBG returns the muted "LightBG" variant used for the
// bar's empty fill segment. getColorBG/getColorFG have no LightBG
// case — only the context bar uses this muted treatment — so this is
// a small dedicated switch over the colors contextColor can return.
func (s *Statusline) contextLightBG(name string) string {
	switch name {
	case colorTeal:
		return s.colors.TealLightBG()
	case colorYellow:
		return s.colors.YellowLightBG()
	default:
		return s.colors.RedLightBG()
	}
}
