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

package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The role x route authorization matrix (task 3981,
// docs/security/audits/2026-11-10-route-authorization.md).
//
// TestRouteAuthorization holds every route to "the access code, or
// nobody". This file holds every route to who else may call it: every
// built-in role, a custom scope set of one scope, an agent run acting
// for a task, an external-agent gma_ credential with narrow scopes, and
// nobody at all. What each is owed is not written out route by route;
// it is computed from the tables the gates themselves read --
// route_scopes.go (ui.AgentAPIRoutes), humanRouteScopes and
// operatorOnlyRoutes (ui.RoleScopeOf) -- so a route added to pkg/ui is
// swept here the moment it is registered, and a route missing from
// those tables fails TestRouteScopesAreTotal and
// TestRoleGateCoversEveryRoute in pkg/ui as well as routeAuthorization
// here.

// matrixCaller is one row of the matrix.
type matrixCaller struct {
	name string
	// role, for a verified person, is the scope set their allowlist
	// entry names.
	role string
	// agent is an agent run acting for matrixAgentTask over the sandbox
	// proxy's channel (X-Grain-Acting-Task, X-Grain-Channel:
	// sandbox-cli), which is what gitproxy.forwardToAPI turns a run's
	// per-attempt proxy token into.
	agent bool
	// external presents the matrix's gma_ credential.
	external bool
	// anonymous presents nothing, from the open internet.
	anonymous bool
}

func (c matrixCaller) email() string {
	return strings.NewReplacer("/", "-", ":", "-").Replace(c.role) + "@example.com"
}

const (
	// matrixAgentTask is the task an agent caller acts for. It is not the
	// fixture's task ({id} routes name that one), so a probe acts on
	// another task as an agent's calls usually do.
	matrixAgentTask = "900"
	// matrixOtherTask is a task in matrixOtherRepo, which no repository-
	// limited caller of TestHorizontalObjectAccess reaches.
	matrixOtherTask = "901"
	matrixOtherRepo = "other/gadgets"
	// matrixOneRead and matrixOneWrite are custom scope sets of one scope.
	matrixOneRead  = "matrix-one-read"
	matrixOneWrite = "matrix-one-write"
	// matrixWidgetsOnly is a set limited to acme/widgets.
	matrixWidgetsOnly = "matrix-widgets-only"
)

// matrixAgentGrants are the agent caller's task grants, and
// matrixExternalScopes the gma_ credential's: narrow, one read and one
// write, so both sides of a decision are exercised.
var (
	matrixAgentGrants    = []string{"tasks:read", "tasks:write"}
	matrixExternalScopes = []string{"tasks:read", "ci:read"}
)

func matrixScopeSets() []model.APIScopeSet {
	return []model.APIScopeSet{
		{Name: matrixOneRead, Scopes: []string{"tasks:read"}, Version: 1},
		{Name: matrixOneWrite, Scopes: []string{"tasks:write"}, Version: 1},
		{Name: matrixWidgetsOnly, Scopes: []string{"tasks:read", "tasks:write", "ci:read", "sessions:read"},
			Repos: []string{"acme/widgets"}, Version: 1},
	}
}

// matrixCallers is every row: each built-in scope set as a role (the
// access roles and every preset), the custom one-scope sets, an agent
// run, a gma_ credential and nobody.
func matrixCallers() []matrixCaller {
	var out []matrixCaller
	for _, s := range model.BuiltinAPIScopeSets() {
		out = append(out, matrixCaller{name: "role " + s.Name, role: s.Name})
	}
	for _, s := range matrixScopeSets() {
		out = append(out, matrixCaller{name: "role " + s.Name, role: s.Name})
	}
	return append(out,
		matrixCaller{name: "agent run", agent: true},
		matrixCaller{name: "gma_ credential", external: true},
		matrixCaller{name: "no identity", anonymous: true},
	)
}

// matrixSeed is the matrix fixture template's routeFixtureSeed: an
// allowlist entry per role, the agent's task, the gma_ credential, and
// a task in another repository.
func matrixSeed(t *testing.T, f *routeFixture) (map[string]string, error) {
	ctx := context.Background()
	cfg, err := f.store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("reading config: %v", err)
	}
	cfg.ExternalAgentAPI = true
	if err := f.store.PutConfig(ctx, *cfg); err != nil {
		return nil, err
	}
	now := time.Now()
	for _, c := range matrixCallers() {
		if c.role == "" {
			continue
		}
		if _, _, err := f.store.AddAllowedPrincipalWithRole(ctx, "user:"+c.email(), "test", "route matrix", c.role, now); err != nil {
			return nil, fmt.Errorf("allowing %s: %w", c.email(), err)
		}
	}
	var grants []model.Grant
	for _, sc := range matrixAgentGrants {
		grants = append(grants, model.Grant{Capability: model.APIRef(sc), Via: model.GrantByLabel})
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "other", Name: "gadgets"}
	for _, task := range []model.Task{
		{ID: matrixAgentTask, Title: "matrix agent", Target: &widgets, Grants: grants},
		{ID: matrixOtherTask, Title: "matrix other repo", Target: &other},
	} {
		task.Intent = model.IntentImplement
		task.Binding = model.BindingDirective
		task.Origin = model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
			Reason:      model.ReasonDirect,
		}
		if err := f.store.PutTask(ctx, task); err != nil {
			return nil, fmt.Errorf("putting task %s: %w", task.ID, err)
		}
	}
	body, _ := json.Marshal(map[string]any{"name": "matrix", "scopes": matrixExternalScopes})
	rec := f.serve(t, http.MethodPost, "/api/external-agents/tokens", string(body), http.Header{})
	var issued ui.IssuedExternalAgentToken
	if rec.code != http.StatusOK && rec.code != http.StatusCreated {
		return nil, fmt.Errorf("issuing the gma_ credential: %d %s", rec.code, rec.excerpt())
	}
	if err := json.Unmarshal(rec.body.Bytes(), &issued); err != nil || !strings.HasPrefix(issued.Token, "gma_") {
		return nil, fmt.Errorf("issuing the gma_ credential: %v %s", err, rec.excerpt())
	}
	return map[string]string{"gma": issued.Token, "gmaID": issued.ID}, nil
}

