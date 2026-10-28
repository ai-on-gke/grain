package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/trustboundary"
)

// A security review works on the held change's own branch
// (model.LinkPushesReviewedBranch) rather than stacking a branch and a
// pull request of its own on it. What it can fix it fixes by pushing to
// that branch, so the fixes land in the change's own pull request: no
// second pull request, no second CI suite, no merge queue entry or repair
// cycle of its own, and nothing to merge back before the change is
// released.
//
// The branch's CI is the reviewed task's alone (model.Task.CIOwner). The
// review owns no CI status: reconcileTaskCI skips it, so it starts,
// retries and collects no run of its own. What its run asks of CI
// (wait_for_checks, run_ci_job, retrigger_ci) is asked as the reviewed
// task and recorded there (ui.Client.StartTaskCIRunOpt). What it reads
// back, on its task page and through pull_request_status, is that task's
// verdict. A fix it pushes moves the head, the reviewed task's reconcile
// starts the one run for the new head, and the review's wait_for_checks
// joins that run. The hold below reads no CI.
//
// Fixes alone do not need a human. A review that pushed fixes and left no
// finding or question releases the hold as a review that pushed nothing
// does, and clears the change at the head the review itself left
// (model.LinkReviewerHead, recordReviewerHead) -- when its fixes are off
// the trust boundary. Fixes that touch the boundary are never cleared on
// the reviewer's own say-so: that head is held afresh and reviewed again
// (reviewerFixesTouchBoundary), still with no human needed if the second
// review comes back clean. A commit anybody else pushes after it is a
// head clearedForHead has not seen, and is classified again before it
// merges. What a review could not fix -- a finding, a question -- still
// ends the hold for a human's Submit (dropSecurityHoldOnFindings).
//
// Only the review a hold names is listened to, as before, and a review
// closed -- superseded by a review of a newer head, or closed with its
// change -- may no longer push the branch at all (model.Store.
// GitPushBranch), nor record a head.
//
// Reviews filed before this, with a branch of their own, keep the stacked
// flow until they settle; so do declared reviews (fileReviewTask), which
// hold their change at the head of the merge queue rather than withdrawing
// its auto-merge, and whose fixes merge back through the queue.

// putSecurityReview files the security review of task: putStackedReview's
// review, working on task's own branch (putReview's inPlace).
func putSecurityReview(ctx context.Context, store *model.Store, task model.Task,
	tmpl model.Template, title, body string, now time.Time) (model.Task, error) {
	return putReview(ctx, store, task, tmpl, title, body, true, now)
}

// recordReviewerHead records, at the end of a run of review -- one working
// on the reviewed branch (reviewsInPlace) -- the head of that branch the
// run left, when it pushed there. pushed reports whether it recorded one.
//
// "Pushed" is the branch having moved past the head the hold was filed
// for, the one the reviewed task's LinkTrustBoundaryCleared names. The
// head is only the reviewer's to claim when nobody else in grain could
// have pushed there meanwhile: a run of the reviewed task started since
// the review was filed, or still going, may have pushed on top, and then
// nothing is recorded -- that run's own finish classifies what it pushed
// and files a fresh review, superseding this one. A closed review records
// nothing either. Recording nothing fails safe: the hold is released, if
// at all, with the clearance it already had, and the moved head is
// classified again before it merges.
//
// A head that cannot be read is logged and recorded as nothing, for the
// same reason.
func recordReviewerHead(ctx context.Context, store *model.Store, client github.Client,
	review model.Task, now time.Time) (pushed bool, err error) {

	reviewedID, ok := reviewsInPlace(review)
	if !ok || client == nil || review.Target == nil {
		return false, nil
	}
	closed, err := taskClosed(ctx, store, review.ID)
	if err != nil || closed {
		return false, err
	}
	reviewed, err := store.GetTask(ctx, reviewedID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s, reviewed by %s: %w", reviewedID, review.ID, err)
	}
	if reviewed == nil {
		return false, nil
	}
	head, err := client.GetBranchHead(review.Target.Owner, review.Target.Name, review.BranchName())
	if err != nil || head == nil || head.SHA == "" {
		log.Printf("orchestrator: review %s: reading the head it left on %s: %v -- recording none, "+
			"so that head is classified again before it merges", review.ID, review.BranchName(), err)
		return false, nil
	}
	if _, filedAt, ok := strings.Cut(linkTarget(*reviewed, model.LinkTrustBoundaryCleared), "@"); ok && filedAt == head.SHA {
		return false, nil
	}
	if raced, err := reviewedRanSince(ctx, store, reviewedID, review.CreatedAt); err != nil || raced {
		if raced {
			log.Printf("orchestrator: review %s: %s has run since the review was filed, so %s's head "+
				"is not the review's to claim; recording none", review.ID, reviewedID, review.BranchName())
		}
		return false, err
	}
	if err := store.UpdateTask(ctx, review.ID, func(t *model.Task) error {
		t.Links = append(withoutLinks(t.Links, model.LinkReviewerHead),
			model.Link{Kind: model.LinkReviewerHead, Target: head.SHA})
		return nil
	}); err != nil {
		return false, fmt.Errorf("orchestrator: recording the head review %s left: %w", review.ID, err)
	}
	return true, noteOnTask(ctx, store, review.ID, fmt.Sprintf(
		"This review pushed fixes to `%s`, the branch under review, up to `%s`. If it left no findings "+
			"or questions, task %s is cleared at that commit and merges automatically again.",
		review.BranchName(), shortSHA(head.SHA), reviewedID), now)
}

