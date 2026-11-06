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
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

const (
	DefaultMaxConcurrentStreams   = 256
	DefaultMaxBufferedBytes       = 16 << 20 // 16 MiB
	DefaultMaxPeerStreamResets    = 16
	DefaultMuxPingInterval        = 15 * time.Second
	DefaultMuxPingTimeout         = 15 * time.Second
	DefaultStreamIdleTimeout      = 1 * time.Hour
	DefaultTunnelTouchInterval    = 30 * time.Second
	DefaultSupersededDrainTimeout = 2 * time.Minute
	DefaultSIGTERMDrainTimeout    = 30 * time.Second
	DefaultSIGTERMStaggerMax      = 5 * time.Second
)

// TunnelOptions configures outbound tunnel parameters on a Router.
type TunnelOptions struct {
	MaxConcurrentStreams   int
	MaxBufferedBytes       int
	PingInterval           time.Duration
	PingTimeout            time.Duration
	ResponseHeaderTimeout  time.Duration
	MaxResponseHeaderBytes int64
	StreamIdleTimeout      time.Duration
	TouchInterval          time.Duration
	DrainTimeout           time.Duration
	DrainStaggerMax        time.Duration
	OnPeerStreamRejected   func(entryName string, streamID uint32)
	OnActiveTunnelsChanged func(count int64)
}

func (o *TunnelOptions) withDefaults() TunnelOptions {
	out := *o
	if out.MaxConcurrentStreams <= 0 {
		out.MaxConcurrentStreams = DefaultMaxConcurrentStreams
	}
	if out.MaxBufferedBytes <= 0 {
		out.MaxBufferedBytes = DefaultMaxBufferedBytes
	}
	if out.PingInterval <= 0 {
		out.PingInterval = DefaultMuxPingInterval
	}
	if out.PingTimeout <= 0 {
		out.PingTimeout = DefaultMuxPingTimeout
	}
	if out.ResponseHeaderTimeout <= 0 {
		out.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if out.MaxResponseHeaderBytes <= 0 {
		out.MaxResponseHeaderBytes = DefaultMaxResponseHeaderBytes
	}
	if out.StreamIdleTimeout <= 0 {
		out.StreamIdleTimeout = DefaultStreamIdleTimeout
	}
	if out.TouchInterval <= 0 {
		out.TouchInterval = DefaultTunnelTouchInterval
	}
	if out.DrainTimeout <= 0 {
		out.DrainTimeout = DefaultSIGTERMDrainTimeout
	}
	if out.DrainStaggerMax < 0 {
		out.DrainStaggerMax = 0
	}
	return out
}

// ActiveTunnel represents an established outbound `/api/entries/{name}/connect` session
// held in memory on this registry replica.
type ActiveTunnel struct {
	EntryID            string
	Name               string
	SessionEpoch       int64
	CredID             string
	OwnerEmail         string
	OwnerGroup         string
	ConnectedPrincipal string
	AuthMode           string // "token" or "identity"
	ConnectedAt        time.Time

	conn      net.Conn
	mux       *mux.Mux
	transport *http.Transport

	maxConcurrentStreams int
	streamIdleTimeout    time.Duration
	activeStreams        atomic.Int64
	superseded           atomic.Bool
	closed               atomic.Bool
	closeOnce            sync.Once
	closeReasonMu        sync.Mutex
	closeReason          string
}

// NewActiveTunnel constructs an ActiveTunnel over an upgraded WebSocket `conn`.
func NewActiveTunnel(
	conn net.Conn,
	entry *store.EntryRow,
	sessionEpoch int64,
	credID string,
	authMode string,
	opts TunnelOptions,
) *ActiveTunnel {
	opts = opts.withDefaults()
	t := &ActiveTunnel{
		EntryID:              entry.ID,
		Name:                 strings.ToLower(strings.TrimSpace(entry.Name)),
		SessionEpoch:         sessionEpoch,
		CredID:               credID,
		OwnerEmail:           entry.OwnerEmail,
		OwnerGroup:           entry.OwnerGroup,
		ConnectedPrincipal:   entry.ConnectedPrincipal,
		AuthMode:             authMode,
		ConnectedAt:          time.Now().UTC(),
		conn:                 conn,
		maxConcurrentStreams: opts.MaxConcurrentStreams,
		streamIdleTimeout:    opts.StreamIdleTimeout,
	}

	m := mux.NewMux(conn, mux.Config{
		IsServer:                     true,
		PingInterval:                 opts.PingInterval,
		PingTimeout:                  opts.PingTimeout,
		MaxStreams:                   opts.MaxConcurrentStreams,
		MaxPeerInitiatedStreams:      0,
		DisallowPeerInitiatedStreams: true,
		MaxPeerStreamViolations:      DefaultMaxPeerStreamResets,
		OnPeerStreamRejected: func(streamID uint32) {
			if opts.OnPeerStreamRejected != nil {
				opts.OnPeerStreamRejected(t.Name, streamID)
			}
		},
		MaxBufferedBytes: opts.MaxBufferedBytes,
	})
	t.mux = m

	t.transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return t.OpenStream(ctx)
		},
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  opts.ResponseHeaderTimeout,
		MaxResponseHeaderBytes: opts.MaxResponseHeaderBytes,
	}
	return t
}

