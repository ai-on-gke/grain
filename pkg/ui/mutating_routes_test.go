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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// attributedRoute is one write path's row in operator-attribution spec
// §7.2's matrix: a request that exercises it against a task setup filed,
// and what it recorded as its actor.
type attributedRoute struct {
	// setup prepares what the request acts on and returns the task it
	// acts on, if any. Nil for none.
	setup func(t *testing.T, client *ui.Client, srv *ui.Server, send sender) string
	// body is the request's JSON body.
	body string
	// recorded reads back who the write recorded. created is the
	// response's task id, for a route that files one.
	recorded func(t *testing.T, ctx context.Context, store *model.Store, id, created string) model.Principal
}

// sender issues one request the way a row of the matrix asks.
type sender func(t *testing.T, method, path, body string) *httptest.ResponseRecorder

func fileTask(approved bool) func(*testing.T, *ui.Client, *ui.Server, sender) string {
	return func(t *testing.T, client *ui.Client, _ *ui.Server, _ sender) string {
		t.Helper()
		task, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "attributed", Approved: approved, NoRepo: true})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task.ID
	}
}

func lastEvent(t *testing.T, ctx context.Context, store *model.Store, id, _ string) model.Principal {
	t.Helper()
	events, err := store.TaskEvents(ctx, id)
	if err != nil || len(events) == 0 {
		t.Fatalf("task events of %s: %v, %v", id, events, err)
	}
	return events[len(events)-1].By
}

// attributedRoutes are the write paths the matrix covers. Step 1 of the
// rollout checks the operator fallback's two sources here; step 2 adds
// §7.2's verified rows (IAP, a registered verifier, a session's
// on-behalf-of) and moves routes up from notYetAttributedRoutes as it
// gives them a case.
var attributedRoutes = map[string]attributedRoute{
	"POST /api/tasks": {
		body: `{"title":"filed","noRepo":true}`,
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, _, created string) model.Principal {
			task, err := store.GetTask(ctx, created)
			if err != nil || task == nil {
				t.Fatalf("the filed task %q: %v, %v", created, task, err)
			}
			return task.Origin.Attribution.Actor
		},
	},
	"POST /api/tasks/{id}/comments": {
		setup: fileTask(true),
		body:  `{"body":"a word"}`,
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, id, _ string) model.Principal {
			comments, err := store.Comments(ctx, id)
			if err != nil || len(comments) == 0 {
				t.Fatalf("comments on %s: %v, %v", id, comments, err)
			}
			return comments[len(comments)-1].Author.Actor
		},
	},
	"POST /api/tasks/{id}/approve": {
		setup: fileTask(false),
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, id, _ string) model.Principal {
			task, err := store.GetTask(ctx, id)
			if err != nil || task == nil || task.Approval == nil {
				t.Fatalf("the approved task %s: %+v, %v", id, task, err)
			}
			return task.Approval.Actor
		},
	},
	"POST /api/tasks/{id}/pause": {
		setup: fileTask(true),
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, id, _ string) model.Principal {
			pause, err := store.GetPause(ctx, id)
			if err != nil || pause == nil {
				t.Fatalf("the pause of %s: %v, %v", id, pause, err)
			}
			return pause.PausedBy
		},
	},
	"POST /api/tasks/{id}/close": {
		setup:    fileTask(true),
		recorded: lastEvent,
	},
	"POST /api/tasks/{id}/reopen": {
		setup: func(t *testing.T, client *ui.Client, srv *ui.Server, send sender) string {
			id := fileTask(true)(t, client, srv, send)
			if rec := send(t, http.MethodPost, "/api/tasks/"+id+"/close", ""); rec.Code >= 300 {
				t.Fatalf("closing %s: %d %s", id, rec.Code, rec.Body)
			}
			return id
		},
		recorded: lastEvent,
	},
	"PUT /api/memory": {
		body: `{"text":"remember this"}`,
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, _, _ string) model.Principal {
			mem, err := store.GetMemory(ctx, nil)
			if err != nil || mem == nil {
				t.Fatalf("the memory: %v, %v", mem, err)
			}
			return mem.UpdatedBy
		},
	},
	"POST /api/pipeline-pause": {
		body: `{"reason":"maintenance"}`,
		recorded: func(t *testing.T, ctx context.Context, store *model.Store, _, _ string) model.Principal {
			p, err := store.GetPipelinePause(ctx)
			if err != nil || p == nil {
				t.Fatalf("the pipeline pause: %v, %v", p, err)
			}
			return p.By
		},
	},
}

