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
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// The security hold's two endings that hand a change to a person -- a
// review leaving findings on it, and a path only a human may release --
// go through the same human-review hold a reviewer's request_human_review
// raises (model.LinkHumanReviewHold): one record, one inbox entry with the
// reason, one banner, timeline event and notification, and one way out,
// a person's Submit or an audited release.
//
// What each hold already meant is kept beside it: LinkHumanOnlyHold still
// marks the path (and still goes when a later head no longer touches it,
// taking its reason along -- model.normalizeHumanReviewHold), and a clean
// security review still releases LinkSecurityHold, which is the earlier,
// review-releasable hold.

// raiseHumanOnlyReview records why a change touching a path only a human
// may release waits for one.
func raiseHumanOnlyReview(ctx context.Context, store *model.Store, taskID string,
	matches []trustboundary.Match, known bool, now time.Time) {

	if _, err := store.RaiseHumanReviewHold(ctx, taskID, model.HumanReviewRequest{
		Reason: model.HumanReviewReason{Source: model.HumanReviewHumanOnly, Category: model.HumanReviewSecurity,
			Reason: humanOnlyReason(matches, known), At: now},
		By: model.GrainPrincipal,
	}); err != nil {
		log.Printf("orchestrator: recording %s's human-only hold as a human-review hold: %v", taskID, err)
	}
}

// humanOnlyReason is the one line a human-only hold is shown under.
func humanOnlyReason(matches []trustboundary.Match, known bool) string {
	if !known {
		return "its file list could not be read in full, so it is held as if it touched a path that requires a human"
	}
	var ids []string
	for _, m := range matches {
		if m.Category.RequiresHuman {
			ids = append(ids, m.Category.ID)
		}
	}
	if len(ids) == 0 {
		return "touches a trust-boundary path that requires a human"
	}
	return "touches " + strings.Join(ids, ", ") + " path that requires a human"
}

// raiseFindingsReview records why a change whose security hold a review's
// findings ended waits for a person.
func raiseFindingsReview(ctx context.Context, store *model.Store, review model.Task, reviewedID, summary string,
	now time.Time) {

	reason := "review " + review.ID + " left findings"
	if s := strings.TrimSpace(summary); s != "" {
		reason += ": " + s
	}
	if _, err := store.RaiseHumanReviewHold(ctx, reviewedID, model.HumanReviewRequest{
		Reason: model.HumanReviewReason{Source: model.HumanReviewSecurityFindings, Category: model.HumanReviewSecurity,
			Reason: reason, ReviewTask: review.ID, Run: store.ActiveRunID(ctx, review.ID), At: now},
		By: model.Principal{Kind: model.PrincipalAgent, ID: review.ID},
	}); err != nil {
		log.Printf("orchestrator: recording %s's findings on %s as a human-review hold: %v", review.ID, reviewedID, err)
	}
}

// findingsSummary is a review's findings on one short line: their count
// and the first one's location and first sentence
// (model.FormatFindingsSummary).
func findingsSummary(findings []reviewFinding) string {
	items := make([]model.FindingSummaryItem, 0, len(findings))
	for _, f := range findings {
		items = append(items, model.FindingSummaryItem{Path: f.Path, Line: f.Line, Body: f.Body})
	}
	return model.FormatFindingsSummary(items)
}

// findingsAwaitReason matches the mark dropSecurityHoldOnFindings leaves
// for a person (model.AwaitPersonForAutoMerge's reason).
var findingsAwaitReason = regexp.MustCompile(`^review (\S+) left findings$`)

// SyncHumanReviewHolds carries every change held for a person the old way
// onto the human-review hold: a human-only hold with no reason recorded,
// and a security-held change a review's findings left waiting for a
// person. Level-triggered and idempotent, so it is both the migration of
// what was held before the human-review hold existed and a backstop for
// any path that sets those marks without recording a reason.
func SyncHumanReviewHolds(ctx context.Context, store *model.Store, now time.Time) error {
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading open pull request links: %w", err)
	}
	seen := map[string]bool{}
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[link.TaskID] {
			continue
		}
		seen[link.TaskID] = true
		task, err := store.GetTask(ctx, link.TaskID)
		if err != nil || task == nil {
			continue
		}
		backfillFindingsSummaries(ctx, store, *task)
		var sources = map[model.HumanReviewSource]bool{}
		for _, r := range model.HumanReviewReasons(*task) {
			sources[r.Source] = true
		}
		if linkTarget(*task, model.LinkHumanOnlyHold) != "" && !sources[model.HumanReviewHumanOnly] {
			raiseHumanOnlyReview(ctx, store, task.ID, nil, true, now)
		}
		if sources[model.HumanReviewSecurityFindings] || hasSecurityHold(*task) ||
			linkTarget(*task, model.LinkSecurityReviewTask) == "" {
			continue
		}
		m := findingsAwaitReason.FindStringSubmatch(linkTarget(*task, model.LinkAutoMergeAwaitsPerson))
		if m == nil {
			continue
		}
		// A person accepted that review's security findings: the mark
		// stays while other findings still hold the change, and must not
		// bring back the hold the acceptance cleared. A re-run of the
		// review that finds more records them again, and is held again.
		if model.ReviewFindingsAccepted(*task, model.GateSecurityReview, m[1]) &&
			!slices.ContainsFunc(model.ReviewFindingsForGate(*task, model.GateSecurityReview),
				func(r model.ReviewFindingsRecord) bool { return r.ReviewTask == m[1] }) {
			continue
		}
		raiseFindingsReview(ctx, store, model.Task{ID: m[1]}, task.ID, "", now)
	}
	return nil
}

// backfillFindingsSummaries rewrites task's review-findings records that
// name a review but none of what it found ("review 5381 left findings",
// which path-trigger reviews recorded until their findings were passed
// along) with what that review did find: its recorded findings, or
// failing those its closing note. A record nothing better can be found
// for is left as it is, and read again next cycle.
func backfillFindingsSummaries(ctx context.Context, store *model.Store, task model.Task) {
	done := map[string]bool{}
	for _, r := range model.ReviewFindings(task) {
		reviewID := model.BareFindingsReviewTask(r)
		if !model.IsBareFindingsSummary(r.Summary) || reviewID == "" || done[reviewID] {
			continue
		}
		done[reviewID] = true
		summary := reviewFindingsSummaryOf(ctx, store, reviewID)
		if summary == "" {
			continue
		}
		if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			model.ReplaceReviewFindingsSummary(t, reviewID, summary)
			return nil
		}); err != nil {
			log.Printf("orchestrator: backfilling %s's findings summary from review %s: %v", task.ID, reviewID, err)
		}
	}
}

// reviewFindingsSummaryOf is review reviewID's findings on one line
// (findingsSummary), from the findings it recorded posting, or failing
// those from its closing note. Empty when it has neither.
func reviewFindingsSummaryOf(ctx context.Context, store *model.Store, reviewID string) string {
	if review, err := store.GetTask(ctx, reviewID); err == nil && review != nil {
		stored := parseStoredReviewFindings(linkTarget(*review, linkReviewPostedFindings))
		items := make([]model.FindingSummaryItem, 0, len(stored))
		for _, f := range stored {
			items = append(items, model.FindingSummaryItem{Path: f.Path, Line: f.Line, Body: f.Body})
		}
		if s := model.FormatFindingsSummary(items); s != "" {
			return s
		}
	}
	note, err := store.LatestClosingNote(ctx, reviewID)
	if err != nil {
		return ""
	}
	return model.SummaryFromClosingNote(reviewID, note)
}
