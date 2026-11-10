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

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// pinHumanSubmit validates and pins a person's Submit approval
// (model.LinkHumanSubmitted) to headSHA, the pull request head syncEntry has
// just read.
//
// Submit is the human-approval gate only: it never clears LinkSecurityHold,
// LinkReviewFindings, LinkReviewQuestions or any pending review task, and it
// never synthesises a trust-boundary clearance for an unreviewed commit. When
// a person presses Submit before required reviews finish, LinkHumanSubmitted
// stays on the task (with AutoMerge off) so the change merges automatically
// once the last review gate passes; once all review holds have settled, it is
// recorded as LinkHumanApproved and consumed.
func pinHumanSubmit(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (model.Task, bool, error) {

	marker := linkTarget(task, model.LinkHumanSubmitted)
	if marker == "" {
		return task, true, nil
	}
	if task.Target == nil || unjudged(task) {
		by := model.HumanSubmittedBy(marker)
		at, ok := model.HumanSubmittedAt(marker)
		if !ok {
			at = now
		}
		approved := model.HumanSubmittedByTarget(by, at, headSHA)
		actor := model.GrainPrincipal
		if by != "" {
			actor = model.Principal{Kind: model.PrincipalHuman, ID: by}
		}
		if err := store.UpdateTask(model.WithAutoMergeChange(ctx, actor, "submitted"), task.ID, func(t *model.Task) error {
			if linkTarget(*t, model.LinkHumanSubmitted) != marker {
				return nil
			}
			// Stacked fix/review tasks are unjudged on their own account.
			t.Links = withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkHumanApproved,
				model.LinkSecurityHold, model.LinkHumanOnlyHold, model.LinkBrokenRulesHold)
			t.Links = append(t.Links, model.Link{Kind: model.LinkHumanApproved, Target: approved})
			if !model.AutoMergeDeclined(*t) && !model.HasPendingReviewHold(*t) {
				t.AutoMerge = true
			}
			return nil
		}); err != nil {
			return task, false, err
		}
		if reloaded, err := store.GetTask(ctx, task.ID); err == nil && reloaded != nil {
			return *reloaded, true, nil
		}
		return task, true, nil
	}
	if headSHA == "" {
		return task, false, nil
	}

	shown := model.HumanSubmittedHead(marker)
	if shown == "" {
		shown = classifiedHead(task)
	}
	if shown == "" {
		shown = headSHA
	}

	if !strings.EqualFold(shown, headSHA) {
		if carried, ok, err := carryApprovedAcrossBaseMerge(ctx, store, client, task, ref, shown, headSHA, marker, now); err != nil || ok {
			return carried, ok, err
		}
		return reclassifySubmitted(ctx, store, client, task, ref, shown, headSHA, now)
	}

	by := model.HumanSubmittedBy(marker)
	at, ok := model.HumanSubmittedAt(marker)
	if !ok {
		at = now
	}
	pinnedMarker := model.HumanSubmittedByTarget(by, at, headSHA)

	// While any review hold or review findings remain, keep LinkHumanSubmitted
	// pinned to headSHA with AutoMerge off and wait for those review gates to
	// settle.
	if hasSecurityHold(task) || model.HasPendingReviewHold(task) {
		if model.HumanSubmittedHead(marker) == "" {
			if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
				if linkTarget(*t, model.LinkHumanSubmitted) != marker {
					return nil
				}
				t.Links = append(withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkHumanApproved),
					model.Link{Kind: model.LinkHumanSubmitted, Target: pinnedMarker})
				return nil
			}); err != nil {
				return task, false, fmt.Errorf("orchestrator: pinning %s's Submit to %s: %w", task.ID, shortSHA(headSHA), err)
			}
			if reloaded, err := store.GetTask(ctx, task.ID); err == nil && reloaded != nil {
				task = *reloaded
			}
		}
		return task, true, nil
	}

	rules := repoRulesFor(ctx, store, *task.Target)
	recorded := linkTarget(task, model.LinkTrustBoundaryCleared)
	unreadableReviewed := recorded == "" && linkTarget(task, model.LinkSecurityReviewTask) != ""
	if recorded != clearedTarget(headSHA, rules) && !unreadableReviewed {
		if err := holdForTrustBoundary(ctx, store, client, task, ref, headSHA, now); err != nil {
			return task, false, err
		}
		current, err := store.GetTask(ctx, task.ID)
		if err != nil || current == nil {
			return task, false, err
		}
		task = *current
		if hasSecurityHold(task) || model.HasPendingReviewHold(task) {
			return task, false, nil
		}
	}

	actor := model.GrainPrincipal
	if by != "" {
		actor = model.Principal{Kind: model.PrincipalHuman, ID: by}
	}
	var out *model.Task
	if err := store.UpdateTask(model.WithAutoMergeChange(ctx, actor, "all approval and review gates passed"), task.ID, func(t *model.Task) error {
		out = nil
		if hasSecurityHold(*t) || model.HasPendingReviewHold(*t) {
			return nil
		}
		t.Links = append(withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkHumanApproved),
			model.Link{Kind: model.LinkHumanApproved, Target: pinnedMarker})
		t.Links = markRelayedReviewThreadsDecided(t.Links, "")
		if !hasPathTriggerHold(*t) && !heldForHuman(*t) && len(model.PendingApprovals(*t)) == 0 {
			t.AutoMerge = true
		}
		cp := *t
		out = &cp
		return nil
	}); err != nil {
		return task, false, err
	}
	if out == nil {
		current, err := store.GetTask(ctx, task.ID)
		if err != nil || current == nil {
			return task, false, err
		}
		return *current, true, nil
	}
	resolveHandledRelayedThreads(ctx, store, client, *out, ref,
		fmt.Sprintf("Resolved per owner decision on task %s.", task.ID))
	return *out, true, nil
}