// neverAttributedRoutes are non-GET routes that write nothing a person
// is credited with: signing in and out, and requests that only check,
// plan or preview a change without making it.
var neverAttributedRoutes = map[string]bool{
	// Signing in and out: there is nobody to credit until it succeeds,
	// and nothing is written that names them.
	"POST /api/auth/access-code": true,
	"POST /api/auth/logout":      true,
	// Checks, tests and previews: they report what a change would do and
	// write nothing.
	"POST /api/agent-keys/{framework}/check":     true,
	"POST /api/capabilities/{id}/check":          true,
	"POST /api/capability-grants/check":          true,
	"POST /api/capability-policy/preview":        true,
	"POST /api/capability-servers/test":          true,
	"POST /api/database/test":                    true,
	"POST /api/deployment/git-connections/check": true,
	"POST /api/settings/plan":                    true,
	// The MCP endpoint is a transport: each tool it serves calls one of
	// the routes here, and is attributed there.
	"POST /mcp": true,
}

// notYetAttributedRoutes are the rest of spec §3's write paths, exempt
// until the matrix has a case for them. Each is one of: a path that
// already records RequestActor (§3 "actor") with no row here yet; one
// that records the operator as a bare string (§3 "coarse"); or one that
// records no actor at all (§3 "none"), which §3's Target column gives a
// note or a config_audit row. This list only shrinks: a route added to
// Server goes into attributedRoutes, or onto neverAttributedRoutes with
// its reason, never here.
var notYetAttributedRoutes = map[string]bool{
	"DELETE /api/executors/tokens/{id}":                                    true,
	"DELETE /api/executors/tokens/{id}/revoke":                             true,
	"POST /api/executors/tokens":                                           true,
	"POST /api/executors/tokens/{id}/revoke":                               true,
	"DELETE /api/agent-backends/{id}/codex-auth":                           true,
	"DELETE /api/agent-backends/{id}/credential":                           true,
	"DELETE /api/agent-keys/{framework}":                                   true,
	"DELETE /api/boards/{id}":                                              true,
	"DELETE /api/ci/cache":                                                 true,
	"DELETE /api/ci/runs/{id}":                                             true,
	"DELETE /api/codex-auth":                                               true,
	"DELETE /api/console-sessions/{id}":                                    true,
	"DELETE /api/console/{id}":                                             true,
	"DELETE /api/deployment/agent-backends/{backendId}":                    true,
	"DELETE /api/deployment/git-connections/{cred}":                        true,
	"DELETE /api/external-agents/tokens/{id}":                              true,
	"DELETE /api/gcp-keys/{name}":                                          true,
	"DELETE /api/github-tokens/{name}":                                     true,
	"DELETE /api/pause":                                                    true,
	"DELETE /api/pipeline-pause":                                           true,
	"DELETE /api/repos/{owner}/{name}":                                     true,
	"DELETE /api/repos/{owner}/{name}/release-secrets/{secret}":            true,
	"DELETE /api/repos/{owner}/{name}/releases/{release}":                  true,
	"DELETE /api/schedules/{id}":                                           true,
	"DELETE /api/secrets/{secret}":                                         true,
	"DELETE /api/secrets/{secret}/{key}":                                   true,
	"DELETE /api/suites/{id}":                                              true,
	"DELETE /api/tasks/{id}/goal":                                          true,
	"DELETE /api/templates/{id}":                                           true,
	"DELETE /api/upgrade/scheduled":                                        true,
	"PATCH /api/boards/{id}":                                               true,
	"PATCH /api/schedules/{id}":                                            true,
	"PATCH /api/suites/{id}":                                               true,
	"PATCH /api/tasks/{id}":                                                true,
	"PATCH /api/templates/{id}":                                            true,
	"POST /api/actions-cache/flush":                                        true,
	"POST /api/api-scope-sets/delete":                                      true,
	"POST /api/boards":                                                     true,
	"POST /api/capability-servers/bind-secret":                             true,
	"POST /api/capability-servers/disable":                                 true,
	"POST /api/capability-servers/dismiss-notice":                          true,
	"POST /api/capability-servers/enable":                                  true,
	"POST /api/capability-servers/gchat-test":                              true,
	"POST /api/capability-servers/remove":                                  true,
	"POST /api/capability-servers/rotate-managed":                          true,
	"POST /api/capability-servers/test-script":                             true,
	"POST /api/ci/runs":                                                    true,
	"POST /api/console-sessions":                                           true,
	"POST /api/console-sessions/{id}/cancel":                               true,
	"POST /api/console-sessions/{id}/restart":                              true,
	"POST /api/console/{id}/input":                                         true,
	"POST /api/console/{id}/resize":                                        true,
	"POST /api/database/upgrade":                                           true,
	"POST /api/deployment/broadcast":                                       true,
	"POST /api/deployment/git-connections":                                 true,
	"POST /api/external-agents/tokens":                                     true,
	"POST /api/external-agents/tokens/revoke-all":                          true,
	"POST /api/external-agents/tokens/{id}/revoke":                         true,
	"POST /api/external-agents/tokens/{id}/rotate":                         true,
	"POST /api/gcp-keys":                                                   true,
	"POST /api/host/reboot":                                                true,
	"POST /api/host/restart":                                               true,
	"POST /api/host/shutdown":                                              true,
	"POST /api/notifications":                                              true,
	"POST /api/notifications/{id}/ack":                                     true,
	"POST /api/notifications/{id}/clear":                                   true,
	"POST /api/notifications/{id}/investigate":                             true,
	"POST /api/package-cache/flush":                                        true,
	"POST /api/persistent-sandboxes/{name}/{action}":                       true,
	"POST /api/repos":                                                      true,
	"POST /api/repos/{owner}/{name}/base-branch-runs":                      true,
	"POST /api/repos/{owner}/{name}/branches":                              true,
	"POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve": true,
	"POST /api/repos/{owner}/{name}/import-issues":                         true,
	"POST /api/repos/{owner}/{name}/lifecycle/refresh":                     true,
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/approve":    true,
	"POST /api/repos/{owner}/{name}/pending-releases/{version}/reject":     true,
	"POST /api/repos/{owner}/{name}/release-runs":                          true,
	"POST /api/repos/{owner}/{name}/releases":                              true,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates":         true,
	"POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote": true,
	"POST /api/repos/{owner}/{name}/releases/{release}/merge":              true,
	"POST /api/sandbox-probe":                                              true,
	"POST /api/schedules":                                                  true,
	"POST /api/schedules/composition":                                      true,
	"POST /api/schedules/{id}/run":                                         true,
	"POST /api/settings":                                                   true,
	"POST /api/settings/db/migrate-to-postgres":                            true,
	"POST /api/state-repo/import-key":                                      true,
	"POST /api/state-repo/pull-requests":                                   true,
	"POST /api/state-repo/remote":                                          true,
	"POST /api/state-repo/reset-notice":                                    true,
	"POST /api/state-repo/secrets-key":                                     true,
	"POST /api/state-repo/secrets-repo":                                    true,
	"POST /api/state-repo/sync":                                            true,
	"POST /api/suite-runs":                                                 true,
	"POST /api/suites":                                                     true,
	"POST /api/suites/composition":                                         true,
	"POST /api/tasks/from-template":                                        true,
	"POST /api/tasks/reorder":                                              true,
	"POST /api/tasks/{id}/activity":                                        true,
	"POST /api/tasks/{id}/agent-messages":                                  true,
	"POST /api/tasks/{id}/capabilities":                                    true,
	"POST /api/tasks/{id}/change-checks":                                   true,
	"POST /api/tasks/{id}/change-mail":                                     true,
	"POST /api/tasks/{id}/change-publish":                                  true,
	"POST /api/tasks/{id}/change-review":                                   true,
	"POST /api/tasks/{id}/change-sync":                                     true,
	"POST /api/tasks/{id}/config-change":                                   true,
	"POST /api/tasks/{id}/config-change/approve":                           true,
	"POST /api/tasks/{id}/config-change/reject":                            true,
	"POST /api/tasks/{id}/db/execute":                                      true,
	"POST /api/tasks/{id}/db/query":                                        true,
	"POST /api/tasks/{id}/defer":                                           true,
	"POST /api/tasks/{id}/delivered":                                       true,
	"POST /api/tasks/{id}/depends-on":                                      true,
	"POST /api/tasks/{id}/force-merge":                                     true,
	"POST /api/tasks/{id}/local-script-approvals":                          true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/answer":        true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/expire":        true,
	"POST /api/tasks/{id}/local-script-approvals/{approval}/wait":          true,
	"POST /api/tasks/{id}/messages":                                        true,
	"POST /api/tasks/{id}/notes":                                           true,
	"POST /api/tasks/{id}/notify":                                          true,
	"POST /api/tasks/{id}/park-until":                                      true,
	"POST /api/tasks/{id}/pull-request":                                    true,
	"POST /api/tasks/{id}/resume":                                          true,
	"POST /api/tasks/{id}/resume-later":                                    true,
	"POST /api/tasks/{id}/retry":                                           true,
	"POST /api/tasks/{id}/retry-merge":                                     true,
	"POST /api/tasks/{id}/run-now":                                         true,
	"POST /api/tasks/{id}/sandbox/recreate":                                true,
	"POST /api/tasks/{id}/staged-attachments":                              true,
	"POST /api/tasks/{id}/stop":                                            true,
	"POST /api/tasks/{id}/submit":                                          true,
	"POST /api/tasks/{id}/undefer":                                         true,
	"POST /api/tasks/{id}/wait-credit":                                     true,
	"POST /api/tasks/{id}/wait-on":                                         true,
	"POST /api/tasks/{id}/withdraw-approval":                               true,
	"POST /api/templates":                                                  true,
	"POST /api/tests/quarantine":                                           true,
	"POST /api/upgrade":                                                    true,
	"POST /api/upgrade/postgres":                                           true,
	"PUT /api/agent-backends/{id}/codex-auth":                              true,
	"PUT /api/agent-backends/{id}/credential":                              true,
	"PUT /api/agent-keys/{framework}":                                      true,
	"PUT /api/api-scope-sets":                                              true,
	"PUT /api/capability-policy":                                           true,
	"PUT /api/capability-servers":                                          true,
	"PUT /api/codex-auth":                                                  true,
	"PUT /api/console-sessions/{id}":                                       true,
	"PUT /api/deployment":                                                  true,
	"PUT /api/deployment/agent-backends/{backendId}":                       true,
	"PUT /api/deployment/repos/{owner}/{name}/config":                      true,
	"PUT /api/deployment/repos/{owner}/{name}/git-connection":              true,
	"PUT /api/gcp-keys/{name}":                                             true,
	"PUT /api/github-tokens/{name}":                                        true,
	"PUT /api/memory/documents":                                            true,
	"PUT /api/release-notes":                                               true,
	"PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates":       true,
	"PUT /api/repos/{owner}/{name}/capabilities":                           true,
	"PUT /api/repos/{owner}/{name}/ci-capabilities":                        true,
	"PUT /api/repos/{owner}/{name}/ci-mode":                                true,
	"PUT /api/repos/{owner}/{name}/declarations":                           true,
	"PUT /api/repos/{owner}/{name}/ignore-github-ci":                       true,
	"PUT /api/repos/{owner}/{name}/merge-on-green":                         true,
	"PUT /api/repos/{owner}/{name}/prompt-extension":                       true,
	"PUT /api/repos/{owner}/{name}/qualification-plan":                     true,
	"PUT /api/repos/{owner}/{name}/release":                                true,
	"PUT /api/repos/{owner}/{name}/release-secrets/{secret}":               true,
	"PUT /api/repos/{owner}/{name}/require-branch-up-to-date":              true,
	"PUT /api/repos/{owner}/{name}/settings":                               true,
	"PUT /api/repos/{owner}/{name}/setup-command":                          true,
	"PUT /api/repos/{owner}/{name}/unfixable-checks":                       true,
	"PUT /api/repos/{owner}/{name}/watch-default-branch":                   true,
	"PUT /api/repos/{owner}/{name}/work-ownership":                         true,
	"PUT /api/repos/{owner}/{name}/workflows":                              true,
	"PUT /api/secrets/{secret}/{key}":                                      true,
	"PUT /api/settings":                                                    true,
	"PUT /api/settings/cli-run":                                            true,
	"PUT /api/tasks/{id}/goal":                                             true,
	"PUT /api/tasks/{id}/secret":                                           true,
	"PUT /api/tasks/{id}/session-title":                                    true,
}

