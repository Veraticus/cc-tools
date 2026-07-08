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

	job, ok := resolveBroadcastSource(
		filepath.Join(cfg, "jobs"), "pam grants alert ingestion needs your input: Want it?")
	if !ok {
		t.Fatal("resolveBroadcastSource() ok = false, want match")
	}
	if job.SessionID != "abc12345-full-session-id" || job.CWD != "/work/CSRE-813" {
		t.Errorf("resolveBroadcastSource() = %+v, want matched job fields", job)
	}
}

func TestResolveBroadcastSource_LongestNameWins(t *testing.T) {
	cfg := t.TempDir()
	writeJobState(t, cfg, "short001", "fix tests", "short-session", "/p/short")
	writeJobState(t, cfg, "long0001", "fix tests and lint", "long-session", "/p/long")

	job, ok := resolveBroadcastSource(filepath.Join(cfg, "jobs"), "fix tests and lint finished")
	if !ok || job.SessionID != "long-session" {
		t.Fatalf("resolveBroadcastSource() = %+v ok=%v, want longest-name match long-session", job, ok)
	}
}

func TestResolveBroadcastSource_NoMatchAndBadInputs(t *testing.T) {
	cfg := t.TempDir()
	writeJobState(t, cfg, "abc12345", "known job", "known-session", "/p/known")
	writeJobState(t, cfg, "evil0001", "evil job", "../escape", "/p/evil")

	if _, ok := resolveBroadcastSource(filepath.Join(cfg, "jobs"), "unrelated message"); ok {
		t.Error("unrelated message matched, want no match")
	}
	if _, ok := resolveBroadcastSource(filepath.Join(cfg, "jobs"), ""); ok {
		t.Error("empty message matched, want no match")
	}
	if _, ok := resolveBroadcastSource(filepath.Join(cfg, "missing"), "known job finished"); ok {
		t.Error("missing jobs dir matched, want no match")
	}
	// A job whose sessionId would escape the state base is never returned.
	if _, ok := resolveBroadcastSource(filepath.Join(cfg, "jobs"), "evil job finished"); ok {
		t.Error("job with path-traversal sessionId matched, want rejection")
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

func TestBroadcastFacts_CoveredBySourceJobSend(t *testing.T) {
	base := t.TempDir()
	cfg := t.TempDir()
	now := time.Now()
	writeJobState(t, cfg, "job00001", "deploy the widget", "job-session-id", "/projects/widget")

	source := SessionState{Dir: filepath.Join(base, "job-session-id")}
	if err := source.MarkNotified(now.Add(-2*time.Second), "deploying now"); err != nil {
		t.Fatalf("marking source session notified: %v", err)
	}

	p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}}
	in := HookInput{
		HookEventName: "Notification", NotificationType: "agent_needs_input",
		Message: "deploy the widget needs your input: proceed?",
	}
	facts := p.broadcastFacts(context.Background(), in, now)
	if facts == nil {
		t.Fatal("broadcastFacts() = nil, want facts")
	}
	if facts.Duplicate {
		t.Error("Duplicate = true, want false for first claim")
	}
	if !facts.Covered {
		t.Error("Covered = false, want true (source session sent 2s ago)")
	}
	if facts.JobProject != "widget" {
		t.Errorf("JobProject = %q, want %q", facts.JobProject, "widget")
	}
}

func TestBroadcastFacts_UncoveredWhenSourceNeverSent(t *testing.T) {
	base := t.TempDir()
	cfg := t.TempDir()
	now := time.Now()
	writeJobState(t, cfg, "job00001", "deploy the widget", "job-session-id", "/projects/widget")

	p := Pipeline{StateBase: base, Environ: []string{"CLAUDE_CONFIG_DIR=" + cfg}}
	in := HookInput{
		HookEventName: "Notification", NotificationType: "agent_needs_input",
		Message: "deploy the widget needs your input: proceed?",
	}
	facts := p.broadcastFacts(context.Background(), in, now)
	if facts == nil || facts.Covered {
		t.Fatalf("broadcastFacts() = %+v, want uncovered facts (backstop must fire)", facts)
	}

	// A second session receiving the same broadcast loses the claim.
	second := p.broadcastFacts(context.Background(), in, now.Add(500*time.Millisecond))
	if second == nil || !second.Duplicate {
		t.Fatalf("second receiver facts = %+v, want Duplicate", second)
	}
}
