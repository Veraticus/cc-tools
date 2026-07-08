package notify

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// listenFDStart is the first inherited file descriptor under systemd's
// socket activation protocol (sd_listen_fds' SD_LISTEN_FDS_START): fds 0-2
// are stdio, so an activated unit's sockets begin at 3.
const listenFDStart = 3

// connReadDeadline bounds how long a single accepted connection may take to
// deliver its frame: each connection is handled in its own goroutine (see
// Daemon.Serve), so a slow or hung client costs one goroutine and one file
// descriptor rather than blocking the accept loop — but without a deadline
// that cost would be unbounded and permanent.
const connReadDeadline = 5 * time.Second

// Daemon runs the notify Pipeline once per accepted connection, serializing
// every access to its in-memory dedupe state through a single event-loop
// goroutine (see Serve/loop). Pipeline is a template: DryRun, Judge, Sender,
// Log, SelfBin, and Host are the daemon's own fixed config, resolved once at
// startup — Environ, Workspace, and ParentPID are overwritten per connection
// from the client's Frame, since those carry the
// hook invocation's own process context (see Frame's doc comment) that the
// daemon's single long-lived environment cannot supply. Pipeline.State and
// Pipeline.Watchdog are also overwritten per connection, bound to the loop's
// channel (loopState and daemonWatchdog respectively) — any State or
// Watchdog the caller sets on the template is discarded.
type Daemon struct {
	Pipeline Pipeline
	// Logger receives malformed-frame and per-connection diagnostics. A nil
	// Logger falls back to slog.Default().
	Logger *slog.Logger
}

// loopMsg is the sum type flowing through the event loop's single channel:
// a newly accepted connection's frame, ready to start a Pipeline run; a
// state operation queued by some Pipeline run (already off the loop, in its
// own goroutine) that needs to read or write the daemon's in-memory dedupe
// state; or a watchdog operation touching the loop's watchdog registry
// (arming, reaping, or a watchdog goroutine's own exit cleanup). Exactly one
// of frame/op/watchdogOp is set.
type loopMsg struct {
	frame      *Frame
	op         func(*MemoryState)
	watchdogOp func(*watchdogRegistry)
}

// Serve accepts connections on ln until ctx is canceled. Each connection is
// decoded in its own goroutine (so a slow or malformed sender never delays
// another), then handed to the single event-loop goroutine (see loop) as a
// loopMsg. The loop starts each frame's Pipeline run in its own goroutine
// too — judge and sender calls happen there, off the loop — so the only
// thing ever serialized through the loop itself is access to its
// in-memory MemoryState. It returns nil when ctx cancellation caused ln to
// close; any other Accept error is returned to the caller.
func (d Daemon) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	ch := make(chan loopMsg)
	go d.loop(ctx, ch)

	for {
		conn, err := ln.Accept()
		if err == nil {
			go d.handleConn(ctx, conn, ch)
			continue
		}

		// ctx.Done() already closed means ln.Close() (triggered above, on
		// cancellation) is what unblocked Accept — an expected shutdown, not
		// a failure to report.
		select {
		case <-ctx.Done():
			return nil
		default:
			return fmt.Errorf("notify: accept: %w", err)
		}
	}
}

// loop is the daemon's single event-loop goroutine: the sole owner of the
// MemoryState it constructs and of reg, its watchdog registry — and the only
// goroutine ever allowed to touch either, so neither needs a mutex (see
// MemoryState's doc comment). It drains ch until ctx is canceled, handling
// each loopMsg synchronously: a frame starts a new Pipeline run (in its own
// goroutine, off the loop); a state op is applied to mem and returns
// immediately (a map read/write, never I/O); a watchdogOp is applied to reg
// the same way — arming/reaping cancels and updates an entry, and starting a
// watchdog's own goroutine is a bare `go` statement, not a blocking call —
// so the loop is never blocked waiting on a judge call, a notification send,
// or another frame's Pipeline run. On ctx cancellation (daemon shutdown),
// every still-live watchdog is canceled before the loop returns.
//
// Same-session races: while one frame's judge call for session S is in
// flight (off-loop), a second frame for S can arrive and be dispatched
// immediately — it is processed normally, reading whatever state the loop
// holds at that moment. When the first frame's judge continuation later
// re-enters the loop (e.g. to call MarkNotified), it likewise sees
// whatever state is then current. This is deliberately the same race the
// old file-based, one-goroutine-per-connection design already had — no
// new ordering guarantee is introduced or promised between two frames for
// the same session.
func (d Daemon) loop(ctx context.Context, ch chan loopMsg) {
	mem := NewMemoryState()
	reg := &watchdogRegistry{entries: make(map[string]watchdogEntry)}
	for {
		select {
		case <-ctx.Done():
			for _, e := range reg.entries {
				e.cancel()
			}
			return
		case msg := <-ch:
			switch {
			case msg.frame != nil:
				d.runFrame(ctx, *msg.frame, ch)
			case msg.op != nil:
				msg.op(mem)
			case msg.watchdogOp != nil:
				msg.watchdogOp(reg)
			}
		}
	}
}

