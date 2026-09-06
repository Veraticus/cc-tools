package main

import (
	"strings"
	"testing"
	"time"
)

func TestGetCacheDuration_UsesOnlyStewardEnvironment(t *testing.T) {
	t.Setenv("DEBUG_CONTEXT", "")
	t.Setenv("CLAUDE_STATUSLINE_CACHE_SECONDS", "1")
	t.Setenv("STEWARD_STATUSLINE_CACHE_SECONDS", "")
	if got := getCacheDuration(); got != 20*time.Second {
		t.Errorf("old cache duration env = %v, want default", got)
	}

	t.Setenv("STEWARD_STATUSLINE_CACHE_SECONDS", "7")
	if got := getCacheDuration(); got != 7*time.Second {
		t.Errorf("canonical cache duration env = %v, want 7s", got)
	}
}

func TestReadStatuslineInput(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stdin   string
		want    string
		wantErr bool
	}{
		{name: "stdin", stdin: `{"cwd":"/stdin"}`, want: `{"cwd":"/stdin"}`},
		{name: "argument", args: []string{`{"cwd":"/argument"}`}, stdin: "ignored", want: `{"cwd":"/argument"}`},
		{name: "too many arguments", args: []string{"{}", "extra"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readStatuslineInput(tt.args, strings.NewReader(tt.stdin))
			if (err != nil) != tt.wantErr {
				t.Fatalf("readStatuslineInput() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && string(got) != tt.want {
				t.Fatalf("readStatuslineInput() = %q, want %q", got, tt.want)
			}
		})
	}
}
