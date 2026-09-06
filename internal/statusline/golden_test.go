package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden reports whether golden files should be rewritten with the
// current rendered output instead of compared against it. Run:
// UPDATE_GOLDEN=1 go test ./...
func updateGolden() bool {
	return os.Getenv("UPDATE_GOLDEN") == "1"
}

// goldenDir holds one .golden file per Scenario, named after the
// scenario itself.
const goldenDir = "testdata"

// renderScenario exercises the exact production path: marshal the
// scenario's parsed Input back to JSON, then feed it through
// Statusline.Generate — the same JSON-in/ANSI-out path the steward
// statusline binary uses on every tick.
func renderScenario(t *testing.T, sc Scenario) string {
	t.Helper()
	data, err := json.Marshal(sc.Input)
	if err != nil {
		t.Fatalf("scenario %s: marshal input: %v", sc.Name, err)
	}
	sl := CreateStatusline(sc.Deps)
	out, err := sl.Generate(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("scenario %s: Generate: %v", sc.Name, err)
	}
	return out
}

func TestGolden(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			got := renderScenario(t, sc)
			goldenPath := filepath.Join(goldenDir, sc.Name+".golden")

			if updateGolden() {
				if err := os.MkdirAll(goldenDir, 0o755); err != nil {
					t.Fatalf("scenario %s: mkdir testdata: %v", sc.Name, err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
					t.Fatalf("scenario %s: write golden: %v", sc.Name, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("scenario %s: missing golden file %s: run: UPDATE_GOLDEN=1 go test ./...", sc.Name, goldenPath)
			}

			if got != string(want) {
				t.Errorf("scenario %s: output mismatch\ngot:  %q\nwant: %q", sc.Name, got, string(want))
			}
		})
	}
}
