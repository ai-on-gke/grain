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
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// seedAPITask files one task in repo holding grants, for the agent API
// gate to read.
func seedAPITask(t *testing.T, client *ui.Client, id, repo string, grants ...string) {
	t.Helper()
	ref, err := model.ParseRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &ref}
	for _, g := range grants {
		task.Grants = append(task.Grants, model.Grant{Capability: g, Via: model.GrantByLabel})
	}
	if err := client.Store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

// asAgent is a request made by a generated api tool for task id's run.
func asAgent(t *testing.T, srv *ui.Server, id, method, path, body, reason string) (int, string) {
	t.Helper()
	headers := map[string]string{ui.ActingTaskHeader: id, "Content-Type": "application/json"}
	if reason != "" {
		headers[ui.ActingReasonHeader] = reason
	}
	rec := doWithHeaders(t, srv, method, path, body, headers)
	return rec.Code, rec.Body.String()
}

func TestAgentAPIRefusesARouteWithoutItsScope(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets")
	seedAPITask(t, client, "2", "acme/widgets", "api:tasks:read")

	if code, body := asAgent(t, srv, "1", http.MethodGet, "/api/tasks", "", ""); code != http.StatusForbidden ||
		!strings.Contains(body, "tasks:read") {
		t.Errorf("a task with no api grant read /api/tasks: %d %s", code, body)
	}
	if code, body := asAgent(t, srv, "2", http.MethodGet, "/api/tasks", "", ""); code != http.StatusOK {
		t.Errorf("api:tasks:read was refused /api/tasks: %d %s", code, body)
	}
	if code, _ := asAgent(t, srv, "2", http.MethodPost, "/api/tasks/1/comments", `{"body":"hi"}`, ""); code != http.StatusForbidden {
		t.Errorf("api:tasks:read was let comment (tasks:write): %d", code)
	}
	// No acting task is the UI, unchanged.
	if rec := do(t, srv, http.MethodGet, "/api/tasks", ""); rec.Code != http.StatusOK {
		t.Errorf("a request with no acting task was refused: %d", rec.Code)
	}
	// Internal plumbing is never reachable as an agent, whatever it holds.
	seedAPITask(t, client, "3", "acme/widgets", "api:task-admin")
	if code, _ := asAgent(t, srv, "3", http.MethodPost, "/api/tasks/1/delivered", `{}`, ""); code != http.StatusForbidden {
		t.Errorf("an internal route answered an agent: %d", code)
	}
}

func TestAgentAPIRepoScopedGrantRefusesOtherRepos(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write@acme/widgets")
	seedAPITask(t, client, "2", "acme/widgets")
	seedAPITask(t, client, "3", "acme/gadgets")

	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/comments", `{"body":"same repo"}`, ""); code >= 300 {
		t.Errorf("tasks:write@acme/widgets was refused a task in acme/widgets: %d %s", code, body)
	}
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/3/comments", `{"body":"other repo"}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "acme/gadgets") {
		t.Errorf("tasks:write@acme/widgets reached acme/gadgets: %d %s", code, body)
	}
	// A route whose repository cannot be told is only a whole grant's.
	if code, _ := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/reorder", `{"ids":["2"]}`, ""); code != http.StatusForbidden {
		t.Errorf("a repo-limited grant reordered the whole queue: %d", code)
	}
}

func TestAgentAPIDangerousScopeNeedsAReason(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:merge:force", "api:ci:write")
	seedAPITask(t, client, "2", "acme/widgets")
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/force-merge", `{}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "reason") {
		t.Errorf("a force-merge with no reason was not refused for it: %d %s", code, body)
	}
	if code, body := asAgent(t, srv, "1", http.MethodDelete, "/api/ci/runs/nope", `{}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "reason") {
		t.Errorf("a DELETE with no reason was not refused for it: %d %s", code, body)
	}
}

// TestAgentAPICannotEscalate holds every route to the rule that an agent
// can never raise its own or another task's grants: one holding every
// scope a person might hand an agent short of the dangerous ones is
// refused by every route that writes grants, and no body naming a
// capability it does not hold, or a dangerous api grant, gets through
// any route.
func TestAgentAPICannotEscalate(t *testing.T) {
	srv, client := testServer(t)
	var grants []string
	for _, s := range ui.APIScopes() {
		if !s.Dangerous {
			grants = append(grants, "api:"+string(s.Scope))
		}
	}
	seedAPITask(t, client, "1", "acme/widgets", grants...)
	seedAPITask(t, client, "2", "acme/widgets")

	for _, r := range ui.AgentAPIRoutes() {
		if r.Scope != "capabilities:admin" {
			continue
		}
		path := r.Path
		for _, p := range r.Params {
			path = strings.Replace(path, "{"+p+"}", map[string]string{"id": "2", "owner": "acme", "name": "widgets"}[p]+"x", 1)
		}
		path = strings.ReplaceAll(path, "2x", "2")
		if code, _ := asAgent(t, srv, "1", r.Method, path, `{}`, "testing"); code != http.StatusForbidden {
			t.Errorf("%s answered an agent without capabilities:admin: %d", r.Pattern, code)
		}
	}
	for _, body := range []string{
		`{"capabilities":["self-debug"]}`,
		`{"capabilities":["api:capabilities:admin"]}`,
		`{"capabilities":["api:secrets:write"]}`,
		`{"defaultCapabilities":["api:deployment-ops"]}`,
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodPatch, "/api/tasks/2"},
			{http.MethodPost, "/api/schedules"},
			{http.MethodPost, "/api/templates"},
		} {
			if code, resp := asAgent(t, srv, "1", route.method, route.path, body, "testing"); code != http.StatusForbidden {
				t.Errorf("%s %s with %s answered an agent %d: %s", route.method, route.path, body, code, resp)
			}
		}
	}
	// Even for capabilities task 1 holds, or empty lists / false flags that
	// strip grants, PATCH /api/tasks/2 touching grants requires
	// capabilities:admin.
	for _, body := range []string{
		`{"capabilities":[]}`,
		`{"capabilities":["api:tasks:read"]}`,
		`{"grants":[{"capability":"api:tasks:read"}]}`,
		`{"priority":false}`,
		`{"keepRunning":false}`,
		`{"keep_running":false}`,
	} {
		if code, resp := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", body, "testing"); code != http.StatusForbidden {
			t.Errorf("PATCH /api/tasks/2 without capabilities:admin with %s answered %d: %s", body, code, resp)
		}
	}

	// Holding capabilities:admin itself -- a dangerous scope a person
	// granted, with a reason -- is the power to attach grants, not to
	// attach ones its own task lacks or dangerous ones, under any of the
	// keys POST /api/tasks/{id}/capabilities takes a grant by.
	seedAPITask(t, client, "3", "acme/widgets", "api:capabilities:admin", "api:tasks:read", "gcp-key")
	for _, body := range []string{
		`{"id":"api:deployment:upgrade","attach":true}`,
		`{"grant":"api:secrets:write","attach":true}`,
		`{"id":"self-debug","attach":true}`,
		`{"ID":"self-debug","attach":true}`,
		`{"capability":"api:merge:force","attach":true}`,
		`{"id":"api:tasks:read","attach":true,"replace":"api:capabilities:admin"}`,
		`{"grant":"api:tasks:read","attach":true,"replace":"api:capabilities:admin"}`,
		`{"id":"api:tasks:write@acme/widgets","attach":true}`,
		`{"id":"api:capabilities:admin","attach":true}`,
		`{"capability":"https://github.com/acme/caps/gcp#key","with":{"project":"p"},"attach":true}`,
	} {
		if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/tasks/2/capabilities", body, "testing"); code != http.StatusForbidden {
			t.Errorf("POST /api/tasks/2/capabilities with %s answered an agent %d: %s", body, code, resp)
		}
	}
	seedAPITask(t, client, "4", "acme/widgets", "api:capabilities:admin", "api:tasks:write", "api:tasks:read", "gcp-key")
	for _, body := range []string{
		`{"capabilities":["self-debug"]}`,
		`{"grants":[{"capability":"self-debug"}]}`,
		`{"priority":true}`,
		`{"priority":false}`,
		`{"keepRunning":true}`,
		`{"keepRunning":false}`,
		`{"keep_running":true}`,
	} {
		if code, resp := asAgent(t, srv, "4", http.MethodPatch, "/api/tasks/2", body, "testing"); code != http.StatusForbidden {
			t.Errorf("PATCH /api/tasks/2 with %s answered an agent %d: %s", body, code, resp)
		}
	}
	for _, body := range []string{
		`{"grants":[{"capability":"self-debug","via":"label"}]}`,
		`{"grants":[{"Capability":"self-debug"}]}`,
		`{"capabilities":[{"capability":"api:secrets:write","via":"label"}]}`,
	} {
		if code, resp := asAgent(t, srv, "4", http.MethodPatch, "/api/tasks/2", body, "testing"); code != http.StatusBadRequest && code != http.StatusForbidden {
			t.Errorf("PATCH /api/tasks/2 with %s answered an agent %d: %s", body, code, resp)
		}
	}
	for _, body := range []string{
		`{"id":"api:tasks:read","attach":true}`,
		`{"grant":"api:tasks:read@acme/widgets","attach":true}`,
		`{"id":"gcp-key","attach":true}`,
	} {
		if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/tasks/2/capabilities", body, "testing"); code != http.StatusOK {
			t.Errorf("POST /api/tasks/2/capabilities with %s, a grant the agent holds, answered %d: %s", body, code, resp)
		}
	}
	task, err := client.Store.GetTask(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range task.Grants {
		if !slices.Contains([]string{"api:tasks:read", "api:tasks:read@acme/widgets", "gcp-key"}, g.Capability) {
			t.Errorf("task 2 was granted %s by an agent", g.Capability)
		}
	}
}

// TestAgentAPICapabilitiesAdminMayDetachUnheldCapability verifies that an
// agent holding api:capabilities:admin can detach (attach: false) a capability
// from a task even when the acting task does not hold that capability, while
// still refusing attach: true or replace: <unheld>.
func TestAgentAPICapabilitiesAdminMayDetachUnheldCapability(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:capabilities:admin")
	capRef := `https://github.com/acme/caps/gcp#key={"project":"p"}`
	seedAPITask(t, client, "2", "acme/widgets", "grain-ci", "self-debug", capRef)

	for _, body := range []string{
		`{"id":"grain-ci","attach":true}`,
		`{"grant":"self-debug","attach":true}`,
		`{"capability":"https://github.com/acme/caps/gcp#key","with":{"project":"p"},"attach":true}`,
		`{"id":"grain-ci","attach":false,"replace":"self-debug"}`,
		`{"grant":"grain-ci","attach":false,"replace":"self-debug"}`,
	} {
		if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/capabilities", body, "testing"); code != http.StatusForbidden {
			t.Errorf("POST /api/tasks/2/capabilities with %s answered an agent %d, want 403: %s", body, code, resp)
		}
	}

	for _, body := range []string{
		`{"id":"grain-ci","attach":false}`,
		`{"grant":"self-debug","attach":false}`,
		`{"capability":"https://github.com/acme/caps/gcp#key","with":{"project":"p"},"attach":false}`,
	} {
		if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/capabilities", body, "testing"); code != http.StatusOK {
			t.Errorf("POST /api/tasks/2/capabilities detach with %s answered %d, want 200: %s", body, code, resp)
		}
	}

	task, err := client.Store.GetTask(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Grants) != 0 {
		t.Errorf("task 2 grants after detach = %v, want empty", task.Grants)
	}
}

