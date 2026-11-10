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
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// railCredential mints an external agent credential owned by
// alice@example.com holding scopes on acme/widgets, and returns its
// bearer token.
func railCredential(t *testing.T, client *ui.Client, name string, scopes ...string) string {
	t.Helper()
	issued, err := client.CreateExternalAgentToken(context.Background(), ui.CreateExternalAgentTokenRequest{
		Name: name, Scopes: scopes, Repos: []string{"acme/widgets"}, Owner: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("minting credential %s: %v", name, err)
	}
	if issued.Owner != "alice@example.com" {
		t.Fatalf("credential %s owner = %q, want alice@example.com", name, issued.Owner)
	}
	return issued.Token
}

// externalTaskAction is the external credential holding token calling
// POST /api/tasks/{id}/<action> over REST.
func externalTaskAction(t *testing.T, srv *ui.Server, token, id, action string) (int, string) {
	t.Helper()
	rec := fileAsExternal(t, srv, token, "/api/tasks/"+id+"/"+action, `{}`,
		map[string]string{ui.ActingReasonHeader: "testing the external approval rail"})
	return rec.Code, rec.Body.String()
}

// externalMCPTaskAction is the same call made through POST /api/mcp's
// grain_invoke, the route the 2026-11-07 walkthrough found approving a
// durable task.
func externalMCPTaskAction(t *testing.T, srv *ui.Server, token, tool, id string) (string, bool) {
	t.Helper()
	return callControllerTool(t, srv, map[string]string{"Authorization": "Bearer " + token}, ui.ToolGrainInvoke, map[string]any{
		"tool":      tool,
		"arguments": map[string]any{"id": id},
		"reason":    "testing the external approval rail",
	})
}

func wantQueued(t *testing.T, client *ui.Client, id string) {
	t.Helper()
	if state, err := client.Store.State(context.Background(), id); err != nil || state != model.StateQueued {
		t.Errorf("task %s is %s (%v), want queued", id, state, err)
	}
}

// An owned external credential is its owner's delegate, not its owner:
// the G8 rail holds it as it holds a task's run. It never approves its
// own filing (or work proposed down that filing's chain), nor a task held
// for a person -- here a person-filed task with admission durable, the
// walkthrough's repro, and one filed with hold_for_approval -- and
// approve and run-now give the same answer over REST and over
// POST /api/mcp. A task held for nobody is still approved, and a person
// still approves the rest.
func TestExternalAgentApprovalRailMatchesApproveAndRunNow(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	token := railCredential(t, client, "rails-ext", "tasks:read", "tasks:write", "tasks:admin", "dispatch:write")

	// Its own filing lands proposed, and is the credential's lineage.
	own := filedTask(t, fileAsExternal(t, srv, token, "/api/tasks", `{"title":"ext filed","repo":"acme/widgets"}`, nil))
	wantProposed(t, client, own.ID)
	// A task proposed by a run of a task the credential filed, two hops down.
	railTask(t, client, "900", own.ID, nil)
	railTask(t, client, "901", "900", nil)
	// Person-filed tasks held for a person.
	railTask(t, client, "910", "", func(task *model.Task) { task.Admission = model.AdmissionDurable })
	railTask(t, client, "911", "", func(task *model.Task) { task.Tags = append(task.Tags, model.HoldRequestedTag) })
	// Person-filed tasks held for nobody.
	railTask(t, client, "920", "", nil)
	railTask(t, client, "921", "", nil)
	railTask(t, client, "922", "", nil)

	for _, tc := range []struct {
		id, want string
	}{
		{own.ID, "it filed it"},
		{"900", "it filed task " + own.ID},
		{"901", own.ID + " → 900"},
		{"910", "admission to `durable`"},
		{"911", "hold_for_approval"},
	} {
		for _, action := range []string{"approve", "run-now"} {
			code, body := externalTaskAction(t, srv, token, tc.id, action)
			if code != http.StatusForbidden || !strings.Contains(body, tc.want) || !strings.Contains(body, "external:rails-ext") {
				t.Errorf("REST %s on task %s: %d %s, want 403 naming external:rails-ext and %q", action, tc.id, code, body, tc.want)
			}
		}
		for _, tool := range []string{"api_approve", "api_run_now"} {
			out, isErr := externalMCPTaskAction(t, srv, token, tool, tc.id)
			if !isErr || !strings.Contains(out, "403") || !strings.Contains(out, tc.want) {
				t.Errorf("POST /api/mcp grain_invoke %s on task %s: isErr=%v %s, want a 403 naming %q", tool, tc.id, isErr, out, tc.want)
			}
		}
		wantProposed(t, client, tc.id)
	}

	// Held for nobody: approve and run-now both approve it, over REST and MCP.
	if code, body := externalTaskAction(t, srv, token, "920", "approve"); code != http.StatusOK {
		t.Errorf("REST approve on unheld task 920: %d %s, want 200", code, body)
	}
	wantQueued(t, client, "920")
	if code, body := externalTaskAction(t, srv, token, "921", "run-now"); code != http.StatusOK {
		t.Errorf("REST run-now on unheld task 921: %d %s, want 200", code, body)
	}
	if task, err := client.Store.GetTask(ctx, "921"); err != nil || task == nil || task.Approval == nil {
		t.Errorf("after run-now task 921 = %+v (%v), want approved", task, err)
	}
	if out, isErr := externalMCPTaskAction(t, srv, token, "api_approve", "922"); isErr {
		t.Errorf("POST /api/mcp grain_invoke api_approve on unheld task 922: %s, want success", out)
	}
	wantQueued(t, client, "922")

	// A person approves what the credential may not.
	for _, id := range []string{own.ID, "910", "911"} {
		if rec := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/approve", ""); rec.Code != http.StatusOK {
			t.Fatalf("a person approving task %s: %d %s", id, rec.Code, rec.Body.String())
		}
		wantQueued(t, client, id)
	}
}

