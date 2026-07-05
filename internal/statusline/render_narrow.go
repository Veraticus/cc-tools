package statusline

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Veraticus/cc-tools/internal/aliases"
	"github.com/mattn/go-runewidth"
)

// narrowWidthThreshold is the upper bound for narrow-mode activation.
// At or below this detected terminal width, Statusline.Render
// dispatches to the narrow renderer; above it, the existing wide
// layout in render.go runs. 80 cells is the natural breakpoint:
// iPhone landscape sits around 80, portrait often well below it,
// and desktop terminals are routinely 100+.
const narrowWidthThreshold = 80

// Quintile block rendering for the context chip body, matching
// internal/subagentstatusline/chips.go conventions exactly.
const (
	narrowContextMaxQuintiles = 5
	narrowContextQuintileSize = 20  // 100% / 5 quintiles
	narrowContextRoundDoubler = 2   // for integer round-half-up
	narrowContextPercentFull  = 100 // clamp ceiling
)

// Chip kind constants. Stored on narrowChip.Kind to route per-chip
// behavior (truncation order, pivot detection).
const (
	kindDir     = "dir"
	kindContext = "context"
	kindAlarm   = "alarm"
	kindBranch  = "branch"
	kindEnv     = "env"
)

// validNarrowBranchName rejects branch names that would inject ANSI
// escapes or otherwise render badly. Mirrors the regex from
// internal/subagentstatusline/chips.go. Compiled once.
var validNarrowBranchName = regexp.MustCompile(`^[^\x00-\x20\x7f][^\x00-\x1f\x7f ]*$`)

