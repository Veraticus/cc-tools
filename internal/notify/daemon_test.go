package notify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func dialAndSendFrame(t *testing.T, socketPath string, frame Frame) {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dialing daemon: %v", err)
	}
	if encodeErr := EncodeFrame(connection, frame); encodeErr != nil {
		_ = connection.Close()
		t.Fatalf("encoding frame: %v", encodeErr)
	}
	if closeErr := connection.Close(); closeErr != nil {
		t.Fatalf("closing frame connection: %v", closeErr)
	}
}

func waitForDecisionRecords(t *testing.T, path string, count int) []DecisionRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			records := readDecisionLog(t, path)
			if len(records) >= count {
				return records
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d decision records", count)
	return nil
}

func startTestDaemon(t *testing.T, pipeline Pipeline) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "notifyd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (Daemon{Pipeline: pipeline}).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case serveErr := <-done:
			if serveErr != nil {
				t.Errorf("Serve() error = %v", serveErr)
			}
		case <-time.After(2 * time.Second):
			t.Error("Serve() did not stop")
		}
	})
	return socketPath
}

type blockingAttributionComposer struct {
	entered chan ComposeInput
	release <-chan struct{}
}

func (c blockingAttributionComposer) Compose(
	_ context.Context,
	input ComposeInput,
	label ComposeLabel,
) (ComposeResult, error) {
	if label != (ComposeLabel{}) {
		return ComposeResult{}, fmt.Errorf("unexpected label: %+v", label)
	}
	c.entered <- input
	<-c.release
	return ComposeResult{Body: "summary for " + input.Assistant}, nil
}

type daemonContextKey struct{}

type cancelAwareComposer struct {
	entered chan any
}

func (c cancelAwareComposer) Compose(
	ctx context.Context,
	_ ComposeInput,
	_ ComposeLabel,
) (ComposeResult, error) {
	c.entered <- ctx.Value(daemonContextKey{})
	<-ctx.Done()
	return ComposeResult{}, fmt.Errorf("pi composer: helper canceled")
}

type deliveryContextObservation struct {
	value       any
	err         error
	hasDeadline bool
	remaining   time.Duration
}

type observingTransport struct {
	base         http.RoundTripper
	observations chan<- deliveryContextObservation
}

func (transport observingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline, hasDeadline := request.Context().Deadline()
	transport.observations <- deliveryContextObservation{
		value:       request.Context().Value(daemonContextKey{}),
		err:         request.Context().Err(),
		hasDeadline: hasDeadline,
		remaining:   time.Until(deadline),
	}
	return transport.base.RoundTrip(request)
}

type successfulTransport struct{}

