// Package actionscache is the one thing a CI job leaves behind: this
// deployment's GitHub Actions cache, kept on the controller and served
// to every sandbox, so that what one run built the next run can read.
//
// A grain CI job runs in a sandbox that is destroyed when the job ends,
// and everything in it goes with it: /tmp, $HOME, the Docker image
// store, the buildx cache. A workflow that already writes
// `cache-from: type=gha` therefore spent its whole life exporting into
// a cache nothing would ever read back -- act does run an Actions cache
// server of its own, and does hand a job ACTIONS_CACHE_URL, but that
// server lives in the sandbox and dies with it.
//
// So the cache moves off the machine that is thrown away and onto the
// one that is not. It is wired the way pkg/pkgcache is, for the same
// reasons: one HTTP service in the controller, on the address every
// sandbox backend can already reach (cmd/grain's -granule-git-proxy-host
// and its siblings), with no mount, no route, no change to any sandbox
// image, and nothing for a workflow file to opt into -- `type=gha` is
// what a workflow written for GitHub already says.
//
// # What it speaks
//
// The Actions cache API as `actions/cache` and buildkit's `gha` backend
// use it, which is the same handful of endpoints act implements in its
// own pkg/artifactcache:
//
//	GET    <base>/<token>/_apis/artifactcache/cache?keys=&version=
//	POST   <base>/<token>/_apis/artifactcache/caches
//	PATCH  <base>/<token>/_apis/artifactcache/caches/<id>
//	POST   <base>/<token>/_apis/artifactcache/caches/<id>
//	GET    <base>/<token>/_apis/artifactcache/artifacts/<id>
//
// An entry is reserved, uploaded in ranges, committed, and afterwards
// found by key and version: an exact match on the first key, then a
// prefix match on each restore key in turn, newest first, as GitHub
// documents and act implements. Keys are matched case-insensitively.
// A version is an opaque string the client derives from what it is
// caching (for buildkit, the cache manifest's shape), and an entry is
// only ever handed to a lookup naming the same one -- which is what
// makes two jobs sharing a cache safe rather than merely likely to work.
//
// # Who may read it
//
// Entries belong to a repository, and a job reaches its own
// repository's entries and no others. The <token> in the path above is
// minted when a job's sandbox is built and revoked when that sandbox is
// released (Scopes, and cmd/grain's ciSandboxes), exactly as the git
// proxy's read scope is: a token that outlives its job, or is read out
// of the file it was written to, names nothing.
//
// Within a repository, entries belong to the branch whose CI wrote them,
// after GitHub's own rule for actions/cache that a branch reads its own
// entries and its base's, and a base never reads a branch's. Here "its
// base's" is what trusted release runs wrote, since a release is the
// only run nobody else can start. Without that boundary, a branch's CI --
// whose workflow file the branch itself can rewrite -- could upload an
// entry under exactly the key a release build restores, and that build
// would restore it (issue #2392).
//
// So every entry records the ref it was written from and whether the
// job that wrote it was a trusted release run (Scope), and:
//
//   - an ordinary job (a push, a pull request, a manual or scheduled
//     run, on any branch) writes under its own ref, and restores its own
//     ref's entries first and, failing those, what release runs wrote.
//     It never restores another branch's entries.
//   - a release run, the one event grain starts itself and no caller
//     can ask for, writes trusted entries and restores trusted entries
//     only. Nothing an ordinary job uploads reaches it, not even one
//     from a push to the branch it is releasing.
//   - an upload in progress may only be added to and committed by a job
//     of the scope that reserved it, and an archive is only downloaded
//     by a job that could have found it. Ids are sequential, so neither
//     may rest on an id being hard to guess.
//
// What a branch shares with its own later runs is therefore the whole of
// what this is for on a task branch -- the second push of a branch
// builds warm -- and what a branch shares with the rest of the
// repository is what release runs leave behind.
//
// # Who cleans it
//
// One byte cap across all repositories, enforced by throwing away the
// entries nobody has read for longest. The cap is a global one rather
// than one per repository because the failure it exists to prevent is a
// disk-shaped one: CI admission here already holds jobs on a free-space
// floor, so a cache that grew without a bound would start refusing the
// jobs it was meant to make fast, and "N GiB per repository" is a bound
// only if somebody counts the repositories. A busy repository can
// evict a quiet one's entries; a cache miss costs a cold build and
// nothing else.
//
// It is a cache and nothing else. An empty one behaves exactly like no
// cache at all, a damaged entry is one miss away from being rebuilt,
// and Flush may throw the whole of it away at any moment, including
// while jobs are using it.
package actionscache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultMaxBytes is how much disk the cache helps itself to when a
// deployment has not said. 20GiB is several full layer sets for a
// repository that builds images -- the case this exists for -- and
// small enough to be an unremarkable thing to find in a data directory
// next to the package cache's own 20.
const DefaultMaxBytes int64 = 20 << 30

