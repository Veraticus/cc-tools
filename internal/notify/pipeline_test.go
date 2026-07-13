package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// readDecisionLog reads every JSON line in path as a DecisionRecord.
func readDecisionLog(t *testing.T, path string) []DecisionRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading decision log %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	recs := make([]DecisionRecord, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec DecisionRecord
		if unmarshalErr := json.Unmarshal([]byte(line), &rec); unmarshalErr != nil {
			t.Fatalf("unmarshaling decision record %q: %v", line, unmarshalErr)
		}
		recs = append(recs, rec)
	}
	return recs
}

// newTestPipeline builds a DryRun Pipeline pointed at a fresh temp state
// base, with judgeBin as the stub claude binary.
func newTestPipeline(
	t *testing.T,
	stdout *bytes.Buffer,
	judgeBin string,
) (Pipeline, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	p := Pipeline{
		DryRun: true,
		Judge:  Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:    DecisionLog{Path: logPath},
		Stdout: stdout,
		Host:   "testhost",
	}
	return p, logPath
}

// goalIncidentDaemonCondition is the exact goal condition text baked into the
// goal_incident_daemon.jsonl fixture's goal_status attachment: it reproduces
// the July 5 grailquest incident, where a live background-Bash daemon parked
// under an armed goal caused Claude Code's built-in /goal evaluator to skip
// re-evaluating the goal forever (it defers whenever a Stop finds a live
// background task). At 235 bytes it exceeds maxGoalConditionLen, so tests
// keying off it also exercise the block-reason truncation path.
const goalIncidentDaemonCondition = "Get the grailquest content daemon (grailquestd-play) running stably on " +
	"127.0.0.1:8080 serving real content, with the health check passing three times in a row before you stop; " +
	"if it crashes, restart it and keep going without asking me."

// newGoalTestPipeline builds a Pipeline pointed at a fresh temp state base
// with DryRun:false (unlike newTestPipeline, which fixes DryRun:true) —
// tests needing the real, non-DryRun notification-send path that DryRun:true
// can never reach use this instead. Watchdog is left nil: tests that need to
// observe a real arm attempt inject a *fakeWatchdog explicitly.
func newGoalTestPipeline(
	t *testing.T,
	stdout *bytes.Buffer,
	judgeBin string,
) (Pipeline, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "notify-decisions.jsonl")
	p := Pipeline{
		DryRun:  false,
		Judge:   Judge{Bin: judgeBin, Model: "claude-haiku-4-5"},
		Log:     DecisionLog{Path: logPath},
		Stdout:  stdout,
		SelfBin: judgeBin,
	}
	return p, logPath
}

// memDedupeState adapts a fresh MemoryState to DedupeState for tests that
// need to seed or inspect Pipeline dedupe state directly, without a live
// daemon event loop: safe because these tests only ever drive Pipeline.Run
// from a single goroutine at a time, so MemoryState's loop-confinement
// requirement (see its doc comment) is trivially satisfied here.
type memDedupeState struct {
	m *MemoryState
}

func newMemDedupeState() memDedupeState {
	return memDedupeState{m: NewMemoryState()}
}

func (d memDedupeState) SinceLastNotify(_ context.Context, sessionID string, now time.Time) time.Duration {
	return d.m.SinceLastNotify(sessionID, now)
}

func (d memDedupeState) SinceLastNotifySame(
	_ context.Context, sessionID string, now time.Time, message string,
) time.Duration {
	return d.m.SinceLastNotifySame(sessionID, now, message)
}

func (d memDedupeState) MarkNotified(_ context.Context, sessionID string, t time.Time, message string) error {
	return d.m.MarkNotified(sessionID, t, message)
}

func (d memDedupeState) ClaimSend(
	_ context.Context, sessionID string, now time.Time, message string, window time.Duration, dryRun bool,
) (bool, time.Duration) {
	return d.m.ClaimSend(sessionID, now, message, window, dryRun)
}

func (d memDedupeState) ClaimBroadcast(
	_ context.Context, key string, window time.Duration, now time.Time, dryRun bool,
) bool {
	return d.m.ClaimBroadcast(key, window, now, dryRun)
}

func (d memDedupeState) DeleteSession(_ context.Context, sessionID string) {
	d.m.DeleteSession(sessionID)
}

type broadcastClaimCall struct {
	key    string
	window time.Duration
	now    time.Time
	dryRun bool
}

// synchronizedClaimState is a goroutine-safe DedupeState test double that
// records broadcast claims while otherwise behaving like NopState. The
// production MemoryState is deliberately loop-confined, so concurrent
// pipeline tests need their own serialized claim ledger.
type synchronizedClaimState struct {
	mu     sync.Mutex
	claims map[string]time.Time
	calls  []broadcastClaimCall
}

func newSynchronizedClaimState() *synchronizedClaimState {
	return &synchronizedClaimState{claims: make(map[string]time.Time)}
}

func (*synchronizedClaimState) SinceLastNotify(context.Context, string, time.Time) time.Duration {
	return neverNotifiedDuration
}

func (*synchronizedClaimState) SinceLastNotifySame(
	context.Context, string, time.Time, string,
) time.Duration {
	return neverNotifiedDuration
}

func (*synchronizedClaimState) MarkNotified(context.Context, string, time.Time, string) error {
	return nil
}

func (*synchronizedClaimState) ClaimSend(
	context.Context, string, time.Time, string, time.Duration, bool,
) (bool, time.Duration) {
	return true, neverNotifiedDuration
}

func (s *synchronizedClaimState) ClaimBroadcast(
	_ context.Context,
	key string,
	window time.Duration,
	now time.Time,
	dryRun bool,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, broadcastClaimCall{key: key, window: window, now: now, dryRun: dryRun})
	claimedAt, exists := s.claims[key]
	won := !exists || now.Sub(claimedAt) >= window
	if won && !dryRun {
		s.claims[key] = now
	}
	return won
}

func (*synchronizedClaimState) DeleteSession(context.Context, string) {}

func (s *synchronizedClaimState) snapshot() ([]broadcastClaimCall, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]broadcastClaimCall(nil), s.calls...), len(s.claims)
}

type synchronizedRequestCapture struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (c *synchronizedRequestCapture) sender() Sender {
	return Sender{
		URL: "http://stub.invalid/publish",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			c.mu.Lock()
			c.requests = append(c.requests, capturedRequest{Title: req.Header.Get("Title"), Body: string(body)})
			c.mu.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
		})},
	}
}

func (c *synchronizedRequestCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// fakeWatchdog is a spy Pipeline.Watchdog for tests that need to observe
// arm/reap calls without a live daemon event loop.
type fakeWatchdog struct {
	armed  []WatchdogArmRequest
	reaped []string
}

func (f *fakeWatchdog) Arm(req WatchdogArmRequest) { f.armed = append(f.armed, req) }
func (f *fakeWatchdog) Reap(sessionID string)      { f.reaped = append(f.reaped, sessionID) }

// capturedRequest is what stubSenderRecording's round tripper records for
// one Sender.Send call: everything a real ntfy POST would have carried.
type capturedRequest struct {
	Title    string
	Body     string
	Priority string
	Tags     string
}

// roundTripFunc adapts a plain function to http.RoundTripper, so
// stubSenderRecording can inject its capture logic without a real network
// call.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubSenderRecording returns a Sender whose Send calls are captured into
// *captured and always succeed with 200 OK, never touching the network:
// Pipeline.Sender is a concrete Sender struct (not an interface), so tests
// intercept delivery via Sender.Client's Transport rather than a fake
// implementation.
func stubSenderRecording(captured *[]capturedRequest) Sender {
	return Sender{
		URL: "http://stub.invalid/publish",
		Client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				*captured = append(*captured, capturedRequest{
					Title:    req.Header.Get("Title"),
					Body:     string(body),
					Priority: req.Header.Get("Priority"),
					Tags:     req.Header.Get("Tags"),
				})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
			}),
		},
	}
}

func waitCapturedRequest(t *testing.T, requestCh <-chan capturedRequest) capturedRequest {
	t.Helper()
	select {
	case request := <-requestCh:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ntfy request")
		return capturedRequest{}
	}
}

