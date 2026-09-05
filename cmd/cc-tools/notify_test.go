package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Veraticus/cc-tools/internal/notify"
)

// clientStubScript is a minimal /bin/sh program standing in for the claude
// binary in the daemon-side tests below (the client's own fallback Judge
// never execs anything real — see TestDispatchNotify_UnreachableSocket_
// JudgeRoute_NeverInvokesRealJudge). It answers per env vars: STUB_SLEEP to
// simulate a slow judge call, STUB_STDOUT for the verdict JSON.
const clientStubScript = `#!/bin/sh
cat >/dev/null
if [ -n "$STUB_SLEEP" ]; then
  sleep "$STUB_SLEEP"
fi
printf '%s' "$STUB_STDOUT"
exit 0
`

func writeClientStubClaude(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte(clientStubScript), 0o755); err != nil {
		t.Fatalf("writing stub claude: %v", err)
	}
	return path
}

// writeMarkerScript writes a /bin/sh program that, if ever executed,
// creates markerPath — used to prove a code path that must never invoke a
// real judge binary in fact never does, even if some future regression
// changed the disabled judge's Bin to a PATH-relative name.
func writeMarkerScript(t *testing.T, dir, name, markerPath string) {
	t.Helper()
	script := "#!/bin/sh\ntouch " + markerPath + "\nexit 0\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing marker script: %v", err)
	}
}

func testNotifyClientConfig(t *testing.T, sockPath string) notifyClientConfig {
	t.Helper()
	stateBase := t.TempDir()
	return notifyClientConfig{
		DryRun:      true,
		Sender:      notify.Sender{},
		Log:         notify.DecisionLog{Path: filepath.Join(stateBase, "notify-decisions.jsonl")},
		Environ:     nil,
		SelfBin:     "cc-tools",
		SockPath:    sockPath,
		DialTimeout: 250 * time.Millisecond,
	}
}

