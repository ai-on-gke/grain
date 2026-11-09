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

package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/server"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/version"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

const strictCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// ServerOptions configures a registry HTTP Server.
type ServerOptions struct {
	Config      Config
	Store       *store.Store
	Admitter    *webauth.Admitter
	Groups      GroupResolver
	Delegation  DelegationPolicy
	Signer      Signer
	Transport   ConnectTransport
	Compute     Compute
	Resources   InstanceResources
	ConfigRepos ConfigRepos
	Orphans     OrphanProvider
	Metrics     *Metrics
	ReplicaID   string
	PeerAddr    string
}

// Server serves the main registry HTTP listener (`:8080`), the public listener
// (`:8081`), and the replica-to-replica peer listener (`:8082`).
type Server struct {
	cfg         Config
	store       *store.Store
	admitter    *webauth.Admitter
	groups      GroupResolver
	delegation  DelegationPolicy
	signer      Signer
	transport   ConnectTransport
	compute     Compute
	resources   InstanceResources
	configRepos ConfigRepos
	orphans     OrphanProvider
	metrics     *Metrics
	static      http.Handler
	router      *server.Router
	draining    atomic.Bool
}

type signerAdapter struct {
	signer Signer
}

func (a signerAdapter) SignAssertion(ctx context.Context, claims server.AssertionClaims) (string, error) {
	var act *DelegationActor
	if claims.Actor != nil {
		act = &DelegationActor{
			Sub:    claims.Actor.Sub,
			Kind:   claims.Actor.Kind,
			App:    claims.Actor.App,
			Grant:  claims.Actor.Grant,
			Access: claims.Actor.Access,
		}
	}
	return a.signer.SignAssertion(ctx, HubClaims{
		Issuer:   claims.Issuer,
		Audience: claims.Audience,
		Subject:  claims.Subject,
		Email:    claims.Email,
		Kind:     claims.Kind,
		Actor:    act,
	})
}

func (a signerAdapter) SignPeerToken(ctx context.Context, replicaID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = server.PeerTokenTTL
	}
	now := time.Now().UTC()
	return a.signer.SignAssertion(ctx, HubClaims{
		Issuer:    HubAssertionIssuer,
		Audience:  server.PeerAuthAudience,
		Subject:   replicaID,
		Kind:      "service",
		IssuedAt:  now.Unix(),
		NotBefore: now.Add(-AssertionNotBeforeSkew).Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		JWTID:     randomJTI(),
	})
}

func (a signerAdapter) VerifyPeerToken(ctx context.Context, token string) (string, error) {
	jwksDoc, err := a.signer.JWKS(ctx)
	if err != nil {
		return "", err
	}
	claims, err := VerifyHubAssertion(token, jwksDoc, server.PeerAuthAudience, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}

func (a signerAdapter) InvalidateGrant(grantID string) {
	if inv, ok := a.signer.(interface{ InvalidateGrant(string) }); ok {
		inv.InvalidateGrant(grantID)
	}
}

func (a signerAdapter) InvalidateApp(appName string) {
	if inv, ok := a.signer.(interface{ InvalidateApp(string) }); ok {
		inv.InvalidateApp(appName)
	}
}

type delegationAdapter struct {
	policy DelegationPolicy
}

func (a delegationAdapter) AuthorizeOnBehalfOf(ctx context.Context, servicePrincipal, targetUserEmail, targetEntryName string) (*server.AssertionActor, error) {
	if a.policy == nil {
		return nil, ErrDelegationDenied
	}
	act, err := a.policy.AuthorizeOnBehalfOf(ctx, servicePrincipal, targetUserEmail, targetEntryName)
	if err != nil {
		return nil, err
	}
	if act == nil {
		return nil, nil
	}
	return &server.AssertionActor{
		Sub:    act.Sub,
		Kind:   act.Kind,
		App:    act.App,
		Grant:  act.Grant,
		Access: act.Access,
	}, nil
}

// NewServer constructs a registry HTTP Server from ServerOptions.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Admitter == nil {
		return nil, errors.New("registry server: Admitter is required")
	}
	if opts.Signer == nil {
		return nil, errors.New("registry server: Signer is required")
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = NewMetrics()
	}

	peerAddr := strings.TrimSpace(opts.PeerAddr)
	if peerAddr == "" {
		peerAddr = strings.TrimSpace(opts.Config.Tunnel.SelfAddr)
	}
	if peerAddr == "" {
		peerAddr = strings.TrimSpace(opts.Config.Tunnel.PeerListen)
	}

	var delChecker server.DelegationChecker
	if opts.Delegation != nil {
		if opts.Store != nil {
			if sa, ok := opts.Delegation.(interface{ SetStore(*store.Store) }); ok {
				sa.SetStore(opts.Store)
			}
		}
		delChecker = delegationAdapter{policy: opts.Delegation}
	}

	rt, err := server.NewRouter(server.RouterOptions{
		HubHost:           opts.Config.Host,
		InstanceDomain:    opts.Config.InstanceDomain,
		IAPAudience:       opts.Config.Auth.IAPAudience,
		ReplicaID:         opts.ReplicaID,
		PeerAddr:          peerAddr,
		DatabaseURL:       opts.Config.Database.URL,
		ReplicaHeartbeat:  opts.Config.Tunnel.ReplicaHeartbeat,
		ReplicaTTL:        opts.Config.Tunnel.ReplicaTTL,
		ReconnectGrace:    opts.Config.Tunnel.ReconnectGrace,
		CatalogTTL:        opts.Config.Catalog.CacheTTL,
		HeartbeatInterval: opts.Config.Catalog.HeartbeatInterval,
		TunnelOptions: server.TunnelOptions{
			MaxConcurrentStreams: opts.Config.Tunnel.MaxConcurrentStreams,
			MaxBufferedBytes:     opts.Config.Tunnel.MaxBufferedBytes,
			DrainTimeout:         opts.Config.Tunnel.DrainTimeout,
			OnPeerStreamRejected: func(_ string, _ uint32) {
				metrics.IncPeerStreamRejected()
			},
			OnActiveTunnelsChanged: func(count int64) {
				metrics.SetActiveTunnels(count)
			},
		},
		Store:               opts.Store,
		Admitter:            opts.Admitter,
		Groups:              opts.Groups,
		Delegation:          delChecker,
		Signer:              signerAdapter{signer: opts.Signer},
		OnPeerForward:       metrics.IncPeerForwards,
		OnAdmissionRejected: metrics.IncAdmissionRejected,
		OnHeartbeat:         metrics.IncHeartbeats,
	})
	if err != nil {
		return nil, err
	}

	srv := &Server{
		cfg:         opts.Config,
		store:       opts.Store,
		admitter:    opts.Admitter,
		groups:      opts.Groups,
		delegation:  opts.Delegation,
		signer:      opts.Signer,
		transport:   opts.Transport,
		compute:     opts.Compute,
		resources:   opts.Resources,
		configRepos: opts.ConfigRepos,
		orphans:     opts.Orphans,
		metrics:     metrics,
		static:      server.NewStaticHandler(),
		router:      rt,
	}
	if srv.store != nil && len(srv.cfg.Catalog.ScopeOwners) > 0 {
		srv.store.SetPrimaryScopeAuthorizer(srv.authorizePrimaryScopeOwner)
	}
	return srv, nil
}

// SetOrphanProvider attaches an OrphanProvider (such as a Reconciler) to the Server.
func (s *Server) SetOrphanProvider(op OrphanProvider) {
	s.orphans = op
}

// SetInstanceProviders attaches Compute, InstanceResources, and ConfigRepos providers to the Server.
func (s *Server) SetInstanceProviders(compute Compute, resources InstanceResources, configRepos ConfigRepos) {
	s.compute = compute
	s.resources = resources
	s.configRepos = configRepos
}

// Router returns the underlying subdomain/tunnel/peer Router.
func (s *Server) Router() *server.Router {
	return s.router
}

// StartBackground launches background LISTEN/poll/heartbeat loops on the Router.
func (s *Server) StartBackground(ctx context.Context) {
	if s.router != nil {
		s.router.StartBackground(ctx)
	}
	if s.store != nil && len(s.cfg.Catalog.ScopeOwners) > 0 {
		go func() {
			if n, err := s.ReconcilePrimaryScopeOwners(ctx); err != nil {
				log.Printf("registry: catalog.scope_owners startup sweep: %v (demoted %d entries)", err, n)
			} else if n > 0 {
				log.Printf("registry: catalog.scope_owners startup sweep demoted unauthorized primary scopes on %d entries", n)
			}
		}()
	}
}

// ReconcilePrimaryScopeOwners re-evaluates every stored entry's primary
// scopes against catalog.scope_owners and demotes those the policy does not
// authorize. Write-time checks alone leave primaries claimed before the
// policy was configured (or before a rule was tightened) in place, and
// /api/route would keep returning them as primary; this sweep, run once at
// startup, closes that gap. It returns how many entries were changed.
func (s *Server) ReconcilePrimaryScopeOwners(ctx context.Context) (int, error) {
	if s.store == nil || len(s.cfg.Catalog.ScopeOwners) == 0 {
		return 0, nil
	}
	entries, err := s.store.ListEntries(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	var firstErr error
	for _, e := range entries {
		hasPrimary := false
		for _, sc := range e.Scopes {
			if sc.PrimaryClaim {
				hasPrimary = true
				break
			}
		}
		if !hasPrimary {
			continue
		}
		updated, dErr := s.demoteUnauthorizedPrimaryScopesWithPolicy(ctx, e, true, false)
		if dErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("entry %q: %w", e.Name, dErr)
			}
			continue
		}
		if updated != e {
			changed++
			_ = s.store.RecordAudit(ctx, "system:scope-owners", "entry_scope_owner_demote", updated.ID, updated.Name, nil)
			if s.router != nil {
				s.router.HostCache().Put(updated)
			}
		}
	}
	return changed, firstErr
}

