package statusline

import (
	"context"
	"fmt"
	"strings"
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

// scenarioStateNone is the shared "no state configured" sentinel used by
// both the gitState and envState scenario-matrix dimensions.
const scenarioStateNone = "none"

// Scenarios returns the full statusline rendering matrix: every
// combination of terminal width, context-window percentage, git
// state, and env-chip state, PLUS a rate-limit dimension (see
// buildRLScenario) layered on as a subset rather than a full
// cross-product. Each scenario is fully self-contained and renders
// deterministically (IconIndex pins the model icon, Now pins "now"
// for rate-limit pace/countdown math; no real filesystem, env, or
// subprocess access is used).
//
// The rate-limit dimension was extended into this matrix now that
// rate-limit/cost/alarm chips render in the middle cluster — this
// replaces the earlier placeholder note that it would land "later".
func Scenarios() []Scenario {
	widths := []int{200, 120, 80, 50}
	contexts := []float64{0, 42, 85, 97}
	gitStates := []string{scenarioStateNone, "clean", "dirty"}
	envStates := []string{scenarioStateNone, "awsk8s"}

	rlWidths := []int{200, 80, 50}
	rlStates := []string{"rl-normal", "rl-extra", "rl-cost"}

	scenarios := make(
		[]Scenario, 0,
		len(widths)*len(contexts)*len(gitStates)*len(envStates)+len(rlWidths)*len(rlStates)+1,
	)
	for _, width := range widths {
		for _, ctx := range contexts {
			for _, gitState := range gitStates {
				for _, envState := range envStates {
					scenarios = append(scenarios, buildScenario(width, ctx, gitState, envState))
				}
			}
		}
	}

	// Rate-limit dimension: a SUBSET of {width} x {rate-limit state},
	// held to context=42/git-none/env-none rather than crossed with
	// every git/env combination — those axes are already covered
	// above and are orthogonal to the middle-cluster chip logic.
	for _, width := range rlWidths {
		for _, rlState := range rlStates {
			scenarios = append(scenarios, buildRLScenario(width, rlState))
		}
	}

	// Squeeze scenario: just above the narrow threshold, the wide
	// middle region is too small for the context-element + alarm-chip
	// cluster, exercising the drop-context/keep-alarm path — the alarm
	// must survive width pressure that would blank a lesser chip.
	scenarios = append(scenarios, buildRLScenario(rlSqueezeScenarioWidth, "rl-extra"))

	return scenarios
}

// scenarioFixedNow is the reference "now" every rate-limit scenario's
// window ResetsAt values are computed relative to. Injected via
// Dependencies.Now so the pace-arrow and countdown math in the
// rate-limit/alarm chips never depends on wall-clock time — goldens
// stay stable forever.
func scenarioFixedNow() time.Time {
	const fixedUnixSeconds = 1751700000
	return time.Unix(fixedUnixSeconds, 0)
}

// Fixture values for buildRLScenario's rate-limit/cost scenarios.
// Named (rather than inlined) so golangci-lint's mnd check passes and
// so the "rl-extra" cost figure only appears once.
const (
	// rlSqueezeScenarioWidth is the terminal width for the alarm
	// squeeze scenario: wide-mode rendering (just above
	// narrowWidthThreshold) with a middle region too small for the
	// full context + alarm cluster.
	rlSqueezeScenarioWidth = 85

	rlScenarioCtx           = 42
	rlNormalFiveHourPct     = 23
	rlNormalSevenDayPct     = 41
	rlExtraFiveHourPct      = 100.0
	rlExtraCostUSD          = 4.12
	rlNormalFiveHourResetIn = 3*time.Hour + 47*time.Minute
	rlNormalSevenDayResetIn = 2 * 24 * time.Hour
	rlExtraFiveHourResetIn  = 1 * time.Hour
)

// buildRLScenario constructs one Scenario for the rate-limit
// dimension: fixed context=42, git-none, env-none, varying only width
// and rlState. rlState selects one of:
//   - "rl-normal": both windows present, comfortably under the alarm
//     threshold (5h=23% resets in 3h47m, 7d=41% resets in 2d).
//   - "rl-extra": five_hour at the alarm threshold (100%, resets in
//     1h) with a nonzero cost, so both the alarm decision and its
//     dollar amount are exercised.
//   - "rl-cost": no rate_limits at all, cost-only, exercising the
//     cost chip.
func buildRLScenario(width int, rlState string) Scenario {
	name := fmt.Sprintf("w%d_ctx%d_%s", width, rlScenarioCtx, rlState)

	input := Input{}
	input.Model.DisplayName = scenarioModelDisplay
	input.ContextWindow.UsedPercentage = rlScenarioCtx
	input.Workspace.ProjectDir = scenarioProjectDir

	now := scenarioFixedNow()
	switch rlState {
	case "rl-normal":
		input.RateLimits = &RateLimitsInput{
			FiveHour: &RateLimitWindow{
				UsedPercentage: rlNormalFiveHourPct,
				ResetsAt:       now.Add(rlNormalFiveHourResetIn).Unix(),
			},
			SevenDay: &RateLimitWindow{
				UsedPercentage: rlNormalSevenDayPct,
				ResetsAt:       now.Add(rlNormalSevenDayResetIn).Unix(),
			},
		}
	case "rl-extra":
		input.RateLimits = &RateLimitsInput{
			FiveHour: &RateLimitWindow{
				UsedPercentage: rlExtraFiveHourPct,
				ResetsAt:       now.Add(rlExtraFiveHourResetIn).Unix(),
			},
		}
		input.Cost = CostInput{TotalCostUSD: rlExtraCostUSD}
	case "rl-cost":
		input.Cost = CostInput{TotalCostUSD: rlExtraCostUSD}
	}

	fr := newFixedFileReader()
	env := newFixedEnvReader(map[string]string{"HOME": scenarioHome})

	deps := &Dependencies{
		FileReader:    fr,
		CommandRunner: newFixedCommandRunner(),
		EnvReader:     env,
		TerminalWidth: fixedTerminalWidth(width),
		CacheDir:      "", // caching disabled: fixtures must never touch the real filesystem
		CacheDuration: 0,
		IconIndex:     func(int) int { return 0 },
		Now:           scenarioFixedNow,
	}

	return Scenario{Name: name, Width: width, Input: input, Deps: deps}
}

// scenarioCleanPorcelain is the canned `git status --porcelain=v2
// --branch` output for the "clean" git-state dimension: on branch
// main, no ahead/behind, no changed entries.
const scenarioCleanPorcelain = "# branch.oid abcdef1234567890\n" +
	"# branch.head main\n" +
	"# branch.upstream origin/main\n" +
	"# branch.ab +0 -0\n"

// scenarioDirtyPorcelain is the canned porcelain output for the
// "dirty" git-state dimension: 3 changed/untracked entries, 2 commits
// ahead of upstream, 0 behind — matching the epic's "dirty(3
// entries)+ahead(2)" scenario spec.
const scenarioDirtyPorcelain = "# branch.oid abcdef1234567890\n" +
	"# branch.head main\n" +
	"# branch.upstream origin/main\n" +
	"# branch.ab +2 -0\n" +
	"1 .M N... 100644 100644 100644 aaaaaaa bbbbbbb file1.go\n" +
	"1 .M N... 100644 100644 100644 aaaaaaa bbbbbbb file2.go\n" +
	"? file3.go\n"

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
	cr := newFixedCommandRunner()

	applyGitState(fr, cr, gitState)
	applyEnvState(fr, env, envState)

	deps := &Dependencies{
		FileReader:    fr,
		CommandRunner: cr,
		EnvReader:     env,
		TerminalWidth: fixedTerminalWidth(width),
		CacheDir:      "", // caching disabled: fixtures must never touch the real filesystem
		CacheDuration: 0,
		// Pin the model icon so golden output doesn't flake across runs.
		IconIndex: func(int) int { return 0 },
	}

	return Scenario{Name: name, Width: width, Input: input, Deps: deps}
}

