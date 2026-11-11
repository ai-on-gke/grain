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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// roleHarness is a server behind IAP whose allowlist the test fills in
// directly, and a way to call it as a verified person.
type roleHarness struct {
	srv    *ui.Server
	client *ui.Client
	as     func(email string) map[string]string
}

func newRoleHarness(t *testing.T) roleHarness {
	t.Helper()
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)
	srv, client := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	return roleHarness{srv: srv, client: client, as: func(email string) map[string]string {
		return map[string]string{
			ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute)),
			"Content-Type":        "application/json",
		}
	}}
}

func (h roleHarness) allow(t *testing.T, entry, role string) {
	t.Helper()
	if _, _, err := h.client.Store.AddAllowedPrincipalWithRole(context.Background(), entry, "test", "test", role, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (h roleHarness) call(email, method, path, body string) (int, string) {
	rec := serveFrom(h.srv, "203.0.113.10:443", method, path, body, h.as(email))
	return rec.Code, rec.Body.String()
}

func (h roleHarness) access(t *testing.T, email string) *ui.AccessRoleInfo {
	t.Helper()
	code, body := h.call(email, http.MethodGet, "/api/auth/user", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/auth/user as %s: %d %s", email, code, body)
	}
	var resp ui.AuthUserResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Access
}

// installScopeSets declares sets for one test and puts back none after.
func installScopeSets(t *testing.T, sets ...model.APIScopeSet) {
	t.Helper()
	if err := model.InstallAPIScopeSets(sets); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
}

func TestViewerRoleReadsButIsRefusedWrites(t *testing.T) {
	h := newRoleHarness(t)
	h.allow(t, "user:viv@example.com", "grain/viewer")
	seedAPITask(t, h.client, "1", "acme/widgets")

	if code, body := h.call("viv@example.com", http.MethodGet, "/api/tasks", ""); code != http.StatusOK {
		t.Fatalf("viewer GET /api/tasks: %d %s", code, body)
	}
	code, body := h.call("viv@example.com", http.MethodPatch, "/api/tasks/1", `{"title":"renamed"}`)
	if code != http.StatusForbidden || !strings.Contains(body, "tasks:write") || !strings.Contains(body, "role grain/viewer") {
		t.Fatalf("viewer PATCH /api/tasks/1: %d %s, want 403 naming tasks:write and the role", code, body)
	}
	if code, body := h.call("viv@example.com", http.MethodPut, "/api/settings", `{}`); code != http.StatusForbidden {
		t.Fatalf("viewer PUT /api/settings: %d %s, want 403", code, body)
	}
	// An acting-task header never widens a person past their role.
	seedAPITask(t, h.client, "2", "acme/widgets", "api:task-admin")
	hdr := h.as("viv@example.com")
	hdr[ui.ActingTaskHeader] = "2"
	if rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPatch, "/api/tasks/1", `{"title":"renamed"}`, hdr); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer PATCH as task 2: %d %s, want 403", rec.Code, rec.Body)
	}
	// Over POST /api/mcp, a viewer can call read tools and is refused write
	// tools with the missing scope named.
	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainListTasks, nil); isErr || !strings.Contains(out, `"id":"1"`) {
		t.Fatalf("viewer grain_list_tasks: isErr=%v out=%s", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": "1", "body": map[string]any{"body": "nope"}},
	}); !isErr || !strings.Contains(out, "tasks:write") || !strings.Contains(out, "role grain/viewer") {
		t.Fatalf("viewer grain_invoke api_add_comment: isErr=%v out=%s, want refusal naming tasks:write and role grain/viewer", isErr, out)
	}

	got := h.access(t, "viv@example.com")
	if got == nil || got.Role != "grain/viewer" || got.Full || !slices.Contains(got.Scopes, "tasks:read") || slices.Contains(got.Scopes, "tasks:write") {
		t.Fatalf("viewer's reported access = %+v", got)
	}
	for _, sc := range got.Scopes {
		if model.IsDangerousAPIScope(sc) || !strings.HasSuffix(string(sc), ":read") {
			t.Errorf("grain/viewer reports %s", sc)
		}
	}
}

// A config change is decided under settings:write, as any other change to
// this deployment's settings is: a viewer is refused both decisions before
// the task is even read, whatever commit they name
// (docs/test-runs/2026-11-09-demo-config-change.md, finding 2).
func TestViewerRoleIsRefusedConfigChangeDecisions(t *testing.T) {
	h := newRoleHarness(t)
	h.allow(t, "user:viv@example.com", "grain/viewer")
	seedAPITask(t, h.client, "1", "acme/widgets")
	for _, verb := range []string{"approve", "reject"} {
		code, body := h.call("viv@example.com", http.MethodPost, "/api/tasks/1/config-change/"+verb, `{"headSha":"abc"}`)
		if code != http.StatusForbidden || !strings.Contains(body, "settings:write") || !strings.Contains(body, "role grain/viewer") {
			t.Errorf("viewer POST config-change/%s: %d %s, want 403 naming settings:write and the role", verb, code, body)
		}
	}
}

func TestRepoLimitedRoleIsRefusedOtherRepositories(t *testing.T) {
	installScopeSets(t, model.APIScopeSet{Name: "acme/widgets-ci", Scopes: []string{"ci:read", "tasks:read"},
		Repos: []string{"acme/widgets"}, Version: 1})
	h := newRoleHarness(t)
	h.allow(t, "user:cy@example.com", "acme/widgets-ci")
	seedAPITask(t, h.client, "1", "acme/widgets")
	seedAPITask(t, h.client, "2", "acme/gadgets")

	if code, body := h.call("cy@example.com", http.MethodGet, "/api/tasks/1", ""); code != http.StatusOK {
		t.Fatalf("repo-limited GET a task of acme/widgets: %d %s", code, body)
	}
	code, body := h.call("cy@example.com", http.MethodGet, "/api/tasks/2", "")
	if code != http.StatusForbidden || !strings.Contains(body, "acme/gadgets") {
		t.Fatalf("repo-limited GET a task of acme/gadgets: %d %s, want 403 naming the repository", code, body)
	}
	if code, body := h.call("cy@example.com", http.MethodGet, "/api/tasks?repo=acme/gadgets", ""); code != http.StatusForbidden {
		t.Fatalf("repo-limited GET /api/tasks?repo=acme/gadgets: %d %s, want 403", code, body)
	}
	if got := h.access(t, "cy@example.com"); got == nil || !slices.Equal(got.RepoLimits["ci:read"], []string{"acme/widgets"}) {
		t.Fatalf("reported access = %+v, want ci:read limited to acme/widgets", got)
	}
}

func TestRolesOfEveryMatchingEntryAreUnioned(t *testing.T) {
	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "builders@example.com" && member == "una@example.com", nil
	})
	h.allow(t, "user:una@example.com", "grain/ci-read")
	h.allow(t, "group:builders@example.com", "grain/contributor")
	h.allow(t, "group:others@example.com", "grain/operator")
	seedAPITask(t, h.client, "1", "acme/widgets")

	got := h.access(t, "una@example.com")
	if got == nil || !slices.Equal(got.Roles, []string{"grain/ci-read", "grain/contributor"}) || got.Full {
		t.Fatalf("reported access = %+v, want the user's and the group's roles", got)
	}
	if code, body := h.call("una@example.com", http.MethodPatch, "/api/tasks/1", `{"title":"renamed"}`); code >= 300 {
		t.Fatalf("tasks:write through the group's role: %d %s", code, body)
	}
	if code, _ := h.call("una@example.com", http.MethodPut, "/api/settings", `{}`); code != http.StatusForbidden {
		t.Fatalf("PUT /api/settings held by neither role: %d, want 403", code)
	}
}

func TestADenyInAnyMatchingRoleWithholdsTheScope(t *testing.T) {
	installScopeSets(t, model.APIScopeSet{Name: "contractors", Includes: []string{"grain/viewer"},
		Deny: []string{"secrets:read"}, Version: 1})
	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "contractors@example.com" && member == "dee@example.com", nil
	})
	h.allow(t, "user:dee@example.com", model.DefaultAccessRole)
	h.allow(t, "group:contractors@example.com", "contractors")

	got := h.access(t, "dee@example.com")
	if got == nil || got.Full || slices.Contains(got.Scopes, "secrets:read") || !slices.Contains(got.Scopes, "settings:write") {
		t.Fatalf("reported access = %+v, want everything grain/operator holds but secrets:read", got)
	}
	if code, body := h.call("dee@example.com", http.MethodGet, "/api/deployment/secret-audit", ""); code != http.StatusForbidden || !strings.Contains(body, "secrets:read") {
		t.Fatalf("GET secret-audit with secrets:read denied by a group's role: %d %s", code, body)
	}
	if code, body := h.call("dee@example.com", http.MethodGet, "/api/tasks", ""); code != http.StatusOK {
		t.Fatalf("GET /api/tasks: %d %s", code, body)
	}
}

func TestAFailedLookupOfAGroupWhoseRoleDeniesRefuses(t *testing.T) {
	installScopeSets(t, model.APIScopeSet{Name: "contractors", Includes: []string{"grain/viewer"},
		Deny: []string{"secrets:read"}, Version: 1})
	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return false, errors.New("cloud identity unavailable")
	})
	h.allow(t, "user:dee@example.com", model.DefaultAccessRole)
	h.allow(t, "group:builders@example.com", "grain/contributor")
	h.allow(t, "user:eve@example.com", model.DefaultAccessRole)

	// A group whose role only grants is left out when its lookup fails:
	// that can only narrow eve, who is admitted directly.
	if code, body := h.call("eve@example.com", http.MethodGet, "/api/tasks", ""); code != http.StatusOK {
		t.Fatalf("GET /api/tasks with a granting group's lookup failing: %d %s", code, body)
	}
	// A group whose role denies is not: leaving its veto out would give
	// dee the secrets:read it withholds, so dee is refused instead.
	h.allow(t, "group:contractors@example.com", "contractors")
	if code, body := h.call("dee@example.com", http.MethodGet, "/api/deployment/secret-audit", ""); code != http.StatusForbidden ||
		!strings.Contains(body, "contractors@example.com") ||
		!strings.Contains(body, "group membership lookup unavailable") ||
		strings.Contains(body, "cloud identity unavailable") {
		t.Fatalf("GET secret-audit with a denying group's lookup failing: %d %s, want generic 403 naming the group without underlying error", code, body)
	}
}

