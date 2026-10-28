// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package actionscache

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// branch is the scope most of these tests run as: an ordinary job on
// the repository's main branch.
func branch(repo string) Scope { return Scope{Repo: repo, Ref: "main"} }

// store writes one whole entry the way a client does: reserve, upload
// it in one range, commit.
func store(t *testing.T, c *Cache, repo, key, version string, body []byte) uint64 {
	t.Helper()
	return storeAs(t, c, branch(repo), key, version, body)
}

// storeAs is store for a job of any scope.
func storeAs(t *testing.T, c *Cache, scope Scope, key, version string, body []byte) uint64 {
	t.Helper()
	id, err := c.Reserve(scope, key, version, int64(len(body)))
	if err != nil {
		t.Fatalf("reserving %s: %v", key, err)
	}
	if err := c.Write(scope, id, 0, bytes.NewReader(body)); err != nil {
		t.Fatalf("uploading %s: %v", key, err)
	}
	if _, err := c.Commit(scope, id); err != nil {
		t.Fatalf("committing %s: %v", key, err)
	}
	return id
}

func newCache(t *testing.T, maxBytes int64) *Cache {
	t.Helper()
	c, err := NewCache(t.TempDir(), maxBytes)
	if err != nil {
		t.Fatalf("opening a cache: %v", err)
	}
	return c
}

// The whole point of the package, in one test: what one job wrote, the
// next job reads -- by its exact key, and by a restore key that is only
// a prefix of it.
func TestCacheRoundTrip(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	body := []byte("layer bytes")
	store(t, c, "acme/widgets", "buildkit-image-abc123", "v1", body)

	entry, ok := c.Find(branch("acme/widgets"), []string{"buildkit-image-abc123"}, "v1")
	if !ok {
		t.Fatal("the entry just written was not found by its own key")
	}
	if entry.Size != int64(len(body)) {
		t.Errorf("entry is %d bytes, want %d", entry.Size, len(body))
	}
	path, ok := c.Body(branch("acme/widgets"), entry.ID)
	if !ok {
		t.Fatal("a committed entry has no body to serve")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored body = %q, %v, want %q", got, err, body)
	}

	// A miss on the exact key falls back to the restore keys, prefix by
	// prefix, which is how a workflow gets last week's layers when this
	// week's key has moved.
	if _, ok := c.Find(branch("acme/widgets"), []string{"buildkit-image-def456", "buildkit-image-"}, "v1"); !ok {
		t.Error("a restore key that is a prefix of a stored key found nothing")
	}
	// A version is not negotiable: a cache archive of a different shape
	// restored into a build is worse than a cold build.
	if _, ok := c.Find(branch("acme/widgets"), []string{"buildkit-image-abc123"}, "another-version"); ok {
		t.Error("an entry was handed to a lookup naming another version")
	}
}

// GitHub matches cache keys case-insensitively, so a workflow whose key
// comes out of a branch name does not get two caches for one thing.
func TestCacheKeysAreCaseInsensitive(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	store(t, c, "acme/widgets", "Node-Modules-ABC", "v1", []byte("x"))
	if _, ok := c.Find(branch("acme/widgets"), []string{"node-modules-abc"}, "v1"); !ok {
		t.Error("a key differing only in case missed")
	}
	if _, ok := c.Find(branch("acme/widgets"), []string{"NODE-MODULES-"}, "v1"); !ok {
		t.Error("a restore key differing only in case missed")
	}
}

// The newest entry wins, both for an exact key and for a prefix, which
// is what makes a cache that is written on every push behave like one.
func TestCacheFindPrefersTheNewest(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	store(t, c, "acme/widgets", "deps-1", "v1", []byte("old"))
	time.Sleep(2 * time.Millisecond)
	store(t, c, "acme/widgets", "deps-2", "v1", []byte("new"))

	entry, ok := c.Find(branch("acme/widgets"), []string{"deps-"}, "v1")
	if !ok || entry.Key != "deps-2" {
		t.Fatalf("prefix match found %+v, %v, want deps-2", entry, ok)
	}
}

// One repository's entries are another repository's nothing: the whole
// of the isolation this service has, so it is checked from both ends --
// the lookup and the body.
func TestCacheIsolatesRepositories(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id := store(t, c, "acme/widgets", "shared-key", "v1", []byte("secret layer"))

	if _, ok := c.Find(branch("evil/fork"), []string{"shared-key"}, "v1"); ok {
		t.Error("another repository found this repository's entry by key")
	}
	if _, ok := c.Body(branch("evil/fork"), id); ok {
		t.Error("another repository was served this repository's entry by id")
	}
	if _, ok := c.Body(branch("acme/widgets"), id); !ok {
		t.Error("the repository that wrote the entry cannot read it back")
	}
}