// Mux returns the underlying `*mux.Mux`.
func (t *ActiveTunnel) Mux() *mux.Mux {
	return t.mux
}

// Transport returns the per-tunnel `*http.Transport` (`DisableKeepAlives: true`).
func (t *ActiveTunnel) Transport() http.RoundTripper {
	return t.transport
}

// ActiveStreams returns the number of currently open streams on this tunnel.
func (t *ActiveTunnel) ActiveStreams() int64 {
	return t.activeStreams.Load()
}

// IsSuperseded reports whether a newer tunnel session has superseded this one.
func (t *ActiveTunnel) IsSuperseded() bool {
	return t.superseded.Load()
}

// IsClosed reports whether this tunnel session has been closed.
func (t *ActiveTunnel) IsClosed() bool {
	return t.closed.Load()
}

// Done returns a channel closed when the underlying mux session terminates.
func (t *ActiveTunnel) Done() <-chan struct{} {
	return t.mux.Done()
}

// OpenStream opens a newregistry-initiated odd-numbered `mux.Stream` on the tunnel,
// enforcing the 256 concurrent stream cap (`ErrTooManyStreams`) and per-stream idle timeout.
func (t *ActiveTunnel) OpenStream(ctx context.Context) (net.Conn, error) {
	if t.closed.Load() || t.superseded.Load() {
		return nil, mux.ErrSessionClosed
	}
	for {
		cur := t.activeStreams.Load()
		if int(cur) >= t.maxConcurrentStreams {
			return nil, ErrTooManyStreams
		}
		if t.activeStreams.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	st, err := t.mux.OpenStream(ctx)
	if err != nil {
		t.activeStreams.Add(-1)
		return nil, err
	}
	return newTrackedStreamConn(st, t.streamIdleTimeout, func() {
		t.activeStreams.Add(-1)
	}), nil
}

// CloseWithReason immediately closes the tunnel session (aborting all open streams)
// and records `reason` if not already set.
func (t *ActiveTunnel) CloseWithReason(reason string) error {
	t.setCloseReason(reason)
	var err error
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		err = t.mux.Close()
		_ = t.conn.Close()
	})
	return err
}