// stripNarrowControl strips C0/C1 control bytes from a string. Used
// on untrusted env values (gcloud project, k8s context, AWS profile,
// cwd, branch name) before they enter chip bodies — otherwise a
// poisoned value could inject terminal escape sequences into the
// rendered tty.
func stripNarrowControl(s string) string {
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

// narrowChip is one chip in the narrow chain.
//
// Color is the palette key (lowercase, matching the strings used in
// render_clouds.go / env_color.go — "lavender", "pink", etc.). The
// ANSI escapes are computed in the composition pass from this key
// via Statusline.getColorBG/FG.
//
// Body is the raw printable content (icon + text). The composition
// pass adds the leading/trailing space padding plus chevrons /
// curves. No ANSI escapes embedded.
//
// Kind is one of kindDir/kindContext/kindBranch/kindEnv — used by
// the truncation pass to know which chips drop first under width
// pressure and which never drop.
type narrowChip struct {
	Color string
	Body  string
	Kind  string
}

// narrowChipCap is the maximum chip count for the narrow chain
// (dir + context + alarm + branch + env). Used to pre-size the chip
// slice.
const narrowChipCap = 5

// gatherNarrowChips returns the chip list for narrow mode in display
// order: dir, context, optional alarm, optional branch, optional env.
// The context chip is always present (UsedPercentage is always
// available, even if 0). The alarm chip (extra-usage emergency
// signal) is included immediately after context when middleChipKind
// classifies the data as chipAlarm — the same decision the wide-mode
// middle cluster uses. Branch is included when CachedData.GitBranch
// is non-empty AND passes the validBranchName check (upstream cache
// layer already resolves .git/HEAD; this layer rejects names that
// would inject terminal escapes).
//
// Env chip is a SINGLE chip from one of AWS / gcloud / k8s, in that
// priority order, first non-empty wins. All chip bodies are passed
// through stripNarrowControl to neutralize injection from poisoned
// .git/HEAD, env, or state-file values.
//
// This pass produces the data shape only — no ANSI, no width
// budgeting, no truncation.
func gatherNarrowChips(deps *Dependencies, data *CachedData) []narrowChip {
	chips := make([]narrowChip, 0, narrowChipCap)
	chips = append(chips, narrowChip{
		Color: colorLavender,
		Body:  stripNarrowControl(formatPath(data.CurrentDir)),
		Kind:  kindDir,
	})

	pct := int(data.UsedPercentage)
	chips = append(chips, narrowChip{
		Color: contextColor(data.UsedPercentage),
		Body:  buildNarrowContextBody(pct),
		Kind:  kindContext,
	})

	if middleChipKind(data) == chipAlarm {
		chips = append(chips, narrowChip{
			Color: colorRed,
			Body:  buildNarrowAlarmBody(data.Cost),
			Kind:  kindAlarm,
		})
	}

	// Narrow mode's branch chip stays branch-only — no dirty/ahead/behind
	// counts, no PR glyph — the narrow width budget is too tight to add
	// them.
	if data.GitBranch != "" && validNarrowBranchName.MatchString(data.GitBranch) {
		chips = append(chips, narrowChip{
			Color: colorPink,
			Body:  GitIcon + stripNarrowControl(data.GitBranch),
			Kind:  kindBranch,
		})
	}

	if chip, ok := firstEnvChip(deps, data); ok {
		chips = append(chips, chip)
	}

	return chips
}

// buildNarrowContextBody renders the context chip body as
// `▰▰▱▱▱ NN%` — 5 quintile blocks (filled by usage) + space + percent.
// Matches the canonical pattern from internal/subagentstatusline.
// Body has NO leading/trailing space — composeNarrowChain adds those.
func buildNarrowContextBody(pct int) string {
	pct = max(0, pct)
	pct = min(narrowContextPercentFull, pct)
	// Round-half-up integer arithmetic, equivalent to round(pct/20).
	filled := min(narrowContextMaxQuintiles,
		(narrowContextRoundDoubler*pct+narrowContextQuintileSize)/
			(narrowContextRoundDoubler*narrowContextQuintileSize))
	empty := narrowContextMaxQuintiles - filled
	return strings.Repeat("▰", filled) +
		strings.Repeat("▱", empty) +
		fmt.Sprintf(" %d%%", pct)
}

// buildNarrowAlarmBody renders the narrow-mode alarm chip body:
// AlarmIcon + "$X.XX". Unlike the wide-mode alarm chip, there's no
// room for the "EXTRA" label at phone widths — the red background
// plus the alarm icon carry the "extra usage" meaning on their own.
func buildNarrowAlarmBody(cost CostInput) string {
	return fmt.Sprintf("%s$%.2f", AlarmIcon, cost.TotalCostUSD)
}

// firstEnvChip returns the highest-priority env chip available, or
// (narrowChip{}, false) if none are set. Priority: AWS, then gcloud
// project (CachedData.GcloudProject), then k8s context
// (CachedData.K8sContext). AWS reads through awsProfileFromEnv
// (which strips the `export AWS_PROFILE=` misconfig).
//
// Color classification reuses env_color.go via deps.Resolver when
// configured. All body content is run through stripNarrowControl to
// prevent ANSI injection from poisoned env values.
func firstEnvChip(deps *Dependencies, data *CachedData) (narrowChip, bool) {
	if deps.EnvReader != nil {
		if profile := awsProfileFromEnv(deps.EnvReader); profile != "" {
			color := colorPeach // default when no resolver
			if deps.Resolver != nil {
				_, env := deps.Resolver.Resolve(aliases.KindAWS, profile)
				color = awsBgColor(env)
			}
			return narrowChip{
				Color: color,
				Body:  AwsIcon + stripNarrowControl(profile),
				Kind:  kindEnv,
			}, true
		}
	}
	if data.GcloudProject != "" {
		color := colorLavender
		if deps.Resolver != nil {
			_, env := deps.Resolver.Resolve(aliases.KindGcloud, data.GcloudProject)
			color = gcloudBgColor(env)
		}
		return narrowChip{
			Color: color,
			Body:  GcloudIcon + stripNarrowControl(data.GcloudProject),
			Kind:  kindEnv,
		}, true
	}
	if data.K8sContext != "" {
		color := colorTeal
		if deps.Resolver != nil {
			_, env := deps.Resolver.Resolve(aliases.KindK8s, data.K8sContext)
			color = k8sBgColor(env)
		}
		return narrowChip{
			Color: color,
			Body:  K8sIcon + stripNarrowControl(data.K8sContext),
			Kind:  kindEnv,
		}, true
	}
	return narrowChip{}, false
}

// narrowChevronColors returns the bg escape, fg escape, and glyph for
// the interior chevron between chip (current) and next. The two
// chevron glyphs are mirror images of each other, so the color
// convention mirrors too:
//   - prePivot (LeftChevron, points right): bg = next chip,
//     fg = current chip.
//   - post-pivot (RightChevron, points left): bg = current chip,
//     fg = next chip.
//
// In both cases, when the adjacent chips share a bg color (e.g.
// context=green next to AWS-dev=green), the default chevron fg would
// equal the bg and the glyph would disappear, making the chips
// visually fuse. Fall back to the dark BaseFG so the boundary stays
// legible.
func (s *Statusline) narrowChevronColors(chip, next narrowChip, prePivot bool) (string, string, string) {
	var bg, fg, glyph string
	if prePivot {
		bg = s.getColorBG(next.Color)
		fg = s.getColorFG(chip.Color)
		glyph = LeftChevron
	} else {
		bg = s.getColorBG(chip.Color)
		fg = s.getColorFG(next.Color)
		glyph = RightChevron
	}
	if chip.Color == next.Color {
		fg = s.colors.BaseFG()
	}
	return bg, fg, glyph
}

// composeNarrowChain renders the chip slice as an ANSI string framed
// by LeftCurve at the start and RightCurve at the end. Interior
// chevrons mirror around the context chip: forward-pointing
// (LeftChevron, U+E0B0) before the pivot, backward-pointing
// (RightChevron, U+E0B2) at and after the pivot.
//
// The pivot is "the chip immediately after the context chip" — where
// direction reverses. If context is the last chip (no chip after it),
// there's no pivot and all chevrons are forward.
//
// Each chip body is wrapped as `<bg><baseFG> <body> <NC>`. Empty
// input returns "".
func (s *Statusline) composeNarrowChain(chips []narrowChip) string {
	if len(chips) == 0 {
		return ""
	}

	// Pivot index: first chip whose Kind comes AFTER context in
	// display order. If context is absent (defensive) or is the last
	// chip, pivot is len(chips) — all chevrons are forward.
	pivot := len(chips)
	for i, c := range chips {
		if c.Kind == kindContext && i+1 < len(chips) {
			pivot = i + 1
			break
		}
	}

	var sb strings.Builder

	// Leading LeftCurve in the first chip's FG, terminal-default bg.
	// chips[0] is safe — guarded by `len(chips) == 0` early-return.
	//nolint:gosec // G602 false positive: length-checked above
	first := chips[0]
	sb.WriteString(s.getColorFG(first.Color))
	sb.WriteString(LeftCurve)
	sb.WriteString(s.colors.NC())

	for i, chip := range chips {
		// Chip body: bg + base fg + padded body + reset.
		sb.WriteString(s.getColorBG(chip.Color))
		sb.WriteString(s.colors.BaseFG())
		sb.WriteString(" ")
		sb.WriteString(chip.Body)
		sb.WriteString(" ")
		sb.WriteString(s.colors.NC())

		// Interior chevron between chip[i] and chip[i+1].
		if i+1 < len(chips) {
			next := chips[i+1]
			bg, fg, glyph := s.narrowChevronColors(chip, next, i+1 < pivot)
			sb.WriteString(bg)
			sb.WriteString(fg)
			sb.WriteString(glyph)
			sb.WriteString(s.colors.NC())
		}
	}

	// Trailing RightCurve in the last chip's FG, terminal-default bg.
	last := chips[len(chips)-1]
	sb.WriteString(s.getColorFG(last.Color))
	sb.WriteString(RightCurve)
	sb.WriteString(s.colors.NC())

	return sb.String()
}

// Width-fit overhead constants. A chain of N chips has:
//   - 1 cell LeftCurve
//   - 1 cell RightCurve
//   - N-1 cells of chevrons (one between each adjacent pair)
//   - 2 cells of padding per chip (the leading/trailing spaces
//     composeNarrowChain adds around each body)
//
// So fixed overhead per chain = 2 + (N-1) + 2N = 3N + 1.
const (
	narrowFixedOverheadPerChip = 3 // 2 padding + 1 chevron (chevron count is N-1, off-by-one absorbed into the base)
	narrowChainBaseCells       = 1 // 2 (curves) − 1 (chevron count is N-1)
)

// fitNarrowChain returns the chip slice trimmed and modified so that
// composeNarrowChain(result) produces a string of exactly `budget`
// visible cells. The directory chip always survives.
//
// When chips fit with slack, the context chip's Body is expanded
// (center-aligned content + colored padding) to absorb it. When chips
// overflow, they're dropped in priority order:
// env → branch → truncate-dir-to-leaf → drop-context → truncate-dir
// with ellipsis. The alarm chip (kindAlarm) is deliberately absent
// from this drop order — dropOneNarrowChip never removes it, so it
// survives even the drop-context step and only ever loses width
// pressure to the dir chip's own truncation. It is an emergency
// signal (epic requirement) and must never be muted or downgraded.
//
// Pure function: returns a new slice; input is not mutated.
func fitNarrowChain(chips []narrowChip, budget int) []narrowChip {
	work := append([]narrowChip(nil), chips...)
	for {
		if len(work) == 0 {
			return work
		}
		n := len(work)
		fixedOverhead := narrowFixedOverheadPerChip*n + narrowChainBaseCells
		bodiesSum := 0
		for _, c := range work {
			bodiesSum += runewidth.StringWidth(c.Body)
		}
		total := fixedOverhead + bodiesSum

		switch {
		case total == budget:
			return work
		case total < budget:
			slack := budget - total
			for i, c := range work {
				if c.Kind == kindContext {
					target := runewidth.StringWidth(c.Body) + slack
					work[i] = narrowChip{
						Color: c.Color,
						Body:  padContextBody(c.Body, target),
						Kind:  c.Kind,
					}
					return work
				}
			}
			// No context chip — slack goes to dir as trailing padding.
			work[0] = narrowChip{
				Color: work[0].Color,
				Body:  work[0].Body + strings.Repeat(" ", slack),
				Kind:  work[0].Kind,
			}
			return work
		}

		// total > budget. Drop one chip / truncate and retry.
		if dropped, ok := dropOneNarrowChip(work); ok {
			work = dropped
			continue
		}
		// Last resort: truncate dir to fit. Body should occupy
		// `budget - (3*1 + 1) = budget - 4` cells.
		const dirAloneOverhead = 4
		target := max(1, budget-dirAloneOverhead)
		work[0] = narrowChip{
			Color: work[0].Color,
			Body:  truncateText(work[0].Body, target),
			Kind:  kindDir,
		}
		return work
	}
}

// dropOneNarrowChip drops a single chip per priority: env → branch →
// truncate-dir-to-leaf → drop-context. Returns (modifiedSlice, true)
// when a drop/truncate happened, or (slice, false) when only dir
// remains.
func dropOneNarrowChip(chips []narrowChip) ([]narrowChip, bool) {
	if idx := indexOfKind(chips, kindEnv); idx >= 0 {
		return removeAt(chips, idx), true
	}
	if idx := indexOfKind(chips, kindBranch); idx >= 0 {
		return removeAt(chips, idx), true
	}
	// Try truncating dir to its leaf component.
	if idx := indexOfKind(chips, kindDir); idx >= 0 && strings.Contains(chips[idx].Body, "/") {
		leaf := filepath.Base(chips[idx].Body)
		out := append([]narrowChip(nil), chips...)
		out[idx] = narrowChip{
			Color: chips[idx].Color,
			Body:  leaf,
			Kind:  kindDir,
		}
		return out, true
	}
	if idx := indexOfKind(chips, kindContext); idx >= 0 {
		return removeAt(chips, idx), true
	}
	return chips, false
}

func indexOfKind(chips []narrowChip, kind string) int {
	for i, c := range chips {
		if c.Kind == kind {
			return i
		}
	}
	return -1
}

func removeAt(chips []narrowChip, idx int) []narrowChip {
	out := make([]narrowChip, 0, len(chips)-1)
	out = append(out, chips[:idx]...)
	out = append(out, chips[idx+1:]...)
	return out
}

// padContextBody centers the original body inside a wider chip body
// of `targetWidth` cells. Odd slack puts the extra cell on the right
// (by convention). If targetWidth ≤ width(body), returns body
// unchanged — let the chain be slightly over-budget rather than
// corrupt the content.
func padContextBody(body string, targetWidth int) string {
	current := runewidth.StringWidth(body)
	if targetWidth <= current {
		return body
	}
	const halfDivisor = 2
	slack := targetWidth - current
	left := slack / halfDivisor
	right := slack - left
	return strings.Repeat(" ", left) + body + strings.Repeat(" ", right)
}

// renderNarrow is the top-level narrow-mode entry point. Gathers
// chips, fits to width, composes the ANSI chain. The result's visible
// width is exactly `width` cells under normal conditions; any
// deviation is logged when DEBUG_WIDTH=1 so smoke tests catch math
// regressions.
func (s *Statusline) renderNarrow(data *CachedData, width int) string {
	chips := gatherNarrowChips(s.deps, data)
	fitted := fitNarrowChain(chips, width)
	result := s.composeNarrowChain(fitted)
	if os.Getenv("DEBUG_WIDTH") == "1" {
		actual := runewidth.StringWidth(stripAnsi(result))
		if actual != width {
			fmt.Fprintf(os.Stderr,
				"renderNarrow: budget=%d actual=%d chips=%d\n",
				width, actual, len(fitted))
		}
	}
	return result
}
