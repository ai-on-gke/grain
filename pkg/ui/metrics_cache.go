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
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

// metricsCacheTTL is how long a computed metrics report is reused for the
// same (window, buckets, section, scope). Short enough that operators see
// newly finished work on the next refresh, long enough that concurrent UI
// polls and scale load do not hammer the store every time a live run writes
// a heartbeat, transcript, or finishes an attempt.
const metricsCacheTTL = 5 * time.Second

// metricsCacheMaxEntries bounds how many distinct (window, buckets, section,
// scope) reports are held at once so arbitrary query parameters cannot grow
// memory without limit.
const metricsCacheMaxEntries = 32

// metricsBuildTimeout bounds one shared report build, which runs detached
// from any single caller's context (runMetricsFlight).
const metricsBuildTimeout = 2 * time.Minute

type metricsCacheKey struct {
	window    time.Duration
	sinceNano int64
	untilNano int64
	buckets   int
	section   string
	repos     string
	authors   string
	fixedEnd  bool
}

func makeMetricsCacheKey(req MetricsRequest, section string) metricsCacheKey {
	k := metricsCacheKey{
		buckets: req.Buckets,
		section: section,
		repos:   strings.Join(req.Repos, "\x00"),
		authors: strings.Join(req.Authors, "\x00"),
		// Only an absolute span (since and until both set) has a fixed end:
		// MetricsRequest.span ignores Until when Since is zero and rolls the
		// window forward with now, so such a request must expire on TTL.
		fixedEnd: !req.Since.IsZero() && !req.Until.IsZero(),
	}
	if !req.Since.IsZero() {
		k.sinceNano = req.Since.UTC().UnixNano()
	} else {
		w := req.Window
		if w <= 0 {
			w = DefaultMetricsWindow
		}
		k.window = w
	}
	if k.fixedEnd {
		k.untilNano = req.Until.UTC().UnixNano()
	}
	return k
}

// isInMemoryMetricsSection reports whether section reads only in-process
// state (zero database queries), so it costs microseconds and bypasses the
// TTL cache to stay instantaneously live.
func isInMemoryMetricsSection(section string) bool {
	switch section {
	case mcp.MetricsSectionCycles, mcp.MetricsSectionGitHub, mcp.MetricsSectionHost:
		return true
	default:
		return false
	}
}

type metricsCacheEntry struct {
	report  MetricsReport
	builtAt time.Time
	token   string
	used    uint64

	bodyOnce sync.Once
	body     []byte
	etag     string
	bodyErr  error
}

func (e *metricsCacheEntry) marshalBody() ([]byte, string, error) {
	e.bodyOnce.Do(func() {
		b, err := json.Marshal(e.report)
		if err != nil {
			e.bodyErr = err
			return
		}
		e.body = b
		e.etag = etagOf(b)
	})
	return e.body, e.etag, e.bodyErr
}

type metricsInflight struct {
	ready chan struct{}
	entry *metricsCacheEntry
	err   error
}

type metricsCache struct {
	mu       sync.Mutex
	entries  map[metricsCacheKey]*metricsCacheEntry
	inflight map[metricsCacheKey]*metricsInflight
	clock    uint64
	builds   atomic.Int64
}

// MetricsBuilds reports how many times the server has computed a metrics
// report rather than answering from cache. For tests.
func MetricsBuilds(s *Server) int64 {
	if s == nil || s.tasks == nil {
		return 0
	}
	return s.tasks.metricsCache.builds.Load()
}

