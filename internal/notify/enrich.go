package notify

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tail tuning: never read a whole (potentially large, still-growing) output
// file. tailWindowBytes bounds how much of the file's end we ever read;
// maxTailLines/maxTailLineLen bound what a digest ever needs to show.
const (
	tailWindowBytes = 8192
	maxTailLines    = 3
	maxTailLineLen  = 160
)

// TaskActivity is a LiveTask enriched with on-disk output freshness.
type TaskActivity struct {
	LiveTask

	OutputExists bool
	LastWrite    time.Time // mtime, symlinks followed; zero if missing
	SizeBytes    int64
	TailLines    []string // last ≤3 non-empty lines, each ≤160 chars; nil for agents
}

// EnrichTasks stats each task's OutputFile (following symlinks — agent
// outputs are symlinks to agent-<id>.jsonl transcripts, so Stat must follow
// them to see the target's real freshness) and, for TaskBash tasks, tails
// the last few lines of output. A task with an empty OutputFile, a file
// that no longer exists, or one that can't be read for any other reason is
// never an error here: it degrades to OutputExists:false with zero
// freshness fields, matching the digest's "no output yet" rendering. Every
// input task produces exactly one TaskActivity, in the same order.
//
// now is accepted for callers that compute staleness (e.g. "no output for
// N minutes") relative to a fixed instant; EnrichTasks itself does not
// derive any age from it.
func EnrichTasks(tasks []LiveTask, now time.Time) []TaskActivity {
	_ = now
	result := make([]TaskActivity, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, enrichTask(task))
	}
	return result
}

// enrichTask is the single-task core of EnrichTasks. An empty OutputFile is
// checked structurally before any filesystem call — os.Stat("") would also
// error, but gating on the empty string up front keeps "no path recorded"
// and "path recorded but file gone" as distinct, documented branches rather
// than collapsing them into one error path.
//
// task.OutputFile is also bounded to the claude task-output shape before
// os.Stat ever touches it (see validOutputPath): the path text is taken
// verbatim from transcript content (LiveTask.OutputFile's doc comment), and
// while a launch acknowledgment is conventionally system-generated, nothing
// upstream enforces that — transcript content can carry attacker-influenced
// text. A path failing the shape check degrades exactly like a missing
// file rather than ever reaching os.Stat.
func enrichTask(task LiveTask) TaskActivity {
	activity := TaskActivity{LiveTask: task}
	if task.OutputFile == "" || !validOutputPath(task.OutputFile) {
		return activity
	}

	fi, err := os.Stat(task.OutputFile)
	if err != nil {
		return activity
	}
	activity.OutputExists = true
	activity.LastWrite = fi.ModTime()
	activity.SizeBytes = fi.Size()

	// Agent outputs are raw JSONL transcripts; a tail of that is multi-KB
	// JSON noise, not useful preview text, so mtime/size alone carry the
	// liveness signal and TailLines is deliberately left nil for agents.
	if task.Kind == TaskAgent {
		return activity
	}

	activity.TailLines = tailLines(task.OutputFile, fi.Size())
	return activity
}

// validOutputPath reports whether p has the shape of a genuine claude
// task-output path: /tmp/claude-<id>/.../tasks/<name>. It is a structural
// check only — it never touches the filesystem — applied to a path string
// enrichTask otherwise has no reason to trust (see enrichTask's doc
// comment). All of the following must hold:
//   - p is absolute.
//   - filepath.Clean(p) == p, i.e. p carries no ".." (or other
//     non-canonical) component — a literal ".." in the raw string fails
//     this even if the path it resolves to would otherwise be valid.
//   - one path element starts with "claude-".
//   - the penultimate path element is exactly "tasks".
func validOutputPath(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}

	parts := strings.Split(p, string(filepath.Separator))
	if len(parts) < 2 || parts[len(parts)-2] != "tasks" {
		return false
	}

	for _, part := range parts {
		if strings.HasPrefix(part, "claude-") {
			return true
		}
	}
	return false
}

// tailLines reads at most the last tailWindowBytes of path and returns its
// last ≤maxTailLines non-empty lines, each truncated to maxTailLineLen
// bytes (a byte truncation, not rune-aware — acceptable for a short preview
// line, not meant to be a display-safe boundary). Any error opening,
// seeking, or reading is treated the same as "no lines": the caller has
// already recorded OutputExists/LastWrite/SizeBytes from Stat, so a read
// failure here degrades to an empty tail instead of surfacing an error.
func tailLines(path string, size int64) []string {
	f, err := os.Open(path) //nolint:gosec // Path comes from our own parsed transcript, trusted source
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	offset := size - tailWindowBytes
	if offset < 0 {
		offset = 0
	}
	if _, seekErr := f.Seek(offset, io.SeekStart); seekErr != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	rawLines := strings.Split(string(data), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if line == "" {
			continue
		}
		lines = append(lines, truncate(line, maxTailLineLen))
	}
	if len(lines) > maxTailLines {
		lines = lines[len(lines)-maxTailLines:]
	}
	if len(lines) == 0 {
		// Preallocating lines above (for prealloc) means it's an empty,
		// non-nil slice rather than nil when no non-empty line is found;
		// normalize back to nil so callers (and TaskActivity's documented
		// zero-value contract) see a consistent "no tail" nil.
		return nil
	}
	return lines
}
