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
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// taskDetailCache holds the marshalled answers for recently opened task panes.
//
// A task detail, like the task list, is a pure function of committed database
// state. Store.ChangeToken therefore makes a cached body safe to reuse, with
// the same important ordering as taskListCache: read the token before building
// the response so a concurrent commit can only make that new entry miss.
//
// Unlike the one task list, details are keyed by task id. Keeping only the last
// one would make several tabs thrash each other's cache; keeping every id would
// retain an unbounded amount of comment and attempt history. Full unpaged panes
// are bounded to 32 entries, while compact paged head summaries (as served by
// GET /api/tasks/summaries, up to 50 ids per request across /next/agents,
// /next/tasks and /next/inbox) live in a separate 256-entry LRU so a 50-id
// summary poll neither thrashes on every quiet 304 nor evicts open panes.
//
// As with taskListCache, a ChangeToken error disables this optimisation for the
// request, and concurrent misses may build concurrently rather than serialising
// callers behind a database read.
type taskDetailCache struct {
	mu           sync.Mutex
	entries      map[string]taskDetailCacheEntry
	pagedEntries map[string]taskDetailCacheEntry
	clock        uint64
	builds       atomic.Int64
}

type taskDetailCacheEntry struct {
	token string
	repo  string
	task  *model.Task
	body  []byte // legacy TaskDetail JSON, never containing gate observation fields
	etag  string
	used  uint64
}

const (
	taskDetailCacheEntries      = 32
	taskDetailPagedCacheEntries = 256
)

func taskDetailCacheKey(id string, page TaskPage, paged bool) string {
	if paged {
		return id + "?cl=" + strconv.Itoa(page.CommentsLimit) + "&al=" + strconv.Itoa(page.AttemptsLimit)
	}
	return id
}

// tokenFor computes the current cache version token once for a request.
// Empty if Store.ChangeToken cannot be read, which disables caching.
func (c *taskDetailCache) tokenFor(ctx context.Context, client *Client) string {
	token := ""
	if client.Store != nil {
		if t, err := client.Store.ChangeToken(ctx); err == nil {
			token = t
		}
	}
	if token != "" {
		if v := client.localScriptApprovals().version(client.now()); v != 0 {
			token = token + ":lsa=" + strconv.FormatInt(v, 10)
		}
		if v := client.dispatchConditionVersion(); v != 0 {
			token = token + ":dc=" + strconv.FormatUint(v, 10)
		}
	}
	return token
}

// response is the body and ETag for GET /api/tasks/{id} (whole or head page).
func (c *taskDetailCache) response(ctx context.Context, client *Client, id string) ([]byte, string, error) {
	return c.responseFor(ctx, client, id, TaskPage{}, false)
}

func (c *taskDetailCache) responseFor(ctx context.Context, client *Client, id string, page TaskPage, paged bool) ([]byte, string, error) {
	token := c.tokenFor(ctx, client)
	return c.responseForWithToken(ctx, client, id, page, paged, token)
}

func (c *taskDetailCache) responseForWithToken(ctx context.Context, client *Client, id string, page TaskPage, paged bool, token string) ([]byte, string, error) {
	// Always cache the domain-only legacy TaskDetail body keyed by the domain
	// token, even for gate-observed tasks. Fresh gate observations are spliced
	// onto the cached legacy body per read and hashed into the returned ETag so
	// diagnostic lease expiry, errors, or caller scope differences can never be
	// hidden behind HTTP 304 responses.
	cacheKey := taskDetailCacheKey(id, page, paged)
	observes := client.observesGates(id)
	if token != "" {
		if entry, ok := c.hit(cacheKey, token, paged); ok {
			if !observes {
				return entry.body, entry.etag, nil
			}
			if entry.task != nil {
				return spliceGateObservations(ctx, client, *entry.task, entry.body, entry.etag)
			}
		}
	}

	c.builds.Add(1)
	var (
		detail  TaskDetail
		fetched *model.Task
		err     error
	)
	if paged {
		detail, err = client.getTaskLegacy(ctx, id, page, true, nil, &fetched)
	} else {
		detail, err = client.getTaskLegacy(ctx, id, TaskPage{}, false, nil, &fetched)
	}
	if err != nil {
		return nil, "", err
	}
	legacyBody, err := json.Marshal(detail)
	if err != nil {
		return nil, "", err
	}
	legacyETag := etagOf(legacyBody)
	if token != "" {
		c.store(cacheKey, paged, taskDetailCacheEntry{
			token: token,
			repo:  detail.Repo,
			task:  fetched,
			body:  legacyBody,
			etag:  legacyETag,
		})
	}
	if !observes {
		return legacyBody, legacyETag, nil
	}
	return spliceGateObservations(ctx, client, *fetched, legacyBody, legacyETag)
}

type gateObservationFields struct {
	GateObservations           *gates.ObservationView            `json:"gate_observations,omitempty"`
	GateObservationDiagnostics *model.GateObservationDiagnostics `json:"gate_observation_diagnostics,omitempty"`
}

func spliceGateObservations(ctx context.Context, client *Client, task model.Task, legacyBody []byte, legacyETag string) ([]byte, string, error) {
	obs, diag := client.gateObservations(ctx, task)
	if obs == nil && diag == nil {
		return legacyBody, legacyETag, nil
	}
	spliced, err := spliceGateFieldsJSON(legacyBody, obs, diag)
	if err != nil {
		return nil, "", err
	}
	return spliced, etagOf(spliced), nil
}

func spliceGateFieldsJSON(legacyBody []byte, obs *gates.ObservationView, diag *model.GateObservationDiagnostics) ([]byte, error) {
	if obs == nil && diag == nil {
		return legacyBody, nil
	}
	prefix, err := json.Marshal(gateObservationFields{
		GateObservations:           obs,
		GateObservationDiagnostics: diag,
	})
	if err != nil {
		return nil, err
	}
	if len(legacyBody) < 2 || legacyBody[0] != '{' || legacyBody[len(legacyBody)-1] != '}' {
		return nil, fmt.Errorf("unexpected task detail json shape")
	}
	if len(legacyBody) == 2 {
		return prefix, nil
	}
	out := make([]byte, 0, len(prefix)+len(legacyBody)-1)
	out = append(out, prefix[:len(prefix)-1]...)
	out = append(out, ',')
	out = append(out, legacyBody[1:]...)
	return out, nil
}

func (c *taskDetailCache) hit(key, token string, paged bool) (taskDetailCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.entries
	if paged {
		m = c.pagedEntries
	}
	entry, ok := m[key]
	if !ok || entry.token != token {
		return taskDetailCacheEntry{}, false
	}
	c.clock++
	entry.used = c.clock
	m[key] = entry
	return entry, true
}

func (c *taskDetailCache) store(key string, paged bool, entry taskDetailCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var m map[string]taskDetailCacheEntry
	limit := taskDetailCacheEntries
	if paged {
		if c.pagedEntries == nil {
			c.pagedEntries = make(map[string]taskDetailCacheEntry)
		}
		m = c.pagedEntries
		limit = taskDetailPagedCacheEntries
	} else {
		if c.entries == nil {
			c.entries = make(map[string]taskDetailCacheEntry)
		}
		m = c.entries
	}
	c.clock++
	entry.used = c.clock
	m[key] = entry
	if len(m) <= limit {
		return
	}
	var oldestKey string
	oldest := ^uint64(0)
	for candidateKey, candidate := range m {
		if candidate.used < oldest {
			oldestKey, oldest = candidateKey, candidate.used
		}
	}
	delete(m, oldestKey)
}
