package notify

import (
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
