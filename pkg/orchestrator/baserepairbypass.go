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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func hasBaseRepairBypass(task model.Task) bool {
	return linkTarget(task, model.LinkBaseRepairBypass) != ""
}

// resolveTaskBaseBranch is the branch task's pull request targets: the one
// the caller read off the pull request itself, else the task's own base
// (model.TaskBaseBranch's default when it names none). It is never read off
// a repair tag: a tag naming some other branch must not let that branch's
// red exempt a pull request into this one.
func resolveTaskBaseBranch(task model.Task, baseBranch string) string {
	if b := strings.TrimSpace(baseBranch); b != "" {
		return b
	}
	if b := strings.TrimSpace(task.Base); b != "" {
		return b
	}
	return "main"
}

// isBaseBranchRepairTask reports whether task is a repair of repo's branch
// that may bypass human gates: the repair grain registered for that red
// branch, or a task carrying that branch's own repair tag. Neither a title
// nor a repair tag for some other repository or branch counts -- an agent
// writes the first and either would let any change claim the exemption.
// A task marked as a repair (model.MarkedBaseRepairTag) counts only when a
// person approved it: an agent's propose_task may set fixes_base_branch,
// and an auto-merge job's proposals are filed already approved
// (model.SelfApproved), so honouring that mark would let an agent's own
// unreviewed work past the very gates that exist to stop it.
func isBaseBranchRepairTask(ctx context.Context, store *model.Store, task model.Task, repo model.RepoRef, branch string) bool {
	if store != nil && branch != "" {
		if ciRow, err := store.GetBaseBranchCI(ctx, repo, branch); err == nil && ciRow != nil && ciRow.RepairTaskID == task.ID {
			return true
		}
	}
	if !hasTag(task, BaseCIRepairBranchTag(repo, branch)) && !hasTag(task, BaseReleaseTriageBranchTag(repo, branch)) {
		return false
	}
	if hasTag(task, model.MarkedBaseRepairTag) && model.SelfApproved(task) {
		return false
	}
	return true
}

func isBaseBranchRed(ctx context.Context, store *model.Store, repo model.RepoRef, branch string) bool {
	if store == nil || branch == "" {
		return false
	}
	if ciRow, err := brokenBaseBranch(ctx, store, repo, branch); err == nil && ciRow != nil {
		return true
	}
	if off, err := baseBranchCIOff(ctx, store, repo); err == nil && !off {
		if ciRow, err := store.GetBaseBranchCI(ctx, repo, branch); err == nil && ciRow != nil {
			if ciRow.Confirmation != model.BaseCICleared &&
				(ciRow.Conclusion == model.BaseBranchCIFailure || ciRow.Conclusion == string(ci.ConclusionErrored)) {
				return true
			}
		}
	}
	if relRow, err := store.GetBaseBranchRelease(ctx, repo, branch); err == nil && relRow != nil {
		if relRow.Failing() || (relRow.Conclusion != "" &&
			relRow.Conclusion != string(ci.ConclusionSuccess) &&
			relRow.Conclusion != string(ci.ConclusionSkipped)) {
			return true
		}
	}
	return false
}

// baseRepairBypassActive reports whether task is an active base-branch repair
// on a repository that has enabled base_repair_bypasses_human_gates while its
// target base branch is actually red.
func baseRepairBypassActive(ctx context.Context, store *model.Store, task model.Task, baseBranch string) (string, bool) {
	if store == nil || task.Target == nil {
		return "", false
	}
	cfg, err := store.GetRepoConfig(ctx, *task.Target)
	if err != nil || cfg == nil || !cfg.BaseRepairBypassesHumanGates {
		return "", false
	}
	branch := resolveTaskBaseBranch(task, baseBranch)
	if !isBaseBranchRepairTask(ctx, store, task, *task.Target, branch) {
		return "", false
	}
	if !isBaseBranchRed(ctx, store, *task.Target, branch) {
		return "", false
	}
	return branch, true
}