// DefaultMaxEntryBytes is the largest single entry worth keeping.
// Anything larger is refused at reservation, which a client treats as
// "this could not be cached" and carries on from; storing it instead
// would mean one entry evicting most of the cache to hold something
// that is itself too big to be worth a round trip.
const DefaultMaxEntryBytes int64 = 4 << 30

// reservationTTL is how long a reserved-but-never-committed entry is
// kept before an eviction pass removes it. An upload that is still
// going touches its entry with every range it writes, so this bounds
// the wreckage of a job that died mid-upload rather than the patience
// of one that is still working.
const reservationTTL = 6 * time.Hour

// Entry is one cached archive: what a lookup matches on, and what
// eviction sorts by.
type Entry struct {
	// ID is what the URL of this entry names it by. Allocated in order,
	// never reused, and unique across repositories -- the repository is
	// checked on every request rather than being read off the id.
	ID uint64 `json:"id"`
	// Repo is "owner/name": no other repository's job ever reads this
	// entry back.
	Repo string `json:"repo"`
	// Ref and Trusted are the Scope of the job that wrote it, which is
	// what decides which of Repo's jobs may read it back (Scope.reads).
	Ref     string `json:"ref"`
	Trusted bool   `json:"trusted,omitempty"`
	// Key and Version are the client's own two halves of a cache
	// identity: the key it looks up by (lower-cased, as GitHub matches
	// them) and the version that must match exactly.
	Key     string `json:"key"`
	Version string `json:"version"`
	// Size is the committed length in bytes; while an entry is still
	// being uploaded it is what the client said it would be, or -1 from
	// a client that did not say (actions/cache v2 does not).
	Size int64 `json:"size"`
	// Complete is set by Commit. Nothing incomplete is ever found by a
	// lookup: a half-uploaded archive restored into a build is worse
	// than no cache at all.
	Complete  bool      `json:"complete"`
	CreatedAt time.Time `json:"createdAt"`
	// UsedAt is what eviction reads -- touched by every lookup that
	// returns this entry and every range written into it, so what
	// survives a full cache is what CI is actually using.
	UsedAt time.Time `json:"usedAt"`
}

// Cache is the on-disk half: entries under one directory each, an index
// of them in memory, and a size bound it keeps by evicting the least
// recently used.
//
// Safe for concurrent use, and it has to be: several jobs of several
// runs upload and look up at once. The index is guarded by mu and every
// long operation -- copying an upload to disk, concatenating an entry
// at commit -- happens outside it, so a 2GiB layer being written never
// blocks a lookup.
type Cache struct {
	dir string

	mu       sync.Mutex
	maxBytes int64
	maxEntry int64
	nextID   uint64
	entries  map[uint64]*Entry
	bytes    int64

	// evicting makes eviction passes exclusive of each other: two at
	// once would each measure the same over-full cache and each throw
	// away enough to fix it, which is twice as much as anybody wanted.
	evicting sync.Mutex
}

// Stats is what the cache holds, for an operator and for a log line.
type Stats struct {
	Entries  int
	Bytes    int64
	MaxBytes int64
	// Repos is bytes held per repository, which is the only breakdown
	// worth having: it answers "who is filling this".
	Repos map[string]int64
}

