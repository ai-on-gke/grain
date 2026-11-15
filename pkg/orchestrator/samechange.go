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
	"maps"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// A head that moves without changing the change (grain/task-4808).
//
// The change a pull request proposes is its diff against where its head
// forked from its base -- `git diff $(git merge-base base head) head`,
// which is what GitHub's three-dot compare serves. A head can move without
// changing that diff at all: an empty "rerun CI" commit, or a merge of the
// base that resolved no conflict, by the task's own agent or by the merge
// queue. One pull request moved thirteen times and changed its diff four,
// and each move could file fresh gating reviews of what had already been
// read.
//
// So before a head move files anything, the change is compared with the
// one the gating reviews read (sameChange): every file's patch, normalised
// the way git patch-id normalises it (filePatches -- hunk line numbers and
// blob ids go), and the same set of files. When they are identical, each
// gating review's result carries to the new head as it stands -- passed,
// findings or questions -- and nothing new is filed: the path-trigger gate
// keeps its reviews and hold (carryPathTriggerGate), and the security hold
// keeps its review, findings and clearance (carrySecurityReview). The task
// says so once per head (noteReviewsCarried).
//
// A person's approval is not carried: it was given at the head they read,
// and a head move consumes it exactly as before, so a `human: required`
// trigger or a path only a human may release asks for their Submit again.
//
// A merge that resolved a conflict changes the patch of the file it
// resolved, and any new commit of the change's own changes its diff: both
// are reviewed again, incrementally where pathtriggerreview.go's rules
// allow. Anything that cannot be read or compared -- a diff GitHub will
// not serve, a binary file, a client without raw diffs -- reads as a
// change, so the failure mode is the review that would have been filed
// anyway.

// sameChange reports whether head proposes exactly the change prev did:
// both diffed against their merge base with task's pull request's base
// branch, file by file.
func sameChange(client github.Client, task model.Task, ref model.PullRequestRef, prev, head string) bool {
	if client == nil || prev == "" || head == "" {
		return false
	}
	if strings.EqualFold(prev, head) {
		return true
	}
	differ, ok := client.(trustBoundaryDiffer)
	if !ok {
		return false
	}
	base := task.Base
	if base == "" {
		detail, err := client.GetPullRequest(ref.Repo.StoredOwner(), ref.Repo.Name, ref.Number)
		if err != nil || detail.BaseRef == "" {
			return false
		}
		base = detail.BaseRef
	}
	before, err := differ.CompareDiff(ref.Repo.StoredOwner(), ref.Repo.Name, base, prev)
	if err != nil {
		log.Printf("orchestrator: reading %s's change at %s to compare with %s: %v -- reviewing it as changed",
			ref, shortSHA(prev), shortSHA(head), err)
		return false
	}
	after, err := differ.CompareDiff(ref.Repo.StoredOwner(), ref.Repo.Name, base, head)
	if err != nil {
		log.Printf("orchestrator: reading %s's change at %s to compare with %s: %v -- reviewing it as changed",
			ref, shortSHA(head), shortSHA(prev), err)
		return false
	}
	beforePatches, ok1 := filePatches(before)
	afterPatches, ok2 := filePatches(after)
	return ok1 && ok2 && maps.Equal(beforePatches, afterPatches)
}

// noteReviewsCarried says on taskID, once per head, that head moved from
// from without changing the change and its reviews were carried rather
// than filed again. The security hold and the path-trigger gate may both
// carry the same move; only the first says so.
func noteReviewsCarried(ctx context.Context, store *model.Store, taskID, from, head string, now time.Time) error {
	told := false
	if err := store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		told = strings.EqualFold(linkTarget(*t, model.LinkReviewsCarried), head)
		if !told {
			t.Links = append(withoutLinks(t.Links, model.LinkReviewsCarried),
				model.Link{Kind: model.LinkReviewsCarried, Target: head})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: recording that %s's reviews carried to %s: %w", taskID, shortSHA(head), err)
	}
	if told {
		return nil
	}
	log.Printf("orchestrator: %s moved to %s without changing its change; reviews carried from %s",
		taskID, shortSHA(head), shortSHA(from))
	body := fmt.Sprintf("Head moved to `%s` without changing the change; reviews carried from `%s`.\n\n"+
		"The diff against the base is the same, file by file, as at the head the reviews read "+
		"(an empty commit, or a merge of the base that resolved no conflict), so each review's "+
		"result stands and no new review was filed. A person's approval is not carried: "+
		"one that was needed is needed again for this head.",
		shortSHA(head), from)
	return reviewComment(ctx, store, taskID, body, now)
}

