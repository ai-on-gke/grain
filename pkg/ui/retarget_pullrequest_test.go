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

// Moving a task to another repository takes the pull request a run
// opened in the old one off it, with what was pinned to that pull
// request's head; restating the repository it already has takes nothing.
func TestUpdateTaskRetargetingTheRepoDropsThePullRequestInTheOldOne(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "moving", Repo: "bwsalmon/grain", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkFixes, Target: "bwsalmon/grain#5044"},
			model.Link{Kind: model.LinkSecurityHold, Target: "v1"},
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "abc123"},
			model.Link{Kind: model.LinkHumanSubmitted, Target: "2026-10-28T00:00:00Z@abc123"},
			model.Link{Kind: model.LinkGoogleChat, Target: "spaces/x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	same, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: ptr("BWSalmon/Grain")})
	if err != nil {
		t.Fatal(err)
	}
	if same.PullRequest != "bwsalmon/grain#5044" {
		t.Fatalf("PullRequest = %q, want it kept by an edit that moves nothing", same.PullRequest)
	}

	moved, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{Repo: ptr("ai-on-gke/grain")})
	if err != nil {
		t.Fatal(err)
	}
	if moved.PullRequest != "" {
		t.Fatalf("PullRequest = %q, want none after the move", moved.PullRequest)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[model.LinkKind]bool{}
	for _, l := range got.Links {
		kinds[l.Kind] = true
	}
	for _, k := range []model.LinkKind{model.LinkFixes, model.LinkSecurityHold,
		model.LinkTrustBoundaryCleared, model.LinkHumanSubmitted} {
		if kinds[k] {
			t.Errorf("links = %+v, want no %s left", got.Links, k)
		}
	}
	if !kinds[model.LinkGoogleChat] {
		t.Errorf("links = %+v, want unrelated links kept", got.Links)
	}
}
