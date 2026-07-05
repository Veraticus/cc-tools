package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFileWithMtime writes content to path and pins its mtime via
// os.Chtimes so freshness assertions are deterministic rather than racing
// the real clock.
func writeFileWithMtime(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// claudeShapedPath builds a path under dir that matches the real claude
// task-output shape enrichTask now requires before it will os.Stat a
// transcript-extracted OutputFile: a "claude-*" path element, with "tasks"
// as the penultimate element. Test fixtures that want a real, enriching
// OutputFile must route through this rather than a bare filepath.Join, since
// dir alone (a bare t.TempDir()) no longer satisfies enrichTask's shape
// check.
func claudeShapedPath(dir, name string) string {
	return filepath.Join(dir, "claude-1000", "-home-user-project", "anon-session-0001", "tasks", name)
}

func TestEnrichTasksBashOutput(t *testing.T) {
	dir := t.TempDir()
	path := claudeShapedPath(dir, "bgtaskid001.output")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "first line of output\nsecond line\nthird line\nfourth line (most recent)\n"
	mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
	writeFileWithMtime(t, path, content, mtime)

	task := LiveTask{ID: "bgtaskid001", Kind: TaskBash, OutputFile: path}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	activity := got[0]

	if !activity.OutputExists {
		t.Error("OutputExists = false, want true")
	}
	if !activity.LastWrite.Equal(mtime) {
		t.Errorf("LastWrite = %v, want %v", activity.LastWrite, mtime)
	}
	if activity.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d", activity.SizeBytes, len(content))
	}
	wantTail := []string{"second line", "third line", "fourth line (most recent)"}
	if !equalStrings(activity.TailLines, wantTail) {
		t.Errorf("TailLines = %v, want %v", activity.TailLines, wantTail)
	}
}

func TestEnrichTasksMissingFile(t *testing.T) {
	dir := t.TempDir()
	task := LiveTask{
		ID:         "missingtask",
		Kind:       TaskBash,
		OutputFile: filepath.Join(dir, "does-not-exist.output"),
	}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	assertNoOutput(t, got[0])
}

func TestEnrichTasksEmptyOutputFile(t *testing.T) {
	task := LiveTask{ID: "nooutputfield", Kind: TaskBash, OutputFile: ""}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	assertNoOutput(t, got[0])
}

func TestEnrichTasksEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := claudeShapedPath(dir, "empty.output")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
	writeFileWithMtime(t, path, "", mtime)

	task := LiveTask{ID: "emptytask", Kind: TaskBash, OutputFile: path}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	activity := got[0]
	if !activity.OutputExists {
		t.Error("OutputExists = false, want true")
	}
	if !activity.LastWrite.Equal(mtime) {
		t.Errorf("LastWrite = %v, want %v", activity.LastWrite, mtime)
	}
	if activity.SizeBytes != 0 {
		t.Errorf("SizeBytes = %d, want 0", activity.SizeBytes)
	}
	if activity.TailLines != nil {
		t.Errorf("TailLines = %v, want nil", activity.TailLines)
	}
}

// TestEnrichTasksLargeFileSeeksTailWindow builds a file well past the 8KB
// tail window and verifies the trailing lines are still recovered correctly
// — i.e. the seek lands inside the filler and the last complete lines
// within the window are what come back, not anything from earlier filler
// that fell outside the window.
func TestEnrichTasksLargeFileSeeksTailWindow(t *testing.T) {
	dir := t.TempDir()
	path := claudeShapedPath(dir, "big.output")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var sb strings.Builder
	for range 1000 {
		sb.WriteString("filler line padding the file out past the tail window boundary number ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteByte('\n')
	}
	sb.WriteString("recent line one\n")
	sb.WriteString("recent line two\n")
	sb.WriteString("recent line three (last)\n")
	content := sb.String()
	if len(content) <= tailWindowBytes {
		t.Fatalf("test content too small to exercise seek window: %d bytes", len(content))
	}

	mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
	writeFileWithMtime(t, path, content, mtime)

	task := LiveTask{ID: "bigtask", Kind: TaskBash, OutputFile: path}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	activity := got[0]
	if activity.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d", activity.SizeBytes, len(content))
	}
	wantTail := []string{"recent line one", "recent line two", "recent line three (last)"}
	if !equalStrings(activity.TailLines, wantTail) {
		t.Errorf("TailLines = %v, want %v", activity.TailLines, wantTail)
	}
}

