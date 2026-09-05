package statusline

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

const (
	requiredPiHarness  = "pi"
	requiredPiProvider = "openai-codex"
	requiredPiBaseURL  = "https://chatgpt.com/backend-api"
	piQuotaFreshLabel  = "fresh"
	piQuotaStaleLabel  = "stale"
)

// piQuotaFreshness is the validated display state for a scoped quota sample.
// Unknown samples deliberately carry no metrics, preventing an expired value
// from reappearing through a more compact rendering path.
type piQuotaFreshness int

const (
	piQuotaUnknown piQuotaFreshness = iota
	piQuotaFresh
	piQuotaStale
)

type piQuotaWindow struct {
	known            bool
	remainingPercent float64
	resetAtMillis    int64
}

type piQuotaState struct {
	freshness piQuotaFreshness
	fiveHour  piQuotaWindow
	weekly    piQuotaWindow
}

// normalizePiQuota both applies the exact Pi/Codex namespace gate and validates
// freshness/windows. nil means the namespace does not apply and callers must
// retain the legacy rendering path; non-nil always renders an explicit quota
// state, including unknown/cleared samples.
func normalizePiQuota(
	harness, modelProvider string,
	quota *StewardQuotaInput,
	now time.Time,
) *piQuotaState {
	if harness != requiredPiHarness || modelProvider != requiredPiProvider || quota == nil ||
		quota.Provider != requiredPiProvider || quota.BaseURL != requiredPiBaseURL {
		return nil
	}

	state := &piQuotaState{freshness: quotaFreshness(quota, now)}
	if state.freshness == piQuotaUnknown {
		return state
	}
	state.fiveHour = normalizePiQuotaWindow(quota.Windows.FiveHour)
	state.weekly = normalizePiQuotaWindow(quota.Windows.Weekly)
	return state
}

func quotaFreshness(quota *StewardQuotaInput, now time.Time) piQuotaFreshness {
	if quota.FetchedAt == nil || quota.Stale == nil || *quota.FetchedAt <= 0 {
		return piQuotaUnknown
	}

	nowMillis := now.UnixMilli()
	if *quota.FetchedAt > nowMillis {
		return piQuotaUnknown
	}
	ageMillis := nowMillis - *quota.FetchedAt
	const (
		freshForMillis  = int64(5 * time.Minute / time.Millisecond)
		retainForMillis = int64(15 * time.Minute / time.Millisecond)
	)
	switch {
	case ageMillis >= retainForMillis:
		return piQuotaUnknown
	case *quota.Stale || ageMillis >= freshForMillis:
		return piQuotaStale
	default:
		return piQuotaFresh
	}
}

func normalizePiQuotaWindow(input *StewardQuotaWindowInput) piQuotaWindow {
	if input == nil || input.RemainingPercent == nil || input.ResetAt == nil {
		return piQuotaWindow{}
	}
	remaining := *input.RemainingPercent
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining < 0 || remaining > 100 ||
		*input.ResetAt < 0 {
		return piQuotaWindow{}
	}
	return piQuotaWindow{
		known:            true,
		remainingPercent: remaining,
		resetAtMillis:    *input.ResetAt,
	}
}

func (f piQuotaFreshness) String() string {
	switch f {
	case piQuotaFresh:
		return piQuotaFreshLabel
	case piQuotaStale:
		return piQuotaStaleLabel
	case piQuotaUnknown:
		return unknownLabel
	default:
		return unknownLabel
	}
}

func piQuotaColor(state *piQuotaState) string {
	switch state.freshness {
	case piQuotaFresh:
		return colorGreen
	case piQuotaStale:
		return colorYellow
	case piQuotaUnknown:
		return colorSapphire
	default:
		return colorSapphire
	}
}

func buildPiQuotaFullBody(state *piQuotaState, now time.Time) string {
	return RateLimitIcon + strings.Join([]string{
		formatPiQuotaFullWindow("5h", state.fiveHour, now),
		formatPiQuotaFullWindow("7d", state.weekly, now),
		state.freshness.String(),
	}, " · ")
}

func formatPiQuotaFullWindow(label string, window piQuotaWindow, now time.Time) string {
	if !window.known {
		return label + "?"
	}
	return fmt.Sprintf("%s %.0f%% resets %s", label, window.remainingPercent,
		formatPiQuotaReset(window.resetAtMillis, now))
}

func buildPiQuotaCompactBody(state *piQuotaState, now time.Time) string {
	return RateLimitIcon + strings.Join([]string{
		formatPiQuotaCompactWindow("5h", state.fiveHour, now),
		formatPiQuotaCompactWindow("7d", state.weekly, now),
		state.freshness.String(),
	}, " ")
}

