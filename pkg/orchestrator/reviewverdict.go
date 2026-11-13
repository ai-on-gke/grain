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
	"log"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A review's record says what it read and what it found, so that a report
// over many reviews -- findings by category, reviews per pull request head
// -- reads them off the task rather than out of the free text of its
// closing note.
//
// What it read is model.LinkReviewedHead. Each filer that has the head in
// hand records it as it files the review: a gating trigger's review
// (fileCoalesced), an external pull request's (fileExternalPRReview) and
// the security hold's (putSecurityReview). A declared stacked review is
// filed by SyncReviews, which reads no GitHub at all, so its head is the
// reviewed branch's when its first run is dispatched
// (recordDispatchedReviewHead).
//
// What it found is model.LinkReviewVerdict, written as each run of the
// review finishes (recordReviewVerdict) from what the run itself did: a
// question it parked on, the findings it left (add_review_comment, each
// with its category), and fixes it pushed.

// recordDispatchedReviewHead records, as a review's run is dispatched, the
// head of the branch it reviews -- for a review whose filer recorded none.
// Best-effort: a head that cannot be read is logged and left unrecorded,
// and nothing about the run depends on it.
func recordDispatchedReviewHead(ctx context.Context, store *model.Store, client github.Client, task model.Task) {
	if store == nil || client == nil || !isReviewTask(task) || task.Target == nil || task.ChangeTarget != nil ||
		model.ReviewedHead(task) != "" || linkTarget(task, model.LinkExternalPR) != "" {
		return
	}
	// Every review is filed with the reviewed change's branch as its base
	// (newReview); one reviewing in place works on that branch itself.
	branch := task.Base
	if _, inPlace := reviewsInPlace(task); inPlace || branch == "" {
		branch = task.BranchName()
	}
	head, err := client.GetBranchHead(task.Target.StoredOwner(), task.Target.Name, branch)
	if err != nil || head == nil || head.SHA == "" {
		log.Printf("orchestrator: review %s: reading the head of %s it reviews: %v -- recording none", task.ID, branch, err)
		return
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if model.ReviewedHead(*t) == "" {
			model.SetReviewedHead(t, head.SHA)
		}
		return nil
	}); err != nil {
		log.Printf("orchestrator: review %s: recording the head it reviews: %v", task.ID, err)
	}
}

// reviewVerdictOf is the verdict a finished run of a review reached:
// questions when it asked a person something (asked), findings when it
// left any or pushed fixes of its own, clean otherwise.
func reviewVerdictOf(findings []reviewFinding, asked, pushed bool) model.ReviewVerdictRecord {
	r := model.ReviewVerdictRecord{Verdict: model.VerdictClean, FindingCount: len(findings), FixesPushed: pushed}
	for _, f := range findings {
		r.Categories = append(r.Categories, model.NormalizeReviewFindingCategory(f.Category))
	}
	switch {
	case asked:
		r.Verdict = model.VerdictQuestions
	case len(findings) > 0 || pushed:
		r.Verdict = model.VerdictFindings
	}
	return r
}

// recordReviewVerdict records on review the verdict of its run runID --
// unless that run left the review waiting (a suspension, or a park on
// anything but a question), which reaches none. The run left result (its
// sandbox's review comments included), asked a person something when
// asked, and -- pushedInPlace -- pushed fixes to the branch under review.
// A stacked review's own fixes are read off its branch here. Nothing here
// fails the finish: a verdict that cannot be written is logged, and the
// run's own outcome stands.
func recordReviewVerdict(ctx context.Context, store *model.Store, client github.Client, review model.Task,
	result *agent.Result, runID string, asked, pushedInPlace bool, now time.Time) {

	if store == nil || result == nil || !isReviewTask(review) || (!asked && leftWaiting(result)) {
		return
	}
	pushed := pushedInPlace
	if _, inPlace := reviewsInPlace(review); !inPlace && !asked {
		pushed = stackedReviewPushed(client, review)
	}
	verdict := reviewVerdictOf(reviewFindings(result), asked, pushed)
	verdict.RunID, verdict.At = runID, now
	if err := store.UpdateTask(ctx, review.ID, func(t *model.Task) error {
		verdict.Head = model.ReviewedHead(*t)
		model.SetReviewVerdict(t, verdict)
		return nil
	}); err != nil {
		log.Printf("orchestrator: review %s: recording its verdict: %v", review.ID, err)
	}
}

// stackedReviewPushed reports whether a review working on a branch of its
// own has commits there its base -- the branch under review -- does not:
// fixes it pushed. A review that only posts findings
// (model.LinkReviewCommentsOnly) pushes nothing, and a branch that cannot
// be compared reads as nothing pushed.
func stackedReviewPushed(client github.Client, review model.Task) bool {
	if client == nil || review.Target == nil || review.ChangeTarget != nil || review.Base == "" ||
		linkTarget(review, model.LinkReviewCommentsOnly) != "" || linkTarget(review, model.LinkExternalPR) != "" {
		return false
	}
	branch := review.BranchName()
	if branch == "" || branch == review.Base {
		return false
	}
	cmp, err := client.CompareRefs(review.Target.StoredOwner(), review.Target.Name, review.Base, branch)
	if err != nil {
		return false
	}
	return len(cmp.Commits) > 0
}

// leftWaiting reports whether result made a call that leaves its task open
// and waiting (parkingTools): a run that has not reached a verdict yet.
func leftWaiting(result *agent.Result) bool {
	for _, c := range result.ToolCalls {
		if !c.IsError && slices.Contains(parkingTools, c.Name) {
			return true
		}
	}
	return false
}
