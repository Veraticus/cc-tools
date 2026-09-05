package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Veraticus/cc-tools/internal/notify"
)

func testNotifyClientConfig(t *testing.T, socketPath string) notifyClientConfig {
	t.Helper()
	return notifyClientConfig{
		DryRun:   true,
		Log:      notify.DecisionLog{Path: filepath.Join(t.TempDir(), "decisions.jsonl")},
		SockPath: socketPath, DialTimeout: 100 * time.Millisecond,
	}
}

func writeMarkerHelper(t *testing.T, name string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "invoked")
	script := "#!/bin/sh\ntouch \"$MARKER\"\nexit 1\n"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	return path, marker
}

func TestDispatchNotifyReachableSocketWritesMinimalFrameAndSkipsFallback(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notifyd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	frames := make(chan notify.Frame, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		frame, decodeErr := notify.DecodeFrame(connection)
		if decodeErr == nil {
			frames <- frame
		}
	}()
	inlineRequests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Errorf("reading inline request: %v", readErr)
		}
		inlineRequests <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := testNotifyClientConfig(t, socketPath)
	cfg.DryRun = false
	cfg.Sender = notify.Sender{URL: server.URL, Client: server.Client()}
	cfg.Environ = []string{"TMUX_PANE=", "SECRET_DO_NOT_SEND=value"}
	var stdout, stderr bytes.Buffer
	dispatchNotify(
		context.Background(), cfg,
		strings.NewReader(
			`{"schema_version":1,"harness":"pi","session_id":"session-1",`+
				`"completion_id":"completion-1","cwd":"/work/project",`+
				`"hook_event_name":"TurnComplete","last_assistant_message":"Inline must not run."}`,
		),
		&stdout, &stderr,
	)
	select {
	case frame := <-frames:
		input := frame.HookInput
		if input.Harness != "pi" || input.SessionID != "session-1" ||
			input.CompletionID != "completion-1" || input.HookEventName != "TurnComplete" ||
			frame.Workspace != "" || frame.DryRun {
			t.Errorf("frame = %+v", frame)
		}
		wire, marshalErr := json.Marshal(frame)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "environ") ||
			strings.Contains(string(wire), "parent_pid") {
			t.Fatalf("frame leaked caller context: %s", wire)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not receive frame")
	}
	select {
	case body := <-inlineRequests:
		t.Fatalf("reachable daemon also ran inline delivery with body %q", body)
	default:
	}
	if _, statErr := os.Stat(cfg.Log.Path); !os.IsNotExist(statErr) {
		t.Fatalf("reachable daemon wrote inline decision log: %v", statErr)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunNotifyCommandHarnessFlagReachesNormalizedCodexFrame(t *testing.T) {
	runtimeDirectory := t.TempDir()
	socketPath := filepath.Join(runtimeDirectory, "cc-tools", "notifyd.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	frames := make(chan notify.Frame, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		frame, decodeErr := notify.DecodeFrame(connection)
		if decodeErr == nil {
			frames <- frame
		}
	}()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("CC_TOOLS_NTFY_URL", "http://unused.invalid/topic")
	payload := `{"session_id":"thread","turn_id":"turn","hook_event_name":"Stop","cwd":"/tmp"}`
	var stdout, stderr bytes.Buffer
	if code := runNotifyCommandWithIO(
		[]string{"--harness=codex", "--state-base=" + t.TempDir()},
		strings.NewReader(payload), &stdout, &stderr,
	); code != 0 {
		t.Fatalf("exit code = %d stderr=%q", code, stderr.String())
	}
	select {
	case frame := <-frames:
		input := frame.HookInput
		if input.Harness != "codex" || input.HookEventName != "TurnComplete" ||
			input.SessionID != "thread" || input.CompletionID != "turn" {
			t.Fatalf("frame = %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no normalized frame")
	}
}

func TestDispatchNotifyInlineOutageAndDryRunNeverInvokePiHelper(t *testing.T) {
	helper, marker := writeMarkerHelper(t, "steward-pi-helper")
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "outage", true: "dry run"}[dryRun], func(t *testing.T) {
			requests := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("reading fallback request: %v", err)
				}
				requests <- string(body)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			cfg := testNotifyClientConfig(t, filepath.Join(t.TempDir(), "missing.sock"))
			cfg.DryRun = dryRun
			cfg.Sender = notify.Sender{URL: server.URL, Client: server.Client()}
			cfg.Environ = []string{"STEWARD_HELPER_BIN=" + helper}
			payload := `{"schema_version":1,"harness":"pi","session_id":"pi-session",` +
				`"completion_id":"completion","cwd":"/work/project","hook_event_name":"TurnComplete",` +
				`"message":"user","last_assistant_message":"Inline fallback."}`
			var stdout, stderr bytes.Buffer
			dispatchNotify(context.Background(), cfg, strings.NewReader(payload), &stdout, &stderr)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("helper marker exists (err=%v): inline path invoked inference", err)
			}
			if dryRun {
				if !strings.Contains(stdout.String(), "Inline fallback.") {
					t.Errorf("stdout = %q", stdout.String())
				}
				select {
				case body := <-requests:
					t.Fatalf("dry run made HTTP request with body %q", body)
				default:
				}
			} else {
				select {
				case body := <-requests:
					if body != "Inline fallback." {
						t.Fatalf("fallback body = %q", body)
					}
				default:
					t.Fatal("socket outage did not send inline fallback")
				}
				select {
				case body := <-requests:
					t.Fatalf("socket outage sent duplicate fallback body %q", body)
				default:
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestNewNotifydPipelineUsesCentralPiConfigurationInRealCommand(t *testing.T) {
	directory := t.TempDir()
	dumpPath := filepath.Join(directory, "request.json")
	helperPath := filepath.Join(directory, "helper")
	script := `#!/bin/sh
cat >"$DUMP_PATH"
printf '%s\n' '{"version":1,"ok":true,"body":"Central helper summary."}'
`
	if err := os.WriteFile(helperPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUMP_PATH", dumpPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := newNotifydPipeline(
		false,
		notify.Sender{URL: server.URL, Client: server.Client()},
		notify.DecisionLog{Path: logPath},
		[]string{
			"STEWARD_HELPER_BIN=" + helperPath,
			"STEWARD_MODEL_PROVIDER=provider-central",
			"STEWARD_MODEL_ID=model-central",
			"STEWARD_MODEL_THINKING=xhigh",
		},
	)
	composer, ok := pipeline.Composer.(notify.PiComposer)
	if !ok {
		t.Fatalf("Composer = %T, want notify.PiComposer", pipeline.Composer)
	}
	if composer.Bin != helperPath || composer.Model != (notify.ComposeModel{
		Provider: "provider-central", ID: "model-central", Thinking: "xhigh",
	}) {
		t.Fatalf("composer = %+v", composer)
	}
	if err := pipeline.Run(context.Background(), notify.HookInput{
		Harness: "pi", SessionID: "session", CompletionID: "completion",
		CWD: "/work/project", HookEventName: "TurnComplete",
		Message: "central user", LastAssistantMessage: "central assistant",
	}); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Operation string              `json:"operation"`
		Model     notify.ComposeModel `json:"model"`
		Input     notify.ComposeInput `json:"input"`
		Label     notify.ComposeLabel `json:"label"`
	}
	if unmarshalErr := json.Unmarshal(wire, &request); unmarshalErr != nil {
		t.Fatalf("request = %q: %v", wire, unmarshalErr)
	}
	if request.Operation != "compose" || request.Model != composer.Model ||
		request.Input != (notify.ComposeInput{User: "central user", Assistant: "central assistant"}) ||
		request.Label != (notify.ComposeLabel{}) {
		t.Errorf("request = %+v", request)
	}
}

func TestNewNotifydPipelineBadExplicitConfigKeepsFallbackService(t *testing.T) {
	serverRequests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		serverRequests <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := newNotifydPipeline(
		false,
		notify.Sender{URL: server.URL, Client: server.Client()},
		notify.DecisionLog{Path: logPath},
		[]string{"STEWARD_MODEL_ID=", "SECRET_CONFIG=must-not-leak"},
	)
	if pipeline.Composer != nil || pipeline.CompositionError == nil {
		t.Fatalf(
			"pipeline composer/error = %T/%v, want safely disabled composer",
			pipeline.Composer,
			pipeline.CompositionError,
		)
	}
	if err := pipeline.Run(context.Background(), notify.HookInput{
		Harness: "pi", SessionID: "session", CompletionID: "completion",
		CWD: "/work/project", HookEventName: "TurnComplete",
		LastAssistantMessage: "Configuration fallback.",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-serverRequests:
		if body != "Configuration fallback." {
			t.Errorf("body = %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bad config disabled fallback delivery")
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "invalid configuration") || strings.Contains(string(logData), "SECRET") {
		t.Errorf("decision log = %s, want safe observable config category", logData)
	}
}

func TestNotifydRequiresSender(t *testing.T) {
	for _, test := range []struct {
		senderOK bool
		dryRun   bool
		want     bool
	}{
		{senderOK: false, dryRun: false, want: true},
		{senderOK: false, dryRun: true, want: false},
		{senderOK: true, dryRun: false, want: false},
		{senderOK: true, dryRun: true, want: false},
	} {
		if got := notifydRequiresSender(test.senderOK, test.dryRun); got != test.want {
			t.Errorf("notifydRequiresSender(%v, %v) = %v", test.senderOK, test.dryRun, got)
		}
	}
}

func TestDefaultNotifyStateBase(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got := defaultNotifyStateBase(); got != filepath.Join("/state", "cc-tools", "notify") {
		t.Errorf("defaultNotifyStateBase() = %q", got)
	}
}