// reviewedRanSince reports whether reviewedID has a run still going, or
// one started after since.
func reviewedRanSince(ctx context.Context, store *model.Store, reviewedID string, since *time.Time) (bool, error) {
	runs, err := store.Runs(ctx, reviewedID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's runs: %w", reviewedID, err)
	}
	for _, r := range runs {
		if r.FinishedAt == nil || since == nil || r.StartedAt.After(*since) {
			return true, nil
		}
	}
	return false, nil
}

// reviewerFixesTouchBoundary reports whether the commits a review pushed
// onto task's branch -- from the head the hold was filed for, the one
// task's LinkTrustBoundaryCleared names, to reviewerHead -- touch a
// trust-boundary path, on grain's list or the repository's own. The
// invariant (operator, 2026-10-26, on this change's own security review):
// no agent's trust-boundary edits are cleared on that agent's own say-so.
// A review whose fixes stay off the boundary clears the head it left;
// one whose fixes touch it leaves a new head on the boundary that another
// review reads before it merges. It may be loosened later.
//
// It fails closed: a hold with no recorded head to compare from, or a
// comparison GitHub truncated, reads as touching. readable is false when
// the comparison could not be read at all, and the caller settles nothing
// until it can.
func reviewerFixesTouchBoundary(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, reviewerHead string) (touches, readable bool) {

	_, heldAt, ok := strings.Cut(linkTarget(task, model.LinkTrustBoundaryCleared), "@")
	if !ok || heldAt == "" || client == nil || task.Target == nil {
		return true, true
	}
	cmp, err := client.CompareRefs(task.Target.Owner, task.Target.Name, heldAt, reviewerHead)
	if err != nil {
		log.Printf("orchestrator: comparing %s's held head with its reviewer's: %v", task.ID, err)
		return false, false
	}
	if cmp.FilesTruncated {
		return true, true
	}
	var files []string
	for _, f := range cmp.Files {
		files = append(files, f.Filename)
		if f.PreviousFilename != "" {
			files = append(files, f.PreviousFilename)
		}
	}
	matches := trustboundary.Classify(files)
	_, guarded := repoGuard(ctx, store, *task.Target, files, true)
	return len(matches)+len(guarded) > 0, true
}

// reviewerFixesHeldNote says why a review's fixes did not release the
// change they were pushed to.
func reviewerFixesHeldNote(reviewID string, task model.Task, reviewerHead string) string {
	return fmt.Sprintf("The security review of this change (task %s) pushed fixes to this branch%s that "+
		"touch grain's trust boundary themselves. A reviewer's own trust-boundary edits are not cleared "+
		"on its own say-so, so the new head is held again and gets a review of its own; if that review "+
		"finishes clean, this change merges automatically.", reviewID, reviewerCommitRange(task, reviewerHead))
}

// reviewerCommitRange names the commits a review pushed onto task's branch
// for the release note: from the head the hold was filed for to the one
// the review left.
func reviewerCommitRange(task model.Task, reviewerHead string) string {
	if _, from, ok := strings.Cut(linkTarget(task, model.LinkTrustBoundaryCleared), "@"); ok && from != "" {
		return fmt.Sprintf(" (`%s..%s`)", shortSHA(from), shortSHA(reviewerHead))
	}
	return ""
}
