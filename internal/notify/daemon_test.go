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
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	d := Daemon{
		Pipeline: Pipeline{
			DryRun:  true,
			Judge:   Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
			Log:     DecisionLog{Path: logPath},
			Stdout:  os.Stdout,
			Host:    "testhost",
			Present: neverPresent,
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

// dialAndSendFrame dials sockPath and writes frame, fire-and-forget —
// mirroring cmd/cc-tools/notify.go's sendFrame, since these daemon-level
// tests exercise Daemon.Serve directly rather than through the client.
func dialAndSendFrame(t *testing.T, sockPath string, frame Frame) {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	if encErr := EncodeFrame(conn, frame); encErr != nil {
		t.Fatalf("EncodeFrame() error = %v", encErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("conn.Close() error = %v", closeErr)
	}
}

// TestDaemon_Loop_SlowJudgeForOneSessionNeverDelaysAnother is the epic's
// core event-loop guarantee: the loop goroutine must never block on a
// judge call. Session A's Stop event routes through the compose judge,
// stubbed to sleep 2s; session B's Notification (permission_prompt) is a
// deterministic send that never touches the judge at all. B's decision
// record must land almost immediately, proving the loop dispatched A's
// judge call off itself instead of processing frames serially.
func TestDaemon_Loop_SlowJudgeForOneSessionNeverDelaysAnother(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	slowJudgeBin := writeStubClaude(t)
	t.Setenv("STUB_SLEEP", "2")
	t.Setenv("STUB_STDOUT", `{"notify":true,"urgency":"done","task":"t","body":"finished","reason":"r"}`)

	d := Daemon{
		Pipeline: Pipeline{
			DryRun:  true,
			Judge:   Judge{Bin: slowJudgeBin, Model: "claude-haiku-4-5"},
			Log:     DecisionLog{Path: logPath},
			Stdout:  os.Stdout,
			Host:    "testhost",
			Present: neverPresent,
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

	transcript := copyFixture(t, "goal_none.jsonl")
	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: "sess-slow-judge", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}})

	start := time.Now()
	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: "sess-fast-send", CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}})

	const budget = 500 * time.Millisecond
	deadline := time.Now().Add(budget)
	var fastRecorded bool
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(logPath); readErr == nil && strings.Contains(string(data), "sess-fast-send") {
			fastRecorded = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if !fastRecorded {
		t.Fatalf("sess-fast-send record did not land within %s (loop appears blocked on the slow judge)", budget)
	}
	if elapsed >= budget {
		t.Errorf(
			"sess-fast-send record took %s, want under %s even with a 2s judge in flight for another session",
			elapsed, budget,
		)
	}

	// Wait for the slow session to finish too, so the test doesn't tear
	// down the daemon (and kill the judge subprocess) mid-flight.
	waitForDecisionRecords(t, logPath, 2, 3*time.Second)
}

// TestDaemon_Loop_IdlePromptDedupe_NeverTouchesFiles proves the daemon's
// dedupe now lives entirely in memory: a deterministic send for a session
// followed by an idle_prompt for the same session inside dedupeWindow must
// resolve silent with a dedupe reason, and the session's on-disk state
// directory (this test's own stateBase temp dir) must never have been
// created — the old file-based dedupe would have created it via
// SessionState.MarkNotified.
func TestDaemon_Loop_IdlePromptDedupe_NeverTouchesFiles(t *testing.T) {
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	var sent []capturedRequest

	d := Daemon{
		Pipeline: Pipeline{
			DryRun:  false,
			Judge:   Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
			Sender:  stubSenderRecording(&sent),
			Log:     DecisionLog{Path: logPath},
			Stdout:  os.Stdout,
			Host:    "testhost",
			Present: neverPresent,
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

	const sessionID = "sess-idle-dedupe"
	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}})
	waitForDecisionRecords(t, logPath, 1, 2*time.Second)

	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "idle_prompt",
	}})
	recs := waitForDecisionRecords(t, logPath, 2, 2*time.Second)

	if recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("first record Outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() || !strings.Contains(recs[1].Reason, "dedupe") {
		t.Fatalf("second record = %+v, want a silent dedupe record", recs[1])
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one delivered notification (the idle_prompt was deduped)", sent)
	}

	if _, statErr := os.Stat(filepath.Join(stateBase, sessionID)); !os.IsNotExist(statErr) {
		t.Errorf(
			"session state dir exists (err = %v), want it never created — dedupe must come entirely from memory",
			statErr,
		)
	}
}

