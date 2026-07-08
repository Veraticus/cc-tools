package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
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

// clientDialTimeout bounds how long the hook client waits to reach notifyd
// before giving up and running its own inline fallback: short enough that
// an unreachable or overloaded daemon never meaningfully delays the hook
// (which must return well inside its own settings.json timeout), long
// enough to survive a momentary scheduling delay on a live daemon.
const clientDialTimeout = 250 * time.Millisecond

// disabledJudgeBin is Judge.Bin for the client's inline fallback Pipeline: a
// path that can never resolve to a real executable, so Judge.Evaluate
// always fails immediately (ENOENT, no subprocess actually runs) rather
// than invoking a real judge. This is what routes every OutcomeJudge case
// in the fallback through Pipeline's existing jerr != nil paths — the judge
// call itself is disabled, not skipped, so those fallback bodies still
// engage exactly as they do for a live judge that errored.
const disabledJudgeBin = "/nonexistent/cc-tools-notify-judge-disabled"

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

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cc-tools"
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyHookTimeout)
	defer cancel()

	dispatchNotify(ctx, notifyClientConfig{
		StateBase:   *stateBase,
		DryRun:      *dryRun,
		Sender:      sender,
		Log:         log,
		Environ:     os.Environ(),
		SelfBin:     selfBin,
		SockPath:    notify.SocketPath(),
		DialTimeout: clientDialTimeout,
	}, os.Stdin, os.Stdout, os.Stderr)
}

// notifyClientConfig groups the dependencies dispatchNotify needs, resolved
// by runNotifyCommand from flags/env — kept as an explicit struct (rather
// than reading os.Args/os.Environ directly inside dispatchNotify) so tests
// can drive the socket-vs-fallback dispatch logic without touching process
// globals.
type notifyClientConfig struct {
	StateBase   string
	DryRun      bool
	Sender      notify.Sender
	Log         notify.DecisionLog
	Environ     []string
	SelfBin     string
	SockPath    string
	DialTimeout time.Duration
}

// dispatchNotify implements the hook client's primary (non-recheck) path:
// parse the hook payload, try handing it to notifyd over the control
// socket (fire-and-forget — see sendFrame), and fall back to running the
// Pipeline inline, with the judge disabled, when the daemon is
// unreachable. It always returns, never blocking past cfg.DialTimeout plus
// whatever the fallback Pipeline itself takes — the hook's own exit-0
// contract is the caller's responsibility (runNotifyCommand never exits
// nonzero from this path).
func dispatchNotify(ctx context.Context, cfg notifyClientConfig, stdin io.Reader, stdout, stderr io.Writer) {
	in, err := notify.ParseHookInput(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cc-tools notify: %v\n", err)
		return
	}

	workspace := notify.WorkspaceName(cfg.Environ, notify.RunCommand)
	frame := notify.Frame{HookInput: in, Workspace: workspace, Environ: cfg.Environ}
	if sendFrame(ctx, cfg.SockPath, frame, cfg.DialTimeout) {
		return
	}

	p := notify.Pipeline{
		StateBase: cfg.StateBase,
		DryRun:    cfg.DryRun,
		Judge:     notify.Judge{Bin: disabledJudgeBin},
		Sender:    cfg.Sender,
		Log:       cfg.Log,
		Environ:   cfg.Environ,
		Stdout:    stdout,
		SelfBin:   cfg.SelfBin,
		Workspace: workspace,
		Present: func(environ []string, now time.Time) bool {
			return notify.UserPresent(environ, now, notify.RunCommand)
		},
	}
	if runErr := p.Run(ctx, in); runErr != nil {
		_, _ = fmt.Fprintf(stderr, "cc-tools notify: %v\n", runErr)
	}
}

// sendFrame dials notifyd's control socket at sockPath and writes frame,
// fire-and-forget: it returns true the moment the frame is fully written —
// the daemon owns everything from there — and false on any dial or write
// failure, so the caller runs its own inline fallback instead. timeout
// bounds only the dial; once connected, the write of one small JSON frame
// is not separately bounded.
func sendFrame(ctx context.Context, sockPath string, frame notify.Frame, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	return notify.EncodeFrame(conn, frame) == nil
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

// notifydRequiresSender reports whether notifyd must refuse to start
// because no ntfy delivery is configured and it isn't a dry run. This
// differs from the hook client's identical-looking gate: a single hook
// invocation can safely skip (exit 0, try again next event), but a
// long-running daemon with no delivery config would silently accept every
// frame and fail every send from then on — coverage loss the user has no
// way to notice. notifyd fails fast at startup instead, with a nonzero
// exit a service supervisor can see and report.
func notifydRequiresSender(senderOK, dryRun bool) bool {
	return !senderOK && !dryRun
}

// runNotifydCommand runs the notifyd daemon: it constructs the real
// Pipeline dependencies once (unlike the hook client, which resolves them
// fresh on every invocation) and serves the control socket until SIGTERM or
// SIGINT, then removes the socket file before exiting.
func runNotifydCommand() {
	flags := flag.NewFlagSet("notifyd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "print what would be sent instead of sending it")
	stateBase := flags.String("state-base", defaultNotifyStateBase(), "root directory for per-session notify state")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(exitUsageError)
	}

	log := notify.DecisionLog{Path: filepath.Join(*stateBase, decisionLogName)}
	judgeModel := notify.ResolveJudgeModel(os.Environ(), notifyJudgeModel)
	judge := notify.Judge{Bin: "claude", Model: judgeModel, Timeout: notifyJudgeTimeout}
	sender, senderOK := notify.ResolveSenderEnv(os.Environ())
	sender.Host = notify.ShortHostname()

	if notifydRequiresSender(senderOK, *dryRun) {
		_, _ = fmt.Fprintln(os.Stderr, "cc-tools notifyd: no ntfy URL configured")
		os.Exit(1)
	}

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cc-tools"
	}

	d := notify.Daemon{
		Pipeline: notify.Pipeline{
			StateBase: *stateBase,
			DryRun:    *dryRun,
			Judge:     judge,
			Sender:    sender,
			Log:       log,
			SelfBin:   selfBin,
			Present: func(environ []string, now time.Time) bool {
				return notify.UserPresent(environ, now, notify.RunCommand)
			},
		},
	}

	sockPath := notify.SocketPath()
	ln, listenErr := notify.Listen(sockPath)
	if listenErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cc-tools notifyd: %v\n", listenErr)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if serveErr := d.Serve(ctx, ln); serveErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cc-tools notifyd: %v\n", serveErr)
	}
	_ = os.Remove(sockPath)
}