// newMatrixFixture is a single-tenant daemon behind an identity proxy,
// seeded with every matrix caller. It holds no access session: once a
// verifier is configured and the allowlist names somebody, the code's
// browser sign-in asks who is signing in (accesscode.go), so the trusted
// caller presents the code itself (trustedHeader) instead.
func newMatrixFixture(t *testing.T) *routeFixture {
	t.Helper()
	f := newRouteFixtureFrom(t, prepareSeededRouteFixtureTemplate(t, true, true, matrixSeed), false, true)
	f.from = loopbackAddr
	return f
}

// trustedHeader is the access code as the CLI and a run's plumbing
// present it.
func trustedHeader() http.Header {
	h := http.Header{}
	h.Set(ui.AccessCodeHeader, testAccessCode)
	return h
}

// as sends one request as c. The trusted access-code session is never
// part of it: a verified person, a gma_ credential and nobody come from
// the open internet without it, and an agent run carries it only beside
// the acting-task headers, as the sandbox proxy's forwarded call does.
func (f *routeFixture) as(t *testing.T, c matrixCaller, method, target, body string) *statusRecorder {
	t.Helper()
	header := http.Header{}
	u := *f
	u.from, u.accessCookie = publicAddr, ""
	switch {
	case c.role != "":
		header = f.proxy.header(t, c.email())
	case c.agent:
		u.from = loopbackAddr
		header = trustedHeader()
		header.Set(ui.ActingTaskHeader, matrixAgentTask)
		header.Set(ui.ChannelHeader, ui.SandboxCLIChannel)
		header.Set(ui.ActingReasonHeader, "route matrix")
	case c.external:
		header.Set("Authorization", "Bearer "+f.seeded["gma"])
		header.Set(ui.ActingReasonHeader, "route matrix")
	}
	return u.serve(t, method, target, body, header)
}

// matrixVerdict is what a caller is owed on a route.
type matrixVerdict int

const (
	// unasserted: the matrix makes no claim, because something other
	// than the caller's scopes decides (see matrixExpect).
	unasserted matrixVerdict = iota
	allowed
	refusedVerdict
)

// agentRouteScopes is route_scopes.go as the agent gate reads it: every
// agent-callable route and its scope. A route absent from it is
// "internal" and no agent or gma_ credential may call it.
var agentRouteScopes = sync.OnceValue(func() map[string]model.APIScope {
	out := map[string]model.APIScope{}
	for _, r := range ui.AgentAPIRoutes() {
		out[r.Pattern] = r.Scope
	}
	return out
})

