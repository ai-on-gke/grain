package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A review is stacked on the branch of the change it reviews, and that
// branch is normally deleted the moment the change merges. So a review
// outlives the change it was filed for in three ways, each handled here:
//
//   - A newer review of the same change is filed (a fresh security hold
//     for a new head): supersedeSecurityReview closes the older one,
//     whatever it is doing -- waiting to run, running (closing it is what
//     cancels the run, watchForTaskClosed), or finished with a stacked
//     pull request still on the merge queue. Its verdict would count for
//     nothing anyway: settleSecurityHold only listens to the review the
//     hold names now.
//   - The reviewed task closes, merged or not: closeObsoleteReviews closes
//     every review of it still waiting to run, every cycle, so reviews
//     orphaned before this existed are closed too.
//   - A task's base branch is gone when it is dispatched: stopOnMissingBase
//     closes a review, which has nothing left to read, and parks any other
//     task for a human, at the first attempt rather than the fifth.

// supersedeSecurityReview closes prevID, an earlier security review of
// the change review was just filed for, as superseded by review.
//
// Store.Supersede does the closing where it can -- the links, the close
// and its comment -- and refuses a review that has ever been dispatched.
// A refused one that is not already closed is closed here the same way,
// with the same links and a comment saying why: a review of a head the
// change has moved past has nothing to say about the change any more, and
// its failed attempts are no reason to dispatch it again.
//
// That includes the two a refusal used to leave standing (tasks 475 and
// 492). A running one is closed too, which is what stops its run
// (watchForTaskClosed) rather than letting two reviews of one pull request
// hold two sandboxes. And a completed one -- model.State.Settled counts
// 'completed' as settled, but for a review it means a stacked pull request
// of its fixes still waiting on the merge queue -- is closed, which takes
// that pull request off the queue (Store.OpenPullRequestLinks) rather than
// leaving it to merge findings about a head the change has moved past.
func supersedeSecurityReview(ctx context.Context, store *model.Store, prevID string, review model.Task,
	now time.Time) error {

	if prevID == "" || prevID == review.ID {
		return nil
	}
	superseded, err := store.Supersede(ctx, prevID, review, "", now)
	refusal := superseded.Refusal
	if err != nil {
		return fmt.Errorf("orchestrator: superseding security review %s with %s: %w", prevID, review.ID, err)
	}
	if refusal == "" {
		log.Printf("orchestrator: security review %s superseded by %s", prevID, review.ID)
		return linkSupersedes(ctx, store, review.ID, prevID)
	}
	state, err := store.State(ctx, prevID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s's state: %w", prevID, err)
	}
	if state == "" || state == model.StateClosed {
		return nil
	}
	if err := store.UpdateTask(ctx, prevID, func(t *model.Task) error {
		t.Links = append(withoutLinks(t.Links, model.LinkSupersededBy),
			model.Link{Kind: model.LinkSupersededBy, Target: review.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: linking security review %s to %s: %w", prevID, review.ID, err)
	}
	if err := observeField(ctx, store, prevID, now, func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		return fmt.Errorf("orchestrator: closing security review %s: %w", prevID, err)
	}
	log.Printf("orchestrator: security review %s (%s) superseded by %s", prevID, state, review.ID)
	inPlace := false
	if prev, err := store.GetTask(ctx, prevID); err == nil && prev != nil {
		_, inPlace = reviewsInPlace(*prev)
	}
	stopped := ""
	switch {
	case state == model.StateRunning && inPlace:
		// Closed is also what takes the reviewed branch out of its push
		// scope (model.Store.GitPushBranch), before the stop reaches it.
		stopped = " Its run in flight has been stopped, and may no longer push to the branch under review."
	case state == model.StateRunning:
		stopped = " Its run in flight has been stopped."
	case state == model.StateCompleted && !inPlace:
		stopped = " Its pull request has been taken off the merge queue."
	}
	if err := reviewComment(ctx, store, prevID, fmt.Sprintf(
		"Closed as superseded by review %s, which was filed for the change's newer head. This review "+
			"was of a commit the change has moved past, so its verdict could no longer release the "+
			"hold; reopen it if the replacement was a mistake.%s", review.ID, stopped), now); err != nil {
		return err
	}
	return linkSupersedes(ctx, store, review.ID, prevID)
}

func linkSupersedes(ctx context.Context, store *model.Store, id, prevID string) error {
	if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkSupersedes, Target: prevID})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: linking %s to the review it supersedes: %w", id, err)
	}
	return nil
}

