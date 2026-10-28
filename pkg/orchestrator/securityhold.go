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
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// The security hold (#2425 Layer 2a): a change whose pull request touches
// grain's own trust boundary -- the fixed, operator-controlled list in
// pkg/trustboundary -- does not merge itself on the strength of its checks
// alone.
//
// The whole life of a hold is three moments:
//
//   - holdForTrustBoundary, at the end of every run that leaves a pull
//     request (finishWithPullRequest), and before that run is marked
//     completed -- so there is no cycle in which the merge queue could see
//     a finished, auto-merging change it has not yet been classified.
//     It withdraws auto-merge, records model.LinkSecurityHold, and files a
//     security review (model.SecurityReviewTemplateID) that works on the
//     change's own branch, pushing what it fixes there rather than opening
//     a pull request of its own (securityhold_inplace.go), and is told
//     which paths put it there.
//   - dropSecurityHoldOnFindings, when any review of the held change leaves
//     findings. The hold link goes, auto-merge stays off, and the change
//     waits for a human's Submit like any other change whose review left
//     findings (noteFindingsOnReviewedTask).
//   - SyncSecurityHolds, every cycle: a held change whose security review
//     finished with no findings or questions gets its auto-merge back --
//     cleared at the head the review left, when it pushed fixes.
//
// And one after it: when the held change's pull request merges or closes
// with its security review still open, closeSecurityReviewOnSettle closes
// the review, and a merge says on the change that it was never reviewed.
// A hold filed again for a new head supersedes the review the old one
// filed, and any review left open once its change has closed is closed by
// closeObsoleteReviews (obsolete.go).
//
// A classification is about one commit. holdForTrustBoundary records the
// head it read the file list at (model.LinkTrustBoundaryCleared), and
// before the merge queue merges a change that grain cleared on its own,
// clearedForHead compares that with the head it is about to merge. Commits
// can reach a branch without a run of its own task finishing -- a stacked
// fix or review merging back into it, a push through the git proxy, which
// has no ref policy (#2387) -- and those are classified again there rather
// than merged on the strength of a reading of some other commit.
//
// Classified again is not reviewed again. A new head that changed none
// of the trust-boundary patches it was cleared for keeps that clearance,
// and a port carrying exactly the patches of a source that was cleared
// inherits the source's (carryClearance, securityhold_inherit.go).
//
// Withdrawing auto-merge, rather than holding the change at the head of
// its queue the way a declared review does (reviewStateOf), is what lets
// the wait be unbounded. A held change is not a queue member
// (isQueueMember), so nothing behind it waits, and so there is no deadline
// after which it merges "reviewed or not" (escalateUnfinishedReview) --
// the one ending a security hold must never have. A review that never
// finishes leaves the change waiting for a human, which is the safe
// direction to be stuck in.
//
// The human override is the button grain already has: Submit sets
// AutoMerge back (ui.Client.Submit), and the change merges once it reads
// clean, whatever state its review is in.
//
// Each of those moments is said twice, by securityHoldNote: on the task,
// and on the pull request, which is where a reviewer who never opens grain
// is looking -- and where a change that reads green and mergeable but is
// not merging otherwise has no reason given. Each is said once per hold,
// not once per cycle: every note is written by the call that changed the
// task's state, and only when it did.

