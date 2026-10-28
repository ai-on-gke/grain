package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// aResult is a job that ran and succeeded -- the only kind of thing a
// cache is ever asked to keep, plus enough output to have a size.
func aResult(name, output string) workflows.JobRun {
	return workflows.JobRun{
		Workflow:     "build",
		WorkflowPath: ".grain/workflows/a.yml",
		JobID:        name,
		Name:         name,
		Conclusion:   workflows.ConclusionSuccess,
		StartedAt:    timePtr(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)),
		FinishedAt:   timePtr(time.Date(2026, 9, 1, 12, 1, 0, 0, time.UTC)),
		Steps:        []workflows.StepRun{{Name: "make test", Conclusion: workflows.ConclusionSuccess, Output: output}},
	}
}

func openCache(t *testing.T, opts DirCacheOptions) *DirCache {
	t.Helper()
	if opts.Log == nil {
		opts.Log = func(format string, args ...any) { t.Logf(format, args...) }
	}
	cache, err := OpenDirCache(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("opening a cache: %v", err)
	}
	return cache
}

func TestDirCacheAnswersWithWhatItWasTold(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	key := workflows.Key("job:" + strings.Repeat("a", 64))

	if _, ok, err := cache.Lookup(ctx, key); ok || err != nil {
		t.Fatalf("an empty cache: ok=%v err=%v", ok, err)
	}
	want := aResult("test", "PASS")
	if err := cache.Save(ctx, key, want); err != nil {
		t.Fatalf("saving: %v", err)
	}
	got, ok, err := cache.Lookup(ctx, key)
	if err != nil || !ok {
		t.Fatalf("looking up what was just saved: ok=%v err=%v", ok, err)
	}
	if got.Conclusion != want.Conclusion || len(got.Steps) != 1 || got.Steps[0].Output != "PASS" {
		t.Errorf("what came back is not what went in: %+v", got)
	}
	if got.Key != key {
		t.Errorf("an entry is served under the key it is filed under: got %q", got.Key)
	}
	if got.Cached {
		t.Error("an entry records the run that produced it; the engine is what marks a hit as cached")
	}
	// A second cache over the same directory is the same cache: this
	// survives the daemon it was written by, which is the whole point.
	again, err := OpenDirCache(cache.Dir(), DirCacheOptions{})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if _, ok, err := again.Lookup(ctx, key); !ok || err != nil {
		t.Errorf("an entry did not survive a restart: ok=%v err=%v", ok, err)
	}
}

func TestDirCacheKeepsOnlySelfContainedConclusions(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	for name, result := range map[string]workflows.JobRun{
		"skipped":   {Conclusion: workflows.ConclusionSkipped},
		"cancelled": {Conclusion: workflows.ConclusionCancelled},
		"pending":   {Conclusion: workflows.ConclusionPending},
		"no sandbox": {Conclusion: workflows.ConclusionFailure,
			Error: "building a sandbox for this job: the backend is out of capacity"},
	} {
		t.Run(name, func(t *testing.T) {
			key := workflows.Key("job:" + strings.Repeat("b", 64))
			if err := cache.Save(ctx, key, result); err == nil {
				t.Errorf("a %s result was accepted: it is a fact about the run, not about the job", name)
			}
			if _, ok, _ := cache.Lookup(ctx, key); ok {
				t.Errorf("a %s result was stored anyway", name)
			}
		})
	}
}

func TestDirCacheForgetsAnEntryPastItsAge(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{MaxAge: time.Hour, Now: func() time.Time { return now }})
	ctx := context.Background()
	key := workflows.Key("job:" + strings.Repeat("c", 64))
	if err := cache.Save(ctx, key, aResult("test", "PASS")); err != nil {
		t.Fatalf("saving: %v", err)
	}

	now = now.Add(59 * time.Minute)
	if _, ok, err := cache.Lookup(ctx, key); !ok || err != nil {
		t.Fatalf("an entry inside its age: ok=%v err=%v", ok, err)
	}
	now = now.Add(2 * time.Minute)
	if _, ok, err := cache.Lookup(ctx, key); ok || err != nil {
		t.Fatalf("an entry past its age: ok=%v err=%v", ok, err)
	}
	// And it is gone, rather than merely refused every time it is asked
	// for: nothing else would ever come along to remove it.
	if entries, err := os.ReadDir(cache.Dir()); err != nil || len(entries) != 0 {
		t.Errorf("the expired entry is still on disk: %v, %v", entries, err)
	}
}

