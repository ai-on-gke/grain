// Copyright 2026 The grain Authors. All rights reserved.
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

package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

// HostRouteKind classifies an incoming HTTP Host header.
type HostRouteKind int

const (
	HostRouteUnknown HostRouteKind = iota
	HostRouteRegistry
	HostRouteInstance
	HostRouteInvalidInstance
)

// HostMatch holds the parsed result of classifying an HTTP Host header.
type HostMatch struct {
	Kind      HostRouteKind
	Host      string // normalized lowercase host without port
	EntryName string // <name> when Kind == HostRouteInstance
}

// NormalizeHost strips whitespace, port, and trailing dot from an HTTP Host header
// and lowercases the hostname.
func NormalizeHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	if h == "" {
		return ""
	}
	if strings.Contains(h, ":") {
		if splitHost, _, err := net.SplitHostPort(h); err == nil {
			h = splitHost
		} else if idx := strings.LastIndex(h, ":"); idx > 0 && !strings.Contains(h, "]") {
			h = h[:idx]
		}
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	h = strings.TrimSuffix(h, ".")
	return h
}

// ClassifyHost classifies an HTTP Host header against registryHost and instanceDomain.
// Multi-label prefixes under instanceDomain (e.g. "a.b.<instance-domain>"), bare
// "<instance-domain>", or syntactically invalid entry names return HostRouteInvalidInstance (404).
// Unrecognized hosts return HostRouteUnknown (404), except loopback addresses used by
// local health probes and httptest servers which map to HostRouteRegistry.
func ClassifyHost(hostHeader, registryHost, instanceDomain string) HostMatch {
	h := NormalizeHost(hostHeader)
	reg := NormalizeHost(registryHost)
	instDom := NormalizeHost(instanceDomain)

	if h == "" {
		return HostMatch{Kind: HostRouteUnknown}
	}
	if reg != "" && h == reg {
		return HostMatch{Kind: HostRouteRegistry, Host: h}
	}
	if instDom != "" {
		if h == instDom {
			return HostMatch{Kind: HostRouteInvalidInstance, Host: h}
		}
		suffix := "." + instDom
		if strings.HasSuffix(h, suffix) {
			prefix := strings.TrimSuffix(h, suffix)
			if prefix == "" || strings.Contains(prefix, ".") || api.ValidateEntryName(prefix) != nil {
				return HostMatch{Kind: HostRouteInvalidInstance, Host: h, EntryName: prefix}
			}
			return HostMatch{
				Kind:      HostRouteInstance,
				Host:      h,
				EntryName: prefix,
			}
		}
	}
	if h == "127.0.0.1" || h == "localhost" || h == "::1" {
		return HostMatch{Kind: HostRouteRegistry, Host: h}
	}
	return HostMatch{Kind: HostRouteUnknown, Host: h}
}

const (
	DefaultHostCacheTTL        = 5 * time.Second
	DefaultHostCacheStaleGrace = 10 * time.Minute
	DefaultTunnelPollInterval  = 3 * time.Second
	DefaultReconnectWindow     = 5 * time.Second
	MaxReconnectWaiters        = 32
)

// ErrTooManyReconnectWaiters is returned when more than 32 requests are already
// waiting on reconnect_grace for the same entry.
var ErrTooManyReconnectWaiters = errors.New("registry server: too many concurrent reconnect_grace waiters")

// HostCacheEntry caches routing metadata for `<name>.<instance-domain>`.
type HostCacheEntry struct {
	Entry      *store.EntryRow
	DeletedRow *store.EntryRow
	Missing    bool
	FetchedAt  time.Time
}

// HostCache maintains an in-memory cache of entry routing metadata keyed by entry name,
// invalidated by `LISTEN registry_entries` with a 5s TTL fallback and a 10m read-only
// stale grace window during PostgreSQL failover.
type HostCache struct {
	store      *store.Store
	ttl        time.Duration
	staleGrace time.Duration
	nowFn      func() time.Time

	mu      sync.RWMutex
	entries map[string]HostCacheEntry
}

