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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Roles for verified-identity sessions (docs/design/access-rbac.md). Every
// allowlist entry names a scope set; a person admitted by several entries
// holds the union of their roles' scopes, less anything any of them
// denies, and every route answers them only within it -- the same route
// table and repository reading agentAPIGate holds an agent to.

// scopeSignedIn is humanRouteScopes' word for a route any admitted caller
// may call, whatever their role.
const scopeSignedIn APIScope = "signed-in"

// humanRouteScopes is what an `internal` route of route_scopes.go needs of
// a person's role: the routes a person reaches from the UI that agents
// never call, plus the MCP endpoints whose dispatches hold each tool call
// to the caller's role in process. An internal route with no entry here is
// grain's own plumbing and answers only a role holding every scope
// (operatorOnlyRoutes names them, so TestRoleGateCoversEveryRoute can hold
// the two tables total). GET /api/config is the sign-in page's own read,
// so every admitted caller gets it, as do the hostd routes, which
// authenticate in their own protocol.
var humanRouteScopes = map[string]APIScope{
	"GET /api/config":                                               scopeSignedIn,
	"GET /api/auth/access-code":                                     scopeSignedIn,
	"POST /api/auth/access-code":                                    scopeSignedIn,
	"GET /api/auth/access-code/login.js":                            scopeSignedIn,
	"POST /api/auth/logout":                                         scopeSignedIn,
	"GET /api/auth/logout":                                          scopeSignedIn,
	"GET /api/auth/user":                                            scopeSignedIn,
	"GET /api/auth/status":                                          scopeSignedIn,
	"GET /api/whoami":                                               scopeSignedIn,
	"GET /api/auth/whoami":                                          scopeSignedIn,
	"GET /api/hostd/connect":                                        scopeSignedIn,
	"POST /api/hostd/join":                                          scopeSignedIn,
	"POST /api/hostd/join/{id}/poll":                                scopeSignedIn,
	"POST /api/hostd/renew":                                         scopeSignedIn,
	"GET /api/hostd/install.sh":                                     scopeSignedIn,
	"GET /hostd/install":                                            scopeSignedIn,
	"GET /api/hostd/agentd/{os}/{arch}":                             scopeSignedIn,
	"GET /api/hostd/agentd/{os}/{arch}/manifest.json":               scopeSignedIn,
	"GET /cli/install.sh":                                           scopeSignedIn,
	"POST /api/mcp":                                                 scopeSignedIn,
	"POST /mcp":                                                     scopeSignedIn,
	"POST /api/tasks/{id}/config-change/approve":                    "settings:write",
	"POST /api/tasks/{id}/config-change/reject":                     "settings:write",
	"POST /api/tasks/{id}/access-request/check":                     "capabilities:read",
	"POST /api/tasks/{id}/access-request/grant":                     "capabilities:admin",
	"POST /api/tasks/{id}/access-request/deny":                      "capabilities:admin",
	"POST /api/notifications/{id}/ack":                              "notifications:write",
	"POST /api/notifications/{id}/investigate":                      "tasks:write",
	"GET /api/local-script-approvals":                               "capabilities:read",
	"POST /api/tasks/{id}/local-script-approvals/{approval}/answer": "capabilities:admin",
	// Accepting a declaration takes the scopes its set needs, which
	// AcceptDeclaration asks for itself (declarationAcceptanceRefusal),
	// as revoking what was accepted does:
	// a dangerous api grant needs its own scope, not capabilities:admin.
	"GET /api/declarations":         "capabilities:read",
	"POST /api/declarations/accept": scopeSignedIn,
	"POST /api/declarations/revoke": scopeSignedIn,
}

// operatorOnlyRoutes are the internal routes no limited role reaches: a
// run's own plumbing, hostd enrollment administration, and a persistent
// sandbox's running service.
var operatorOnlyRoutes = map[string]bool{
	"POST /api/tasks/{id}/config-change":                            true,
	"POST /api/tasks/{id}/activity":                                 true,
	"PUT /api/tasks/{id}/session-title":                             true,
	"POST /api/tasks/{id}/staged-attachments":                       true,
	"POST /api/tasks/{id}/db/query":                                 true,
	"POST /api/secrets/redact":                                      true,
	"POST /api/tasks/{id}/db/execute":                               true,
	"POST /api/tasks/{id}/notify":                                   true,
	"POST /api/tasks/{id}/delivered":                                true,
	"POST /api/tasks/{id}/wait-credit":                              true,
	"POST /api/tasks/{id}/change-checks":                            true,
	"POST /api/tasks/{id}/change-review":                            true,
	"POST /api/tasks/{id}/local-script-approvals":                   true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/wait":   true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire": true,
	"GET /api/hostds/enrollment-codes":                              true,
	"POST /api/hostds/enrollment-codes":                             true,
	"POST /api/hostds/enrollment-codes/{name}/rotate":               true,
	"POST /api/hostds/enrollment-codes/{name}/revoke":               true,
	"GET /api/hostds/pending":                                       true,
	"POST /api/hostds/pending/{id}/accept":                          true,
	"POST /api/hostds/pending/{id}/reject":                          true,
	"/staging/{name}/{path...}":                                     true,
	// Repointing grain/config at another remote hands everything the
	// config repository grants -- host scripts, scope sets, secrets
	// bindings -- to whoever can push there, on the next sync, with no
	// Submit (task 3965). settings:write alone is not enough for that.
	"POST /api/state-repo/remote": true,
}

// accessRole is what a verified session's matching allowlist entries let
// it do.
type accessRole struct {
	// Who is the verified principal's email.
	Who string
	// Roles are the matching entries' roles, sorted, each once.
	Roles []string
	// Missing are Roles that name no scope set in effect, which grant
	// nothing.
	Missing []string
	grants  []string
	set     APIGrantSet
	deny    map[APIScope]bool
	// Full is true for a session holding every scope on every
	// repository with nothing denied: grain/operator, as everybody was
	// before roles.
	Full bool
	// Delegated is true when the request is a delegated (on-behalf-of) call
	// carrying a verified Via identity (AU-25).
	Delegated     bool
	DelegatedFrom string
	DelegatingApp string
	ViaLabel      string
	ViaAccess     string
	personRole    *accessRole
	// matches reports whether an allowlist entry admits this session --
	// its own user: entry, or a group: it is a member of -- so a change
	// to the list can tell an entry that is the caller's own
	// (docs/design/access-rbac.md §3). Nil matches the user: entry only.
	matches func(ctx context.Context, entry string) (bool, error)
}

// matchesEntry reports whether entry admits the session a describes. A
// group whose membership cannot be looked up is reported with the error.
func (a *accessRole) matchesEntry(ctx context.Context, entry string) (bool, error) {
	if a.matches != nil {
		return a.matches(ctx, entry)
	}
	if strings.HasPrefix(entry, "group:") {
		return false, errors.New("group membership checker unavailable")
	}
	return strings.EqualFold(entry, "user:"+a.Who) || strings.EqualFold(entry, "serviceAccount:"+a.Who), nil
}

// allows reports whether the role grants scope on repo.
func (a *accessRole) allows(scope APIScope, repo string) bool {
	return a.Full || (!a.deny[scope] && a.set.Allows(scope, repo))
}

// has reports whether the role grants scope on at least one repository.
func (a *accessRole) has(scope APIScope) bool {
	return a.Full || (!a.deny[scope] && a.set.Has(scope))
}

// label is how refusals name the caller: "alice@x (role grain/viewer)".
func (a *accessRole) label() string {
	roles := strings.Join(a.Roles, ", ")
	if roles == "" {
		roles = "none"
	}
	noun := "role"
	if len(a.Roles) > 1 {
		noun = "roles"
	}
	return fmt.Sprintf("%s (%s %s)", a.Who, noun, roles)
}

