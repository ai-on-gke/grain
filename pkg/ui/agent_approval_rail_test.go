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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// railTask is one proposed task for the approval rail's tests: filed in
// acme/widgets, proposed by proposedBy ("" for nobody), holding grants
// by label, and shaped further by edit.
func railTask(t *testing.T, client *ui.Client, id, proposedBy string, edit func(*model.Task), grants ...string) {
	t.Helper()
	ref, err := model.ParseRepo("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &ref}
	if proposedBy != "" {
		task.Links = []model.Link{{Kind: model.LinkProposedBy, Target: proposedBy}}
	}
	for _, g := range grants {
		task.Grants = append(task.Grants, model.Grant{Capability: g, Via: model.GrantByLabel})
	}
	if edit != nil {
		edit(&task)
	}
	if err := client.Store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

// approveAsAgent is task acting's run approving id over the agent API.
func approveAsAgent(t *testing.T, srv *ui.Server, acting, id string) (int, string) {
	t.Helper()
	return asAgent(t, srv, acting, http.MethodPost, "/api/tasks/"+id+"/approve", `{}`, "testing the approval rail")
}

func wantProposed(t *testing.T, client *ui.Client, id string) {
	t.Helper()
	if state, err := client.Store.State(context.Background(), id); err != nil || state != model.StateProposed {
		t.Errorf("task %s is %s (%v), want still proposed", id, state, err)
	}
}

// An agent never approves its own proposals, even holding a person's
// api:tasks:admin: not one it filed, and not one filed by any task down
// its LinkProposedBy chain (director-capabilities.spec.md §3.3 item 3).
// A person still may.
func TestAgentApprovalRailRefusesOwnProposalLineage(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin")
	railTask(t, client, "2", "1", nil)
	railTask(t, client, "3", "2", nil)
	railTask(t, client, "4", "3", nil)

	code, body := approveAsAgent(t, srv, "1", "2")
	if code != http.StatusForbidden || !strings.Contains(body, "it proposed it") {
		t.Errorf("approving its own direct proposal: %d %s, want 403 saying it proposed it", code, body)
	}
	wantProposed(t, client, "2")

	code, body = approveAsAgent(t, srv, "1", "4")
	if code != http.StatusForbidden || !strings.Contains(body, "1 → 2 → 3") {
		t.Errorf("approving a proposal three hops down its lineage: %d %s, want 403 naming the chain 1 → 2 → 3", code, body)
	}
	wantProposed(t, client, "4")

	// A middle task of the chain is refused its own descendants too, but
	// not its ancestors' work outside its lineage.
	seedAPITask(t, client, "5", "acme/widgets", "api:tasks:admin")
	railTask(t, client, "6", "5", nil)
	railTask(t, client, "7", "6", nil)
	if code, body := approveAsAgent(t, srv, "6", "7"); code != http.StatusForbidden {
		t.Errorf("task 6 approving its own proposal 7: %d %s, want 403", code, body)
	}

	if rec := do(t, srv, http.MethodPost, "/api/tasks/4/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("a person approving task 4: %d %s", rec.Code, rec.Body.String())
	}
	if state, err := client.Store.State(context.Background(), "4"); err != nil || state != model.StateQueued {
		t.Fatalf("after a person's approval task 4 is %s (%v), want queued", state, err)
	}
}

// A task held for a person is a person's to approve, whoever proposed it:
// hold_for_approval, a capability, write or read its proposer does not
// hold, a person-only grant, keep_running, or a requires_person grant in
// the catalogue.
func TestAgentApprovalRailRefusesTasksHeldForAPerson(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin", "self-debug", "read-issues")
	// The proposer of every held task below: unrelated to task 1.
	seedAPITask(t, client, "9", "acme/widgets")
	gadgets, err := model.ParseRepo("acme/gadgets")
	if err != nil {
		t.Fatal(err)
	}
	setCatalogue(t, ctx, client.Store, &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"read-issues": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
	}})
	widgets, err := model.ParseRepo("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	pushTmplID := model.RepoDeclaredID(model.DeclaredTemplate, widgets, "tag-release")
	if _, err := client.Store.SyncRepoDeclarations(ctx, widgets, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     ".grain/templates/tag-release.yml",
			Template: model.Template{ID: pushTmplID, Name: "tag-release", Title: "Tag release", Target: &widgets},
			Push:     model.RefPushSet{{Pattern: "refs/tags/v*"}},
		}},
	}, time.Now(), time.UTC); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, id   string
		proposedBy string
		edit       func(*model.Task)
		grants     []string
		want       string
	}{
		{name: "hold_for_approval", id: "20", proposedBy: "9",
			edit: func(t *model.Task) { t.Tags = []string{model.HeldForApprovalTag} },
			want: "hold_for_approval"},
		{name: "capability escalation", id: "21", proposedBy: "9",
			grants: []string{"self-debug"}, want: "it asks for self-debug, which task 9"},
		{name: "ungranted write", id: "22", proposedBy: "9",
			edit: func(t *model.Task) { t.Writes = []model.WriteTarget{{Repo: gadgets}} },
			want: "it may land work in acme/gadgets"},
		{name: "ungranted read", id: "23", proposedBy: "9",
			edit: func(t *model.Task) { t.Reads = []model.RepoRef{gadgets} },
			want: "it reads acme/gadgets"},
		{name: "keep_running", id: "24", proposedBy: "9",
			edit: func(t *model.Task) { t.KeepRunning = true },
			want: "keep_running"},
		{name: "priority escalation", id: "27", proposedBy: "9",
			edit: func(t *model.Task) { t.Priority = model.DefaultTaskPriority + 1 },
			want: "above the"},
		{name: "config repository target", id: "28", proposedBy: "9",
			edit: func(t *model.Task) {
				cfgRepo := model.ConfigRepo()
				t.Target = &cfgRepo
			},
			want: "configuration repository"},
		{name: "change target needing person approval", id: "29", proposedBy: "9",
			edit: func(t *model.Task) {
				t.Target = nil
				t.ChangeTarget = &model.ChangeTarget{Kind: testTargetKind, Spec: json.RawMessage(`{"workspace":"w1"}`)}
			},
			want: "it names another workspace"},
		{name: "declared push set", id: "35", proposedBy: "9",
			edit: func(t *model.Task) { t.TemplateID = pushTmplID },
			want: "declared push set"},
		{name: "ungranted auto-merge", id: "36", proposedBy: "9",
			edit: func(t *model.Task) { t.AutoMerge = true },
			want: "auto-merge"},
		{name: "keep_running filed by a person", id: "37",
			edit: func(t *model.Task) { t.KeepRunning = true },
			want: "keep_running"},
		// Admission above standard replaced the person-only
		// priority-dispatch and durable grants, which held a task for a
		// person whoever filed it; the setting has to as well.
		{name: "admission bypass proposed", id: "38", proposedBy: "9",
			edit: func(t *model.Task) { t.Admission = model.AdmissionBypass },
			want: "admission to `bypass`"},
		{name: "admission bypass filed by a person", id: "39",
			edit: func(t *model.Task) { t.Admission = model.AdmissionBypass },
			want: "admission to `bypass`"},
		{name: "admission durable filed by a person", id: "40",
			edit: func(t *model.Task) { t.Admission = model.AdmissionDurable },
			want: "admission to `durable`"},
		{name: "person-only grant filed by a person", id: "25",
			grants: []string{"api:merge:force"}, want: "which only a person may grant"},
		{name: "requires_person grant filed by a person", id: "26",
			grants: []string{"read-issues"}, want: "requires_person"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			railTask(t, client, tc.id, tc.proposedBy, tc.edit, tc.grants...)
			code, body := approveAsAgent(t, srv, "1", tc.id)
			if code != http.StatusForbidden || !strings.Contains(body, "held for a person") || !strings.Contains(body, tc.want) {
				t.Errorf("approving %s: %d %s, want 403 saying it is held for a person and %q", tc.id, code, body, tc.want)
			}
			wantProposed(t, client, tc.id)
			if rec := do(t, srv, http.MethodPost, "/api/tasks/"+tc.id+"/approve", ""); rec.Code != http.StatusOK {
				t.Errorf("a person approving %s: %d %s", tc.id, rec.Code, rec.Body.String())
			}
		})
	}
}

