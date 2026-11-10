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
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Person is true for a role holding a dangerous scope: assigning it
	// needs a reason, and a person whose own role holds all of it.
	Person bool `json:"person"`
	// Denies is true for a role that denies scopes: in a role a deny is a
	// veto, so changing an entry that has it needs a reason too.
	Denies   bool       `json:"denies,omitempty"`
	Resolved []APIScope `json:"resolved"`
}

// AllowPrincipalRequest is POST /api/access/principals. Reason is why the
// entry is added; it is required for a serviceAccount: entry and may come
// from the X-Grain-Acting-Reason header instead.
//
// Role is the scope set the entry's sessions are held to, grain/operator
// when empty (docs/design/access-rbac.md). A role holding a dangerous
// scope -- grain/operator among them -- is assigned only with a reason, by
// the access code or by a signed-in person holding every scope of it.
type AllowPrincipalRequest struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason,omitempty"`
	Role   string `json:"role,omitempty"`
	Label  string `json:"label,omitempty"`
}

// SetPrincipalRoleRequest is PATCH /api/access/principals/{entry}: the role
// to give an entry already on the list, and why.
//
// ConfirmSelf confirms narrowing an entry that matches the caller itself,
// which is refused without it (docs/design/access-rbac.md §3).
type SetPrincipalRoleRequest struct {
	Role        string `json:"role"`
	Reason      string `json:"reason,omitempty"`
	ConfirmSelf bool   `json:"confirmSelf,omitempty"`
}

// DenyPrincipalOptions are DELETE /api/access/principals/{entry}'s query:
// why the entry is removed (?reason=, required when its role holds a
// dangerous scope or denies scopes and a signed-in admin removes it) and
// ?confirmSelf=1 to confirm removing an entry that matches the caller.
type DenyPrincipalOptions struct {
	Reason      string
	ConfirmSelf bool
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

type confirmSelfContextKey struct{}

// withConfirmSelf records that the caller confirmed a change narrowing an
// entry that matches itself (DELETE's ?confirmSelf=1).
func withConfirmSelf(ctx context.Context, confirm bool) context.Context {
	if !confirm {
		return ctx
	}
	return context.WithValue(ctx, confirmSelfContextKey{}, true)
}

func confirmSelfFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(confirmSelfContextKey{}).(bool)
	return v
}

var (
	accessReasonANSIOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	accessReasonANSICSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	accessReasonANSIESC = regexp.MustCompile(`\x1b[@-_]`)
)