// scopes is every scope the role grants anywhere, sorted, and for each
// one limited to some repositories, which.
func (a *accessRole) scopes() ([]APIScope, map[APIScope][]string) {
	var out []APIScope
	limits := map[APIScope][]string{}
	for _, info := range model.APIScopes() {
		if !a.has(info.Scope) {
			continue
		}
		out = append(out, info.Scope)
		if !a.Full {
			if repos := a.set.Repos(info.Scope); len(repos) > 0 {
				limits[info.Scope] = repos
			}
		}
	}
	return out, limits
}

// noWiderThan reports why other is wider than a -- a scope, or a
// repository of one, other reaches and a does not -- or nil.
func (a *accessRole) noWiderThan(other *accessRole) error {
	if a.Full {
		return nil
	}
	for _, info := range model.APIScopes() {
		sc := info.Scope
		if !other.has(sc) {
			continue
		}
		if !a.has(sc) {
			return fmt.Errorf("it grants %s, which %s does not hold", sc, a.label())
		}
		repos := other.set.Repos(sc)
		if repos == nil && !other.Full {
			// other reaches sc on every repository.
			if !a.allows(sc, "") {
				return fmt.Errorf("it grants %s on every repository, which %s holds only on %s", sc, a.label(), strings.Join(a.set.Repos(sc), ", "))
			}
			continue
		}
		for _, repo := range repos {
			if !a.allows(sc, repo) {
				return fmt.Errorf("it grants %s on %s, which %s does not reach", sc, repo, a.label())
			}
		}
	}
	return nil
}

// resolveAccessRoles turns roles (scope set names) into what they grant
// together: the union of every role's scopes, repository limits kept per
// scope, less every scope any role's set -- or a set it includes --
// denies.
func resolveAccessRoles(who string, roles []string) *accessRole {
	a := &accessRole{Who: who, deny: map[APIScope]bool{}}
	var grants []string
	for _, role := range roles {
		if role == "" || slices.Contains(a.Roles, role) {
			continue
		}
		a.Roles = append(a.Roles, role)
		if _, ok := model.APIScopeSetNamed(role); !ok {
			a.Missing = append(a.Missing, role)
			continue
		}
		grants = append(grants, model.APIRef(role))
		for sc := range roleDenies(role) {
			a.deny[sc] = true
		}
	}
	sort.Strings(a.Roles)
	a.set = model.APIGrantsOf(grants).Without(a.deny)
	a.grants = roleGrantList(grants, a.set, a.deny)
	a.Full = len(a.deny) == 0
	for _, info := range model.APIScopes() {
		if !a.set.Allows(info.Scope, "") {
			a.Full = false
			break
		}
	}
	return a
}

// roleGrantList is the .../api#<role> list resolveAccessRoles built, or -- when a
// role's deny vetoed a scope -- the remaining per-scope grants from set so
// grant-escalation checks cannot pass on a vetoed scope.
func roleGrantList(grants []string, set APIGrantSet, deny map[APIScope]bool) []string {
	if len(deny) == 0 {
		return grants
	}
	var out []string
	for _, sc := range set.Scopes() {
		repos := set.Repos(sc)
		if len(repos) == 0 {
			out = append(out, model.APIRef(string(sc)))
			continue
		}
		for _, r := range repos {
			out = append(out, model.APIGrantString(string(sc), r))
		}
	}
	return out
}

// roleDenies is every scope the set named role, or any set it includes,
// denies (docs/design/access-rbac.md §2: in a role a deny is a veto).
func roleDenies(role string) map[APIScope]bool {
	out := map[APIScope]bool{}
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		set, ok := model.APIScopeSetNamed(name)
		if !ok {
			return
		}
		for _, p := range set.Deny {
			scopes, err := model.ExpandAPIScopePattern(p)
			if err != nil {
				continue
			}
			for _, sc := range scopes {
				out[sc] = true
			}
		}
		for _, inc := range set.Includes {
			walk(strings.TrimSpace(inc))
		}
	}
	walk(role)
	return out
}

type accessRoleKey struct{}

// delegatedIdentity records the verified delegating app and person for an
// admitted on-behalf-of request (AU-22..AU-26).
type delegatedIdentity struct {
	PersonEmail string
	AppEmail    string
	ViaLabel    string
	Grant       string
	Access      string
	JTI         string
	RequestID   string
}

type delegatedIdentityKey struct{}

func withDelegatedIdentity(ctx context.Context, d *delegatedIdentity) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, delegatedIdentityKey{}, d)
}

func delegatedIdentityFromContext(ctx context.Context) *delegatedIdentity {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(delegatedIdentityKey{}).(*delegatedIdentity)
	return d
}

func isDelegatedRequest(ctx context.Context) bool {
	if delegatedIdentityFromContext(ctx) != nil {
		return true
	}
	if a := accessRoleFromContext(ctx); a != nil && a.Delegated {
		return true
	}
	return false
}

// accessRoleSlot is where singleTenantGate leaves the role (and optional
// delegation) of the verified session it admitted, for serveHTTP to put on
// the request's context.
type accessRoleSlot struct {
	role       *accessRole
	delegation *delegatedIdentity
}

type accessRoleSlotKey struct{}

func withAccessRole(ctx context.Context, a *accessRole) context.Context {
	return context.WithValue(ctx, accessRoleKey{}, a)
}

// accessRoleFromContext is the role a verified session is held to, or nil
// for every other caller: the access code, loopback, in-process callers
// and agents (whose own gate holds them to their grants).
func accessRoleFromContext(ctx context.Context) *accessRole {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(accessRoleKey{}).(*accessRole)
	return a
}

func grantListFromSet(set APIGrantSet) []string {
	var out []string
	for _, sc := range set.Scopes() {
		repos := set.Repos(sc)
		if len(repos) == 0 {
			out = append(out, model.APIRef(string(sc)))
			continue
		}
		for _, r := range repos {
			out = append(out, model.APIGrantString(string(sc), r))
		}
	}
	return out
}

