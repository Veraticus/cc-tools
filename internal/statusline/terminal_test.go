package statusline

import (
	"testing"
)

func TestDefaultTerminalWidth_GetWidth(t *testing.T) {
	tw := &DefaultTerminalWidth{}

	tests := []struct {
		name     string
		setupEnv func(*testing.T)
		want     int
	}{
		{
			name: "STEWARD_STATUSLINE_WIDTH overrides everything",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "120")
				t.Setenv("CLAUDE_STATUSLINE_WIDTH", "40")
				t.Setenv("COLUMNS", "80")
			},
			want: 120,
		},
		{
			name: "old override is ignored",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "")
				t.Setenv("CLAUDE_STATUSLINE_WIDTH", "120")
				t.Setenv("COLUMNS", "80")
			},
			want: 80,
		},
		{
			name: "COLUMNS used when test override absent",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "")
				t.Setenv("COLUMNS", "100")
			},
			want: 100,
		},
		{
			name: "COLUMNS=0 falls through to default",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "")
				t.Setenv("COLUMNS", "0")
			},
			want: 200,
		},
		{
			name: "garbage COLUMNS falls through to default",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "")
				t.Setenv("COLUMNS", "not-a-number")
			},
			want: 200,
		},
		{
			name: "no env vars yields default",
			setupEnv: func(t *testing.T) {
				t.Helper()
				t.Setenv("STEWARD_STATUSLINE_WIDTH", "")
				t.Setenv("COLUMNS", "")
			},
			want: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv(t)
			if got := tw.GetWidth(); got != tt.want {
				t.Errorf("GetWidth() = %d, want %d", got, tt.want)
			}
		})
	}
}