// NewHostCache creates a new HostCache.
func NewHostCache(st *store.Store, ttl, staleGrace time.Duration) *HostCache {
	if ttl <= 0 {
		ttl = DefaultHostCacheTTL
	}
	if staleGrace <= 0 {
		staleGrace = DefaultHostCacheStaleGrace
	}
	return &HostCache{
		store:      st,
		ttl:        ttl,
		staleGrace: staleGrace,
		nowFn:      time.Now,
		entries:    make(map[string]HostCacheEntry),
	}
}

// SetNow overrides the clock used by HostCache (for deterministic tests).
func (c *HostCache) SetNow(fn func() time.Time) {
	if fn != nil {
		c.mu.Lock()
		c.nowFn = fn
		c.mu.Unlock()
	}
}

func (c *HostCache) now() time.Time {
	c.mu.RLock()
	fn := c.nowFn
	c.mu.RUnlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// Invalidate removes `name` from the active TTL window while preserving its last known
// EntryRow for the 10-minute PostgreSQL failover grace window.
func (c *HostCache) Invalidate(name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.entries[name]; ok {
		cur.FetchedAt = time.Time{}
		c.entries[name] = cur
	}
}

// Put stores an entry directly into HostCache (useful for tests or immediate post-write updates).
func (c *HostCache) Put(row *store.EntryRow) {
	if row == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(row.Name))
	c.mu.Lock()
	c.entries[name] = HostCacheEntry{
		Entry:     row,
		FetchedAt: c.nowFn().UTC(),
	}
	c.mu.Unlock()
}

// Lookup resolves `<name>` from HostCache or refreshes from PostgreSQL.
// If PostgreSQL fails with a transient/connection error and a non-missing entry
// was cached within `staleGrace` (10m), the cached entry is returned so existing
// instances and tunnels continue serving during a database failover.
func (c *HostCache) Lookup(ctx context.Context, name string) (HostCacheEntry, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	now := c.now()

	c.mu.RLock()
	cached, hasCached := c.entries[name]
	c.mu.RUnlock()

	if hasCached && !cached.FetchedAt.IsZero() && now.Sub(cached.FetchedAt) < c.ttl {
		return cached, nil
	}

	if c.store == nil {
		if hasCached && cached.Entry != nil {
			return cached, nil
		}
		return HostCacheEntry{Missing: true, FetchedAt: now}, nil
	}

	row, err := c.store.GetEntryByName(ctx, name)
	if err == nil {
		entry := HostCacheEntry{
			Entry:     row,
			FetchedAt: now,
		}
		c.mu.Lock()
		c.entries[name] = entry
		c.mu.Unlock()
		return entry, nil
	}

	if errors.Is(err, store.ErrNotFound) {
		var delRow *store.EntryRow
		if d, dErr := c.store.GetLatestDeletedEntryByName(ctx, name); dErr == nil {
			delRow = d
		}
		entry := HostCacheEntry{
			DeletedRow: delRow,
			Missing:    true,
			FetchedAt:  now,
		}
		c.mu.Lock()
		c.entries[name] = entry
		c.mu.Unlock()
		return entry, nil
	}

	// Database error: serve stale cached entry if within the 10-minute failover grace window.
	if hasCached && cached.Entry != nil && now.Sub(cached.UpdatedAtOrFetched()) <= c.staleGrace {
		return cached, nil
	}
	return HostCacheEntry{}, err
}

// UpdatedAtOrFetched returns the timestamp used for evaluating the 10m failover grace window.
func (e HostCacheEntry) UpdatedAtOrFetched() time.Time {
	if !e.FetchedAt.IsZero() {
		return e.FetchedAt
	}
	if e.Entry != nil {
		return e.Entry.UpdatedAt
	}
	return time.Time{}
}