// closeObsoleteReviews closes every review grain filed that is still
// waiting to run for a task that has closed (Store.ObsoleteReviews), and
// says on each which task closed and why that leaves it nothing to do.
//
// Level-triggered, like closeFinishedReviews beside it in SyncReviews:
// closeSecurityReviewOnSettle closes the one review a security hold names
// on the cycle its change's pull request settles, and this closes the
// rest -- older reviews of the same change, declared reviews, and any
// review whose task was closed by hand -- whenever it next looks.
//
// The close is written before the note, as closeFinishedReview writes
// its own, so a failed note is not posted again every cycle.
func closeObsoleteReviews(ctx context.Context, store *model.Store, now time.Time) error {
	reviews, err := store.ObsoleteReviews(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	var errs []error
	for _, r := range reviews {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if reviewed, err := store.GetTask(ctx, r.ReviewedID); err == nil && reviewed != nil && hasBaseRepairBypass(*reviewed) {
			if obs, err := store.GetObservation(ctx, r.ReviewedID); err == nil && obs != nil && obs.PrMergedAt != nil {
				baseBranch := resolveTaskBaseBranch(*reviewed, "")
				_ = store.UpdateTask(ctx, r.ReviewID, func(t *model.Task) error {
					if t.Base == reviewed.BranchName() {
						// A review is never classified on its own account
						// (holdForTrustBoundary, pullRequestTriggersApply skip
						// it: it merges into the branch it reviews). Moved onto
						// the base branch, its pull request would land there
						// unclassified, so it waits for a person.
						t.Base = baseBranch
						t.AutoMerge = false
						detachFromReviewedBranch(t)
					}
					return nil
				})
				continue
			}
		}
		if err := observeField(ctx, store, r.ReviewID, now, func(o *model.Observation) {
			o.ClosedAt = &now
		}); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: closing review %s: %w", r.ReviewID, err))
			continue
		}
		log.Printf("orchestrator: task %s is closed; closing its open review %s", r.ReviewedID, r.ReviewID)
		if err := reviewComment(ctx, store, r.ReviewID, obsoleteReviewNote(ctx, store, r.ReviewedID), now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// detachFromReviewedBranch gives a review that worked on the reviewed
// change's own branch (reviewsInPlace) a branch of its own again, for a
// review moved onto the base branch once that change has merged: the
// reviewed branch is gone, and the review's fixes now want a pull request
// of their own, which a human Submits. Its Branch is cleared so the next
// write fixes the review's own name onto it (model.Store.PutTask).
func detachFromReviewedBranch(t *model.Task) {
	if _, ok := reviewsInPlace(*t); !ok {
		return
	}
	t.Branch = ""
	t.Links = withoutLinks(t.Links, model.LinkPushesReviewedBranch, model.LinkReviewerHead)
}

// obsoleteReviewNote says which way the reviewed task closed, when its
// observation says, and falls back to the plain fact when it cannot be
// read.
func obsoleteReviewNote(ctx context.Context, store *model.Store, reviewedID string) string {
	how := "closed"
	if obs, err := store.GetObservation(ctx, reviewedID); err == nil && obs != nil && obs.PrMergedAt != nil {
		how = "merged and closed"
	}
	return fmt.Sprintf("Closing this review: task %s %s, so there is nothing left to review -- the branch "+
		"this review was stacked on goes with it, and every attempt to dispatch it would fail looking "+
		"for that branch.", reviewedID, how)
}

// stopOnMissingBase is what a task gets when its clone failed because its
// base branch is not on the remote (baseGoneError): a stop at the first
// attempt, instead of the dispatch loop offering it again until
// model.MaxConsecutiveFailures retires it four attempts later.
//
// A review is closed: its base is the branch of the change it reviews, so
// a missing base means that change merged or was thrown away and there is
// nothing left for it to read. Any other task is parked on a comment, the
// way noteRefusedGrant parks one -- whether the base merged into the
// default branch or was abandoned is a human's call, and so is where the
// task should point now. A reply dispatches it again.
//
// Written after the run's own outcome, as noteRefusedGrant is, and its
// error only logged by the caller: the run has already failed, and the
// failure's own text is on it.
func stopOnMissingBase(ctx context.Context, store *model.Store, task model.Task, gone *baseGoneError,
	now time.Time) error {

	if isReviewTask(task) {
		if id := linkTarget(task, model.LinkProposedBy); id != "" {
			if reviewed, err := store.GetTask(ctx, id); err == nil && reviewed != nil && hasBaseRepairBypass(*reviewed) {
				baseBranch := resolveTaskBaseBranch(*reviewed, "")
				if gone.Base != baseBranch {
					return store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
						// Moved onto the base branch, a review's pull request
						// would land there unclassified (closeSecurityReviewOnSettle).
						t.Base = baseBranch
						t.AutoMerge = false
						return nil
					})
				}
			}
		}
		if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.ClosedAt = &now
		}); err != nil {
			return fmt.Errorf("orchestrator: closing review %s over its missing base: %w", task.ID, err)
		}
		log.Printf("orchestrator: review %s's base %q is gone from %s; closing it", task.ID, gone.Base, gone.Repo)
		reviewed := ""
		if id := linkTarget(task, model.LinkProposedBy); id != "" {
			reviewed = fmt.Sprintf(" (task %s's)", id)
		}
		return reviewComment(ctx, store, task.ID, fmt.Sprintf(
			"Closing this review: the branch it reviews%s, `%s`, no longer exists on %s -- the change "+
				"merged or was closed, and its branch was deleted -- so there is nothing left to review. "+
				"If what landed still wants a review, file one against the branch it landed on.",
			reviewed, gone.Base, gone.Repo), now)
	}

	log.Printf("orchestrator: task %s's base %q is gone from %s; parking it rather than retrying",
		task.ID, gone.Base, gone.Repo)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf(
			"This task could not be dispatched: its base branch `%s` does not exist on %s -- it may "+
				"have been merged and deleted.\n\n"+
				"Every attempt fails the same way, before an agent exists, so grain has stopped rather "+
				"than retrying. Point this task at a branch that exists (its Base, on the task's own "+
				"pane) and reply here to have it dispatched again, or close it if the work is no "+
				"longer needed.", gone.Base, gone.Repo),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: recording that %s's base %q is gone: %w", task.ID, gone.Base, err)
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}

