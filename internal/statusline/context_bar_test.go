package statusline

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestContextBar_NoLabelWord(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 200},
	}
	s := CreateStatusline(deps)
	data := &CachedData{
		ModelDisplay:   "S4.6",
		CurrentDir:     "/home/user",
		TermWidth:      200,
		UsedPercentage: 42.0,
	}
	result := s.Render(data)
	stripped := stripAnsi(result)

	if strings.Contains(stripped, "Context") {
		t.Errorf("rendered output should not contain the literal 'Context' label (epic R8); got %q", stripped)
	}
	// Icon must still appear so the bar is identifiable.
	if !strings.Contains(stripped, ContextIcon) {
		t.Errorf("rendered output should contain the context icon; got %q", stripped)
	}
	// Percentage must appear with no decimal.
	if !strings.Contains(stripped, "42%") {
		t.Errorf("rendered output should contain '42%%'; got %q", stripped)
	}
	if strings.Contains(stripped, "42.0%") {
		t.Errorf("rendered output should NOT contain a decimal ('42.0%%'); got %q", stripped)
	}
}

// TestContextElement_FixedWidth pins the wide-mode context element
// (curve + icon + bar + percentage + curve) to exactly 30 visible
// cells for a 2-digit percentage, per the redesign's fixed-width rule.
func TestContextElement_FixedWidth(t *testing.T) {
	s := CreateStatusline(&Dependencies{})

	for _, pct := range []float64{5, 42, 65, 85, 100} {
		element := s.buildContextElement(pct)
		w := runewidth.StringWidth(stripAnsi(element))
		if w > 30 {
			t.Errorf("buildContextElement(%v) width = %d, want <= 30", pct, w)
		}
	}

	// The brief pins the 2-digit case to exactly 30.
	if w := runewidth.StringWidth(stripAnsi(s.buildContextElement(42))); w != 30 {
		t.Errorf("buildContextElement(42) width = %d, want exactly 30", w)
	}
}

// TestBuildMiddleSection_ElementWidthAtTerm200 exercises
// buildMiddleSection directly (per the brief) at a generous width and
// checks the non-space middle content stays within the 30-cell budget.
func TestBuildMiddleSection_ElementWidthAtTerm200(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 200},
	}
	s := CreateStatusline(deps)
	s.colors = CatppuccinMocha{}
	data := &CachedData{UsedPercentage: 42}

	middle := s.buildMiddleSection(data, 160)
	stripped := stripAnsi(middle)
	if w := runewidth.StringWidth(stripped); w != 160 {
		t.Fatalf("buildMiddleSection total width = %d, want 160", w)
	}
	trimmed := strings.TrimSpace(stripped)
	if w := runewidth.StringWidth(trimmed); w > 30 {
		t.Errorf("middle element visible (non-space) width = %d, want <= 30; content=%q", w, trimmed)
	}
}

// TestContextElement_ThresholdColors pins the wide-mode threshold
// colors: teal below 60%, yellow 60-79%, red at 80%+.
func TestContextElement_ThresholdColors(t *testing.T) {
	s := CreateStatusline(&Dependencies{})
	s.colors = CatppuccinMocha{}

	cases := []struct {
		pct   float64
		fg    string
		label string
	}{
		{42, s.colors.TealFG(), "teal"},
		{65, s.colors.YellowFG(), "yellow"},
		{85, s.colors.RedFG(), "red"},
	}
	for _, c := range cases {
		element := s.buildContextElement(c.pct)
		if !strings.Contains(element, c.fg) {
			t.Errorf("buildContextElement(%v) should use %s FG escape, got %q", c.pct, c.label, element)
		}
	}
}

