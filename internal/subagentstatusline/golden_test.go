package subagentstatusline

import (
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

func TestGolden(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			got := RenderScenario(sc)
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
