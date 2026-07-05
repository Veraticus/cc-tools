package statusline

import "strings"

// RenderClouds emits the AWS / gcloud / k8s chip chain as raw ANSI,
// starting with a chevron transition from sky (git's color) and ending
// with a closing right curve. Each chip uses its env-classified bg color.
//
// Designed to be embedded in starship's right_format after $git_status:
// starship handles the static-color section (devspace, host, git), this
// function handles the dynamic-color section. Always emits at least the
// closing curve so git's right edge is sealed even when no cloud chips
// are present.
func RenderClouds(deps *Dependencies) string {
	s := CreateStatusline(deps)

	awsProfile := awsProfileFromEnv(s.deps.EnvReader)
	k8sContext := s.getK8sContext()
	gcloudProject := s.getGcloudProject()

	const cloudMaxLen = 30
	var chips []Component
	if awsProfile != "" {
		chips = append(chips, s.createAwsComponent(awsProfile, cloudMaxLen))
	}
	if gcloudProject != "" {
		chips = append(chips, s.createGcloudComponent(gcloudProject, cloudMaxLen))
	}
	if k8sContext != "" {
		chips = append(chips, s.createK8sComponent(k8sContext, cloudMaxLen))
	}

	return s.renderChainAfter(colorSky, chips)
}

// renderChainAfter renders a chip chain where the leading chevron
// transitions from prevColor into the first chip's color. The chain is
// always terminated with a closing right curve. If chips is empty, the
// output is just the closing curve in prevColor — useful for sealing
// the right edge of whatever module preceded us.
func (s *Statusline) renderChainAfter(prevColor string, chips []Component) string {
	if len(chips) == 0 {
		return s.getColorFG(prevColor) + RightCurve + s.colors.NC()
	}

	var sb strings.Builder
	// Each chip emits ~80 bytes of ANSI + text; pre-size to skip the
	// usual 2-3 small reallocations.
	const bytesPerChip = 80
	sb.Grow(len(chips) * bytesPerChip)
	prev := prevColor
	for _, chip := range chips {
		// Transition chevron: bg = previous chip's color, fg = THIS chip's
		// color. The chevron triangle is drawn in the next chip's color
		// over the previous chip's background — matching starship's
		// convention and renderComponentSeparator. Reversing fg/bg here
		// would draw the triangle in the wrong color and the transition
		// would look like the prev color was "pushing forward" instead
		// of the next color "encroaching back".
		sb.WriteString(s.getColorBG(prev))
		sb.WriteString(s.getColorFG(chip.Color))
		sb.WriteString(RightChevron)
		sb.WriteString(s.colors.NC())

		// Chip content: bg = self color, fg = base.
		sb.WriteString(s.getColorBG(chip.Color))
		sb.WriteString(s.colors.BaseFG())
		sb.WriteString(" ")
		sb.WriteString(chip.Text)
		sb.WriteString(" ")
		sb.WriteString(s.colors.NC())

		prev = chip.Color
	}

	// Closing right curve in the last chip's color.
	sb.WriteString(s.getColorFG(prev))
	sb.WriteString(RightCurve)
	sb.WriteString(s.colors.NC())

	return sb.String()
}
