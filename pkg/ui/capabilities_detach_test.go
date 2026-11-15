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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The api:capabilities:detach scope (director-capabilities.spec.md §3.1.2,
// gap G6) reaches the capabilities:admin routes that remove grants, and
// only to remove them: these tests pin each half of that.

func seedDetachRepoConfig(t *testing.T, client *ui.Client) model.RepoRef {
	t.Helper()
	ctx := context.Background()
	cfg := model.DefaultConfig()
	cfg.PollInterval = 30 * time.Second
	cfg.GitHubHost = "github.com"
	cfg.TargetRepos = []string{"acme/widgets", "acme/gadgets"}
	if err := client.Store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		CICapabilities:      []string{model.GitHubSandboxRepoGrant, model.GCPKeySandboxAgentGrant, model.PackagePublishRef + "={}"},
		DefaultCapabilities: []string{model.GitHubSandboxRepoGrant, model.GCPKeySandboxAgentGrant},
	}); err != nil {
		t.Fatal(err)
	}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                gadgets,
		DefaultCapabilities: []string{model.GitHubSandboxRepoGrant, model.GCPKeySandboxAgentGrant},
	}); err != nil {
		t.Fatal(err)
	}
	return widgets
}

func seedSessionTask(t *testing.T, client *ui.Client, id, session string, grants ...string) {
	t.Helper()
	now := time.Now()
	task := model.Task{
		ID: id, Title: "session " + session, Intent: model.IntentImplement,
		Interactive: true, Tags: []string{model.ConsoleSessionTag(session)},
		Origin:     model.Origin{Reason: model.ReasonConsoleSession},
		ApprovedAt: &now, CreatedAt: &now,
	}
	for _, g := range grants {
		task.Grants = append(task.Grants, model.Grant{Capability: g, Via: model.GrantByLabel})
	}
	if err := client.Store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, err := client.Store.GetTask(context.Background(), id)
	if err != nil || got == nil || !got.IsConsoleSession() {
		t.Fatalf("seeded task %s is not a console session: %+v, %v", id, got, err)
	}
}

func taskGrantNames(t *testing.T, client *ui.Client, id string) []string {
	t.Helper()
	task, err := client.Store.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	var names []string
	for _, g := range task.Grants {
		names = append(names, g.Capability)
	}
	return names
}

func TestCapabilitiesDetachRemovesATaskGrant(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")
	seedAPITask(t, client, "2", "acme/widgets", "self-debug", model.GrainCIGrant)

	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/capabilities",
		`{"grant":"self-debug","attach":false}`, "stale: the task no longer reads grain's source"); code != http.StatusOK {
		t.Fatalf("detaching self-debug under capabilities:detach answered %d: %s", code, resp)
	}
	if got := taskGrantNames(t, client, "2"); slices.Contains(got, "self-debug") || len(got) != 1 {
		t.Errorf("task 2 grants after detach = %v, want only the grain-ci grant", got)
	}
}

func TestCapabilitiesDetachRefusesAttachReplaceAndMissingAttach(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach", "self-debug")
	seedAPITask(t, client, "2", "acme/widgets", model.GrainCIGrant)

	for _, body := range []string{
		`{"grant":"self-debug","attach":true}`,
		`{"id":"self-debug","attach":true}`,
		`{"capability":"https://grain.dev/capabilities/grain#ci","attach":true}`,
		`{"grant":"grain-ci","attach":false,"replace":"self-debug"}`,
		`{"id":"grain-ci","attach":false,"Replace":"self-debug"}`,
		`{"id":"grain-ci"}`,
		`{"attach":false}`,
		``,
	} {
		if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/capabilities", body, "cleanup"); code != http.StatusForbidden ||
			!strings.Contains(resp, "capabilities:detach") {
			t.Errorf("capabilities:detach with body %q answered %d, want 403 from the detach check: %s", body, code, resp)
		}
	}
	if got := taskGrantNames(t, client, "2"); len(got) != 1 {
		t.Errorf("task 2 grants after refused calls = %v, want unchanged", got)
	}
}

