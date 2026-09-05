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
	"strings"
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

// notifyCodexJudgeModel is the default compose-only Codex model used by
// notifyd for Codex TurnComplete events.
const notifyCodexJudgeModel = "gpt-5.6-luna"

// notifyCodexJudgeTimeout bounds the ephemeral Codex compose subprocess.
const notifyCodexJudgeTimeout = 10 * time.Second

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
	if exitCode := runNotifyCommandWithIO(os.Args[2:], os.Stdin, os.Stdout, os.Stderr); exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runNotifyCommandWithIO parses notify's command-line flags and runs the hook
// client against explicit streams, returning the process exit code.
func runNotifyCommandWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("notify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print what would be sent instead of sending it")
	harness := flags.String("harness", "", "explicit hook harness (claude-code, codex, or pi)")
	stateBase := flags.String("state-base", defaultNotifyStateBase(), "root directory for per-session notify state")
	if err := flags.Parse(args); err != nil {
		return exitUsageError
	}

	log := notify.DecisionLog{Path: filepath.Join(*stateBase, decisionLogName)}
	sender, senderOK := notify.ResolveSenderEnv(os.Environ())
	sender.Host = notify.ShortHostname()

	if !senderOK && !*dryRun {
		_, _ = fmt.Fprintln(stderr, "cc-tools notify: no ntfy URL configured, skipping")
		return 0
	}

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cc-tools"
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyHookTimeout)
	defer cancel()
	payload := stdin
	if flags.NArg() == 1 {
		// Codex passes its notification JSON as one argv value. Claude writes
		// hook JSON to stdin. Normalize both before they reach dispatchNotify.
		payload = strings.NewReader(flags.Arg(0))
	} else if flags.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, "cc-tools notify: expected at most one JSON payload argument")
		return 0
	}

	dispatchNotify(ctx, notifyClientConfig{
		DryRun:      *dryRun,
		Harness:     *harness,
		Sender:      sender,
		Log:         log,
		Environ:     os.Environ(),
		SelfBin:     selfBin,
		SockPath:    notify.SocketPath(),
		DialTimeout: clientDialTimeout,
	}, payload, stdout, stderr)
	return 0
}

// notifyClientConfig groups the dependencies dispatchNotify needs, resolved
// by runNotifyCommand from flags/env — kept as an explicit struct (rather
// than reading os.Args/os.Environ directly inside dispatchNotify) so tests
// can drive the socket-vs-fallback dispatch logic without touching process
// globals.
type notifyClientConfig struct {
	DryRun      bool
	Harness     string
	Sender      notify.Sender
	Log         notify.DecisionLog
	Environ     []string
	SelfBin     string
	SockPath    string
	DialTimeout time.Duration
}

// dispatchNotify implements the hook client's primary path: parse the hook
// payload, try handing it to notifyd over the control socket (fire-and-forget
// — see sendFrame), and fall back to running the Pipeline inline, with the
// judge disabled and no watchdog, when the daemon is unreachable. It always
// returns, never blocking past cfg.DialTimeout plus whatever the fallback
// Pipeline itself takes — the hook's own exit-0 contract is the caller's
// responsibility (runNotifyCommand never exits nonzero from this path).
func dispatchNotify(ctx context.Context, cfg notifyClientConfig, stdin io.Reader, stdout, stderr io.Writer) {
	in, err := notify.ParseHookInputForHarness(stdin, cfg.Harness)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cc-tools notify: %v\n", err)
		return
	}

	workspace := notify.WorkspaceName(cfg.Environ, notify.RunCommand)
	frame := notify.Frame{
		HookInput: in, Workspace: workspace, Environ: cfg.Environ,
		ParentPID: os.Getppid(), DryRun: cfg.DryRun,
	}
	// A dry run must print its rehearsal to this invocation's stdout. Sending
	// it to a live daemon would put the output in the daemon's logs instead
	// (and older daemons did not know the per-frame DryRun field at all).
	if !cfg.DryRun && sendFrame(ctx, cfg.SockPath, frame, cfg.DialTimeout) {
		return
	}

	p := notify.Pipeline{
		DryRun: cfg.DryRun,
		Judge:  notify.Judge{Bin: disabledJudgeBin},
		Sender: cfg.Sender,
		Log:    cfg.Log,
		// NopState: notifyd holds the real dedupe state in memory now, so
		// this single fallback invocation has no shared history to consult
		// on disk — see NopState's doc comment for the reliability
		// rationale (a duplicate ping beats a lost one). Watchdog is left
		// nil (no field set below): this single invocation has no
		// long-lived goroutine to arm one on, so it runs with no watchdog
		// coverage — the documented degraded mode (see Watchdog's doc
		// comment).
		State:     notify.NopState{},
		Environ:   cfg.Environ,
		Stdout:    stdout,
		SelfBin:   cfg.SelfBin,
		Workspace: workspace,
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

func newNotifydPipeline(
	dryRun bool,
	sender notify.Sender,
	log notify.DecisionLog,
	selfBin string,
	environ []string,
) notify.Pipeline {
	return notify.Pipeline{
		DryRun: dryRun,
		Judge: notify.Judge{
			Bin:     "claude",
			Model:   notify.ResolveJudgeModel(environ, notifyJudgeModel),
			Timeout: notifyJudgeTimeout,
		},
		CodexJudge: notify.CodexJudge{
			Bin: "codex", Model: notify.ResolveCodexJudgeModel(environ, notifyCodexJudgeModel),
			Timeout: notifyCodexJudgeTimeout,
		},
		Sender:  sender,
		Log:     log,
		SelfBin: selfBin,
	}
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
		Pipeline: newNotifydPipeline(*dryRun, sender, log, selfBin, os.Environ()),
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
