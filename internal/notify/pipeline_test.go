package notify

import (
	"bufio"
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

type composeCall struct {
	Input ComposeInput
	Label ComposeLabel
}

type recordingComposer struct {
	mutex  sync.Mutex
	calls  []composeCall
	result ComposeResult
	err    error
}

func (c *recordingComposer) Compose(
	_ context.Context,
	input ComposeInput,
	label ComposeLabel,
) (ComposeResult, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.calls = append(c.calls, composeCall{Input: input, Label: label})
	return c.result, c.err
}

func (c *recordingComposer) Calls() []composeCall {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([]composeCall(nil), c.calls...)
}

type panicComposer struct{}

func (panicComposer) Compose(context.Context, ComposeInput, ComposeLabel) (ComposeResult, error) {
	panic("composer must not be called")
}

type capturedNotification struct {
	Title    string
	Body     string
	Priority string
	Tags     string
}

type pipelineRoundTripFunc func(*http.Request) (*http.Response, error)

func (function pipelineRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func captureNotificationServer(t *testing.T) (*httptest.Server, <-chan capturedNotification) {
	t.Helper()
	requests := make(chan capturedNotification, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading notification body: %v", err)
		}
		requests <- capturedNotification{
			Title: req.Header.Get("Title"), Body: string(body),
			Priority: req.Header.Get("Priority"), Tags: req.Header.Get("Tags"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	return server, requests
}

func waitNotification(t *testing.T, requests <-chan capturedNotification) capturedNotification {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification")
		return capturedNotification{}
	}
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	return path
}

func conversationTranscript(t *testing.T, uuid, messageID string, malformedTail bool) string {
	t.Helper()
	assistant := map[string]any{
		"type":      "assistant",
		"timestamp": "2026-07-01T00:00:01Z",
		"message": map[string]any{
			"role": "assistant",
			"id":   messageID,
			"content": []map[string]string{{
				"type": "text", "text": "latest assistant text",
			}},
		},
	}
	if uuid != "" {
		assistant["uuid"] = uuid
	}
	user, err := json.Marshal(map[string]any{
		"type":      "user",
		"timestamp": "2026-07-01T00:00:00Z",
		"message": map[string]any{
			"role": "user", "content": "latest user text",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assistantJSON, err := json.Marshal(assistant)
	if err != nil {
		t.Fatal(err)
	}
	earlierUser := `{"type":"user","timestamp":"2026-06-30T23:59:58Z",` +
		`"message":{"role":"user","content":"earlier user text"}}`
	earlierAssistant := `{"type":"assistant","uuid":"earlier-assistant-uuid",` +
		`"timestamp":"2026-06-30T23:59:59Z","message":{"role":"assistant",` +
		`"content":[{"type":"text","text":"earlier assistant text"}]}}`
	lines := []string{earlierUser, earlierAssistant, string(user), string(assistantJSON)}
	if malformedTail {
		lines = append(lines, "{")
	}
	return writeTranscript(t, lines...)
}

func testPipeline(t *testing.T, server *httptest.Server, composer Composer) (Pipeline, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	return Pipeline{
		Composer:  composer,
		Sender:    Sender{URL: server.URL, Client: server.Client()},
		Log:       DecisionLog{Path: logPath},
		Host:      "testhost",
		Workspace: "earth:3",
	}, logPath
}

func readDecisionLog(t *testing.T, path string) []DecisionRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading decision log: %v", err)
	}
	var records []DecisionRecord
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var record DecisionRecord
		if unmarshalErr := json.Unmarshal(scanner.Bytes(), &record); unmarshalErr != nil {
			t.Fatalf("decoding decision record: %v", unmarshalErr)
		}
		records = append(records, record)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatalf("scanning decision log: %v", scanErr)
	}
	return records
}

func TestPipelineClaudeCompletionUsesReliableNativeIdentityAndLatestConversation(t *testing.T) {
	tests := []struct {
		name      string
		uuid      string
		messageID string
		wantID    string
	}{
		{name: "UUID takes precedence", uuid: "assistant-uuid", messageID: "message-id", wantID: "assistant-uuid"},
		{name: "message ID is reliable fallback", messageID: "message-id", wantID: "message-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			composer := &recordingComposer{result: ComposeResult{Body: "Composed completion summary."}}
			pipeline, logPath := testPipeline(t, server, composer)
			in := HookInput{
				Harness: harnessClaude, SessionID: "session-1", CompletionID: "stale-supplied-id",
				CWD: "/home/user/project", HookEventName: eventStop,
				TranscriptPath:       conversationTranscript(t, tt.uuid, tt.messageID, false),
				LastAssistantMessage: "hook fallback must not win",
			}

			if err := pipeline.Run(context.Background(), in); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			request := waitNotification(t, requests)
			if request.Title != "project · earth:3" || request.Body != "Composed completion summary." {
				t.Errorf("notification = %+v, want composed body with cwd/locator title", request)
			}
			if request.Priority != "4" || request.Tags != "white_check_mark" {
				t.Errorf("headers = priority %q tags %q, want done urgency", request.Priority, request.Tags)
			}
			calls := composer.Calls()
			if len(calls) != 1 {
				t.Fatalf("Compose calls = %d, want exactly one", len(calls))
			}
			if calls[0].Input != (ComposeInput{User: "latest user text", Assistant: "latest assistant text"}) {
				t.Errorf("Compose input = %+v, want latest scanned conversation", calls[0].Input)
			}
			if calls[0].Label != (ComposeLabel{Current: "", Refresh: false}) {
				t.Errorf("Compose label = %+v, want naming disabled", calls[0].Label)
			}

			records := readDecisionLog(t, logPath)
			if len(records) != 1 {
				t.Fatalf("records = %+v, want one", records)
			}
			record := records[0]
			if record.Harness != harnessClaude || record.SessionID != "session-1" ||
				record.CompletionID != tt.wantID || record.CompositionOutcome != compositionComposed {
				t.Errorf("record attribution/composition = %+v", record)
			}
			if record.Urgency != UrgencyDone || record.Outcome != OutcomeSend.String() {
				t.Errorf("record decision = %+v, want send/done", record)
			}
		})
	}
}

func TestPipelineRunImplicitClaudeUsesTranscriptIdentity(t *testing.T) {
	for _, tt := range []struct {
		name         string
		completionID string
	}{
		{name: "absent supplied ID"},
		{name: "stale supplied ID", completionID: "stale-hook-id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			composer := &recordingComposer{result: ComposeResult{Body: "Implicit Claude summary."}}
			pipeline, logPath := testPipeline(t, server, composer)
			if err := pipeline.Run(context.Background(), HookInput{
				SessionID: "implicit-session", CompletionID: tt.completionID,
				CWD: "/work/implicit", HookEventName: eventStop,
				TranscriptPath:       conversationTranscript(t, "implicit-uuid", "message-id", false),
				LastAssistantMessage: "stale fallback",
			}); err != nil {
				t.Fatal(err)
			}
			if request := waitNotification(t, requests); request.Body != "Implicit Claude summary." {
				t.Fatalf("request = %+v", request)
			}
			calls := composer.Calls()
			if len(calls) != 1 || calls[0].Input != (ComposeInput{
				User: "latest user text", Assistant: "latest assistant text",
			}) {
				t.Fatalf("Compose calls = %+v", calls)
			}
			record := readDecisionLog(t, logPath)[0]
			if record.Harness != harnessClaude || record.CompletionID != "implicit-uuid" {
				t.Fatalf("record = %+v", record)
			}
		})
	}
}

func TestPipelineProviderTurnCompleteComposesOnceWithNativeInput(t *testing.T) {
	for _, harness := range []string{harnessCodex, harnessPi} {
		t.Run(harness, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			composer := &recordingComposer{result: ComposeResult{Body: "Provider completion summary."}}
			pipeline, logPath := testPipeline(t, server, composer)
			in := HookInput{
				Harness: harness, SessionID: harness + "-session", CompletionID: harness + "-completion",
				CWD: "/work/provider-project", HookEventName: eventTurnComplete,
				Message: "provider user text", LastAssistantMessage: "provider assistant text",
			}
			if err := pipeline.Run(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			request := waitNotification(t, requests)
			if request.Priority != "4" || request.Body != "Provider completion summary." {
				t.Fatalf("notification = %+v, want composed done notification", request)
			}
			calls := composer.Calls()
			if len(calls) != 1 || calls[0].Input != (ComposeInput{
				User: "provider user text", Assistant: "provider assistant text",
			}) {
				t.Fatalf("Compose calls = %+v", calls)
			}
			record := readDecisionLog(t, logPath)[0]
			if record.Harness != harness || record.CompletionID != harness+"-completion" {
				t.Errorf("record = %+v, want original source and ID", record)
			}
		})
	}
}

func TestPipelineClaudeIdentityDegradationSkipsCompositionAndClearsStaleID(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "absent transcript"},
		{
			name: "unreadable transcript",
			path: filepath.Join(t.TempDir(), "missing.jsonl"),
		},
		{name: "empty transcript", path: writeTranscript(t)},
		{name: "malformed transcript", path: writeTranscript(t, "{")},
		{name: "assistant identity absent", path: conversationTranscript(t, "", "", false)},
		{
			name: "assistant identity unreliable",
			path: conversationTranscript(t, "assistant-uuid", "message-id", true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, logPath := testPipeline(t, server, panicComposer{})
			in := HookInput{
				Harness: harnessClaude, SessionID: "degraded", CompletionID: "stale-supplied-id",
				CWD: "/home/user/project", HookEventName: eventStop,
				TranscriptPath: tt.path, LastAssistantMessage: "Hook fallback summary.",
			}
			if err := pipeline.Run(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			request := waitNotification(t, requests)
			if request.Body != "Hook fallback summary." || request.Priority != "4" {
				t.Errorf("fallback notification = %+v", request)
			}
			record := readDecisionLog(t, logPath)[0]
			if record.CompletionID != "" {
				t.Errorf("CompletionID = %q, want stale supplied ID cleared", record.CompletionID)
			}
			if record.CompositionOutcome != compositionFallback ||
				record.CompositionError != compositionErrorIdentityUnavailable {
				t.Errorf("composition fields = %+v, want identity-unavailable fallback", record)
			}
		})
	}
}