// Outside its own lineage, and on a task held for nobody, a person's
// api:tasks:admin still approves: the rail narrows the grant, it does not
// retire it.
func TestAgentApprovalRailAdmitsUnrelatedTasksUnderTasksAdmin(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin")
	seedAPITask(t, client, "9", "acme/widgets", "self-debug")
	// Task 1's own proposal, for a sibling below to sit beside.
	railTask(t, client, "2", "1", nil)
	for _, tc := range []struct {
		name, id, proposedBy string
		grants               []string
	}{
		{name: "filed by a person", id: "30"},
		{name: "proposed by another task", id: "31", proposedBy: "9"},
		{name: "holding only what its proposer holds", id: "32", proposedBy: "9", grants: []string{"self-debug"}},
		{name: "proposed by a task whose proposer is gone", id: "33", proposedBy: "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			railTask(t, client, tc.id, tc.proposedBy, nil, tc.grants...)
			if code, body := approveAsAgent(t, srv, "1", tc.id); code != http.StatusOK {
				t.Fatalf("approving unrelated task %s: %d %s, want 200", tc.id, code, body)
			}
			if state, err := client.Store.State(context.Background(), tc.id); err != nil || state != model.StateQueued {
				t.Errorf("task %s is %s (%v), want queued", tc.id, state, err)
			}
		})
	}
	// Task 1's proposal is the proposer of a task that task 8 may
	// approve: lineage is the approver's own, not anybody's.
	seedAPITask(t, client, "8", "acme/widgets", "api:tasks:admin")
	railTask(t, client, "34", "2", nil)
	if code, body := approveAsAgent(t, srv, "8", "34"); code != http.StatusOK {
		t.Errorf("an unrelated tasks:admin approving task 1's grandchild: %d %s, want 200", code, body)
	}
}

