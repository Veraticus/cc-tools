package statusline

import (
	"fmt"
	"time"
)

// Scenario is one labeled point in the statusline rendering matrix: a
// terminal width, a parsed Input, and the Dependencies needed to
// render it deterministically. Both the `cc-tools preview` subcommand
// and the golden tests iterate Scenarios() and render each one through
// the same production path (Generate), so this type is production
// code, not test-only fixtures.
type Scenario struct {
	Name  string
	Width int
	Input Input
	Deps  *Dependencies
}

// scenarioProjectDir is the fixed workspace directory used by every
// scenario. It's deliberately outside any real machine's home
// directory tree (formatPath consults the real process HOME env var,
// not an injected dependency) so rendered output can't accidentally
// vary with whatever HOME happens to be set on the machine running
// the tests or the preview binary. It has exactly two non-root path
// segments, so formatPath's "truncate after 3 parts" rule never
// engages either.
const scenarioProjectDir = "/workspace/demo-project"

// scenarioHome is the HOME value handed to scenarios' EnvReader (used
// for the gcloud config dir and the default kubeconfig path). It is
// unrelated to the real process HOME env var formatPath reads.
const scenarioHome = "/home/fixture"

// scenarioModelDisplay is the fixed model display name fed to every
// scenario's Input. abbreviateModel turns this into "S4.5".
const scenarioModelDisplay = "Sonnet 4.5"

// Scenarios returns the full statusline rendering matrix: every
// combination of terminal width, context-window percentage, git
// state, and env-chip state. Each scenario is fully self-contained and
// renders deterministically (IconIndex pins the model icon; no real
// filesystem, env, or subprocess access is used).
//
// NOTE: this matrix will be EXTENDED with rate-limit states {absent,
// normal, extra-usage} by a later epic task once rate_limits parsing
// exists. Keep additions here table-driven so that extension is a
// straightforward new axis rather than a rewrite.
func Scenarios() []Scenario {
	widths := []int{200, 120, 80, 50}
	contexts := []float64{0, 42, 85, 97}
	gitStates := []string{"none", "clean", "dirty"}
	envStates := []string{"none", "awsk8s"}

	var scenarios []Scenario
	for _, width := range widths {
		for _, ctx := range contexts {
			for _, gitState := range gitStates {
				for _, envState := range envStates {
					scenarios = append(scenarios, buildScenario(width, ctx, gitState, envState))
				}
			}
		}
	}
	return scenarios
}

// buildScenario constructs one Scenario for the given axis values.
func buildScenario(width int, ctxPercent float64, gitState, envState string) Scenario {
	name := fmt.Sprintf("w%d_ctx%d_git-%s_env-%s", width, int(ctxPercent), gitState, envState)

	input := Input{}
	input.Model.DisplayName = scenarioModelDisplay
	input.ContextWindow.UsedPercentage = ctxPercent
	input.Workspace.ProjectDir = scenarioProjectDir

	// Widest scenarios carry an effort level so the model chip's effort
	// suffix ("×H") is exercised in goldens; other widths stay
	// effort-absent rather than adding a full cross-product axis.
	const effortScenarioWidth = 200
	if width == effortScenarioWidth {
		input.Effort = &EffortInput{Level: "high"}
	}

	fr := newFixedFileReader()
	env := newFixedEnvReader(map[string]string{"HOME": scenarioHome})

	applyGitState(fr, gitState)
	applyEnvState(fr, env, envState)

	deps := &Dependencies{
		FileReader:    fr,
		CommandRunner: newFixedCommandRunner(),
		EnvReader:     env,
		TerminalWidth: fixedTerminalWidth(width),
		CacheDir:      "/tmp",
		CacheDuration: 0,
		// Pin the model icon so golden output doesn't flake across runs.
		IconIndex: func(int) int { return 0 },
	}

	return Scenario{Name: name, Width: width, Input: input, Deps: deps}
}