func TestPipelineGoalActiveSuppressesBeforeIdentityDegradation(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	pipeline, logPath := testPipeline(t, server, panicComposer{})
	if err := pipeline.Run(context.Background(), HookInput{
		Harness: harnessClaude, SessionID: "active-goal", CompletionID: "stale-id",
		CWD: "/home/user/project", HookEventName: eventStop,
		TranscriptPath:       filepath.Join("testdata", "goal_active_set.jsonl"),
		LastAssistantMessage: "must remain silent",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-requests:
		t.Fatalf("sent = %+v, want structural silence", request)
	case <-time.After(100 * time.Millisecond):
	}
	record := readDecisionLog(t, logPath)[0]
	if record.Outcome != OutcomeSilent.String() || !strings.Contains(record.Reason, "goal active") {
		t.Fatalf("record = %+v, want goal-active silence", record)
	}
	if record.CompletionID != "" {
		t.Errorf("CompletionID = %q, want stale supplied ID cleared", record.CompletionID)
	}
}

func TestPipelineExplicitInputBypassesScanAndComposer(t *testing.T) {
	tests := []struct {
		notificationType string
		message          string
		fallback         string
	}{
		{notificationType: "permission_prompt", message: "allow command?", fallback: "needs permission"},
		{notificationType: "elicitation_dialog", message: "choose a region", fallback: "needs input"},
		{notificationType: "agent_needs_input", message: "answer the worker", fallback: "needs input"},
		{notificationType: "permission_prompt", fallback: "needs permission"},
	}
	for _, tt := range tests {
		t.Run(tt.notificationType+tt.message, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, _ := testPipeline(t, server, panicComposer{})
			if err := pipeline.Run(context.Background(), HookInput{
				Harness: harnessClaude, SessionID: "input", CWD: "/work/project",
				HookEventName: eventNotification, NotificationType: tt.notificationType,
				Message: tt.message, TranscriptPath: "/must/not/be/read",
			}); err != nil {
				t.Fatal(err)
			}
			request := waitNotification(t, requests)
			wantBody := tt.message
			if wantBody == "" {
				wantBody = tt.fallback
			}
			if request.Body != wantBody || request.Priority != "5" || request.Tags != "question" {
				t.Errorf("notification = %+v, want immediate blocked input", request)
			}
		})
	}
}