func TestCapabilitiesDetachRequiresAReason(t *testing.T) {
	srv, client := testServer(t)
	widgets := seedDetachRepoConfig(t, client)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")
	seedAPITask(t, client, "2", "acme/widgets", "self-debug")

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tasks/2/capabilities", `{"grant":"self-debug","attach":false}`},
		{http.MethodPut, "/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":["github-sandbox"]}`},
		{http.MethodPut, "/api/repos/acme/widgets/ci-capabilities", `{"ciCapabilities":["github-sandbox"]}`},
	} {
		code, resp := asAgent(t, srv, "1", tc.method, tc.path, tc.body, "")
		if code != http.StatusForbidden || !strings.Contains(resp, "reason") {
			t.Errorf("%s %s with no reason answered %d, want 403 naming the reason: %s", tc.method, tc.path, code, resp)
		}
	}
	if got := taskGrantNames(t, client, "2"); !slices.Equal(got, []string{"self-debug"}) {
		t.Errorf("task 2 grants after reasonless detach = %v, want unchanged", got)
	}
	rc, err := client.Store.GetRepoConfig(context.Background(), widgets)
	if err != nil || rc == nil || len(rc.DefaultCapabilities) != 2 || len(rc.CICapabilities) != 3 {
		t.Errorf("repo config after reasonless narrowing = %+v, %v, want unchanged", rc, err)
	}
}

func TestCapabilitiesDetachNarrowsRepoLists(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	widgets := seedDetachRepoConfig(t, client)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")

	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":["`+model.GitHubSandboxRepoGrant+`"]}`, "gcp-key is over-broad for every task"); code != http.StatusOK {
		t.Fatalf("narrowing defaultCapabilities answered %d: %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/ci-capabilities",
		`{"ciCapabilities":["`+model.GitHubSandboxRepoGrant+`","`+model.PackagePublishRef+`={}"]}`, "CI no longer needs gcp-key"); code != http.StatusOK {
		t.Fatalf("narrowing ciCapabilities answered %d: %s", code, resp)
	}
	rc, err := client.Store.GetRepoConfig(ctx, widgets)
	if err != nil || rc == nil {
		t.Fatalf("reading repo config: %+v, %v", rc, err)
	}
	if !slices.Equal(rc.DefaultCapabilities, []string{model.GitHubSandboxRepoGrant}) {
		t.Errorf("defaultCapabilities = %v, want [%s]", rc.DefaultCapabilities, model.GitHubSandboxRepoGrant)
	}
	if slices.Contains(rc.CICapabilities, model.GCPKeySandboxAgentGrant) || len(rc.CICapabilities) != 2 {
		t.Errorf("ciCapabilities = %v, want github-sandbox and package-publish", rc.CICapabilities)
	}
	// An empty list is a subset too: removing everything is still a detach.
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":[]}`, "no default grants"); code != http.StatusOK {
		t.Fatalf("clearing defaultCapabilities answered %d: %s", code, resp)
	}
}

func TestCapabilitiesDetachRefusesRepoSupersetsAndOtherFields(t *testing.T) {
	srv, client := testServer(t)
	widgets := seedDetachRepoConfig(t, client)
	// The acting task holds gcp-key and self-debug itself, so it is the
	// detach scope (not agentGrantEscalation) that refuses adding them.
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach", "self-debug", "gcp-key")

	for _, tc := range []struct{ path, body string }{
		{"/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":["github-sandbox","self-debug"]}`},
		{"/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":["package-publish"]}`}, // a CI-only entry, not on this field
		{"/api/repos/acme/widgets/capabilities", `{}`},
		{"/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":null}`},
		{"/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":["github-sandbox"],"ciCapabilities":[]}`},
		{"/api/repos/acme/widgets/ci-capabilities", `{"ciCapabilities":["github-sandbox","gcp-key","package-publish","self-debug"]}`},
		{"/api/repos/acme/widgets/ci-capabilities", `{"defaultCapabilities":["github-sandbox"]}`},
	} {
		if code, resp := asAgent(t, srv, "1", http.MethodPut, tc.path, tc.body, "cleanup"); code != http.StatusForbidden ||
			!strings.Contains(resp, "capabilities:detach") {
			t.Errorf("PUT %s %s under capabilities:detach answered %d, want 403 from the detach check: %s", tc.path, tc.body, code, resp)
		}
	}
	rc, err := client.Store.GetRepoConfig(context.Background(), widgets)
	if err != nil || rc == nil || len(rc.DefaultCapabilities) != 2 || len(rc.CICapabilities) != 3 {
		t.Errorf("repo config after refused calls = %+v, %v, want unchanged", rc, err)
	}
}