// DrainAndClose stops routing new streams to `t`, waits up to `timeout` for in-flight
// streams to complete, and then closes the tunnel with `reason`.
func (t *ActiveTunnel) DrainAndClose(timeout time.Duration, reason string) {
	t.superseded.Store(true)
	t.setCloseReason(reason)
	if timeout > 0 && t.ActiveStreams() > 0 {
		deadline := time.Now().Add(timeout)
		for t.ActiveStreams() > 0 && time.Now().Before(deadline) {
			select {
			case <-t.mux.Done():
				break
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	_ = t.CloseWithReason(reason)
}

func (t *ActiveTunnel) setCloseReason(reason string) {
	if reason == "" {
		return
	}
	t.closeReasonMu.Lock()
	if t.closeReason == "" {
		t.closeReason = reason
	}
	t.closeReasonMu.Unlock()
}

// CloseReason returns the recorded disconnect reason.
func (t *ActiveTunnel) CloseReason() string {
	t.closeReasonMu.Lock()
	r := t.closeReason
	t.closeReasonMu.Unlock()
	if r != "" {
		return r
	}
	return "disconnected"
}

type trackedStreamConn struct {
	*mux.Stream
	idleTimeout  time.Duration
	onClose      func()
	closeOnce    sync.Once
	lastActiveNs atomic.Int64
	stopIdle     chan struct{}
}

func newTrackedStreamConn(st *mux.Stream, idleTimeout time.Duration, onClose func()) *trackedStreamConn {
	c := &trackedStreamConn{
		Stream:      st,
		idleTimeout: idleTimeout,
		onClose:     onClose,
		stopIdle:    make(chan struct{}),
	}
	c.lastActiveNs.Store(time.Now().UnixNano())
	if idleTimeout > 0 && idleTimeout < 10*time.Minute {
		// For short custom idle timeouts (or tests), run a lightweight watchdog.
		go c.idleWatchdog()
	}
	return c
}

func (c *trackedStreamConn) idleWatchdog() {
	checkInterval := c.idleTimeout / 4
	if checkInterval < 10*time.Millisecond {
		checkInterval = 10 * time.Millisecond
	}
	if checkInterval > 30*time.Second {
		checkInterval = 30 * time.Second
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopIdle:
			return
		case <-ticker.C:
			last := time.Unix(0, c.lastActiveNs.Load())
			if time.Since(last) >= c.idleTimeout {
				_ = c.Stream.Reset()
				_ = c.Close()
				return
			}
		}
	}
}

func (c *trackedStreamConn) Read(p []byte) (int, error) {
	if c.idleTimeout >= 10*time.Minute {
		_ = c.Stream.SetReadDeadline(time.Now().Add(c.idleTimeout))
	}
	n, err := c.Stream.Read(p)
	if n > 0 {
		c.lastActiveNs.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *trackedStreamConn) Write(p []byte) (int, error) {
	n, err := c.Stream.Write(p)
	if n > 0 {
		c.lastActiveNs.Store(time.Now().UnixNano())
	}
	return n, err
}

func (c *trackedStreamConn) CloseWrite() error {
	c.lastActiveNs.Store(time.Now().UnixNano())
	return c.Stream.CloseWrite()
}

func (c *trackedStreamConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.stopIdle)
		err = c.Stream.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

// ExtractRegistryToken extracts a `grc_…` bearer credential from `X-Grain-Registry-Token`
// or `Authorization: Bearer grc_…`.
func ExtractRegistryToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if tok := strings.TrimSpace(r.Header.Get(api.HeaderRegistryToken)); tok != "" {
		return tok
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		cand := strings.TrimSpace(auth[7:])
		if strings.HasPrefix(cand, "grc_") {
			return cand
		}
	}
	return ""
}

// HandleConnect handles `GET /api/entries/{name}/connect` on both `grain-registry-public`
// (`publicPort == true`, requiring `grc_…` token auth and ignoring `FrontIdentity` headers)
// and `grain-registry` (`publicPort == false`, supporting `owner`/`owner_group`/`connected_principal`
// via `FrontIdentity` as well as `grc_…` token auth when `X-Grain-Auth: token` is sent).
func (rt *Router) HandleConnect(w http.ResponseWriter, r *http.Request, name string, publicPort bool) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET is required for /connect")
		return
	}
	name = strings.ToLower(strings.TrimSpace(name))
	ip := ClientIP(r)

	// 1. Per-IP connect/heartbeat rate limit (`30/min`, burst 10).
	if ok, retryAfter := rt.limiters.PublicPerIP.Allow(ip); !ok {
		WriteRateLimited(w, retryAfter, "too many connect attempts from this IP; retry later")
		return
	}

	// 2. Validate <name> syntax before entry-level rate limit or DB query.
	if err := api.ValidateEntryName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_name", err.Error())
		return
	}

	// 3. Per-entry `/connect` rate limit (`12/min`, burst 6).
	if ok, retryAfter := rt.limiters.ConnectPerEntry.Allow(name); !ok {
		WriteRateLimited(w, retryAfter, "too many connect handshakes for this entry; retry later")
		return
	}

	// 4. Reject new tunnel handshakes while this replica is draining on SIGTERM.
	if rt.IsDraining() {
		w.Header().Set("Retry-After", "2")
		writeJSONError(w, http.StatusServiceUnavailable, "draining", "registry replica is draining; reconnect to another replica")
		return
	}

	ctx := r.Context()
	var (
		entry    *store.EntryRow
		credID   string
		authMode string
	)

	wantsTokenAuth := publicPort ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get(api.HeaderAuth)), api.AuthModeToken)

	if wantsTokenAuth {
		// On the public listener, require `X-Grain-Auth: token` (or a `grc_` token) and
		// never inspect `FrontIdentity` headers.
		if publicPort && !strings.EqualFold(strings.TrimSpace(r.Header.Get(api.HeaderAuth)), api.AuthModeToken) &&
			ExtractRegistryToken(r) == "" {
			rt.recordFailedAuthOrWrite429(w, ip, "X-Grain-Auth: token and a valid grc_ credential are required on the public connect endpoint")
			return
		}
		tok := ExtractRegistryToken(r)
		if tok == "" || rt.store == nil {
			rt.recordFailedAuthOrWrite429(w, ip, "missing or invalid entry credential")
			return
		}
		authedEntry, cid, err := rt.store.AuthenticateCredentialWithID(ctx, tok)
		if err != nil || !strings.EqualFold(authedEntry.Name, name) {
			rt.recordFailedAuthOrWrite429(w, ip, "invalid, revoked, or mismatched entry credential")
			return
		}
		entry = authedEntry
		credID = cid
		authMode = "token"
	} else {
		// Authenticate via FrontIdentity (`Admitter`).
		if rt.admitter == nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		callerEmail, authErr := rt.authenticateConnectIdentity(r)
		if authErr != nil {
			if webauthErrForbidden(authErr) {
				writeJSONError(w, http.StatusForbidden, "forbidden", "access denied")
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}

		if rt.store == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "entry not found")
			return
		}
		e, err := rt.store.GetEntryByName(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "entry not found")
			return
		}
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if !rt.canConnectAsIdentity(ctx, callerEmail, e) {
			writeJSONError(w, http.StatusForbidden, "forbidden", "caller is not authorized to connect this entry (must be owner, owner_group member, or connected_principal)")
			return
		}
		// If X-Grain-Registry-Token was also supplied alongside FrontIdentity, validate it too.
		if tok := ExtractRegistryToken(r); tok != "" {
			authedEntry, cid, err := rt.store.AuthenticateCredentialWithID(ctx, tok)
			if err != nil || authedEntry.ID != e.ID {
				rt.recordFailedAuthOrWrite429(w, ip, "invalid or mismatched entry credential")
				return
			}
			credID = cid
		}
		entry = e
		authMode = "identity"
	}

	// 5. Verify entry kind, owner status, and lifecycle state.
	if entry.OwnerDisabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "entry owner account is disabled")
		return
	}
	if entry.Kind != api.KindConnected {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"connected\" entries may establish an outbound tunnel")
		return
	}
	if entry.LifecycleState != api.StateRunning && entry.LifecycleState != "ACTIVE" {
		writeJSONError(w, http.StatusConflict, "entry_not_running", "entry is not in running state")
		return
	}

	// 6. Verify protocol version (`X-Grain-Connect-Protocol: 1` -> 426 if unsupported)
	// and entry binding (`X-Grain-Entry: <name>` -> 400 if mismatched).
	if strings.TrimSpace(r.Header.Get(api.HeaderConnectProtocol)) != api.ConnectProtocolVersion {
		w.Header().Set(api.HeaderConnectProtocol, api.ConnectProtocolVersion)
		writeJSONError(w, http.StatusUpgradeRequired, "upgrade_required", "unsupported or missing X-Grain-Connect-Protocol (expected \"1\")")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get(api.HeaderEntry)), name) {
		writeJSONError(w, http.StatusBadRequest, "entry_mismatch", "X-Grain-Entry header must match entry name in URL path")
		return
	}
	if !IsUpgradeRequest(r) {
		writeJSONError(w, http.StatusBadRequest, "upgrade_required", "WebSocket upgrade headers required")
		return
	}

	// 7. Acquire fenced `session_epoch` in `registry.tunnel_sessions` (+ NOTIFY registry_tunnels).
	sessionEpoch := rt.nextLocalEpoch.Add(1)
	if rt.store != nil {
		_ = rt.store.UpsertReplica(ctx, rt.replicaID, rt.PeerAddr())
		ts, err := rt.store.AcquireTunnelSessionWithCred(ctx, entry.ID, rt.replicaID, r.RemoteAddr, credID)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		sessionEpoch = ts.SessionEpoch
	}

	// 8. Complete 101 Switching Protocols upgrade with X-Grain-Connect-Protocol and X-Grain-Tunnel-Session.
	sessionIDStr := strconv.FormatInt(sessionEpoch, 10)
	wsConn, err := wsupgrade.Upgrade(w, r, http.Header{
		api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
		api.HeaderTunnelSession:   []string{sessionIDStr},
	})
	if err != nil {
		if rt.store != nil {
			_, _ = rt.store.ReleaseTunnelSessionWithReason(context.Background(), entry.ID, sessionEpoch, "handshake error")
		}
		return
	}
	_ = wsConn.SetDeadline(time.Time{})

	active := NewActiveTunnel(wsConn, entry, sessionEpoch, credID, authMode, rt.tunnelOpts)
	rt.registerActiveTunnel(active, entry)

	// 9. Block until the tunnel session terminates (keeping the hijacked connection alive
	// and periodically updating `tunnel_sessions.last_seen_at`).
	rt.runActiveTunnelLifecycle(active)
}

