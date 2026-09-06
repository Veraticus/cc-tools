package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joshsymonds/steward/internal/statusline"
	"github.com/joshsymonds/steward/internal/subagentstatusline"
)

// TestRenderPreview_ContainsEveryScenarioName asserts renderPreview's
// output mentions every scenario from both packages' Scenarios() as a
// label line, so a scenario silently missing from the preview output
// (e.g. a typo in the grouping/iteration logic) fails this test.
func TestRenderPreview_ContainsEveryScenarioName(t *testing.T) {
	var buf bytes.Buffer
	renderPreview(&buf)
	out := buf.String()

	for _, sc := range statusline.Scenarios() {
		if !strings.Contains(out, sc.Name) {
			t.Errorf("renderPreview output missing statusline scenario name %q", sc.Name)
		}
	}
	for _, sc := range subagentstatusline.Scenarios() {
		if !strings.Contains(out, sc.Name) {
			t.Errorf("renderPreview output missing subagentstatusline scenario name %q", sc.Name)
		}
	}
}