// run-now's implicit approval is an approval: a credential holding
// dispatch:write without tasks:admin cannot approve an unapproved task by
// running it, which the approve route would refuse it too, but still
// jumps an approved task ahead of the queue.
func TestExternalAgentRunNowApprovesOnlyUnderTasksAdmin(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	token := railCredential(t, client, "dispatcher", "tasks:read", "dispatch:write")
	railTask(t, client, "930", "", nil)
	railTask(t, client, "931", "", nil)

	if code, body := externalTaskAction(t, srv, token, "930", "approve"); code != http.StatusForbidden || !strings.Contains(body, "tasks:admin") {
		t.Errorf("approve without tasks:admin: %d %s, want 403 naming tasks:admin", code, body)
	}
	if code, body := externalTaskAction(t, srv, token, "930", "run-now"); code != http.StatusForbidden || !strings.Contains(body, "tasks:admin") {
		t.Errorf("run-now on an unapproved task without tasks:admin: %d %s, want 403 naming tasks:admin", code, body)
	}
	if out, isErr := externalMCPTaskAction(t, srv, token, "api_run_now", "930"); !isErr || !strings.Contains(out, "tasks:admin") {
		t.Errorf("POST /api/mcp grain_invoke api_run_now on an unapproved task without tasks:admin: isErr=%v %s, want refusal naming tasks:admin", isErr, out)
	}
	wantProposed(t, client, "930")

	if rec := do(t, srv, http.MethodPost, "/api/tasks/931/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("a person approving task 931: %d %s", rec.Code, rec.Body.String())
	}
	if code, body := externalTaskAction(t, srv, token, "931", "run-now"); code != http.StatusOK {
		t.Errorf("run-now on an approved task under dispatch:write: %d %s, want 200", code, body)
	}
}

