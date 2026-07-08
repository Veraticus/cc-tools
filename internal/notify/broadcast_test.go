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

func TestClaimBroadcast_FirstClaimWinsSecondLoses(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "agent_needs_input\nmsg", broadcastClaimWindow, now, false) {
		t.Fatal("first claim = false, want true")
	}
	if claimBroadcast(base, "agent_needs_input\nmsg", broadcastClaimWindow, now.Add(time.Second), false) {
		t.Fatal("second claim within window = true, want false")
	}
}

func TestClaimBroadcast_DistinctKeysBothWin(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "agent_needs_input\njob A asks", broadcastClaimWindow, now, false) {
		t.Fatal("claim A = false, want true")
	}
	if !claimBroadcast(base, "agent_needs_input\njob B asks", broadcastClaimWindow, now, false) {
		t.Fatal("claim B = false, want true")
	}
}

func TestClaimBroadcast_StaleClaimIsReclaimed(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "k", broadcastClaimWindow, now, false) {
		t.Fatal("initial claim = false, want true")
	}
	if !claimBroadcast(base, "k", broadcastClaimWindow, now.Add(broadcastClaimWindow+time.Second), false) {
		t.Fatal("claim after window = false, want true (stale claim should be reclaimed)")
	}
}

func TestClaimBroadcast_DryRunObservesWithoutWriting(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "k", broadcastClaimWindow, now, true) {
		t.Fatal("dry-run claim with no live claim = false, want true")
	}
	// The dry run must not have written anything: a subsequent real claim
	// still wins.
	if !claimBroadcast(base, "k", broadcastClaimWindow, now, false) {
		t.Fatal("real claim after dry run = false, want true (dry run must not write)")
	}
	// And a dry run against a live real claim reports the dedupe.
	if claimBroadcast(base, "k", broadcastClaimWindow, now.Add(time.Second), true) {
		t.Fatal("dry-run claim against live claim = true, want false")
	}
}

func TestClaimBroadcast_AgentCompletedWindowOutlivesAgentNeedsInputWindow(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "agent_completed\nfinished", claimWindowFor(notifTypeAgentCompleted), now, false) {
		t.Fatal("initial claim = false, want true")
	}
	// Past the agent_needs_input window (2m) but still well inside the
	// agent_completed window (30m): a re-broadcast of the same "job
	// finished" message must still be suppressed.
	past := now.Add(broadcastClaimWindow + time.Minute)
	if claimBroadcast(base, "agent_completed\nfinished", claimWindowFor(notifTypeAgentCompleted), past, false) {
		t.Fatal("reclaim at 3m = true, want false (agent_completed window is 30m)")
	}
	// Past the agent_completed window: the claim is stale and reclaimed.
	stale := now.Add(broadcastClaimWindowCompleted + time.Minute)
	if !claimBroadcast(base, "agent_completed\nfinished", claimWindowFor(notifTypeAgentCompleted), stale, false) {
		t.Fatal("reclaim past 30m = false, want true (stale claim should be reclaimed)")
	}
}

func TestClaimWindowFor_PerNotificationType(t *testing.T) {
	if got := claimWindowFor(notifTypeAgentCompleted); got != broadcastClaimWindowCompleted {
		t.Errorf("claimWindowFor(agent_completed) = %v, want %v", got, broadcastClaimWindowCompleted)
	}
	if got := claimWindowFor(notifTypeAgentNeedsInput); got != broadcastClaimWindow {
		t.Errorf("claimWindowFor(agent_needs_input) = %v, want %v", got, broadcastClaimWindow)
	}
}

func TestSweepBroadcastClaims_NeverRemovesLiveAgentCompletedClaim(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "agent_completed\nfinished", claimWindowFor(notifTypeAgentCompleted), now, false) {
		t.Fatal("claiming: want true")
	}
	dir := filepath.Join(base, broadcastClaimsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("claims dir entries = %d (%v), want 1", len(entries), err)
	}
	claim := filepath.Join(dir, entries[0].Name())
	// Backdate the claim to 20 minutes old: within the 30m agent_completed
	// window but past a naive TTL, to prove the sweep TTL itself (not just
	// the claim-window check) respects the longer window.
	past := now.Add(-20 * time.Minute)
	if chErr := os.Chtimes(claim, past, past); chErr != nil {
		t.Fatalf("backdating claim: %v", chErr)
	}

	sweepBroadcastClaims(dir, now)

	if _, statErr := os.Stat(claim); statErr != nil {
		t.Fatalf("live agent_completed claim was swept early: %v", statErr)
	}
}

func TestSweepBroadcastClaims_RemovesOnlyExpired(t *testing.T) {
	base := t.TempDir()
	now := time.Now()

	if !claimBroadcast(base, "old", broadcastClaimWindow, now, false) {
		t.Fatal("claiming old: want true")
	}
	dir := filepath.Join(base, broadcastClaimsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("claims dir entries = %d (%v), want 1", len(entries), err)
	}
	stale := filepath.Join(dir, entries[0].Name())
	past := now.Add(-broadcastClaimTTL - time.Minute)
	if chErr := os.Chtimes(stale, past, past); chErr != nil {
		t.Fatalf("backdating claim: %v", chErr)
	}

	// A fresh claim's sweep removes the expired one and keeps itself.
	if !claimBroadcast(base, "new", broadcastClaimWindow, now, false) {
		t.Fatal("claiming new: want true")
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("rereading claims dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("claims dir entries after sweep = %d, want 1 (expired claim swept)", len(entries))
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

// TestBroadcastFacts_LocalJobDefersRegardlessOfSourceSendTiming replaces the
// deleted broadcastCoveredWindow heuristic's coverage: ownership of a
// broadcast that resolves to a local job is structural, not a function of
// when (or whether) the source session has sent its own notification yet.
// This is the epic's 18s-judge-race scenario: a source session can still be
// composing its own richer ping when the broadcast lands, and the receiver
// must defer anyway, trusting the source's fail-open-to-send guarantee.
func TestBroadcastFacts_LocalJobDefersRegardlessOfSourceSendTiming(t *testing.T) {
	tests := []struct {
		name           string
		notifyType     string
		markSourceSent bool
	}{
		{name: "source sent moments ago", notifyType: "agent_needs_input", markSourceSent: true},
		{name: "source never sent", notifyType: "agent_needs_input", markSourceSent: false},
		{name: "agent_completed, source never sent", notifyType: "agent_completed", markSourceSent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			cfg := t.TempDir()
			now := time.Now()
			writeJobState(t, cfg, "job00001", "deploy the widget", "job-session-id", "/projects/widget")

			if tt.markSourceSent {
				source := SessionState{Dir: filepath.Join(base, "job-session-id")}
				if err := source.MarkNotified(now.Add(-2*time.Second), "deploying now"); err != nil {
					t.Fatalf("marking source session notified: %v", err)
				}
			}

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

	p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}}
	in := HookInput{
		HookEventName: "Notification", NotificationType: "agent_needs_input",
		Message: "deploy the widget needs your input: proceed?",
	}
	if facts := p.broadcastFacts(context.Background(), in, now); facts == nil || !facts.Local {
		t.Fatalf("broadcastFacts() = %+v, want Local", facts)
	}

	claimsDir := filepath.Join(base, broadcastClaimsDirName)
	entries, err := os.ReadDir(claimsDir)
	if err == nil && len(entries) != 0 {
		t.Fatalf("claims dir entries = %d, want 0 (a local broadcast must not write a claim)", len(entries))
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

	p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}}
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