func (rt *Router) recordFailedAuthOrWrite429(w http.ResponseWriter, ip, msg string) {
	if ok, retryAfter := rt.limiters.FailedAuthPerIP.Allow(ip); !ok {
		WriteRateLimited(w, retryAfter, "too many failed authentication attempts; retry later")
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "unauthenticated", msg)
}

// GetLocalTunnel returns the active, non-superseded, non-closed tunnel on this replica for `name`.
func (rt *Router) GetLocalTunnel(name string) *ActiveTunnel {
	name = strings.ToLower(strings.TrimSpace(name))
	rt.tunnelsMu.RLock()
	t := rt.activeTunnels[name]
	rt.tunnelsMu.RUnlock()
	if t == nil || t.IsClosed() || t.IsSuperseded() {
		return nil
	}
	return t
}

// ReconcileAllLocalTunnels reconciles every active tunnel currently held on this replica.
func (rt *Router) ReconcileAllLocalTunnels(ctx context.Context) {
	rt.tunnelsMu.RLock()
	names := make([]string, 0, len(rt.activeTunnels))
	for name := range rt.activeTunnels {
		names = append(names, name)
	}
	rt.tunnelsMu.RUnlock()
	for _, name := range names {
		rt.ReconcileLocalTunnel(ctx, name)
	}
}