func TestCodexEvaluatorFailureClaimKey_UsesOnlyStableLocusMetadata(t *testing.T) {
	tests := []struct {
		name                       string
		pane, workspace, host, cwd string
		want                       string
	}{
		{
			name: "pane and cwd take precedence",
			pane: "%42", workspace: "IGNORED-WORKSPACE", host: "IGNORED-HOST", cwd: "/work/project",
			want: "codex-evaluator-failure\npane=3:%42\ncwd=13:/work/project",
		},
		{
			name:      "workspace and cwd when pane absent",
			workspace: "earth:3", host: "IGNORED-HOST", cwd: "/work/project",
			want: "codex-evaluator-failure\nworkspace=7:earth:3\ncwd=13:/work/project",
		},
		{
			name: "host and empty cwd when pane and workspace absent",
			host: "vermissian",
			want: "codex-evaluator-failure\nhost=10:vermissian\ncwd=0:",
		},
		{
			name: "cwd remains a locus when every runtime locator is absent",
			cwd:  "/work/project",
			want: "codex-evaluator-failure\nhost=0:\ncwd=13:/work/project",
		},
		{
			name: "all locator fields absent",
			want: "codex-evaluator-failure\nunknown-locus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexEvaluatorFailureClaimKey(tt.pane, tt.workspace, tt.host, tt.cwd)
			if got != tt.want {
				t.Errorf("codexEvaluatorFailureClaimKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPipeline_TurnComplete_CodexFailureRateLimitedAcrossSessionsAndExpires(t *testing.T) {
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.Sender = stubSenderRecording(&sent)
	p.State = newMemDedupeState()
	p.Environ = []string{"TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	inputs := []HookInput{
		{
			SessionID: "thread-one", CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "First fallback body.",
		},
		{
			SessionID: "thread-two", CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "A different fallback body from a sibling thread.",
		},
		{
			SessionID: "thread-three", CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "Failure after the quiet window.",
		},
	}

	for i := range 2 {
		if err := p.Run(context.Background(), inputs[i]); err != nil {
			t.Fatalf("Run() #%d error = %v", i+1, err)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("sent after sibling failures = %+v, want only first failure", sent)
	}

	state, ok := p.State.(memDedupeState)
	if !ok {
		t.Fatalf("State = %T, want memDedupeState", p.State)
	}
	if len(state.m.claims) != 1 {
		t.Fatalf("broadcast claims = %+v, want one failure-locus claim", state.m.claims)
	}
	for key := range state.m.claims {
		state.m.claims[key] = claimMemState{claimedAt: time.Now().Add(-failOpenWindow - time.Second)}
	}
	if err := p.Run(context.Background(), inputs[2]); err != nil {
		t.Fatalf("Run() after expiry error = %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent after expiry = %+v, want failure to send again", sent)
	}

	records := readDecisionLog(t, logPath)
	if len(records) != 3 || records[0].Outcome != OutcomeSend.String() ||
		records[1].Outcome != OutcomeSilent.String() || records[2].Outcome != OutcomeSend.String() {
		t.Fatalf("decision records = %+v, want send, silent, send", records)
	}
	if records[1].Title != "" || records[1].Body != "" {
		t.Errorf("suppressed record carries notification fields: %+v", records[1])
	}
}

func TestPipeline_TurnComplete_CodexFailureLociAreIndependent(t *testing.T) {
	tests := []struct {
		name                            string
		firstPane, secondPane           string
		firstWorkspace, secondWorkspace string
		firstHost, secondHost           string
		firstCWD, secondCWD             string
		wantSent                        int
	}{
		{
			name: "different pane", firstPane: "%1", secondPane: "%2",
			firstWorkspace: "earth:3", secondWorkspace: "earth:3",
			firstHost: "host-a", secondHost: "host-a", firstCWD: "/work/project", secondCWD: "/work/project",
			wantSent: 2,
		},
		{
			name: "different cwd", firstPane: "%1", secondPane: "%1",
			firstWorkspace: "earth:3", secondWorkspace: "earth:3",
			firstHost: "host-a", secondHost: "host-a", firstCWD: "/work/one", secondCWD: "/work/two",
			wantSent: 2,
		},
		{
			name:           "workspace stable without pane despite host change",
			firstWorkspace: "earth:3", secondWorkspace: "earth:3",
			firstHost: "host-a", secondHost: "host-b", firstCWD: "/work/project", secondCWD: "/work/project",
			wantSent: 1,
		},
		{
			name:      "host stable without pane or workspace",
			firstHost: "host-a", secondHost: "host-a", firstCWD: "/work/project", secondCWD: "/work/project",
			wantSent: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var sent []capturedRequest
			p, _ := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
			p.Sender = stubSenderRecording(&sent)
			p.State = newMemDedupeState()

			p.Environ = []string{"TMUX_PANE=" + tt.firstPane}
			p.Workspace = tt.firstWorkspace
			p.Host = tt.firstHost
			if err := p.Run(context.Background(), HookInput{
				SessionID: "thread-one", CWD: tt.firstCWD, HookEventName: eventTurnComplete,
				LastAssistantMessage: "First evaluator failure.",
			}); err != nil {
				t.Fatalf("first Run() error = %v", err)
			}

			p.Environ = []string{"TMUX_PANE=" + tt.secondPane}
			p.Workspace = tt.secondWorkspace
			p.Host = tt.secondHost
			if err := p.Run(context.Background(), HookInput{
				SessionID: "thread-two", CWD: tt.secondCWD, HookEventName: eventTurnComplete,
				LastAssistantMessage: "Second evaluator failure.",
			}); err != nil {
				t.Fatalf("second Run() error = %v", err)
			}

			if len(sent) != tt.wantSent {
				t.Errorf("sent = %+v, want %d notifications", sent, tt.wantSent)
			}
		})
	}
}

func TestPipeline_TurnComplete_NopStateCodexFallbacksNeverSuppressOrEvaluate(t *testing.T) {
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.Sender = stubSenderRecording(&sent)
	p.State = NopState{}
	p.Environ = []string{"TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	for i, in := range []HookInput{
		{
			SessionID: "inline-one", CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "First inline fallback.",
		},
		{
			SessionID: "inline-two", CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "Second inline fallback.",
		},
	} {
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() #%d error = %v", i+1, err)
		}
	}

	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want both inline fallbacks", sent)
	}
	for _, record := range readDecisionLog(t, logPath) {
		if record.Outcome != OutcomeSend.String() || record.JudgeErr != "" ||
			record.JudgeMs != 0 || record.Digest != "" {
			t.Errorf("inline record = %+v, want unsuppressed model-free send", record)
		}
	}
}

func TestPipeline_TurnComplete_DryRunCodexFailureClaimDoesNotRecord(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "codex-calls")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))
	state := newSynchronizedClaimState()
	p.State = state
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Environ = []string{"TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"
	in := HookInput{
		SessionID: "dry-run", CWD: "/work/project", HookEventName: eventTurnComplete,
		LastAssistantMessage: "Dry-run fallback.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("dry Run() error = %v", err)
	}

	calls, claimCount := state.snapshot()
	if len(calls) != 1 || !calls[0].dryRun || claimCount != 0 {
		t.Fatalf("dry-run claims = %+v, recorded=%d; want one observation and no record", calls, claimCount)
	}
	if _, err := os.Stat(callLog); err == nil {
		t.Fatal("Codex call log exists: dry run evaluated a model")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Codex call log: %v", err)
	}

	var sent []capturedRequest
	p.DryRun = false
	p.CodexJudge = CodexJudge{}
	p.Sender = stubSenderRecording(&sent)
	in.SessionID = "real-run"
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("real Run() error = %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want real failure to win after dry-run observation", sent)
	}
}

func TestPipeline_TurnComplete_CodexFailureClaimUsesFreshTimeAndPreservesEventTime(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t))
	state := newSynchronizedClaimState()
	p.State = state

	eventNow := time.Now().Add(-time.Hour)
	claimNotBefore := time.Now()
	p.handleCodexEvaluatorFailure(
		context.Background(),
		HookInput{SessionID: "stale-event", CWD: "/work/project", HookEventName: eventTurnComplete},
		eventNow,
		"project",
		"earth:3",
		"vermissian",
		"codex turn complete",
		"",
		errors.New("evaluator failed"),
		"digest",
		1,
	)
	claimNotAfter := time.Now()

	calls, _ := state.snapshot()
	if len(calls) != 1 {
		t.Fatalf("failure claim calls = %+v, want one", calls)
	}
	if calls[0].now.Before(claimNotBefore) || calls[0].now.After(claimNotAfter) {
		t.Errorf(
			"claim time = %s, want fresh time in [%s, %s] (event time %s)",
			calls[0].now,
			claimNotBefore,
			claimNotAfter,
			eventNow,
		)
	}

	records := readDecisionLog(t, logPath)
	if len(records) != 1 || !records[0].Time.Equal(eventNow) {
		t.Errorf("decision records = %+v, want event time %s unchanged", records, eventNow)
	}
}

