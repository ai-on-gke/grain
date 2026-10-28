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

package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// finishStateChange is finishTouching for any repository: an auto-merging
// task whose run pushed path to repo, taken through ProcessResult.
func finishStateChange(t *testing.T, store *model.Store, ctx context.Context, repo model.RepoRef, path string) model.Task {
	t.Helper()
	sim, client := newSim(t, repo.Owner, repo.Name, "main")
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pushFile(t, sim.BareRepo, branch, path, "changed\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if reviewID := linkOf(got, model.LinkSecurityReviewTask); reviewID != "" {
		completeReview(t, ctx, store, reviewID)
		if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got = reloaded(t, ctx, store, task.ID)
	}
	return got
}

// A grant merged into the state repository is applied to the running
// daemon on its next tick, so a change to one waits for a person: a clean
// review does not give auto-merge back.
func TestStateRepoGrantChangeIsHeldForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	got := finishStateChange(t, store, ctx, model.ConfigRepo(), "tables/template_grant.json")
	if linkOf(got, model.LinkSecurityReviewTask) == "" {
		t.Fatalf("no security review filed for a grant change: %+v", got.Links)
	}
	if got.AutoMerge {
		t.Fatal("a clean review released a change to tables/template_grant.json in the state repository")
	}
}

func TestStateRepoBoardChangeIsNotHeld(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	got := finishStateChange(t, store, ctx, model.ConfigRepo(), "tables/board.json")
	if !got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != "" {
		t.Fatalf("a board change was held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// The state repository's layout means nothing in any other repository.
func TestStatePathsInAnOrdinaryRepoAreNotHeld(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	store.SettingsRepo = func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "settings"} }
	got := finishStateChange(t, store, ctx, model.RepoRef{Owner: "acme", Name: "widgets"}, "tables/template_grant.json")
	if !got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != "" {
		t.Fatalf("an ordinary repository's tables/ was held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A deployment whose state lives in a repository of its own judges that
// one the same way.
func TestExternalStateRepoTemplateChangeIsHeldForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings := model.RepoRef{Owner: "acme", Name: "settings"}
	store.SettingsRepo = func() model.RepoRef { return settings }
	got := finishStateChange(t, store, ctx, settings, "settings/templates.yml")
	if linkOf(got, model.LinkSecurityReviewTask) == "" || got.AutoMerge {
		t.Fatalf("a template change to the external state repository merges itself: automerge=%v links=%+v",
			got.AutoMerge, got.Links)
	}
}