func TestDirCacheStaysInsideItsBounds(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{MaxEntries: 3, Now: func() time.Time { return now }})
	ctx := context.Background()

	keys := make([]workflows.Key, 0, 5)
	for i := range 5 {
		key := workflows.Key(fmt.Sprintf("job:%s%02d", strings.Repeat("d", 62), i))
		keys = append(keys, key)
		if err := cache.Save(ctx, key, aResult("test", "PASS")); err != nil {
			t.Fatalf("saving: %v", err)
		}
		// Each one a minute after the last, so "oldest" means something
		// on a filesystem whose timestamps this test cannot fake.
		now = now.Add(time.Minute)
		path := filepath.Join(cache.Dir(), strings.ReplaceAll(string(key), ":", "-")+".json")
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatalf("ageing %s: %v", path, err)
		}
		// The sweep is on a clock of its own; this is a test of what it
		// does, not of when it decides to.
		cache.Sweep()
	}

	entries, err := os.ReadDir(cache.Dir())
	if err != nil {
		t.Fatalf("reading the cache: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("a cache bounded at 3 entries holds %d", len(entries))
	}
	for i, key := range keys {
		_, ok, err := cache.Lookup(ctx, key)
		if err != nil {
			t.Fatalf("looking up %s: %v", key, err)
		}
		if want := i >= 2; ok != want {
			t.Errorf("entry %d: held=%v, want %v -- the oldest go first", i, ok, want)
		}
	}
}

func TestDirCacheDeclinesAnEntryTooLargeToBeWorthIt(t *testing.T) {
	var logged []string
	cache := openCache(t, DirCacheOptions{
		MaxEntryBytes: 1 << 10,
		Log:           func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	})
	ctx := context.Background()
	key := workflows.Key("job:" + strings.Repeat("e", 64))

	if err := cache.Save(ctx, key, aResult("test", strings.Repeat("x", 4<<10))); err != nil {
		t.Fatalf("an oversized result is declined, not an error: %v", err)
	}
	if _, ok, _ := cache.Lookup(ctx, key); ok {
		t.Error("an oversized result was stored")
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "not caching it") {
		t.Errorf("nothing said why it was not cached: %v", logged)
	}
}

func TestDirCacheTreatsAnUnreadableEntryAsAMiss(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	key := workflows.Key("job:" + strings.Repeat("f", 64))
	path := filepath.Join(cache.Dir(), strings.ReplaceAll(string(key), ":", "-")+".json")

	for name, body := range map[string]string{
		"not json":          "{{{",
		"another version":   `{"version":99,"key":"` + string(key) + `","result":{"conclusion":"success"}}`,
		"another key":       `{"version":1,"key":"job:somebodyelse","result":{"conclusion":"success"}}`,
		"a skipped result":  `{"version":1,"key":"` + string(key) + `","result":{"conclusion":"skipped"}}`,
		"an empty envelope": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("writing: %v", err)
			}
			defer os.Remove(path)
			result, ok, err := cache.Lookup(ctx, key)
			if ok || err != nil {
				t.Errorf("a cache must never fail a build: ok=%v err=%v result=%+v", ok, err, result)
			}
		})
	}
}

// A cache that is broken as a whole says so, and the engine's answer to
// that is to run the job (TestEngineRunsTheJobWhenTheCacheIsBroken).
func TestDirCacheReportsAnEntryItCannotRead(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	key := workflows.Key("job:" + strings.Repeat("0", 64))
	path := filepath.Join(cache.Dir(), strings.ReplaceAll(string(key), ":", "-")+".json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("putting a directory where an entry goes: %v", err)
	}
	if _, ok, err := cache.Lookup(ctx, key); ok || err == nil {
		t.Errorf("an entry that cannot be read: ok=%v err=%v", ok, err)
	}
}

func TestDirCacheRefusesAKeyItCannotNameAFileAfter(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	for _, key := range []workflows.Key{"", "../../etc/passwd", "job:../escape", "JOB:UPPER", "job-with-a-hyphen"} {
		if _, ok, err := cache.Lookup(ctx, key); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v", key, ok, err)
		}
		if err := cache.Save(ctx, key, aResult("test", "PASS")); err == nil {
			t.Errorf("%q was saved", key)
		}
	}
	if entries, _ := os.ReadDir(cache.Dir()); len(entries) != 0 {
		t.Errorf("something was written anyway: %v", entries)
	}
}