// intersectAccessRoles computes the effective role for a delegated request:
// personAR intersected with delegateAR (person_scopes ∩ delegate_scopes,
// per-repository limits intersected per scope, union of deny lists, Full: false)
// per AU-25.1.
func intersectAccessRoles(personEmail, appEmail, viaLabel, viaAccess string, personAR, delegateAR *accessRole) *accessRole {
	deny := make(map[APIScope]bool, len(personAR.deny)+len(delegateAR.deny))
	for sc := range personAR.deny {
		deny[sc] = true
	}
	for sc := range delegateAR.deny {
		deny[sc] = true
	}
	set := personAR.set.Intersect(delegateAR.set).Without(deny)
	grants := grantListFromSet(set)
	var roles []string
	for _, r := range personAR.Roles {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	for _, r := range delegateAR.Roles {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	sort.Strings(roles)
	missing := append(slices.Clone(personAR.Missing), delegateAR.Missing...)
	return &accessRole{
		Who:           fmt.Sprintf("%s (via %s)", personEmail, viaLabel),
		Roles:         roles,
		Missing:       missing,
		grants:        grants,
		set:           set,
		deny:          deny,
		Full:          false,
		Delegated:     true,
		DelegatedFrom: personEmail,
		DelegatingApp: appEmail,
		ViaLabel:      viaLabel,
		ViaAccess:     viaAccess,
		personRole:    personAR,
	}
}

// admitVerifiedPrincipal is checkAllowedPrincipal that also resolves the
// admitted session's role, and leaves it in the request's slot.
func (s *Server) admitVerifiedPrincipal(ctx context.Context, id VerifiedIdentity) error {
	if id.Via != nil {
		personID := VerifiedIdentity{Email: id.Email, Sub: id.Sub, Kind: id.Kind}
		personRoles, err := s.allowedPrincipalRoles(ctx, personID, true)
		if err != nil {
			logRefusedPrincipal(personID, err)
			return err
		}
		personEmail := normalizeIdentityEmail(id.Email)
		appEmail := normalizeIdentityEmail(id.Via.Email)
		delEntry, ok, err := s.delegateEntryFor(ctx, appEmail)
		if err != nil {
			return fmt.Errorf("delegating app %q cannot be authorized: %w", id.Via.Email, err)
		}
		if !ok {
			de := &delegateNotAllowedError{
				personEmail: personEmail,
				appEmail:    appEmail,
				label:       delegateDisplayLabel(id.Via),
				verifier:    string(AuthMethodHub),
			}
			logRefusedDelegate(personEmail, appEmail)
			return de
		}
		s.touchDelegateLastUsed(ctx, "delegate:"+appEmail)
		viaLabel := strings.TrimSpace(delEntry.Label)
		if viaLabel == "" {
			viaLabel = model.DelegateLabelForEmail(appEmail)
		} else {
			model.RegisterDelegateLabel(appEmail, viaLabel)
		}
		if slot, ok := ctx.Value(accessRoleSlotKey{}).(*accessRoleSlot); ok && slot != nil {
			personAR := resolveAccessRoles(personEmail, personRoles)
			delegateAR := resolveAccessRoles("delegate:"+appEmail, []string{delEntry.Role})
			slot.role = intersectAccessRoles(personEmail, appEmail, viaLabel, id.Via.Access, personAR, delegateAR)
			slot.delegation = &delegatedIdentity{
				PersonEmail: personEmail,
				AppEmail:    appEmail,
				ViaLabel:    viaLabel,
				Grant:       id.Via.Grant,
				Access:      id.Via.Access,
				JTI:         id.Via.JTI,
				RequestID:   id.Via.RequestID,
			}
		}
		return nil
	}
	roles, err := s.allowedPrincipalRoles(ctx, id, true)
	if err != nil {
		logRefusedPrincipal(id, err)
		return err
	}
	if slot, ok := ctx.Value(accessRoleSlotKey{}).(*accessRoleSlot); ok && slot != nil {
		email := normalizeIdentityEmail(id.Email)
		slot.role = resolveAccessRoles(email, roles)
		slot.role.matches = s.entryMatcher(id.Kind, email)
	}
	return nil
}

// entryMatcher is accessRole.matches for a verified session of kind as
// email: the entry naming it directly, or a group: entry it is a member of
// (groups never admit a service account, docs/design/access-rbac.md §2).
func (s *Server) entryMatcher(kind model.PrincipalKind, email string) func(ctx context.Context, entry string) (bool, error) {
	return func(ctx context.Context, entry string) (bool, error) {
		prefix, value, ok := strings.Cut(entry, ":")
		if !ok {
			return false, nil
		}
		switch prefix {
		case "user":
			return kind == model.PrincipalHuman && strings.EqualFold(value, email), nil
		case "serviceAccount":
			return kind == model.PrincipalService && strings.EqualFold(value, email), nil
		case "group":
			if kind != model.PrincipalHuman {
				return false, nil
			}
			return s.groupChecker().IsMember(ctx, value, email)
		}
		return false, nil
	}
}

// externalTokenOwnerCap refuses an external agent token wider than its
// owner's current role, so a gma_ token issued while its owner held
// grain/operator stops working when the owner is moved to a narrower role
// (docs/test-runs/2026-11-07-rbac-roles.md F2). It returns the HTTP status
// to refuse with, or nil. A failed group lookup that could narrow the owner
// refuses.
//
// An owner the allowlist does not match is refused while an identity
// verifier (IAP, the registry, or a wrapper's) is active: that list is then
// what admits people, so an owner removed from it (`grain access deny`), or
// never on it, has no role for the token to act within
// (docs/test-runs/2026-11-07-rbac-roles.md O4). With no verifier the list
// admits nobody -- single-user and access-code deployments, whose token
// owners were never on it -- and such a token is left as before. The
// deployment's local account, which a token issued without an owner acts
// for, is no email address and is never refused for not being listed.
func (s *Server) externalTokenOwnerCap(ctx context.Context, tok *model.ExternalAgentToken) (int, error) {
	owner := tok.OwnerPrincipal()
	if owner == nil || owner.Kind != model.PrincipalHuman {
		return 0, nil
	}
	email := normalizeIdentityEmail(owner.ID)
	roles, err := s.allowedPrincipalRoles(ctx, VerifiedIdentity{Email: email, Sub: email, Kind: model.PrincipalHuman}, true)
	if err != nil {
		var groupErr *groupAllowlistRefusalError
		if errors.As(err, &groupErr) {
			// Groups hold email addresses: an owner that is not one --
			// the deployment's local account, which a token issued
			// without an owner acts for -- is in none of them, so a
			// failed lookup cannot be hiding a role it holds.
			if !strings.Contains(email, "@") {
				return 0, nil
			}
			return http.StatusForbidden, fmt.Errorf("external agent token %s acts for %s, whose role cannot be checked: %s", tok.Name, email, groupErr.public)
		}
		if strings.Contains(email, "@") && len(activeVerifiers(ctx)) > 0 {
			return http.StatusForbidden, fmt.Errorf("external agent token %s acts for %s, who holds no role on this deployment: %v; add them back in Settings -> Access, or revoke the token", tok.Name, email, err)
		}
		return 0, nil
	}
	ownerAR := resolveAccessRoles(email, roles)
	if ownerAR.Full {
		return 0, nil
	}
	_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
	if err != nil {
		return 0, nil
	}
	tokAR := &accessRole{Who: "external:" + tok.Name, set: set, deny: map[APIScope]bool{}}
	if err := ownerAR.noWiderThan(tokAR); err != nil {
		return http.StatusForbidden, fmt.Errorf("external agent token %s is wider than its owner's current role: %v; re-issue it within %s", tok.Name, err, strings.Join(ownerAR.Roles, ", "))
	}
	return 0, nil
}

// roleGate wraps one route: a verified session is answered only within
// its role (docs/design/access-rbac.md §5). Everybody else passes through
// to agentAPIGate.
func (s *Server) roleGate(pattern string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := accessRoleFromContext(r.Context())
		if a == nil {
			next(w, r)
			return
		}
		if a.Delegated {
			if a.ViaAccess == "read" && r.Method != http.MethodGet && r.Method != http.MethodHead {
				err := fmt.Errorf("%s holds read-only delegation (act.access=read) and cannot call %s", a.label(), pattern)
				log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error":         err.Error(),
					"code":          "read_only_delegation",
					"delegatedFrom": a.DelegatedFrom,
					"delegatingApp": a.DelegatingApp,
				})
				return
			}
			if err := s.checkDelegatedPersonOnly(r, pattern, a); err != nil {
				log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error":         err.Error(),
					"code":          "requires_person",
					"delegatedFrom": a.DelegatedFrom,
					"delegatingApp": a.DelegatingApp,
				})
				return
			}
		}
		if a.Full {
			next(w, r)
			return
		}
		if err := s.authorizeRoleCall(r, pattern, a); err != nil {
			log.Printf("grain api: refused {human, %s} %s: %v", a.Who, pattern, err)
			if a.Delegated {
				writeJSON(w, agentRefusalStatus(err), map[string]any{
					"error":         err.Error(),
					"delegatedFrom": a.DelegatedFrom,
					"delegatingApp": a.DelegatingApp,
				})
				return
			}
			writeError(w, agentRefusalStatus(err), err)
			return
		}
		next(w, r)
	}
}