func formatPiQuotaCompactWindow(label string, window piQuotaWindow, now time.Time) string {
	if !window.known {
		return label + "?"
	}
	return fmt.Sprintf("%s%.0f%%@%s", label, window.remainingPercent,
		formatPiQuotaCompactReset(window.resetAtMillis, now))
}

func buildPiQuotaSummaryBody(state *piQuotaState) string {
	return RateLimitIcon + "quota " + state.freshness.String()
}

// formatPiQuotaReset formats a Unix-millisecond reset without constructing a
// potentially overflowing time.Duration. Past resets are honest "now" values,
// and very distant values are capped so hostile timestamps cannot consume the
// statusline's width budget.
func formatPiQuotaReset(resetAtMillis int64, now time.Time) string {
	nowMillis := now.UnixMilli()
	if resetAtMillis <= nowMillis {
		return "now"
	}

	remainingMillis := resetAtMillis - nowMillis
	const (
		minuteMillis = int64(time.Minute / time.Millisecond)
		hourMillis   = int64(time.Hour / time.Millisecond)
		dayMillis    = 24 * hourMillis
		maxDays      = int64(999)
	)
	if remainingMillis < minuteMillis {
		return "<1m"
	}
	if remainingMillis < hourMillis {
		return fmt.Sprintf("%dm", remainingMillis/minuteMillis)
	}
	if remainingMillis < dayMillis {
		hours := remainingMillis / hourMillis
		minutes := (remainingMillis % hourMillis) / minuteMillis
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}

	days := remainingMillis / dayMillis
	if days > maxDays {
		return ">999d"
	}
	hours := (remainingMillis % dayMillis) / hourMillis
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, hours)
}

// formatPiQuotaCompactReset bounds reset tokens at five cells. Rich reset
// output remains available to the full chip; only a compact token that would
// exceed the bound falls back to its honest, floored largest unit.
func formatPiQuotaCompactReset(resetAtMillis int64, now time.Time) string {
	reset := formatPiQuotaReset(resetAtMillis, now)
	const maxCompactResetWidth = 5
	if runewidth.StringWidth(reset) <= maxCompactResetWidth {
		return reset
	}

	remainingMillis := resetAtMillis - now.UnixMilli()
	const (
		hourMillis = int64(time.Hour / time.Millisecond)
		dayMillis  = 24 * hourMillis
	)
	if remainingMillis < dayMillis {
		return fmt.Sprintf("%dh", remainingMillis/hourMillis)
	}
	return fmt.Sprintf("%dd", remainingMillis/dayMillis)
}

func piQuotaRenderNow(deps *Dependencies) time.Time {
	if deps != nil && deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

func (s *Statusline) buildPiQuotaChip(state *piQuotaState, now time.Time, compact bool) string {
	body := buildPiQuotaFullBody(state, now)
	if compact {
		body = buildPiQuotaCompactBody(state, now)
	}
	return s.buildPowerlineChip(body, piQuotaColor(state))
}

func (s *Statusline) buildSqueezedPiQuotaChip(state *piQuotaState, now time.Time, width int) string {
	compactBody := buildPiQuotaCompactBody(state, now)
	compact := s.buildPowerlineChip(compactBody, piQuotaColor(state))
	compactWidth := runewidth.StringWidth(stripAnsi(compact))
	if compactWidth <= width {
		return s.centerElement(compact, compactWidth, width)
	}

	const chipChromeWidth = 4
	bodyBudget := width - chipChromeWidth
	if bodyBudget < 1 {
		return s.getColorBG(piQuotaColor(state)) + strings.Repeat(" ", max(0, width)) + s.colors.NC()
	}
	body := fitPiQuotaBody(compactBody, bodyBudget)
	chip := s.buildPowerlineChip(body, piQuotaColor(state))
	return s.centerElement(chip, runewidth.StringWidth(stripAnsi(chip)), width)
}

// fitPiQuotaBody retains a complete compact body whenever possible, then
// removes the decorative icon as a semantic compact tier before sacrificing
// metrics. At pathological widths it switches to an honest quota marker rather
// than exposing a clipped percentage or reset that could be mistaken for
// complete data.
func fitPiQuotaBody(body string, width int) string {
	if runewidth.StringWidth(body) <= width {
		return body
	}
	semantic := strings.TrimPrefix(body, RateLimitIcon)
	if runewidth.StringWidth(semantic) <= width {
		return semantic
	}
	marker := RateLimitIcon + "quota"
	if runewidth.StringWidth(marker) <= width {
		return marker
	}
	return truncateText(marker, max(1, width))
}
