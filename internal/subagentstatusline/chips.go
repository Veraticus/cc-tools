package subagentstatusline

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joshsymonds/steward/internal/statusline"
)

// EnvReader is the small interface every env-driven chip uses. The
// production wiring plugs in statusline.DefaultEnvReader{} which
// already overlays the steward state file on top of process env for
// AWS_PROFILE — meaning a Bash-tool export in a subagent reaches the
// agent-view's chip via that file rather than via process inheritance.
type EnvReader interface {
	Get(key string) string
}

// EnvSnapshot captures the env values the env-driven chips need at
// a single point in time. SnapshotEnv reads them once per invocation
// so a multi-task batch doesn't repeatedly re-open the AWS_PROFILE
// state file (which DefaultEnvReader.Get does on every call).
type EnvSnapshot struct {
	AWSProfile    string
	GCloudProject string
	K8sContext    string
}

// SnapshotEnv reads the relevant env values once, applying the same
// precedence used by the individual chip helpers (CLOUDSDK over GOOGLE,
// KUBE_CONTEXT over KUBERNETES_CONTEXT). Callers that need to do
// per-task rendering should snapshot once and pass the result down,
// not call this per task.
func SnapshotEnv(env EnvReader) EnvSnapshot {
	snap := EnvSnapshot{
		AWSProfile:    env.Get("AWS_PROFILE"),
		GCloudProject: env.Get("CLOUDSDK_CORE_PROJECT"),
		K8sContext:    env.Get("KUBE_CONTEXT"),
	}
	if snap.GCloudProject == "" {
		snap.GCloudProject = env.Get("GOOGLE_CLOUD_PROJECT")
	}
	if snap.K8sContext == "" {
		snap.K8sContext = env.Get("KUBERNETES_CONTEXT")
	}
	return snap
}

// validBranchName rejects branch names that would inject ANSI escapes
// or otherwise render badly: any C0/C1 control bytes (0x00-0x1f, 0x7f),
// spaces, or a leading dash (git's own ref-naming rule). Compiled
// once at package init.
var validBranchName = regexp.MustCompile(`^[^\x00-\x20\x7f][^\x00-\x1f\x7f ]*$`)

// stripControl strips C0/C1 control bytes from a string. Used on
// untrusted env values before they enter chip bodies — otherwise a
// poisoned AWS profile name (via the state file) or gcloud/k8s env
// could inject terminal escape sequences into the rendered tty.
func stripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Color is a palette entry name. The methods FG()/BG() delegate to
// the existing CatppuccinMocha palette in internal/statusline/colors.go
// so the hex values aren't duplicated here.
//
// Stored as a string so chips and decorations are cheap value types
// the chain builder can pass around freely.
type Color string

// Palette entries this package uses. The set is intentionally narrow:
// adding a new chip color is a deliberate decision, not an accident.
const (
	ColorLavender Color = "lavender"
	ColorGreen    Color = "green"
	ColorYellow   Color = "yellow"
	ColorPeach    Color = "peach"
	ColorRed      Color = "red"
	ColorPink     Color = "pink"
	ColorTeal     Color = "teal"
	// ColorSapphire is reserved for the description chip — a cool
	// blue that reads as "info" against the warm peach name chip
	// without colliding with dir (lavender), branch (pink), or any
	// env-chip color.
	ColorSapphire Color = "sapphire"
)

// chip text uses the dark base foreground so the colored bg stays
// readable; consistent with how internal/statusline renders chips.
var palette = statusline.CatppuccinMocha{} //nolint:gochecknoglobals // palette is a stateless value, shared

// BG returns the 24-bit ANSI background escape for this color.
// Returns "" for unknown colors so chain assembly degrades visibly
// rather than panicking; new colors must be added explicitly here.
func (c Color) BG() string {
	switch c {
	case ColorLavender:
		return palette.LavenderBG()
	case ColorGreen:
		return palette.GreenBG()
	case ColorYellow:
		return palette.YellowBG()
	case ColorPeach:
		return palette.PeachBG()
	case ColorRed:
		return palette.RedBG()
	case ColorPink:
		return palette.PinkBG()
	case ColorTeal:
		return palette.TealBG()
	case ColorSapphire:
		return palette.SapphireBG()
	}
	return ""
}