// runFrame starts one frame's Pipeline run in its own goroutine: this is
// what keeps judge and sender calls off the event loop. Pipeline.deliver
// (a Sender.Send call) stays inside this same goroutine rather than
// bouncing back through the loop for delivery too — only the state access
// around it (MarkNotified, SinceLastNotify, ClaimBroadcast) needs to be
// loop-serialized, and loopState already provides that regardless of which
// goroutine calls it.
func (d Daemon) runFrame(ctx context.Context, frame Frame, ch chan<- loopMsg) {
	p := d.Pipeline
	p.Environ = frame.Environ
	p.Workspace = frame.Workspace
	p.ParentPID = frame.ParentPID
	p.State = loopState{ch: ch}
	p.Watchdog = daemonWatchdog{
		ch: ch, done: ctx.Done(), judge: p.Judge, sender: p.Sender, log: p.Log,
	}

	go func() {
		if runErr := p.Run(ctx, frame.HookInput); runErr != nil {
			d.logger().ErrorContext(
				ctx, "notify: pipeline run failed", "error", runErr, "session_id", frame.HookInput.SessionID,
			)
		}
	}()
}

// loopState is DedupeState's daemon-side implementation: every method call
// — from whichever per-frame goroutine is running a Pipeline, including a
// judge continuation resuming well after its frame first arrived — sends a
// closure onto ch and blocks for the loop to run it against the shared
// MemoryState, then reports the result back to the loop, exactly like a
// newly accepted frame goes through the same channel. That round trip
// through ch, not a mutex, is what makes MemoryState access safe from any
// goroutine.
type loopState struct {
	ch chan<- loopMsg
}

// call runs op against the loop's MemoryState and waits for it to finish,
// or gives up early if ctx is canceled before the send — guarding against
// a caller blocking forever sending to ch after the loop itself has
// already exited on shutdown. ctx is a plain parameter, not a loopState
// field: the same loopState is reused across every DedupeState call a
// Pipeline run makes, potentially long after the frame that created it, so
// it carries no ctx of its own — each call gets the ctx it was actually
// invoked with.
//
// Once l.ch <- msg succeeds, the loop has committed to running op to
// completion as part of handling that single channel receive — it never
// re-checks ctx mid-body, and op is pure map access with no I/O, so done
// is guaranteed to close promptly. The wait after a successful send is
// therefore unconditional (`<-done`, not raced against ctx.Done()): racing
// it would let call() return while op is still writing into a variable the
// caller (e.g. SinceLastNotify) reads immediately after — a data race, not
// a real cancellation path, since op can never hang.
func (l loopState) call(ctx context.Context, op func(*MemoryState)) {
	done := make(chan struct{})
	msg := loopMsg{op: func(m *MemoryState) {
		op(m)
		close(done)
	}}
	select {
	case l.ch <- msg:
	case <-ctx.Done():
		return
	}
	<-done
}

// SinceLastNotify implements DedupeState via the loop's MemoryState.
func (l loopState) SinceLastNotify(ctx context.Context, sessionID string, now time.Time) time.Duration {
	result := neverNotifiedDuration
	l.call(ctx, func(m *MemoryState) { result = m.SinceLastNotify(sessionID, now) })
	return result
}

// SinceLastNotifySame implements DedupeState via the loop's MemoryState.
func (l loopState) SinceLastNotifySame(
	ctx context.Context, sessionID string, now time.Time, message string,
) time.Duration {
	result := neverNotifiedDuration
	l.call(ctx, func(m *MemoryState) { result = m.SinceLastNotifySame(sessionID, now, message) })
	return result
}

