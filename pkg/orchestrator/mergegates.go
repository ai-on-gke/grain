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
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// LinkPostMergeReviewCommit records on a review task that the pull request it
// was reviewing merged at the target commit SHA while the review was still
// running (under a merge:force gate override or base_repair_bypasses_human_gates),
// so the review continues against that commit on the base branch rather than
// closing unfinished, and any findings it reports are filed as follow-up fix
// tasks.
const LinkPostMergeReviewCommit model.LinkKind = "post-merge-review-commit"

// reviewGateOf identifies which merge gate a review task belongs to on reviewed.
func reviewGateOf(reviewed model.Task, reviewID string) string {
	if reviewID != "" && linkTarget(reviewed, model.LinkSecurityReviewTask) == reviewID {
		return model.GateSecurityReview
	}
	if trigger, ok := gatingTaskIDs(reviewed)[reviewID]; ok && trigger != "" {
		return "trigger:" + trigger
	}
	return model.GateReview
}

// mergedThroughOverrideOrBypass reports whether reviewed merged via grain's
// explicit merge:force gate override or base_repair_bypasses_human_gates,
// rather than by hand outside grain.
func mergedThroughOverrideOrBypass(ctx context.Context, store *model.Store, reviewed model.Task) bool {
	if hasBaseRepairBypass(reviewed) {
		return true
	}
	if _, ok := model.LatestGateOverride(reviewed); ok {
		return true
	}
	if store != nil {
		if fresh, err := store.GetTask(ctx, reviewed.ID); err == nil && fresh != nil {
			if hasBaseRepairBypass(*fresh) {
				return true
			}
			if _, ok := model.LatestGateOverride(*fresh); ok {
				return true
			}
		}
		if records, err := store.ForceMergeRecords(ctx); err == nil {
			for _, rec := range records {
				if rec.TaskID == reviewed.ID {
					return true
				}
			}
		}
	}
	return false
}

// isPostMergeReview reports whether reviewed has already merged under an
// override or base-repair bypass so findings from review should be filed as a
// follow-up fix task on the base branch instead of holding an already-merged
// pull request.
func isPostMergeReview(ctx context.Context, store *model.Store, review, reviewed model.Task) bool {
	if hasBaseRepairBypass(reviewed) {
		return true
	}
	if linkTarget(review, LinkPostMergeReviewCommit) != "" {
		return true
	}
	if mergedThroughOverrideOrBypass(ctx, store, reviewed) {
		if obs, err := store.GetObservation(ctx, reviewed.ID); err == nil && obs != nil && obs.PrMergedAt != nil {
			return true
		}
	}
	return false
}

// humanApprovalMarker returns task's LinkHumanSubmitted or LinkHumanApproved
// target, whichever is present.
func humanApprovalMarker(task model.Task) string {
	if m := linkTarget(task, model.LinkHumanSubmitted); m != "" {
		return m
	}
	return linkTarget(task, model.LinkHumanApproved)
}

