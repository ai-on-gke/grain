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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Horizontal access (task 3981). grain is single-tenant: a task, its
// runs, transcripts, comments, attachments, PRs and sandboxes belong to
// the deployment, not to whoever filed them, and Task.Owner (#3238) is
// attribution, not an access boundary (docs/design/access-rbac.md §8).
// What does scope an object to a caller is
//
//   - the repository: a role, an agent's grants and a gma_ credential
//     may each be limited to some repositories, and every object route
//     resolves the object's repository before answering
//     (agentCallRepo), so a caller limited to acme/widgets reaches
//     nothing of a task in other/gadgets;
//   - for an agent run, its own task: it reads only its own
//     conversation, never ends or regrants itself, and touches no
//     standing session it does not own (agentRails);
//   - for a gma_ credential, itself: it may always revoke itself, and
//     no other credential without capabilities:admin.
//
// docs/security/audits/2026-11-10-route-authorization.md lists which
// objects are which.

// horizontalCallers are limited to acme/widgets, each its own way.
func horizontalCallers() []matrixCaller {
	return []matrixCaller{
		{name: "role " + matrixWidgetsOnly, role: matrixWidgetsOnly},
		{name: "agent limited to acme/widgets", agent: true, task: matrixWidgetsAgentTask},
		{name: "agent limited to acme/widgets (controller MCP)", agent: true, task: matrixWidgetsAgentTask, controller: true},
		{name: "gma_ credential limited to acme/widgets", external: true, token: "gmaWidgets"},
	}
}

// TestHorizontalObjectAccess points every task-object route -- the task,
// its comments, messages, attachments, attempts and transcripts,
// capability audit, sandbox, PR and goal -- at a task in a repository
// the caller does not reach, and requires a refusal. It is generated
// from the route table like the matrix, so a new /api/tasks/{id} route
// is held to it on arrival.
func TestHorizontalObjectAccess(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	if err := model.InstallAPIScopeSets(matrixScopeSets()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })

	shared := newMatrixFixture(t)
	other := *shared
	other.taskID = matrixOtherTask
	checked := 0
	for _, pattern := range shared.server.Routes() {
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/api/tasks/{id}") {
			continue
		}
		checked++
		body := ""
		if method != http.MethodGet {
			body = "{}"
			if b, ok := probeBodies[pattern]; ok {
				body = b
			}
		}
		target := other.path(pattern)
		for _, c := range horizontalCallers() {
			f := shared
			if method != http.MethodGet || c.external {
				f = newMatrixFixture(t)
			}
			if rec := f.as(t, c, method, target, body); !refused(rec.code) {
				t.Errorf("%s on task %s (%s) as %s: answered %d %s, want 401 or 403",
					pattern, matrixOtherTask, matrixOtherRepo, c.name, rec.code, rec.excerpt())
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked %d /api/tasks/{id} routes, want at least 50", checked)
	}

	// Lists and the namespace narrow rather than refuse: they answer, but
	// with nothing of the other repository in them.
	for _, target := range []string{"/api/tasks", "/api/tasks?repo=" + matrixOtherRepo, "/api/inbox",
		"/api/fs/task/" + matrixOtherTask, "/api/fs/task/"} {
		for _, c := range horizontalCallers() {
			f := shared
			if c.external {
				f = newMatrixFixture(t)
			}
			rec := f.as(t, c, http.MethodGet, target, "")
			if !refused(rec.code) && strings.Contains(rec.body.String(), "matrix other repo") {
				t.Errorf("GET %s as %s: answered %d naming task %s of %s, which it does not reach: %s",
					target, c.name, rec.code, matrixOtherTask, matrixOtherRepo, rec.excerpt())
			}
		}
	}
}