// applyGitState configures fr so getGitInfo/readGitInfo observe the
// requested git state for scenarioProjectDir:
//   - "none": no .git present anywhere on the walk up from the
//     project dir.
//   - "clean": .git exists with an old index mtime (no "!" status).
//   - "dirty": .git exists with an index mtime inside the 60s
//     recent-change window (readGitInfo's heuristic for uncommitted
//     changes), rendering the "!" status.
func applyGitState(fr *fixedFileReader, gitState string) {
	if gitState == "none" {
		return
	}

	gitDir := scenarioProjectDir + "/.git"
	fr.setExists(gitDir, true)
	fr.setFile(gitDir+"/HEAD", []byte("ref: refs/heads/main\n"))

	switch gitState {
	case "dirty":
		fr.setModTime(gitDir+"/index", time.Now())
	case "clean":
		const longAgo = 24 * time.Hour
		fr.setModTime(gitDir+"/index", time.Now().Add(-longAgo))
	}
}

// applyEnvState configures env and fr so the right-hand cloud chips
// render (or don't). "awsk8s" sets an AWS_PROFILE and a kubeconfig
// with a current-context, matching the cross-product's "aws+k8s" env
// chip case; "none" leaves both absent.
func applyEnvState(fr *fixedFileReader, env *fixedEnvReader, envState string) {
	if envState == "none" {
		return
	}

	env.vars["AWS_PROFILE"] = "dev-account"
	kubeconfig := scenarioHome + "/.kube/config"
	fr.setExists(kubeconfig, true)
	fr.setFile(kubeconfig, []byte("current-context: dev-cluster\n"))
}

// fixedFileReader is a small, deterministic FileReader fixture for
// scenarios: paths not explicitly configured simply don't exist. It
// is intentionally separate from the _test.go Mock types (which
// can't be imported into production code — the preview subcommand
// consumes Scenarios() at runtime).
type fixedFileReader struct {
	files  map[string][]byte
	times  map[string]time.Time
	exists map[string]bool
}

func newFixedFileReader() *fixedFileReader {
	return &fixedFileReader{
		files:  make(map[string][]byte),
		times:  make(map[string]time.Time),
		exists: make(map[string]bool),
	}
}

func (f *fixedFileReader) setFile(path string, content []byte) {
	f.files[path] = content
	f.exists[path] = true
}

func (f *fixedFileReader) setExists(path string, v bool) {
	f.exists[path] = v
}

func (f *fixedFileReader) setModTime(path string, t time.Time) {
	f.times[path] = t
}

func (f *fixedFileReader) ReadFile(path string) ([]byte, error) {
	if content, ok := f.files[path]; ok {
		return content, nil
	}
	return nil, fmt.Errorf("fixedFileReader: %s not configured", path)
}

func (f *fixedFileReader) Exists(path string) bool {
	return f.exists[path]
}

func (f *fixedFileReader) ModTime(path string) (time.Time, error) {
	if t, ok := f.times[path]; ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("fixedFileReader: no mtime for %s", path)
}

// fixedCommandRunner is a deterministic CommandRunner fixture:
// unconfigured commands return empty output with no error, matching
// DefaultCommandRunner's behavior when a tool (e.g. `hostname`) isn't
// available. No scenario currently needs a configured response, so
// there's no setter yet — add one if a future scenario needs it.
type fixedCommandRunner struct{}

func newFixedCommandRunner() *fixedCommandRunner {
	return &fixedCommandRunner{}
}

func (c *fixedCommandRunner) Run(string, ...string) ([]byte, error) {
	return []byte(""), nil
}

// fixedEnvReader is a deterministic EnvReader fixture backed by a
// plain map; unconfigured keys return "".
type fixedEnvReader struct {
	vars map[string]string
}

func newFixedEnvReader(vars map[string]string) *fixedEnvReader {
	if vars == nil {
		vars = make(map[string]string)
	}
	return &fixedEnvReader{vars: vars}
}

func (e *fixedEnvReader) Get(key string) string {
	return e.vars[key]
}

// fixedTerminalWidth is a deterministic TerminalWidth fixture that
// always reports the same width.
type fixedTerminalWidth int

func (w fixedTerminalWidth) GetWidth() int {
	return int(w)
}
