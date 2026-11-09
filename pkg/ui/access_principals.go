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
	// Roles are the scope sets in effect that an entry can be given, for
	// the role picker: built-ins first, then by name. Omitted on paged
	// calls so agent pages stay compact.
	Roles      []AccessRoleChoice `json:"roles,omitempty"`
	Total      int                `json:"total,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// AccessRoleChoice is one scope set offered as a role.
type AccessRoleChoice struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Builtin     bool   `json:"builtin"`
	// Person is true for a role only an access-code admin may assign.
	Person   bool       `json:"person"`
	Resolved []APIScope `json:"resolved"`
}

// AllowPrincipalRequest is POST /api/access/principals. Reason is why the
// entry is added; it is required for a serviceAccount: entry and may come
// from the X-Grain-Acting-Reason header instead.
//
// Role is the scope set the entry's sessions are held to, grain/operator
// when empty (docs/design/access-rbac.md). A role holding a dangerous
// scope -- grain/operator among them -- is assigned only by an access-code
// admin, and only with a reason.
type AllowPrincipalRequest struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason,omitempty"`
	Role   string `json:"role,omitempty"`
	Label  string `json:"label,omitempty"`
}

// SetPrincipalRoleRequest is PATCH /api/access/principals/{entry}: the role
// to give an entry already on the list, and why.
type SetPrincipalRoleRequest struct {
	Role   string `json:"role"`
	Reason string `json:"reason,omitempty"`
}

