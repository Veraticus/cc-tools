package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Veraticus/cc-tools/internal/notify"
)

// notifyJudgeModel is the model pinned for the notify judge — cheap and
// fast, since it only ever composes short text or makes a binary
// notify/silence call.
const notifyJudgeModel = "claude-haiku-4-5"

// notifyJudgeTimeout bounds the judge subprocess call. 60s: successful
// decide-mode calls have been logged at up to 27s, and a 30s bound was
// producing timeout → fail-open sends; the hook's own settings.json
// timeout is 90s, so 60s still leaves room for scanning and delivery.
const notifyJudgeTimeout = 60 * time.Second

// notifyHookTimeout bounds the whole hook invocation, comfortably above
// notifyJudgeTimeout to leave room for transcript scanning/enrichment and
// under the 90s hook timeout configured in settings.json.
const notifyHookTimeout = 80 * time.Second

// decisionLogName is the decision log's filename, a sibling of the
// per-session state directories inside the notify state base.
const decisionLogName = "notify-decisions.jsonl"

func runNotifyCommand() {
	flags := flag.NewFlagSet("notify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "print what would be sent instead of sending it")
	recheck := flags.Bool("recheck", false, "run as a detached watchdog recheck instead of processing a hook payload")
	session := flags.String("session", "", "session ID (required with --recheck)")
	stateBase := flags.String("state-base", defaultNotifyStateBase(), "root directory for per-session notify state")
	project := flags.String("project", "", "project name (used with --recheck)")
	host := flags.String("host", "", "host name (used with --recheck)")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(exitUsageError)
	}

	log := notify.DecisionLog{Path: filepath.Join(*stateBase, decisionLogName)}
	judgeModel := notify.ResolveJudgeModel(os.Environ(), notifyJudgeModel)
	judge := notify.Judge{Bin: "claude", Model: judgeModel, Timeout: notifyJudgeTimeout}
	sender, senderOK := notify.ResolveSenderEnv(os.Environ())
	sender.Host = notify.ShortHostname()

	if *recheck {
		runNotifyRecheck(*session, *stateBase, *project, *host, judge, sender, log)
		return
	}

	if !senderOK && !*dryRun {
		_, _ = fmt.Fprintln(os.Stderr, "cc-tools notify: no ntfy URL configured, skipping")
		os.Exit(0)
	}

	in, err := notify.ParseHookInput(os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cc-tools notify: %v\n", err)
		return
	}

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cc-tools"
	}

	p := notify.Pipeline{
		StateBase: *stateBase,
		DryRun:    *dryRun,
		Judge:     judge,
		Sender:    sender,
		Log:       log,
		Environ:   os.Environ(),
		Stdout:    os.Stdout,
		SelfBin:   selfBin,
		Workspace: notify.WorkspaceName(os.Environ(), notify.RunCommand),
		Present: func(environ []string, now time.Time) bool {
			return notify.UserPresent(environ, now, notify.RunCommand)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyHookTimeout)
	defer cancel()

	if runErr := p.Run(ctx, in); runErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cc-tools notify: %v\n", runErr)
	}
}

// runNotifyRecheck runs the detached watchdog loop for an already-armed
// session. It requires --session; every other flag has a usable default or
// is optional decoration for the decision log's DigestMeta.
func runNotifyRecheck(
	session, stateBase, project, host string, judge notify.Judge, sender notify.Sender, log notify.DecisionLog,
) {
	if session == "" {
		_, _ = fmt.Fprintln(os.Stderr, "cc-tools notify --recheck: --session is required")
		os.Exit(1)
	}

	state := notify.SessionState{Dir: filepath.Join(stateBase, session)}
	meta := notify.DigestMeta{Project: project, Host: host, Event: "recheck"}
	deps := notify.DefaultWatchdogDeps(judge, sender, log)

	notify.RunWatchdog(context.Background(), state, meta, deps, session)
}

// defaultNotifyStateBase resolves the default notify state directory:
// ${XDG_STATE_HOME:-~/.local/state}/cc-tools/notify.
func defaultNotifyStateBase() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(os.TempDir(), "cc-tools", "notify")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "cc-tools", "notify")
}