// MarkNotified implements DedupeState via the loop's MemoryState.
func (l loopState) MarkNotified(ctx context.Context, sessionID string, t time.Time, message string) error {
	l.call(ctx, func(m *MemoryState) { _ = m.MarkNotified(sessionID, t, message) })
	return nil
}

// ClaimSend implements DedupeState via the loop's MemoryState. The default
// (a shutdown-canceled ctx skips the loop round trip) is a win with no
// history: failing open to a possible duplicate rather than eating a ping,
// matching NopState.
func (l loopState) ClaimSend(
	ctx context.Context, sessionID string, now time.Time, message string, window time.Duration, dryRun bool,
) (bool, time.Duration) {
	won, since := true, neverNotifiedDuration
	l.call(ctx, func(m *MemoryState) { won, since = m.ClaimSend(sessionID, now, message, window, dryRun) })
	return won, since
}

// ClaimBroadcast implements DedupeState via the loop's MemoryState.
func (l loopState) ClaimBroadcast(
	ctx context.Context, key string, window time.Duration, now time.Time, dryRun bool,
) bool {
	var won bool
	l.call(ctx, func(m *MemoryState) { won = m.ClaimBroadcast(key, window, now, dryRun) })
	return won
}

// DeleteSession implements DedupeState via the loop's MemoryState.
func (l loopState) DeleteSession(ctx context.Context, sessionID string) {
	l.call(ctx, func(m *MemoryState) { m.DeleteSession(sessionID) })
}

// watchdogEntry is one session's live watchdog registration in the loop's
// registry (see watchdogRegistry): cancel stops its goroutine, on Reap or on
// supersession by a later Arm. gen distinguishes this registration from a
// later one for the same session, so the goroutine's own exit-cleanup op
// (see cleanupWatchdog) only removes itself from the map if nothing has
// since re-armed the session — otherwise it would delete the newer entry a
// superseding Arm just installed.
type watchdogEntry struct {
	cancel context.CancelFunc
	gen    uint64
}

// watchdogRegistry is Daemon.loop's watchdog bookkeeping, loop-confined like
// the MemoryState it sits alongside: entries maps each live session to its
// current watchdogEntry, and nextGen assigns every Arm a generation number
// unique for the loop's entire lifetime — never derived from (and so never
// reset by) a map entry, which Reap or a watchdog's own exit can delete out
// from under it. Deriving gen from the entry itself (old.gen + 1) was
// previously incorrect: Reap-then-re-Arm restarts at gen 1 with no entry to
// derive from, so the reaped watchdog's delayed exit-cleanup op (captured
// gen 1) could match the successor's freshly-registered gen 1 and delete
// it — leaking a live watchdog past the shutdown-cancel branch and
// permitting two concurrent watchdogs for one session. A counter that only
// ever increases closes that: no later Arm can ever reuse an earlier one's
// generation, deleted entry or not.
type watchdogRegistry struct {
	entries map[string]watchdogEntry
	nextGen uint64
}

// daemonWatchdog is Pipeline.Watchdog's daemon-side implementation. Arm and
// Reap each queue a closure onto the event loop's channel, so the
// loop-confined cancel map in Daemon.loop is only ever touched by the loop
// goroutine itself — the same discipline MemoryState relies on. done is
// ctx.Done() rather than a stored context.Context: a struct field can't hold
// a context.Context (see loopState, which has the same constraint), and a
// bare receive-only channel gives Arm/Reap the same shutdown-aware,
// non-blocking-forever send loopState.call already relies on.
type daemonWatchdog struct {
	ch     chan<- loopMsg
	done   <-chan struct{}
	judge  Judge
	sender Sender
	log    DecisionLog
}

// Arm implements Watchdog: it queues armWatchdog onto the loop.
func (w daemonWatchdog) Arm(req WatchdogArmRequest) {
	select {
	case w.ch <- loopMsg{watchdogOp: func(reg *watchdogRegistry) {
		armWatchdog(reg, req, w)
	}}:
	case <-w.done:
	}
}

// Reap implements Watchdog: it queues a closure that cancels and removes
// sessionID's watchdog entry, if one exists. Idempotent: no entry is a
// no-op.
func (w daemonWatchdog) Reap(sessionID string) {
	select {
	case w.ch <- loopMsg{watchdogOp: func(reg *watchdogRegistry) {
		if e, ok := reg.entries[sessionID]; ok {
			e.cancel()
			delete(reg.entries, sessionID)
		}
	}}:
	case <-w.done:
	}
}