func TestPipeline_TurnComplete_ConcurrentCodexFailuresHaveOneWinner(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	state := newSynchronizedClaimState()
	captured := &synchronizedRequestCapture{}
	p.State = state
	p.Sender = captured.sender()
	p.Environ = []string{"TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	const runs = 16
	start := make(chan struct{})
	errCh := make(chan error, runs)
	var wg sync.WaitGroup
	for range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- p.Run(context.Background(), HookInput{
				SessionID: "sibling-thread", CWD: "/work/project", HookEventName: eventTurnComplete,
				LastAssistantMessage: "Concurrent evaluator failure.",
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	}

	if captured.count() != 1 {
		t.Fatalf("sent = %d, want exactly one atomic claim winner", captured.count())
	}
	records := readDecisionLog(t, logPath)
	sends, silences := 0, 0
	for _, record := range records {
		switch record.Outcome {
		case OutcomeSend.String():
			sends++
		case OutcomeSilent.String():
			silences++
			if record.Title != "" || record.Body != "" {
				t.Errorf("suppressed record carries notification fields: %+v", record)
			}
		}
	}
	if sends != 1 || silences != runs-1 {
		t.Errorf("outcomes: sends=%d silences=%d records=%d, want 1/%d/%d", sends, silences, len(records), runs-1, runs)
	}
}

func TestPipeline_TurnComplete_ValidCodexVerdictsNeverConsultFailureClaims(t *testing.T) {
	tests := []struct {
		name        string
		verdict     string
		wantSent    int
		wantOutcome string
	}{
		{
			name: "done", verdict: validCodexVerdict,
			wantSent: 1, wantOutcome: OutcomeSend.String(),
		},
		{
			name: "blocked",
			verdict: `{"notify":true,"urgency":"blocked","task":"choose database",` +
				`"body":"Choose a database.","reason":"input needed"}`,
			wantSent: 1, wantOutcome: OutcomeSend.String(),
		},
		{
			name:        "semantic silence",
			verdict:     `{"notify":false,"urgency":null,"task":null,"body":null,"reason":"work continues"}`,
			wantOutcome: OutcomeSilent.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STUB_STDOUT", tt.verdict)
			var stdout bytes.Buffer
			var sent []capturedRequest
			p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
			state := newSynchronizedClaimState()
			p.State = state
			p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
			p.Sender = stubSenderRecording(&sent)
			p.Environ = []string{"TMUX_PANE=%42"}
			p.Workspace = "earth:3"
			p.Host = "vermissian"
			key := codexEvaluatorFailureClaimKey("%42", p.Workspace, p.Host, "/work/project")
			if !state.ClaimBroadcast(context.Background(), key, failOpenWindow, time.Now(), false) {
				t.Fatal("seeding failure claim lost unexpectedly")
			}

			if err := p.Run(context.Background(), HookInput{
				SessionID: "valid-verdict", CWD: "/work/project", HookEventName: eventTurnComplete,
				Message: "evaluate this", LastAssistantMessage: "A valid semantic result.",
			}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if len(sent) != tt.wantSent {
				t.Errorf("sent = %+v, want %d", sent, tt.wantSent)
			}
			calls, _ := state.snapshot()
			if len(calls) != 1 {
				t.Errorf("failure claim calls = %+v, want only the test seed", calls)
			}
			records := readDecisionLog(t, logPath)
			if len(records) != 1 || records[0].Outcome != tt.wantOutcome {
				t.Errorf("decision records = %+v, want one %q", records, tt.wantOutcome)
			}
		})
	}
}

func TestPipeline_TurnComplete_CodexErrorSharesDisabledFailureClaimAndLogsBoundedSilence(t *testing.T) {
	t.Setenv("STUB_STDOUT", "EVALUATOR-OUTPUT-SENTINEL")
	t.Setenv("STUB_STDERR", strings.Repeat("E", 3*maxErrSnippetBytes)+" EVALUATOR-ERROR-SENTINEL")
	t.Setenv("STUB_EXIT", "7")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	state := newSynchronizedClaimState()
	p.State = state
	p.Sender = stubSenderRecording(&sent)
	p.Environ = []string{"IGNORED", "TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"
	first := HookInput{
		SessionID: "DISABLED-SESSION-SENTINEL", CWD: "/work/project", HookEventName: eventTurnComplete,
		Message: "DISABLED-USER-CONTENT", LastAssistantMessage: "DISABLED-FALLBACK-BODY",
	}
	if err := p.Run(context.Background(), first); err != nil {
		t.Fatalf("disabled Run() error = %v", err)
	}

	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	second := HookInput{
		SessionID: "ERROR-SESSION-SENTINEL", CWD: "/work/project", HookEventName: eventTurnComplete,
		Message: "ERROR-USER-CONTENT", LastAssistantMessage: "ERROR-FALLBACK-BODY",
	}
	if err := p.Run(context.Background(), second); err != nil {
		t.Fatalf("error Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want disabled failure send then shared-class error suppression", sent)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 2 {
		t.Fatalf("decision records = %+v, want two", records)
	}
	suppressed := records[1]
	if suppressed.Outcome != OutcomeSilent.String() || suppressed.Title != "" || suppressed.Body != "" {
		t.Errorf("suppressed record = %+v, want silent with empty notification fields", suppressed)
	}
	if suppressed.Reason == "" || len(suppressed.Reason) > maxErrSnippetBytes ||
		!strings.Contains(suppressed.Reason, "suppressed") {
		t.Errorf(
			"Reason = %q (%d bytes), want bounded suppression diagnostic",
			suppressed.Reason,
			len(suppressed.Reason),
		)
	}
	if suppressed.JudgeErr == "" || len(suppressed.JudgeErr) > maxErrSnippetBytes ||
		!utf8.ValidString(suppressed.JudgeErr) {
		t.Errorf(
			"JudgeErr = %q (%d bytes), want bounded valid diagnostic",
			suppressed.JudgeErr,
			len(suppressed.JudgeErr),
		)
	}

	calls, _ := state.snapshot()
	if len(calls) != 2 {
		t.Fatalf("failure claim calls = %+v, want disabled and error claims", calls)
	}
	wantKey := "codex-evaluator-failure\npane=3:%42\ncwd=13:/work/project"
	for i, call := range calls {
		if call.key != wantKey || call.window != failOpenWindow || call.dryRun {
			t.Errorf("claim #%d = %+v, want key %q window %s non-dry", i+1, call, wantKey, failOpenWindow)
		}
		for _, forbidden := range []string{
			first.SessionID, first.Message, first.LastAssistantMessage,
			second.SessionID, second.Message, second.LastAssistantMessage,
			"EVALUATOR-OUTPUT-SENTINEL", "EVALUATOR-ERROR-SENTINEL",
		} {
			if strings.Contains(call.key, forbidden) {
				t.Errorf("claim key leaked %q: %q", forbidden, call.key)
			}
		}
	}
}

func TestPipeline_TurnComplete_SuppressedCodexErrorNormalizesInvalidUTF8BeforeBounding(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex-invalid-stderr")
	const script = `#!/bin/sh
cat >/dev/null
i=0
while [ "$i" -lt 60 ]; do
  printf '\377' >&2
  i=$((i + 1))
done
exit 7
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing invalid-stderr Codex stub: %v", err)
	}

	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	state := newSynchronizedClaimState()
	p.State = state
	p.CodexJudge = CodexJudge{Bin: bin, Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Environ = []string{"TMUX_PANE=%42"}
	p.Workspace = "earth:3"
	p.Host = "vermissian"
	key := codexEvaluatorFailureClaimKey("%42", p.Workspace, p.Host, "/work/project")
	if !state.ClaimBroadcast(context.Background(), key, failOpenWindow, time.Now(), false) {
		t.Fatal("seeding failure claim lost unexpectedly")
	}

	if err := p.Run(context.Background(), HookInput{
		SessionID: "invalid-stderr", CWD: "/work/project", HookEventName: eventTurnComplete,
		LastAssistantMessage: "Suppressed fallback.",
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	records := readDecisionLog(t, logPath)
	if len(records) != 1 {
		t.Fatalf("decision records = %+v, want one suppressed evaluator failure", records)
	}
	suppressed := records[0]
	if suppressed.Outcome != OutcomeSilent.String() || suppressed.Title != "" || suppressed.Body != "" {
		t.Errorf("suppressed record = %+v, want silent with empty notification fields", suppressed)
	}
	if suppressed.JudgeErr == "" || !utf8.ValidString(suppressed.JudgeErr) ||
		len(suppressed.JudgeErr) > maxErrSnippetBytes {
		t.Errorf(
			"JudgeErr = %q (%d bytes), want valid UTF-8 within %d bytes",
			suppressed.JudgeErr,
			len(suppressed.JudgeErr),
			maxErrSnippetBytes,
		)
	}
}

func TestPipeline_TurnComplete_CodexDoneVerdictSentAsNormalizedPlainText(t *testing.T) {
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"## **tests_complete**","body":"`+"```text"+`\n# Result\n- **Fixed** [tests](https://example.test)\n`+"```"+`\n> Use `+"`go test ./...`"+` in src/my_project/*.go.\n~~Old note~~ 完了 ✅","reason":"SECRET-REASON"}`,
	)

	requestCh := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading ntfy body: %v", err)
		}
		requestCh <- capturedRequest{Title: req.Header.Get("Title"), Body: string(body)}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = Sender{URL: server.URL, Client: server.Client()}
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	in := HookInput{
		SessionID: "codex-plain-text", CWD: "/home/user/project", HookEventName: eventTurnComplete,
		Message: "fix the tests", LastAssistantMessage: "All checks now pass.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	request := waitCapturedRequest(t, requestCh)
	if want := "project · tests_complete"; request.Title != want {
		t.Errorf("Title = %q, want %q", request.Title, want)
	}
	if want := "Result Fixed tests Use go test ./... in src/my_project/*.go. Old note 完了 ✅\n\n— earth:3 @ vermissian"; request.Body != want {
		t.Errorf("Body = %q, want %q", request.Body, want)
	}
	for _, forbidden := range []string{
		"{", `"notify"`, "SECRET-REASON", "##", "**", "[tests](", "```", "`", "~~", "\n- ", "\n> ",
	} {
		if strings.Contains(request.Title, forbidden) || strings.Contains(request.Body, forbidden) {
			t.Errorf(
				"ntfy request leaked %q: title=%q body=%q",
				forbidden,
				request.Title,
				request.Body,
			)
		}
	}
	if !utf8.ValidString(request.Title) || !utf8.ValidString(request.Body) || len(request.Body) > maxBodyBytes {
		t.Errorf(
			"ntfy request is not bounded valid UTF-8: title=%q body=%q (%d bytes)",
			request.Title,
			request.Body,
			len(request.Body),
		)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 || records[0].JudgeMode != "" {
		t.Errorf(
			"decision records = %+v, want dedicated Codex handling without a Claude JudgeMode",
			records,
		)
	}
}

func TestPipeline_TurnComplete_CodexSilentVerdictSendsNothing(t *testing.T) {
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":false,"urgency":null,"task":null,"body":null,"reason":"routine worker report"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)

	in := HookInput{
		SessionID: "codex-silent", CWD: "/home/user/project", HookEventName: eventTurnComplete,
		Message: "report status", LastAssistantMessage: "The scout reported its findings to the parent.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 0 {
		t.Fatalf("sent = %+v, want semantic silence", sent)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 || records[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("decision records = %+v, want one silent outcome", records)
	}
	if records[0].Urgency != "" || records[0].Title != "" || records[0].Body != "" {
		t.Errorf("silent decision record carries notification fields: %+v", records[0])
	}
}

func TestPipeline_TurnComplete_CodexBlockedVerdictNeedsInput(t *testing.T) {
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"blocked","task":"choose database","body":"Choose SQLite or Postgres to continue.","reason":"user decision gates progress"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	in := HookInput{
		SessionID: "codex-blocked", CWD: "/home/user/project", HookEventName: eventTurnComplete,
		Message: "pick storage", LastAssistantMessage: "Which database should I use?",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one blocked notification", sent)
	}
	if want := "project · Needs input · choose database"; sent[0].Title != want {
		t.Errorf("Title = %q, want %q", sent[0].Title, want)
	}
	if want := "Choose SQLite or Postgres to continue.\n\n— earth:3 @ vermissian"; sent[0].Body != want {
		t.Errorf("Body = %q, want exact action and locator %q", sent[0].Body, want)
	}
	if sent[0].Priority != "5" || sent[0].Tags != "question" {
		t.Errorf("ntfy headers = priority %q tags %q, want Priority 5/question", sent[0].Priority, sent[0].Tags)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 || records[0].Outcome != OutcomeSend.String() || records[0].Urgency != UrgencyBlocked {
		t.Errorf("decision records = %+v, want one blocked send", records)
	}
}

func TestPipeline_TurnComplete_CodexJSONFieldsNeverReachSender(t *testing.T) {
	rawJSON := `{"notify":true,"urgency":"done","task":"secret","body":"SECRET-JSON-SENTINEL","reason":"secret"}`
	verdict, marshalErr := json.Marshal(JudgeVerdict{
		Notify: true, Urgency: UrgencyDone, Task: rawJSON, Body: rawJSON, Reason: "r",
	})
	if marshalErr != nil {
		t.Fatalf("marshaling verdict: %v", marshalErr)
	}
	t.Setenv("STUB_STDOUT", string(verdict))

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)
	p.Host = "vermissian"

	in := HookInput{SessionID: "codex-json", CWD: "/home/user/project", HookEventName: eventTurnComplete}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one notification", sent)
	}
	for _, forbidden := range []string{"{", `"notify"`, "SECRET-JSON-SENTINEL"} {
		if strings.Contains(sent[0].Title, forbidden) || strings.Contains(sent[0].Body, forbidden) {
			t.Errorf("sender boundary leaked raw evaluator JSON %q: %+v", forbidden, sent[0])
		}
	}
}

func TestPipeline_TurnComplete_CodexFailuresSendBoundedPlainTextFallback(t *testing.T) {
	tests := []struct {
		name, stdout, stderr, exit, sleep string
		missingBin                        bool
	}{
		{name: "missing binary", missingBin: true},
		{name: "timeout", sleep: "1"},
		{name: "nonzero", stdout: "SECRET-STDOUT", stderr: "SECRET-STDERR", exit: "7"},
		{name: "empty stdout"},
		{name: "malformed verdict", stdout: `{MALFORMED-SECRET`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STUB_STDOUT", tt.stdout)
			t.Setenv("STUB_STDERR", tt.stderr)
			t.Setenv("STUB_EXIT", tt.exit)
			t.Setenv("STUB_SLEEP", tt.sleep)

			requestCh := make(chan capturedRequest, 1)
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Errorf("reading ntfy body: %v", err)
					}
					requestCh <- capturedRequest{Title: req.Header.Get("Title"), Body: string(body)}
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer server.Close()

			bin := writeStubCodex(t)
			if tt.missingBin {
				bin = filepath.Join(t.TempDir(), "ERROR-SENTINEL-missing-codex")
			}
			var stdout bytes.Buffer
			p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
			p.CodexJudge = CodexJudge{
				Bin:     bin,
				Model:   "gpt-5.6-luna",
				Timeout: 20 * time.Millisecond,
			}
			p.Sender = Sender{URL: server.URL, Client: server.Client()}
			p.Workspace = "earth:3"
			p.Host = "vermissian"

			final := strings.Repeat("# Prefix αβγ\n- **item** [docs](https://example.test)\n", 12) +
				"> Use `src/my_project/*.go`\n~~meaningful tail~~ ✅"
			plainFinal := strings.Repeat("Prefix αβγ item docs ", 12) +
				"Use src/my_project/*.go meaningful tail ✅"
			in := HookInput{
				SessionID:            "codex-failure",
				CWD:                  "/home/user/project",
				HookEventName:        eventTurnComplete,
				Message:              "summarize this",
				LastAssistantMessage: final,
			}
			if err := p.Run(context.Background(), in); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			request := waitCapturedRequest(t, requestCh)
			if want := "project · earth:3"; request.Title != want {
				t.Errorf("Title = %q, want %q", request.Title, want)
			}
			if want := truncateHeadWords(plainFinal, maxNotificationTailLen); request.Body != want {
				t.Errorf("Body = %q, want bounded fallback %q", request.Body, want)
			}
			if len(request.Body) > maxNotificationTailLen || !utf8.ValidString(request.Body) {
				t.Errorf(
					"Body = %q (%d bytes), want valid UTF-8 within %d bytes",
					request.Body,
					len(request.Body),
					maxNotificationTailLen,
				)
			}
			if !strings.HasPrefix(request.Body, truncationEllipsis) {
				t.Errorf("Body = %q, want visible leading truncation ellipsis", request.Body)
			}
			for _, forbidden := range []string{"# ", "- **", "[docs](", "`", "~~", "\n", "{"} {
				if strings.Contains(request.Body, forbidden) {
					t.Errorf("fallback body contains Markdown/JSON artifact %q: %q", forbidden, request.Body)
				}
			}
			for _, forbidden := range []string{
				"SECRET-STDOUT", "SECRET-STDERR", "MALFORMED-SECRET", "ERROR-SENTINEL", "codex exec:",
			} {
				if strings.Contains(request.Title, forbidden) ||
					strings.Contains(request.Body, forbidden) {
					t.Errorf(
						"ntfy request leaked %q: title=%q body=%q",
						forbidden,
						request.Title,
						request.Body,
					)
				}
			}
			records := readDecisionLog(t, logPath)
			if len(records) != 1 || records[0].JudgeErr == "" {
				t.Fatalf(
					"decision records = %+v, want one internally logged evaluator error",
					records,
				)
			}
			if len(records[0].JudgeErr) > maxErrSnippetBytes ||
				!utf8.ValidString(records[0].JudgeErr) {
				t.Errorf(
					"JudgeErr = %q (%d bytes), want bounded valid UTF-8 within %d bytes",
					records[0].JudgeErr, len(records[0].JudgeErr), maxErrSnippetBytes,
				)
			}
		})
	}
}

func TestPipeline_TurnComplete_CodexDigestPreservesCompleteInput(t *testing.T) {
	stdinFile := filepath.Join(t.TempDir(), "codex-stdin")
	t.Setenv("STUB_STDIN_FILE", stdinFile)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)

	userInput := strings.Repeat("用户输入 αβγ \"quoted\" $() `backticks`\nsecond line ✅\n", 80)
	final := strings.Repeat("最终回答 🌙 \"verbatim\" $() `literal`\nnext line\n", 80)
	in := HookInput{
		SessionID:            "codex-complete-digest",
		CWD:                  "/home/user/project",
		HookEventName:        eventTurnComplete,
		Message:              userInput,
		LastAssistantMessage: final,
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading evaluator stdin: %v", err)
	}
	wantDigest := "USER INPUT\n" + userInput + "\n\nFINAL ASSISTANT MESSAGE\n" + final
	want := codexJudgeRubric + "\n\nDIGEST\n" + wantDigest
	if string(got) != want {
		t.Errorf(
			"evaluator stdin was modified: got %d bytes, want exact %d-byte rubric+digest",
			len(got),
			len(want),
		)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one notification", sent)
	}
}

func TestPipeline_TurnComplete_DryRunNeverEvaluatesCodex(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "codex-calls")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}

	in := HookInput{
		SessionID:            "codex-dry-run",
		CWD:                  "/home/user/project",
		HookEventName:        eventTurnComplete,
		Message:              "summarize",
		LastAssistantMessage: "Dry-run fallback only.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := os.Stat(callLog); err == nil {
		t.Fatal("Codex call log exists: dry run evaluated a model")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Codex call log: %v", err)
	}
	if !strings.Contains(stdout.String(), "Dry-run fallback only.") {
		t.Errorf("stdout = %q, want deterministic fallback", stdout.String())
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 {
		t.Fatalf("decision records = %+v, want one", records)
	}
	if records[0].JudgeErr != "" || records[0].JudgeMs != 0 || records[0].Digest != "" {
		t.Errorf("decision record = %+v, want no evaluator attempt", records[0])
	}
}

