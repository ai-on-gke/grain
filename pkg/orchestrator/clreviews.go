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
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SyncCLReviews is SyncReviews for a task whose change is a CL
// (changelists.md §5, G3-5). A pull request's review is stacked on its
// branch and fixes what it finds; a CL's is read-only. It is filed holding
// the grant the provider's ReviewGrant gives (read-only, naming the
// reviewed task and the snapshot), and records what it finds with
// add_review_comment, which grain saves on the CL as unpublished drafts
// (relayCLReviewFeedback) and puts to the owner as questions. One that
// finishes having found nothing clears its snapshot
// (model.LinkCLReviewCleared, clearCLReview), which is what the provider's
// GateSubmittable reads.
//
// The reviews required are the provider's to say where it implements
// change.ReviewGate (ReviewsMissing), and otherwise the one the task
// names (Task.ReviewTemplateID). Each is filed once per snapshot
// (model.LinkCLReviewFiled): a new snapshot of the CL files them again,
// and clears nothing an older one's review cleared.
//
// Only a finished task waiting on its Submit (or finished outright) is
// reviewed: a running one is still changing its CL. The snapshot is the
// one the provider's poll last observed (change.LastObserved), so a CL
// not yet polled is reviewed a poll later rather than at a guess.
func SyncCLReviews(ctx context.Context, store *model.Store, now time.Time) error {
	p, ok := change.Lookup(model.LinkChangelist)
	if !ok {
		return nil
	}
	filer, ok := p.(change.ReviewFiler)
	if !ok {
		return nil
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing tasks for CL reviews: %w", err)
	}
	var deployment string
	var errs []error
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		target := change.Primary(task)
		if target == "" || isReviewTask(task) {
			continue
		}
		obs, ok := change.LastObserved(target)
		if !ok || obs.Snapshot <= 0 || obs.Status == change.StatusSubmitted || obs.Status == change.StatusAbandoned {
			continue
		}
		state, err := store.State(ctx, task.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's state: %w", task.ID, err))
			continue
		}
		if state != model.StateAwaitingSubmit && state != model.StateCompleted {
			continue
		}
		templates, err := clReviewsRequired(ctx, p, task, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading the reviews %s's %s needs: %w", task.ID, target, err))
			continue
		}
		for _, tmplID := range templates {
			key := model.CLReviewTarget(target, obs.Snapshot, tmplID)
			if clReviewLinked(task, key) {
				continue
			}
			if deployment == "" {
				if deployment, err = store.DeploymentID(ctx); err != nil {
					return errors.Join(append(errs, fmt.Errorf("orchestrator: reading the deployment id: %w", err))...)
				}
			}
			if err := fileCLReview(ctx, store, filer, deployment, task, target, obs.Snapshot, tmplID, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// clReviewsRequired is the review templates task's CL still needs on its
// current snapshot: the provider's ReviewsMissing where it gates reviews,
// and otherwise the task's own review template.
func clReviewsRequired(ctx context.Context, p change.Provider, task model.Task, target string) ([]string, error) {
	if g, ok := p.(change.ReviewGate); ok {
		return g.ReviewsMissing(ctx, task, target)
	}
	if task.ReviewTemplateID == "" {
		return nil, nil
	}
	return []string{task.ReviewTemplateID}, nil
}

// clReviewLinked reports whether key's review has been filed for, or has
// cleared, task's CL.
func clReviewLinked(task model.Task, key string) bool {
	return slices.ContainsFunc(task.Links, func(l model.Link) bool {
		return l.Target == key && (l.Kind == model.LinkCLReviewFiled || l.Kind == model.LinkCLReviewCleared)
	})
}

// fileCLReview files one review of snapshot of task's CL target from
// template tmplID: the review putReview files of a pull request, with the
// provider's read-only grant, grain's record of having filed it
// (model.LinkCLReviewOf) and no branch to stack on.
func fileCLReview(ctx context.Context, store *model.Store, filer change.ReviewFiler, deployment string,
	task model.Task, target string, snapshot int, tmplID string, now time.Time) error {

	tmpl, err := store.GetTemplate(ctx, tmplID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading review template %s for %s: %w", tmplID, task.ID, err)
	}
	if tmpl == nil {
		return fmt.Errorf("orchestrator: reviewing %s: template %s no longer exists", task.ID, tmplID)
	}
	grant, err := filer.ReviewGrant(deployment, task, snapshot)
	if err != nil {
		return fmt.Errorf("orchestrator: the review grant for %s snapshot %d of %s: %w", target, snapshot, task.ID, err)
	}
	if !model.GrantNamesReviewOf(grant) {
		return fmt.Errorf("orchestrator: the review grant for %s snapshot %d of %s names no %s: %q",
			target, snapshot, task.ID, model.ReviewOfArg, grant)
	}
	review, err := newReview(ctx, store, task, *tmpl, reviewTaskTitle(*tmpl, task),
		clReviewTaskBody(*tmpl, task, target, snapshot), false, now)
	if err != nil {
		return err
	}
	key := model.CLReviewTarget(target, snapshot, tmplID)
	// A CL has no branch for the review to stack on, and a read-only
	// review pushes nothing anywhere to merge back.
	review.Base = ""
	review.AutoMerge = false
	review.Grants = append(review.Grants, model.Grant{Capability: grant, Via: model.GrantByGrain})
	review.Links = append(review.Links, model.Link{Kind: model.LinkCLReviewOf, Target: key})
	if err := store.PutTask(ctx, review); err != nil {
		return fmt.Errorf("orchestrator: filing review task %s: %w", review.ID, err)
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links,
			model.Link{Kind: model.LinkReviewTask, Target: review.ID},
			model.Link{Kind: model.LinkCLReviewFiled, Target: key})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: linking %s to its review task %s: %w", task.ID, review.ID, err)
	}
	return reviewComment(ctx, store, task.ID, fmt.Sprintf(
		"Filed task %s to review snapshot %d of %s, from the %q template. It reads the CL "+
			"and changes nothing; what it finds is saved on the CL as unpublished drafts "+
			"and put to you here. Submit waits until it has passed.",
		review.ID, snapshot, target, tmpl.Name), now)
}

// clReviewTaskBody is reviewTaskBody for a CL: the template's
// instructions, then the subject, which the review reads and cannot
// change.
func clReviewTaskBody(tmpl model.Template, task model.Task, target string, snapshot int) string {
	var b strings.Builder
	if body := strings.TrimSpace(model.SubstituteFields(tmpl.Body, tmpl.FieldValues(nil))); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n---\n\n")
	}
	fmt.Fprintf(&b,
		"You are reviewing task %s (%q), whose change is %s, at snapshot %d.\n\n"+
			"Your access to it is read-only: you cannot change the CL, so record every "+
			"finding with an add_review_comment call -- on the line it is about where it has "+
			"one -- written as a question with the answer you recommend. grain saves them on "+
			"%s as unpublished drafts and puts them to task %s's owner, whose reply sends "+
			"another run to carry out their decision. A review that records nothing passes, "+
			"and its pass is what lets snapshot %d be submitted.",
		task.ID, task.Title, target, snapshot, target, task.ID, snapshot)
	return b.String()
}

// clReviewOf is the CL review task is, as fileCLReview recorded it, and
// the task it reviews; ok is false for any other task. Both halves of the
// record are required -- the review reason and grain's own link -- so a
// task that merely holds a review grant is no review grain filed.
func clReviewOf(task model.Task) (key, reviewedID string, ok bool) {
	if !isReviewTask(task) {
		return "", "", false
	}
	key = linkTarget(task, model.LinkCLReviewOf)
	reviewedID = linkTarget(task, model.LinkProposedBy)
	return key, reviewedID, key != "" && reviewedID != ""
}

// relayCLReviewFeedback relays a CL review's findings: saved on the CL as
// unpublished drafts (change.ReviewFiler's SaveReviewFindings -- never
// published, which is a person's to do) and put to the reviewed task's
// owner as open questions, exactly as a pull request review's are
// (noteFindingsOnReviewedTask). handled is false for any task that is not
// a CL review grain filed.
func relayCLReviewFeedback(ctx context.Context, store *model.Store, task model.Task,
	findings []reviewFinding, wantedAutoMerge bool, now time.Time) (handled bool, n int, err error) {

	key, reviewedID, ok := clReviewOf(task)
	if !ok {
		return false, 0, nil
	}
	reviewed, err := store.GetTask(ctx, reviewedID)
	if err != nil {
		return true, 0, fmt.Errorf("orchestrator: reading the task %s reviews: %w", task.ID, err)
	}
	if reviewed == nil {
		return true, 0, relayFindingsToConversation(ctx, store, task, findings, noPullRequestPreamble(task), now)
	}
	target, snapshot, _, _ := model.ParseCLReviewTarget(key)

	saved := "Saved"
	if err := saveCLReviewFindings(ctx, *reviewed, target, findings); err != nil {
		log.Printf("orchestrator: saving %s's %d review finding(s) on %s: %v", task.ID, len(findings), target, err)
		saved = fmt.Sprintf("Could not save these as drafts on %s (%v); recorded", target, err)
	}
	if err := reviewComment(ctx, store, task.ID, fmt.Sprintf(
		"%s %s of snapshot %d as unpublished drafts on %s, and put them to task %s:\n\n%s",
		saved, countOf(len(findings), "finding"), snapshot, target, reviewed.ID, findingsList(findings)), now); err != nil {
		return true, 0, err
	}
	return true, len(findings), noteFindingsOnReviewedTask(ctx, store, task, *reviewed, target,
		"unpublished drafts", findings, wantedAutoMerge, 0, now)
}

// saveCLReviewFindings hands findings to the provider as drafts on target.
func saveCLReviewFindings(ctx context.Context, reviewed model.Task, target string, findings []reviewFinding) error {
	p, ok := change.Lookup(model.LinkChangelist)
	if !ok {
		return fmt.Errorf("no provider for %s links", model.LinkChangelist)
	}
	filer, ok := p.(change.ReviewFiler)
	if !ok {
		return fmt.Errorf("the %s provider saves no review findings", model.LinkChangelist)
	}
	out := make([]change.ReviewFinding, 0, len(findings))
	for _, f := range findings {
		cf := change.ReviewFinding{Body: f.Body}
		if f.Located {
			cf.Path, cf.Line = f.Path, f.Line
		}
		out = append(out, cf)
	}
	return filer.SaveReviewFindings(ctx, reviewed, target, out)
}

// clearCLReview records that a CL review grain filed passed: a run that
// finished without parking, suspending or recording a finding clears the
// snapshot it reviewed for its template (model.LinkCLReviewCleared) on
// the reviewed task. Only that snapshot: one the CL has since moved past
// is cleared all the same, and clears nothing about the new one.
func clearCLReview(ctx context.Context, store *model.Store, task model.Task, findings int, now time.Time) error {
	key, reviewedID, ok := clReviewOf(task)
	if !ok || findings > 0 {
		return nil
	}
	added := false
	if err := store.UpdateTask(ctx, reviewedID, func(t *model.Task) error {
		cleared := model.Link{Kind: model.LinkCLReviewCleared, Target: key}
		if !slices.Contains(t.Links, cleared) {
			t.Links = append(t.Links, cleared)
			added = true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: recording %s's passed review %s: %w", reviewedID, task.ID, err)
	}
	if !added {
		return nil
	}
	target, snapshot, _, _ := model.ParseCLReviewTarget(key)
	return reviewComment(ctx, store, reviewedID, fmt.Sprintf(
		"The review of snapshot %d of %s (task %s) passed with nothing to raise.", snapshot, target, task.ID), now)
}
