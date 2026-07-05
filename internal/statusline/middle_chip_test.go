package statusline

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// --- middleChipKind: the four display rules ---

func TestMiddleChipKind_Alarm(t *testing.T) {
	data := &CachedData{
		RateLimits: &RateLimitsInput{
			FiveHour: &RateLimitWindow{UsedPercentage: 100, ResetsAt: 1000},
		},
	}
	if got := middleChipKind(data); got != chipAlarm {
		t.Errorf("middleChipKind() = %v, want chipAlarm", got)
	}
}

func TestMiddleChipKind_AlarmAboveHundred(t *testing.T) {
	data := &CachedData{
		RateLimits: &RateLimitsInput{
			FiveHour: &RateLimitWindow{UsedPercentage: 143, ResetsAt: 1000},
		},
	}
	if got := middleChipKind(data); got != chipAlarm {
		t.Errorf("middleChipKind() = %v, want chipAlarm", got)
	}
}

func TestMiddleChipKind_RateLimit(t *testing.T) {
	data := &CachedData{
		RateLimits: &RateLimitsInput{
			FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: 1000},
		},
	}
	if got := middleChipKind(data); got != chipRateLimit {
		t.Errorf("middleChipKind() = %v, want chipRateLimit", got)
	}
}

// RateLimits present but FiveHour nil (only SevenDay reported) is
// still the plain rate-limit chip, never the alarm — the alarm rule
// keys specifically off FiveHour.
func TestMiddleChipKind_RateLimitNoFiveHour(t *testing.T) {
	data := &CachedData{
		RateLimits: &RateLimitsInput{
			SevenDay: &RateLimitWindow{UsedPercentage: 100, ResetsAt: 1000},
		},
	}
	if got := middleChipKind(data); got != chipRateLimit {
		t.Errorf("middleChipKind() = %v, want chipRateLimit", got)
	}
}

func TestMiddleChipKind_Cost(t *testing.T) {
	data := &CachedData{
		Cost: CostInput{TotalCostUSD: 4.12},
	}
	if got := middleChipKind(data); got != chipCost {
		t.Errorf("middleChipKind() = %v, want chipCost", got)
	}
}

func TestMiddleChipKind_None(t *testing.T) {
	data := &CachedData{}
	if got := middleChipKind(data); got != chipNone {
		t.Errorf("middleChipKind() = %v, want chipNone", got)
	}
}

func TestMiddleChipKind_NoneWhenCostZero(t *testing.T) {
	data := &CachedData{Cost: CostInput{TotalCostUSD: 0}}
	if got := middleChipKind(data); got != chipNone {
		t.Errorf("middleChipKind() = %v, want chipNone", got)
	}
}

// --- Rendering: rate-limit chip ---

func TestBuildRateLimitChip_ContainsBothWindowsAndSapphireBg(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	now := time.Unix(1751700000, 0)
	rl := &RateLimitsInput{
		FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: now.Add(3*time.Hour + 47*time.Minute).Unix()},
		SevenDay: &RateLimitWindow{UsedPercentage: 41, ResetsAt: now.Add(48 * time.Hour).Unix()},
	}
	chip := s.buildRateLimitChip(rl, now)

	if !strings.Contains(chip, "5h 23%") {
		t.Errorf("chip should contain '5h 23%%'; got %q", chip)
	}
	if !strings.Contains(chip, "7d 41%") {
		t.Errorf("chip should contain '7d 41%%'; got %q", chip)
	}
	if !strings.Contains(chip, s.colors.SapphireBG()) {
		t.Errorf("chip should use sapphire bg escape; got %q", chip)
	}
}

func TestBuildRateLimitChip_FiveHourOnly(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	now := time.Unix(1751700000, 0)
	rl := &RateLimitsInput{
		FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: now.Add(1 * time.Hour).Unix()},
	}
	chip := s.buildRateLimitChip(rl, now)
	stripped := stripAnsi(chip)

	if !strings.Contains(stripped, "5h 23%") {
		t.Errorf("chip should contain '5h 23%%'; got %q", stripped)
	}
	if strings.Contains(stripped, "7d") {
		t.Errorf("chip should not mention '7d' when seven_day is absent; got %q", stripped)
	}
}

// --- Rendering: cost chip ---

