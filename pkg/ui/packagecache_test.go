package ui_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakePackageCache stands in for the daemon's own cache: it remembers
// what it was asked to flush and answers with numbers a pane can show.
type fakePackageCache struct {
	stats   []ui.PackageCacheStats
	entries int
	flushed []string
	err     error
}

func (f *fakePackageCache) PackageCacheStats() ([]ui.PackageCacheStats, error) {
	return f.stats, f.err
}

func (f *fakePackageCache) PackageCacheEntryCount() int { return f.entries }

func (f *fakePackageCache) FlushPackageCache(kind string) (int, int64, error) {
	if f.err != nil {
		return 0, 0, f.err
	}
	f.flushed = append(f.flushed, kind)
	f.stats = []ui.PackageCacheStats{{Kind: "apt"}, {Kind: "pypi"}}
	return 7, 4096, nil
}

// The switch stays a setting; what the cache holds does not (see
// grain/task-266 and the doc comment on SandboxHealthResponse).
func TestSettingsReportWhetherThePackageCacheIsOn(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.PackageCache = &fakePackageCache{stats: []ui.PackageCacheStats{
		{Kind: "apt", Entries: 3, Bytes: 4096},
	}}

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	// A deployment that has stored nothing yet still caches: this is the
	// state an upgraded deployment is in before anybody opens Settings.
	if !settings.PackageCacheEnabled {
		t.Error("a deployment that has never chosen should have the package cache on")
	}
}

// The host pane draws the disk bars the cache is filling, so its own poll
// is what carries the cache's figures.
func TestSandboxHealthReportsWhatThePackageCacheHolds(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	client.Config.PackageCache = &fakePackageCache{stats: []ui.PackageCacheStats{
		{Kind: "apt", Entries: 3, Bytes: 4096},
		{Kind: "pypi", Entries: 1, Bytes: 2048},
	}}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if len(got.PackageCache) != 2 || got.PackageCache[0].Entries != 3 {
		t.Errorf("package cache stats = %+v", got.PackageCache)
	}
}

// The sidebar's Proxy cache entry carries a count like every other
// entry in the rail, and takes it from the config poll every view
// already makes rather than from the cache page's own walk
// (grain/task-333).
func TestConfigCarriesThePackageCacheEntryCount(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.PackageCache = &fakePackageCache{entries: 2431}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["packageCacheEntries"] != float64(2431) {
		t.Fatalf("packageCacheEntries = %v, want 2431", got["packageCacheEntries"])
	}
}

// An empty cache is a reading and not an absence: the rail should say
// 0, which is why this count is nullable rather than omitempty on an
// int.
func TestConfigReportsAnEmptyPackageCacheAsZero(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.PackageCache = &fakePackageCache{}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["packageCacheEntries"] != float64(0) {
		t.Fatalf("packageCacheEntries = %v, want 0", got["packageCacheEntries"])
	}
}

// A deployment running no cache has no number to show, and must not be
// given somebody else's zero.
func TestConfigOmitsThePackageCacheCountWithNoCache(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if _, ok := got["packageCacheEntries"]; ok {
		t.Fatalf("packageCacheEntries = %v, want it absent on a deployment with no cache", got["packageCacheEntries"])
	}
}

// A cache directory that cannot be read must cost the pane its numbers
// and nothing else: the disk bars and the sandbox list are the reading an
// operator came for, and they do not depend on it.
func TestSandboxHealthSurvivesACacheThatCannotBeRead(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.Sandboxes = fakeSandboxHealth{snapshots: []ui.SandboxSnapshot{{Sandbox: "task-1"}}}
	client.Config.PackageCache = &fakePackageCache{err: errors.New("permission denied")}

	rec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a host pane must load even when the cache cannot be read: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.SandboxHealthResponse](t, rec)
	if got.PackageCache != nil {
		t.Errorf("package cache stats = %+v, want none", got.PackageCache)
	}
	if len(got.Sandboxes) != 1 {
		t.Errorf("sandboxes = %+v, want the list to survive", got.Sandboxes)
	}
}

func TestFlushPackageCache(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	cache := &fakePackageCache{}
	c.Config.PackageCache = cache

	result, err := c.FlushPackageCache(ctx, "apt")
	if err != nil {
		t.Fatalf("FlushPackageCache: %v", err)
	}
	if result.Entries != 7 || result.Bytes != 4096 || result.Kind != "apt" {
		t.Errorf("flush result = %+v", result)
	}
	if len(cache.flushed) != 1 || cache.flushed[0] != "apt" {
		t.Errorf("flushed = %v, want one apt flush", cache.flushed)
	}
	if len(result.Cache) != 2 {
		t.Errorf("the answer should carry the cache as it stands afterwards, got %+v", result.Cache)
	}

	if _, err := c.FlushPackageCache(ctx, ""); err != nil {
		t.Fatalf("flushing everything: %v", err)
	}
	if len(cache.flushed) != 2 || cache.flushed[1] != "" {
		t.Errorf("flushed = %v, want an everything flush second", cache.flushed)
	}
}

// The Proxy cache page's own poll (grain/task-321). Both answers are a
// reading: a deployment running no cache is not a fault, so it is a 200
// saying so rather than the 501 this route used to give a page nothing
// to draw but an error banner.
func TestGetPackageCacheReportsWhatItHolds(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.PackageCache = &fakePackageCache{stats: []ui.PackageCacheStats{
		{Kind: "apt", Entries: 3, Bytes: 4096},
		{Kind: "npm", Entries: 1, Bytes: 512},
	}}

	rec := do(t, srv, http.MethodGet, "/api/package-cache", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	state := decode[ui.PackageCacheState](t, rec)
	if !state.Enabled {
		t.Error("a deployment running a cache should report it enabled")
	}
	if len(state.Cache) != 2 || state.Cache[0].Entries != 3 {
		t.Errorf("cache = %+v", state.Cache)
	}
}

func TestGetPackageCacheWithoutOne(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/package-cache", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	state := decode[ui.PackageCacheState](t, rec)
	if state.Enabled || state.Cache != nil {
		t.Errorf("state = %+v, want a deployment with no cache saying so", state)
	}
}

func TestFlushPackageCacheWithoutOne(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	if _, err := c.FlushPackageCache(context.Background(), ""); err == nil {
		t.Fatal("a deployment with no package cache should say so rather than report a flush")
	}
}