// The Client asks the rail again of an external caller, so a route that
// reaches Client.Approve or Client.RunNow without the agent API gate
// still holds it.
func TestExternalAgentApprovalRailHoldsInTheClient(t *testing.T) {
	_, client := testServer(t)
	principal := ui.WithPrincipal(context.Background(), model.Principal{
		Kind: model.PrincipalAgent, ID: "external:rails-ext", Source: model.SourceExternalToken,
	})
	ctx := ui.WithExternalTokenForTest(principal, &model.ExternalAgentToken{
		Name: "rails-ext", Scopes: []string{"tasks:read", "tasks:write", "tasks:admin", "dispatch:write"}, Repos: []string{"acme/widgets"},
	})
	railTask(t, client, "940", "", func(task *model.Task) { task.Admission = model.AdmissionDurable })
	railTask(t, client, "941", "", func(task *model.Task) {
		task.Origin.Attribution.Actor = model.Principal{Kind: model.PrincipalAgent, ID: "external:rails-ext", Source: model.SourceExternalToken}
	})
	railTask(t, client, "942", "", nil)
	for _, id := range []string{"940", "941"} {
		if err := client.Approve(ctx, id); err == nil {
			t.Errorf("Client.Approve(%s) as external:rails-ext succeeded, want a refusal", id)
		}
		if err := client.RunNow(ctx, id); err == nil {
			t.Errorf("Client.RunNow(%s) as external:rails-ext succeeded, want a refusal", id)
		}
		wantProposed(t, client, id)
	}
	// Past the credential's own ceiling: writes to a repository it does not hold.
	railTask(t, client, "943", "", func(task *model.Task) {
		task.Writes = []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "gadgets"}}}
	})
	for _, err := range []error{client.Approve(ctx, "943"), client.RunNow(ctx, "943")} {
		if err == nil || !strings.Contains(err.Error(), "(writes)") {
			t.Errorf("approving task 943 (writes acme/gadgets) as external:rails-ext: %v, want a refusal naming writes", err)
		}
	}
	wantProposed(t, client, "943")
	// An external caller whose credential cannot be read is refused, not
	// let through on grants nobody can see.
	if err := client.Approve(principal, "942"); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("Client.Approve(942) as external:rails-ext with no credential: %v, want a refusal", err)
	}
	wantProposed(t, client, "942")
	if err := client.RunNow(ctx, "942"); err != nil {
		t.Errorf("Client.RunNow(942) as external:rails-ext: %v, want it approved and run", err)
	}
}

// run-now's implicit approval asks for exactly the tasks:admin the
// approve route's own scope check does. For a task with no repository
// that is tasks:admin on every repository: a credential holding
// dispatch:write everywhere but tasks:admin only for acme/widgets is
// refused both routes, over REST and over POST /api/mcp's grain_invoke,
// and the task stays proposed (2026-11-08 second review, finding 3). A
// credential holding both scopes everywhere is granted both.
func TestExternalAgentRunNowOnANoRepoTaskNeedsUnlimitedTasksAdmin(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	mint := func(name string, scopes ...string) string {
		t.Helper()
		issued, err := client.CreateExternalAgentToken(context.Background(), ui.CreateExternalAgentTokenRequest{
			Name: name, Scopes: scopes, Owner: "alice@example.com",
		})
		if err != nil {
			t.Fatalf("minting credential %s: %v", name, err)
		}
		return issued.Token
	}
	noRepo := func(task *model.Task) { task.Target = nil }

	mixed := mint("mixed", "tasks:read", "dispatch:write", "tasks:admin@acme/widgets")
	railTask(t, client, "950", "", noRepo)
	if code, body := externalTaskAction(t, srv, mixed, "950", "approve"); code != http.StatusForbidden || !strings.Contains(body, "tasks:admin") {
		t.Errorf("approve on a no-repository task under tasks:admin@acme/widgets: %d %s, want 403 naming tasks:admin", code, body)
	}
	if code, body := externalTaskAction(t, srv, mixed, "950", "run-now"); code != http.StatusForbidden || !strings.Contains(body, "tasks:admin") {
		t.Errorf("run-now on an unapproved no-repository task under tasks:admin@acme/widgets: %d %s, want 403 naming tasks:admin", code, body)
	}
	if out, isErr := externalMCPTaskAction(t, srv, mixed, "api_approve", "950"); !isErr || !strings.Contains(out, "tasks:admin") {
		t.Errorf("POST /api/mcp grain_invoke api_approve on a no-repository task under tasks:admin@acme/widgets: isErr=%v %s, want refusal naming tasks:admin", isErr, out)
	}
	if out, isErr := externalMCPTaskAction(t, srv, mixed, "api_run_now", "950"); !isErr || !strings.Contains(out, "tasks:admin") {
		t.Errorf("POST /api/mcp grain_invoke api_run_now on an unapproved no-repository task under tasks:admin@acme/widgets: isErr=%v %s, want refusal naming tasks:admin", isErr, out)
	}
	wantProposed(t, client, "950")

	admin := mint("admin", "tasks:read", "dispatch:write", "tasks:admin")
	railTask(t, client, "951", "", noRepo)
	railTask(t, client, "952", "", noRepo)
	railTask(t, client, "953", "", noRepo)
	railTask(t, client, "954", "", noRepo)
	if code, body := externalTaskAction(t, srv, admin, "951", "approve"); code != http.StatusOK {
		t.Errorf("approve on a no-repository task under unlimited tasks:admin: %d %s, want 200", code, body)
	}
	if code, body := externalTaskAction(t, srv, admin, "952", "run-now"); code != http.StatusOK {
		t.Errorf("run-now on an unapproved no-repository task under unlimited tasks:admin: %d %s, want 200", code, body)
	}
	wantQueued(t, client, "952")
	if out, isErr := externalMCPTaskAction(t, srv, admin, "api_approve", "953"); isErr {
		t.Errorf("POST /api/mcp grain_invoke api_approve on a no-repository task under unlimited tasks:admin: %s, want it approved", out)
	}
	if out, isErr := externalMCPTaskAction(t, srv, admin, "api_run_now", "954"); isErr {
		t.Errorf("POST /api/mcp grain_invoke api_run_now on an unapproved no-repository task under unlimited tasks:admin: %s, want it approved and run", out)
	}
	wantQueued(t, client, "954")
}