// BeginDrain marks `/healthz` not-ready (`503`) and drains active tunnels on SIGTERM.
func (s *Server) BeginDrain(ctx context.Context) {
	s.SetDraining(true)
	if s.router != nil {
		s.router.BeginDrain(ctx)
	}
}

// SetDraining toggles the server's drain mode (`GET /healthz` returns 503 while draining).
func (s *Server) SetDraining(draining bool) {
	s.draining.Store(draining)
	if s.router != nil {
		s.router.SetDraining(draining)
	}
}

// IsDraining reports whether the server is currently draining.
func (s *Server) IsDraining() bool {
	if s.draining.Load() {
		return true
	}
	return s.router != nil && s.router.IsDraining()
}

// JWKSHandler returns the handler for the dedicated public listener (`:8081`),
// serving `GET /.well-known/jwks.json`, `GET /healthz`, `GET /api/entries/{name}/connect`,
// and `PUT`/`POST /api/entries/{name}/heartbeat`.
func (s *Server) JWKSHandler() http.Handler {
	return s.PublicHandler()
}

// PublicHandler returns the handler for `grain-registry-public` (`:8081`).
func (s *Server) PublicHandler() http.Handler {
	return s.router.PublicHandler(s.handleJWKS, s.handleHealthz)
}

// PeerHandler returns the handler for the replica-to-replica peer listener (`:8082`).
func (s *Server) PeerHandler() http.Handler {
	return s.router.PeerHandler()
}

// Handler returns the main HTTP handler for `:8080`.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.Handle("GET /metrics", s.admitter.RequireAdmin(s.metrics.Handler()))
	mux.HandleFunc("POST /api/internal/heartbeat", s.handleInternalHeartbeat)
	mux.HandleFunc("GET /api/entries/{name}/connect", func(w http.ResponseWriter, r *http.Request) {
		s.router.HandleConnect(w, r, r.PathValue("name"), false)
	})
	mux.HandleFunc("PUT /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		s.router.HandleEntryHeartbeat(w, r, false)
	})
	mux.HandleFunc("POST /api/entries/{name}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		s.router.HandleEntryHeartbeat(w, r, false)
	})

	mux.Handle("GET /api/me", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetMe)))
	mux.Handle("GET /api/status", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetStatus)))
	mux.Handle("GET /api/admin/summary", s.admitter.RequireAdmin(http.HandlerFunc(s.handleGetAdminSummary)))
	mux.Handle("POST /api/admin/owners/{email}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetOwnerDisabled)))
	mux.Handle("PATCH /api/admin/owners/{email}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetOwnerDisabled)))
	mux.Handle("PUT /api/admin/quotas/{email}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetQuota)))
	mux.Handle("POST /api/admin/quotas/{email}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetQuota)))
	mux.Handle("DELETE /api/admin/quotas/{email}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminResetQuota)))
	mux.Handle("POST /api/admin/conflicts/resolve", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminResolveConflict)))
	mux.Handle("POST /api/admin/sql-instances/{name}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetSQLDraining)))
	mux.Handle("PATCH /api/admin/sql-instances/{name}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetSQLDraining)))
	mux.Handle("DELETE /api/admin/orphans/{zone}/{instance}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminDeleteOrphan)))
	mux.Handle("DELETE /api/admin/tunnels/{name}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminDisconnectTunnel)))
	mux.Handle("POST /api/admin/entries/{name}/purge", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminPurgeEntry)))
	mux.Handle("POST /api/admin/entries/{name}/transfer", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminForceTransfer)))
	mux.Handle("GET /api/admin/delegating-apps", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminListDelegatingApps)))
	mux.Handle("POST /api/admin/delegating-apps", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminCreateDelegatingApp)))
	mux.Handle("PATCH /api/admin/delegating-apps/{name}", s.admitter.RequireAdmin(http.HandlerFunc(s.handleAdminSetDelegatingAppDisabled)))
	mux.Handle("GET /api/delegation-grants", s.admitter.RequireHuman(http.HandlerFunc(s.handleListDelegationGrants)))
	mux.Handle("POST /api/delegation-grants", s.admitter.RequireHuman(http.HandlerFunc(s.handleCreateDelegationGrant)))
	mux.Handle("DELETE /api/delegation-grants/{ref}", s.admitter.RequireHuman(http.HandlerFunc(s.handleRevokeDelegationGrant)))
	// Service principals read the catalog (/api/directory, /api/route) and
	// nothing else; shapes and control-plane endpoints are for humans
	// (hub.design.md, "Plain service calls").
	mux.Handle("GET /api/shapes", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetShapes)))
	mux.Handle("GET /api/directory", s.admitter.RequireAdmitted(http.HandlerFunc(s.handleGetDirectory)))
	mux.Handle("GET /api/route", s.admitter.RequireAdmitted(http.HandlerFunc(s.handleGetRoute)))
	mux.Handle("GET /api/entries/{name}/availability", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntryAvailability)))
	mux.Handle("GET /api/names/{name}", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntryAvailability)))

	mux.Handle("GET /api/entries", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntries)))
	mux.Handle("POST /api/entries", s.admitter.RequireHuman(http.HandlerFunc(s.handleCreateEntry)))
	mux.Handle("GET /api/entries/{name}", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntry)))
	mux.Handle("PATCH /api/entries/{name}", s.admitter.RequireHuman(http.HandlerFunc(s.handleUpdateEntry)))
	mux.Handle("DELETE /api/entries/{name}", s.admitter.RequireHuman(http.HandlerFunc(s.handleDeleteEntry)))
	mux.Handle("PUT /api/entries/{name}/scopes", s.admitter.RequireHuman(http.HandlerFunc(s.handleSetScopes)))
	mux.Handle("POST /api/entries/{name}/credentials/rotate", s.admitter.RequireHuman(http.HandlerFunc(s.handleRotateCredential)))
	mux.Handle("POST /api/entries/{name}/rotate-credential", s.admitter.RequireHuman(http.HandlerFunc(s.handleRotateCredential)))
	mux.Handle("DELETE /api/entries/{name}/credentials/{id}", s.admitter.RequireHuman(http.HandlerFunc(s.handleRevokeCredential)))
	mux.Handle("GET /api/entries/{name}/credentials", s.admitter.RequireHuman(http.HandlerFunc(s.handleListCredentials)))
	mux.Handle("POST /api/entries/{name}/upgrade", s.admitter.RequireHuman(http.HandlerFunc(s.handleUpgradeEntry)))
	mux.Handle("POST /api/entries/{name}/replace", s.admitter.RequireHuman(http.HandlerFunc(s.handleReplaceEntry)))
	mux.Handle("POST /api/entries/{name}/stop", s.admitter.RequireHuman(http.HandlerFunc(s.handleStopEntry)))
	mux.Handle("POST /api/entries/{name}/start", s.admitter.RequireHuman(http.HandlerFunc(s.handleStartEntry)))
	mux.Handle("POST /api/entries/{name}/restore", s.admitter.RequireHuman(http.HandlerFunc(s.handleRestoreEntry)))
	mux.Handle("POST /api/entries/{name}/transfer", s.admitter.RequireHuman(http.HandlerFunc(s.handleOfferTransfer)))
	mux.Handle("POST /api/entries/{name}/transfer/accept", s.admitter.RequireHuman(http.HandlerFunc(s.handleAcceptTransfer)))
	mux.Handle("/", s.admitter.RequireHuman(s.static))

	controlPlane := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", strictCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Enforce CSRF protection on state-changing requests other than bearer-authenticated internal endpoints.
		if !strings.HasPrefix(r.URL.Path, "/api/internal/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if err := webauth.VerifySameOriginForProxyAuth(r); err != nil {
				s.metrics.IncRequests(http.StatusForbidden)
				writeJSONError(w, http.StatusForbidden, "csrf_failed", err.Error())
				return
			}
			if server.ExtractHubToken(r) == "" {
				if err := server.VerifyPreStripCSRF(r, s.cfg.Host); err != nil {
					s.metrics.IncRequests(http.StatusForbidden)
					writeJSONError(w, http.StatusForbidden, "csrf_failed", err.Error())
					return
				}
			}
		}

		rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rec, r)
		s.metrics.IncRequests(rec.status)
	})

	return s.router.WrapMainHandler(controlPlane)
}

func (s *Server) extractInstanceSubdomain(hostHeader string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(hostHeader))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	instDomain := strings.ToLower(strings.TrimSpace(s.cfg.InstanceDomain))
	regHost := strings.ToLower(strings.TrimSpace(s.cfg.Host))
	if instDomain == "" || host == "" || host == regHost {
		return "", false
	}
	suffix := "." + instDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	sub := strings.TrimSuffix(host, suffix)
	if sub == "" || strings.Contains(sub, ".") {
		return "", false
	}
	return sub, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.IsDraining() {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "status": "draining"})
		return
	}
	if s.store != nil && s.store.DB() != nil {
		if err := s.store.DB().PingContext(r.Context()); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "status": "db_unhealthy"})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "ok"})
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	jwksDoc, err := s.signer.JWKS(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "jwks_unavailable", "failed to load JWKS")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jwksDoc)
}