func TestRepoLimitedRoleReadsOnlyItsRepositoriesInTheNamespace(t *testing.T) {
	installScopeSets(t, model.APIScopeSet{Name: "acme/widgets-read", Scopes: []string{"ci:read", "tasks:read", "memory:read"},
		Repos: []string{"acme/widgets"}, Version: 1})
	h := newRoleHarness(t)
	seedAPITask(t, h.client, "7", "acme/widgets")
	seedAPITask(t, h.client, "8", "acme/secret")
	ns := fsTestNamespace(t)
	h.srv.SetNamespace(func() *namespace.Server { return ns })
	h.allow(t, "user:fay@example.com", "acme/widgets-read")

	for _, p := range []struct {
		path, contains, absent string
		want                   int
	}{
		{path: "/api/fs/ci/acme/widgets/run-w/go/Test.log", want: http.StatusOK, contains: "FAIL: TestThing"},
		{path: "/api/fs/ci/acme/secret/run-s/go/Test.log", want: http.StatusForbidden},
		// A listing naming no repository is narrowed to the role's.
		{path: "/api/fs/ci/?list", want: http.StatusOK, contains: "widgets", absent: "secret"},
		{path: "/api/fs/task/7/record.md", want: http.StatusOK, contains: "task 7 in widgets"},
		{path: "/api/fs/task/8/record.md", want: http.StatusForbidden},
		{path: "/api/fs/memory/repos/acme/widgets.md", want: http.StatusOK, contains: "widgets memory"},
		{path: "/api/fs/memory/repos/acme/secret.md", want: http.StatusForbidden},
	} {
		code, body := h.call("fay@example.com", http.MethodGet, p.path, "")
		if code != p.want || (p.contains != "" && !strings.Contains(body, p.contains)) || (p.absent != "" && strings.Contains(body, p.absent)) {
			t.Errorf("GET %s as acme/widgets-read: %d %q, want %d containing %q and not %q", p.path, code, body, p.want, p.contains, p.absent)
		}
	}
}

func TestEntriesFromBeforeRolesKeepFullAccess(t *testing.T) {
	h := newRoleHarness(t)
	// AddAllowedPrincipal is what wrote every entry before roles; the
	// column's default is what migration 110 gives them.
	if _, _, err := h.client.Store.AddAllowedPrincipal(context.Background(), "user:old@example.com", "installer", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := h.access(t, "old@example.com")
	if got == nil || !got.Full || got.Role != model.DefaultAccessRole {
		t.Fatalf("reported access = %+v, want full grain/operator", got)
	}
	if code, body := h.call("old@example.com", http.MethodGet, "/api/deployment/secret-audit", ""); code != http.StatusOK {
		t.Fatalf("an operator GET secret-audit: %d %s", code, body)
	}
	if code, body := h.call("old@example.com", http.MethodPost, "/api/mcp", `{}`); code == http.StatusForbidden && strings.Contains(body, "role") {
		t.Fatalf("an operator POST /api/mcp refused by role: %s", body)
	}
}

func TestAnAdminLiftsADenyOnlyOnOthersAndWithAReason(t *testing.T) {
	installScopeSets(t,
		model.APIScopeSet{Name: "contractors", Includes: []string{"grain/viewer"}, Deny: []string{"tasks:write"}, Version: 1},
		model.APIScopeSet{Name: "access-admins", Includes: []string{"grain/contributor"}, Scopes: []string{"access:write"}, Version: 1})
	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "contractors@example.com" && member == "lead@example.com", nil
	})
	h.allow(t, "user:lead@example.com", "access-admins")
	h.allow(t, "user:ops@example.com", model.DefaultAccessRole)
	h.allow(t, "group:contractors@example.com", "contractors")

	if got := h.access(t, "lead@example.com"); got == nil || slices.Contains(got.Scopes, "tasks:write") {
		t.Fatalf("lead's access = %+v, want tasks:write withheld by the contractors group", got)
	}
	// lead is in the group: lifting its deny would widen lead, so lead
	// may not, reason or none.
	group := "/api/access/principals/" + url.PathEscape("group:contractors@example.com")
	if code, body := h.call("lead@example.com", http.MethodPatch, group, `{"role":"grain/viewer","reason":"done"}`); code != http.StatusForbidden || !strings.Contains(body, "yourself") {
		t.Fatalf("lead PATCH a denying entry matching lead: %d %s, want 403", code, body)
	}
	if code, body := h.call("lead@example.com", http.MethodDelete, group+"?reason=done", ""); code != http.StatusForbidden || !strings.Contains(body, "yourself") {
		t.Fatalf("lead DELETE a denying entry matching lead: %d %s, want 403", code, body)
	}
	// ops is not in it, and holds tasks:write: ops lifts it with a reason.
	if code, body := h.call("ops@example.com", http.MethodPatch, group, `{"role":"grain/viewer"}`); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("ops PATCH a denying entry without a reason: %d %s, want 400", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, group, ""); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("ops DELETE a denying entry without a reason: %d %s, want 400", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, group+"?reason=contract+ended", ""); code != http.StatusOK {
		t.Fatalf("ops DELETE a denying entry with a reason: %d %s", code, body)
	}
	// ... and adds one back on others the same way.
	if code, body := h.call("ops@example.com", http.MethodPost, "/api/access/principals", `{"entry":"group:contractors@example.com","role":"contractors","reason":"new contract"}`); code != http.StatusCreated {
		t.Fatalf("ops POST a denying entry with a reason: %d %s", code, body)
	}
	audits, err := h.client.Store.ConfigAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawRemove bool
	for _, a := range audits {
		if a.Surface == "access" && a.By.ID == "ops@example.com" && a.Summary == "removed group:contractors@example.com (was contractors) (contract ended)" {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Fatalf("config_audit = %+v, want the removal with its old role and reason", audits)
	}
}