func TestDispatchNotify_ReachableSocket_WritesFrameAndSkipsFallback(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	frameCh := make(chan notify.Frame, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		f, decodeErr := notify.DecodeFrame(conn)
		if decodeErr != nil {
			return
		}
		frameCh <- f
	}()

	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false
	stdin := strings.NewReader(`{"session_id":"sess-1","hook_event_name":"SessionEnd"}`)
	var stdout, stderr bytes.Buffer

	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)

	select {
	case f := <-frameCh:
		if f.HookInput.SessionID != "sess-1" || f.HookInput.HookEventName != "SessionEnd" {
			t.Errorf("received frame HookInput = %+v, want session-end for sess-1", f.HookInput)
		}
		if f.DryRun {
			t.Error("received frame DryRun = true, want normal client frame")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon never received a frame")
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty — reachable socket must never run the inline fallback", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunNotifyCommandHarnessFlagReachesNormalizedCodexFrame(t *testing.T) {
	runtimeDir := t.TempDir()
	sockPath := filepath.Join(runtimeDir, "cc-tools", "notifyd.sock")
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	frames := make(chan notify.Frame, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		frame, decodeErr := notify.DecodeFrame(conn)
		if decodeErr == nil {
			frames <- frame
		}
	}()

	payload := `{"session_id":"thread-flag","turn_id":"turn-flag",` +
		`"hook_event_name":"Stop","cwd":"/tmp"}`
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("CC_TOOLS_NTFY_URL", "http://unused.invalid/topic")
	t.Setenv("CC_TOOLS_NTFY_DISABLED", "false")
	var stdout, stderr bytes.Buffer
	exitCode := runNotifyCommandWithIO(
		[]string{"--harness=codex", "--state-base=" + t.TempDir()},
		strings.NewReader(payload),
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}

	select {
	case frame := <-frames:
		in := frame.HookInput
		if in.Harness != "codex" || in.HookEventName != "TurnComplete" ||
			in.CompletionID != "turn-flag" || in.SessionID != "thread-flag" {
			t.Fatalf("frame = %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runNotifyCommand did not send a frame")
	}
}

func TestDispatchNotify_ExplicitCodexStopWritesNormalizedFrame(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	frames := make(chan notify.Frame, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		frame, decodeErr := notify.DecodeFrame(conn)
		if decodeErr == nil {
			frames <- frame
		}
	}()
	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false
	cfg.Harness = "codex"
	var stdout, stderr bytes.Buffer
	dispatchNotify(
		context.Background(),
		cfg,
		strings.NewReader(
			`{"session_id":"thread-1","turn_id":"turn-1","hook_event_name":"Stop","cwd":"/tmp"}`,
		),
		&stdout,
		&stderr,
	)
	select {
	case frame := <-frames:
		if frame.HookInput.Harness != "codex" || frame.HookInput.HookEventName != "TurnComplete" ||
			frame.HookInput.CompletionID != "turn-1" {
			t.Fatalf("frame = %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive normalized Codex frame")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDispatchNotify_CanonicalHarnessMismatchDoesNotCallDaemon(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	called := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			called <- struct{}{}
			_ = conn.Close()
		}
	}()
	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false
	cfg.Harness = "codex"
	var stdout, stderr bytes.Buffer
	dispatchNotify(
		context.Background(),
		cfg,
		strings.NewReader(
			`{"schema_version":1,"harness":"pi","session_id":"p","completion_id":"c","hook_event_name":"TurnComplete"}`,
		),
		&stdout,
		&stderr,
	)
	select {
	case <-called:
		t.Fatal("mismatched canonical payload called daemon")
	case <-time.After(100 * time.Millisecond):
	}
	if !strings.Contains(stderr.String(), "harness mismatch") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDispatchNotify_UnreachableSocket_OutcomeSend_DeterministicFallback(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	cfg := testNotifyClientConfig(t, sockPath)

	stdin := strings.NewReader(
		`{"session_id":"sess-2","cwd":"/home/user/proj","hook_event_name":"Notification",` +
			`"notification_type":"permission_prompt","message":"May I run rm?"}`,
	)
	var stdout, stderr bytes.Buffer

	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)

	if !strings.Contains(stdout.String(), "May I run rm?") {
		t.Errorf("stdout = %q, want it to contain the deterministic permission-prompt message", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[blocked]") {
		t.Errorf("stdout = %q, want blocked urgency", stdout.String())
	}
}

func TestDispatchNotify_CodexTurnComplete_DeterministicFallback(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false

	requestCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading ntfy body: %v", err)
		}
		requestCh <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg.Sender = notify.Sender{URL: server.URL, Client: server.Client()}

	markerDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "codex-invoked.marker")
	writeMarkerScript(t, markerDir, "codex", markerPath)
	t.Setenv("PATH", markerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	final := strings.Repeat("earlier detail ", 20) + "meaningful tail"

	input := strings.NewReader(
		`{"type":"agent-turn-complete","thread-id":"codex-thread-1","turn-id":"turn-1",` +
			`"cwd":"/home/user/proj","input-messages":["fix it"],` +
			`"last-assistant-message":"` + final + `"}`,
	)
	var stdout, stderr bytes.Buffer

	dispatchNotify(context.Background(), cfg, input, &stdout, &stderr)

	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("Codex marker exists: inline fallback invoked a model")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Codex marker: %v", err)
	}
	var got string
	select {
	case got = <-requestCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ntfy request")
	}
	if len(got) > 160 ||
		!utf8.ValidString(got) ||
		!strings.HasPrefix(got, "…") ||
		!strings.HasSuffix(got, "meaningful tail") {
		t.Errorf("ntfy body = %q (%d bytes), want bounded UTF-8 tail fallback", got, len(got))
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty in a real send", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	logData, err := os.ReadFile(cfg.Log.Path)
	if err != nil {
		t.Fatalf("reading decision log: %v", err)
	}
	if strings.Contains(string(logData), "judge_err") ||
		strings.Contains(string(logData), "codex exec") {
		t.Errorf(
			"decision log = %q, want disabled composition without an evaluator attempt",
			logData,
		)
	}
}

func TestDispatchNotify_CodexTurnComplete_DryRunNeverInvokesCodex(t *testing.T) {
	cfg := testNotifyClientConfig(t, filepath.Join(t.TempDir(), "unused-daemon.sock"))
	markerDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "codex-dry-run.marker")
	writeMarkerScript(t, markerDir, "codex", markerPath)
	t.Setenv("PATH", markerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	input := strings.NewReader(
		`{"type":"agent-turn-complete","thread-id":"codex-dry-run","turn-id":"turn-1",` +
			`"cwd":"/home/user/proj","input-messages":["fix it"],` +
			`"last-assistant-message":"Dry-run fallback only."}`,
	)
	var stdout, stderr bytes.Buffer
	dispatchNotify(context.Background(), cfg, input, &stdout, &stderr)

	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("Codex marker exists: dry-run invoked a model")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Codex marker: %v", err)
	}
	if !strings.Contains(stdout.String(), "Dry-run fallback only.") {
		t.Errorf("stdout = %q, want deterministic dry-run fallback", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestDispatchNotify_UnreachableSocket_GoalActive_NoWatchdogArmAttempt pins
// the client fallback's degraded mode: its inline Pipeline sets no Watchdog,
// so an ArmWatchdog decision (a goal-active Stop, here) must resolve exactly
// as it always has — logged with the same Reason, no panic — with no
// watchdog coverage attempted, since a single hook invocation has no
// long-lived goroutine to arm one on.
func TestDispatchNotify_UnreachableSocket_GoalActive_NoWatchdogArmAttempt(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false

	transcriptPath := filepath.Join(t.TempDir(), "goal_active.jsonl")
	goalActiveLine := `{"parentUuid":"00000000-0000-0000-0000-000000000020","isSidechain":false,` +
		`"type":"attachment","uuid":"00000000-0000-0000-0000-000000000021",` +
		`"timestamp":"2026-05-25T17:45:43.950Z","attachment":{"type":"goal_status","met":false,` +
		`"sentinel":true,"condition":"keep going"},"userType":"external","entrypoint":"cli",` +
		`"cwd":"/home/user/project","sessionId":"anon-session-0002","version":"2.1.150","gitBranch":"main"}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(goalActiveLine), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}

	stdin := strings.NewReader(
		`{"session_id":"sess-goalactive","cwd":"/home/user/project","hook_event_name":"Stop",` +
			`"transcript_path":"` + transcriptPath + `"}`,
	)
	var stdout, stderr bytes.Buffer

	// Must not panic: Pipeline.Watchdog is nil in the fallback.
	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)

	data, err := os.ReadFile(cfg.Log.Path)
	if err != nil {
		t.Fatalf("reading decision log: %v", err)
	}
	logLine := string(data)
	if !strings.Contains(logLine, "goal active") {
		t.Errorf("decision log = %q, want it to mention the goal-active defer", logLine)
	}
	if strings.Contains(logLine, "arm failed") {
		t.Errorf(
			"decision log = %q, want no arm-failed record (arm is a nil-Watchdog no-op, not a failure)", logLine,
		)
	}
}

func TestDispatchNotify_UnreachableSocket_JudgeRoute_NeverInvokesRealJudge(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	cfg := testNotifyClientConfig(t, sockPath)

	// Regression guard: if the fallback's disabled Judge Bin were ever
	// changed from an absolute nonexistent path to a PATH-relative name
	// (e.g. "claude"), this marker script — first on PATH — would prove it
	// by getting invoked.
	markerDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "invoked.marker")
	writeMarkerScript(t, markerDir, "claude", markerPath)
	t.Setenv("PATH", markerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdin := strings.NewReader(
		`{"session_id":"sess-3","cwd":"/home/user/proj","hook_event_name":"Stop",` +
			`"transcript_path":"/nonexistent/transcript.jsonl","last_assistant_message":"All done here."}`,
	)
	var stdout, stderr bytes.Buffer

	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)

	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatal("marker file exists: the fallback invoked a real judge binary")
	}
	if !strings.Contains(stdout.String(), "All done here.") {
		t.Errorf(
			"stdout = %q, want the existing compose-judge-error fallback body (last assistant message tail)",
			stdout.String(),
		)
	}
}

func TestNotifydRequiresSender(t *testing.T) {
	cases := []struct {
		name               string
		senderOK, dryRun   bool
		wantRequiresSender bool
	}{
		{"no sender configured, real run: must fail fast", false, false, true},
		{"no sender configured, dry run: allowed to start", false, true, false},
		{"sender configured, real run: allowed to start", true, false, false},
		{"sender configured, dry run: allowed to start", true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notifydRequiresSender(c.senderOK, c.dryRun); got != c.wantRequiresSender {
				t.Errorf(
					"notifydRequiresSender(senderOK=%v, dryRun=%v) = %v, want %v",
					c.senderOK, c.dryRun, got, c.wantRequiresSender,
				)
			}
		})
	}
}

func TestNewNotifydPipeline_ConfiguresCodexJudge(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		model   string
	}{
		{name: "unset", model: "gpt-5.6-luna"},
		{name: "empty", environ: []string{"CC_TOOLS_CODEX_JUDGE_MODEL="}, model: "gpt-5.6-luna"},
		{
			name:    "override",
			environ: []string{"CC_TOOLS_CODEX_JUDGE_MODEL=gpt-custom"},
			model:   "gpt-custom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newNotifydPipeline(
				false,
				notify.Sender{},
				notify.DecisionLog{},
				"cc-tools",
				tt.environ,
			)
			if p.CodexJudge.Bin != "codex" || p.CodexJudge.Model != tt.model ||
				p.CodexJudge.Timeout != 10*time.Second {
				t.Errorf("CodexJudge = %+v, want codex/%s/10s", p.CodexJudge, tt.model)
			}
			if p.Judge.Bin != "claude" || p.Judge.Model != notifyJudgeModel ||
				p.Judge.Timeout != notifyJudgeTimeout {
				t.Errorf(
					"Claude Judge = %+v, want existing claude/%s/%s",
					p.Judge,
					notifyJudgeModel,
					notifyJudgeTimeout,
				)
			}
		})
	}
}

func TestDispatchNotify_ReachableSocket_ReturnsFastEvenWithSlowDaemonJudge(t *testing.T) {
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	slowJudgeBin := writeClientStubClaude(t)
	t.Setenv("STUB_SLEEP", "1")
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"done","task":"t","body":"finished","reason":"r"}`)

	d := notify.Daemon{
		Pipeline: notify.Pipeline{
			DryRun: true,
			Judge:  notify.Judge{Bin: slowJudgeBin, Model: "claude-haiku-4-5"},
			Log:    notify.DecisionLog{Path: logPath},
			Stdout: os.Stdout,
			Host:   "testhost",
		},
	}
	sockPath := filepath.Join(t.TempDir(), "notifyd.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Serve(ctx, ln) }()

	cfg := testNotifyClientConfig(t, sockPath)
	cfg.DryRun = false
	stdin := strings.NewReader(
		`{"session_id":"sess-4","cwd":"/home/user/proj","hook_event_name":"Stop",` +
			`"transcript_path":"/nonexistent/transcript.jsonl","last_assistant_message":"ship it"}`,
	)
	var stdout, stderr bytes.Buffer

	start := time.Now()
	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)
	elapsed := time.Since(start)

	const budget = 100 * time.Millisecond
	if elapsed >= budget {
		t.Errorf("dispatchNotify() took %s, want under %s even with a 1s daemon-side judge", elapsed, budget)
	}

	// Confirm the daemon actually processed it (proves the slow judge really
	// ran server-side, not that the client silently dropped the frame).
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, readErr := os.ReadFile(logPath)
		if readErr == nil && strings.Contains(string(data), "sess-4") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the daemon to process the frame")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
