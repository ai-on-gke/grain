package ci

// What a stored bound has to do once it is a setting rather than a flag
// (grain/task-317): reach both directories, mean what the columns say,
// and move what they hold themselves to while the process that opened
// them goes on running.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The seven settings, as the sweep reads them: the units applied, and a
// blank column left as the zero that means "grain's own" one layer
// further down.
func TestStorageBoundsFromAppliesTheSettingsUnits(t *testing.T) {
	t.Parallel()
	got := StorageBoundsFrom(model.Config{
		CICacheMB: 64, CICacheEntries: 100, CICacheDays: 3,
		CIArtifactMB: 512, CIArtifactRunMB: 32, CIArtifactFileMB: 8, CIArtifactDays: 5,
	})
	want := StorageBounds{
		Cache: CacheBounds{MaxBytes: 64 << 20, MaxEntries: 100, MaxAge: 72 * time.Hour},
		Artifacts: ArtifactBounds{
			MaxBytes: 512 << 20, MaxRunBytes: 32 << 20, MaxArtifactBytes: 8 << 20,
			MaxAge: 120 * time.Hour,
		},
	}
	if got != want {
		t.Errorf("bounds = %+v, want %+v", got, want)
	}
	// A row that has chosen nothing carries zeroes, which resolve to
	// grain's own numbers rather than to a directory nothing sweeps.
	blank := StorageBoundsFrom(model.Config{})
	if blank != (StorageBounds{}) {
		t.Errorf("an unset row = %+v, want every bound left at zero", blank)
	}
	if got := blank.Resolved(); got != DefaultStorageBounds() {
		t.Errorf("resolved = %+v, want grain's own %+v", got, DefaultStorageBounds())
	}
	// And a value no bound can be -- a hand-edited row, or a settings
	// file merged from somewhere with no validation in front of it --
	// reads as unset rather than as unbounded.
	negative := StorageBoundsFrom(model.Config{CICacheMB: -1, CIArtifactDays: -1}).Resolved()
	if negative.Cache.MaxBytes != DefaultCacheMaxBytes || negative.Artifacts.MaxAge != DefaultArtifactMaxAge {
		t.Errorf("a negative setting = %+v, want grain's own defaults -- CI's disk has no unbounded reading",
			negative)
	}
}

// The seed, and the round trip back: what a first start writes to the
// config row out of the flags it was given, in the columns' whole MiB and
// whole days.
func TestStorageBoundsSeedTheColumnsAndReadBack(t *testing.T) {
	t.Parallel()
	bounds := StorageBounds{
		Cache:     CacheBounds{MaxBytes: 300 << 20, MaxEntries: 50, MaxAge: 48 * time.Hour},
		Artifacts: ArtifactBounds{MaxBytes: 1 << 30, MaxRunBytes: 64 << 20, MaxArtifactBytes: 16 << 20, MaxAge: 96 * time.Hour},
	}
	cfg := StorageBoundsStoredAs(bounds, model.Config{})
	if cfg.CICacheMB != 300 || cfg.CICacheEntries != 50 || cfg.CICacheDays != 2 {
		t.Errorf("seeded cache columns = %d MiB/%d entries/%d days, want 300/50/2",
			cfg.CICacheMB, cfg.CICacheEntries, cfg.CICacheDays)
	}
	if cfg.CIArtifactMB != 1024 || cfg.CIArtifactRunMB != 64 || cfg.CIArtifactFileMB != 16 || cfg.CIArtifactDays != 4 {
		t.Errorf("seeded artifact columns = %d/%d/%d MiB and %d days, want 1024/64/16 and 4",
			cfg.CIArtifactMB, cfg.CIArtifactRunMB, cfg.CIArtifactFileMB, cfg.CIArtifactDays)
	}
	// MaxEntryBytes has no column, so the seed says nothing about it and
	// nothing reads one back: it stays whatever the flag was.
	if back := StorageBoundsFrom(cfg); back.Cache.MaxBytes != 300<<20 || back.Cache.MaxEntryBytes != 0 {
		t.Errorf("round trip = %+v, want the cache ceiling back and no per-entry bound", back.Cache)
	}

	// A bound finer than the column can hold is rounded down rather than
	// up -- it is a ceiling on somebody else's disk -- and never to zero,
	// which would read back as "grain's own default" and undo the only
	// thing the unit file asked for.
	fine := StorageBoundsStoredAs(StorageBounds{
		Cache:     CacheBounds{MaxBytes: 100_000_000, MaxAge: 90 * time.Minute},
		Artifacts: ArtifactBounds{MaxBytes: 500, MaxAge: time.Second},
	}, model.Config{})
	if fine.CICacheMB != 95 || fine.CICacheDays != 1 {
		t.Errorf("a 100 MB, 90 minute cache seeds as %d MiB/%d days, want 95 and 1",
			fine.CICacheMB, fine.CICacheDays)
	}
	if fine.CIArtifactMB != 1 || fine.CIArtifactDays != 1 {
		t.Errorf("a 500 byte, one second store seeds as %d MiB/%d days, want 1 and 1 -- never 0",
			fine.CIArtifactMB, fine.CIArtifactDays)
	}
}

