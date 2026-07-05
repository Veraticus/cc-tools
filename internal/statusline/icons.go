package statusline

const (
	// LeftChevron is the left chevron powerline separator.
	LeftChevron = ""
	// LeftCurve is the left curved powerline separator.
	LeftCurve = ""
	// RightCurve is the right curved powerline separator.
	RightCurve = ""
	// RightChevron is the right chevron powerline separator.
	RightChevron = ""

	// GitIcon is the icon for git branch display.
	GitIcon = " "
	// AgentIcon is the icon for the subagent-name chip (nf-cod-hubot,
	// U+EBA1). Distinct from ModelIcons (Claude model variants) so a
	// subagent row reads as "this is an agent" at a glance.
	AgentIcon = " "
	// AwsIcon is the icon for AWS profile display.
	AwsIcon = " "
	// K8sIcon is the icon for Kubernetes context display.
	K8sIcon = "☸ "
	// GcloudIcon is the icon for gcloud project display.
	GcloudIcon = "󱇶 "
	// HostnameIcon is the icon for hostname display.
	HostnameIcon = " "
	// ContextIcon is the icon for context bar display.
	ContextIcon = " "
	// RateLimitIcon is the icon for the rate-limit chip.
	RateLimitIcon = "󰜎 "
	// CostIcon is the icon for the cost chip.
	CostIcon = "󰈐 "
	// AlarmIcon is the icon for the extra-usage alarm chip.
	AlarmIcon = "󰀦 "
	// ModelIcons contains various icons for model display.
	ModelIcons = "󰚩󱚝󱚟󱚡󱚣󱚥"

	// ProgressLeftEmpty is the left empty progress bar character.
	ProgressLeftEmpty = ""
	// ProgressMidEmpty is the middle empty progress bar character.
	ProgressMidEmpty = ""
	// ProgressRightEmpty is the right empty progress bar character.
	ProgressRightEmpty = ""
	// ProgressLeftFull is the left filled progress bar character.
	ProgressLeftFull = ""
	// ProgressMidFull is the middle filled progress bar character.
	ProgressMidFull = ""
	// ProgressRightFull is the right filled progress bar character.
	ProgressRightFull = ""
)