// carriedPathTriggerGates decides whether task's pull request, judged at
// headSHA, keeps the gating reviews its last judged head filed rather than
// filing its own, and returns that head and the gates as they carry.
//
// It carries only when the new judgement would gate on exactly what the
// last one did -- every gating trigger with a review names the review the
// task already waits on (or the security review this head already reuses),
// and none is missing or new -- and the change is the same as at that
// head. A trigger the base's rules added or dropped, a review that could
// not be filed last time, a file list that could not be read in full, or a
// base-repair bypass in force all judge the head afresh.
func carriedPathTriggerGates(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, headSHA string, known bool, kinds []string, byKind map[string][]reviewMatch,
	noneGates []gateFiring, securityReviewID string, coveredBySecurityReview map[string]bool) (string, []gateFiring, bool) {

	from := linkTarget(task, model.LinkPathTriggersRead)
	if !known || headSHA == "" || from == "" || strings.EqualFold(from, headSHA) || hasBaseRepairBypass(task) {
		return "", nil, false
	}
	if _, bypass := baseRepairBypassActive(ctx, store, task, ""); bypass {
		return "", nil, false
	}
	// A `template: none` gate files no review, so nothing on the task
	// names it: the only trace of one is the hold it keeps until a person
	// settles it. One with no hold to carry -- added by the base's rules,
	// or settled at the earlier head -- holds the change afresh, and a
	// hold no trigger asks for any more is released afresh.
	held := hasPathTriggerHold(task)
	if len(noneGates) > 0 && !held || len(noneGates) == 0 && len(kinds) == 0 && held {
		return "", nil, false
	}
	prev := map[string]string{}
	for id, triggers := range gatingTaskIDs(task) {
		for _, trigger := range triggers {
			prev[trigger] = id
		}
	}
	gates := append([]gateFiring(nil), noneGates...)
	seen := 0
	for _, kind := range kinds {
		matches := byKind[kind]
		id := ""
		if kind == model.SecurityReviewTemplateID && securityReviewID != "" &&
			allPathsCovered(unionPaths(matches), coveredBySecurityReview) {
			id = securityReviewID
		}
		for _, m := range matches {
			was := prev[m.rule.Trigger.Name]
			if id == "" {
				id = was
			}
			if was == "" || was != id {
				return "", nil, false
			}
			seen++
			gates = append(gates, gateFiring{trigger: m.rule.Trigger.Name, human: m.rule.Trigger.Human,
				taskID: id, paths: m.paths, sensitive: m.rule.Trigger.Sensitive,
				approvers: m.approvers, template: kind})
		}
	}
	if seen != len(prev) {
		return "", nil, false
	}
	if !sameChange(client, task, ref, from, headSHA) {
		return "", nil, false
	}
	return from, gates, true
}

// carryPathTriggerGate records that headSHA was judged and keeps every
// gating review's result as it stood at from: a hold still waiting on its
// reviews keeps waiting on them, findings and questions stay, and a gate
// that passed stays passed. What it does decide afresh is who must still
// approve, as on any head move: approvals were given at the head a person
// read, so a `human: required` trigger without a Submit of this head holds
// the change for one again.
func carryPathTriggerGate(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, from, headSHA string, gates []gateFiring, now time.Time) error {

	var human model.TriggerHuman
	for _, g := range gates {
		human = model.StricterHuman(human, g.human)
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(withoutLinks(t.Links, model.LinkPathTriggersRead),
			model.Link{Kind: model.LinkPathTriggersRead, Target: headSHA})
		t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleTrigger)
		t.Links = append(t.Links, approvalLinks(gates)...)
		if hasPathTriggerHold(*t) && len(gates) > 0 {
			t.Links = append(withoutLinks(t.Links, model.LinkPathTriggerHold),
				model.Link{Kind: model.LinkPathTriggerHold, Target: string(human)})
		}
		subMarker := humanApprovalMarker(*t)
		submittedThisHead := subMarker != "" &&
			(model.HumanSubmittedHead(subMarker) == "" || strings.EqualFold(model.HumanSubmittedHead(subMarker), headSHA))
		if human == model.TriggerHumanRequired && !submittedThisHead && linkTarget(*t, model.LinkHumanOnlyHold) == "" {
			t.Links = append(t.Links, model.Link{Kind: model.LinkHumanOnlyHold, Target: "path-trigger"})
			t.AutoMerge = false
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: carrying %s's path-trigger gate to %s: %w", task.ID, shortSHA(headSHA), err)
	}
	log.Printf("orchestrator: %s moved from %s to %s without changing its change; its gating reviews (%s) carry",
		ref, shortSHA(from), shortSHA(headSHA), gateSummary(gates))
	return noteReviewsCarried(ctx, store, task.ID, from, headSHA, now)
}