// roleSet is what role grants, resolved as resolveAccessRoles does.
func roleSet(role string) (model.APIGrantSet, bool) {
	set := model.APIGrantsOf([]string{model.APIRef(role)})
	full := true
	for _, info := range model.APIScopes() {
		if !set.Allows(info.Scope, "") {
			full = false
			break
		}
	}
	return set, full
}

// matrixExpect is what c is owed on pattern, worked out from the tables
// the gates read, never from what the handler does.
func matrixExpect(c matrixCaller, pattern string) matrixVerdict {
	if routeAuthorization[pattern] == public {
		// Anybody may call it, so nobody can be refused it for who they
		// are; the hostd routes among them authenticate in their own
		// protocol.
		return unasserted
	}
	switch {
	case c.anonymous:
		return refusedVerdict
	case c.role != "":
		set, full := roleSet(c.role)
		if full {
			return allowed
		}
		switch scope := ui.RoleScopeOf(pattern); scope {
		case "signed-in":
			return allowed
		case "":
			return refusedVerdict
		default:
			if set.Has(scope) {
				return allowed
			}
			return refusedVerdict
		}
	case c.agent:
		if pattern == "POST /api/mcp" {
			// Held to the task's grants per tool call (mcp_tools_test.go).
			return allowed
		}
		scope, ok := agentRouteScopes()[pattern]
		if !ok || ui.IsSandboxDeniedScope(scope) {
			return refusedVerdict
		}
		if model.SandboxAPIGrantsOf(apiRefs(matrixAgentGrants)).Has(scope) {
			return allowed
		}
		return refusedVerdict
	case c.external:
		switch pattern {
		case "GET /api/auth/whoami", "POST /api/mcp", "POST /mcp",
			// The probe's {id} is the credential's own (matrixSeed issues
			// the fixture's first), and a credential may always revoke
			// itself (ownCredentialRoute); TestHorizontalObjectAccess
			// holds it off every other credential.
			"POST /api/external-agents/tokens/{id}/revoke":
			// Its own identity, and the MCP endpoints, which hold each
			// tool call to its scopes.
			return allowed
		}
		scope, ok := agentRouteScopes()[pattern]
		if !ok {
			return refusedVerdict
		}
		if slices.Contains(matrixExternalScopes, string(scope)) {
			return allowed
		}
		return refusedVerdict
	}
	return unasserted
}

func apiRefs(scopes []string) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = model.APIRef(s)
	}
	return out
}

// matrixNotDecidedByScope are routes on which a rail beyond the scope
// refuses some callers the scope tables allow, so the "allowed" half of
// the matrix is not asserted on them (the "refused" half still is). Each
// says what decides instead; every one is a refusal of more, never an
// answer to less.
var matrixNotDecidedByScope = map[string]string{
	"POST /mcp": "the external-agent endpoint answers only a gma_ bearer token; a person uses POST /api/mcp",
	"POST /api/tasks/{id}/submit": "submitting turns on auto-merge, which only an unlimited role or the code may do " +
		"(roleGrantEscalation; agents: agentTaskPatchBodyAllowed)",
	"POST /api/tasks/{id}/run-now":                                  "running an unapproved task approves it, which needs tasks:admin (roleApprovalWithin)",
	"POST /api/notifications/{id}/investigate":                      "files a task whose grants the role must be able to give (filingEscalation)",
	"POST /api/tasks/{id}/notify":                                   "only the task's own run raises notifications as it",
	"POST /api/tasks/{id}/local-script-approvals":                   "requested by the task's own run, never by a person",
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire": "requested by the task's own run, never by a person",
	"GET /api/tasks/{id}/messages":                                  "an agent reads only its own task's conversation (agentRails, ownMessagesReadRoute)",
	"POST /api/tasks/{id}/human-review":                             "a run asks for human review only as its own task",
	"POST /api/tasks/{id}/human-review/release":                     "only a person releases a human-review hold",
	"POST /api/tasks/{id}/change-mail":                              "an agent never mails, publishes or syncs a change",
	"POST /api/tasks/{id}/change-publish":                           "an agent never mails, publishes or syncs a change",
	"POST /api/tasks/{id}/change-sync":                              "an agent never mails, publishes or syncs a change",
	"POST /api/tasks/{id}/messages":                                 "a model-only control action, refused on the sandbox CLI channel",
	"POST /api/tasks/{id}/park-until":                               "a model-only control action, refused on the sandbox CLI channel",
	"POST /api/tasks/{id}/resume-later":                             "a model-only control action, refused on the sandbox CLI channel",
}