func (s *Server) checkDelegatedPersonOnly(r *http.Request, pattern string, a *accessRole) error {
	switch pattern {
	case "POST /api/access/principals",
		"PATCH /api/access/principals/{entry}",
		"DELETE /api/access/principals/{entry}":
		return fmt.Errorf("%s cannot call %s: modifying the allowed users, groups and delegating apps list requires a person acting directly", a.label(), pattern)
	case "POST /api/hostds",
		"POST /api/hostds/enrollment-codes",
		"POST /api/hostds/pending/{id}/accept":
		return fmt.Errorf("%s cannot call %s: enrolling a hostd requires a person acting directly", a.label(), pattern)
	case "POST /api/tasks/{id}/access-request/grant",
		"POST /api/tasks/{id}/access-request/deny":
		return fmt.Errorf("%s cannot call %s: deciding an access request requires a person acting directly", a.label(), pattern)
	case "POST /api/external-agents/tokens":
		body, err := peekBody(r)
		if err == nil && len(body) > 0 {
			var req CreateExternalAgentTokenRequest
			if json.Unmarshal(body, &req) == nil {
				if _, _, _, _, _, needsPerson, nerr := model.NormalizeExternalAgentSpec(req.ScopeSet, req.Scopes, req.Repos); nerr == nil && needsPerson {
					return fmt.Errorf("%s cannot call %s: minting an external agent token with a dangerous or requires_person scope requires a person acting directly", a.label(), pattern)
				}
			}
		}
	case "POST /api/external-agents/tokens/{id}/rotate":
		if s.tasks != nil && s.tasks.Store != nil {
			if tok, err := s.tasks.Store.GetExternalAgentToken(r.Context(), strings.TrimSpace(r.PathValue("id"))); err == nil && tok != nil {
				if _, _, needsPerson, rerr := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos); rerr == nil && needsPerson {
					return fmt.Errorf("%s cannot call %s: rotating an external agent token with a dangerous or requires_person scope requires a person acting directly", a.label(), pattern)
				}
			}
		}
	case "POST /api/tasks/{id}/approve", "POST /api/tasks/{id}/run-now":
		if s.tasks != nil && s.tasks.Store != nil {
			if task, err := s.tasks.Store.GetTask(r.Context(), model.NormalizeTaskID(r.PathValue("id"))); err == nil && task != nil {
				if err := s.tasks.catalogueApprovalRefusal(r.Context(), *task); err != nil {
					return err
				}
			}
		}
	case "POST /api/tasks/{id}/capabilities", "PATCH /api/tasks/{id}", "POST /api/tasks/{id}/capability-grants":
		body, err := peekBody(r)
		if err == nil && len(body) > 0 {
			var stored map[string][]string
			if s.tasks != nil && s.tasks.Store != nil {
				stored = s.storedFieldGrants(r.Context(), pattern, "")
			}
			normalize := s.normalizeCapFn()
			for _, gf := range addedGrantFieldsInBody(pattern, body, stored, normalize) {
				trimmed := strings.TrimSpace(gf.name)
				if model.IsPersonOnlyGrant(trimmed) || model.IsAdminOnlyGrant(trimmed) || grantNamesPersonScopeSet(trimmed) {
					return fmt.Errorf("%s cannot set %s: %s requires a person acting directly", a.label(), gf.key, trimmed)
				}
			}
		}
	}
	return nil
}

// RoleScopeOf is the scope pattern needs of a person's role: "" for a
// route only a role holding every scope reaches.
func RoleScopeOf(pattern string) APIScope {
	return roleScopeOf(pattern)
}

func roleScopeOf(pattern string) APIScope {
	if sc, ok := humanRouteScopes[pattern]; ok {
		return sc
	}
	sc := routeScopes[pattern]
	if sc == scopeInternal || operatorOnlyRoutes[pattern] {
		return ""
	}
	return sc
}

// authorizeRoleCall decides whether a limited role may make this call.
func (s *Server) authorizeRoleCall(r *http.Request, pattern string, a *accessRole) error {
	const ask = "ask an access-code admin to assign a role that includes it (docs/access-code.md)"
	scope := roleScopeOf(pattern)
	switch scope {
	case scopeSignedIn:
		return nil
	case "":
		return fmt.Errorf("%s holds a limited role, and %s answers only a role holding every scope (grain/operator): %s",
			a.label(), pattern, ask)
	}
	if !a.has(scope) {
		if len(a.Missing) > 0 {
			return fmt.Errorf("%s holds no scope %s, which %s needs (no scope set is named %s): %s",
				a.label(), scope, pattern, strings.Join(a.Missing, ", "), ask)
		}
		return fmt.Errorf("%s holds no scope %s, which %s needs: %s", a.label(), scope, pattern, ask)
	}
	mutating := r.Method != http.MethodGet && !strings.HasSuffix(string(scope), ":read")
	if a.allows(scope, "") && !mutating {
		return nil
	}
	if !mutating && (s.tasks == nil || s.tasks.Store == nil) {
		return nil
	}
	body, err := peekBody(r)
	if err != nil {
		return err
	}
	var repo string
	if s.tasks != nil && s.tasks.Store != nil {
		var target string
		if id := r.PathValue("id"); id != "" &&
			(strings.HasPrefix(pathOf(pattern), "/api/tasks/{id}") || strings.HasPrefix(pathOf(pattern), "/api/console/{id}")) {
			target = id
		}
		callRepo, callErr := s.agentCallRepo(r.Context(), r, pattern, target, body, nil, a.set, scope, false)
		if callErr != nil {
			if !a.allows(scope, "") {
				return callErr
			}
		} else {
			repo = callRepo
		}
		if repo == "" && pattern == "POST /api/tasks" && len(body) > 0 {
			var req struct {
				TemplateID string `json:"templateId"`
			}
			if json.Unmarshal(body, &req) == nil && strings.TrimSpace(req.TemplateID) != "" {
				if tmpl := s.gateTemplate(r.Context(), req.TemplateID); tmpl != nil && tmpl.Target != nil {
					repo = tmpl.Target.String()
				}
			}
		}
	}
	if repo == "" && (pattern == "GET /api/persistent-sandboxes/{name}/logs" || pattern == "POST /api/persistent-sandboxes/{name}/{action}") {
		repo = s.persistentSandboxRepo(r.Context(), r.PathValue("name"))
	}
	if !a.allows(scope, "") {
		if _, fs := fsRoutes[pattern]; (fs || pattern == "GET /api/events" || pattern == "GET /api/deployment/reachable-repos") && repo == "" {
			// A namespace read, event stream or reachable-repos listing naming no
			// repository is narrowed to the ones the role reaches by fsScope /
			// eventsReadable / reposReadable, as an agent's is.
			return nil
		}
		if (pattern == "GET /api/inbox" && strings.TrimSpace(r.URL.Query().Get("repo")) == "") || (isRepoNarrowedTasksRead(pattern, r) && repo == "") {
			return nil
		}
		if !a.allows(scope, repo) {
			if repo == "" {
				return fmt.Errorf("%s holds %s only on %s, and %s does not name one of them",
					a.label(), scope, strings.Join(a.set.Repos(scope), ", "), pattern)
			}
			return fmt.Errorf("%s holds %s, but not on %s: %s", a.label(), scope, repo, ask)
		}
		if !mutating {
			return nil
		}
	}
	ctx := r.Context()
	stored := s.storedFieldGrants(ctx, pattern, repo)
	normalize := s.normalizeCapFn()
	if _, isGrantBody := grantBodies[pattern]; isGrantBody {
		if err := roleGrantEscalation(a, pattern, body, stored, normalize); err != nil {
			return err
		}
	}
	allowedRepo := func(need APIScope, targetRepo string) bool {
		targetRepo = strings.TrimSpace(targetRepo)
		if targetRepo == "" {
			return true
		}
		return a.allows(need, targetRepo)
	}
	if err := s.filingEscalation(ctx, a.grants, a.set, allowedRepo, pattern, scope, repo, body, filingEscalationOpts{
		role:      a,
		stored:    stored,
		normalize: normalize,
	}); err != nil {
		return err
	}
	if err := roleGrantEscalation(a, pattern, body, stored, normalize); err != nil {
		return err
	}
	return s.roleStandingTargetEscalation(ctx, r, pattern, scope, repo, a, allowedRepo, normalize)
}

