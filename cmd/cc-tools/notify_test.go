package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
func writeMarkerScript(t *testing.T, dir, markerPath string) {
	t.Helper()
	script := "#!/bin/sh\ntouch " + markerPath + "\nexit 0\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing marker script: %v", err)
	}
}

func testNotifyClientConfig(t *testing.T, sockPath string) notifyClientConfig {
	t.Helper()
	stateBase := t.TempDir()
	return notifyClientConfig{
		StateBase:   stateBase,
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
	stdin := strings.NewReader(`{"session_id":"sess-1","hook_event_name":"SessionEnd"}`)
	var stdout, stderr bytes.Buffer

	dispatchNotify(context.Background(), cfg, stdin, &stdout, &stderr)

	select {
	case f := <-frameCh:
		if f.HookInput.SessionID != "sess-1" || f.HookInput.HookEventName != "SessionEnd" {
			t.Errorf("received frame HookInput = %+v, want session-end for sess-1", f.HookInput)
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

func TestDispatchNotify_UnreachableSocket_JudgeRoute_NeverInvokesRealJudge(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon-here.sock")
	cfg := testNotifyClientConfig(t, sockPath)

	// Regression guard: if the fallback's disabled Judge Bin were ever
	// changed from an absolute nonexistent path to a PATH-relative name
	// (e.g. "claude"), this marker script — first on PATH — would prove it
	// by getting invoked.
	markerDir := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "invoked.marker")
	writeMarkerScript(t, markerDir, markerPath)
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

func TestDispatchNotify_ReachableSocket_ReturnsFastEvenWithSlowDaemonJudge(t *testing.T) {
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	slowJudgeBin := writeClientStubClaude(t)
	t.Setenv("STUB_SLEEP", "1")
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"done","task":"t","body":"finished","reason":"r"}`)

	d := notify.Daemon{
		Pipeline: notify.Pipeline{
			StateBase: stateBase,
			DryRun:    true,
			Judge:     notify.Judge{Bin: slowJudgeBin, Model: "claude-haiku-4-5"},
			Log:       notify.DecisionLog{Path: logPath},
			Stdout:    os.Stdout,
			Host:      "testhost",
			Present:   func(_ []string, _ time.Time) bool { return false },
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