// TestAVerifiedAdminChangesAnotherAdmin is the operator signed in through
// a verifier changing another admin's role (docs/design/access-rbac.md
// §3): allowed and audited with a reason, refused without one, refused
// when it widens the caller's own entry, when it leaves no full admin, when
// the caller's own role is narrower, and to an agent.
func TestAVerifiedAdminChangesAnotherAdmin(t *testing.T) {
	installScopeSets(t, model.APIScopeSet{Name: "access-admins", Includes: []string{"grain/contributor"},
		Scopes: []string{"access:write"}, Version: 1})
	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "admins@example.com" && (member == "ops@example.com" || member == "temp@example.com"), nil
	})
	h.allow(t, "user:ops@example.com", model.DefaultAccessRole)
	h.allow(t, "user:other@example.com", model.DefaultAccessRole)
	h.allow(t, "user:lead@example.com", "access-admins")
	path := func(entry string) string { return "/api/access/principals/" + url.PathEscape(entry) }

	// Another admin: needs a reason, then is changed and audited.
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"), `{"role":"grain/viewer"}`); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("ops PATCH another admin without a reason: %d %s, want 400", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"), `{"role":"grain/viewer","reason":"left the on-call rota"}`); code != http.StatusOK {
		t.Fatalf("ops PATCH another admin with a reason: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"), `{"role":"grain/operator","reason":"back on call"}`); code != http.StatusOK {
		t.Fatalf("ops PATCH another entry up to grain/operator: %d %s", code, body)
	}
	audits, err := h.client.Store.ConfigAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawDown, sawUp bool
	for _, a := range audits {
		if a.Surface != "access" || a.By.Kind != model.PrincipalHuman || a.By.ID != "ops@example.com" || a.At.IsZero() {
			continue
		}
		sawDown = sawDown || a.Summary == "role user:other@example.com grain/operator -> grain/viewer (left the on-call rota)"
		sawUp = sawUp || a.Summary == "role user:other@example.com grain/viewer -> grain/operator (back on call)"
	}
	if !sawDown || !sawUp {
		t.Fatalf("config_audit = %+v, want both changes by ops with old and new role and reason", audits)
	}
	// Settings -> Access reads the same trail, at access:read.
	if code, body := h.call("lead@example.com", http.MethodGet, "/api/access/history", ""); code != http.StatusOK || !strings.Contains(body, `grain/operator -\u003e grain/viewer (left the on-call rota)`) || !strings.Contains(body, `"actor":"ops@example.com"`) {
		t.Fatalf("GET /api/access/history: %d %s, want the change by ops", code, body)
	}

	// A narrower caller neither changes an admin nor gives the role.
	if code, body := h.call("lead@example.com", http.MethodPatch, path("user:other@example.com"), `{"role":"grain/viewer","reason":"x"}`); code != http.StatusForbidden || !strings.Contains(body, "wider than") {
		t.Fatalf("lead PATCH an admin: %d %s, want 403", code, body)
	}
	if code, body := h.call("lead@example.com", http.MethodPost, "/api/access/principals", `{"entry":"user:new@example.com","reason":"x"}`); code != http.StatusForbidden || !strings.Contains(body, "wider than") {
		t.Fatalf("lead POST at grain/operator: %d %s, want 403", code, body)
	}

	// Self: widening or adding an entry matching the caller is refused,
	// even a group the caller is in (closing DELETE?confirmSelf=1 + POST
	// and group-to-user persistence); narrowing it asks for confirmation.
	h.allow(t, "group:admins@example.com", "grain/viewer")
	if code, body := h.call("ops@example.com", http.MethodPatch, path("group:admins@example.com"), `{"role":"grain/contributor","reason":"x"}`); code != http.StatusForbidden || !strings.Contains(body, "self_escalation") {
		t.Fatalf("ops PATCH its own group wider: %d %s, want 403 self_escalation", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, path("group:admins@example.com"), ""); code != http.StatusForbidden || !strings.Contains(body, "confirm_self") {
		t.Fatalf("ops DELETE its own group unconfirmed: %d %s, want 403 confirm_self", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, path("group:admins@example.com")+"?confirmSelf=1", ""); code != http.StatusOK {
		t.Fatalf("ops DELETE its own group confirmed: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPost, "/api/access/principals", `{"entry":"group:admins@example.com","role":"grain/contributor"}`); code != http.StatusForbidden || !strings.Contains(body, "self_escalation") {
		t.Fatalf("ops POST its own group after DELETE: %d %s, want 403 self_escalation", code, body)
	}
	// A person admitted only through a group cannot entrench a direct user:
	// entry (or another matching group) for themselves.
	h.allow(t, "group:admins@example.com", model.DefaultAccessRole)
	if code, body := h.call("temp@example.com", http.MethodPost, "/api/access/principals", `{"entry":"user:temp@example.com","role":"grain/operator","reason":"persist"}`); code != http.StatusForbidden || !strings.Contains(body, "self_escalation") {
		t.Fatalf("group admin POST its own user entry: %d %s, want 403 self_escalation", code, body)
	}
	if code, body := h.call("other@example.com", http.MethodDelete, path("group:admins@example.com")+"?reason=done", ""); code != http.StatusOK {
		t.Fatalf("other DELETE admins group: %d %s", code, body)
	}

	// Newlines, ANSI CSI/OSC escapes and control/bidi characters in a reason
	// are stripped so config_audit and daemon logs stay one clean line, and a
	// pure-escape reason is refused as empty.
	var logBuf bytes.Buffer
	prevLogOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevLogOut) })

	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"),
		`{"role":"grain/viewer","reason":"line1\r\nforged\u001b[31mRED\u001b[0m\u001b]52;c;dGVzdA==\u0007\u202eend"}`); code != http.StatusOK {
		t.Fatalf("ops PATCH with newline/ANSI reason: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"),
		`{"role":"grain/operator","reason":"\u001b[31m\u001b[0m\r\n"}`); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("ops PATCH with pure-ANSI reason: %d %s, want 400 naming reason", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:other@example.com"), `{"role":"grain/operator","reason":"restore"}`); code != http.StatusOK {
		t.Fatalf("ops PATCH restore other: %d %s", code, body)
	}
	auditsAfterNL, err := h.client.Store.ConfigAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range auditsAfterNL {
		if strings.ContainsAny(a.Summary, "\r\n\x1b\x07\u202e") {
			t.Fatalf("config_audit summary contains raw newline or terminal escape: %q", a.Summary)
		}
	}
	if strings.ContainsAny(logBuf.String(), "\x1b\x07\u202e") {
		t.Fatalf("daemon log contains terminal escape or bidi control: %q", logBuf.String())
	}

	// The last full admin is never removed or narrowed by a session, and
	// receives last_admin directly even when confirmSelf is omitted.
	if code, body := h.call("ops@example.com", http.MethodDelete, path("user:other@example.com")+"?reason=offboarded", ""); code != http.StatusOK {
		t.Fatalf("ops DELETE another admin: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:ops@example.com"), `{"role":"grain/viewer","reason":"x"}`); code != http.StatusForbidden || !strings.Contains(body, "last_admin") || !strings.Contains(body, "access code") {
		t.Fatalf("ops PATCH the last admin narrower without confirmSelf: %d %s, want 403 last_admin naming the access code", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, path("user:ops@example.com"), `{"role":"grain/viewer","reason":"x","confirmSelf":true}`); code != http.StatusForbidden || !strings.Contains(body, "last_admin") || !strings.Contains(body, "access code") {
		t.Fatalf("ops PATCH the last admin narrower: %d %s, want 403 last_admin naming the access code", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, path("user:ops@example.com")+"?reason=x", ""); code != http.StatusForbidden || !strings.Contains(body, "last_admin") {
		t.Fatalf("ops DELETE the last admin without confirmSelf: %d %s, want 403 last_admin", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, path("user:ops@example.com")+"?reason=x&confirmSelf=1", ""); code != http.StatusForbidden || !strings.Contains(body, "last_admin") {
		t.Fatalf("ops DELETE the last admin: %d %s, want 403 last_admin", code, body)
	}

	// An agent, even with a person-granted access:write and a reason,
	// changes nothing -- including an idempotent POST on an existing entry.
	seedAPITask(t, h.client, "9", "acme/widgets", "api:access:write")
	if code, body := asAgent(t, h.srv, "9", http.MethodPost, "/api/access/principals", `{"entry":"user:lead@example.com"}`, "probe"); code != http.StatusForbidden {
		t.Fatalf("agent POST existing entry: %d %s, want 403", code, body)
	}
	if code, body := asAgent(t, h.srv, "9", http.MethodPatch, path("user:lead@example.com"), `{"role":"grain/viewer","reason":"x"}`, "rotation"); code != http.StatusForbidden {
		t.Fatalf("agent PATCH: %d %s, want 403", code, body)
	}
}

func TestRoleAssignmentRules(t *testing.T) {
	h := newRoleHarness(t)
	h.allow(t, "user:ops@example.com", model.DefaultAccessRole)
	installScopeSets(t, model.APIScopeSet{Name: "access-admins", Includes: []string{"grain/contributor"},
		Scopes: []string{"access:write"}, Version: 1})
	h.allow(t, "user:lead@example.com", "access-admins")

	bearer := map[string]string{"Authorization": "Bearer " + testAccessCode, "Content-Type": "application/json"}
	viaCode := func(method, path, body string) (int, string) {
		rec := serveFrom(h.srv, "127.0.0.1:4242", method, path, body, bearer)
		return rec.Code, rec.Body.String()
	}
	entry := "/api/access/principals/" + url.PathEscape("user:new@example.com")

	// The access code assigns a dangerous role only with a reason.
	if code, body := viaCode(http.MethodPost, "/api/access/principals", `{"entry":"user:new@example.com"}`); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("access code POST at grain/operator without a reason: %d %s", code, body)
	}
	if code, body := viaCode(http.MethodPost, "/api/access/principals", `{"entry":"user:new@example.com","role":"grain/viewer"}`); code != http.StatusCreated {
		t.Fatalf("access code POST at grain/viewer: %d %s", code, body)
	}
	if code, body := viaCode(http.MethodPatch, entry, `{"role":"no/such-set","reason":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("PATCH to a role naming no set: %d %s", code, body)
	}
	// A verified operator gives a role holding a dangerous scope only
	// with a reason.
	if code, body := h.call("ops@example.com", http.MethodPatch, entry, `{"role":"grain/operator"}`); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("verified operator PATCH to grain/operator without a reason: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, entry, `{"role":"grain/operator","reason":"promote"}`); code != http.StatusOK {
		t.Fatalf("verified operator PATCH to grain/operator: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, entry, `{"role":"grain/contributor","reason":"demote"}`); code != http.StatusOK {
		t.Fatalf("verified operator PATCH to grain/contributor: %d %s", code, body)
	}
	// access-admins holds access:write, so it is a person-only role
	// itself and gives nothing wider than its own.
	if code, body := h.call("lead@example.com", http.MethodPatch, entry, `{"role":"grain/viewer"}`); code != http.StatusOK {
		t.Fatalf("access-admins PATCH to grain/viewer: %d %s", code, body)
	}
	installScopeSets(t,
		model.APIScopeSet{Name: "access-admins", Includes: []string{"grain/contributor"}, Scopes: []string{"access:write"}, Version: 1},
		model.APIScopeSet{Name: "releasers", Includes: []string{"grain/viewer"}, Scopes: []string{"releases:write"}, Version: 1})
	if code, body := h.call("lead@example.com", http.MethodPatch, entry, `{"role":"releasers"}`); code != http.StatusForbidden || !strings.Contains(body, "releases:write") {
		t.Fatalf("access-admins PATCH to a wider role: %d %s", code, body)
	}
	if code, body := viaCode(http.MethodPatch, entry, `{"role":"grain/operator","reason":"on-call"}`); code != http.StatusOK {
		t.Fatalf("access code PATCH to grain/operator with a reason: %d %s", code, body)
	}
	// An entry at a dangerous role a verified session removes only with
	// a reason, and only when its own role is no narrower.
	if code, body := h.call("lead@example.com", http.MethodDelete, entry+"?reason=x", ""); code != http.StatusForbidden {
		t.Fatalf("access-admins DELETE an operator entry: %d %s, want 403", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodDelete, entry, ""); code != http.StatusBadRequest {
		t.Fatalf("verified operator DELETE an operator entry without a reason: %d %s, want 400", code, body)
	}

	audits, err := h.client.Store.ConfigAudit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var sawRole bool
	for _, a := range audits {
		if a.Surface == "access" && a.Summary == "role user:new@example.com grain/viewer -> grain/operator (on-call)" {
			sawRole = true
		}
	}
	if !sawRole {
		t.Fatalf("config_audit = %+v, want the role change with its reason", audits)
	}
}

func TestContributorRoleCannotEscalateThroughFiling(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name:    "widgets-cap-admin",
			Scopes:  []string{"tasks:read", "tasks:write", "ci:read", "capabilities:admin"},
			Repos:   []string{"acme/widgets"},
			Version: 1,
		},
		model.APIScopeSet{
			Name:           "person-ci-reader",
			Scopes:         []string{"ci:read"},
			RequiresPerson: true,
			Version:        1,
		},
	)

	h := newRoleHarness(t)
	if err := h.client.Store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.RepoRef{Owner: "acme", Name: "other"}}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, h.client, "7", "acme/widgets")
	seedAPITask(t, h.client, "8", "acme/other")
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     "tmpl-priv",
		Name:   "privileged",
		Title:  "privileged",
		Grants: []model.Grant{{Capability: "api:secrets:write"}},
	}); err != nil {
		t.Fatal(err)
	}

	h.allow(t, "user:con@example.com", "grain/contributor")
	h.allow(t, "user:ops@example.com", model.DefaultAccessRole)
	h.allow(t, "user:cap@example.com", "widgets-cap-admin")

	if got := h.access(t, "con@example.com"); got == nil || got.Full {
		t.Fatalf("contributor access = %+v, want full=false", got)
	}

	const escalateCreate = `{"repo":"acme/widgets","title":"escalate","body":"x",` +
		`"capabilities":["api:settings:write","api:secrets:write","api:access:write","api:grain/operator"],` +
		`"autoMerge":true,"approved":true,"writes":[{"repo":"acme/other"}]}`
	if code, body := h.call("con@example.com", http.MethodPost, "/api/tasks", escalateCreate); code != http.StatusForbidden ||
		!strings.Contains(body, "grain/contributor") || !strings.Contains(body, "capabilities") {
		t.Fatalf("contributor POST escalating task = %d %s, want 403 naming grain/contributor and capabilities", code, body)
	}
	tasks, err := h.client.Store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.Title == "escalate" {
			t.Fatalf("escalating task was stored despite 403: %+v", tk)
		}
	}

	const escalatePatch = `{"capabilities":["api:grain/operator","api:secrets:write"],"autoMerge":true}`
	if code, body := h.call("con@example.com", http.MethodPatch, "/api/tasks/7", escalatePatch); code != http.StatusForbidden ||
		!strings.Contains(body, "grain/contributor") || !strings.Contains(body, "capabilities") {
		t.Fatalf("contributor PATCH escalating task = %d %s, want 403 naming grain/contributor and capabilities", code, body)
	}
	// First refused key follows wire order when autoMerge precedes capabilities.
	if code, body := h.call("con@example.com", http.MethodPatch, "/api/tasks/7", `{"autoMerge":true,"capabilities":["api:ci:read"]}`); code != http.StatusForbidden ||
		!strings.Contains(body, "grain/contributor") || !strings.Contains(body, "autoMerge") {
		t.Fatalf("contributor PATCH autoMerge-first = %d %s, want 403 naming grain/contributor and autoMerge", code, body)
	}
	stored7, err := h.client.Store.GetTask(ctx, "7")
	if err != nil || stored7 == nil {
		t.Fatalf("GetTask(7): %v %+v", err, stored7)
	}
	if len(stored7.Grants) != 0 || stored7.AutoMerge {
		t.Fatalf("stored task 7 changed after refused PATCH: grants=%+v autoMerge=%v", stored7.Grants, stored7.AutoMerge)
	}

	// Individual escalating keys on filing and submit are each refused naming the role and key.
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		wantKey string
	}{
		{"autoMerge", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","autoMerge":true}`, "autoMerge"},
		{"approved", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","approved":true}`, "approved"},
		{"interactive", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","interactive":true}`, "interactive"},
		{"autoApproveProposals", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","autoApproveProposals":true}`, "autoApproveProposals"},
		{"keepRunning", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","keepRunning":true}`, "keepRunning"},
		{"priority", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","priority":10}`, "priority"},
		{"fixesBaseBranch", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","fixesBaseBranch":true}`, "fixesBaseBranch"},
		{"author", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","author":"ops@example.com"}`, "author"},
		{"creator", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","creator":"ops@example.com"}`, "creator"},
		{"personScopeSet", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","capabilities":["api:person-ci-reader"]}`, "capabilities"},
		{"templateId", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","templateId":"tmpl-priv"}`, "templateId"},
		{"submit", http.MethodPost, "/api/tasks/7/submit", `{}`, "autoMerge"},
		{"prSubmit", http.MethodPost, "/api/pull-requests/submit", `{"pr":"acme/widgets#7"}`, "autoMerge"},
	} {
		code, body := h.call("con@example.com", tc.method, tc.path, tc.body)
		if code != http.StatusForbidden || !strings.Contains(body, "grain/contributor") || !strings.Contains(body, tc.wantKey) {
			t.Errorf("%s: got %d %s, want 403 naming grain/contributor and %s", tc.name, code, body, tc.wantKey)
		}
	}

	// The same POST with no escalating key succeeds unapproved.
	code, createdBody := h.call("con@example.com", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"plain","body":"x"}`)
	if code != http.StatusCreated {
		t.Fatalf("contributor plain POST /api/tasks = %d %s, want 201", code, createdBody)
	}
	var plainTask ui.Task
	if err := json.Unmarshal([]byte(createdBody), &plainTask); err != nil {
		t.Fatal(err)
	}
	if plainTask.State != "proposed" || plainTask.AutoMerge {
		t.Fatalf("contributor plain task = state %q autoMerge %v, want proposed and false", plainTask.State, plainTask.AutoMerge)
	}

	// Custom role holding capabilities:admin@acme/widgets may add a non-dangerous grant
	// it holds on acme/widgets, and is refused one it does not hold, a dangerous/Person
	// grant, or writes/reads outside acme/widgets.
	if code, body := h.call("cap@example.com", http.MethodPatch, "/api/tasks/7", `{"capabilities":["api:ci:read@acme/widgets"]}`); code != http.StatusOK {
		t.Fatalf("widgets-cap-admin PATCH held grant = %d %s, want 200", code, body)
	}
	if code, body := h.call("cap@example.com", http.MethodPost, "/api/tasks/7/capabilities", `{"id":"api:tasks:read@acme/widgets","attach":true}`); code != http.StatusOK {
		t.Fatalf("widgets-cap-admin POST capabilities held grant = %d %s, want 200", code, body)
	}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		wantKey string
	}{
		{"unheldScope", http.MethodPatch, "/api/tasks/7", `{"capabilities":["api:releases:write"]}`, "capabilities"},
		{"widerRepoGrant", http.MethodPatch, "/api/tasks/7", `{"capabilities":["api:ci:read"]}`, "capabilities"},
		{"dangerousSelfAdmin", http.MethodPatch, "/api/tasks/7", `{"capabilities":["api:capabilities:admin@acme/widgets"]}`, "capabilities"},
		{"personSetOnCapAdmin", http.MethodPatch, "/api/tasks/7", `{"capabilities":["api:person-ci-reader@acme/widgets"]}`, "capabilities"},
		{"unheldPostCap", http.MethodPost, "/api/tasks/7/capabilities", `{"id":"api:releases:write","attach":true}`, "id"},
		{"writesOutsideRole", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","writes":[{"repo":"acme/other"}]}`, "writes"},
		{"readsOutsideRole", http.MethodPost, "/api/tasks", `{"repo":"acme/widgets","title":"x","reads":["acme/other"]}`, "reads"},
	} {
		code, body := h.call("cap@example.com", tc.method, tc.path, tc.body)
		if code != http.StatusForbidden || !strings.Contains(body, "widgets-cap-admin") || !strings.Contains(body, tc.wantKey) {
			t.Errorf("widgets-cap-admin %s: got %d %s, want 403 naming widgets-cap-admin and %s", tc.name, code, body, tc.wantKey)
		}
	}

	// Unlimited sessions -- both grain/operator and the access code -- keep 201/200 unchanged.
	seedAPITask(t, h.client, "9", "acme/widgets")
	if code, body := h.call("ops@example.com", http.MethodPost, "/api/tasks", escalateCreate); code != http.StatusCreated {
		t.Fatalf("grain/operator POST escalating task = %d %s, want 201", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, "/api/tasks/9", escalatePatch); code != http.StatusOK {
		t.Fatalf("grain/operator PATCH escalating task = %d %s, want 200", code, body)
	}
	seedAPITask(t, h.client, "10", "acme/widgets")
	bearer := map[string]string{"Authorization": "Bearer " + testAccessCode, "Content-Type": "application/json"}
	if rec := serveFrom(h.srv, "127.0.0.1:4242", http.MethodPost, "/api/tasks", escalateCreate, bearer); rec.Code != http.StatusCreated {
		t.Fatalf("access code POST escalating task = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if rec := serveFrom(h.srv, "127.0.0.1:4242", http.MethodPatch, "/api/tasks/10", escalatePatch, bearer); rec.Code != http.StatusOK {
		t.Fatalf("access code PATCH escalating task = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestStandingWorkAndFromTemplateHoldLimitedRolesToTheirScopes(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name:     "standing-writer",
			Includes: []string{"grain/contributor"},
			Scopes:   []string{"tasks:from-template", "templates:write", "schedules:write", "suites:write"},
			Version:  1,
		},
		model.APIScopeSet{
			Name:     "no-secrets",
			Includes: []string{"grain/viewer"},
			Deny:     []string{"secrets:read"},
			Version:  1,
		},
	)

	h := newRoleHarness(t)
	h.client.Config.GroupChecker = groupCheckerFunc(func(_ context.Context, group, member string) (bool, error) {
		return group == "no-secrets@example.com" && member == "vetoed@example.com", nil
	})
	h.allow(t, "user:sw@example.com", "standing-writer")
	h.allow(t, "user:vetoed@example.com", model.DefaultAccessRole)
	h.allow(t, "group:no-secrets@example.com", "no-secrets")

	declaredID := model.RepoDeclaredID(model.DeclaredTemplate, model.RepoRef{Owner: "acme", Name: "widgets"}, "deploy")
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     declaredID,
		Name:   "deploy",
		Title:  "deploy",
		Grants: []model.Grant{{Capability: "api:secrets:write"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     "tmpl-priv",
		Name:   "priv",
		Title:  "priv",
		Grants: []model.Grant{{Capability: "api:grain/operator"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-priv",
		Title:      "priv",
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Grants:     []model.Grant{{Capability: "api:grain/operator"}},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
	}); err != nil {
		t.Fatal(err)
	}

	// Admission above standard replaced the priority-dispatch and durable
	// grants, which a limited role could not hand out: a template or
	// schedule carrying the setting is refused it the same way.
	bypassID := model.RepoDeclaredID(model.DeclaredTemplate, model.RepoRef{Owner: "acme", Name: "widgets"}, "urgent")
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:        bypassID,
		Name:      "urgent",
		Title:     "urgent",
		Admission: model.AdmissionBypass,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-durable",
		Title:      "durable",
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Admission:  model.AdmissionDurable,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    string
		wantKey string
	}{
		{"fromTemplateBypassAdmission", http.MethodPost, "/api/tasks/from-template", `{"repo":"acme/widgets","template":"urgent"}`, model.PriorityDispatchCapability},
		{"runDurableSchedule", http.MethodPost, "/api/schedules/sched-durable/run", `{}`, model.DurableCapability},
		{"createTemplateAdmission", http.MethodPost, "/api/templates", `{"name":"t","title":"t","admission":"durable"}`, "admission"},
		{"fromTemplateApproved", http.MethodPost, "/api/tasks/from-template", `{"repo":"acme/widgets","template":"deploy","approved":true}`, "template"},
		{"fromTemplatePrivGrant", http.MethodPost, "/api/tasks/from-template", `{"repo":"acme/widgets","template":"deploy"}`, "template"},
		{"createTemplateAutoMerge", http.MethodPost, "/api/templates", `{"name":"t","title":"t","autoMerge":true}`, "autoMerge"},
		{"createTemplateGrant", http.MethodPost, "/api/templates", `{"name":"t","title":"t","capabilities":["api:settings:write"]}`, "capabilities"},
		{"patchPrivTemplatePrompt", http.MethodPatch, "/api/templates/tmpl-priv", `{"description":"hijack"}`, "template"},
		{"createScheduleGrant", http.MethodPost, "/api/schedules", `{"title":"s","repo":"acme/widgets","capabilities":["api:secrets:write"],"recurrence":{"kind":"everyNHours","everyNHours":24}}`, "capabilities"},
		{"runPrivSchedule", http.MethodPost, "/api/schedules/sched-priv/run", `{}`, "capabilities"},
		{"createSuiteImplicitAutoMerge", http.MethodPost, "/api/suites", `{"name":"s","mode":"count","count":1}`, "autoMerge"},
	} {
		code, body := h.call("sw@example.com", tc.method, tc.path, tc.body)
		if code != http.StatusForbidden || !strings.Contains(body, "standing-writer") || !strings.Contains(body, tc.wantKey) {
			t.Errorf("%s: got %d %s, want 403 naming standing-writer and %s", tc.name, code, body, tc.wantKey)
		}
	}

	// A deny vetoing secrets:read also prevents handing api:secrets:read to a task.
	if code, body := h.call("vetoed@example.com", http.MethodPost, "/api/tasks",
		`{"repo":"acme/widgets","title":"leak","capabilities":["api:secrets:read"]}`); code != http.StatusForbidden || !strings.Contains(body, "capabilities") {
		t.Fatalf("vetoed role granting denied scope = %d %s, want 403 naming capabilities", code, body)
	}
}

func TestMCPPerToolRoleChecks(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name:    "widgets-contrib",
			Scopes:  []string{"tasks:read", "tasks:write", "templates:read"},
			Repos:   []string{"acme/widgets"},
			Version: 1,
		},
		model.APIScopeSet{
			Name:    "ci-only",
			Scopes:  []string{"ci:read"},
			Version: 1,
		},
	)

	h := newRoleHarness(t)
	h.allow(t, "user:viv@example.com", "grain/viewer")
	h.allow(t, "user:connie@example.com", "grain/contributor")
	h.allow(t, "user:walt@example.com", "widgets-contrib")
	h.allow(t, "user:ned@example.com", "ci-only")

	seedAPITask(t, h.client, "1", "acme/widgets")
	seedAPITask(t, h.client, "2", "acme/gadgets")
	seedAPITask(t, h.client, "3", "acme/widgets", "api:task-admin")

	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     "tmpl-widgets",
		Name:   "widgets-tmpl",
		Title:  "widgets",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     "tmpl-gadgets",
		Name:   "gadgets-tmpl",
		Title:  "gadgets",
		Target: &model.RepoRef{Owner: "acme", Name: "gadgets"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. grain/viewer on POST /api/mcp: read tools succeed, write/admin tools
	// are omitted from discovery and refused on invocation naming the missing scope.
	exploreOut, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainExploreScope, map[string]any{
		"scope": "all",
		"view":  "index",
	})
	if isErr {
		t.Fatalf("viewer grain_explore_scope: %s", exploreOut)
	}
	if !strings.Contains(exploreOut, "api_list_tasks") || !strings.Contains(exploreOut, "api_get_settings") {
		t.Fatalf("viewer explore missing read tools: %s", exploreOut)
	}
	for _, writeTool := range []string{"api_add_comment", "api_create_task", "api_update_settings", "api_approve", "api_delete_board"} {
		if strings.Contains(exploreOut, writeTool) {
			t.Errorf("viewer explore leaked unauthorized tool %q: %s", writeTool, exploreOut)
		}
	}

	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainGetTask, map[string]any{"id": "1"}); isErr || !strings.Contains(out, "Task 1") {
		t.Fatalf("viewer grain_get_task: isErr=%v out=%s", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainFilingContext, nil); isErr || !strings.Contains(out, "templates") {
		t.Fatalf("viewer grain_filing_context: isErr=%v out=%s", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainInvoke, map[string]any{
		"tool": "api_get_settings",
	}); isErr {
		t.Fatalf("viewer grain_invoke api_get_settings: %s", out)
	}

	if out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainSubmitTask, map[string]any{
		"title": "attempt write",
		"repo":  "acme/widgets",
	}); !isErr || !strings.Contains(out, "tasks:write") || !strings.Contains(out, "role grain/viewer") {
		t.Fatalf("viewer grain_submit_task: isErr=%v out=%s, want refusal naming tasks:write and role grain/viewer", isErr, out)
	}

	for _, tc := range []struct {
		tool      string
		args      map[string]any
		wantScope string
	}{
		{
			tool: "api_add_comment",
			args: map[string]any{
				"id":     "1",
				"body":   map[string]any{"body": "hello"},
				"expect": map[string]any{"state": "closed"},
			},
			wantScope: "tasks:write",
		},
		{
			tool:      "api_update_settings",
			args:      map[string]any{"body": map[string]any{}},
			wantScope: "settings:write",
		},
		{
			tool:      "api_approve",
			args:      map[string]any{"id": "1"},
			wantScope: "tasks:admin",
		},
		{
			tool:      "api_delete_board",
			args:      map[string]any{"id": "b1"},
			wantScope: "boards:write",
		},
	} {
		out, isErr := callControllerTool(t, h.srv, h.as("viv@example.com"), ui.ToolGrainInvoke, map[string]any{
			"tool":      tc.tool,
			"arguments": tc.args,
		})
		if !isErr || !strings.Contains(out, tc.wantScope) || !strings.Contains(out, "role grain/viewer") {
			t.Errorf("viewer grain_invoke %s: isErr=%v out=%s, want refusal naming %s and role grain/viewer", tc.tool, isErr, out, tc.wantScope)
		}
		if strings.Contains(out, "expect:") {
			t.Errorf("viewer grain_invoke %s leaked expect check before role check: %s", tc.tool, out)
		}
	}

	// X-Grain-Acting-Task cannot widen a viewer's role on POST /api/mcp.
	actingHdr := h.as("viv@example.com")
	actingHdr[ui.ActingTaskHeader] = "3"
	if out, isErr := callControllerTool(t, h.srv, actingHdr, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": "1", "body": map[string]any{"body": "escalate"}},
	}); !isErr || !strings.Contains(out, "tasks:write") || !strings.Contains(out, "role grain/viewer") {
		t.Fatalf("viewer + acting task 3 api_add_comment: isErr=%v out=%s", isErr, out)
	}

	// 2. Repo-limited and contributor escalations over POST /api/mcp.
	if out, isErr := callControllerTool(t, h.srv, h.as("walt@example.com"), ui.ToolGrainGetTask, map[string]any{"id": "1"}); isErr {
		t.Fatalf("widgets-contrib grain_get_task(1): %s", out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("walt@example.com"), ui.ToolGrainGetTask, map[string]any{"id": "2"}); !isErr || !strings.Contains(out, "acme/gadgets") {
		t.Fatalf("widgets-contrib grain_get_task(2): isErr=%v out=%s, want 403 naming acme/gadgets", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("walt@example.com"), ui.ToolGrainInvoke, map[string]any{
		"tool": "api_add_comment",
		"arguments": map[string]any{
			"id":     "2",
			"body":   map[string]any{"body": "wrong repo"},
			"expect": map[string]any{"state": "closed"},
		},
	}); !isErr || !strings.Contains(out, "acme/gadgets") || strings.Contains(out, "expect:") {
		t.Fatalf("widgets-contrib api_add_comment on acme/gadgets with expect: isErr=%v out=%s", isErr, out)
	}
	fcOut, isErr := callControllerTool(t, h.srv, h.as("walt@example.com"), ui.ToolGrainFilingContext, nil)
	if isErr {
		t.Fatalf("widgets-contrib grain_filing_context: %s", fcOut)
	}
	if !strings.Contains(fcOut, "tmpl-widgets") || strings.Contains(fcOut, "tmpl-gadgets") || strings.Contains(fcOut, `"boards"`) || strings.Contains(fcOut, `"capabilities"`) {
		t.Fatalf("widgets-contrib grain_filing_context leaked out-of-scope sections/repos: %s", fcOut)
	}

	if out, isErr := callControllerTool(t, h.srv, h.as("ned@example.com"), ui.ToolGrainFilingContext, nil); !isErr || !strings.Contains(out, "tasks:read") || !strings.Contains(out, "role ci-only") {
		t.Fatalf("ci-only grain_filing_context: isErr=%v out=%s, want refusal naming tasks:read and role ci-only", isErr, out)
	}

	if out, isErr := callControllerTool(t, h.srv, h.as("connie@example.com"), ui.ToolGrainSubmitTask, map[string]any{
		"title":   "approved task",
		"repo":    "acme/widgets",
		"approve": true,
	}); !isErr || !strings.Contains(out, "tasks:admin") {
		t.Fatalf("contributor grain_submit_task(approve=true): isErr=%v out=%s, want tasks:admin refusal", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("connie@example.com"), ui.ToolGrainSubmitTask, map[string]any{
		"title":   "proposed task",
		"repo":    "acme/widgets",
		"approve": false,
	}); isErr || !strings.Contains(out, `"approved":false`) {
		t.Fatalf("contributor grain_submit_task(approve=false): isErr=%v out=%s", isErr, out)
	}

	// 3. POST /mcp behind IAP with a viewer session and a wider gma_ token:
	// tools/list filters out write tools, read calls succeed, write calls fail
	// naming the missing scope and role.
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:operator@example.com", "grain/operator") // behind IAP a token works only while its owner is listed
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := h.client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "contrib-bot",
		ScopeSet: "grain/contributor",
		Owner:    "operator@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpHeaders := h.as("viv@example.com")
	mcpHeaders["Authorization"] = "Bearer " + issued.Token

	listRec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, mcpHeaders)
	if listRec.Code != http.StatusOK {
		t.Fatalf("viewer POST /mcp tools/list: %d %s", listRec.Code, listRec.Body.String())
	}
	listBody := listRec.Body.String()
	if !strings.Contains(listBody, "api_list_tasks") || strings.Contains(listBody, "api_add_comment") {
		t.Fatalf("viewer POST /mcp tools/list = %s, want api_list_tasks without api_add_comment", listBody)
	}

	readRec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"api_list_tasks","arguments":{}}}`, mcpHeaders)
	if readRec.Code != http.StatusOK || strings.Contains(readRec.Body.String(), `"isError":true`) || !strings.Contains(readRec.Body.String(), `\"id\":\"2\"`) {
		t.Fatalf("viewer POST /mcp api_list_tasks: %d %s", readRec.Code, readRec.Body.String())
	}

	writeRec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"api_add_comment","arguments":{"id":"1","body":{"body":"nope"}}}}`, mcpHeaders)
	if writeRec.Code != http.StatusOK || !strings.Contains(writeRec.Body.String(), `"isError":true`) ||
		!strings.Contains(writeRec.Body.String(), "tasks:write") || !strings.Contains(writeRec.Body.String(), "role grain/viewer") {
		t.Fatalf("viewer POST /mcp api_add_comment: %d %s, want tool error naming tasks:write and role grain/viewer", writeRec.Code, writeRec.Body.String())
	}
}

// TestExternalTokenCannotExceedItsOwnersCurrentRole: a gma_ token issued
// while its owner held grain/operator is refused once the owner is moved to
// grain/viewer, on REST and both MCP endpoints, and works again within the
// owner's role (docs/test-runs/2026-11-07-rbac-roles.md F2).
func TestExternalTokenCannotExceedItsOwnersCurrentRole(t *testing.T) {
	ctx := context.Background()
	h := newRoleHarness(t)
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:olivia@example.com", model.DefaultAccessRole)
	issued, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "olivia-laptop", ScopeSet: model.DefaultAccessRole, Owner: "olivia@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + issued.Token, "Content-Type": "application/json"}
	file := func(path string) int {
		body := `{"title":"gma filing","description":"x","noRepo":true}`
		if path != "/api/tasks" {
			body = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		}
		return serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, path, body, bearer).Code
	}
	if code := file("/api/tasks"); code != http.StatusCreated {
		t.Fatalf("token of an operator owner filing = %d, want 201", code)
	}
	if _, _, _, err := h.client.Store.SetAllowedPrincipalRole(ctx, "user:olivia@example.com", "grain/viewer"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/tasks", "/mcp", "/api/mcp"} {
		rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, path, `{"title":"x","description":"x","noRepo":true}`, bearer)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "wider than its owner's current role") {
			t.Fatalf("POST %s with a downgraded owner's operator token = %d %s, want 403 naming the owner's role", path, rec.Code, rec.Body.String())
		}
	}

	narrow, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "olivia-reader", ScopeSet: "grain/viewer", Owner: "olivia@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	bearer["Authorization"] = "Bearer " + narrow.Token
	if code := file("/mcp"); code != http.StatusOK {
		t.Fatalf("viewer token of a viewer owner on /mcp = %d, want 200", code)
	}
	if code := file("/api/tasks"); code != http.StatusForbidden {
		t.Fatalf("viewer token filing = %d, want 403", code)
	}

	// A group lookup that fails cannot narrow an owner whose own user:
	// entry's role has no deny: contributor's token still files.
	h.allow(t, "user:gus@example.com", "grain/contributor")
	h.allow(t, "group:readers@example.com", "grain/viewer")
	h.client.Config.GroupChecker = groupCheckerFunc(func(context.Context, string, string) (bool, error) {
		return false, errors.New("cloud identity unreachable")
	})
	gus, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "gus-laptop", ScopeSet: "grain/contributor", Owner: "gus@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	bearer["Authorization"] = "Bearer " + gus.Token
	if code := file("/api/tasks"); code != http.StatusCreated {
		t.Fatalf("contributor token of a contributor owner, group lookup down = %d, want 201 (no deny to miss)", code)
	}

	// A token issued without an owner acts for the deployment's local
	// account, which is no email address and so in no group: a group
	// lookup that fails leaves it working (ui/e2e/external-agent-filer.spec.js).
	unowned, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "widgets-writer", Scopes: []string{"tasks:write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bearer["Authorization"] = "Bearer " + unowned.Token
	if code := file("/api/tasks"); code != http.StatusCreated {
		t.Fatalf("unowned token, group lookup down = %d, want 201", code)
	}

	// An owner with an email address and no entry of their own, whose only
	// possible match is the group that cannot be looked up, is refused.
	ghost, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "hal-laptop", Scopes: []string{"tasks:write"}, Owner: "hal@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	bearer["Authorization"] = "Bearer " + ghost.Token
	rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"x","description":"x","noRepo":true}`, bearer)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cannot be checked") {
		t.Fatalf("token of an owner only a failed group lookup could match = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// A gma_ token stops working once its owner is removed from the allowlist,
// and never works for an owner the list does not match, while a verifier
// admits people by that list; with no verifier the list admits nobody and
// such a token works as before (docs/test-runs/2026-11-07-rbac-roles.md O4).
func TestExternalTokenOfAnUnlistedOwnerIsRefusedBehindAVerifier(t *testing.T) {
	ctx := context.Background()
	h := newRoleHarness(t)
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:olivia@example.com", "grain/contributor")
	issue := func(name, owner string) map[string]string {
		t.Helper()
		issued, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
			Name: name, ScopeSet: "grain/contributor", Owner: owner,
		})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"Authorization": "Bearer " + issued.Token, "Content-Type": "application/json"}
	}
	file := func(bearer map[string]string) *httptest.ResponseRecorder {
		return serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/api/tasks", `{"title":"gma filing","description":"x","noRepo":true}`, bearer)
	}
	olivia := issue("olivia-laptop", "olivia@example.com")
	stranger := issue("sam-laptop", "sam@example.com")
	unowned, err := h.client.CreateExternalAgentToken(ctx, ui.CreateExternalAgentTokenRequest{
		Name: "widgets-writer", Scopes: []string{"tasks:write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	local := map[string]string{"Authorization": "Bearer " + unowned.Token, "Content-Type": "application/json"}

	if rec := file(olivia); rec.Code != http.StatusCreated {
		t.Fatalf("token of a listed owner = %d %s, want 201", rec.Code, rec.Body)
	}
	if _, _, err := h.client.Store.RemoveAllowedPrincipal(ctx, "user:olivia@example.com"); err != nil {
		t.Fatal(err)
	}
	for name, bearer := range map[string]map[string]string{"removed owner": olivia, "never-listed owner": stranger} {
		for _, path := range []string{"/api/tasks", "/mcp", "/api/mcp"} {
			rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, path, `{"title":"x","description":"x","noRepo":true}`, bearer)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "holds no role on this deployment") {
				t.Fatalf("POST %s with a %s's token behind IAP = %d %s, want 403 naming the missing role", path, name, rec.Code, rec.Body)
			}
		}
	}
	if rec := file(local); rec.Code != http.StatusCreated {
		t.Fatalf("unowned token (the local account) behind IAP = %d %s, want 201", rec.Code, rec.Body)
	}

	// No verifier: a single-user or access-code deployment, whose token
	// owners were never on the list.
	ui.ResetIAPConfig()
	for name, bearer := range map[string]map[string]string{"removed owner": olivia, "never-listed owner": stranger, "local account": local} {
		if rec := file(bearer); rec.Code != http.StatusCreated {
			t.Fatalf("%s's token with no verifier = %d %s, want 201", name, rec.Code, rec.Body)
		}
	}
}

// Behind a verifier whose assertion beats the access code, signing in by
// code is refused with the reason rather than answered 200 with a cookie
// that is then never used (docs/test-runs/2026-11-07-rbac-roles.md F1).
func TestAccessCodeSignInBehindIAPSaysTheVerifiedRoleApplies(t *testing.T) {
	h := newRoleHarness(t)
	h.allow(t, "user:vic@example.com", "grain/viewer")
	for _, c := range []struct{ name, contentType, body string }{
		{"json", "application/json", `{"code":"` + testAccessCode + `","name":"vic@example.com"}`},
		{"form", "application/x-www-form-urlencoded", url.Values{"code": {testAccessCode}, "name": {"vic@example.com"}}.Encode()},
	} {
		hdrs := h.as("vic@example.com")
		hdrs["Content-Type"] = c.contentType
		rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/api/auth/access-code", c.body, hdrs)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "vic@example.com") ||
			!strings.Contains(rec.Body.String(), "the access code does not change it") {
			t.Fatalf("%s sign-in by code behind IAP = %d %s, want 403 saying the IAP role applies", c.name, rec.Code, rec.Body)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == ui.AccessCookieName && ck.Value != "" {
				t.Fatalf("%s sign-in by code behind IAP set %s", c.name, ui.AccessCookieName)
			}
		}
	}
	// Without an assertion the code still signs in.
	rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/api/auth/access-code", `{"code":"`+testAccessCode+`","name":"vic@example.com"}`,
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in by code without an assertion = %d %s, want 200", rec.Code, rec.Body)
	}
}

// A limited role approves a stored task -- POST /api/tasks/{id}/approve, or
// POST /api/tasks/{id}/run-now on one with no approval yet -- only with
// tasks:admin on its repository and only when the role could have filed it
// approved itself, over REST, POST /api/mcp's grain_invoke and POST /mcp
// behind a verified session carrying a wider gma_ token.
func TestLimitedRoleApprovalIsHeldToTasksAdminAndTheRolesCeiling(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name:    "dispatcher",
			Scopes:  []string{"tasks:read", "tasks:write", "dispatch:read", "dispatch:write"},
			Version: 1,
		},
		model.APIScopeSet{
			Name:    "widgets-admin",
			Scopes:  []string{"tasks:read", "tasks:write", "tasks:admin", "dispatch:read", "dispatch:write"},
			Repos:   []string{"acme/widgets"},
			Version: 1,
		},
	)
	h := newRoleHarness(t)
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:dee@example.com", "dispatcher")
	h.allow(t, "user:wade@example.com", "widgets-admin")
	h.allow(t, "user:operator@example.com", "grain/operator")

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	seed := func(id string, edit func(*model.Task)) string {
		t.Helper()
		task := model.Task{ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &widgets}
		if edit != nil {
			edit(&task)
		}
		if err := h.client.Store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		return id
	}
	approved := func(task *model.Task) {
		task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}}
	}
	actions := []struct{ route, tool string }{{"approve", "api_approve"}, {"run-now", "api_run_now"}}
	n := 0
	next := func() string { n++; return "9" + strconv.Itoa(1000 + n)[1:] }

	defaultReviewID := model.DefaultReviewTemplateID(widgets)
	releasePushID := model.RepoDeclaredID(model.DeclaredTemplate, widgets, "release-push")
	if _, err := h.client.Store.SyncRepoDeclarations(ctx, widgets, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/release-push.yml",
			Template: model.Template{ID: releasePushID, Name: "release-push", Title: "Cut a release", Body: "Run release.", Target: &widgets},
			Push:     model.RefPushSet{{Pattern: "refs/tags/v*"}, {Pattern: "refs/tags/latest", Force: true}},
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range []model.Template{
		{
			ID:     "heavy-review",
			Name:   "heavy review",
			Title:  "heavy review",
			Writes: []model.WriteTarget{{Repo: gadgets}},
			Reads:  []model.RepoRef{gadgets},
			Grants: []model.Grant{{Capability: "api:tasks:write", Via: model.GrantByLabel}},
		},
		{
			ID:     "gadgets-write-review",
			Name:   "gadgets write review",
			Title:  "gadgets write review",
			Writes: []model.WriteTarget{{Repo: gadgets}},
		},
		{
			ID:    "gadgets-read-review",
			Name:  "gadgets read review",
			Title: "gadgets read review",
			Reads: []model.RepoRef{gadgets},
		},
		{
			ID:     defaultReviewID,
			Name:   "default review",
			Title:  "default review",
			Writes: []model.WriteTarget{{Repo: gadgets}},
			Reads:  []model.RepoRef{gadgets},
			Grants: []model.Grant{{Capability: "api:tasks:write", Via: model.GrantByLabel}},
		},
	} {
		if err := h.client.Store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	// Control: filing a task naming heavy-review is refused on POST /api/tasks
	// for the same role, so filing and approving enforce the same ceiling.
	if code, body := h.call("wade@example.com", http.MethodPost, "/api/tasks",
		`{"title":"t","body":"b","repo":"acme/widgets","reviewTemplateId":"heavy-review"}`); code != http.StatusForbidden ||
		!strings.Contains(body, "reviewTemplateId") || !strings.Contains(body, "widgets-admin") {
		t.Fatalf("widgets-admin POST /api/tasks with heavy-review: %d %s, want 403 naming reviewTemplateId and the role", code, body)
	}

	// dispatch:write without tasks:admin runs an approved task now but
	// cannot approve one by running it.
	for _, surface := range []string{"rest", "api-mcp"} {
		id := seed(next(), nil)
		var out string
		var refused bool
		if surface == "rest" {
			code, body := h.call("dee@example.com", http.MethodPost, "/api/tasks/"+id+"/run-now", `{}`)
			out, refused = body, code == http.StatusForbidden
		} else {
			out, refused = callControllerTool(t, h.srv, h.as("dee@example.com"), ui.ToolGrainInvoke, map[string]any{
				"tool": "api_run_now", "arguments": map[string]any{"id": id}, "reason": "testing the role ceiling",
			})
		}
		if !refused || !strings.Contains(out, "tasks:admin") || !strings.Contains(out, "dispatcher") {
			t.Errorf("%s: dispatcher run-now on unapproved task %s: refused=%v %s, want refusal naming tasks:admin and the role", surface, id, refused, out)
		}
		if task, err := h.client.Store.GetTask(ctx, id); err != nil || task == nil || task.Approval != nil {
			t.Errorf("%s: task %s was approved by a refused run-now: %+v %v", surface, id, task, err)
		}
	}
	okID := seed(next(), approved)
	if code, body := h.call("dee@example.com", http.MethodPost, "/api/tasks/"+okID+"/run-now", `{}`); code == http.StatusForbidden {
		t.Errorf("dispatcher run-now on approved task: %d %s, want it let through on dispatch:write", code, body)
	}

	// tasks:admin on the repository approves a plain task or one naming the
	// repository's own default review template by either route.
	for _, a := range actions {
		id := seed(next(), nil)
		if code, body := h.call("wade@example.com", http.MethodPost, "/api/tasks/"+id+"/"+a.route, `{}`); code == http.StatusForbidden {
			t.Errorf("widgets-admin %s on a plain task: %d %s, want it allowed", a.route, code, body)
		}
		defID := seed(next(), func(t *model.Task) { t.ReviewTemplateID = defaultReviewID })
		if code, body := h.call("wade@example.com", http.MethodPost, "/api/tasks/"+defID+"/"+a.route, `{}`); code >= 300 {
			t.Errorf("widgets-admin %s on task naming default review template: %d %s, want 2xx", a.route, code, body)
		}
		if task, err := h.client.Store.GetTask(ctx, defID); err != nil || task == nil || task.Approval == nil {
			t.Errorf("widgets-admin %s on task %s naming default review template did not approve it: %+v %v", a.route, defID, task, err)
		}
	}

	// ... but not one carrying what the role could not have filed approved.
	ceilings := []struct {
		name, want string
		edit       func(*model.Task)
	}{
		{"autoMerge", "autoMerge", func(t *model.Task) { t.AutoMerge = true }},
		{"keepRunning", "keepRunning", func(t *model.Task) { t.KeepRunning = true }},
		{"durable", model.DurableCapability, func(t *model.Task) { t.Admission = model.AdmissionDurable }},
		{"dangerousGrant", "api:settings:write", func(t *model.Task) {
			t.Grants = []model.Grant{{Capability: "api:settings:write", Via: model.GrantByLabel}}
		}},
		{"writes", "acme/gadgets", func(t *model.Task) { t.Writes = []model.WriteTarget{{Repo: gadgets}} }},
		{"reads", "acme/gadgets", func(t *model.Task) { t.Reads = []model.RepoRef{gadgets} }},
		{"reviewTemplateId", "reviewTemplateId", func(t *model.Task) { t.ReviewTemplateID = "heavy-review" }},
		{"reviewTemplateWrites", "reviewTemplateId", func(t *model.Task) { t.ReviewTemplateID = "gadgets-write-review" }},
		{"reviewTemplateReads", "reviewTemplateId", func(t *model.Task) { t.ReviewTemplateID = "gadgets-read-review" }},
		{"declaredPush", "declaredPush", func(t *model.Task) { t.TemplateID = releasePushID }},
	}
	for _, c := range ceilings {
		for _, a := range actions {
			for _, surface := range []string{"rest", "api-mcp"} {
				id := seed(next(), c.edit)
				var out string
				var refused bool
				if surface == "rest" {
					code, body := h.call("wade@example.com", http.MethodPost, "/api/tasks/"+id+"/"+a.route, `{}`)
					out, refused = body, code == http.StatusForbidden
				} else {
					out, refused = callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainInvoke, map[string]any{
						"tool": a.tool, "arguments": map[string]any{"id": id}, "reason": "testing the role ceiling",
					})
				}
				if !refused || !strings.Contains(out, c.want) || !strings.Contains(out, "widgets-admin") {
					t.Errorf("%s %s %s: refused=%v %s, want refusal naming %s and the role", surface, a.route, c.name, refused, out, c.want)
				}
				if task, err := h.client.Store.GetTask(ctx, id); err != nil || task == nil || task.Approval != nil {
					t.Errorf("%s %s %s: task %s was approved: %+v %v", surface, a.route, c.name, id, task, err)
				}
			}
		}
	}

	// A verified dispatcher session carrying an operator's gma_ token holding
	// tasks:admin is held to the session's role, on REST and on POST /mcp.
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := h.client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "operator-admin",
		Scopes: []string{"tasks:read", "tasks:write", "tasks:admin", "dispatch:read", "dispatch:write"},
		Owner:  "operator@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	withToken := h.as("dee@example.com")
	withToken["Authorization"] = "Bearer " + issued.Token
	withToken[ui.ActingReasonHeader] = "testing the role ceiling"
	for _, a := range actions {
		id := seed(next(), nil)
		rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/api/tasks/"+id+"/"+a.route, `{}`, withToken)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tasks:admin") {
			t.Errorf("REST %s as dispatcher with an admin token: %d %s, want 403 naming tasks:admin", a.route, rec.Code, rec.Body.String())
		}
		id = seed(next(), nil)
		rec = serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+a.tool+`","arguments":{"id":"`+id+`"}}}`, withToken)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":true`) || !strings.Contains(rec.Body.String(), "tasks:admin") {
			t.Errorf("POST /mcp %s as dispatcher with an admin token: %d %s, want a tool error naming tasks:admin", a.tool, rec.Code, rec.Body.String())
		}
		if task, err := h.client.Store.GetTask(ctx, id); err != nil || task == nil || task.Approval != nil {
			t.Errorf("POST /mcp %s: task %s was approved: %+v %v", a.tool, id, task, err)
		}
	}
}