// run-now's implicit approval asks for the reason approve does: approve
// is destructive under tasks:admin and refused without one, and run-now
// on an unapproved task used to approve it with none
// (docs/test-runs/2026-11-09-external-agent-rails.md, F1). Run-now of an
// approved task approves nothing and still needs no reason.
func TestExternalAgentRunNowApprovalNeedsAReasonAsApproveDoes(t *testing.T) {
	srv, client := testServer(t)
	enableExternalAgentAPI(t, client.Store)
	token := railCredential(t, client, "reasonless", "tasks:read", "dispatch:write", "tasks:admin")
	for _, id := range []string{"960", "961", "962", "963", "964"} {
		railTask(t, client, id, "", nil)
	}
	bare := func(id, action string) (int, string) {
		rec := fileAsExternal(t, srv, token, "/api/tasks/"+id+"/"+action, `{}`, nil)
		return rec.Code, rec.Body.String()
	}
	if code, body := bare("960", "approve"); code != http.StatusForbidden || !strings.Contains(body, "give a reason") {
		t.Errorf("approve without a reason: %d %s, want 403 asking for a reason", code, body)
	}
	if code, body := bare("960", "run-now"); code != http.StatusForbidden || !strings.Contains(body, "give a reason") {
		t.Errorf("run-now of an unapproved task without a reason: %d %s, want 403 asking for a reason", code, body)
	}
	out, isErr := callControllerTool(t, srv, map[string]string{"Authorization": "Bearer " + token}, ui.ToolGrainInvoke, map[string]any{
		"tool": "api_run_now", "arguments": map[string]any{"id": "961"},
	})
	if !isErr || !strings.Contains(out, "give a reason") {
		t.Errorf("grain_invoke api_run_now of an unapproved task without a reason: isErr=%v %s, want a refusal asking for a reason", isErr, out)
	}
	wantProposed(t, client, "960")
	wantProposed(t, client, "961")

	if code, body := externalTaskAction(t, srv, token, "962", "run-now"); code != http.StatusOK {
		t.Errorf("run-now of an unapproved task with a reason: %d %s, want 200", code, body)
	}
	if code, body := externalTaskAction(t, srv, token, "963", "approve"); code != http.StatusOK {
		t.Errorf("approve with a reason: %d %s, want 200", code, body)
	}
	if rec := do(t, srv, http.MethodPost, "/api/tasks/964/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("a person approving task 964: %d %s", rec.Code, rec.Body.String())
	}
	if code, body := bare("964", "run-now"); code != http.StatusOK {
		t.Errorf("run-now of an approved task without a reason: %d %s, want 200", code, body)
	}
}