func (c *Client) metricsCached(ctx context.Context, req MetricsRequest) (*metricsCacheEntry, error) {
	section := strings.TrimSpace(strings.ToLower(req.Section))
	req.Section = section

	if isInMemoryMetricsSection(section) {
		c.metricsCache.builds.Add(1)
		rep, err := c.computeMetrics(ctx, req)
		if err != nil {
			return nil, err
		}
		return &metricsCacheEntry{report: rep, builtAt: c.now()}, nil
	}

	key := makeMetricsCacheKey(req, section)
	for {
		now := c.now()
		c.metricsCache.mu.Lock()
		var staleFixed *metricsCacheEntry
		if entry, ok := c.metricsCache.entries[key]; ok {
			age := now.Sub(entry.builtAt)
			if age >= 0 && age < metricsCacheTTL {
				c.metricsCache.clock++
				entry.used = c.metricsCache.clock
				c.metricsCache.mu.Unlock()
				return entry, nil
			}
			if key.fixedEnd && entry.token != "" {
				staleFixed = entry
			}
		}
		if staleFixed != nil {
			// For a fixed-end span past TTL, reuse the entry if nothing in
			// the store has been committed since it was built.
			c.metricsCache.mu.Unlock()
			if c.Store != nil {
				if tok, err := c.Store.ChangeToken(ctx); err == nil && tok == staleFixed.token {
					c.metricsCache.mu.Lock()
					if cur, ok := c.metricsCache.entries[key]; ok && cur == staleFixed {
						cur.builtAt = now
						c.metricsCache.clock++
						cur.used = c.metricsCache.clock
					}
					c.metricsCache.mu.Unlock()
					return staleFixed, nil
				}
			}
			c.metricsCache.mu.Lock()
			// Re-check in case another caller refreshed or started a build
			// while ChangeToken ran.
			if entry, ok := c.metricsCache.entries[key]; ok && entry != staleFixed {
				age := now.Sub(entry.builtAt)
				if age >= 0 && age < metricsCacheTTL {
					c.metricsCache.clock++
					entry.used = c.metricsCache.clock
					c.metricsCache.mu.Unlock()
					return entry, nil
				}
			}
		}

		if flight, ok := c.metricsCache.inflight[key]; ok {
			c.metricsCache.mu.Unlock()
			select {
			case <-flight.ready:
				if flight.err != nil && ctx.Err() == nil && errors.Is(flight.err, context.Canceled) {
					continue
				}
				return flight.entry, flight.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if c.metricsCache.inflight == nil {
			c.metricsCache.inflight = map[metricsCacheKey]*metricsInflight{}
		}
		flight := &metricsInflight{ready: make(chan struct{})}
		c.metricsCache.inflight[key] = flight
		c.metricsCache.mu.Unlock()

		entry, err := c.runMetricsFlight(ctx, key, req, now, flight)
		return entry, err
	}
}

func (c *Client) runMetricsFlight(ctx context.Context, key metricsCacheKey, req MetricsRequest, now time.Time, flight *metricsInflight) (entry *metricsCacheEntry, err error) {
	defer func() {
		c.metricsCache.mu.Lock()
		if c.metricsCache.inflight[key] == flight {
			delete(c.metricsCache.inflight, key)
		}
		if err == nil && entry != nil && len(entry.report.Failed) == 0 {
			if c.metricsCache.entries == nil {
				c.metricsCache.entries = map[metricsCacheKey]*metricsCacheEntry{}
			}
			c.metricsCache.clock++
			entry.used = c.metricsCache.clock
			c.metricsCache.entries[key] = entry
			if len(c.metricsCache.entries) > metricsCacheMaxEntries {
				var oldestKey metricsCacheKey
				oldest := ^uint64(0)
				for k, v := range c.metricsCache.entries {
					if v.used < oldest {
						oldestKey, oldest = k, v.used
					}
				}
				delete(c.metricsCache.entries, oldestKey)
			}
		}
		flight.entry = entry
		flight.err = err
		c.metricsCache.mu.Unlock()
		close(flight.ready)
	}()

	var token string
	// Read the change token before querying so a concurrent commit cannot be
	// swallowed into a fixed-end cached entry.
	if key.fixedEnd && c.Store != nil {
		if t, err := c.Store.ChangeToken(context.WithoutCancel(ctx)); err == nil {
			token = t
		}
	}
	c.metricsCache.builds.Add(1)
	// The build is shared by every caller coalesced onto this flight, so it
	// runs detached from the leader's own context: a leader whose client goes
	// away must not hand context-cancelled section failures to the waiters
	// still connected. Waiters still stop waiting through their own ctx, and
	// the build is bounded by metricsBuildTimeout.
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsBuildTimeout)
	defer cancel()
	rep, buildErr := c.computeMetrics(buildCtx, req)
	if buildErr != nil {
		return nil, buildErr
	}
	return &metricsCacheEntry{
		report:  rep,
		builtAt: now,
		token:   token,
	}, nil
}
