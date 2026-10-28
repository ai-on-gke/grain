package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// DirCache is the Cache pkg/ci was designed against, as one file per key
// under a directory of the daemon's own.
//
// # Why a directory rather than a table in model.Store
//
// Because a cache entry is not state. Everything in model.Store is
// exported to the state repository as text and committed
// (pkg/staterepo), which is exactly right for the rows a human or an
// agent would want to read a diff of, and exactly wrong for this: a
// JobRun carries every step's output, up to MaxStepOutput of it each, so
// a cached run of a chatty test suite would be a few hundred kilobytes
// of build log committed to git, forever, per commit tested. A cache is
// disposable by definition -- losing it costs a re-run and nothing else
// -- and a host restored from the state repository wants its tasks back,
// not last month's build logs. That also keeps the whole of this out of
// the schema: no table, no SchemaVersion bump, no migration for a
// deployment that turns CI on later.
//
// # What bounds it
//
// Two bounds, because a key is a promise that the work is identical and
// so there is nothing here to invalidate: this is about disk, not about
// correctness (see Cache's own doc comment).
//
// The bounds themselves are not held here: they are read through a
// provider on every sweep, every Lookup and every Save (CacheBounds,
// StorageBounds), so a ceiling moved in the Settings pane binds the next
// sweep rather than the next restart (grain/task-317).
//
//   - Age. An entry older than MaxAge is not served and is swept. It is
//     not that the answer went stale -- it cannot -- it is that a commit
//     nobody has asked about in a month is one whose branch has long
//     since moved on, and its entry is dead weight.
//   - Size. MaxEntryBytes refuses to store one enormous result at all,
//     and MaxBytes/MaxEntries evict oldest-first until the directory is
//     back under both. A cache that grows without a ceiling is a
//     deployment that fills its data disk and stops dispatching, which
//     is a far worse day than a cache miss.
//
// Nothing here can fail a build. Lookup answers "no" for everything it
// cannot read or does not trust, an unreadable entry is dropped rather
// than reported, and the only errors it returns at all are the ones that
// say this cache is broken as a whole -- which the engine treats as a
// miss and runs the job.
type DirCache struct {
	dir string
	// bounds is what this cache may hold, asked afresh every time a bound
	// is needed rather than resolved once -- see DirCacheOptions.Bounds.
	// Never nil: OpenDirCache wraps a fixed CacheBounds in a func when
	// no provider was given.
	bounds func() workflows.CacheBounds
	now    func() time.Time
	log    func(format string, args ...any)

	mu        sync.Mutex
	epoch     string
	lastSweep time.Time
	hits      int64
	misses    int64
}

// sweepInterval is how often a Save is allowed to walk the directory.
// Entries only appear when a job finishes, so this is already a slow
// clock; the interval is what stops a burst of short jobs turning every
// one of them into a directory scan.
const sweepInterval = 5 * time.Minute

// tempEntryTTL is how long a half-written entry is left alone before the
// sweep removes it: long enough that no live Save can be interrupted by
// it, short enough that a daemon killed mid-write does not leak.
const tempEntryTTL = time.Hour

// cacheEntryVersion is the on-disk envelope's version, and is not
// fingerprintVersion: that one is the meaning of a key, this one is the
// shape of the file a key names. An entry written under another version
// is a miss.
//
// 2 because a JobRun's timestamps became optional (*time.Time, see the
// note above Run): an entry written under 1 spells "no time" as an
// instant in the year 1, which decodes into a pointer that is not nil
// and would put that instant back on the wire on the next cache hit.
// The whole point of the change is that nothing downstream has to
// disbelieve a timestamp any more, so the old entries are refused --
// which costs each of them one re-run, and a cache is disposable by
// definition (see DirCache).
const cacheEntryVersion = 2

// cacheEntry is one file: the result, plus enough about the entry itself
// to check it is the one that was asked for and to know how old it is.
//
// SavedAt rather than the file's mtime, because the file's mtime is a
// property of the filesystem -- a restore, a copy or an rsync rewrites
// it -- and "when was this work actually done" is a property of the
// answer. The sweep does use mtime, which is the cheap approximation it
// can afford for a whole directory; the entry Lookup is about to serve
// is checked against the honest one.
type cacheEntry struct {
	Version    int              `json:"version"`
	Key        workflows.Key    `json:"key"`
	SavedAt    time.Time        `json:"savedAt"`
	BaseCommit string           `json:"baseCommit,omitempty"`
	Result     workflows.JobRun `json:"result"`
}