// TestAgentAPICannotFileAroundTheProposalGate holds tasks:write to what
// propose_task allows a run: work it files or edits through the API
// cannot carry push or read access its own task lacks, auto-merge (or a
// Submit, which turns it on), its own approval, somebody else's name, a template's
// grants, or a move to a repository its grant does not reach.
func TestAgentAPICannotFileAroundTheProposalGate(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write", "api:schedules:write", "api:templates:write")
	seedAPITask(t, client, "2", "acme/widgets")
	seedAPITask(t, client, "3", "acme/widgets", "api:tasks:write@acme/widgets")
	ref, _ := model.ParseRepo("acme/widgets")
	if err := client.Store.PutTemplate(context.Background(), model.Template{ID: "tpl-key", Name: "keyed", Title: "keyed",
		Target: &ref, Grants: []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","writes":[{"repo":"acme/gadgets"}]}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","reads":["acme/secret"]}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","autoMerge":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","AutoMerge":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","autoApproveProposals":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","approved":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","author":"admin@example.com"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-key"}`},
		{http.MethodPatch, "/api/tasks/2", `{"autoMerge":true}`},
		{http.MethodPatch, "/api/tasks/2", `{"autoApproveProposals":true}`},
		{http.MethodPatch, "/api/tasks/2", `{"auto_approve_proposals":true}`},
		{http.MethodPost, "/api/tasks/2/submit", `{}`},
		// Mailing a CL and publishing its replies are a person's.
		{http.MethodPost, "/api/tasks/2/change-mail", `{"reviewers":["alice"]}`},
		{http.MethodPost, "/api/tasks/2/change-publish", `{}`},
		{http.MethodPost, "/api/tasks/2/change-sync", `{}`},
		{http.MethodPost, "/api/schedules", `{"title":"x","repo":"acme/widgets","writes":[{"repo":"acme/gadgets"}]}`},
		{http.MethodPost, "/api/templates", `{"name":"x","title":"x","autoMerge":true}`},
	} {
		if code, resp := asAgent(t, srv, "1", c.method, c.path, c.body, "testing"); code != http.StatusForbidden {
			t.Errorf("%s %s with %s answered an agent %d: %s", c.method, c.path, c.body, code, resp)
		}
	}
	// A repo-limited grant cannot move a task out of its repository, nor
	// vouch for a repository by naming it beside a resource it does not
	// own.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/tasks/2", `{"repo":"acme/gadgets"}`},
		{http.MethodPost, "/api/tasks/reorder?repo=acme/widgets", `{"ids":["2"]}`},
	} {
		if code, resp := asAgent(t, srv, "3", c.method, c.path, c.body, "testing"); code != http.StatusForbidden {
			t.Errorf("tasks:write@acme/widgets: %s %s with %s answered %d: %s", c.method, c.path, c.body, code, resp)
		}
	}
	// What its own task holds, it may pass on.
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks",
		`{"title":"follow-up","repo":"acme/widgets","autoMerge":false,"approved":false}`, "testing"); code >= 300 {
		t.Errorf("filing a task within the acting task's own grants was refused: %d %s", code, resp)
	}
}

// TestAgentAPIReviewTemplateWithinGrants: the review a task names
// (reviewTemplateId) is filed approved and auto-merge on that task's
// branch, so an agent may not name one carrying a grant or a repository
// its own task lacks -- on POST or PATCH alike. The repository's own
// default review, and a template within its grants, it may.
func TestAgentAPIReviewTemplateWithinGrants(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	// Ids past the ones the tasks it files are given (Store.NewTaskID).
	seedAPITask(t, client, "101", "acme/widgets", "api:tasks:write", "gcp-key")
	seedAPITask(t, client, "102", "acme/widgets")
	ref, _ := model.ParseRepo("acme/widgets")
	gadgets, _ := model.ParseRepo("acme/gadgets")
	for _, tmpl := range []model.Template{
		{ID: "tpl-upgrade", Name: "upgrade", Title: "upgrade", Grants: []model.Grant{{Capability: "api:deployment:upgrade", Via: model.GrantByLabel}}},
		{ID: "tpl-writes", Name: "writes", Title: "writes", Writes: []model.WriteTarget{{Repo: gadgets}}},
		{ID: "tpl-reads", Name: "reads", Title: "reads", Reads: []model.RepoRef{gadgets}},
		{ID: "tpl-other", Name: "other", Title: "other", Grants: []model.Grant{{Capability: "github-app", Via: model.GrantByLabel}}},
		{ID: "tpl-within", Name: "within", Title: "within", Grants: []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}},
		{ID: model.DefaultReviewTemplateID(ref), Name: "default review", Title: "review",
			Grants: []model.Grant{{Capability: "api:deployment:upgrade", Via: model.GrantByLabel}}},
	} {
		if err := client.Store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"tpl-upgrade", "tpl-writes", "tpl-reads", "tpl-other"} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","reviewTemplateId":"` + id + `"}`},
			{http.MethodPatch, "/api/tasks/102", `{"reviewTemplateId":"` + id + `"}`},
		} {
			if code, resp := asAgent(t, srv, "101", c.method, c.path, c.body, "testing"); code != http.StatusForbidden {
				t.Errorf("%s %s with %s answered an agent %d: %s", c.method, c.path, c.body, code, resp)
			}
		}
	}
	for _, id := range []string{"tpl-within", model.DefaultReviewTemplateID(ref)} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","reviewTemplateId":"` + id + `"}`},
			{http.MethodPatch, "/api/tasks/102", `{"reviewTemplateId":"` + id + `"}`},
		} {
			if code, resp := asAgent(t, srv, "101", c.method, c.path, c.body, "testing"); code >= 300 {
				t.Errorf("%s %s with %s was refused: %d %s", c.method, c.path, c.body, code, resp)
			}
		}
	}
}