func (s *Server) checkCallerEnabled(w http.ResponseWriter, r *http.Request, caller *webauth.AdmittedCaller) bool {
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return false
	}
	if s.store != nil {
		if disabled, err := s.store.IsOwnerDisabled(r.Context(), caller.Email); err == nil && disabled {
			writeJSONError(w, http.StatusForbidden, "owner_disabled", "caller account is disabled")
			return false
		}
	}
	return true
}

func (s *Server) checkMutationRateLimit(w http.ResponseWriter, caller *webauth.AdmittedCaller) bool {
	if s.router == nil || caller == nil {
		return true
	}
	if ok, retryAfter := s.router.RateLimiters().MutationsPerUser.Allow(caller.Email); !ok {
		server.WriteRateLimited(w, retryAfter, "user control-plane mutation rate limit exceeded")
		return false
	}
	return true
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	resp := map[string]any{
		"email":             caller.Email,
		"sub":               caller.Subject,
		"kind":              caller.Kind,
		"is_admin":          caller.IsAdmin,
		"is_registry_admin": caller.IsAdmin,
		"can_create":        caller.CanCreate,
		"instance_domain":   s.cfg.InstanceDomain,
		"registry_host":     s.cfg.Host,
	}
	if s.store != nil {
		_, _ = s.store.UpsertOwner(r.Context(), caller.Email)
		if quota, err := s.store.GetQuota(r.Context(), caller.Email); err == nil {
			resp["quota"] = api.QuotaUsage{
				QuotaUnitsUsed:  quota.UsedInstanceUnits,
				QuotaUnitsLimit: quota.MaxInstanceUnits,
				ConnectedUsed:   quota.UsedConnected,
				ConnectedLimit:  quota.MaxConnected,
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetShapes(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store != nil {
		shapes, err := s.store.ListShapes(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"default_shape": s.cfg.Compute.DefaultShape,
			"shapes":        shapes,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_shape": s.cfg.Compute.DefaultShape,
		"shapes":        s.cfg.Compute.Shapes,
	})
}

func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	vInfo := version.Get()
	release := vInfo.Release
	if release == "" {
		release = "dev"
	}
	resp := api.StatusResponse{
		OK:             !s.IsDraining(),
		Status:         "ok",
		Host:           s.cfg.Host,
		InstanceDomain: s.cfg.InstanceDomain,
		Release:        release,
		Revision:       vInfo.Revision,
		DefaultShape:   s.cfg.Compute.DefaultShape,
		Replicas:       []api.ReplicaStatus{},
		VersionCounts:  map[string]int{},
		Shapes:         []api.Shape{},
	}
	if s.IsDraining() {
		resp.Status = "draining"
	}
	if s.store != nil {
		entries, err := s.store.ListEntries(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		listedByName := make(map[string]bool, len(entries))
		for _, e := range entries {
			listedByName[e.Name] = e.Listed
			if !caller.IsAdmin && !e.Listed {
				continue
			}
			apiEntry := s.enrichAPIEntry(r.Context(), e, caller.IsAdmin)
			resp.TotalEntries++
			switch apiEntry.Status {
			case api.StatusOnline:
				resp.OnlineEntries++
			case api.StatusDegraded:
				resp.DegradedEntries++
			default:
				resp.OfflineEntries++
			}
			switch e.Kind {
			case api.KindInstance:
				resp.InstanceEntries++
			case api.KindConnected:
				resp.ConnectedCount++
			}
			ver := strings.TrimSpace(e.GrainVersion)
			if ver == "" {
				ver = "unreported"
			}
			resp.VersionCounts[ver]++
		}

		replicaTTL := s.cfg.Tunnel.ReplicaTTL
		if replicaTTL <= 0 {
			replicaTTL = DefaultTunnelReplicaTTL
		}
		now := s.store.Now()

		tunnels, err := s.store.ListActiveTunnelSessions(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		tunnelsByReplica := make(map[string]int)
		for _, ts := range tunnels {
			if now.Sub(ts.ReplicaLastSeen) > replicaTTL {
				continue
			}
			if !caller.IsAdmin && !listedByName[ts.EntryName] {
				continue
			}
			tunnelsByReplica[ts.ReplicaID]++
			resp.ActiveTunnels++
		}

		replicas, err := s.store.ListReplicas(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, rep := range replicas {
			healthy := now.Sub(rep.HeartbeatAt) <= replicaTTL
			peerAddr := rep.PeerAddr
			if !caller.IsAdmin {
				peerAddr = ""
			}
			resp.Replicas = append(resp.Replicas, api.ReplicaStatus{
				ReplicaID:     rep.ReplicaID,
				PeerAddr:      peerAddr,
				StartedAt:     rep.StartedAt,
				HeartbeatAt:   rep.HeartbeatAt,
				Healthy:       healthy,
				ActiveTunnels: tunnelsByReplica[rep.ReplicaID],
			})
		}
		shapes, err := s.store.ListShapes(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, sh := range shapes {
			resp.Shapes = append(resp.Shapes, api.Shape{
				Name:         sh.Name,
				MachineType:  sh.MachineType,
				BootDiskGB:   sh.BootDiskGB,
				BootDiskIOPS: sh.BootDiskIOPS,
				QuotaUnits:   sh.QuotaUnits,
				Description:  sh.Description,
				Default:      sh.Name == s.cfg.Compute.DefaultShape,
			})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetAdminSummary(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	out := api.AdminSummaryResponse{
		Replicas:       []api.ReplicaStatus{},
		TunnelSessions: []api.TunnelSessionInfo{},
		Owners:         []api.OwnerQuotaSummary{},
		Quotas:         []api.OwnerQuotaSummary{},
		ScopeConflicts: []api.AdminScopeConflict{},
		SQLInstances:   []api.SQLInstanceStatus{},
		Orphans:        []api.OrphanVMInfo{},
		DeletedEntries: []api.Entry{},
		VersionCounts:  map[string]int{},
		AuditLog:       []api.AuditLogEntry{},
	}
	if s.orphans != nil {
		if orph := s.orphans.ListOrphans(); len(orph) > 0 {
			out.Orphans = orph
		}
	}
	if s.store != nil {
		replicaTTL := s.cfg.Tunnel.ReplicaTTL
		if replicaTTL <= 0 {
			replicaTTL = DefaultTunnelReplicaTTL
		}
		now := s.store.Now()

		tunnels, err := s.store.ListActiveTunnelSessions(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		tunnelsByReplica := make(map[string]int)
		for _, ts := range tunnels {
			if now.Sub(ts.ReplicaLastSeen) > replicaTTL {
				continue
			}
			tunnelsByReplica[ts.ReplicaID]++
			out.TunnelSessions = append(out.TunnelSessions, api.TunnelSessionInfo{
				EntryName:    ts.EntryName,
				ReplicaID:    ts.ReplicaID,
				PeerAddr:     ts.PeerAddr,
				SessionEpoch: ts.SessionEpoch,
				RemoteAddr:   ts.RemoteAddr,
				ConnectedAt:  ts.ConnectedAt,
				LastSeenAt:   ts.LastSeenAt,
				RTTMs:        ts.RTTMs,
				CredID:       ts.CredID,
			})
		}
		replicas, err := s.store.ListReplicas(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, rep := range replicas {
			out.Replicas = append(out.Replicas, api.ReplicaStatus{
				ReplicaID:     rep.ReplicaID,
				PeerAddr:      rep.PeerAddr,
				StartedAt:     rep.StartedAt,
				HeartbeatAt:   rep.HeartbeatAt,
				Healthy:       now.Sub(rep.HeartbeatAt) <= replicaTTL,
				ActiveTunnels: tunnelsByReplica[rep.ReplicaID],
			})
		}
		if owners, err := s.store.ListOwnerQuotaSummaries(r.Context()); err == nil && len(owners) > 0 {
			out.Owners = owners
			out.Quotas = owners
		}
		if conflicts, err := s.store.ListAllScopeConflicts(r.Context()); err == nil && len(conflicts) > 0 {
			out.ScopeConflicts = conflicts
		}
		if sqlInsts, err := s.store.ListSQLInstanceStatuses(r.Context()); err == nil && len(sqlInsts) > 0 {
			out.SQLInstances = sqlInsts
		}
		if lease, err := s.store.GetLeaseStatus(r.Context(), "reconciler"); err == nil && lease != nil {
			out.ReconcilerLease = lease
		}
		if allEntries, err := s.store.ListEntriesFiltered(r.Context(), true); err == nil {
			for _, e := range allEntries {
				if e.DeletedAt != nil {
					out.DeletedEntries = append(out.DeletedEntries, s.enrichAPIEntry(r.Context(), e, true))
					continue
				}
				ver := strings.TrimSpace(e.GrainVersion)
				if ver == "" {
					ver = "unreported"
				}
				out.VersionCounts[ver]++
			}
		}
		audits, err := s.store.ListAuditLog(r.Context(), 100)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		for _, a := range audits {
			out.AuditLog = append(out.AuditLog, api.AuditLogEntry{
				ID:         a.ID,
				At:         a.At,
				ActorEmail: a.ActorEmail,
				Via:        a.Via,
				GrantID:    a.GrantID,
				Action:     a.Action,
				EntryID:    a.EntryID,
				EntryName:  a.EntryName,
				Detail:     a.Detail,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDirectory(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil || s.router == nil || s.router.CatalogCache() == nil {
		writeJSON(w, http.StatusOK, api.DirectoryResponse{Entries: []api.DirectoryEntry{}})
		return
	}
	qv := r.URL.Query()
	query := api.DirectoryQuery{
		Query:    qv.Get("q"),
		Repo:     qv.Get("repo"),
		Tag:      strings.Join(qv["tag"], ","),
		Status:   api.EntryStatus(qv.Get("status")),
		Kind:     api.EntryKind(qv.Get("kind")),
		HostKind: api.HostKind(qv.Get("host_kind")),
		Owner:    qv.Get("owner"),
		Cursor:   qv.Get("cursor"),
	}
	res, err := s.router.CatalogCache().Directory(r.Context(), query)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	qv := r.URL.Query()
	repo := strings.TrimSpace(qv.Get("repo"))
	if repo == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "query parameter \"repo\" is required")
		return
	}
	normRepo, err := api.NormalizeRepo(repo)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	filePath := qv.Get("path")
	normPath, err := api.NormalizePathPrefix(filePath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	rawIncludeOffline := strings.TrimSpace(qv.Get("include_offline"))
	if s.store == nil || s.router == nil || s.router.CatalogCache() == nil {
		writeJSON(w, http.StatusOK, api.RouteResponse{Repo: normRepo, Path: normPath, Candidates: []api.RouteCandidate{}})
		return
	}
	res, err := s.router.CatalogCache().Route(r.Context(), normRepo, normPath, rawIncludeOffline)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetEntryAvailability(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	name := r.PathValue("name")
	if s.store == nil {
		if err := api.ValidateEntryName(name); err != nil {
			writeJSON(w, http.StatusOK, store.NameAvailability{Name: name, State: "invalid", Status: api.NameInvalid, Reason: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, store.NameAvailability{Name: name, State: "available", Status: api.NameFree})
		return
	}
	avail, err := s.store.CheckNameAvailability(r.Context(), name, caller.Email)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, avail)
}

func (s *Server) handleGetEntries(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if caller.Kind != webauth.PrincipalHuman {
		writeJSONError(w, http.StatusForbidden, "forbidden", "service principals may only call read-only catalog endpoints")
		return
	}

	qv := r.URL.Query()
	wantAll := strings.EqualFold(qv.Get("all"), "true") || qv.Get("all") == "1"
	if wantAll && !caller.IsAdmin {
		writeJSONError(w, http.StatusForbidden, "forbidden", "only admins may list all entries")
		return
	}
	ownerFilter := strings.ToLower(strings.TrimSpace(qv.Get("owner")))
	if ownerFilter != "" && !caller.IsAdmin && !strings.EqualFold(ownerFilter, caller.Email) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "only admins may filter entries by another owner")
		return
	}
	ownerGroupFilter := normalizeGroupSpec(qv.Get("owner_group"))
	if ownerGroupFilter != "" && !caller.IsAdmin {
		ok, err := s.isGroupMember(r.Context(), ownerGroupFilter, caller.Email)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
			return
		}
		if !ok {
			writeJSONError(w, http.StatusForbidden, "forbidden", "caller is not a member of the requested owner_group")
			return
		}
	}

	kindFilter := strings.ToLower(strings.TrimSpace(qv.Get("kind")))
	statusFilter := strings.ToLower(strings.TrimSpace(qv.Get("status")))
	includeDeleted := strings.EqualFold(qv.Get("include_deleted"), "true") || qv.Get("include_deleted") == "1"
	mineOnly := strings.EqualFold(qv.Get("mine"), "true") || qv.Get("mine") == "1"
	var listedFilter *bool
	if rawListed := strings.TrimSpace(qv.Get("listed")); rawListed != "" {
		b := strings.EqualFold(rawListed, "true") || rawListed == "1"
		listedFilter = &b
	}
	var conflictFilter *bool
	if rawConflict := strings.TrimSpace(qv.Get("conflict")); rawConflict != "" {
		b := strings.EqualFold(rawConflict, "true") || rawConflict == "1"
		conflictFilter = &b
	}

	if s.store == nil {
		writeJSON(w, http.StatusOK, api.ListEntriesResponse{Entries: []api.Entry{}})
		return
	}

	entries, err := s.store.ListEntriesFiltered(r.Context(), includeDeleted)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	out := make([]api.Entry, 0, len(entries))
	for _, e := range entries {
		if ownerFilter != "" && !strings.EqualFold(e.OwnerEmail, ownerFilter) {
			continue
		}
		if ownerGroupFilter != "" && normalizeGroupSpec(e.OwnerGroup) != ownerGroupFilter {
			continue
		}
		if kindFilter != "" && !strings.EqualFold(string(e.Kind), kindFilter) {
			continue
		}
		if listedFilter != nil && e.Listed != *listedFilter {
			continue
		}

		isManager := caller.IsAdmin
		if !caller.IsAdmin || mineOnly {
			isOwner := strings.EqualFold(e.OwnerEmail, caller.Email)
			isPending := e.PendingOwner != "" && strings.EqualFold(e.PendingOwner, caller.Email)
			inOwnerGroup := false
			if !isOwner && normalizeGroupSpec(e.OwnerGroup) != "" {
				ok, err := s.isGroupMember(r.Context(), e.OwnerGroup, caller.Email)
				if err != nil {
					w.Header().Set("Retry-After", "5")
					writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
					return
				}
				inOwnerGroup = ok
			}
			isManager = caller.IsAdmin || isOwner || inOwnerGroup
			if !isOwner && !isPending && !inOwnerGroup {
				continue
			}
		}

		apiEntry := s.enrichAPIEntry(r.Context(), e, caller.IsAdmin)
		if !isManager && !caller.IsAdmin {
			apiEntry.Credentials = nil
			apiEntry.StatusDetail = ""
		}
		if statusFilter != "" && !strings.EqualFold(string(apiEntry.Status), statusFilter) && !strings.EqualFold(string(apiEntry.State), statusFilter) {
			continue
		}
		if conflictFilter != nil {
			hasConflict := len(apiEntry.ScopeConflicts) > 0
			if hasConflict != *conflictFilter {
				continue
			}
		}
		out = append(out, apiEntry)
	}
	writeJSON(w, http.StatusOK, api.ListEntriesResponse{Entries: out})
}

func scopeInputsToClaims(inputs []api.ScopeInput) []store.ScopeClaim {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]store.ScopeClaim, 0, len(inputs))
	for _, sc := range inputs {
		primary := sc.Role == "" || sc.Role == api.RolePrimary
		out = append(out, store.ScopeClaim{
			Repo:       sc.Repo,
			PathPrefix: sc.PathPrefix,
			Primary:    primary,
		})
	}
	return out
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if !caller.CanCreate && !caller.IsAdmin {
		writeJSONError(w, http.StatusForbidden, "forbidden", "caller is not permitted to create hub entries")
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}
	if s.router != nil {
		if ok, retryAfter := s.router.RateLimiters().CreatesPerUser.Allow(caller.Email); !ok {
			server.WriteRateLimited(w, retryAfter, "daily entry creation limit exceeded")
			return
		}
	}

	var req api.CreateEntryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}

	// Default `listed` to false on HTTP create when omitted (`hub.design.md`).
	if req.Listed == nil {
		defaultListed := false
		req.Listed = &defaultListed
	}

	if strings.TrimSpace(req.OwnerGroup) != "" && !caller.IsAdmin {
		ok, err := s.isGroupMember(r.Context(), req.OwnerGroup, caller.Email)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
			return
		}
		if !ok {
			writeJSONError(w, http.StatusForbidden, "forbidden", "caller must be a member of owner_group to assign it")
			return
		}
	}

	if req.Kind == api.KindInstance && strings.TrimSpace(req.Shape) == "" {
		if ds := strings.TrimSpace(s.cfg.Compute.DefaultShape); ds != "" {
			req.Shape = ds
		} else if len(s.cfg.Compute.Shapes) > 0 {
			req.Shape = s.cfg.Compute.Shapes[0].Name
		}
	}

	declaredClaims := scopeInputsToClaims(req.Scopes)
	if !s.checkPrimaryScopeClaims(w, r, declaredClaims, caller.Email, req.OwnerGroup) {
		return
	}

	created, err := s.store.CreateEntry(r.Context(), store.CreateEntryInput{
		Name:               req.Name,
		Kind:               req.Kind,
		OwnerEmail:         caller.Email,
		OwnerGroup:         req.OwnerGroup,
		ConnectedPrincipal: req.ConnectedPrincipal,
		HostKind:           string(req.HostKind),
		Listed:             req.Listed,
		DisplayName:        req.DisplayName,
		Purpose:            req.Purpose,
		Tags:               req.Tags,
		Shape:              req.Shape,
		BootDiskGB:         req.BootDiskGB,
		Zone:               req.Zone,
		AllowSSH:           req.AllowSSH,
		ShipLogs:           req.ShipLogs,
		AutoUpgrade:        req.AutoUpgrade,
		AutoWake:           req.AutoWake,
		DeclaredScopes:     declaredClaims,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	var rawToken string
	var credMeta *api.CredentialMeta
	if created.Kind == api.KindConnected {
		label := strings.TrimSpace(req.CredentialLabel)
		if label == "" {
			label = "default"
		}
		tok, meta, credErr := s.store.IssueCredentialWithMeta(r.Context(), created.ID, label, caller.Email)
		if credErr != nil {
			writeInternalError(w, credErr)
			return
		}
		rawToken = tok
		credMeta = &meta
		if fresh, gErr := s.store.GetEntryByID(r.Context(), created.ID); gErr == nil {
			created = fresh
		}
	}

	if s.router != nil {
		s.router.HostCache().Put(created)
	}

	apiEntry := s.enrichAPIEntry(r.Context(), created, caller.IsAdmin)
	status := http.StatusCreated
	if created.Kind == api.KindInstance {
		status = http.StatusAccepted
	}
	writeJSON(w, status, api.CreateEntryResponse{
		Entry:             apiEntry,
		RegistrationToken: rawToken,
		Credential:        credMeta,
	})
}

func (s *Server) handleGetEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "entry not found")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, err := s.store.GetEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		if delEntry, delErr := s.store.GetLatestDeletedEntryByName(r.Context(), name); delErr == nil && delEntry != nil {
			entry = delEntry
		} else {
			writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
			return
		}
	} else if err != nil {
		writeInternalError(w, err)
		return
	}

	isManager, err := s.canCallerManageEntry(r.Context(), caller, entry)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		return
	}
	isPendingOwner := entry.PendingOwner != "" && strings.EqualFold(entry.PendingOwner, caller.Email)

	if !isManager && !isPendingOwner {
		// Somebody else's deleted entry is gone as far as this caller is
		// concerned: answer 404 rather than 403, so `grain hub register`
		// goes on to claim a name whose reservation has lapsed or been
		// released instead of stopping at "permission to view", and so the
		// tombstone's existence is not disclosed (GET /api/names reports
		// reservations).
		if entry.LifecycleState == api.StateDeleted || entry.DeletedAt != nil {
			writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
			return
		}
		if !entry.Listed {
			writeJSONError(w, http.StatusForbidden, "forbidden", "caller does not have permission to view this entry")
			return
		}
	}

	apiEntry := s.enrichAPIEntry(r.Context(), entry, caller.IsAdmin)
	if !isManager && !caller.IsAdmin {
		apiEntry.Credentials = nil
		apiEntry.StatusDetail = ""
	}
	writeJSON(w, http.StatusOK, apiEntry)
}

func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	var req api.UpdateEntryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}

	if req.OwnerGroup != nil {
		targetGroup := normalizeGroupSpec(*req.OwnerGroup)
		currentGroup := normalizeGroupSpec(entry.OwnerGroup)
		if targetGroup != "" && targetGroup != currentGroup && !caller.IsAdmin {
			member, err := s.isGroupMember(r.Context(), targetGroup, caller.Email)
			if err != nil {
				w.Header().Set("Retry-After", "5")
				writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
				return
			}
			if !member {
				writeJSONError(w, http.StatusForbidden, "forbidden", "caller must be a member of the target owner_group")
				return
			}
		}
	}

	if req.OwnerGroup != nil || (req.Listed != nil && *req.Listed && !entry.Listed) {
		nextGroup := entry.OwnerGroup
		if req.OwnerGroup != nil {
			nextGroup = *req.OwnerGroup
		}
		if !s.checkPrimaryScopeClaims(w, r, scopeRowsToClaims(entry.Scopes, "declared"), entry.OwnerEmail, nextGroup) {
			return
		}
	}

	prevState := entry.LifecycleState
	updated, err := s.store.UpdateEntryCatalog(r.Context(), entry.ID, entry.Version, store.UpdateEntryCatalogInput{
		DisplayName:        req.DisplayName,
		Purpose:            req.Purpose,
		HostKind:           req.HostKind,
		ConnectedPrincipal: req.ConnectedPrincipal,
		OwnerGroup:         req.OwnerGroup,
		Listed:             req.Listed,
		Tags:               req.Tags,
		Shape:              req.Shape,
		AllowSSH:           req.AllowSSH,
		ShipLogs:           req.ShipLogs,
		AutoUpgrade:        req.AutoUpgrade,
		AutoWake:           req.AutoWake,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if req.OwnerGroup != nil || (req.Listed != nil && *req.Listed && !entry.Listed) {
		if demoted, dErr := s.demoteUnauthorizedPrimaryScopes(r.Context(), updated, false); dErr == nil && demoted != nil {
			updated = demoted
		}
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_update", updated.ID, updated.Name, nil)

	if s.router != nil {
		s.router.HostCache().Put(updated)
		if req.ConnectedPrincipal != nil || req.OwnerGroup != nil {
			s.router.ReconcileLocalTunnel(r.Context(), updated.Name)
		}
	}

	status := http.StatusOK
	if updated.LifecycleState == api.StateReplacing && prevState != api.StateReplacing {
		status = http.StatusAccepted
	}
	writeJSON(w, status, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleSetScopes(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	var req api.SetScopesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}

	claims := scopeInputsToClaims(req.Scopes)
	if !s.checkPrimaryScopeClaims(w, r, claims, entry.OwnerEmail, entry.OwnerGroup) {
		return
	}

	updated, err := s.store.SetDeclaredScopes(r.Context(), entry.ID, claims)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_scopes_update", updated.ID, updated.Name, nil)
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}

	conflicts := s.store.EntryScopeConflicts(r.Context(), updated)
	if conflicts == nil {
		conflicts = []api.ScopeConflict{}
	}
	writeJSON(w, http.StatusOK, api.SetScopesResponse{
		Scopes:         store.CombinedAPIScopes(updated.Scopes),
		ReportedScopes: store.RawAPIScopes(updated.Scopes, api.SourceReported),
		DeclaredScopes: store.RawAPIScopes(updated.Scopes, api.SourceDeclared),
		Conflicts:      conflicts,
	})
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	var req api.DeleteEntryRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
			return
		}
	}
	confirmName := strings.TrimSpace(req.Confirm)
	if confirmName == "" {
		confirmName = strings.TrimSpace(req.ConfirmName)
	}
	if qConfirm := strings.TrimSpace(r.URL.Query().Get("confirm")); qConfirm != "" && confirmName == "" {
		confirmName = qConfirm
	}
	if confirmName != "" && !strings.EqualFold(confirmName, entry.Name) {
		writeJSONError(w, http.StatusBadRequest, "confirmation_mismatch", fmt.Sprintf("confirm value %q does not match entry name %q", confirmName, entry.Name))
		return
	}

	if _, err := s.store.SoftDeleteEntryWithOptions(r.Context(), entry.ID, entry.Version, req.DeleteDisk); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_delete", entry.ID, entry.Name, map[string]any{"quota_owner": entry.OwnerEmail})

	if s.router != nil {
		s.router.HostCache().Invalidate(entry.Name)
		s.router.ReconcileLocalTunnel(r.Context(), entry.Name)
	}

	status := http.StatusOK
	if entry.Kind == api.KindInstance {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{
		"status": "deleted",
		"name":   entry.Name,
	})
}

func (s *Server) handleRotateCredential(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}
	if entry.Kind != api.KindConnected {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"connected\" entries have registration credentials")
		return
	}

	var req api.RotateCredentialRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
			return
		}
	}

	rawToken, meta, err := s.store.RotateCredential(r.Context(), entry.ID, req, caller.Email)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.router != nil && (req.RevokePrevious || strings.TrimSpace(req.CredID) != "") {
		s.router.ReconcileLocalTunnel(r.Context(), entry.Name)
	}
	writeJSON(w, http.StatusOK, api.RotateCredentialResponse{
		RegistrationToken: rawToken,
		Credential:        meta,
	})
}

func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	credID := strings.TrimSpace(r.PathValue("id"))
	if credID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "credential id is required")
		return
	}
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	if err := s.store.RevokeCredential(r.Context(), entry.ID, credID); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "credential_revoke", entry.ID, entry.Name, map[string]any{"cred_id": credID})
	if s.router != nil {
		s.router.ReconcileLocalTunnel(r.Context(), entry.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "revoked",
		"cred_id": credID,
	})
}