// holdForTrustBoundary classifies task's pull request and, if it touches
// the trust boundary, puts it on hold. See the overview above.
//
// Only a change that would otherwise merge itself is classified: one with
// auto-merge on, or one already on hold (a rerun of a held change gets a
// fresh review of what it now proposes, rather than being released by a
// review of what it used to). A change with auto-merge off is already
// waiting for a human.
//
// Stacked tasks are not classified on their own account: a review or a
// fix merges into another task's branch, never into the repository's base.
//
// A pull request whose files cannot be read, or that reaches GitHub's
// listing cap, is held as if it touched everything: the hold fails closed.
//
// Nothing here fails the finish once auto-merge is withdrawn. Filing the
// review can fail, and that is said on the task rather than returned: the
// change is already safe, waiting for a human, and losing the pull request
// link or the completion over a review that could not be filed would be
// the worse outcome.
//
// headSHA is the commit the caller has just read the pull request's head
// to be, or "" for this to read it. Either way it is read before the file
// list, so the list classified can only be newer than the commit it is
// recorded against, never older. A head that cannot be read is recorded
// as "", which matches no head clearedForHead will ever see, so that
// change is classified again before it merges.
func holdForTrustBoundary(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) error {

	if task.Target == nil || unjudged(task) {
		return nil
	}
	// Fresh, for the reason finishWithPullRequest's own UpdateTasks give:
	// somebody may have pressed Submit while the run was going.
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s before classifying its diff: %w", task.ID, err)
	}
	if current == nil || (!current.AutoMerge && !hasSecurityHold(*current) && !hasBrokenRulesHold(*current)) {
		return nil
	}

	if headSHA == "" {
		detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			log.Printf("orchestrator: reading %s's head before classifying it: %v -- "+
				"it is classified again before it merges", ref, err)
		} else {
			headSHA = detail.HeadSHA
		}
	}

	files, err := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	known := err == nil && len(files) < github.PullRequestFilesCap
	if err != nil {
		log.Printf("orchestrator: reading which files %s touches: %v -- holding it for a "+
			"security review rather than guessing it touches nothing", ref, err)
	}
	matches := trustboundary.Classify(files)
	// The repository's own rules add to grain's list, never take from it
	// (repoGuard): the effective list is both.
	rules, guarded := repoGuard(ctx, store, client, *task.Target, files, known)
	matches = append(matches, guarded...)
	cleared := model.Link{Kind: model.LinkTrustBoundaryCleared, Target: clearedTarget(headSHA, rules)}
	if known && len(matches) == 0 {
		if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Links = append(withoutLinks(t.Links, model.LinkTrustBoundaryCleared, model.LinkBrokenRulesHold,
				model.LinkHumanSubmitted, model.LinkHumanOnlyHold), cleared)
			t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleBuiltin)
			return nil
		}); err != nil {
			return fmt.Errorf("orchestrator: recording which commit of %s was classified: %w", task.ID, err)
		}
		return nil
	}
	// A port of a change already cleared, or a head that changed none of
	// the trust-boundary patches a clearance was given for, keeps that
	// clearance rather than being reviewed again (securityhold_inherit.go).
	if known {
		if carried, ok := carryClearance(ctx, store, client, *current, ref, headSHA, matches, rules); ok {
			return keepClearance(ctx, store, client, *current, ref, headSHA, cleared, carried, matches, now)
		}
	}
	// A clean review may release this hold unless the change touches a
	// path only a human may let through -- and a change whose file list
	// could not be read in full is held as if it touched every path,
	// those included: the unread part is exactly where one could be.
	releasable := known && !trustboundary.RequiresHuman(matches)
	approvers := ownerApprovers(ctx, store, *task.Target)
	// The review an earlier hold filed, which the one filed below replaces
	// (supersedeSecurityReview).
	prevReview := linkTarget(*current, model.LinkSecurityReviewTask)
	bypassBranch, bypass := baseRepairBypassActive(ctx, store, *current, "")
	// A head already classified under these rules, whose review is still
	// open or already came back clean, keeps that review rather than being
	// filed a duplicate of it -- unless a base-repair bypass has begun since
	// and the hold must be lifted under it.
	if known && headSHA != "" && linkTarget(*current, model.LinkTrustBoundaryCleared) == cleared.Target &&
		(!bypass || linkTarget(*current, model.LinkBaseRepairBypass) == headSHA) &&
		securityReviewReusable(ctx, store, prevReview) {
		return nil
	}

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		wasHeld := hasSecurityHold(*t) || hasBrokenRulesHold(*t)
		t.Links = withoutLinks(t.Links, model.LinkSecurityHold, model.LinkSecurityReviewTask,
			model.LinkTrustBoundaryCleared, model.LinkRepoRules, model.LinkBrokenRulesHold,
			model.LinkBaseRepairBypass, model.LinkHumanSubmitted, model.LinkHumanOnlyHold)
		// Who may release this head is decided afresh with it.
		t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleBuiltin)
		if !rules.Empty() {
			t.Links = append(t.Links, model.Link{Kind: model.LinkRepoRules, Target: rules.String()})
		}
		if bypass {
			if wasHeld && !hasPathTriggerHold(*t) {
				t.AutoMerge = true
			}
			t.Links = append(t.Links, cleared, model.Link{Kind: model.LinkBaseRepairBypass, Target: headSHA})
			return nil
		}
		t.AutoMerge = false
		for _, m := range matches {
			if m.Category.OwnerApproval {
				t.Links = append(t.Links, model.ApprovalRequiredLink(model.ApprovalRuleBuiltin+m.Category.ID, approvers))
			}
		}
		if releasable {
			t.Links = append(t.Links,
				model.Link{Kind: model.LinkSecurityHold, Target: trustboundary.Version})
		} else {
			// Marked, not merely left without a hold link: an absent hold
			// link reads as "nothing of grain's own holds this change" to
			// every other hold that may hand auto-merge back
			// (heldForHuman).
			t.Links = append(t.Links, model.Link{Kind: model.LinkHumanOnlyHold, Target: trustboundary.Version})
		}
		if known {
			t.Links = append(t.Links, cleared)
		}
		if heldOnlyForBrokenRules(matches, known, headSHA) {
			t.Links = append(t.Links, model.Link{Kind: model.LinkBrokenRulesHold, Target: headSHA})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: withdrawing %s's auto-merge for a security review: %w", task.ID, err)
	}

	tmpl := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{})
	review, fileErr := putSecurityReview(ctx, store, *current, *tmpl,
		securityReviewTitle(*current), securityReviewBody(*tmpl, *current, ref, matches, known), now)
	if fileErr == nil {
		fileErr = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			t.Links = append(t.Links, model.Link{Kind: model.LinkSecurityReviewTask, Target: review.ID})
			return nil
		})
	}
	if fileErr != nil {
		log.Printf("orchestrator: filing the security review of %s: %v -- it waits for a human", task.ID, fileErr)
	} else if err := supersedeSecurityReview(ctx, store, prevReview, review, now); err != nil {
		// The new review is filed and the hold names it, so the old one can
		// no longer release anything; at worst it runs for nothing.
		log.Printf("orchestrator: %v", err)
	}
	prFileErr := fileErr
	if fileErr != nil {
		prFileErr = errSecurityReviewNotFiled
	}
	if bypass {
		log.Printf("orchestrator: %s touches the trust boundary (%s); human gate bypassed under base_repair_bypasses_human_gates while %s is red (security review %s filed)",
			ref, matchSummary(matches, known), bypassBranch, review.ID)
		return securityHoldNote(ctx, store, client, task.ID, &ref,
			securityBypassComment(ref, bypassBranch, matches, known, review.ID, fileErr),
			securityBypassComment(ref, bypassBranch, matches, known, review.ID, prFileErr), now)
	}
	log.Printf("orchestrator: %s touches the trust boundary (%s); auto-merge withdrawn pending security review",
		ref, matchSummary(matches, known))
	return securityHoldNote(ctx, store, client, task.ID, &ref,
		securityHoldComment(ref, matches, known, releasable, review.ID, fileErr)+releaseNote(matches, approvers, rules),
		securityHoldComment(ref, matches, known, releasable, review.ID, prFileErr)+releaseNote(matches, approvers, rules), now)
}