// roleGrantEscalation refuses a body that grants a capability outside a's own
// scopes, a dangerous grant, or a person-only scope set.
func roleGrantEscalation(a *accessRole, pattern string, body []byte, stored map[string][]string, normalize func(string) (string, error)) error {
	for _, gf := range addedGrantFieldsInBody(pattern, body, stored, normalize) {
		if err := roleGrantRefusal(a, gf.key, gf.name, normalize); err != nil {
			return err
		}
	}
	return nil
}

func roleGrantRefusal(a *accessRole, key, name string, normalize func(string) (string, error)) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil
	}
	if grantNamesPersonScopeSet(trimmed) {
		return fmt.Errorf("%s cannot set %s: %s names a person-only scope set, which requires an unlimited role (grain/operator)",
			a.label(), key, trimmed)
	}
	if err := agentGrantRefusal(a.grants, trimmed, normalize); err != nil {
		if slices.ContainsFunc(APIGrantsOf([]string{trimmed}).Scopes(), IsDangerousAPIScope) ||
			model.IsDangerousAPIGrant(trimmed) || model.IsDangerousGrant(trimmed) ||
			trimmed == model.PriorityDispatchCapability || trimmed == model.DurableCapability {
			return fmt.Errorf("%s cannot set %s: %s requires an unlimited role (grain/operator)", a.label(), key, trimmed)
		}
		return fmt.Errorf("%s cannot set %s: cannot grant %s, which the role does not hold", a.label(), key, trimmed)
	}
	return nil
}

// admissionGrantName is the retired grant an admission above standard
// stands for (priority-dispatch for bypass, durable for durable), or ""
// for standard: the role checks below refused a template or schedule
// carrying either grant, and refuse one carrying the setting that
// replaced them the same way (unified-grants.design.md §4.1).
func admissionGrantName(adm model.Admission) string {
	switch model.NormalizeAdmission(adm) {
	case model.AdmissionDurable:
		return model.DurableCapability
	case model.AdmissionBypass:
		return model.PriorityDispatchCapability
	}
	return ""
}

func roleTemplateWithin(a *accessRole, key string, tmpl model.Template, allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	for _, g := range tmpl.Grants {
		if err := roleGrantRefusal(a, key, g.Capability, normalize); err != nil {
			return err
		}
	}
	if name := admissionGrantName(tmpl.Admission); name != "" {
		if err := roleGrantRefusal(a, key, name, normalize); err != nil {
			return err
		}
	}
	for _, w := range tmpl.Writes {
		if !allowedRepo("tasks:write", w.Repo.String()) {
			return fmt.Errorf("%s cannot set %s: template %s grants push access to %s, which role's tasks:write does not reach",
				a.label(), key, tmpl.ID, w.Repo)
		}
	}
	for _, r := range tmpl.Reads {
		if !allowedRepo("tasks:read", r.String()) {
			return fmt.Errorf("%s cannot set %s: template %s grants read access to %s, which role's tasks:read does not reach",
				a.label(), key, tmpl.ID, r)
		}
	}
	return nil
}

// roleStandingTargetEscalation holds indirect filings and edits of stored
// templates, schedules and suites to a's own scopes even when the request body
// leaves their grants untouched.
func (s *Server) roleStandingTargetEscalation(ctx context.Context, r *http.Request, pattern string, scope APIScope, repo string,
	a *accessRole, allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	opts := filingEscalationOpts{role: a, normalize: normalize}
	switch pattern {
	case "POST /api/notifications/{id}/investigate":
		if tmpl := model.SystemTemplate(model.InvestigateNotificationTemplateID, model.RepoRef{}); tmpl != nil {
			return roleTemplateWithin(a, "capabilities", *tmpl, allowedRepo, normalize)
		}
	case "PATCH /api/templates/{id}":
		if s.tasks != nil && s.tasks.Store != nil {
			id := r.PathValue("id")
			existing, err := s.tasks.Store.GetTemplate(ctx, id)
			if err != nil {
				return fmt.Errorf("reading template %s: %w", id, err)
			}
			if existing != nil {
				return s.checkFilingTemplate(ctx, *existing, "template", a.grants, a.set, allowedRepo, scope, repo, opts)
			}
			return s.checkDeclaredPushID(ctx, id, "template", opts)
		}
	case "POST /api/schedules":
		if !a.has("tasks:write") {
			return fmt.Errorf("%s cannot create a schedule: firing scheduled tasks needs tasks:write", a.label())
		}
		if repo != "" {
			if !a.allows("tasks:write", repo) {
				return fmt.Errorf("%s cannot set repo: role does not hold tasks:write on %s", a.label(), repo)
			}
			if parsed, err := model.ParseRepo(repo); err == nil {
				if why := model.ConfigRepoProposalHold(model.Task{Target: &parsed}); why != "" {
					return fmt.Errorf("%s cannot create a schedule: %s, which requires an unlimited role (grain/operator)", a.label(), why)
				}
			}
		}
	case "PATCH /api/schedules/{id}", "POST /api/schedules/{id}/run":
		if s.tasks != nil && s.tasks.Store != nil {
			sched, err := s.tasks.Store.GetSchedule(ctx, r.PathValue("id"))
			if err != nil {
				return fmt.Errorf("reading schedule %s: %w", r.PathValue("id"), err)
			}
			if sched == nil {
				return nil
			}
			schedRepo := ""
			if sched.Target.Owner != "" {
				schedRepo = sched.Target.String()
				if !a.allows("tasks:write", schedRepo) {
					return fmt.Errorf("%s cannot modify or run schedule %s: role does not hold tasks:write on %s",
						a.label(), sched.ID, schedRepo)
				}
			}
			if sched.AutoMerge {
				return fmt.Errorf("%s cannot modify or run schedule %s (autoMerge): auto-merge requires an unlimited role (grain/operator)",
					a.label(), sched.ID)
			}
			if sched.KeepRunning {
				return fmt.Errorf("%s cannot modify or run schedule %s (keepRunning): keepRunning requires an unlimited role (grain/operator)",
					a.label(), sched.ID)
			}
			if why := model.ConfigRepoProposalHold(model.Task{Target: &sched.Target, Writes: sched.Writes}); why != "" {
				return fmt.Errorf("%s cannot modify or run schedule %s: %s, which requires an unlimited role (grain/operator)",
					a.label(), sched.ID, why)
			}
			if s.networkAccessWidens(ctx, "", string(sched.NetworkAccess)) {
				return fmt.Errorf("%s cannot modify or run schedule %s (networkAccess): widening networkAccess past this deployment's default requires an unlimited role (grain/operator)",
					a.label(), sched.ID)
			}
			if sched.AutoApproveProposals && !a.allows("tasks:admin", schedRepo) {
				return fmt.Errorf("%s cannot modify or run schedule %s (autoApproveProposals): needs tasks:admin",
					a.label(), sched.ID)
			}
			for _, g := range sched.Grants {
				if err := roleGrantRefusal(a, "capabilities", g.Capability, normalize); err != nil {
					return err
				}
			}
			if name := admissionGrantName(sched.Admission); name != "" {
				if err := roleGrantRefusal(a, "admission", name, normalize); err != nil {
					return err
				}
			}
			for _, w := range sched.Writes {
				if !allowedRepo("tasks:write", w.Repo.String()) {
					return fmt.Errorf("%s cannot modify or run schedule %s (writes): role does not hold tasks:write on %s",
						a.label(), sched.ID, w.Repo)
				}
			}
			for _, rd := range sched.Reads {
				if !allowedRepo("tasks:read", rd.String()) {
					return fmt.Errorf("%s cannot modify or run schedule %s (reads): role does not hold tasks:read on %s",
						a.label(), sched.ID, rd)
				}
			}
			if sched.TemplateID != nil && *sched.TemplateID != "" {
				if tmpl := s.gateTemplate(ctx, *sched.TemplateID); tmpl != nil {
					if err := s.checkFilingTemplate(ctx, *tmpl, "templateId", a.grants, a.set, allowedRepo, scope, schedRepo, opts); err != nil {
						return err
					}
				} else if err := s.checkDeclaredPushID(ctx, *sched.TemplateID, "templateId", opts); err != nil {
					return err
				}
			}
			if sched.SuiteID != nil && *sched.SuiteID != "" {
				suite, err := s.tasks.Store.GetSuite(ctx, *sched.SuiteID)
				if err != nil {
					return fmt.Errorf("reading suite %s: %w", *sched.SuiteID, err)
				}
				if suite != nil {
					if err := s.checkRoleSuite(ctx, *suite, "suiteId", a.grants, a.set, allowedRepo, scope, schedRepo, opts); err != nil {
						return err
					}
				}
			}
		}
	case "PATCH /api/suites/{id}":
		if s.tasks != nil && s.tasks.Store != nil {
			suite, err := s.tasks.Store.GetSuite(ctx, r.PathValue("id"))
			if err != nil {
				return fmt.Errorf("reading suite %s: %w", r.PathValue("id"), err)
			}
			if suite != nil {
				return s.checkRoleSuite(ctx, *suite, "suite", a.grants, a.set, allowedRepo, scope, repo, opts)
			}
		}
	case "POST /api/tasks/{id}/approve", "POST /api/tasks/{id}/run-now":
		if s.tasks != nil && s.tasks.Store != nil {
			task, err := s.tasks.Store.GetTask(ctx, model.NormalizeTaskID(r.PathValue("id")))
			if err != nil || task == nil {
				return nil
			}
			if task.Approval != nil {
				if pattern == "POST /api/tasks/{id}/approve" {
					return nil
				}
				// Running an already-approved task needs dispatch:write rather
				// than tasks:admin, while still holding the run to grants and
				// settings within the caller's own role.
				return s.tasks.roleTaskWithin(ctx, a, *task, "run", allowedRepo, normalize)
			}
			return s.roleApprovalWithin(ctx, a, *task, allowedRepo, normalize)
		}
	case "PATCH /api/tasks/{id}",
		"POST /api/tasks/{id}/retry",
		"POST /api/tasks/{id}/reopen",
		"POST /api/tasks/{id}/undefer",
		"POST /api/tasks/{id}/resume",
		"POST /api/tasks/{id}/comments",
		"POST /api/tasks/{id}/reply":
		if s.tasks != nil && s.tasks.Store != nil {
			task, err := s.tasks.Store.GetTask(ctx, model.NormalizeTaskID(r.PathValue("id")))
			if err != nil || task == nil {
				return nil
			}
			verb := "modify"
			switch pattern {
			case "POST /api/tasks/{id}/retry":
				verb = "retry"
			case "POST /api/tasks/{id}/reopen":
				verb = "reopen"
			case "POST /api/tasks/{id}/undefer":
				verb = "undefer"
			case "POST /api/tasks/{id}/resume":
				verb = "resume"
			case "POST /api/tasks/{id}/comments":
				verb = "comment on"
				// "Reopen and send" (addCommentRequest.Reopen) is a reopen
				// as well as a comment, and answers to both.
				if commentAsksReopen(r) {
					if err := s.tasks.roleTaskWithin(ctx, a, *task, "reopen", allowedRepo, normalize); err != nil {
						return err
					}
				}
			case "POST /api/tasks/{id}/reply":
				verb = "reply to"
			}
			return s.tasks.roleTaskWithin(ctx, a, *task, verb, allowedRepo, normalize)
		}
	case "PUT /api/console-sessions/{id}",
		"POST /api/console-sessions/{id}/restart",
		"POST /api/console-sessions/{id}/cancel",
		"DELETE /api/console-sessions/{id}":
		if s.tasks != nil && s.tasks.Store != nil {
			return s.tasks.roleConsoleSessionWithin(ctx, a, r.PathValue("id"), allowedRepo, normalize)
		}
	case "POST /api/suite-runs":
		// suiteId in the body is already checked by filingEscalation.
	}
	return nil
}