// revokeExpiredBaseRepairBypass checks whether a task carrying
// LinkBaseRepairBypass is still eligible for the exemption (base branch still
// red and option still on). Once the base branch is green again (or the option
// is turned off), the exemption no longer applies: the bypass link and cached
// clearances are dropped and both trust-boundary and path-trigger gates are
// re-evaluated against the pull request.
func revokeExpiredBaseRepairBypass(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (bool, error) {

	if store == nil {
		return false, nil
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	if !hasBaseRepairBypass(*current) {
		return false, nil
	}
	if _, active := baseRepairBypassActive(ctx, store, *current, ""); active {
		return false, nil
	}
	wasAutoMerge := current.AutoMerge
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = withoutLinks(t.Links,
			model.LinkBaseRepairBypass,
			model.LinkTrustBoundaryCleared,
			model.LinkPathTriggersRead,
		)
		return nil
	}); err != nil {
		return false, fmt.Errorf("orchestrator: revoking expired base-repair bypass on %s: %w", task.ID, err)
	}
	updated, err := store.GetTask(ctx, task.ID)
	if err != nil || updated == nil {
		return false, err
	}
	log.Printf("orchestrator: base branch for %s (%s) is no longer red under base_repair_bypasses_human_gates; re-evaluating human gates",
		task.ID, ref)
	if err := holdForTrustBoundary(ctx, store, client, *updated, ref, headSHA, now); err != nil {
		return false, err
	}
	afterTB, err := store.GetTask(ctx, task.ID)
	if err != nil || afterTB == nil {
		return false, err
	}
	withOriginalAutoMerge := *afterTB
	if wasAutoMerge {
		withOriginalAutoMerge.AutoMerge = true
	}
	if err := judgePullRequestTriggers(ctx, store, client, withOriginalAutoMerge, ref, headSHA, now); err != nil {
		return false, err
	}
	final, err := store.GetTask(ctx, task.ID)
	if err != nil || final == nil {
		return false, err
	}
	return !final.AutoMerge, nil
}

// fileBaseRepairReviewFollowUp files a follow-up task and posts a loud alert on
// the reviewed base-repair task (and its pull request) when a review reports
// findings on a base-branch repair that bypassed human gates under
// base_repair_bypasses_human_gates. It never blocks or withdraws auto-merge on
// the repair task.
func fileBaseRepairReviewFollowUp(ctx context.Context, store *model.Store, client github.Client,
	review, reviewed model.Task, ref model.PullRequestRef, findings []reviewFinding, draftID int,
	now time.Time) error {

	baseBranch := resolveTaskBaseBranch(reviewed, "")
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: allocating follow-up task ID for %s's review findings: %w", reviewed.ID, err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return fmt.Errorf("orchestrator: ordering follow-up task for %s's review findings: %w", reviewed.ID, err)
	}
	title := fmt.Sprintf("Address review findings from task %s on base-repair %s", review.ID, ref)
	body := fmt.Sprintf(
		"Base-branch repair task %s (%q, %s) bypassed human review gates under `base_repair_bypasses_human_gates` "+
			"while `%s` was red so the broken base branch could recover on green CI.\n\n"+
			"Review task %s (%q) reported %s that need follow-up on `%s`:\n\n%s",
		reviewed.ID, reviewed.Title, ref, baseBranch,
		review.ID, review.Title, countOf(len(findings), "finding"), baseBranch,
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
		AutoMerge:  reviewed.AutoMerge,
		Links:      []model.Link{{Kind: model.LinkProposedBy, Target: reviewed.ID}},
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	model.InheritRequester(&followUp, reviewed)
	if err := store.PutTask(ctx, followUp); err != nil {
		return fmt.Errorf("orchestrator: filing follow-up task %s for %s's review findings: %w", followUp.ID, reviewed.ID, err)
	}
	if draftID != 0 {
		if err := store.UpdateTask(ctx, reviewed.ID, func(t *model.Task) error {
			t.Links = append(t.Links, model.Link{Kind: model.LinkDraftReview, Target: fmt.Sprintf("%d", draftID)})
			return nil
		}); err != nil {
			return fmt.Errorf("orchestrator: recording draft review on %s: %w", reviewed.ID, err)
		}
	}
	loudNote := fmt.Sprintf(
		"**ALERT: Review findings on bypassed base-branch repair — filed follow-up task %s.**\n\n"+
			"This base-branch repair (%s) bypassed human review gates under `base_repair_bypasses_human_gates` "+
			"while `%s` was red, so these findings do **not** block its merge. Review task %s left %s:\n\n%s\n\n"+
			"Filed follow-up task %s on `%s` to address them.",
		followUp.ID, ref, baseBranch, review.ID, countOf(len(findings), "finding"),
		findingsList(findings), followUp.ID, baseBranch,
	)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    reviewed.ID,
		Author:    model.Attribution{Actor: reviewer},
		Body:      loudNote,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: posting review follow-up alert on %s: %w", reviewed.ID, err)
	}
	if client != nil && ref.Number > 0 {
		prNote := fmt.Sprintf("**grain review alert** (task %s)\n\n%s", reviewed.ID, loudNote)
		if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, prNote); err != nil {
			log.Printf("orchestrator: noting %s's review follow-up task %s on %s: %v -- it is on the task only",
				reviewed.ID, followUp.ID, ref, err)
		}
	}
	log.Printf("orchestrator: review task %s left %d finding(s) on bypassed base-repair task %s (%s); filed follow-up task %s without blocking merge",
		review.ID, len(findings), reviewed.ID, ref, followUp.ID)
	return nil
}