func (rt *Router) canConnectAsIdentity(ctx context.Context, callerEmail string, entry *store.EntryRow) bool {
	callerEmail = strings.ToLower(strings.TrimSpace(callerEmail))
	if callerEmail == "" || entry == nil {
		return false
	}
	if strings.EqualFold(callerEmail, entry.OwnerEmail) {
		return true
	}
	if entry.ConnectedPrincipal != "" {
		cp := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(entry.ConnectedPrincipal)), "user:"), "serviceaccount:")
		if strings.EqualFold(callerEmail, cp) {
			return true
		}
	}
	if entry.OwnerGroup != "" && rt.groups != nil {
		if ok, err := rt.groups.IsMember(ctx, entry.OwnerGroup, callerEmail); err == nil && ok {
			return true
		}
	}
	return false
}

func (rt *Router) registerActiveTunnel(t *ActiveTunnel, entry *store.EntryRow) {
	rt.tunnelsMu.Lock()
	prev := rt.activeTunnels[t.Name]
	rt.activeTunnels[t.Name] = t
	count := int64(len(rt.activeTunnels))
	rt.tunnelsMu.Unlock()

	if rt.tunnelOpts.OnActiveTunnelsChanged != nil {
		rt.tunnelOpts.OnActiveTunnelsChanged(count)
	}

	if prev != nil && prev != t {
		go prev.DrainAndClose(DefaultSupersededDrainTimeout, "superseded by newer session")
	}

	now := rt.now()
	rt.hostCache.Put(entry)
	rt.tunnelCache.SetActive(TunnelCacheEntry{
		EntryID:         t.EntryID,
		Name:            t.Name,
		SessionEpoch:    t.SessionEpoch,
		ReplicaID:       rt.replicaID,
		PeerAddr:        rt.PeerAddr(),
		ReplicaLastSeen: now,
		ConnectedAt:     t.ConnectedAt,
		LastSeenAt:      now,
		CredID:          t.CredID,
		UpdatedAt:       now,
	})
}

