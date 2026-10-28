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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// authLevel is who a route is meant to answer. grain is single-tenant
// (docs/single-tenant-reset.md, stage 3): it keeps no accounts and no
// team roles, so a route either answers anybody or answers only a
// caller the daemon trusts as itself.
type authLevel int

const (
	// public answers anybody: the frontend, the access-code sign-in,
	// and the executor bootstrap that authenticates in its own protocol.
	public authLevel = iota
	// trusted answers only a caller the daemon trusts as itself -- one
	// presenting its access code (a bearer token, or the access session
	// the code signs in to: the CLI's token and the browser's sign-in),
	// or an in-process request -- and refuses everybody else, whoever
	// they claim to be.
	trusted
)

func (l authLevel) String() string {
	return [...]string{"public", "trusted"}[l]
}

// routeAuthorization is every route pkg/ui's Server serves and who it is
// meant to answer (#2425 Layer 1b). TestRouteAuthorization fails on a
// route that is missing here, so a new route arrives with a decision
// about who may call it rather than inheriting whatever its handler
// happens to check. Where the tree falls short of a line here today, the
// gap is counted in .grain/baselines/route-authorization.txt rather than
// the level being lowered to match: this table says what is meant, the
// baseline what is still owed.
//
// Every route that is not public is trusted: a single-tenant daemon has
// one caller, itself, so there is no member, team admin or system admin
// left for a route to tell apart (the team-admin and system-admin levels
// went in slice 3e-iii-c of docs/single-tenant-reset.md, and the admin
// list they read, admin_accounts, with task 4880). A handler may still
// tell a person from an agent; owner_test.go holds those.
var routeAuthorization = map[string]authLevel{
	// The frontend, and the /api/ 404s apiFallback answers in front of it.
	"/": public,

	// What the login page reads, and signing out.
	"GET /api/config":       public,
	"POST /api/auth/logout": public,
	// A single-tenant daemon's access-code sign-in (pkg/ui/accesscode.go):
	// what a caller without the code is shown, so public by design;
	// TestASingleTenantDaemon* hold what it lets in.
	"GET /api/auth/access-code":          public,
	"POST /api/auth/access-code":         public,
	"GET /api/auth/access-code/login.js": public,
	"GET /api/auth/logout":               public,
	"GET /api/auth/user":                 public,
	"GET /api/auth/status":               public,

	// The long poll that tells a signed-in page the daemon is going down
	// for a restart before it stops answering (grain/task-3543).
	"GET /api/restart-watch": trusted,

	"GET /api/settings":                         trusted,
	"PUT /api/settings":                         trusted,
	"POST /api/settings":                        trusted,
	"POST /api/settings/plan":                   trusted,
	"GET /api/settings/cli-run":                 trusted,
	"PUT /api/settings/cli-run":                 trusted,
	"POST /api/capabilities/{id}/check":         trusted,
	"POST /api/settings/db/migrate-to-postgres": trusted,

	// Tasks.
	"GET /api/tasks":                                                trusted,
	"GET /api/tasks/states":                                         trusted,
	"GET /api/states":                                               trusted,
	"POST /api/tasks":                                               trusted,
	"POST /api/tasks/from-template":                                 trusted,
	"POST /api/tasks/reorder":                                       trusted,
	"GET /api/tasks/{id}":                                           trusted,
	"PATCH /api/tasks/{id}":                                         trusted,
	"POST /api/tasks/{id}/capabilities":                             trusted,
	"POST /api/tasks/{id}/depends-on":                               trusted,
	"POST /api/tasks/{id}/approve":                                  trusted,
	"POST /api/tasks/{id}/withdraw-approval":                        trusted,
	"POST /api/tasks/{id}/defer":                                    trusted,
	"POST /api/tasks/{id}/undefer":                                  trusted,
	"POST /api/tasks/{id}/submit":                                   trusted,
	"POST /api/tasks/{id}/comments":                                 trusted,
	"PUT /api/tasks/{id}/secret":                                    trusted,
	"GET /api/tasks/{id}/attachments/{attachmentId}":                trusted,
	"POST /api/tasks/{id}/close":                                    trusted,
	"POST /api/tasks/{id}/stop":                                     trusted,
	"POST /api/tasks/{id}/pause":                                    trusted,
	"POST /api/tasks/{id}/resume":                                   trusted,
	"PUT /api/tasks/{id}/goal":                                      trusted,
	"DELETE /api/tasks/{id}/goal":                                   trusted,
	"POST /api/tasks/{id}/reopen":                                   trusted,
	"POST /api/tasks/{id}/retry":                                    trusted,
	"POST /api/tasks/{id}/retry-merge":                              trusted,
	"POST /api/tasks/{id}/force-merge":                              trusted,
	"POST /api/tasks/{id}/run-now":                                  trusted,
	"POST /api/tasks/{id}/pull-request":                             trusted,
	"POST /api/tasks/{id}/sandbox/recreate":                         trusted,
	"POST /api/tasks/{id}/activity":                                 trusted,
	"PUT /api/tasks/{id}/session-title":                             trusted,
	"POST /api/tasks/{id}/staged-attachments":                       trusted,
	"POST /api/tasks/{id}/db/query":                                 trusted,
	"POST /api/tasks/{id}/db/execute":                               trusted,
	"POST /api/tasks/{id}/messages":                                 trusted,
	"GET /api/tasks/{id}/messages":                                  trusted,
	"POST /api/tasks/{id}/agent-messages":                           trusted,
	"POST /api/tasks/{id}/notes":                                    trusted,
	"POST /api/tasks/{id}/notify":                                   trusted,
	"POST /api/notifications":                                       trusted,
	"GET /api/notifications":                                        trusted,
	"POST /api/notifications/{id}/ack":                              trusted,
	"POST /api/notifications/{id}/clear":                            trusted,
	"POST /api/notifications/{id}/investigate":                      trusted,
	"POST /api/tasks/{id}/wait-credit":                              trusted,
	"POST /api/tasks/{id}/wait-on":                                  trusted,
	"POST /api/tasks/{id}/park-until":                               trusted,
	"POST /api/tasks/{id}/resume-later":                             trusted,
	"POST /api/tasks/{id}/local-script-approvals":                   trusted,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/wait":   trusted,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire": trusted,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/answer": trusted,
	"GET /api/local-script-approvals":                               trusted,
	"POST /api/tasks/{id}/delivered":                                trusted,
	"GET /api/tasks/{id}/attempts/{number}/transcript":              trusted,
	"GET /api/tasks/{id}/attempts/{number}/capability-audit":        trusted,
	"GET /api/tasks/{id}/prompt":                                    trusted,
	"GET /api/tasks/{id}/tree":                                      trusted,

	"GET /api/memory":                           trusted,
	"PUT /api/memory":                           trusted,
	"GET /api/memory/documents":                 trusted,
	"PUT /api/memory/documents":                 trusted,
	"GET /api/memory/documents/{name}":          trusted,
	"GET /api/release-notes":                    trusted,
	"PUT /api/release-notes":                    trusted,
	"GET /api/release-notes/stats":              trusted,
	"GET /api/release-notes/{period}":           trusted,
	"GET /api/release-notes/versions":           trusted,
	"GET /api/release-notes/versions/{version}": trusted,

	// Repos: their configuration, releases and branches.
	"POST /api/repos":                                                      trusted,
	"DELETE /api/repos/{owner}/{name}":                                     trusted,
	"GET /api/repos/{owner}/{name}/ci-capabilities":                        trusted,
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                        trusted,
	"GET /api/repos/{owner}/{name}/release":                                trusted,
	"PUT /api/repos/{owner}/{name}/release":                                trusted,
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":               trusted,
	"DELETE /api/repos/{owner}/{name}/release-secrets/{secret}":            trusted,
	"POST /api/repos/{owner}/{name}/release-runs":                          trusted,
	"POST /api/repos/{owner}/{name}/base-branch-runs":                      trusted,
	"GET /api/repos/{owner}/{name}/work-ownership":                         trusted,
	"PUT /api/repos/{owner}/{name}/work-ownership":                         trusted,
	"GET /api/repos/{owner}/{name}/capabilities":                           trusted,
	"PUT /api/repos/{owner}/{name}/capabilities":                           trusted,
	"GET /api/repos/{owner}/{name}/prompt-extension":                       trusted,
	"PUT /api/repos/{owner}/{name}/prompt-extension":                       trusted,
	"GET /api/repos/{owner}/{name}/setup-command":                          trusted,
	"PUT /api/repos/{owner}/{name}/setup-command":                          trusted,
	"GET /api/repos/{owner}/{name}/declarations":                           trusted,
	"PUT /api/repos/{owner}/{name}/declarations":                           trusted,
	"GET /api/repos/{owner}/{name}/require-branch-up-to-date":              trusted,
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":              trusted,
	"GET /api/repos/{owner}/{name}/ignore-github-ci":                       trusted,
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                       trusted,
	"GET /api/repos/{owner}/{name}/ci-mode":                                trusted,
	"PUT /api/repos/{owner}/{name}/ci-mode":                                trusted,
	"GET /api/repos/{owner}/{name}/settings":                               trusted,
	"PUT /api/repos/{owner}/{name}/settings":                               trusted,
	"GET /api/repos/{owner}/{name}/watch-default-branch":                   trusted,
	"PUT /api/repos/{owner}/{name}/watch-default-branch":                   trusted,
	"GET /api/repos/{owner}/{name}/merge-on-green":                         trusted,
	"PUT /api/repos/{owner}/{name}/merge-on-green":                         trusted,
	"GET /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":       trusted,
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":       trusted,
	"GET /api/repos/{owner}/{name}/unfixable-checks":                       trusted,
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                       trusted,
	"GET /api/repos/{owner}/{name}/workflows":                              trusted,
	"PUT /api/repos/{owner}/{name}/workflows":                              trusted,
	"GET /api/repos/{owner}/{name}/lifecycle":                              trusted,
	"POST /api/repos/{owner}/{name}/lifecycle/refresh":                     trusted,
	"GET /api/repos/{owner}/{name}/releases":                               trusted,
	"POST /api/repos/{owner}/{name}/releases":                              trusted,
	"GET /api/repos/{owner}/{name}/release-creation":                       trusted,
	"GET /api/repos/{owner}/{name}/releases/{release}":                     trusted,
	"DELETE /api/repos/{owner}/{name}/releases/{release}":                  trusted,
	"POST /api/repos/{owner}/{name}/releases/{release}/merge":              trusted,
	"GET /api/repos/{owner}/{name}/releases/{release}/candidates":          trusted,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates":         trusted,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote": trusted,
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve":    trusted,
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":     trusted,
	"GET /api/repos/{owner}/{name}/branches":                               trusted,
	"POST /api/repos/{owner}/{name}/branches":                              trusted,
	"GET /api/repos/{owner}/{name}/github-branches":                        trusted,
	"GET /api/repos/{owner}/{name}/compare":                                trusted,
	"GET /api/repos/{owner}/{name}/qualification-plan":                     trusted,
	"PUT /api/repos/{owner}/{name}/qualification-plan":                     trusted,
	"GET /api/repos/{owner}/{name}/candidates/{id}/qualification":          trusted,
	"POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve": trusted,

	// Standing work: schedules, templates, suites and boards.
	"GET /api/schedules":                  trusted,
	"POST /api/schedules":                 trusted,
	"PATCH /api/schedules/{id}":           trusted,
	"POST /api/schedules/{id}/run":        trusted,
	"GET /api/schedules/{id}/composition": trusted,
	"POST /api/schedules/composition":     trusted,
	"DELETE /api/schedules/{id}":          trusted,
	"GET /api/templates":                  trusted,
	"POST /api/templates":                 trusted,
	"GET /api/templates/{id}/composition": trusted,
	"PATCH /api/templates/{id}":           trusted,
	"DELETE /api/templates/{id}":          trusted,
	"GET /api/suites":                     trusted,
	"POST /api/suites":                    trusted,
	"PATCH /api/suites/{id}":              trusted,
	"GET /api/suites/{id}/composition":    trusted,
	"POST /api/suites/composition":        trusted,
	"DELETE /api/suites/{id}":             trusted,
	"GET /api/suite-runs":                 trusted,
	"POST /api/suite-runs":                trusted,
	"GET /api/suite-runs/{id}":            trusted,
	"GET /api/boards":                     trusted,
	"POST /api/boards":                    trusted,
	"PATCH /api/boards/{id}":              trusted,
	"DELETE /api/boards/{id}":             trusted,

	"GET /api/git-ambient-sources": trusted,

	// The deployment's own connections and configuration (stage 3e-iv of
	// the single-tenant reset).
	"GET /api/deployment":                                     trusted,
	"PUT /api/deployment":                                     trusted,
	"GET /api/deployment/reachable-repos":                     trusted,
	"GET /api/deployment/git-connections":                     trusted,
	"POST /api/deployment/git-connections":                    trusted,
	"DELETE /api/deployment/git-connections/{cred}":           trusted,
	"POST /api/deployment/git-connections/check":              trusted,
	"PUT /api/deployment/agent-backends/{backendId}":          trusted,
	"DELETE /api/deployment/agent-backends/{backendId}":       trusted,
	"GET /api/deployment/repos/{owner}/{name}/config":         trusted,
	"PUT /api/deployment/repos/{owner}/{name}/config":         trusted,
	"PUT /api/deployment/repos/{owner}/{name}/git-connection": trusted,
	// A broadcast instructs every active task.
	"POST /api/deployment/broadcast":   trusted,
	"GET /api/deployment/secret-audit": trusted,
	"GET /api/deployment/audit-logs":   trusted,

	"GET /api/agent-models": trusted,

	// Deployment-wide credentials: executor tokens, model keys, GitHub
	// tokens, secrets.
	"GET /api/executors/tokens":                trusted,
	"POST /api/executors/tokens":               trusted,
	"POST /api/executors/tokens/{id}/revoke":   trusted,
	"DELETE /api/executors/tokens/{id}":        trusted,
	"DELETE /api/executors/tokens/{id}/revoke": trusted,
	"GET /api/agent-keys":                      trusted,
	"PUT /api/agent-keys/{framework}":          trusted,
	"DELETE /api/agent-keys/{framework}":       trusted,
	"POST /api/agent-keys/{framework}/check":   trusted,
	// A connection's key (grain/task-1601).
	"PUT /api/agent-backends/{id}/credential":    trusted,
	"DELETE /api/agent-backends/{id}/credential": trusted,
	"GET /api/agent-backends/{id}/codex-auth":    trusted,
	"PUT /api/agent-backends/{id}/codex-auth":    trusted,
	"DELETE /api/agent-backends/{id}/codex-auth": trusted,
	"PUT /api/codex-auth":                        trusted,
	"DELETE /api/codex-auth":                     trusted,
	// The deployment's named GCP keys (single-tenant reset stage 4e).
	// Listing names projects, accounts and minter secrets, never key
	// material, and is what a person granting gcp-key:<name> reads.
	// Capability servers: the deployment's own configuration, and a
	// bind that writes into the secrets store.
	"GET /api/capability-servers":                 trusted,
	"GET /api/capability-methods":                 trusted,
	"POST /api/capability-grants/check":           trusted,
	"PUT /api/capability-servers":                 trusted,
	"POST /api/capability-servers/remove":         trusted,
	"POST /api/capability-servers/enable":         trusted,
	"POST /api/capability-servers/dismiss-notice": trusted,
	"POST /api/capability-servers/rotate-managed": trusted,
	"POST /api/capability-servers/disable":        trusted,
	"POST /api/capability-servers/bind-secret":    trusted,
	"POST /api/capability-servers/test":           trusted,
	"POST /api/capability-servers/test-script":    trusted,
	"POST /api/capability-servers/gchat-test":     trusted,
	"GET /api/capability-servers/gchat-usage":     trusted,
	"GET /api/capability-servers/host-entries":    trusted,
	"GET /api/capability-servers/usage":           trusted,
	"GET /api/capability-policy":                  trusted,
	"PUT /api/capability-policy":                  trusted,
	"POST /api/capability-policy/preview":         trusted,
	"GET /api/api-scope-sets":                     trusted,
	"PUT /api/api-scope-sets":                     trusted,
	"POST /api/api-scope-sets/delete":             trusted,
	"GET /api/gcp-keys":                           trusted,
	"POST /api/gcp-keys":                          trusted,
	"GET /api/gcp-keys/{name}":                    trusted,
	"PUT /api/gcp-keys/{name}":                    trusted,
	"DELETE /api/gcp-keys/{name}":                 trusted,
	"GET /api/github-tokens":                      trusted,
	"PUT /api/github-tokens/{name}":               trusted,
	"DELETE /api/github-tokens/{name}":            trusted,
	"GET /api/secrets":                            trusted,
	"PUT /api/secrets/{secret}/{key}":             trusted,
	"DELETE /api/secrets/{secret}/{key}":          trusted,
	"DELETE /api/secrets/{secret}":                trusted,

	// The deployment's own state, host, daemon and database.
	"GET /api/state-repo":               trusted,
	"GET /api/state-repo/files":         trusted,
	"GET /api/state-repo/file":          trusted,
	"POST /api/state-repo/sync":         trusted,
	"POST /api/state-repo/remote":       trusted,
	"POST /api/state-repo/secrets-key":  trusted,
	"POST /api/state-repo/import-key":   trusted,
	"POST /api/state-repo/secrets-repo": trusted,
	"POST /api/state-repo/reset-notice": trusted,
	"POST /api/host/reboot":             trusted,
	"POST /api/host/shutdown":           trusted,
	"POST /api/host/restart":            trusted,
	"GET /api/host/top":                 trusted,
	"GET /api/upgrade":                  trusted,
	"POST /api/upgrade":                 trusted,
	"DELETE /api/upgrade/scheduled":     trusted,
	"GET /api/upgrade/targets":          trusted,
	"GET /api/upgrade/release-notes":    trusted,
	"GET /api/database":                 trusted,
	"POST /api/database/test":           trusted,
	"POST /api/database/upgrade":        trusted,
	"POST /api/upgrade/postgres":        trusted,
	"GET /api/logs":                     trusted,
	"GET /api/logs/{source}":            trusted,

	"GET /api/sandboxes":                             trusted,
	"GET /api/persistent-sandboxes":                  trusted,
	"GET /api/persistent-sandboxes/{name}/logs":      trusted,
	"POST /api/persistent-sandboxes/{name}/{action}": trusted,
	"/staging/{name}/{path...}":                      trusted,
	"GET /api/metrics":                               trusted,
	"GET /api/unmatched-failures":                    trusted,
	"GET /api/pause":                                 trusted,
	"DELETE /api/pause":                              trusted,

	"GET /api/console":                        trusted,
	"GET /api/console/{id}/output":            trusted,
	"POST /api/console/{id}/input":            trusted,
	"POST /api/console/{id}/resize":           trusted,
	"DELETE /api/console/{id}":                trusted,
	"GET /api/console-sessions":               trusted,
	"POST /api/console-sessions":              trusted,
	"PUT /api/console-sessions/{id}":          trusted,
	"DELETE /api/console-sessions/{id}":       trusted,
	"POST /api/console-sessions/{id}/restart": trusted,
	"POST /api/console-sessions/{id}/cancel":  trusted,

	"POST /api/ci/runs":                            trusted,
	"GET /api/ci/workflows":                        trusted,
	"GET /api/ci/runs":                             trusted,
	"GET /api/ci/faults":                           trusted,
	"GET /api/ci/runs/{id}":                        trusted,
	"DELETE /api/ci/runs/{id}":                     trusted,
	"GET /api/ci/runs/{id}/artifacts":              trusted,
	"GET /api/ci/runs/{id}/artifacts/{job}/{name}": trusted,
	"GET /api/ci/cache":                            trusted,
	"DELETE /api/ci/cache":                         trusted,
	"GET /api/merge-batches":                       trusted,
	"GET /api/pipeline/changes":                    trusted,
	"GET /api/base-branch-gates":                   trusted,
	"GET /api/package-cache":                       trusted,
	"POST /api/package-cache/flush":                trusted,
	"GET /api/actions-cache":                       trusted,
	"POST /api/actions-cache/flush":                trusted,
	"GET /api/pipeline-pause":                      trusted,
	"POST /api/pipeline-pause":                     trusted,
	"DELETE /api/pipeline-pause":                   trusted,

	// Remote executors: the list is anybody's; the bootstrap script and
	// binaries are fetched by a machine with nothing but curl, and the
	// connection authenticates with an executor token in its own
	// handshake, after the upgrade -- not with a principal.
	"GET /api/executors":                  trusted,
	"GET /api/model-servers/install.sh":   public,
	"GET /api/executors/install.sh":       public,
	"GET /install-executor.sh":            public,
	"GET /api/executors/bin/{binary}": public,
	"GET /install/{os}/{arch}":        public,
	"GET /_grain/executor":            public,
	"GET /api/executors/connect":      public,
	"GET /api/executors/stream":       public,
	// A grain hostd dialing in authenticates with the hostd token in
	// its upgrade (hostd.Remote.ServeHTTP), not with a principal.
	"GET /api/hostd/connect": public,
}

