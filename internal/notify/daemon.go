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
// goroutine (see Serve/loop). Pipeline is a template: StateBase, DryRun,
// Judge, Sender, Log, SelfBin, Host, and Present are the daemon's own fixed
// config, resolved once at startup — Environ and Workspace are overwritten
// per connection from the client's Frame, since those carry the hook
// invocation's own process context (see Frame's doc comment) that the
// daemon's single long-lived environment cannot supply. Pipeline.State is
// also overwritten per connection, with a loopState bound to the loop's
// channel — any State the caller sets on the template is discarded.
type Daemon struct {
	Pipeline Pipeline
	// Logger receives malformed-frame and per-connection diagnostics. A nil
	// Logger falls back to slog.Default().
	Logger *slog.Logger
}

// loopMsg is the sum type flowing through the event loop's single channel:
// either a newly accepted connection's frame, ready to start a Pipeline
// run, or a state operation queued by some Pipeline run (already off the
// loop, in its own goroutine) that needs to read or write the daemon's
// in-memory dedupe state. Exactly one of frame/op is set.
type loopMsg struct {
	frame *Frame
	op    func(*MemoryState)
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
// MemoryState it constructs, and the only goroutine ever allowed to touch
// it — see MemoryState's doc comment for why that means no mutex is
// needed. It drains ch until ctx is canceled, handling each loopMsg
// synchronously: a frame starts a new Pipeline run (in its own goroutine,
// off the loop); a state op is applied to mem and returns immediately (a
// map read/write, never I/O), so the loop is never blocked waiting on a
// judge call, a notification send, or another frame's Pipeline run.
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
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if msg.frame != nil {
				d.runFrame(ctx, *msg.frame, ch)
			} else {
				msg.op(mem)
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
	p.State = loopState{ch: ch}

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

// ClaimBroadcast implements DedupeState via the loop's MemoryState.
func (l loopState) ClaimBroadcast(
	ctx context.Context, key string, window time.Duration, now time.Time, dryRun bool,
) bool {
	var won bool
	l.call(ctx, func(m *MemoryState) { won = m.ClaimBroadcast(key, window, now, dryRun) })
	return won
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

// listenSelf binds a fresh unix socket at path, per Listen's self-bind
// contract.
func listenSelf(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("notify: creating socket dir: %w", err)
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