func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}
	creds, err := s.store.ListEntryCredentials(r.Context(), entry.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if creds == nil {
		creds = []api.CredentialMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
}

func (s *Server) handleUpgradeEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}
	if entry.Kind != api.KindInstance {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"instance\" entries can be upgraded")
		return
	}

	var req api.UpgradeEntryRequest
	if r.Body != nil && r.Body != http.NoBody {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	}

	updated, err := s.store.RequestEntryUpgrade(r.Context(), entry.ID, entry.Version, req.TargetVersion)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_upgrade", updated.ID, updated.Name, nil)
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}
	writeJSON(w, http.StatusAccepted, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleReplaceEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	// Shapes, boot disks and VM replacement only mean something for hosted
	// instances: a connected entry's grain runs on its owner's machine, and
	// putting it into "replacing" stops the registry routing to its tunnel.
	if entry.Kind != api.KindInstance {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"instance\" entries can be resized or replaced")
		return
	}

	var req api.ReplaceEntryRequest
	if r.Body != nil && r.Body != http.NoBody {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	}

	updated, err := s.store.RequestEntryReplaceWithDisk(r.Context(), entry.ID, entry.Version, req.Shape, req.TargetVersion, req.BootDiskGB)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_replace", updated.ID, updated.Name, map[string]any{"quota_owner": updated.OwnerEmail})
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}
	writeJSON(w, http.StatusAccepted, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleStopEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}
	if entry.Kind != api.KindInstance {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"instance\" entries can be stopped")
		return
	}
	if entry.LifecycleState == api.StateStopping || entry.LifecycleState == api.StateStopped {
		writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), entry, caller.IsAdmin))
		return
	}

	updated, err := s.store.BeginStopEntry(r.Context(), entry.ID, entry.Version, "stopping instance")
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_stop", updated.ID, updated.Name, nil)
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}
	writeJSON(w, http.StatusAccepted, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleStartEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}
	if entry.Kind != api.KindInstance {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_kind", "only kind=\"instance\" entries can be started")
		return
	}
	if entry.LifecycleState == api.StateCreating || entry.LifecycleState == api.StateRunning {
		writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), entry, caller.IsAdmin))
		return
	}

	updated, err := s.store.BeginStartEntry(r.Context(), entry.ID, entry.Version, "starting instance")
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_start", updated.ID, updated.Name, nil)
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}
	writeJSON(w, http.StatusAccepted, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleRestoreEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, true)
	if !ok {
		return
	}

	restored, err := s.store.RestoreEntry(r.Context(), entry.ID, entry.Version)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_restore", restored.ID, restored.Name, map[string]any{"quota_owner": restored.OwnerEmail})
	if s.router != nil {
		s.router.HostCache().Put(restored)
	}
	status := http.StatusOK
	if restored.Kind == api.KindInstance {
		status = http.StatusAccepted
	}
	writeJSON(w, status, s.enrichAPIEntry(r.Context(), restored, caller.IsAdmin))
}