// DirCacheOptions are OpenDirCache's knobs; every zero value means the
// corresponding default above.
type DirCacheOptions struct {
	MaxAge        time.Duration
	MaxBytes      int64
	MaxEntryBytes int64
	MaxEntries    int
	// Bounds, when set, is where this cache's bounds come from instead of
	// the four fields above, and it is called every time one is needed:
	// on each Lookup, each Save and each sweep, and by Stats, so what an
	// operator is shown is the ceiling actually in force.
	//
	// A function rather than a value because the bounds are stored
	// settings a daemon re-reads (model.Config.CICacheMB and its
	// neighbours, cmd/grain's liveConfig, which keeps them in memory and
	// refreshes them once a reconcile tick -- so this costs a mutex and
	// not a database read). A ceiling lowered in the Settings pane is
	// applied by the next sweep, within sweepInterval; an operator who
	// wants the disk back sooner has Clear, which is what the button in
	// the CI pane calls.
	//
	// The whole StorageBounds rather than this store's own half, because
	// the artifact store next door takes the same provider and a daemon
	// hands one closure to both (StorageBounds' own doc comment).
	Bounds func() workflows.StorageBounds
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Log, when set, is told what the cache declined to store and what it
	// swept -- the daemon's own log. nil discards.
	Log func(format string, args ...any)
	// Epoch identifies the environment generation (e.g. daemon build revision
	// and guest sandbox image digest). When non-empty, if OpenDirCache finds
	// that the cache directory was populated under a different epoch or has
	// unversioned legacy entries, it automatically clears previous cache entries
	// so that stale results from older daemon versions or guest environments do
	// not persist across upgrades.
	Epoch string
}

// OpenDirCache prepares dir and returns a cache over it, sweeping once
// on the way in so that a daemon which has been down for a month does
// not start by serving a directory of entries it would refuse one by
// one.
//
// It returns an error only for a directory it could not make or read at
// all. A caller that gets one should log it and run with no cache: every
// job runs, which is where every deployment was before this existed.
func OpenDirCache(dir string, opts DirCacheOptions) (*DirCache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("ci: a cache needs a directory to live in")
	}
	// 0o700: nothing but the daemon has any business reading build output
	// out of it, and a cache entry is one job's whole log.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ci: preparing the cache directory %s: %w", dir, err)
	}
	c := &DirCache{dir: dir, now: opts.Now, log: opts.Log, epoch: opts.Epoch}
	if opts.Bounds != nil {
		c.bounds = func() workflows.CacheBounds { return opts.Bounds().Cache.Resolved() }
	} else {
		// No provider: the four fields are this cache's bounds for the
		// life of the process, resolved once here so that the read path
		// is the same one either way.
		fixed := workflows.CacheBounds{
			MaxAge:        opts.MaxAge,
			MaxBytes:      opts.MaxBytes,
			MaxEntries:    opts.MaxEntries,
			MaxEntryBytes: opts.MaxEntryBytes,
		}.Resolved()
		c.bounds = func() workflows.CacheBounds { return fixed }
	}
	if _, err := os.ReadDir(dir); err != nil {
		return nil, fmt.Errorf("ci: reading the cache directory %s: %w", dir, err)
	}
	if opts.Epoch != "" {
		epochFile := filepath.Join(dir, ".cache-epoch")
		storedBytes, err := os.ReadFile(epochFile)
		if err == nil {
			storedEpoch := strings.TrimSpace(string(storedBytes))
			if storedEpoch != opts.Epoch {
				removed, clearErr := c.Clear(context.Background())
				if clearErr != nil {
					c.logf("ci: cache: clearing cache on epoch change (%s -> %s): %v", storedEpoch, opts.Epoch, clearErr)
				} else if removed > 0 {
					c.logf("ci: cache: cleared %d cached results on upgrade (%s -> %s)", removed, storedEpoch, opts.Epoch)
				}
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			if hasCacheEntries(dir) {
				removed, clearErr := c.Clear(context.Background())
				if clearErr != nil {
					c.logf("ci: cache: clearing unversioned cache on epoch setup (%s): %v", opts.Epoch, clearErr)
				} else if removed > 0 {
					c.logf("ci: cache: cleared %d unversioned cached results on upgrade to %s", removed, opts.Epoch)
				}
			}
		}
		if writeErr := writeEpochFile(epochFile, opts.Epoch); writeErr != nil {
			c.logf("ci: cache: writing epoch file %s: %v", epochFile, writeErr)
		}
	}
	c.Sweep()
	return c, nil
}