// The people the fixtures name. On the single-tenant daemon
// TestRouteAuthorization sweeps none of them is anybody -- there are no
// accounts, and the access code is the only credential -- and root is
// who an untrusted probe claims to be.
const (
	rootUser   = "root@example.com"
	memberUser = "member@example.com"
	alphaTeam  = "alpha"
	// standingID is the schedule, template and suite every fixture
	// files under alpha for the standing-work {id} routes to name.
	standingID = "route-authorization"
	// publicAddr is where every request comes from: an address on the
	// open internet, so a plaintext identity header (and the fallback
	// that believes one from loopback) cannot vouch for anybody.
	publicAddr = "203.0.113.9:4242" // TEST-NET-3
)

// TestRouteAuthorization walks every route pkg/ui's Server registers and
// holds it to routeAuthorization (#2425 Layer 1b), on a single-tenant
// daemon (docs/single-tenant-reset.md, stage 3):
//
//   - a route missing from the table fails, and so does a table entry
//     no longer registered;
//   - every trusted route is asked by a caller the daemon does not
//     trust -- from loopback, without the access code, claiming to be
//     the system admin with a session token a multi-tenant build signed,
//     an identity header and ?user= -- and must refuse with 401 or 403
//     (#2370, grain/task-4343);
//   - every trusted route is asked by the trusted caller -- the same
//     address, with the access session its code signs in to -- and must
//     not refuse it: a route that does turned the daemon's own owner
//     away for a role nobody holds any more.
//
// "Refuse" is strictly 401 or 403. A 400 for a malformed body, a 404
// for a missing object, or a 503 for a feature this fixture leaves off
// all mean the handler looked at the request before deciding who may
// make it, which is the ordering this test exists to hold.
//
// Gaps are a ratchet, .grain/baselines/route-authorization.txt, keyed
// "<METHOD>:<path>" with one count per failed check, in the same
// both-ways semantics as the other baselines: fixing a route means
// lowering it.
func TestRouteAuthorization(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	// One fixture serves every GET probe: a read changes nothing the next
	// read is answered from, and a fixture is most of this test's time.
	// Anything else gets fresh ones (see probeRoute).
	shared := newRouteFixtureFor(t, true)
	routes := shared.server.Routes()
	registered := map[string]bool{}
	for _, pattern := range routes {
		registered[pattern] = true
		if _, ok := routeAuthorization[pattern]; !ok {
			t.Errorf("route %q is not in routeAuthorization: add it with the level of who may call it "+
				"(public or trusted)", pattern)
		}
	}
	for pattern := range routeAuthorization {
		if !registered[pattern] {
			t.Errorf("routeAuthorization lists %q, which pkg/ui no longer registers: remove it", pattern)
		}
	}

	var mu sync.Mutex
	found := map[string][]string{}
	t.Run("routes", func(t *testing.T) {
		for _, pattern := range routes {
			level, ok := routeAuthorization[pattern]
			if !ok || level == public {
				continue
			}
			t.Run(pattern, func(t *testing.T) {
				t.Parallel()
				violations := probeRoute(t, pattern, shared)
				if len(violations) > 0 {
					mu.Lock()
					found[routeKey(pattern)] = violations
					mu.Unlock()
				}
			})
		}
	})

	baseline, err := readBaseline(filepath.Join(repoRoot(t), ".grain", "baselines", "route-authorization.txt"))
	if err != nil {
		t.Fatalf("reading the baseline: %v", err)
	}
	checkRatchet(t, ratchetRule{
		name: "route-authorization",
		why: "a route answered a caller the daemon does not trust, or refused the one it does. The access gate " +
			"(pkg/ui/accesscode.go) refuses the first before any handler runs; a handler refuses the second when it " +
			"still asks for a role nobody holds on a single-tenant daemon: whoever holds the access code is the owner.",
	}, baseline, found)
}

