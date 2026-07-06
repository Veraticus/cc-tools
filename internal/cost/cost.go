// Package cost computes USD cost from Claude Code session transcripts
// (JSONL files under a project's transcripts directory). It knows nothing
// about the statusline or any other consumer: Session prices one transcript
// file, Daily aggregates today's cost across every transcript on disk. Both
// are pure functions of their inputs (paths, a caller-supplied "now", and a
// caller-supplied subscribed flag) so a later cache layer can call them
// synchronously on a TTL.
package cost

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// jsonlExt is the only file extension Daily's directory walk considers.
const jsonlExt = ".jsonl"

// Session returns the USD cost of one session transcript file at
// transcriptPath. subscribed selects anthropic pricing: true means
// anthropic-backend rows are free (a Claude subscription), false means they
// are priced at list rates. Bedrock-backed rows are always priced,
// regardless of subscribed.
//
// Session dedups message.id+requestId pairs within this one file (see
// Daily's doc comment for why the same dedup rule matters across files
// too), but does not filter by date: a single transcript file is already
// scoped to one session, so there is no "today" boundary to apply.
func Session(transcriptPath string, subscribed bool) (float64, error) {
	f, err := openTranscriptFile(transcriptPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	total, err := scanRows(f, subscribed, make(map[string]bool), nil)
	if err != nil {
		return 0, fmt.Errorf("scanning session %s: %w", transcriptPath, err)
	}
	return total, nil
}

// Daily returns today's total USD cost across every *.jsonl file found
// recursively under projectsDir, where "today" is now's local date (the
// date in now's own time.Location). Only files whose mtime is at or after
// local midnight of now are scanned: an older file cannot contain any row
// written after its own mtime, so it cannot contain a today row and is
// skipped without ever being opened. An individual file that cannot be
// opened or read is skipped rather than failing the whole call; a missing
// or unreadable projectsDir is fatal.
//
// Daily dedups message.id+requestId pairs across the ENTIRE walk, not per
// file: Claude Code re-logs a resumed session's parent history into a new
// transcript file under a different sessionId, but with the same
// message.id and (observed on real transcripts, 2026-07-06) a null
// requestId. Without a dedup set shared across every file in the walk,
// that resumed history would be counted once in the original file and
// again in the new one.
func Daily(projectsDir string, now time.Time, subscribed bool) (float64, error) {
	root, err := os.OpenRoot(projectsDir)
	if err != nil {
		return 0, fmt.Errorf("opening projects dir %s: %w", projectsDir, err)
	}
	defer func() { _ = root.Close() }()

	midnight := localMidnight(now)
	dedup := make(map[string]bool)
	var total float64

	walkErr := fs.WalkDir(root.FS(), ".", func(relPath string, d fs.DirEntry, entryErr error) error {
		if !shouldScanEntry(relPath, d, entryErr, midnight) {
			return nil
		}
		total += scanEntryCost(root, relPath, subscribed, dedup, now)
		return nil
	})
	if walkErr != nil {
		return total, fmt.Errorf("walking projects dir %s: %w", projectsDir, walkErr)
	}
	return total, nil
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