// applyGitState configures fr and cr so getGitInfo/readGitInfo and
// gitStatus observe the requested git state for scenarioProjectDir:
//   - "none": no .git present anywhere on the walk up from the
//     project dir, so gitStatus is never invoked.
//   - "clean": .git exists with a "main" HEAD; cr returns clean
//     porcelain output (no dirty entries, no ahead/behind) for the
//     `git -C scenarioProjectDir status --porcelain=v2 --branch`
//     command.
//   - "dirty": same HEAD; cr returns porcelain output with 3
//     changed/untracked entries and 2 commits ahead of upstream.
func applyGitState(fr *fixedFileReader, cr *fixedCommandRunner, gitState string) {
	if gitState == scenarioStateNone {
		return
	}

	gitDir := scenarioProjectDir + "/.git"
	fr.setExists(gitDir, true)
	fr.setFile(gitDir+"/HEAD", []byte("ref: refs/heads/main\n"))

	gitStatusArgs := []string{"-C", scenarioProjectDir, "status", "--porcelain=v2", "--branch"}
	switch gitState {
	case "dirty":
		cr.setResponse("git", gitStatusArgs, []byte(scenarioDirtyPorcelain))
	case "clean":
		cr.setResponse("git", gitStatusArgs, []byte(scenarioCleanPorcelain))
	}
}