func (s *Server) handleOfferTransfer(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, ok := s.loadAndAuthorizeManagedEntry(w, r, caller, name, false)
	if !ok {
		return
	}

	var req api.TransferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}

	targetOwner := strings.ToLower(strings.TrimSpace(req.To))
	if targetOwner == "" {
		targetOwner = strings.ToLower(strings.TrimSpace(req.NewOwner))
	}
	if req.Cancel {
		targetOwner = ""
	}
	if targetOwner != "" && !strings.Contains(targetOwner, "@") {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "valid target owner email is required")
		return
	}

	var ownerGroupPtr *string
	if req.ClearOwnerGroup {
		empty := ""
		ownerGroupPtr = &empty
	} else if strings.TrimSpace(req.OwnerGroup) != "" {
		targetGroup := normalizeGroupSpec(req.OwnerGroup)
		currentGroup := normalizeGroupSpec(entry.OwnerGroup)
		if targetGroup != currentGroup && !caller.IsAdmin {
			member, err := s.isGroupMember(r.Context(), targetGroup, caller.Email)
			if err != nil {
				w.Header().Set("Retry-After", "5")
				writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
				return
			}
			if !member {
				writeJSONError(w, http.StatusForbidden, "forbidden", "caller must be a member of the target owner_group")
				return
			}
		}
		ownerGroupPtr = &targetGroup
	}

	if req.Force {
		if !caller.IsAdmin {
			writeJSONError(w, http.StatusForbidden, "forbidden", "only hub admins may force-transfer entry ownership")
			return
		}
		if targetOwner == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "force transfer requires a non-empty target owner email")
			return
		}
		s.performAdminForceTransfer(w, r, caller, entry, targetOwner, ownerGroupPtr)
		return
	}

	if targetOwner == "" && !req.Cancel && ownerGroupPtr == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "transfer requires 'to' email (or 'cancel: true' to cancel a pending offer)")
		return
	}
	if !req.Cancel {
		nextGroup := entry.OwnerGroup
		if ownerGroupPtr != nil {
			nextGroup = *ownerGroupPtr
		}
		declared := scopeRowsToClaims(entry.Scopes, "declared")
		if ownerGroupPtr != nil {
			if !s.checkPrimaryScopeClaims(w, r, declared, entry.OwnerEmail, nextGroup) {
				return
			}
		}
		if targetOwner != "" {
			if !s.checkPrimaryScopeClaims(w, r, declared, targetOwner, nextGroup) {
				return
			}
		}
	}

	updated, err := s.store.OfferEntryTransfer(r.Context(), entry.ID, entry.Version, targetOwner, ownerGroupPtr)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if ownerGroupPtr != nil {
		if demoted, dErr := s.demoteUnauthorizedPrimaryScopes(r.Context(), updated, false); dErr == nil && demoted != nil {
			updated = demoted
		}
	}
	auditAction := "entry_transfer_offer"
	if targetOwner == "" {
		auditAction = "entry_transfer_cancel"
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, auditAction, updated.ID, updated.Name, map[string]any{
		"from":        entry.OwnerEmail,
		"to":          updated.PendingOwner,
		"owner_group": updated.OwnerGroup,
		"expires_at":  updated.TransferExpiresAt,
	})
	if s.router != nil {
		s.router.HostCache().Put(updated)
	}
	writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) performAdminForceTransfer(w http.ResponseWriter, r *http.Request, caller *webauth.AdmittedCaller, entry *store.EntryRow, targetOwner string, ownerGroupPtr *string) {
	nextGroup := entry.OwnerGroup
	if ownerGroupPtr != nil {
		nextGroup = *ownerGroupPtr
	}
	if entry.Kind == api.KindInstance && entry.SSHGrantedTo != "" && s.resources != nil {
		_ = s.resources.RevokeSSH(r.Context(), entry.Zone, entry.GCEInstanceID, entry.SSHGrantedTo)
	}
	updated, err := s.store.TransferEntryOwner(r.Context(), entry.ID, entry.Version, targetOwner, nextGroup)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if demoted, dErr := s.demoteUnauthorizedPrimaryScopes(r.Context(), updated, true); dErr == nil && demoted != nil {
		updated = demoted
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_entry_force_transfer", updated.ID, updated.Name, map[string]any{
		"from":        entry.OwnerEmail,
		"to":          updated.OwnerEmail,
		"quota_owner": updated.OwnerEmail,
		"owner_group": updated.OwnerGroup,
		"forced":      true,
	})
	if s.router != nil {
		s.router.HostCache().Put(updated)
		s.router.ReconcileLocalTunnel(r.Context(), updated.Name)
	}
	writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleAcceptTransfer(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, err := s.store.GetEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if strings.TrimSpace(entry.PendingOwner) == "" || !strings.EqualFold(entry.PendingOwner, caller.Email) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "only the named pending transfer recipient may accept this transfer")
		return
	}
	if !s.checkPrimaryScopeClaims(w, r, scopeRowsToClaims(entry.Scopes, "declared"), caller.Email, entry.OwnerGroup) {
		return
	}
	if entry.Kind == api.KindInstance && entry.SSHGrantedTo != "" && s.resources != nil {
		_ = s.resources.RevokeSSH(r.Context(), entry.Zone, entry.GCEInstanceID, entry.SSHGrantedTo)
	}

	updated, err := s.store.AcceptEntryTransfer(r.Context(), entry.ID, entry.Version, caller.Email)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if demoted, dErr := s.demoteUnauthorizedPrimaryScopes(r.Context(), updated, false); dErr == nil && demoted != nil {
		updated = demoted
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "entry_transfer_accept", updated.ID, updated.Name, map[string]any{
		"from":        entry.OwnerEmail,
		"to":          updated.OwnerEmail,
		"quota_owner": updated.OwnerEmail,
		"owner_group": updated.OwnerGroup,
	})
	if s.router != nil {
		s.router.HostCache().Put(updated)
		s.router.ReconcileLocalTunnel(r.Context(), updated.Name)
	}
	writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), updated, caller.IsAdmin))
}

