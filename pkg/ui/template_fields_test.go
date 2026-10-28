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

// Filing a task straight from a template, with its input fields filled
// in (grain/task-58) -- the API half of it: what a template may declare,
// what a filing may supply for it, and what the task that comes out
// carries. Against a real embedded store, for templates_test.go's own
// reason.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTemplateAcceptsInputFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{service}} to {{environment}}",
		Fields: []ui.TemplateField{
			{Name: "service", Label: "Service", Required: true},
			{Name: "environment", Options: []string{"staging", "production"}, Default: "staging"},
		},
	})
	if err != nil {
		t.Fatalf("creating a template with input fields: %v", err)
	}
	if len(tmpl.Fields) != 2 {
		t.Fatalf("fields = %+v, want both, in the order declared", tmpl.Fields)
	}
	if tmpl.Fields[0].Name != "service" || !tmpl.Fields[0].Required {
		t.Errorf("text field = %+v, want it as declared", tmpl.Fields[0])
	}
	if len(tmpl.Fields[1].Options) != 2 || tmpl.Fields[1].Default != "staging" {
		t.Errorf("selector = %+v, want its options and default", tmpl.Fields[1])
	}
}

func TestCreateTemplateRefusesAFieldNobodyCouldFillIn(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy",
		Fields: []ui.TemplateField{
			{Name: "environment", Options: []string{"staging"}, Default: "production"},
		},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError about a default outside the options", err)
	}
}

// The form renders every field, so what it sends is the whole
// declaration -- including the empty list that removes the last one.
func TestUpdateTemplateReplacesTheWholeFieldDeclaration(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{service}}",
		Fields: []ui.TemplateField{{Name: "service"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := []ui.TemplateField{{Name: "environment", Options: []string{"staging"}}}
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Fields: &replacement})
	if err != nil {
		t.Fatalf("replacing the fields: %v", err)
	}
	if len(updated.Fields) != 1 || updated.Fields[0].Name != "environment" {
		t.Fatalf("fields = %+v, want only the replacement", updated.Fields)
	}

	// Absent leaves them alone; empty-but-present removes them.
	name := "Deploy again"
	updated, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Name: &name})
	if err != nil || len(updated.Fields) != 1 {
		t.Fatalf("an unrelated edit = %+v (%v), want the fields untouched", updated.Fields, err)
	}
	none := []ui.TemplateField{}
	updated, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Fields: &none})
	if err != nil || len(updated.Fields) != 0 {
		t.Fatalf("fields = %+v (%v), want them removed", updated.Fields, err)
	}
}

func TestCreateTaskFromATemplateFillsInTheHoles(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{service}} to {{environment}}",
		Description:  "Deploy {{service}}, then watch it.",
		Capabilities: []string{"gemini-key"},
		Reads:        []string{"acme/shared-lib"},
		AutoMerge:    true,
		Fields: []ui.TemplateField{
			{Name: "service"},
			{Name: "environment", Options: []string{"staging", "production"}, Default: "staging"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		TemplateID: tmpl.ID,
		Fields:     map[string]string{"service": "payments"},
		Approved:   true,
	})
	if err != nil {
		t.Fatalf("filing from a template: %v", err)
	}
	if task.Title != "Deploy payments to staging" {
		t.Errorf("title = %q, want the value given and the default filled in", task.Title)
	}
	if task.Description != "Deploy payments, then watch it." {
		t.Errorf("description = %q, want the template's, filled in", task.Description)
	}
	// Everything else the template decides comes with it -- a filing
	// from a template is not a blank task with a title.
	if !task.AutoMerge {
		t.Errorf("autoMerge = false, want the template's")
	}
	if len(task.Capabilities) != 1 || task.Capabilities[0] != "gemini-key" {
		t.Errorf("capabilities = %v, want the template's", task.Capabilities)
	}
	if len(task.Reads) != 1 || task.Reads[0] != "acme/shared-lib" {
		t.Errorf("reads = %v, want the template's", task.Reads)
	}
}

