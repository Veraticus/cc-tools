package statusline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// --- parseGitPorcelain -------------------------------------------------

func TestParseGitPorcelain_Clean(t *testing.T) {
	out := "# branch.oid abc123\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -0\n"
	got := parseGitPorcelain(out)

	if !got.OK {
		t.Fatalf("expected OK=true, got %+v", got)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want main", got.Branch)
	}
	if got.DirtyCount != 0 {
		t.Errorf("expected clean (DirtyCount=0), got DirtyCount=%d", got.DirtyCount)
	}
	if got.Ahead != 0 || got.Behind != 0 {
		t.Errorf("expected Ahead=0 Behind=0, got Ahead=%d Behind=%d", got.Ahead, got.Behind)
	}
}

func TestParseGitPorcelain_Dirty3(t *testing.T) {
	out := "# branch.head main\n" +
		"# branch.ab +0 -0\n" +
		"1 .M N... 100644 100644 100644 aaa bbb file1.go\n" +
		"1 .M N... 100644 100644 100644 aaa bbb file2.go\n" +
		"? file3.go\n"
	got := parseGitPorcelain(out)

	if !got.OK {
		t.Fatalf("expected OK=true, got %+v", got)
	}
	if got.DirtyCount != 3 {
		t.Errorf("expected DirtyCount=3, got DirtyCount=%d", got.DirtyCount)
	}
}

func TestParseGitPorcelain_AheadBehind(t *testing.T) {
	out := "# branch.head main\n# branch.ab +2 -1\n"
	got := parseGitPorcelain(out)

	if got.Ahead != 2 {
		t.Errorf("Ahead = %d, want 2", got.Ahead)
	}
	if got.Behind != 1 {
		t.Errorf("Behind = %d, want 1", got.Behind)
	}
	if got.DirtyCount != 0 {
		t.Errorf("ahead/behind-only repo should not be dirty, got DirtyCount=%d", got.DirtyCount)
	}
}

func TestParseGitPorcelain_Detached(t *testing.T) {
	out := "# branch.head (detached)\n# branch.ab +0 -0\n"
	got := parseGitPorcelain(out)

	// The literal porcelain token is preserved here; display code never
	// reads gitState.Branch for the detached case (see the gitState
	// doc comment) — it keeps using the existing HEAD-file short-SHA
	// fallback instead.
	if got.Branch != "(detached)" {
		t.Errorf("Branch = %q, want (detached)", got.Branch)
	}
	if !got.OK {
		t.Errorf("expected OK=true for a well-formed detached-HEAD status, got %+v", got)
	}
}

func TestParseGitPorcelain_Empty(t *testing.T) {
	got := parseGitPorcelain("")

	if !got.OK {
		t.Errorf("empty output should still parse OK, got %+v", got)
	}
	if got.Branch != "" || got.DirtyCount != 0 || got.Ahead != 0 || got.Behind != 0 {
		t.Errorf("expected zero-value state for empty output, got %+v", got)
	}
}

func TestParseGitPorcelain_MalformedBranchAb(t *testing.T) {
	out := "# branch.head main\n# branch.ab garbage\n"
	got := parseGitPorcelain(out)

	if !got.OK {
		t.Errorf("a malformed branch.ab header shouldn't fail the whole parse, got %+v", got)
	}
	if got.Ahead != 0 || got.Behind != 0 {
		t.Errorf("malformed branch.ab should leave Ahead/Behind at 0, got Ahead=%d Behind=%d", got.Ahead, got.Behind)
	}
	if got.DirtyCount != 0 {
		t.Errorf("malformed header line must not be counted as a dirty entry, got DirtyCount=%d", got.DirtyCount)
	}
}

// --- gitStatus caching --------------------------------------------------

// countingRunner is a CommandRunner fake that records how many times
// RunContext was invoked and the context it was last called with, so
// tests can assert on cache-hit/cache-miss behavior and on the context
// gitStatus builds without spawning any real subprocess.
type countingRunner struct {
	calls  int
	output []byte
	err    error
	gotCtx context.Context
}

func (r *countingRunner) Run(command string, args ...string) ([]byte, error) {
	return r.RunContext(context.Background(), command, args...)
}

