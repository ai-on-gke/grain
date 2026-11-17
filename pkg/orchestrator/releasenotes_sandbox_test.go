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

package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A release-notes template's sandbox shape (model.Template.Sandbox) is
// held to this deployment's maximum, as a schedule firing it is: one above
// it files nothing and says why, and one within it files the shape.
func TestReleaseNotesHoldsTemplateSandboxToDeploymentMax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maxCPUs int
		wantErr bool
	}{
		{name: "above the maximum", maxCPUs: 8, wantErr: true},
		{name: "within the maximum", maxCPUs: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetReleaseNotesWatch(t)
			ctx := context.Background()
			store := openTestStoreForOrchestrator(t)
			repo := model.RepoRef{Owner: "example", Name: "widgets"}

			cfg := model.DefaultConfig()
			cfg.TargetRepos = []string{repo.String()}
			cfg.SandboxShapeLimits = model.SandboxShapeLimits{Max: model.SandboxShape{CPUs: tc.maxCPUs}}
			if err := store.PutConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub, Declarations: true}); err != nil {
				t.Fatal(err)
			}
			tmpl := loadRepoReleaseNotesTemplate(t, repo)
			tmpl.Sandbox = model.SandboxShape{CPUs: 16}
			now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
			if _, err := store.SyncRepoDeclarations(ctx, repo, "abc1234", model.RepoDeclarations{
				Templates: []model.DeclaredTemplateFile{{Path: ".grain/templates/release-notes.yml", Template: tmpl}},
			}, now, time.UTC); err != nil {
				t.Fatalf("SyncRepoDeclarations: %v", err)
			}
			forge := &fixtureForge{
				repo: repo.String(),
				tags: map[string]fixtureTag{
					"v3.1.0": {commit: strings.Repeat("a", 40), message: "Release v3.1.0"},
					"v3.2.0": {commit: strings.Repeat("e", 40), message: "Release v3.2.0"},
				},
				files: map[string]string{
					"scripts/release.conf":    "RELEASE_MAJOR=3\n",
					"docs/releases/v3.1.0.md": "# v3.1.0\n",
				},
			}
			err := reconcileReleaseNotes(ctx, Deps{Store: store, Client: github.NewClient(forge, nil)}, now)
			tasks, lerr := store.ListTasks(ctx)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "above this deployment's maximum") {
					t.Fatalf("reconcileReleaseNotes = %v, want it to name the deployment's maximum", err)
				}
				if len(tasks) != 0 {
					t.Fatalf("filed %d tasks (%+v), want none for a 16-CPU template above an 8-CPU maximum", len(tasks), tasks)
				}
				return
			}
			if err != nil {
				t.Fatalf("reconcileReleaseNotes: %v", err)
			}
			if len(tasks) != 1 || tasks[0].SandboxCPUs != 16 {
				t.Fatalf("filed %+v, want one task carrying the template's 16 CPUs", tasks)
			}
		})
	}
}