// The template fills the blanks; a filing that states a field of its own
// keeps it. Somebody instantiating a template can still retitle the task
// or send it at another branch without editing the template.
func TestCreateTaskFromATemplateKeepsWhatTheRequestStates(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{service}}", Description: "Deploy it.",
		Fields: []ui.TemplateField{{Name: "service", Default: "payments"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		TemplateID:  tmpl.ID,
		Title:       "Deploy payments by hand",
		Description: "Do it carefully.",
		Base:        "release",
		Approved:    true,
	})
	if err != nil {
		t.Fatalf("filing from a template: %v", err)
	}
	if task.Title != "Deploy payments by hand" || task.Description != "Do it carefully." {
		t.Errorf("task = %q / %q, want what the request stated", task.Title, task.Description)
	}
	if task.Base != "release" {
		t.Errorf("base = %q, want the branch the filing chose", task.Base)
	}
}

// A bound template decides where its firings go, here as everywhere
// else (model.Template.Target).
func TestCreateTaskFromABoundTemplateTargetsTheBinding(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy", Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatalf("filing from a bound template: %v", err)
	}
	if task.Repo != "acme/widgets" || task.Base != "release" {
		t.Errorf("placement = %q @ %q, want the binding's", task.Repo, task.Base)
	}
}

func TestCreateTaskFromATemplateRefusesValuesItCannotUse(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{environment}}",
		Fields: []ui.TemplateField{
			{Name: "environment", Options: []string{"staging", "production"}},
			{Name: "service", Required: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"a name the template does not declare", map[string]string{"service": "x", "enviroment": "staging"}, "no input field"},
		{"a value outside the options", map[string]string{"service": "x", "environment": "laptop"}, "not one of its options"},
		{"a required field left blank", map[string]string{"environment": "staging"}, "required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
				TemplateID: tmpl.ID, Fields: tc.fields, Approved: true,
			})
			var ve *ui.ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a ValidationError mentioning %q", err, tc.want)
			}
		})
	}
}

func TestCreateTaskRefusesFieldsWithNoTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "x", Fields: map[string]string{"service": "payments"}, Approved: true,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateTaskFromAnUnknownTemplateSaysSo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: "template-404", Approved: true})
	if err == nil || !strings.Contains(err.Error(), "template-404") {
		t.Fatalf("error = %v, want the missing template named", err)
	}
}

// The preview beside the form is the filing's own answer, computed
// without writing anything -- composition.go's own promise, applied to a
// template somebody is filling in.
func TestTemplateCompositionPreviewsTheFiling(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Deploy", Title: "Deploy {{service}} to {{environment}}",
		Description: "Watch it afterwards.",
		Fields: []ui.TemplateField{
			{Name: "service"},
			{Name: "environment", Options: []string{"staging", "production"}, Default: "staging"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	comp, err := c.TemplateComposition(ctx, tmpl.ID, "acme/widgets", "main",
		map[string]string{"service": "payments", "environment": "production"}, true)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if len(comp.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want the one task this files", comp.Tasks)
	}
	if comp.Tasks[0].Title != "Deploy payments to production" {
		t.Errorf("title = %q, want the values filled in", comp.Tasks[0].Title)
	}
	if comp.Repo != "acme/widgets" || comp.Base != "main" {
		t.Errorf("placement = %q @ %q, want what the form chose", comp.Repo, comp.Base)
	}
	// Nothing was written: a preview is a question.
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want a preview to file nothing", len(tasks))
	}
}

func TestTemplateCompositionWithNoRepoChosenYet(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Deploy", Title: "Deploy"})
	if err != nil {
		t.Fatal(err)
	}
	comp, err := c.TemplateComposition(ctx, tmpl.ID, "", "", nil, true)
	if err != nil {
		t.Fatalf("composing with no repo: %v", err)
	}
	if len(comp.Tasks) != 1 || comp.Tasks[0].Title != "Deploy" {
		t.Errorf("composition = %+v, want the content it already decides", comp.Tasks)
	}
}
