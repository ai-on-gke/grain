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
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// AccessPrincipalsResponse is GET /api/access/principals: the database-backed
// allowed users, groups and service accounts plus any entries dropped during
// one-time migration off `-ui-allowed-principals` / `$GRAIN_ALLOWED_PRINCIPALS`.
type AccessPrincipalsResponse struct {
	Entries            []model.AllowedPrincipal `json:"entries"`
	DroppedFlagEntries []string                 `json:"droppedFlagEntries,omitempty"`
}

// AllowPrincipalRequest is POST /api/access/principals. Reason is why the
// entry is added; it is required for a serviceAccount: entry and may come
// from the X-Grain-Acting-Reason header instead.
type AllowPrincipalRequest struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason,omitempty"`
}

// AllowPrincipalResponse is the response to POST /api/access/principals.
type AllowPrincipalResponse struct {
	Principal model.AllowedPrincipal `json:"principal"`
	Created   bool                   `json:"created"`
}

// DenyPrincipalResponse is the response to DELETE /api/access/principals/{entry}.
type DenyPrincipalResponse struct {
	Entry   string `json:"entry"`
	Removed bool   `json:"removed"`
}

type actingReasonContextKey struct{}

func withActingReason(ctx context.Context, reason string) context.Context {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, actingReasonContextKey{}, reason)
}

func actingReasonFromContext(ctx context.Context) string {
	v, _ := ctx.Value(actingReasonContextKey{}).(string)
	return v
}

// MigrateAllowedPrincipals imports `user:` and `group:` entries from the
// deprecated `-ui-allowed-principals` flag (or `$GRAIN_ALLOWED_PRINCIPALS`)
// into the database at most once when the database list is empty. On every
// subsequent startup where the flag or environment variable is still set, it
// logs a deprecation warning and ignores the flag. `serviceAccount:` entries
// are never imported -- an operator adds each one deliberately, with a
// reason -- and neither are unsupported `domain:` entries; each dropped
// entry is logged and recorded so Settings -> Access can surface it.
func (c *Client) MigrateAllowedPrincipals(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return nil
	}
	c.allowedPrincipalsMigrateMu.Lock()
	defer c.allowedPrincipalsMigrateMu.Unlock()
	if c.allowedPrincipalsMigrated {
		return nil
	}
	raw, source := c.explicitAllowedPrincipalsRawWithSource()
	if len(raw) == 0 {
		c.allowedPrincipalsMigrated = true
		return nil
	}
	pool := c.gcpKeyPoolSet(ctx)
	var importable []string
	var dropped []string
	for _, r := range raw {
		for _, item := range SplitAllowedPrincipals(r) {
			prefix, email, canonical, err := model.NormalizeAllowedPrincipalEntry(item)
			if err == nil && prefix != "serviceAccount" {
				if pool[email] {
					dropped = append(dropped, item)
					continue
				}
				importable = append(importable, canonical)
				continue
			}
			dropped = append(dropped, item)
		}
	}

	imported, didImport, err := c.Store.ImportAllowedPrincipalsFromFlag(ctx, importable, dropped, source, c.now())
	if err != nil {
		log.Printf("grain access: ERROR importing allowed principals from %s: %v", source, err)
		return err
	}
	c.allowedPrincipalsMigrated = true
	if !didImport {
		log.Printf("grain access: WARNING: %s is deprecated and ignored because the allowed users and groups list lives in the database (manage it in Settings -> Access or with 'grain access')", source)
		return nil
	}

	names := make([]string, 0, len(imported))
	for _, p := range imported {
		names = append(names, p.Entry)
	}
	if len(names) > 0 {
		log.Printf("grain access: imported %d allowed user/group entr%s from %s into the database (%s); %s is deprecated and will not be applied on future starts (manage access in Settings -> Access or with 'grain access')",
			len(names), pluralSuffix(len(names), "y", "ies"), source, strings.Join(names, ", "), source)
	} else {
		log.Printf("grain access: recorded one-time migration from %s (no user: or group: entries to import); %s is deprecated and will not be applied on future starts (manage access in Settings -> Access or with 'grain access')",
			source, source)
	}
	for _, d := range dropped {
		log.Printf("grain access: WARNING: dropped %q from %s during migration -- only user:<email> and group:<email> entries are imported; add a service account deliberately with 'grain access allow serviceAccount:<email> --reason <why>'", d, source)
	}

	summary := fmt.Sprintf("imported %d allowed principal(s) from deprecated %s", len(names), source)
	if len(dropped) > 0 {
		summary += fmt.Sprintf(" (dropped unsupported: %s)", strings.Join(dropped, ", "))
	}
	if _, auditErr := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:      c.now(),
		By:      model.Principal{Kind: model.PrincipalAutomation, ID: "flag-migration"},
		Surface: "access",
		Summary: summary,
	}); auditErr != nil {
		log.Printf("grain access: recording flag migration in config audit: %v", auditErr)
	}
	return nil
}

func pluralSuffix(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func (c *Client) explicitAllowedPrincipalsRawWithSource() ([]string, string) {
	if c != nil && len(c.Config.AllowedPrincipals) > 0 {
		return c.Config.AllowedPrincipals, "-ui-allowed-principals"
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_ALLOWED_PRINCIPALS")); env != "" {
		return SplitAllowedPrincipals(env), "$GRAIN_ALLOWED_PRINCIPALS"
	}
	return nil, ""
}

// ListAllowedPrincipals returns the database-backed allowed users and groups
// list along with any entries dropped from a deprecated startup flag import.
func (c *Client) ListAllowedPrincipals(ctx context.Context) (AccessPrincipalsResponse, error) {
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return AccessPrincipalsResponse{}, err
	}
	entries, err := c.Store.ListAllowedPrincipals(ctx)
	if err != nil {
		return AccessPrincipalsResponse{}, err
	}
	if entries == nil {
		entries = []model.AllowedPrincipal{}
	}
	var dropped []string
	if mig, err := c.Store.GetAllowedPrincipalsMigration(ctx); err != nil {
		return AccessPrincipalsResponse{}, err
	} else if mig != nil && len(mig.Dropped) > 0 {
		dropped = mig.Dropped
	}
	return AccessPrincipalsResponse{
		Entries:            entries,
		DroppedFlagEntries: dropped,
	}, nil
}

