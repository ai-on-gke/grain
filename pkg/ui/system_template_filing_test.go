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
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain's own fileable templates (model.FileableSystemTemplates) are
// listed beside the deployment's, marked system, after them -- and the
// system templates that back schedules are not listed at all.
func TestListTemplatesOffersGrainsOwnFileableTemplates(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"mine","title":"Mine"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rec.Code, rec.Body)
	}
	list := decode[[]ui.Template](t, rec)
	if len(list) < 2 || list[0].Name != "mine" || list[0].System {
		t.Fatalf("the deployment's own template should lead, unmarked: %+v", list)
	}
	i := slices.IndexFunc(list, func(t ui.Template) bool { return t.ID == model.GKESandboxClusterTemplateID })
	if i < 0 {
		t.Fatalf("%s is not listed: %+v", model.GKESandboxClusterTemplateID, list)
	}
	gke := list[i]
	if !gke.System || !gke.Interactive {
		t.Errorf("system = %v, interactive = %v; want both", gke.System, gke.Interactive)
	}
	for _, want := range []string{"bootstrap-playbooks", "self-repair"} {
		if !slices.Contains(gke.Capabilities, want) {
			t.Errorf("capabilities %v lack %s", gke.Capabilities, want)
		}
	}
	for _, tmpl := range list {
		if tmpl.ID == model.MemoryAgentTemplateID {
			t.Errorf("the memory agent's template is offered for filing")
		}
	}
}

// Filing from the built-in GKE template with no repo files an
// interactive, approved, repo-less task holding the template's grants,
// with the form's answers in its prompt.
func TestFilingTheGKESandboxClusterTemplate(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks",
		`{"templateId":"`+model.GKESandboxClusterTemplateID+`","fields":{"project":"acme-sandboxes","machine_type":"c4-standard-96-lssd"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, want 201: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if !task.Interactive || !task.Approved {
		t.Errorf("interactive = %v, approved = %v; want both", task.Interactive, task.Approved)
	}
	if task.Repo != "" {
		t.Errorf("repo = %q, want none", task.Repo)
	}
	for _, want := range []string{"bootstrap-playbooks", "self-repair"} {
		if !slices.Contains(task.Capabilities, want) {
			t.Errorf("capabilities %v lack %s", task.Capabilities, want)
		}
	}
	for _, want := range []string{"acme-sandboxes", "c4-standard-96-lssd", "us-central1-a", "grain-sandboxes", "-dry-run", "kubernetes-granule"} {
		if !strings.Contains(task.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, task.Description)
		}
	}

	rec = do(t, srv, http.MethodGet, "/api/templates/"+model.GKESandboxClusterTemplateID+"/composition?field.project=p", "")
	if rec.Code != http.StatusOK {
		t.Errorf("composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// A system id that backs a schedule is not fileable by naming it, and
// none of grain's templates can be edited or deleted here.
func TestSystemTemplatesAreNotEditableOrFileableBeyondThePicker(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"templateId":"`+model.MemoryAgentTemplateID+`"}`)
	if rec.Code == http.StatusCreated {
		t.Errorf("filing from %s succeeded: %s", model.MemoryAgentTemplateID, rec.Body)
	}
	rec = do(t, srv, http.MethodPatch, "/api/templates/"+model.GKESandboxClusterTemplateID, `{"title":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "built-in") {
		t.Errorf("patch status = %d, want 400 naming it built-in: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodDelete, "/api/templates/"+model.GKESandboxClusterTemplateID, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "built-in") {
		t.Errorf("delete status = %d, want 400 naming it built-in: %s", rec.Code, rec.Body)
	}
}

// ownTemplates is a listing without grain's own built-in templates --
// what a test about this deployment's rows asserts against, so that
// shipping another built-in template does not break it.
func ownTemplates(list []ui.Template) []ui.Template {
	return slices.DeleteFunc(slices.Clone(list), func(t ui.Template) bool { return t.System })
}