func (s *Server) handleAdminForceTransfer(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, err := s.store.GetEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	var req api.TransferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	targetOwner := strings.ToLower(strings.TrimSpace(req.To))
	if targetOwner == "" {
		targetOwner = strings.ToLower(strings.TrimSpace(req.NewOwner))
	}
	if targetOwner == "" || !strings.Contains(targetOwner, "@") {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "valid target owner email is required")
		return
	}
	var ownerGroupPtr *string
	if req.ClearOwnerGroup {
		empty := ""
		ownerGroupPtr = &empty
	} else if strings.TrimSpace(req.OwnerGroup) != "" {
		g := normalizeGroupSpec(req.OwnerGroup)
		ownerGroupPtr = &g
	}
	s.performAdminForceTransfer(w, r, caller, entry, targetOwner, ownerGroupPtr)
}

func (s *Server) handleAdminSetOwnerDisabled(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" || !strings.Contains(email, "@") {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "valid owner email is required")
		return
	}
	var req api.SetOwnerDisabledRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if err := s.store.SetOwnerDisabled(r.Context(), email, req.Disabled); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if req.Disabled && s.router != nil {
		if entries, err := s.store.ListEntries(r.Context()); err == nil {
			for _, e := range entries {
				if strings.EqualFold(e.OwnerEmail, email) {
					s.router.HostCache().Put(e)
					s.router.ReconcileLocalTunnel(r.Context(), e.Name)
				}
			}
		}
	}
	action := "admin_owner_enable"
	if req.Disabled {
		action = "admin_owner_disable"
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, action, "", "", map[string]any{
		"owner_email": email,
		"disabled":    req.Disabled,
	})
	sum, err := s.store.GetOwnerQuotaSummary(r.Context(), email)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleAdminSetQuota(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" || !strings.Contains(email, "@") {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "valid owner email is required")
		return
	}
	var req api.SetQuotaRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if req.MaxInstanceUnits < 0 || req.MaxConnected < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "quota limits must be non-negative")
		return
	}
	if err := s.store.SetQuota(r.Context(), email, req.MaxInstanceUnits, req.MaxConnected, caller.Email); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_quota_set", "", "", map[string]any{
		"owner_email":        email,
		"max_instance_units": req.MaxInstanceUnits,
		"max_connected":      req.MaxConnected,
	})
	sum, err := s.store.GetOwnerQuotaSummary(r.Context(), email)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleAdminResetQuota(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" || !strings.Contains(email, "@") {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "valid owner email is required")
		return
	}
	if err := s.store.DeleteQuota(r.Context(), email); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_quota_reset", "", "", map[string]any{
		"owner_email": email,
	})
	sum, err := s.store.GetOwnerQuotaSummary(r.Context(), email)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleAdminResolveConflict(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	var req api.ResolveConflictRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.EntryName) == "" || strings.TrimSpace(req.Repo) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "entry_name and repo are required")
		return
	}
	if err := s.store.ResolveScopeConflict(r.Context(), req.EntryName, req.Repo, req.PathPrefix, req.Resolution); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_conflict_resolve", "", strings.ToLower(strings.TrimSpace(req.EntryName)), map[string]any{
		"repo":        req.Repo,
		"path_prefix": req.PathPrefix,
		"resolution":  req.Resolution,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminSetSQLDraining(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	var req api.SetSQLInstanceDrainingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if err := s.store.SetSQLInstanceDraining(r.Context(), name, req.Draining); err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_sql_instance_draining", "", "", map[string]any{
		"sql_instance": name,
		"draining":     req.Draining,
	})
	st, err := s.store.GetSQLInstanceStatus(r.Context(), name)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleAdminDeleteOrphan(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	zone := strings.TrimSpace(r.PathValue("zone"))
	instID := strings.TrimSpace(r.PathValue("instance"))
	if zone == "" || instID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "zone and instance are required")
		return
	}
	if s.orphans != nil {
		if err := s.orphans.DeleteOrphan(r.Context(), zone, instID); err != nil {
			writeInternalError(w, err)
			return
		}
	} else if s.compute != nil {
		if err := s.compute.DeleteInstance(r.Context(), zone, instID); err != nil {
			writeInternalError(w, err)
			return
		}
	} else {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "compute provider is not configured")
		return
	}
	if s.store != nil {
		_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_orphan_delete", "", "", map[string]any{
			"zone":        zone,
			"instance_id": instID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "zone": zone, "instance_id": instID})
}