func TestAgentAPIRails(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:task-admin", "api:sessions:write", "api:dispatch-control")
	for _, path := range []string{"/api/tasks/1/stop", "/api/tasks/1/close", "/api/tasks/1/retry", "/api/tasks/1/defer"} {
		if code, body := asAgent(t, srv, "1", http.MethodPost, path, `{}`, "testing"); code != http.StatusForbidden ||
			!strings.Contains(body, "itself") {
			t.Errorf("%s on the acting task's own run answered %d: %s", path, code, body)
		}
	}
	if code, _ := asAgent(t, srv, "1", http.MethodPost, "/api/console-sessions/director/restart", `{}`, "testing"); code != http.StatusForbidden {
		t.Errorf("an agent restarted a standing session it does not own: %d", code)
	}

	// A pause somebody else took is theirs to lift.
	if err := client.Store.SetPipelinePause(context.Background(), model.PipelinePause{
		Reason: "upgrade check", By: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, UntilTaskID: "99",
	}); err != nil {
		t.Fatal(err)
	}
	if code, body := asAgent(t, srv, "1", http.MethodDelete, "/api/pipeline-pause", ``, "testing"); code != http.StatusForbidden {
		t.Errorf("an agent lifted an upgrade hold: %d %s", code, body)
	}
}

// TestAgentAPIChildNotesItsDirector: the standing-session rail lets a
// director's child leave it a note (message_task) through the agent API,
// as the CLI and MCP hops already do, and nothing else: a task that is
// not the director's parent or child is still refused, and so is any
// other call the child makes on the director.
func TestAgentAPIChildNotesItsDirector(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	directorID, err := model.FileConsoleSessionTask(ctx, client.Store, model.DefaultConsoleSessions()[0],
		model.FileConsoleSessionOptions{Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "101", "acme/widgets", "api:tasks:write")
	child, err := client.Store.GetTask(ctx, "101")
	if err != nil || child == nil {
		t.Fatalf("GetTask(101) = %v, %v", child, err)
	}
	child.Links = append(child.Links, model.Link{Kind: model.LinkProposedBy, Target: directorID})
	if err := client.Store.PutTask(ctx, *child); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "102", "acme/widgets", "api:tasks:write")

	notes := "/api/tasks/" + directorID + "/notes"
	if code, body := asAgent(t, srv, "101", http.MethodPost, notes, `{"body":"finished the check"}`, "testing"); code != http.StatusOK {
		t.Fatalf("the director's child noting it answered %d: %s", code, body)
	}
	comments, err := client.Store.Comments(ctx, directorID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(comments, func(c model.Comment) bool {
		return c.Body == "finished the check" && c.Author.Actor == model.Principal{Kind: model.PrincipalAgent, ID: "101"}
	}) {
		t.Errorf("director comments = %+v, want the child's note", comments)
	}

	if code, body := asAgent(t, srv, "102", http.MethodPost, notes, `{"body":"do it my way"}`, "testing"); code != http.StatusForbidden ||
		!strings.Contains(body, "does not own") {
		t.Errorf("an unrelated task noting the director answered %d: %s", code, body)
	}
	if code, body := asAgent(t, srv, "101", http.MethodPost, "/api/tasks/"+directorID+"/comments", `{"body":"hi"}`, "testing"); code != http.StatusForbidden ||
		!strings.Contains(body, "does not own") {
		t.Errorf("the director's child commenting on it answered %d: %s", code, body)
	}
}

func TestAgentAPIAttributesTheCall(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write")
	seedAPITask(t, client, "2", "acme/widgets")
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/comments", `{"body":"from the director"}`, "triage"); code >= 300 {
		t.Fatalf("comment: %d %s", code, body)
	}
	comments, err := client.Store.Comments(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	var sawComment, sawRecord bool
	for _, c := range comments {
		if c.Body == "from the director" && c.Author.Actor == (model.Principal{Kind: model.PrincipalAgent, ID: "1"}) {
			sawComment = true
		}
		if strings.Contains(c.Body, "Task 1 called") && strings.Contains(c.Body, "triage") &&
			c.Author.OnBehalfOf != nil && *c.Author.OnBehalfOf == (model.Principal{Kind: model.PrincipalAgent, ID: "1"}) {
			sawRecord = true
		}
	}
	if !sawComment {
		t.Errorf("the comment was not written as {agent, 1}: %+v", comments)
	}
	if !sawRecord {
		t.Errorf("the call was not recorded on task 2's conversation: %+v", comments)
	}
}

// TestAgentAPIPassesRepoPersonGateWhenConfigIsStored verifies that routes
// guarded by checkRepoPerson / requireRepoPerson admit an agent task whose
// call agentAPIGate authorized for the route's scope, even when Store holds a
// non-nil model.Config row, while checkReleaseRef and withholdReleaseSecrets
// continue to hold PrincipalAgent callers to reviewed branches and withhold
// release secrets on arbitrary /api/ci/runs calls.
func TestAgentAPIPassesRepoPersonGateWhenConfigIsStored(t *testing.T) {
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets"}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "base-1", Commit: "abc123abc123abc",
		StartedAt: baseTime, Conclusion: string(ci.ConclusionCancelled), Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelease(ctx, repo, "2.1", baseTime); err != nil {
		t.Fatal(err)
	}

	seedAPITask(t, client, "1", "acme/widgets", "api:ci:write")
	seedAPITask(t, client, "2", "acme/widgets", "api:releases:write")
	seedAPITask(t, client, "3", "acme/widgets", "api:repos:write")
	seedAPITask(t, client, "4", "acme/widgets", "api:secrets:write")

	// POST /api/repos/{owner}/{name}/release-runs: allowed on watched base
	// branch and open release branches, refused on task branch or pinned commit.
	for _, branch := range []string{"main", "2.1", "2.1.latest"} {
		before := len(runner.started)
		code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"`+branch+`"}`, "")
		if code != http.StatusAccepted {
			t.Fatalf("release-runs on %s: %d %s, want 202", branch, code, body)
		}
		if len(runner.started) != before+1 || !runner.started[before].TrustedRelease || runner.started[before].RequestedBy != "1" {
			t.Fatalf("release-runs on %s started = %+v", branch, runner.started[before:])
		}
	}
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"grain/task-1"}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "grain/task-1 is not a branch") {
		t.Errorf("release-runs on unreviewed branch: %d %s, want 403 from checkReleaseRef", code, body)
	}
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/release-runs", `{"branch":"main","commit":"abc123"}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "only a person may pin") {
		t.Errorf("release-runs with pinned commit: %d %s, want 403 from checkReleaseRef", code, body)
	}

	// POST /api/ci/runs: release secrets remain withheld for PrincipalAgent
	// on release and workflow_dispatch events.
	for _, ev := range []string{"release", "workflow_dispatch"} {
		before := len(runner.started)
		code, body := asAgent(t, srv, "1", http.MethodPost, "/api/ci/runs",
			`{"repo":"acme/widgets","branch":"grain/task-1","event":"`+ev+`"}`, "")
		if code != http.StatusAccepted {
			t.Fatalf("/api/ci/runs (%s): %d %s, want 202", ev, code, body)
		}
		got := runner.started[before]
		if !got.WithholdReleaseSecrets || !strings.Contains(got.WithholdReason, "not a person") {
			t.Errorf("/api/ci/runs (%s): WithholdReleaseSecrets=%v WithholdReason=%q, want withheld", ev, got.WithholdReleaseSecrets, got.WithholdReason)
		}
	}

	// POST /api/repos/{owner}/{name}/base-branch-runs (ci:write).
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/repos/acme/widgets/base-branch-runs", `{"branch":"main"}`, ""); code != http.StatusAccepted {
		t.Errorf("base-branch-runs: %d %s, want 202", code, body)
	}

	// POST /api/repos/{owner}/{name}/releases and PUT /api/repos/{owner}/{name}/release (releases:write).
	if code, body := asAgent(t, srv, "2", http.MethodPost, "/api/repos/acme/widgets/releases", `{"name":"2.2"}`, ""); code != http.StatusCreated {
		t.Errorf("create release: %d %s, want 201", code, body)
	}
	if code, body := asAgent(t, srv, "2", http.MethodPut, "/api/repos/acme/widgets/release", `{"enabled":false}`, ""); code != http.StatusOK {
		t.Errorf("put repo release settings: %d %s, want 200", code, body)
	}

	// PUT /api/repos/{owner}/{name}/prompt-extension and POST /api/repos/{owner}/{name}/branches (repos:write).
	if code, body := asAgent(t, srv, "3", http.MethodPut, "/api/repos/acme/widgets/prompt-extension", `{"promptExtension":"agent note"}`, ""); code != http.StatusOK {
		t.Errorf("put prompt-extension: %d %s, want 200", code, body)
	}
	if code, body := asAgent(t, srv, "3", http.MethodPost, "/api/repos/acme/widgets/branches", `{"name":"feature-agent"}`, ""); code != http.StatusCreated {
		t.Errorf("create branch: %d %s, want 201", code, body)
	}

	// PUT /api/repos/{owner}/{name}/release-secrets/{secret} (secrets:write).
	if code, body := asAgent(t, srv, "4", http.MethodPut, "/api/repos/acme/widgets/release-secrets/NPM_TOKEN", `{"value":"s3cr3t"}`, "rotate token"); code != http.StatusOK {
		t.Errorf("put release secret: %d %s, want 200", code, body)
	}
}