// TunnelCacheEntry represents the cached tunnel routing state for a `connected` entry.
type TunnelCacheEntry struct {
	EntryID          string
	Name             string
	SessionEpoch     int64
	ReplicaID        string
	PeerAddr         string
	ReplicaLastSeen  time.Time
	ConnectedAt      time.Time
	LastSeenAt       time.Time
	CredID           string
	DisconnectedAt   *time.Time
	DisconnectReason string
	UpdatedAt        time.Time
}

// HasActiveSession reports whether the cache entry points to a live tunnel session
// whose holding replica heartbeat is within `replicaTTL`.
func (e TunnelCacheEntry) HasActiveSession(now time.Time, replicaTTL time.Duration) bool {
	if e.SessionEpoch <= 0 || e.ReplicaID == "" {
		return false
	}
	if replicaTTL > 0 && !e.ReplicaLastSeen.IsZero() && now.Sub(e.ReplicaLastSeen) > replicaTTL {
		return false
	}
	return true
}

// DisconnectedRecently reports whether the tunnel disconnected within `window` (5s default).
func (e TunnelCacheEntry) DisconnectedRecently(now time.Time, window time.Duration) bool {
	if e.DisconnectedAt == nil {
		return false
	}
	if window <= 0 {
		window = DefaultReconnectWindow
	}
	age := now.Sub(*e.DisconnectedAt)
	return age >= 0 && age <= window
}

// TunnelCache maintains an in-memory map of active and recently disconnected tunnels
// keyed by entry name, updated via `LISTEN registry_tunnels` and a 3s fallback poll,
// and coordinates `reconnect_grace` waiters (bounded to 32 concurrent waiters per entry).
type TunnelCache struct {
	store      *store.Store
	replicaTTL time.Duration
	nowFn      func() time.Time

	mu      sync.RWMutex
	byName  map[string]TunnelCacheEntry
	waiters map[string]map[chan struct{}]struct{}
}

// NewTunnelCache creates a new TunnelCache.
func NewTunnelCache(st *store.Store, replicaTTL time.Duration) *TunnelCache {
	if replicaTTL <= 0 {
		replicaTTL = 15 * time.Second
	}
	return &TunnelCache{
		store:      st,
		replicaTTL: replicaTTL,
		nowFn:      time.Now,
		byName:     make(map[string]TunnelCacheEntry),
		waiters:    make(map[string]map[chan struct{}]struct{}),
	}
}

// SetNow overrides the clock used by TunnelCache.
func (c *TunnelCache) SetNow(fn func() time.Time) {
	if fn != nil {
		c.mu.Lock()
		c.nowFn = fn
		c.mu.Unlock()
	}
}

func (c *TunnelCache) now() time.Time {
	c.mu.RLock()
	fn := c.nowFn
	c.mu.RUnlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// Get returns the current TunnelCacheEntry for `name`, if any.
func (c *TunnelCache) Get(name string) (TunnelCacheEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byName[name]
	return e, ok
}

// SetActive records an active tunnel session for `entry.Name` and wakes any
// requests waiting in `WaitForReconnect`.
func (c *TunnelCache) SetActive(entry TunnelCacheEntry) {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	if name == "" {
		return
	}
	entry.Name = name
	entry.DisconnectedAt = nil
	entry.DisconnectReason = ""
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = c.now()
	}

	c.mu.Lock()
	c.byName[name] = entry
	wset := c.waiters[name]
	delete(c.waiters, name)
	c.mu.Unlock()

	for ch := range wset {
		close(ch)
	}
}