// A template carrying a repository-declared push set (declaredPushOf) grants
// direct git-proxy push rights to refs beyond the task's own branch, so a
// limited role -- even one holding tasks:admin, schedules:write,
// templates:write and suites:write on the repository -- cannot file, schedule,
// modify, fire or approve work carrying it over REST, POST /api/mcp or POST /mcp.
func TestLimitedRolesCannotFileScheduleModifyOrApproveDeclaredPushTemplates(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name: "widgets-maintainer",
			Scopes: []string{
				"tasks:read", "tasks:write", "tasks:admin", "tasks:from-template",
				"dispatch:read", "dispatch:write",
				"schedules:read", "schedules:write",
				"templates:read", "templates:write",
				"suites:read", "suites:write",
			},
			Repos:   []string{"acme/widgets"},
			Version: 1,
		},
		model.APIScopeSet{
			Name: "standing-maintainer",
			Scopes: []string{
				"tasks:read", "tasks:write", "tasks:admin", "tasks:from-template",
				"dispatch:read", "dispatch:write",
				"schedules:read", "schedules:write",
				"templates:read", "templates:write",
				"suites:read", "suites:write",
			},
			Version: 1,
		},
	)
	h := newRoleHarness(t)
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:wade@example.com", "widgets-maintainer")
	h.allow(t, "user:stan@example.com", "standing-maintainer")
	h.allow(t, "user:operator@example.com", "grain/operator")

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if _, err := h.client.AddTargetRepo(ctx, widgets.String()); err != nil {
		t.Fatal(err)
	}
	releaseID := model.RepoDeclaredID(model.DeclaredTemplate, widgets, "release-train")
	plainID := model.RepoDeclaredID(model.DeclaredTemplate, widgets, "plain-check")
	if _, err := h.client.Store.SyncRepoDeclarations(ctx, widgets, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{
			{
				Path:     model.TemplateDeclarationDir + "/release-train.yml",
				Template: model.Template{ID: releaseID, Name: "release-train", Title: "Cut release", Body: "Run the release script.", Target: &widgets},
				Push:     model.RefPushSet{{Pattern: "refs/tags/v*"}, {Pattern: "refs/tags/latest", Force: true}},
			},
			{
				Path:     model.TemplateDeclarationDir + "/plain-check.yml",
				Template: model.Template{ID: plainID, Name: "plain-check", Title: "Plain check", Body: "Run plain check.", Target: &widgets},
			},
		},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatal(err)
	}

	toolOf := func(pattern string) string {
		t.Helper()
		for _, rt := range ui.AgentAPIRoutes() {
			if rt.Pattern == pattern {
				return rt.Tool
			}
		}
		t.Fatalf("no MCP tool registered for %s", pattern)
		return ""
	}

	// Operator-owned gma_ token presented inside wade's limited verified session
	// so POST /mcp exercises the outer role ceiling alongside REST and /api/mcp.
	opCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := h.client.CreateExternalAgentToken(opCtx, ui.CreateExternalAgentTokenRequest{
		Name: "operator-wide",
		Scopes: []string{
			"tasks:read", "tasks:write", "tasks:admin", "tasks:from-template",
			"dispatch:read", "dispatch:write",
			"schedules:read", "schedules:write",
			"templates:read", "templates:write",
			"suites:read", "suites:write",
		},
		Owner: "operator@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	mcpHeadersFor := func(email string) map[string]string {
		hdrs := h.as(email)
		hdrs["Authorization"] = "Bearer " + issued.Token
		hdrs[ui.ActingReasonHeader] = "testing declaredPush role ceiling"
		return hdrs
	}
	callSessionMCPAs := func(email, tool string, args map[string]any) (string, bool) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params":  map[string]any{"name": tool, "arguments": args},
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := serveFrom(h.srv, "203.0.113.10:443", http.MethodPost, "/mcp", string(payload), mcpHeadersFor(email))
		if rec.Code != http.StatusOK {
			return rec.Body.String(), true
		}
		raw := rec.Body.String()
		return raw, strings.Contains(raw, `"isError":true`)
	}
	callSessionMCP := func(tool string, args map[string]any) (string, bool) {
		return callSessionMCPAs("wade@example.com", tool, args)
	}

	// Control: the limited role CAN file and approve the plain declared template.
	if code, body := h.call("wade@example.com", http.MethodPost, "/api/tasks",
		`{"templateId":"`+plainID+`","approved":true}`); code != http.StatusCreated {
		t.Fatalf("widgets-maintainer filing plain template: %d %s, want 201", code, body)
	}

	// 1. Filing from release-train (with or without custom description/approval) is
	// refused across REST, POST /api/mcp (grain_submit_task + grain_invoke), and POST /mcp.
	for _, body := range []string{
		`{"templateId":"` + releaseID + `"}`,
		`{"templateId":"` + releaseID + `","approved":true}`,
		`{"templateId":"` + releaseID + `","approved":true,"description":"git push origin HEAD:refs/tags/v9.9.9"}`,
	} {
		code, resp := h.call("wade@example.com", http.MethodPost, "/api/tasks", body)
		if code != http.StatusForbidden || !strings.Contains(resp, "declared push set") || !strings.Contains(resp, "widgets-maintainer") {
			t.Errorf("REST POST /api/tasks %s: %d %s, want 403 naming declared push set and role", body, code, resp)
		}
	}
	if code, resp := h.call("wade@example.com", http.MethodPost, "/api/tasks/from-template",
		`{"repo":"acme/widgets","template":"release-train","approved":true}`); code != http.StatusForbidden ||
		!strings.Contains(resp, "declared push set") || !strings.Contains(resp, "widgets-maintainer") {
		t.Errorf("REST POST /api/tasks/from-template: %d %s, want 403 naming declared push set and role", code, resp)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainSubmitTask, map[string]any{
		"template_id": releaseID, "approve": true, "description": "git push origin HEAD:refs/tags/v9.9.9",
	}); !isErr || !strings.Contains(out, "declared push set") || !strings.Contains(out, "widgets-maintainer") {
		t.Errorf("POST /api/mcp grain_submit_task: isErr=%v %s, want refusal naming declared push set and role", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainInvoke, map[string]any{
		"tool":      toolOf("POST /api/tasks"),
		"arguments": map[string]any{"body": map[string]any{"templateId": releaseID, "approved": true}},
		"reason":    "testing role ceiling",
	}); !isErr || !strings.Contains(out, "declared push set") || !strings.Contains(out, "widgets-maintainer") {
		t.Errorf("POST /api/mcp grain_invoke create_task: isErr=%v %s, want refusal naming declared push set and role", isErr, out)
	}
	if out, isErr := callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainInvoke, map[string]any{
		"tool":      toolOf("POST /api/tasks/from-template"),
		"arguments": map[string]any{"body": map[string]any{"repo": "acme/widgets", "template": "release-train"}},
	}); !isErr || !strings.Contains(out, "declared push set") || !strings.Contains(out, "widgets-maintainer") {
		t.Errorf("POST /api/mcp grain_invoke from_template: isErr=%v %s, want refusal naming declared push set and role", isErr, out)
	}
	if out, isErr := callSessionMCP(toolOf("POST /api/tasks"), map[string]any{
		"body": map[string]any{"templateId": releaseID, "approved": true}, "reason": "testing role ceiling",
	}); !isErr || !strings.Contains(out, "declared push set") {
		t.Errorf("POST /mcp create_task in limited session: isErr=%v %s, want refusal naming declared push set", isErr, out)
	}
	if out, isErr := callSessionMCP(toolOf("POST /api/tasks/from-template"), map[string]any{
		"body": map[string]any{"repo": "acme/widgets", "template": "release-train"},
	}); !isErr || !strings.Contains(out, "declared push set") {
		t.Errorf("POST /mcp from_template in limited session: isErr=%v %s, want refusal naming declared push set", isErr, out)
	}

	// 2. Approving or run-now on an unapproved task carrying releaseID is refused
	// across REST, POST /api/mcp (with and without expect preflight), and POST /mcp.
	seedSeq := 9500
	seedUnapprovedRelease := func() string {
		t.Helper()
		seedSeq++
		id := strconv.Itoa(seedSeq)
		if err := h.client.Store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "Cut release",
			Body: "Run the release script.", Target: &widgets, TemplateID: releaseID,
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, act := range []struct{ route, pattern string }{
		{"approve", "POST /api/tasks/{id}/approve"},
		{"run-now", "POST /api/tasks/{id}/run-now"},
	} {
		tool := toolOf(act.pattern)
		id := seedUnapprovedRelease()
		code, resp := h.call("wade@example.com", http.MethodPost, "/api/tasks/"+id+"/"+act.route, `{}`)
		if code != http.StatusForbidden || !strings.Contains(resp, "declaredPush") || !strings.Contains(resp, "widgets-maintainer") {
			t.Errorf("REST %s on declaredPush task: %d %s, want 403 naming declaredPush and role", act.route, code, resp)
		}
		id = seedUnapprovedRelease()
		out, isErr := callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainInvoke, map[string]any{
			"tool":      tool,
			"arguments": map[string]any{"id": id, "expect": map[string]any{"state": "proposed"}},
			"reason":    "testing role ceiling",
		})
		if !isErr || !strings.Contains(out, "declaredPush") || !strings.Contains(out, "widgets-maintainer") {
			t.Errorf("POST /api/mcp %s on declaredPush task: isErr=%v %s, want refusal naming declaredPush and role", act.route, isErr, out)
		}
		id = seedUnapprovedRelease()
		out, isErr = callSessionMCP(tool, map[string]any{
			"id": id, "expect": map[string]any{"state": "proposed"}, "reason": "testing role ceiling",
		})
		if !isErr || !strings.Contains(out, "declaredPush") {
			t.Errorf("POST /mcp %s on declaredPush task: isErr=%v %s, want refusal naming declaredPush", act.route, isErr, out)
		}
		if stored, err := h.client.Store.GetTask(ctx, id); err != nil || stored == nil || stored.Approval != nil {
			t.Errorf("task %s approval after refused %s = %+v (err=%v), want nil", id, act.route, stored, err)
		}
	}

	// Operator CAN file and approve releaseID.
	opCode, opResp := h.call("operator@example.com", http.MethodPost, "/api/tasks", `{"templateId":"`+releaseID+`"}`)
	if opCode != http.StatusCreated {
		t.Fatalf("operator filing release template: %d %s, want 201", opCode, opResp)
	}
	var opTask ui.Task
	if err := json.Unmarshal([]byte(opResp), &opTask); err != nil {
		t.Fatal(err)
	}
	if code, resp := h.call("operator@example.com", http.MethodPost, "/api/tasks/"+opTask.ID+"/approve", `{}`); code != http.StatusOK {
		t.Fatalf("operator approving release task: %d %s, want 200", code, resp)
	}

	// 3. Standing schedule, template, and suite escalations: creating, modifying,
	// or running schedules/templates/suites referencing releaseID is refused across
	// REST, POST /api/mcp, and POST /mcp.
	sched, err := h.client.CreateSchedule(opCtx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: releaseID,
		Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("operator creating release schedule: %v", err)
	}
	suite, err := h.client.CreateSuite(opCtx, ui.CreateSuiteRequest{
		Name: "release-suite", TemplateIDs: []string{releaseID},
		Mode: "count", Count: 1, AutoMerge: new(false), RequireApproval: true,
	})
	if err != nil {
		t.Fatalf("operator creating release suite: %v", err)
	}
	suiteSched, err := h.client.CreateSchedule(opCtx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", SuiteID: suite.ID,
		Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("operator creating release suite schedule: %v", err)
	}

	standingCalls := []struct {
		name, method, path, pattern, body string
		mcpArgs                           map[string]any
	}{
		{
			name: "POST /api/schedules with templateId", method: http.MethodPost, path: "/api/schedules", pattern: "POST /api/schedules",
			body:    `{"repo":"acme/widgets","base":"main","templateId":"` + releaseID + `","recurrence":{"kind":"interval","every":"24h"}}`,
			mcpArgs: map[string]any{"body": map[string]any{"repo": "acme/widgets", "base": "main", "templateId": releaseID, "recurrence": map[string]any{"kind": "interval", "every": "24h"}}},
		},
		{
			name: "PATCH /api/schedules/{id}", method: http.MethodPatch, path: "/api/schedules/" + sched.ID, pattern: "PATCH /api/schedules/{id}",
			body:    `{"paused":true}`,
			mcpArgs: map[string]any{"id": sched.ID, "body": map[string]any{"paused": true}},
		},
		{
			name: "POST /api/schedules/{id}/run", method: http.MethodPost, path: "/api/schedules/" + sched.ID + "/run", pattern: "POST /api/schedules/{id}/run",
			body:    `{}`,
			mcpArgs: map[string]any{"id": sched.ID},
		},
		{
			name: "POST /api/schedules/{id}/run (suite)", method: http.MethodPost, path: "/api/schedules/" + suiteSched.ID + "/run", pattern: "POST /api/schedules/{id}/run",
			body:    `{}`,
			mcpArgs: map[string]any{"id": suiteSched.ID},
		},
		{
			name: "PATCH /api/templates/{id}", method: http.MethodPatch, path: "/api/templates/" + releaseID, pattern: "PATCH /api/templates/{id}",
			body:    `{"title":"Renamed"}`,
			mcpArgs: map[string]any{"id": releaseID, "body": map[string]any{"title": "Renamed"}},
		},
		{
			name: "POST /api/suites", method: http.MethodPost, path: "/api/suites", pattern: "POST /api/suites",
			body:    `{"name":"s2","templateIds":["` + releaseID + `"],"mode":"count","count":1,"autoMerge":false,"requireApproval":true}`,
			mcpArgs: map[string]any{"body": map[string]any{"name": "s2", "templateIds": []string{releaseID}, "mode": "count", "count": 1, "autoMerge": false, "requireApproval": true}},
		},
		{
			name: "PATCH /api/suites/{id}", method: http.MethodPatch, path: "/api/suites/" + suite.ID, pattern: "PATCH /api/suites/{id}",
			body:    `{"name":"renamed-suite"}`,
			mcpArgs: map[string]any{"id": suite.ID, "body": map[string]any{"name": "renamed-suite"}},
		},
		{
			name: "POST /api/suite-runs", method: http.MethodPost, path: "/api/suite-runs", pattern: "POST /api/suite-runs",
			body:    `{"suiteId":"` + suite.ID + `","repo":"acme/widgets","base":"main"}`,
			mcpArgs: map[string]any{"body": map[string]any{"suiteId": suite.ID, "repo": "acme/widgets", "base": "main"}},
		},
	}
	for _, sc := range standingCalls {
		code, resp := h.call("stan@example.com", sc.method, sc.path, sc.body)
		if code != http.StatusForbidden || !strings.Contains(resp, "declared push set") || !strings.Contains(resp, "standing-maintainer") {
			t.Errorf("REST %s: %d %s, want 403 naming declared push set and role", sc.name, code, resp)
		}
		tool := toolOf(sc.pattern)
		if out, isErr := callControllerTool(t, h.srv, h.as("stan@example.com"), ui.ToolGrainInvoke, map[string]any{
			"tool": tool, "arguments": sc.mcpArgs, "reason": "testing standing role ceiling",
		}); !isErr || !strings.Contains(out, "declared push set") || !strings.Contains(out, "standing-maintainer") {
			t.Errorf("POST /api/mcp %s: isErr=%v %s, want refusal naming declared push set and role", sc.name, isErr, out)
		}
		argsWithReason := make(map[string]any, len(sc.mcpArgs)+1)
		for k, v := range sc.mcpArgs {
			argsWithReason[k] = v
		}
		argsWithReason["reason"] = "testing standing role ceiling"
		if out, isErr := callSessionMCPAs("stan@example.com", tool, argsWithReason); !isErr || !strings.Contains(out, "declared push set") {
			t.Errorf("POST /mcp %s: isErr=%v %s, want refusal naming declared push set", sc.name, isErr, out)
		}
	}
}