// Neither direct POST /api/tasks filing (approved: true, or filing unapproved
// and then calling approve) nor POST /api/tasks/{id}/run-now (which approves
// unapproved tasks in Client.RunNow) may step around agentApprovalRail; once a
// person has approved a task, run-now still jumps it ahead of the queue.
func TestAgentApprovalRailCoversDirectAPIFilingAndRunNow(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if _, err := client.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "100", "acme/widgets",
		"api:tasks:write", "api:tasks:admin", "api:dispatch-control")
	cfgRepo := model.ConfigRepo()
	if err := client.Store.UpdateTask(ctx, "100", func(tk *model.Task) error {
		tk.Writes = []model.WriteTarget{{Repo: cfgRepo}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Direct self-approval at creation is refused even under api:tasks:admin.
	for _, body := range []string{
		`{"title":"self-approved","repo":"acme/widgets","approved":true}`,
		`{"title":"self-approved-case","repo":"acme/widgets","Approved":true}`,
	} {
		code, resp := asAgent(t, srv, "100", http.MethodPost, "/api/tasks", body, "self-approving at creation")
		if code != http.StatusForbidden || !strings.Contains(resp, "never approves its own proposals") {
			t.Errorf("POST /api/tasks with %s: got %d %s, want 403 refusing self-approval", body, code, resp)
		}
	}

	// 2. Two-step filing over POST /api/tasks records task 100 on Origin rather
	//    than LinkProposedBy; neither approve nor run-now may approve it or its
	//    descendants.
	code, resp := asAgent(t, srv, "100", http.MethodPost, "/api/tasks",
		`{"title":"filed-over-api","repo":"acme/widgets"}`, "filing unapproved")
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("POST /api/tasks unapproved: %d %s", code, resp)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(resp), &created); err != nil || created.ID == "" {
		t.Fatalf("decoding created task: %v (%s)", err, resp)
	}
	wantProposed(t, client, created.ID)

	if code, body := approveAsAgent(t, srv, "100", created.ID); code != http.StatusForbidden || !strings.Contains(body, "it proposed it") {
		t.Errorf("approving own POST /api/tasks filing %s: %d %s, want 403", created.ID, code, body)
	}
	if code, body := asAgent(t, srv, "100", http.MethodPost, "/api/tasks/"+created.ID+"/run-now", `{}`, "run-now own filing"); code != http.StatusForbidden || !strings.Contains(body, "it proposed it") {
		t.Errorf("run-now on own unapproved filing %s: %d %s, want 403", created.ID, code, body)
	}
	wantProposed(t, client, created.ID)

	railTask(t, client, "101", created.ID, nil)
	if code, body := approveAsAgent(t, srv, "100", "101"); code != http.StatusForbidden || !strings.Contains(body, "100 → "+created.ID) {
		t.Errorf("approving descendant of POST /api/tasks filing: %d %s, want 403 naming 100 → %s", code, body, created.ID)
	}

	// 3. run-now also refuses unapproved tasks held for a person, even when the
	//    acting task writes grain/config (as the director does).
	seedAPITask(t, client, "109", "acme/widgets")
	railTask(t, client, "102", "109", func(tk *model.Task) {
		tk.Target = &cfgRepo
	})
	if code, body := asAgent(t, srv, "100", http.MethodPost, "/api/tasks/102/run-now", `{}`, "run-now config proposal"); code != http.StatusForbidden || !strings.Contains(body, "held for a person") {
		t.Errorf("run-now on unapproved config-repo proposal: %d %s, want 403 saying held for a person", code, body)
	}
	wantProposed(t, client, "102")

	// 4. Once a person approves task 100's filing, run-now under dispatch:write
	//    may jump it ahead of the queue.
	if rec := do(t, srv, http.MethodPost, "/api/tasks/"+created.ID+"/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("a person approving %s: %d %s", created.ID, rec.Code, rec.Body.String())
	}
	if code, body := asAgent(t, srv, "100", http.MethodPost, "/api/tasks/"+created.ID+"/run-now", `{}`, "jump approved task"); code != http.StatusOK {
		t.Errorf("run-now on already-approved task %s: %d %s, want 200", created.ID, code, body)
	}
}

// An agent holding dispatch:write (api:dispatch-control) without api:tasks:admin
// cannot use POST /api/tasks/{id}/run-now's implicit approval to start its own
// LinkProposedBy lineage or a proposal held for a person (hold_for_approval, or
// an escalation against the task's proposer even when the caller holds that
// grant). Once a person approves those tasks, run-now jumps them ahead of the
// queue, and unrelated unapproved tasks held for nobody still pass when
// agentMayApprove admits them.
func TestAgentApprovalRailOnRunNowUnderDispatchWrite(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "1", "acme/widgets", "api:dispatch-control", "self-debug")
	seedAPITask(t, client, "9", "acme/widgets")

	runNowAsAgent := func(id string) (int, string) {
		t.Helper()
		return asAgent(t, srv, "1", http.MethodPost, "/api/tasks/"+id+"/run-now", `{}`, "testing run-now approval rail")
	}

	// 1. Own LinkProposedBy lineage (direct and multi-hop) is refused while unapproved.
	railTask(t, client, "2", "1", nil)
	railTask(t, client, "3", "2", nil)
	railTask(t, client, "4", "3", nil)

	if code, body := runNowAsAgent("2"); code != http.StatusForbidden || !strings.Contains(body, "it proposed it") {
		t.Errorf("run-now on own direct proposal: %d %s, want 403 saying it proposed it", code, body)
	}
	wantProposed(t, client, "2")
	if obs, err := client.Store.GetObservation(ctx, "2"); err != nil || (obs != nil && obs.RunNowAt != nil) {
		t.Errorf("observation on refused run-now = %+v (%v), want nil RunNowAt", obs, err)
	}

	if code, body := runNowAsAgent("4"); code != http.StatusForbidden || !strings.Contains(body, "1 → 2 → 3") {
		t.Errorf("run-now three hops down LinkProposedBy chain: %d %s, want 403 naming 1 → 2 → 3", code, body)
	}
	wantProposed(t, client, "4")

	// 2. Tasks held for a person that agentMayApprove alone would let past
	//    (hold_for_approval, and a grant escalated against the proposer that
	//    the run-now caller itself holds) are refused while unapproved.
	railTask(t, client, "20", "9", func(tk *model.Task) {
		tk.Tags = []string{model.HeldForApprovalTag}
	})
	if code, body := runNowAsAgent("20"); code != http.StatusForbidden ||
		!strings.Contains(body, "held for a person") || !strings.Contains(body, "hold_for_approval") {
		t.Errorf("run-now on hold_for_approval proposal: %d %s, want 403 naming hold_for_approval", code, body)
	}
	wantProposed(t, client, "20")

	railTask(t, client, "21", "9", nil, "self-debug")
	if code, body := runNowAsAgent("21"); code != http.StatusForbidden ||
		!strings.Contains(body, "held for a person") || !strings.Contains(body, "it asks for self-debug, which task 9") {
		t.Errorf("run-now on proposal escalated past its proposer: %d %s, want 403 naming proposer escalation", code, body)
	}
	wantProposed(t, client, "21")

	// 3. Once a person approves them, run-now is not an approval and succeeds.
	for _, id := range []string{"2", "20"} {
		if rec := do(t, srv, http.MethodPost, "/api/tasks/"+id+"/approve", ""); rec.Code != http.StatusOK {
			t.Fatalf("a person approving %s: %d %s", id, rec.Code, rec.Body.String())
		}
		if code, body := runNowAsAgent(id); code != http.StatusOK {
			t.Errorf("run-now on already-approved task %s: %d %s, want 200", id, code, body)
		}
		if obs, err := client.Store.GetObservation(ctx, id); err != nil || obs == nil || obs.RunNowAt == nil {
			t.Errorf("after approved run-now on %s: obs = %+v (%v), want RunNowAt stamped", id, obs, err)
		}
	}

	// 4. An unrelated unapproved task held for nobody and within task 1's
	//    grants is still approved and queued ahead by run-now.
	railTask(t, client, "30", "9", nil)
	if code, body := runNowAsAgent("30"); code != http.StatusOK {
		t.Fatalf("run-now on unrelated unapproved task 30: %d %s, want 200", code, body)
	}
	if state, err := client.Store.State(ctx, "30"); err != nil || state != model.StateQueued {
		t.Errorf("task 30 state = %s (%v), want queued", state, err)
	}
}