func (rt *Router) runActiveTunnelLifecycle(t *ActiveTunnel) {
	touchInterval := rt.tunnelOpts.TouchInterval
	if touchInterval <= 0 {
		touchInterval = DefaultTunnelTouchInterval
	}
	ticker := time.NewTicker(touchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.Done():
			rt.cleanupActiveTunnel(t)
			return
		case <-rt.stopCh:
			_ = t.CloseWithReason("server shutdown")
			rt.cleanupActiveTunnel(t)
			return
		case <-ticker.C:
			if t.IsSuperseded() {
				continue
			}
			if rt.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := rt.store.TouchTunnelSession(ctx, t.EntryID, t.SessionEpoch, 0)
				cancel()
				if errors.Is(err, store.ErrConflict) {
					go t.DrainAndClose(DefaultSupersededDrainTimeout, "superseded by newer session")
				}
			}
		}
	}
}

func (rt *Router) cleanupActiveTunnel(t *ActiveTunnel) {
	rt.tunnelsMu.Lock()
	cur := rt.activeTunnels[t.Name]
	isCurrent := cur == t
	if isCurrent {
		delete(rt.activeTunnels, t.Name)
	}
	count := int64(len(rt.activeTunnels))
	rt.tunnelsMu.Unlock()

	if rt.tunnelOpts.OnActiveTunnelsChanged != nil {
		rt.tunnelOpts.OnActiveTunnelsChanged(count)
	}

	reason := t.CloseReason()
	now := rt.now()
	if isCurrent {
		rt.tunnelCache.MarkDisconnected(t.Name, t.SessionEpoch, now, reason)
	}
	if rt.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = rt.store.ReleaseTunnelSessionWithReason(ctx, t.EntryID, t.SessionEpoch, reason)
		cancel()
	}
}

