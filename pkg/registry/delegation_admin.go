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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// The delegating-app admin API (`/api/admin/delegating-apps`) and the
// person-facing consent API (`/api/delegation-grants`) that feed
// `delegation_policy: consent` (ConsentDelegationPolicy). Apps are created
// and disabled by registry admins only; a grant is always made by, listed
// for, and revoked by the signed-in person it belongs to.

// delegatingAppNameRe is the shape of a delegating app's short name: a DNS
// label, so it can never be mistaken for a `dlg_` grant ID in
// DELETE /api/delegation-grants/<ref>.
var delegatingAppNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)

const grantIDPrefix = "dlg_"

func delegatingAppToAPI(a *store.DelegatingAppRow) api.DelegatingApp {
	out := api.DelegatingApp{
		Name:                a.Name,
		Principal:           a.Principal,
		DisplayName:         a.DisplayName,
		Description:         a.Description,
		Homepage:            a.Homepage,
		Owner:               a.Owner,
		OwnerGroup:          a.OwnerGroup,
		MaxAccess:           a.MaxAccess,
		MaxGrantTTLSeconds:  int64(a.MaxGrantTTL / time.Second),
		RequireSubjectToken: a.RequireSubjectToken,
		RedirectURIs:        a.RedirectURIs,
		CreatedBy:           a.CreatedBy,
		CreatedAt:           a.CreatedAt.UTC(),
		DisabledAt:          a.DisabledAt,
		DisabledBy:          a.DisabledBy,
	}
	return out
}

func delegationGrantToAPI(g *store.DelegationGrantRow) api.DelegationGrant {
	entries := g.Entries
	if entries == nil {
		entries = []string{}
	}
	return api.DelegationGrant{
		ID:         g.ID,
		App:        g.App,
		Person:     g.Person,
		Entries:    entries,
		Access:     g.Access,
		CreatedAt:  g.CreatedAt.UTC(),
		UpdatedAt:  g.UpdatedAt.UTC(),
		ExpiresAt:  g.ExpiresAt.UTC(),
		RevokedAt:  g.RevokedAt,
		RevokedBy:  g.RevokedBy,
		LastUsedAt: g.LastUsedAt,
	}
}

// validateAppPrincipal normalises raw through store.NormalizeAppPrincipal and
// checks the result names a service account by email: a delegating app's
// principal is matched against the caller identity of machine requests, and
// a human email (or no email at all) would register an app nothing can ever
// present as.
func validateAppPrincipal(raw string) (string, error) {
	principal := store.NormalizeAppPrincipal(raw)
	if principal == "" {
		return "", errors.New("principal is required (a service account email, optionally prefixed serviceAccount:)")
	}
	email := strings.TrimPrefix(principal, "serviceAccount:")
	if strings.ContainsAny(email, " \t\r\n:/") || strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return "", fmt.Errorf("principal %q is not a service account email", raw)
	}
	if webauth.PrincipalKindForEmail(email) == webauth.PrincipalHuman {
		return "", fmt.Errorf("principal %q is a person, not a service account: a delegating app acts as its own service account", raw)
	}
	return principal, nil
}

func (s *Server) handleAdminListDelegatingApps(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	rows, err := s.store.ListDelegatingApps(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	resp := api.ListDelegatingAppsResponse{Apps: make([]api.DelegatingApp, 0, len(rows))}
	for i := range rows {
		resp.Apps = append(resp.Apps, delegatingAppToAPI(&rows[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAdminCreateDelegatingApp(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	var req api.CreateDelegatingAppRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !delegatingAppNameRe.MatchString(name) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument",
			fmt.Sprintf("invalid app name %q: must be 3..63 lowercase ASCII letters, digits, or hyphens, starting with a letter and ending with a letter or digit", req.Name))
		return
	}
	principal, err := validateAppPrincipal(req.Principal)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	owner := strings.ToLower(strings.TrimSpace(req.Owner))
	if owner == "" {
		owner = strings.ToLower(caller.Email)
	}
	if !strings.Contains(owner, "@") || webauth.PrincipalKindForEmail(owner) != webauth.PrincipalHuman {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("owner %q must be a person's email", req.Owner))
		return
	}
	ownerGroup := strings.TrimSpace(req.OwnerGroup)
	if ownerGroup != "" && s.groups != nil {
		norm, err := s.groups.NormalizeGroup(ownerGroup)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		ownerGroup = norm
	}
	if req.MaxGrantTTLSeconds < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "max_grant_ttl_seconds must not be negative")
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = name
	}
	created, err := s.store.CreateDelegatingApp(r.Context(), store.DelegatingAppRow{
		Name:                name,
		Principal:           principal,
		DisplayName:         displayName,
		Description:         req.Description,
		Homepage:            req.Homepage,
		Owner:               owner,
		OwnerGroup:          ownerGroup,
		MaxAccess:           req.MaxAccess,
		MaxGrantTTL:         time.Duration(req.MaxGrantTTLSeconds) * time.Second,
		RequireSubjectToken: req.RequireSubjectToken,
		RedirectURIs:        req.RedirectURIs,
		CreatedBy:           caller.Email,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "admin_delegating_app_create", "", "", map[string]any{
		"app":        created.Name,
		"principal":  created.Principal,
		"max_access": created.MaxAccess,
	})
	writeJSON(w, http.StatusCreated, delegatingAppToAPI(created))
}

func (s *Server) handleAdminSetDelegatingAppDisabled(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	var req api.SetDelegatingAppDisabledRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if err := s.store.SetDelegatingAppDisabled(r.Context(), name, req.Disabled, caller.Email); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no delegating app named %q", name))
			return
		}
		s.writeStoreError(w, err)
		return
	}
	action := "admin_delegating_app_enable"
	if req.Disabled {
		action = "admin_delegating_app_disable"
		// Drop this replica's cached assertions minted for the disabled app now,
		// rather than letting them live out the 30s cache.
		if inv, ok := s.signer.(interface{ InvalidateApp(string) }); ok {
			inv.InvalidateApp(name)
		}
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, action, "", "", map[string]any{"app": name})
	app, err := s.store.GetDelegatingApp(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, delegatingAppToAPI(app))
}