func (successfulTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func TestDaemonShutdownCancelsCompositionAndDrainsFallback(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	observations := make(chan deliveryContextObservation, 1)
	client := server.Client()
	client.Transport = observingTransport{base: client.Transport, observations: observations}
	composerEntered := make(chan any, 1)
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := Pipeline{
		Composer: cancelAwareComposer{entered: composerEntered},
		Sender:   Sender{URL: server.URL, Client: client},
		Log:      DecisionLog{Path: logPath}, Host: "host",
	}

	socketPath := filepath.Join(t.TempDir(), "notifyd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := context.WithValue(context.Background(), daemonContextKey{}, "preserved")
	ctx, cancel := context.WithCancel(lifecycle)
	done := make(chan error, 1)
	go func() { done <- (Daemon{Pipeline: pipeline}).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
	})

	dialAndSendFrame(t, socketPath, Frame{HookInput: HookInput{
		Harness: harnessPi, SessionID: "shutdown", CompletionID: "completion",
		CWD: "/work/project", HookEventName: eventTurnComplete,
		Message: "user", LastAssistantMessage: "Deterministic shutdown fallback.",
	}})
	select {
	case value := <-composerEntered:
		if value != "preserved" {
			t.Fatalf("composition context value = %v, want preserved", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("composer did not start")
	}
	cancel()

	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("Serve() error = %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not drain canceled composition")
	}
	select {
	case request := <-requests:
		if request.Body != "Deterministic shutdown fallback." || request.Priority != "4" {
			t.Fatalf("fallback request = %+v", request)
		}
	default:
		t.Fatal("Serve() returned before delivering the accepted event fallback")
	}
	select {
	case extra := <-requests:
		t.Fatalf("extra fallback request = %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case observation := <-observations:
		if observation.value != "preserved" || observation.err != nil || !observation.hasDeadline ||
			observation.remaining < 10*time.Second || observation.remaining > 11*time.Second {
			t.Fatalf("delivery context = %+v, want live bounded context with preserved values", observation)
		}
	default:
		t.Fatal("delivery context was not observed before Serve returned")
	}
	if records := readDecisionLog(t, logPath); len(records) != 1 ||
		records[0].CompositionError != compositionErrorHelperCanceled {
		t.Fatalf("records = %+v, want one canceled-composition fallback", records)
	}
}

func TestDaemonShutdownDrainIsBoundedForUncooperativeComposer(t *testing.T) {
	if got := (Daemon{}).gracefulDrainTimeout(); got != 20*time.Second {
		t.Fatalf("default graceful drain = %s, want 20s", got)
	}
	if got := (Daemon{drainTimeout: 21 * time.Second}).gracefulDrainTimeout(); got != 20*time.Second {
		t.Fatalf("oversized graceful drain = %s, want maximum 20s", got)
	}

	entered := make(chan ComposeInput, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseComposer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseComposer)
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := Pipeline{
		Composer: blockingAttributionComposer{entered: entered, release: release},
		Sender: Sender{
			URL: "https://notify.invalid/test", Client: &http.Client{Transport: successfulTransport{}},
		},
		Log: DecisionLog{Path: logPath}, Host: "host",
	}

	socketPath := filepath.Join(t.TempDir(), "notifyd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	const drainTimeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- (Daemon{Pipeline: pipeline, drainTimeout: drainTimeout}).Serve(ctx, listener)
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
	})

	dialAndSendFrame(t, socketPath, Frame{HookInput: HookInput{
		Harness: harnessPi, SessionID: "uncooperative", CompletionID: "completion",
		CWD: "/work/project", HookEventName: eventTurnComplete,
		LastAssistantMessage: "fallback",
	}})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("composer did not start")
	}
	started := time.Now()
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("Serve() error = %v", serveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() waited indefinitely for uncooperative composer")
	}
	if elapsed := time.Since(started); elapsed < drainTimeout/2 || elapsed > time.Second {
		t.Fatalf("Serve() drain elapsed = %s, want bounded wait near %s", elapsed, drainTimeout)
	}

	releaseComposer()
	waitForDecisionRecords(t, logPath, 1)
}

type deadlineRecordingConn struct {
	net.Conn

	deadlines chan<- time.Time
}

func (connection deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	connection.deadlines <- deadline
	return connection.Conn.SetReadDeadline(deadline)
}

func TestDaemonAcceptedConnectionKeepsFiveSecondReadBound(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	deadlines := make(chan time.Time, 1)
	done := make(chan struct{})
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	daemon := Daemon{Pipeline: Pipeline{Log: DecisionLog{Path: logPath}}}
	started := time.Now()
	go func() {
		defer close(done)
		daemon.handleConn(context.Background(), deadlineRecordingConn{Conn: server, deadlines: deadlines})
	}()
	select {
	case deadline := <-deadlines:
		if delay := deadline.Sub(started); delay < 4900*time.Millisecond || delay > 5100*time.Millisecond {
			t.Fatalf("read deadline delay = %s, want 5s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("connection read deadline was not set")
	}
	if err := EncodeFrame(client, Frame{HookInput: HookInput{
		Harness: harnessClaude, SessionID: "bounded-read", HookEventName: eventSessionEnd,
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not stop after its frame")
	}
}

type failingListener struct {
	closeCalls atomic.Int32
}

func (listener *failingListener) Accept() (net.Conn, error) {
	return nil, fmt.Errorf("sentinel listener failure")
}

func (listener *failingListener) Close() error {
	listener.closeCalls.Add(1)
	return nil
}

func (*failingListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "failing-listener", Net: "unix"}
}

func TestDaemonListenerErrorStopsCancellationWatcher(t *testing.T) {
	listener := &failingListener{}
	ctx, cancel := context.WithCancel(context.Background())
	err := (Daemon{}).Serve(ctx, listener)
	if err == nil || !strings.Contains(err.Error(), "sentinel listener failure") {
		t.Fatalf("Serve() error = %v", err)
	}
	callsAtReturn := listener.closeCalls.Load()
	cancel()
	time.Sleep(50 * time.Millisecond)
	if calls := listener.closeCalls.Load(); calls != callsAtReturn {
		t.Fatalf("listener Close calls after Serve returned = %d, was %d; watcher leaked", calls, callsAtReturn)
	}
}

func TestDaemonConcurrentCompletionsRetainOriginalAttribution(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	entered := make(chan ComposeInput, 2)
	release := make(chan struct{})
	pipeline := Pipeline{
		Composer: blockingAttributionComposer{entered: entered, release: release},
		Sender:   Sender{URL: server.URL, Client: server.Client()},
		Log:      DecisionLog{Path: logPath}, Host: "host",
	}
	socketPath := startTestDaemon(t, pipeline)

	frames := []Frame{
		{
			HookInput: HookInput{
				Harness: harnessCodex, SessionID: "session-a", CompletionID: "completion-a",
				CWD: "/work/project-a", HookEventName: eventTurnComplete,
				Message: "user-a", LastAssistantMessage: "assistant-a",
			},
			Workspace: "workspace-a:1",
		},
		{
			HookInput: HookInput{
				Harness: harnessPi, SessionID: "session-b", CompletionID: "completion-b",
				CWD: "/work/project-b", HookEventName: eventTurnComplete,
				Message: "user-b", LastAssistantMessage: "assistant-b",
			},
			Workspace: "workspace-b:2",
		},
	}
	for _, frame := range frames {
		dialAndSendFrame(t, socketPath, frame)
	}

	seenInputs := make(map[string]string)
	for range 2 {
		select {
		case input := <-entered:
			seenInputs[input.User] = input.Assistant
		case <-time.After(2 * time.Second):
			t.Fatal("completions did not compose concurrently")
		}
	}
	if seenInputs["user-a"] != "assistant-a" || seenInputs["user-b"] != "assistant-b" {
		t.Fatalf("Compose inputs = %+v, want original pairs", seenInputs)
	}
	close(release)

	notifications := make(map[string]capturedNotification)
	for range 2 {
		request := waitNotification(t, requests)
		notifications[request.Body] = request
	}
	if got := notifications["summary for assistant-a"].Title; got != "project-a · workspace-a:1" {
		t.Errorf("session A title = %q", got)
	}
	if got := notifications["summary for assistant-b"].Title; got != "project-b · workspace-b:2" {
		t.Errorf("session B title = %q", got)
	}

	records := waitForDecisionRecords(t, logPath, 2)
	byID := make(map[string]DecisionRecord)
	for _, record := range records {
		byID[record.CompletionID] = record
	}
	if got := byID["completion-a"]; got.SessionID != "session-a" || got.Harness != harnessCodex ||
		got.Title != "project-a · workspace-a:1" || got.Body != "summary for assistant-a" {
		t.Errorf("session A record = %+v", got)
	}
	if got := byID["completion-b"]; got.SessionID != "session-b" || got.Harness != harnessPi ||
		got.Title != "project-b · workspace-b:2" || got.Body != "summary for assistant-b" {
		t.Errorf("session B record = %+v", got)
	}
}

func TestDaemonSlowCompositionDoesNotDelayExplicitInput(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	entered := make(chan ComposeInput, 1)
	release := make(chan struct{})
	pipeline := Pipeline{
		Composer: blockingAttributionComposer{entered: entered, release: release},
		Sender:   Sender{URL: server.URL, Client: server.Client()},
		Log:      DecisionLog{Path: logPath}, Host: "host",
	}
	socketPath := startTestDaemon(t, pipeline)
	dialAndSendFrame(t, socketPath, Frame{
		HookInput: HookInput{
			Harness: harnessPi, SessionID: "slow", CompletionID: "slow-id",
			CWD: "/work/slow", HookEventName: eventTurnComplete,
			LastAssistantMessage: "slow assistant",
		},
		Workspace: "slow:1",
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow completion never entered composer")
	}

	dialAndSendFrame(t, socketPath, Frame{HookInput: HookInput{
		Harness: harnessClaude, SessionID: "input", CWD: "/work/fast",
		HookEventName: eventNotification, NotificationType: "permission_prompt", Message: "allow now?",
	}, Workspace: "fast:2"})
	request := waitNotification(t, requests)
	if request.Body != "allow now?" || request.Priority != "5" || request.Title != "fast · fast:2" {
		t.Fatalf("first request = %+v, want immediate blocked input", request)
	}
	close(release)
	if completionRequest := waitNotification(t, requests); completionRequest.Body != "summary for slow assistant" {
		t.Fatalf("second request = %+v, want released completion", completionRequest)
	}
	waitForDecisionRecords(t, logPath, 2)
}

func TestDaemonClientDryRunSkipsComposerAndDelivery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := Pipeline{
		Composer: panicComposer{}, Sender: Sender{URL: server.URL, Client: server.Client()},
		Log: DecisionLog{Path: logPath}, Stdout: new(strings.Builder), Host: "host",
	}
	socketPath := startTestDaemon(t, pipeline)
	dialAndSendFrame(t, socketPath, Frame{
		HookInput: HookInput{
			Harness: harnessPi, SessionID: "dry", CompletionID: "completion",
			CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "dry fallback",
		},
		Workspace: "dry:1", DryRun: true,
	})
	record := waitForDecisionRecords(t, logPath, 1)[0]
	if requests.Load() != 0 {
		t.Fatalf("delivered %d requests during dry run", requests.Load())
	}
	if record.CompositionError != compositionErrorDryRun {
		t.Errorf("record = %+v, want dry-run composition skip", record)
	}
}

func TestDaemonMalformedFrameDoesNotStopService(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	socketPath := startTestDaemon(t, Pipeline{
		DryRun: true,
		Log:    DecisionLog{Path: logPath},
		Stdout: new(strings.Builder),
	})
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = connection.Write([]byte("not json{"))
	_ = connection.Close()
	dialAndSendFrame(t, socketPath, Frame{HookInput: HookInput{
		Harness: harnessClaude, SessionID: "after-malformed", HookEventName: eventSessionEnd,
	}})
	if record := waitForDecisionRecords(t, logPath, 1)[0]; record.SessionID != "after-malformed" {
		t.Fatalf("record = %+v", record)
	}
}

const listenHelperEnv = "CC_TOOLS_NOTIFY_LISTEN_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(listenHelperEnv) == "1" {
		runListenHelper()
		return
	}
	os.Exit(m.Run())
}

func runListenHelper() {
	listener, err := Listen(filepath.Join(os.TempDir(), "unused.sock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("LISTENING")
	connection, err := listener.Accept()
	if err != nil {
		fmt.Fprintf(os.Stderr, "accept error: %v\n", err)
		os.Exit(1)
	}
	_ = connection.Close()
	fmt.Println("ACCEPTED")
	os.Exit(0)
}

func TestListenSelfBindCreatesPrivateSocket(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	socketPath := filepath.Join(t.TempDir(), "notifyd", "notifyd.sock")
	listener, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	for path, want := range map[string]os.FileMode{
		filepath.Dir(socketPath): 0o700,
		socketPath:               0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestListenSelfBindRejectsUnsafeDirectories(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		realDirectory := filepath.Join(root, "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(realDirectory, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(filepath.Join(link, "notifyd.sock")); err == nil {
			t.Fatal("Listen() accepted symlink directory")
		}
	})
	t.Run("world writable", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "unsafe")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(filepath.Join(directory, "notifyd.sock")); err == nil {
			t.Fatal("Listen() accepted world-writable directory")
		}
	})
}

func TestListenSelfBindRemovesStaleSocket(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	directory := filepath.Join(t.TempDir(), "notifyd")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(directory, "notifyd.sock")
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
}

func TestListenSystemdSocketActivation(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "systemd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener = %T", listener)
	}
	file, err := unixListener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), listenHelperEnv+"=1", "LISTEN_FDS=1")
	command.ExtraFiles = []*os.File{file}
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if startErr := command.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	var dialErr error
	for range 50 {
		var connection net.Conn
		connection, dialErr = net.Dial("unix", socketPath)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dialErr != nil {
		_ = command.Process.Kill()
		t.Fatalf("dialing inherited socket: %v", dialErr)
	}
	if waitErr := command.Wait(); waitErr != nil {
		t.Fatalf("helper failed: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "LISTENING") || !strings.Contains(stdout.String(), "ACCEPTED") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestSocketPathResolution(t *testing.T) {
	t.Run("XDG runtime", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
		want := filepath.Join("/run/user/1000", "cc-tools", "notifyd.sock")
		if got := SocketPath(); got != want {
			t.Errorf("SocketPath() = %q, want %q", got, want)
		}
	})
	t.Run("tmp fallback", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		want := filepath.Join("/tmp", "cc-tools-"+strconv.Itoa(os.Getuid()), "notifyd.sock")
		if got := SocketPath(); got != want {
			t.Errorf("SocketPath() = %q, want %q", got, want)
		}
	})
}

func TestDaemonConcurrentDecisionLogWritesRemainIntact(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := Pipeline{DryRun: true, Log: DecisionLog{Path: logPath}, Stdout: new(strings.Builder)}
	socketPath := startTestDaemon(t, pipeline)
	const count = 20
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			dialAndSendFrame(t, socketPath, Frame{HookInput: HookInput{
				Harness: harnessClaude, SessionID: fmt.Sprintf("session-%d", index), HookEventName: eventSessionEnd,
			}})
		}(index)
	}
	wait.Wait()
	records := waitForDecisionRecords(t, logPath, count)
	if len(records) != count {
		t.Fatalf("records = %d, want %d", len(records), count)
	}
}
