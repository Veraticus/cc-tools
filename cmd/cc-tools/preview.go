package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Veraticus/cc-tools/internal/statusline"
	"github.com/Veraticus/cc-tools/internal/subagentstatusline"
)

// runPreviewCommand renders every scenario in the shared statusline
// and subagent-statusline scenario matrices, labeled and grouped by
// width, so a developer can eyeball the full rendering surface in one
// shot instead of constructing JSON payloads by hand.
func runPreviewCommand() {
	renderPreview(os.Stdout)
}

// renderPreview writes every statusline.Scenario and
// subagentstatusline.Scenario, grouped by width/columns, as a label
// line followed by its rendered ANSI output. Each package's scenarios
// are rendered through the same path production uses: statusline via
// json.Marshal(Input) + Statusline.Generate, subagentstatusline via
// RenderScenario (which calls BuildContent per task).
func renderPreview(w io.Writer) {
	renderStatuslinePreview(w)
	renderSubagentPreview(w)
}

func renderStatuslinePreview(w io.Writer) {
	_, _ = fmt.Fprintln(w, "== statusline ==")

	byWidth := make(map[int][]statusline.Scenario)
	var widths []int
	for _, sc := range statusline.Scenarios() {
		if _, ok := byWidth[sc.Width]; !ok {
			widths = append(widths, sc.Width)
		}
		byWidth[sc.Width] = append(byWidth[sc.Width], sc)
	}

	for _, width := range widths {
		_, _ = fmt.Fprintf(w, "-- width %d --\n", width)
		for _, sc := range byWidth[width] {
			_, _ = fmt.Fprintf(w, "%s\n", sc.Name)
			data, err := json.Marshal(sc.Input)
			if err != nil {
				_, _ = fmt.Fprintf(w, "  (marshal error: %v)\n", err)
				continue
			}
			sl := statusline.CreateStatusline(sc.Deps)
			out, err := sl.Generate(bytes.NewReader(data))
			if err != nil {
				_, _ = fmt.Fprintf(w, "  (render error: %v)\n", err)
				continue
			}
			_, _ = fmt.Fprintln(w, out)
		}
	}
}

func renderSubagentPreview(w io.Writer) {
	_, _ = fmt.Fprintln(w, "== subagent-statusline ==")

	byColumns := make(map[int][]subagentstatusline.Scenario)
	var widths []int
	for _, sc := range subagentstatusline.Scenarios() {
		if _, ok := byColumns[sc.Columns]; !ok {
			widths = append(widths, sc.Columns)
		}
		byColumns[sc.Columns] = append(byColumns[sc.Columns], sc)
	}

	for _, columns := range widths {
		_, _ = fmt.Fprintf(w, "-- width %d --\n", columns)
		for _, sc := range byColumns[columns] {
			_, _ = fmt.Fprintf(w, "%s\n", sc.Name)
			_, _ = fmt.Fprintln(w, subagentstatusline.RenderScenario(sc))
		}
	}
}
