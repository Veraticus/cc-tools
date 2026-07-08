package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJobState creates configDir/jobs/<id>/state.json with the given
// fields, mirroring the harness's background-job layout.
func writeJobState(t *testing.T, configDir, id, name, sessionID, cwd string) {
	t.Helper()
	dir := filepath.Join(configDir, "jobs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating job dir: %v", err)
	}
	body := `{"name":` + jsonString(name) + `,"sessionId":` + jsonString(sessionID) +
		`,"cwd":` + jsonString(cwd) + `,"state":"blocked"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing state.json: %v", err)
	}
}

func jsonString(s string) string {
	out := `"`
	for _, r := range s {
		switch r {
		case '"', '\\':
			out += `\` + string(r)
		default:
			out += string(r)
		}
	}
	return out + `"`
}

func TestClaimWindowFor_PerNotificationType(t *testing.T) {
	if got := claimWindowFor(notifTypeAgentCompleted); got != broadcastClaimWindowCompleted {
		t.Errorf("claimWindowFor(agent_completed) = %v, want %v", got, broadcastClaimWindowCompleted)
	}
	if got := claimWindowFor(notifTypeAgentNeedsInput); got != broadcastClaimWindow {
		t.Errorf("claimWindowFor(agent_needs_input) = %v, want %v", got, broadcastClaimWindow)
	}
}

func TestResolveBroadcastSource_MatchesNamePrefix(t *testing.T) {
	cfg := t.TempDir()
	writeJobState(t, cfg, "abc12345", "pam grants alert ingestion", "abc12345-full-session-id", "/work/CSRE-813")

	if !resolveBroadcastSource(filepath.Join(cfg, "jobs"), "pam grants alert ingestion needs your input: Want it?") {
		t.Fatal("resolveBroadcastSource() = false, want match")
	}
}

// TestResolveBroadcastSource_PrefixNestedNamesBothMatch replaces the old
// longest-match-wins assertion: resolveBroadcastSource no longer returns a
// specific job's fields (see broadcastJob and BroadcastFacts.Local) — it
// only reports whether SOME local job owns the broadcast, so disambiguating
// among prefix-nested names is no longer this function's job. Two jobs whose
// names both prefix the message must still resolve to a match.
func TestResolveBroadcastSource_PrefixNestedNamesBothMatch(t *testing.T) {
	cfg := t.TempDir()
	writeJobState(t, cfg, "short001", "fix tests", "short-session", "/p/short")
	writeJobState(t, cfg, "long0001", "fix tests and lint", "long-session", "/p/long")

	if !resolveBroadcastSource(filepath.Join(cfg, "jobs"), "fix tests and lint finished") {
		t.Fatal("resolveBroadcastSource() = false, want match (prefix-nested job names)")
	}
}

// TestResolveBroadcastSource_NoMatchAndBadInputs covers cases where no local
// job should be treated as the broadcast's source. The path-traversal
// sessionId guard this test used to cover is gone along with the SessionID
// field itself (see broadcastJob): resolveBroadcastSource no longer reads or
// returns a job's session ID, so there is nothing left for a malicious value
// in that field to attack.
func TestResolveBroadcastSource_NoMatchAndBadInputs(t *testing.T) {
	cfg := t.TempDir()
	writeJobState(t, cfg, "abc12345", "known job", "known-session", "/p/known")

	if resolveBroadcastSource(filepath.Join(cfg, "jobs"), "unrelated message") {
		t.Error("unrelated message matched, want no match")
	}
	if resolveBroadcastSource(filepath.Join(cfg, "jobs"), "") {
		t.Error("empty message matched, want no match")
	}
	if resolveBroadcastSource(filepath.Join(cfg, "missing"), "known job finished") {
		t.Error("missing jobs dir matched, want no match")
	}
}

func TestBroadcastFacts_NonBroadcastEventsAreNil(t *testing.T) {
	p := Pipeline{StateBase: t.TempDir()}
	now := time.Now()

	if got := p.broadcastFacts(context.Background(), HookInput{HookEventName: "Stop"}, now); got != nil {
		t.Errorf("broadcastFacts(Stop) = %+v, want nil", got)
	}
	perm := HookInput{HookEventName: "Notification", NotificationType: "permission_prompt"}
	if got := p.broadcastFacts(context.Background(), perm, now); got != nil {
		t.Errorf("broadcastFacts(permission_prompt) = %+v, want nil", got)
	}
	agent := HookInput{HookEventName: "Notification", NotificationType: "agent_needs_input", AgentID: "a1"}
	if got := p.broadcastFacts(context.Background(), agent, now); got != nil {
		t.Errorf("broadcastFacts(agent context) = %+v, want nil", got)
	}
}

// claimBroadcastSpy is a DedupeState whose other methods report "never
// notified" and whose ClaimBroadcast records whether it was ever called —
// used to assert that a resolved-local broadcast never reaches the shared
// claim ledger at all (see TestBroadcastFacts_LocalJobDoesNotWriteClaim).
type claimBroadcastSpy struct {
	called *bool
}

func (claimBroadcastSpy) SinceLastNotify(context.Context, string, time.Time) time.Duration {
	return neverNotifiedDuration
}

func (claimBroadcastSpy) SinceLastNotifySame(context.Context, string, time.Time, string) time.Duration {
	return neverNotifiedDuration
}

func (claimBroadcastSpy) MarkNotified(context.Context, string, time.Time, string) error { return nil }

func (s claimBroadcastSpy) ClaimBroadcast(context.Context, string, time.Duration, time.Time, bool) bool {
	*s.called = true
	return true
}

// TestBroadcastFacts_LocalJobDefersRegardlessOfSourceSendTiming replaces the
// deleted broadcastCoveredWindow heuristic's coverage: ownership of a
// broadcast that resolves to a local job is structural, not a function of
// when (or whether) the source session has sent its own notification yet —
// broadcastFacts never even consults dedupe state on the Local path (see
// TestBroadcastFacts_LocalJobDoesNotWriteClaim), so there is nothing left to
// seed here; this just pins that every notifyType still resolves Local.
func TestBroadcastFacts_LocalJobDefersRegardlessOfSourceSendTiming(t *testing.T) {
	tests := []struct {
		name       string
		notifyType string
	}{
		{name: "agent_needs_input", notifyType: "agent_needs_input"},
		{name: "agent_completed", notifyType: "agent_completed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			cfg := t.TempDir()
			now := time.Now()
			writeJobState(t, cfg, "job00001", "deploy the widget", "job-session-id", "/projects/widget")

			p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}}
			in := HookInput{
				HookEventName: "Notification", NotificationType: tt.notifyType,
				Message: "deploy the widget needs your input: proceed?",
			}
			facts := p.broadcastFacts(context.Background(), in, now)
			if facts == nil {
				t.Fatal("broadcastFacts() = nil, want facts")
			}
			if !facts.Local {
				t.Error("Local = false, want true (broadcast resolves to a job in this host's jobs dir)")
			}
			if facts.Duplicate {
				t.Error("Duplicate = true, want false (a local broadcast is never claimed)")
			}
		})
	}
}

