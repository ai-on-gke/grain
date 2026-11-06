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