func (s *Server) handleAdminDisconnectTunnel(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	entry, err := s.store.GetEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if err := s.store.DisconnectEntryTunnel(r.Context(), entry.ID, entry.Name, "admin disconnected tunnel"); err != nil {
		writeInternalError(w, err)
		return
	}
	if s.router != nil {
		s.router.ReconcileLocalTunnel(r.Context(), entry.Name)
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_tunnel_disconnect", entry.ID, entry.Name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": true, "entry_name": entry.Name})
}

func (s *Server) handleAdminPurgeEntry(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	var req api.HardPurgeRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	}

	entry, err := s.store.GetLatestDeletedEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) && req.Force {
		entry, err = s.store.GetEntryByName(r.Context(), name)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("deleted entry %q not found", name))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if entry.DeletedAt == nil && !req.Force {
		writeJSONError(w, http.StatusConflict, "conflict", "entry is still active; delete it first or pass force=true")
		return
	}

	clearCols := entry.Kind != api.KindInstance
	if entry.Kind == api.KindInstance {
		if s.compute != nil && entry.GCEInstanceID != "" && entry.Zone != "" {
			if err := s.compute.DeleteInstance(r.Context(), entry.Zone, entry.GCEInstanceID); err != nil {
				writeInternalError(w, err)
				return
			}
		}
		if s.configRepos != nil && s.cfg.ConfigRepos.Provider != "" && s.cfg.ConfigRepos.Provider != "none" {
			if err := s.configRepos.ArchiveRepo(r.Context(), entry.Name); err != nil {
				writeInternalError(w, err)
				return
			}
		}
		if s.resources != nil {
			resSpec := InstanceResourceSpec{
				EntryID:     entry.ID,
				Name:        entry.Name,
				OwnerEmail:  entry.OwnerEmail,
				SQLInstance: entry.SQLInstance,
				ShipLogs:    entry.ShipLogs,
				AllowSSH:    entry.AllowSSH,
			}
			resBundle := InstanceResourceBundle{
				ServiceAccountEmail: entry.ServiceAccount,
				SQLInstance:         entry.SQLInstance,
				DatabaseName:        entry.DBName,
				SecretPrefix:        entry.SecretPrefix,
			}
			if err := s.resources.Teardown(r.Context(), resSpec, resBundle, false); err != nil {
				writeInternalError(w, err)
				return
			}
			clearCols = true
		} else if entry.DBName == "" && entry.ServiceAccount == "" && entry.SecretPrefix == "" && entry.ConfigRepo == "" {
			clearCols = true
		}
	}

	purged, err := s.store.HardPurgeEntry(r.Context(), entry.ID, clearCols, req.ReleaseReservation)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.router != nil {
		s.router.HostCache().Invalidate(purged.Name)
		s.router.ReconcileLocalTunnel(r.Context(), purged.Name)
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_entry_purge", purged.ID, purged.Name, map[string]any{
		"quota_owner":         purged.OwnerEmail,
		"release_reservation": req.ReleaseReservation,
	})
	writeJSON(w, http.StatusOK, s.enrichAPIEntry(r.Context(), purged, true))
}

func (s *Server) loadAndAuthorizeManagedEntry(w http.ResponseWriter, r *http.Request, caller *webauth.AdmittedCaller, name string, includeDeleted bool) (*store.EntryRow, bool) {
	entry, err := s.store.GetEntryByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) && includeDeleted {
		entry, err = s.store.GetLatestDeletedEntryByName(r.Context(), name)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", name))
		return nil, false
	}
	if err != nil {
		writeInternalError(w, err)
		return nil, false
	}
	allowed, err := s.canCallerManageEntry(r.Context(), caller, entry)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		return nil, false
	}
	if !allowed {
		writeJSONError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("caller %q is not authorized to manage entry %q", caller.Email, name))
		return nil, false
	}
	return entry, true
}

func (s *Server) enrichAPIEntry(ctx context.Context, e *store.EntryRow, isAdmin bool) api.Entry {
	out := s.store.ToAPIEntry(e, s.cfg.InstanceDomain)
	if !isAdmin {
		out.BackendURL = ""
	}
	if s.store != nil {
		out.ScopeConflicts = s.store.EntryScopeConflicts(ctx, e)
		if creds, err := s.store.ListEntryCredentials(ctx, e.ID); err == nil && len(creds) > 0 {
			out.Credentials = creds
		}
	}
	if s.router != nil && s.router.TunnelCache() != nil {
		now := time.Now().UTC()
		if s.store != nil {
			now = s.store.Now()
		}
		replicaTTL := s.cfg.Tunnel.ReplicaTTL
		if replicaTTL <= 0 {
			replicaTTL = DefaultTunnelReplicaTTL
		}
		if tc, ok := s.router.TunnelCache().Get(e.Name); ok {
			connected := tc.HasActiveSession(now, replicaTTL)
			out.TunnelConnected = &connected
			if connected && !tc.ConnectedAt.IsZero() {
				ca := tc.ConnectedAt
				out.ConnectedSince = &ca
			}
			if !tc.LastSeenAt.IsZero() && (out.LastSeenAt == nil || tc.LastSeenAt.After(*out.LastSeenAt)) {
				ls := tc.LastSeenAt
				out.LastSeenAt = &ls
			}
		} else if e.Kind == api.KindConnected {
			connected := false
			out.TunnelConnected = &connected
		}
		if s.router.CatalogCache() != nil {
			liveStatus, ageSec := s.router.CatalogCache().DeriveLiveStatusAndAge(e)
			out.Status = liveStatus
			out.HeartbeatAgeSeconds = ageSec
		}
	}
	return out
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	var pce *store.PrimaryScopeConflictError
	if errors.As(err, &pce) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":       pce.Error(),
			"code":        "primary_scope_conflict",
			"conflict":    pce.Conflict,
			"name":        pce.Conflict.ConflictingEntry,
			"owner":       pce.Conflict.ConflictingOwner,
			"owner_group": pce.Conflict.ConflictingOwnerGroup,
			"repo":        pce.Conflict.Repo,
			"path_prefix": pce.Conflict.ConflictingPrefix,
		})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrQuotaExceeded):
		writeJSONError(w, http.StatusForbidden, "quota_exceeded", err.Error())
	case errors.Is(err, store.ErrNameReserved):
		writeJSONError(w, http.StatusConflict, "name_reserved", err.Error())
	case errors.Is(err, store.ErrPrimaryScopeConflict):
		writeJSONError(w, http.StatusConflict, "primary_scope_conflict", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeJSONError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, store.ErrUnknownShape):
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
	}
}