func TestPipelineExplicitInputRepeatsAreNeverQuieted(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	pipeline, _ := testPipeline(t, server, panicComposer{})
	input := HookInput{
		Harness: harnessClaude, SessionID: "repeat", CWD: "/work/project",
		HookEventName: eventNotification, NotificationType: "permission_prompt",
		Message: "allow command?",
	}
	for range 2 {
		if err := pipeline.Run(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if request := waitNotification(t, requests); request.Body != "allow command?" || request.Priority != "5" {
			t.Errorf("request = %+v, want unsuppressed blocked repeat", request)
		}
	}
}

func TestPipelineDeliveryFailureLogIsObservableAndSecretSafe(t *testing.T) {
	const rawDiagnostic = "RAW_TRANSPORT_DIAGNOSTIC"
	attempts := 0
	client := &http.Client{Transport: pipelineRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New(rawDiagnostic)
	})}
	logPath := filepath.Join(t.TempDir(), "decisions.jsonl")
	pipeline := Pipeline{
		Sender: Sender{
			URL:    "https://topic-user:topic-secret@notify.invalid/private-topic-token",
			Token:  "header-secret",
			Client: client,
		},
		Log: DecisionLog{Path: logPath}, Host: "testhost",
	}
	if err := pipeline.Run(context.Background(), HookInput{
		Harness: harnessClaude, SessionID: "send-failure", CWD: "/work/project",
		HookEventName: eventNotification, NotificationType: "permission_prompt",
		Message: "allow command?",
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("delivery attempts = %d, want Sender's initial attempt and retry", attempts)
	}
	records := readDecisionLog(t, logPath)
	if len(records) != 1 || !strings.Contains(records[0].Reason, "send failed") {
		t.Fatalf("records = %+v, want observable send-failure category", records)
	}
	wire, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"topic-user", "topic-secret", "private-topic-token", "header-secret", rawDiagnostic,
	} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("decision log leaked %q: %s", secret, wire)
		}
	}
}