// A half-uploaded entry is not a cache hit. It is the failure mode with
// the worst consequences -- a truncated archive restores into a build
// as if it were whole -- so nothing incomplete is ever findable.
func TestCacheHidesIncompleteEntries(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id, err := c.Reserve(branch("acme/widgets"), "half", "v1", 10)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 0, strings.NewReader("12345")); err != nil {
		t.Fatalf("uploading: %v", err)
	}
	if _, ok := c.Find(branch("acme/widgets"), []string{"half"}, "v1"); ok {
		t.Error("an entry that has not been committed was found")
	}
	if _, err := c.Commit(branch("acme/widgets"), id); err == nil {
		t.Error("committing 5 bytes against a 10 byte reservation was accepted")
	}
	if _, ok := c.Find(branch("acme/widgets"), []string{"half"}, "v1"); ok {
		t.Error("an entry whose commit was refused was found anyway")
	}
}

// An upload arrives in ranges, in whatever order the client sends them,
// and commit is what puts them back in order.
func TestCacheStitchesRangesBackTogether(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id, err := c.Reserve(branch("acme/widgets"), "ranged", "v1", 9)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 3, strings.NewReader("456")); err != nil {
		t.Fatalf("uploading the second range: %v", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 6, strings.NewReader("789")); err != nil {
		t.Fatalf("uploading the third range: %v", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 0, strings.NewReader("123")); err != nil {
		t.Fatalf("uploading the first range: %v", err)
	}
	if _, err := c.Commit(branch("acme/widgets"), id); err != nil {
		t.Fatalf("committing: %v", err)
	}
	path, _ := c.Body(branch("acme/widgets"), id)
	got, _ := os.ReadFile(path)
	if string(got) != "123456789" {
		t.Errorf("stored body = %q, want the ranges in offset order", got)
	}
}

// The bound is the promise this cache makes to the disk CI admission is
// gating on, so it is kept by throwing away what nothing has read for
// longest rather than by refusing to write.
func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	c := newCache(t, 300)
	body := make([]byte, 100)

	store(t, c, "acme/widgets", "old", "v1", body)
	store(t, c, "acme/widgets", "middle", "v1", body)
	// Reading "old" is what saves it: the entry a job is still using
	// must outlive the entry nobody has touched since it was written.
	if _, ok := c.Find(branch("acme/widgets"), []string{"old"}, "v1"); !ok {
		t.Fatal("the first entry went missing before anything was evicted")
	}
	store(t, c, "acme/widgets", "new", "v1", body)
	store(t, c, "acme/widgets", "newest", "v1", body)

	if _, ok := c.Find(branch("acme/widgets"), []string{"middle"}, "v1"); ok {
		t.Error("the least recently used entry survived an eviction pass")
	}
	for _, key := range []string{"old", "new", "newest"} {
		if _, ok := c.Find(branch("acme/widgets"), []string{key}, "v1"); !ok {
			t.Errorf("%q was evicted, and something older had been read less recently", key)
		}
	}
	if stats := c.Stats(); stats.Bytes > 300 {
		t.Errorf("the cache holds %d bytes, over its own bound of 300", stats.Bytes)
	}
}

// An entry too big to be worth keeping is refused at reservation, which
// a client reads as "carry on without a cache" rather than as an error.
func TestCacheRefusesAnOversizeEntry(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	c.SetMaxEntryBytes(16)
	if _, err := c.Reserve(branch("acme/widgets"), "huge", "v1", 1024); err == nil {
		t.Fatal("an entry over the per-entry limit was reserved")
	}
	// And again for a client that did not say how big it would be: the
	// length is only known once it has all arrived.
	id, err := c.Reserve(branch("acme/widgets"), "huge", "v1", -1)
	if err != nil {
		t.Fatalf("reserving without a size: %v", err)
	}
	// The range is refused as it is written, rather than kept on disk
	// until a commit that may never come.
	if err := c.Write(branch("acme/widgets"), id, 0, bytes.NewReader(make([]byte, 64))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("uploading 64 bytes against a 16 byte limit = %v, want ErrTooLarge", err)
	}
	if _, err := c.Commit(branch("acme/widgets"), id); err == nil {
		t.Error("an oversize entry was committed by a client that never declared its size")
	}
}