// NewCache opens (creating if need be) a cache under dir. maxBytes <= 0
// means DefaultMaxBytes.
//
// It reads what is already there, which is what makes the cache survive
// a restart -- and drops anything incomplete while it is doing it,
// since an upload that was in flight when the daemon stopped has no
// client left to finish it.
func NewCache(dir string, maxBytes int64) (*Cache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("actionscache: no directory to cache in")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("actionscache: creating %s: %w", dir, err)
	}
	c := &Cache{
		dir:      dir,
		maxBytes: maxBytes,
		maxEntry: DefaultMaxEntryBytes,
		entries:  map[uint64]*Entry{},
		// Ids start at one, and it matters: `actions/cache` decides
		// whether its reservation succeeded by testing the cacheId it
		// was given for truth, in JavaScript, where zero is false. An
		// entry numbered 0 is therefore reported to the job as "unable
		// to reserve cache ... another job may be creating this cache"
		// and never uploaded -- which is a cache that silently never
		// fills, found here only by running a real act against a real
		// one. act's own server starts at one for the same reason.
		nextID: 1,
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("actionscache: reading %s: %w", dir, err)
	}
	for _, name := range names {
		if !name.IsDir() {
			continue
		}
		id, err := strconv.ParseUint(name.Name(), 10, 64)
		if err != nil {
			continue
		}
		entry, err := c.readMeta(id)
		if err != nil || !entry.Complete || entry.Ref == "" {
			// Either a directory this package did not write, an upload
			// nobody is going to finish, or an entry written before
			// entries recorded their ref -- which no job may read, since
			// nothing says which branch it came from. All garbage, and
			// leaving them would be a slow leak of the disk this cache
			// is supposed to be bounded on.
			_ = os.RemoveAll(c.entryDir(id))
			continue
		}
		c.entries[id] = entry
		c.bytes += entry.Size
		if id >= c.nextID {
			c.nextID = id + 1
		}
	}
	return c, nil
}

// Dir is where the cache keeps its files, for a log line that has to
// tell an operator which directory to look in.
func (c *Cache) Dir() string { return c.dir }

// MaxBytes is the bound this cache is holding itself to.
func (c *Cache) MaxBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxBytes
}

// SetMaxBytes changes the bound, evicting straight away if the cache is
// already over the new one. maxBytes <= 0 means DefaultMaxBytes.
func (c *Cache) SetMaxBytes(maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	c.mu.Lock()
	c.maxBytes = maxBytes
	over := c.bytes > c.maxBytes
	c.mu.Unlock()
	if over {
		c.evict()
	}
}

// SetMaxEntryBytes bounds a single entry. Zero or less means
// DefaultMaxEntryBytes. Exported for a test that would otherwise have
// to write four gigabytes to reach the limit.
func (c *Cache) SetMaxEntryBytes(max int64) {
	if max <= 0 {
		max = DefaultMaxEntryBytes
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxEntry = max
}

// ErrTooLarge is what Reserve answers for an entry bigger than a single
// entry may be. It is not a failure of the request: the client is
// expected to carry on without caching, which is what both
// actions/cache and buildkit do.
var ErrTooLarge = errors.New("actionscache: larger than one cache entry may be")

// ErrNotReserved is what the upload and commit halves answer for an id
// that names no reservation of this scope's -- a stale id, an entry
// evicted while its own upload was in flight, or another repository's,
// branch's or trust's.
var ErrNotReserved = errors.New("actionscache: no such reservation")

// Reserve starts an entry for scope under key and version, and answers
// the id the client will upload against. size is what the client says
// the archive will be, or -1 where it has not said.
func (c *Cache) Reserve(scope Scope, key, version string, size int64) (uint64, error) {
	scope = scope.normalize()
	key = strings.ToLower(strings.TrimSpace(key))
	if !scope.valid() || key == "" {
		return 0, errors.New("actionscache: a cache entry needs a repository, a ref and a key")
	}
	c.mu.Lock()
	maxEntry := c.maxEntry
	c.mu.Unlock()
	if size > maxEntry {
		return 0, fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, size, maxEntry)
	}
	if size == 0 {
		// An `actions/cache@v2` client sends no size at all, which
		// decodes as zero. -1 is how the rest of this package spells
		// "unknown", the way act's own handler does.
		size = -1
	}

	now := time.Now().UTC()
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	entry := &Entry{ID: id, Repo: scope.Repo, Ref: scope.Ref, Trusted: scope.Trusted, Key: key, Version: version, Size: size, CreatedAt: now, UsedAt: now}
	c.entries[id] = entry
	c.mu.Unlock()

	if err := os.MkdirAll(c.partsDir(id), 0o755); err != nil {
		c.drop(id)
		return 0, fmt.Errorf("actionscache: reserving %s: %w", key, err)
	}
	if err := c.writeMeta(entry); err != nil {
		c.drop(id)
		return 0, err
	}
	return id, nil
}