// routeKey is a pattern as the baseline names it: one field, so the
// "<path> <count>" format every baseline shares still reads it.
func routeKey(pattern string) string {
	return strings.Replace(pattern, " ", ":", 1)
}

// probeRoute asks one trusted route as an untrusted caller and as the
// trusted one. A GET is asked of shared; anything else of a fixture of
// its own, so that a write one request should not have been allowed to
// make cannot change what the next is answered. It returns one line per
// check that failed.
func probeRoute(t *testing.T, pattern string, shared *routeFixture) []string {
	fixture := func() *routeFixture {
		if strings.HasPrefix(pattern, http.MethodGet+" ") {
			return shared
		}
		return newRouteFixtureFor(t, true)
	}
	var violations []string
	if rec := fixture().untrusted().do(t, pattern, rootUser); !refused(rec.code) {
		violations = append(violations, fmt.Sprintf("%s: answered %d %s to a caller from loopback without the access code "+
			"claiming to be %s, want 401 or 403", pattern, rec.code, rec.excerpt(), rootUser))
	}
	if rec := fixture().do(t, pattern, ""); refused(rec.code) && !refusesTheTaskNotTheCaller[pattern] {
		violations = append(violations, fmt.Sprintf("%s: refused the trusted caller (the access code's session) with %d %s",
			pattern, rec.code, rec.excerpt()))
	}
	return violations
}