// continueReviewAfterMerge keeps an open review task running against the
// merged commit on baseBranch instead of closing it unfinished when the pull
// request it was reviewing merges under an override. Returns true when the
// review was kept alive.
func continueReviewAfterMerge(ctx context.Context, store *model.Store, client github.Client,
	reviewed model.Task, reviewID string, ref *model.PullRequestRef, mergedCommit, baseBranch string,
	now time.Time) (bool, error) {

	if reviewID == "" {
		return false, nil
	}
	review, err := store.GetTask(ctx, reviewID)
	if err != nil {
		return false, err
	}
	if review == nil || hasPullRequest(*review) {
		return false, nil
	}
	state, err := store.State(ctx, reviewID)
	if err != nil {
		return false, err
	}
	if state == "" || state.Settled() {
		return false, nil
	}
	resolvedBase := resolveTaskBaseBranch(reviewed, baseBranch)
	commit := strings.TrimSpace(mergedCommit)
	if commit == "" {
		commit = "HEAD"
	}
	alreadyContinued := false
	if err := store.UpdateTask(ctx, reviewID, func(t *model.Task) error {
		alreadyContinued = linkTarget(*t, LinkPostMergeReviewCommit) != ""
		// Retarget onto the base branch and disable auto-merge on any PR the
		// review itself might open, since a review is unclassified on its own
		// account when aimed at the base branch.
		t.Base = resolvedBase
		t.AutoMerge = false
		detachFromReviewedBranch(t)
		if !alreadyContinued {
			t.Links = append(t.Links, model.Link{Kind: LinkPostMergeReviewCommit, Target: commit})
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("orchestrator: continuing review %s post-merge on %s: %w", reviewID, resolvedBase, err)
	}
	if alreadyContinued {
		return true, nil
	}

	prLabel := "its pull request"
	if ref != nil {
		prLabel = ref.String()
	}
	commitDisplay := commit
	if commit != "HEAD" {
		commitDisplay = shortSHA(commit)
	}
	reviewNote := fmt.Sprintf(
		"The change under review (task %s, %s) merged onto `%s` at `%s` while this review was still in flight "+
			"(under an explicit override). Keep reviewing commit `%s` on `%s`; do not push directly to the old "+
			"feature branch. Report any defects via `add_review_comment` so grain files follow-up fix tasks on `%s`.",
		reviewed.ID, prLabel, resolvedBase, commitDisplay, commitDisplay, resolvedBase, resolvedBase,
	)
	if err := reviewComment(ctx, store, reviewID, reviewNote, now); err != nil {
		return true, err
	}
	taskNote := fmt.Sprintf(
		"This change merged onto `%s` at `%s` while review task %s was still running. "+
			"Kept task %s running post-merge against `%s` on `%s`; any findings it leaves will be filed "+
			"as follow-up fix tasks.",
		resolvedBase, commitDisplay, reviewID, reviewID, commitDisplay, resolvedBase,
	)
	if err := reviewComment(ctx, store, reviewed.ID, taskNote, now); err != nil {
		return true, err
	}
	if ref != nil && client != nil && ref.Number > 0 {
		prNote := fmt.Sprintf("**grain post-merge review** (task %s)\n\n%s", reviewed.ID, taskNote)
		if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prNote); err != nil {
			log.Printf("orchestrator: noting post-merge continuation of review %s on %s: %v", reviewID, ref, err)
		}
	}
	return true, nil
}

// filePostMergeReviewFollowUp files an approved follow-up fix task on the base
// branch and posts an alert on both the reviewed task and its pull request when
// a review continuing post-merge (after a merge:force gate override or
// base_repair_bypasses_human_gates) reports findings.
func filePostMergeReviewFollowUp(ctx context.Context, store *model.Store, client github.Client,
	review, reviewed model.Task, ref model.PullRequestRef, findings []reviewFinding, draftID int,
	now time.Time) error {

	if hasBaseRepairBypass(reviewed) {
		return fileBaseRepairReviewFollowUp(ctx, store, client, review, reviewed, ref, findings, draftID, now)
	}
	baseBranch := resolveTaskBaseBranch(reviewed, "")
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: allocating follow-up task ID for %s's post-merge review findings: %w", reviewed.ID, err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return fmt.Errorf("orchestrator: ordering follow-up task for %s's post-merge review findings: %w", reviewed.ID, err)
	}
	commit := linkTarget(review, LinkPostMergeReviewCommit)
	commitClause := ""
	if commit != "" && commit != "HEAD" {
		commitClause = fmt.Sprintf(" (merged commit `%s`)", shortSHA(commit))
	}
	overrideClause := "before its review finished"
	if ov, ok := model.LatestGateOverride(reviewed); ok {
		overrideClause = fmt.Sprintf("under a gate override by %s (%q)", ov.By, ov.Reason)
	}
	title := fmt.Sprintf("Fix post-merge review findings from task %s on %s", review.ID, ref)
	body := fmt.Sprintf(
		"Task %s (%q, %s)%s merged onto `%s` %s while review task %s (%q) was still running.\n\n"+
			"Post-merge review task %s reported %s that need follow-up fixes on `%s`:\n\n%s",
		reviewed.ID, reviewed.Title, ref, commitClause, baseBranch, overrideClause,
		review.ID, review.Title,
		review.ID, countOf(len(findings), "finding"), baseBranch,
		findingsList(findings),
	)
	followUp := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  title,
		Body:   body,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: reviewer},
			Reason:      model.ReasonProposal,
		},
		Approval:   &model.Attribution{Actor: reviewer},
		ApprovedAt: &now,
		Target:     reviewed.Target,
		Binding:    model.BindingDirective,
		Base:       baseBranch,
		Reads:      reviewed.Reads,
		Grants:     reviewed.Grants,
		AutoMerge:  true,
		Links:      []model.Link{{Kind: model.LinkProposedBy, Target: reviewed.ID}},
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	model.InheritRequester(&followUp, reviewed)
	model.InheritContext(&followUp, reviewed)
	if err := store.PutTask(ctx, followUp); err != nil {
		return fmt.Errorf("orchestrator: filing post-merge follow-up task %s for %s: %w", followUp.ID, reviewed.ID, err)
	}
	if draftID != 0 {
		relayed := discoverRelayedThreads(client, ref, review.ID, draftID, true)
		_ = store.UpdateTask(ctx, reviewed.ID, func(t *model.Task) error {
			t.Links = append(t.Links, model.Link{Kind: model.LinkDraftReview, Target: fmt.Sprintf("%d", draftID)})
			for _, rt := range relayed {
				t.Links = append(t.Links, model.Link{Kind: model.LinkRelayedReviewThread, Target: rt.Target()})
			}
			return nil
		})
	}
	alertNote := fmt.Sprintf(
		"**Post-merge review findings — filed follow-up task %s.**\n\n"+
			"This change (%s)%s merged onto `%s` %s while review task %s was still running. "+
			"Review task %s finished with %s:\n\n%s\n\n"+
			"Filed follow-up task [Task #%s](/tasks/%s) on `%s` to fix them.",
		followUp.ID, ref, commitClause, baseBranch, overrideClause, review.ID,
		review.ID, countOf(len(findings), "finding"), findingsList(findings),
		followUp.ID, followUp.ID, baseBranch,
	)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    reviewed.ID,
		Author:    model.Attribution{Actor: reviewer},
		Body:      alertNote,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: posting post-merge review alert on %s: %w", reviewed.ID, err)
	}
	if client != nil && ref.Number > 0 {
		prNote := fmt.Sprintf("**grain post-merge review alert** (task %s)\n\n%s", reviewed.ID, alertNote)
		if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prNote); err != nil {
			log.Printf("orchestrator: noting post-merge follow-up task %s on %s: %v", followUp.ID, ref, err)
		}
	}
	return nil
}

