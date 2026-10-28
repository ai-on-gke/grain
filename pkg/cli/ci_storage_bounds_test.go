package cli

// ci_storage_bounds_test.go covers what the bounds on CI's two
// directories on the data disk have to do: reach ci.DirCache and
// ci.DirArtifacts, come from the stored settings rather than from the
// flags once a deployment has a config row, and -- the part that is the
// point of storing them at all (grain/task-317) -- change what those two
// stores are holding themselves to *without the daemon being restarted*.
//
// A setting that needed a restart to take effect would be most of the
// cost of moving these off the flags and none of the benefit: the
// System pane reports what each of these is holding against its ceiling,
// and an operator who moves the ceiling from that pane is asking for it
// to move now, not at the next deploy.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// cacheStatsFor opens the cache ciJobCache would build for cfg and reads
// the bounds back off it -- the resolved ones, which is the only honest
// answer to "what is this cache sized at".
func cacheStatsFor(t *testing.T, cfg daemon.Config, bounds func() ci.StorageBounds) ci.CacheStats {
	t.Helper()
	cache := daemon.CiJobCache(cfg, bounds)
	if cache == nil {
		t.Fatal("ciJobCache = nil, want a cache under a writable data dir")
	}
	inspectable, ok := cache.(ci.InspectableCache)
	if !ok {
		t.Fatalf("ciJobCache returned a %T, which cannot report its own bounds", cache)
	}
	stats, err := inspectable.Stats(context.Background())
	if err != nil {
		t.Fatalf("reading the cache's bounds back: %v", err)
	}
	return stats
}

// artifactStatsFor is cacheStatsFor for the store next door.
func artifactStatsFor(t *testing.T, cfg daemon.Config, bounds func() ci.StorageBounds) ci.ArtifactStats {
	t.Helper()
	store := daemon.CiArtifactStore(cfg, bounds)
	if store == nil {
		t.Fatal("ciArtifactStore = nil, want a store under a writable data dir")
	}
	inspectable, ok := store.(ci.InspectableArtifacts)
	if !ok {
		t.Fatalf("ciArtifactStore returned a %T, which cannot report its own bounds", store)
	}
	stats, err := inspectable.Stats(context.Background())
	if err != nil {
		t.Fatalf("reading the store's bounds back: %v", err)
	}
	return stats
}

// The stored settings, in the columns' own MiB and days, are the bounds
// the two stores actually run with -- which is what withModelConfig
// copying them onto the config this process is running is for.
func TestTheStoredCIStorageSettingsAreTheBoundsInForce(t *testing.T) {
	t.Parallel()
	cfg := startable().WithModelConfig(model.Config{
		// Nothing like the defaults, in every dimension, so a bound that
		// silently fell back to its constant fails rather than coincides.
		CICacheMB: 3, CICacheEntries: 7, CICacheDays: 2,
		CIArtifactMB: 9, CIArtifactRunMB: 5, CIArtifactFileMB: 1, CIArtifactDays: 4,
	})
	cfg.DataDir = t.TempDir()
	bounds := cfg.CiStorageBounds

	cache := cacheStatsFor(t, cfg, bounds)
	if cache.MaxBytes != 3<<20 || cache.MaxEntries != 7 || cache.MaxAge != 48*time.Hour {
		t.Errorf("cache bounds = %d bytes/%d entries/%s, want the stored 3 MiB, 7 entries and 2 days",
			cache.MaxBytes, cache.MaxEntries, cache.MaxAge)
	}
	// The one bound in the cache's group with no column behind it: it is
	// still whatever -ci-cache-max-entry-bytes said, which on a
	// deployment that passed nothing is grain's own.
	if cache.MaxEntryBytes != ci.DefaultCacheMaxEntryBytes {
		t.Errorf("cache max entry bytes = %d, want the flag's own default %d -- it has no setting",
			cache.MaxEntryBytes, ci.DefaultCacheMaxEntryBytes)
	}

	artifacts := artifactStatsFor(t, cfg, bounds)
	if artifacts.MaxBytes != 9<<20 || artifacts.MaxRunBytes != 5<<20 ||
		artifacts.MaxArtifactBytes != 1<<20 || artifacts.MaxAge != 96*time.Hour {
		t.Errorf("artifact bounds = %+v, want the stored 9 MiB, 5 MiB per run, 1 MiB per artifact and 4 days",
			artifacts)
	}
}

// And the whole of why they are settings: a ceiling saved in the Settings
// pane is the one the next sweep holds the directory within, with nothing
// restarted in between. The provider is read per call, exactly as the CI
// limit's is (ci_capacity_test.go), so a live copy of the configuration
// is all this takes.
func TestACIStorageBoundMovesUnderALiveDaemon(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.DataDir = t.TempDir()
	// live is what liveConfig.current() hands back on each tick: the
	// configuration in effect, which withModelConfig has just copied the
	// stored row onto.
	live := cfg.WithModelConfig(model.Config{CICacheMB: 256, CIArtifactFileMB: 32})
	bounds := func() ci.StorageBounds { return live.CiStorageBounds() }

	cache := daemon.CiJobCache(cfg, bounds).(ci.InspectableCache)
	artifacts := daemon.CiArtifactStore(cfg, bounds).(ci.InspectableArtifacts)
	if stats, err := cache.Stats(context.Background()); err != nil || stats.MaxBytes != 256<<20 {
		t.Fatalf("cache ceiling at startup = %+v (%v), want 256 MiB", stats, err)
	}

	// An operator raises the cache's ceiling and lowers what one artifact
	// may be -- both directions, since narrowing a bound is the one that
	// has to be safe as well as prompt.
	live = live.WithModelConfig(model.Config{CICacheMB: 1024, CIArtifactFileMB: 4})

	stats, err := cache.Stats(context.Background())
	if err != nil {
		t.Fatalf("reading the cache's bounds back: %v", err)
	}
	if stats.MaxBytes != 1024<<20 {
		t.Errorf("cache ceiling after the save = %d, want the stored 1024 MiB with no restart", stats.MaxBytes)
	}
	artifactStats, err := artifacts.Stats(context.Background())
	if err != nil {
		t.Fatalf("reading the artifact store's bounds back: %v", err)
	}
	if artifactStats.MaxArtifactBytes != 4<<20 {
		t.Errorf("per-artifact cap after the save = %d, want the stored 4 MiB", artifactStats.MaxArtifactBytes)
	}
	// The same number the collector hands the job's sandbox, so a file
	// that would now be refused is not copied out of it either: the cap
	// is not only reported live, it is enforced live.
	if got := artifacts.MaxArtifactBytes(); got != 4<<20 {
		t.Errorf("the cap a job's sandbox is told = %d, want the stored 4 MiB", got)
	}
}