// A per-run ceiling below the per-artifact one is a wiring mistake, and
// honouring it would mean a store that declines the first artifact it is
// ever offered. Corrected wherever the bounds are resolved, which is now
// on every read rather than once at open.
func TestArtifactBoundsRaiseAPerRunCeilingBelowOneArtifact(t *testing.T) {
	t.Parallel()
	got := ArtifactBounds{MaxRunBytes: 1 << 20, MaxArtifactBytes: 8 << 20}.Resolved()
	if got.MaxRunBytes != 8<<20 {
		t.Errorf("per-run ceiling = %d, want it raised to the %d one artifact may be",
			got.MaxRunBytes, got.MaxArtifactBytes)
	}
}

// stamp puts every file in dir, and dir itself, on the clock a store
// under test is running -- DirArtifacts' sweep reads mtimes off the
// filesystem (its own doc comment says why), so a test that fakes the
// clock has to fake these too or it is comparing a fixed instant in this
// file against whatever today happens to be.
func stamp(t *testing.T, dir string, at time.Time) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s to stamp it: %v", dir, err)
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(dir, e.Name()), at, at); err != nil {
			t.Fatalf("stamping %s: %v", e.Name(), err)
		}
	}
	if err := os.Chtimes(dir, at, at); err != nil {
		t.Fatalf("stamping %s: %v", dir, err)
	}
}

