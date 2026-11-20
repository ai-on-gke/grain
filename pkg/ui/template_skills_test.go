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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestTaskSkillsComposeWithTheTemplatesFieldByField: a task filed from a
// template that carries skills: {folders: [...]} and sets only part of
// skills itself keeps the part it did not set from the template, the way
// every other skills layer composes (docs/test-runs/2026-11-11-repo-skills.md,
// finding F2). Before, the task's skills replaced the template's whole, so
// {enabled: true} silently dropped the template's folders.
func TestTaskSkillsComposeWithTheTemplatesFieldByField(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tplFolders := []string{"layer/template"}
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "skills", Title: "Skills", Repo: "acme/widgets",
		Skills: &model.SkillsConfig{Folders: &tplFolders},
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	yes, no := true, false
	taskFolders := []string{"layer/task"}
	empty := []string{}
	for _, tc := range []struct {
		name        string
		skills      *model.SkillsConfig
		wantEnabled *bool
		wantFolders *[]string
	}{
		{"unset takes the template's", nil, nil, &tplFolders},
		{"enabled only keeps the template's folders", &model.SkillsConfig{Enabled: &yes}, &yes, &tplFolders},
		{"enabled false still turns it off", &model.SkillsConfig{Enabled: &no}, &no, &tplFolders},
		{"folders win over the template's", &model.SkillsConfig{Folders: &taskFolders}, nil, &taskFolders},
		{"folders [] wins over the template's", &model.SkillsConfig{Folders: &empty}, nil, &empty},
	} {
		task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			TemplateID: tmpl.ID, Title: tc.name, Description: "x", Skills: tc.skills,
		})
		if err != nil {
			t.Fatalf("%s: filing from the template: %v", tc.name, err)
		}
		got := task.Skills
		if got == nil {
			t.Fatalf("%s: task skills = nil", tc.name)
		}
		if (got.Enabled == nil) != (tc.wantEnabled == nil) || (got.Enabled != nil && *got.Enabled != *tc.wantEnabled) {
			t.Errorf("%s: enabled = %v, want %v", tc.name, got.Enabled, tc.wantEnabled)
		}
		if got.Folders == nil || !slices.Equal(*got.Folders, *tc.wantFolders) {
			t.Errorf("%s: folders = %v, want %v", tc.name, got.Folders, *tc.wantFolders)
		}
	}
	bad := []string{"../escape"}
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		TemplateID: tmpl.ID, Title: "bad", Description: "x", Skills: &model.SkillsConfig{Folders: &bad},
	}); err == nil {
		t.Error("a task skills folder escaping the repository was accepted when filed from a template")
	}
}