// FG returns the 24-bit ANSI foreground escape for this color. Used
// for chevron transitions (chevron's fg = previous chip's color) and
// for the LeftCurve/RightCurve end-caps (fg of the touching chip).
func (c Color) FG() string {
	switch c {
	case ColorLavender:
		return palette.LavenderFG()
	case ColorGreen:
		return palette.GreenFG()
	case ColorYellow:
		return palette.YellowFG()
	case ColorPeach:
		return palette.PeachFG()
	case ColorRed:
		return palette.RedFG()
	case ColorPink:
		return palette.PinkFG()
	case ColorTeal:
		return palette.TealFG()
	case ColorSapphire:
		return palette.SapphireFG()
	}
	return ""
}

// Chip is one rendered powerline cell with Body padded and ready to
// be wrapped by the chain builder. Body never contains chevrons or
// end-caps — those are the chain builder's responsibility.
type Chip struct {
	// Color is the background palette entry. Chain assembly uses
	// Color.FG() to compute chevron transitions to adjacent chips.
	Color Color

	// Body is the printable content with a leading and trailing space
	// so it seats cleanly inside curves/chevrons (which butt directly
	// against the chip background).
	Body string
}

// renderAgentNameChip builds the leading chip that identifies which
// subagent this row belongs to. Color is driven by Task.Status so
// the row's lifecycle reads at a glance: peach=running (warm/active),
// green=completed, red=failed/killed, yellow=intermediate, lavender
// for unknown states.
//
// Body uses the best identifier available via agentNameText (Name →
// Label → Description → type fallback), prefixed with AgentIcon
// (nf-cod-hubot, U+EBA1) to distinguish from the model-icon family.
func renderAgentNameChip(t Task) Chip {
	return Chip{
		Color: agentStatusColor(t.Status),
		Body:  " " + statusline.AgentIcon + agentNameText(t) + " ",
	}
}

// agentNameMaxRunes caps the chip body's identifier portion. Most
// Claude-pre-computed labels are short prose ("Audit code", "Run
// tests") and fit; long Descriptions get truncated with "…". 20
// runes balances readability against horizontal budget.
const agentNameMaxRunes = 20

// nameSource records which Task field the name chip's text came
// from. Used downstream by renderAgentDescriptionChip to avoid
// rendering a second chip that just repeats the same source under
// a different truncation.
type nameSource int

const (
	sourceTypeLabel   nameSource = iota // type-derived fallback ("agent")
	sourceName                          // Task.Name
	sourceLabel                         // Task.Label
	sourceDescription                   // Task.Description
)

// agentNameText returns the truncated text for the name chip. See
// agentNamePicked for the source priority.
func agentNameText(t Task) string {
	text, _ := agentNamePicked(t)
	return text
}

// agentNamePicked picks the most informative identifier and reports
// which Task field it came from. Priority:
//  1. Task.Name (user-assigned, e.g. via `/agents` UI)
//  2. Task.Label (Claude's pre-computed short label — matches the
//     agents-view header text)
//  3. Task.Description (the agent's current prompt/summary)
//  4. agentTypeLabel(Type) as a final fallback
//
// Every source is run through stripControl to neutralize ANSI
// injection from poisoned Name/Label/Description values.
func agentNamePicked(t Task) (string, nameSource) {
	if t.Name != nil {
		if s := strings.TrimSpace(stripControl(*t.Name)); s != "" {
			return truncateRunes(s, agentNameMaxRunes), sourceName
		}
	}
	if s := strings.TrimSpace(stripControl(t.Label)); s != "" {
		return truncateRunes(s, agentNameMaxRunes), sourceLabel
	}
	if s := strings.TrimSpace(stripControl(t.Description)); s != "" {
		return truncateRunes(s, agentNameMaxRunes), sourceDescription
	}
	return agentTypeLabel(t.Type), sourceTypeLabel
}

