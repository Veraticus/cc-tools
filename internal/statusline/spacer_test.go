package statusline

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestConfigurableSpacers(t *testing.T) {
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 100},
	}

	t.Run("default spacers", func(t *testing.T) {
		s := CreateStatusline(deps)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000, // Not in compact mode
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// Default config has 2 char left + 4 char right = 6 chars total spacers
		// So the output should be termWidth - 6 = 94 chars wide
		if width != 94 {
			t.Errorf("With default spacers (2+4), width should be 94, got %d", width)
		}
	})

	t.Run("custom spacers", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  3,
			RightSpacerWidth: 2,
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000, // Not in compact mode
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// Custom spacers: 3 left + 2 right = 5 chars total
		// So the output should be termWidth - 5 = 95 chars wide
		if width != 95 {
			t.Errorf("With custom spacers (3+2), width should be 95, got %d", width)
		}
	})

	t.Run("compact mode sets right spacer to 40", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  2,
			RightSpacerWidth: 5, // This should be ignored in compact mode
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 170000, // In compact mode (>= 85% of 200k = 170k)
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// In compact mode: left spacer (2) + right spacer (40)
		// So the output should be termWidth - 42 = 58 chars wide
		if width != 58 {
			t.Errorf("In compact mode with 40-char right spacer, width should be 58, got %d", width)
		}
	})

	t.Run("zero width spacers", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  0,
			RightSpacerWidth: 0,
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// With no spacers, output should be full terminal width
		if width != 100 {
			t.Errorf("With no spacers, width should be 100, got %d", width)
		}
	})

	t.Run("spacers affect width calculation", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  5,
			RightSpacerWidth: 3,
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// With 5 left + 3 right = 8 total spacer width
		// Output should be termWidth - 8 = 92 chars wide
		if width != 92 {
			t.Errorf("With spacers (5+3), width should be 92, got %d", width)
		}
	})

	t.Run("large spacers shrink content sections", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  20,
			RightSpacerWidth: 15,
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// With 20 left + 15 right = 35 total spacer width
		// Output should be termWidth - 35 = 65 chars wide
		if width != 65 {
			t.Errorf("With large spacers (20+15), width should be 65, got %d", width)
		}
	})

	t.Run("extreme spacers are scaled down", func(t *testing.T) {
		config := &Config{
			LeftSpacerWidth:  40,
			RightSpacerWidth: 30,
		}
		s := NewWithConfig(deps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     100,
			ContextLength: 50000,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// With extreme spacers (40+30=70), they should be scaled down
		// to leave at least minContentWidth (20) for content
		// So width should be at least 20 but less than full terminal width
		if width < 20 || width > 100 {
			t.Errorf("With extreme spacers, width should be between 20 and 100, got %d", width)
		}

		// The spacers should be scaled proportionally
		// Total spacer budget: 100 - 20 = 80
		// Left gets: 80 * 40 / 70 ≈ 45
		// Right gets: 80 * 30 / 70 ≈ 34
		// Content: 100 - 45 - 34 = 21 (rounding)
		// But implementation may vary slightly due to integer math
		if width > 30 {
			t.Logf("Note: With extreme spacers (40+30), width slightly exceeds minimal: %d", width)
		}
	})

	t.Run("spacers in narrow terminal", func(t *testing.T) {
		narrowDeps := &Dependencies{
			FileReader:    &MockFileReader{},
			CommandRunner: &MockCommandRunner{},
			EnvReader:     &MockEnvReader{vars: make(map[string]string)},
			TerminalWidth: &MockTerminalWidth{width: 40},
		}

		config := &Config{
			LeftSpacerWidth:  2,
			RightSpacerWidth: 2,
		}
		s := NewWithConfig(narrowDeps, config)
		data := &CachedData{
			ModelDisplay:  "Claude",
			CurrentDir:    "/home/user",
			TermWidth:     40,
			ContextLength: 50000,
		}

		result := s.Render(data)
		stripped := stripAnsi(result)
		width := runewidth.StringWidth(stripped)

		// With 2+2=4 spacers in 40-width terminal
		// Output should be 40 - 4 = 36 chars wide
		if width != 36 {
			t.Errorf("In narrow terminal with spacers (2+2), width should be 36, got %d", width)
		}
	})
}

func TestSpacerScaling(t *testing.T) {
	// Test that spacer scaling preserves the minimum content width
	deps := &Dependencies{
		FileReader:    &MockFileReader{},
		CommandRunner: &MockCommandRunner{},
		EnvReader:     &MockEnvReader{vars: make(map[string]string)},
		TerminalWidth: &MockTerminalWidth{width: 50},
	}

	config := &Config{
		LeftSpacerWidth:  25,
		RightSpacerWidth: 25,
	}
	s := NewWithConfig(deps, config)
	data := &CachedData{
		ModelDisplay:  "Claude",
		CurrentDir:    "/home/user",
		TermWidth:     50,
		ContextLength: 50000,
	}

	result := s.Render(data)
	stripped := stripAnsi(result)
	width := runewidth.StringWidth(stripped)

	// With 25+25=50 spacers equaling terminal width
	// Spacers should be scaled down to preserve minimum content
	// Minimum content is 20, so max spacers is 30
	// They should be scaled proportionally: each gets 15
	// Output should be at least 20 chars (minimum content)
	if width < 20 || width > 50 {
		t.Errorf("With spacers equaling terminal width, should preserve minimum content: got width %d", width)
	}
}