func (s *Server) persistentSandboxRepo(ctx context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || s.tasks == nil {
		return ""
	}
	if ps := s.tasks.Config.PersistentSandboxes; ps != nil {
		if list, err := ps.List(ctx); err == nil {
			for _, st := range list {
				if st.Name == name && strings.TrimSpace(st.Repo) != "" {
					if canonical, cerr := s.agentCanonicalRepo(st.Repo); cerr == nil {
						return canonical
					}
					return strings.TrimSpace(st.Repo)
				}
			}
		}
	}
	return ""
}

func grantDeniedByRole(a *accessRole, name string) bool {
	if a == nil || len(a.deny) == 0 {
		return false
	}
	for _, sc := range APIGrantsOf([]string{strings.TrimSpace(name)}).Scopes() {
		if a.deny[sc] {
			return true
		}
	}
	return false
}

// roleApprovalWithin refuses a limited role approving stored task -- by
// POST /api/tasks/{id}/approve, or POST /api/tasks/{id}/run-now on a task
// with no approval yet (Client.RunNow approves it first) -- unless the role
// could have filed task approved itself: it needs tasks:admin on task's
// repository, and every grant, setting, write, read and review template task
// carries must be one filingEscalation would have let it set on POST /api/tasks.
func (s *Server) roleApprovalWithin(ctx context.Context, a *accessRole, task model.Task,
	allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	return s.tasks.roleApprovalWithin(ctx, a, task, allowedRepo, normalize)
}

func (c *Client) roleApprovalWithin(ctx context.Context, a *accessRole, task model.Task,
	allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	repo := taskRepo(&task)
	if !a.allows("tasks:admin", repo) {
		if repo == "" {
			return fmt.Errorf("%s cannot approve task %s: approving work needs tasks:admin", a.label(), task.ID)
		}
		return fmt.Errorf("%s cannot approve task %s: approving work needs tasks:admin on %s", a.label(), task.ID, repo)
	}
	return c.roleTaskWithin(ctx, a, task, "approve", allowedRepo, normalize)
}