// TestAgentAPIWriteRefusesAStaleExpectation is a task-admin write made
// through the generated tool with an expect argument
// (docs/design/durable-tasks.md#DT-13): the tool reads the task first and
// refuses, changing nothing, when the record disagrees.
func TestAgentAPIWriteRefusesAStaleExpectation(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:task-admin")
	seedAPITask(t, client, "2", "acme/widgets")
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)

	var comment mcp.Tool
	for _, tool := range ui.AgentAPITools(serverAccessClient(srv, daemon.URL), "1", []string{"api:task-admin"}) {
		for _, r := range ui.AgentAPIRoutes() {
			if r.Pattern == "POST /api/tasks/{id}/comments" && r.Tool == tool.Name {
				comment = tool
			}
		}
	}
	if comment.Handler == nil {
		t.Fatal("no tool for POST /api/tasks/{id}/comments")
	}
	if _, ok := comment.InputSchema["properties"].(map[string]any)["expect"]; !ok {
		t.Fatalf("the comment tool takes no expect: %v", comment.InputSchema)
	}
	count := func() int {
		comments, err := client.Store.Comments(context.Background(), "2")
		if err != nil {
			t.Fatal(err)
		}
		return len(comments)
	}
	before := count()
	res := comment.Handler(t.Context(), map[string]any{"id": "2", "body": map[string]any{"body": "stale"},
		"expect": map[string]any{"state": "running", "kind": "port"}})
	if !res.IsError || !strings.Contains(res.Text, "not running") || !strings.Contains(res.Text, "kind is proposal, not port") {
		t.Fatalf("a stale expectation was not refused: %+v", res)
	}
	if count() != before {
		t.Fatal("a refused call still wrote its comment")
	}
	res = comment.Handler(t.Context(), map[string]any{"id": "2", "body": map[string]any{"body": "current"},
		"expect": map[string]any{"title_prefix": "task 2", "kind": "proposal"}})
	if res.IsError {
		t.Fatalf("a matching expectation was refused: %s", res.Text)
	}
	if count() == before {
		t.Fatal("the call that matched wrote nothing")
	}
}

// TestAgentAPIExpectReadsOnlyWhatTheRunMayRead is expect's read made as
// the acting task, through the gate: a run holding tasks:write but no
// tasks:read is refused the read, and the refusal says nothing of the
// task -- where a trusted read would have echoed its title and state back
// in the mismatch.
func TestAgentAPIExpectReadsOnlyWhatTheRunMayRead(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write")
	seedAPITask(t, client, "2", "acme/widgets")
	daemon := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)

	var comment mcp.Tool
	for _, tool := range ui.AgentAPITools(serverAccessClient(srv, daemon.URL), "1", []string{"api:tasks:write"}) {
		for _, r := range ui.AgentAPIRoutes() {
			if r.Pattern == "POST /api/tasks/{id}/comments" && r.Tool == tool.Name {
				comment = tool
			}
		}
	}
	if comment.Handler == nil {
		t.Fatal("no tool for POST /api/tasks/{id}/comments")
	}
	res := comment.Handler(t.Context(), map[string]any{"id": "2", "body": map[string]any{"body": "probe"},
		"expect": map[string]any{"title_prefix": "zzz"}})
	if !res.IsError || !strings.Contains(res.Text, "tasks:read") || strings.Contains(res.Text, "task 2\"") {
		t.Fatalf("expect read a task the run holds no tasks:read for: %s", res.Text)
	}
	comments, err := client.Store.Comments(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, "probe") {
			t.Fatal("a refused call still wrote its comment")
		}
	}
}