// Dir is where this cache keeps its entries -- for an operator being
// told where to look, and for a test.
func (c *DirCache) Dir() string { return c.dir }

func (c *DirCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *DirCache) logf(format string, args ...any) {
	if c.log != nil {
		c.log(format, args...)
	}
}

// Lookup answers from the file key names, or says no.
//
// Every reason to distrust what it finds -- a file that does not parse,
// an envelope from another build, a key that is not the one asked for, a
// result no longer old enough to serve or one this build would not have
// saved in the first place -- is a miss, and takes the file with it. An
// error is reserved for a cache that is broken as a whole (a directory
// that has stopped answering, a key that cannot name a file), because
// the engine's response to one is to log it and run the job.
//
// It is also the one place a hit is counted (CacheStats.Hits): every
// answer that is not a served entry is a miss, errors included, because
// that is exactly how the engine reads it.
func (c *DirCache) Lookup(ctx context.Context, key workflows.Key) (workflows.JobRun, bool, error) {
	result, ok, err := c.lookup(ctx, key)
	c.mu.Lock()
	if ok {
		c.hits++
	} else {
		c.misses++
	}
	c.mu.Unlock()
	return result, ok, err
}

func (c *DirCache) lookup(_ context.Context, key workflows.Key) (workflows.JobRun, bool, error) {
	path, err := c.path(key)
	if err != nil {
		return workflows.JobRun{}, false, err
	}
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return workflows.JobRun{}, false, nil
	case err != nil:
		return workflows.JobRun{}, false, fmt.Errorf("ci: reading cache entry for %s: %w", key, err)
	}

	var entry cacheEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		c.discard(path, fmt.Sprintf("it does not parse: %v", err))
		return workflows.JobRun{}, false, nil
	}
	switch {
	case entry.Version != cacheEntryVersion:
		c.discard(path, fmt.Sprintf("it is a version %d entry and this build writes %d", entry.Version, cacheEntryVersion))
		return workflows.JobRun{}, false, nil
	case entry.Key != key:
		// Two keys cannot name one file (see path), so this is a file
		// somebody else put here. Left alone rather than deleted: it is
		// not ours to remove.
		return workflows.JobRun{}, false, nil
	case !workflows.Cacheable(entry.Result):
		c.discard(path, fmt.Sprintf("a %q result is not one this build would have saved", entry.Result.Conclusion))
		return workflows.JobRun{}, false, nil
	case c.clock().Sub(entry.SavedAt) > c.bounds().MaxAge:
		c.discard(path, fmt.Sprintf("it is older than %s", c.bounds().MaxAge))
		return workflows.JobRun{}, false, nil
	}
	res := entry.Result
	if res.BaseCommit == "" && entry.BaseCommit != "" {
		res.BaseCommit = entry.BaseCommit
	}
	return res, true, nil
}