// TestRouteAuthorizationMatrix asks every route pkg/ui registers as every
// matrixCaller and holds the answer to matrixExpect:
//
//   - a caller the tables refuse must be answered 401 or 403;
//   - a caller the tables allow must not be, unless the trusted access-
//     code caller is refused the same probe (the fixture's objects, not
//     the caller, decided -- refusesTheTaskNotTheCaller);
//   - nobody but the trusted caller and a full role is ever answered a
//     body holding the access code (ui.Config.LocalAPIToken) or the
//     gma_ credential itself.
//
// A GET is asked of one shared fixture; anything else of a fresh one per
// caller the tables allow, so a write one caller was allowed cannot
// change what the next is answered.
func TestRouteAuthorizationMatrix(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	if err := model.InstallAPIScopeSets(matrixScopeSets()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })

	shared := newMatrixFixture(t)
	routes := shared.server.Routes()
	for _, pattern := range routes {
		if _, ok := routeAuthorization[pattern]; !ok {
			t.Errorf("route %q is not in routeAuthorization (routes_test.go): every route needs a declared level "+
				"before the matrix can hold it to its scope", pattern)
		}
	}
	callers := matrixCallers()

	var mu sync.Mutex
	var failures []string
	asserted := map[matrixVerdict]int{}
	fail := func(format string, args ...any) {
		mu.Lock()
		failures = append(failures, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	t.Run("routes", func(t *testing.T) {
		for _, pattern := range routes {
			t.Run(pattern, func(t *testing.T) {
				t.Parallel()
				method, _, ok := strings.Cut(pattern, " ")
				if !ok {
					method = http.MethodGet
				}
				body := ""
				if method != http.MethodGet {
					body = "{}"
					if b, ok := probeBodies[pattern]; ok {
						body = b
					}
				}
				get := method == http.MethodGet
				fresh := func() *routeFixture {
					if get {
						return shared
					}
					return newMatrixFixture(t)
				}
				target := shared.path(pattern)
				trusted := fresh().serve(t, method, target, body, trustedHeader())
				refusedSink := fresh()
				for _, c := range callers {
					want := matrixExpect(c, pattern)
					f := refusedSink
					switch {
					case c.external:
						// A gma_ credential is rate limited per daemon, and
						// the shared fixture would spend its budget in a
						// few hundred probes.
						f = newMatrixFixture(t)
					case want != refusedVerdict:
						f = fresh()
					}
					rec := f.as(t, c, method, target, body)
					mu.Lock()
					asserted[want]++
					mu.Unlock()
					switch want {
					case refusedVerdict:
						if !refused(rec.code) {
							fail("%s as %s: answered %d %s, want 401 or 403", pattern, c.name, rec.code, rec.excerpt())
						}
					case allowed:
						if c.role == matrixWidgetsOnly {
							// A repository-limited role is refused a route
							// whose repository cannot be told, by design
							// (authorizeRoleCall); it is held to its
							// repository in TestHorizontalObjectAccess.
							break
						}
						if refused(rec.code) && !refused(trusted.code) && matrixNotDecidedByScope[pattern] == "" {
							fail("%s as %s: refused %d %s, but its scopes allow it (the access code is answered %d)",
								pattern, c.name, rec.code, rec.excerpt(), trusted.code)
						}
					}
					if c.role != "" {
						if _, full := roleSet(c.role); full {
							continue
						}
					}
					for what, secret := range map[string]string{"the access code": testAccessCode, "the gma_ credential": f.seeded["gma"]} {
						if secret != "" && strings.Contains(rec.body.String(), secret) {
							fail("%s as %s: answered a body holding %s", pattern, c.name, what)
						}
					}
				}
			})
		}
	})
	sort.Strings(failures)
	for _, f := range failures {
		t.Error(f)
	}
	// A sweep that asserted nothing passes too: hold it to having asked
	// most of the matrix (routes x callers, less the public routes).
	if min := len(routes) * len(callers) / 2; asserted[allowed] == 0 || asserted[allowed]+asserted[refusedVerdict] < min {
		t.Errorf("asserted %d allowed and %d refused probes over %d routes and %d callers, want at least %d",
			asserted[allowed], asserted[refusedVerdict], len(routes), len(callers), min)
	}
	t.Logf("%d routes x %d callers: %d allowed, %d refused, %d unasserted", len(routes), len(callers),
		asserted[allowed], asserted[refusedVerdict], asserted[unasserted])
}
