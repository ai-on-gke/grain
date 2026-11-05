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
// never call. An internal route with no entry here is grain's own
// plumbing and answers only a role holding every scope (operatorOnlyRoutes
// names them, so TestRoleGateCoversEveryRoute can hold the two tables
// total). GET /api/config is the sign-in page's own read, so every
// admitted caller gets it, as do the hostd routes, which authenticate in
// their own protocol.
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
	"GET /api/hostd/install.sh":                                     scopeSignedIn,
	"POST /api/tasks/{id}/config-change/approve":                    "settings:write",
	"POST /api/tasks/{id}/config-change/reject":                     "settings:write",
	"POST /api/tasks/{id}/access-request/check":                     "capabilities:read",
	"POST /api/tasks/{id}/access-request/grant":                     "capabilities:admin",
	"POST /api/tasks/{id}/access-request/deny":                      "capabilities:admin",
	"POST /api/notifications/{id}/ack":                              "notifications:write",
	"POST /api/notifications/{id}/investigate":                      "tasks:write",
	"GET /api/local-script-approvals":                               "capabilities:read",
	"POST /api/tasks/{id}/local-script-approvals/{approval}/answer": "capabilities:admin",
}

// operatorOnlyRoutes are the internal routes no limited role reaches: a
// run's own plumbing, a persistent sandbox's running service, and the MCP
// endpoints, whose tools dispatch to many routes in process without a
// per-tool role check.
var operatorOnlyRoutes = map[string]bool{
	"POST /api/tasks/{id}/config-change":                            true,
	"POST /api/tasks/{id}/activity":                                 true,
	"PUT /api/tasks/{id}/session-title":                             true,
	"POST /api/tasks/{id}/staged-attachments":                       true,
	"POST /api/tasks/{id}/db/query":                                 true,
	"POST /api/tasks/{id}/db/execute":                               true,
	"POST /api/tasks/{id}/notify":                                   true,
	"POST /api/tasks/{id}/delivered":                                true,
	"POST /api/tasks/{id}/wait-credit":                              true,
	"POST /api/tasks/{id}/change-checks":                            true,
	"POST /api/tasks/{id}/change-review":                            true,
	"POST /api/tasks/{id}/local-script-approvals":                   true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/wait":   true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire": true,
	"/staging/{name}/{path...}":                                     true,
	"POST /api/mcp":                                                 true,
	"POST /mcp":                                                     true,
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
	set     APIGrantSet
	deny    map[APIScope]bool
	// Full is true for a session holding every scope on every
	// repository with nothing denied: grain/operator, as everybody was
	// before roles.
	Full bool
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
		grants = append(grants, model.APIGrantPrefix+role)
		for sc := range roleDenies(role) {
			a.deny[sc] = true
		}
	}
	sort.Strings(a.Roles)
	a.set = model.APIGrantsOf(grants)
	a.Full = len(a.deny) == 0
	for _, info := range model.APIScopes() {
		if !a.set.Allows(info.Scope, "") {
			a.Full = false
			break
		}
	}
	return a
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

// accessRoleSlot is where singleTenantGate leaves the role of the verified
// session it admitted, for serveHTTP to put on the request's context.
type accessRoleSlot struct{ role *accessRole }

type accessRoleSlotKey struct{}

func withAccessRole(ctx context.Context, a *accessRole) context.Context {
	return context.WithValue(ctx, accessRoleKey{}, a)
}

// accessRoleFromContext is the role a verified session is held to, or nil
// for every other caller: the access code, loopback, in-process callers
// and agents (whose own gate holds them to their grants).
func accessRoleFromContext(ctx context.Context) *accessRole {
	a, _ := ctx.Value(accessRoleKey{}).(*accessRole)
	return a
}

// admitVerifiedPrincipal is checkAllowedPrincipal that also resolves the
// admitted session's role, and leaves it in the request's slot.
func (s *Server) admitVerifiedPrincipal(ctx context.Context, id VerifiedIdentity) error {
	roles, err := s.allowedPrincipalRoles(ctx, id, true)
	if err != nil {
		logRefusedPrincipal(id, err)
		return err
	}
	if slot, ok := ctx.Value(accessRoleSlotKey{}).(*accessRoleSlot); ok && slot != nil {
		slot.role = resolveAccessRoles(normalizeIdentityEmail(id.Email), roles)
	}
	return nil
}

// roleGate wraps one route: a verified session is answered only within
// its role (docs/design/access-rbac.md §5). Everybody else passes through
// to agentAPIGate.
func (s *Server) roleGate(pattern string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := accessRoleFromContext(r.Context())
		if a == nil || a.Full {
			next(w, r)
			return
		}
		if err := s.authorizeRoleCall(r, pattern, a); err != nil {
			log.Printf("grain api: refused {human, %s} %s: %v", a.Who, pattern, err)
			writeError(w, agentRefusalStatus(err), err)
			return
		}
		next(w, r)
	}
}

// roleScopeOf is the scope pattern needs of a person's role: "" for a
// route only a role holding every scope reaches.
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
	if a.allows(scope, "") || s.tasks == nil || s.tasks.Store == nil {
		return nil
	}
	body, err := peekBody(r)
	if err != nil {
		return err
	}
	var target string
	if id := r.PathValue("id"); id != "" && strings.HasPrefix(pathOf(pattern), "/api/tasks/{id}") {
		target = id
	}
	repo, err := s.agentCallRepo(r.Context(), r, pattern, target, body, nil, a.set, scope, false)
	if err != nil {
		return err
	}
	if _, fs := fsRoutes[pattern]; (fs || pattern == "GET /api/events") && repo == "" {
		// A namespace read or event stream naming no repository is narrowed
		// to the ones the role reaches by fsScope / eventsReadable, as an
		// agent's is.
		return nil
	}
	if a.allows(scope, repo) {
		return nil
	}
	if repo == "" {
		return fmt.Errorf("%s holds %s only on %s, and %s does not name one of them",
			a.label(), scope, strings.Join(a.set.Repos(scope), ", "), pattern)
	}
	return fmt.Errorf("%s holds %s, but not on %s: %s", a.label(), scope, repo, ask)
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
