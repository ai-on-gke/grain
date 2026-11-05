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

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/server"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

const strictCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// ServerOptions configures a registry HTTP Server.
type ServerOptions struct {
	Config     Config
	Store      *store.Store
	Admitter   *webauth.Admitter
	Groups     GroupResolver
	Delegation DelegationPolicy
	Signer     Signer
	Transport  ConnectTransport
	Metrics    *Metrics
}

// Server serves the main registry HTTP listener (`:8080`) and the public JWKS
// listener (`:8081`).
type Server struct {
	cfg        Config
	store      *store.Store
	admitter   *webauth.Admitter
	groups     GroupResolver
	delegation DelegationPolicy
	signer     Signer
	transport  ConnectTransport
	metrics    *Metrics
	static     http.Handler
	draining   atomic.Bool
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
	return &Server{
		cfg:        opts.Config,
		store:      opts.Store,
		admitter:   opts.Admitter,
		groups:     opts.Groups,
		delegation: opts.Delegation,
		signer:     opts.Signer,
		transport:  opts.Transport,
		metrics:    metrics,
		static:     server.NewStaticHandler(),
	}, nil
}

// SetDraining toggles the server's drain mode (`GET /healthz` returns 503 while draining).
func (s *Server) SetDraining(draining bool) {
	s.draining.Store(draining)
}

// IsDraining reports whether the server is currently draining.
func (s *Server) IsDraining() bool {
	return s.draining.Load()
}

// JWKSHandler returns the unauthenticated handler for the dedicated public JWKS listener (`:8081`).
func (s *Server) JWKSHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

// Handler returns the main HTTP handler for `:8080`.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /metrics", s.metrics.Handler())
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("POST /api/internal/heartbeat", s.handleInternalHeartbeat)

	// Service principals may read the catalog -- /api/directory and
	// /api/route -- and nothing else: entries, shapes and the rest of the
	// control plane are for people (registry.design.md, "Plain service
	// calls").
	mux.Handle("GET /api/me", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetMe)))
	mux.Handle("GET /api/shapes", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetShapes)))
	mux.Handle("GET /api/directory", s.admitter.RequireAdmitted(http.HandlerFunc(s.handleGetDirectory)))
	mux.Handle("GET /api/entries", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntries)))
	mux.Handle("GET /api/entries/{name}/availability", s.admitter.RequireHuman(http.HandlerFunc(s.handleGetEntryAvailability)))
	mux.Handle("GET /api/route", s.admitter.RequireAdmitted(http.HandlerFunc(s.handleGetRoute)))
	mux.Handle("/", s.admitter.RequireHuman(s.static))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", strictCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if entryName, ok := s.extractInstanceSubdomain(r.Host); ok {
			s.serveEntrySubdomain(w, r, entryName)
			return
		}

		// Enforce CSRF protection on state-changing requests other than bearer-authenticated internal endpoints.
		if !strings.HasPrefix(r.URL.Path, "/api/internal/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if err := webauth.VerifySameOriginForProxyAuth(r); err != nil {
				s.metrics.IncRequests(http.StatusForbidden)
				writeJSONError(w, http.StatusForbidden, "csrf_failed", err.Error())
				return
			}
		}

		rec := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rec, r)
		s.metrics.IncRequests(rec.status)
	})
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

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	resp := map[string]any{
		"email":             caller.Email,
		"sub":               caller.Subject,
		"kind":              caller.Kind,
		"is_admin":          caller.IsAdmin,
		"is_registry_admin": caller.IsAdmin,
		"can_create":        caller.CanCreate,
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
	if s.store != nil {
		shapes, err := s.store.ListShapes(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
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

func (s *Server) handleGetDirectory(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	if s.store == nil {
		writeJSON(w, http.StatusOK, api.DirectoryResponse{Entries: []api.DirectoryEntry{}})
		return
	}
	entries, err := s.store.ListEntries(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]api.DirectoryEntry, 0, len(entries))
	for _, e := range entries {
		if !inCatalog(e) {
			continue
		}
		out = append(out, s.store.ToDirectoryEntry(e, s.cfg.InstanceDomain))
	}
	writeJSON(w, http.StatusOK, api.DirectoryResponse{Entries: out})
}

func (s *Server) handleGetEntries(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	if s.store == nil {
		writeJSON(w, http.StatusOK, api.ListEntriesResponse{Entries: []api.Entry{}})
		return
	}
	entries, err := s.store.ListEntries(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]api.Entry, 0, len(entries))
	for _, e := range entries {
		ok, err := s.canCallerListEntry(r.Context(), caller, e)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "authorization lookup temporarily unavailable")
			return
		}
		if !ok {
			continue
		}
		out = append(out, s.store.ToAPIEntry(e, s.cfg.InstanceDomain))
	}
	writeJSON(w, http.StatusOK, api.ListEntriesResponse{Entries: out})
}

func (s *Server) handleGetEntryAvailability(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
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
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, avail)
}

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "query parameter \"repo\" is required")
		return
	}
	filePath := r.URL.Query().Get("path")
	if s.store == nil {
		writeJSON(w, http.StatusOK, api.RouteResponse{Repo: repo, Path: filePath, Candidates: []api.RouteCandidate{}})
		return
	}
	res, err := s.store.RouteRepoPath(r.Context(), repo, filePath, s.cfg.InstanceDomain, inCatalog)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
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
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.metrics.IncHeartbeats()
	writeJSON(w, http.StatusOK, api.HeartbeatResponse{
		Status: s.store.DeriveStatus(updated),
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
		writeJSONError(w, http.StatusInternalServerError, "internal", err.Error())
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

// inCatalog reports whether e belongs in catalog discovery (/api/directory
// and /api/route). The catalog is the same for every admitted caller --
// person, admin or service principal -- and holds listed entries only:
// an unlisted entry never appears there, whoever owns it, whatever its
// visibility, and however it is shared (registry.design.md: "Catalog
// discovery exposes only entries explicitly marked listed").
func inCatalog(e *store.EntryRow) bool {
	return e != nil && e.Listed && e.DeletedAt == nil && e.LifecycleState != api.StateDeleted
}

// canCallerListEntry reports whether e is one of the caller's own entries
// for GET /api/entries: one they own, one whose owner_group they are in,
// or -- for a registry admin -- any entry. Being listed in the catalog,
// public, or shared with the caller does not put an entry here: this
// projection carries internal fields (backend_url, zone, shape) that the
// catalog never shows.
func (s *Server) canCallerListEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || caller.Kind != webauth.PrincipalHuman || e == nil {
		return false, nil
	}
	if caller.IsAdmin || strings.EqualFold(e.OwnerEmail, caller.Email) {
		return true, nil
	}
	if g := strings.TrimSpace(e.OwnerGroup); g != "" {
		if s.groups == nil {
			return false, errors.New("group resolver not configured")
		}
		return s.groups.IsMember(ctx, strings.TrimPrefix(strings.ToLower(g), "group:"), strings.ToLower(strings.TrimSpace(caller.Email)))
	}
	return false, nil
}

func (s *Server) canCallerAccessEntry(ctx context.Context, caller *webauth.AdmittedCaller, e *store.EntryRow) (bool, error) {
	if caller == nil || caller.Kind != webauth.PrincipalHuman {
		return false, nil
	}
	if strings.EqualFold(e.OwnerEmail, caller.Email) || e.Visibility == store.VisibilityPublic {
		return true, nil
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

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	})
}