// clearReviewFindingsOnCleanReview clears any unresolved findings record on the
// task review was reviewing once a subsequent review (or a later re-run of the
// same review task) finishes cleanly without leaving new findings, and restores
// auto-merge when all remaining gates have passed.
func clearReviewFindingsOnCleanReview(ctx context.Context, store *model.Store, review model.Task) error {
	if !isReviewTask(review) {
		return nil
	}
	reviewedID := linkTarget(review, model.LinkProposedBy)
	if reviewedID == "" {
		return nil
	}
	reviewed, err := store.GetTask(ctx, reviewedID)
	if err != nil || reviewed == nil {
		return err
	}
	obs, err := store.GetObservation(ctx, review.ID)
	if err != nil || obs == nil || obs.CompletedAt == nil {
		return err
	}
	completedAt := obs.CompletedAt.UTC().Truncate(time.Second)
	gate := reviewGateOf(*reviewed, review.ID)
	records := model.ReviewFindingsForGate(*reviewed, gate)
	if len(records) == 0 {
		return nil
	}
	for _, rec := range records {
		// Do not clear findings from the very same review run that just
		// recorded them: clearing requires either a newly filed review task
		// (rec.ReviewTask != review.ID) or a later re-run of review.ID whose
		// CompletedAt is strictly after rec.At.
		if rec.ReviewTask == review.ID && !completedAt.After(rec.At.UTC().Truncate(time.Second)) {
			return nil
		}
	}
	return store.UpdateTask(ctx, reviewedID, func(t *model.Task) error {
		switch {
		case gate == model.GateSecurityReview && linkTarget(*t, model.LinkSecurityReviewTask) != review.ID:
			return nil
		case gate == model.GateReview && linkTarget(*t, model.LinkReviewTask) != "" && linkTarget(*t, model.LinkReviewTask) != review.ID:
			return nil
		}
		model.ClearReviewFindingsForGate(t, gate)
		if q, ok := model.ReviewQuestionsOf(*t); ok && (q.ReviewTaskID == review.ID || q.ReviewTaskID != "") {
			t.Links = withoutLinks(t.Links, model.LinkReviewQuestions)
		}
		if !hasSecurityHold(*t) && !hasPathTriggerHold(*t) && !heldForHuman(*t) &&
			!model.HasReviewFindings(*t) && len(model.PendingApprovals(*t)) == 0 {
			t.AutoMerge = true
		}
		return nil
	})
}