func TestCapabilitiesDetachIsHeldToItsRepositories(t *testing.T) {
	srv, client := testServer(t)
	seedDetachRepoConfig(t, client)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach@acme/widgets")
	seedAPITask(t, client, "2", "acme/gadgets", "self-debug")

	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/gadgets/capabilities",
		`{"defaultCapabilities":["`+model.GitHubSandboxRepoGrant+`"]}`, "cleanup"); code != http.StatusForbidden {
		t.Errorf("capabilities:detach@acme/widgets narrowed acme/gadgets: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/capabilities",
		`{"grant":"self-debug","attach":false}`, "cleanup"); code != http.StatusForbidden {
		t.Errorf("capabilities:detach@acme/widgets detached from a task in acme/gadgets: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":["`+model.GitHubSandboxRepoGrant+`"]}`, "cleanup"); code != http.StatusOK {
		t.Errorf("capabilities:detach@acme/widgets was refused acme/widgets: %d %s", code, resp)
	}
}

func TestCapabilitiesDetachIsRefusedOnTheSandboxChannel(t *testing.T) {
	srv, client := testServer(t)
	seedDetachRepoConfig(t, client)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")
	seedAPITask(t, client, "2", "acme/widgets", "self-debug")

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tasks/2/capabilities", `{"grant":"self-debug","attach":false}`},
		{http.MethodPut, "/api/repos/acme/widgets/capabilities", `{"defaultCapabilities":["github-sandbox"]}`},
		{http.MethodPut, "/api/repos/acme/widgets/ci-capabilities", `{"ciCapabilities":["github-sandbox"]}`},
	} {
		if code, resp := asSandboxAgent(t, srv, "1", "run-1", tc.method, tc.path, tc.body, "cleanup"); code != http.StatusForbidden {
			t.Errorf("%s %s over sandbox-cli under capabilities:detach answered %d, want 403: %s", tc.method, tc.path, code, resp)
		}
	}
	if got := taskGrantNames(t, client, "2"); !slices.Equal(got, []string{"self-debug"}) {
		t.Errorf("task 2 grants after sandbox-cli detach = %v, want unchanged", got)
	}
}

func TestCapabilitiesDetachRefusesStandingSessions(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")
	seedSessionTask(t, client, "8", "ops", "self-debug")
	seedSessionTask(t, client, "9", "director", "api:capabilities:detach", "self-debug")

	// Another standing session.
	code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/8/capabilities",
		`{"grant":"self-debug","attach":false}`, "cleanup")
	if code != http.StatusForbidden || !strings.Contains(resp, "standing session") {
		t.Errorf("detaching from another standing session answered %d, want 403 naming it: %s", code, resp)
	}
	// The caller's own standing session.
	code, resp = asAgent(t, srv, "9", http.MethodPost, "/api/tasks/9/capabilities",
		`{"grant":"self-debug","attach":false}`, "cleanup")
	if code != http.StatusForbidden || !strings.Contains(resp, "standing session") {
		t.Errorf("a session detaching from itself answered %d, want 403 naming it: %s", code, resp)
	}
	// And from one session to another.
	if code, resp := asAgent(t, srv, "9", http.MethodPost, "/api/tasks/8/capabilities",
		`{"grant":"self-debug","attach":false}`, "cleanup"); code != http.StatusForbidden {
		t.Errorf("the director detached from another session: %d %s", code, resp)
	}
	for _, id := range []string{"8", "9"} {
		if got := taskGrantNames(t, client, id); !slices.Contains(got, "self-debug") {
			t.Errorf("session task %s grants after refused detach = %v, want self-debug kept", id, got)
		}
	}
}

func TestCapabilitiesDetachDoesNotReachOtherAdminRoutes(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:detach")
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/capability-policy", `{}`, "cleanup"); code != http.StatusForbidden {
		t.Errorf("capabilities:detach reached PUT /api/capability-policy: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/capability-servers/remove", `{"url":"https://example.com/caps"}`, "cleanup"); code != http.StatusForbidden {
		t.Errorf("capabilities:detach reached POST /api/capability-servers/remove: %d %s", code, resp)
	}
}

func TestCapabilitiesDetachListsItsRoutesAsTools(t *testing.T) {
	want := map[string]bool{}
	for _, rt := range ui.AgentAPIRoutes() {
		switch rt.Pattern {
		case "POST /api/tasks/{id}/capabilities",
			"PUT /api/repos/{owner}/{name}/capabilities",
			"PUT /api/repos/{owner}/{name}/ci-capabilities":
			want[rt.Tool] = true
		}
	}
	if len(want) != 3 {
		t.Fatalf("found %d detach routes in AgentAPIRoutes, want 3", len(want))
	}
	got := map[string]bool{}
	for _, tool := range ui.AgentAPITools(nil, "1", []string{"api:capabilities:detach"}) {
		got[tool.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("api:capabilities:detach does not list tool %s", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("api:capabilities:detach lists %d tools, want exactly the %d detach routes: %v", len(got), len(want), got)
	}
}

// TestCapabilitiesDetachOverTheControllerMCPServer pins the scope over the
// path the director's MCP tools take for an external credential: the
// generated tool is listed and detaches, and the same tool is still held
// to detach-only bodies and refused on a standing session.
func TestCapabilitiesDetachOverTheControllerMCPServer(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	seedAPITask(t, client, "2", "acme/widgets", "self-debug", model.GrainCIGrant)
	seedSessionTask(t, client, "8", "ops", "self-debug")

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "detacher",
		Scopes: []string{"capabilities:detach"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := ""
	for _, rt := range ui.AgentAPIRoutes() {
		if rt.Pattern == "POST /api/tasks/{id}/capabilities" {
			tool = rt.Tool
		}
	}
	if tool == "" {
		t.Fatal("no generated tool for POST /api/tasks/{id}/capabilities")
	}

	call := func(id string, body map[string]any) (bool, string) {
		t.Helper()
		rec, res := callMCPTool(t, srv, issued.Token, tool, map[string]any{"id": id, "body": body, "reason": "stale grant"})
		if rec.Code != http.StatusOK || len(res.Content) == 0 {
			t.Fatalf("calling %s over MCP: %d %s", tool, rec.Code, rec.Body.String())
		}
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		return res.IsError, text
	}
	if isErr, text := call("2", map[string]any{"grant": "self-debug", "attach": true}); !isErr || !strings.Contains(text, "capabilities:detach") {
		t.Errorf("attach over MCP under capabilities:detach was not refused by the detach check: %v %s", isErr, text)
	}
	if isErr, text := call("8", map[string]any{"grant": "self-debug", "attach": false}); !isErr || !strings.Contains(text, "standing session") {
		t.Errorf("detach from a standing session over MCP was not refused: %v %s", isErr, text)
	}
	if isErr, text := call("2", map[string]any{"grant": "self-debug", "attach": false}); isErr {
		t.Fatalf("detach over MCP under capabilities:detach failed: %s", text)
	}
	if got := taskGrantNames(t, client, "2"); slices.Contains(got, "self-debug") {
		t.Errorf("task 2 grants after MCP detach = %v, want self-debug removed", got)
	}
}

// grain_explore_scope labels the routes a credential reaches through
// capabilities:detach with that scope, not with the dangerous
// capabilities:admin they are registered under: the 2026-11-07 §4
// walkthrough found a detach-only credential told it held
// "capabilities:admin" (dangerous: true).
func TestCapabilitiesDetachIsWhatGrainExploreScopeSays(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "detacher",
		Scopes: []string{"capabilities:detach", "tasks:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + issued.Token}
	for _, args := range []map[string]any{{}, {"scope": "capabilities"}} {
		text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, args)
		if isErr {
			t.Fatalf("grain_explore_scope(%v): %s", args, text)
		}
		// The overview's scope list, and each route's scope field; a
		// route's own description may still say which scope it is
		// registered under.
		if strings.Contains(text, `"scope":"capabilities:admin"`) {
			t.Errorf("grain_explore_scope(%v) lists capabilities:admin to a detach-only credential:\n%s", args, text)
		}
		if !strings.Contains(text, `"scope":"capabilities:detach"`) {
			t.Errorf("grain_explore_scope(%v) does not name capabilities:detach:\n%s", args, text)
		}
	}
	text, _ := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, map[string]any{})
	var overview struct {
		Groups []struct {
			Group  string `json:"group"`
			Scopes []struct {
				Scope      string `json:"scope"`
				Dangerous  bool   `json:"dangerous"`
				RouteCount int    `json:"routeCount"`
			} `json:"scopes"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(text), &overview); err != nil {
		t.Fatalf("decoding the overview: %v\n%s", err, text)
	}
	found := false
	for _, g := range overview.Groups {
		for _, sc := range g.Scopes {
			if sc.Scope == "capabilities:detach" {
				found = true
				if sc.Dangerous || sc.RouteCount != 3 {
					t.Errorf("capabilities:detach overview = %+v, want 3 routes, not dangerous", sc)
				}
			}
		}
	}
	if !found {
		t.Errorf("overview groups = %+v, want a capabilities:detach entry", overview.Groups)
	}
}