// securityReviewUnchanged reports whether task's security review read the
// change headSHA proposes, and the head it read it at: the head its
// classification is recorded for (a review pending or passed), or -- once
// findings have taken that classification away -- the head the findings
// were left at, under the same repository rules. A change with no security
// review on record, rules that do not parse, or a base-repair bypass in
// force is classified afresh.
func securityReviewUnchanged(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, headSHA string, rules model.RepoRules) (string, bool) {

	if headSHA == "" || rules.Broken != "" || task.Target == nil || hasBrokenRulesHold(task) ||
		hasBaseRepairBypass(task) || linkTarget(task, model.LinkSecurityReviewTask) == "" {
		return "", false
	}
	if _, bypass := baseRepairBypassActive(ctx, store, task, ""); bypass {
		return "", false
	}
	from := ""
	version := strings.TrimSuffix(clearedTarget("", rules), "@")
	if recordedVersion, recordedSHA, ok := strings.Cut(linkTarget(task, model.LinkTrustBoundaryCleared), "@"); ok {
		if recordedVersion != version {
			return "", false
		}
		from = recordedSHA
	} else {
		ruleString := ""
		if !rules.Empty() {
			ruleString = rules.String()
		}
		if linkTarget(task, model.LinkRepoRules) != ruleString {
			return "", false
		}
		for _, rf := range model.ReviewFindings(task) {
			if rf.Gate == model.GateSecurityReview && rf.HeadSHA != "" {
				from = rf.HeadSHA
			}
		}
	}
	if from == "" || strings.EqualFold(from, headSHA) {
		return "", false
	}
	return from, sameChange(client, task, ref, from, headSHA)
}

// carrySecurityReview moves task's security review, at from, to headSHA,
// whose change is the same: the hold, the review it waits on, its findings
// and questions all stay as they are, and a clearance moves to headSHA.
// Who must approve is decided afresh, the way holdForTrustBoundary decides
// it for any head: a Submit of another head is consumed, a path only a
// human may release holds the change for one again, and owner approvals
// are asked for again.
func carrySecurityReview(ctx context.Context, store *model.Store, task model.Task, ref model.PullRequestRef,
	from, headSHA string, cleared model.Link, matches []trustboundary.Match, now time.Time) error {

	releasable := !trustboundary.RequiresHuman(matches)
	approvers := ownerApprovers(ctx, store, *task.Target)
	needsHuman := false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		needsHuman = false
		if linkTarget(*t, model.LinkTrustBoundaryCleared) != "" {
			t.Links = append(withoutLinks(t.Links, model.LinkTrustBoundaryCleared), cleared)
		}
		subMarker := humanApprovalMarker(*t)
		submittedThisHead := subMarker != "" &&
			(model.HumanSubmittedHead(subMarker) == "" || strings.EqualFold(model.HumanSubmittedHead(subMarker), headSHA))
		hadPendingSubmit := linkTarget(*t, model.LinkHumanSubmitted) != ""
		t.Links = withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkHumanApproved, model.LinkHumanOnlyHold)
		if submittedThisHead {
			by := model.HumanSubmittedBy(subMarker)
			at, ok := model.HumanSubmittedAt(subMarker)
			if !ok {
				at = now
			}
			kind := model.LinkHumanApproved
			if hadPendingSubmit {
				kind = model.LinkHumanSubmitted
			}
			t.Links = append(t.Links, model.Link{Kind: kind, Target: model.HumanSubmittedByTarget(by, at, headSHA)})
		}
		t.Links = model.WithoutApprovalRules(t.Links, model.ApprovalRuleBuiltin)
		for _, m := range matches {
			if m.Category.OwnerApproval {
				t.Links = append(t.Links, model.ApprovalRequiredLink(model.ApprovalRuleBuiltin+m.Category.ID, approvers))
			}
		}
		if !releasable && !submittedThisHead {
			t.Links = append(t.Links, model.Link{Kind: model.LinkHumanOnlyHold, Target: trustboundary.Version})
			t.AutoMerge, needsHuman = false, true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: carrying %s's security review to %s: %w", task.ID, shortSHA(headSHA), err)
	}
	if needsHuman {
		raiseHumanOnlyReview(ctx, store, task.ID, matches, true, now)
	}
	log.Printf("orchestrator: %s moved from %s to %s without changing its change; its security review %s carries",
		ref, shortSHA(from), shortSHA(headSHA), linkTarget(task, model.LinkSecurityReviewTask))
	return noteReviewsCarried(ctx, store, task.ID, from, headSHA, now)
}
