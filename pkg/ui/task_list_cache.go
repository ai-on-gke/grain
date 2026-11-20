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

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What GET /api/tasks and GET /api/inbox do when asked the same question again.
//
// Under load with 100 live runs, Store.ChangeToken moves on every heartbeat
// (SetTaskActivity), transcript flush (SetRunTranscript), telemetry write, and
// progress comment, so a cache keyed on ChangeToken never hits.
//
// taskListCache instead keys on Store.TaskListTokens():
//   - structural moves on task-row, state, observation, pause, goal, link,
//     template, QA, notification, or dispatch-condition changes.
//   - activity moves on live-run activity updates (SetTaskActivity,
//     SetRunAgentStarted) alone.
//
// High-frequency live-run writes that change no list row (SetRunTranscript,
// SetRunPrompt, telemetry, progress comments on running tasks) move neither
// token, so cached answers hit directly with zero DB work. When only activity
// moves, live views overlay fresh Store.TaskActivity (<= live runs only) onto
// the cached task slice without re-querying or re-enriching tasks, while
// non-running views (queued, closed, proposed, fields=summary, /api/inbox)
// ignore activity completely and hit directly.
//
// As before, tokens are read strictly BEFORE querying the store (and Store
// bumps them strictly AFTER tx.Commit), so a concurrent commit can at worst
// cause one extra rebuild and can never freeze a stale value.
type taskListCache struct {
	mu         sync.Mutex
	structural string // structural token + dispatchConditionVersion
	liveBodies map[string]liveCacheEntry
	filtered   map[string]filteredCacheEntry
	counts     map[string]model.TaskListCounts
	inbox      map[string][]InboxItem

	// builds counts the times a task-list request went to the store to query
	// and enrich tasks (not activity-only overlays), for export_test.go.
	builds atomic.Int64
}

const (
	maxFilteredCacheEntries = 64
	maxCountsCacheEntries   = 64
	maxInboxCacheEntries    = 32
)

type liveCacheEntry struct {
	activity string
	tasks    []Task
	body     []byte
	etag     string
}

type filteredCacheEntry struct {
	activity string
	tasks    []Task // non-nil only when needsActivity is true
	meta     any    // page metadata for re-rendering after activity overlay
	body     []byte
}

// taskListView is which cut of the unnarrowed list a body holds.
type taskListView int

const (
	// taskListLive is what the UI polls: every open task, without
	// descriptions.
	taskListLive taskListView = iota
	// taskListFull is every task with every field (?fields=full).
	taskListFull
)

func taskListKey(view taskListView, repo, scopeKey string) string {
	key := "live:"
	if view == taskListFull {
		key = "full:"
	}
	if repo != "" {
		key += "repo:" + strings.ToLower(repo) + ":"
	}
	if scopeKey != "" {
		key += "scope:" + scopeKey
	}
	return key
}

// listRow is t as a list carries it: everything but its description,
// which is GET /api/tasks/{id}'s to answer.
func listRow(t Task) Task {
	t.Description = ""
	return t
}

// cacheTokens returns the (structural, activity) version tokens under which
// taskListCache entries are valid. Empty structural disables caching.
func (c *taskListCache) cacheTokens(client *Client) (structural, activity string) {
	if client == nil || client.Store == nil {
		return "", ""
	}
	structural, activity = client.Store.TaskListTokens()
	if structural != "" {
		if v := client.dispatchConditionVersion(); v != 0 {
			structural = structural + ":dc=" + strconv.FormatUint(v, 10)
		}
	}
	return structural, activity
}

func (c *taskListCache) resetIfStaleLocked(structural string) {
	if c.structural != structural {
		c.structural = structural
		c.liveBodies = map[string]liveCacheEntry{}
		c.filtered = map[string]filteredCacheEntry{}
		c.counts = map[string]model.TaskListCounts{}
		c.inbox = map[string][]InboxItem{}
	}
}