// refusesTheTaskNotTheCaller are the trusted routes that answer the
// trusted caller 403 because of the fixture's task, not because of who
// asked: a task's database is only for its live run, and the fixture's
// task is proposed (pkg/ui's taskdb handlers).
var refusesTheTaskNotTheCaller = map[string]bool{
	"POST /api/tasks/{id}/db/query":   true,
	"POST /api/tasks/{id}/db/execute": true,
}

// untrusted is f without its access session: a request from the same
// place that does not present the code.
func (f *routeFixture) untrusted() *routeFixture {
	u := *f
	u.accessCookie = ""
	return &u
}

func refused(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// routeFixture is one Server over its own store: acme/widgets a repo it
// targets, and one task to aim task routes at.
type routeFixture struct {
	server *ui.Server
	store  *model.Store
	taskID string
	// from is where requests come from; empty means publicAddr.
	from string
	// accessCookie, on a single-tenant fixture, is the access session
	// every request carries (its access code, signed in), so a person's
	// identity can ride beside it.
	accessCookie string
	// proxy signs people in (identityProxy) on a multi-tenant fixture.
	proxy *identityProxy
}

// newRouteFixtureFor is a routeFixture of a single-tenant daemon
// (ui.Config.SingleTenant) when singleTenant is set. Its requests then
// come from loopback, the only place a single-tenant daemon serves, and
// its task is made by the daemon's own actor.
func newRouteFixtureFor(t *testing.T, singleTenant bool) *routeFixture {
	t.Helper()
	return newRouteFixtureWith(t, singleTenant, !singleTenant)
}

type routeFixtureTemplateFile struct {
	name string
	mode os.FileMode
	data []byte
}

type routeFixtureTemplate struct {
	once      sync.Once
	files     []routeFixtureTemplateFile
	taskID    string
	secretKey []byte
	err       error
}

var routeFixtureTemplates [4]routeFixtureTemplate

func prepareRouteFixtureTemplate(t *testing.T, singleTenant, withProxy bool) *routeFixtureTemplate {
	t.Helper()
	idx := 0
	if singleTenant {
		idx |= 1
	}
	if withProxy {
		idx |= 2
	}
	tmpl := &routeFixtureTemplates[idx]
	tmpl.once.Do(func() {
		dir, err := os.MkdirTemp("", "grain-route-fixture-template-")
		if err != nil {
			tmpl.err = err
			return
		}
		defer os.RemoveAll(dir)

		dbDir := filepath.Join(dir, "db")
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			tmpl.err = err
			return
		}
		ctx := context.Background()
		db, err := sqlite.Open(sqlite.DefaultConfig(dbDir))
		if err != nil {
			tmpl.err = fmt.Errorf("opening sqlite: %w", err)
			return
		}
		store := model.New(db)
		if err := store.Init(ctx); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("init store: %w", err)
			return
		}
		cfg, err := store.GetConfig(ctx)
		if err != nil {
			_ = db.Close()
			tmpl.err = err
			return
		}
		if cfg == nil {
			d := model.NewDeploymentConfig()
			cfg = &d
		}
		if err := store.PutConfig(ctx, *cfg); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("putting config: %w", err)
			return
		}
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutSchedule(ctx, model.Schedule{
			ID: standingID, Title: "route authorization", Target: repo,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		}); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("putting the fixture schedule: %w", err)
			return
		}
		if err := store.PutTemplate(ctx, model.Template{
			ID: standingID, Name: "route authorization", Title: "route authorization",
		}); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("putting the fixture template: %w", err)
			return
		}
		if err := store.PutSuite(ctx, model.Suite{
			ID: standingID, Name: "route authorization", Mode: model.SuiteCount, Count: 1,
		}); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("putting the fixture suite: %w", err)
			return
		}
		secDir := filepath.Join(dir, "secrets")
		if err := os.MkdirAll(secDir, 0o700); err != nil {
			_ = db.Close()
			tmpl.err = err
			return
		}
		secStore := secrets.New(secDir)
		if keyData, err := os.ReadFile(secStore.KeyFile()); err == nil {
			tmpl.secretKey = keyData
		}
		credDir := filepath.Join(dir, "creds")
		if err := os.MkdirAll(credDir, 0o700); err != nil {
			_ = db.Close()
			tmpl.err = err
			return
		}
		credentials, err := gitproxy.LoadCredentialSet(credDir)
		if err != nil {
			_ = db.Close()
			tmpl.err = err
			return
		}
		dataDir := filepath.Join(dir, "data")
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			_ = db.Close()
			tmpl.err = err
			return
		}
		uiCfg := ui.Config{
			Actor:         ui.DefaultActor("operator"),
			DefaultTarget: &repo,
			Capabilities:  ui.OfferedCapabilities(),
			Secrets:       secStore,
			Credentials:   credentials,
			CodexAuthFile: filepath.Join(dir, "codex-auth.json"),
			DataDir:       dataDir,
			LocalAPIToken: testAccessCode,
		}
		var proxy *identityProxy
		if withProxy {
			proxy = sharedIdentityProxy()
			proxy.configure(&uiCfg)
		}
		server := ui.NewServer(uiCfg, store)
		f := &routeFixture{server: server, store: store, taskID: "1", proxy: proxy}
		var rec *statusRecorder
		if singleTenant {
			f.from = loopbackAddr
			f.accessCookie = accessSession(t, server, loopbackAddr, testAccessCode)
			rec = f.send(t, http.MethodPost, "/api/tasks", `{"title":"route authorization","repo":"acme/widgets"}`, "")
		} else {
			rec = f.send(t, http.MethodPost, "/api/tasks", `{"title":"route authorization","repo":"acme/widgets"}`, rootUser)
		}
		var task struct {
			ID string `json:"id"`
		}
		if rec.code/100 == 2 && json.Unmarshal(rec.body.Bytes(), &task) == nil && task.ID != "" {
			tmpl.taskID = task.ID
		} else {
			tmpl.taskID = "1"
		}
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			_ = db.Close()
			tmpl.err = fmt.Errorf("checkpointing fixture template: %w", err)
			return
		}
		if err := db.Close(); err != nil {
			tmpl.err = fmt.Errorf("closing fixture template: %w", err)
			return
		}
		entries, err := os.ReadDir(dbDir)
		if err != nil {
			tmpl.err = err
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				tmpl.err = err
				return
			}
			data, err := os.ReadFile(filepath.Join(dbDir, entry.Name()))
			if err != nil {
				tmpl.err = err
				return
			}
			tmpl.files = append(tmpl.files, routeFixtureTemplateFile{
				name: entry.Name(),
				mode: info.Mode().Perm(),
				data: data,
			})
		}
	})
	return tmpl
}

