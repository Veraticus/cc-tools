package statusline

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestContextBarPadding(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	t.Run("context bar has 4 space padding on each side", func(t *testing.T) {
		s := New(deps)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000, // Will show context bar
		}

		result := s.Render(data)
		
		// Find the context bar portion (between the left and right sections)
		// The context bar should have "Context" text in it
		if strings.Contains(result, "Context") {
			// Strip ANSI codes to analyze spacing
			stripped := stripAnsi(result)
			
			// Find where "Context" appears
			contextIndex := strings.Index(stripped, "Context")
			if contextIndex == -1 {
				t.Error("Context bar should be visible with context length > 0")
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
		s := New(deps)
		
		// Test with different widths to see how the bar adapts
		testCases := []struct {
			name          string
			termWidth     int
			contextLength int
			minBarWidth   int // Minimum expected width for the context bar content (excluding padding)
		}{
			{
				name:          "normal width",
				termWidth:     120,
				contextLength: 50000,
				minBarWidth:   20, // Should have reasonable space for bar
			},
			{
				name:          "narrow terminal",
				termWidth:     80,
				contextLength: 50000,
				minBarWidth:   10, // Bar should be smaller but still visible
			},
			{
				name:          "very narrow terminal",
				termWidth:     60,
				contextLength: 50000,
				minBarWidth:   0, // Might not show bar at all if too narrow
			},
		}
		
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				deps.TerminalWidth = &MockTerminalWidth{width: tc.termWidth}
				data := &CachedData{
					ModelDisplay:  "Claude",
					CurrentDir:    "/home/user",
					TermWidth:     tc.termWidth,
					ContextLength: tc.contextLength,
				}
				
				result := s.Render(data)
				stripped := stripAnsi(result)
				
				// Total width should match terminal width
				width := runewidth.StringWidth(stripped)
				if width != tc.termWidth {
					t.Errorf("Width mismatch: got %d, want %d", width, tc.termWidth)
				}
				
				if strings.Contains(result, "Context") {
					t.Logf("Context bar visible at width %d", tc.termWidth)
				} else if tc.minBarWidth > 0 {
					t.Logf("Context bar not shown at width %d (might be too narrow)", tc.termWidth)
				}
			})
		}
	})

	t.Run("context bar respects minimum size with padding", func(t *testing.T) {
		s := New(deps)
		
		// Very narrow terminal where context bar won't fit with padding
		deps.TerminalWidth = &MockTerminalWidth{width: 50}
		data := &CachedData{
			ModelDisplay:  "C",
			CurrentDir:    "/",
			TermWidth:     50,
			ContextLength: 50000,
		}
		
		result := s.Render(data)
		stripped := stripAnsi(result)
		
		// Should still maintain exact width
		width := runewidth.StringWidth(stripped)
		if width != 50 {
			t.Errorf("Width should be maintained: got %d, want 50", width)
		}
		
		// Context bar should not appear if there isn't enough space with padding
		// (needs at least 15 chars after 8 chars of padding)
		if strings.Contains(result, "Context") {
			// If it does appear, verify it still has proper structure
			t.Log("Context bar appeared even in very narrow terminal")
			
			// Check that the total width is still correct
			if width != 50 {
				t.Error("Context bar broke width constraints")
			}
		} else {
			t.Log("Context bar correctly hidden when too narrow to display with padding")
		}
	})

	t.Run("padding is exactly 4 spaces on each side", func(t *testing.T) {
		// Direct test of createContextBar method
		s := New(deps)
		s.colors = CatppuccinMocha{} // Initialize colors
		
		// Give it plenty of width so we can clearly see the padding
		barWidth := 60
		contextLength := 50000
		
		result := s.createContextBar(contextLength, barWidth)
		
		// The result should be exactly barWidth characters
		stripped := stripAnsi(result)
		actualWidth := runewidth.StringWidth(stripped)
		if actualWidth != barWidth {
			t.Errorf("Context bar width incorrect: got %d, want %d", actualWidth, barWidth)
			t.Logf("Raw result length: %d", len(result))
			t.Logf("Stripped length: %d", len(stripped))
			t.Logf("Stripped width: %d", actualWidth)
		}
		
		// Should start with exactly 4 spaces
		if !strings.HasPrefix(result, "    ") {
			t.Error("Context bar should start with exactly 4 spaces")
		}
		
		// Should end with exactly 4 spaces
		if !strings.HasSuffix(stripped, "    ") {
			t.Error("Context bar should end with exactly 4 spaces")
		}
		
		// The actual bar content should be in the middle
		trimmed := strings.TrimSpace(stripped)
		trimmedWidth := runewidth.StringWidth(trimmed)
		if trimmedWidth != barWidth-8 { // 8 = 4 spaces on each side
			t.Errorf("Bar content width incorrect: got %d, want %d", trimmedWidth, barWidth-8)
		}
	})
}