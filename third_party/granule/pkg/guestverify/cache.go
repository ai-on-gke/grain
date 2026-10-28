package guestverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// cacheVersion is bumped when what a verdict means changes -- a check
// gaining a timeout, the boot gaining a step -- so that entries written
// by an older granule are ignored rather than trusted. It costs one
// boot; trusting them would mean an image passing on a claim nothing
// checked.
const cacheVersion = "granule-guest-verify v1"

// key identifies a verdict: these bytes, under that granule, having passed
// exactly these checks.
//
// Everything that could change the answer is in it, and nothing that
// could not. The image digests are in because that is what is being
// judged; the check set is in, names and scripts both, because a verdict
// is only ever about the questions that were asked -- adding a check to
// the file has to invalidate yesterday's pass, or the first thing a
// consumer does after tightening its checks is skip them. What is
// deliberately *not* in the key is the reference either image was named
// by, the timeouts, or anything about the host: a tag that has moved
// resolves to a different digest already, and a verdict that changed
// with -ready-timeout would be a verdict about the machine that ran it.
func key(imageID, granuleID string, checks []Check) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", cacheVersion)
	fmt.Fprintf(h, "image %s\n", imageID)
	fmt.Fprintf(h, "granule %s\n", granuleID)
	for _, c := range checks {
		// Lengths rather than separators: a check named "a" with script
		// "b" and one named "a\nb" with no script must not hash alike.
		fmt.Fprintf(h, "check %d %s %d %s\n", len(c.Name), c.Name, len(c.Script), c.Script)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DefaultCacheDir is where verdicts are kept when the caller names no
// directory, following its caller the way granule's state directory
// does: a node-wide place for root, and the user's own cache otherwise.
//
// Under the cache hierarchy rather than the state one on purpose. Every
// entry in it is reproducible by booting the image again, and losing the
// lot costs exactly that -- which is what a cache is, and what makes it
// safe for anything that cleans caches to delete.
func DefaultCacheDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/granule/verify"
	}
	if dir := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "granule", "verify")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "granule", "verify")
	}
	return "/var/lib/granule/verify"
}

// cacheEntry is one cached verdict as it is stored: the Result, plus the
// version it was written by.
type cacheEntry struct {
	Version string `json:"version"`
	Result
}

func cachePath(dir, key string) string { return filepath.Join(dir, key+".json") }

// readCache returns a previous pass for key, if there is one. Anything
// wrong with the file -- absent, unreadable, written by another version,
// keyed differently from its own name -- reads as "not cached", which
// costs a boot and is always safe. The alternative, refusing to run
// because a cache file is corrupt, would make a cache able to block a
// verify, and a cache must never be able to do that.
//
// A verdict is also only believed if this user wrote it. The key is no
// secret -- an image digest, a granule digest and a check set anyone can
// read -- so a file under that name proves nothing by existing: in a
// cache directory somebody else can write to (a shared -cache-dir, one
// made in /tmp by whoever got there first) it could be a pass nobody
// booted anything to earn. writeCache's own files are this user's and
// mode 0600, so the check costs a legitimate entry nothing.
func readCache(dir, key string) (Result, bool) {
	// No following a symlink to somebody else's file, and no blocking on
	// a FIFO left under the entry's name: the file is checked once it is
	// open, and opening must not be the thing that hangs.
	f, err := os.OpenFile(cachePath(dir, key), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Result{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !ownEntry(info) {
		return Result{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEntryBytes))
	if err != nil {
		return Result{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Result{}, false
	}
	if entry.Version != cacheVersion || entry.Key != key {
		return Result{}, false
	}
	return entry.Result, true
}

// maxEntryBytes bounds how much of an entry is read: a verdict is a few
// kilobytes of check output, and a cache file is not trusted to be one.
const maxEntryBytes = 1 << 20

// ownEntry reports whether a cache file is one this user could have
// written and nobody else could have: a regular file, owned by the
// effective user, writable by no group and no other user.
func ownEntry(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// writeCache records a pass. Written to a temporary file and renamed, so
// that a run interrupted mid-write leaves no half-written entry for the
// next one to read as a verdict.
func writeCache(dir string, res Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating the verdict cache %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cacheEntry{Version: cacheVersion, Result: res}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".verdict-")
	if err != nil {
		return fmt.Errorf("writing to the verdict cache %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), cachePath(dir, res.Key))
}
