package ci

import (
	"context"
	"strings"
	"testing"
)

func TestBaseTransitionCachePurgeAndInvalidation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	dirCache, err := OpenDirCache(dir, DirCacheOptions{})
	if err != nil {
		t.Fatalf("OpenDirCache: %v", err)
	}

	keyPass := Key("job:" + strings.Repeat("1", 64))
	keyFail := Key("job:" + strings.Repeat("2", 64))

	passRun := JobRun{
		JobID:      "build",
		Conclusion: ConclusionSuccess,
		BaseCommit: "base-commit-1",
		Steps:      []StepRun{{Name: "test", Conclusion: ConclusionSuccess}},
	}
	failRun := JobRun{
		JobID:      "test",
		Conclusion: ConclusionFailure,
		BaseCommit: "base-commit-1",
		Steps:      []StepRun{{Name: "test", Conclusion: ConclusionFailure}},
	}

	// Save both results
	if err := dirCache.Save(ctx, keyPass, passRun); err != nil {
		t.Fatalf("Save pass: %v", err)
	}
	if err := dirCache.Save(ctx, keyFail, failRun); err != nil {
		t.Fatalf("Save fail: %v", err)
	}

	// Verify both exist in cache
	if hit, ok, err := dirCache.Lookup(ctx, keyPass); err != nil || !ok || hit.Conclusion != ConclusionSuccess {
		t.Fatalf("lookup keyPass: got %v, %v, %v", hit, ok, err)
	}
	if hit, ok, err := dirCache.Lookup(ctx, keyFail); err != nil || !ok || hit.Conclusion != ConclusionFailure {
		t.Fatalf("lookup keyFail: got %v, %v, %v", hit, ok, err)
	}

	// Purge negative entries tied to base-commit-1
	purged, err := EvictBaseTransitionFailures(ctx, dirCache, "base-commit-1")
	if err != nil {
		t.Fatalf("EvictBaseTransitionFailures: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want 1", purged)
	}

	// The failing entry should now be gone (miss)
	if _, ok, err := dirCache.Lookup(ctx, keyFail); err != nil || ok {
		t.Errorf("keyFail should have been purged, got ok=%v, err=%v", ok, err)
	}

	// The passing entry MUST still be in the cache!
	if hit, ok, err := dirCache.Lookup(ctx, keyPass); err != nil || !ok || hit.Conclusion != ConclusionSuccess {
		t.Errorf("keyPass should still be cached, got %v, ok=%v, err=%v", hit, ok, err)
	}
}

func TestEngineInvalidatesNegativeVerdictOnBaseTransition(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()

	// Plan with current base commit = base-2
	plan := planFor(t, Request{Repo: "o/r", Ref: "feature", Commit: "abc", BaseCommit: "base-2", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	// Cached failure was recorded under base-1
	cache.entries[plan.Jobs[0].Key] = JobRun{
		JobID:      "one",
		Conclusion: ConclusionFailure,
		BaseCommit: "base-1",
		Steps:      []StepRun{{Name: "true", Conclusion: ConclusionFailure}},
	}

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q, want %q", run.Conclusion, ConclusionSuccess)
	}
	job := jobByID(t, run, "one")
	if job.Cached {
		t.Error("a cached failure from an older base commit should be invalidated, not served as cached")
	}
	if sandboxes.sandboxes != 1 {
		t.Errorf("sandbox should have been built to re-run invalidated failure: built %d", sandboxes.sandboxes)
	}
}

// A base transition evicts the failures recorded against the base it
// names and nothing else: a failure against another base (another
// branch's, or one the base passed through unjudged) is the engine's to
// discard at lookup, and one recorded with no base at all is not a
// base's to evict. Only the empty base purges every failure.
func TestPurgeNegativeEntriesHonoursItsBase(t *testing.T) {
	ctx := context.Background()
	dirCache, err := OpenDirCache(t.TempDir(), DirCacheOptions{})
	if err != nil {
		t.Fatalf("OpenDirCache: %v", err)
	}
	keys := map[string]Key{}
	for i, base := range []string{"base-1", "base-2", ""} {
		key := Key("job:" + strings.Repeat(string(rune('a'+i)), 64))
		keys[base] = key
		if err := dirCache.Save(ctx, key, JobRun{
			JobID: "test", Conclusion: ConclusionFailure, BaseCommit: base,
			Steps: []StepRun{{Name: "test", Conclusion: ConclusionFailure}},
		}); err != nil {
			t.Fatalf("Save %q: %v", base, err)
		}
	}
	cached := func(base string) bool {
		t.Helper()
		_, ok, err := dirCache.Lookup(ctx, keys[base])
		if err != nil {
			t.Fatalf("Lookup %q: %v", base, err)
		}
		return ok
	}

	if n, err := dirCache.PurgeNegativeEntries(ctx, "base-1"); err != nil || n != 1 {
		t.Fatalf("PurgeNegativeEntries(base-1) = %d, %v; want 1, nil", n, err)
	}
	if cached("base-1") {
		t.Error("the failure against base-1 survived its own base's eviction")
	}
	if !cached("base-2") || !cached("") {
		t.Errorf("evicting base-1 took failures against other bases with it: base-2 cached %v, no base cached %v",
			cached("base-2"), cached(""))
	}

	if n, err := dirCache.PurgeNegativeEntries(ctx, ""); err != nil || n != 2 {
		t.Fatalf("PurgeNegativeEntries(\"\") = %d, %v; want 2, nil", n, err)
	}
	if cached("base-2") || cached("") {
		t.Error("purging with no base left a failure behind")
	}
}