// TestEveryMutatingRouteIsAttributed is spec §7.3: every non-GET route
// Server registers is covered by the matrix or on a commented exemption
// list, and each covered one records the operator fallback with the
// source the request came by.
func TestEveryMutatingRouteIsAttributed(t *testing.T) {
	srv := ui.NewServerWithClient(nil)
	registered := map[string]bool{}
	for _, pattern := range srv.Routes() {
		method, _, ok := strings.Cut(pattern, " ")
		if !ok || method == http.MethodGet {
			continue
		}
		registered[pattern] = true
		lists := 0
		for _, on := range []bool{attributedRoutes[pattern].recorded != nil, neverAttributedRoutes[pattern], notYetAttributedRoutes[pattern]} {
			if on {
				lists++
			}
		}
		switch lists {
		case 0:
			t.Errorf("%s is neither attributed (attributedRoutes) nor exempt (neverAttributedRoutes, with its reason): "+
				"a write must record ui.RequestActor", pattern)
		case 1:
		default:
			t.Errorf("%s is on more than one of attributedRoutes, neverAttributedRoutes and notYetAttributedRoutes", pattern)
		}
	}
	for _, list := range []map[string]bool{neverAttributedRoutes, notYetAttributedRoutes} {
		for pattern := range list {
			if !registered[pattern] {
				t.Errorf("%s is exempt, but Server registers no such non-GET route", pattern)
			}
		}
	}

	for pattern, route := range attributedRoutes {
		if !registered[pattern] {
			t.Errorf("attributedRoutes covers %s, which Server does not register", pattern)
			continue
		}
		for _, way := range []struct {
			name   string
			source model.AuthSource
			send   func(srv *ui.Server) sender
		}{
			{"in-process", model.SourceInProcess, func(srv *ui.Server) sender {
				return func(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
					return do(t, srv, method, path, body)
				}
			}},
			{"access code", model.SourceAccessCode, func(srv *ui.Server) sender {
				return func(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
					t.Helper()
					req := httptest.NewRequest(method, path, strings.NewReader(body))
					req.RemoteAddr = "198.51.100.7:4242"
					req.Header.Set("Authorization", "Bearer "+testAccessCode)
					rec := httptest.NewRecorder()
					srv.ServeHTTP(rec, req)
					return rec
				}
			}},
		} {
			t.Run(pattern+"/"+way.name, func(t *testing.T) {
				client, store, ctx := testClient(t)
				client.Config.LocalAPIToken = testAccessCode
				srv := ui.NewServerWithClient(client)
				send := way.send(srv)
				var id string
				if route.setup != nil {
					id = route.setup(t, client, srv, send)
				}
				_, path, _ := strings.Cut(pattern, " ")
				method, _, _ := strings.Cut(pattern, " ")
				rec := send(t, method, strings.ReplaceAll(path, "{id}", id), route.body)
				if rec.Code >= 300 {
					t.Fatalf("%s: %d %s", pattern, rec.Code, rec.Body)
				}
				var created struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &created)
				want := ui.DefaultActor("alice")
				want.Source = way.source
				if got := route.recorded(t, ctx, store, id, created.ID); got != want {
					t.Errorf("%s by %s recorded %+v, want %+v", pattern, way.name, got, want)
				}
			})
		}
	}
}