// TestEnrichTasksAgentSymlinkNoTail exercises a real agent-launch shape: the
// task's OutputFile is a symlink to an agent-<id>.jsonl transcript. Stat
// must follow the symlink to report the target's freshness, but TailLines
// must stay nil regardless of content — a raw JSONL tail is useless noise
// for agents, so bash-only tailing must never fire for TaskAgent.
func TestEnrichTasksAgentSymlinkNoTail(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent-abc123.jsonl")
	mtime := time.Date(2026, 7, 5, 6, 30, 0, 0, time.UTC)
	content := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"line one"}]}}` + "\n"
	writeFileWithMtime(t, target, content, mtime)

	link := claudeShapedPath(dir, "agenttestid01.output")
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlinking: %v", err)
	}

	task := LiveTask{ID: "agenttestid01", Kind: TaskAgent, OutputFile: link}
	got := EnrichTasks([]LiveTask{task}, time.Now())
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	activity := got[0]
	if !activity.OutputExists {
		t.Error("OutputExists = false, want true (symlink target exists)")
	}
	if !activity.LastWrite.Equal(mtime) {
		t.Errorf("LastWrite = %v, want %v", activity.LastWrite, mtime)
	}
	if activity.SizeBytes != int64(len(content)) {
		t.Errorf("SizeBytes = %d, want %d", activity.SizeBytes, len(content))
	}
	if activity.TailLines != nil {
		t.Errorf("TailLines = %v, want nil for agent task", activity.TailLines)
	}
}

// TestEnrichTasksPreservesOrderMixedPresence exercises a realistic digest
// input: several tasks in launch order, some with output on disk and some
// without, and confirms EnrichTasks returns one TaskActivity per input task
// in the same order with no reordering or dropped entries.
func TestEnrichTasksPreservesOrderMixedPresence(t *testing.T) {
	dir := t.TempDir()
	presentPath := claudeShapedPath(dir, "present.output")
	if err := os.MkdirAll(filepath.Dir(presentPath), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mtime := time.Date(2026, 7, 5, 7, 0, 0, 0, time.UTC)
	writeFileWithMtime(t, presentPath, "only line\n", mtime)

	tasks := []LiveTask{
		{ID: "task-a", Kind: TaskBash, OutputFile: presentPath},
		{ID: "task-b", Kind: TaskAgent, OutputFile: ""},
		{ID: "task-c", Kind: TaskBash, OutputFile: filepath.Join(dir, "missing.output")},
	}
	got := EnrichTasks(tasks, time.Now())
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	if got[0].ID != "task-a" || !got[0].OutputExists {
		t.Errorf("got[0] = %+v, want present task-a", got[0])
	}
	if got[1].ID != "task-b" {
		t.Errorf("got[1].ID = %q, want task-b", got[1].ID)
	}
	assertNoOutput(t, got[1])
	if got[2].ID != "task-c" {
		t.Errorf("got[2].ID = %q, want task-c", got[2].ID)
	}
	assertNoOutput(t, got[2])
}

// TestEnrichTaskOutputPathValidation exercises the shape gate enrichTask
// applies to task.OutputFile before ever calling os.Stat on it: that path
// text is taken verbatim from transcript content (see LiveTask.OutputFile's
// doc comment), which can carry attacker-influenced text, while the ack
// that produces it is only conventionally system-generated — nothing
// upstream enforces the shape. Every invalid-shape case here creates a real
// file at the rejected path first, so a pass would be caught by the shape
// gate specifically and not merely by the file being absent.
func TestEnrichTaskOutputPathValidation(t *testing.T) {
	t.Run("valid real-shaped path still enriches", func(t *testing.T) {
		dir := t.TempDir()
		path := claudeShapedPath(dir, "validtask01.output")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
		writeFileWithMtime(t, path, "hello\n", mtime)

		task := LiveTask{ID: "validtask01", Kind: TaskBash, OutputFile: path}
		got := EnrichTasks([]LiveTask{task}, time.Now())
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if !got[0].OutputExists {
			t.Error("OutputExists = false, want true for a valid real-shaped path")
		}
	})

	t.Run("relative path degrades even though the file really exists", func(t *testing.T) {
		dir := t.TempDir()
		relDir := filepath.Join("claude-1000", "proj", "sess", "tasks")
		if err := os.MkdirAll(filepath.Join(dir, relDir), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		relPath := filepath.Join(relDir, "reltask01.output")
		mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
		writeFileWithMtime(t, filepath.Join(dir, relPath), "hello\n", mtime)

		t.Chdir(dir)
		task := LiveTask{ID: "reltask01", Kind: TaskBash, OutputFile: relPath}
		got := EnrichTasks([]LiveTask{task}, time.Now())
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		assertNoOutput(t, got[0])
	})

	t.Run("..-containing path degrades even though it resolves to a real file", func(t *testing.T) {
		dir := t.TempDir()
		realPath := claudeShapedPath(dir, "traversaltask01.output")
		if err := os.MkdirAll(filepath.Dir(realPath), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
		writeFileWithMtime(t, realPath, "hello\n", mtime)

		// Same file, reached via a literal ".." segment that Clean would
		// remove — filepath.Clean(p) != p on the raw string is exactly what
		// the gate checks for, regardless of where the ".." resolves to.
		// Built by string concatenation, not filepath.Join, since Join
		// itself calls Clean and would silently normalize the ".." away
		// before the path ever reached the code under test.
		traversalPath := filepath.Dir(realPath) + string(filepath.Separator) + ".." +
			string(filepath.Separator) + "tasks" + string(filepath.Separator) + "traversaltask01.output"

		task := LiveTask{ID: "traversaltask01", Kind: TaskBash, OutputFile: traversalPath}
		got := EnrichTasks([]LiveTask{task}, time.Now())
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		assertNoOutput(t, got[0])
	})

	t.Run("/etc/passwd degrades", func(t *testing.T) {
		task := LiveTask{ID: "etcpasswdtask", Kind: TaskBash, OutputFile: "/etc/passwd"}
		got := EnrichTasks([]LiveTask{task}, time.Now())
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		assertNoOutput(t, got[0])
	})

	t.Run("symlink-friendly-but-wrong-tree path degrades", func(t *testing.T) {
		dir := t.TempDir()
		// claude- element present, but the penultimate element is "notes",
		// not "tasks" — a real file in a plausible-but-wrong location.
		path := filepath.Join(dir, "claude-1000", "proj", "sess", "notes", "wrongtreetask01.output")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mtime := time.Date(2026, 7, 5, 6, 0, 0, 0, time.UTC)
		writeFileWithMtime(t, path, "hello\n", mtime)

		task := LiveTask{ID: "wrongtreetask01", Kind: TaskBash, OutputFile: path}
		got := EnrichTasks([]LiveTask{task}, time.Now())
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		assertNoOutput(t, got[0])
	})
}

func assertNoOutput(t *testing.T, activity TaskActivity) {
	t.Helper()
	if activity.OutputExists {
		t.Error("OutputExists = true, want false")
	}
	if !activity.LastWrite.IsZero() {
		t.Errorf("LastWrite = %v, want zero", activity.LastWrite)
	}
	if activity.SizeBytes != 0 {
		t.Errorf("SizeBytes = %d, want 0", activity.SizeBytes)
	}
	if activity.TailLines != nil {
		t.Errorf("TailLines = %v, want nil", activity.TailLines)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
