package statusline

import (
	"testing"
)

func TestDefaultTerminalWidth_GetWidth(t *testing.T) {
	tw := &DefaultTerminalWidth{}

	// Test with various environment setups
	tests := []struct {
		name     string
		setupEnv func(*testing.T)
		minWidth int
		maxWidth int
	}{
		{
			name: "with COLUMNS env var",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("COLUMNS", "100")
			},
			minWidth: 80, // Could be 100 or fallback
			maxWidth: 150,
		},
		{
			name: "with CLAUDE_STATUSLINE_WIDTH env var",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("CLAUDE_STATUSLINE_WIDTH", "120")
			},
			minWidth: 120,
			maxWidth: 120,
		},
		{
			name: "no env vars",
			setupEnv: func(_ *testing.T) {
				// t.Setenv automatically handles cleanup
			},
			minWidth: 80,  // Default fallback
			maxWidth: 250, // Could get from terminal (increased to handle wider terminals)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv(t)

			width := tw.GetWidth()

			if width < tt.minWidth || width > tt.maxWidth {
				t.Errorf("Expected width between %d and %d, got %d",
					tt.minWidth, tt.maxWidth, width)
			}
		})
	}
}