func (s *Server) handleListDelegationGrants(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	includeRevoked := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_revoked")), "true")
	rows, err := s.store.ListDelegationGrantsForPerson(r.Context(), caller.Email, includeRevoked)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	resp := api.ListDelegationGrantsResponse{Grants: make([]api.DelegationGrant, 0, len(rows))}
	for i := range rows {
		resp.Grants = append(resp.Grants, delegationGrantToAPI(&rows[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateDelegationGrant(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	var req api.CreateDelegationGrantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	person := strings.ToLower(strings.TrimSpace(caller.Email))
	// A grant is consent, and consent is only ever the signed-in person's to
	// give: naming anybody else is refused outright -- admins included --
	// rather than silently rewritten to the caller.
	if p := strings.ToLower(strings.TrimSpace(req.Person)); p != "" && p != person {
		writeJSONError(w, http.StatusForbidden, "forbidden", "a delegation grant can only be made by the person it is for")
		return
	}
	appName := strings.ToLower(strings.TrimSpace(req.App))
	if appName == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "app is required")
		return
	}
	var entries []string
	seen := map[string]bool{}
	for _, raw := range req.Entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" || seen[e] {
			continue
		}
		if e != "*" {
			if err := api.ValidateEntryName(e); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error())
				return
			}
		}
		seen[e] = true
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", `entries is required: name the entries the app may reach for you, or "*" for every entry you can reach`)
		return
	}
	if req.TTLSeconds < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "ttl_seconds must not be negative")
		return
	}
	app, err := s.store.GetDelegatingApp(r.Context(), appName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no delegating app named %q", appName))
			return
		}
		writeInternalError(w, err)
		return
	}
	if app.Disabled() {
		writeJSONError(w, http.StatusForbidden, "app_disabled", fmt.Sprintf("delegating app %q is disabled", appName))
		return
	}
	access := strings.ToLower(strings.TrimSpace(req.Access))
	if access == "" {
		access = app.MaxAccess
	}
	if access != "read" && access != "write" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("invalid access %q: must be \"read\" or \"write\"", req.Access))
		return
	}
	if app.MaxAccess == "read" && access == "write" {
		writeJSONError(w, http.StatusForbidden, "exceeds_max_access", fmt.Sprintf("delegating app %q accepts read-only grants", appName))
		return
	}
	grant, err := s.store.UpsertDelegationGrant(r.Context(), appName, person, entries, access, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	// Upsert reuses the existing live grant ID when narrowing entries or access;
	// evict any cached assertions minted under the previous scope on this replica.
	if inv, ok := s.signer.(interface{ InvalidateGrant(string) }); ok {
		inv.InvalidateGrant(grant.ID)
	}
	_ = s.store.RecordAudit(r.Context(), caller.Email, "delegation_grant_create", "", "", map[string]any{
		"app":      grant.App,
		"grant_id": grant.ID,
		"entries":  grant.Entries,
		"access":   grant.Access,
	})
	writeJSON(w, http.StatusOK, delegationGrantToAPI(grant))
}

func (s *Server) handleRevokeDelegationGrant(w http.ResponseWriter, r *http.Request) {
	caller := webauth.AdmittedFromContext(r.Context())
	if !s.checkCallerEnabled(w, r, caller) {
		return
	}
	if !s.checkMutationRateLimit(w, caller) {
		return
	}
	if s.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "store is not configured")
		return
	}
	ref := strings.TrimSpace(r.PathValue("ref"))
	person := strings.ToLower(strings.TrimSpace(caller.Email))
	var grant *store.DelegationGrantRow
	var err error
	if strings.HasPrefix(ref, grantIDPrefix) {
		grant, err = s.store.GetDelegationGrant(r.Context(), ref)
	} else {
		grant, err = s.store.GetLiveDelegationGrant(r.Context(), ref, person)
	}
	// Somebody else's grant reads exactly like a grant that does not exist,
	// so grant IDs cannot be probed across people.
	if errors.Is(err, store.ErrNotFound) || (err == nil && !strings.EqualFold(grant.Person, person)) {
		writeJSONError(w, http.StatusNotFound, "not_found", fmt.Sprintf("you have no live delegation grant %q", ref))
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if grant.RevokedAt == nil {
		if err := s.store.RevokeDelegationGrant(r.Context(), grant.ID, person); err != nil && !errors.Is(err, store.ErrNotFound) {
			writeInternalError(w, err)
			return
		}
		// Drop this replica's cached assertions minted under the grant now,
		// rather than letting them live out the 30s cache (REG-22 item 4).
		if inv, ok := s.signer.(interface{ InvalidateGrant(string) }); ok {
			inv.InvalidateGrant(grant.ID)
		}
		_ = s.store.RecordAudit(r.Context(), caller.Email, "delegation_grant_revoke", "", "", map[string]any{
			"app":      grant.App,
			"grant_id": grant.ID,
		})
	}
	fresh, err := s.store.GetDelegationGrant(r.Context(), grant.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, delegationGrantToAPI(fresh))
}
