// Package cost computes USD cost from Claude Code session transcripts
// (JSONL files under a project's transcripts directory). It knows nothing
// about the statusline or any other consumer: Costs prices one transcript
// file as "the session" and, in the same pass, aggregates today's cost
// across every other transcript on disk as "the day". It is a pure
// function of its inputs (paths, a caller-supplied "now", and a
// caller-supplied subscribed flag) so a later cache layer can call it
// synchronously on a TTL.
package cost

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// jsonlExt is the only file extension the directory walk considers.
const jsonlExt = ".jsonl"

// Costs returns transcriptPath's own USD cost ("session") and today's total
// USD cost across every *.jsonl file under projectsDir ("daily"), where
// "today" is now's local date (the date in now's own time.Location).
//
// transcriptPath — the largest, live-appended file in the corpus — is
// scanned exactly once: sessionUSD is its full-file total with no date
// filter (a single transcript file is already scoped to one session, so
// there is no "today" boundary to apply to it), and that same scan's
// today-matching rows seed dailyUSD. The remaining *.jsonl files under
// projectsDir are then walked for their own today contribution, using the
// same dedup set the transcript scan built — a resumed session re-logs its
// parent history into a new transcript file under a different sessionId,
// but with the same message.id and (observed on real transcripts,
// 2026-07-06) a null requestId, and a dedup set shared across the whole
// call is what stops that resumed history from being counted once in the
// original file and again in the new one. transcriptPath itself is
// skipped during the walk (already scanned above) whenever its path
// relative to projectsDir can be determined; if it can't, the walk simply
// re-scans it, and the shared dedup set still prevents double-counting —
// slower, never wrong.
//
// Only files whose mtime is at or after local midnight of now are walked:
// an older file cannot contain any row written after its own mtime, so it
// cannot contain a today row and is skipped without ever being opened. An
// individual file that cannot be opened or read is skipped rather than
// failing the whole call; a missing or unreadable transcriptPath or
// projectsDir is fatal.
func Costs(transcriptPath, projectsDir string, now time.Time, subscribed bool) (float64, float64, error) {
	f, err := openTranscriptFile(transcriptPath)
	if err != nil {
		return 0, 0, err
	}

	dedup := make(map[string]bool)
	filter := newTodayFilter(now)

	sessionUSD, dailyUSD, err := scanSessionRows(f, subscribed, dedup, filter)
	_ = f.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("scanning session %s: %w", transcriptPath, err)
	}

	root, err := os.OpenRoot(projectsDir)
	if err != nil {
		return 0, 0, fmt.Errorf("opening projects dir %s: %w", projectsDir, err)
	}
	defer func() { _ = root.Close() }()

	skipRel, skipErr := filepath.Rel(projectsDir, transcriptPath)
	if skipErr != nil {
		skipRel = ""
	}
	skipRel = filepath.Clean(skipRel)

	midnight := localMidnight(now)
	walkErr := fs.WalkDir(root.FS(), ".", func(relPath string, d fs.DirEntry, entryErr error) error {
		if !shouldScanEntry(relPath, d, entryErr, midnight) {
			return nil
		}
		if skipErr == nil && filepath.Clean(relPath) == skipRel {
			return nil
		}
		dailyUSD += scanEntryCost(root, relPath, subscribed, dedup, now)
		return nil
	})
	if walkErr != nil {
		return sessionUSD, dailyUSD, fmt.Errorf("walking projects dir %s: %w", projectsDir, walkErr)
	}
	return sessionUSD, dailyUSD, nil
}

// scanEntryCost scans relPath within root and returns its USD cost,
// swallowing any open/read error to 0: Daily's contract is that an
// individual unreadable file is skipped, not fatal to the whole call.
func scanEntryCost(root *os.Root, relPath string, subscribed bool, dedup map[string]bool, now time.Time) float64 {
	amount, err := scanTranscriptFile(root, relPath, subscribed, dedup, &now)
	if err != nil {
		return 0
	}
	return amount
}

// shouldScanEntry reports whether a walk entry is a *.jsonl file worth
// opening: not a directory, not an entry the walk itself failed to read,
// with a .jsonl extension, and an mtime at or after midnight. An older
// file cannot contain any row newer than its own mtime, so it cannot
// contain a today row.
func shouldScanEntry(relPath string, d fs.DirEntry, entryErr error, midnight time.Time) bool {
	if entryErr != nil || d.IsDir() || filepath.Ext(relPath) != jsonlExt {
		return false
	}
	info, err := d.Info()
	if err != nil {
		return false
	}
	return !info.ModTime().Before(midnight)
}

// localMidnight returns local midnight (00:00:00) of now's own date, in
// now's own time.Location — never time.Local — so that the boundary
// matches whatever local date now.In(now.Location()) resolves to.
func localMidnight(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
}