// cleanAccessReason flattens whitespace/newlines and strips ANSI escape
// sequences and control or format characters so config_audit summaries,
// `grain access list` and daemon logs stay single-line and free of terminal
// escapes. A reason consisting only of escapes or control bytes becomes "".
func cleanAccessReason(reason string) string {
	reason = accessReasonANSIOSC.ReplaceAllString(reason, " ")
	reason = accessReasonANSICSI.ReplaceAllString(reason, " ")
	reason = accessReasonANSIESC.ReplaceAllString(reason, " ")
	var b strings.Builder
	space := false
	for _, r := range reason {
		switch {
		case r == utf8.RuneError:
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func withActingReason(ctx context.Context, reason string) context.Context {
	reason = cleanAccessReason(reason)
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
			Builtin: set.Builtin, Person: set.Person, Denies: len(roleDenies(set.Name)) > 0, Resolved: set.Resolved})
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
	if err := checkAccessWriteCaller(ctx); err != nil {
		return AllowPrincipalResponse{}, err
	}
	if err := c.MigrateAllowedPrincipals(ctx); err != nil {
		return AllowPrincipalResponse{}, err
	}
	prefix, email, canonical, err := model.NormalizeAllowedPrincipalEntry(req.Entry)
	if err != nil {
		return AllowPrincipalResponse{}, validationErrorf("%v", err)
	}
	reason := cleanAccessReason(req.Reason)
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
	if err := c.checkAccessChange(ctx, accessChange{entry: canonical, role: role, reason: reason}); err != nil {
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
	if err := checkAccessWriteCaller(ctx); err != nil {
		return DenyPrincipalResponse{}, err
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
	reason := actingReasonFromContext(ctx)
	existing, ok, err := c.allowedPrincipalNamed(ctx, canonical)
	if err != nil {
		return DenyPrincipalResponse{}, err
	}
	current := existing.Role
	if ok && current == "" {
		current = model.DefaultAccessRole
	}
	if err := c.checkAccessChange(ctx, accessChange{entry: canonical, current: current, reason: reason,
		confirmSelf: confirmSelfFromContext(ctx)}); err != nil {
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
	if current != "" {
		summary += " (was " + current + ")"
	}
	if reason != "" {
		summary += " (" + reason + ")"
	}
	c.auditAccessChange(ctx, summary)
	return DenyPrincipalResponse{Entry: removedEntry, Removed: true}, nil
}

// SetPrincipalRole gives an entry already on the allowlist another role.
func (c *Client) SetPrincipalRole(ctx context.Context, rawEntry string, req SetPrincipalRoleRequest) (SetPrincipalRoleResponse, error) {
	if err := checkAccessWriteCaller(ctx); err != nil {
		return SetPrincipalRoleResponse{}, err
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
	reason := cleanAccessReason(req.Reason)
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
	current := existing.Role
	if current == "" {
		current = model.DefaultAccessRole
	}
	if err := c.checkAccessChange(ctx, accessChange{entry: canonical, current: current, role: role, reason: reason,
		confirmSelf: req.ConfirmSelf || confirmSelfFromContext(ctx)}); err != nil {
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

// accessChange is one change to the allowlist: entry going from role
// current ("" for an entry not on it yet) to role ("" for removing it).
type accessChange struct {
	entry, current, role string
	reason               string
	// confirmSelf is the caller saying yes to narrowing an entry that is
	// its own (docs/design/access-rbac.md §3).
	confirmSelf bool
}

// sensitiveRole reports whether role holds a dangerous scope (a set only a
// person may grant) or denies scopes: the roles a change to needs a reason
// and, from a verified session, a person.
func sensitiveRole(role string) bool {
	if role == "" {
		return false
	}
	if set, ok := model.APIScopeSetNamed(role); ok && set.Person {
		return true
	}
	return len(roleDenies(role)) > 0
}

// accessRulesHint is what every refusal of a verified admin's change ends
// with, so the person refused reads the rules they are held to.
const accessRulesHint = "a signed-in admin changes another entry's role, dangerous or deny roles included, when it holds every scope of both the old and new role and gives a reason; it never widens or lifts a deny on an entry that matches itself, and never removes the last full admin (the access code can still do any of these)"

func checkAccessWriteCaller(ctx context.Context) error {
	if isDelegatedRequest(ctx) {
		return forbiddenCodeErrorf("requires_person", "delegated requests may not change the allowed users, groups and delegating apps list")
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		return &ForbiddenError{err: fmt.Errorf("agent %s may not change who signs in to this deployment: only a person changes the allowed users and groups list", p.ID)}
	}
	return nil
}

// checkAccessChange refuses ch to a caller who may not make it
// (docs/design/access-rbac.md §3):
//
//   - delegated requests and agents never change the list;
//   - the access code (and every caller grain trusts as itself) may make
//     any change, with a reason when it assigns a dangerous role;
//   - a verified session may make a change only within its own role: it
//     holds every scope of the current role and of the new one (and every
//     scope a deny role vetoes);
//   - a change involving a dangerous or deny role needs a person and a
//     reason;
//   - an entry that matches the caller is never added, widened or given or
//     freed of a deny, and narrowing it needs ch.confirmSelf;
//   - the last entry holding a full role is never removed or narrowed.
func (c *Client) checkAccessChange(ctx context.Context, ch accessChange) error {
	if err := checkAccessWriteCaller(ctx); err != nil {
		return err
	}
	var newSet model.ResolvedAPIScopeSet
	if ch.role != "" {
		set, ok := model.APIScopeSetNamed(ch.role)
		if !ok {
			return validationErrorf("role %q is no scope set in effect (Settings -> Scope sets, or a built-in such as %s or grain/viewer)", ch.role, model.DefaultAccessRole)
		}
		newSet = set
	}
	p, hasPrincipal := PrincipalFromContext(ctx)
	if strings.HasPrefix(ch.entry, "delegate:") && hasPrincipal && p.Kind != model.PrincipalHuman {
		return forbiddenCodeErrorf("requires_person", "%s may not change %s: only a person may change delegate: entries", p.ID, ch.entry)
	}
	reasonNeeded := newSet.Person
	caller := accessRoleFromContext(ctx)
	if caller != nil {
		reasonNeeded = sensitiveRole(ch.current) || sensitiveRole(ch.role)
		if reasonNeeded && (!hasPrincipal || p.Kind != model.PrincipalHuman) {
			return &ForbiddenError{err: fmt.Errorf("%s may not change %s: a change involving a role that holds a dangerous scope or denies scopes is made only by a person (or with the access code)", caller.label(), ch.entry)}
		}
	}
	if reasonNeeded && cleanAccessReason(ch.reason) == "" {
		return validationErrorf("%s: give a reason to %s (--reason, or the reason field)", ch.entry, describeAccessChange(ch))
	}
	if caller == nil {
		return nil
	}
	if err := checkSelfAccessEscalation(ctx, caller, ch); err != nil {
		return err
	}
	for _, role := range []string{ch.current, ch.role} {
		if role == "" {
			continue
		}
		if err := caller.noWiderThan(resolveAccessRoles(ch.entry, []string{role})); err != nil {
			return &ForbiddenError{err: fmt.Errorf("role %s is wider than %s's own: %v (%s)", role, caller.Who, err, accessRulesHint)}
		}
		for sc := range roleDenies(role) {
			if !caller.has(sc) {
				return &ForbiddenError{err: fmt.Errorf("role %s denies %s, which %s does not hold, so it may not add or lift that deny (%s)", role, sc, caller.label(), accessRulesHint)}
			}
		}
	}
	if err := c.checkLastFullAdmin(ctx, ch); err != nil {
		return err
	}
	return checkSelfAccessConfirm(ctx, caller, ch)
}

// describeAccessChange is ch in words, for a message.
func describeAccessChange(ch accessChange) string {
	switch {
	case ch.role == "":
		return "remove it (role " + ch.current + ")"
	case ch.current == "":
		return "give it role " + ch.role
	default:
		return "change its role " + ch.current + " -> " + ch.role
	}
}

// checkSelfAccessEscalation refuses a change to an entry that admits the caller
// when it would add or widen what the entry grants, or add or lift a deny on
// it. Adding a matching entry is refused too so a caller admitted through a
// group cannot entrench a direct user: entry or re-POST a matching entry wider
// after DELETE?confirmSelf=1. A group whose membership cannot be looked up is
// taken to match.
func checkSelfAccessEscalation(ctx context.Context, caller *accessRole, ch accessChange) error {
	mine, err := caller.matchesEntry(ctx, ch.entry)
	how := "matches you"
	if err != nil {
		mine, how = true, "may match you (its membership could not be checked)"
	}
	if !mine || ch.current == ch.role {
		return nil
	}
	if len(roleDenies(ch.current)) > 0 || len(roleDenies(ch.role)) > 0 {
		return forbiddenCodeErrorf("self_access_change", "%s %s: you may not add or lift a deny on an entry that matches yourself; another admin, or the access code, can", ch.entry, how)
	}
	if ch.role != "" {
		if ch.current == "" {
			return forbiddenCodeErrorf("self_escalation", "%s %s: you may not add an entry that matches yourself (role %s); another admin, or the access code, can", ch.entry, how, ch.role)
		}
		if err := resolveAccessRoles(ch.entry, []string{ch.current}).noWiderThan(resolveAccessRoles(ch.entry, []string{ch.role})); err != nil {
			return forbiddenCodeErrorf("self_escalation", "%s %s: you may not widen it from %s to %s (%v); another admin, or the access code, can", ch.entry, how, ch.current, ch.role, err)
		}
	}
	return nil
}

// checkSelfAccessConfirm requires ch.confirmSelf before narrowing or removing
// an entry that admits the caller. It runs after checkLastFullAdmin so the last
// full admin receives last_admin directly instead of being asked to confirm a
// narrowing that will be refused anyway.
func checkSelfAccessConfirm(ctx context.Context, caller *accessRole, ch accessChange) error {
	mine, err := caller.matchesEntry(ctx, ch.entry)
	how := "matches you"
	if err != nil {
		mine, how = true, "may match you (its membership could not be checked)"
	}
	if !mine || ch.current == ch.role || ch.confirmSelf {
		return nil
	}
	return forbiddenCodeErrorf("confirm_self", "%s %s: confirm that you want to %s, which narrows your own access (confirmSelf, or --confirm-self)", ch.entry, how, describeAccessChange(ch))
}

// checkLastFullAdmin refuses removing or narrowing the only user: or
// group: entry whose role holds every scope, so a verified session cannot
// leave the deployment with nobody who can sign in as a full admin.
func (c *Client) checkLastFullAdmin(ctx context.Context, ch accessChange) error {
	if !fullAccessRole(ch.entry, ch.current) || fullAccessRole(ch.entry, ch.role) {
		return nil
	}
	entries, err := c.Store.ListAllowedPrincipals(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		role := e.Role
		if role == "" {
			role = model.DefaultAccessRole
		}
		if e.Entry != ch.entry && fullAccessRole(e.Entry, role) {
			return nil
		}
	}
	return forbiddenCodeErrorf("last_admin", "%s is the last entry holding a full admin role (%s): refusing to %s, which would leave nobody to sign in as a full admin; give another entry %s first (the access code can still recover the deployment)", ch.entry, ch.current, describeAccessChange(ch), model.DefaultAccessRole)
}

// fullAccessRole reports whether a user: or group: entry with role holds
// every scope on every repository.
func fullAccessRole(entry, role string) bool {
	if role == "" || !(strings.HasPrefix(entry, "user:") || strings.HasPrefix(entry, "group:")) {
		return false
	}
	return resolveAccessRoles(entry, []string{role}).Full
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

// accessHistoryLimit is how many access changes an unpaged GET
// /api/access/history (the Access page's own read) returns, newest first.
const accessHistoryLimit = 100

// handleAccessHistory is GET /api/access/history: the config_audit rows of
// surface "access", so Settings -> Access shows who changed which role,
// when and why to a caller holding access:read without settings:read.
func (s *Server) handleAccessHistory(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if !isAgentListCaller(r) && !r.URL.Query().Has("cursor") && !r.URL.Query().Has("q") {
		list.Paged = false
	}
	entries, err := s.tasks.Store.ConfigAudit(r.Context(), maxConfigAudit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	title := s.tasks.sessionTitles(r.Context())
	out := []ConfigAuditView{}
	for _, e := range entries {
		if e.Surface != "access" {
			continue
		}
		view := ConfigAuditView{
			ID:          e.ID,
			At:          e.At,
			Actor:       e.By.ID,
			ActorKind:   string(e.By.Kind),
			ActorSource: string(e.By.Source),
			ActorLabel:  sessionLabel(e.By, e.OnBehalfOf, title),
			Surface:     e.Surface,
			Summary:     e.Summary,
		}
		if !list.matches(view.Actor, view.ActorLabel, view.Summary) {
			continue
		}
		out = append(out, view)
		if !list.Paged && len(out) == accessHistoryLimit {
			break
		}
	}
	writeList(w, out, list)
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
	reason := r.Header.Get(ActingReasonHeader)
	if q := cleanAccessReason(r.URL.Query().Get("reason")); q != "" {
		reason = q
	}
	ctx := withActingReason(r.Context(), reason)
	ctx = withConfirmSelf(ctx, confirmSelfQuery(r.URL.Query().Get("confirmSelf")))
	out, err := s.tasks.DenyPrincipal(ctx, rawEntry)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// confirmSelfQuery reads ?confirmSelf=: 1, true or yes.
func confirmSelfQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
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
	return c.DenyPrincipalWith(ctx, entry, DenyPrincipalOptions{})
}

// DenyPrincipalWith is DELETE /api/access/principals/{entry} with a reason
// and confirmation.
func (c *HTTPClient) DenyPrincipalWith(ctx context.Context, entry string, opts DenyPrincipalOptions) (DenyPrincipalResponse, error) {
	var out DenyPrincipalResponse
	q := url.Values{}
	if r := strings.TrimSpace(opts.Reason); r != "" {
		q.Set("reason", r)
	}
	if opts.ConfirmSelf {
		q.Set("confirmSelf", "1")
	}
	path := "/api/access/principals/" + url.PathEscape(strings.TrimSpace(entry))
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	err := c.do(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}
