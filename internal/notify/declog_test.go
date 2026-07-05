package notify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecisionLog_AppendRoundTripsParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "decisions.jsonl")
	l := DecisionLog{Path: path}

	want := DecisionRecord{
		Time:      time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
		SessionID: "sess-1",
		Event:     "Stop",
		Outcome:   "send",
		Reason:    "turn ended",
		Urgency:   UrgencyDone,
		Title:     "hi",
		Body:      "there",
		JudgeMode: "compose",
		JudgeErr:  "",
		JudgeMs:   42,
		Digest:    "SESSION\n...",
	}
	if err := l.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	line := strings.TrimSuffix(string(data), "\n")
	var got DecisionRecord
	if unmarshalErr := json.Unmarshal([]byte(line), &got); unmarshalErr != nil {
		t.Fatalf("unmarshaling record: %v (line: %q)", unmarshalErr, line)
	}
	if !got.Time.Equal(want.Time) {
		t.Errorf("Time = %v, want %v", got.Time, want.Time)
	}
	got.Time = want.Time // neutralize for the rest of the comparison
	if got != want {
		t.Errorf("record = %+v, want %+v", got, want)
	}
}

func TestDecisionLog_RotatesBeforeAppendPastFiveMB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := DecisionLog{Path: path}

	// 4 records at 1.5MB each (~6.29MB) push the file past the 5MB
	// threshold; the 5th Append's pre-check then rotates the existing file
	// before writing its own (much smaller) line.
	bigBody := strings.Repeat("x", 3*1024*1024/2)
	for i := range 5 {
		if err := l.Append(DecisionRecord{SessionID: "sess", Body: bigBody}); err != nil {
			t.Fatalf("Append() #%d error = %v", i, err)
		}
	}

	rotatedPath := path + ".1"
	rotatedInfo, err := os.Stat(rotatedPath)
	if err != nil {
		t.Fatalf("stat rotated file: %v", err)
	}
	if rotatedInfo.Size() < 4*1024*1024 {
		t.Errorf("rotated file size = %d, want it to hold the earlier bulk (>4MB)", rotatedInfo.Size())
	}

	freshInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fresh file: %v", err)
	}
	if freshInfo.Size() >= rotatedInfo.Size() {
		t.Errorf("fresh file size = %d, want smaller than rotated file %d", freshInfo.Size(), rotatedInfo.Size())
	}
}

func TestDecisionLog_ConcurrentAppendsAllIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := DecisionLog{Path: path}

	const goroutines = 10
	const perGoroutine = 20

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range perGoroutine {
				rec := DecisionRecord{SessionID: "sess", Reason: "concurrent", JudgeMs: int64(g*perGoroutine + i)}
				if appendErr := l.Append(rec); appendErr != nil {
					t.Errorf("Append() goroutine %d iter %d error = %v", g, i, appendErr)
				}
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		var rec DecisionRecord
		if unmarshalErr := json.Unmarshal(line, &rec); unmarshalErr != nil {
			t.Fatalf("line %d did not parse as JSON: %v (line: %q)", count, unmarshalErr, line)
		}
		count++
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatalf("scanning log: %v", scanErr)
	}
	if count != goroutines*perGoroutine {
		t.Errorf("line count = %d, want %d", count, goroutines*perGoroutine)
	}
}