func TestPipeline_TurnComplete_CodexSuccessClampsBodyAndPreservesLocator(t *testing.T) {
	rawBody := strings.Repeat("a", 180)
	verdict, marshalErr := json.Marshal(JudgeVerdict{
		Notify: true, Urgency: UrgencyDone, Task: "large result", Body: rawBody, Reason: "complete",
	})
	if marshalErr != nil {
		t.Fatalf("marshaling verdict: %v", marshalErr)
	}
	t.Setenv("STUB_STDOUT", string(verdict))

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	in := HookInput{
		SessionID:            "codex-clamped-success",
		CWD:                  "/home/user/project",
		HookEventName:        eventTurnComplete,
		Message:              "summarize",
		LastAssistantMessage: "Delivered the requested result.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one notification", sent)
	}
	suffix := locatorSuffix("earth:3", "vermissian")
	want := truncateWords(rawBody, maxBodyBytes-len(suffix)) + suffix
	if sent[0].Body != want {
		t.Errorf("Body = %q, want summary clamp with preserved locator %q", sent[0].Body, want)
	}
	if len(sent[0].Body) > maxBodyBytes ||
		!utf8.ValidString(sent[0].Body) ||
		!strings.HasSuffix(sent[0].Body, suffix) ||
		!strings.HasSuffix(strings.TrimSuffix(sent[0].Body, suffix), truncationEllipsis) {
		t.Errorf(
			"Body = %q (%d bytes), want visible summary clamp and exact locator within %d bytes",
			sent[0].Body,
			len(sent[0].Body),
			maxBodyBytes,
		)
	}
}