func (r *countingRunner) RunContext(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	r.calls++
	r.gotCtx = ctx
	return r.output, r.err
}

func TestGitStatus_CacheHitSkipsRunner(t *testing.T) {
	cacheDir := t.TempDir()
	cwd := "/some/project"
	now := time.Now()

	runner := &countingRunner{output: []byte("# branch.head main\n# branch.ab +0 -0\n")}

	first := gitStatus(runner, cacheDir, time.Minute, cwd, now)
	if !first.OK || runner.calls != 1 {
		t.Fatalf("first call: expected OK with 1 runner invocation, got OK=%v calls=%d", first.OK, runner.calls)
	}

	second := gitStatus(runner, cacheDir, time.Minute, cwd, now.Add(time.Second))
	if runner.calls != 1 {
		t.Errorf("cache hit should not invoke the runner again, calls=%d", runner.calls)
	}
	if second.Branch != first.Branch {
		t.Errorf("cached state should match the original: got %+v, want %+v", second, first)
	}
}

func TestGitStatus_StaleCacheReRuns(t *testing.T) {
	cacheDir := t.TempDir()
	cwd := "/some/project"
	now := time.Now()

	runner := &countingRunner{output: []byte("# branch.head main\n# branch.ab +0 -0\n")}

	gitStatus(runner, cacheDir, time.Minute, cwd, now)
	if runner.calls != 1 {
		t.Fatalf("expected 1 call after first invocation, got %d", runner.calls)
	}

	// Beyond the TTL: cache must be treated as stale.
	gitStatus(runner, cacheDir, time.Minute, cwd, now.Add(2*time.Minute))
	if runner.calls != 2 {
		t.Errorf("stale cache should re-invoke the runner, calls=%d", runner.calls)
	}
}