// armWatchdog is loop-confined: called only from the watchdogOp Arm queues,
// so it is the loop goroutine itself that ever touches reg. It cancels any
// existing watchdog for req.SessionID (supersession), then starts exactly
// one new watchdog goroutine running RunWatchdog against a fresh, standalone
// cancelable context — standalone rather than a child of the daemon's own
// ctx, since Daemon.loop's shutdown branch already cancels every entry
// directly when its ctx is done, achieving the same effect without
// daemonWatchdog needing to store that ctx (see its doc comment). gen comes
// from reg.nextGen, not the entry being replaced — see watchdogRegistry's
// doc comment for why that distinction is load-bearing.
func armWatchdog(reg *watchdogRegistry, req WatchdogArmRequest, w daemonWatchdog) {
	if old, existed := reg.entries[req.SessionID]; existed {
		old.cancel()
	}
	reg.nextGen++
	gen := reg.nextGen
	wctx, cancel := context.WithCancel(context.Background())
	reg.entries[req.SessionID] = watchdogEntry{cancel: cancel, gen: gen}

	deps := DefaultWatchdogDeps(w.judge, w.sender, w.log)
	deps.ClaimSend = watchdogClaimSend(w.ch)

	go func() {
		RunWatchdog(wctx, req, deps)
		select {
		case w.ch <- loopMsg{watchdogOp: func(reg *watchdogRegistry) {
			cleanupWatchdog(reg, req.SessionID, gen)
		}}:
		case <-w.done:
		}
	}()
}

// cleanupWatchdog is loop-confined, queued by a watchdog goroutine on its
// own exit: it removes sessionID's entry from reg only if that entry's gen
// still matches the generation this watchdog was armed with — otherwise a
// later Arm has already superseded it, and removing the entry would delete
// that (live) successor instead of this (now-exited) watchdog's own stale
// registration. Factored out from armWatchdog's goroutine so a test can
// drive this exact interleaving deterministically (see
// TestArmWatchdog_ReapThenReArm_DelayedCleanupDoesNotDeleteSuccessor).
func cleanupWatchdog(reg *watchdogRegistry, sessionID string, gen uint64) {
	if cur, ok := reg.entries[sessionID]; ok && cur.gen == gen {
		delete(reg.entries, sessionID)
	}
}

// watchdogClaimSend builds WatchdogDeps.ClaimSend against the loop's own
// DedupeState (loopState): the watchdog's one send claims the session with
// the same atomic check-and-mark an ordinary hook invocation's judged send
// runs, even though the send itself happens off-loop, in the watchdog's own
// goroutine — so a watchdog never re-pings a session a hook event pinged
// within dedupeWindow, and its own send is visible to later hook events'
// dedupe reads.
func watchdogClaimSend(
	ch chan<- loopMsg,
) func(ctx context.Context, sessionID string, now time.Time, message string) (bool, time.Duration) {
	ds := loopState{ch: ch}
	return func(ctx context.Context, sessionID string, now time.Time, message string) (bool, time.Duration) {
		return ds.ClaimSend(ctx, sessionID, now, message, dedupeWindow, false)
	}
}

// handleConn decodes exactly one Frame off conn and hands it to the event
// loop via ch. A malformed frame is logged and the connection dropped —
// never fatal to the daemon, per the transport contract that one bad client
// must not take down service for every other connection.
func (d Daemon) handleConn(ctx context.Context, conn net.Conn, ch chan<- loopMsg) {
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(connReadDeadline)); err != nil {
		d.logger().ErrorContext(ctx, "notify: setting connection read deadline", "error", err)
		return
	}

	frame, err := DecodeFrame(conn)
	if err != nil {
		d.logger().WarnContext(ctx, "notify: malformed frame, dropping connection", "error", err)
		return
	}

	select {
	case ch <- loopMsg{frame: &frame}:
	case <-ctx.Done():
	}
}

// logger returns Logger, or slog.Default() when unset.
func (d Daemon) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// SocketPath resolves the notifyd control socket path:
// $XDG_RUNTIME_DIR/cc-tools/notifyd.sock, falling back to
// /tmp/cc-tools-$UID/notifyd.sock when XDG_RUNTIME_DIR is unset.
func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "cc-tools", "notifyd.sock")
	}
	return filepath.Join("/tmp", fmt.Sprintf("cc-tools-%d", os.Getuid()), "notifyd.sock")
}