// An external credential approves only work it could have filed itself:
// one held to acme/widgets, refused POST /api/tasks for writes or reads
// reaching acme/gadgets, open networkAccess, a grant it does not hold or a
// review template granting one, is refused approving -- or running now --
// a stored person-filed acme/widgets task carrying any of them, over REST,
// POST /mcp and POST /api/mcp's grain_invoke alike, and the task stays
// proposed (post-merge review of PR #987, finding 2.2). Work within its
// ceiling is still approved.
func TestExternalAgentApprovalIsHeldToTheCredentialsCeiling(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("reading config: %+v %v", cfg, err)
	}
	cfg.NetworkAccess = model.NetworkNone
	if err := client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	token := railCredential(t, client, "widgets-admin", "tasks:read", "tasks:write", "tasks:admin", "dispatch:write")
	gadgets, err := model.ParseRepo("acme/gadgets")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutTemplate(ctx, model.Template{
		ID: "heavy-review", Name: "heavy-review", Title: "Heavy review", Body: "Review it.",
		Grants: []model.Grant{{Capability: "github#read-issues"}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, want string
		edit       func(*model.Task)
	}{
		{"writes", "(writes)", func(task *model.Task) { task.Writes = []model.WriteTarget{{Repo: gadgets}} }},
		{"reads", "(reads)", func(task *model.Task) { task.Reads = []model.RepoRef{gadgets} }},
		{"networkAccess", "(networkAccess)", func(task *model.Task) { task.NetworkAccess = model.NetworkOpen }},
		{"api grant", "(capabilities)", func(task *model.Task) {
			task.Grants = append(task.Grants, model.Grant{Capability: model.APIRef("tasks:write")})
		}},
		{"capability", "(capabilities)", func(task *model.Task) {
			task.Grants = append(task.Grants, model.Grant{Capability: "github#read-issues"})
		}},
		{"reviewTemplateId", "(reviewTemplateId)", func(task *model.Task) { task.ReviewTemplateID = "heavy-review" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := []string{"rest-approve", "rest-run-now", "mcp-approve", "mcp-run-now", "invoke-approve", "invoke-run-now"}
			for i := range ids {
				ids[i] = strings.ReplaceAll(tc.name, " ", "-") + "-" + ids[i]
				railTask(t, client, ids[i], "", tc.edit)
			}
			refused := func(route, out string) {
				t.Helper()
				if !strings.Contains(out, tc.want) || !strings.Contains(out, "external:widgets-admin") {
					t.Errorf("%s: %s, want a refusal naming external:widgets-admin and %q", route, out, tc.want)
				}
			}
			for i, action := range []string{"approve", "run-now"} {
				code, body := externalTaskAction(t, srv, token, ids[i], action)
				if code != http.StatusForbidden {
					t.Errorf("REST %s: %d %s, want 403", action, code, body)
				}
				refused("REST "+action, body)
			}
			for i, tool := range []string{"api_approve", "api_run_now"} {
				_, res := callMCPTool(t, srv, token, tool, map[string]any{"id": ids[2+i], "reason": "testing the approval ceiling"})
				var text string
				for _, c := range res.Content {
					text += c.Text
				}
				if !res.IsError {
					t.Errorf("POST /mcp %s: %s, want an error", tool, text)
				}
				refused("POST /mcp "+tool, text)
				out, isErr := externalMCPTaskAction(t, srv, token, tool, ids[4+i])
				if !isErr {
					t.Errorf("POST /api/mcp grain_invoke %s: %s, want an error", tool, out)
				}
				refused("POST /api/mcp grain_invoke "+tool, out)
			}
			for _, id := range ids {
				wantProposed(t, client, id)
			}
		})
	}

	// Within the ceiling: writes, reads and grants on acme/widgets alone.
	within := func(task *model.Task) {
		widgets := *task.Target
		task.Writes = []model.WriteTarget{{Repo: widgets}}
		task.Reads = []model.RepoRef{widgets}
		task.Grants = append(task.Grants, model.Grant{Capability: model.APIGrantString("tasks:read", "acme/widgets")})
	}
	railTask(t, client, "970", "", within)
	railTask(t, client, "971", "", within)
	railTask(t, client, "972", "", within)
	if code, body := externalTaskAction(t, srv, token, "970", "approve"); code != http.StatusOK {
		t.Errorf("REST approve within the ceiling: %d %s, want 200", code, body)
	}
	wantQueued(t, client, "970")
	if _, res := callMCPTool(t, srv, token, "api_run_now", map[string]any{"id": "971", "reason": "within the ceiling"}); res.IsError {
		t.Errorf("POST /mcp api_run_now within the ceiling: %+v, want success", res)
	}
	if out, isErr := externalMCPTaskAction(t, srv, token, "api_approve", "972"); isErr {
		t.Errorf("POST /api/mcp grain_invoke api_approve within the ceiling: %s, want success", out)
	}
	wantQueued(t, client, "972")
}
