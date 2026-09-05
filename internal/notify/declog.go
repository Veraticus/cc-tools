package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// maxLogSizeBytes triggers one-deep rotation before the next append: past
// this, Path is renamed to Path+".1" (clobbering any previous .1) and the
// next line starts a fresh file. This is a tuning corpus a human skims, not
// an audit trail, so keeping just the current + previous file is a
// deliberate, documented choice rather than unbounded growth or full log
// rotation.
const maxLogSizeBytes = 5 * 1024 * 1024

// DecisionLog appends one JSON line per notification decision to Path — the
// corpus used to tune the judge rubric.
type DecisionLog struct {
	Path string
}

// DecisionRecord is one decision-log entry: what event came in, what the
// pipeline decided, and (for judged evaluations) what the judge itself saw
// and returned.
type DecisionRecord struct {
	Time         time.Time `json:"time"`
	SessionID    string    `json:"session_id"`
	Event        string    `json:"event"`
	Harness      string    `json:"harness"`
	CompletionID string    `json:"completion_id,omitempty"`
	Outcome      string    `json:"outcome"`
	Reason       string    `json:"reason"`
	Urgency      Urgency   `json:"urgency,omitempty"`
	Title        string    `json:"title,omitempty"`
	Body         string    `json:"body,omitempty"`
	JudgeMode    string    `json:"judge_mode,omitempty"`
	JudgeErr     string    `json:"judge_err,omitempty"`
	JudgeMs      int64     `json:"judge_ms,omitempty"`
	// Digest is the full digest text, set only for judged evaluations — this
	// is the tuning corpus, so it is kept in full rather than truncated.
	Digest string `json:"digest,omitempty"`
}

// Append writes rec as one JSON line, creating Path's parent directory as
// needed and rotating first if the file has already grown past
// maxLogSizeBytes. The single os.O_APPEND write of "line+\n" is the whole
// concurrency story: POSIX guarantees a write to an O_APPEND-opened file
// descriptor of this size is atomic with respect to other writers on the
// same file, so concurrent Append calls interleave whole lines, never
// partial ones — no additional locking is needed.
func (l DecisionLog) Append(rec DecisionRecord) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(l.Path), 0o750); mkdirErr != nil {
		return fmt.Errorf("notify: creating decision log dir: %w", mkdirErr)
	}
	if rotateErr := l.rotateIfLarge(); rotateErr != nil {
		return rotateErr
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("notify: marshaling decision record: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("notify: opening decision log: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, writeErr := f.Write(line); writeErr != nil {
		return fmt.Errorf("notify: writing decision record: %w", writeErr)
	}
	return nil
}

// rotateIfLarge renames Path to Path+".1" (clobbering any existing .1) when
// Path already exceeds maxLogSizeBytes. No file yet at Path is not an error
// here — there is nothing to rotate; any other Stat failure (e.g.
// permission denied) is a genuine error and propagates.
func (l DecisionLog) rotateIfLarge() error {
	fi, err := os.Stat(l.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("notify: statting decision log: %w", err)
	}
	if fi.Size() <= maxLogSizeBytes {
		return nil
	}
	if renameErr := os.Rename(l.Path, l.Path+".1"); renameErr != nil {
		return fmt.Errorf("notify: rotating decision log: %w", renameErr)
	}
	return nil
}
