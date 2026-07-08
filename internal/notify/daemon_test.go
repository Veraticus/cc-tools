package notify

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listenHelperEnv, when set to "1" in this test binary's own environment,
// diverts TestMain into runListenHelper instead of the normal test suite:
// see TestListen_SystemdSocketActivation_BindsInheritedFD, which re-execs
// this binary as a subprocess to exercise Listen's LISTEN_FDS path. That
// path binds fd 3 directly (systemd's SD_LISTEN_FDS_START), and fd 3 in
// *this* process is already the Go runtime's own netpoller epoll fd — only
// a freshly started process, before its runtime claims low fds, is safe to
// hand a socket at fd 3 the way a real systemd-activated daemon receives
// one.
const listenHelperEnv = "CC_TOOLS_NOTIFY_LISTEN_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(listenHelperEnv) == "1" {
		runListenHelper()
		return
	}
	os.Exit(m.Run())
}

// runListenHelper is the subprocess body for the systemd-activation test:
// it calls Listen (expecting LISTEN_FDS=1 and an inherited socket at fd 3,
// set up by the parent via exec.Cmd.ExtraFiles), accepts one connection to
// prove the listener is live, and reports progress on stdout for the parent
// to assert on.
func runListenHelper() {
	ln, err := Listen(filepath.Join(os.TempDir(), "should-not-be-used.sock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("LISTENING")

	conn, err := ln.Accept()
	if err != nil {
		fmt.Fprintf(os.Stderr, "accept error: %v\n", err)
		os.Exit(1)
	}
	_ = conn.Close()
	fmt.Println("ACCEPTED")
	os.Exit(0)
}

// waitForDecisionRecords polls path until it holds at least n decision
// records or timeout elapses: Daemon.Serve dispatches each connection in
// its own goroutine, so a test observing the resulting DecisionLog write
// has no synchronous signal to wait on instead.
func waitForDecisionRecords(t *testing.T, path string, n int, timeout time.Duration) []DecisionRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			if recs := readDecisionLog(t, path); len(recs) >= n {
				return recs
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d decision record(s) at %s", n, path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newTestDaemon builds a Daemon whose Pipeline is a DryRun pipeline pointed
// at a fresh temp state base, mirroring newTestPipeline's fakes.
func newTestDaemon(t *testing.T) (Daemon, string) {
	t.Helper()
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	d := Daemon{
		Pipeline: Pipeline{
			StateBase: stateBase,
			DryRun:    true,
			Judge:     Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
			Log:       DecisionLog{Path: logPath},
			Stdout:    os.Stdout,
			Host:      "testhost",
			Present:   neverPresent,
		},
	}
	return d, logPath
}

func TestDaemon_Serve_AcceptsConnectionAndRunsInjectedPipeline(t *testing.T) {
	d, logPath := newTestDaemon(t)

	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- d.Serve(ctx, ln) }()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	frame := Frame{
		HookInput: HookInput{SessionID: "sess-daemon-1", HookEventName: "SessionEnd"},
	}
	if encErr := EncodeFrame(conn, frame); encErr != nil {
		t.Fatalf("EncodeFrame() error = %v", encErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("conn.Close() error = %v", closeErr)
	}

	recs := waitForDecisionRecords(t, logPath, 1, 2*time.Second)
	if recs[0].SessionID != "sess-daemon-1" || recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("record = %+v, want session end silent record for sess-daemon-1", recs[0])
	}

	cancel()
	select {
	case serveErr := <-serveDone:
		if serveErr != nil {
			t.Errorf("Serve() error = %v, want nil after ctx cancel", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after ctx cancel")
	}
}

func TestDaemon_Serve_MalformedFrame_LogsAndKeepsServing(t *testing.T) {
	d, logPath := newTestDaemon(t)

	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Serve(ctx, ln) }()

	badConn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	if _, writeErr := badConn.Write([]byte("this is not json{")); writeErr != nil {
		t.Fatalf("Write() error = %v", writeErr)
	}
	if closeErr := badConn.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	goodConn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	frame := Frame{HookInput: HookInput{SessionID: "sess-after-malformed", HookEventName: "SessionEnd"}}
	if encErr := EncodeFrame(goodConn, frame); encErr != nil {
		t.Fatalf("EncodeFrame() error = %v", encErr)
	}
	if closeErr := goodConn.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	recs := waitForDecisionRecords(t, logPath, 1, 2*time.Second)
	if recs[0].SessionID != "sess-after-malformed" {
		t.Errorf("record = %+v, want the post-malformed connection's session to have been processed", recs[0])
	}
}

func TestListen_SelfBind_CreatesDirAndSocketWithExpectedPerms(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")

	runtimeDir := t.TempDir()
	sockPath := filepath.Join(runtimeDir, "cc-tools", "notifyd.sock")

	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	dirInfo, err := os.Stat(filepath.Dir(sockPath))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir perm = %o, want 0700", perm)
	}

	sockInfo, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket file: %v", err)
	}
	if perm := sockInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket file perm = %o, want 0600", perm)
	}
}

func TestListen_SelfBind_RemovesStaleSocket(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "notifyd.sock")

	// Simulate a crashed prior daemon: a socket file (well, any leftover
	// file — Listen must not care which) sits at sockPath with nothing
	// listening on it.
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatalf("writing stale socket file: %v", err)
	}

	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen() error = %v, want the stale socket to be replaced", err)
	}
	_ = ln.Close()
}

func TestListen_SystemdSocketActivation_BindsInheritedFD(t *testing.T) {
	if os.Getenv(listenHelperEnv) == "1" {
		t.Skip("this test run is the re-exec'd helper process; see TestMain")
	}

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "sd.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener type = %T, want *net.UnixListener", ln)
	}

	f, err := unixLn.File()
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	defer func() { _ = f.Close() }()

	// exec.Cmd.ExtraFiles places f at fd 3 in the child — exactly how
	// systemd hands off a socket-activated unit's listening socket, and
	// safe here because the child's Go runtime hasn't started yet (unlike
	// dup2'ing onto fd 3 in this already-running process, which would
	// clobber the runtime's own netpoller fd).
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), listenHelperEnv+"=1", "LISTEN_FDS=1")
	cmd.ExtraFiles = []*os.File{f}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("starting helper subprocess: %v", startErr)
	}

	// The helper needs a moment to reach Accept() after Listen() returns; a
	// short retrying dial handles that race without an arbitrary fixed
	// sleep.
	var dialErr error
	for range 50 {
		var conn net.Conn
		conn, dialErr = net.Dial("unix", sockPath)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dialErr != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("dialing inherited listener: %v", dialErr)
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		t.Fatalf("helper subprocess failed: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "LISTENING") || !strings.Contains(stdout.String(), "ACCEPTED") {
		t.Errorf("helper stdout = %q, want it to report LISTENING and ACCEPTED", stdout.String())
	}
}

func TestListen_LISTEN_FDS_Zero_FallsBackToSelfBind(t *testing.T) {
	t.Setenv("LISTEN_FDS", "0")

	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := Listen(sockPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Errorf("stat self-bound socket: %v, want Listen to have self-bound at sockPath", statErr)
	}
}

func TestSocketPath_UsesXDGRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	want := filepath.Join("/run/user/1000", "cc-tools", "notifyd.sock")
	if got := SocketPath(); got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}

func TestSocketPath_FallsBackToTmp_WhenXDGRuntimeDirUnset(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	got := SocketPath()
	want := filepath.Join("/tmp", "cc-tools-"+strconv.Itoa(os.Getuid()), "notifyd.sock")
	if got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
}