// The point of the provider: a ceiling an operator lowers in the Settings
// pane is the one the next sweep holds the directory within, with nothing
// reopened and nothing restarted.
func TestDirCacheAdoptsAMovedCeilingWithoutBeingReopened(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	// What liveConfig hands over: the stored row, re-read per call.
	stored := model.Config{CICacheMB: 64, CICacheEntries: 5, CICacheDays: 30}
	cache, err := OpenDirCache(t.TempDir(), DirCacheOptions{
		Bounds: func() StorageBounds { return StorageBoundsFrom(stored) },
		Now:    func() time.Time { return now },
		Log:    func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("opening a cache: %v", err)
	}
	ctx := context.Background()

	var keys []Key
	for i := range 4 {
		key := Key(fmt.Sprintf("job:%s%02d", strings.Repeat("a", 62), i))
		keys = append(keys, key)
		if err := cache.Save(ctx, key, aResult("test", "PASS")); err != nil {
			t.Fatalf("saving: %v", err)
		}
		now = now.Add(time.Minute)
		path := filepath.Join(cache.Dir(), strings.ReplaceAll(string(key), ":", "-")+".json")
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatalf("ageing %s: %v", path, err)
		}
	}
	cache.Sweep()
	if entries, err := os.ReadDir(cache.Dir()); err != nil || len(entries) != 4 {
		t.Fatalf("a cache bounded at 5 entries holds %d (%v), want all four", len(entries), err)
	}

	// The operator narrows it to two entries and a day. Nothing is
	// reopened; the next sweep is simply a larger one.
	stored.CICacheEntries, stored.CICacheDays = 2, 1
	if stats, err := cache.Stats(ctx); err != nil || stats.MaxEntries != 2 ||
		stats.MaxAge != 24*time.Hour {
		t.Fatalf("stats after the save = %+v (%v), want the 2 entries and 1 day now stored", stats, err)
	}
	cache.Sweep()
	entries, err := os.ReadDir(cache.Dir())
	if err != nil {
		t.Fatalf("reading the cache: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("a cache narrowed to 2 entries holds %d -- the sweep read the old bound", len(entries))
	}
	// The oldest went, which is the same rule a narrower bound has always
	// had, and the newest is still served.
	if _, ok, err := cache.Lookup(ctx, keys[3]); !ok || err != nil {
		t.Errorf("the newest entry after a narrowing sweep: ok=%v err=%v", ok, err)
	}

	// Widening it again is the same operation in the other direction, and
	// what an operator reading "200 MiB of 256 MiB" on the System pane
	// actually came for.
	stored.CICacheMB = 512
	if stats, err := cache.Stats(ctx); err != nil || stats.MaxBytes != 512<<20 {
		t.Errorf("ceiling after it was raised = %+v (%v), want 512 MiB with no restart", stats, err)
	}
}

// The same for the artifacts, including the bound that leaves the
// daemon: the per-artifact cap a job's sandbox is told before it copies
// anything out, which has to be the stored one or a file the store would
// now refuse is still copied across.
func TestDirArtifactsAdoptAMovedCeilingWithoutBeingReopened(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	stored := model.Config{CIArtifactMB: 64, CIArtifactRunMB: 32, CIArtifactFileMB: 8, CIArtifactDays: 14}
	store, err := OpenDirArtifacts(t.TempDir(), DirArtifactsOptions{
		Bounds: func() StorageBounds { return StorageBoundsFrom(stored) },
		Now:    func() time.Time { return now },
		Log:    func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("opening an artifact store: %v", err)
	}
	ctx := context.Background()

	ref := ArtifactRef{Run: "9f8e7d6c", Job: "go", Name: "traces"}
	kept, err := store.Save(ctx, ref, []byte(strings.Repeat("x", 4<<20)))
	if err != nil {
		t.Fatalf("saving a 4 MiB artifact under an 8 MiB cap: %v", err)
	}
	if !kept.Stored {
		t.Fatalf("a 4 MiB artifact under an 8 MiB cap was not kept: %s", kept.Note)
	}
	// The sweep ages what it finds by the file's own mtime, read off the
	// filesystem, against this store's clock -- so the artifact has to be
	// stamped on that clock rather than on the wall's, exactly as
	// TestDirCacheStaysInsideItsBounds stamps a cache entry. Left on the
	// wall clock this test asks whether a fixed instant in this file is
	// more than a day ahead of today, which was true on the day it was
	// written and false ever after.
	stamp(t, filepath.Join(store.Dir(), ref.Run), now)

	// The operator decides 2 MiB is as much as one artifact is worth.
	stored.CIArtifactFileMB = 2
	if got := store.MaxArtifactBytes(); got != 2<<20 {
		t.Errorf("the cap a job's sandbox is told = %d, want the stored 2 MiB", got)
	}
	again := ArtifactRef{Run: "9f8e7d6c", Job: "go", Name: "traces-2"}
	declined, err := store.Save(ctx, again, []byte(strings.Repeat("x", 4<<20)))
	if err != nil {
		t.Fatalf("offering a 4 MiB artifact under a 2 MiB cap: %v", err)
	}
	if declined.Stored {
		t.Error("a 4 MiB artifact was kept under a 2 MiB cap -- the Save read the old bound")
	}
	// Through humanBytes rather than against a spelling of it: what the
	// note has to carry is the bound that refused this artifact, and a
	// test that hard-codes "2.0MB" is one that goes red the day that
	// formatter gains a space.
	if want := humanBytes(2 << 20); !strings.Contains(declined.Note, want) {
		t.Errorf("the note says %q, want it to name the %s bound that refused it", declined.Note, want)
	}

	// And the age bound, which only the sweep applies: the artifact this
	// store does hold outlives a fortnight and not a day.
	now = now.Add(48 * time.Hour)
	stored.CIArtifactDays = 1
	if stats, err := store.Stats(ctx); err != nil || stats.MaxAge != 24*time.Hour {
		t.Fatalf("stats after the save = %+v (%v), want the 1 day now stored", stats, err)
	}
	store.Sweep()
	if _, _, err := store.Open(ctx, ref); err == nil {
		t.Error("an artifact two days old survived a sweep bounded at one day")
	}
}