// applyEnvState configures env and fr so the right-hand cloud chips
// render (or don't). "awsk8s" sets an AWS_PROFILE and a kubeconfig
// with a current-context, matching the cross-product's "aws+k8s" env
// chip case; "none" leaves both absent.
func applyEnvState(fr *fixedFileReader, env *fixedEnvReader, envState string) {
	if envState == scenarioStateNone {
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
	exists map[string]bool
}

func newFixedFileReader() *fixedFileReader {
	return &fixedFileReader{
		files:  make(map[string][]byte),
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

func (f *fixedFileReader) ReadFile(path string) ([]byte, error) {
	if content, ok := f.files[path]; ok {
		return content, nil
	}
	return nil, fmt.Errorf("fixedFileReader: %s not configured", path)
}

func (f *fixedFileReader) Exists(path string) bool {
	return f.exists[path]
}

// fixedCommandRunner is a deterministic CommandRunner fixture:
// unconfigured commands return empty output with no error, matching
// DefaultCommandRunner's behavior when a tool (e.g. `hostname`) isn't
// available. setResponse configures canned output for one specific
// command+args combination (used for the git-status porcelain
// fixtures in applyGitState).
type fixedCommandRunner struct {
	responses map[string][]byte
}

func newFixedCommandRunner() *fixedCommandRunner {
	return &fixedCommandRunner{responses: make(map[string][]byte)}
}

// setResponse configures the canned output returned for command+args.
func (c *fixedCommandRunner) setResponse(command string, args []string, output []byte) {
	c.responses[fixedCommandRunnerKey(command, args)] = output
}

func (c *fixedCommandRunner) Run(command string, args ...string) ([]byte, error) {
	if response, ok := c.responses[fixedCommandRunnerKey(command, args)]; ok {
		return response, nil
	}
	return []byte(""), nil
}

// RunContext ignores ctx: scenarios are deterministic fixtures with no
// real subprocess or timeout behavior to honor.
func (c *fixedCommandRunner) RunContext(_ context.Context, command string, args ...string) ([]byte, error) {
	return c.Run(command, args...)
}

// fixedCommandRunnerKey builds the same "command arg1 arg2..." lookup
// key style used by MockCommandRunner in statusline_test.go.
func fixedCommandRunnerKey(command string, args []string) string {
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
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