// MarkDisconnected records that `name`'s tunnel session disconnected at `at` with `reason`.
// If `expectedEpoch > 0` and the cached session has already advanced to a newer epoch,
// MarkDisconnected is a no-op.
func (c *TunnelCache) MarkDisconnected(name string, expectedEpoch int64, at time.Time, reason string) {
	name = strings.ToLower(strings.TrimSpace(name))
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.byName[name]
	if ok && expectedEpoch > 0 && cur.SessionEpoch > expectedEpoch {
		return
	}
	discAt := at.UTC()
	cur.Name = name
	cur.SessionEpoch = 0
	cur.ReplicaID = ""
	cur.PeerAddr = ""
	cur.DisconnectedAt = &discAt
	if discAt.After(cur.LastSeenAt) {
		cur.LastSeenAt = discAt
	}
	if reason != "" {
		cur.DisconnectReason = reason
	}
	cur.UpdatedAt = discAt
	c.byName[name] = cur
}

// InvalidatePeerGone marks `name` as having just lost its remote peer tunnel so the
// ingress replica immediately enters the 2s `reconnect_grace` hold.
func (c *TunnelCache) InvalidatePeerGone(name string) {
	c.MarkDisconnected(name, 0, c.now(), "peer tunnel gone")
}

// RefreshEntry reloads `name`'s tunnel state from PostgreSQL and wakes any waiting
// requests if an active tunnel is now registered.
func (c *TunnelCache) RefreshEntry(ctx context.Context, name string) (TunnelCacheEntry, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if c.store == nil {
		e, _ := c.Get(name)
		return e, nil
	}
	row, err := c.store.GetEntryByName(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.MarkDisconnected(name, 0, c.now(), "entry deleted")
		}
		return TunnelCacheEntry{}, err
	}
	return c.SyncFromEntryRow(row), nil
}

// SyncFromEntryRow updates TunnelCache from a loaded `*store.EntryRow`.
func (c *TunnelCache) SyncFromEntryRow(row *store.EntryRow) TunnelCacheEntry {
	if row == nil {
		return TunnelCacheEntry{}
	}
	now := c.now()
	name := strings.ToLower(strings.TrimSpace(row.Name))
	if row.Tunnel != nil && row.DeletedAt == nil && !row.OwnerDisabled {
		tc := TunnelCacheEntry{
			EntryID:         row.ID,
			Name:            name,
			SessionEpoch:    row.Tunnel.SessionEpoch,
			ReplicaID:       row.Tunnel.ReplicaID,
			PeerAddr:        row.Tunnel.PeerAddr,
			ReplicaLastSeen: row.Tunnel.ReplicaLastSeen,
			ConnectedAt:     row.Tunnel.ConnectedAt,
			LastSeenAt:      row.Tunnel.LastSeenAt,
			CredID:          row.Tunnel.CredID,
			UpdatedAt:       now,
		}
		if tc.HasActiveSession(now, c.replicaTTL) {
			c.SetActive(tc)
			return tc
		}
	}

	var lastSeen time.Time
	if row.LastHeartbeatAt != nil {
		lastSeen = *row.LastHeartbeatAt
	}
	c.mu.Lock()
	cur := c.byName[name]
	if cur.LastSeenAt.After(lastSeen) {
		lastSeen = cur.LastSeenAt
	}
	discAt := row.LastDisconnectAt
	if discAt == nil && cur.DisconnectedAt != nil {
		discAt = cur.DisconnectedAt
	}
	reason := row.LastDisconnectReason
	if reason == "" {
		reason = cur.DisconnectReason
	}
	updated := TunnelCacheEntry{
		EntryID:          row.ID,
		Name:             name,
		LastSeenAt:       lastSeen,
		DisconnectedAt:   discAt,
		DisconnectReason: reason,
		UpdatedAt:        now,
	}
	c.byName[name] = updated
	c.mu.Unlock()
	return updated
}