func TestDirCacheSweepsWhatAnInterruptedSaveLeftBehind(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{Now: func() time.Time { return now }})
	stale := filepath.Join(cache.Dir(), tempPrefix+"halfwritten")
	if err := os.WriteFile(stale, []byte("{"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	// The sweep reads mtimes off the filesystem and ages them against
	// this cache's clock, so the file has to be stamped on that clock
	// rather than on the wall's.
	if err := os.Chtimes(stale, now, now); err != nil {
		t.Fatalf("stamping %s: %v", stale, err)
	}

	cache.Sweep()
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("a temporary file a live Save might own was removed: %v", err)
	}
	now = now.Add(2 * tempEntryTTL)
	cache.Sweep()
	if _, err := os.Stat(stale); err == nil {
		t.Error("a temporary file from an interrupted save was left behind")
	}
}

// The cache is one directory, and it is not the only thing that might
// ever be in one: a wiring mistake that pointed it at a directory with
// something else in it must not be destructive.
func TestDirCacheLeavesFilesThatAreNotItsAlone(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{MaxAge: time.Minute, MaxEntries: 1, Now: func() time.Time { return now }})
	other := filepath.Join(cache.Dir(), "somebody-elses.txt")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	now = now.Add(time.Hour)
	cache.Sweep()
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file this cache did not write was removed: %v", err)
	}
}