// Write stores one uploaded range of a reserved entry. Ranges arrive in
// any order and are stitched together by Commit, which is what the
// client's own parallel upload needs.
//
// A range is bounded as it is written rather than only at Commit: the
// bytes land on disk before anything counts them against the cache's
// own bound, so an upload that ran on past the largest entry -- or past
// the size it reserved -- would otherwise fill the disk before Commit
// ever got to refuse it.
func (c *Cache) Write(scope Scope, id uint64, offset int64, r io.Reader) error {
	entry, err := c.reservation(scope, id)
	if err != nil {
		return err
	}
	if offset < 0 {
		return fmt.Errorf("actionscache: %d is not an offset", offset)
	}
	c.mu.Lock()
	limit := c.maxEntry
	c.mu.Unlock()
	if entry.Size >= 0 && entry.Size < limit {
		limit = entry.Size
	}
	if offset > limit {
		return fmt.Errorf("%w (a range at offset %d, limit %d)", ErrTooLarge, offset, limit)
	}
	name := filepath.Join(c.partsDir(id), fmt.Sprintf("%016x", offset))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit-offset+1))
	if err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if offset+n > limit {
		os.Remove(name)
		return fmt.Errorf("%w (a range at offset %d runs past %d bytes)", ErrTooLarge, offset, limit)
	}
	c.touch(id)
	return nil
}

// Commit finishes an entry: its ranges are concatenated in order into
// the entry's body, the length is checked against what was promised,
// and only then is it something a lookup can find. It answers the size
// that was actually stored.
//
// A body whose length does not match the reservation is thrown away
// rather than kept: a truncated cache archive is restored into a build
// as if it were whole, and what that produces is a failure in whatever
// the build does next rather than a cache miss.
func (c *Cache) Commit(scope Scope, id uint64) (int64, error) {
	entry, err := c.reservation(scope, id)
	if err != nil {
		return 0, err
	}
	parts, err := os.ReadDir(c.partsDir(id))
	if err != nil {
		return 0, fmt.Errorf("actionscache: committing %d: %w", id, err)
	}
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if !p.IsDir() {
			names = append(names, p.Name())
		}
	}
	// The range files are named after their offset, zero-padded hex, so
	// the order the bytes go back together in is the order of the names.
	sort.Strings(names)
	if len(names) == 0 {
		// Nothing was uploaded -- every range refused, or none sent. An
		// empty body is no archive any client wrote, and committing one
		// would hand the next lookup a hit that restores nothing.
		c.drop(id)
		return 0, fmt.Errorf("actionscache: committing %d: nothing was uploaded", id)
	}

	body, err := os.Create(c.bodyPath(id))
	if err != nil {
		return 0, fmt.Errorf("actionscache: committing %d: %w", id, err)
	}
	var written int64
	for _, name := range names {
		// Each range has to start where the last one ended. A gap or an
		// overlap would concatenate into an archive whose bytes are not
		// where the client put them, which a reservation of unknown size
		// would otherwise commit without anything noticing.
		if offset, err := strconv.ParseInt(name, 16, 64); err != nil || offset != written {
			body.Close()
			c.drop(id)
			return 0, fmt.Errorf("actionscache: committing %d: the range at %s does not start at byte %d, where the last one ended", id, name, written)
		}
		f, err := os.Open(filepath.Join(c.partsDir(id), name))
		if err != nil {
			body.Close()
			c.drop(id)
			return 0, fmt.Errorf("actionscache: committing %d: %w", id, err)
		}
		n, err := io.Copy(body, f)
		f.Close()
		written += n
		if err != nil {
			body.Close()
			c.drop(id)
			return 0, fmt.Errorf("actionscache: committing %d: %w", id, err)
		}
	}
	if err := body.Close(); err != nil {
		c.drop(id)
		return 0, fmt.Errorf("actionscache: committing %d: %w", id, err)
	}
	if entry.Size >= 0 && written != entry.Size {
		c.drop(id)
		return 0, fmt.Errorf("actionscache: %s was uploaded as %d bytes, not the %d it reserved", entry.Key, written, entry.Size)
	}
	c.mu.Lock()
	maxEntry := c.maxEntry
	c.mu.Unlock()
	if written > maxEntry {
		c.drop(id)
		return 0, fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, written, maxEntry)
	}
	_ = os.RemoveAll(c.partsDir(id))

	now := time.Now().UTC()
	c.mu.Lock()
	live, ok := c.entries[id]
	if !ok {
		// Evicted while its own upload was in flight. The bytes are
		// already gone from the index, so put nothing back.
		c.mu.Unlock()
		c.drop(id)
		return 0, ErrNotReserved
	}
	live.Size = written
	live.Complete = true
	live.UsedAt = now
	c.bytes += written
	stored := *live
	c.mu.Unlock()

	if err := c.writeMeta(&stored); err != nil {
		c.drop(id)
		return 0, err
	}
	c.evict()
	return written, nil
}

