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
	"errors"
	"net/http"
	"net/url"
	"slices"
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
	// Limited roles never reach the MCP endpoint, whose tools dispatch to
	// many routes.
	if code, _ := h.call("viv@example.com", http.MethodPost, "/api/mcp", `{}`); code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/mcp: %d, want 403", code)
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
	if code, body := h.call("dee@example.com", http.MethodGet, "/api/deployment/secret-audit", ""); code != http.StatusForbidden || !strings.Contains(body, "contractors@example.com") {
		t.Fatalf("GET secret-audit with a denying group's lookup failing: %d %s, want 403 naming the group", code, body)
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

func TestOnlyTheAccessCodeLiftsADeny(t *testing.T) {
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
	// The deny grants nothing, so it is "no wider" than anybody's role;
	// re-roling or removing it would still give lead -- and everybody
	// else in the group -- what it withholds.
	group := "/api/access/principals/" + url.PathEscape("group:contractors@example.com")
	for _, as := range []string{"lead@example.com", "ops@example.com"} {
		if code, body := h.call(as, http.MethodPatch, group, `{"role":"grain/viewer"}`); code != http.StatusForbidden || !strings.Contains(body, "denies") {
			t.Fatalf("%s PATCH a denying entry: %d %s, want 403", as, code, body)
		}
		if code, body := h.call(as, http.MethodDelete, group, ""); code != http.StatusForbidden || !strings.Contains(body, "denies") {
			t.Fatalf("%s DELETE a denying entry: %d %s, want 403", as, code, body)
		}
	}
	bearer := map[string]string{"Authorization": "Bearer " + testAccessCode, "Content-Type": "application/json"}
	if rec := serveFrom(h.srv, "127.0.0.1:4242", http.MethodDelete, group, "", bearer); rec.Code != http.StatusOK {
		t.Fatalf("access code DELETE a denying entry: %d %s", rec.Code, rec.Body.String())
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
	// A verified operator gives no role holding a dangerous scope.
	if code, body := h.call("ops@example.com", http.MethodPatch, entry, `{"role":"grain/operator","reason":"promote"}`); code != http.StatusForbidden || !strings.Contains(body, "access-code admin") {
		t.Fatalf("verified operator PATCH to grain/operator: %d %s", code, body)
	}
	if code, body := h.call("ops@example.com", http.MethodPatch, entry, `{"role":"grain/contributor"}`); code != http.StatusOK {
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
	// An entry at a dangerous role only the access code removes.
	if code, _ := h.call("ops@example.com", http.MethodDelete, entry, ""); code != http.StatusForbidden {
		t.Fatalf("verified operator DELETE an operator entry: %d, want 403", code)
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
