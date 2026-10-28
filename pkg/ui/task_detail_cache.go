package ui

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
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
// retain an unbounded amount of comment and attempt history. Thirty-two entries
// covers considerably more simultaneously open panes than a normal browser
// session while placing a firm bound on retained response bodies. The least
// recently used entry is evicted when that bound is crossed.
//
// As with taskListCache, a ChangeToken error disables this optimisation for the
// request, and concurrent misses may build concurrently rather than serialising
// callers behind a database read.
type taskDetailCache struct {
	mu      sync.Mutex
	entries map[string]taskDetailCacheEntry
	clock   uint64
	builds  atomic.Int64
}

type taskDetailCacheEntry struct {
	token string
	body  []byte
	etag  string
	used  uint64
}

const taskDetailCacheEntries = 32

// response is the body and ETag for GET /api/tasks/{id}.
func (c *taskDetailCache) response(ctx context.Context, client *Client, id string) ([]byte, string, error) {
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
		if entry, ok := c.hit(id, token); ok {
			return entry.body, entry.etag, nil
		}
	}

	c.builds.Add(1)
	detail, err := client.GetTask(ctx, id)
	if err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return nil, "", err
	}
	etag := etagOf(body)
	if token != "" {
		c.store(id, taskDetailCacheEntry{token: token, body: body, etag: etag})
	}
	return body, etag, nil
}

func (c *taskDetailCache) hit(id, token string) (taskDetailCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok || entry.token != token {
		return taskDetailCacheEntry{}, false
	}
	c.clock++
	entry.used = c.clock
	c.entries[id] = entry
	return entry, true
}

func (c *taskDetailCache) store(id string, entry taskDetailCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]taskDetailCacheEntry)
	}
	c.clock++
	entry.used = c.clock
	c.entries[id] = entry
	if len(c.entries) <= taskDetailCacheEntries {
		return
	}
	var oldestID string
	oldest := ^uint64(0)
	for candidateID, candidate := range c.entries {
		if candidate.used < oldest {
			oldestID, oldest = candidateID, candidate.used
		}
	}
	delete(c.entries, oldestID)
}