// Save writes result under key, and is best effort about everything that
// is not a programming error: a result too large to be worth keeping is
// declined and logged rather than failed, and the sweep that follows it
// is what keeps the directory inside its bounds.
//
// A result that is not cacheable is an error rather than a quiet
// no-op. The engine already checks (see runJob), so reaching this is a
// caller that has decided to cache "skipped" -- which would make one
// run's circumstances permanent for a commit -- and it should be loud.
func (c *DirCache) Save(_ context.Context, key workflows.Key, result workflows.JobRun) error {
	if !workflows.Cacheable(result) {
		return fmt.Errorf("ci: refusing to cache a %q result for %s: only a job that ran to a conclusion of its own is a fact about the job",
			result.Conclusion, key)
	}
	path, err := c.path(key)
	if err != nil {
		return err
	}

	// The entry is about the job, not about the run it happened in: the
	// key it is filed under is the key it is served for, and it was
	// produced by a sandbox rather than by a cache.
	result.Key, result.Cached = key, false
	// The artifacts go with the run rather than with the job, so they do
	// not go in here. They are files in one run's directory, swept on
	// their own schedule; a hit that advertised them would be offering a
	// download that belongs to a run the reader is not looking at, and
	// would keep offering it long after the sweep had removed it. A
	// reader who wants the files from a cached failure re-runs the job
	// (`grain ci run -job <id>`), which is what an artifact is for.
	result.Artifacts = nil
	body, err := json.Marshal(cacheEntry{
		Version:    cacheEntryVersion,
		Key:        key,
		SavedAt:    c.clock(),
		BaseCommit: result.BaseCommit,
		Result:     result,
	})
	if err != nil {
		return fmt.Errorf("ci: encoding the cache entry for %s: %w", key, err)
	}
	if maxEntry := c.bounds().MaxEntryBytes; int64(len(body)) > maxEntry {
		c.logf("ci: cache: %s produced %dKB of result, over the %dKB an entry may be -- not caching it",
			result.Name, len(body)/1024, maxEntry/1024)
		return nil
	}
	if err := c.writeFile(path, body); err != nil {
		return err
	}
	c.maybeSweep()
	return nil
}

// writeFile puts body at path atomically: a reader either sees the
// entry that was there before or the whole of the new one, never half of
// either. A half-written cache entry that parsed would be the one thing
// this package must never produce.
func (c *DirCache) writeFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(c.dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("ci: writing a cache entry in %s: %w", c.dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename below has moved it
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("ci: writing the cache entry %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ci: writing the cache entry %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("ci: writing the cache entry %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("ci: putting the cache entry at %s: %w", path, err)
	}
	return nil
}

// Discard drops the entry key names, saying why: what the engine does
// to a cached failure whose base commit has since moved.
func (c *DirCache) Discard(key workflows.Key, why string) {
	if p, err := c.path(key); err == nil {
		c.discard(p, why)
	}
}

// discard removes an entry this cache has stopped trusting, saying why.
// A removal that fails is only worth a log line: the entry is a miss
// either way, and the sweep will have another go at it.
func (c *DirCache) discard(path, why string) {
	c.logf("ci: cache: dropping %s: %s", filepath.Base(path), why)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.logf("ci: cache: removing %s: %v", path, err)
	}
}

// tempPrefix is what a half-written entry is called. It begins with a
// dot so it sorts and greps apart from real entries, and the sweep
// removes stale ones.
const tempPrefix = ".tmp-"

// path is the file a key names, and the one place a Key becomes a
// filename.
//
// Keys come from hashOf, which produces "<kind>:<64 hex digits>" and
// nothing else, so the only transformation needed is the colon -- and
// refusing every other character is what makes that fold injective (no
// two keys can name one file) as well as keeping a key from ever
// reaching for a path of its own.
func (c *DirCache) path(key workflows.Key) (string, error) {
	if key == "" {
		return "", fmt.Errorf("ci: the empty key names no cache entry")
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ':':
			b.WriteByte('-')
		default:
			return "", fmt.Errorf("ci: %q is not a key a cache entry can be named after", key)
		}
	}
	return filepath.Join(c.dir, b.String()+".json"), nil
}

// maybeSweep runs a sweep if one is due, so that a run of a dozen jobs
// walks the directory once rather than a dozen times.
func (c *DirCache) maybeSweep() {
	now := c.clock()
	c.mu.Lock()
	due := now.Sub(c.lastSweep) >= sweepInterval
	if due {
		c.lastSweep = now
	}
	c.mu.Unlock()
	if due {
		c.Sweep()
	}
}