// SetPrincipalRoleResponse is the response to PATCH
// /api/access/principals/{entry}.
type SetPrincipalRoleResponse struct {
	Entry    string `json:"entry"`
	Role     string `json:"role"`
	Previous string `json:"previous"`
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
		return c.ensureDeploymentOwnerAllowedLocked(ctx)
	}
	raw, source := c.explicitAllowedPrincipalsRawWithSource()
	if len(raw) == 0 {
		c.allowedPrincipalsMigrated = true
		return c.ensureDeploymentOwnerAllowedLocked(ctx)
	}
	pool := c.gcpKeyPoolSet(ctx)
	var importable []string
	var dropped []string
	for _, r := range raw {
		for _, item := range SplitAllowedPrincipals(r) {
			prefix, email, canonical, err := model.NormalizeAllowedPrincipalEntry(item)
			if err == nil && prefix != "serviceAccount" && prefix != "delegate" {
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
		return c.ensureDeploymentOwnerAllowedLocked(ctx)
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
	return c.ensureDeploymentOwnerAllowedLocked(ctx)
}

func (c *Client) ensureDeploymentOwnerAllowedLocked(ctx context.Context) error {
	owner := c.explicitDeploymentOwnerEmail()
	if owner == "" || c.Store == nil || c.deploymentOwnerSeeded == owner {
		return nil
	}
	canonical := "user:" + owner
	if existing, ok, err := c.allowedPrincipalNamed(ctx, canonical); err != nil {
		return err
	} else if ok {
		if existing.Role != model.DefaultAccessRole {
			if _, _, _, err := c.Store.SetAllowedPrincipalRole(ctx, canonical, model.DefaultAccessRole); err != nil {
				return err
			}
		}
		c.deploymentOwnerSeeded = owner
		return nil
	}
	if _, _, err := c.Store.AddAllowedPrincipalWithRole(ctx, canonical, "deployment-owner", "-deployment-owner", model.DefaultAccessRole, c.now()); err != nil {
		return err
	}
	c.deploymentOwnerSeeded = owner
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
	roles := []AccessRoleChoice{}
	for _, set := range model.APIScopeSetsInEffect() {
		if set.Deprecated {
			continue
		}
		roles = append(roles, AccessRoleChoice{Name: set.Name, Description: set.Description,
			Builtin: set.Builtin, Person: set.Person, Resolved: set.Resolved})
	}
	return AccessPrincipalsResponse{
		Entries:            entries,
		DroppedFlagEntries: dropped,
		Roles:              roles,
	}, nil
}

// AllowPrincipal adds a `user:<email>`, `group:<email>`,
// `serviceAccount:<email>` or `delegate:<email>` entry to the database
// allowlist. A serviceAccount: or delegate: entry needs a reason, and an
// account whose key grain hands to tasks (the gcp-key / gcp permission-set
// pools) is always refused. Adding an existing entry is an idempotent no-op.
func (c *Client) AllowPrincipal(ctx context.Context, req AllowPrincipalRequest) (AllowPrincipalResponse, error) {
	if isDelegatedRequest(ctx) {
		return AllowPrincipalResponse{}, forbiddenCodeErrorf("requires_person", "delegated requests may not change the allowed users, groups and delegating apps list")
	}
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return AllowPrincipalResponse{}, err
	}
	prefix, email, canonical, err := model.NormalizeAllowedPrincipalEntry(req.Entry)
	if err != nil {
		return AllowPrincipalResponse{}, validationErrorf("%v", err)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = actingReasonFromContext(ctx)
	}
	role := strings.TrimSpace(req.Role)
	var label string
	if prefix == "delegate" {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind != model.PrincipalHuman {
			return AllowPrincipalResponse{}, forbiddenCodeErrorf("requires_person", "%s may not add %s: only a person may add delegate: entries", p.ID, canonical)
		}
		label = strings.TrimSpace(req.Label)
		if err := model.ValidateDelegateLabel(label); err != nil {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q: %v", canonical, err)
		}
		if c.gcpKeyPoolSet(ctx)[email] {
			return AllowPrincipalResponse{}, unprocessableErrorf("allowed principal %q names a grain-managed service account whose key is handed to task sandboxes and cannot be admitted as a delegating app", canonical)
		}
		if role == "" {
			role = model.DefaultDelegateAccessRole
		}
		if reason == "" {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q: give a reason to admit a delegating app (with role %s)", canonical, role)
		}
		if _, ok := model.APIScopeSetNamed(role); !ok {
			return AllowPrincipalResponse{}, validationErrorf("role %q is no scope set in effect (Settings -> Scope sets, or a built-in such as %s or grain/viewer)", role, model.DefaultDelegateAccessRole)
		}
		if err := model.ValidateDelegateRole(role); err != nil {
			return AllowPrincipalResponse{}, unprocessableErrorf("allowed principal %q: %v", canonical, err)
		}
	} else {
		if strings.TrimSpace(req.Label) != "" {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q: label is only supported on delegate:<email> entries", canonical)
		}
		if prefix != "group" && c.gcpKeyPoolSet(ctx)[email] {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q names a grain-managed service account whose key is handed to task sandboxes and cannot be admitted", canonical)
		}
		if role == "" {
			role = model.DefaultAccessRole
		}
		if owner := c.explicitDeploymentOwnerEmail(); owner != "" && canonical == "user:"+owner && role != model.DefaultAccessRole {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q is the deployment owner (-deployment-owner) and must keep role %s", canonical, model.DefaultAccessRole)
		}
		if prefix == "serviceAccount" && reason == "" {
			return AllowPrincipalResponse{}, validationErrorf("allowed principal %q: give a reason to admit a service account (with role %s)", canonical, role)
		}
	}
	if existing, ok, err := c.allowedPrincipalNamed(ctx, canonical); err != nil {
		return AllowPrincipalResponse{}, err
	} else if ok {
		// Already on the list: an idempotent no-op that keeps its role
		// (PATCH changes one), so nothing is assigned.
		return AllowPrincipalResponse{Principal: existing, Created: false}, nil
	}
	if err := checkAccessRoleAssignment(ctx, canonical, role, "", reason); err != nil {
		return AllowPrincipalResponse{}, err
	}
	p, created, err := c.Store.AddAllowedPrincipalWithOptions(ctx, canonical, writerName(ctx), reason, role, label, c.now())
	if err != nil {
		return AllowPrincipalResponse{}, err
	}
	if created {
		summary := "allowed " + p.Entry + " as " + p.Role
		if reason != "" {
			summary += " (" + reason + ")"
		}
		c.auditAccessChange(ctx, summary)
	}
	return AllowPrincipalResponse{Principal: p, Created: created}, nil
}

// DenyPrincipal removes an entry from the database allowlist.
func (c *Client) DenyPrincipal(ctx context.Context, rawEntry string) (DenyPrincipalResponse, error) {
	if isDelegatedRequest(ctx) {
		return DenyPrincipalResponse{}, forbiddenCodeErrorf("requires_person", "delegated requests may not change the allowed users, groups and delegating apps list")
	}
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return DenyPrincipalResponse{}, err
	}
	_, _, canonical, err := model.NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return DenyPrincipalResponse{}, validationErrorf("%v", err)
	}
	if owner := c.explicitDeploymentOwnerEmail(); owner != "" && canonical == "user:"+owner {
		return DenyPrincipalResponse{}, validationErrorf("allowed principal %q is the deployment owner (-deployment-owner) and cannot be removed from the allowlist", canonical)
	}
	if existing, ok, err := c.allowedPrincipalNamed(ctx, canonical); err != nil {
		return DenyPrincipalResponse{}, err
	} else if ok {
		if err := checkAccessEntryChange(ctx, canonical, existing.Role); err != nil {
			return DenyPrincipalResponse{}, err
		}
	} else if err := checkAccessEntryChange(ctx, canonical, ""); err != nil {
		return DenyPrincipalResponse{}, err
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

// SetPrincipalRole gives an entry already on the allowlist another role.
func (c *Client) SetPrincipalRole(ctx context.Context, rawEntry string, req SetPrincipalRoleRequest) (SetPrincipalRoleResponse, error) {
	if isDelegatedRequest(ctx) {
		return SetPrincipalRoleResponse{}, forbiddenCodeErrorf("requires_person", "delegated requests may not change the allowed users, groups and delegating apps list")
	}
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return SetPrincipalRoleResponse{}, err
	}
	prefix, _, canonical, err := model.NormalizeAllowedPrincipalEntry(rawEntry)
	if err != nil {
		return SetPrincipalRoleResponse{}, validationErrorf("%v", err)
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		return SetPrincipalRoleResponse{}, validationErrorf("allowed principal %q: name the role (a scope set) to give it", canonical)
	}
	if owner := c.explicitDeploymentOwnerEmail(); owner != "" && canonical == "user:"+owner && role != model.DefaultAccessRole {
		return SetPrincipalRoleResponse{}, validationErrorf("allowed principal %q is the deployment owner (-deployment-owner) and must keep role %s", canonical, model.DefaultAccessRole)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = actingReasonFromContext(ctx)
	}
	if prefix == "delegate" {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind != model.PrincipalHuman {
			return SetPrincipalRoleResponse{}, forbiddenCodeErrorf("requires_person", "%s may not change %s: only a person may change delegate: entries", p.ID, canonical)
		}
		if _, ok := model.APIScopeSetNamed(role); !ok {
			return SetPrincipalRoleResponse{}, validationErrorf("role %q is no scope set in effect (Settings -> Scope sets, or a built-in such as %s or grain/viewer)", role, model.DefaultDelegateAccessRole)
		}
		if err := model.ValidateDelegateRole(role); err != nil {
			return SetPrincipalRoleResponse{}, unprocessableErrorf("allowed principal %q: %v", canonical, err)
		}
	}
	existing, ok, err := c.allowedPrincipalNamed(ctx, canonical)
	if err != nil {
		return SetPrincipalRoleResponse{}, err
	}
	if !ok {
		return SetPrincipalRoleResponse{}, &NotFoundError{message: fmt.Sprintf("allowed principal %q is not on the allowlist", canonical)}
	}
	if err := checkAccessRoleAssignment(ctx, canonical, role, existing.Role, reason); err != nil {
		return SetPrincipalRoleResponse{}, err
	}
	if existing.Role == role {
		return SetPrincipalRoleResponse{Entry: canonical, Role: role, Previous: role}, nil
	}
	_, previous, found, err := c.Store.SetAllowedPrincipalRole(ctx, canonical, role)
	if err != nil {
		return SetPrincipalRoleResponse{}, err
	}
	if !found {
		return SetPrincipalRoleResponse{}, &NotFoundError{message: fmt.Sprintf("allowed principal %q is not on the allowlist", canonical)}
	}
	summary := "role " + canonical + " " + previous + " -> " + role
	if reason != "" {
		summary += " (" + reason + ")"
	}
	c.auditAccessChange(ctx, summary)
	return SetPrincipalRoleResponse{Entry: canonical, Role: role, Previous: previous}, nil
}

