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

// notifyHookTimeout bounds the hook client, including its inline delivery
// fallback. Daemon-side Pi composition is asynchronous to the client.
const notifyHookTimeout = 80 * time.Second

// decisionLogName is the decision log's filename inside the notify state base.
const decisionLogName = "notify-decisions.jsonl"

// clientDialTimeout bounds how long the hook client waits to reach notifyd
// before giving up and running its own inline fallback: short enough that
// an unreachable or overloaded daemon never meaningfully delays the hook
// (which must return well inside its own settings.json timeout), long
// enough to survive a momentary scheduling delay on a live daemon.
const clientDialTimeout = 250 * time.Millisecond

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
	stateBase := flags.String("state-base", defaultNotifyStateBase(), "root directory for notify state")
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
	SockPath    string
	DialTimeout time.Duration
}

// dispatchNotify parses one hook payload, sends a minimal frame to notifyd,
// and uses a deterministic model-free inline fallback when the daemon is
// unreachable. Dry runs also stay inline so their output reaches this client.
func dispatchNotify(ctx context.Context, cfg notifyClientConfig, stdin io.Reader, stdout, stderr io.Writer) {
	in, err := notify.ParseHookInputForHarness(stdin, cfg.Harness)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cc-tools notify: %v\n", err)
		return
	}

	workspace := notify.WorkspaceName(cfg.Environ, notify.RunCommand)
	frame := notify.Frame{HookInput: in, Workspace: workspace, DryRun: cfg.DryRun}
	// A dry run must print its rehearsal to this invocation's stdout. Sending
	// it to a live daemon would put the output in the daemon's logs instead
	// (and older daemons did not know the per-frame DryRun field at all).
	if !cfg.DryRun && sendFrame(ctx, cfg.SockPath, frame, cfg.DialTimeout) {
		return
	}

	p := notify.Pipeline{
		DryRun:    cfg.DryRun,
		Sender:    cfg.Sender,
		Log:       cfg.Log,
		Stdout:    stdout,
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
	environ []string,
) notify.Pipeline {
	pipeline := notify.Pipeline{
		DryRun: dryRun,
		Sender: sender,
		Log:    log,
		Stdout: os.Stdout,
	}
	composer, err := notify.NewPiComposer(environ)
	if err != nil {
		pipeline.CompositionError = err
		return pipeline
	}
	pipeline.Composer = composer
	return pipeline
}

// runNotifydCommand runs the notifyd daemon: it constructs the real
// Pipeline dependencies once (unlike the hook client, which resolves them
// fresh on every invocation) and serves the control socket until SIGTERM or
// SIGINT, then removes the socket file before exiting.
func runNotifydCommand() {
	flags := flag.NewFlagSet("notifyd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "print what would be sent instead of sending it")
	stateBase := flags.String("state-base", defaultNotifyStateBase(), "root directory for notify state")
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

	d := notify.Daemon{
		Pipeline: newNotifydPipeline(*dryRun, sender, log, os.Environ()),
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