// liveResponse answers unnarrowed GET /api/tasks (live or fields=full) with
// body and ETag, using SQL-bounded QueryLiveTasks for the live view and
// overlaying fresh TaskActivity on activity-only ticks without rebuilding.
func (c *taskListCache) liveResponse(
	ctx context.Context,
	client *Client,
	filter model.TaskListFilter,
	scopeKey string,
	view taskListView,
	liveLimit int,
) ([]byte, string, error) {
	structural, activity := c.cacheTokens(client)
	key := taskListKey(view, filter.Repo, scopeKey)

	if structural != "" {
		c.mu.Lock()
		var entry liveCacheEntry
		var ok bool
		if c.structural == structural {
			entry, ok = c.liveBodies[key]
		}
		c.mu.Unlock()
		if ok {
			if entry.activity == activity {
				return entry.body, entry.etag, nil
			}
			// Only live-run activity moved: patch running rows from
			// Store.TaskActivity (<= live runs) without reloading tasks.
			if entry.tasks != nil {
				liveAct, err := client.Store.TaskActivity(ctx)
				if err != nil {
					return nil, "", err
				}
				patched := overlayTaskActivity(entry.tasks, liveAct)
				body, err := json.Marshal(patched)
				if err != nil {
					return nil, "", err
				}
				etag := etagOf(body)
				c.mu.Lock()
				if c.structural == structural {
					c.liveBodies[key] = liveCacheEntry{
						activity: activity,
						tasks:    patched,
						body:     body,
						etag:     etag,
					}
				}
				c.mu.Unlock()
				return body, etag, nil
			}
		}
	}

	c.builds.Add(1)
	var tasks []Task
	var err error
	if view == taskListLive {
		tasks, err = client.QueryLiveTasks(ctx, filter, liveLimit)
		if err != nil {
			return nil, "", err
		}
		for i := range tasks {
			tasks[i] = listRow(tasks[i])
		}
	} else {
		all, err := client.ListTasks(ctx)
		if err != nil {
			return nil, "", err
		}
		tasks = filterTasksByRepoScope(all, filter)
	}
	if tasks == nil {
		tasks = []Task{}
	}
	body, err := json.Marshal(tasks)
	if err != nil {
		return nil, "", err
	}
	etag := etagOf(body)
	if structural != "" {
		c.mu.Lock()
		c.resetIfStaleLocked(structural)
		c.liveBodies[key] = liveCacheEntry{
			activity: activity,
			tasks:    tasks,
			body:     body,
			etag:     etag,
		}
		c.mu.Unlock()
	}
	return body, etag, nil
}