func TestPipeline_TurnComplete_DisabledCodexEmptyFinalFallsBackToTurnComplete(t *testing.T) {
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.Sender = stubSenderRecording(&sent)
	p.Workspace = "earth:3"
	p.Host = "vermissian"

	in := HookInput{
		SessionID:     "codex-empty-fallback",
		CWD:           "/home/user/project",
		HookEventName: eventTurnComplete,
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one notification", sent)
	}
	if sent[0].Title != "project · earth:3" || sent[0].Body != "turn complete" {
		t.Errorf("notification = %+v, want locus title and exact fallback without locator", sent[0])
	}
}

func TestPipeline_TurnComplete_IdenticalEventsAlwaysSendWithoutWatchdog(t *testing.T) {
	t.Setenv("STUB_STDOUT", validCodexVerdict)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.CodexJudge = CodexJudge{Bin: writeStubCodex(t), Model: "gpt-5.6-luna", Timeout: time.Second}
	p.Sender = stubSenderRecording(&sent)
	dedupe := newMemDedupeState()
	p.State = dedupe
	wd := &fakeWatchdog{}
	p.Watchdog = wd

	in := HookInput{
		SessionID: "codex-always-send", CWD: "/home/user/project", HookEventName: eventTurnComplete,
		Message: "run checks", LastAssistantMessage: "The requested checks now pass.",
	}
	if err := dedupe.MarkNotified(
		context.Background(), in.SessionID, time.Now(), "The requested checks now pass.",
	); err != nil {
		t.Fatalf("seeding dedupe state: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() #%d error = %v", i, err)
		}
	}

	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want both identical TurnComplete notifications", sent)
	}
	if since := dedupe.SinceLastNotifySame(
		context.Background(), in.SessionID, time.Now(), sent[1].Body,
	); since < 0 || since > 5*time.Second {
		t.Errorf("SinceLastNotifySame(latest body) = %v, want a recent notification", since)
	}
	if len(wd.armed) != 0 {
		t.Errorf("watchdog arms = %+v, want none", wd.armed)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 2 || records[0].Outcome != OutcomeSend.String() ||
		records[1].Outcome != OutcomeSend.String() {
		t.Errorf("decision records = %+v, want two sends", records)
	}
}

func TestPipeline_TurnComplete_SkipsTranscriptScan(t *testing.T) {
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, writeStubClaude(t))
	p.Sender = stubSenderRecording(&sent)

	in := HookInput{
		SessionID:            "codex-no-transcript-scan",
		CWD:                  "/home/user/project",
		HookEventName:        eventTurnComplete,
		TranscriptPath:       "/nonexistent/TRANSCRIPT-SCAN-SENTINEL",
		LastAssistantMessage: "Turn complete.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one fallback notification", sent)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 {
		t.Fatalf("decision records = %+v, want one", records)
	}
	if strings.Contains(records[0].Reason, "transcript error") ||
		strings.Contains(records[0].Reason, "TRANSCRIPT-SCAN-SENTINEL") {
		t.Errorf("Reason = %q, want TurnComplete to skip transcript scanning", records[0].Reason)
	}
}

func TestPipeline_SessionEnd_ReapsWatchdog(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))
	wd := &fakeWatchdog{}
	p.Watchdog = wd

	sessionID := "sess-end-1"
	in := HookInput{SessionID: sessionID, HookEventName: "SessionEnd"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.reaped) != 1 || wd.reaped[0] != sessionID {
		t.Errorf("reaped = %v, want [%s]", wd.reaped, sessionID)
	}
}

// TestPipeline_SessionEnd_NilWatchdog_NeverPanics proves the SessionEnd
// branch's Watchdog.Reap call is properly guarded: a Pipeline with no
// Watchdog configured (the hook client's inline fallback shape) must not
// panic on SessionEnd.
func TestPipeline_SessionEnd_NilWatchdog_NeverPanics(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))

	in := HookInput{SessionID: "sess-end-nil-watchdog", HookEventName: "SessionEnd"}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPipeline_Stop_GoalActive_DryRun_WouldArmWatchdog(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t))
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-1", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent outcome writes no DRY RUN line)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1", len(recs))
	}
	if recs[0].Outcome != OutcomeSilent.String() {
		t.Errorf("Outcome = %q, want silent", recs[0].Outcome)
	}
	if !strings.Contains(recs[0].Reason, "would arm watchdog") {
		t.Errorf("Reason = %q, want it to mention would arm watchdog", recs[0].Reason)
	}
}

func TestPipeline_PermissionPrompt_BadTranscript_StillDelivers(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))

	in := HookInput{
		SessionID:        "sess-2",
		CWD:              "/home/user/project",
		TranscriptPath:   "/nonexistent/path.jsonl",
		HookEventName:    "Notification",
		NotificationType: "permission_prompt",
		Message:          "Claude needs permission to run rm",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [blocked]") {
		t.Errorf("stdout = %q, want DRY RUN blocked line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Claude needs permission to run rm") {
		t.Errorf("stdout = %q, want it to contain the message", stdout.String())
	}
}

func TestPipeline_Stop_Clean_ComposeVerdict_DryRunLine(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"ship it","body":"all tests green","reason":"turn ended cleanly"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-3", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "ship it") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

// TestPipeline_Stop_ComposeJudgeError_FallbackUsesLastAssistantMessage pins
// the Stop-event compose fallback: when the judge errors, the deterministic
// body must come from LastAssistantMessage (the field Stop actually
// populates), describing the turn that just ended — never a hardcoded
// "turn ended" when there's real content to report.
func TestPipeline_Stop_ComposeJudgeError_FallbackUsesLastAssistantMessage(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-4", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "All done here.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "project · testhost") {
		t.Errorf("stdout = %q, want fallback title located by host", stdout.String())
	}
	if !strings.Contains(stdout.String(), "All done here.") {
		t.Errorf("stdout = %q, want it to contain last assistant message tail", stdout.String())
	}
}