func TestBuildCostChip_ContainsDollarAmount(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := s.buildCostChip(CostInput{TotalCostUSD: 4.12})
	if !strings.Contains(chip, "$4.12") {
		t.Errorf("chip should contain '$4.12'; got %q", chip)
	}
	if !strings.Contains(chip, s.colors.SapphireBG()) {
		t.Errorf("chip should use sapphire bg escape; got %q", chip)
	}
}

func TestBuildCostChip_AlwaysTwoDecimals(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := s.buildCostChip(CostInput{TotalCostUSD: 4})
	if !strings.Contains(chip, "$4.00") {
		t.Errorf("chip should contain '$4.00'; got %q", chip)
	}
}

// --- Rendering: alarm chip ---

func TestBuildAlarmChip_ContainsExtraAndRedBg(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	chip := s.buildAlarmChip(CostInput{TotalCostUSD: 4.12})
	if !strings.Contains(chip, "EXTRA") {
		t.Errorf("chip should contain 'EXTRA'; got %q", chip)
	}
	if !strings.Contains(chip, "$4.12") {
		t.Errorf("chip should contain '$4.12'; got %q", chip)
	}
	if !strings.Contains(chip, s.colors.RedBG()) {
		t.Errorf("chip should use red bg escape; got %q", chip)
	}
}

// --- Powerline invariants (audited manually here; the orchestrator's
// ANSI decoder re-verifies across all goldens) ---

func TestBuildRateLimitChip_PowerlineInvariants(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	now := time.Unix(1751700000, 0)
	rl := &RateLimitsInput{FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: now.Add(time.Hour).Unix()}}
	chip := s.buildRateLimitChip(rl, now)

	wantPrefix := s.colors.SapphireFG() + LeftCurve + s.colors.NC()
	if !strings.HasPrefix(chip, wantPrefix) {
		t.Errorf("chip should start with sapphire-fg LeftCurve (no bg); got %q", chip)
	}
	wantSuffix := s.colors.SapphireFG() + RightCurve + s.colors.NC()
	if !strings.HasSuffix(chip, wantSuffix) {
		t.Errorf("chip should end with sapphire-fg RightCurve (no bg); got %q", chip)
	}
	if !strings.Contains(chip, s.colors.SapphireBG()+s.colors.BaseFG()) {
		t.Errorf("chip body should be sapphire bg + BaseFG text; got %q", chip)
	}
}

// --- Pace arrows ---

func TestPaceArrow_AheadOfPace(t *testing.T) {
	now := time.Unix(1751700000, 0)
	windowLen := 5 * time.Hour
	// 10% elapsed (30 min into a 5h window), used 90%.
	win := &RateLimitWindow{UsedPercentage: 90, ResetsAt: now.Add(windowLen - 30*time.Minute).Unix()}
	if got := paceArrow(win, windowLen, now); got != "⇡" {
		t.Errorf("paceArrow() = %q, want ⇡", got)
	}
}

func TestPaceArrow_BehindPace(t *testing.T) {
	now := time.Unix(1751700000, 0)
	windowLen := 5 * time.Hour
	// 90% elapsed, used only 10%.
	win := &RateLimitWindow{UsedPercentage: 10, ResetsAt: now.Add(windowLen / 10).Unix()}
	if got := paceArrow(win, windowLen, now); got != "⇣" {
		t.Errorf("paceArrow() = %q, want ⇣", got)
	}
}

func TestPaceArrow_OnPace_NoArrow(t *testing.T) {
	now := time.Unix(1751700000, 0)
	windowLen := 5 * time.Hour
	// 50% elapsed, used 50% — dead on pace.
	win := &RateLimitWindow{UsedPercentage: 50, ResetsAt: now.Add(windowLen / 2).Unix()}
	if got := paceArrow(win, windowLen, now); got != "" {
		t.Errorf("paceArrow() = %q, want no arrow", got)
	}
}

func TestPaceArrow_StaleResetsAt_NoArrow(t *testing.T) {
	now := time.Unix(1751700000, 0)
	windowLen := 5 * time.Hour
	// resets_at in the past: stale payload, must not show an arrow
	// even though the naive used-vs-expected comparison would.
	win := &RateLimitWindow{UsedPercentage: 90, ResetsAt: now.Add(-time.Minute).Unix()}
	if got := paceArrow(win, windowLen, now); got != "" {
		t.Errorf("paceArrow() = %q, want no arrow for stale resets_at", got)
	}
}

