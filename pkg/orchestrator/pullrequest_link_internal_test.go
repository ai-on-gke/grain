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
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
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

func TestLinkPullRequestResetsSettledTimestampsAndScopesSecurityReviewSettle(t *testing.T) {
	t.Parallel()
	store := openInternalStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	pr1 := model.PullRequestRef{Repo: repo, Number: 954}
	pr2 := model.PullRequestRef{Repo: repo, Number: 1023}

	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	opened1 := now.Add(-2 * time.Hour)
	merged1 := now.Add(-time.Hour)
	add1, del1, files1 := 7, 1, 1

	task := model.Task{
		ID: "t3546", Intent: model.IntentImplement, Title: "release train", Target: &repo,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Links:    []model.Link{{Kind: model.LinkFixes, Target: pr1.String()}},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID:         task.ID,
		PrOpenedAt:     &opened1,
		PrMergedAt:     &merged1,
		PrAdditions:    &add1,
		PrDeletions:    &del1,
		PrChangedFiles: &files1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := linkPullRequest(ctx, store, task.ID, pr2); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PrOpenedAt != nil || obs.PrMergedAt != nil || obs.PrClosedAt != nil ||
		obs.PrAdditions != nil || obs.PrDeletions != nil || obs.PrChangedFiles != nil {
		t.Fatalf("observation after linking %s = %+v, want settled stamps from %s cleared", pr2, obs, pr1)
	}

	// File a security review specifically for pr2 and verify settling pr1
	// cannot close it, even if closeSecurityReviewOnSettle is invoked directly.
	tmpl := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	updated, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	review, err := putSecurityReview(ctx, store, *updated, pr2, "", *tmpl, "Security review: release train", "body", now)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget := linkTarget(review, model.LinkReviewsPullRequest); gotTarget != pr2.String() {
		t.Fatalf("LinkReviewsPullRequest = %q, want %q", gotTarget, pr2)
	}
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkSecurityReviewTask, Target: review.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err = store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Even against a stale task snapshot that only knew pr1, LinkReviewsPullRequest
	// on the review itself blocks pr1 from closing pr2's review.
	staleSnapshot := *updated
	staleSnapshot.Links = []model.Link{
		{Kind: model.LinkFixes, Target: pr1.String()},
		{Kind: model.LinkSecurityReviewTask, Target: review.ID},
	}
	if err := closeSecurityReviewOnSettle(ctx, store, nil, staleSnapshot, pr1,
		github.PullRequestDetail{State: "closed", Merged: true, HeadSHA: "5b7319ce1234"}, model.PrMerged, now); err != nil {
		t.Fatalf("closeSecurityReviewOnSettle(pr1): %v", err)
	}
	if st, err := store.State(ctx, review.ID); err != nil || st == model.StateClosed {
		t.Fatalf("review state after pr1 settle = %q (%v), want still open", st, err)
	}

	// Settling pr2 itself closes pr2's unfinished review.
	if err := closeSecurityReviewOnSettle(ctx, store, nil, *updated, pr2,
		github.PullRequestDetail{State: "closed", Merged: true, HeadSHA: "0a9473c71234"}, model.PrMerged, now); err != nil {
		t.Fatalf("closeSecurityReviewOnSettle(pr2): %v", err)
	}
	if st, err := store.State(ctx, review.ID); err != nil || st != model.StateClosed {
		t.Fatalf("review state after pr2 settle = %q (%v), want closed", st, err)
	}
}
