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
	"sync"
	"syscall"
	"time"
)

const (
	listenFDStart        = 3
	connReadDeadline     = 5 * time.Second
	connWriteDeadline    = 5 * time.Second
	maximumGracefulDrain = 20 * time.Second
	socketDirPerm        = 0o700
)

type daemonEventLog int

const (
	daemonLogAccepted daemonEventLog = iota
	daemonLogAcceptedDegraded
	daemonLogDuplicate
	daemonLogSendFailed
)

// Daemon accepts minimal hook frames and runs each independently. Pipeline is
// immutable startup configuration; every invocation receives its own value
// copy with frame-specific workspace and dry-run state.
type Daemon struct {
	Pipeline Pipeline
	Logger   *slog.Logger

	// claims and clock are private daemon/test seams. Production always gets
	// a fresh in-memory store and time.Now at Serve startup; TTL/capacity are
	// fixed constants rather than runtime configuration.
	claims *claimStore
	clock  func() time.Time

	// drainTimeout shortens the graceful drain only in package tests. The
	// production zero value always uses maximumGracefulDrain.
	drainTimeout time.Duration
}

type daemonWorkTracker struct {
	mutex    sync.Mutex
	active   int
	stopping bool
	done     chan struct{}
}

func newDaemonWorkTracker() *daemonWorkTracker {
	return &daemonWorkTracker{done: make(chan struct{})}
}

func (tracker *daemonWorkTracker) start() {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.active++
}

func (tracker *daemonWorkTracker) finish() {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.active--
	if tracker.stopping && tracker.active == 0 {
		close(tracker.done)
	}
}

func (tracker *daemonWorkTracker) stop() <-chan struct{} {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.stopping = true
	if tracker.active == 0 {
		close(tracker.done)
	}
	return tracker.done
}

// Serve decodes and runs each accepted frame concurrently. Shutdown closes the
// listener, cancels in-flight composition, and gives accepted handlers a
// bounded opportunity to finish their fallback delivery and decision log.
func (daemon Daemon) Serve(ctx context.Context, listener net.Listener) error {
	// Claims are deliberately process-local and non-durable. A new Serve
	// lifecycle starts empty, including after a daemon restart.
	if daemon.claims == nil {
		daemon.claims = newClaimStore(daemon.clock)
	}
	workContext, cancelWork := context.WithCancel(ctx)
	tracker := newDaemonWorkTracker()
	watcherDone := make(chan struct{})
	watcherStopped := make(chan struct{})
	var closeListener sync.Once
	stopAccepting := func() {
		closeListener.Do(func() { _ = listener.Close() })
	}
	go func() {
		defer close(watcherStopped)
		select {
		case <-ctx.Done():
			stopAccepting()
		case <-watcherDone:
		}
	}()

	var serveErr error
	for {
		connection, err := listener.Accept()
		if err == nil {
			tracker.start()
			go func() {
				defer tracker.finish()
				daemon.handleConn(workContext, connection)
			}()
			continue
		}
		if ctx.Err() == nil {
			serveErr = fmt.Errorf("notify: accept: %w", err)
		}
		break
	}

	stopAccepting()
	cancelWork()
	close(watcherDone)
	<-watcherStopped
	daemon.drain(tracker.stop())
	return serveErr
}