// tasks:from-template is the narrow scope a CI job's token holds
// (pkg/capability/ciapi); an agent calling it is also held to POST
// /api/tasks's own gate, so the narrow scope never files what
// tasks:write would not.
func TestAgentAPIFromTemplateIsHeldToTaskFilingsOwnGate(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:from-template")
	seedAPITask(t, client, "2", "acme/widgets", "api:tasks:write")
	body := `{"repo":"acme/widgets","template":"release-notes"}`
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/from-template", body, ""); code != http.StatusForbidden ||
		!strings.Contains(resp, "tasks:write") {
		t.Errorf("tasks:from-template alone filed a task: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "2", http.MethodPost, "/api/tasks/from-template", body, ""); code != http.StatusForbidden ||
		!strings.Contains(resp, "tasks:from-template") {
		t.Errorf("tasks:write alone reached from-template: %d %s", code, resp)
	}
}

type staticCISource struct {
	workflows []ci.Workflow
	commit    string
}

func (s staticCISource) Workflows(context.Context, string, string) ([]ci.Workflow, string, error) {
	return s.workflows, s.commit, nil
}

type recordingCISandboxes struct {
	mu       sync.Mutex
	acquired []ci.SandboxRequest
}

func (r *recordingCISandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	r.mu.Lock()
	r.acquired = append(r.acquired, req)
	r.mu.Unlock()
	return noopCISandbox{name: req.Name}, nil
}

type noopCISandbox struct{ name string }

func (s noopCISandbox) Name() string { return s.name }
func (noopCISandbox) Exec(context.Context, ci.Command) (ci.CommandResult, error) {
	return ci.CommandResult{}, nil
}
func (noopCISandbox) Release(context.Context) error { return nil }

// An agent holding api:task-admin (which carries ci:write) posting
// POST /api/ci/runs with event: workflow_dispatch on a branch whose
// workflow declares repo-write:<repo> and package-publish gets a run
// whose job records both as unmet and resolves no capability; the same
// request from a human principal does not withhold them.
func TestAgentAPIWorkflowDispatchWithholdsEventGatedCapabilities(t *testing.T) {
	w, err := ci.Parse(".grain/workflows/x.yml", []byte(
		"on: workflow_dispatch\n"+
			"jobs:\n"+
			"  publish:\n"+
			"    cache: false\n"+
			"    capabilities: [\"repo-write:acme/widgets\", package-publish]\n"+
			"    steps:\n"+
			"      - run: true\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	sandboxes := &recordingCISandboxes{}
	finished := make(chan string, 8)
	service := &ci.Service{
		Source: staticCISource{workflows: []ci.Workflow{w}, commit: "abc1234"},
		Engine: &ci.Engine{Sandboxes: sandboxes},
		Capabilities: func(context.Context, string) ([]string, error) {
			return []string{"package-publish"}, nil
		},
		Log: func(format string, args ...any) {
			if line := fmt.Sprintf(format, args...); strings.Contains(line, "ci: run ") {
				id, _, _ := strings.Cut(strings.TrimPrefix(line, "ci: run "), ":")
				select {
				case finished <- id:
				default:
				}
			}
		},
	}
	awaitRun := func(id string) ci.Run {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			if run, ok := service.Get(id); ok && run.Conclusion.Done() {
				return run
			}
			select {
			case <-finished:
			case <-deadline:
				t.Fatalf("timed out waiting for CI run %s", id)
			}
		}
	}

	srv, client, store := releaseServer(t, service)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:           repo,
		CIMode:         model.CIModeGrain,
		CICapabilities: []string{"package-publish"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "admin-agent", "acme/widgets", "api:task-admin")

	reqBody := `{"repo":"acme/widgets","ref":"grain/049f1e09/task-1359","event":"workflow_dispatch","workflow":".grain/workflows/x.yml"}`

	// 1. Agent holding api:task-admin: both repo-write:acme/widgets and
	// package-publish are recorded as unmet, and no sandbox or capability is acquired.
	code, resp := asAgent(t, srv, "admin-agent", http.MethodPost, "/api/ci/runs", reqBody, "")
	if code != http.StatusAccepted {
		t.Fatalf("agent POST /api/ci/runs: %d %s, want 202", code, resp)
	}
	var started ci.Run
	if err := json.Unmarshal([]byte(resp), &started); err != nil {
		t.Fatal(err)
	}
	agentRun := awaitRun(started.ID)
	if len(agentRun.Jobs) != 1 || agentRun.Jobs[0].Conclusion != ci.ConclusionErrored {
		t.Fatalf("agent run jobs = %+v, want 1 errored job", agentRun.Jobs)
	}
	jobErr := agentRun.Jobs[0].Error
	for _, capID := range []string{`"repo-write:acme/widgets"`, `"package-publish"`} {
		if !strings.Contains(jobErr, capID) || !strings.Contains(jobErr, "not a person") {
			t.Errorf("agent job error = %q, want unmet requirement naming %s and 'not a person'", jobErr, capID)
		}
	}
	sandboxes.mu.Lock()
	nAcquired := len(sandboxes.acquired)
	sandboxes.mu.Unlock()
	if nAcquired != 0 {
		t.Fatalf("agent run acquired %d sandboxes, want 0", nAcquired)
	}

	// 2. Human principal making the same request: neither capability is withheld.
	rec := doAs(t, srv, "operator@example.com", http.MethodPost, "/api/ci/runs", reqBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("human POST /api/ci/runs: %d %s, want 202", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	humanRun := awaitRun(started.ID)
	if len(humanRun.Jobs) != 1 || humanRun.Jobs[0].Conclusion != ci.ConclusionSuccess {
		t.Fatalf("human run jobs = %+v, want 1 succeeded job", humanRun.Jobs)
	}
	sandboxes.mu.Lock()
	defer sandboxes.mu.Unlock()
	if len(sandboxes.acquired) != 1 ||
		!slices.Equal(sandboxes.acquired[0].Capabilities, []string{"repo-write:acme/widgets", "package-publish"}) ||
		sandboxes.acquired[0].WithholdEventGated {
		t.Fatalf("human run acquired = %+v, want [repo-write:acme/widgets package-publish] with WithholdEventGated=false",
			sandboxes.acquired)
	}
}

// sandboxes:read was split out of deployment:read; a grant of the wider
// scope still reaches the persistent sandboxes.
func TestAgentAPIDeploymentReadStillReadsPersistentSandboxes(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:deployment:read")
	seedAPITask(t, client, "2", "acme/widgets", "api:tasks:read")
	if code, resp := asAgent(t, srv, "1", http.MethodGet, "/api/persistent-sandboxes", "", ""); code == http.StatusForbidden {
		t.Errorf("api:deployment:read was refused /api/persistent-sandboxes: %s", resp)
	}
	if code, _ := asAgent(t, srv, "2", http.MethodGet, "/api/persistent-sandboxes", "", ""); code != http.StatusForbidden {
		t.Errorf("api:tasks:read read /api/persistent-sandboxes: %d", code)
	}
}

// TestAgentAPICanNarrowExistingCapabilityLists verifies that an agent with
// api:repos:write or api:settings:write can remove a capability from an
// existing whole-list replacement field (such as ciCapabilities or
// defaultCapabilities, including CI-only capabilities like package-publish)
// without holding every remaining capability already stored on that row, while
// still being refused if it tries to add a capability it does not hold or copy
// a capability from ciCapabilities into defaultCapabilities.
func TestAgentAPICanNarrowExistingCapabilityLists(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.PollInterval = 30_000_000_000
	cfg.GitHubHost = "github.com"
	cfg.TargetRepos = []string{"acme/widgets", "git.forge.example/experimental/ggrain"}
	cfg.DefaultCapabilities = []string{"gemini-key", "gcp-key"}
	if err := client.Store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		CICapabilities:      []string{"gemini-key", "gcp-key", "package-publish"},
		DefaultCapabilities: []string{"gemini-key", "gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}
	ggrain := model.RepoRef{Host: "git.forge.example", Owner: "experimental", Name: "ggrain"}
	if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:           ggrain,
		CICapabilities: []string{"package-publish", "gemini-key", "gcp-key"},
	}); err != nil {
		t.Fatal(err)
	}

	// Task 1 holds only api:repos:write and api:settings:write (and
	// api:capabilities:admin for /capabilities and /ci-capabilities) — not
	// gemini-key, gcp-key, or the CI-only package-publish.
	seedAPITask(t, client, "1", "acme/widgets", "api:repos:write", "api:settings:write", "api:capabilities:admin")

	// 1. Narrowing ciCapabilities via PUT /api/repos/{owner}/{name}/settings
	// succeeds even though gemini-key and CI-only package-publish remain.
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/settings",
		`{"ciCapabilities":["gemini-key","package-publish"]}`, "remove gcp-key"); code != http.StatusOK {
		t.Fatalf("PUT /api/repos/acme/widgets/settings narrowing ciCapabilities: %d %s", code, resp)
	}
	rc, err := client.Store.GetRepoConfig(ctx, widgets)
	if err != nil || rc == nil || !slices.Equal(rc.CICapabilities, []string{"gemini-key", "package-publish"}) {
		t.Fatalf("stored ciCapabilities = %+v, %v, want [gemini-key package-publish]", rc, err)
	}

	// 2. Adding back gcp-key (which is no longer stored and not held by task 1) is refused.
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/settings",
		`{"ciCapabilities":["gemini-key","package-publish","gcp-key"]}`, "re-add gcp-key"); code != http.StatusForbidden ||
		!strings.Contains(resp, "gcp-key") {
		t.Fatalf("re-adding unheld gcp-key: got %d %s, want 403 naming gcp-key", code, resp)
	}

	// 3. Narrowing defaultCapabilities via PUT /api/repos/{owner}/{name}/settings,
	// PUT /api/repos/{owner}/{name}/capabilities, and PUT /api/deployment/repos/{owner}/{name}/config.
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/settings",
		`{"defaultCapabilities":["gemini-key"]}`, "remove gcp-key from defaultCapabilities"); code != http.StatusOK {
		t.Fatalf("PUT /api/repos/acme/widgets/settings narrowing defaultCapabilities: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/capabilities",
		`{"defaultCapabilities":["gemini-key"]}`, "keep gemini-key"); code != http.StatusOK {
		t.Fatalf("PUT /api/repos/acme/widgets/capabilities keeping stored gemini-key: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/deployment/repos/acme/widgets/config",
		`{"ciCapabilities":["package-publish"],"defaultCapabilities":[]}`, "narrow via deployment config"); code != http.StatusOK {
		t.Fatalf("PUT /api/deployment/repos/acme/widgets/config narrowing lists: %d %s", code, resp)
	}

	// 4. Cross-field escalation is refused: package-publish is stored in
	// ciCapabilities, not defaultCapabilities, so putting it in defaultCapabilities is refused.
	if code, _ := asAgent(t, srv, "1", http.MethodPut, "/api/repos/acme/widgets/settings",
		`{"defaultCapabilities":["package-publish"]}`, "cross-field"); code != http.StatusForbidden {
		t.Fatalf("copying ciCapabilities entry into defaultCapabilities answered %d, want 403", code)
	}

	// 5. Host-qualified repo via PUT /api/repos/{owner}/{name}/ci-capabilities and /settings.
	if code, resp := asAgent(t, srv, "1", http.MethodPut,
		"/api/repos/git.forge.example%2Fexperimental/ggrain/ci-capabilities",
		`{"ciCapabilities":["package-publish","gemini-key"]}`, "remove gcp-key"); code != http.StatusOK {
		t.Fatalf("PUT host-qualified /ci-capabilities narrowing: %d %s", code, resp)
	}
	rcGgrain, err := client.Store.GetRepoConfig(ctx, ggrain)
	if err != nil || rcGgrain == nil || !slices.Equal(rcGgrain.CICapabilities, []string{"package-publish", "gemini-key"}) {
		t.Fatalf("ggrain ciCapabilities = %+v, %v, want [package-publish gemini-key]", rcGgrain, err)
	}

	// 6. Deployment-wide defaultCapabilities via PUT /api/settings.
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/settings",
		`{"defaultCapabilities":["gemini-key"]}`, "remove gcp-key from deployment defaults"); code != http.StatusOK {
		t.Fatalf("PUT /api/settings narrowing defaultCapabilities: %d %s", code, resp)
	}
	if code, _ := asAgent(t, srv, "1", http.MethodPut, "/api/settings",
		`{"defaultCapabilities":["gemini-key","gcp-key"]}`, "re-add gcp-key"); code != http.StatusForbidden {
		t.Fatalf("PUT /api/settings re-adding unheld gcp-key answered %d, want 403", code)
	}
}

func asSandboxAgent(t *testing.T, srv *ui.Server, id, runID, method, path, body, reason string) (int, string) {
	t.Helper()
	headers := map[string]string{
		ui.ActingTaskHeader: id,
		ui.ActingRunHeader:  runID,
		ui.ChannelHeader:    ui.SandboxCLIChannel,
		"Content-Type":      "application/json",
	}
	if reason != "" {
		headers[ui.ActingReasonHeader] = reason
	}
	rec := doWithHeaders(t, srv, method, path, body, headers)
	return rec.Code, rec.Body.String()
}

func TestSandboxCLIChannelAllowsNonDangerousScopesAndRefusesDangerousAndEscalation(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets",
		"api:tasks:read", "api:tasks:write", "api:tasks:admin",
		"api:merge:write", "api:merge:force",
		"api:settings:write", "api:connections:write", "api:secrets:write",
		"api:capabilities:admin", "api:deployment:restart", "api:dispatch:write",
		"api:repos:write", "api:sessions:write", "api:memory:write", "self-debug")
	seedAPITask(t, client, "2", "acme/widgets")

	// Non-dangerous read and comment write succeed over sandbox-cli.
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodGet, "/api/tasks/2", "", ""); code != http.StatusOK {
		t.Fatalf("GET /api/tasks/2 over sandbox-cli: %d %s", code, resp)
	}
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, "/api/tasks/2/comments",
		`{"body":"note from sandbox cli"}`, ""); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("POST /api/tasks/2/comments over sandbox-cli: %d %s", code, resp)
	}

	// Dangerous and model-only scopes are refused with HTTP 403 even when the task holds them.
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/tasks/2/approve", `{}`},
		{http.MethodPut, "/api/settings", `{"maxConcurrent":2}`},
		{http.MethodPost, "/api/tasks/2/run-now", `{}`},
		{http.MethodPut, "/api/repos/acme/widgets/settings", `{"autoMerge":false}`},
		{http.MethodPost, "/api/deployment/git-connections", `{"id":"c1"}`},
		{http.MethodPost, "/api/tasks/2/force-merge", `{}`},
		{http.MethodPut, "/api/tasks/2/secret", `{"value":"s"}`},
	} {
		code, resp := asSandboxAgent(t, srv, "1", "run-1", tc.method, tc.path, tc.body, "testing sandbox gate")
		if code != http.StatusForbidden || !strings.Contains(resp, "not permitted on the sandbox-cli channel") {
			t.Errorf("%s %s over sandbox-cli: got %d %s, want 403 refusing dangerous/model-only scope", tc.method, tc.path, code, resp)
		}
	}

	// Capability attachment and supersedes escalation via tasks:write are refused on sandbox-cli.
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPatch, "/api/tasks/2",
		`{"capabilities":["self-debug"]}`, "escalate"); code != http.StatusForbidden || !strings.Contains(resp, "capabilities") {
		t.Errorf("PATCH /api/tasks/2 capabilities over sandbox-cli: %d %s, want 403", code, resp)
	}
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, "/api/tasks/2/capabilities",
		`{"capability":"self-debug"}`, "escalate"); code != http.StatusForbidden || !strings.Contains(resp, "capabilities") {
		t.Errorf("POST /api/tasks/2/capabilities over sandbox-cli: %d %s, want 403", code, resp)
	}
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, "/api/tasks",
		`{"title":"dup","description":"b","repo":"acme/widgets","supersedes":"2"}`, "supersede"); code != http.StatusForbidden || !strings.Contains(resp, "supersedes is not permitted") {
		t.Errorf("POST /api/tasks with supersedes over sandbox-cli: %d %s, want 403", code, resp)
	}

	// Agent filing escalation refuses proposalKey tag injection (even with tasks:admin)
	// and refuses interactive auto-approval without tasks:admin.
	seedAPITask(t, client, "3", "acme/widgets", "api:tasks:write")
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/tasks",
		`{"title":"chat","description":"b","repo":"acme/widgets","interactive":true}`, ""); code != http.StatusForbidden || !strings.Contains(resp, "tasks:admin") {
		t.Errorf("POST /api/tasks with interactive=true without tasks:admin: %d %s, want 403", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/tasks",
		`{"title":"tag-spoof","description":"b","repo":"acme/widgets","proposalKey":"console-session:director"}`, "spoof"); code != http.StatusForbidden || !strings.Contains(resp, "proposalKey") {
		t.Errorf("POST /api/tasks with proposalKey by agent: %d %s, want 403", code, resp)
	}

	// SandboxCLIActions.ProposeTask hashes --id with a proposal: prefix (preventing system tag injection),
	// resolves --depends-on <local-id> across calls in the same run, and defaults Base to "".
	cliActions := ui.SandboxCLIActions{Client: client}
	ctx := context.Background()
	p1, err := cliActions.ProposeTask(ctx, "1", "run-1", gitproxy.CLIProposeRequest{
		ID:    "console-session:director",
		Title: "Step 1",
		Body:  "First piece",
	})
	if err != nil {
		t.Fatalf("ProposeTask step 1: %v", err)
	}
	stored1, err := client.Store.GetTask(ctx, p1.ID)
	if err != nil || stored1 == nil {
		t.Fatalf("GetTask(%s): %v, %v", p1.ID, stored1, err)
	}
	if slices.Contains(stored1.Tags, "console-session:director") {
		t.Errorf("ProposeTask injected raw --id into task.Tags: %v", stored1.Tags)
	}
	if _, ok := model.ConsoleSessionIDOf(*stored1); ok {
		t.Errorf("proposed task spoofed a standing console session via --id: %v", stored1.Tags)
	}
	p2, err := cliActions.ProposeTask(ctx, "1", "run-1", gitproxy.CLIProposeRequest{
		Title:     "Step 2",
		Body:      "Depends on step 1 by local ID",
		DependsOn: []string{"console-session:director"},
	})
	if err != nil {
		t.Fatalf("ProposeTask step 2: %v", err)
	}
	stored2, err := client.Store.GetTask(ctx, p2.ID)
	if err != nil || stored2 == nil {
		t.Fatalf("GetTask(%s): %v, %v", p2.ID, stored2, err)
	}
	if !slices.Contains(stored2.Links, model.Link{Kind: model.LinkDependsOn, Target: p1.ID}) {
		t.Errorf("Step 2 links = %v, want LinkDependsOn -> %s", stored2.Links, p1.ID)
	}

	// SandboxAPIRoutes includes PUT /api/memory/documents when holding "memory",
	// includes granted non-dangerous API routes, and excludes model-only routes.
	memRoutes := ui.SandboxAPIRoutes([]string{"memory"}, "")
	hasDocWrite := false
	for _, r := range memRoutes {
		if r.Pattern == "PUT /api/memory/documents" {
			hasDocWrite = true
		}
	}
	if !hasDocWrite {
		t.Errorf("SandboxAPIRoutes([memory]) missing PUT /api/memory/documents: %+v", memRoutes)
	}
	taskRoutes := ui.SandboxAPIRoutes([]string{"api:tasks:read", "api:tasks:write", "api:tasks:admin"}, "")
	hasListTasks := false
	for _, r := range taskRoutes {
		if r.Pattern == "GET /api/tasks" {
			hasListTasks = true
		}
		if r.Scope == "tasks:admin" || r.Pattern == "POST /api/tasks/{id}/messages" ||
			r.Pattern == "POST /api/tasks/{id}/park-until" || r.Pattern == "POST /api/tasks/{id}/resume-later" {
			t.Errorf("SandboxAPIRoutes included denied or model-only route %s (scope %s)", r.Pattern, r.Scope)
		}
	}
	if !hasListTasks {
		t.Errorf("SandboxAPIRoutes([api:tasks:read ...]) missing GET /api/tasks: %+v", taskRoutes)
	}
}