// modelMaxRunes caps the model chip's identifier portion. Short ids
// ("F5", "sol", "glm-4.6") are the common case; a long unrecognized
// id truncates rather than starving the chips to its right.
const modelMaxRunes = 14

// renderModelChip builds the chip identifying which model this task
// runs — the one piece of row identity Claude's own chrome doesn't
// show, and the only visible difference between otherwise-identical
// rungs routed to different upstreams. Returns (Chip{}, false) when
// the task has no model (Claude omits the field for inherited-model
// tasks). Effort renders as a "×H"-style suffix, matching the main
// statusline's model chip.
//
// Color is ColorTeal — distinct from the status-colored name chip on
// its left and the sapphire description chip on its right.
func renderModelChip(t Task) (Chip, bool) {
	text := shortModelName(t.Model)
	if text == "" {
		return Chip{}, false
	}
	if code := effortCode(string(t.Effort)); code != "" {
		text += " ×" + code
	}
	return Chip{
		Color: ColorTeal,
		Body:  " " + statusline.ModelIcon + text + " ",
	}, true
}

// effortCode maps an effort level to the compact code shown as the
// model chip's suffix. Same codes as the main statusline's effortCode
// (internal/statusline/render.go); unknown or empty levels return "".
func effortCode(level string) string {
	switch level {
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
	}
	return ""
}

// shortModelName compresses a model id or alias into chip-sized text:
//
//  1. Control bytes stripped, whitespace trimmed.
//  2. A trailing bracket marker is dropped ("claude-fable-5[1m]" →
//     "claude-fable-5") — same intent as the main statusline stripping
//     "(1M Context)".
//  3. A provider prefix is dropped ("chatgpt/sol" → "sol",
//     "openrouter/glm-4.6" → "glm-4.6").
//  4. A "claude-" id abbreviates to first-letter + dotted version
//     ("claude-fable-5" → "F5", "claude-opus-4-8" → "O4.8",
//     "claude-3-5-sonnet-20241022" → "S3.5") via abbreviateClaudeID.
//  5. Anything else passes through truncated to modelMaxRunes.
//
// Empty input returns "" (caller omits the chip).
func shortModelName(model string) string {
	s := strings.TrimSpace(stripControl(model))
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '['); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	if rest, ok := strings.CutPrefix(s, "claude-"); ok {
		if short := abbreviateClaudeID(rest); short != "" {
			return short
		}
	}
	return truncateRunes(s, modelMaxRunes)
}

// dateStampMinDigits distinguishes a version token ("5", "4", "8")
// from a date stamp ("20241022") inside a claude model id: any
// all-digit token this long or longer is a snapshot date, not a
// version component.
const dateStampMinDigits = 6

// abbreviateClaudeID turns the post-"claude-" tail of a model id into
// first-letter-of-family + dotted version: "fable-5" → "F5",
// "opus-4-8" → "O4.8", "haiku-4-5-20251001" → "H4.5",
// "3-5-sonnet-20241022" → "S3.5" (old ids put the version first).
// The family is the last alphabetic token; the version joins every
// short all-digit token with dots, skipping date stamps. Returns ""
// when either half is missing so the caller can fall back to the raw
// id.
func abbreviateClaudeID(rest string) string {
	var family string
	var version []string
	for tok := range strings.SplitSeq(rest, "-") {
		switch {
		case tok == "":
		case isDigits(tok):
			if len(tok) < dateStampMinDigits {
				version = append(version, tok)
			}
		case isAlphaToken(tok):
			family = tok
		}
	}
	if family == "" || len(version) == 0 {
		return ""
	}
	return strings.ToUpper(family[:1]) + strings.Join(version, ".")
}

// isDigits reports whether s is non-empty and all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isAlphaToken reports whether s is non-empty and all ASCII letters.
// Mixed tokens ("5m", "v2") count as neither family nor version and
// are skipped by abbreviateClaudeID.
func isAlphaToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// Task.Status values from Claude 2.1.145+. Shared by the status
// color map, the running-labels filter, and the preview scenarios so
// the strings exist exactly once.
const (
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusKilled    = "killed"
	statusPending   = "pending"
	statusQueued    = "queued"
)