// PollAll loads all active tunnel sessions from PostgreSQL (`3s` fallback poll).
func (c *TunnelCache) PollAll(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	sessions, err := c.store.ListActiveTunnelSessions(ctx)
	if err != nil {
		return err
	}
	now := c.now()
	activeNames := make(map[string]bool, len(sessions))
	for _, ts := range sessions {
		name := strings.ToLower(strings.TrimSpace(ts.EntryName))
		if name == "" {
			continue
		}
		activeNames[name] = true
		tc := TunnelCacheEntry{
			EntryID:         ts.EntryID,
			Name:            name,
			SessionEpoch:    ts.SessionEpoch,
			ReplicaID:       ts.ReplicaID,
			PeerAddr:        ts.PeerAddr,
			ReplicaLastSeen: ts.ReplicaLastSeen,
			ConnectedAt:     ts.ConnectedAt,
			LastSeenAt:      ts.LastSeenAt,
			CredID:          ts.CredID,
			UpdatedAt:       now,
		}
		if tc.HasActiveSession(now, c.replicaTTL) {
			c.SetActive(tc)
		}
	}

	c.mu.Lock()
	for name, cur := range c.byName {
		if cur.SessionEpoch > 0 && !activeNames[name] {
			disc := now
			cur.SessionEpoch = 0
			cur.ReplicaID = ""
			cur.PeerAddr = ""
			cur.DisconnectedAt = &disc
			cur.UpdatedAt = now
			c.byName[name] = cur
		}
	}
	c.mu.Unlock()
	return nil
}

// WaitForReconnect blocks up to `grace` (default 2s) for `name` to establish an active
// tunnel session. At most 32 concurrent waiters are permitted per entry; the 33rd
// returns ErrTooManyReconnectWaiters immediately.
func (c *TunnelCache) WaitForReconnect(ctx context.Context, name string, grace time.Duration) (TunnelCacheEntry, bool, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if grace <= 0 {
		grace = 2 * time.Second
	}
	now := c.now()

	c.mu.Lock()
	if cur, ok := c.byName[name]; ok && cur.HasActiveSession(now, c.replicaTTL) {
		c.mu.Unlock()
		return cur, true, nil
	}
	wset := c.waiters[name]
	if len(wset) >= MaxReconnectWaiters {
		c.mu.Unlock()
		return TunnelCacheEntry{}, false, ErrTooManyReconnectWaiters
	}
	if wset == nil {
		wset = make(map[chan struct{}]struct{})
		c.waiters[name] = wset
	}
	ch := make(chan struct{})
	wset[ch] = struct{}{}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if set, ok := c.waiters[name]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(c.waiters, name)
			}
		}
		c.mu.Unlock()
	}()

	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-ch:
		cur, ok := c.Get(name)
		return cur, ok && cur.HasActiveSession(c.now(), c.replicaTTL), nil
	case <-timer.C:
		cur, ok := c.Get(name)
		if ok && cur.HasActiveSession(c.now(), c.replicaTTL) {
			return cur, true, nil
		}
		return cur, false, nil
	case <-ctx.Done():
		return TunnelCacheEntry{}, false, ctx.Err()
	}
}

// ActiveWaiters returns the number of goroutines currently waiting on `reconnect_grace` for `name`.
func (c *TunnelCache) ActiveWaiters(name string) int {
	name = strings.ToLower(strings.TrimSpace(name))
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.waiters[name])
}

// ListenPostgresNotifications connects to `dsn` and listens on `registry_entries` and
// `registry_tunnels`, invoking `onEntry(payload)` and `onTunnel(payload)` when notifications arrive.
// It automatically reconnects with backoff until `ctx` is canceled.
func ListenPostgresNotifications(ctx context.Context, dsn string, onEntry func(name string), onTunnel func(name string)) {
	if strings.TrimSpace(dsn) == "" {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		_ = runPostgresListenSession(ctx, dsn, onEntry, onTunnel)
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func runPostgresListenSession(ctx context.Context, dsn string, onEntry func(name string), onTunnel func(name string)) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, `LISTEN registry_entries`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `LISTEN registry_tunnels`); err != nil {
		return err
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		payload := strings.ToLower(strings.TrimSpace(notification.Payload))
		switch notification.Channel {
		case "registry_entries":
			if onEntry != nil {
				onEntry(payload)
			}
		case "registry_tunnels":
			if onTunnel != nil {
				onTunnel(payload)
			}
		}
	}
}
