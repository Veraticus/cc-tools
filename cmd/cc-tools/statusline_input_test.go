package main

import (
	"strings"
	"testing"
)

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
