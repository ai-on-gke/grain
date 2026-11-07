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

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

const (
	// HeaderOnBehalfOf names the target human email when a delegating service
	// principal acts on behalf of a signed-in user.
	HeaderOnBehalfOf = "X-Grain-On-Behalf-Of"

	// HeaderSubjectToken optionally carries the human user's ID token when an app
	// requires proof of presence (`require_subject_token`).
	HeaderSubjectToken = "X-Grain-Subject-Token"
)

func writeDelegationRefused(w http.ResponseWriter, reason, msg string) {
	writeJSONStateResponse(w, http.StatusForbidden, struct {
		Error  string `json:"error"`
		Code   string `json:"code"`
		Reason string `json:"reason,omitempty"`
	}{
		Error:  msg,
		Code:   "delegation_refused",
		Reason: reason,
	})
}

// AssertionActor represents the RFC 8693 `act` claim in a delegated registry JWT.
type AssertionActor struct {
	Sub    string `json:"sub"`
	Kind   string `json:"kind,omitempty"`
	App    string `json:"app,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Access string `json:"access,omitempty"`
}

// AssertionClaims holds the identity and target audience claims to sign into
// `X-Grain-Registry-Assertion`.
type AssertionClaims struct {
	Issuer   string
	Audience string
	Subject  string
	Email    string
	Kind     string
	Actor    *AssertionActor
}

// AssertionSigner signs per-request `X-Grain-Registry-Assertion` JWTs and
// replica-to-replica `X-Registry-Peer-Auth` tokens.
type AssertionSigner interface {
	SignAssertion(ctx context.Context, claims AssertionClaims) (string, error)
	SignPeerToken(ctx context.Context, replicaID string, ttl time.Duration) (string, error)
	VerifyPeerToken(ctx context.Context, token string) (string, error)
}

// GroupChecker checks group membership for authorization decisions.
type GroupChecker interface {
	IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error)
}

// DelegationChecker evaluates `X-Grain-On-Behalf-Of` delegation requests from
// service principals.
type DelegationChecker interface {
	AuthorizeOnBehalfOf(ctx context.Context, servicePrincipal, targetUserEmail, targetEntryName string) (*AssertionActor, error)
}

// RouterOptions configures a registry subdomain/tunnel/peer Router.
type RouterOptions struct {
	RegistryHost     string
	InstanceDomain   string
	IAPAudience      string
	ReplicaID        string
	PeerAddr         string
	DatabaseURL      string
	ReplicaHeartbeat time.Duration
	ReplicaTTL       time.Duration
	ReconnectGrace   time.Duration
	CatalogTTL       time.Duration
	TunnelOptions    TunnelOptions

	Store        *store.Store
	Admitter     *webauth.Admitter
	Groups       GroupChecker
	Delegation   DelegationChecker
	Signer       AssertionSigner
	RateLimiters *RateLimiters

	// Optional HTTP transports for testing or custom timeouts.
	InstanceHTTPTransport http.RoundTripper
	PeerHTTPTransport     http.RoundTripper

	// Optional metric hooks.
	OnPeerForward       func()
	OnAdmissionRejected func()
	OnHeartbeat         func()
}

// Router manages subdomain routing (`<name>.<instance-domain>` vs `<registry-host>`),
// `hostCache`, `tunnelCache`, `GET /api/entries/<name>/connect`, `:8082` peer
// forwarding, and reverse proxying for `instance` and `connected` entries.
type Router struct {
	opts                  RouterOptions
	replicaID             string
	peerAddrMu            sync.RWMutex
	peerAddr              string
	store                 *store.Store
	admitter              *webauth.Admitter
	groups                GroupChecker
	delegation            DelegationChecker
	signer                AssertionSigner
	hostCache             *HostCache
	tunnelCache           *TunnelCache
	catalogCache          *CatalogCache
	limiters              *RateLimiters
	tunnelOpts            TunnelOptions
	peerTokens            *PeerTokenCache
	instanceHTTPTransport http.RoundTripper
	peerHTTPTransport     http.RoundTripper

	nowMu sync.RWMutex
	nowFn func() time.Time

	tunnelsMu      sync.RWMutex
	activeTunnels  map[string]*ActiveTunnel
	nextLocalEpoch atomic.Int64

	stopOnce sync.Once
	stopCh   chan struct{}
	draining atomic.Bool
}

// NewRouter constructs a Router from RouterOptions.
func NewRouter(opts RouterOptions) (*Router, error) {
	if opts.Admitter == nil {
		return nil, errors.New("registry router: Admitter is required")
	}
	if opts.Signer == nil {
		return nil, errors.New("registry router: Signer is required")
	}
	if opts.ReplicaHeartbeat <= 0 {
		opts.ReplicaHeartbeat = 5 * time.Second
	}
	if opts.ReplicaTTL <= 0 {
		opts.ReplicaTTL = 15 * time.Second
	}
	if opts.ReconnectGrace <= 0 {
		opts.ReconnectGrace = 2 * time.Second
	}

	replicaID := strings.TrimSpace(opts.ReplicaID)
	if replicaID == "" {
		replicaID = randomReplicaID()
	}
	peerAddr := NormalizePeerAddr(opts.PeerAddr)
	if peerAddr == "" {
		peerAddr = "127.0.0.1:8082"
	}

	limiters := opts.RateLimiters
	if limiters == nil {
		limiters = NewDefaultRateLimiters()
	}

	tunnelCache := NewTunnelCache(opts.Store, opts.ReplicaTTL)
	catalogCache := NewCatalogCache(opts.Store, tunnelCache, opts.InstanceDomain, opts.CatalogTTL)

	rt := &Router{
		opts:                  opts,
		replicaID:             replicaID,
		peerAddr:              peerAddr,
		store:                 opts.Store,
		admitter:              opts.Admitter,
		groups:                opts.Groups,
		delegation:            opts.Delegation,
		signer:                opts.Signer,
		hostCache:             NewHostCache(opts.Store, DefaultHostCacheTTL, DefaultHostCacheStaleGrace),
		tunnelCache:           tunnelCache,
		catalogCache:          catalogCache,
		limiters:              limiters,
		tunnelOpts:            opts.TunnelOptions.withDefaults(),
		peerTokens:            NewPeerTokenCache(opts.Signer, replicaID),
		instanceHTTPTransport: opts.InstanceHTTPTransport,
		peerHTTPTransport:     opts.PeerHTTPTransport,
		nowFn:                 time.Now,
		activeTunnels:         make(map[string]*ActiveTunnel),
		stopCh:                make(chan struct{}),
	}
	return rt, nil
}

func randomReplicaID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "replica-" + hex.EncodeToString(b[:8])
}

// SetNow overrides the clock across Router, HostCache, TunnelCache, CatalogCache, PeerTokenCache, and RateLimiters.
func (rt *Router) SetNow(fn func() time.Time) {
	if fn == nil {
		return
	}
	rt.nowMu.Lock()
	rt.nowFn = fn
	rt.nowMu.Unlock()
	rt.hostCache.SetNow(fn)
	rt.tunnelCache.SetNow(fn)
	rt.catalogCache.SetNow(fn)
	rt.peerTokens.SetNow(fn)
	rt.limiters.SetNow(fn)
}

func (rt *Router) now() time.Time {
	rt.nowMu.RLock()
	fn := rt.nowFn
	rt.nowMu.RUnlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// HostCache returns the router's in-memory HostCache.
func (rt *Router) HostCache() *HostCache {
	return rt.hostCache
}

// TunnelCache returns the router's in-memory TunnelCache.
func (rt *Router) TunnelCache() *TunnelCache {
	return rt.tunnelCache
}

// CatalogCache returns the router's in-memory CatalogCache.
func (rt *Router) CatalogCache() *CatalogCache {
	return rt.catalogCache
}

// RateLimiters returns the router's rate limiters.
func (rt *Router) RateLimiters() *RateLimiters {
	return rt.limiters
}

// ReplicaID returns this router's unique replica identifier.
func (rt *Router) ReplicaID() string {
	return rt.replicaID
}

// PeerAddr returns this replica's `:8082` peer address.
func (rt *Router) PeerAddr() string {
	rt.peerAddrMu.RLock()
	defer rt.peerAddrMu.RUnlock()
	return rt.peerAddr
}

// SetPeerAddr updates this replica's `:8082` peer address and registers it in the store.
func (rt *Router) SetPeerAddr(addr string) {
	norm := NormalizePeerAddr(addr)
	if norm == "" {
		return
	}
	rt.peerAddrMu.Lock()
	rt.peerAddr = norm
	rt.peerAddrMu.Unlock()
	if rt.store != nil {
		_ = rt.store.UpsertReplica(context.Background(), rt.replicaID, norm)
	}
}

// IsDraining reports whether the router is in SIGTERM drain mode.
func (rt *Router) IsDraining() bool {
	return rt.draining.Load()
}

// SetDraining toggles the router's drain flag.
func (rt *Router) SetDraining(draining bool) {
	rt.draining.Store(draining)
}

// Close stops all background loops and active tunnels on this router.
func (rt *Router) Close() {
	rt.stopOnce.Do(func() {
		close(rt.stopCh)
	})
}

// StartBackground launches the background goroutines for:
//   - PostgreSQL `LISTEN registry_entries` and `LISTEN registry_tunnels`
//   - 3s `tunnelCache` fallback poll + local tunnel reconciliation
//   - `replica_heartbeat` (`replicas` table upsert)
func (rt *Router) StartBackground(ctx context.Context) {
	if rt.store == nil {
		return
	}
	_ = rt.store.UpsertReplica(ctx, rt.replicaID, rt.PeerAddr())
	_ = rt.tunnelCache.PollAll(ctx)
	_ = rt.catalogCache.Reload(ctx)
	go rt.catalogCache.StartLoop(ctx)

	if strings.TrimSpace(rt.opts.DatabaseURL) != "" {
		go ListenPostgresAllNotifications(ctx, rt.opts.DatabaseURL, rt.onEntryNotification, rt.onTunnelNotification, rt.onCatalogNotification)
	}

	go func() {
		ticker := time.NewTicker(DefaultTunnelPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rt.stopCh:
				return
			case <-ticker.C:
				_ = rt.tunnelCache.PollAll(ctx)
				rt.ReconcileAllLocalTunnels(ctx)
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(rt.opts.ReplicaHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rt.stopCh:
				return
			case <-ticker.C:
				if !rt.IsDraining() {
					_ = rt.store.UpsertReplica(ctx, rt.replicaID, rt.PeerAddr())
				}
			}
		}
	}()
}

func (rt *Router) onEntryNotification(name string) {
	rt.hostCache.Invalidate(name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if name == "" {
		rt.ReconcileAllLocalTunnels(ctx)
		return
	}
	rt.ReconcileLocalTunnel(ctx, name)
}

func (rt *Router) onTunnelNotification(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if name == "" {
		_ = rt.tunnelCache.PollAll(ctx)
		rt.ReconcileAllLocalTunnels(ctx)
		return
	}
	_, _ = rt.tunnelCache.RefreshEntry(ctx, name)
	rt.ReconcileLocalTunnel(ctx, name)
}

func (rt *Router) onCatalogNotification(_ string) {
	rt.catalogCache.Invalidate()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = rt.catalogCache.Reload(ctx)
}

// WrapMainHandler wraps the control-plane handler on `:8080`, routing
// `<name>.<instance-domain>` requests to ServeInstanceHost, `<registry-host>`
// requests to controlPlane, and rejecting multi-label or unknown Host headers with 404.
func (rt *Router) WrapMainHandler(controlPlane http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		match := ClassifyHost(r.Host, rt.opts.RegistryHost, rt.opts.InstanceDomain)
		switch match.Kind {
		case HostRouteInstance:
			rt.ServeInstanceHost(w, r, match.EntryName, match.Host)
		case HostRouteInvalidInstance, HostRouteUnknown:
			WriteMissingEntryState(w, r, rt.opts.RegistryHost, match.EntryName, nil, false)
		case HostRouteRegistry:
			if strings.TrimSpace(r.Header.Get(HeaderOnBehalfOf)) != "" {
				writeJSONError(w, http.StatusBadRequest, "delegation_not_supported", "X-Grain-On-Behalf-Of is only supported on entry hosts")
				return
			}
			controlPlane.ServeHTTP(w, r)
		}
	})
}

// PublicHandler returns the HTTP handler for `grain-registry-public` (`:8081`),
// exposing strictly:
//   - `GET /.well-known/jwks.json`
//   - `GET /healthz`
//   - `GET /api/entries/{name}/connect` (`X-Grain-Auth: token` + `grc_…` required)
//   - `PUT /api/entries/{name}/heartbeat` and `POST /api/entries/{name}/heartbeat` (`grc_…` required)
//
// and returning 404 for all other paths or Host headers.
func (rt *Router) PublicHandler(jwksHandler, healthzHandler http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	if jwksHandler != nil {
		mux.HandleFunc("GET /.well-known/jwks.json", jwksHandler)
	}
	if healthzHandler != nil {
		mux.HandleFunc("GET /healthz", healthzHandler)
	}
	mux.HandleFunc("GET /api/entries/{name}/connect", func(w http.ResponseWriter, r *http.Request) {
		rt.HandleConnect(w, r, r.PathValue("name"), true)
	})
	mux.HandleFunc("PUT /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		rt.HandleEntryHeartbeat(w, r, true)
	})
	mux.HandleFunc("POST /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		rt.HandleEntryHeartbeat(w, r, true)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		match := ClassifyHost(r.Host, rt.opts.RegistryHost, rt.opts.InstanceDomain)
		if match.Kind != HostRouteRegistry {
			writeJSONError(w, http.StatusNotFound, "not_found", "public endpoint only serves registry host")
			return
		}
		if strings.TrimSpace(r.Header.Get(HeaderOnBehalfOf)) != "" {
			writeJSONError(w, http.StatusBadRequest, "delegation_not_supported", "X-Grain-On-Behalf-Of is only supported on entry hosts")
			return
		}
		p := r.URL.Path
		allowed := p == "/.well-known/jwks.json" ||
			p == "/healthz" ||
			(strings.HasPrefix(p, "/api/entries/") && (strings.HasSuffix(p, "/connect") || strings.HasSuffix(p, "/heartbeat")))
		if !allowed {
			writeJSONError(w, http.StatusNotFound, "not_found", "endpoint not exposed on public listener")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// HandleEntryHeartbeat handles `PUT /api/entries/{name}/heartbeat` and
// `POST /api/entries/{name}/heartbeat` on both `:8081` (`publicOnly = true`) and `:8080`.
func (rt *Router) HandleEntryHeartbeat(w http.ResponseWriter, r *http.Request, publicOnly bool) {
	if rt.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	ip := ClientIP(r)
	if ok, retryAfter := rt.limiters.PublicPerIP.Allow(ip); !ok {
		WriteRateLimited(w, retryAfter, "too many requests from client IP")
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if err := api.ValidateEntryName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_name", err.Error())
		return
	}

	var entry *store.EntryRow
	rawToken := ExtractRegistryToken(r)
	wantsTokenOnly := publicOnly || strings.EqualFold(strings.TrimSpace(r.Header.Get(api.HeaderAuth)), api.AuthModeToken)
	if wantsTokenOnly {
		if rawToken == "" {
			rt.recordFailedAuthOrWrite429(w, ip, "Bearer grc_ token required")
			return
		}
		authedEntry, _, err := rt.store.AuthenticateCredentialWithID(r.Context(), rawToken)
		if err != nil || !strings.EqualFold(authedEntry.Name, name) {
			rt.recordFailedAuthOrWrite429(w, ip, "invalid or revoked credential")
			return
		}
		entry = authedEntry
	} else {
		callerEmail, authErr := rt.authenticateConnectIdentity(r)
		if authErr == nil && callerEmail != "" {
			loaded, err := rt.store.GetEntryByName(r.Context(), name)
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
				return
			}
			if err != nil {
				writeInternalError(w, err)
				return
			}
			ok, groupErr := rt.canConnectAsIdentity(r.Context(), callerEmail, loaded)
			if groupErr != nil {
				w.Header().Set("Retry-After", "5")
				writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
				return
			}
			if !ok {
				writeJSONError(w, http.StatusForbidden, "forbidden", "caller is not authorized for this entry")
				return
			}
			if rawToken != "" {
				authedEntry, _, err := rt.store.AuthenticateCredentialWithID(r.Context(), rawToken)
				if err != nil || authedEntry.ID != loaded.ID {
					rt.recordFailedAuthOrWrite429(w, ip, "invalid or mismatched entry credential")
					return
				}
			}
			entry = loaded
		} else if errors.Is(authErr, webauth.ErrCallerUnauthenticated) && rawToken != "" {
			authedEntry, _, err := rt.store.AuthenticateCredentialWithID(r.Context(), rawToken)
			if err != nil || !strings.EqualFold(authedEntry.Name, name) {
				rt.recordFailedAuthOrWrite429(w, ip, "invalid or revoked credential")
				return
			}
			entry = authedEntry
		} else {
			if errors.Is(authErr, webauth.ErrGroupLookupUnavailable) {
				w.Header().Set("Retry-After", "5")
				writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
				return
			}
			if webauthErrForbidden(authErr) {
				writeJSONError(w, http.StatusForbidden, "forbidden", "access denied")
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
	}

	if entry.OwnerDisabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "entry owner account is disabled")
		return
	}
	if disabled, err := rt.isOwnerDisabled(r.Context(), entry.OwnerEmail); err == nil && disabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "entry owner account is disabled")
		return
	}
	if entry.LifecycleState == api.StateDeleted || entry.DeletedAt != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}

	if ok, retryAfter := rt.limiters.HeartbeatPerEntry.Allow(entry.Name); !ok {
		WriteRateLimited(w, retryAfter, "entry heartbeat rate limit exceeded")
		return
	}

	var req api.HeartbeatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	updated, err := rt.store.RecordHeartbeat(r.Context(), entry.ID, req)
	if err != nil {
		if errors.Is(err, store.ErrPrimaryScopeConflict) {
			writeJSONError(w, http.StatusConflict, "scope_conflict", err.Error())
			return
		}
		writeInternalError(w, err)
		return
	}
	rt.hostCache.Put(updated)
	if rt.opts.OnHeartbeat != nil {
		rt.opts.OnHeartbeat()
	}

	combined := store.CombinedAPIScopes(updated.Scopes)
	activeScopes := make([]api.Scope, 0, len(combined))
	for _, sc := range combined {
		if sc.Active {
			activeScopes = append(activeScopes, sc)
		}
	}
	conflicts := rt.store.EntryScopeConflicts(r.Context(), updated)
	if conflicts == nil {
		conflicts = []api.ScopeConflict{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
		Status:               rt.store.DeriveStatus(updated),
		ActiveScopes:         activeScopes,
		Conflicts:            conflicts,
		ScopeConflicts:       conflicts,
		NextIntervalSeconds:  15,
		NextHeartbeatSeconds: 15,
	})
}

func (rt *Router) authenticateConnectIdentity(r *http.Request) (string, error) {
	admitted, err := rt.admitter.Admit(r)
	if err == nil && admitted != nil {
		return admitted.Email, nil
	}
	if errors.Is(err, webauth.ErrCallerForbidden) {
		// Allow service principals (e.g. `connected_principal`) that are not in the
		// static catalog-reader `service_principals` list to be evaluated against the entry.
		if rawCaller, idErr := rt.admitter.AuthenticateIdentity(r); idErr == nil && rawCaller != nil && rawCaller.Kind == webauth.PrincipalService {
			return rawCaller.Email, nil
		}
	}
	return "", err
}

// ServeInstanceHost routes a request arriving on `<name>.<instance-domain>`.
func (rt *Router) ServeInstanceHost(w http.ResponseWriter, r *http.Request, name, hostHeader string) {
	// 1. Authenticate caller at the front door.
	caller, err := rt.admitter.Admit(r)
	if err != nil {
		// If a service principal not in `service_principals` is making a delegated
		// request (or accessing an entry whose `access_list` names it), allow
		// `AuthenticateIdentity` to identify the service principal so step 3 / step 6
		// can evaluate it.
		if errors.Is(err, webauth.ErrCallerForbidden) {
			if rawCaller, idErr := rt.admitter.AuthenticateIdentity(r); idErr == nil && rawCaller != nil && rawCaller.Kind == webauth.PrincipalService {
				caller = &webauth.AdmittedCaller{
					Email:        rawCaller.Email,
					Subject:      rawCaller.Subject,
					HostedDomain: rawCaller.HostedDomain,
					Kind:         webauth.PrincipalService,
				}
				err = nil
			}
		}
	}
	if err != nil {
		if rt.opts.OnAdmissionRejected != nil {
			rt.opts.OnAdmissionRejected()
		}
		switch {
		case errors.Is(err, webauth.ErrGroupLookupUnavailable):
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		case webauthErrForbidden(err):
			writeJSONError(w, http.StatusForbidden, "forbidden", err.Error())
		default:
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		}
		return
	}

	// 2. Check if the authenticated caller is disabled.
	if disabled, err := rt.isOwnerDisabled(r.Context(), caller.Email); err == nil && disabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "caller account is disabled")
		return
	}

	// 3. Handle X-Grain-On-Behalf-Of delegation before lookup/authorization.
	effectiveCaller := caller
	var actor *AssertionActor
	oboRaw := strings.TrimSpace(r.Header.Get(HeaderOnBehalfOf))
	if oboRaw != "" {
		if strings.TrimSpace(r.Header.Get("Cookie")) != "" ||
			strings.TrimSpace(r.Header.Get("Origin")) != "" ||
			strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")) != "" {
			writeJSONError(w, http.StatusForbidden, "delegation_from_browser", "delegated requests must not carry browser headers (Cookie, Origin, Sec-Fetch-Site)")
			return
		}
		if caller.Kind == webauth.PrincipalHuman {
			writeJSONError(w, http.StatusBadRequest, "delegation_requires_service_caller", "human callers must not set X-Grain-On-Behalf-Of")
			return
		}
		if IsUpgradeRequest(r) {
			writeDelegationRefused(w, "not_delegable", "protocol upgrades cannot use on-behalf-of delegation")
			return
		}
		oboEmail := strings.ToLower(oboRaw)
		if webauth.PrincipalKindForEmail(oboEmail) != webauth.PrincipalHuman {
			writeDelegationRefused(w, "person_not_admitted", "X-Grain-On-Behalf-Of must name a human principal")
			return
		}
		if rt.delegation == nil {
			writeDelegationRefused(w, "delegation_disabled", "on-behalf-of delegation is not permitted")
			return
		}
		act, delErr := rt.delegation.AuthorizeOnBehalfOf(r.Context(), caller.Email, oboEmail, name)
		if delErr != nil {
			reason := "delegation_disabled"
			var refErr interface{ DelegationRefusalReason() string }
			if errors.As(delErr, &refErr) && refErr.DelegationRefusalReason() != "" {
				reason = refErr.DelegationRefusalReason()
			}
			writeDelegationRefused(w, reason, delErr.Error())
			return
		}
		if act != nil && strings.EqualFold(act.Access, "read") && r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDelegationRefused(w, "read_only_grant", fmt.Sprintf("read-only delegation grant cannot perform %s", r.Method))
			return
		}
		if disabled, err := rt.isOwnerDisabled(r.Context(), oboEmail); err == nil && disabled {
			writeJSONError(w, http.StatusForbidden, "owner_disabled", "delegated user account is disabled")
			return
		}
		actor = act
		if actor != nil && actor.Sub == "" {
			actor.Sub = caller.Email
		}
		if actor != nil && actor.Kind == "" {
			actor.Kind = "service"
		}
		effectiveCaller = &webauth.AdmittedCaller{
			Email:   oboEmail,
			Subject: oboEmail,
			Kind:    webauth.PrincipalHuman,
		}
	}

	// 4. Lookup `<name>` in hostCache.
	cached, err := rt.hostCache.Lookup(r.Context(), name)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "entry routing metadata temporarily unavailable")
		return
	}
	if cached.Missing || cached.Entry == nil {
		isManager := false
		if cached.DeletedRow != nil {
			isManager, _ = rt.canCallerManageEntry(r.Context(), effectiveCaller, cached.DeletedRow)
		} else if effectiveCaller != nil && effectiveCaller.IsAdmin {
			isManager = true
		}
		WriteMissingEntryState(w, r, rt.opts.RegistryHost, name, cached.DeletedRow, isManager)
		return
	}

	entry := cached.Entry

	// 5. Reject if entry owner is disabled.
	if entry.OwnerDisabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "entry owner account is disabled")
		return
	}
	if disabled, err := rt.isOwnerDisabled(r.Context(), entry.OwnerEmail); err == nil && disabled {
		writeJSONError(w, http.StatusForbidden, "owner_disabled", "entry owner account is disabled")
		return
	}

	// 6. Enforce entry visibility / access policy.
	allowed, err := rt.canCallerAccessEntry(r.Context(), effectiveCaller, entry)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		return
	}
	if !allowed {
		if effectiveCaller.Kind != webauth.PrincipalHuman {
			writeJSONError(w, http.StatusForbidden, "forbidden", "service principals cannot access instance subdomains")
			return
		}
		writeJSONError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("caller %q does not have access to %q", effectiveCaller.Email, name))
		return
	}

	isManager, _ := rt.canCallerManageEntry(r.Context(), effectiveCaller, entry)

	// 7. Handle non-running lifecycle states with self-contained HTML / JSON state pages.
	switch strings.ToLower(strings.TrimSpace(string(entry.LifecycleState))) {
	case "creating", "replacing", "provisioning":
		WriteProvisioningState(w, r, rt.opts.RegistryHost, entry)
		return
	case "deleting", "deprovisioning":
		WriteDeletingState(w, r, entry)
		return
	case "failed":
		WriteFailedState(w, r, rt.opts.RegistryHost, entry, isManager)
		return
	case "deleted":
		WriteMissingEntryState(w, r, rt.opts.RegistryHost, name, entry, isManager)
		return
	}

	// 8. Enforce per-entry proxied HTTP request rate limit (100 req/s, burst 200).
	if ok, retryAfter := rt.limiters.ProxyPerEntry.Allow(entry.Name); !ok {
		WriteRateLimited(w, retryAfter, "entry request rate limit exceeded")
		return
	}

	entryHost := entry.Name + "." + rt.opts.InstanceDomain
	if hostHeader != "" {
		entryHost = hostHeader
	}

	// 9. Enforce pre-strip CSRF check on state-changing cookie/browser requests.
	if err := VerifyPreStripCSRF(r, entryHost); err != nil {
		writeJSONError(w, http.StatusForbidden, "csrf_failed", err.Error())
		return
	}

	// 10. Cap non-upgraded request body at 64 MiB (413 Payload Too Large).
	if !IsUpgradeRequest(r) {
		if r.ContentLength > MaxProxiedRequestBodyBytes {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 64 MiB limit")
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, MaxProxiedRequestBodyBytes)
		}
	}

	// 11. Sign (or reuse cached 30s) ES256 X-Grain-Registry-Assertion (`aud = "https://<name>.<instance-domain>"`)
	// and sanitize headers/cookies while preserving Host.
	sub := strings.ToLower(strings.TrimSpace(effectiveCaller.Subject))
	if sub == "" || actor != nil {
		sub = strings.ToLower(strings.TrimSpace(effectiveCaller.Email))
	}
	kind := "human"
	if effectiveCaller.Kind == webauth.PrincipalService {
		kind = "service"
	}
	assertionJWT, err := rt.signer.SignAssertion(r.Context(), AssertionClaims{
		Audience: "https://" + entryHost,
		Subject:  sub,
		Email:    strings.ToLower(strings.TrimSpace(effectiveCaller.Email)),
		Kind:     kind,
		Actor:    actor,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}

	reqID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	isWriteMethod := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
	if actor != nil && rt.store != nil {
		if actor.Grant != "" {
			_ = rt.store.RecordDelegationUsage(r.Context(), actor.Grant, entry.Name, isWriteMethod)
		}
		if isWriteMethod {
			_ = rt.store.RecordAuditWithDelegation(r.Context(), effectiveCaller.Email, actor.Sub, actor.Grant, "delegated_write", entry.ID, entry.Name, map[string]any{
				"method": r.Method,
				"path":   r.URL.Path,
				"rid":    reqID,
			})
		}
	}
	SanitizeProxyRequest(r, entryHost, rt.opts.RegistryHost, rt.opts.IAPAudience, assertionJWT, reqID)

	// 12. Dispatch per entry kind.
	switch entry.Kind {
	case api.KindInstance:
		rt.routeInstanceEntry(w, r, entry, entryHost)
	case api.KindConnected:
		rt.routeConnectedEntry(w, r, entry, isManager, true)
	default:
		writeJSONError(w, http.StatusNotImplemented, "unsupported_entry_kind", fmt.Sprintf("entry kind %q is not supported", entry.Kind))
	}
}

func (rt *Router) routeInstanceEntry(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, entryHost string) {
	rawURL := strings.TrimSpace(entry.BackendURL)
	if rawURL == "" {
		WriteProvisioningState(w, r, rt.opts.RegistryHost, entry)
		return
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		WriteProvisioningState(w, r, rt.opts.RegistryHost, entry)
		return
	}
	proxy := NewInstanceReverseProxy(targetURL, entryHost, rt.instanceHTTPTransport)
	proxy.ServeHTTP(w, r)
}

func (rt *Router) routeConnectedEntry(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, isManager bool, allowWait bool) {
	// 1. Check local active tunnel first (1 hop).
	if local := rt.GetLocalTunnel(entry.Name); local != nil {
		rt.serveLocalTunnel(w, r, entry, isManager, local)
		return
	}

	// 2. Check tunnelCache for a peer replica holding the active tunnel.
	now := rt.now()
	tc, ok := rt.tunnelCache.Get(entry.Name)
	if !ok && rt.store != nil {
		tc, _ = rt.tunnelCache.RefreshEntry(r.Context(), entry.Name)
		ok = true
	}
	if ok && tc.HasActiveSession(now, rt.opts.ReplicaTTL) {
		if tc.ReplicaID != "" && tc.ReplicaID != rt.replicaID && tc.PeerAddr != "" {
			rt.forwardToPeer(w, r, entry, isManager, tc.PeerAddr, allowWait)
			return
		}
		if tc.ReplicaID == rt.replicaID {
			// Local tunnel is already gone; mark disconnected in tunnelCache.
			rt.tunnelCache.MarkDisconnected(entry.Name, tc.SessionEpoch, now, "disconnected")
		}
	}

	// 3. Tunnel is offline; wait in reconnect_grace if recently disconnected, else render 503 offline page.
	rt.handleOfflineOrWait(w, r, entry, isManager, allowWait)
}

func (rt *Router) serveLocalTunnel(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, isManager bool, local *ActiveTunnel) {
	origHost := r.Host
	if origHost == "" {
		origHost = entry.Name + "." + rt.opts.InstanceDomain
	}
	targetURL := &url.URL{
		Scheme: "http",
		Host:   origHost,
	}
	proxy := NewInstanceReverseProxy(targetURL, origHost, local.Transport())
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		if local.IsClosed() || local.IsSuperseded() {
			tc, _ := rt.tunnelCache.Get(entry.Name)
			WriteOfflineConnectedState(rw, req, rt.enrichOfflineEntry(req.Context(), entry), tc, rt.now(), isManager)
			return
		}
		handleProxyError(rw, req, err)
	}
	proxy.ServeHTTP(w, r)
}

func (rt *Router) handleOfflineOrWait(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, isManager bool, allowWait bool) {
	now := rt.now()
	tc, _ := rt.tunnelCache.Get(entry.Name)
	if allowWait && rt.opts.ReconnectGrace > 0 && tc.DisconnectedRecently(now, DefaultReconnectWindow) {
		_, ok, err := rt.tunnelCache.WaitForReconnect(r.Context(), entry.Name, rt.opts.ReconnectGrace)
		if err == nil && ok {
			rt.routeConnectedEntry(w, r, entry, isManager, false)
			return
		}
		tc, _ = rt.tunnelCache.Get(entry.Name)
	}
	WriteOfflineConnectedState(w, r, rt.enrichOfflineEntry(r.Context(), entry), tc, rt.now(), isManager)
}

func (rt *Router) enrichOfflineEntry(ctx context.Context, entry *store.EntryRow) *store.EntryRow {
	if entry == nil {
		return nil
	}
	clone := *entry
	if rt.store != nil {
		if fresh, err := rt.store.GetEntryByName(ctx, entry.Name); err == nil && fresh != nil {
			clone = *fresh
		}
	}
	return &clone
}

func (rt *Router) isOwnerDisabled(ctx context.Context, email string) (bool, error) {
	if rt.store == nil || strings.TrimSpace(email) == "" {
		return false, nil
	}
	return rt.store.IsOwnerDisabled(ctx, email)
}

func normalizeGroupSpec(g string) string {
	g = strings.TrimSpace(g)
	if g == "" {
		return ""
	}
	if !strings.HasPrefix(g, "group:") {
		return "group:" + strings.ToLower(g)
	}
	return "group:" + strings.ToLower(strings.TrimSpace(strings.TrimPrefix(g, "group:")))
}

func (rt *Router) canCallerManageEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || e == nil || caller.Kind != webauth.PrincipalHuman {
		return false, nil
	}
	if caller.IsAdmin || strings.EqualFold(e.OwnerEmail, caller.Email) {
		return true, nil
	}
	if g := normalizeGroupSpec(e.OwnerGroup); g != "" {
		return rt.matchesAccessList(ctx, caller.Email, []string{g})
	}
	return false, nil
}

func (rt *Router) canCallerAccessEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || e == nil {
		return false, nil
	}
	if caller.Kind != webauth.PrincipalHuman {
		return rt.matchesAccessList(ctx, caller.Email, e.AccessList)
	}
	if caller.IsAdmin || strings.EqualFold(e.OwnerEmail, caller.Email) || e.Visibility == store.VisibilityPublic {
		return true, nil
	}
	if g := normalizeGroupSpec(e.OwnerGroup); g != "" {
		ok, err := rt.matchesAccessList(ctx, caller.Email, []string{g})
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	if e.Visibility == store.VisibilityShared {
		return rt.matchesAccessList(ctx, caller.Email, e.AccessList)
	}
	return false, nil
}

func (rt *Router) matchesAccessList(ctx context.Context, email string, accessList []string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, spec := range accessList {
		spec = strings.TrimSpace(spec)
		switch {
		case strings.HasPrefix(spec, "user:"):
			u := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec, "user:")))
			if u == email {
				return true, nil
			}
		case strings.HasPrefix(spec, "serviceAccount:"):
			sa := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec, "serviceAccount:")))
			if sa == email {
				return true, nil
			}
		case strings.HasPrefix(spec, "group:"):
			if rt.groups == nil {
				return false, errors.New("group resolver not configured")
			}
			gEmail := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec, "group:")))
			ok, err := rt.groups.IsMember(ctx, gEmail, email)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func webauthErrForbidden(err error) bool {
	return errors.Is(err, webauth.ErrCallerForbidden)
}
