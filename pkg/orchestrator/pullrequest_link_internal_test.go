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
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func fixesTargets(task model.Task) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			out = append(out, l.Target)
		}
	}
	return out
}

// A task moved from bwsalmon/grain to ai-on-gke/grain opens its pull
// request in the new repository: that one replaces the old repository's
// rather than being linked beside it, while an earlier pull request in
// the same repository (a reopened task's merged one) stays.
func TestLinkPullRequestReplacesAPullRequestInTheRepoTheTaskLeft(t *testing.T) {
	t.Parallel()
	store := openInternalStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "ai-on-gke", Name: "grain"}
	task := model.Task{
		ID: "t1493", Intent: model.IntentImplement, Title: "retargeted", Target: &repo,
		Links: []model.Link{
			{Kind: model.LinkFixes, Target: "bwsalmon/grain#5044"},
			{Kind: model.LinkFixes, Target: "ai-on-gke/grain#10"},
			{Kind: model.LinkProposedBy, Target: "t1"},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	ref := model.PullRequestRef{Repo: repo, Number: 100}
	for range 2 { // idempotent
		if err := linkPullRequest(ctx, store, task.ID, ref); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ai-on-gke/grain#10", "ai-on-gke/grain#100"}; !slices.Equal(fixesTargets(*got), want) {
		t.Fatalf("LinkFixes = %v, want %v", fixesTargets(*got), want)
	}
	if linkTarget(*got, model.LinkProposedBy) != "t1" {
		t.Fatalf("links = %+v, want the other links kept", got.Links)
	}
	if pr := model.TaskPullRequest(*got); pr == nil || *pr != ref {
		t.Fatalf("TaskPullRequest = %v, want %v", pr, ref)
	}
}
