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

package ui

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func TestGetTeamWithRepoWorkflowsEmptyPreloadSkipsStoreRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	c := &Client{Store: store}
	wfRepo := model.RepoRef{Owner: "acme", Name: "wf-only"}
	if err := store.PutRepoWorkflows(ctx, wfRepo, []model.RepoWorkflow{{
		Repo: wfRepo,
		Path: ".grain/workflows/ci.yml",
		Text: "on: [push]\n",
	}}); err != nil {
		t.Fatal(err)
	}

	// Passing an explicit empty preloaded slice marks workflowsLoaded=true so
	// getTeamWithRepoWorkflows must not redundantly re-query ListRepoWorkflows.
	team, err := c.getTeamWithRepoWorkflows(ctx, model.DefaultTeamID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range team.TargetRepos {
		if r == wfRepo.String() {
			t.Fatalf("TargetRepos unexpectedly included %q; empty preload re-read ListRepoWorkflows", r)
		}
	}

	// Without preloaded workflows, loadTeamRepoSourcesWith still reads them.
	src := c.loadTeamRepoSourcesWith(ctx, false, nil)
	if !src.workflows[wfRepo.String()] {
		t.Fatalf("workflows = %+v, want %q loaded when workflowsLoaded=false", src.workflows, wfRepo.String())
	}
}
