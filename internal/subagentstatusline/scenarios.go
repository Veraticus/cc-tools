package subagentstatusline

import (
	"fmt"
	"strings"
)

// Scenario is one labeled point in the subagent statusline rendering
// matrix: a terminal column budget, a chain of Tasks, the context
// window, and the env snapshot needed to render it deterministically.
// Both the `cc-tools preview` subcommand and the golden tests iterate
// Scenarios() and render each one through BuildContent — the same
// per-task rendering path the production Render() entry point calls —
// so this type is production code, not test-only fixtures.
type Scenario struct {
	Name          string
	Columns       int
	Tasks         []Task
	ContextWindow int
	Env           EnvSnapshot
}

// scenarioCWD is the fixed working directory used by every scenario
// task. It doesn't exist on disk, so renderBranchChip's real
// os.Lstat/os.ReadFile calls always fail closed and no branch chip
// appears — deterministic across every machine this runs on. (This
// package has no injectable filesystem dependency for the branch
// chip, unlike internal/statusline.)
const scenarioCWD = "/workspace/demo-project"

// Scenarios returns the full subagent statusline rendering matrix:
// every combination of task status, chain size (1 or 3 tasks), and
// terminal column budget. Each scenario is fully self-contained and
// renders deterministically — no real filesystem, env, or subprocess
// access is exercised (BuildContent has none of the nondeterminism
// internal/statusline's selectModelIcon has, verified by inspection:
// no math/rand usage anywhere in this package's production code).
func Scenarios() []Scenario {
	statuses := []string{statusRunning, statusCompleted, statusFailed, statusPending}
	chainSizes := []int{1, 3}
	columns := []int{200, 80}

	scenarios := make([]Scenario, 0, len(columns)*len(chainSizes)*len(statuses))
	for _, columnCount := range columns {
		for _, chainSize := range chainSizes {
			for _, status := range statuses {
				scenarios = append(scenarios, buildScenario(columnCount, chainSize, status))
			}
		}
	}
	return scenarios
}

// scenarioTokensPerTask is the TokenCount step between successive
// tasks in a chain, giving each row a distinct (but still low, so the
// context chip stays green) context-bar percentage.
const scenarioTokensPerTask = 10000

// scenarioModels cycles across a chain's tasks so multi-task scenarios
// exercise the model chip's three id shapes: a claude id that
// abbreviates, a provider-prefixed alias, and a claude id with a date
// stamp. Effort accompanies the first model only, covering both the
// suffixed and suffix-free renderings.
var scenarioModels = []struct { //nolint:gochecknoglobals // immutable fixture table
	model  string
	effort EffortLevel
}{
	{"claude-fable-5[1m]", "high"},
	{"chatgpt/sol", ""},
	{"claude-haiku-4-5-20251001", ""},
}

// buildScenario constructs one Scenario: a chain of chainSize tasks,
// all sharing status, rendered at the given column budget.
func buildScenario(columns, chainSize int, status string) Scenario {
	name := fmt.Sprintf("c%d_chain%d_%s", columns, chainSize, status)

	tasks := make([]Task, chainSize)
	for i := range chainSize {
		agentName := fmt.Sprintf("Agent %d", i+1)
		m := scenarioModels[i%len(scenarioModels)]
		tasks[i] = Task{
			ID:          fmt.Sprintf("task-%d", i+1),
			Name:        &agentName,
			Type:        "local_agent",
			Status:      status,
			Description: fmt.Sprintf("Doing work item %d", i+1),
			TokenCount:  (i + 1) * scenarioTokensPerTask,
			Model:       m.model,
			Effort:      m.effort,
			CWD:         scenarioCWD,
		}
	}

	return Scenario{
		Name:          name,
		Columns:       columns,
		Tasks:         tasks,
		ContextWindow: DefaultContextWindow,
	}
}

// RenderScenario renders every task in the scenario via BuildContent
// (the package's per-task rendering entry point — the same one
// Render's production JSON-lines path calls) and joins the resulting
// chip chains with a newline, one line per chain/task row.
func RenderScenario(sc Scenario) string {
	lines := make([]string, 0, len(sc.Tasks))
	for _, task := range sc.Tasks {
		lines = append(lines, BuildContent(task, sc.Columns, sc.ContextWindow, sc.Env))
	}
	return strings.Join(lines, "\n")
}
