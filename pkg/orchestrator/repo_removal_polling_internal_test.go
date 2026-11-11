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
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// After a repository is removed -- the same two writes
// ui.Client.RemoveTargetRepoForTeam makes, Store.ForgetRepo and the
// narrowed TargetRepos -- nothing grain polls GitHub for still names it,
// even though an operator-made workflow-run schedule targeting it
// outlives the removal (grain/task-4288). Its neighbour is polled exactly
// as before.
func TestARemovedRepositoryIsNoLongerPolled(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	gone := model.RepoRef{Owner: "acme", Name: "widgets"}
	kept := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := store.PutConfig(ctx, model.Config{
		PollInterval: model.DefaultPollInterval,
		TargetRepos:  []string{gone.String(), kept.String()},
	}); err != nil {
		t.Fatal(err)
	}
	tmplID := model.GitHubWorkflowReviewTemplateID("nightly")
	for _, repo := range []model.RepoRef{gone, kept} {
		if err := store.PutSchedule(ctx, model.Schedule{
			ID: "review-nightly-" + repo.Name, Title: "Review nightly", Target: repo, Enabled: true,
			Overlap: model.OverlapSkip, TemplateID: &tmplID,
			Recurrence: model.Recurrence{
				Kind: model.RecurrenceGitHubWorkflowRun,
				GitHubWorkflowRun: &model.GitHubWorkflowRunTrigger{
					Workflow: "nightly.yml", Conclusions: []string{"failure"}, Branches: []string{"main"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
		// A base-branch watch with a run in flight.
		started := time.Now().UTC()
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo: repo, Branch: "release", RunID: "run-" + repo.Name, Commit: "abc123", StartedAt: started,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
			t.Fatal(err)
		}
	}

	polled := func() map[string][]string {
		t.Helper()
		targets, err := gitHubWorkflowRunPollTargets(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, target := range targets {
			branches := slices.Clone(target.branches)
			slices.Sort(branches)
			out[target.repo.String()] = branches
		}
		return out
	}
	before := polled()
	for _, repo := range []model.RepoRef{gone, kept} {
		if got := before[repo.String()]; !slices.Equal(got, []string{"main", "release"}) {
			t.Fatalf("before removal %s is polled on %v, want main and release", repo, got)
		}
	}

	if err := store.ForgetRepo(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.TargetRepos = []string{kept.String()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	after := polled()
	if got, ok := after[gone.String()]; ok {
		t.Errorf("removed %s is still polled for GitHub workflow runs on %v", gone, got)
	}
	if got := after[kept.String()]; !slices.Equal(got, []string{"main", "release"}) {
		t.Errorf("removing %s changed what %s is polled on: %v", gone, kept, got)
	}
	// The operator's schedule is theirs to delete; it stays, and is the
	// only thing that still names the removed repository.
	scheds, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(scheds, func(s model.Schedule) bool { return s.Target.Same(gone) }) {
		t.Error("removing a repository deleted an operator-made schedule targeting it")
	}
	// reconcileDeclarations walks repo_config rows with Declarations on;
	// the removed repository's row is gone, so it is never read again.
	configs, err := store.ListRepoConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range configs {
		if rc.Repo.Same(gone) {
			t.Errorf("removed %s still has a repo_config row (declarations %v)", gone, rc.Declarations)
		}
	}
	if bci, err := store.GetBaseBranchCI(ctx, gone, "release"); err != nil || bci != nil {
		t.Errorf("removed %s's in-flight base-branch run = %+v, %v; want forgotten", gone, bci, err)
	}
}