// TestBroadcastFacts_LocalJobDoesNotWriteClaim asserts a resolved-local
// broadcast never touches the shared claim ledger: claiming for an event
// this session will never deliver is pointless state, so broadcastFacts must
// resolve the source before it ever calls ClaimBroadcast.
func TestBroadcastFacts_LocalJobDoesNotWriteClaim(t *testing.T) {
	base := t.TempDir()
	cfg := t.TempDir()
	now := time.Now()
	writeJobState(t, cfg, "job00001", "deploy the widget", "job-session-id", "/projects/widget")

	var claimed bool
	p := Pipeline{
		StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}, State: claimBroadcastSpy{called: &claimed},
	}
	in := HookInput{
		HookEventName: "Notification", NotificationType: "agent_needs_input",
		Message: "deploy the widget needs your input: proceed?",
	}
	if facts := p.broadcastFacts(context.Background(), in, now); facts == nil || !facts.Local {
		t.Fatalf("broadcastFacts() = %+v, want Local", facts)
	}

	if claimed {
		t.Error("ClaimBroadcast was called for a resolved-local broadcast, want it never reached")
	}
}

// TestBroadcastFacts_UnresolvedBroadcastStillUsesClaimPath covers the other
// side of the ownership split: a broadcast that does not resolve to a local
// job (no matching job state in this host's jobs dir) is unaffected by the
// Local change and still runs the first-claimant-wins dedupe.
func TestBroadcastFacts_UnresolvedBroadcastStillUsesClaimPath(t *testing.T) {
	base := t.TempDir()
	cfg := t.TempDir()
	now := time.Now()

	p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}, State: newMemDedupeState()}
	in := HookInput{
		HookEventName: "Notification", NotificationType: "agent_needs_input",
		Message: "deploy the widget needs your input: proceed?",
	}
	facts := p.broadcastFacts(context.Background(), in, now)
	if facts == nil || facts.Local || facts.Duplicate {
		t.Fatalf("broadcastFacts() = %+v, want unresolved first claim (not Local, not Duplicate)", facts)
	}

	// A second session receiving the same broadcast loses the claim.
	second := p.broadcastFacts(context.Background(), in, now.Add(500*time.Millisecond))
	if second == nil || !second.Duplicate {
		t.Fatalf("second receiver facts = %+v, want Duplicate", second)
	}
}