// TestSandboxCLIChannelRefusesModelOnlyControlRoutes pins
// sandboxChannelGuard: speaking to an operator and parking a run are the
// model's control tools, never the sandbox CLI's, even for a run whose
// grants (api:tasks:admin among them) reach the route on any other task.
func TestSandboxCLIChannelRefusesModelOnlyControlRoutes(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:read", "api:tasks:write", "api:tasks:admin")
	seedAPITask(t, client, "2", "acme/widgets")
	seedAPITask(t, client, "3", "acme/widgets")

	for _, tc := range []struct {
		path, body string
	}{
		{"/api/tasks/2/messages", `{"body":"hello from the sandbox"}`},
		{"/api/tasks/2/park-until", `{"until":"2030-01-01T00:00:00Z"}`},
		{"/api/tasks/2/resume-later", `{"when":{"at":"1h"}}`},
		{"/api/tasks/2/wait-on", `{"ids":["3"],"park":true}`},
	} {
		code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, tc.path, tc.body, "testing sandbox guard")
		if code != http.StatusForbidden || !strings.Contains(resp, "model-only control action") {
			t.Errorf("POST %s %s over sandbox-cli: got %d %s, want 403 refusing a model-only control action", tc.path, tc.body, code, resp)
		}
	}
	// A blocking wait is the CLI's own (grain task wait): only park is refused.
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPost, "/api/tasks/2/wait-on",
		`{"ids":["3"]}`, ""); code != http.StatusOK {
		t.Errorf("POST /api/tasks/2/wait-on without park over sandbox-cli: got %d %s, want 200", code, resp)
	}
}