// agentsHandoff collects what the main statusline needs from this
// tick (statusline.WriteAgentsState): one model label per running
// task — the same short form the model chip shows ("O5", "sol ×XH"),
// duplicates intact so the main statusline can count them — plus the
// focused task's label when an agent view is open (any status: a
// completed agent's transcript is still a view worth labeling). Tasks
// Claude sent no model for contribute an empty label; the reader
// substitutes the session's own model, which is what an
// inherited-model task runs. The returned focused pointer is nil when
// no agent view is open (or on stock Claude, which never sets
// Task.Focused).
func agentsHandoff(tasks []Task) ([]string, *string) {
	labels := make([]string, 0, len(tasks))
	var focused *string
	for _, t := range tasks {
		label := shortModelName(t.Model)
		if code := effortCode(string(t.Effort)); label != "" && code != "" {
			label += " ×" + code
		}
		if t.Focused && focused == nil {
			focused = &label
		}
		switch t.Status {
		case statusRunning, statusPending, statusQueued:
			labels = append(labels, label)
		}
	}
	return labels, focused
}

// descriptionMaxRunes caps the description chip's text. Wider than
// the name cap because descriptions are usually full sentences
// ("Audit security findings", "Run integration tests against …").
const descriptionMaxRunes = 28

// renderAgentDescriptionChip builds the secondary chip that conveys
// what the agent is currently doing. Returns (Chip{}, false) when:
//   - the name chip is already showing Label or Description (so a
//     second chip would just repeat the same source under a
//     different truncation — common Task-tool case);
//   - no Label and no Description are set.
//
// Color is ColorSapphire — a cool blue that contrasts with the
// status-colored name chip beside it and with the lavender dir chip
// that follows.
func renderAgentDescriptionChip(t Task) (Chip, bool) {
	_, src := agentNamePicked(t)
	// If the name chip is already showing Label or Description text,
	// don't render a redundant chip beside it.
	if src == sourceLabel || src == sourceDescription {
		return Chip{}, false
	}
	// Prefer Description (full text), fall back to Label.
	text := strings.TrimSpace(stripControl(t.Description))
	if text == "" {
		text = strings.TrimSpace(stripControl(t.Label))
	}
	if text == "" {
		return Chip{}, false
	}
	return Chip{
		Color: ColorSapphire,
		Body:  " " + truncateRunes(text, descriptionMaxRunes) + " ",
	}, true
}

// truncateRunes returns s if its rune count is ≤ max, otherwise the
// first (max-1) runes followed by "…". Uses rune count (not byte
// length or display width) because agent labels are typically ASCII
// prose where rune count tracks visual width closely enough; a
// dedicated runewidth dependency isn't worth it for this one site.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}

// agentStatusColor maps Task.Status strings to palette entries. The
// status set comes from Claude 2.1.145 (subagentStatusLine schema):
// "running", "completed", "failed", "killed", plus intermediate
// states ("pending", "queued"). Unknown statuses use Lavender as a
// neutral fallback.
func agentStatusColor(status string) Color {
	switch status {
	case statusRunning:
		return ColorPeach
	case statusCompleted:
		return ColorGreen
	case statusFailed, statusKilled:
		return ColorRed
	case statusPending, statusQueued:
		return ColorYellow
	default:
		return ColorLavender
	}
}

// agentTypeLabel maps Task.Type to a short fallback label used when
// Task.Name is null. Type values come from Claude 2.1.145.
func agentTypeLabel(taskType string) string {
	switch taskType {
	case "local_agent":
		return "agent"
	case "local_bash":
		return "bash"
	case "local_workflow":
		return "workflow"
	case "monitor_mcp", "mcp_task":
		return "mcp"
	case "in_process_teammate", "remote_agent":
		return "teammate"
	case "dream":
		return "dream"
	default:
		return "task"
	}
}