// TestAgentRunIsHeldToItsOwnTask holds an agent run off the objects that
// are its task's own, whatever its grants.
func TestAgentRunIsHeldToItsOwnTask(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	if err := model.InstallAPIScopeSets(matrixScopeSets()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })

	agent := matrixCaller{name: "agent run", agent: true}
	for _, probe := range []struct {
		method, target, body, why string
	}{
		{http.MethodGet, "/api/tasks/" + matrixWidgetsAgentTask + "/messages", "",
			"reads another task's conversation"},
		{http.MethodPost, "/api/tasks/" + matrixAgentTask + "/close", `{}`, "closes its own task"},
		{http.MethodPost, "/api/tasks/" + matrixAgentTask + "/capabilities", `{"id":"api:tasks:admin"}`,
			"regrants its own task"},
		{http.MethodPost, "/api/tasks/" + matrixWidgetsAgentTask + "/human-review", `{}`,
			"asks for human review as another task"},
		{http.MethodPost, "/api/console-sessions", `{"id":"matrix"}`, "starts a standing session"},
		{http.MethodPost, "/api/console/x/input", `{"data":"id\n"}`, "types into a console"},
	} {
		f := newMatrixFixture(t)
		if rec := f.as(t, agent, probe.method, probe.target, probe.body); !refused(rec.code) {
			t.Errorf("an agent run %s (%s %s): answered %d %s, want 401 or 403",
				probe.why, probe.method, probe.target, rec.code, rec.excerpt())
		}
	}
}

// TestExternalCredentialRevokesOnlyItself holds a gma_ credential without
// capabilities:admin to itself: it may revoke itself, never another.
func TestExternalCredentialRevokesOnlyItself(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	f := newMatrixFixture(t)
	gma := matrixCaller{name: "gma_ credential", external: true}
	for _, path := range []string{
		"/api/external-agents/tokens/" + f.seeded["gmaWidgetsID"] + "/revoke",
		"/api/external-agents/tokens/" + f.seeded["gmaWidgetsID"] + "/rotate",
		"/api/external-agents/tokens/revoke-all",
	} {
		if rec := f.as(t, gma, http.MethodPost, path, `{}`); !refused(rec.code) {
			t.Errorf("POST %s with another credential: answered %d %s, want 401 or 403", path, rec.code, rec.excerpt())
		}
	}
	if rec := f.as(t, gma, http.MethodDelete, "/api/external-agents/tokens/"+f.seeded["gmaWidgetsID"], ""); !refused(rec.code) {
		t.Errorf("DELETE another credential: answered %d %s, want 401 or 403", rec.code, rec.excerpt())
	}
	if rec := f.as(t, gma, http.MethodPost, "/api/external-agents/tokens/"+f.seeded["gmaID"]+"/revoke", `{}`); refused(rec.code) {
		t.Errorf("a credential revoking itself: refused %d %s", rec.code, rec.excerpt())
	}
}

const consoleNetworkOpen = `{"id":"matrix-esc","title":"escalate","networkAccess":"open"}`