// carryApprovedAcrossBaseMerge carries a completed security clearance from
// shown to headSHA on the strength of a human's Submit of shown, when the
// branch moved without changing any trust-boundary patch (such as the merge
// queue merging main in). The Submit itself is consumed, not moved: it was
// about the head the person read, and a `human: required` trigger judging
// headSHA (applyPathTriggerGate) asks for theirs again rather than reading
// an approval of files they never saw as one given at headSHA.
func carryApprovedAcrossBaseMerge(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, shown, headSHA, marker string, now time.Time) (model.Task, bool, error) {

	if hasSecurityHold(task) || model.HasPendingReviewHold(task) || hasBrokenRulesHold(task) || client == nil || task.Target == nil {
		return task, false, nil
	}
	rules := repoRulesFor(ctx, store, *task.Target)
	if rules.Broken != "" || linkTarget(task, model.LinkTrustBoundaryCleared) != clearedTarget(shown, rules) {
		return task, false, nil
	}
	files, err := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil || len(files) >= github.PullRequestFilesCap {
		return task, false, nil
	}
	matches := trustboundary.Classify(files)
	_, guarded := repoGuard(ctx, store, client, *task.Target, files, true)
	matches = append(matches, guarded...)
	if m, ok := guardTemplatePush(client, *task.Target, headSHA, files); ok {
		matches = append(matches, m)
	}
	if len(matches) > 0 {
		differ, ok := client.(trustBoundaryDiffer)
		if !ok {
			return task, false, nil
		}
		base := task.Base
		if base == "" {
			detail, derr := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
			if derr != nil || detail.BaseRef == "" {
				return task, false, nil
			}
			base = detail.BaseRef
		}
		afterDiff, err1 := differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, base, headSHA)
		beforeDiff, err2 := differ.CompareDiff(ref.Repo.Owner, ref.Repo.Name, base, shown)
		if err1 != nil || err2 != nil {
			return task, false, nil
		}
		afterPatches, ok1 := filePatches(afterDiff)
		beforePatches, ok2 := filePatches(beforeDiff)
		if !ok1 || !ok2 || !sameTrustBoundaryPatches(ctx, store, client, *task.Target, beforePatches, afterPatches, matches) {
			return task, false, nil
		}
	}
	cleared := model.Link{Kind: model.LinkTrustBoundaryCleared, Target: clearedTarget(headSHA, rules)}
	by := model.HumanSubmittedBy(marker)
	at, ok := model.HumanSubmittedAt(marker)
	if !ok {
		at = now
	}
	actor := model.GrainPrincipal
	if by != "" {
		actor = model.Principal{Kind: model.PrincipalHuman, ID: by}
	}
	approved := model.Link{Kind: model.LinkHumanApproved, Target: model.HumanSubmittedByTarget(by, at, shown)}
	if err := store.UpdateTask(model.WithAutoMergeChange(ctx, actor, "carried approval across base merge"), task.ID, func(t *model.Task) error {
		if linkTarget(*t, model.LinkHumanSubmitted) != marker {
			return nil
		}
		t.Links = append(withoutLinks(t.Links, model.LinkTrustBoundaryCleared, model.LinkHumanSubmitted, model.LinkHumanApproved),
			cleared, approved)
		if !hasSecurityHold(*t) && !hasPathTriggerHold(*t) && !heldForHuman(*t) &&
			!model.HasPendingReviewHold(*t) && len(model.PendingApprovals(*t)) == 0 {
			t.AutoMerge = true
		}
		return nil
	}); err != nil {
		return task, false, err
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		return task, false, err
	}
	return *current, true, nil
}

// reclassifySubmitted drops a stale LinkHumanSubmitted marker when the branch
// moved to an unreviewed headSHA and classifies headSHA afresh.
func reclassifySubmitted(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, shown, headSHA string, now time.Time) (model.Task, bool, error) {

	log.Printf("orchestrator: %s was submitted by a human at %s, and its head is now %s -- "+
		"classifying that head rather than carrying the Submit to it", ref, shortSHA(shown), shortSHA(headSHA))
	_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkHumanApproved)
		return nil
	})
	if err := holdForTrustBoundary(ctx, store, client, task, ref, headSHA, now); err != nil {
		return task, false, err
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		return task, false, err
	}
	rules := repoRulesFor(ctx, store, *task.Target)
	ok := current.AutoMerge && !hasSecurityHold(*current) && !model.HasPendingReviewHold(*current) &&
		linkTarget(*current, model.LinkTrustBoundaryCleared) == clearedTarget(headSHA, rules)
	return *current, ok, nil
}

// classifiedHead is the head task's recorded classification
// (model.LinkTrustBoundaryCleared) is for, or "" where it has none.
func classifiedHead(task model.Task) string {
	recorded := linkTarget(task, model.LinkTrustBoundaryCleared)
	if i := strings.LastIndexByte(recorded, '@'); i >= 0 {
		return recorded[i+1:]
	}
	return ""
}