// Listen binds the notifyd control socket. When the process was started
// with systemd socket activation (LISTEN_FDS names at least one inherited
// fd, and LISTEN_PID — if set — matches this process), it reuses the
// inherited socket at listenFDStart and path is ignored, since systemd
// already bound and owns that socket file. Otherwise it self-binds at path:
// creating path's parent directory (0700), removing any stale socket left
// by a prior crashed daemon, and setting the new socket file to 0600.
func Listen(path string) (net.Listener, error) {
	if ln, ok, err := listenFromSystemd(); ok {
		return ln, err
	}
	return listenSelf(path)
}

// listenFromSystemd reports ok=true when LISTEN_FDS indicates an inherited
// socket is available, regardless of whether binding it then succeeds — a
// caller that gets ok=true should not fall back to self-binding, since
// systemd (not this process) owns the socket file in that case.
func listenFromSystemd() (net.Listener, bool, error) {
	if !systemdSocketInherited() {
		return nil, false, nil
	}

	f := os.NewFile(uintptr(listenFDStart), "notifyd.sock")
	ln, lnErr := net.FileListener(f)
	_ = f.Close()
	if lnErr != nil {
		return nil, true, fmt.Errorf("notify: binding inherited listen fd: %w", lnErr)
	}
	return ln, true, nil
}

// systemdSocketInherited reports whether the process's environment
// indicates a socket-activated fd is available at listenFDStart:
// LISTEN_FDS names at least one inherited fd, and LISTEN_PID — when set —
// matches this process (guarding against a stale env var surviving into an
// unrelated child that inherited it by accident).
func systemdSocketInherited() bool {
	n, convErr := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	fdsOK := convErr == nil && n >= 1
	if !fdsOK {
		return false
	}

	pidStr := os.Getenv("LISTEN_PID")
	if pidStr == "" {
		return true
	}
	pid, pidErr := strconv.Atoi(pidStr)
	return pidErr == nil && pid == os.Getpid()
}

// socketDirPerm is the permission mode required of the directory a
// self-bound notifyd socket lives in — owner-only, mirroring cacheDirPerm
// in statusline/cache.go.
const socketDirPerm = 0o700

// listenSelf binds a fresh unix socket at path, per Listen's self-bind
// contract.
func listenSelf(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, socketDirPerm); err != nil {
		return nil, fmt.Errorf("notify: creating socket dir: %w", err)
	}
	if err := verifySocketDir(dir); err != nil {
		return nil, fmt.Errorf("notify: socket dir %s: %w", dir, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("notify: removing stale socket: %w", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, fmt.Errorf("notify: listening on %s: %w", path, err)
	}
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("notify: chmod socket: %w", chmodErr)
	}
	return ln, nil
}

// verifySocketDir rejects dir unless it is a real directory (not a
// symlink), mode socketDirPerm, and owned by this process's uid — the same
// three checks openCacheRoot applies to statusline's shared cache directory
// (see statusline/cache.go), applied here because a self-bound socket's
// parent can equally be a shared, world-writable location (/tmp, when
// XDG_RUNTIME_DIR is unset) that another local user got to first.
// os.MkdirAll does not change an existing directory's mode or owner, so
// without this check a pre-planted world-writable directory — or a symlink
// redirecting elsewhere — would let another local user unlink the 0600
// socket this process binds and substitute their own, receiving every
// client's Frame (including its full os.Environ() copy) instead. Fails
// closed: any mismatch is an error, never a warning, so the caller's
// listenErr path exits nonzero rather than binding inside an untrusted dir.
func verifySocketDir(dir string) error {
	// Lstat, not Stat: a planted symlink to a directory must be seen as a
	// symlink (IsDir() false) and rejected, not resolved through.
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return errors.New("not a real directory (possibly a symlink)")
	}
	if info.Mode().Perm() != socketDirPerm {
		return fmt.Errorf("mode %o, want %o", info.Mode().Perm(), socketDirPerm)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine directory owner")
	}
	if int(stat.Uid) != os.Getuid() {
		return errors.New("not owned by this user")
	}
	return nil
}
