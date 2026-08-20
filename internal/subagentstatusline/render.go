package subagentstatusline

import (
	"fmt"
	"io"
	"time"

	"github.com/Veraticus/cc-tools/internal/statusline"
)

// Render is the package's top-level orchestrator. Reads the
// subagentStatusLine JSON blob from r, calls BuildContent per task,
// and writes one newline-delimited Decoration per task to w.
//
// contextWindow defaults to defaultContextWindow (1_000_000, Opus 1M)
// when <= 0. env supplies AWS_PROFILE / gcloud / k8s. cacheDir keys
// the agents-state handoff to the main statusline ("" disables it).
//
// Returns wrapped errors for input parse failures or output write
// failures. Errors are intended to surface to stderr at the cmd/
// layer; this function never writes to stderr itself.
func Render(r io.Reader, w io.Writer, contextWindow int, env EnvReader, cacheDir string) error {
	in, err := Parse(r)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	// Hand the running agents' model labels to the main statusline
	// via the shared state file. Best-effort by design: this hook's
	// job is the row decorations, and a read-only cache dir must not
	// take them down with it.
	if cacheDir != "" {
		_ = statusline.WriteAgentsState(cacheDir, in.SessionID, runningModelLabels(in.Tasks), time.Now())
	}
	// Snapshot env ONCE, not per-task. DefaultEnvReader.Get for
	// AWS_PROFILE re-opens the state file on every call; with N tasks
	// that's N file reads per tick. The values can't differ across
	// tasks in a single invocation, so a snapshot is correct and cheap.
	snap := SnapshotEnv(env)

	decorations := make([]Decoration, 0, len(in.Tasks))
	for _, task := range in.Tasks {
		decorations = append(decorations, Decoration{
			ID:      task.ID,
			Content: BuildContent(task, in.Columns, contextWindow, snap),
		})
	}
	if writeErr := WriteDecorations(w, decorations); writeErr != nil {
		return fmt.Errorf("render: %w", writeErr)
	}
	return nil
}
