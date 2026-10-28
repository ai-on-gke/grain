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

package daemon

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/actionscache"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// actionsCache is this process's cache: the store, the live token
// registry, and where a sandbox reaches the service in front of both.
type ActionsCache struct {
	cache  *actionscache.Cache
	Scopes *actionscache.Scopes
	Url    string
}

// actionsCacheDir resolves the directory holding the Actions cache:
// an explicit -actions-cache-dir if set, otherwise <sandbox-dir>/cache/actions
// when -sandbox-dir is set, falling back to <data-dir>/cache/actions.
func actionsCacheDir(cfg Config) string {
	if cfg.ActionsCacheDir != "" {
		return cfg.ActionsCacheDir
	}
	if cfg.SandboxDir != "" {
		return filepath.Join(cfg.SandboxDir, "cache", "actions")
	}
	return filepath.Join(cfg.DataDir, "cache", "actions")
}

// startActionsCache opens the Actions cache and binds its listener.
//
// It starts whatever the switch says, which is the package cache's
// arrangement next door and is deliberate: "are CI jobs pointed at this
// cache" (model.Config.ActionsCacheEnabled, asked per job by grant
// below) and "is there a cache on this disk to look at" are different
// questions, and an operator who has just turned the switch off is at
// exactly the moment they want the page that says what is still stored
// and the button that empties it. A deployment that has never turned it
// on keeps an empty directory and an unused listener, which costs
// nothing anybody can measure.
//
// A size of zero or less is grain's own default (actionscache.NewCache),
// not "no cache": since grain/task-357 the way to have no Actions cache
// is the switch -- see "Who may read it" in pkg/actionscache's package
// doc for what the cache does and does not share.
func startActionsCache(cfg Config, fabric Fabric) (*ActionsCache, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	dir := actionsCacheDir(cfg)
	cache, err := actionscache.NewCache(dir, int64(cfg.ActionsCacheGB)<<30)
	if err != nil {
		return nil, noop, err
	}
	scopes := actionscache.NewScopes()
	handler := actionscache.NewServer(actionscache.Config{
		Cache:  cache,
		Scopes: scopes,
		Log:    func(format string, args ...any) { log.Printf("grain daemon: "+format, args...) },
	})

	endpoint, stopServer, err := fabric.Serve("Actions cache", handler)
	if err != nil {
		return nil, noop, err
	}
	return &ActionsCache{cache: cache, Scopes: scopes, Url: endpoint.URL}, stopServer, nil
}

// grant is what a CI job's sandbox is built with: a token minted for
// this one job, registered against the repository, branch and trust it
// is building under (ciActionsCacheScope), the URL that token makes a
// cache URL, and the way to hand it back.
//
// live is the stored switch, asked here rather than at startup so that
// turning the cache off in Settings reaches the next job rather than
// the next restart. A job already running keeps the URL its sandbox was
// built with, for the reason a running sandbox keeps the package cache
// configuration it was handed: the environment is written once, when
// the sandbox is created.
//
// Safe on a nil receiver, which is a deployment with no cache: the
// empty URL is what ciSandboxes reads as "there is nothing to tell this
// job about", and so is the empty URL a switched-off cache answers with
// -- and the one a job with no repository or no ref is answered with,
// since there is no corner of the cache it could be scoped to.
func (a *ActionsCache) Grant(live *LiveConfig, scope actionscache.Scope) (string, func(), error) {
	if a == nil || strings.TrimSpace(scope.Repo) == "" || strings.TrimSpace(scope.Ref) == "" {
		return "", func() {}, nil
	}
	if live != nil && !live.actionsCacheUsable() {
		return "", func() {}, nil
	}
	token, err := actionscache.MintToken()
	if err != nil {
		return "", func() {}, err
	}
	a.Scopes.Grant(token, scope)
	return actionscache.SandboxURL(a.Url, token), func() { a.Scopes.Revoke(token) }, nil
}

// grantFor is the func ciSandboxes holds, written so that a deployment
// with no cache hands the adapter nil rather than a method on nothing.
func (a *ActionsCache) grantFor(live *LiveConfig) func(actionscache.Scope) (string, func(), error) {
	if a == nil {
		return nil
	}
	return func(scope actionscache.Scope) (string, func(), error) { return a.Grant(live, scope) }
}

// ActionsCacheStats implements ui.ActionsCache: what is stored right
// now, the bound it is held to, and the per-repository breakdown, which
// is the figure that answers "who is filling this".
//
// Largest repository first. The map actionscache.Stats hands back has
// no order of its own, and a list of repositories that shuffles itself
// every five seconds under a polling page is unreadable -- sorted by
// size rather than by name because the one being read is the one at the
// top.
func (a *ActionsCache) ActionsCacheStats() (int, int64, int64, []ui.ActionsCacheRepoStats) {
	if a == nil || a.cache == nil {
		return 0, 0, 0, nil
	}
	stats := a.cache.Stats()
	repos := make([]ui.ActionsCacheRepoStats, 0, len(stats.Repos))
	for repo, bytes := range stats.Repos {
		repos = append(repos, ui.ActionsCacheRepoStats{Repo: repo, Bytes: bytes})
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Bytes != repos[j].Bytes {
			return repos[i].Bytes > repos[j].Bytes
		}
		return repos[i].Repo < repos[j].Repo
	})
	return stats.Entries, stats.Bytes, stats.MaxBytes, repos
}

// ActionsCacheEntryCount implements ui.ActionsCache.
func (a *ActionsCache) ActionsCacheEntryCount() int {
	if a == nil || a.cache == nil {
		return 0
	}
	return a.cache.Stats().Entries
}

// FlushActionsCache implements ui.ActionsCache.
//
// The bytes are measured before the flush rather than reported by it:
// Flush answers with a count of entries, since what it costs the cache
// is entries, and what somebody pressing a button on a page about disk
// wants told back is space. Measuring first is exact enough for that --
// a job committing an entry in the microseconds between the two makes
// this figure a little small, and nothing at all depends on it.
func (a *ActionsCache) FlushActionsCache(repo string) (int, int64) {
	if a == nil || a.cache == nil {
		return 0, 0
	}
	before := a.cache.Stats()
	freed := before.Bytes
	if repo != "" {
		freed = before.Repos[repo]
	}
	entries := a.cache.Flush(repo)
	what := repo
	if what == "" {
		what = "every repository's"
	}
	log.Printf("grain daemon: Actions cache: flushed %s entries -- %d entries, %d MiB", what, entries, freed>>20)
	return entries, freed
}

// uiActionsCache is a as the UI takes one.
func (a *ActionsCache) uiActionsCache() ui.ActionsCache {
	if a == nil || a.cache == nil {
		return nil
	}
	return a
}