// TestPipeline_Stop_ComposeJudgeError_FallbackEmptyMessage_TurnEnded pins the
// Stop-event compose fallback's last resort: when LastAssistantMessage is
// empty (a Stop with nothing to quote), the body falls back to the literal
// "turn ended" — this is the one case where that text is truthful.
func TestPipeline_Stop_ComposeJudgeError_FallbackEmptyMessage_TurnEnded(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-4b", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "project · testhost") {
		t.Errorf("stdout = %q, want fallback title located by host", stdout.String())
	}
	if !strings.Contains(stdout.String(), "turn ended") {
		t.Errorf("stdout = %q, want it to contain the turn-ended fallback body", stdout.String())
	}
}

// TestPipeline_Notification_IdlePrompt_ComposeJudgeError_FallbackUsesMessage
// pins the Notification-event compose fallback: an idle_prompt reaching the
// compose backstop (no goal, no live tasks — the goal_none fixture) with a
// judge error must build its body from in.Message, the field Notification
// events actually populate — never from LastAssistantMessage, which is
// always empty on this event and would silently produce the wrong text.
func TestPipeline_Notification_IdlePrompt_ComposeJudgeError_FallbackUsesMessage(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-5", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: eventNotification, NotificationType: "idle_prompt",
		Message: "Waiting on you to review the diff.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "project · testhost") {
		t.Errorf("stdout = %q, want fallback title located by host", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Waiting on you to review the diff.") {
		t.Errorf("stdout = %q, want it to contain the notification message tail", stdout.String())
	}
	if strings.Contains(stdout.String(), "turn ended") {
		t.Errorf("stdout = %q, must never claim a turn ended on a Notification event", stdout.String())
	}
}

// TestPipeline_Notification_IdlePrompt_ComposeJudgeError_FallbackEmptyMessage_SessionIdle
// pins the Notification-event compose fallback's last resort: when in.Message
// is empty, the body must describe an idle session waiting for input — never
// the Stop-only "turn ended" text, which would misdescribe a session that
// never stopped.
func TestPipeline_Notification_IdlePrompt_ComposeJudgeError_FallbackEmptyMessage_SessionIdle(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-5b", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: eventNotification, NotificationType: "idle_prompt",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "project · testhost") {
		t.Errorf("stdout = %q, want fallback title located by host", stdout.String())
	}
	if !strings.Contains(stdout.String(), "session idle — waiting for input") {
		t.Errorf("stdout = %q, want the session-idle fallback body", stdout.String())
	}
	if strings.Contains(stdout.String(), "turn ended") {
		t.Errorf("stdout = %q, must never claim a turn ended on a Notification event", stdout.String())
	}
}

func TestPipeline_Stop_LiveTasks_DecideNotifyFalse_Silent(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"parked, still building"}`)

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-5", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no DRY RUN line)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "parked, still building") {
		t.Errorf("Reason = %q, want verdict reason", recs[0].Reason)
	}
}

// TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Delivers exercises the
// decide-mode delivery branch: verdict.Notify true, urgency not blocked, so
// the pipeline actually delivers (here: the DryRun line for what the skill
// brief calls the parked-dev-server ping). Nothing gates this path on
// whether anyone is at the terminal — a decide-mode notify:true verdict
// always sends.
func TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Delivers(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"dev server ready","body":"parked and listening on :3000","reason":"r"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-8", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [done]") {
		t.Errorf("stdout = %q, want DRY RUN done line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "dev server ready") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

// TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Blocked_Delivers exercises a
// blocked-urgency decide-mode verdict: it delivers exactly like any other
// notify:true verdict, since no terminal-focus gate exists to bypass.
func TestPipeline_Stop_LiveTasks_DecideNotifyTrue_Blocked_Delivers(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"blocked","task":"needs a decision","body":"which approach?","reason":"r"}`,
	)

	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-9", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.HasPrefix(stdout.String(), "DRY RUN: [blocked]") {
		t.Errorf("stdout = %q, want DRY RUN blocked line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "needs a decision") {
		t.Errorf("stdout = %q, want it to contain the verdict task", stdout.String())
	}
}

// TestPipeline_Stop_Teammates_DecideNotifyFalse_SilentAndArms exercises the
// teammates Stop gate end to end: a transcript with a teammate spawn and a
// recent teammate reply (empty queue, no goal, no live tasks) reaches
// OutcomeSilent through the decide-mode judge (never compose) and arms the
// watchdog, and the digest handed to the judge carries the TEAMMATES section
// the rubric's teammates guidance depends on.
func TestPipeline_Stop_Teammates_DecideNotifyFalse_SilentAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)
	const judgeReason = "teammate still working, no reply yet"
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	const parentPIDSentinel = 424242
	p.ParentPID = parentPIDSentinel
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "teammates.jsonl")
	wantOffset := scannedBytes(t, transcript)

	sessionID := "sess-teammates"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no DRY RUN line)", stdout.String())
	}

	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Fatalf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}
	// Pins the rest of the arm request's forwarding: a broken ParentPID
	// plumbing path (pipeline.go's arm literal, or the daemon's per-frame
	// copy) would be invisible to a SessionID-only assertion, and
	// parentAlive treats a zero ParentPID as "no probe" — so a silently
	// dropped ParentPID would never surface as a dead-session exit either.
	got := wd.armed[0]
	if got.ParentPID != parentPIDSentinel {
		t.Errorf("armed[0].ParentPID = %d, want %d", got.ParentPID, parentPIDSentinel)
	}
	if got.Transcript != transcript {
		t.Errorf("armed[0].Transcript = %q, want %q", got.Transcript, transcript)
	}
	if got.Offset != wantOffset {
		t.Errorf("armed[0].Offset = %d, want %d", got.Offset, wantOffset)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, judgeReason) {
		t.Errorf("Reason = %q, want the judge's verdict reason", recs[0].Reason)
	}

	dump, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	if !strings.Contains(string(dump), "TEAMMATES") {
		t.Errorf("stdin did not carry the TEAMMATES digest section: %q", dump)
	}
}

// TestPipeline_Stop_GoalActive_NilWatchdog_NoOpNoPanic proves arm() is a
// safe no-op when Pipeline.Watchdog is unset (the hook client's inline
// fallback shape): a goal-active Stop must still resolve silent and log
// normally, with no "arm failed" record — that outcome no longer exists now
// that arming is an interface call, not a subprocess spawn that can fail.
func TestPipeline_Stop_GoalActive_NilWatchdog_NoOpNoPanic(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t))
	p.DryRun = false
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-nil-watchdog", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "goal active") {
		t.Errorf("Reason = %q, want it to mention the goal-active defer", recs[0].Reason)
	}
}

func TestPipeline_IdlePrompt_DedupeWindow_Silent(t *testing.T) {
	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, writeStubClaude(t))
	ds := newMemDedupeState()
	p.State = ds

	sessionID := "sess-7"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-30*time.Second), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: filepath.Join("testdata", "goal_none.jsonl"),
		HookEventName: "Notification", NotificationType: "idle_prompt",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (dedupe silence)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "dedupe") {
		t.Errorf("Reason = %q, want dedupe reason", recs[0].Reason)
	}
}

// TestPipeline_IdlePrompt_LiveTasks_SilentNoJudgeCallArmsWatchdog is the
// pipeline-level proof for the idle_prompt live-work gate (see decideNotification
// in decide.go): an idle_prompt Notification for a session with live
// background tasks resolves silent and arms the watchdog WITHOUT ever
// invoking the judge — the Stop decide-judge already ruled on this state
// ~60s earlier, so a second judge call here would be redundant, and the
// epic requires this path to make zero judge calls. STUB_DUMP_FILE proves
// non-invocation mechanically (not just via the outcome): the stub claude
// binary only ever writes it when actually executed, so its absence after
// Run proves the judge subprocess never ran.
func TestPipeline_IdlePrompt_LiveTasks_SilentNoJudgeCallArmsWatchdog(t *testing.T) {
	stubBin := writeStubClaude(t)
	dumpFile := filepath.Join(t.TempDir(), "dump.txt")
	t.Setenv("STUB_DUMP_FILE", dumpFile)

	var stdout bytes.Buffer
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-idle-live-tasks"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Notification", NotificationType: "idle_prompt",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, statErr := os.Stat(dumpFile); statErr == nil {
		t.Fatal("dump file exists: the judge stub was invoked, but idle_prompt with live tasks must never call it")
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("stat dump file: %v", statErr)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (silent, no DRY RUN line)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "live work: watchdog covers") {
		t.Errorf("Reason = %q, want the live-work watchdog reason", recs[0].Reason)
	}

	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Fatalf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}
}