// renderDirectoryChip builds the directory chip. The cwd is shortened
// to ~/<rel> when under $HOME, kept as-is otherwise. Empty cwd falls
// back to the process's current working directory; if that's also
// unavailable, "?" is the final fallback. The directory chip is
// never dropped from the chain, so a placeholder is always rendered.
func renderDirectoryChip(cwd string) Chip {
	return Chip{
		Color: ColorLavender,
		Body:  " " + stripControl(shortenCWD(cwd)) + " ",
	}
}

func shortenCWD(cwd string) string {
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil && wd != "" {
			cwd = wd
		} else {
			return "?"
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	if cwd == home {
		return "~"
	}
	if strings.HasPrefix(cwd, home+"/") {
		return "~" + cwd[len(home):]
	}
	return cwd
}

// Context-bar tuning constants. Keeping them named so reading the
// renderContextChip body doesn't require chasing magic numbers.
const (
	contextThresholdYellow = 40
	contextThresholdPeach  = 60
	contextThresholdRed    = 80
	contextQuintileSize    = 20 // 100% / 5 quintiles
	contextMaxQuintiles    = 5
	contextPercentFull     = 100
)

// renderContextChip builds the context bar chip. Format is exactly
// " ▰▰▰▱▱ NN% " — leading space, 5 quintile blocks (filled by usage),
// space, percent, trailing space. Background shifts Green / Yellow /
// Peach / Red at 40 / 60 / 80% thresholds.
//
// Defensive against bad input: negative tokens clamp to 0; tokens
// exceeding contextWindow clamp to 100%.
func renderContextChip(tokenCount, contextWindow int) Chip {
	if contextWindow <= 0 {
		contextWindow = 1
	}
	pct := contextPercentFull * tokenCount / contextWindow
	pct = max(0, pct)
	pct = min(contextPercentFull, pct)

	// Quintile count uses ROUND (half-up), not floor. Per epic spec
	// `min(5, round(tokens/window * 5))`. Implemented in integers as
	// `(roundDoubler*pct + quintileSize) / (roundDoubler*quintileSize)` —
	// equivalent to `round(pct/20)` without pulling in math.Round.
	const roundDoubler = 2
	filled := min(contextMaxQuintiles,
		(roundDoubler*pct+contextQuintileSize)/(roundDoubler*contextQuintileSize))
	empty := contextMaxQuintiles - filled

	body := " " + strings.Repeat("▰", filled) + strings.Repeat("▱", empty) +
		fmt.Sprintf(" %d%% ", pct)

	var color Color
	switch {
	case pct < contextThresholdYellow:
		color = ColorGreen
	case pct < contextThresholdPeach:
		color = ColorYellow
	case pct < contextThresholdRed:
		color = ColorPeach
	default:
		color = ColorRed
	}
	return Chip{Color: color, Body: body}
}

// shortSHALen is how many hex chars of a commit SHA we display for
// detached HEADs. 8 is git's default abbreviation length.
const shortSHALen = 8

// renderBranchChip reads <cwd>/.git/HEAD (or dereferences a worktree
// gitdir pointer) and returns the branch chip. Returns (Chip{}, false)
// when:
//   - cwd is empty
//   - no .git exists
//   - .git is a symlink (rejected as a defense against arbitrary-file
//     reads via planted symlinks; same for HEAD)
//   - HEAD content doesn't yield a recognizable branch name
//   - branch name fails validBranchName (rejects names with control
//     bytes, spaces, or leading dash — git's own rules + ANSI safety)
//
// No subprocess fork. At most three syscalls (stat .git, read .git or
// HEAD, optional read of worktree HEAD).
func renderBranchChip(cwd string) (Chip, bool) {
	if cwd == "" {
		return Chip{}, false
	}
	headPath, ok := resolveGitHEADPath(cwd)
	if !ok {
		return Chip{}, false
	}
	// Lstat HEAD before reading: refuse a symlinked HEAD that crosses
	// the cwd boundary. (Inside the .git dir, HEAD is always a regular
	// file in normal repositories.)
	headInfo, err := os.Lstat(headPath)
	if err != nil || headInfo.Mode()&os.ModeSymlink != 0 {
		return Chip{}, false
	}
	//nolint:gosec // resolved path validated above
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return Chip{}, false
	}
	head := strings.TrimSpace(string(raw))
	var name string
	switch {
	case strings.HasPrefix(head, "ref: refs/heads/"):
		name = strings.TrimPrefix(head, "ref: refs/heads/")
	case len(head) >= shortSHALen && isHexPrefix(head, shortSHALen):
		name = head[:shortSHALen]
	default:
		return Chip{}, false
	}
	if !validBranchName.MatchString(name) {
		return Chip{}, false
	}
	return Chip{
		Color: ColorPink,
		Body:  " " + statusline.GitIcon + name + " ",
	}, true
}