// TestAgentAPIReleasesWriteDoesNotWriteReleaseNotes pins that the
// release-manager scope reads the deployment's weekly release notes but
// cannot write them, and that memory:write cannot write them either:
// writing stays with release-notes:write (and the api:release-notes preset),
// on either channel.
func TestAgentAPIReleasesWriteDoesNotWriteReleaseNotes(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:releases:read", "api:releases:write")
	seedAPITask(t, client, "2", "acme/widgets", "api:memory")
	body := `{"period":"2026-W43","title":"t","text":"x"}`
	if code, resp := asAgent(t, srv, "1", http.MethodPut, "/api/release-notes", body, ""); code != http.StatusForbidden {
		t.Errorf("api:releases:write wrote release notes: %d %s, want 403", code, resp)
	}
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPut, "/api/release-notes", body, ""); code != http.StatusForbidden {
		t.Errorf("api:releases:write wrote release notes over sandbox-cli: %d %s, want 403", code, resp)
	}
	if code, resp := asAgent(t, srv, "2", http.MethodPut, "/api/release-notes", body, ""); code != http.StatusForbidden {
		t.Errorf("api:memory wrote release notes: %d %s, want 403", code, resp)
	}
	if code, resp := asSandboxAgent(t, srv, "2", "run-1", http.MethodPut, "/api/release-notes", body, ""); code != http.StatusForbidden {
		t.Errorf("api:memory wrote release notes over sandbox-cli: %d %s, want 403", code, resp)
	}
	if code, resp := asAgent(t, srv, "1", http.MethodGet, "/api/release-notes", "", ""); code == http.StatusForbidden {
		t.Errorf("api:releases:read was refused reading release notes: %d %s", code, resp)
	}
	for _, r := range ui.SandboxAPIRoutes([]string{"api:releases:write", "api:memory"}, "") {
		if r.Pattern == "PUT /api/release-notes" {
			t.Errorf("SandboxAPIRoutes([api:releases:write api:memory]) lists PUT /api/release-notes")
		}
	}
}