// roleTaskWithin refuses a limited role acting on stored task when task carries
// grants, writes, reads, settings or review templates beyond what the role may
// attach or run.
func (c *Client) roleTaskWithin(ctx context.Context, a *accessRole, task model.Task, verb string,
	allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	isSession := task.IsConsoleSession()
	if isSession && verb != "approve" && !a.has("sessions:write") && verb != "access console of" {
		return fmt.Errorf("%s cannot %s task %s: standing console sessions require scope sessions:write", a.label(), verb, task.ID)
	}
	if isSession && verb == "comment on" && a.Delegated && a.personRole != nil {
		// Messaging a console session through an authorized delegating app
		// (e.g. grain/delegated, which holds sessions:write and tasks:write
		// without dangerous scopes) attributes the session turn to the verified
		// human speaker (AU-26, sessionTurnAuthor); once the intersection
		// admits sessions:write, hold the session's envelope to that speaker's
		// own role.
		if a.personRole.Full {
			return nil
		}
		a = a.personRole
		allowedRepo = func(need APIScope, targetRepo string) bool {
			targetRepo = strings.TrimSpace(targetRepo)
			if targetRepo == "" {
				return true
			}
			return a.allows(need, targetRepo)
		}
	}
	if task.AutoMerge {
		return fmt.Errorf("%s cannot %s task %s (autoMerge): auto-merge requires an unlimited role (grain/operator)",
			a.label(), verb, task.ID)
	}
	if task.KeepRunning {
		return fmt.Errorf("%s cannot %s task %s (keepRunning): keepRunning requires an unlimited role (grain/operator)",
			a.label(), verb, task.ID)
	}
	if push, err := c.templateDeclaredPush(ctx, task.TemplateID); err != nil {
		return err
	} else if push != nil {
		return fmt.Errorf("%s cannot %s task %s (declaredPush): template %s carries a declared push set (%s), which requires an unlimited role (grain/operator)",
			a.label(), verb, task.ID, strings.TrimSpace(task.TemplateID), push.Path)
	}
	if why := model.ConfigRepoProposalHold(task); why != "" {
		return fmt.Errorf("%s cannot %s task %s: %s, which requires an unlimited role (grain/operator)", a.label(), verb, task.ID, why)
	}
	if needs, why := model.ChangeTargetNeedsPersonApproval(task.ChangeTarget); needs {
		return fmt.Errorf("%s cannot %s task %s: %s, which requires an unlimited role (grain/operator)", a.label(), verb, task.ID, why)
	}
	netAccess := string(task.NetworkAccess)
	var sessionCfg *model.ConsoleSession
	if sessionID, ok := model.ConsoleSessionIDOf(task); ok {
		if cfg, err := c.consoleSessionConfig(ctx); err == nil {
			if sess, found := model.ConsoleSessionByID(model.ConsoleSessionsOr(cfg.ConsoleSessions), sessionID); found {
				sessionCfg = &sess
				netAccess = string(sess.NetworkAccess)
			}
		}
	}
	if c.networkAccessWidens(ctx, "", netAccess) {
		return fmt.Errorf("%s cannot %s task %s (networkAccess): widening networkAccess past this deployment's default requires an unlimited role (grain/operator)",
			a.label(), verb, task.ID)
	}
	if err := c.roleSandboxShapeRefusal(ctx, a, model.TaskSandboxShape(task), verb+" task "+task.ID); err != nil {
		return err
	}
	// An agent's proposal asking for a larger sandbox than the task that
	// proposed it runs in waits for an unlimited role
	// (model.ProposalSandboxShapeAbove); a proposer no longer stored is
	// measured as one at grain's own default shape.
	if verb == "approve" {
		if up := proposedByOf(task); up != "" {
			parent, err := c.Store.GetTask(ctx, up)
			if err != nil {
				return fmt.Errorf("reading task %s, which proposed %s: %w", up, task.ID, err)
			}
			if parent == nil {
				parent = &model.Task{ID: up}
			}
			if why := model.ProposalSandboxShapeAbove(task, *parent); why != "" {
				return fmt.Errorf("%s cannot approve task %s: %s, which requires an unlimited role (grain/operator)",
					a.label(), task.ID, why)
			}
		}
	}
	for _, g := range task.Grants {
		if verb != "approve" && g.Via == model.GrantByDefault && !grantDeniedByRole(a, g.Capability) {
			continue
		}
		if err := roleGrantRefusal(a, "capabilities", g.Capability, normalize); err != nil {
			return err
		}
	}
	if !(isSession && verb != "approve" && model.NormalizeAdmission(task.Admission) == model.AdmissionDurable) {
		if name := admissionGrantName(task.Admission); name != "" {
			if err := roleGrantRefusal(a, "admission", name, normalize); err != nil {
				return err
			}
		}
	}
	for _, w := range task.Writes {
		if !allowedRepo("tasks:write", w.Repo.String()) {
			return fmt.Errorf("%s cannot %s task %s (writes): role does not hold tasks:write on %s",
				a.label(), verb, task.ID, w.Repo)
		}
	}
	if sessionCfg != nil {
		if sessionCfg.TasksApproved && !a.allows("tasks:admin", "") {
			return fmt.Errorf("%s cannot %s task %s (tasksApproved): approving session proposals needs tasks:admin",
				a.label(), verb, task.ID)
		}
		if sessionCfg.ReadsAllRepos() && !a.allows("tasks:read", "") {
			return fmt.Errorf("%s cannot %s task %s (readsAll): reading every repository requires tasks:read without repository limits",
				a.label(), verb, task.ID)
		}
		stateRepo := c.stateRepoRef()
		for _, rd := range task.Reads {
			if stateRepo.Owner != "" && rd.Same(stateRepo) && a.allows("settings:read", "") {
				continue
			}
			if !allowedRepo("tasks:read", rd.String()) {
				return fmt.Errorf("%s cannot %s task %s (reads): role does not hold tasks:read on %s",
					a.label(), verb, task.ID, rd)
			}
		}
	} else {
		for _, rd := range task.Reads {
			if !allowedRepo("tasks:read", rd.String()) {
				return fmt.Errorf("%s cannot %s task %s (reads): role does not hold tasks:read on %s",
					a.label(), verb, task.ID, rd)
			}
		}
	}
	if id := strings.TrimSpace(task.ReviewTemplateID); id != "" &&
		!(task.Target != nil && id == model.DefaultReviewTemplateID(*task.Target)) {
		if tmpl := c.gateTemplate(ctx, id); tmpl != nil &&
			!(task.Target != nil && tmpl.ID == model.DefaultReviewTemplateID(*task.Target)) {
			if err := roleTemplateWithin(a, "reviewTemplateId", *tmpl, allowedRepo, normalize); err != nil {
				why := strings.TrimPrefix(err.Error(), a.label()+" cannot set reviewTemplateId: ")
				return fmt.Errorf("%s cannot %s task %s (reviewTemplateId): %s", a.label(), verb, task.ID, why)
			}
		}
	}
	return nil
}

func (c *Client) roleConsoleSessionWithin(ctx context.Context, a *accessRole, id string,
	allowedRepo func(APIScope, string) bool, normalize func(string) (string, error)) error {
	cfg, err := c.consoleSessionConfig(ctx)
	if err != nil {
		return err
	}
	session, ok := model.ConsoleSessionByID(model.ConsoleSessionsOr(cfg.ConsoleSessions), strings.TrimSpace(id))
	if !ok {
		return nil
	}
	if session.KeepRunning {
		return fmt.Errorf("%s cannot modify console session %s (keepRunning): keepRunning requires an unlimited role (grain/operator)",
			a.label(), session.ID)
	}
	if why := model.ConfigRepoProposalHold(model.Task{Writes: session.Writes}); why != "" {
		return fmt.Errorf("%s cannot modify console session %s: %s, which requires an unlimited role (grain/operator)",
			a.label(), session.ID, why)
	}
	if session.TasksApproved && !a.allows("tasks:admin", "") {
		return fmt.Errorf("%s cannot modify console session %s (tasksApproved): approving proposals needs tasks:admin",
			a.label(), session.ID)
	}
	if c.networkAccessWidens(ctx, "", string(session.NetworkAccess)) {
		return fmt.Errorf("%s cannot modify console session %s (networkAccess): widening networkAccess past this deployment's default requires an unlimited role (grain/operator)",
			a.label(), session.ID)
	}
	if session.ReadsAllRepos() && !a.allows("tasks:read", "") {
		return fmt.Errorf("%s cannot modify console session %s (readsAll): reading every repository requires tasks:read without repository limits",
			a.label(), session.ID)
	}
	for _, capName := range session.Capabilities {
		if err := roleGrantRefusal(a, "capabilities", capName, normalize); err != nil {
			return err
		}
	}
	for _, defID := range model.ConsoleSessionDefaultGrants() {
		if session.DeclinesDefault(defID) {
			continue
		}
		if err := roleGrantRefusal(a, "capabilities", defID, normalize); err != nil {
			return err
		}
	}
	for _, w := range session.Writes {
		if !allowedRepo("tasks:write", w.Repo.String()) {
			return fmt.Errorf("%s cannot modify console session %s (writes): role does not hold tasks:write on %s",
				a.label(), session.ID, w.Repo)
		}
	}
	for _, rd := range session.Reads {
		if !allowedRepo("tasks:read", rd.String()) {
			return fmt.Errorf("%s cannot modify console session %s (reads): role does not hold tasks:read on %s",
				a.label(), session.ID, rd)
		}
	}
	if openID, err := c.Store.TaskIDWithTag(ctx, model.ConsoleSessionTag(session.ID)); err == nil && openID != "" {
		if openTask, err := c.Store.GetTask(ctx, openID); err == nil && openTask != nil {
			return c.roleTaskWithin(ctx, a, *openTask, "modify", allowedRepo, normalize)
		}
	}
	return nil
}