// allowedPrincipalNamed is the allowlist entry canonical, if it is on it.
func (c *Client) allowedPrincipalNamed(ctx context.Context, canonical string) (model.AllowedPrincipal, bool, error) {
	entries, err := c.Store.ListAllowedPrincipals(ctx)
	if err != nil {
		return model.AllowedPrincipal{}, false, err
	}
	for _, e := range entries {
		if e.Entry == canonical {
			return e, true, nil
		}
	}
	return model.AllowedPrincipal{}, false, nil
}

// checkAccessEntryChange refuses a change to entry (whose role is current,
// "" for none) by a caller who may not make it: any agent, and a verified
// session whose own role is narrower than current, or for which current is
// a role only an access-code admin assigns or one that denies scopes
// (docs/design/access-rbac.md §3).
func checkAccessEntryChange(ctx context.Context, entry, current string) error {
	if isDelegatedRequest(ctx) {
		return forbiddenCodeErrorf("requires_person", "delegated requests may not change the allowed users, groups and delegating apps list")
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		return &ForbiddenError{err: fmt.Errorf("agent %s may not change who signs in to this deployment: only a person changes the allowed users and groups list", p.ID)}
	}
	if strings.HasPrefix(entry, "delegate:") {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind != model.PrincipalHuman {
			return forbiddenCodeErrorf("requires_person", "%s may not change %s: only a person may change delegate: entries", p.ID, entry)
		}
	}
	caller := accessRoleFromContext(ctx)
	if caller == nil || current == "" {
		return nil
	}
	set, ok := model.APIScopeSetNamed(current)
	if !ok {
		return nil
	}
	if set.Person {
		return &ForbiddenError{err: fmt.Errorf("%s has role %s, which holds a dangerous scope: only an access-code admin changes it or removes it", entry, current)}
	}
	if len(roleDenies(current)) > 0 {
		// A deny is a veto over every other entry the same people match
		// (docs/design/access-rbac.md §2): removing or re-roling it
		// widens them -- the caller among them, perhaps -- past anything
		// noWiderThan sees, since a deny grants nothing to compare.
		return &ForbiddenError{err: fmt.Errorf("%s has role %s, which denies scopes: only an access-code admin lifts a deny by changing it or removing it", entry, current)}
	}
	if err := caller.noWiderThan(resolveAccessRoles(entry, []string{current})); err != nil {
		return &ForbiddenError{err: fmt.Errorf("%s has role %s, which is wider than %s's own (%v): only an access-code admin changes it", entry, current, caller.Who, err)}
	}
	return nil
}