// resolveGitHEADPath returns the absolute path to HEAD for the
// repository at cwd, supporting both plain repos (.git is a directory)
// and worktrees (.git is a file with `gitdir: <path>`). Returns false
// if .git is a symlink (refused — same threat model as HEAD symlink),
// missing, or otherwise unresolvable.
func resolveGitHEADPath(cwd string) (string, bool) {
	gitPath := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "HEAD"), true
	}
	// Worktree pointer: a regular file containing `gitdir: <real-path>`.
	//nolint:gosec // gitPath inside a cwd we already trust
	raw, err := os.ReadFile(gitPath)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if rest, ok := strings.CutPrefix(line, "gitdir: "); ok {
			gitDir := strings.TrimSpace(rest)
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(cwd, gitDir)
			}
			return filepath.Join(gitDir, "HEAD"), true
		}
	}
	return "", false
}

// isHexPrefix returns true when the first n bytes of s are all hex
// digits. Used to distinguish a real detached-HEAD SHA from a
// symlinked-into-arbitrary-file degenerate case.
func isHexPrefix(s string, n int) bool {
	if len(s) < n {
		return false
	}
	for i := range n {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// renderAWSChip builds the AWS chip from a pre-fetched profile.
// Empty profile → (Chip{}, false). Profile names containing "prod"
// (case-insensitive) get Peach; others Teal.
//
// This is a deliberately simpler classifier than the main statusline's
// awsBgColor (internal/statusline/env_color.go) which resolves through
// an alias table into a four-state Red/Peach/Green/Teal scheme. The
// subagent statusline runs without the alias resolver dependency, so
// a binary substring-of-"prod" → Peach is the budget-tier rule. Two
// statuslines may therefore color the same profile differently — by
// design, not a bug.
func renderAWSChip(profile string) (Chip, bool) {
	clean := stripControl(profile)
	if clean == "" {
		return Chip{}, false
	}
	color := ColorTeal
	if strings.Contains(strings.ToLower(clean), "prod") {
		color = ColorPeach
	}
	return Chip{
		Color: color,
		Body:  " " + statusline.AwsIcon + clean + " ",
	}, true
}

// renderGCloudChip builds the gcloud chip from a pre-fetched project.
// Empty project → (Chip{}, false).
func renderGCloudChip(project string) (Chip, bool) {
	clean := stripControl(project)
	if clean == "" {
		return Chip{}, false
	}
	return Chip{
		Color: ColorPeach,
		Body:  " " + statusline.GcloudIcon + clean + " ",
	}, true
}

// renderK8sChip builds the kubernetes context chip from a pre-fetched
// context. Empty context → (Chip{}, false).
//
// Note: the true source of "current kubectl context" is ~/.kube/config
// under current-context, but parsing YAML adds a dependency we don't
// otherwise need. If env-driven sourcing proves insufficient in
// practice, that's the right next step.
func renderK8sChip(ctx string) (Chip, bool) {
	clean := stripControl(ctx)
	if clean == "" {
		return Chip{}, false
	}
	return Chip{
		Color: ColorTeal,
		Body:  " " + statusline.K8sIcon + clean + " ",
	}, true
}