// AllowPrincipal adds a `user:<email>`, `group:<email>` or
// `serviceAccount:<email>` entry to the database allowlist. A
// serviceAccount: entry needs a reason, and an account whose key grain hands
// to tasks (the gcp-key / gcp permission-set pools) is always refused.
// Adding an existing entry is an idempotent no-op.
func (c *Client) AllowPrincipal(ctx context.Context, req AllowPrincipalRequest) (AllowPrincipalResponse, error) {
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return AllowPrincipalResponse{}, err
	}
	prefix, email, canonical, err := model.NormalizeAllowedPrincipalEntry(req.Entry)
	if err != nil {
		return AllowPrincipalResponse{}, validationErrorf("%v", err)
	}
	if prefix != "group" && c.gcpKeyPoolSet(ctx)[email] {
		return AllowPrincipalResponse{}, validationErrorf("allowed principal %q names a grain-managed service account whose key is handed to task sandboxes and cannot be admitted", canonical)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = actingReasonFromContext(ctx)
	}
	if prefix == "serviceAccount" && reason == "" {
		return AllowPrincipalResponse{}, validationErrorf("allowed principal %q: give a reason to admit a service account (it gets full operator access)", canonical)
	}
	p, created, err := c.Store.AddAllowedPrincipal(ctx, canonical, writerName(ctx), reason, c.now())
	if err != nil {
		return AllowPrincipalResponse{}, err
	}
	if created {
		summary := "allowed " + p.Entry
		if reason != "" {
			summary += " (" + reason + ")"
		}
		c.auditAccessChange(ctx, summary)
	}
	return AllowPrincipalResponse{Principal: p, Created: created}, nil
}

// DenyPrincipal removes an entry from the database allowlist.
func (c *Client) DenyPrincipal(ctx context.Context, rawEntry string) (DenyPrincipalResponse, error) {
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return DenyPrincipalResponse{}, err
	}
	_, _, canonical, err := model.NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return DenyPrincipalResponse{}, validationErrorf("%v", err)
	}
	removedEntry, removed, err := c.Store.RemoveAllowedPrincipal(ctx, canonical)
	if err != nil {
		return DenyPrincipalResponse{}, err
	}
	if !removed {
		return DenyPrincipalResponse{}, &NotFoundError{message: fmt.Sprintf("allowed principal %q is not on the allowlist", canonical)}
	}
	summary := "removed " + removedEntry
	if reason := actingReasonFromContext(ctx); reason != "" {
		summary += " (" + reason + ")"
	}
	c.auditAccessChange(ctx, summary)
	return DenyPrincipalResponse{Entry: removedEntry, Removed: true}, nil
}

func (c *Client) auditAccessChange(ctx context.Context, summary string) {
	by := RequestActor(ctx)
	log.Printf("grain access: %s (%s) %s",
		PrincipalLabel(by.Actor, by.OnBehalfOf), sourceName(by.Actor.Source), summary)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:         c.now(),
		By:         by.Actor,
		OnBehalfOf: by.OnBehalfOf,
		Surface:    "access",
		Summary:    summary,
	}); err != nil {
		log.Printf("grain access: recording %q in config audit: %v", summary, err)
	}
}

func (s *Server) handleListAccessPrincipals(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.ListAllowedPrincipals(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAllowPrincipal(w http.ResponseWriter, r *http.Request) {
	var req AllowPrincipalRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx := withActingReason(r.Context(), r.Header.Get(ActingReasonHeader))
	out, err := s.tasks.AllowPrincipal(ctx, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	status := http.StatusOK
	if out.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func (s *Server) handleDenyPrincipal(w http.ResponseWriter, r *http.Request) {
	rawEntry := r.PathValue("entry")
	if strings.Contains(rawEntry, "%") {
		if unescaped, err := url.PathUnescape(rawEntry); err == nil {
			rawEntry = unescaped
		}
	}
	ctx := withActingReason(r.Context(), r.Header.Get(ActingReasonHeader))
	out, err := s.tasks.DenyPrincipal(ctx, rawEntry)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAllowedPrincipals is GET /api/access/principals.
func (c *HTTPClient) ListAllowedPrincipals(ctx context.Context) (AccessPrincipalsResponse, error) {
	var out AccessPrincipalsResponse
	err := c.do(ctx, http.MethodGet, "/api/access/principals", nil, &out)
	return out, err
}

// AllowPrincipal is POST /api/access/principals.
func (c *HTTPClient) AllowPrincipal(ctx context.Context, req AllowPrincipalRequest) (AllowPrincipalResponse, error) {
	var out AllowPrincipalResponse
	err := c.do(ctx, http.MethodPost, "/api/access/principals", req, &out)
	return out, err
}

// DenyPrincipal is DELETE /api/access/principals/{entry}.
func (c *HTTPClient) DenyPrincipal(ctx context.Context, entry string) (DenyPrincipalResponse, error) {
	var out DenyPrincipalResponse
	err := c.do(ctx, http.MethodDelete, "/api/access/principals/"+url.PathEscape(strings.TrimSpace(entry)), nil, &out)
	return out, err
}