// AccessRoleInfo is a verified session's resolved access, as
// GET /api/auth/user and GET /api/config report it to the UI. It is absent
// for an unlimited caller (the access code).
type AccessRoleInfo struct {
	// Role is the caller's role, or its roles joined with ", " when
	// several entries match.
	Role       string                `json:"role"`
	Roles      []string              `json:"roles"`
	Missing    []string              `json:"missing,omitempty"`
	Full       bool                  `json:"full"`
	Scopes     []APIScope            `json:"scopes"`
	RepoLimits map[APIScope][]string `json:"repoLimits,omitempty"`
}

// accessRoleInfo reports ctx's role, or nil for an unlimited caller.
func accessRoleInfo(ctx context.Context) *AccessRoleInfo {
	a := accessRoleFromContext(ctx)
	if a == nil {
		return nil
	}
	scopes, limits := a.scopes()
	if scopes == nil {
		scopes = []APIScope{}
	}
	if len(limits) == 0 {
		limits = nil
	}
	return &AccessRoleInfo{Role: strings.Join(a.Roles, ", "), Roles: a.Roles, Missing: a.Missing,
		Full: a.Full, Scopes: scopes, RepoLimits: limits}
}

// agentCallAccessRoleInfo reports an external agent token's or task agent
// call's resolved scopes for GET /api/config so UI clients never mistake a
// scoped bearer token (such as grain/viewer) for an unlimited operator.
func agentCallAccessRoleInfo(call agentCall) *AccessRoleInfo {
	role := "agent"
	if call.external != nil {
		if ss := strings.TrimSpace(call.external.ScopeSet); ss != "" {
			role = ss
		} else {
			role = "external-token"
		}
	}
	var scopes []APIScope
	limits := map[APIScope][]string{}
	for _, info := range model.APIScopes() {
		if !call.grants.Has(info.Scope) {
			continue
		}
		scopes = append(scopes, info.Scope)
		if repos := call.grants.Repos(info.Scope); len(repos) > 0 {
			limits[info.Scope] = repos
		}
	}
	if scopes == nil {
		scopes = []APIScope{}
	}
	if len(limits) == 0 {
		limits = nil
	}
	return &AccessRoleInfo{
		Role:       role,
		Roles:      []string{role},
		Full:       false,
		Scopes:     scopes,
		RepoLimits: limits,
	}
}

// knownRepos returns the sorted repositories explicitly named across a's
// repo-limited scopes, or nil when a is nil or unlimited.
func (a *accessRole) knownRepos() []string {
	if a == nil || a.Full {
		return nil
	}
	var out []string
	for _, sc := range a.set.Scopes() {
		if a.deny[sc] {
			continue
		}
		for _, r := range a.set.Repos(sc) {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// isUnconditionalPersonOnlyRoute reports whether pattern is always refused
// to a delegated caller regardless of request body or stored task state.
func isUnconditionalPersonOnlyRoute(pattern string) bool {
	switch pattern {
	case "POST /api/access/principals",
		"PATCH /api/access/principals/{entry}",
		"DELETE /api/access/principals/{entry}",
		"POST /api/hostds",
		"POST /api/hostds/enrollment-codes",
		"POST /api/hostds/pending/{id}/accept",
		"POST /api/tasks/{id}/access-request/grant",
		"POST /api/tasks/{id}/access-request/deny":
		return true
	default:
		return false
	}
}

// roleAllowsMCPRoute reports whether a may reach rt at the scope level,
// for MCP tool discovery (grain_explore_scope on POST /api/mcp and
// tools/list on POST /mcp). Repository, body, and escalation checks still
// run per call through authorizeRoleCall.
func roleAllowsMCPRoute(a *accessRole, rt APIRoute) bool {
	if rt.Scope == "" || rt.Scope == scopeInternal {
		return false
	}
	if a == nil {
		return true
	}
	if a.Delegated {
		if a.ViaAccess == "read" && rt.Method != http.MethodGet && rt.Method != http.MethodHead {
			return false
		}
		if isUnconditionalPersonOnlyRoute(rt.Pattern) {
			return false
		}
	}
	if a.Full {
		return true
	}
	sc := roleScopeOf(rt.Pattern)
	if sc == "" {
		return false
	}
	return sc == scopeSignedIn || a.has(sc)
}

func delegatedRefusalError(code string, a *accessRole, err error) error {
	payload, _ := json.Marshal(map[string]any{
		"error":         err.Error(),
		"code":          code,
		"delegatedFrom": a.DelegatedFrom,
		"delegatingApp": a.DelegatingApp,
	})
	return routeError(http.StatusForbidden, payload)
}

// authorizeMCPRoleScope checks ctx's role against pattern's scope before an
// MCP tool validates call arguments or reads a target task, using the same
// authorizeRoleCall refusal as roleGate. Repository and body escalations run
// when the inner request is dispatched (or in authorizeMCPRoleCall when an
// expect preflight read runs first).
func (s *Server) authorizeMCPRoleScope(ctx context.Context, pattern string) error {
	a := accessRoleFromContext(ctx)
	if a == nil {
		return nil
	}
	method, _, _ := strings.Cut(pattern, " ")
	if a.Delegated {
		if a.ViaAccess == "read" && method != http.MethodGet && method != http.MethodHead {
			err := fmt.Errorf("%s holds read-only delegation (act.access=read) and cannot call %s", a.label(), pattern)
			log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
			return delegatedRefusalError("read_only_delegation", a, err)
		}
		if isUnconditionalPersonOnlyRoute(pattern) {
			if err := s.checkDelegatedPersonOnly(nil, pattern, a); err != nil {
				log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
				return delegatedRefusalError("requires_person", a, err)
			}
		}
	}
	if a.Full {
		return nil
	}
	sc := roleScopeOf(pattern)
	if sc == "" || (sc != scopeSignedIn && !a.has(sc)) {
		req, err := http.NewRequestWithContext(ctx, method, "http://localhost/", http.NoBody)
		if err != nil {
			return err
		}
		if err := s.authorizeRoleCall(req, pattern, a); err != nil {
			log.Printf("grain api: refused {human, %s} %s: %v", a.Who, pattern, err)
			return fmt.Errorf("HTTP %d: %w", agentRefusalStatus(err), err)
		}
	}
	return nil
}

// authorizeMCPRoleCall runs roleGate's full checkDelegatedPersonOnly and
// authorizeRoleCall checks against the built routeCall before an MCP tool
// performs an auxiliary preflight (such as reading a task for arguments.expect).
func (s *Server) authorizeMCPRoleCall(ctx context.Context, pattern string, rc routeCall) error {
	a := accessRoleFromContext(ctx)
	if a == nil || (a.Full && !a.Delegated) {
		return nil
	}
	method, _, _ := strings.Cut(pattern, " ")
	var reader io.Reader = http.NoBody
	if rc.body != nil {
		reader = bytes.NewReader(rc.body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+rc.path, reader)
	if err != nil {
		return err
	}
	if rc.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range rc.params {
		req.SetPathValue(k, v)
	}
	if a.Delegated {
		if a.ViaAccess == "read" && method != http.MethodGet && method != http.MethodHead {
			err := fmt.Errorf("%s holds read-only delegation (act.access=read) and cannot call %s", a.label(), pattern)
			log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
			return delegatedRefusalError("read_only_delegation", a, err)
		}
		if err := s.checkDelegatedPersonOnly(req, pattern, a); err != nil {
			log.Printf("grain api: refused {delegated, %s via %s} %s: %v", a.DelegatedFrom, a.DelegatingApp, pattern, err)
			return delegatedRefusalError("requires_person", a, err)
		}
	}
	if !a.Full {
		if err := s.authorizeRoleCall(req, pattern, a); err != nil {
			log.Printf("grain api: refused {human, %s} %s: %v", a.Who, pattern, err)
			return fmt.Errorf("HTTP %d: %w", agentRefusalStatus(err), err)
		}
	}
	return nil
}