// TestPipeline_PermissionPrompt_IdenticalRepeat_SecondIsSilent is the
// end-to-end version of the blockedRepeatWindow gate: two identical
// permission_prompt events for the same session, roughly a minute apart,
// must produce exactly one send followed by one silent (dedupe) record —
// the real-world storm being fixed is "Claude needs your permission" firing
// 3x in 9 minutes with identical text.
func TestPipeline_PermissionPrompt_IdenticalRepeat_SecondIsSilent(t *testing.T) {
	stubBin := writeStubClaude(t)
	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	ds := newMemDedupeState()
	p.State = ds

	sessionID := "sess-perm-repeat"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt",
		Message: "Claude needs permission to run rm",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification on first ping", sent)
	}

	// Backdate the recorded send to simulate the second identical ping
	// arriving roughly a minute later — well inside blockedRepeatWindow.
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-1*time.Minute), in.Message); err != nil {
		t.Fatalf("backdating MarkNotified: %v", err)
	}

	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Errorf("sent = %+v, want still exactly one notification (second identical ping suppressed)", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 2 {
		t.Fatalf("log records = %d, want 2 (one send, one silent)", len(recs))
	}
	if recs[0].Outcome != OutcomeSend.String() {
		t.Errorf("first record Outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() {
		t.Errorf("second record Outcome = %q, want silent", recs[1].Outcome)
	}
	if !strings.Contains(recs[1].Reason, "dedupe: identical ping") {
		t.Errorf("second record Reason = %q, want identical-ping dedupe reason", recs[1].Reason)
	}
}

// TestPipeline_Regression_ComposePath_NonDryRun_StdoutEmpty pins that a
// non-goal outcome (the existing compose path) writes NOTHING to stdout,
// even outside DryRun — this task is the only one permitted to add stdout
// writes to the pipeline, and this test guards against a future change
// accidentally leaking the new block-JSON write (or anything else) onto an
// unrelated Stop's stdout.
func TestPipeline_Regression_ComposePath_NonDryRun_StdoutEmpty(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"ship it","body":"all tests green","reason":"turn ended cleanly"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-regression-1", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (non-goal outcome must never write to stdout)", stdout.String())
	}
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one notification (compose path still delivers normally)", sent)
	}
}

// TestScanTranscript_GoalIncidentDaemon_LiveBackgroundBashTask is the direct
// ScanTranscript-level assertion for the grailquest-incident fixture: a goal
// armed via a sentinel goal_status record, with exactly one live task and no
// non-sentinel goal_status verdict anywhere in the transcript, registered as
// a background Bash launch (not an Agent) — the shape that Claude Code's
// built-in /goal evaluator defers on, which is what stalled the goal in the
// real incident this fixture reproduces.
func TestScanTranscript_GoalIncidentDaemon_LiveBackgroundBashTask(t *testing.T) {
	f := openFixture(t, "goal_incident_daemon.jsonl")
	res, err := ScanTranscript(f)
	if err != nil {
		t.Fatalf("ScanTranscript returned error: %v", err)
	}

	if res.Goal.Status != GoalActive {
		t.Errorf("Status = %v, want GoalActive", res.Goal.Status)
	}
	if res.Goal.Condition != goalIncidentDaemonCondition {
		t.Errorf("Condition = %q, want %q", res.Goal.Condition, goalIncidentDaemonCondition)
	}

	if len(res.LiveTasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1 (the parked daemon): %+v", len(res.LiveTasks), res.LiveTasks)
	}
	task := res.LiveTasks[0]
	if task.Kind != TaskBash {
		t.Errorf("Kind = %v, want TaskBash (a background daemon, not an Agent)", task.Kind)
	}
	if task.ID != "bgtaskid001" {
		t.Errorf("ID = %q, want bgtaskid001", task.ID)
	}
	wantDetail := "exec /tmp/grailquestd-play --addr 127.0.0.1:8080 --content content"
	if task.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", task.Detail, wantDetail)
	}
}

// TestPipeline_Stop_ComposeJudgeError_SuppressedWithinFailOpenWindow exercises
// the compose-mode fail-open rate limit: a judge error within failOpenWindow
// of the session's last notification must be suppressed (silent, watchdog
// armed) rather than sent again, logged as the distinct "judge error"
// outcome so it stays distinguishable from a genuine silent verdict.
func TestPipeline_Stop_ComposeJudgeError_SuppressedWithinFailOpenWindow(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom")

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin)
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "goal_none.jsonl")

	sessionID := "sess-compose-suppressed"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-2*time.Minute), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "All done here.",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (suppressed judge error must not send)", stdout.String())
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "suppressed") {
		t.Errorf("Reason = %q, want it to mention suppressed", recs[0].Reason)
	}
	if !strings.Contains(recs[0].Reason, "would arm watchdog") {
		t.Errorf("Reason = %q, want it to mention would arm watchdog (DryRun)", recs[0].Reason)
	}
}

// TestPipeline_Stop_DecideJudgeError_NeverNotified_SendsFallback pins the
// baseline decide-mode judge-error fallback (never notified before, so
// SinceLastNotify is negative): it must send exactly as before the fail-open
// rate limit existed — the epic's anti-pattern guard is that the FIRST judge
// error may never fail to notify.
func TestPipeline_Stop_DecideJudgeError_NeverNotified_SendsFallback(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-decide-never-notified", CWD: "/home/user/project", TranscriptPath: transcript,
		HookEventName: "Stop", LastAssistantMessage: "background build still going",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one fallback notification", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record", recs)
	}
}

// TestPipeline_Stop_DecideJudgeError_SuppressedWithinFailOpenWindow_NoSend
// exercises the decide-mode fail-open rate limit: within failOpenWindow of
// the last notification, a judge error must resolve silent (watchdog armed,
// last-notify untouched) rather than sending a repeat fallback ping.
func TestPipeline_Stop_DecideJudgeError_SuppressedWithinFailOpenWindow_NoSend(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-suppressed"
	notifiedAt := time.Now().Add(-3 * time.Minute)
	if err := ds.MarkNotified(context.Background(), sessionID, notifiedAt, "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification (suppressed within failOpenWindow)", sent)
	}
	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Errorf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}
	if got := ds.SinceLastNotify(context.Background(), sessionID, time.Now()); got < 2*time.Minute {
		t.Errorf("SinceLastNotify() = %v, want last-notify unchanged (suppressed path must not send)", got)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Outcome != "judge error" {
		t.Errorf("Outcome = %q, want %q", recs[0].Outcome, "judge error")
	}
	if !strings.Contains(recs[0].Reason, "suppressed") {
		t.Errorf("Reason = %q, want it to mention suppressed", recs[0].Reason)
	}
}

// TestPipeline_Stop_DecideJudgeError_AfterFailOpenWindow_SendsAgain pins the
// window's far edge: once SinceLastNotify has cleared failOpenWindow, a
// judge error sends exactly as it would have before the rate limit existed.
func TestPipeline_Stop_DecideJudgeError_AfterFailOpenWindow_SendsAgain(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_EXIT", "1")
	t.Setenv("STUB_STDERR", "boom: judge unavailable")

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	ds := newMemDedupeState()
	p.State = ds
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-decide-after-window"
	if err := ds.MarkNotified(context.Background(), sessionID, time.Now().Add(-11*time.Minute), "prior"); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}

	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want one fallback notification (window elapsed)", sent)
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSend.String() {
		t.Fatalf("records = %+v, want one send record", recs)
	}
}

// TestPipeline_Stop_ComposeVerdict_RetriedWithoutModel_AppendsReasonSuffix
// spot-checks the compose-mode path: a verdict whose RetriedWithoutModel is
// true must have its decision-log Reason carry the retry annotation, so an
// operator reading the log can see when the judge's no-model retry path ran.
func TestPipeline_Stop_ComposeVerdict_RetriedWithoutModel_AppendsReasonSuffix(t *testing.T) {
	stubBin := writeRetryStubClaude(t)
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("STUB_CALL_LOG", callLog)
	t.Setenv("STUB_RETRY_MODE", "invalid_then_ok")

	var stdout bytes.Buffer
	p, logPath := newTestPipeline(t, &stdout, stubBin)
	transcript := copyFixture(t, "goal_none.jsonl")

	in := HookInput{
		SessionID: "sess-retry-compose", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("log records = %d, want 1: %+v", len(recs), recs)
	}
	if !strings.Contains(recs[0].Reason, "judge retried without --model") {
		t.Errorf("Reason = %q, want it to mention the retry", recs[0].Reason)
	}
}

func TestPipeline_SendTitles_LocusByWorkspaceAndBroadcastByHost(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))
	p.Workspace = "mercury"

	// A same-session event (permission prompt) locates by workspace.
	in := HookInput{
		SessionID: "sess-loc-1", CWD: "/home/user/grailquest", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "permission_prompt", Message: "allow rm?",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "grailquest · mercury") {
		t.Errorf("stdout = %q, want permission title located by workspace", stdout.String())
	}

	// A broadcast is about a headless job, so the receiving session's
	// workspace would mislead: it locates by host instead.
	stdout.Reset()
	in = HookInput{
		SessionID: "sess-loc-2", CWD: "/home/user/grailquest", TranscriptPath: "/nonexistent",
		HookEventName: "Notification", NotificationType: "agent_needs_input", Message: "remote job needs your input",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "grailquest · testhost") {
		t.Errorf("stdout = %q, want broadcast title located by host", stdout.String())
	}
}

