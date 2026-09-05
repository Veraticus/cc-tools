package notify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecisionLogRoundTripsSafeCompletionRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "decisions.jsonl")
	log := DecisionLog{Path: path}
	want := DecisionRecord{
		Time:      time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		SessionID: "session-1", Event: eventStop, Harness: harnessClaude,
		CompletionID: "assistant-uuid", Outcome: OutcomeSend.String(), Reason: "root completion",
		Urgency: UrgencyDone, Title: "project · earth:3", Body: "summary",
		CompositionOutcome: compositionFallback, CompositionError: compositionErrorHelperUnavailable,
	}
	if err := log.Append(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got DecisionRecord
	if unmarshalErr := json.Unmarshal(bytes.TrimSpace(data), &got); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if got != want {
		t.Errorf("record = %+v, want %+v", got, want)
	}
	for _, obsolete := range []string{"judge_mode", "judge_err", "judge_ms", "digest", "prompt", "environ"} {
		if bytes.Contains(data, []byte(obsolete)) {
			t.Errorf("log contains obsolete/private field %q: %s", obsolete, data)
		}
	}
}

func TestDecisionLogOmitsMissingCompletionIdentityAndCompositionFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	if err := (DecisionLog{Path: path}).Append(DecisionRecord{
		Harness: harnessClaude, Outcome: OutcomeSilent.String(), Reason: "session end",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, omitted := range []string{"completion_id", "composition_outcome", "composition_error"} {
		if strings.Contains(string(data), omitted) {
			t.Errorf("record unexpectedly contains %q: %s", omitted, data)
		}
	}
}

func TestDecisionLogRotatesBeforeAppendPastFiveMB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	log := DecisionLog{Path: path}
	bigBody := strings.Repeat("x", 3*1024*1024/2)
	for index := range 5 {
		if err := log.Append(DecisionRecord{SessionID: "session", Body: bigBody}); err != nil {
			t.Fatalf("Append() #%d: %v", index, err)
		}
	}
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Size() <= fresh.Size() {
		t.Errorf("rotated size %d <= fresh size %d", rotated.Size(), fresh.Size())
	}
}

func TestDecisionLogConcurrentAppendsRemainWholeJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	log := DecisionLog{Path: path}
	const goroutines = 10
	const perGoroutine = 20
	var wait sync.WaitGroup
	for goroutine := range goroutines {
		wait.Add(1)
		go func(goroutine int) {
			defer wait.Done()
			for index := range perGoroutine {
				record := DecisionRecord{SessionID: "session", Reason: fmt.Sprintf("%d-%d", goroutine, index)}
				if err := log.Append(record); err != nil {
					t.Errorf("Append(): %v", err)
				}
			}
		}(goroutine)
	}
	wait.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	count := 0
	for scanner.Scan() {
		var record DecisionRecord
		if unmarshalErr := json.Unmarshal(scanner.Bytes(), &record); unmarshalErr != nil {
			t.Fatalf("line %d: %v", count, unmarshalErr)
		}
		count++
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatal(scanErr)
	}
	if count != goroutines*perGoroutine {
		t.Errorf("line count = %d, want %d", count, goroutines*perGoroutine)
	}
}