// A range that runs past what was reserved is refused as it arrives:
// nothing counts reserved bytes against the cache's bound until commit,
// so an upload that kept going would otherwise fill the disk first.
func TestCacheRefusesARangePastItsReservation(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id, err := c.Reserve(branch("acme/widgets"), "small", "v1", 4)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 0, strings.NewReader("123456789")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("writing 9 bytes into a 4 byte reservation = %v, want ErrTooLarge", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 8, strings.NewReader("x")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("writing at offset 8 of a 4 byte reservation = %v, want ErrTooLarge", err)
	}
	if err := c.Write(branch("acme/widgets"), id, 0, strings.NewReader("1234")); err != nil {
		t.Fatalf("writing exactly what was reserved: %v", err)
	}
	if _, err := c.Commit(branch("acme/widgets"), id); err != nil {
		t.Errorf("committing a reservation filled exactly: %v", err)
	}
}

// Ranges that leave a gap or overlap do not stitch into the archive the
// client uploaded, and a reservation of unknown size has no length to
// catch that with -- so commit checks the ranges meet.
func TestCacheRefusesRangesThatDoNotMeet(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	for name, ranges := range map[string]map[int64]string{
		"gap":     {0: "123", 5: "678"},
		"overlap": {0: "123", 2: "3456"},
	} {
		id, err := c.Reserve(branch("acme/widgets"), name, "v1", -1)
		if err != nil {
			t.Fatalf("%s: reserving: %v", name, err)
		}
		for offset, body := range ranges {
			if err := c.Write(branch("acme/widgets"), id, offset, strings.NewReader(body)); err != nil {
				t.Fatalf("%s: uploading at %d: %v", name, offset, err)
			}
		}
		if _, err := c.Commit(branch("acme/widgets"), id); err == nil {
			t.Errorf("%s: ranges that do not meet were committed", name)
		}
		if _, ok := c.Find(branch("acme/widgets"), []string{name}, "v1"); ok {
			t.Errorf("%s: an entry whose commit was refused was found", name)
		}
	}
}

// The cache is the one thing that survives a sandbox; it had better
// survive the daemon too.
func TestCacheSurvivesAReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := NewCache(dir, 0)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	store(t, c, "acme/widgets", "kept", "v1", []byte("layer"))
	// An upload nobody is going to finish: there is no client left to
	// commit it after a restart, so it is garbage rather than an entry.
	if _, err := c.Reserve(branch("acme/widgets"), "abandoned", "v1", 99); err != nil {
		t.Fatalf("reserving: %v", err)
	}

	reopened, err := NewCache(dir, 0)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	entry, ok := reopened.Find(branch("acme/widgets"), []string{"kept"}, "v1")
	if !ok {
		t.Fatal("a committed entry did not survive the reopen")
	}
	if entry.Repo != "acme/widgets" || entry.Size != 5 {
		t.Errorf("entry came back as %+v", entry)
	}
	if stats := reopened.Stats(); stats.Entries != 1 || stats.Bytes != 5 {
		t.Errorf("stats after reopen = %+v, want the one committed entry", stats)
	}
	// And the abandoned reservation is not on the disk this cache is
	// bounded on any more.
	names, _ := os.ReadDir(dir)
	if len(names) != 1 {
		t.Errorf("%s holds %d entries, want only the committed one", dir, len(names))
	}
	// A new entry must not be filed under an id the reopened cache has
	// already handed out.
	id := store(t, reopened, "acme/widgets", "second", "v1", []byte("more"))
	if _, err := os.Stat(filepath.Join(dir, "0", "body")); err == nil && id == 0 {
		t.Error("the reopened cache reused an id it already had on disk")
	}
}

// `actions/cache` tests the cacheId it is handed for truth in
// JavaScript, where zero is false, and reports a reservation numbered 0
// as "unable to reserve cache" -- a cache that fills with nothing and
// says nothing about why. So the first entry is 1.
func TestCacheIDsStartAtOne(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	id, err := c.Reserve(branch("acme/widgets"), "first", "v1", 1)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if id == 0 {
		t.Error("the first entry was numbered 0, which every actions/cache client reads as a failed reservation")
	}
}

// Flush is allowed to throw everything away, and to throw one
// repository's away without touching another's.
func TestCacheFlush(t *testing.T) {
	t.Parallel()
	c := newCache(t, 0)
	store(t, c, "acme/widgets", "a", "v1", []byte("1"))
	store(t, c, "acme/widgets", "b", "v1", []byte("2"))
	store(t, c, "other/repo", "c", "v1", []byte("3"))

	if removed := c.Flush("acme/widgets"); removed != 2 {
		t.Errorf("flushing one repository removed %d entries, want 2", removed)
	}
	if _, ok := c.Find(branch("other/repo"), []string{"c"}, "v1"); !ok {
		t.Error("flushing one repository took another's entries with it")
	}
	if removed := c.Flush(""); removed != 1 {
		t.Errorf("flushing everything removed %d entries, want the 1 that was left", removed)
	}
	if stats := c.Stats(); stats.Entries != 0 || stats.Bytes != 0 {
		t.Errorf("stats after a full flush = %+v, want an empty cache", stats)
	}
}