// advanceHumanSubmitted carries a LinkHumanSubmitted marker forward from fromHead
// to toHead when a clean review commit or carried clearance advances the head
// without introducing unreviewed changes.
func advanceHumanSubmitted(links []model.Link, fromHead, toHead string) []model.Link {
	if fromHead == "" || toHead == "" || strings.EqualFold(fromHead, toHead) {
		return links
	}
	out := slices.Clone(links)
	for i, l := range out {
		if l.Kind != model.LinkHumanSubmitted && l.Kind != model.LinkHumanApproved {
			continue
		}
		head := model.HumanSubmittedHead(l.Target)
		if head != "" && strings.EqualFold(head, fromHead) {
			by := model.HumanSubmittedBy(l.Target)
			at, ok := model.HumanSubmittedAt(l.Target)
			if !ok {
				at = time.Now().UTC()
			}
			out[i] = model.Link{Kind: l.Kind, Target: model.HumanSubmittedByTarget(by, at, toHead)}
		}
	}
	return out
}

// forceOverridesGates reports whether the operator's gate override for the
// forced commit (model.LinkGateOverride, pinned to forcedHead) covers every
// merge gate other than CI that still stands on taskID as the store has it
// now. A force request is checked against the gates it could see when it was
// made (ui.Client.ForceMergeWithGates); a head classified only afterwards --
// a trust-boundary push the request never saw held, an approver still owed,
// `auto_merge: never` -- can raise a gate it did not name, and it is that gate,
// not the request's say-so, that decides whether the forced merge proceeds.
func forceOverridesGates(ctx context.Context, store *model.Store, taskID, forcedHead string) (bool, error) {
	if forcedHead == "" {
		return false, nil
	}
	t, err := store.GetTask(ctx, taskID)
	if err != nil || t == nil {
		return false, err
	}
	ov, ok := model.LatestGateOverride(*t)
	if !ok || !strings.EqualFold(ov.HeadSHA, forcedHead) {
		return false, nil
	}
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		return false, err
	}
	states, err := store.States(ctx)
	if err != nil {
		return false, err
	}
	for _, g := range model.ComputeMergeGates(*t, obs, states) {
		if g.ID != model.GateCI && !g.State.Satisfied() {
			return false, nil
		}
	}
	return true, nil
}
