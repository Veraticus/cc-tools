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

// Daemon runs the notify Pipeline once per accepted connection. Pipeline is
// a template: StateBase, DryRun, Judge, Sender, Log, SelfBin, Host, and
// Present are the daemon's own fixed config, resolved once at startup —
// Environ and Workspace are overwritten per connection from the client's
// Frame, since those carry the hook invocation's own process context (see
// Frame's doc comment) that the daemon's single long-lived environment
// cannot supply.
type Daemon struct {
	Pipeline Pipeline
	// Logger receives malformed-frame and per-connection diagnostics. A nil
	// Logger falls back to slog.Default().
	Logger *slog.Logger
}

// Serve accepts connections on ln until ctx is canceled, dispatching each
// to its own goroutine so one slow judge or sender call never delays
// another connection. It returns nil when ctx cancellation caused ln to
// close; any other Accept error is returned to the caller.
func (d Daemon) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err == nil {
			go d.handleConn(ctx, conn)
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

// handleConn decodes exactly one Frame off conn and runs the Pipeline
// against it. A malformed frame is logged and the connection dropped —
// never fatal to the daemon, per the transport contract that one bad client
// must not take down service for every other connection.
func (d Daemon) handleConn(ctx context.Context, conn net.Conn) {
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

	p := d.Pipeline
	p.Environ = frame.Environ
	p.Workspace = frame.Workspace

	if runErr := p.Run(ctx, frame.HookInput); runErr != nil {
		d.logger().ErrorContext(
			ctx, "notify: pipeline run failed", "error", runErr, "session_id", frame.HookInput.SessionID,
		)
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