// Stats is what an operator reads to find out who is filling the disk.
func TestCacheStatsPerRepo(t *testing.T) {
	t.Parallel()
	c := newCache(t, 1024)
	store(t, c, "acme/widgets", "a", "v1", make([]byte, 10))
	store(t, c, "other/repo", "b", "v1", make([]byte, 20))

	stats := c.Stats()
	if stats.Entries != 2 || stats.Bytes != 30 || stats.MaxBytes != 1024 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.Repos["acme/widgets"] != 10 || stats.Repos["other/repo"] != 20 {
		t.Errorf("per-repo bytes = %+v", stats.Repos)
	}
}

func TestCacheInvalidArgumentsAndNewCache(t *testing.T) {
	t.Parallel()
	if _, err := NewCache("", 0); err == nil {
		t.Error("NewCache with empty directory was accepted")
	}

	c := newCache(t, 1024)
	if c.Dir() == "" {
		t.Error("Dir() returned empty string")
	}
	if c.MaxBytes() != 1024 {
		t.Errorf("MaxBytes() = %d, want 1024", c.MaxBytes())
	}
	c.SetMaxBytes(2048)
	if c.MaxBytes() != 2048 {
		t.Errorf("MaxBytes() = %d, want 2048", c.MaxBytes())
	}
	c.SetMaxBytes(0) // resets to default
	if c.MaxBytes() != DefaultMaxBytes {
		t.Errorf("MaxBytes() after reset = %d, want %d", c.MaxBytes(), DefaultMaxBytes)
	}

	if _, err := c.Reserve(branch(""), "key", "v1", 10); err == nil {
		t.Error("Reserve with empty repo was accepted")
	}
	if _, err := c.Reserve(branch("repo"), "", "v1", 10); err == nil {
		t.Error("Reserve with empty key was accepted")
	}

	id, err := c.Reserve(branch("repo"), "key", "v1", 10)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	if err := c.Write(branch("repo"), id, -1, strings.NewReader("123")); err == nil {
		t.Error("Write with negative offset was accepted")
	}
	if err := c.Write(branch("repo"), 999999, 0, strings.NewReader("123")); err == nil {
		t.Error("Write to nonexistent ID was accepted")
	}
	if _, err := c.Commit(branch("repo"), 999999); err == nil {
		t.Error("Commit nonexistent ID was accepted")
	}
	if _, err := c.Commit(branch("wrong-repo"), id); err == nil {
		t.Error("Commit with wrong repo was accepted")
	}
}

func TestCacheReopenHandlesCorruptedEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := NewCache(dir, 0)
	if err != nil {
		t.Fatalf("opening cache: %v", err)
	}
	store(t, c, "acme/widgets", "valid", "v1", []byte("ok"))

	// Create corrupted entries directly in directory:
	// 1. Invalid JSON in meta.json
	corruptDir := filepath.Join(dir, "100")
	if err := os.MkdirAll(corruptDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "meta.json"), []byte("{bad-json"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Metadata with mismatching ID
	mismatchDir := filepath.Join(dir, "101")
	if err := os.MkdirAll(mismatchDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mismatchDir, "meta.json"), []byte(`{"id":999,"repo":"acme/widgets","key":"mismatch","version":"v1","complete":true}`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Complete metadata but missing body file
	missingBodyDir := filepath.Join(dir, "102")
	if err := os.MkdirAll(missingBodyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(missingBodyDir, "meta.json"), []byte(`{"id":102,"repo":"acme/widgets","key":"nobody","version":"v1","complete":true}`), 0644); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewCache(dir, 0)
	if err != nil {
		t.Fatalf("reopening cache with corrupt entries: %v", err)
	}
	if _, ok := reopened.Find(branch("acme/widgets"), []string{"valid"}, "v1"); !ok {
		t.Error("valid entry was lost after reopening with corrupt neighbor entries")
	}
	if _, ok := reopened.Find(branch("acme/widgets"), []string{"mismatch"}, "v1"); ok {
		t.Error("mismatched entry was found")
	}
	if _, ok := reopened.Find(branch("acme/widgets"), []string{"nobody"}, "v1"); ok {
		t.Error("entry with missing body was found")
	}
}
