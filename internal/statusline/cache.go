package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// cacheFilePerm is the permission mode for cache files written under a
// verified cache root; owner-only since CacheDir may be a shared
// location like /dev/shm.
const cacheFilePerm = 0o600

// cacheDirPerm is the permission mode for the per-uid cache
// subdirectory — owner-only, so no other user of a shared CacheDir can
// plant or read entries inside it.
const cacheDirPerm = 0o700

// cacheHashBytes is how many bytes of a cache key's sha256 sum are
// kept for the cache filename — enough to make collisions practically
// impossible for the small number of entries any one user renders a
// statusline from.
const cacheHashBytes = 8

// openCacheRoot creates (or verifies) the per-uid subdirectory of
// cacheDir that all TTL-cached statusline data lives in (git status,
// transcript-derived cost, ...), and returns it as an opened os.Root
// handle. CacheDir is typically a shared, world-writable location
// (/dev/shm, /tmp) where the cache filename is predictable, so another
// local user could pre-plant a symlink at that path and redirect our
// 0600 write onto a file they choose. Confining every access to a
// directory that is (a) a real directory, not a symlink, (b) owned by
// this uid, and (c) mode 0700 closes that off — and doing all
// subsequent reads/writes through the returned handle (openat
// semantics) means the verification and the file operations target
// the same inode: swapping the directory for a symlink after the
// check cannot redirect them, even in a non-sticky parent. The
// ownership/mode checks fstat the handle actually held, not a
// re-resolved path. Any failure returns ok=false, which disables
// caching for the call — the caller just computes fresh, exactly as
// with CacheDir "". The caller must Close the returned root.
func openCacheRoot(cacheDir string) (*os.Root, bool) {
	dir := filepath.Join(cacheDir, fmt.Sprintf("cc-tools-%d", os.Getuid()))
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return nil, false
	}

	// Lstat, not Stat: a planted symlink to a directory must be seen
	// as a symlink and rejected, not resolved through.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, false
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm() != cacheDirPerm {
		_ = root.Close()
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		_ = root.Close()
		return nil, false
	}
	return root, true
}

// readFreshCache reads name from the verified cache root and decodes
// it into *out, reporting true only if the file exists, is younger
// than ttl relative to now, and decodes as valid JSON. Any other
// outcome — missing, stale, or corrupt — is a cache miss: false, with
// *out left unmodified. out takes the decoded value by pointer (rather
// than readFreshCache returning a T) so the function never returns a
// bare generic type parameter.
func readFreshCache[T any](root *os.Root, name string, ttl time.Duration, now time.Time, out *T) bool {
	info, err := root.Stat(name)
	if err != nil {
		return false
	}
	if now.Sub(info.ModTime()) >= ttl {
		return false
	}

	f, err := root.Open(name)
	if err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return false
	}

	if unmarshalErr := json.Unmarshal(data, out); unmarshalErr != nil {
		return false
	}
	return true
}

// writeCache best-effort writes value as JSON to name inside the
// verified cache root. A write failure isn't fatal — the freshly
// computed value is returned to the caller regardless — so the error
// is deliberately not propagated any further than this.
func writeCache[T any](root *os.Root, name string, value T) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, cacheFilePerm)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_ = f.Close()
}