func TestStoresNobodySizedAreSizedTheWayTheyAlwaysWere(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.DataDir = t.TempDir()
	// A deployment whose row says nothing about any of them: every bound
	// is grain's own, exactly as it was when these were flags nobody
	// passed.
	bounds := cfg.WithModelConfig(model.Config{}).CiStorageBounds

	cache := cacheStatsFor(t, cfg, bounds)
	if cache.MaxAge != ci.DefaultCacheMaxAge || cache.MaxBytes != ci.DefaultCacheMaxBytes ||
		cache.MaxEntries != ci.DefaultCacheMaxEntries || cache.MaxEntryBytes != ci.DefaultCacheMaxEntryBytes {
		t.Errorf("an unsized cache is %s/%d bytes/%d entries/%d per entry, want the ci package's own defaults "+
			"%s/%d/%d/%d", cache.MaxAge, cache.MaxBytes, cache.MaxEntries, cache.MaxEntryBytes,
			ci.DefaultCacheMaxAge, ci.DefaultCacheMaxBytes, ci.DefaultCacheMaxEntries, ci.DefaultCacheMaxEntryBytes)
	}
	artifacts := artifactStatsFor(t, cfg, bounds)
	if artifacts.MaxAge != ci.DefaultArtifactMaxAge || artifacts.MaxBytes != ci.DefaultArtifactMaxBytes ||
		artifacts.MaxRunBytes != ci.DefaultArtifactMaxRunBytes ||
		artifacts.MaxArtifactBytes != ci.DefaultArtifactMaxArtifactBytes {
		t.Errorf("an unsized artifact store is %+v, want the ci package's own defaults", artifacts)
	}
}

// The flags are the seed and nothing more once a row exists: what they
// are given at a first start is what the row carries, in the columns'
// own units, so the deployment goes on running exactly as the unit file
// asked while the pane now has something to edit.
func TestTheCIStorageFlagsSeedTheConfigRow(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.CiCacheMaxAge, cfg.CiCacheMaxBytes, cfg.CiCacheMaxEntries = 72*time.Hour, 100<<20, 64
	cfg.CiArtifactMaxAge, cfg.CiArtifactMaxBytes = 48*time.Hour, 3<<30
	cfg.CiArtifactMaxRunBytes, cfg.CiArtifactMaxEntryBytes = 200<<20, 50<<20

	seed := cfg.ToModelConfig()
	if seed.CICacheMB != 100 || seed.CICacheEntries != 64 || seed.CICacheDays != 3 {
		t.Errorf("seeded cache settings = %d MiB/%d entries/%d days, want 100/64/3",
			seed.CICacheMB, seed.CICacheEntries, seed.CICacheDays)
	}
	if seed.CIArtifactMB != 3072 || seed.CIArtifactRunMB != 200 ||
		seed.CIArtifactFileMB != 50 || seed.CIArtifactDays != 2 {
		t.Errorf("seeded artifact settings = %d/%d/%d MiB and %d days, want 3072/200/50 and 2",
			seed.CIArtifactMB, seed.CIArtifactRunMB, seed.CIArtifactFileMB, seed.CIArtifactDays)
	}
	// And the round trip back: a daemon reading that row runs with the
	// bounds the flags asked for, to the whole MiB and the whole day the
	// column can hold.
	back := cfg.WithModelConfig(seed).CiStorageBounds()
	if back.Cache.MaxBytes != 100<<20 || back.Cache.MaxAge != 72*time.Hour || back.Cache.MaxEntries != 64 {
		t.Errorf("cache bounds after the round trip = %+v, want the flags back", back.Cache)
	}
	if back.Artifacts.MaxBytes != 3<<30 || back.Artifacts.MaxRunBytes != 200<<20 ||
		back.Artifacts.MaxArtifactBytes != 50<<20 || back.Artifacts.MaxAge != 48*time.Hour {
		t.Errorf("artifact bounds after the round trip = %+v, want the flags back", back.Artifacts)
	}
}

// The other half of checkConfig's rule for these flags: a negative bound
// is refused (daemon_flag_check_test.go), and a zero one is not, because
// zero is how "leave it at grain's own default" is spelled all the way
// down -- through the config row now, and still to ci.OpenDirCache.
func TestCheckConfigAllowsAnUnsizedCICache(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.Ci, cfg.MaxCIJobs, cfg.MaxWorkers, cfg.MaxMergers = true, 1, 2, model.DefaultMaxMergers
	cfg.CiCacheMaxAge = 0
	cfg.CiCacheMaxBytes, cfg.CiCacheMaxEntries, cfg.CiCacheMaxEntryBytes = 0, 0, 0
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("a daemon that named none of the cache bounds should start on the defaults: %v", err)
	}
}