// Find answers the entry a lookup of keys at version should restore, as
// GitHub matches them: the first key exactly, then each key in turn as
// a prefix, newest first -- all of that against the job's own ref, and
// only then against the entries it may fall back on (Scope.reads). Only
// complete entries are ever considered.
func (c *Cache) Find(scope Scope, keys []string, version string) (Entry, bool) {
	scope = scope.normalize()
	if !scope.valid() {
		return Entry{}, false
	}
	c.mu.Lock()
	var own, fallback []Entry
	for _, e := range c.entries {
		if !e.Complete || e.Version != version {
			continue
		}
		switch scope.reads(e) {
		case 2:
			own = append(own, *e)
		case 1:
			fallback = append(fallback, *e)
		}
	}
	c.mu.Unlock()

	for _, candidates := range [][]Entry{own, fallback} {
		// Newest first, so that both halves of the match below -- exact
		// and prefix -- answer with the most recent entry that qualifies.
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.After(candidates[j].CreatedAt) })
		for _, key := range keys {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" {
				continue
			}
			for _, e := range candidates {
				if e.Key == key {
					return c.hit(e.ID)
				}
			}
			for _, e := range candidates {
				if strings.HasPrefix(e.Key, key) {
					return c.hit(e.ID)
				}
			}
		}
	}
	return Entry{}, false
}

// hit is the tail of Find: the entry's body is checked to be there
// before it is offered, since an entry whose file has gone is a
// redirect to a 404 rather than a cache hit, and it is touched so that
// what a job is reading survives the next eviction pass.
func (c *Cache) hit(id uint64) (Entry, bool) {
	if _, err := os.Stat(c.bodyPath(id)); err != nil {
		c.drop(id)
		return Entry{}, false
	}
	c.touch(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	live, ok := c.entries[id]
	if !ok {
		return Entry{}, false
	}
	return *live, true
}

// Body is the path of a committed entry's archive, for the handler to
// serve. It answers only for an entry Find could have answered this
// scope with, so an id guessed out of another repository's URL, or
// another branch's, answers nothing.
func (c *Cache) Body(scope Scope, id uint64) (string, bool) {
	scope = scope.normalize()
	c.mu.Lock()
	entry, ok := c.entries[id]
	match := ok && entry.Complete && scope.valid() && scope.reads(entry) > 0
	c.mu.Unlock()
	if !match {
		return "", false
	}
	c.touch(id)
	return c.bodyPath(id), true
}

// Stats reports what the cache holds.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := Stats{MaxBytes: c.maxBytes, Repos: map[string]int64{}}
	for _, e := range c.entries {
		if !e.Complete {
			continue
		}
		stats.Entries++
		stats.Bytes += e.Size
		stats.Repos[e.Repo] += e.Size
	}
	return stats
}

