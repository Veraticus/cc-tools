package statusline

import (
	"encoding/json"
	"errors"
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

// readFreshCache reads name from the verified cache root and decodes it into
// *out, reporting true only if the file exists, has no real wall-clock future
// mtime, is younger than ttl relative to now, and decodes as valid JSON. Any
// other outcome — missing, future-dated, stale, or corrupt — is a cache miss:
// false, with *out left unmodified. out takes the decoded value by pointer
// (rather than readFreshCache returning a T) so the function never returns a
// bare generic type parameter.
func readFreshCache[T any](root *os.Root, name string, ttl time.Duration, now time.Time, out *T) bool {
	info, err := root.Stat(name)
	if err != nil {
		return false
	}
	// File mtimes are wall-clock values while callers may inject a logical
	// `now` for deterministic expiration tests. Reject only a timestamp that
	// lies in the real wall-clock future; the logical clock still decides TTL.
	if info.ModTime().After(time.Now()) || now.Sub(info.ModTime()) >= ttl {
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

// readCache decodes name from the verified cache root regardless of age. It
// leaves *out untouched on any read or decode failure.
func readCache[T any](root *os.Root, name string, out *T) bool {
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return false
	}
	var decoded T
	if decodeErr := json.Unmarshal(data, &decoded); decodeErr != nil {
		return false
	}
	*out = decoded
	return true
}

type cacheRefillLockStatus uint8

const (
	cacheRefillLockAcquired cacheRefillLockStatus = iota
	cacheRefillLockHeld
	cacheRefillLockUnavailable
)

type cacheRefillLock struct {
	file   *os.File
	status cacheRefillLockStatus
}

// tryCacheRefillLock acquires an advisory, per-cache-entry refill lock without
// waiting. Contention permits a stale fallback; an unavailable locking facility
// degrades to the pre-lock behavior of fetching fresh data.
func tryCacheRefillLock(root *os.Root, name string) cacheRefillLock {
	return tryCacheRefillLockWithFlock(root, name, syscall.Flock)
}

func tryCacheRefillLockWithFlock(
	root *os.Root,
	name string,
	flock func(int, int) error,
) cacheRefillLock {
	f, openErr := root.OpenFile(name+".lock", os.O_CREATE|os.O_WRONLY, cacheFilePerm)
	if openErr != nil {
		return cacheRefillLock{status: cacheRefillLockUnavailable}
	}
	flockErr := flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if flockErr != nil {
		_ = f.Close()
		if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
			return cacheRefillLock{status: cacheRefillLockHeld}
		}
		return cacheRefillLock{status: cacheRefillLockUnavailable}
	}
	return cacheRefillLock{file: f, status: cacheRefillLockAcquired}
}

func releaseCacheRefillLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// writeCache best-effort writes value as JSON to name inside the verified cache
// root. It publishes a fully written temporary sibling with rename, so readers
// see either the old complete JSON document or the new one. A write failure
// isn't fatal — the freshly computed value is returned to the caller regardless.
func writeCache[T any](root *os.Root, name string, value T) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	tempName := fmt.Sprintf(".%s.%d.%d.tmp", name, os.Getpid(), time.Now().UnixNano())
	f, err := root.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, cacheFilePerm)
	if err != nil {
		return
	}
	defer func() { _ = root.Remove(tempName) }()
	n, writeErr := f.Write(data)
	if writeErr != nil || n != len(data) {
		_ = f.Close()
		return
	}
	if closeErr := f.Close(); closeErr != nil {
		return
	}
	_ = root.Rename(tempName, name)
}