func TestGitStatus_CorruptCacheReRuns(t *testing.T) {
	cacheDir := t.TempDir()
	cwd := "/some/project"
	now := time.Now()

	// Pre-seed a corrupt cache file at the exact path gitStatus computes
	// (inside the per-uid subdirectory).
	dir, ok := ensureGitCacheDir(cacheDir)
	if !ok {
		t.Fatal("ensureGitCacheDir failed in a fresh temp dir")
	}
	path := gitStatusCachePath(dir, cwd)
	if err := os.WriteFile(path, []byte("not json {"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &countingRunner{output: []byte("# branch.head main\n# branch.ab +0 -0\n")}
	got := gitStatus(runner, cacheDir, time.Minute, cwd, now)

	if runner.calls != 1 {
		t.Errorf("corrupt cache should be treated as a miss and re-invoke the runner, calls=%d", runner.calls)
	}
	if !got.OK {
		t.Errorf("expected a fresh, valid state despite the corrupt cache file, got %+v", got)
	}
}

func TestGitStatus_RunnerErrorYieldsNotOK(t *testing.T) {
	cacheDir := t.TempDir()
	runner := &countingRunner{err: errors.New("boom")}

	got := gitStatus(runner, cacheDir, time.Minute, "/some/project", time.Now())
	if got.OK {
		t.Errorf("expected OK=false on a runner error, got %+v", got)
	}
}

func TestGitStatus_ContextTimeoutYieldsNotOKAndObservesDeadline(t *testing.T) {
	cacheDir := t.TempDir()
	runner := &countingRunner{err: context.DeadlineExceeded}

	got := gitStatus(runner, cacheDir, time.Minute, "/some/project", time.Now())
	if got.OK {
		t.Errorf("expected OK=false on context timeout, got %+v", got)
	}
	if runner.gotCtx == nil {
		t.Fatal("expected the runner to have observed a context")
	}
	if _, hasDeadline := runner.gotCtx.Deadline(); !hasDeadline {
		t.Error("expected gitStatus to invoke the runner with a context.WithTimeout deadline, not context.Background()")
	}
}

func TestGitStatus_EmptyCacheDirSkipsDiskEntirely(t *testing.T) {
	// cacheDir == "" must disable caching outright rather than resolving
	// a relative path against the process's current working directory
	// (which would leave a stray file in whatever directory `go test`
	// happens to run from).
	runner := &countingRunner{output: []byte("# branch.head main\n# branch.ab +0 -0\n")}

	gitStatus(runner, "", time.Minute, "/some/project", time.Now())
	gitStatus(runner, "", time.Minute, "/some/project", time.Now())

	if runner.calls != 2 {
		t.Errorf("empty cacheDir should always re-run rather than cache, calls=%d", runner.calls)
	}
}

// --- per-uid cache-dir hardening ------------------------------------------

func TestEnsureGitCacheDir_CreatesOwnedPrivateSubdir(t *testing.T) {
	cacheDir := t.TempDir()

	dir, ok := ensureGitCacheDir(cacheDir)
	if !ok {
		t.Fatal("expected ok=true in a fresh temp dir")
	}
	if filepath.Dir(dir) != cacheDir {
		t.Errorf("subdir %q should live directly inside %q", dir, cacheDir)
	}

	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected a real directory, got mode %v", info.Mode())
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("expected mode 0700, got %v", info.Mode().Perm())
	}
}

func TestEnsureGitCacheDir_RejectsPlantedSymlink(t *testing.T) {
	cacheDir := t.TempDir()
	target := t.TempDir()

	// An attacker pre-plants a symlink at the exact per-uid path,
	// pointing at a directory they control.
	planted := filepath.Join(cacheDir, "cc-tools-"+strconv.Itoa(os.Getuid()))
	if err := os.Symlink(target, planted); err != nil {
		t.Fatal(err)
	}

	if _, ok := ensureGitCacheDir(cacheDir); ok {
		t.Error("a symlink at the per-uid path must disable caching, got ok=true")
	}
}

func TestEnsureGitCacheDir_RejectsWrongMode(t *testing.T) {
	cacheDir := t.TempDir()

	// A pre-existing per-uid dir with loose permissions (e.g. created
	// by something else, or tampered) must not be trusted; MkdirAll
	// won't tighten an existing directory's mode.
	pre := filepath.Join(cacheDir, "cc-tools-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(pre, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok := ensureGitCacheDir(cacheDir); ok {
		t.Error("a per-uid dir that isn't mode 0700 must disable caching, got ok=true")
	}
}

func TestGitStatus_UntrustedCacheDirStillReturnsFreshState(t *testing.T) {
	cacheDir := t.TempDir()
	planted := filepath.Join(cacheDir, "cc-tools-"+strconv.Itoa(os.Getuid()))
	if err := os.Symlink(t.TempDir(), planted); err != nil {
		t.Fatal(err)
	}

	runner := &countingRunner{output: []byte("# branch.head main\n# branch.ab +0 -0\n")}

	first := gitStatus(runner, cacheDir, time.Minute, "/some/project", time.Now())
	second := gitStatus(runner, cacheDir, time.Minute, "/some/project", time.Now())

	if !first.OK || !second.OK {
		t.Errorf("an untrusted cache dir must not break the git chip: got %+v / %+v", first, second)
	}
	if runner.calls != 2 {
		t.Errorf("untrusted cache dir should re-run every call rather than cache, calls=%d", runner.calls)
	}
}

// --- real git integration ------------------------------------------------

// TestGitStatus_RealGitDirtyRepo exercises gitStatus against a real git
// binary and a real repo in t.TempDir() -- no FileReader/CommandRunner
// mocking -- matching the unguarded real-FS test convention already
// used by real_fs_test.go in this package (no build tag, no
// testing.Short skip).
func TestGitStatus_RealGitDirtyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=cc-tools-test", "GIT_AUTHOR_EMAIL=test@cc-tools.invalid",
			"GIT_COMMITTER_NAME=cc-tools-test", "GIT_COMMITTER_EMAIL=test@cc-tools.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "init")

	// Dirty the file after the commit.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}

	gs := gitStatus(&DefaultCommandRunner{}, t.TempDir(), 0, dir, time.Now())

	if !gs.OK {
		t.Fatalf("expected OK=true against a real git repo, got %+v", gs)
	}
	if gs.DirtyCount < 1 {
		t.Errorf("expected DirtyCount>=1, got DirtyCount=%d", gs.DirtyCount)
	}
}
