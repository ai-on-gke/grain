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
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

// HostRouteKind classifies an incoming HTTP Host header.
type HostRouteKind int

const (
	HostRouteUnknown HostRouteKind = iota
	HostRouteHub
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
// local health probes and httptest servers which map to HostRouteHub.
func ClassifyHost(hostHeader, hubHost, instanceDomain string) HostMatch {
	h := NormalizeHost(hostHeader)
	reg := NormalizeHost(hubHost)
	instDom := NormalizeHost(instanceDomain)

	if h == "" {
		return HostMatch{Kind: HostRouteUnknown}
	}
	if reg != "" && h == reg {
		return HostMatch{Kind: HostRouteHub, Host: h}
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
		return HostMatch{Kind: HostRouteHub, Host: h}
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
var ErrTooManyReconnectWaiters = errors.New("hub server: too many concurrent reconnect_grace waiters")

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

// PostgresNotificationHandlers holds callbacks for PostgreSQL LISTEN/NOTIFY channels.
type PostgresNotificationHandlers struct {
	OnEntry      func(name string)
	OnTunnel     func(name string)
	OnCatalog    func(name string)
	OnDelegation func(payload string)
}

// ListenPostgresNotifications connects to `dsn` and listens on `registry_entries` and
// `registry_tunnels`, invoking `onEntry(payload)` and `onTunnel(payload)` when notifications arrive.
// It automatically reconnects with backoff until `ctx` is canceled.
func ListenPostgresNotifications(ctx context.Context, dsn string, onEntry func(name string), onTunnel func(name string)) {
	ListenPostgresAllNotifications(ctx, dsn, onEntry, onTunnel, nil)
}

// ListenPostgresAllNotifications connects to `dsn` and listens on `registry_entries`,
// `registry_tunnels`, and `registry_catalog`.
func ListenPostgresAllNotifications(
	ctx context.Context,
	dsn string,
	onEntry func(name string),
	onTunnel func(name string),
	onCatalog func(name string),
) {
	ListenPostgresChannels(ctx, dsn, PostgresNotificationHandlers{
		OnEntry:   onEntry,
		OnTunnel:  onTunnel,
		OnCatalog: onCatalog,
	})
}

// ListenPostgresChannels connects to `dsn` and listens on `registry_entries`,
// `registry_tunnels`, `registry_catalog`, and `registry_delegations`.
// It automatically reconnects with backoff until `ctx` is canceled.
func ListenPostgresChannels(ctx context.Context, dsn string, h PostgresNotificationHandlers) {
	listenPostgresChannels(ctx, dsn, h, nil)
}

func listenPostgresChannels(
	ctx context.Context,
	dsn string,
	h PostgresNotificationHandlers,
	onFirstAttempt func(),
) {
	if strings.TrimSpace(dsn) == "" {
		if onFirstAttempt != nil {
			onFirstAttempt()
		}
		return
	}
	for {
		if ctx.Err() != nil {
			if onFirstAttempt != nil {
				onFirstAttempt()
			}
			return
		}
		_ = runPostgresListenSession(ctx, dsn, h, onFirstAttempt)
		if onFirstAttempt != nil {
			onFirstAttempt()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func runPostgresListenSession(
	ctx context.Context,
	dsn string,
	h PostgresNotificationHandlers,
	onReady func(),
) error {
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
	if _, err := conn.Exec(ctx, `LISTEN registry_catalog`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `LISTEN registry_delegations`); err != nil {
		return err
	}
	if onReady != nil {
		onReady()
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		rawPayload := strings.TrimSpace(notification.Payload)
		payload := strings.ToLower(rawPayload)
		switch notification.Channel {
		case "registry_entries":
			if h.OnEntry != nil {
				h.OnEntry(payload)
			}
		case "registry_tunnels":
			if h.OnTunnel != nil {
				h.OnTunnel(payload)
			}
		case "registry_catalog":
			if h.OnCatalog != nil {
				h.OnCatalog(payload)
			}
		case "registry_delegations":
			if h.OnDelegation != nil {
				h.OnDelegation(rawPayload)
			}
		}
	}
}

// DefaultCatalogCacheTTL is the background refresh interval for CatalogCache (3s).
const DefaultCatalogCacheTTL = 3 * time.Second

// CatalogSnapshot is an immutable in-memory snapshot of all listed (`listed = true`),
// non-deleted catalog entries and their scopes. Internal placement fields (`backend_url`,
// `internal_ip`, `gce_instance_id`) and unlisted entries are never stored here.
type CatalogSnapshot struct {
	Entries  []*store.EntryRow
	ByRepo   map[string][]int
	BuiltAt  time.Time
	StoreGen uint64
}

// CatalogCache serves `GET /api/directory` and `GET /api/route` from an in-memory
// snapshot refreshed by `LISTEN registry_catalog` and a 3s background poll, executing
// zero SQL queries on the request hot path.
type CatalogCache struct {
	st             *store.Store
	tunnels        *TunnelCache
	instanceDomain string
	ttl            time.Duration
	nowFn          func() time.Time

	reloadMu    sync.Mutex
	snap        atomic.Pointer[CatalogSnapshot]
	dirty       atomic.Bool
	reloadCount atomic.Uint64
	wakeCh      chan struct{}
}

// NewCatalogCache creates a new CatalogCache backed by `st` and `tunnels`.
func NewCatalogCache(st *store.Store, tunnels *TunnelCache, instanceDomain string, ttl time.Duration) *CatalogCache {
	if ttl <= 0 {
		ttl = DefaultCatalogCacheTTL
	}
	c := &CatalogCache{
		st:             st,
		tunnels:        tunnels,
		instanceDomain: instanceDomain,
		ttl:            ttl,
		nowFn:          time.Now,
		wakeCh:         make(chan struct{}, 1),
	}
	c.dirty.Store(true)
	return c
}

// SetNow overrides the clock used by CatalogCache (for deterministic tests).
func (c *CatalogCache) SetNow(fn func() time.Time) {
	if c != nil && fn != nil {
		c.nowFn = fn
	}
}

func (c *CatalogCache) now() time.Time {
	if c != nil && c.nowFn != nil {
		return c.nowFn().UTC()
	}
	return time.Now().UTC()
}

// ReloadCount returns the total number of SQL snapshot reloads executed by this cache.
func (c *CatalogCache) ReloadCount() uint64 {
	if c == nil {
		return 0
	}
	return c.reloadCount.Load()
}

// Invalidate marks the catalog snapshot dirty and wakes the background refresh loop.
func (c *CatalogCache) Invalidate() {
	if c == nil {
		return
	}
	c.dirty.Store(true)
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

// StartLoop periodically refreshes the catalog snapshot every `ttl` (default 3s)
// or immediately when woken by `Invalidate()` (`LISTEN registry_catalog`).
func (c *CatalogCache) StartLoop(ctx context.Context) {
	if c == nil || c.st == nil {
		return
	}
	_ = c.Reload(ctx)
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wakeCh:
			_ = c.Reload(ctx)
		case <-ticker.C:
			_ = c.Reload(ctx)
		}
	}
}

// Reload fetches all `listed = true` entries and their scopes from PostgreSQL and
// atomically swaps the in-memory `CatalogSnapshot`.
func (c *CatalogCache) Reload(ctx context.Context) error {
	if c == nil || c.st == nil {
		return nil
	}
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()

	genBefore := c.st.CatalogGeneration()
	entries, err := c.st.ListCatalogEntries(ctx)
	if err != nil {
		return err
	}
	c.reloadCount.Add(1)

	byRepo := make(map[string][]int)
	for idx, e := range entries {
		// Defensive scrub: never retain backend_url or internal_ip in CatalogCache.
		e.BackendURL = ""
		e.InternalIP = ""
		e.GCEInstanceID = ""
		seenRepo := make(map[string]bool)
		for _, sc := range store.CombinedAPIScopes(e.Scopes) {
			if !sc.Active {
				continue
			}
			r := strings.ToLower(strings.TrimSpace(sc.Repo))
			if r != "" && !seenRepo[r] {
				seenRepo[r] = true
				byRepo[r] = append(byRepo[r], idx)
			}
		}
	}

	snap := &CatalogSnapshot{
		Entries:  entries,
		ByRepo:   byRepo,
		BuiltAt:  c.now(),
		StoreGen: genBefore,
	}
	c.snap.Store(snap)
	if c.st.CatalogGeneration() == genBefore {
		c.dirty.Store(false)
	}
	return nil
}

// Snapshot returns the current in-memory CatalogSnapshot, loading it if needed.
func (c *CatalogCache) Snapshot(ctx context.Context) (*CatalogSnapshot, error) {
	if c == nil {
		return &CatalogSnapshot{ByRepo: map[string][]int{}}, nil
	}
	snap := c.snap.Load()
	if snap == nil || c.dirty.Load() || (c.st != nil && snap.StoreGen != c.st.CatalogGeneration()) {
		if err := c.Reload(ctx); err != nil {
			if snap != nil {
				return snap, nil
			}
			return nil, err
		}
		snap = c.snap.Load()
	}
	return snap, nil
}

// DeriveLiveStatusAndAge computes the live `status` and `heartbeat_age_seconds`
// for an entry against the current clock and `tunnelCache`.
func (c *CatalogCache) DeriveLiveStatusAndAge(e *store.EntryRow) (api.EntryStatus, *int) {
	if c == nil {
		return api.StatusOffline, nil
	}
	return c.deriveLiveStatusAndAge(e, c.now())
}

func (c *CatalogCache) deriveLiveStatusAndAge(e *store.EntryRow, now time.Time) (api.EntryStatus, *int) {
	if e == nil || e.OwnerDisabled {
		return api.StatusOffline, nil
	}
	th := store.DefaultThresholds()
	if c.st != nil {
		th = c.st.Thresholds()
	}

	var lastSeen *time.Time
	if e.LastHeartbeatAt != nil {
		t := *e.LastHeartbeatAt
		lastSeen = &t
	}

	var hasTunnel bool
	var tunnelLastSeen time.Time
	if e.Kind == api.KindConnected {
		if c.tunnels != nil {
			if tc, ok := c.tunnels.Get(e.Name); ok && tc.HasActiveSession(now, th.ReplicaTTL) {
				hasTunnel = true
				tunnelLastSeen = tc.LastSeenAt
			}
		}
		if !hasTunnel && e.Tunnel != nil && now.Sub(e.Tunnel.ReplicaLastSeen) <= th.ReplicaTTL {
			hasTunnel = true
			tunnelLastSeen = e.Tunnel.LastSeenAt
		}
		if hasTunnel && (lastSeen == nil || tunnelLastSeen.After(*lastSeen)) {
			t := tunnelLastSeen
			lastSeen = &t
		}
	}

	var ageSec *int
	if lastSeen != nil {
		secs := int(now.Sub(*lastSeen).Seconds())
		if secs < 0 {
			secs = 0
		}
		ageSec = &secs
	}

	if e.LifecycleState != api.StateRunning && e.LifecycleState != "ACTIVE" {
		return api.StatusOffline, ageSec
	}

	var age time.Duration
	switch e.Kind {
	case api.KindInstance:
		if e.LastHeartbeatAt == nil {
			return api.StatusOffline, ageSec
		}
		age = now.Sub(*e.LastHeartbeatAt)
	case api.KindConnected:
		if !hasTunnel {
			return api.StatusOffline, ageSec
		}
		age = now.Sub(tunnelLastSeen)
		if e.LastHeartbeatAt != nil {
			if hbAge := now.Sub(*e.LastHeartbeatAt); hbAge < age {
				age = hbAge
			}
		}
	default:
		return api.StatusOffline, ageSec
	}

	if age > th.OfflineAfter {
		return api.StatusOffline, ageSec
	}
	if e.HealthState == api.HealthDegraded || e.HealthState == api.HealthUnhealthy {
		return api.StatusDegraded, ageSec
	}
	if age <= th.DegradedAfter {
		return api.StatusOnline, ageSec
	}
	return api.StatusDegraded, ageSec
}

// Directory evaluates `GET /api/directory` against the in-memory `CatalogSnapshot`.
func (c *CatalogCache) Directory(ctx context.Context, q api.DirectoryQuery) (*api.DirectoryResponse, error) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	searchQ := strings.ToLower(strings.TrimSpace(q.Query))
	filterRepo := strings.ToLower(strings.TrimSpace(q.Repo))
	if filterRepo != "" {
		if norm, err := api.NormalizeRepo(filterRepo); err == nil {
			filterRepo = norm
		}
	}
	filterTag := strings.ToLower(strings.TrimSpace(q.Tag))
	filterOwner := strings.ToLower(strings.TrimSpace(q.Owner))

	var out []api.DirectoryEntry
	for _, e := range snap.Entries {
		if !e.Listed || e.DeletedAt != nil || e.LifecycleState == api.StateDeleted {
			continue
		}
		status, ageSec := c.deriveLiveStatusAndAge(e, now)
		if q.Status != "" && status != q.Status {
			continue
		}
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if q.HostKind != "" && api.HostKind(e.HostKind) != q.HostKind {
			continue
		}
		if filterOwner != "" && !strings.EqualFold(e.OwnerEmail, filterOwner) && !strings.EqualFold(e.OwnerGroup, filterOwner) {
			continue
		}
		if filterTag != "" {
			matchedTag := false
			for _, t := range e.Tags {
				if strings.EqualFold(t, filterTag) {
					matchedTag = true
					break
				}
			}
			if !matchedTag {
				continue
			}
		}
		scopes := store.CombinedAPIScopes(e.Scopes)
		if filterRepo != "" {
			matchedRepo := false
			for _, sc := range scopes {
				if sc.Active && strings.EqualFold(sc.Repo, filterRepo) {
					matchedRepo = true
					break
				}
			}
			if !matchedRepo {
				continue
			}
		}
		if searchQ != "" {
			matchedQ := strings.Contains(strings.ToLower(e.Name), searchQ) ||
				strings.Contains(strings.ToLower(e.DisplayName), searchQ) ||
				strings.Contains(strings.ToLower(e.Purpose), searchQ) ||
				strings.Contains(strings.ToLower(e.OwnerEmail), searchQ) ||
				strings.Contains(strings.ToLower(e.OwnerGroup), searchQ)
			if !matchedQ {
				for _, t := range e.Tags {
					if strings.Contains(strings.ToLower(t), searchQ) {
						matchedQ = true
						break
					}
				}
			}
			if !matchedQ {
				for _, sc := range scopes {
					if sc.Active && (strings.Contains(strings.ToLower(sc.Repo), searchQ) || strings.Contains(strings.ToLower(sc.PathPrefix), searchQ)) {
						matchedQ = true
						break
					}
				}
			}
			if !matchedQ {
				continue
			}
		}

		urlStr := ""
		if c.instanceDomain != "" {
			urlStr = fmt.Sprintf("https://%s.%s", e.Name, c.instanceDomain)
		}
		out = append(out, api.DirectoryEntry{
			Name:                e.Name,
			URL:                 urlStr,
			Kind:                e.Kind,
			State:               e.LifecycleState,
			Status:              status,
			HeartbeatAgeSeconds: ageSec,
			DisplayName:         e.DisplayName,
			Purpose:             e.Purpose,
			Owner:               e.OwnerEmail,
			OwnerGroup:          e.OwnerGroup,
			Tags:                e.Tags,
			GrainVersion:        e.GrainVersion,
			GrainCommit:         e.GrainCommit,
			HostKind:            api.HostKind(e.HostKind),
			Scopes:              scopes,
			ActiveTasks:         e.ActiveTasks,
			AwaitingReplyTasks:  e.AwaitingReplyTasks,
			QueueDepth:          e.PendingInbox,
			UpdatedAt:           e.UpdatedAt,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		ri := catalogStatusRank(out[i].Status)
		rj := catalogStatusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})

	if q.Cursor != "" {
		idx := 0
		for idx < len(out) && out[idx].Name <= q.Cursor {
			idx++
		}
		out = out[idx:]
	}
	var nextCursor string
	if q.Limit > 0 && len(out) > q.Limit {
		nextCursor = out[q.Limit-1].Name
		out = out[:q.Limit]
	}
	if out == nil {
		out = []api.DirectoryEntry{}
	}
	return &api.DirectoryResponse{
		Entries:    out,
		NextCursor: nextCursor,
	}, nil
}

// Route evaluates `GET /api/route` against the in-memory `CatalogSnapshot`.
// `rawIncludeOffline` is the raw query parameter ("true", "false", or "").
// When `rawIncludeOffline` is "", offline secondary entries are excluded while
// offline primary entries are retained in rank 1 with `"status": "offline"`.
func (c *CatalogCache) Route(ctx context.Context, repo, filePath, rawIncludeOffline string) (*api.RouteResponse, error) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if normRepo, err := api.NormalizeRepo(repo); err == nil {
		repo = normRepo
	} else {
		repo = strings.ToLower(strings.TrimSpace(repo))
	}
	normPath, err := api.NormalizePathPrefix(filePath)
	if err != nil {
		return nil, err
	}

	now := c.now()
	idxs := snap.ByRepo[repo]
	var matches []api.RouteCandidate
	for _, idx := range idxs {
		if idx < 0 || idx >= len(snap.Entries) {
			continue
		}
		e := snap.Entries[idx]
		if !e.Listed || e.DeletedAt != nil || e.LifecycleState == api.StateDeleted {
			continue
		}
		combined := store.CombinedAPIScopes(e.Scopes)
		bestLen := -1
		var bestScope *api.Scope
		for i := range combined {
			sc := &combined[i]
			if !sc.Active {
				continue
			}
			if !strings.EqualFold(sc.Repo, repo) {
				continue
			}
			matched, spec := api.PathMatchesPrefix(normPath, sc.PathPrefix)
			if !matched {
				continue
			}
			effRole := sc.EffectiveRole
			if effRole == "" {
				effRole = sc.Role
			}
			bestRole := api.RoleSecondary
			if bestScope != nil {
				bestRole = bestScope.EffectiveRole
				if bestRole == "" {
					bestRole = bestScope.Role
				}
			}
			if spec > bestLen || (spec == bestLen && effRole == api.RolePrimary && bestRole != api.RolePrimary) {
				bestLen = spec
				bestScope = sc
			}
		}
		if bestScope == nil {
			continue
		}
		effRole := bestScope.EffectiveRole
		if effRole == "" {
			effRole = bestScope.Role
		}
		status, ageSec := c.deriveLiveStatusAndAge(e, now)
		if status == api.StatusOffline {
			switch strings.ToLower(strings.TrimSpace(rawIncludeOffline)) {
			case "true", "1":
				// Include all offline entries when explicitly requested.
			case "false", "0":
				// Exclude all offline entries (including primary) when include_offline=false.
				continue
			default:
				// Default: exclude offline secondaries, but retain offline primaries.
				if effRole != api.RolePrimary {
					continue
				}
			}
		}

		urlStr := ""
		if c.instanceDomain != "" {
			urlStr = fmt.Sprintf("https://%s.%s", e.Name, c.instanceDomain)
		}
		matches = append(matches, api.RouteCandidate{
			Name:                e.Name,
			URL:                 urlStr,
			Kind:                e.Kind,
			State:               e.LifecycleState,
			Status:              status,
			HeartbeatAgeSeconds: ageSec,
			DisplayName:         e.DisplayName,
			Purpose:             e.Purpose,
			Owner:               e.OwnerEmail,
			OwnerGroup:          e.OwnerGroup,
			ActiveTasks:         e.ActiveTasks,
			AwaitingReplyTasks:  e.AwaitingReplyTasks,
			MatchedScope: api.MatchedScope{
				Repo:        bestScope.Repo,
				PathPrefix:  bestScope.PathPrefix,
				Role:        effRole,
				Source:      bestScope.Source,
				Specificity: bestLen,
			},
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		iPrimary := matches[i].MatchedScope.Role == api.RolePrimary
		jPrimary := matches[j].MatchedScope.Role == api.RolePrimary
		if iPrimary != jPrimary {
			return iPrimary
		}
		if matches[i].MatchedScope.Specificity != matches[j].MatchedScope.Specificity {
			return matches[i].MatchedScope.Specificity > matches[j].MatchedScope.Specificity
		}
		iRank := catalogStatusRank(matches[i].Status)
		jRank := catalogStatusRank(matches[j].Status)
		if iRank != jRank {
			return iRank < jRank
		}
		if matches[i].ActiveTasks != matches[j].ActiveTasks {
			return matches[i].ActiveTasks < matches[j].ActiveTasks
		}
		return matches[i].Name < matches[j].Name
	})

	res := &api.RouteResponse{
		Repo:       repo,
		Path:       normPath,
		Candidates: matches,
	}
	if res.Candidates == nil {
		res.Candidates = []api.RouteCandidate{}
	}
	for i := range matches {
		if matches[i].MatchedScope.Role == api.RolePrimary {
			m := matches[i]
			res.Primary = &m
			break
		}
	}
	return res, nil
}

func catalogStatusRank(s api.EntryStatus) int {
	switch s {
	case api.StatusOnline:
		return 0
	case api.StatusDegraded:
		return 1
	default:
		return 2
	}
}