// checkAccessRoleAssignment refuses giving entry role (from current, ""
// for a new entry) to a caller who may not, or without a reason a
// dangerous role needs.
func checkAccessRoleAssignment(ctx context.Context, entry, role, current, reason string) error {
	set, ok := model.APIScopeSetNamed(role)
	if !ok {
		return validationErrorf("role %q is no scope set in effect (Settings -> Scope sets, or a built-in such as %s or grain/viewer)", role, model.DefaultAccessRole)
	}
	if err := checkAccessEntryChange(ctx, entry, current); err != nil {
		return err
	}
	caller := accessRoleFromContext(ctx)
	if set.Person {
		if caller != nil {
			return &ForbiddenError{err: fmt.Errorf("role %s holds a dangerous scope (or only a person may grant it): only an access-code admin assigns it, not %s", role, caller.label())}
		}
		if strings.TrimSpace(reason) == "" {
			return validationErrorf("role %s holds a dangerous scope: give a reason to assign it to %s (--reason, or the reason field)", role, entry)
		}
		return nil
	}
	if caller != nil {
		if err := caller.noWiderThan(resolveAccessRoles(entry, []string{role})); err != nil {
			return &ForbiddenError{err: fmt.Errorf("role %s is wider than %s's own: %v", role, caller.Who, err)}
		}
	}
	return nil
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
	list, err := parseListQuery(r, listPageMedium)
	if err != nil {
		writeClientError(w, err)
		return
	}
	out, err := s.tasks.ListAllowedPrincipals(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	if list.Paged {
		matched := filterList(out.Entries, func(p model.AllowedPrincipal) bool {
			return list.matches(p.Entry, p.Role, p.Reason, p.AddedBy)
		})
		page := pageOf(matched, list)
		out.Entries = page.Items
		out.Roles = nil
		out.Total = page.Total
		out.Truncated = page.Truncated
		out.NextCursor = page.NextCursor
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

func (s *Server) handleSetPrincipalRole(w http.ResponseWriter, r *http.Request) {
	rawEntry := r.PathValue("entry")
	if strings.Contains(rawEntry, "%") {
		if unescaped, err := url.PathUnescape(rawEntry); err == nil {
			rawEntry = unescaped
		}
	}
	var req SetPrincipalRoleRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx := withActingReason(r.Context(), r.Header.Get(ActingReasonHeader))
	out, err := s.tasks.SetPrincipalRole(ctx, rawEntry, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	if err := c.do(ctx, http.MethodGet, "/api/access/principals", nil, &out); err != nil {
		return out, err
	}
	for out.Truncated && out.NextCursor != "" {
		var page AccessPrincipalsResponse
		if err := c.do(ctx, http.MethodGet, "/api/access/principals?cursor="+url.QueryEscape(out.NextCursor), nil, &page); err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, page.Entries...)
		out.Truncated = page.Truncated
		out.NextCursor = page.NextCursor
	}
	return out, nil
}

// AllowPrincipal is POST /api/access/principals.
func (c *HTTPClient) AllowPrincipal(ctx context.Context, req AllowPrincipalRequest) (AllowPrincipalResponse, error) {
	var out AllowPrincipalResponse
	err := c.do(ctx, http.MethodPost, "/api/access/principals", req, &out)
	return out, err
}

// SetPrincipalRole is PATCH /api/access/principals/{entry}.
func (c *HTTPClient) SetPrincipalRole(ctx context.Context, entry string, req SetPrincipalRoleRequest) (SetPrincipalRoleResponse, error) {
	var out SetPrincipalRoleResponse
	err := c.do(ctx, http.MethodPatch, "/api/access/principals/"+url.PathEscape(strings.TrimSpace(entry)), req, &out)
	return out, err
}

// DenyPrincipal is DELETE /api/access/principals/{entry}.
func (c *HTTPClient) DenyPrincipal(ctx context.Context, entry string) (DenyPrincipalResponse, error) {
	var out DenyPrincipalResponse
	err := c.do(ctx, http.MethodDelete, "/api/access/principals/"+url.PathEscape(strings.TrimSpace(entry)), nil, &out)
	return out, err
}