// TestDaemon_Loop_SessionEnd_EvictsDedupeState proves SessionEnd deletes a
// session's dedupe record from the loop's MemoryState (see
// MemoryState.DeleteSession): without eviction, every session the daemon
// ever handles would leak a MemoryState entry for its entire uptime, since
// each session_id is a fresh UUID. An identical permission_prompt sent
// again after a SessionEnd frame must resolve as a fresh send, not the
// suppressBlockedRepeat dedupe (decide.go) that an identical repeat within
// blockedRepeatWindow would otherwise trigger — see
// TestDaemon_Loop_IdlePromptDedupe_NeverTouchesFiles for that same dedupe
// path with no SessionEnd in between.
func TestDaemon_Loop_SessionEnd_EvictsDedupeState(t *testing.T) {
	stateBase := t.TempDir()
	logPath := filepath.Join(stateBase, "notify-decisions.jsonl")
	var sent []capturedRequest

	d := Daemon{
		Pipeline: Pipeline{
			DryRun:  false,
			Judge:   Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
			Sender:  stubSenderRecording(&sent),
			Log:     DecisionLog{Path: logPath},
			Stdout:  os.Stdout,
			Host:    "testhost",
			Present: neverPresent,
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

	const sessionID = "sess-end-evicts"
	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}})
	waitForDecisionRecords(t, logPath, 1, 2*time.Second)

	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{SessionID: sessionID, HookEventName: "SessionEnd"}})
	waitForDecisionRecords(t, logPath, 2, 2*time.Second)

	dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}})
	recs := waitForDecisionRecords(t, logPath, 3, 2*time.Second)

	if recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("record[0] Outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() || recs[1].Reason != "session end" {
		t.Fatalf("record[1] = %+v, want the session-end silent record", recs[1])
	}
	if recs[2].Outcome != OutcomeSend.String() {
		t.Fatalf("record[2] = %+v, want send (SessionEnd must have evicted the dedupe record)", recs[2])
	}
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want exactly two delivered notifications (SessionEnd evicts dedupe state)", sent)
	}
}