// ReconcileLocalTunnel checks whether the local ActiveTunnel for `name` (if any)
// is still authorized and current in PostgreSQL.
// - If the entry was deleted, owner disabled, transferred, `connected_principal` changed,
//   or its `cred_id` was revoked, the live tunnel socket is terminated immediately (aborting open streams).
// - If `session_epoch` advanced to a newer session on another replica, the local tunnel
//   stops receiving new streams, drains in-flight streams, and closes.
func (rt *Router) ReconcileLocalTunnel(ctx context.Context, name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	rt.tunnelsMu.RLock()
	local := rt.activeTunnels[name]
	rt.tunnelsMu.RUnlock()
	if local == nil || rt.store == nil {
		return
	}

	entry, err := rt.store.GetEntryByID(ctx, local.EntryID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = local.CloseWithReason("entry deleted")
		}
		return
	}
	if entry.DeletedAt != nil || entry.LifecycleState == api.StateDeleted || entry.LifecycleState == "DELETED" {
		_ = local.CloseWithReason("entry deleted")
		return
	}
	if entry.OwnerDisabled {
		_ = local.CloseWithReason("owner disabled")
		return
	}
	if !strings.EqualFold(entry.OwnerEmail, local.OwnerEmail) || entry.OwnerGroup != local.OwnerGroup {
		_ = local.CloseWithReason("ownership transferred")
		return
	}
	if entry.ConnectedPrincipal != local.ConnectedPrincipal {
		_ = local.CloseWithReason("connected_principal changed")
		return
	}
	if local.CredID != "" {
		active, err := rt.store.IsCredentialActive(ctx, local.EntryID, local.CredID)
		if err == nil && !active {
			_ = local.CloseWithReason("credential revoked")
			return
		}
	}
	if entry.Tunnel == nil {
		// Tunnel session row was deleted externally (e.g. credential revoked or admin kick).
		reason := entry.LastDisconnectReason
		if reason == "" {
			reason = "session terminated"
		}
		_ = local.CloseWithReason(reason)
		return
	}
	if entry.Tunnel.SessionEpoch > local.SessionEpoch {
		go local.DrainAndClose(DefaultSupersededDrainTimeout, "superseded by newer session")
	}
}

// BeginDrain marks `/healthz` not-ready (`503`), closes idle tunnels staggered across
// `0 .. DrainStaggerMax` (releasing their `tunnel_sessions` rows and emitting `NOTIFY registry_tunnels`),
// and allows tunnels with active streams up to `DrainTimeout` to finish before closing.
func (rt *Router) BeginDrain(ctx context.Context) {
	rt.draining.Store(true)

	rt.tunnelsMu.RLock()
	tunnels := make([]*ActiveTunnel, 0, len(rt.activeTunnels))
	for _, t := range rt.activeTunnels {
		tunnels = append(tunnels, t)
	}
	rt.tunnelsMu.RUnlock()

	if len(tunnels) == 0 {
		return
	}

	staggerMax := rt.tunnelOpts.DrainStaggerMax
	drainTimeout := rt.tunnelOpts.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = DefaultSIGTERMDrainTimeout
	}

	var wg sync.WaitGroup
	for _, t := range tunnels {
		wg.Add(1)
		go func(tun *ActiveTunnel) {
			defer wg.Done()
			if tun.ActiveStreams() == 0 {
				if staggerMax > 0 {
					delay := time.Duration(rand.Int63n(int64(staggerMax) + 1))
					select {
					case <-ctx.Done():
					case <-time.After(delay):
					}
				}
				if tun.ActiveStreams() == 0 {
					_ = tun.CloseWithReason("graceful shutdown")
					return
				}
			}
			tun.DrainAndClose(drainTimeout, "graceful shutdown")
		}(t)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		for _, t := range tunnels {
			_ = t.CloseWithReason("graceful shutdown")
		}
	}
}