// stopOnAccessRefused is stopOnMissingBase for a clone the git proxy
// refused because the team's credential cannot reach the task's
// repository (accessRefusedError): access revoked after the task was
// filed, a credential removed or swapped, or a task filed before CreateTask
// checked reach at all. Nothing another attempt does changes that, so the
// task is parked at the first failure, on a comment carrying the proxy's
// own explanation -- which names the credential and the repository --
// rather than offered to dispatch again until MaxConsecutiveFailures
// retires it, spending a sandbox each time. A reply dispatches it again,
// once the credential has been given the repository.
func stopOnAccessRefused(ctx context.Context, store *model.Store, task model.Task, refused *accessRefusedError,
	now time.Time) error {

	log.Printf("orchestrator: task %s's clone of %s was refused for lack of access; parking it rather than retrying",
		task.ID, refused.Repo)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf(
			"This task could not be dispatched: the deployment's GitHub credential cannot reach %s, so cloning "+
				"it was refused. The git proxy said:\n\n```\n%s\n```\n\n"+
				"Every attempt would fail the same way, before an agent exists, so grain has stopped rather "+
				"than retrying. Give the credential access to %s on GitHub, or choose another in "+
				"Settings → Git (/settings/git), and reply here to have it dispatched again -- or close it if the "+
				"work is no longer needed.", refused.Repo, refused.Output, refused.Repo),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: recording that %s's clone of %s was refused: %w", task.ID, refused.Repo, err)
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}

// stopOnExpiredHostCredentials parks task after a run that could not start
// a host tool because the host's credentials have lapsed (authfix.Error,
// model.EnvironmentOutcome). Nothing another attempt does changes that --
// the host refuses the tool until somebody acts on it -- so rather than
// offering the task again into the same refusal, it is parked on a
// comment carrying why, which is what says what to run to whoever reads
// the task; a reply dispatches it again. The
// run itself is not counted against the task (EnvironmentOutcome), and
// the deployment-health pane says the same once for the whole host
// (ui.HostCredentialsWarning).
func stopOnExpiredHostCredentials(ctx context.Context, store *model.Store, task model.Task, why string,
	now time.Time) error {

	log.Printf("orchestrator: task %s could not start a host tool, its host credentials have expired; parking it rather than retrying",
		task.ID)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf(
			"This task's run could not start: %s.\n\n"+
				"Every run on this host fails the same way until then, whatever the task, so grain has "+
				"parked this one rather than retrying it, and has not counted the attempt against it. "+
				"Reply here once that has been done to have it dispatched again.", strings.TrimRight(why, ".")),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: recording that %s's host credentials expired: %w", task.ID, err)
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}
