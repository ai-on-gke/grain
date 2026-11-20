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

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

func TestAGitHubReleaseRefusedByReviewedMergeNotifiesInsteadOfTriaging(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	sha := headSHA(t, client, repo, "main")

	r1 := reviewedmerge.Refusal{
		Commit:   "111111111111aaaa",
		Pull:     1626,
		MergedBy: "alice",
		HeadSHA:  "head1626abcdef0123456789",
		Reason:   "with no approval from an owner, member or collaborator other than its author, and not by grain",
	}
	r2 := reviewedmerge.Refusal{
		Commit:   "222222222222bbbb",
		Pull:     1608,
		MergedBy: "bob",
		HeadSHA:  "head1608abcdef0123456789",
		Reason:   "and carol's approval was of an earlier head than the head1608abcd that was merged",
	}

	sim.WorkflowRuns = []githubsim.WorkflowRun{{
		ID: 1401, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{
			Name: "check reviewed merge", Conclusion: "failure", FailingStep: "Check reviewed-merge chain",
			Log: "2026-11-16T10:09:00.0000000Z reviewed-merge: refused: 2 unreviewed commit(s) between 111111111111 and 000000000000\n" +
				"2026-11-16T10:09:00.0000001Z " + reviewedmerge.FormatRefusalLine(r1) + "\n" +
				"2026-11-16T10:09:00.0000002Z " + reviewedmerge.FormatRefusalLine(r2) + "\n",
		}},
	}}
	deps := orchestrator.Deps{Store: store, Client: client}

	at := baseTime
	runGitHubCITails(t, ctx, deps, at)

	if ids := releaseTriaged(t, store); len(ids) != 0 {
		t.Fatalf("triage = %v, want no triage task on reviewed-merge refusal", ids)
	}
	subject := orchestrator.UnreviewedMergeNotificationSubject(repo)
	if subject != "pipeline.unreviewed-merge:acme/widgets" {
		t.Fatalf("subject = %q, want pipeline.unreviewed-merge:acme/widgets", subject)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("OpenNotification(%q) = %+v, %v; want open warning notification", subject, n, err)
	}
	if n.Severity != model.NotificationWarning {
		t.Fatalf("severity = %q, want warning", n.Severity)
	}
	for _, want := range []string{
		"[PR #1626](https://github.com/acme/widgets/pull/1626)",
		"alice",
		"head1626abcdef0123456789",
		"[PR #1608](https://github.com/acme/widgets/pull/1608)",
		"bob",
		"head1608abcdef0123456789",
		"an owner, member or collaborator who is not the author approves the merged head; then re-run release.yml",
	} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("notification body missing %q:\n%s", want, n.Body)
		}
	}
	firstID := n.ID

	// PR #1626 is approved and release.yml is re-run; PR #1608 still blocks it.
	// The existing notification must be updated in place (same ID, PR #1626 removed).
	sim.WorkflowRuns[0].RunAttempt = 2
	sim.WorkflowRuns[0].Jobs[0].Log = "2026-11-16T10:20:00.0000000Z reviewed-merge: refused: 1 unreviewed commit(s) between 111111111111 and 000000000000\n" +
		"2026-11-16T10:20:00.0000001Z " + reviewedmerge.FormatRefusalLine(r2) + "\n"
	at = at.Add(10 * time.Minute)
	runGitHubCITails(t, ctx, deps, at)

	if ids := releaseTriaged(t, store); len(ids) != 0 {
		t.Fatalf("after attempt 2: triage = %v, want none", ids)
	}
	n2, err := store.OpenNotification(ctx, subject)
	if err != nil || n2 == nil {
		t.Fatalf("after attempt 2: OpenNotification = %+v, %v", n2, err)
	}
	if n2.ID != firstID {
		t.Fatalf("notification ID changed from %d to %d; want in-place update", firstID, n2.ID)
	}
	if n2.RaiseCount != 2 {
		t.Fatalf("RaiseCount = %d, want 2", n2.RaiseCount)
	}
	if strings.Contains(n2.Body, "#1626") {
		t.Errorf("updated notification still lists approved PR #1626:\n%s", n2.Body)
	}
	if !strings.Contains(n2.Body, "[PR #1608](https://github.com/acme/widgets/pull/1608)") {
		t.Errorf("updated notification dropped remaining PR #1608:\n%s", n2.Body)
	}

	// PR #1608 is approved and release.yml publishes; notification clears.
	sim.WorkflowRuns[0].RunAttempt = 3
	sim.WorkflowRuns[0].Conclusion = "success"
	sim.WorkflowRuns[0].Jobs = []githubsim.WorkflowJob{
		{Name: "check reviewed merge", Conclusion: "success"},
		{Name: "release images", Conclusion: "success"},
	}
	at = at.Add(10 * time.Minute)
	runGitHubCITails(t, ctx, deps, at)

	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("after release published: OpenNotification = %+v, %v; want cleared", open, err)
	}
}

func TestAGitHubReleaseRefusedByLegacyReviewedMergeLogNotifies(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	sha := headSHA(t, client, repo, "main")

	sim.WorkflowRuns = []githubsim.WorkflowRun{{
		ID: 1402, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{
			Name: "release images", Conclusion: "failure", FailingStep: "Publish the images",
			Log: "2026-11-16T10:50:12.0000000Z reviewed-merge: refused: aaaaaaaaaaaa carries bbbbbbbbbbbb, which it was built on: bbbbbbbbbbbb did not reach main through a reviewed merge: pull request #1403 was merged by carol, with no approval from an owner, member or collaborator other than its author, and not by grain\n" +
				"2026-11-16T10:50:12.0000001Z release-images.sh: refusing to publish aaaaaaa: it did not reach main through a reviewed merge (above)\n",
		}},
	}}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

	if ids := releaseTriaged(t, store); len(ids) != 0 {
		t.Fatalf("triage = %v, want none on legacy reviewed-merge refusal", ids)
	}
	n, err := store.OpenNotification(ctx, orchestrator.UnreviewedMergeNotificationSubject(repo))
	if err != nil || n == nil {
		t.Fatalf("OpenNotification = %+v, %v", n, err)
	}
	if !strings.Contains(n.Body, "[PR #1403](https://github.com/acme/widgets/pull/1403)") || !strings.Contains(n.Body, "carol") {
		t.Fatalf("legacy refusal notification missing PR #1403 / carol:\n%s", n.Body)
	}
}