// The engine and the cache have to agree about the shape on disk, and
// the entry is JSON so that an operator can read one.
func TestDirCacheEntryIsReadableJSON(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	key := workflows.Key("job:" + strings.Repeat("9", 64))
	if err := cache.Save(context.Background(), key, aResult("test", "PASS")); err != nil {
		t.Fatalf("saving: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(cache.Dir(), strings.ReplaceAll(string(key), ":", "-")+".json"))
	if err != nil {
		t.Fatalf("reading the entry: %v", err)
	}
	var entry struct {
		Version int    `json:"version"`
		Key     string `json:"key"`
		SavedAt string `json:"savedAt"`
		Result  struct {
			Conclusion string `json:"conclusion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("an entry is JSON: %v", err)
	}
	if entry.Version != cacheEntryVersion || entry.Key != string(key) ||
		entry.SavedAt == "" || entry.Result.Conclusion != string(workflows.ConclusionSuccess) {
		t.Errorf("entry: %+v", entry)
	}
}

func TestDirCacheStatsCountWhatIsHeld(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{Now: func() time.Time { return now }})
	ctx := context.Background()

	empty, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats of an empty cache: %v", err)
	}
	if empty.Entries != 0 || empty.Bytes != 0 || !empty.Oldest.IsZero() || !empty.Newest.IsZero() {
		t.Errorf("an empty cache holds nothing: %+v", empty)
	}
	// The bounds are reported whether or not anything is held: "256MB of
	// cache" means nothing without them.
	if empty.MaxAge != workflows.DefaultCacheMaxAge || empty.MaxBytes != workflows.DefaultCacheMaxBytes ||
		empty.MaxEntries != workflows.DefaultCacheMaxEntries || empty.MaxEntryBytes != workflows.DefaultCacheMaxEntryBytes {
		t.Errorf("stats do not carry the bounds: %+v", empty)
	}
	if empty.Dir != cache.Dir() {
		t.Errorf("stats name the directory an operator would go and look in: %q", empty.Dir)
	}

	for i, key := range twoKeys() {
		if err := cache.Save(ctx, key, aResult(fmt.Sprintf("job%d", i), strings.Repeat("x", 1000))); err != nil {
			t.Fatalf("saving: %v", err)
		}
	}
	held, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if held.Entries != 2 {
		t.Errorf("entries: got %d, want 2", held.Entries)
	}
	if held.Bytes < 2000 {
		t.Errorf("two entries of a thousand bytes of output each: %d bytes", held.Bytes)
	}
	if held.Oldest.IsZero() || held.Newest.IsZero() || held.Newest.Before(held.Oldest) {
		t.Errorf("the age span of what is held: %+v", held)
	}
}

func TestDirCacheStatsCountHitsAndMisses(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	key := twoKeys()[0]

	if _, ok, _ := cache.Lookup(ctx, key); ok {
		t.Fatal("an empty cache answered yes")
	}
	if err := cache.Save(ctx, key, aResult("test", "PASS")); err != nil {
		t.Fatalf("saving: %v", err)
	}
	cache.Lookup(ctx, key)
	cache.Lookup(ctx, key)
	cache.Lookup(ctx, twoKeys()[1])

	stats, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Hits != 2 || stats.Misses != 2 {
		t.Errorf("hits/misses: got %d/%d, want 2/2", stats.Hits, stats.Misses)
	}
}

// An entry too old to serve is still on the disk this is reporting, so
// it is still counted -- Lookup refuses it and the next sweep takes it.
func TestDirCacheStatsCountEntriesTooOldToServe(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	cache := openCache(t, DirCacheOptions{MaxAge: time.Hour, Now: func() time.Time { return now }})
	ctx := context.Background()
	key := twoKeys()[0]
	if err := cache.Save(ctx, key, aResult("test", "PASS")); err != nil {
		t.Fatalf("saving: %v", err)
	}
	now = now.Add(2 * time.Hour)

	stats, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries != 1 {
		t.Errorf("an entry past MaxAge still occupies the disk: entries=%d", stats.Entries)
	}
}

func TestDirCacheClearEmptiesItAndLeavesEverythingElse(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	ctx := context.Background()
	keys := twoKeys()
	for i, key := range keys {
		if err := cache.Save(ctx, key, aResult(fmt.Sprintf("job%d", i), "PASS")); err != nil {
			t.Fatalf("saving: %v", err)
		}
	}
	// Not ours, and not this cache's to delete -- the same rule the
	// sweep follows, so that a mistyped directory in the wiring is not
	// destructive.
	other := filepath.Join(cache.Dir(), "notes.txt")
	if err := os.WriteFile(other, []byte("mine"), 0o600); err != nil {
		t.Fatalf("writing another file: %v", err)
	}

	removed, err := cache.Clear(ctx)
	if err != nil || removed != 2 {
		t.Fatalf("clearing: removed=%d err=%v", removed, err)
	}
	for _, key := range keys {
		if _, ok, err := cache.Lookup(ctx, key); ok || err != nil {
			t.Errorf("a cleared entry was served: ok=%v err=%v", ok, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file this cache did not write was removed: %v", err)
	}
	stats, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries != 0 || stats.Bytes != 0 {
		t.Errorf("a cleared cache still holds something: %+v", stats)
	}

	// Clearing an empty cache is not an error: an operator who clicks it
	// twice has done nothing wrong.
	if removed, err := cache.Clear(ctx); removed != 0 || err != nil {
		t.Errorf("clearing an empty cache: removed=%d err=%v", removed, err)
	}
	// And the cache still works afterwards, which is the point of it
	// being safe to clear at all.
	if err := cache.Save(ctx, keys[0], aResult("job0", "PASS")); err != nil {
		t.Fatalf("saving after a clear: %v", err)
	}
	if _, ok, err := cache.Lookup(ctx, keys[0]); !ok || err != nil {
		t.Errorf("a cache cleared and used again: ok=%v err=%v", ok, err)
	}
}

// DirCache is what pkg/ui and `grain ci cache` ask for, so the interface
// they ask through has to be one it satisfies.
func TestDirCacheIsInspectable(t *testing.T) {
	var _ workflows.InspectableCache = (*DirCache)(nil)
}

func TestDirCacheEpochClearsOnUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keys := twoKeys()

	// Initial run under epoch 1.
	c1, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v1|guest:img1"})
	if err != nil {
		t.Fatalf("opening c1: %v", err)
	}
	successRes := aResult("build", "PASS")
	failureRes := aResult("test", "FAIL")
	failureRes.Conclusion = workflows.ConclusionFailure

	if err := c1.Save(ctx, keys[0], successRes); err != nil {
		t.Fatalf("saving success: %v", err)
	}
	if err := c1.Save(ctx, keys[1], failureRes); err != nil {
		t.Fatalf("saving failure: %v", err)
	}

	if _, ok, err := c1.Lookup(ctx, keys[0]); !ok || err != nil {
		t.Fatalf("lookup key 0 in c1: ok=%v err=%v", ok, err)
	}
	if _, ok, err := c1.Lookup(ctx, keys[1]); !ok || err != nil {
		t.Fatalf("lookup key 1 in c1: ok=%v err=%v", ok, err)
	}

	// Reopening with the same epoch must preserve cached entries.
	cSame, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v1|guest:img1"})
	if err != nil {
		t.Fatalf("opening cSame: %v", err)
	}
	if _, ok, err := cSame.Lookup(ctx, keys[0]); !ok || err != nil {
		t.Errorf("lookup key 0 in cSame: ok=%v err=%v", ok, err)
	}
	if _, ok, err := cSame.Lookup(ctx, keys[1]); !ok || err != nil {
		t.Errorf("lookup key 1 in cSame: ok=%v err=%v", ok, err)
	}

	// Reopening with an upgraded daemon revision must clear cached entries.
	cUpgradedDaemon, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v2|guest:img1"})
	if err != nil {
		t.Fatalf("opening cUpgradedDaemon: %v", err)
	}
	if _, ok, err := cUpgradedDaemon.Lookup(ctx, keys[0]); ok || err != nil {
		t.Errorf("lookup key 0 in cUpgradedDaemon served stale entry: ok=%v err=%v", ok, err)
	}
	if _, ok, err := cUpgradedDaemon.Lookup(ctx, keys[1]); ok || err != nil {
		t.Errorf("lookup key 1 in cUpgradedDaemon served stale entry: ok=%v err=%v", ok, err)
	}

	// Save new entry under upgraded daemon.
	if err := cUpgradedDaemon.Save(ctx, keys[0], successRes); err != nil {
		t.Fatalf("saving success in cUpgradedDaemon: %v", err)
	}

	// Reopening with an upgraded guest image must clear cached entries.
	cUpgradedGuest, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v2|guest:img2"})
	if err != nil {
		t.Fatalf("opening cUpgradedGuest: %v", err)
	}
	if _, ok, err := cUpgradedGuest.Lookup(ctx, keys[0]); ok || err != nil {
		t.Errorf("lookup key 0 in cUpgradedGuest served stale entry: ok=%v err=%v", ok, err)
	}
}

func TestDirCacheSetEpochClearsDynamically(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keys := twoKeys()

	cache, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v1|guest:img1"})
	if err != nil {
		t.Fatalf("opening cache: %v", err)
	}
	if err := cache.Save(ctx, keys[0], aResult("job", "PASS")); err != nil {
		t.Fatalf("saving: %v", err)
	}

	if got := cache.Epoch(); got != "daemon:v1|guest:img1" {
		t.Errorf("cache.Epoch() = %q, want daemon:v1|guest:img1", got)
	}

	// Same epoch is a no-op.
	removed, err := cache.SetEpoch(ctx, "daemon:v1|guest:img1")
	if err != nil || removed != 0 {
		t.Fatalf("SetEpoch same epoch: removed=%d err=%v", removed, err)
	}
	if _, ok, err := cache.Lookup(ctx, keys[0]); !ok || err != nil {
		t.Fatalf("lookup after same epoch: ok=%v err=%v", ok, err)
	}

	// Changed epoch clears entries.
	removed, err = cache.SetEpoch(ctx, "daemon:v1|guest:img2")
	if err != nil || removed != 1 {
		t.Fatalf("SetEpoch new epoch: removed=%d err=%v", removed, err)
	}
	if _, ok, err := cache.Lookup(ctx, keys[0]); ok || err != nil {
		t.Errorf("lookup after changed epoch served stale entry: ok=%v err=%v", ok, err)
	}
	if got := cache.Epoch(); got != "daemon:v1|guest:img2" {
		t.Errorf("cache.Epoch() = %q, want daemon:v1|guest:img2", got)
	}
}

func TestDirCacheEpochClearsLegacyUnversionedEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keys := twoKeys()

	// Legacy cache opened without epoch.
	cLegacy, err := OpenDirCache(dir, DirCacheOptions{})
	if err != nil {
		t.Fatalf("opening legacy cache: %v", err)
	}
	if err := cLegacy.Save(ctx, keys[0], aResult("legacy", "FAIL")); err != nil {
		t.Fatalf("saving legacy entry: %v", err)
	}
	if _, ok, err := cLegacy.Lookup(ctx, keys[0]); !ok || err != nil {
		t.Fatalf("lookup in legacy cache: ok=%v err=%v", ok, err)
	}

	// Opening with epoch must clear unversioned legacy entries.
	cVersioned, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v1|guest:img1"})
	if err != nil {
		t.Fatalf("opening versioned cache: %v", err)
	}
	if _, ok, err := cVersioned.Lookup(ctx, keys[0]); ok || err != nil {
		t.Errorf("lookup served legacy unversioned entry: ok=%v err=%v", ok, err)
	}
}

func TestDirCacheEpochFileNotCountedInStatsOrSweep(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cache, err := OpenDirCache(dir, DirCacheOptions{Epoch: "daemon:v1|guest:img1"})
	if err != nil {
		t.Fatalf("opening cache: %v", err)
	}
	stats, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries != 0 || stats.Bytes != 0 {
		t.Errorf("stats counted epoch file as entry: %+v", stats)
	}
}

func twoKeys() []workflows.Key {
	return []workflows.Key{
		workflows.Key("job:" + strings.Repeat("1", 64)),
		workflows.Key("job:" + strings.Repeat("2", 64)),
	}
}

func timePtr(t time.Time) *time.Time { return &t }
