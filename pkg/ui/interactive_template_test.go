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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A template's interactive switch (model.Template.Interactive) is saved
// and edited over the API like any other, and a task filed from the
// template with it on is interactive -- a live chat the frontend opens
// straight after filing it -- landing approved like any interactive task
// a person files.
func TestInstantiatingAnInteractiveTemplateFilesAnInteractiveTask(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"GKE cluster","title":"Set up a GKE cluster","description":"Ask which project first.","interactive":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	if !created.Interactive {
		t.Fatal("the created template is not interactive")
	}

	// The composition preview says the task is approved from the template
	// even if ?approved=false is passed, matching what CreateTask files.
	approvalField := func(task ui.ComposedTask) ui.ComposedField {
		for _, f := range task.Fields {
			if f.Name == "approval" {
				return f
			}
		}
		return ui.ComposedField{}
	}
	rec = do(t, srv, http.MethodGet, "/api/templates/"+created.ID+"/composition?approved=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	appField := approvalField(decode[ui.Composition](t, rec).Tasks[0])
	if len(appField.Values) != 1 || appField.Values[0] != "approved when filed" || appField.Source != string(model.LayerTemplate) {
		t.Errorf("interactive template composition approval = %+v, want [approved when filed] from %q",
			appField, model.LayerTemplate)
	}

	rec = do(t, srv, http.MethodPost, "/api/tasks", `{"templateId":"`+created.ID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, want 201: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if !task.Interactive {
		t.Error("the task filed from an interactive template is not interactive")
	}
	if !task.Approved {
		t.Error("an interactive task a person filed is not approved")
	}

	// Turned off, the next filing is an ordinary task.
	rec = do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, `{"interactive":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch template status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if decode[ui.Template](t, rec).Interactive {
		t.Fatal("the template is still interactive after turning it off")
	}
	rec = do(t, srv, http.MethodGet, "/api/templates/"+created.ID+"/composition?approved=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	appField = approvalField(decode[ui.Composition](t, rec).Tasks[0])
	if len(appField.Values) != 1 || appField.Values[0] != "waits for approval" || appField.Source != string(model.LayerFiling) {
		t.Errorf("non-interactive template composition approval = %+v, want [waits for approval] from %q",
			appField, model.LayerFiling)
	}
	rec = do(t, srv, http.MethodPost, "/api/tasks", `{"templateId":"`+created.ID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if decode[ui.Task](t, rec).Interactive {
		t.Error("a task filed from a template no longer interactive is interactive")
	}
}

// propose_task's template_id files through CreateTask's templateId, so a
// proposal from an interactive template is interactive too -- but an
// interactive proposal is not approved by being one: that approval is
// for a person filing a chat they are about to sit with, and a run's
// proposal lands the way every other proposal does.
func TestAProposalFromAnInteractiveTemplateIsInteractiveButNotApproved(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "cluster", Title: "Set up a cluster", Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "the whole job", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, ProposedBy: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !filed.Interactive {
		t.Error("the proposal is not interactive")
	}
	if filed.Approved || filed.State != model.StateProposed {
		t.Errorf("proposal approved = %v in state %s, want an unapproved proposal", filed.Approved, filed.State)
	}
}

// A template's capability grants carry their with: arguments -- the
// shape a gcp-key grant choosing a permission set has
// (docs/gcp-key-permission-sets.md, step 3) -- and its network access,
// and both reach the task filed from it.
func TestATemplatesGrantArgumentsAndNetworkAccessReachTheTask(t *testing.T) {
	t.Parallel()
	_, client := grantTestServer(t)
	ctx := context.Background()
	grant := grantTestCap + `#key={"account":"staging"}`
	recorded := grantTestCap + `#key={"account":"staging","scopes":["all"]}`
	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "cluster", Title: "Set up a cluster", Interactive: true,
		NetworkAccess: string(model.NetworkOpen),
		Capabilities:  []string{grant},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := refGrants(tmpl.Capabilities); len(got) != 1 || got[0] != recorded {
		t.Fatalf("template grants = %q, want %q", got, recorded)
	}
	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := refGrants(filed.Capabilities); len(got) != 1 || got[0] != recorded {
		t.Errorf("task grants = %q, want %q", got, recorded)
	}
	if filed.NetworkAccess != string(model.NetworkOpen) {
		t.Errorf("task network access = %q, want %q", filed.NetworkAccess, model.NetworkOpen)
	}
	if !filed.Interactive {
		t.Error("the task is not interactive")
	}
}

// An interactive template's composition preview always reports "approved
// when filed" from the template layer, even when the caller passes
// approved=false (for instance on a deployment where approvedByDefault is
// false or after unticking Approved on a non-interactive template before
// switching the picker): UseTemplateOverlay hides the Approved checkbox for
// an interactive template because Client.CreateTask always approves a
// person's interactive template filing.
func TestInteractiveTemplateCompositionReportsApprovedFromTemplate(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	interactiveTmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Interactive triage", Title: "Triage incident", Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	backgroundTmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Background sweep", Title: "Sweep logs", Interactive: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/templates/"+interactiveTmpl.ID+"/composition?approved=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("interactive composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	comp := decode[ui.Composition](t, rec)
	if len(comp.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(comp.Tasks))
	}
	var gotApproval *ui.ComposedField
	for i := range comp.Tasks[0].Fields {
		if comp.Tasks[0].Fields[i].Name == "approval" {
			gotApproval = &comp.Tasks[0].Fields[i]
			break
		}
	}
	if gotApproval == nil {
		t.Fatal("interactive composition has no approval field")
	}
	if len(gotApproval.Values) != 1 || gotApproval.Values[0] != "approved when filed" {
		t.Errorf("interactive approval values = %v, want [\"approved when filed\"]", gotApproval.Values)
	}
	if gotApproval.Source != string(model.LayerTemplate) {
		t.Errorf("interactive approval source = %q, want %q", gotApproval.Source, model.LayerTemplate)
	}

	// Non-interactive template still honors approved=false from the filing form.
	rec = do(t, srv, http.MethodGet, "/api/templates/"+backgroundTmpl.ID+"/composition?approved=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("background composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	bgComp := decode[ui.Composition](t, rec)
	for _, f := range bgComp.Tasks[0].Fields {
		if f.Name == "approval" {
			if len(f.Values) != 1 || f.Values[0] != "waits for approval" || f.Source != string(model.LayerFiling) {
				t.Errorf("background approval = %+v, want waits for approval from filing", f)
			}
		}
	}
}
