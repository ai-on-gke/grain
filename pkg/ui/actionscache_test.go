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

package ui_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeActionsCache stands in for the daemon's own Actions cache: it
// remembers what it was asked to flush and answers with numbers a page
// can show.
type fakeActionsCache struct {
	entries  int
	bytes    int64
	maxBytes int64
	repos    []ui.ActionsCacheRepoStats
	flushed  []string
}

func (f *fakeActionsCache) ActionsCacheStats() (int, int64, int64, []ui.ActionsCacheRepoStats) {
	return f.entries, f.bytes, f.maxBytes, f.repos
}

func (f *fakeActionsCache) ActionsCacheEntryCount() int { return f.entries }

func (f *fakeActionsCache) FlushActionsCache(repo string) (int, int64) {
	f.flushed = append(f.flushed, repo)
	freed := f.bytes
	if repo != "" {
		freed = 1 << 20
	}
	f.entries, f.bytes, f.repos = 0, 0, nil
	return 4, freed
}

// The page's own poll: what is stored, the bound it is held under, and
// who is filling it.
func TestGetActionsCacheReportsWhatItHolds(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.ActionsCache = &fakeActionsCache{
		entries: 12, bytes: 3 << 30, maxBytes: 20 << 30,
		repos: []ui.ActionsCacheRepoStats{
			{Repo: "acme/widgets", Bytes: 2 << 30},
			{Repo: "acme/gadgets", Bytes: 1 << 30},
		},
	}

	rec := do(t, srv, http.MethodGet, "/api/actions-cache", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	state := decode[ui.ActionsCacheState](t, rec)
	if !state.Enabled {
		t.Error("a deployment keeping a cache should report it enabled")
	}
	if state.Entries != 12 || state.Bytes != 3<<30 || state.MaxBytes != 20<<30 {
		t.Errorf("state = %+v", state)
	}
	if len(state.Repos) != 2 || state.Repos[0].Repo != "acme/widgets" {
		t.Errorf("repos = %+v, want the breakdown largest first", state.Repos)
	}
	// A deployment that has stored no configuration still hands the
	// cache to its jobs, for the same reason the package cache is on.
	if !state.UsedByJobs {
		t.Error("a deployment that has never chosen should have CI jobs using the cache")
	}
}

// A deployment with no cache at all is a reading, not a fault: the page
// draws "this deployment keeps none" rather than an error banner.
func TestGetActionsCacheWithoutOne(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/actions-cache", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	state := decode[ui.ActionsCacheState](t, rec)
	if state.Enabled || state.Entries != 0 {
		t.Errorf("state = %+v, want a deployment with no cache saying so", state)
	}
}

// The state a full cache nobody is reading looks like: the switch is
// off, the entries are still there, and the page has to be able to say
// both -- that is the moment somebody wants the flush button.
func TestGetActionsCacheSaysWhenNoJobIsUsingIt(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.ActionsCache = &fakeActionsCache{entries: 5, bytes: 1 << 30}
	cfg := model.DefaultConfig()
	cfg.ActionsCacheEnabled = false
	if err := client.Store.PutConfig(t.Context(), cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	rec := do(t, srv, http.MethodGet, "/api/actions-cache", "")
	state := decode[ui.ActionsCacheState](t, rec)
	if !state.Enabled || state.Entries != 5 {
		t.Errorf("state = %+v, want the stored entries still reported", state)
	}
	if state.UsedByJobs {
		t.Error("with the switch off, no CI job is handed the cache and the page should say so")
	}
}

func TestFlushActionsCache(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	_ = store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval})
	cache := &fakeActionsCache{entries: 9, bytes: 2 << 30}
	c.Config.ActionsCache = cache

	result, err := c.FlushActionsCache(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("FlushActionsCache: %v", err)
	}
	if result.Entries != 4 || result.Bytes != 1<<20 || result.Repo != "acme/widgets" {
		t.Errorf("flush result = %+v", result)
	}
	if len(cache.flushed) != 1 || cache.flushed[0] != "acme/widgets" {
		t.Errorf("flushed = %v, want one repository flush", cache.flushed)
	}
	// The answer carries the cache as it stands afterwards, so a page
	// redraws without waiting for its next poll.
	if result.Cache.Entries != 0 || !result.Cache.Enabled {
		t.Errorf("cache afterwards = %+v", result.Cache)
	}

	if _, err := c.FlushActionsCache(ctx, ""); err != nil {
		t.Fatalf("flushing everything: %v", err)
	}
	if len(cache.flushed) != 2 || cache.flushed[1] != "" {
		t.Errorf("flushed = %v, want an everything flush second", cache.flushed)
	}
}

func TestFlushActionsCacheWithoutOne(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	_ = client.Store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	rec := do(t, srv, http.MethodPost, "/api/actions-cache/flush", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body)
	}
}

// The sidebar's Actions cache entry carries a count, taken from the
// config poll every view already makes -- and no count at all on a
// deployment that keeps no cache, since "0" would claim an empty one.
func TestConfigCarriesTheActionsCacheEntryCount(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	client.Config.ActionsCache = &fakeActionsCache{entries: 318}

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if got["actionsCacheEntries"] != float64(318) {
		t.Fatalf("actionsCacheEntries = %v, want 318", got["actionsCacheEntries"])
	}
}

func TestConfigOmitsTheActionsCacheCountWithNoCache(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	got := decode[map[string]any](t, rec)
	if _, ok := got["actionsCacheEntries"]; ok {
		t.Fatalf("actionsCacheEntries = %v, want it absent on a deployment with no cache", got["actionsCacheEntries"])
	}
}

// The switch is a setting like any other, and on for a deployment that
// has stored nothing: an upgrade into the cache gets warm CI jobs
// without anybody having to find it.
func TestSettingsReportWhetherTheActionsCacheIsUsed(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !settings.ActionsCacheEnabled {
		t.Error("a deployment that has never chosen should have the Actions cache on")
	}
	if settings.ActionsCacheSizeGBDefault == 0 {
		t.Error("the pane needs grain's own size to show as a placeholder")
	}

	// A stored row, so that the save below is an edit rather than the
	// first one -- which has its own list of settings it must carry
	// (validateSettings) and nothing to do with this cache.
	stored := model.DefaultConfig()
	stored.PollInterval = 30 * time.Second
	stored.MaxWorkers = 2
	if err := c.Store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	off := false
	size := 50
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		ActionsCacheEnabled: &off, ActionsCacheSizeGB: &size,
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	settings, err = c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.ActionsCacheEnabled || settings.ActionsCacheSizeGB != 50 {
		t.Errorf("settings = %+v, want the switch off and the size stored", settings)
	}
}