// TestNoEscalationThroughTheAPI is the operator's question (2026-11-10)
// asked of the API itself: can somebody who is not an admin give
// themselves, or a credential they control, more than they hold?
func TestNoEscalationThroughTheAPI(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	if err := model.InstallAPIScopeSets(matrixScopeSets()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })

	capAdmin := matrixCaller{name: "role " + matrixCapAdmin, role: matrixCapAdmin}
	capAdminAgent := matrixCaller{name: "agent holding capabilities:admin", agent: true, task: matrixCapAdminAgentTask, controller: true}
	accessAdmin := matrixCaller{name: "role " + matrixAccessAdmin, role: matrixAccessAdmin}
	sessions := matrixCaller{name: "role " + matrixSessions, role: matrixSessions}
	viewer := matrixCaller{name: "role grain/viewer", role: "grain/viewer"}
	gmaWidgets := matrixCaller{name: "gma_ credential limited to acme/widgets", external: true, token: "gmaWidgets"}

	mustRefuse := func(c matrixCaller, method, target, body, why string) {
		t.Helper()
		f := newMatrixFixture(t)
		if rec := f.as(t, c, method, target, body); !refused(rec.code) {
			t.Errorf("%s %s (%s %s): answered %d %s, want 401 or 403", c.name, why, method, target, rec.code, rec.excerpt())
		}
	}
	mustAnswer := func(c matrixCaller, method, target, body, why string) {
		t.Helper()
		f := newMatrixFixture(t)
		if rec := f.as(t, c, method, target, body); refused(rec.code) {
			t.Errorf("%s %s (%s %s): refused %d %s, want it answered (the control for the refusals beside it)",
				c.name, why, method, target, rec.code, rec.excerpt())
		}
	}
	token := func(scopes []string, scopeSet string, repos ...string) string {
		b, _ := json.Marshal(map[string]any{"name": "escalate", "scopes": scopes, "scopeSet": scopeSet, "repos": repos})
		return string(b)
	}

	// Minting an external-agent credential wider than yourself.
	const mint = "/api/external-agents/tokens"
	for _, c := range []matrixCaller{capAdmin, capAdminAgent} {
		mustRefuse(c, http.MethodPost, mint, token([]string{"secrets:read"}, ""), "mints a credential holding secrets:read")
		mustRefuse(c, http.MethodPost, mint, token(nil, "grain/operator"), "mints a grain/operator credential")
		mustRefuse(c, http.MethodPost, mint, token([]string{"access:write"}, ""), "mints a credential holding access:write")
		mustRefuse(c, http.MethodPost, mint, token([]string{"settings:write"}, ""), "mints a credential holding settings:write")
	}
	mustAnswer(capAdmin, http.MethodPost, mint, token([]string{"tasks:read"}, ""), "mints a credential within its role")
	mustRefuse(gmaWidgets, http.MethodPost, mint, token([]string{"tasks:read"}, ""), "mints a credential")
	mustRefuse(viewer, http.MethodPost, mint, token([]string{"tasks:read"}, ""), "mints a credential")

	// Editing scope sets: writing a set hands what it resolves to every
	// task and person holding it.
	set := func(name string, scopes ...string) string {
		b, _ := json.Marshal(ui.PutAPIScopeSetRequest{Set: model.APIScopeSet{Name: name, Scopes: scopes, Version: 1}})
		return string(b)
	}
	for _, c := range []matrixCaller{capAdmin, capAdminAgent} {
		mustRefuse(c, http.MethodPut, "/api/api-scope-sets", set(matrixCapAdmin, "capabilities:admin", "secrets:write"),
			"widens its own role's scope set")
		mustRefuse(c, http.MethodPut, "/api/api-scope-sets", set("matrix-escalate", "secrets:read", "access:write"),
			"writes a scope set wider than itself")
		// Nobody removes a built-in set, the access code included, so this
		// is a 400 rather than a refusal of the caller; it once was a 500.
		if rec := newMatrixFixture(t).as(t, c, http.MethodPost, "/api/api-scope-sets/delete", `{"name":"grain/viewer"}`); rec.code/100 == 2 || rec.code >= 500 {
			t.Errorf("%s deletes a built-in set: answered %d %s, want a 4xx", c.name, rec.code, rec.excerpt())
		}
	}
	mustRefuse(viewer, http.MethodPut, "/api/api-scope-sets", set("matrix-escalate", "tasks:read"), "writes a scope set")

	// Changing an allowlist entry: its own, or anybody's to a role wider
	// than its own.
	own := "user:" + accessAdmin.email()
	role := func(r string) string {
		b, _ := json.Marshal(ui.SetPrincipalRoleRequest{Role: r, Reason: "escalate", ConfirmSelf: true})
		return string(b)
	}
	allow := func(entry, r string) string {
		b, _ := json.Marshal(ui.AllowPrincipalRequest{Entry: entry, Role: r, Reason: "escalate"})
		return string(b)
	}
	mustRefuse(accessAdmin, http.MethodPatch, "/api/access/principals/"+own, role("grain/operator"),
		"gives its own entry grain/operator")
	mustRefuse(accessAdmin, http.MethodPatch, "/api/access/principals/user:"+viewer.email(), role("grain/operator"),
		"gives another entry grain/operator")
	// Re-adding an entry already on the list answers it as it stands
	// ("created": false), so what matters is that its role is unchanged.
	{
		f := newMatrixFixture(t)
		rec := f.as(t, accessAdmin, http.MethodPost, "/api/access/principals", allow(own, "grain/operator"))
		var got struct {
			Principal model.AllowedPrincipal `json:"principal"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &got)
		entries, err := f.store.ListAllowedPrincipals(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Entry == own && e.Role != matrixAccessAdmin {
				t.Errorf("re-adding its own entry as grain/operator (%d %s) left it %s, want %s",
					rec.code, rec.excerpt(), e.Role, matrixAccessAdmin)
			}
		}
		if !refused(rec.code) && got.Principal.Role != matrixAccessAdmin {
			t.Errorf("re-adding its own entry as grain/operator answered %d with role %q", rec.code, got.Principal.Role)
		}
	}
	mustRefuse(accessAdmin, http.MethodPost, "/api/access/principals", allow("user:accomplice@example.com", "grain/operator"),
		"adds an accomplice as grain/operator")
	mustRefuse(accessAdmin, http.MethodPost, "/api/access/principals", allow("group:everyone@example.com", "grain/operator"),
		"adds a group as grain/operator")
	for _, c := range []matrixCaller{viewer, capAdmin, sessions} {
		mustRefuse(c, http.MethodPatch, "/api/access/principals/user:"+c.email(), role("grain/operator"),
			"changes its own entry")
	}

	// Reading the access code or the daemon's LocalAPIToken is covered
	// route by route in TestRouteAuthorizationMatrix (no limited caller is
	// answered a body holding it); the routes that show the code at all
	// must refuse every limited role.
	for _, target := range []string{"/api/auth/access-code", "/api/settings", "/api/deployment", "/api/config"} {
		for _, c := range []matrixCaller{viewer, capAdmin, accessAdmin, sessions, gmaWidgets} {
			f := newMatrixFixture(t)
			if rec := f.as(t, c, http.MethodGet, target, ""); strings.Contains(rec.body.String(), testAccessCode) {
				t.Errorf("GET %s as %s: answered %d with the access code in it", target, c.name, rec.code)
			}
		}
	}

	// Starting a console session -- a terminal into a sandbox running as
	// the session's task -- with grants wider than the role.
	consoleBodies := map[string]string{
		"secrets:write":  `{"id":"matrix-esc","title":"escalate","capabilities":["api:secrets:write"]}`,
		"grain/operator": `{"id":"matrix-esc","title":"escalate","capabilities":["api:grain/operator"]}`,
		"task-admin":     `{"id":"matrix-esc","title":"escalate","capabilities":["api:task-admin"]}`,
		"tasksApproved":  `{"id":"matrix-esc","title":"escalate","tasksApproved":true}`,
		"other repo":     fmt.Sprintf(`{"id":"matrix-esc","title":"escalate","writes":[{"repo":%q}]}`, matrixOtherRepo),
	}
	for what, body := range consoleBodies {
		mustRefuse(sessions, http.MethodPost, "/api/console-sessions", body, "starts a console session with "+what)
	}
	// Network access widens only past a deployment default of "none"
	// (networkAccessWidens), so ask that of one whose default it is.
	{
		f := newMatrixFixture(t)
		ctx := context.Background()
		cfg, err := f.store.GetConfig(ctx)
		if err != nil || cfg == nil {
			t.Fatalf("reading config: %v", err)
		}
		cfg.NetworkAccess = model.NetworkNone
		if err := f.store.PutConfig(ctx, *cfg); err != nil {
			t.Fatal(err)
		}
		if rec := f.as(t, sessions, http.MethodPost, "/api/console-sessions", consoleNetworkOpen); !refused(rec.code) {
			t.Errorf("%s starts a console session with open network on a deployment whose default is none: answered %d %s, want 401 or 403",
				sessions.name, rec.code, rec.excerpt())
		}
	}
	mustRefuse(viewer, http.MethodPost, "/api/console-sessions", `{"id":"matrix-esc","title":"escalate"}`, "starts a console session")
	mustRefuse(viewer, http.MethodPost, "/api/console/x/input", `{"data":"id\n"}`, "types into a console")
}