// Sweep brings the directory back inside its bounds: everything past
// MaxAge goes, then the oldest entries go until what is left is under
// both MaxBytes and MaxEntries.
//
// Oldest-first rather than least-recently-used, because an entry's
// mtime is when the work was done and nothing here rewrites it on a hit:
// making a hit a write would turn the cheap half of this cache into a
// disk write per job, to order the evictions of a directory that is
// mostly evicted by age anyway.
//
// Every failure here is logged and stepped over. A sweep that cannot
// finish leaves a cache that is too big, which is a disk to watch; a
// sweep that gave up and returned an error would leave a build to
// re-run for it.
func (c *DirCache) Sweep() {
	now := c.clock()
	// One reading of the bounds for the whole sweep, so that a ceiling
	// edited halfway through it cannot make the age pass and the size
	// pass disagree about what this directory is allowed to be.
	bounds := c.bounds()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		c.logf("ci: cache: reading %s to sweep it: %v", c.dir, err)
		return
	}

	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var (
		kept    []item
		total   int64
		removed int
	)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // it went away underneath us, which is the outcome anyway
		}
		switch {
		case strings.HasPrefix(name, tempPrefix):
			// A Save that died between CreateTemp and Rename. Not removed
			// immediately: a Save happening right now owns one of these.
			if now.Sub(info.ModTime()) > tempEntryTTL {
				os.Remove(filepath.Join(c.dir, name))
				removed++
			}
		case !strings.HasSuffix(name, ".json"):
			// Not ours. Somebody else's file in this directory is their
			// business, and deleting one would make a mistyped directory
			// in the wiring destructive.
		case now.Sub(info.ModTime()) > bounds.MaxAge:
			os.Remove(filepath.Join(c.dir, name))
			removed++
		default:
			kept = append(kept, item{filepath.Join(c.dir, name), info.Size(), info.ModTime()})
			total += info.Size()
		}
	}

	sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
	count := len(kept)
	for i := 0; i < len(kept) && (total > bounds.MaxBytes || count > bounds.MaxEntries); i++ {
		if err := os.Remove(kept[i].path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.logf("ci: cache: removing %s: %v", kept[i].path, err)
			continue
		}
		total -= kept[i].size
		count--
		removed++
	}
	if removed > 0 {
		c.logf("ci: cache: swept %d entries from %s; %d left, %dKB", removed, c.dir, count, total/1024)
	}
}

// isCacheEntry reports whether a directory entry is one this cache
// wrote: what Stats counts and what Clear removes, stated once so those
// two cannot drift apart from each other or from the sweep's own rule
// about whose files these are.
func isCacheEntry(e fs.DirEntry) bool {
	name := e.Name()
	return !e.IsDir() && !strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, ".json")
}

// Stats is what this directory is holding, for an operator who until now
// could only reach it with a du(1) on the host.
//
// Read-only on purpose, including the part that looks like it should not
// be: an entry past MaxAge is counted, because it is still occupying the
// disk this is being asked about until a sweep takes it: Lookup refuses
// to serve one and the next sweep removes it. A stats call that swept as
// a side effect would be a pane deleting files every time it polled.
//
// Sizes and times come from the directory entries themselves rather than
// from each entry's envelope, which is what makes this one readdir and a
// stat per file rather than a parse of every cached build log. That is
// the same approximation the sweep runs on, for the same reason, and it
// is why Oldest and Newest are mtimes: honest for a directory only this
// daemon writes, and off by a restore or an rsync exactly as the sweep's
// own decisions would be.
func (c *DirCache) Stats(_ context.Context) (workflows.CacheStats, error) {
	// The bounds as they stand now rather than as this cache was opened
	// with, which is the whole point of reading them through a provider:
	// the Storage section draws its bar against what it is told here.
	bounds := c.bounds()
	c.mu.Lock()
	stats := workflows.CacheStats{
		Dir:           c.dir,
		LastSweep:     c.lastSweep,
		Hits:          c.hits,
		Misses:        c.misses,
		MaxAge:        bounds.MaxAge,
		MaxBytes:      bounds.MaxBytes,
		MaxEntries:    bounds.MaxEntries,
		MaxEntryBytes: bounds.MaxEntryBytes,
	}
	c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return workflows.CacheStats{}, fmt.Errorf("ci: reading the cache directory %s: %w", c.dir, err)
	}
	for _, e := range entries {
		if !isCacheEntry(e) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // it went away underneath us, so it is not held any more
		}
		stats.Entries++
		stats.Bytes += info.Size()
		mod := info.ModTime()
		if stats.Oldest.IsZero() || mod.Before(stats.Oldest) {
			stats.Oldest = mod
		}
		if mod.After(stats.Newest) {
			stats.Newest = mod
		}
	}
	return stats, nil
}

