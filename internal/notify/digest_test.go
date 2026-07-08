package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// assertDigestEqual compares two full digest strings and, on mismatch,
// prints both in full rather than a fragment-level diff: a golden mismatch
// in a prompt document is easiest to debug by eyeballing the two complete
// renders side by side.
func assertDigestEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("digest mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

const wantFullDigest = `SESSION
project=savecraft.gg host=gnomon event=Stop
session running 2h14m, 5 user turns

WHAT THE USER LAST ASKED
Can you add pure digest rendering that turns already-gathered session state into a plain text document for the LLM judge to read, with deterministic formatting and golden tests.
most recent reply: "yes, go ahead"

HOW THE TURN ENDED
All done — deployed to staging.

STILL RUNNING
- bash started 3m20s ago: "run test suite" — go test ./internal/notify/... -count=1
  output: 2048 bytes, last write 45s ago
  tail: --- PASS: TestBuildDigest (0.00s)
  tail: PASS
- agent started 12m ago: "investigate flaky test" — check internal/notify for race conditions in EnrichTasks
  output: 512 bytes, last write 8m0s ago

GOAL
condition: "tests green and lint clean for internal/notify/digest.go" status=active iterations=3
`

func TestBuildDigest_Full(t *testing.T) {
	now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)

	meta := DigestMeta{
		Project:              "savecraft.gg",
		Host:                 "gnomon",
		Event:                "Stop",
		LastAssistantMessage: "All done — deployed to staging.",
	}

	scan := ScanResult{
		Goal: GoalState{
			Status:     GoalActive,
			Condition:  "tests green and lint clean for internal/notify/digest.go",
			Iterations: 3,
		},
		LastUserMessage:     "yes, go ahead",
		LastSubstantiveUser: "Can you add pure digest rendering that turns already-gathered session state into a plain text document for the LLM judge to read, with deterministic formatting and golden tests.",
		LastAssistantText:   "some old text that should not appear because meta.LastAssistantMessage wins",
		FirstTimestamp:      now.Add(-(2*time.Hour + 14*time.Minute + 3*time.Second)),
		UserTurns:           5,
	}

	tasks := []TaskActivity{
		{
			LiveTask: LiveTask{
				ID:          "bg1",
				Kind:        TaskBash,
				Description: "run test suite",
				Detail:      "go test ./internal/notify/... -count=1",
				LaunchedAt:  now.Add(-(3*time.Minute + 20*time.Second)),
			},
			OutputExists: true,
			LastWrite:    now.Add(-45 * time.Second),
			SizeBytes:    2048,
			TailLines:    []string{"--- PASS: TestBuildDigest (0.00s)", "PASS"},
		},
		{
			LiveTask: LiveTask{
				ID:          "ag1",
				Kind:        TaskAgent,
				Description: "investigate flaky test",
				Detail:      "check internal/notify for race conditions in EnrichTasks",
				LaunchedAt:  now.Add(-12 * time.Minute),
			},
			OutputExists: true,
			LastWrite:    now.Add(-8 * time.Minute),
			SizeBytes:    512,
			TailLines:    nil,
		},
	}

	got := BuildDigest(meta, scan, tasks, now)
	assertDigestEqual(t, got, wantFullDigest)
}

const wantEmptyDigest = `SESSION
project=proj host=host1 event=recheck: no session activity 25m

WHAT THE USER LAST ASKED
(none)

HOW THE TURN ENDED
(no assistant text)

STILL RUNNING
  (nothing)

GOAL
none
`

func TestBuildDigest_Empty(t *testing.T) {
	now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)

	meta := DigestMeta{
		Project: "proj",
		Host:    "host1",
		Event:   "recheck: no session activity 25m",
	}
	scan := ScanResult{}

	got := BuildDigest(meta, scan, nil, now)
	assertDigestEqual(t, got, wantEmptyDigest)
}

const wantTranscriptFallbackDigest = `SESSION
project=proj2 host=host2 event=Stop

WHAT THE USER LAST ASKED
(none)

HOW THE TURN ENDED
Fixed the failing test by handling the zero-duration case.

STILL RUNNING
  (nothing)

GOAL
none
`

func TestBuildDigest_TranscriptFallback(t *testing.T) {
	now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)

	meta := DigestMeta{
		Project: "proj2",
		Host:    "host2",
		Event:   "Stop",
		// LastAssistantMessage deliberately empty: HOW THE TURN ENDED must
		// fall back to scan.LastAssistantText.
	}
	scan := ScanResult{
		LastAssistantText: "Fixed the failing test by handling the zero-duration case.",
	}

	got := BuildDigest(meta, scan, nil, now)
	assertDigestEqual(t, got, wantTranscriptFallbackDigest)
}

const wantTeammatesDigest = `SESSION
project=proj3 host=host3 event=Stop

WHAT THE USER LAST ASKED
(none)

HOW THE TURN ENDED
(no assistant text)

STILL RUNNING
  (nothing)

TEAMMATES
- "worker-wire" spawned 20m ago
  last message 5m0s ago: "DONE: TagGranted/Revoked/Transformed wired"
- "worker-cli" spawned 10m ago

GOAL
none
`

