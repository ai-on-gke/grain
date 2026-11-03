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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A template names a hostd selector (model.Template.Hostd): stored,
// normalized, edited and cleared through the templates API, and a task
// filed from it starts pinned to it -- unless the filing names its own
// hostd, which wins.
func TestTemplateHostdPinsTheTasksItFiles(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	registerHostds(t, store, model.Hostd{Name: "gpu-box", Labels: map[string]string{"gpu": "yes", "zone": "b"}}, model.Hostd{Name: "laptop"})

	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "bad", Title: "bad", Description: "bad", Hostd: "Not A Hostd",
	}); err == nil {
		t.Error("CreateTemplate accepted a hostd that is neither a name nor labels")
	}
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "train", Title: "Train the model", Description: "on a GPU", Hostd: "zone=b, gpu=yes",
	})
	if err != nil {
		t.Fatalf("creating a template with a hostd: %v", err)
	}
	if tmpl.Hostd != "gpu=yes,zone=b" {
		t.Fatalf("template.hostd on the wire = %q, want the selector, labels sorted", tmpl.Hostd)
	}

	filed, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatalf("filing from the template: %v", err)
	}
	if got, err := store.TaskHostd(ctx, filed.ID); err != nil || got != "gpu=yes,zone=b" {
		t.Errorf("task filed from the template is pinned to %q, %v; want gpu=yes,zone=b", got, err)
	}
	if detail, err := c.GetTask(ctx, filed.ID); err != nil || detail.Hostd != "gpu=yes,zone=b" {
		t.Errorf("task detail hostd = %q, %v; want the template's", detail.Hostd, err)
	}

	own, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Hostd: "laptop", Approved: true})
	if err != nil {
		t.Fatalf("filing from the template with a hostd of its own: %v", err)
	}
	if got, _ := store.TaskHostd(ctx, own.ID); got != "laptop" {
		t.Errorf("task's own hostd = %q, want laptop to beat the template's", got)
	}

	edited, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Hostd: ptr("workstation")})
	if err != nil || edited.Hostd != "workstation" {
		t.Fatalf("editing the template's hostd = %q, %v; want workstation", edited.Hostd, err)
	}
	if _, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Hostd: ptr("a=b,=c")}); err == nil {
		t.Error("UpdateTemplate accepted a label with no key")
	}
	cleared, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Hostd: ptr("")})
	if err != nil || cleared.Hostd != "" {
		t.Fatalf("clearing the template's hostd = %q, %v; want the default", cleared.Hostd, err)
	}
	unpinned, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.TaskHostd(ctx, unpinned.ID); got != "" {
		t.Errorf("task filed from a template with no hostd is pinned to %q, want the default hostd", got)
	}
}