// A proposer that asks hold_for_approval wants a person to decide, whether
// or not its own permission would have queued the proposal. Filed through
// CreateTask by a run with no auto-approve permission, the proposal carries
// no HeldForApprovalTag (no choice was made), so the rail used to let an
// agent holding api:tasks:admin or dispatch:write approve it for the person
// it was left for (docs/test-runs/2026-11-07-director-agent-api.md,
// finding 2). HoldRequestedTag records the request itself.
func TestAgentApprovalRailHonoursHoldForApprovalFromAProposerWithoutPermission(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	// Seeded ids well clear of the ones CreateTask allocates (1, 2, ...).
	seedAPITask(t, client, "1001", "acme/widgets", "api:tasks:admin", "api:dispatch:write")
	seedAPITask(t, client, "1009", "acme/widgets")

	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:           "Drop the staging database",
		Repo:            "acme/widgets",
		ProposedBy:      "1009",
		HoldForApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := client.Store.GetTask(ctx, held.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask(%s): %v", held.ID, err)
	}
	if model.HeldForApproval(*stored) {
		t.Errorf("task %s carries %s though task 1009's permission would not have queued it", held.ID, model.HeldForApprovalTag)
	}
	if !model.HoldRequested(*stored) {
		t.Fatalf("task %s tags = %v, want %s recorded", held.ID, stored.Tags, model.HoldRequestedTag)
	}
	wantProposed(t, client, held.ID)

	if code, body := approveAsAgent(t, srv, "1001", held.ID); code != http.StatusForbidden ||
		!strings.Contains(body, "held for a person") || !strings.Contains(body, "hold_for_approval") {
		t.Errorf("approve: %d %s, want 403 naming hold_for_approval", code, body)
	}
	if code, body := asAgent(t, srv, "1001", http.MethodPost, "/api/tasks/"+held.ID+"/run-now", `{}`, "testing the approval rail"); code != http.StatusForbidden ||
		!strings.Contains(body, "hold_for_approval") {
		t.Errorf("run-now: %d %s, want 403 naming hold_for_approval", code, body)
	}
	wantProposed(t, client, held.ID)

	// The same proposal without hold_for_approval is held for nobody, so
	// an unrelated tasks:admin still approves it.
	plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Tidy the README", Repo: "acme/widgets", ProposedBy: "1009"})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := approveAsAgent(t, srv, "1001", plain.ID); code != http.StatusOK {
		t.Errorf("approving plain proposal %s: %d %s, want 200", plain.ID, code, body)
	}
}