func TestPipelineCompositionFailuresUseBoundedSafeFallback(t *testing.T) {
	tests := []struct {
		name      string
		composer  *recordingComposer
		wantError string
	}{
		{
			name:      "invalid request",
			composer:  &recordingComposer{err: errors.New("pi composer: invalid compose request")},
			wantError: compositionErrorInvalidRequest,
		},
		{
			name:      "helper unavailable",
			composer:  &recordingComposer{err: errors.New("pi composer: helper unavailable")},
			wantError: compositionErrorHelperUnavailable,
		},
		{
			name:      "helper execution failure",
			composer:  &recordingComposer{err: errors.New("pi composer: helper execution failed")},
			wantError: compositionErrorHelperExecution,
		},
		{
			name:      "helper timeout",
			composer:  &recordingComposer{err: errors.New("pi composer: helper timed out")},
			wantError: compositionErrorHelperTimeout,
		},
		{
			name:      "helper cancellation",
			composer:  &recordingComposer{err: errors.New("pi composer: helper canceled")},
			wantError: compositionErrorHelperCanceled,
		},
		{
			name:      "malformed protocol",
			composer:  &recordingComposer{err: errors.New("pi composer: invalid helper protocol")},
			wantError: compositionErrorInvalidProtocol,
		},
		{
			name:      "helper rejection",
			composer:  &recordingComposer{err: errors.New("pi composer: helper rejected request")},
			wantError: compositionErrorRejectedRequest,
		},
		{
			name:      "model or authentication unavailable",
			composer:  &recordingComposer{err: errors.New("pi composer: helper model unavailable")},
			wantError: compositionErrorModelUnavailable,
		},
		{
			name:      "generation failure",
			composer:  &recordingComposer{err: errors.New("pi composer: helper generation failed")},
			wantError: compositionErrorGenerationFailed,
		},
		{
			name:      "helper reported timeout",
			composer:  &recordingComposer{err: errors.New("pi composer: helper reported timeout")},
			wantError: compositionErrorReportedTimeout,
		},
		{
			name:      "helper invalid output",
			composer:  &recordingComposer{err: errors.New("pi composer: helper output invalid")},
			wantError: compositionErrorInvalidOutput,
		},
		{
			name:      "unknown error is categorized without leaking it",
			composer:  &recordingComposer{err: errors.New("SECRET raw subprocess stderr")},
			wantError: compositionErrorFailed,
		},
		{
			name:      "invalid injected result",
			composer:  &recordingComposer{result: ComposeResult{Body: "**markdown is invalid**"}},
			wantError: compositionErrorInvalidResult,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, logPath := testPipeline(t, server, tt.composer)
			longFinal := strings.Repeat("# Earlier **detail** [link](https://invalid) ", 10) +
				"`meaningful` tail"
			if err := pipeline.Run(context.Background(), HookInput{
				Harness: harnessCodex, SessionID: "failure", CompletionID: "turn-1",
				CWD: "/work/project", HookEventName: eventTurnComplete,
				Message: "user", LastAssistantMessage: longFinal,
			}); err != nil {
				t.Fatal(err)
			}
			request := waitNotification(t, requests)
			if len(request.Body) > maxNotificationFallbackBytes || !utf8.ValidString(request.Body) ||
				!strings.HasPrefix(request.Body, truncationEllipsis) ||
				!strings.HasSuffix(request.Body, "meaningful tail") || strings.Contains(request.Body, "`") {
				t.Errorf("fallback body = %q (%d bytes), want bounded plain-text tail", request.Body, len(request.Body))
			}
			if calls := tt.composer.Calls(); len(calls) != 1 {
				t.Errorf("Compose calls = %d, want one attempt with no retry", len(calls))
			}
			record := readDecisionLog(t, logPath)[0]
			if record.CompositionOutcome != compositionFallback || record.CompositionError != tt.wantError {
				t.Errorf("record = %+v, want categorized fallback", record)
			}
			wire, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(wire), "SECRET") {
				t.Fatalf("decision log leaked raw composer error: %s", wire)
			}
		})
	}
}

