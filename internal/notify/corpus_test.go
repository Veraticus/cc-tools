package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorpusActiveParkedStopsRemainStructurallySilent(t *testing.T) {
	fixtures := []string{
		"corpus_active_parked_grailquest.jsonl",
		"corpus_active_parked_savecraft.jsonl",
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			pipeline, logPath := testPipeline(t, server, panicComposer{})
			for index := range 2 {
				if err := pipeline.Run(context.Background(), HookInput{
					Harness: harnessClaude, SessionID: "active-" + fixture,
					CWD: "/work/project", HookEventName: eventStop,
					TranscriptPath: filepath.Join("testdata", fixture),
				}); err != nil {
					t.Fatalf("Run() #%d: %v", index+1, err)
				}
			}
			select {
			case request := <-requests:
				t.Fatalf("active-goal corpus delivered %+v", request)
			case <-time.After(100 * time.Millisecond):
			}
			for _, record := range readDecisionLog(t, logPath) {
				if record.Outcome != OutcomeSilent.String() || !strings.Contains(record.Reason, "goal active") {
					t.Errorf("record = %+v, want structural active-goal silence", record)
				}
			}
		})
	}
}

func TestCorpusCompletedGoalShapesNeverBlockAndSendDone(t *testing.T) {
	fixtures := []string{"corpus_goal_met.jsonl", "corpus_goal_cleared.jsonl"}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			server, requests := captureNotificationServer(t)
			defer server.Close()
			composer := &recordingComposer{result: ComposeResult{Body: "Corpus completion summary."}}
			pipeline, logPath := testPipeline(t, server, composer)
			if err := pipeline.Run(context.Background(), HookInput{
				Harness: harnessClaude, SessionID: "complete-" + fixture,
				CWD: "/work/project", HookEventName: eventStop,
				TranscriptPath:       filepath.Join("testdata", fixture),
				LastAssistantMessage: "Corpus fallback.",
			}); err != nil {
				t.Fatal(err)
			}
			request := waitNotification(t, requests)
			if request.Priority != "4" {
				t.Errorf("request = %+v, want done urgency", request)
			}
			record := readDecisionLog(t, logPath)[0]
			if record.Outcome != OutcomeSend.String() || record.Urgency != UrgencyDone {
				t.Errorf("record = %+v, want deterministic done send", record)
			}
		})
	}
}

func TestAllSanitizedCorpusFixturesHaveOnlyDeterministicOutcomes(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "corpus_*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no sanitized corpus fixtures")
	}
	for _, fixture := range fixtures {
		file, openErr := os.Open(fixture)
		if openErr != nil {
			t.Fatal(openErr)
		}
		scan, scanErr := ScanTranscript(file)
		_ = file.Close()
		if scanErr != nil {
			t.Fatalf("ScanTranscript(%s): %v", fixture, scanErr)
		}
		decision := Decide(HookInput{Harness: harnessClaude, HookEventName: eventStop}, scan)
		if decision.Outcome != OutcomeSilent && decision.Outcome != OutcomeSend {
			t.Errorf("%s: decision = %+v, want deterministic silent/send only", fixture, decision)
		}
	}
}