// TestDaemon_Loop_SameSessionFramesProcessInArrivalOrder sends three
// identical frames for the same session, each held back until the prior
// one's decision record has landed, and confirms every repeat after the
// first correctly dedupes. This is "arrival order" in the sense the loop
// actually guarantees it: frames that arrive after a prior one has fully
// completed see that prior write, with no torn or lost state access along
// the way. It deliberately does not fire the frames concurrently — two
// truly simultaneous frames for the same session can both read "not yet
// marked" before either writes and both send, exactly as the old
// file-based, one-goroutine-per-connection design already could (see
// Daemon.loop's doc comment); that race is unchanged by this task, not
// fixed by it. Run with -race, this also catches any concurrent,
// unsynchronized map access in MemoryState.
func TestDaemon_Loop_SameSessionFramesProcessInArrivalOrder(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	var sent []capturedRequest

	d := Daemon{
		Pipeline: Pipeline{
			DryRun:  false,
			Judge:   Judge{Bin: writeStubClaude(t), Model: "claude-haiku-4-5"},
			Sender:  stubSenderRecording(&sent),
			Log:     DecisionLog{Path: logPath},
			Stdout:  os.Stdout,
			Host:    "testhost",
			Present: neverPresent,
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

	const sessionID = "sess-arrival-order"
	for i := 1; i <= 3; i++ {
		dialAndSendFrame(t, sockPath, Frame{HookInput: HookInput{
			SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
			HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
		}})
		waitForDecisionRecords(t, logPath, i, 2*time.Second)
	}

	recs := readDecisionLog(t, logPath)
	if recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("first record Outcome = %q, want send", recs[0].Outcome)
	}
	for i, rec := range recs[1:] {
		if rec.Outcome != OutcomeSilent.String() || !strings.Contains(rec.Reason, "dedupe: identical ping") {
			t.Errorf("record[%d] = %+v, want a silent identical-ping dedupe record", i+1, rec)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one delivered notification (identical repeats deduped)", sent)
	}
}

// canceledWaitTimeout bounds waitForCanceledCount's poll.
const canceledWaitTimeout = 2 * time.Second

// waitForCanceledCount polls path until it holds at least want decision
// records with Event "watchdog", Outcome "canceled", and the given
// SessionID, or canceledWaitTimeout elapses. Counting (not just "at least
// one") matters for a session that gets more than one "canceled" record in a
// single test (e.g. supersession followed by an explicit Reap of the
// survivor).
func waitForCanceledCount(t *testing.T, path, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(canceledWaitTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			count := 0
			for _, rec := range readDecisionLog(t, path) {
				if rec.Event == "watchdog" && rec.SessionID == sessionID && rec.Outcome == "canceled" {
					count++
				}
			}
			if count >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d watchdog/canceled record(s) for %s at %s", want, sessionID, path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDaemonWatchdog_Arm_SupersedesPriorWatchdog proves Arm cancels a prior
// watchdog for the same session rather than letting both run: arming twice
// for the same SessionID must leave exactly one live watchdog, with the
// first goroutine observed exiting "canceled". Neither watchdog's Sleep
// call is ever reached with a real 5-minute wait — RunWatchdog's ctx-aware
// Sleep unblocks on cancellation immediately, regardless of the timer
// duration, so this test runs against the real clock without waiting. The
// surviving (second) watchdog is explicitly reaped before the test returns
// so no goroutine is still touching the transcript/log files under this
// test's TempDir when it gets cleaned up.
func TestDaemonWatchdog_Arm_SupersedesPriorWatchdog(t *testing.T) {
	ch := make(chan loopMsg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := Daemon{}
	go d.loop(ctx, ch)

	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	w := daemonWatchdog{
		ch: ch, done: ctx.Done(),
		judge: Judge{Bin: "/nonexistent"}, sender: Sender{}, log: DecisionLog{Path: logPath},
	}

	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	const sessionID = "sess-supersede"
	req := WatchdogArmRequest{
		SessionID: sessionID, Transcript: transcript, Offset: offset, ParentPID: 0, ArmedAt: time.Now(),
		Meta: DigestMeta{Project: "proj"},
	}

	w.Arm(req)
	w.Arm(req) // supersedes the first

	waitForCanceledCount(t, logPath, sessionID, 1)

	w.Reap(sessionID)
	waitForCanceledCount(t, logPath, sessionID, 2)
}

// TestArmWatchdog_ReapThenReArm_DelayedCleanupDoesNotDeleteSuccessor is the
// regression test for the gen-reuse bug: armWatchdog used to derive gen from
// the (deletable) map entry it was replacing (old.gen + 1), so Reap-then-
// re-Arm for the same session restarted at gen 1 with nothing to derive
// from — and the reaped watchdog's own delayed exit-cleanup op, captured
// with that same gen 1, could then match the successor's freshly-registered
// gen 1 and delete its entry out of the registry, leaking a live watchdog
// past the daemon-shutdown-cancel branch.
//
// This drives armWatchdog and cleanupWatchdog directly (the loop-confined
// functions themselves, not through the channel/goroutine machinery) so the
// exact interleaving — the first watchdog's cleanup arriving LAST, after
// the second Arm has already re-registered the session — is deterministic
// rather than a real race dependent on goroutine scheduling.
func TestArmWatchdog_ReapThenReArm_DelayedCleanupDoesNotDeleteSuccessor(t *testing.T) {
	reg := &watchdogRegistry{entries: make(map[string]watchdogEntry)}
	w := daemonWatchdog{}

	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	const sessionID = "sess-gen-reuse"
	req := WatchdogArmRequest{
		SessionID: sessionID, Transcript: transcript, Offset: offset, Meta: DigestMeta{Project: "proj"},
	}

	armWatchdog(reg, req, w)
	firstGen := reg.entries[sessionID].gen

	// Reap: cancel and remove, mirroring daemonWatchdog.Reap's loop-confined
	// body exactly (without the channel round trip).
	reg.entries[sessionID].cancel()
	delete(reg.entries, sessionID)

	// Re-arm the same session. With the bug, this lands on gen 1 again
	// (nothing to derive old.gen from — the entry is gone); fixed, nextGen
	// is loop-lifetime monotonic and never reused.
	armWatchdog(reg, req, w)
	secondGen := reg.entries[sessionID].gen
	t.Cleanup(func() {
		if e, ok := reg.entries[sessionID]; ok {
			e.cancel()
		}
	})
	if secondGen == firstGen {
		t.Fatalf(
			"second arm's gen = %d, same as first arm's gen %d (gen must never be reused after a delete)",
			secondGen, firstGen,
		)
	}

	// Simulate the first (reaped) watchdog's cleanup op arriving late — well
	// after the second Arm already re-registered the session. With the bug
	// this deletes the live successor's entry entirely.
	cleanupWatchdog(reg, sessionID, firstGen)

	got, ok := reg.entries[sessionID]
	if !ok {
		t.Fatal("successor watchdog entry was deleted by the reaped predecessor's delayed cleanup")
	}
	if got.gen != secondGen {
		t.Errorf("surviving entry gen = %d, want %d (the second arm's)", got.gen, secondGen)
	}
}

// TestDaemonWatchdog_Reap_CancelsRunningWatchdog proves Reap cancels a live
// watchdog's goroutine: after Arm, Reap must produce an observable
// "canceled" exit record for that session.
func TestDaemonWatchdog_Reap_CancelsRunningWatchdog(t *testing.T) {
	ch := make(chan loopMsg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := Daemon{}
	go d.loop(ctx, ch)

	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	w := daemonWatchdog{
		ch: ch, done: ctx.Done(),
		judge: Judge{Bin: "/nonexistent"}, sender: Sender{}, log: DecisionLog{Path: logPath},
	}

	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	const sessionID = "sess-reap"
	req := WatchdogArmRequest{
		SessionID: sessionID, Transcript: transcript, Offset: offset, ParentPID: 0, ArmedAt: time.Now(),
		Meta: DigestMeta{Project: "proj"},
	}

	w.Arm(req)
	w.Reap(sessionID)

	waitForCanceledCount(t, logPath, sessionID, 1)
}

// TestDaemonLoop_ShutdownCancelsAllLiveWatchdogs proves Daemon.loop's ctx.Done
// branch cancels every still-live watchdog, not just the one most recently
// armed: two distinct sessions armed, then the loop's own ctx canceled, must
// each produce a "canceled" exit record.
func TestDaemonLoop_ShutdownCancelsAllLiveWatchdogs(t *testing.T) {
	ch := make(chan loopMsg)
	ctx, cancel := context.WithCancel(context.Background())
	d := Daemon{}
	go d.loop(ctx, ch)

	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	w := daemonWatchdog{
		ch: ch, done: ctx.Done(),
		judge: Judge{Bin: "/nonexistent"}, sender: Sender{}, log: DecisionLog{Path: logPath},
	}

	transcript := copyFixture(t, "goal_none.jsonl")
	offset := scannedBytes(t, transcript)
	meta := DigestMeta{Project: "proj"}
	w.Arm(WatchdogArmRequest{SessionID: "sess-shutdown-a", Transcript: transcript, Offset: offset, Meta: meta})
	w.Arm(WatchdogArmRequest{SessionID: "sess-shutdown-b", Transcript: transcript, Offset: offset, Meta: meta})

	cancel() // daemon shutdown

	waitForCanceledCount(t, logPath, "sess-shutdown-a", 1)
	waitForCanceledCount(t, logPath, "sess-shutdown-b", 1)
}

// TestWatchdogSendWithDedupe_MarksNotifiedThroughLoopState proves a
// watchdog's send routes MarkNotified through the loop's own DedupeState
// (loopState/MemoryState), not any state this package owns directly: a
// following idle_prompt for the same session within dedupeWindow must
// dedupe against it, exactly as an ordinary hook invocation's send would.
func TestWatchdogSendWithDedupe_MarksNotifiedThroughLoopState(t *testing.T) {
	ch := make(chan loopMsg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := Daemon{}
	go d.loop(ctx, ch)

	const sessionID = "sess-watchdog-dedupe"
	deps := WatchdogDeps{
		Now:  time.Now,
		Send: func(_ context.Context, _ Notification) error { return nil },
	}
	deps.Send = watchdogSendWithDedupe(sessionID, deps, ch)

	if err := deps.Send(context.Background(), Notification{Title: "t", Body: "watchdog said hi"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	p := Pipeline{
		DryRun:  true,
		Judge:   Judge{Bin: writeStubClaude(t)},
		Log:     DecisionLog{Path: logPath},
		Stdout:  new(strings.Builder),
		Host:    "testhost",
		Present: neverPresent,
		State:   loopState{ch: ch},
	}
	in := HookInput{SessionID: sessionID, HookEventName: "Notification", NotificationType: "idle_prompt"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() || !strings.Contains(recs[0].Reason, "dedupe") {
		t.Fatalf("records = %+v, want a silent dedupe record (watchdog send must have marked notified)", recs)
	}
}

// TestLoopState_Call_WaitsForOpEvenIfCtxCancelsAfterSend drives
// loopState.call directly against a hand-rolled receiver (standing in for
// the real event loop) to pin the exact interleaving a mutex-free design
// depends on: once the send to ch succeeds, the "loop" has committed to
// running the op, so call() must wait for it unconditionally — even if ctx
// is canceled in the gap between the send completing and the op actually
// running. Racing that wait against ctx.Done() would let call() return
// (and the caller read its result variable) while the op is still
// in-flight, writing to the same variable — a data race only -race
// reliably catches, which is why this test drives msg.op itself on a
// controlled delay rather than relying on the real daemon's shutdown
// timing.
func TestLoopState_Call_WaitsForOpEvenIfCtxCancelsAfterSend(t *testing.T) {
	ch := make(chan loopMsg)
	ctx, cancel := context.WithCancel(context.Background())
	l := loopState{ch: ch}

	callReturned := make(chan struct{})
	go func() {
		l.call(ctx, func(_ *MemoryState) {})
		close(callReturned)
	}()

	// Receive the msg the way the real loop would — this is what unblocks
	// call()'s first select.
	msg := <-ch
	cancel()

	// Give call() every chance to race ahead if it still raced the wait
	// against ctx.Done(): with ctx already canceled and the op not yet
	// run, a buggy second select would return here.
	select {
	case <-callReturned:
		t.Fatal("call() returned before its op ran — the wait raced ctx cancellation against the op completing")
	case <-time.After(20 * time.Millisecond):
	}

	// Now run the op the way the loop would, well after cancellation.
	msg.op(nil)

	select {
	case <-callReturned:
	case <-time.After(time.Second):
		t.Fatal("call() never returned after its op ran")
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

// TestListen_SelfBind_RejectsSymlinkedSocketDir proves Listen refuses to
// bind inside a socket directory that is actually a symlink: another local
// user sharing /tmp (the XDG_RUNTIME_DIR-unset fallback) could otherwise
// plant a symlink at the expected cc-tools-$UID path and redirect the
// daemon's bind wherever they choose. os.MkdirAll is a no-op on a path that
// already resolves to a directory (symlink or not), so the fix must reject
// this after MkdirAll, not rely on it to fail.
func TestListen_SelfBind_RejectsSymlinkedSocketDir(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("creating real dir: %v", err)
	}
	link := filepath.Join(root, "cc-tools-link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	sockPath := filepath.Join(link, "notifyd.sock")

	if _, err := Listen(sockPath); err == nil {
		t.Fatal("Listen() error = nil, want an error for a symlinked socket dir")
	}
	if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
		t.Errorf("socket exists (err = %v), want no bind through a symlinked dir", statErr)
	}
}

// TestListen_SelfBind_RejectsWorldWritableSocketDir proves Listen refuses to
// bind inside a socket directory that is not owner-only (0700): MkdirAll
// never changes an existing directory's mode, so a pre-planted
// world-writable directory at the expected path would otherwise let any
// other local user race the daemon for the socket file.
func TestListen_SelfBind_RejectsWorldWritableSocketDir(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")

	dir := filepath.Join(t.TempDir(), "cc-tools-777")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatalf("creating dir: %v", err)
	}
	// Mkdir's requested mode is subject to umask; Chmod is not.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	sockPath := filepath.Join(dir, "notifyd.sock")

	if _, err := Listen(sockPath); err == nil {
		t.Fatal("Listen() error = nil, want an error for a world-writable socket dir")
	}
	if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
		t.Errorf("socket exists (err = %v), want no bind through a world-writable dir", statErr)
	}
}

func TestListen_SelfBind_RemovesStaleSocket(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")

	// A fresh subdirectory, not t.TempDir() itself: t.TempDir() is 0755,
	// and verifySocketDir now requires 0700 — exactly as a real crashed
	// prior daemon's own MkdirAll(dir, 0700) would have left it.
	dir := filepath.Join(t.TempDir(), "notifyd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
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

	// A nonexistent subdirectory, not t.TempDir() itself (0755): MkdirAll
	// must create it fresh at 0700 for verifySocketDir to accept it.
	sockPath := filepath.Join(t.TempDir(), "notifyd", "notifyd.sock")
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