// Clear removes every entry, returning how many it removed.
//
// Always safe, and safe for a reason worth stating where the button that
// calls it can see it: a key is a promise that identical work was
// already done, so an entry is never the only copy of anything. Emptying
// this costs the re-runs it would have saved and nothing else -- which
// is why the question this answers was only ever "who may do it", and
// why an operator with a suspicion about a cached result should not have
// to `rm -rf` a directory on the host to act on it.
//
// It removes what this cache writes (`*.json`) and leaves everything
// else where it is, which is the sweep's own rule: a directory that was
// mistyped in the wiring should not become a directory this deletes. A
// half-written entry from a Save happening right now is left alone for
// the same reason, and the sweep collects it later (tempEntryTTL).
//
// A removal that fails is reported rather than swallowed -- an operator
// clearing a cache is acting on a suspicion, and "cleared" for a
// directory that still has entries in it would be the wrong thing to be
// told -- but the count says how far it got, and the entries it did
// remove are gone.
func (c *DirCache) Clear(_ context.Context) (int, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, fmt.Errorf("ci: reading the cache directory %s: %w", c.dir, err)
	}
	var (
		removed int
		failed  error
	)
	for _, e := range entries {
		if !isCacheEntry(e) {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if failed == nil {
				failed = fmt.Errorf("ci: clearing the cache in %s: %w", c.dir, err)
			}
			continue
		}
		removed++
	}
	c.logf("ci: cache: cleared %d entries from %s", removed, c.dir)
	return removed, failed
}

// PurgeNegativeEntries removes cached failures from this cache. If baseCommit is non-empty,
// only entries with ConclusionFailure recorded against exactly that base commit are removed:
// a cache entry carries a commit SHA and nothing that orders it against another, and the
// cache is shared by every branch and repository, so "older" is not something it can judge.
// Failures recorded against some other base are left to the engine, which discards a cached
// failure whose base differs from the run's own at lookup. If baseCommit is empty, all entries
// with ConclusionFailure are removed. Passing entries are never affected.
func (c *DirCache) PurgeNegativeEntries(_ context.Context, baseCommit string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, fmt.Errorf("ci: reading the cache directory %s: %w", c.dir, err)
	}
	var (
		removed int
		failed  error
	)
	for _, e := range entries {
		if !isCacheEntry(e) {
			continue
		}
		entryPath := filepath.Join(c.dir, e.Name())
		body, err := os.ReadFile(entryPath)
		if err != nil {
			continue
		}
		var entry cacheEntry
		if err := json.Unmarshal(body, &entry); err != nil {
			continue
		}
		if entry.Result.Conclusion != workflows.ConclusionFailure {
			continue
		}
		if baseCommit != "" && entry.BaseCommit != baseCommit {
			continue
		}
		if err := os.Remove(entryPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if failed == nil {
				failed = fmt.Errorf("ci: clearing failure from %s: %w", c.dir, err)
			}
			continue
		}
		removed++
	}
	if removed > 0 {
		c.logf("ci: cache: purged %d negative entries from %s (base %s)", removed, c.dir, baseCommit)
	}
	return removed, failed
}

// Epoch returns the epoch this cache is currently configured for.
func (c *DirCache) Epoch() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// SetEpoch updates the cache epoch. If the epoch has changed, it clears all
// cached entries so that results from an older epoch are not served, and
// persists the new epoch to disk.
func (c *DirCache) SetEpoch(ctx context.Context, epoch string) (int, error) {
	if epoch == "" {
		return 0, nil
	}
	c.mu.Lock()
	if c.epoch == epoch {
		c.mu.Unlock()
		return 0, nil
	}
	prev := c.epoch
	c.epoch = epoch
	c.mu.Unlock()

	removed, err := c.Clear(ctx)
	epochFile := filepath.Join(c.dir, ".cache-epoch")
	if writeErr := writeEpochFile(epochFile, epoch); writeErr != nil {
		c.logf("ci: cache: writing epoch file %s: %v", epochFile, writeErr)
	}
	if removed > 0 {
		c.logf("ci: cache: cleared %d cached results on epoch change (%s -> %s)", removed, prev, epoch)
	}
	return removed, err
}

func hasCacheEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if isCacheEntry(e) {
			return true
		}
	}
	return false
}

func writeEpochFile(path, epoch string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"epoch-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(epoch + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}