// TestBuildDigest_Teammates exercises the TEAMMATES section: a teammate
// that has sent a message shows its age and summary, one that hasn't shows
// only its spawn age, and (per TestBuildDigest_Empty/TestBuildDigest_Full
// already covering the nil case) the section is omitted entirely rather
// than rendered empty when there are no teammates at all.
func TestBuildDigest_Teammates(t *testing.T) {
	now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)

	meta := DigestMeta{Project: "proj3", Host: "host3", Event: "Stop"}
	scan := ScanResult{
		Teammates: []TeammateActivity{
			{
				Name:          "worker-wire",
				ID:            "worker-wire@anon-session-0100",
				SpawnedAt:     now.Add(-20 * time.Minute),
				LastMessageAt: now.Add(-5 * time.Minute),
				LastSummary:   "DONE: TagGranted/Revoked/Transformed wired",
			},
			{
				Name:      "worker-cli",
				ID:        "worker-cli@anon-session-0100",
				SpawnedAt: now.Add(-10 * time.Minute),
			},
		},
	}

	got := BuildDigest(meta, scan, nil, now)
	assertDigestEqual(t, got, wantTeammatesDigest)
}

// makeTeammateAt returns a TeammateActivity whose last activity — the value
// buildTeammatesSection sorts and stales by — is exactly idleFor before now:
// spawned an hour before that, with a last message idleFor ago.
func makeTeammateAt(name string, now time.Time, idleFor time.Duration) TeammateActivity {
	return TeammateActivity{
		Name:          name,
		SpawnedAt:     now.Add(-idleFor - time.Hour),
		LastMessageAt: now.Add(-idleFor),
		LastSummary:   name + " summary",
	}
}

// wantTeammateLines renders the two lines buildTeammatesSection emits for a
// teammate built by makeTeammateAt, using the same humanDuration helper the
// implementation uses — this test is exercising the sort/cap logic, not
// humanDuration's formatting (which TestHumanDuration covers directly).
func wantTeammateLines(name string, idleFor time.Duration) string {
	return fmt.Sprintf("- %q spawned %s ago\n  last message %s ago: %q",
		name, humanDuration(idleFor+time.Hour), humanDuration(idleFor), name+" summary")
}

// makeTeammates returns n teammates named tm1..tmN, tm1 the most recently
// active (idle 1 hour) through tmN the least (idle N hours) — so the
// recency order the cap must preserve is simply ascending name order.
func makeTeammates(now time.Time, n int) []TeammateActivity {
	teammates := make([]TeammateActivity, n)
	for i := range teammates {
		teammates[i] = makeTeammateAt(fmt.Sprintf("tm%d", i+1), now, time.Duration(i+1)*time.Hour)
	}
	return teammates
}

func TestBuildTeammatesSection_AtCapNoSummary(t *testing.T) {
	now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)
	teammates := makeTeammates(now, maxDigestTeammates)

	got := buildTeammatesSection(teammates, now)

	var wantLines []string
	wantLines = append(wantLines, "TEAMMATES")
	for i := 1; i <= maxDigestTeammates; i++ {
		wantLines = append(wantLines, wantTeammateLines(fmt.Sprintf("tm%d", i), time.Duration(i)*time.Hour))
	}
	want := strings.Join(wantLines, "\n")

	if got != want {
		t.Errorf("teammates section at cap mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuildTeammatesSection_OverCapAddsSummary(t *testing.T) {
	tests := []struct {
		name        string
		count       int
		wantSummary string
	}{
		{"one over cap", maxDigestTeammates + 1, "+1 more, idle >9h0m"},
		{"twenty teammates", 20, "+12 more, idle >9h0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)
			teammates := makeTeammates(now, tt.count)

			got := buildTeammatesSection(teammates, now)

			var wantLines []string
			wantLines = append(wantLines, "TEAMMATES")
			for i := 1; i <= maxDigestTeammates; i++ {
				wantLines = append(wantLines, wantTeammateLines(fmt.Sprintf("tm%d", i), time.Duration(i)*time.Hour))
			}
			wantLines = append(wantLines, tt.wantSummary)
			want := strings.Join(wantLines, "\n")

			if got != want {
				t.Errorf("teammates section over cap mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}

			excludedName := fmt.Sprintf("tm%d", maxDigestTeammates+1)
			if strings.Contains(got, "\""+excludedName+"\"") {
				t.Errorf("excluded teammate %q must not appear in the section, got:\n%s", excludedName, got)
			}
		})
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"negative", -5 * time.Second, "0s"},
		{"zero", 0, "0s"},
		{"sub-second", 500 * time.Millisecond, "0s"},
		{"one second", 1 * time.Second, "1s"},
		{"a few seconds", 4 * time.Second, "4s"},
		{"just under a minute", 59 * time.Second, "59s"},
		{"exactly one minute", 60 * time.Second, "1m0s"},
		{"minutes and seconds", 3*time.Minute + 20*time.Second, "3m20s"},
		{"just under ten minutes", 9*time.Minute + 59*time.Second, "9m59s"},
		{"exactly ten minutes", 10 * time.Minute, "10m"},
		{"ten minutes plus seconds drops seconds", 10*time.Minute + 30*time.Second, "10m"},
		{"just under an hour", 59 * time.Minute, "59m"},
		{"exactly one hour", 60 * time.Minute, "1h0m"},
		{"hours and minutes", 2*time.Hour + 14*time.Minute, "2h14m"},
		{"just under a day", 23*time.Hour + 59*time.Minute, "23h59m"},
		{"exactly one day", 24 * time.Hour, "1d0h"},
		{"days and hours", 3*24*time.Hour + 2*time.Hour, "3d2h"},
		{"many days", 100*24*time.Hour + 5*time.Hour, "100d5h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := humanDuration(tt.d); got != tt.want {
				t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