func TestAGitHubReleaseDirectPushRefusalNotifiesAndMixedFailureTriages(t *testing.T) {
	t.Run("direct push refusal notifies with commit sha", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		defer orchestrator.SetGitHubCIBudgetLow(false)()
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
			t.Fatal(err)
		}
		sha := headSHA(t, client, repo, "main")

		direct := reviewedmerge.Refusal{
			Commit: "333333333333cccc",
			Reason: "not the merge commit of any pull request merged into main or release/v*: pushed onto the branch, not merged through a pull request",
		}
		sim.WorkflowRuns = []githubsim.WorkflowRun{{
			ID: 1405, Name: "release", Path: ".github/workflows/release.yml",
			HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
			Jobs: []githubsim.WorkflowJob{{
				Name: "check reviewed merge", Conclusion: "failure", FailingStep: "Check reviewed-merge chain",
				Log: "2026-11-16T10:15:00.0000000Z " + reviewedmerge.FormatRefusalLine(direct) + "\n",
			}},
		}}
		runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

		if ids := releaseTriaged(t, store); len(ids) != 0 {
			t.Fatalf("triage = %v, want none on direct-push refusal", ids)
		}
		n, err := store.OpenNotification(ctx, orchestrator.UnreviewedMergeNotificationSubject(repo))
		if err != nil || n == nil {
			t.Fatalf("OpenNotification = %+v, %v", n, err)
		}
		if !strings.Contains(n.Body, "333333333333") || !strings.Contains(n.Body, "pushed directly") {
			t.Fatalf("direct-push notification missing commit/detail:\n%s", n.Body)
		}
	})

	t.Run("mixed reviewed-merge and unrelated job failure still files triage", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		defer orchestrator.SetGitHubCIBudgetLow(false)()
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
			t.Fatal(err)
		}
		sha := headSHA(t, client, repo, "main")

		r1 := reviewedmerge.Refusal{
			Commit:   "111111111111aaaa",
			Pull:     1626,
			MergedBy: "alice",
			HeadSHA:  "head1626",
			Reason:   "with no approval from an owner, member or collaborator other than its author, and not by grain",
		}
		sim.WorkflowRuns = []githubsim.WorkflowRun{{
			ID: 1406, Name: "release", Path: ".github/workflows/release.yml",
			HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
			Jobs: []githubsim.WorkflowJob{
				{
					Name: "check reviewed merge", Conclusion: "failure", FailingStep: "Check reviewed-merge chain",
					Log: reviewedmerge.FormatRefusalLine(r1) + "\n",
				},
				{
					Name: "release images", Conclusion: "failure", FailingStep: "Build the images",
					Log: "ERROR: failed to solve: Dockerfile syntax error\n",
				},
			},
		}}
		runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

		if ids := releaseTriaged(t, store); len(ids) != 1 {
			t.Fatalf("triage = %v, want 1 triage task when another job failed for an unrelated reason", ids)
		}
		if open, err := store.OpenNotification(ctx, orchestrator.UnreviewedMergeNotificationSubject(repo)); err != nil || open != nil {
			t.Fatalf("OpenNotification = %+v, %v; want none for mixed failure", open, err)
		}
	})
}

func TestAGitHubReleaseOtherFailureAndForgeErrorStillFileTriage(t *testing.T) {
	cases := []struct {
		name        string
		runID       int64
		jobName     string
		failingStep string
		log         string
	}{
		{
			name:        "other release images failure",
			runID:       1403,
			jobName:     "release images",
			failingStep: "Run the container e2e suite against what is about to be published",
			log:         "2026-11-16T10:30:00.0000000Z --- FAIL: TestContainerBoot (12.00s)\n",
		},
		{
			name:        "reviewed-merge forge error is not a policy refusal",
			runID:       1404,
			jobName:     "check reviewed merge",
			failingStep: "Check reviewed-merge chain",
			log:         "2026-11-16T10:30:00.0000000Z reviewed-merge: forge error, not a policy refusal: could not read the pull requests for abc123456789: 403 Forbidden\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			defer orchestrator.SetGitHubCIBudgetLow(false)()
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
				t.Fatal(err)
			}
			sha := headSHA(t, client, repo, "main")

			sim.WorkflowRuns = []githubsim.WorkflowRun{{
				ID: tc.runID, Name: "release", Path: ".github/workflows/release.yml",
				HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
				Jobs: []githubsim.WorkflowJob{{
					Name: tc.jobName, Conclusion: "failure", FailingStep: tc.failingStep,
					Log: tc.log,
				}},
			}}
			runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

			if ids := releaseTriaged(t, store); len(ids) != 1 {
				t.Fatalf("triage = %v, want 1 triage task", ids)
			}
			if open, err := store.OpenNotification(ctx, orchestrator.UnreviewedMergeNotificationSubject(repo)); err != nil || open != nil {
				t.Fatalf("OpenNotification = %+v, %v; want none for non-refusal failure", open, err)
			}
		})
	}
}