func TestPipelineInlineAndDryRunNeverCompose(t *testing.T) {
	t.Run("inline composer unavailable", func(t *testing.T) {
		server, requests := captureNotificationServer(t)
		defer server.Close()
		pipeline, logPath := testPipeline(t, server, nil)
		if err := pipeline.Run(context.Background(), HookInput{
			Harness: harnessPi, SessionID: "inline", CompletionID: "completion",
			CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "Inline fallback.",
		}); err != nil {
			t.Fatal(err)
		}
		if request := waitNotification(t, requests); request.Body != "Inline fallback." {
			t.Errorf("request = %+v", request)
		}
		record := readDecisionLog(t, logPath)[0]
		if record.CompositionError != compositionErrorUnavailable {
			t.Errorf("record = %+v, want unavailable category", record)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		server, requests := captureNotificationServer(t)
		defer server.Close()
		pipeline, logPath := testPipeline(t, server, panicComposer{})
		pipeline.DryRun = true
		var stdout strings.Builder
		pipeline.Stdout = &stdout
		if err := pipeline.Run(context.Background(), HookInput{
			Harness: harnessCodex, SessionID: "dry", CompletionID: "completion",
			CWD: "/work/project", HookEventName: eventTurnComplete,
			LastAssistantMessage: "Dry-run fallback.",
		}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "Dry-run fallback.") || !strings.Contains(stdout.String(), "[done]") {
			t.Errorf("stdout = %q, want done fallback rehearsal", stdout.String())
		}
		select {
		case request := <-requests:
			t.Fatalf("dry run delivered %+v", request)
		default:
		}
		record := readDecisionLog(t, logPath)[0]
		if record.CompositionError != compositionErrorDryRun {
			t.Errorf("record = %+v, want dry-run category", record)
		}
	})
}

func TestPipelineComposedBodyRemainsBoundedAndUrgencyCannotChange(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	composer := &recordingComposer{result: ComposeResult{Body: strings.Repeat("完", 100)}}
	pipeline, _ := testPipeline(t, server, composer)
	if err := pipeline.Run(context.Background(), HookInput{
		Harness: harnessPi, SessionID: "bounded", CompletionID: "completion",
		CWD: "/work/project", HookEventName: eventTurnComplete,
	}); err != nil {
		t.Fatal(err)
	}
	request := waitNotification(t, requests)
	if len(request.Body) > maxNotificationBodyBytes || !utf8.ValidString(request.Body) ||
		!strings.HasSuffix(request.Body, truncationEllipsis) {
		t.Errorf("body = %q (%d bytes), want bounded valid UTF-8", request.Body, len(request.Body))
	}
	if request.Priority != "4" || request.Tags != "white_check_mark" {
		t.Errorf("headers = %+v, model must not alter done urgency", request)
	}
}

func TestPipelineRunPreparedUsesFrozenClaudeSnapshot(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	composer := &recordingComposer{result: ComposeResult{Body: "Frozen summary."}}
	pipeline, _ := testPipeline(t, server, composer)
	path := conversationTranscript(t, "frozen-uuid", "frozen-message", false)
	prepared, err := PrepareEvent(HookInput{
		Harness: harnessClaude, SessionID: "frozen", HookEventName: eventStop,
		TranscriptPath: path, LastAssistantMessage: "stale hook fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = pipeline.RunPrepared(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	calls := composer.Calls()
	wantInput := ComposeInput{User: "latest user text", Assistant: "latest assistant text"}
	if len(calls) != 1 || calls[0].Input != wantInput {
		t.Fatalf("Compose calls after transcript removal = %+v", calls)
	}
	if request := waitNotification(t, requests); request.Body != "Frozen summary." {
		t.Fatalf("request = %+v", request)
	}
}

func TestPipelineRunPreparedClaudeFallbackUsesCapturedAssistant(t *testing.T) {
	server, requests := captureNotificationServer(t)
	defer server.Close()
	pipeline, _ := testPipeline(t, server, nil)
	path := conversationTranscript(t, "frozen-uuid", "frozen-message", false)
	prepared, err := PrepareEvent(HookInput{
		Harness: harnessClaude, SessionID: "frozen", HookEventName: eventStop,
		TranscriptPath: path, LastAssistantMessage: "stale hook fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = pipeline.RunPrepared(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if request := waitNotification(t, requests); request.Body != "latest assistant text" {
		t.Fatalf("fallback = %q, want captured assistant", request.Body)
	}
}

func TestPipelineRunPreparedIncompleteIdentityPairSkipsCompositionOnEveryRepeat(t *testing.T) {
	for _, tt := range []struct {
		name         string
		sessionID    string
		completionID string
	}{
		{name: "session only", sessionID: "session"},
		{name: "completion only", completionID: "completion"},
		{name: "both empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, logPath := testPipeline(t, server, panicComposer{})
			prepared := PreparedEvent{
				Version: 1, Harness: harnessPi, SessionID: tt.sessionID,
				Kind: eventKindCompletion, SourceEvent: eventTurnComplete,
				CompletionID: tt.completionID,
				Assistant:    strings.Repeat("bounded fallback words ", 20),
			}
			for range 2 {
				if err := pipeline.RunPrepared(context.Background(), prepared); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				request := waitNotification(t, requests)
				if len(request.Body) > maxNotificationFallbackBytes || request.Body == "" {
					t.Fatalf("fallback body = %q (%d bytes)", request.Body, len(request.Body))
				}
			}
			records := readDecisionLog(t, logPath)
			if len(records) != 2 {
				t.Fatalf("records = %+v", records)
			}
			for _, record := range records {
				if record.CompositionOutcome != compositionFallback ||
					record.CompositionError != compositionErrorIdentityUnavailable {
					t.Fatalf("record = %+v", record)
				}
			}
		})
	}
}

func TestPipelineRunPreparedRejectsInvalidEvent(t *testing.T) {
	err := (Pipeline{}).RunPrepared(context.Background(), PreparedEvent{})
	if err == nil || err.Error() != "notify: invalid prepared event" {
		t.Fatalf("RunPrepared() error = %v", err)
	}
}

func TestPipelineSessionEndAndSilentEventsNeverComposeOrSend(t *testing.T) {
	tests := []HookInput{
		{Harness: harnessClaude, SessionID: "end", HookEventName: eventSessionEnd},
		{Harness: harnessClaude, SessionID: "idle", HookEventName: eventNotification, NotificationType: "idle_prompt"},
		{
			Harness: harnessClaude, SessionID: "completed",
			HookEventName: eventNotification, NotificationType: "agent_completed",
		},
		{
			Harness: harnessPi, SessionID: "child", HookEventName: eventTurnComplete,
			CompletionID: "id", AgentType: "worker",
		},
	}
	for _, in := range tests {
		t.Run(in.SessionID, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, logPath := testPipeline(t, server, panicComposer{})
			if err := pipeline.Run(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-requests:
				t.Fatalf("silent event delivered %+v", request)
			case <-time.After(50 * time.Millisecond):
			}
			if record := readDecisionLog(t, logPath)[0]; record.Outcome != OutcomeSilent.String() {
				t.Errorf("record = %+v, want silent", record)
			}
		})
	}
}