// TestPipeline_Stop_GoalActive_LiveTasks_DefersSilentNoBlock is the keystone
// regression for the goal-defer epic: an active goal with one parked live
// task (the grailquest-incident daemon shape) must resolve silent and arm
// the watchdog, deferring goal continuation entirely to Claude Code's
// built-in /goal — it must NEVER emit {"decision":"block"}, even when a
// stubbed judge answer is shaped like the old goal-judgment stack's
// "parked, unmet" verdict (which used to trigger exactly that block).
func TestPipeline_Stop_GoalActive_LiveTasks_DefersSilentNoBlock(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv("STUB_STDOUT", `{"tasks":"parked","goal_met":false,"reason":"still needs the restart script"}`)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "goal_incident_daemon.jsonl")

	sessionID := "sess-defer-keystone"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if strings.Contains(stdout.String(), `"decision":"block"`) {
		t.Fatalf("stdout = %q, must never contain a block decision", stdout.String())
	}
	if len(sent) != 0 {
		t.Errorf("sent = %+v, want no notification (silent defer)", sent)
	}

	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Errorf("armed = %+v, want exactly one arm for %s", wd.armed, sessionID)
	}

	recs := readDecisionLog(t, logPath)
	if len(recs) != 1 || recs[0].Outcome != OutcomeSilent.String() {
		t.Fatalf("records = %+v, want one silent record", recs)
	}
	if !strings.Contains(recs[0].Reason, "goal active") {
		t.Errorf("Reason = %q, want it to mention the goal-active defer", recs[0].Reason)
	}
}

// TestPackageSource_NeverProducesBlockDecision is the structural invariant
// backing the epic's core requirement: notify must never emit
// {"decision":"block"}. It reads every non-test .go file in this package and
// fails if either the raw JSON literal or the equivalent Go struct-literal
// form (Decision: "block") appears anywhere — this test file is the sole
// permitted exception, since it must name the literal to check for it.
func TestPackageSource_NeverProducesBlockDecision(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}
	const rawJSON = `"decision":"block"`
	const structLiteral = `Decision: "block"`
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		src := string(data)
		if strings.Contains(src, rawJSON) {
			t.Errorf("%s contains the raw block-decision JSON literal %q", name, rawJSON)
		}
		if strings.Contains(src, structLiteral) {
			t.Errorf("%s contains a block-decision struct literal %q", name, structLiteral)
		}
	}
}

// TestPipeline_TwoJudgedStops_SecondLosesClaimAndArms reproduces the
// double-ping the ClaimSend gate exists to kill (two Stop events seconds
// apart, both judged notify=true — observed 2026-07-08 as two "Awaiting …
// decision" pings 10s apart): the first Run's send claims the session, so
// the second Run's identical verdict must resolve silent with a post-judge
// dedupe reason — and still arm the watchdog, since the live work behind the
// suppressed ping is uncovered otherwise.
func TestPipeline_TwoJudgedStops_SecondLosesClaimAndArms(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"blocked","task":"needs a decision","body":"which approach?","reason":"r"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, logPath := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	p.State = newMemDedupeState()
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "tasks_live.jsonl")

	sessionID := "sess-claim-race"
	in := HookInput{
		SessionID: sessionID, CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	for i := 1; i <= 2; i++ {
		if err := p.Run(context.Background(), in); err != nil {
			t.Fatalf("Run() #%d error = %v", i, err)
		}
	}

	if len(sent) != 1 {
		t.Fatalf("sent %d notifications, want exactly 1 (second Stop must lose the claim)", len(sent))
	}
	recs := readDecisionLog(t, logPath)
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want 2", recs)
	}
	if recs[0].Outcome != OutcomeSend.String() {
		t.Errorf("first outcome = %q, want send", recs[0].Outcome)
	}
	if recs[1].Outcome != OutcomeSilent.String() || !strings.Contains(recs[1].Reason, "post-judge") {
		t.Errorf("second record = %+v, want silent with a post-judge dedupe reason", recs[1])
	}
	// The suppressed decide-mode send must keep watchdog coverage on the
	// live tasks, exactly as a pre-judge silent outcome would.
	if len(wd.armed) != 1 || wd.armed[0].SessionID != sessionID {
		t.Fatalf("armed = %+v, want exactly one arm for %s (from the suppressed second Stop)", wd.armed, sessionID)
	}
}

// TestPipeline_JudgedSend_BodyCarriesLocator pins the where-did-this-come-from
// trailer on judged sends: their title carries the judge's task label, so the
// body must end with the tmux locator and host.
func TestPipeline_JudgedSend_BodyCarriesLocator(t *testing.T) {
	stubBin := writeStubClaude(t)
	t.Setenv(
		"STUB_STDOUT",
		`{"notify":true,"urgency":"done","task":"dev server ready","body":"parked on :3000","reason":"r"}`,
	)

	var stdout bytes.Buffer
	var sent []capturedRequest
	p, _ := newGoalTestPipeline(t, &stdout, stubBin)
	p.Sender = stubSenderRecording(&sent)
	p.Workspace = "earth:3"
	p.Host = "vermissian"
	transcript := copyFixture(t, "tasks_live.jsonl")

	in := HookInput{
		SessionID: "sess-locator", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(sent))
	}
	if want := "parked on :3000\n\n— earth:3 @ vermissian"; sent[0].Body != want {
		t.Errorf("Body = %q, want %q", sent[0].Body, want)
	}
}

// TestLocatorSuffix covers the trailer's fallback shapes: both segments,
// workspace only, host only, neither.
func TestLocatorSuffix(t *testing.T) {
	tests := []struct {
		name, workspace, host, want string
	}{
		{name: "workspace and host", workspace: "earth:3", host: "vermissian", want: "\n\n— earth:3 @ vermissian"},
		{name: "workspace only", workspace: "earth:3", want: "\n\n— earth:3"},
		{name: "host only", host: "vermissian", want: "\n\n— vermissian"},
		{name: "neither", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := locatorSuffix(tt.workspace, tt.host); got != tt.want {
				t.Errorf("locatorSuffix(%q, %q) = %q, want %q", tt.workspace, tt.host, got, tt.want)
			}
		})
	}
}

// TestPipeline_Arm_ForwardsWorkspace pins the Workspace plumbing into
// WatchdogArmRequest: the watchdog fires long after the arming hook, so a
// dropped Workspace would silently produce locator-less watchdog pings.
func TestPipeline_Arm_ForwardsWorkspace(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))
	p.DryRun = false
	p.Workspace = "earth:3"
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-arm-ws", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.armed) != 1 || wd.armed[0].Workspace != "earth:3" {
		t.Fatalf("armed = %+v, want one arm carrying Workspace earth:3", wd.armed)
	}
}

// TestPipeline_Arm_GoalActive_SetsGoalArmedTrue pins GoalArmed plumbing at
// the arm site: a scan whose Goal.Status is GoalActive must produce a
// WatchdogArmRequest with GoalArmed true, since that watchdog's first wakes
// are the delivery path for a met/failed verdict landing seconds later.
func TestPipeline_Arm_GoalActive_SetsGoalArmedTrue(t *testing.T) {
	var stdout bytes.Buffer
	p, _ := newTestPipeline(t, &stdout, writeStubClaude(t))
	p.DryRun = false
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "goal_active_set.jsonl")

	in := HookInput{
		SessionID: "sess-arm-goal-active", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.armed) != 1 || !wd.armed[0].GoalArmed {
		t.Fatalf("armed = %+v, want one arm carrying GoalArmed true", wd.armed)
	}
}

// TestPipeline_Arm_GoalNone_GoalArmedFalse pins the other side: a scan with
// no goal in progress must not front-load the fast wake schedule. Mirrors
// TestPipeline_Stop_Teammates_DecideNotifyFalse_SilentAndArms's setup (a
// no-goal, no-live-task transcript that reaches OutcomeSilent through the
// decide-mode judge and arms the watchdog) since that is the arming path
// this fixture actually exercises.
func TestPipeline_Arm_GoalNone_GoalArmedFalse(t *testing.T) {
	stubBin := writeStubClaude(t)
	const judgeReason = "teammate still working, no reply yet"
	t.Setenv("STUB_STDOUT", `{"notify":false,"urgency":"info","task":"t","body":"b","reason":"`+judgeReason+`"}`)

	var stdout bytes.Buffer
	p, _ := newGoalTestPipeline(t, &stdout, stubBin)
	wd := &fakeWatchdog{}
	p.Watchdog = wd
	transcript := copyFixture(t, "teammates.jsonl")

	in := HookInput{
		SessionID: "sess-arm-goal-none", CWD: "/home/user/project", TranscriptPath: transcript, HookEventName: "Stop",
	}
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(wd.armed) != 1 || wd.armed[0].GoalArmed {
		t.Fatalf("armed = %+v, want one arm carrying GoalArmed false", wd.armed)
	}
}