// errSecurityReviewNotFiled stands in for the reason a security review
// could not be filed, in the copy of the hold's note posted on the pull
// request: the real error is grain's own internals, for the task's
// readers, not the pull request's.
var errSecurityReviewNotFiled = errors.New("the reason is on the task in grain")

// securityHoldNote says body on taskID's own conversation and, when ref is
// not nil, prBody on its pull request.
//
// The pull request's copy is best-effort: the hold is already in force
// and the task already says why, so a comment GitHub refused is worth a
// log line, not an error that would fail the finish or the cycle -- and
// nothing here retries it, since the state change it reports has already
// been written and will not be made, or said, again.
func securityHoldNote(ctx context.Context, store *model.Store, client github.Client,
	taskID string, ref *model.PullRequestRef, body, prBody string, now time.Time) error {

	if err := reviewComment(ctx, store, taskID, body, now); err != nil {
		return err
	}
	if ref == nil || client == nil {
		return nil
	}
	prBody = fmt.Sprintf("**grain security hold** (task %s)\n\n%s", taskID, prBody)
	if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prBody); err != nil {
		log.Printf("orchestrator: noting %s's security hold on %s: %v -- it is on the task only", taskID, ref, err)
	}
	return nil
}

// dropSecurityHoldOnFindings ends the hold on the change review was filed
// against, when review left findings -- whichever review it was. A
// declared review's findings hold the change for a human as surely as the
// security review's do (noteFindingsOnReviewedTask), and a security review
// finishing clean afterwards must not hand auto-merge back over them.
//
// Called with the findings in hand, before the review's run is marked
// completed, so SyncSecurityHolds can never read the review as finished
// while the hold it should have dropped is still there.
func dropSecurityHoldOnFindings(ctx context.Context, store *model.Store, client github.Client,
	review model.Task, now time.Time) error {

	if !isReviewTask(review) {
		return nil
	}
	reviewedID := linkTarget(review, model.LinkProposedBy)
	if reviewedID == "" {
		return nil
	}
	if reviewed, err := store.GetTask(ctx, reviewedID); err == nil && reviewed != nil && hasBaseRepairBypass(*reviewed) {
		return nil
	}
	dropped := false
	if err := store.UpdateTask(ctx, reviewedID, func(t *model.Task) error {
		dropped = false
		if !hasSecurityHold(*t) && !hasBrokenRulesHold(*t) {
			return nil
		}
		t.AutoMerge, dropped = false, true
		t.Links = withoutLinks(t.Links, model.LinkSecurityHold, model.LinkTrustBoundaryCleared,
			model.LinkBrokenRulesHold)
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: ending %s's security hold on its review's findings: %w", reviewedID, err)
	}
	if !dropped {
		return nil
	}
	body := fmt.Sprintf(
		"Review task %s left findings on this change, so its security hold will not be released "+
			"automatically: it waits for you. Press Submit once you have decided what the findings "+
			"are worth.", review.ID)
	_, ref, ok, err := reviewedPullRequest(ctx, store, review)
	if err != nil || !ok {
		if err != nil {
			log.Printf("orchestrator: finding %s's pull request to note its dropped security hold: %v", reviewedID, err)
		}
		return securityHoldNote(ctx, store, client, reviewedID, nil, body, body, now)
	}
	return securityHoldNote(ctx, store, client, reviewedID, &ref, body, body, now)
}

// SyncSecurityHolds releases every hold whose security review has finished
// clean: its run completed and it left no findings (which would already
// have dropped the hold). Such a change gets its auto-merge back and
// merges once it reads clean. A review that pushed its fixes onto the
// held branch itself is clean too, and clears the change at the head it
// left (model.LinkReviewerHead) -- unless its fixes touch the trust
// boundary themselves, which no agent clears on its own say-so: that head
// is held afresh for a second review (reviewerFixesTouchBoundary). A
// change held only
// because its base's rules did not parse is judged again once they do
// (releaseFixedRulesHold).
//
// A review that pushed fixes counts as clean once they have merged back
// into the held branch and it left no findings: the reviewer is meant to
// fix what it finds, and the change then goes through. The new head is
// classified again before it merges: one whose only trust-boundary
// changes since the reviewed head are the review's own merged fixes, and
// that adds no path only a human or an owner may release, keeps the
// clearance (reviewFixClearance); any other trust-boundary change is held
// and reviewed in its turn.
// Fixes closed without merging end the hold and leave the change for a
// human. A review still running, parked or failing, or whose fixes are
// still on their way back, releases nothing, for as long as it takes.
//
// A hold only a human may release (trustboundary.RequiresHuman) never
// carries the hold link in the first place, so no review releases it; it
// carries model.LinkHumanOnlyHold instead, so nothing else does either.
//
// Level-triggered, like SyncReviews: read the store, act on what it says.
func SyncSecurityHolds(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}
	var errs []error
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
			continue
		}
		if task == nil {
			continue
		}
		var ref *model.PullRequestRef
		if r, err := model.ParsePullRequestRef(link.PullRequest); err == nil {
			ref = &r
		}
		if hasBaseRepairBypass(*task) && ref != nil {
			if _, err := revokeExpiredBaseRepairBypass(ctx, store, client, *task, *ref, "", now); err != nil {
				errs = append(errs, err)
				continue
			}
			task, err = store.GetTask(ctx, link.TaskID)
			if err != nil || task == nil {
				if err != nil {
					errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", link.TaskID, err))
				}
				continue
			}
		}
		if !hasSecurityHold(*task) && !hasBrokenRulesHold(*task) {
			continue
		}
		if hasBrokenRulesHold(*task) {
			if err := releaseFixedRulesHold(ctx, store, client, *task, ref, now); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := settleSecurityHold(ctx, store, client, *task, ref, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func settleSecurityHold(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref *model.PullRequestRef, now time.Time) error {

	reviewID := linkTarget(task, model.LinkSecurityReviewTask)
	if reviewID == "" {
		return nil
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s: %w", reviewID, err)
	}
	if review == nil {
		return nil
	}
	obs, err := store.GetObservation(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s's observation: %w", reviewID, err)
	}
	pushed, landed, ok := reviewOutcome(*review, obs)
	if !ok {
		return nil
	}
	// A review that fixed what it found, had its fixes merged back and
	// left no findings (which would already have dropped the hold) is a
	// clean review: the policy is that the reviewer fixes, and the change
	// goes through. Fixes that never landed are not.
	clean := !pushed || landed

	var advancedClearance string
	_, inPlace := reviewsInPlace(*review)
	reviewerHead := linkTarget(*review, model.LinkReviewerHead)
	if inPlace && reviewerHead != "" && task.Target != nil {
		// A review that pushed its fixes onto this very branch clears the
		// head it left -- but only when those fixes are off the trust
		// boundary. No agent clears its own trust-boundary edits: fixes
		// that touch it are a new head on the boundary, held afresh and
		// given a review of their own (reviewerFixesTouchBoundary).
		// Anything pushed after the reviewer's head, by anyone else, reads
		// as a mismatch in clearedForHead and is classified again there.
		touches, readable := reviewerFixesTouchBoundary(ctx, store, client, task, reviewerHead)
		if !readable || (touches && ref == nil) {
			// Nothing settled on a read that failed: the next cycle reads
			// again, and the change stays held meanwhile.
			return nil
		}
		if touches {
			if err := holdForTrustBoundary(ctx, store, client, task, *ref, reviewerHead, now); err != nil {
				return err
			}
			note := reviewerFixesHeldNote(reviewID, task, reviewerHead)
			return securityHoldNote(ctx, store, client, task.ID, ref, note, note, now)
		}
		advancedClearance = clearedTarget(reviewerHead, repoRulesFor(ctx, store, *task.Target))
	} else if clean && pushed && landed && ref != nil && client != nil && task.Target != nil {
		if detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number); err == nil && detail.HeadSHA != "" {
			_, recordedSHA, ok := strings.Cut(linkTarget(task, model.LinkTrustBoundaryCleared), "@")
			if ok && recordedSHA != "" && detail.HeadSHA != recordedSHA {
				files, ferr := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
				known := ferr == nil && len(files) < github.PullRequestFilesCap
				matches := trustboundary.Classify(files)
				rules, guarded := repoGuard(ctx, store, client, *task.Target, files, known)
				matches = append(matches, guarded...)
				switch {
				case known && len(matches) == 0:
					advancedClearance = clearedTarget(detail.HeadSHA, rules)
				case known:
					if _, carryOK := carryClearance(ctx, store, client, task, *ref, detail.HeadSHA, matches, rules); carryOK {
						advancedClearance = clearedTarget(detail.HeadSHA, rules)
					} else {
						return holdForTrustBoundary(ctx, store, client, task, *ref, detail.HeadSHA, now)
					}
				default:
					return holdForTrustBoundary(ctx, store, client, task, *ref, detail.HeadSHA, now)
				}
			}
		}
	}

	settled, released := false, false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		// Re-read under the update: findings relayed since this cycle read
		// the task will have dropped the hold, and a rerun will have
		// replaced the review it names. Either way there is nothing left
		// for this call to settle, or to say it settled.
		settled, released = false, false
		if !hasSecurityHold(*t) || linkTarget(*t, model.LinkSecurityReviewTask) != reviewID {
			return nil
		}
		settled = true
		t.Links = withoutLinks(t.Links, model.LinkSecurityHold)
		if clean {
			if advancedClearance != "" {
				t.Links = append(withoutLinks(t.Links, model.LinkTrustBoundaryCleared),
					model.Link{Kind: model.LinkTrustBoundaryCleared, Target: advancedClearance})
			}
			// A gating path trigger still holding the change keeps it
			// held; its own settle hands auto-merge back
			// (pathtriggerhold.go).
			if !hasPathTriggerHold(*t) && !heldForHuman(*t) && len(model.PendingApprovals(*t)) == 0 {
				t.AutoMerge, released = true, true
			}
		} else {
			// The decision is a human's now, and their Submit is about
			// the head they read, review commits included.
			t.Links = withoutLinks(t.Links, model.LinkTrustBoundaryCleared)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: settling %s's security hold: %w", task.ID, err)
	}

	var body string
	switch {
	case !settled:
		return nil
	case released && inPlace && reviewerHead != "":
		body = fmt.Sprintf("The security review of this change (task %s) fixed what it found by pushing "+
			"commits to this branch%s, none of them on the trust boundary, and left no open findings or "+
			"questions, so this change merges "+
			"automatically again once it reads clean -- no Submit needed. It is cleared at `%s`, the head "+
			"the review left; its closing note on task %s describes each fix. Anything pushed after that "+
			"head is classified again before it merges.",
			reviewID, reviewerCommitRange(task, reviewerHead), shortSHA(reviewerHead), reviewID)
	case released && landed:
		body = fmt.Sprintf("The security review of this change (task %s) fixed what it found, its "+
			"fixes have merged into this branch, and it left no questions, so this change merges "+
			"automatically again once it reads clean.", reviewID)
	case released:
		body = fmt.Sprintf("The security review of this change (task %s) finished without findings, "+
			"so it merges automatically again once it reads clean.", reviewID)
	case !clean:
		body = fmt.Sprintf("The security review of this change (task %s) pushed fixes that were "+
			"closed without merging into this branch, so this change is not merging "+
			"automatically: press Submit once you have read it.", reviewID)
	default:
		return nil
	}
	return securityHoldNote(ctx, store, client, task.ID, ref, body, body, now)
}

// reviewOutcome reads how a gating review ended: pushed is whether it
// opened a pull request of its own, landed whether that pull request has
// merged back into the branch it reviewed. ok is false while there is no
// outcome yet -- the run still going, or its fixes still open.
//
// A review that works on the reviewed branch itself (reviewsInPlace) has
// no pull request to wait for: what it pushed is on the branch the moment
// it is pushed, so pushed and landed are both whether it recorded the head
// it left (model.LinkReviewerHead), and ok is whether its run completed.
func reviewOutcome(review model.Task, obs *model.Observation) (pushed, landed, ok bool) {
	if _, inPlace := reviewsInPlace(review); inPlace {
		pushed = linkTarget(review, model.LinkReviewerHead) != ""
		return pushed, pushed, obs != nil && obs.CompletedAt != nil
	}
	if !hasPullRequest(review) {
		return false, false, obs != nil && obs.CompletedAt != nil
	}
	switch {
	case obs != nil && obs.PrMergedAt != nil:
		return true, true, true
	case obs != nil && (obs.PrClosedAt != nil || obs.ClosedAt != nil):
		return true, false, true
	}
	return true, false, false
}

// securityReviewReusable reports whether reviewID is an open/in-flight
// security review or one that already completed cleanly for the head it was
// filed against.
func securityReviewReusable(ctx context.Context, store *model.Store, reviewID string) bool {
	if reviewID == "" {
		return false
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil || review == nil {
		return false
	}
	obs, err := store.GetObservation(ctx, reviewID)
	if err != nil {
		return false
	}
	if pushed, landed, ok := reviewOutcome(*review, obs); ok {
		return !pushed || landed
	}
	state, err := store.State(ctx, reviewID)
	if err != nil {
		return false
	}
	switch state {
	case model.StateProposed, model.StateQueued, model.StateRunning,
		model.StateAwaitingReply, model.StateAwaitingSubmit, model.StateCompleted:
		return true
	default:
		return false
	}
}

// closeSecurityReviewOnSettle closes the security review filed for task
// once task's own pull request has merged or closed, if that review is
// still open: a review parked on a question about a branch that has
// already landed, or been thrown away, is a row in somebody's inbox that
// nothing can ever answer (task 1570).
//
// Both endings close it. A change closed unmerged leaves nothing to
// review. A change that merged before its review finished did so on a
// human's say-so -- Submit over the hold, or a merge on GitHub -- and the
// review is stacked on a branch that is normally deleted with the merge,
// so it could not read what landed even if it ran. Closing it would lose
// the one thing its silence says, that nobody has reviewed this change's
// security, so a merge says that on task and on its pull request too.
//
// Called by syncEntry on the cycle that reads the pull request merged or
// closed, before recordPullRequestEvents takes task out of
// OpenPullRequestLinks: an error here is returned before that write, so
// the next cycle tries again. The close is written before the notes, and
// a review already settled is skipped, so a retry after a failed note
// does not say the same thing twice.
//
// A review with a pull request of its own is left alone: what happens to
// commits stacked on a branch that has gone is the merge queue's
// question, not this one's.
func closeSecurityReviewOnSettle(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, detail github.PullRequestDetail, health model.PrHealth,
	now time.Time) error {

	if health != model.PrMerged && health != model.PrClosed {
		return nil
	}
	reviewID := linkTarget(task, model.LinkSecurityReviewTask)
	if reviewID == "" {
		return nil
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s: %w", reviewID, err)
	}
	if review == nil || hasPullRequest(*review) {
		return nil
	}
	state, err := store.State(ctx, reviewID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading security review %s's state: %w", reviewID, err)
	}
	if state == "" || state.Settled() {
		return nil
	}
	if health == model.PrMerged && hasBaseRepairBypass(task) {
		if state == model.StateQueued || state == model.StateProposed {
			baseBranch := resolveTaskBaseBranch(task, detail.BaseRef)
			_ = store.UpdateTask(ctx, reviewID, func(t *model.Task) error {
				// A review is never classified on its own account
				// (holdForTrustBoundary skips it, since it merges into the
				// branch it reviews). Moved onto the base branch, its pull
				// request would land there unclassified and unattended, so
				// it waits for a person to Submit it instead.
				t.Base = baseBranch
				t.AutoMerge = false
				detachFromReviewedBranch(t)
				return nil
			})
		}
		return nil
	}
	if err := observeField(ctx, store, reviewID, now, func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		return fmt.Errorf("orchestrator: closing security review %s: %w", reviewID, err)
	}
	log.Printf("orchestrator: %s is %s; closing its unfinished security review %s", ref, health, reviewID)

	if health == model.PrClosed {
		return reviewComment(ctx, store, reviewID, fmt.Sprintf(
			"Closing this review: the change it reviewed (task %s, %s) was closed without merging, "+
				"so there is nothing left to review.", task.ID, ref), now)
	}
	merged := ""
	if detail.HeadSHA != "" {
		merged = fmt.Sprintf(" at `%s`", shortSHA(detail.HeadSHA))
	}
	if err := reviewComment(ctx, store, reviewID, fmt.Sprintf(
		"Closing this review unfinished: the change it reviewed (task %s, %s) merged%s before this "+
			"review finished, on a human's say-so rather than this review's. Its branch is gone, so there "+
			"is nothing left here to read. If what merged still wants a security read, file one "+
			"against the base branch.", task.ID, ref, merged), now); err != nil {
		return err
	}
	body := fmt.Sprintf(
		"This change merged%s before its security review (task %s) finished, so that review has been "+
			"closed unfinished: nobody has reviewed this change's security. If it still wants a "+
			"security read, file one against what merged.", merged, reviewID)
	return securityHoldNote(ctx, store, client, task.ID, &ref, body, body, now)
}

// stateRepoGuard is the hold's own rule for a pull request against this
// deployment's state repository (trustboundary.ClassifyState): a change
// there that grants reach, binds a secret or loosens isolation is applied
// to the running daemon once merged, so it waits for a person whatever
// its review says. It is matched here, in grain, rather than as one of
// that repository's triggers, so nothing merged into the repository can
// take it away. repoGuard adds it to every reading of a change's paths,
// which is what keeps a clearance, a Submit pin and a carried clearance
// all judged under it.
//
// Which repository that is comes from the store (model.Store.SettingsRepo);
// grain/config is always one, so a store nobody told still judges the
// default state repository. A file list that could not be read in full
// is held as touching every privileged path.
func stateRepoGuard(client github.Client, store *model.Store, repo model.RepoRef, files []string, known bool) []trustboundary.Match {
	if !isSettingsRepo(store, repo) {
		return nil
	}
	if !known {
		return []trustboundary.Match{{Category: trustboundary.StatePrivilege, Paths: everyPath(files, known)}}
	}
	autoMergePaths := loadConfigRepoAutoMergePaths(client, repo)
	return trustboundary.ClassifyState(files, autoMergePaths...)
}

// loadConfigRepoAutoMergePaths loads settings/config-policy.yml from repo's
// base branch (main), never from the unmerged pull request head, so a pull
// request cannot exempt its own paths before a human submits it.
func loadConfigRepoAutoMergePaths(client github.Client, repo model.RepoRef) []string {
	if client == nil {
		return nil
	}
	reader, ok := client.(github.FileReader)
	if !ok {
		return nil
	}
	base := "main"
	if def, err := client.DefaultBranch(repo.Owner, repo.Name); err == nil && strings.TrimSpace(def) != "" {
		base = strings.TrimSpace(def)
	}
	data, exists, err := reader.ReadFile(repo.Owner, repo.Name, base, trustboundary.ConfigPolicyPath)
	if err != nil {
		log.Printf("orchestrator: reading %s from %s@%s: %v -- holding all config paths for human Submit",
			trustboundary.ConfigPolicyPath, repo, base, err)
		return nil
	}
	if !exists {
		return nil
	}
	policy, err := trustboundary.ParseConfigPolicy(data)
	if err != nil {
		log.Printf("orchestrator: invalid %s on %s@%s: %v -- holding all config paths for human Submit",
			trustboundary.ConfigPolicyPath, repo, base, err)
		return nil
	}
	return policy.AutoMergePaths
}

// isSettingsRepo reports whether repo is this deployment's state
// repository: grain/config, or the one the store was told it reads.
func isSettingsRepo(store *model.Store, repo model.RepoRef) bool {
	if model.IsConfigRepo(repo) {
		return true
	}
	if store == nil || store.SettingsRepo == nil {
		return false
	}
	settings := store.SettingsRepo()
	return settings != (model.RepoRef{}) && repo.Same(settings)
}

// clearedForHead reports whether the security hold lets task's pull
// request go on towards a merge of headSHA, the head the merge queue is
// about to pin its merge to.
//
// A change grain cleared on its own -- one carrying
// model.LinkTrustBoundaryCleared -- was cleared about one commit, under
// one list. Merging any other head, or under a newer list, waits for
// holdForTrustBoundary to read that head's file list again: a change that
// still touches nothing on the list is cleared for the new head and
// carries on this very cycle, and one that now does is held and reviewed
// like any other. Only a mismatch costs a GitHub read, and only a head
// that moved outside a run is classified here: a run's own finish
// classifies what it pushed, and syncEntry does not ask while that run is
// still pushing.
//
// A change carrying no such link is not grain's call: auto-merge is off,
// or a human turned it on over a hold only they could release.
func clearedForHead(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (bool, error) {

	if hasBaseRepairBypass(task) {
		if revoked, err := revokeExpiredBaseRepairBypass(ctx, store, client, task, ref, headSHA, now); err != nil || revoked {
			return !revoked, err
		}
	}
	recorded := linkTarget(task, model.LinkTrustBoundaryCleared)
	if !task.AutoMerge || recorded == "" {
		return true, nil
	}
	rules := repoRulesFor(ctx, store, *task.Target)
	if recorded == clearedTarget(headSHA, rules) {
		return true, nil
	}
	if headSHA == "" {
		// Nothing to classify, and a merge with no head would land
		// unpinned. Wait for a read that has one.
		return false, nil
	}
	log.Printf("orchestrator: %s is at %s, but its trust-boundary classification is %q -- classifying it again",
		ref, shortSHA(headSHA), recorded)
	if err := holdForTrustBoundary(ctx, store, client, task, ref, headSHA, now); err != nil {
		return false, err
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s after classifying its new head: %w", task.ID, err)
	}
	return current != nil && current.AutoMerge &&
		linkTarget(*current, model.LinkTrustBoundaryCleared) == clearedTarget(headSHA, rules), nil
}

// unjudged reports whether task is one the trust boundary does not judge
// (holdForTrustBoundary classifies nothing of it): a stacked fix, which
// lands on a branch that is judged when it merges, or a review.
func unjudged(task model.Task) bool {
	return task.Origin.Reason == model.ReasonFix || isReviewTask(task)
}

// clearedTarget is LinkTrustBoundaryCleared's target for headSHA under this
// build's list and the repository's own rules. A repository declaring no
// triggers adds nothing, so its clearances read exactly as they always
// have; one that does pins them to what its triggers say, so a clearance
// given before a trigger started guarding a template is not one after.
func clearedTarget(headSHA string, rules model.RepoRules) string {
	version := trustboundary.Version
	switch {
	case rules.Broken != "":
		version += "+broken"
	case rules.Hash != "":
		version += "+" + rules.Hash
	}
	return version + "@" + headSHA
}

func hasBrokenRulesHold(task model.Task) bool {
	return linkTarget(task, model.LinkBrokenRulesHold) != ""
}

func hasSecurityHold(task model.Task) bool {
	return linkTarget(task, model.LinkSecurityHold) != ""
}

// heldForHuman reports whether task is on a trust-boundary hold only a
// human's Submit may end (model.LinkHumanOnlyHold). Every path that hands
// auto-merge back on grain's own judgement checks it, except the
// base-repair bypass, which exists to lift human gates.
func heldForHuman(task model.Task) bool {
	return linkTarget(task, model.LinkHumanOnlyHold) != ""
}

// linkTarget is the target of task's last link of kind, or "".
func linkTarget(task model.Task, kind model.LinkKind) string {
	target := ""
	for _, l := range task.Links {
		if l.Kind == kind {
			target = l.Target
		}
	}
	return target
}

func withoutLinks(links []model.Link, kinds ...model.LinkKind) []model.Link {
	out := links[:0:0]
	for _, l := range links {
		drop := false
		for _, k := range kinds {
			if l.Kind == k {
				drop = true
			}
		}
		if !drop {
			out = append(out, l)
		}
	}
	return out
}

func securityReviewTitle(task model.Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = task.ID
	}
	return "Security review: " + title
}

// securityReviewBody is the template's instructions with the subject
// appended -- reviewTaskBody's shape, with the trust-boundary paths that
// put the change on hold, which is what scopes the review to the diff. The
// review works on the change's own branch (putSecurityReview), so its
// checkout is that branch and what it pushes there is the change's.
func securityReviewBody(tmpl model.Template, task model.Task, ref model.PullRequestRef,
	matches []trustboundary.Match, known bool) string {

	var b strings.Builder
	b.WriteString(strings.TrimSpace(tmpl.Body))
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "You are reviewing task %s (%q), whose work is on `%s` and proposed in %s. "+
		"Your checkout is that branch itself: diff it against %s's base to see the change, and push "+
		"any fix to it.\n\n",
		task.ID, task.Title, task.BranchName(), ref, ref)
	b.WriteString(matchList(matches, known))
	return b.String()
}

func securityBypassComment(ref model.PullRequestRef, branch string, matches []trustboundary.Match, known bool,
	reviewID string, fileErr error) string {

	var b strings.Builder
	fmt.Fprintf(&b, "%s touches grain's trust boundary, but its human gate was bypassed under `base_repair_bypasses_human_gates` "+
		"because this task is repairing broken base branch `%s` while `%s` is red. Auto-merge remains enabled so the repair can merge on green CI.\n\n",
		ref, branch, branch)
	b.WriteString(matchList(matches, known))
	b.WriteString("\n")
	if fileErr != nil {
		fmt.Fprintf(&b, "Filing the security review failed (%v); the repair still merges on green CI under `base_repair_bypasses_human_gates`.", fileErr)
	} else {
		fmt.Fprintf(&b, "Filed security review task %s. It does not block this repair from merging on green CI; "+
			"if it reports findings, grain will file a follow-up task and post an alert here.", reviewID)
	}
	return b.String()
}

func securityHoldComment(ref model.PullRequestRef, matches []trustboundary.Match, known, releasable bool,
	reviewID string, fileErr error) string {

	var b strings.Builder
	fmt.Fprintf(&b, "%s touches grain's trust boundary, so it is no longer merging automatically.\n\n", ref)
	b.WriteString(matchList(matches, known))
	b.WriteString("\n")
	switch {
	case fileErr != nil:
		fmt.Fprintf(&b, "Filing the security review failed (%v), so this change waits for you: "+
			"press Submit once you have reviewed it yourself.", fileErr)
	case !known:
		fmt.Fprintf(&b, "Filed task %s to review it, but grain could not read every file this change "+
			"touches, so it will not merge automatically: press Submit once you have read the review.", reviewID)
	case releasable:
		fmt.Fprintf(&b, "Filed task %s to review it. If that review finishes without findings, "+
			"this change merges automatically again; if it leaves findings, it waits for you. "+
			"Press Submit to merge it without waiting.", reviewID)
	default:
		fmt.Fprintf(&b, "Filed task %s to review it, but changes to %s never merge automatically: "+
			"press Submit once you have read the review.", reviewID, humanOnlyList(matches))
	}
	return b.String()
}

func matchList(matches []trustboundary.Match, known bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Trust-boundary paths (%s):\n\n", trustboundary.Version)
	for _, m := range matches {
		fmt.Fprintf(&b, "- **%s** (%s) -- %s:\n", m.Category.ID, categorySource(m.Category), m.Category.Description)
		for _, p := range m.Paths {
			fmt.Fprintf(&b, "  - `%s`\n", p)
		}
	}
	if !known {
		b.WriteString("- grain could not read the full list of files this pull request touches, " +
			"so treat every file in it as on the trust boundary.\n")
	}
	return b.String()
}

// humanOnlyList names the categories in matches no review may release.
func humanOnlyList(matches []trustboundary.Match) string {
	var ids []string
	for _, m := range matches {
		if m.Category.RequiresHuman {
			ids = append(ids, "**"+m.Category.ID+"**")
		}
	}
	return strings.Join(ids, " and ")
}

func matchSummary(matches []trustboundary.Match, known bool) string {
	var ids []string
	for _, m := range matches {
		ids = append(ids, m.Category.ID)
	}
	if !known {
		ids = append(ids, "unreadable file list")
	}
	return strings.Join(ids, ", ")
}