// Flush throws entries away: one repository's where repo names one,
// everything otherwise. It answers how many entries went.
//
// Allowed at any moment, including while jobs are using the cache: the
// worst a flush can do to a job in flight is turn a hit it has not made
// yet into a miss.
func (c *Cache) Flush(repo string) int {
	repo = strings.TrimSpace(repo)
	c.mu.Lock()
	ids := make([]uint64, 0, len(c.entries))
	for id, e := range c.entries {
		if repo == "" || e.Repo == repo {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.drop(id)
	}
	return len(ids)
}

// evict brings the cache back under its bound by removing the entries
// nobody has read for longest, and clears out reservations nobody is
// going to finish while it is there.
func (c *Cache) evict() {
	c.evicting.Lock()
	defer c.evicting.Unlock()

	cutoff := time.Now().UTC().Add(-reservationTTL)
	c.mu.Lock()
	var stale []uint64
	live := make([]Entry, 0, len(c.entries))
	for id, e := range c.entries {
		if !e.Complete {
			if e.UsedAt.Before(cutoff) {
				stale = append(stale, id)
			}
			continue
		}
		live = append(live, *e)
	}
	over := c.bytes - c.maxBytes
	c.mu.Unlock()

	for _, id := range stale {
		c.drop(id)
	}
	if over <= 0 {
		return
	}
	// Least recently used first: what a job read on its last run is the
	// last thing to go.
	sort.Slice(live, func(i, j int) bool { return live[i].UsedAt.Before(live[j].UsedAt) })
	for _, e := range live {
		if over <= 0 {
			return
		}
		over -= e.Size
		c.drop(e.ID)
	}
}

// reservation is the check both upload halves make: this id names an
// entry, of exactly this scope, that has not been committed yet.
func (c *Cache) reservation(scope Scope, id uint64) (Entry, error) {
	scope = scope.normalize()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok || !scope.valid() || !scope.writes(entry) {
		return Entry{}, ErrNotReserved
	}
	if entry.Complete {
		return Entry{}, fmt.Errorf("actionscache: %d (%s) is already committed", id, entry.Key)
	}
	return *entry, nil
}

// touch records that an entry was used just now, in the index and on
// disk. The on-disk half is what makes an eviction order survive a
// restart, and it is a small file written next to a large one.
func (c *Cache) touch(id uint64) {
	now := time.Now().UTC()
	c.mu.Lock()
	entry, ok := c.entries[id]
	if ok {
		entry.UsedAt = now
	}
	var copied Entry
	if ok {
		copied = *entry
	}
	c.mu.Unlock()
	if ok {
		_ = c.writeMeta(&copied)
	}
}

// drop removes an entry from the index and from disk. It is the one
// place bytes come off the total, so it is also the one place that has
// to be safe to call twice -- eviction, a failed commit and a flush can
// all reach the same entry.
func (c *Cache) drop(id uint64) {
	c.mu.Lock()
	entry, ok := c.entries[id]
	if ok {
		delete(c.entries, id)
		if entry.Complete {
			c.bytes -= entry.Size
			if c.bytes < 0 {
				c.bytes = 0
			}
		}
	}
	c.mu.Unlock()
	_ = os.RemoveAll(c.entryDir(id))
}

func (c *Cache) entryDir(id uint64) string { return filepath.Join(c.dir, strconv.FormatUint(id, 10)) }
func (c *Cache) bodyPath(id uint64) string { return filepath.Join(c.entryDir(id), "body") }
func (c *Cache) partsDir(id uint64) string { return filepath.Join(c.entryDir(id), "parts") }
func (c *Cache) metaPath(id uint64) string { return filepath.Join(c.entryDir(id), "meta.json") }

func (c *Cache) writeMeta(entry *Entry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("actionscache: recording entry %d: %w", entry.ID, err)
	}
	if err := os.MkdirAll(c.entryDir(entry.ID), 0o755); err != nil {
		return fmt.Errorf("actionscache: recording entry %d: %w", entry.ID, err)
	}
	if err := os.WriteFile(c.metaPath(entry.ID), encoded, 0o644); err != nil {
		return fmt.Errorf("actionscache: recording entry %d: %w", entry.ID, err)
	}
	return nil
}

func (c *Cache) readMeta(id uint64) (*Entry, error) {
	raw, err := os.ReadFile(c.metaPath(id))
	if err != nil {
		return nil, err
	}
	var entry Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, err
	}
	if entry.ID != id {
		return nil, fmt.Errorf("actionscache: %s holds entry %d", c.metaPath(id), entry.ID)
	}
	info, err := os.Stat(c.bodyPath(id))
	if err != nil {
		return nil, err
	}
	if entry.Complete && info.Size() != entry.Size {
		return nil, fmt.Errorf("actionscache: entry %d is %d bytes, not the %d recorded beside it", id, info.Size(), entry.Size)
	}
	return &entry, nil
}