func (daemon Daemon) drain(done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	timer := time.NewTimer(daemon.gracefulDrainTimeout())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (daemon Daemon) gracefulDrainTimeout() time.Duration {
	if daemon.drainTimeout > 0 && daemon.drainTimeout < maximumGracefulDrain {
		return daemon.drainTimeout
	}
	return maximumGracefulDrain
}

func (daemon Daemon) handleConn(ctx context.Context, connection net.Conn) {
	defer func() { _ = connection.Close() }()
	if err := connection.SetReadDeadline(time.Now().Add(connReadDeadline)); err != nil {
		daemon.logger().WarnContext(ctx, "notify frame rejected", "reason", "read_deadline")
		return
	}
	frame, err := DecodeFrame(connection)
	if err != nil {
		daemon.writeAck(ctx, connection, ackStatusRejected)
		daemon.logger().WarnContext(ctx, "notify frame rejected", "reason", "invalid_frame")
		return
	}

	if daemon.claims == nil {
		daemon.claims = newClaimStore(daemon.clock)
	}
	status := ackStatusAccepted
	key, token, claimed := daemon.claimFrame(frame)
	if claimed && token == 0 {
		status = ackStatusDuplicate
	}
	daemon.writeAck(ctx, connection, status)
	if status == ackStatusDuplicate {
		daemon.logEvent(ctx, daemonLogDuplicate, frame.Event)
		return
	}
	if frame.Event.Kind == eventKindCompletion &&
		(frame.Event.SessionID == "" || frame.Event.CompletionID == "") {
		daemon.logEvent(ctx, daemonLogAcceptedDegraded, frame.Event)
	} else {
		daemon.logEvent(ctx, daemonLogAccepted, frame.Event)
	}

	delivered := daemon.runFrame(ctx, frame)
	if claimed {
		daemon.claims.finish(key, token, delivered)
	}
	if !delivered {
		daemon.logEvent(ctx, daemonLogSendFailed, frame.Event)
	}
}

func (daemon Daemon) writeAck(ctx context.Context, connection net.Conn, status string) {
	if err := connection.SetWriteDeadline(time.Now().Add(connWriteDeadline)); err != nil {
		daemon.logger().WarnContext(ctx, "notify ack failed", "reason", "write_deadline")
		return
	}
	if err := EncodeAck(connection, Ack{Version: preparedEventVersion, Status: status}); err != nil {
		// Accepted work remains owned by the daemon even when the acknowledgement
		// cannot be observed; the client must treat that ambiguity as fallback.
		daemon.logger().WarnContext(ctx, "notify ack failed", "reason", "write")
	}
}

// claimFrame atomically admits only reliable non-dry root completions. Its
// bool reports eligibility; an eligible zero token is a duplicate.
func (daemon Daemon) claimFrame(frame Frame) (claimKey, claimToken, bool) {
	event := frame.Event
	decision := decidePreparedEvent(event)
	eligible := !daemon.Pipeline.DryRun && !frame.DryRun &&
		event.Kind == eventKindCompletion && decision.Outcome == OutcomeSend &&
		event.Harness != "" && event.SessionID != "" && event.CompletionID != ""
	if !eligible {
		return claimKey{}, 0, false
	}
	key := claimKey{
		Harness: event.Harness, SessionID: event.SessionID,
		Kind: event.Kind, CompletionID: event.CompletionID,
	}
	token, admitted := daemon.claims.claim(key)
	if !admitted {
		return key, 0, true
	}
	return key, token, true
}

// runFrame copies per-request routing state and processes the already-prepared
// snapshot without consulting a transcript or mutable hook input.
func (daemon Daemon) runFrame(ctx context.Context, frame Frame) bool {
	pipeline := daemon.Pipeline
	pipeline.Workspace = frame.Workspace
	pipeline.DryRun = pipeline.DryRun || frame.DryRun
	delivered, err := pipeline.processPrepared(ctx, frame.Event)
	return err == nil && delivered
}

func (daemon Daemon) logEvent(ctx context.Context, kind daemonEventLog, event PreparedEvent) {
	attributes := []any{
		frameFieldHarness, event.Harness,
		frameFieldSessionID, event.SessionID,
		frameFieldKind, event.Kind,
		frameFieldCompletionID, event.CompletionID,
		frameFieldSourceEvent, event.SourceEvent,
	}
	switch kind {
	case daemonLogAccepted:
		daemon.logger().InfoContext(ctx, "notify frame accepted", attributes...)
	case daemonLogAcceptedDegraded:
		daemon.logger().InfoContext(ctx, "notify frame accepted degraded", attributes...)
	case daemonLogDuplicate:
		daemon.logger().InfoContext(ctx, "notify frame duplicate", attributes...)
	case daemonLogSendFailed:
		daemon.logger().InfoContext(ctx, "notify send failed", attributes...)
	}
}

func (daemon Daemon) logger() *slog.Logger {
	if daemon.Logger != nil {
		return daemon.Logger
	}
	return slog.Default()
}

// SocketPath resolves the notifyd control socket path.
func SocketPath() string {
	if directory := os.Getenv("XDG_RUNTIME_DIR"); directory != "" {
		return filepath.Join(directory, "steward", "notifyd.sock")
	}
	return filepath.Join("/tmp", fmt.Sprintf("steward-%d", os.Getuid()), "notifyd.sock")
}

// Listen uses a systemd-activated socket when present and otherwise binds a
// private self-managed Unix socket.
func Listen(path string) (net.Listener, error) {
	if listener, ok, err := listenFromSystemd(); ok {
		return listener, err
	}
	return listenSelf(path)
}

func listenFromSystemd() (net.Listener, bool, error) {
	if !systemdSocketInherited() {
		return nil, false, nil
	}
	file := os.NewFile(uintptr(listenFDStart), "notifyd.sock")
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, true, fmt.Errorf("notify: binding inherited listen fd: %w", err)
	}
	return listener, true, nil
}

func systemdSocketInherited() bool {
	count, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || count < 1 {
		return false
	}
	pidValue := os.Getenv("LISTEN_PID")
	if pidValue == "" {
		return true
	}
	pid, err := strconv.Atoi(pidValue)
	return err == nil && pid == os.Getpid()
}

func listenSelf(path string) (net.Listener, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, socketDirPerm); err != nil {
		return nil, fmt.Errorf("notify: creating socket dir: %w", err)
	}
	if err := verifySocketDir(directory); err != nil {
		return nil, fmt.Errorf("notify: socket dir %s: %w", directory, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("notify: removing stale socket: %w", err)
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, fmt.Errorf("notify: listening on %s: %w", path, err)
	}
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("notify: chmod socket: %w", chmodErr)
	}
	return listener, nil
}

func verifySocketDir(directory string) error {
	info, err := os.Lstat(directory)
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
