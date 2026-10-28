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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestSyncPullRequestsClosesOutAMergedPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	// GitHub merges the PR "out of band" -- SyncPullRequests must notice
	// on its own next read, not be told.
	if err := client.MergePullRequest("acme", "widgets", pr.Number, ""); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}
	// Closing out is one store write. There is no issue to close alongside
	// it, so nothing here can end up with a closed issue and a store that
	// still believes the task is open.
	if len(sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.Issues)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PrMergedAt == nil {
		t.Fatal("expected PrMergedAt to be set for a merged pull request")
	}
	if obs.PrClosedAt != nil {
		t.Fatal("expected PrClosedAt to stay nil for a merged pull request")
	}
	if obs.PrOpenedAt == nil {
		t.Fatal("expected PrOpenedAt to be set alongside PrMergedAt")
	}
}

func TestSyncPullRequestsAutoClosesCorrelatedGitHubIssueOnMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Issue = 42
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	sim.Issues[42] = &githubsim.Issue{Title: "parser bug", Body: "details", State: "open", Labels: map[string]struct{}{}}
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected 1 pull request, got %d", len(sim.PullRequests))
	}
	if !strings.Contains(sim.PullRequests[0].Body, "Fixes #42") {
		t.Fatalf("pr body = %q, want Fixes #42", sim.PullRequests[0].Body)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	// GitHub merges the PR -- the Fixes #42 directive causes the simulated GitHub
	// to auto-close the correlated issue.
	if err := client.MergePullRequest("acme", "widgets", pr.Number, ""); err != nil {
		t.Fatal(err)
	}

	if issue := sim.Issues[42]; issue.State != "closed" {
		t.Fatalf("issue state = %q, want closed", issue.State)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}
}

func TestSyncPullRequestsRecordsAPullRequestClosedWithoutMerging(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	// A human closes the PR on GitHub without merging it -- Sim's own
	// doc comment on PullRequest.State says a test does this by setting
	// State directly, standing in for GitHub's own close-without-merge
	// button (there is no merge involved, so Merged stays false).
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PrClosedAt == nil {
		t.Fatal("expected PrClosedAt to be set for a pull request closed without merging")
	}
	if obs.PrMergedAt != nil {
		t.Fatal("expected PrMergedAt to stay nil for a pull request closed without merging")
	}
}

// How big the merged diff was, recorded on the cycle that stamps
// PrMergedAt off the same response -- what a week's release notes count
// their lines-changed figure from (model.ClusterStats). And the other
// half of it: a merge GitHub reported no size for is left unrecorded
// rather than written down as a pull request that changed nothing.
func TestSyncPullRequestsRecordsHowBigTheMergedDiffWas(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                          string
		additions, deletions, changed int
		want                          bool
	}{
		{name: "GitHub reported a size", additions: 214, deletions: 37, changed: 9, want: true},
		{name: "GitHub reported nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task := filedTask(t, ctx, store, "t1", repo)
			pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

			pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
			if err != nil {
				t.Fatal(err)
			}
			task.Links = append(task.Links, model.Link{
				Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
			})
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
				t.Fatal(err)
			}
			for i := range sim.PullRequests {
				sim.PullRequests[i].Additions = c.additions
				sim.PullRequests[i].Deletions = c.deletions
				sim.PullRequests[i].ChangedFiles = c.changed
			}
			if err := client.MergePullRequest("acme", "widgets", pr.Number, ""); err != nil {
				t.Fatal(err)
			}

			if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}

			obs, err := store.GetObservation(ctx, task.ID)
			if err != nil || obs == nil {
				t.Fatalf("get observation: (%+v, %v)", obs, err)
			}
			if obs.PrMergedAt == nil {
				t.Fatal("expected the merge itself to have been recorded")
			}
			if !c.want {
				if obs.PrAdditions != nil || obs.PrDeletions != nil || obs.PrChangedFiles != nil {
					t.Fatalf("recorded a size of +%v -%v over %v files, want none recorded at "+
						"all: a response carrying no size is not a diff that changed nothing",
						obs.PrAdditions, obs.PrDeletions, obs.PrChangedFiles)
				}
				return
			}
			if obs.PrAdditions == nil || *obs.PrAdditions != c.additions {
				t.Errorf("PrAdditions = %v, want %d", obs.PrAdditions, c.additions)
			}
			if obs.PrDeletions == nil || *obs.PrDeletions != c.deletions {
				t.Errorf("PrDeletions = %v, want %d", obs.PrDeletions, c.deletions)
			}
			if obs.PrChangedFiles == nil || *obs.PrChangedFiles != c.changed {
				t.Errorf("PrChangedFiles = %v, want %d", obs.PrChangedFiles, c.changed)
			}
		})
	}
}

func TestSyncPullRequestsLeavesAnOpenCleanPullRequestAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want it still watched and not yet closed", st)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.ClosedAt != nil {
		t.Fatal("expected the task not to have been closed out yet")
	}
	// PrOpenedAt is recorded the first cycle SyncPullRequests sees the
	// pull request at all, whether or not it is done -- an open PR left
	// alone still gets its own "PR opened" timeline moment recorded.
	if obs == nil || obs.PrOpenedAt == nil {
		t.Fatal("expected PrOpenedAt to be recorded even for a still-open pull request")
	}
}

func TestSyncPullRequestsAutoMergesACleanPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	clean := true
	for i := range sim.PullRequests {
		sim.PullRequests[i].Mergeable = &clean
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed once auto-merge landed", st)
	}
}

// A task that completes a merge-queue repair attempt must clear RetryRequestedAt
// and allow SyncPullRequests to merge the settled repair PR without re-requesting repair.
func TestSyncPullRequestsMergesCompletedRepairWithoutRedispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	// Step 1: Head is conflicted, SyncPullRequests requeues for repair.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRepairAt == nil || obs.RetryRequestedAt == nil {
		t.Fatalf("expected repair queued: %+v", obs)
	}

	// Step 2: Repair finishes cleanly.
	repairTime := baseTime.Add(time.Hour)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-2", repairTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.RetryRequestedAt != nil {
		t.Fatalf("expected RetryRequestedAt to be nil, got %v", obs.RetryRequestedAt)
	}

	// Step 3: SyncPullRequests reconciles the clean settled PR and merges it.
	if err := orchestrator.SyncPullRequests(ctx, store, client, repairTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after repair: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed after merge", st)
	}
}