// newRouteFixtureWith is newRouteFixtureFor, signing people in through an
// identityProxy only when withProxy is set. A daemon with one verifies
// IAP assertions, and so believes no plaintext identity header at all;
// one without is what an operator who configured neither runs.
//
// configure, when given, edits the UI's config before the server is
// built from it: a daemon hook a route consults (ui.Config.CLIRunCheck).
func newRouteFixtureWith(t *testing.T, singleTenant, withProxy bool, configure ...func(*ui.Config)) *routeFixture {
	t.Helper()
	tmpl := prepareRouteFixtureTemplate(t, singleTenant, withProxy)
	if tmpl.err != nil {
		t.Fatalf("preparing route fixture template: %v", tmpl.err)
	}
	root := t.TempDir()
	dbDir := filepath.Join(root, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range tmpl.files {
		if err := os.WriteFile(filepath.Join(dbDir, file.name), file.data, file.mode); err != nil {
			t.Fatalf("copying route fixture db: %v", err)
		}
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dbDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)

	credDir := filepath.Join(root, "creds")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}
	secDir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if len(tmpl.secretKey) > 0 {
		_ = os.WriteFile(filepath.Join(secDir, secrets.DefaultKeyFileName), tmpl.secretKey, 0o600)
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	uiCfg := ui.Config{
		// The actor a request that names nobody falls back to. A
		// deployment's is its operating-system user.
		Actor:         ui.DefaultActor("operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		// Somewhere for the credential routes to write, so they reach
		// whatever they check about the caller instead of stopping at
		// "this UI has no secrets directory".
		Secrets:       secrets.New(secDir),
		Credentials:   credentials,
		CodexAuthFile: filepath.Join(root, "codex-auth.json"),
		// Where the embedded PostgreSQL goes when a
		// POST /api/database/test or /upgrade names no DSN and the
		// binaries are installed. Left empty, EnsureRunning initdb's a
		// cluster in ./postgres -- tests/security/postgres/, which
		// `git add -A` once committed.
		DataDir:       dataDir,
		LocalAPIToken: testAccessCode,
	}
	var proxy *identityProxy
	if withProxy {
		// One signer for every fixture, so its keys are fetched once
		// and cached (iapKeysTTL) rather than once per fixture, where a
		// slow fetch on a loaded machine would refuse a probe for a
		// reason that has nothing to do with the route.
		proxy = sharedIdentityProxy()
		proxy.configure(&uiCfg)
	}
	for _, c := range configure {
		c(&uiCfg)
	}
	server := ui.NewServer(uiCfg, store)
	f := &routeFixture{server: server, store: store, taskID: tmpl.taskID, proxy: proxy}
	if singleTenant {
		f.from = loopbackAddr
		f.accessCookie = accessSession(t, server, loopbackAddr, testAccessCode)
	}
	return f
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// path fills a pattern's wildcards with objects the fixture has, so a
// route reaches whatever check it makes on the thing it names rather
// than stopping at "no such thing".
func (f *routeFixture) path(pattern string) string {
	_, path, ok := strings.Cut(pattern, " ")
	if !ok {
		path = pattern
	}
	return pathParam.ReplaceAllStringFunc(path, func(m string) string {
		switch name := m[1 : len(m)-1]; {
		case name == "owner":
			return "acme"
		case name == "name" && strings.Contains(path, "{owner}/{name}"):
			return "widgets"
		case name == "id" && strings.HasPrefix(path, "/api/tasks/"):
			return f.taskID
		case name == "id" && (strings.HasPrefix(path, "/api/schedules/") ||
			strings.HasPrefix(path, "/api/templates/") ||
			strings.HasPrefix(path, "/api/suites/")):
			return standingID
		case name == "id" || name == "number":
			return "1"
		default:
			return "x"
		}
	})
}

// do sends pattern's request as user ("" for nobody). Every probe also
// claims to be the system admin in the query string: who is asking is
// never something the URL gets to say, and an access-account handler
// once believed ?user= (fixed in task 1330), so a route that does the
// same fails here as a route that checked nobody. A probe as user also
// names them in X-Grain-User, the header grain's own frontend sends.
func (f *routeFixture) do(t *testing.T, pattern, user string) *statusRecorder {
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
	target := f.path(pattern) + "?user=" + url.QueryEscape(rootUser)
	header := f.sessionHeader(t, user)
	if user != "" {
		header.Set("X-Grain-User", user)
	}
	return f.serve(t, method, target, body, header)
}

// probeBodies are the requests a bare "{}" would not get past
// validation with: well-formed ones aimed at alpha, so the only thing
// left for the route to object to is who is asking. Without them a
// handler that validates before it authorizes answers 400, and the
// sweep cannot tell that from a refusal it never made.
var probeBodies = map[string]string{
	"POST /api/schedules": `{"title":"route authorization","repo":"acme/widgets",` +
		`"recurrence":{"kind":"everyNHours","everyNHours":24}}`,
	"POST /api/templates":                     `{"name":"route authorization","title":"route authorization"}`,
	"POST /api/suites":                        `{"name":"route authorization","mode":"count","count":1}`,
	"POST /api/repos/{owner}/{name}/branches": `{"name":"route-authorization"}`,
}

// send serves one request from publicAddr, authenticated as user by an
// assertion this deployment verifies (identityProxy), or not at all. Streaming routes
// never finish on their own, so the request's context is cancelled once
// it has written its status (serveUntilAnswered).
func (f *routeFixture) send(t *testing.T, method, target, body, user string) *statusRecorder {
	t.Helper()
	return f.serve(t, method, target, body, f.sessionHeader(t, user))
}

// sessionHeader is the header that signs user in: an assertion on a
// multi-tenant fixture. A single-tenant daemon signs nobody in, so there
// it is the session a multi-tenant build signed for user
// (oldSessionToken), which must name nobody.
func (f *routeFixture) sessionHeader(t *testing.T, user string) http.Header {
	t.Helper()
	if f.proxy == nil {
		header := http.Header{}
		if user != "" {
			header.Set("Authorization", "Bearer "+oldSessionToken(t, user))
		}
		return header
	}
	return f.proxy.header(t, user)
}

// serve answers one request from f.from (publicAddr if empty) with
// header, f's access cookie beside it.
func (f *routeFixture) serve(t *testing.T, method, target, body string, header http.Header) *statusRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = publicAddr
	if f.from != "" {
		r.RemoteAddr = f.from
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, values := range header {
		r.Header[name] = values
	}
	if f.accessCookie != "" {
		r.AddCookie(&http.Cookie{Name: ui.AccessCookieName, Value: f.accessCookie})
	}
	return serveUntilAnswered(t, f.server, r)
}

// unansweredAfter is how long serveUntilAnswered lets a handler run
// without writing anything before it ends the request and counts it
// accepted. Far longer than any handler takes on an idle machine, so a
// slow one on a loaded CI runner (-race, every package's tests at once)
// still gets to answer.
const unansweredAfter = 30 * time.Second

// serveUntilAnswered serves r on srv until its handler has returned or
// written its status, whichever is first, and then cancels the
// request's context: a streaming route never finishes on its own, and
// the status it started with is all a probe reads. The request carries
// no deadline of its own until then. One did (2s), and on a loaded
// runner it expired under handlers that had not been reached yet --
// the access check's store lookup answered 503 "timed out" for a
// member on GET /api/upgrade, which the sweep had to count as let
// through.
func serveUntilAnswered(t *testing.T, srv http.Handler, r *http.Request) *statusRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	rec := &statusRecorder{header: http.Header{}, answered: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(rec, r)
	}()
	select {
	case <-done:
	case <-rec.answered:
	case <-time.After(unansweredAfter):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("%s %s did not return within 10s of its context ending", r.Method, r.URL)
	}
	return rec.snapshot()
}

// statusRecorder is a ResponseWriter a streaming handler can go on
// writing to while the test reads the status it started with.
type statusRecorder struct {
	mu     sync.Mutex
	header http.Header
	code   int
	body   bytes.Buffer
	// answered, if set, is closed when the status is first written.
	answered chan struct{}
}

func (r *statusRecorder) Header() http.Header { return r.header }

func (r *statusRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCode(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCode(http.StatusOK)
	return r.body.Write(p)
}

// setCode records code unless a status was already written. r.mu is held.
func (r *statusRecorder) setCode(code int) {
	if r.code != 0 {
		return
	}
	r.code = code
	if r.answered != nil {
		close(r.answered)
	}
}

func (r *statusRecorder) Flush() {}

// excerpt is the start of what a route answered, quoted, so a failure
// says why it was let through as well as that it was.
func (r *statusRecorder) excerpt() string {
	b := strings.TrimSpace(r.body.String())
	if len(b) > 120 {
		b = b[:120] + "..."
	}
	return fmt.Sprintf("%q", b)
}

func (r *statusRecorder) snapshot() *statusRecorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := &statusRecorder{code: r.code}
	out.body.Write(r.body.Bytes())
	if out.code == 0 {
		// A handler still running that has written nothing has accepted
		// the request as surely as one that answered 200.
		out.code = http.StatusOK
	}
	return out
}