// TestAgentReleaseNoteWriteIsAttributedToTheAgent: a run that may write
// the release notes (the release-notes capability) over the sandbox
// channel is refused naming a person as their author, and a write that
// names nobody is recorded as the acting run itself.
func TestAgentReleaseNoteWriteIsAttributedToTheAgent(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "release-notes")
	spoofed := `{"period":"2026-W43","title":"t","text":"x","author":"alice@example.com","authorKind":"human"}`
	if code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPut, "/api/release-notes", spoofed, ""); code != http.StatusForbidden {
		t.Errorf("an agent naming a person as the release notes' author: %d %s, want 403", code, resp)
	}
	body := `{"period":"2026-W43","title":"t","text":"x"}`
	code, resp := asSandboxAgent(t, srv, "1", "run-1", http.MethodPut, "/api/release-notes", body, "")
	if code != http.StatusOK {
		t.Fatalf("release-notes capability writing release notes over sandbox-cli: %d %s, want 200", code, resp)
	}
	var note ui.ReleaseNote
	if err := json.Unmarshal([]byte(resp), &note); err != nil {
		t.Fatalf("decoding %s: %v", resp, err)
	}
	if note.UpdatedByKind != string(model.PrincipalAgent) || note.UpdatedBy != "1" {
		t.Errorf("release note written by task 1 is attributed to %s %q, want agent \"1\"", note.UpdatedByKind, note.UpdatedBy)
	}
}

// A CI run is its repository's: a task holding ci:read for some
// repositories reads a run in one of them by id, and a run elsewhere is
// refused; a listing that names no repository is narrowed to the one
// the grant reaches rather than refused.
func TestAgentAPIRepoScopedCIReadReachesRunsByID(t *testing.T) {
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "run-w", Repo: "acme/widgets", Ref: "grain/task-2"},
		{ID: "run-g", Repo: "acme/gadgets", Ref: "grain/task-3"},
	}}
	client, _, _ := testClient(t)
	client.Config.CI = runner
	srv := ui.NewServerWithClient(client)
	seedAPITask(t, client, "1", "acme/widgets", "api:ci:read@acme/widgets")

	if code, body := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs/run-w", "", ""); code != http.StatusOK {
		t.Errorf("ci:read@acme/widgets was refused a run in acme/widgets: %d %s", code, body)
	}
	if code, body := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs/run-g", "", ""); code != http.StatusForbidden ||
		!strings.Contains(body, "acme/gadgets") {
		t.Errorf("ci:read@acme/widgets read a run in acme/gadgets: %d %s", code, body)
	}
	code, body := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs", "", "")
	if code != http.StatusOK || !strings.Contains(body, "run-w") || strings.Contains(body, "run-g") {
		t.Errorf("GET /api/ci/runs without ?repo= was not narrowed to acme/widgets: %d %s", code, body)
	}
}

// grain-ci reads CI over the agent API for the repositories its task's
// runs may read -- its own and its reads -- as it does over /fs/ci/, and
// for no other.
func TestAgentAPIGrainCIReadsCIOfTheTasksRepositories(t *testing.T) {
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "run-w", Repo: "acme/widgets", Ref: "grain/task-2"},
		{ID: "run-l", Repo: "acme/library", Ref: "main"},
		{ID: "run-g", Repo: "acme/gadgets", Ref: "grain/task-3"},
	}}
	client, _, _ := testClient(t)
	client.Config.CI = runner
	srv := ui.NewServerWithClient(client)
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := client.Store.PutTask(context.Background(), model.Task{
		ID: "1", Intent: model.IntentImplement, Title: "task 1", Target: &target,
		Reads:  []model.RepoRef{{Owner: "acme", Name: "library"}},
		Grants: []model.Grant{{Capability: "grain-ci", Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"run-w", "run-l"} {
		if code, body := asSandboxAgent(t, srv, "1", "", http.MethodGet, "/api/ci/runs/"+id, "", ""); code != http.StatusOK {
			t.Errorf("grain-ci was refused %s: %d %s", id, code, body)
		}
	}
	if code, body := asSandboxAgent(t, srv, "1", "", http.MethodGet, "/api/ci/runs/run-g", "", ""); code != http.StatusForbidden {
		t.Errorf("grain-ci read a run in a repository its task does not read: %d %s", code, body)
	}
	code, body := asSandboxAgent(t, srv, "1", "", http.MethodGet, "/api/ci/runs", "", "")
	if code != http.StatusOK || !strings.Contains(body, "run-w") || strings.Contains(body, "run-g") {
		t.Errorf("grain-ci's GET /api/ci/runs was not narrowed to its own repository: %d %s", code, body)
	}
	if code, body := asSandboxAgent(t, srv, "1", "", http.MethodGet, "/api/ci/runs?repo=acme/library", "", ""); code != http.StatusOK ||
		!strings.Contains(body, "run-l") {
		t.Errorf("grain-ci was refused its reads repository's runs: %d %s", code, body)
	}
	if code, _ := asSandboxAgent(t, srv, "1", "", http.MethodGet, "/api/ci/runs?repo=acme/gadgets", "", ""); code != http.StatusForbidden {
		t.Errorf("grain-ci listed a repository its task does not read: %d", code)
	}
	if code, _ := asSandboxAgent(t, srv, "1", "", http.MethodDelete, "/api/ci/runs/run-w", "", "stop"); code != http.StatusForbidden {
		t.Errorf("grain-ci cancelled a run: %d", code)
	}
}

// settings/new-task-form.yml's hidden defaults are a person's form, not a
// way around the proposal gate: an agent's POST /api/tasks is judged on
// the body it sent, so the defaults are not put in after that gate.
func TestAgentAPIFilingTakesNoHiddenFormDefaults(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write")
	var form model.NewTaskForm
	if err := json.Unmarshal([]byte(`{"default": {"fields": {
		"autoMerge": {"visibility": "hidden", "default": true},
		"approved": {"visibility": "hidden", "default": true},
		"writes": {"visibility": "hidden", "default": ["acme/gadgets"]},
		"reads": {"visibility": "hidden", "default": ["acme/secret"]}
	}}}`), &form); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.UpdateConfig(context.Background(), func(c *model.Config) error {
		c.NewTaskForm = &form
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets"}`, "testing")
	if code != http.StatusCreated {
		t.Fatalf("agent filing answered %d: %s", code, body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("created = %s (%v)", body, err)
	}
	task, err := client.Store.GetTask(context.Background(), created.ID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s) = %v, %v", created.ID, task, err)
	}
	if task.AutoMerge || task.Approval != nil || len(task.Writes) != 0 || len(task.Reads) != 0 {
		t.Errorf("agent-filed task took the form's hidden defaults: autoMerge=%v approval=%v writes=%v reads=%v",
			task.AutoMerge, task.Approval, task.Writes, task.Reads)
	}
}