func filterTasksByRepoScope(tasks []Task, filter model.TaskListFilter) []Task {
	if filter.Repo == "" && !filter.UseRepoScope {
		return tasks
	}
	allowed := map[string]bool{}
	for _, r := range filter.AllowedRepos {
		allowed[strings.ToLower(r.String())] = true
	}
	out := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if filter.Repo != "" && !strings.EqualFold(t.Repo, filter.Repo) {
			continue
		}
		if filter.UseRepoScope {
			if t.Repo == "" {
				if !filter.AllowEmptyRepo {
					continue
				}
			} else if !allowed[strings.ToLower(t.Repo)] {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// filteredResponse serves a filtered or paged GET /api/tasks body from the
// cache keyed on structural (plus activity overlay only when needsActivity is
// true), calling build on a structural miss.
func (c *taskListCache) filteredResponse(
	ctx context.Context,
	client *Client,
	key string,
	needsActivity bool,
	build func() (tasks []Task, meta any, body []byte, err error),
	render func(tasks []Task, meta any) ([]byte, error),
) ([]byte, error) {
	structural, activity := c.cacheTokens(client)
	if structural != "" {
		c.mu.Lock()
		var entry filteredCacheEntry
		var ok bool
		if c.structural == structural {
			entry, ok = c.filtered[key]
		}
		c.mu.Unlock()
		if ok {
			if !needsActivity || entry.activity == activity {
				return entry.body, nil
			}
			if entry.tasks != nil && render != nil {
				liveAct, err := client.Store.TaskActivity(ctx)
				if err != nil {
					return nil, err
				}
				patched := overlayTaskActivity(entry.tasks, liveAct)
				body, err := render(patched, entry.meta)
				if err != nil {
					return nil, err
				}
				c.mu.Lock()
				if c.structural == structural {
					c.filtered[key] = filteredCacheEntry{
						activity: activity,
						tasks:    patched,
						meta:     entry.meta,
						body:     body,
					}
				}
				c.mu.Unlock()
				return body, nil
			}
		}
	}

	c.builds.Add(1)
	tasks, meta, body, err := build()
	if err != nil {
		return nil, err
	}
	if structural != "" {
		c.mu.Lock()
		c.resetIfStaleLocked(structural)
		if len(c.filtered) >= maxFilteredCacheEntries {
			c.filtered = map[string]filteredCacheEntry{}
		}
		var kept []Task
		var keptMeta any
		if needsActivity {
			kept, keptMeta = tasks, meta
		}
		c.filtered[key] = filteredCacheEntry{
			activity: activity,
			tasks:    kept,
			meta:     keptMeta,
			body:     body,
		}
		c.mu.Unlock()
	}
	return body, nil
}

// cachedCounts returns TaskListCounts for filterKey at the current structural
// token, so walking cursor pages does not re-run aggregate COUNT queries.
func (c *taskListCache) cachedCounts(
	client *Client,
	filterKey string,
	compute func() (model.TaskListCounts, error),
) (model.TaskListCounts, error) {
	structural, _ := c.cacheTokens(client)
	if structural != "" {
		c.mu.Lock()
		if c.structural == structural {
			if got, ok := c.counts[filterKey]; ok {
				c.mu.Unlock()
				return got, nil
			}
		}
		c.mu.Unlock()
	}
	got, err := compute()
	if err != nil {
		return model.TaskListCounts{}, err
	}
	if structural != "" {
		c.mu.Lock()
		c.resetIfStaleLocked(structural)
		if len(c.counts) >= maxCountsCacheEntries {
			c.counts = map[string]model.TaskListCounts{}
		}
		c.counts[filterKey] = got
		c.mu.Unlock()
	}
	return got, nil
}

// cachedInbox returns cached inbox items keyed on the structural token alone:
// live-run heartbeats and transcripts cannot change inbox membership.
func (c *taskListCache) cachedInbox(
	client *Client,
	key string,
	build func() ([]InboxItem, error),
) ([]InboxItem, error) {
	structural, _ := c.cacheTokens(client)
	if structural != "" {
		c.mu.Lock()
		if c.structural == structural {
			if items, ok := c.inbox[key]; ok {
				c.mu.Unlock()
				return items, nil
			}
		}
		c.mu.Unlock()
	}
	items, err := build()
	if err != nil {
		return nil, err
	}
	if structural != "" {
		c.mu.Lock()
		c.resetIfStaleLocked(structural)
		if len(c.inbox) >= maxInboxCacheEntries {
			c.inbox = map[string][]InboxItem{}
		}
		c.inbox[key] = items
		c.mu.Unlock()
	}
	return items, nil
}

// overlayTaskActivity returns a copy of tasks with live running tasks'
// Activity/ActivityAt/ActivityBySetup refreshed from live without touching
// non-running tasks (such as queued grant-wait or OOM-retry notes).
func overlayTaskActivity(tasks []Task, live map[string]model.RunActivity) []Task {
	out := make([]Task, len(tasks))
	copy(out, tasks)
	for i := range out {
		if out[i].State != model.StateRunning {
			continue
		}
		if a, ok := live[out[i].ID]; ok {
			out[i].Activity = a.Note
			out[i].ActivityAt = a.At
			out[i].ActivityBySetup = a.BySetup
		} else {
			out[i].Activity = ""
			out[i].ActivityAt = nil
			out[i].ActivityBySetup = false
		}
	}
	return out
}

// etagOf is a strong validator over the exact bytes served: the same
// list is the same ETag, whoever built it and whenever.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches reports whether an If-None-Match header names this ETag.
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
