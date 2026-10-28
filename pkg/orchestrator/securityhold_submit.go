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

// pinHumanSubmit turns a human's Submit (model.LinkHumanSubmitted, which
// ui.Client.Submit writes with the head the human was shown) into a
// clearance of headSHA, the head syncEntry has just read and is about to
// judge -- before clearedForHead, and so before anything this cycle can
// merge the base branch into the branch or merge the branch itself.
//
// Only where headSHA is that head, though. A push landing between the
// Submit and this cycle is a commit nobody reviewed, and pinning the
// decision to it merged it on the strength of the human's look at its
// parent (task 979). So where the branch has moved since, headSHA is
// classified here and now, as any moved head is (holdForTrustBoundary,
// which drops the marker), and held for a review of its own where it
// touches the trust boundary. A marker naming no head -- written before
// the head was recorded, or by a Submit that could not read it -- is
// taken to be about the head grain last classified, the one the hold the
// human answered was for. With no classification either -- a change
// that never merged itself, so was never classified, submitted where the
// UI could read no head (the config repository's pull requests, which
// have no GitHub head and no grain CI) -- nothing says which commit the
// human looked at, and the Submit is pinned to headSHA as it was before
// task 979: classifying it instead would hold every such Submit, with no
// way for the human to answer the hold but another Submit that reads no
// head either.
//
// Without it, a Submit over a stale clearance (one recorded for a head a
// stacked fix has since moved past) reads as a head that moved, and is
// classified again and held over the human's decision; and a Submit over
// no clearance at all leaves carryClearance nothing to carry from once a
// later run reclassifies the change. task 441 was held four times that
// way after it had been shipped.
//
// A human's Submit is about the head they read, so what is pinned is the
// clearance a review could have given for it and no more: where the head
// touches a path only a human or an owner may release, or the base's
// rules do not parse, no clearance is recorded -- and any stale one is
// dropped -- which leaves the change outside clearedForHead's checks
// altogether, the "not grain's call" a human-only hold always was.
// Carrying a clearance is what those paths can never do (carryClearance),
// so recording one for them would only hold the change again at the next
// base-branch merge.
//
// ok is false when this cycle should stop for task: the head is unknown,
// or its file list could not be read, and the marker stays for the next
// cycle to try again. The task returned is the one to judge from here on.
func pinHumanSubmit(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, headSHA string, now time.Time) (model.Task, bool, error) {

	marker := linkTarget(task, model.LinkHumanSubmitted)
	if marker == "" {
		return task, true, nil
	}
	cleared := ""
	// A change the trust boundary does not judge is never classified, so
	// no head of it could ever be cleared: its marker is only dropped.
	judged := task.AutoMerge && task.Target != nil && !unjudged(task)
	if judged {
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
		if shown == "" || !strings.EqualFold(shown, headSHA) {
			return reclassifySubmitted(ctx, store, client, task, ref, shown, headSHA, now)
		}
	}
	if judged {
		files, err := client.ListPullRequestFiles(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			log.Printf("orchestrator: reading which files %s touches to pin its Submit to %s: %v -- "+
				"trying again next cycle", ref, shortSHA(headSHA), err)
			return task, false, nil
		}
		known := len(files) < github.PullRequestFilesCap
		matches := trustboundary.Classify(files)
		rules, guarded := repoGuard(ctx, store, client, *task.Target, files, known)
		matches = append(matches, guarded...)
		if known && rules.Broken == "" && carryable(matches) {
			cleared = clearedTarget(headSHA, rules)
		}
	}

	var out *model.Task
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		out = nil
		if linkTarget(*t, model.LinkHumanSubmitted) != marker {
			// Classified, or submitted again, since it was read: that
			// answer stands, and the next cycle judges a new Submit.
			return nil
		}
		t.Links = withoutLinks(t.Links, model.LinkHumanSubmitted, model.LinkTrustBoundaryCleared,
			model.LinkClearanceInheritedFrom, model.LinkHumanOnlyHold)
		if cleared != "" && t.AutoMerge {
			t.Links = append(t.Links, model.Link{Kind: model.LinkTrustBoundaryCleared, Target: cleared})
		}
		cp := *t
		out = &cp
		return nil
	}); err != nil {
		return task, false, fmt.Errorf("orchestrator: pinning %s's Submit to %s: %w", task.ID, shortSHA(headSHA), err)
	}
	if out == nil {
		current, err := store.GetTask(ctx, task.ID)
		if err != nil || current == nil {
			return task, false, err
		}
		return *current, true, nil
	}
	if cleared != "" {
		log.Printf("orchestrator: %s was submitted by a human; its clearance is pinned to %s", ref, shortSHA(headSHA))
	}
	return *out, true, nil
}

// reclassifySubmitted is pinHumanSubmit's answer to a branch that has
// moved from shown, the head the human submitted, to headSHA: that head
// is classified as any moved head is, and this cycle goes on for task
// only if the classification cleared it.
func reclassifySubmitted(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, shown, headSHA string, now time.Time) (model.Task, bool, error) {

	log.Printf("orchestrator: %s was submitted by a human at %s, and its head is now %s -- "+
		"classifying that head rather than pinning the Submit to it", ref, shortSHA(shown), shortSHA(headSHA))
	if err := holdForTrustBoundary(ctx, store, client, task, ref, headSHA, now); err != nil {
		return task, false, err
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		return task, false, err
	}
	rules := repoRulesFor(ctx, store, *task.Target)
	ok := current.AutoMerge && linkTarget(*current, model.LinkTrustBoundaryCleared) == clearedTarget(headSHA, rules)
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

// carryable reports whether a clearance of a head touching matches is one
// carryClearance could ever carry to a later head: none of them is a path
// only a human or a named owner may release.
func carryable(matches []trustboundary.Match) bool {
	for _, m := range matches {
		if m.Category.RequiresHuman || m.Category.OwnerApproval {
			return false
		}
	}
	return true
}