func (s *Server) handleInternalHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	authHdr := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authHdr, "Bearer ") {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "Bearer grc_ token required")
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
	entry, err := s.store.AuthenticateCredential(r.Context(), token)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "invalid or revoked credential")
		return
	}
	var req api.HeartbeatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	updated, err := s.store.RecordHeartbeat(r.Context(), entry.ID, req)
	if err != nil {
		if errors.Is(err, store.ErrPrimaryScopeConflict) {
			writeJSONError(w, http.StatusConflict, "scope_conflict", err.Error())
			return
		}
		writeInternalError(w, err)
		return
	}
	s.metrics.IncHeartbeats()
	writeJSON(w, http.StatusOK, api.HeartbeatResponse{
		Status:               s.store.DeriveStatus(updated),
		NextIntervalSeconds:  server.HeartbeatIntervalSeconds(s.cfg.Catalog.HeartbeatInterval),
		NextHeartbeatSeconds: server.HeartbeatIntervalSeconds(s.cfg.Catalog.HeartbeatInterval),
	})
}

func (s *Server) serveEntrySubdomain(w http.ResponseWriter, r *http.Request, entryName string) {
	caller, err := s.admitter.Admit(r)
	if err != nil {
		s.metrics.IncAdmissionRejected()
		switch {
		case errors.Is(err, webauth.ErrGroupLookupUnavailable):
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		case errors.Is(err, webauth.ErrCallerForbidden):
			writeJSONError(w, http.StatusForbidden, "forbidden", err.Error())
		default:
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		}
		return
	}
	if caller.Kind != webauth.PrincipalHuman {
		writeJSONError(w, http.StatusForbidden, "forbidden", "service principals cannot access instance subdomains")
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", entryName))
		return
	}
	entry, err := s.store.GetEntryByName(r.Context(), entryName)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("entry %q not found", entryName))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	allowed, err := s.canCallerAccessEntry(r.Context(), caller, entry)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
		return
	}
	if !allowed {
		writeJSONError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("caller %q does not have access to %q", caller.Email, entryName))
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, "entry_not_ready", fmt.Sprintf("entry %q (%s) backend proxy is not yet active", entry.Name, entry.Kind))
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

func (s *Server) isGroupMember(ctx context.Context, groupSpec, email string) (bool, error) {
	g := normalizeGroupSpec(groupSpec)
	if g == "" {
		return false, nil
	}
	return s.matchesAccessList(ctx, email, []string{g})
}

func (s *Server) canCallerManageEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || e == nil || caller.Kind != webauth.PrincipalHuman {
		return false, nil
	}
	if caller.IsAdmin || strings.EqualFold(e.OwnerEmail, caller.Email) {
		return true, nil
	}
	if g := normalizeGroupSpec(e.OwnerGroup); g != "" {
		return s.matchesAccessList(ctx, caller.Email, []string{g})
	}
	return false, nil
}

func (s *Server) canCallerAccessEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || e == nil || caller.Kind != webauth.PrincipalHuman {
		return false, nil
	}
	if caller.IsAdmin || strings.EqualFold(e.OwnerEmail, caller.Email) || e.Visibility == store.VisibilityPublic {
		return true, nil
	}
	if g := normalizeGroupSpec(e.OwnerGroup); g != "" {
		ok, err := s.matchesAccessList(ctx, caller.Email, []string{g})
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	if e.Visibility == store.VisibilityShared {
		return s.matchesAccessList(ctx, caller.Email, e.AccessList)
	}
	return false, nil
}

func (s *Server) matchesAccessList(ctx context.Context, email string, accessList []string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, spec := range accessList {
		spec = strings.TrimSpace(spec)
		switch {
		case strings.HasPrefix(spec, "user:"):
			u := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec, "user:")))
			if u == email {
				return true, nil
			}
		case strings.HasPrefix(spec, "group:"):
			if s.groups == nil {
				return false, errors.New("group resolver not configured")
			}
			gEmail := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec, "group:")))
			ok, err := s.groups.IsMember(ctx, gEmail, email)
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

func scopeRowsToClaims(rows []store.ScopeRow, source string) []store.ScopeClaim {
	var out []store.ScopeClaim
	for _, r := range rows {
		if r.Source != source {
			continue
		}
		out = append(out, store.ScopeClaim{
			Repo:       r.Repo,
			PathPrefix: r.PathPrefix,
			Primary:    r.PrimaryClaim,
			Branch:     r.Branch,
		})
	}
	return out
}

// authorizePrimaryScopeOwner evaluates catalog.scope_owners for (repo, ownerEmail, ownerGroup).
// When no rule matches repo, primary claims remain open to any admitted entry manager.
func (s *Server) authorizePrimaryScopeOwner(ctx context.Context, repo, ownerEmail, ownerGroup string) (bool, error) {
	rule, restricted := MatchScopeOwnerRule(s.cfg.Catalog.ScopeOwners, repo)
	if !restricted {
		return true, nil
	}
	normOwner := strings.ToLower(strings.TrimSpace(ownerEmail))
	normGroup := normalizeGroupSpec(ownerGroup)
	var groupSpecs []string
	for _, p := range rule.Principals {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "user:"):
			if normOwner != "" && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(p, "user:")), normOwner) {
				return true, nil
			}
		case strings.HasPrefix(p, "group:"):
			if normGroup != "" && strings.EqualFold(p, normGroup) {
				return true, nil
			}
			groupSpecs = append(groupSpecs, p)
		}
	}
	if len(groupSpecs) == 0 || normOwner == "" {
		return false, nil
	}
	var firstErr error
	for _, spec := range groupSpecs {
		ok, err := s.matchesAccessList(ctx, normOwner, []string{spec})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			return true, nil
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return false, nil
}

func (s *Server) checkPrimaryScopeClaims(w http.ResponseWriter, r *http.Request, claims []store.ScopeClaim, ownerEmail, ownerGroup string) bool {
	if len(s.cfg.Catalog.ScopeOwners) == 0 || len(claims) == 0 {
		return true
	}
	for _, sc := range claims {
		if !sc.Primary {
			continue
		}
		allowed, err := s.authorizePrimaryScopeOwner(r.Context(), sc.Repo, ownerEmail, ownerGroup)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
			return false
		}
		if !allowed {
			normRepo, rErr := api.NormalizeRepo(sc.Repo)
			if rErr != nil {
				normRepo = strings.ToLower(strings.TrimSpace(sc.Repo))
			}
			writeJSONError(w, http.StatusForbidden, "scope_owner_forbidden",
				fmt.Sprintf("entry owner %q is not authorized to claim a primary scope on %q", strings.ToLower(strings.TrimSpace(ownerEmail)), normRepo))
			return false
		}
	}
	return true
}

func (s *Server) demoteUnauthorizedPrimaryScopes(ctx context.Context, entry *store.EntryRow, demoteDeclared bool) (*store.EntryRow, error) {
	return s.demoteUnauthorizedPrimaryScopesWithPolicy(ctx, entry, demoteDeclared, true)
}

// demoteUnauthorizedPrimaryScopesWithPolicy demotes entry's primary scopes
// that catalog.scope_owners does not authorize. demoteOnLookupError decides
// what a GroupResolver error does: true demotes (fail closed, used right
// after an ownership change), false leaves the claim as it is (used by the
// startup sweep, so a transient resolver outage at boot cannot permanently
// rewrite a legitimately-held declared primary scope).
func (s *Server) demoteUnauthorizedPrimaryScopesWithPolicy(ctx context.Context, entry *store.EntryRow, demoteDeclared, demoteOnLookupError bool) (*store.EntryRow, error) {
	if s.store == nil || entry == nil || len(s.cfg.Catalog.ScopeOwners) == 0 || len(entry.Scopes) == 0 {
		return entry, nil
	}
	changedDeclared := false
	declared := scopeRowsToClaims(entry.Scopes, "declared")
	if demoteDeclared {
		for i := range declared {
			if !declared[i].Primary {
				continue
			}
			allowed, err := s.authorizePrimaryScopeOwner(ctx, declared[i].Repo, entry.OwnerEmail, entry.OwnerGroup)
			if (err != nil && demoteOnLookupError) || (err == nil && !allowed) {
				declared[i].Primary = false
				changedDeclared = true
			}
		}
	}
	changedReported := false
	reported := scopeRowsToClaims(entry.Scopes, "reported")
	for i := range reported {
		if !reported[i].Primary {
			continue
		}
		allowed, err := s.authorizePrimaryScopeOwner(ctx, reported[i].Repo, entry.OwnerEmail, entry.OwnerGroup)
		if (err != nil && demoteOnLookupError) || (err == nil && !allowed) {
			reported[i].Primary = false
			changedReported = true
		}
	}
	if changedReported {
		if err := s.store.ReplaceEntryScopes(ctx, entry.ID, "reported", reported); err != nil {
			return entry, err
		}
	}
	if changedDeclared {
		updated, err := s.store.SetDeclaredScopes(ctx, entry.ID, declared)
		if err != nil {
			return entry, err
		}
		if s.router != nil && updated.Listed {
			_ = s.router.CatalogCache().Reload(ctx)
		}
		return updated, nil
	}
	if changedReported {
		if s.router != nil && entry.Listed {
			_ = s.router.CatalogCache().Reload(ctx)
		}
		if fresh, err := s.store.GetEntryByID(ctx, entry.ID); err == nil {
			return fresh, nil
		}
	}
	return entry, nil
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

func (w *statusCapturingWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (w *statusCapturingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apierror.Response{
		Error: msg,
		Code:  code,
	})
}

func writeInternalError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, apierror.Body(http.StatusInternalServerError, err))
}