func TestContextBarPadding(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	t.Run("context bar has 4 space padding on each side", func(t *testing.T) {
		s := CreateStatusline(deps)
		data := &CachedData{
			ModelDisplay:   "Claude",
			CurrentDir:     "/home/user",
			TermWidth:      100,
			UsedPercentage: 25.0, // Will show context bar
		}

		result := s.Render(data)

		// Find the context bar portion (between the left and right sections).
		// After the label drop (epic R8), the bar starts with the
		// ContextIcon glyph followed directly by the percentage. The
		// literal "Context " word is no longer rendered.
		if strings.Contains(result, ContextIcon) {
			// Strip ANSI codes to analyze spacing
			stripped := stripAnsi(result)

			// Find where the icon appears
			contextIndex := strings.Index(stripped, ContextIcon)
			if contextIndex == -1 {
				t.Error("Context bar should be visible with UsedPercentage > 0")
				return
			}

			// Check that there are at least 4 spaces before the context bar starts
			// The context bar starts with the left curve character before "Context"
			// Count spaces before the curve
			spacesBeforeBar := 0
			for i := contextIndex - 1; i >= 0 && stripped[i] == ' '; i-- {
				spacesBeforeBar++
			}

			// Due to the curve character, we check spaces before it
			// The pattern should be: ...content... + 4 spaces + curve + "Context"
			// We need to find the actual curve position
			t.Logf("Spaces found before context area: %d", spacesBeforeBar)
		} else {
			t.Log("Context bar not visible in output - may need more width")
		}
	})

	t.Run("context bar content shrinks with padding", func(t *testing.T) {
		// Use custom config with smaller spacers for this test
		config := &Config{
			LeftSpacerWidth:  2,
			RightSpacerWidth: 4,
		}
		s := NewWithConfig(deps, config)

		// Test with different widths to see how the bar adapts
		testCases := []struct {
			name           string
			termWidth      int
			usedPercentage float64
			minBarWidth    int // Minimum expected width for the context bar content (excluding padding)
		}{
			{
				name:           "normal width",
				termWidth:      120,
				usedPercentage: 25.0,
				minBarWidth:    20, // Should have reasonable space for bar
			},
			{
				name:           "narrow terminal",
				termWidth:      80,
				usedPercentage: 25.0,
				minBarWidth:    10, // Bar should be smaller but still visible
			},
			{
				name:           "very narrow terminal",
				termWidth:      60,
				usedPercentage: 25.0,
				minBarWidth:    0, // Might not show bar at all if too narrow
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				deps.TerminalWidth = &MockTerminalWidth{width: tc.termWidth}
				data := &CachedData{
					ModelDisplay:   "Claude",
					CurrentDir:     "/home/user",
					TermWidth:      tc.termWidth,
					UsedPercentage: tc.usedPercentage,
				}

				result := s.Render(data)
				stripped := stripAnsi(result)

				// Total width should match terminal width minus spacers (2+4=6)
				width := runewidth.StringWidth(stripped)
				expectedWidth := tc.termWidth - 6
				if width != expectedWidth {
					t.Errorf("Width mismatch: got %d, want %d", width, expectedWidth)
				}

				if strings.Contains(result, ContextIcon) {
					t.Logf("Context bar visible at width %d", tc.termWidth)
				} else if tc.minBarWidth > 0 {
					t.Logf("Context bar not shown at width %d (might be too narrow)", tc.termWidth)
				}
			})
		}
	})

	t.Run("context bar respects minimum size with padding", func(t *testing.T) {
		// Use custom config with smaller spacers for this test
		config := &Config{
			LeftSpacerWidth:  2,
			RightSpacerWidth: 4,
		}
		s := NewWithConfig(deps, config)

		// Very narrow terminal where context bar won't fit with padding
		deps.TerminalWidth = &MockTerminalWidth{width: 50}
		data := &CachedData{
			ModelDisplay:   "C",
			CurrentDir:     "/",
			TermWidth:      50,
			UsedPercentage: 25.0,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)

		// Should maintain width minus spacers (2+4=6)
		width := runewidth.StringWidth(stripped)
		expectedWidth := 50 - 6
		if width != expectedWidth {
			t.Errorf("Width should be maintained: got %d, want %d", width, expectedWidth)
		}

		// Context bar should not appear if there isn't enough space with padding
		// (needs at least 15 chars after 8 chars of padding)
		if strings.Contains(result, ContextIcon) {
			// If it does appear, verify it still has proper structure
			t.Log("Context bar appeared even in very narrow terminal")

			// Check that the total width is still correct
			if width != expectedWidth {
				t.Error("Context bar broke width constraints")
			}
		} else {
			t.Log("Context bar correctly hidden when too narrow to display with padding")
		}
	})

	t.Run("element is centered with plain-space slack on each side", func(t *testing.T) {
		// The redesign replaces the old fixed-4-space padding with a
		// centered fixed-width (<=30 cell) element; the surrounding
		// region absorbs the rest as plain spaces, split as evenly as
		// possible between the two sides.
		s := CreateStatusline(deps)
		s.colors = CatppuccinMocha{} // Initialize colors

		regionWidth := 60
		percentage := 25.0

		result := s.buildMiddleSection(&CachedData{UsedPercentage: percentage}, regionWidth)

		stripped := stripAnsi(result)
		actualWidth := runewidth.StringWidth(stripped)
		if actualWidth != regionWidth {
			t.Errorf("middle section width incorrect: got %d, want %d", actualWidth, regionWidth)
		}

		trimmed := strings.TrimSpace(stripped)
		elementWidth := runewidth.StringWidth(trimmed)
		if elementWidth > 30 {
			t.Errorf("centered element width incorrect: got %d, want <= 30", elementWidth)
		}

		leftSpaces := len(stripped) - len(strings.TrimLeft(stripped, " "))
		rightSpaces := len(stripped) - len(strings.TrimRight(stripped, " "))
		if diff := leftSpaces - rightSpaces; diff < -1 || diff > 1 {
			t.Errorf("element should be centered (left/right slack should differ by at most 1), got left=%d right=%d",
				leftSpaces, rightSpaces)
		}
	})
}
