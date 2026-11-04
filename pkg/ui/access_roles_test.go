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
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
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

func TestEntriesFromBeforeRolesKeepFullAccess(t *testing.T) {
	h := newRoleHarness(t)
	// AddAllowedPrincipal is what wrote every entry before roles; the
	// column's default is what migration 109 gives them.
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