func TestPaceArrow_ResetsAtExactlyNow_NoArrow(t *testing.T) {
	now := time.Unix(1751700000, 0)
	windowLen := 5 * time.Hour
	win := &RateLimitWindow{UsedPercentage: 90, ResetsAt: now.Unix()}
	if got := paceArrow(win, windowLen, now); got != "" {
		t.Errorf("paceArrow() = %q, want no arrow when resets_at == now", got)
	}
}

// --- Countdown ---
// Format tiers by magnitude: >= 24h renders days+hours ("2d0h"),
// >= 1h renders hours+minutes ("3h47m"), < 1h renders bare minutes
// ("23m").

func TestFormatCountdown_HoursAndMinutes(t *testing.T) {
	now := time.Unix(1751700000, 0)
	resetsAt := now.Add(3*time.Hour + 47*time.Minute).Unix()
	if got := formatCountdown(resetsAt, now); got != "3h47m" {
		t.Errorf("formatCountdown() = %q, want 3h47m", got)
	}
}

func TestFormatCountdown_DaysAndHours(t *testing.T) {
	now := time.Unix(1751700000, 0)
	cases := []struct {
		remaining time.Duration
		want      string
	}{
		{48 * time.Hour, "2d0h"},
		{47 * time.Hour, "1d23h"},
		{24 * time.Hour, "1d0h"},
		// Just under the day threshold stays in hours+minutes.
		{23*time.Hour + 59*time.Minute, "23h59m"},
	}
	for _, c := range cases {
		resetsAt := now.Add(c.remaining).Unix()
		if got := formatCountdown(resetsAt, now); got != c.want {
			t.Errorf("formatCountdown(now+%v) = %q, want %q", c.remaining, got, c.want)
		}
	}
}

func TestFormatCountdown_MinutesOnly(t *testing.T) {
	now := time.Unix(1751700000, 0)
	cases := []struct {
		remaining time.Duration
		want      string
	}{
		{23 * time.Minute, "23m"},
		{59 * time.Minute, "59m"},
		// Exactly one hour crosses into hours+minutes.
		{time.Hour, "1h0m"},
		// Clamped negative/zero remaining renders as zero minutes.
		{0, "0m"},
		{-time.Minute, "0m"},
	}
	for _, c := range cases {
		resetsAt := now.Add(c.remaining).Unix()
		if got := formatCountdown(resetsAt, now); got != c.want {
			t.Errorf("formatCountdown(now+%v) = %q, want %q", c.remaining, got, c.want)
		}
	}
}

func TestBuildRateLimitChip_CountdownForHigherUsedWindow(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}
	now := time.Unix(1751700000, 0)
	rl := &RateLimitsInput{
		FiveHour: &RateLimitWindow{UsedPercentage: 23, ResetsAt: now.Add(3*time.Hour + 47*time.Minute).Unix()},
		SevenDay: &RateLimitWindow{UsedPercentage: 41, ResetsAt: now.Add(48 * time.Hour).Unix()},
	}
	chip := s.buildRateLimitChip(rl, now)
	stripped := stripAnsi(chip)
	// SevenDay has the higher used% (41 > 23), so its reset (48h from
	// now, rendered days-first) is the one that should appear as the
	// countdown.
	if !strings.Contains(stripped, "(2d0h)") {
		t.Errorf("chip should contain countdown for the higher-used window '(2d0h)'; got %q", stripped)
	}
}

// --- middle cluster assembly: context-element-drops-first under
// width pressure in wide mode ---

func TestBuildMiddleSection_DropsContextBeforeChipWhenTight(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 200},
	}
	s := CreateStatusline(deps)
	s.colors = CatppuccinMocha{}
	data := &CachedData{
		UsedPercentage: 42,
		Cost:           CostInput{TotalCostUSD: 4.12},
	}

	// Wide enough for the chip alone, too narrow for context+chip together.
	chipOnly := s.buildCostChip(data.Cost)
	chipWidth := runewidth.StringWidth(stripAnsi(chipOnly))
	tightWidth := chipWidth + 2

	middle := s.buildMiddleSection(data, tightWidth)
	stripped := stripAnsi(middle)
	if !strings.Contains(stripped, "$4.12") {
		t.Errorf("chip should still render when context+chip don't both fit; got %q", stripped)
	}
	if strings.Contains(stripped, ContextIcon) {
		t.Errorf("context element should be dropped in favor of the chip under width pressure; got %q", stripped)
	}
}
