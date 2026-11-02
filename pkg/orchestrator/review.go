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
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// reviewer is the automation principal every review task is attributed
// to -- its own actor, distinct from "schedule" (a schedule's firing),
// "merge-queue" (automatic PR repair) and "grain" (relayed agent
// output), so a human reading a task's conversation or origin can tell
// which of grain's own mechanisms filed it.
var reviewer = model.Principal{Kind: model.PrincipalAutomation, ID: "review"}

// SyncReviews drives both ends of a review's life: it files the review a
// task declares (Task.ReviewTemplateID) once that task's own work is
// done -- grain/task-284's "when the task has completed a new agent
// should be spun up on the same branch with instructions to review and
// fix bugs in the proposed code" -- and it closes the review once that
// review's own run is over and has left nothing behind to land
// (closeFinishedReviews).
//
// "Completed" is read off the same query the merge queue reads,
// Store.OpenPullRequestLinks: a task whose run is over, whose pull
// request is open, and which has not closed. That is the moment there is
// code to review at all, and it is also the moment before the merge --
// syncEntry holds the pull request back while the review it filed is
// still in flight (reviewPending), so the sequence a task with a review
// attached goes through is work, review, merge, rather than work, merge,
// review-of-something-already-landed.
//
// Level-triggered like every other reconciler here, and for the same
// reason SyncQualifications gives: what to do is a function of what the
// store says right now, so a cycle that fails files the review on the
// next one instead, and a review attached to a task that finished
// yesterday is filed the first cycle after somebody attaches it rather
// than never. Exactly one is ever filed per task -- LinkReviewTask is
// the record of that, checked before filing and written with it -- so a
// review that ran and found nothing is not re-run every cycle for the
// rest of the task's life.
//
// It takes a *model.Store and no github.Client because it needs neither:
// the branch to review is derived (model.BranchName), the template is a
// row, and the task it files is a row. SyncPullRequests is what talks to
// GitHub about the same pull requests.
func SyncReviews(ctx context.Context, store *model.Store, now time.Time) error {
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
		if task == nil || !reviewDue(*task) {
			continue
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: task %s: %w", link.TaskID, err))
			continue
		}
		if err := fileReviewTask(ctx, store, *task, ref, now); err != nil {
			errs = append(errs, err)
		}
	}
	if err := closeFinishedReviews(ctx, store, now); err != nil {
		errs = append(errs, err)
	}
	if err := closeObsoleteReviews(ctx, store, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// closeFinishedReviews closes every review grain filed whose run is over
// and which left nothing behind to land -- the review that found nothing
// to change, and the far commoner one that wrote down what it found and
// pushed no commit.
//
// It is the ending such a task had none of. A review that pushed fixes
// ends when its pull request merges back into the branch under review
// (recordPullRequestEvents sets ClosedAt); a review that pushed nothing
// has no pull request, so syncEntry never sees it, nothing puts it on a
// queue and nothing was ever going to set ClosedAt for it. It completed
// and then stayed completed forever, badged "Queued for merge" about a
// merge that does not exist -- the exact wrong reading StateAwaitingSubmit
// and StateAwaitingAck were each split out of that state to correct -- and
// in no inbox group either, so nothing on the page about what needs a
// person would ever have shown it.
//
// Closed rather than acknowledged, because there is nothing here for
// anybody to acknowledge: model.AwaitsAck's own argument is that the
// findings were delivered on the change under review, that the change is
// what somebody has to answer for, and that a second row per reviewed
// change would be the inbox's largest group and its least actionable. A
// task waiting on nobody, with nothing left to happen to it, is finished;
// 'closed' is the word grain already has for that, and it takes the row
// out of the lists that are about work in play (ui/src/state.js's
// SET_ASIDE_STATES) without hiding anything the review said -- its
// conversation, its findings and its transcript are all still on it.
//
// Level-triggered like the filing above, and for the extra reason that
// finishing the run is not the only way a task gets into this shape: a
// review already sitting in 'completed' from before this existed is
// closed by the first cycle that reads it, which a close written into
// ProcessResult would never have reached.
//
// Only a review grain filed itself (isReviewTask). A task that merely
// declares model.IntentReview has nowhere else its findings could have
// been delivered -- relayReviewFeedback relays them into its own
// conversation -- so what is waiting on it is a reader, not nothing, and
// it reads 'awaiting_ack' and is closed by a person (model.AwaitsAck).
//
// Level-triggered has one consequence worth stating: nothing in the
// record tells a review that has never been closed from one a human
// reopened, so reopening a finished review by hand holds only until the
// next cycle. That costs nothing a human wanted -- a reopened review is
// no more dispatchable than a completed one was, since Observation.
// CompletedAt is what stops both -- and the way to give a change a second
// review is ui.Client.RunNow, which clears CompletedAt and so takes the
// task out of this set for as long as the new run lasts.
//
// Store.States is a row per task, which is the same query a task list
// costs and is read here once per cycle. The GetTask beside it is per
// *completed* task, which is the merge queue's own working set plus
// whatever is waiting for this: bounded by the open pull requests, not by
// the size of the backlog.
func closeFinishedReviews(ctx context.Context, store *model.Store, now time.Time) error {
	states, err := store.States(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading task states: %w", err)
	}
	// Sorted, because States is a map and a cycle that closes two
	// reviews should say so in the same order twice.
	ids := make([]string, 0, len(states))
	for id, state := range states {
		// 'completed' and nothing else. A review still running, parked on
		// a question, or failing has not finished, and one whose pull
		// request is on the queue is in this state legitimately -- the
		// link check below is what tells those two apart, since
		// model.LinkFixes and Observation.CompletedAt are written in the
		// same step of the same finish (finishWithPullRequest) and so a
		// review that is about to have a pull request is never briefly
		// mistaken for one that will never have one.
		if state == model.StateCompleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		task, err := store.GetTask(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading task %s: %w", id, err))
			continue
		}
		if task == nil || !isReviewTask(*task) || hasPullRequest(*task) {
			continue
		}
		if err := closeFinishedReview(ctx, store, *task, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hasPullRequest reports whether a task has opened one -- the same
// fixes-link model.AwaitsSubmit and model.AwaitsAck both read, and the
// only evidence in the store that there is something to land.
func hasPullRequest(task model.Task) bool {
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			return true
		}
	}
	return false
}

// closeFinishedReview closes one, and says on the task why it closed
// itself. The note matters more than it looks: a task that shuts without
// a word is a puzzle for whoever opens it later, and the one thing they
// want to know -- where the findings went, if there were any -- is not
// otherwise on this page.
//
// Written in the order fileReviewTask writes its own pair: the fact
// first, the telling after. A failed note leaves the review closed and
// unexplained, which the log line and the returned error record; the
// other order would post a fresh copy of the note every cycle for as
// long as the close kept failing.
func closeFinishedReview(ctx context.Context, store *model.Store, task model.Task, now time.Time) error {
	if err := closeTask(ctx, store, task.ID, now, "review finished", func(o *model.Observation) {
		o.ClosedAt = &now
	}); err != nil {
		return err
	}
	return reviewComment(ctx, store, task.ID, reviewFinishedNote(task), now)
}

// reviewFinishedNote is what that close says for itself, named apart
// because which of the two sentences it ends with depends on whether the
// task it reviewed is still findable -- and a note that pointed at a task
// grain cannot name would be worse than one that does not point anywhere.
func reviewFinishedNote(task model.Task) string {
	const closing = "Closing it: with no pull request of its own there is nothing here to merge, " +
		"and nothing that would ever have moved it off \"Merging\" on its own."
	for _, l := range task.Links {
		if l.Kind == model.LinkProposedBy {
			return fmt.Sprintf(
				"This review is finished and pushed no change of its own. Whatever it found is "+
					"on task %s and on that task's pull request, which is where somebody reads "+
					"it and what waits for them to. %s", l.Target, closing)
		}
	}
	return "This review is finished and pushed no change of its own. " + closing
}

// reviewDue reports whether task is one SyncReviews still owes a review:
// it has one attached, it has a repo to file the review against, and no
// review has been filed for it yet.
//
// A task with no Target is unreachable here -- it could not have a pull
// request to be read out of OpenPullRequestLinks in the first place --
// and is checked anyway because the review task's own Target is taken
// straight from it.
func reviewDue(task model.Task) bool {
	if task.ReviewTemplateID == "" || task.Target == nil {
		return false
	}
	_, filed := reviewTaskLink(task)
	return !filed
}

// reviewTaskLink returns the ID task's own LinkReviewTask names, if a
// review has already been filed for it -- the counterpart of the
// LinkFixTask read the merge queue kept while it still filed a separate
// repair task, and read the same two ways: SyncReviews checks it so
// exactly one review is ever filed, and syncEntry checks it to know
// whose landing it is waiting on.
func reviewTaskLink(task model.Task) (string, bool) {
	for _, l := range task.Links {
		if l.Kind == model.LinkReviewTask {
			return l.Target, true
		}
	}
	return "", false
}

// isReviewTask reports whether task is one filed by SyncReviews -- a
// review stacked on the branch of the task it reviews, rather than a
// change of its own. syncEntry treats it exactly the way it treats a fix
// task: never a queue entry in its own right, and merged into the branch
// it was filed against the moment it reads clean.
func isReviewTask(task model.Task) bool { return task.Origin.Reason == model.ReasonReview }

// reviewsInPlace reports whether task is a review working on the reviewed
// change's own branch (model.LinkPushesReviewedBranch), and which task
// that is. Such a review never opens a pull request of its own.
func reviewsInPlace(task model.Task) (reviewedID string, ok bool) {
	reviewedID, ok = task.CIOwner()
	if !ok {
		return "", false
	}
	return reviewedID, true
}

// fileReviewTask files one review of task, from the template task names.
//
// The shape is the one the merge queue's own repair task had while it
// was still filed separately (model.LinkFixTask), because the situation
// is the same one: a second agent is wanted on a branch a first agent
// has already pushed, and what it does there has to end up back on that
// branch. So the
// review task's Base is task's own branch (model.BranchName -- derived,
// never self-reported, so this and the checkout agree without either
// asking the other), its AutoMerge is set, and the pull request it opens
// is merged straight back into the branch under review by syncEntry the
// moment it reads clean. Stacking is also what makes "the same branch"
// true in the only sense grain can offer it: a run gets a branch of its
// own (BranchName is a function of the task id), so a review that works
// on task's branch is one whose own branch starts there and returns
// there.
//
// It is filed already approved, by automation, for the reason
// fireTaskSchedule gives about a schedule: attaching a review to a task
// is itself a human's standing instruction to run it, and asking them to
// approve the thing they asked for would leave every review sitting as a
// proposal until somebody noticed. It is filed at the head of the
// backlog for the reason the merge queue's own repairs reach the head of
// it (Store.Ready): the change it reviews is waiting on it, and
// everything behind that change in the repo's merge queue is waiting on
// both.
//
// Three of the template's fields are used and three are deliberately
// not. Title, Body and Grants (plus Reads) are the reusable content --
// the instructions "read this branch and fix what is wrong with it"
// written once and improved once. Target and Base are not the
// template's to give (model.Template's own doc comment on why), and here
// they are decided by the task under review. AutoMerge is not the
// template's either, for a reason that is this function's own: a stacked
// review branch that never merges back into the branch it reviewed is
// work nobody will ever see, so it is set here regardless of what the
// template says about the tasks it files anywhere else.
//
// Origin.Reason is ReasonReview, which is what isReviewTask reads and
// therefore what keeps the review out of its repo's merge queue as an
// entry in its own right. It is deliberately not ReasonFix: that reason
// draws on the capacity model.Limits.Mergers keeps back for the merge
// queue's own repairs, and a review is ordinary work -- it runs an agent
// over a whole change rather than finishing one that is already nearly
// landed.
//
// The task's own body ends with what only grain can supply: which task
// is being reviewed, which branch its code is on, and where its pull
// request is. The template supplies the instructions; this supplies the
// subject.
func fileReviewTask(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, now time.Time) error {

	tmpl, err := store.GetTemplate(ctx, task.ReviewTemplateID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading review template %s for %s: %w",
			task.ReviewTemplateID, task.ID, err)
	}
	if tmpl == nil {
		// A template deleted out from under a task -- ui.Client.
		// DeleteTemplate refuses this while a task still names one, so
		// it takes a row disappearing some other way. Reported every
		// cycle rather than swallowed, and never fatal to the tasks
		// beside it (SyncReviews collects), the same way a schedule
		// whose template has gone fails its own firing.
		return fmt.Errorf("orchestrator: reviewing %s: template %s no longer exists",
			task.ID, task.ReviewTemplateID)
	}

	reviewTask, err := putStackedReview(ctx, store, task, *tmpl,
		reviewTaskTitle(*tmpl, task), reviewTaskBody(*tmpl, task, ref, task.BranchName()), now)
	if err != nil {
		return err
	}
	branch := task.BranchName()

	// Through UpdateTask, for the reason finishWithPullRequest gives: the
	// task was read at the top of this cycle and something else may have
	// written to it since, so the link is appended to whatever is there
	// now rather than to the copy in hand.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewTask, Target: reviewTask.ID})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: linking %s to its review task %s: %w", task.ID, reviewTask.ID, err)
	}

	comment := fmt.Sprintf(
		"Filed task %s to review this one's own code, from the %q template. It works "+
			"from `%s` (the same branch) and its own pull request merges straight back "+
			"into it, so %s waits to merge until the review has landed.",
		reviewTask.ID, tmpl.Name, branch, ref,
	)
	return reviewComment(ctx, store, task.ID, comment, now)
}

// putStackedReview files a review of task from tmpl, stacked on task's own
// branch, with the title and body given -- everything fileReviewTask's own
// doc comment says about the shape of a review task, which the security
// hold's review (securityhold.go) shares. Linking it back onto task, and
// saying so, is the caller's.
func putStackedReview(ctx context.Context, store *model.Store, task model.Task,
	tmpl model.Template, title, body string, now time.Time) (model.Task, error) {
	return putReview(ctx, store, task, tmpl, title, body, false, now)
}

// putReview is putStackedReview, or -- inPlace -- a review that works on
// task's own branch rather than a branch of its own
// (model.LinkPushesReviewedBranch): its Branch is task's, so its checkout
// starts there and the git proxy lets it push there and nowhere else, and
// it never opens a pull request, so what it fixes lands in task's own and
// it never takes a merge queue entry of its own. The security review is
// filed this way (putSecurityReview).
func putReview(ctx context.Context, store *model.Store, task model.Task,
	tmpl model.Template, title, body string, inPlace bool, now time.Time) (model.Task, error) {
	reviewTask, err := newReview(ctx, store, task, tmpl, title, body, inPlace, now)
	if err != nil {
		return model.Task{}, err
	}
	if err := store.PutTask(ctx, reviewTask); err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: filing review task %s: %w", reviewTask.ID, err)
	}
	return reviewTask, nil
}

// newReview is the review task putReview files, built but not yet
// stored, for a caller with more to add to it first (fileCLReview).
func newReview(ctx context.Context, store *model.Store, task model.Task,
	tmpl model.Template, title, body string, inPlace bool, now time.Time) (model.Task, error) {

	// The template's own values for this deployment's declared fields,
	// sifted against what is still declared exactly as a schedule's
	// firing is (model.Store.FiringCustomFields): a review filed from a template
	// that says `team: infra` says it too, and says nothing about a
	// field that has since been retired.
	customFields, err := store.FiringCustomFields(ctx, tmpl.CustomFields)
	if err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: reviewing %s: %w", task.ID, err)
	}

	id, err := store.NewTaskID(ctx)
	if err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: allocating an id for a review of %s: %w", task.ID, err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return model.Task{}, fmt.Errorf("orchestrator: placing a review of %s at the head of the backlog: %w", task.ID, err)
	}

	branch := task.BranchName()
	// The repo the review is filed against -- the reviewed task's own,
	// which a task waiting on a pull request always has. Read as a value
	// only so the template's write targets can be sifted against it
	// below.
	var reviewTarget model.RepoRef
	if task.Target != nil {
		reviewTarget = *task.Target
	}
	grants := tmpl.Grants
	if task.Target != nil {
		usesGrainCI, err := store.RepoUsesGrainCI(ctx, *task.Target)
		if err != nil {
			return model.Task{}, fmt.Errorf("orchestrator: checking CI mode for %s review of %s: %w", *task.Target, task.ID, err)
		}
		if usesGrainCI {
			found := false
			for _, g := range grants {
				if g.Capability == grainci.CapabilityName {
					found = true
					break
				}
			}
			if !found {
				grants = append(grants, model.Grant{Capability: grainci.CapabilityName, Via: model.GrantByDefault})
			}
		} else {
			filtered := make([]model.Grant, 0, len(grants))
			for _, g := range grants {
				if g.Capability != grainci.CapabilityName {
					filtered = append(filtered, g)
				}
			}
			grants = filtered
		}
	}
	// Never a dangerous api grant the reviewed task did not hold itself
	// (model.ReviewGrants): whoever chose the template was checked when
	// they chose it, but the template may have been edited since.
	grants, dropped := model.ReviewGrants(grants, task)
	if len(dropped) > 0 {
		log.Printf("orchestrator: review of %s: dropped %s from template %s, dangerous grants %s does not hold",
			task.ID, strings.Join(dropped, ", "), tmpl.ID, task.ID)
	}
	reviewTask := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Title:  title,
		Body:   body,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: reviewer},
			Reason:      model.ReasonReview,
		},
		Approval: &model.Attribution{Actor: reviewer},
		Target:   task.Target,
		Binding:  model.BindingDirective,
		Base:     branch,
		Reads:    tmpl.Reads,
		// A review template's own extra write targets, minus the repo
		// and base branch this review is filed against (model.WritesBesides):
		// a review task that may propose work in a second repository or
		// another branch is the same declaration as any other template's,
		// and there is no reason for the review path to be the one that drops it.
		Writes:       model.WritesBesides(tmpl.Writes, reviewTarget, branch),
		Grants:       grants,
		CustomFields: customFields,
		// The network the review template says its firings get, like its
		// grants (model.Template.NetworkAccess, grain/task-269): a
		// review that only reads a branch is exactly the shape of job
		// worth filing with none.
		NetworkAccess: tmpl.NetworkAccess,
		// And the model it chose (model.Template.Model), empty for the
		// team's default.
		Model:       tmpl.Model,
		ModelSource: model.ModelSourceOf(tmpl.Model, model.LayerTemplate),
		AutoMerge:   true,
		Links:       []model.Link{{Kind: model.LinkProposedBy, Target: task.ID}},
		CreatedAt:   &now,
		OrderKey:    orderKey,
	}
	if inPlace {
		// The reviewed change's branch, fixed here as any task's Branch is
		// when it is first written, so the review keeps pushing where the
		// change is however the deployment's naming template changes.
		reviewTask.Branch = branch
		reviewTask.Links = append(reviewTask.Links, model.Link{Kind: model.LinkPushesReviewedBranch, Target: task.ID})
	}
	model.InheritRequester(&reviewTask, task)
	return reviewTask, nil
}

// reviewTaskTitle names a review after the template it came from, since
// that is what the person who attached it wrote and what they will
// recognise in a list. A template with no title of its own falls back to
// naming the task under review -- fixTaskTitle's own fallback reasoning,
// for the one thing a template cannot cover.
//
// Reviews do not nest (a review task is filed with no review template of
// its own, and only a task with one is ever reviewed), so this cannot
// compound into "Review: Review: ...".
// A review template's own input fields (model.TemplateField) are filled
// in with their declared defaults, as they are at every other firing
// nobody is watching: a review is filed by the machinery when a task's
// work is done, with no form and nobody to ask, and a raw `{{name}}`
// in the title of the task or the body an agent is handed is worse than
// the empty string a field with no default means.
func reviewTaskTitle(tmpl model.Template, task model.Task) string {
	if title := strings.TrimSpace(model.SubstituteFields(tmpl.Title, tmpl.FieldValues(nil))); title != "" {
		return title
	}
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = task.ID
	}
	return "Review: " + title
}

// reviewTaskBody is the template's instructions with the subject of the
// review appended: the task, the branch its code is on, and its pull
// request.
//
// Appended rather than prepended, and separated by a rule, so the
// template's own opening line is still the first thing the dispatched
// agent reads -- the instructions are what the run is for, and this is
// the context they are carried out against.
func reviewTaskBody(tmpl model.Template, task model.Task, ref model.PullRequestRef, branch string) string {
	var b strings.Builder
	// The template's defaults fill its holes here too -- reviewTaskTitle's
	// own note on why a review has nobody to ask.
	if body := strings.TrimSpace(model.SubstituteFields(tmpl.Body, tmpl.FieldValues(nil))); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n---\n\n")
	}
	fmt.Fprintf(&b,
		"You are reviewing task %s (%q), whose work is on `%s` and proposed in %s.\n\n"+
			"Your own branch is based on `%s`, so what you find you can also fix: commit "+
			"the fixes and push, and your pull request is merged straight back into `%s` "+
			"once it reads clean. Task %s does not merge until that has happened, so this "+
			"is the last look anybody gets at the change before it lands.\n\n"+
			"Fix everything you can: bugs, missing tests, docs that no longer match the "+
			"code, anything with an answer you can stand behind. Commit and push those "+
			"fixes; once they have merged back and grain CI is green the change goes "+
			"through. Never leave a finding you could have fixed.\n\n"+
			"Only what you cannot settle without the change's owner -- a design decision, "+
			"a trade-off, a behaviour change the brief does not settle -- goes in an "+
			"add_review_comment call, written as a question with the answer you "+
			"recommend. Those are posted as a draft review on %s, on the lines they are "+
			"about, and put to the owner on task %s; any such call holds the change for "+
			"their decision, and their reply sends another run to carry it out.",
		task.ID, task.Title, branch, ref, branch, branch, task.ID, ref, task.ID)
	return b.String()
}

// reviewComment records something the review machinery said about a
// task, attributed to it rather than to grain generally -- queueComment's
// counterpart, and its own automation principal for the same reason.
func reviewComment(ctx context.Context, store *model.Store, taskID, body string, now time.Time) error {
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: reviewer},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: commenting on %s: %w", taskID, err)
	}
	return nil
}

// defaultReviewTaskDeadline bounds how long a task's own merge waits on
// the review filed for it, and exists for the reason
// defaultRepairDeadline exists: without it the wait is unbounded, and
// by the same route. A task holding its merge for a review reads
// PrClean, not PrPending, so defaultCheckStallDeadline never times it,
// and the review task's own pull request is never a queue head itself
// (isQueueMember excludes it), so nothing times that either. Anything
// that leaves the review open forever -- its checks wedged, a dispatch
// that fails without closing it, a run that never comes back -- would
// otherwise hold its parent at the head of the repo's queue
// indefinitely, with everything behind it waiting.
//
// Six hours, the same as a repair's, and chosen against the same
// budget: a review waits briefly to be dispatched (it is filed at the
// head of the backlog), runs an agent (capped at DefaultMaxRunRuntime,
// two hours), opens a pull request and gets its own CI through. Erring
// long costs one wait per stuck review; erring short throws away a
// review that may have been minutes from landing.
//
// Measured from the review task's own CreatedAt, which fileReviewTask
// stamps in the same cycle it writes LinkReviewTask, so a restart does
// not hand a stuck review another six hours.
const defaultReviewTaskDeadline = 6 * time.Hour

// reviewState is what syncEntry needs to know about a queue entry's own
// review: whether the change is still waiting on one, and whether it has
// been waiting longer than anyone should.
type reviewState struct {
	// taskID is the review task's own ID, empty both when no review has
	// been filed yet and when the one filed has finished -- pending is
	// what tells those two apart.
	taskID string
	// pending is true while this task's merge is waiting on a review:
	// either one is owed and SyncReviews has not filed it yet, or the
	// one filed has not reached StateClosed. A review closes when its
	// own pull request merges into the branch under review
	// (recordPullRequestEvents), which is exactly the moment the change
	// is worth merging on.
	//
	// The owed case is deliberately part of it. Without it, whether a
	// reviewed change merged before its review was even filed would
	// depend on which of "reviews" and "sync" ran first in a cycle
	// (Reconcilers), and a merge is not a decision worth making on an
	// ordering -- so the declaration alone (Task.ReviewTemplateID) holds
	// the merge, and filing the review is what starts the clock rather
	// than what starts the wait.
	pending bool
	// overdue is true when pending has been true for longer than
	// defaultReviewTaskDeadline.
	overdue bool
}

// reviewStateOf reads what task's own review means for merging it now.
//
// The clock it measures against is whichever start the wait actually
// has: the review task's own CreatedAt once one has been filed, and the
// task's own CompletedAt while one is merely owed -- the moment there
// was something to review, and so the moment a review that never turns
// up started costing something.
//
// A review task the store has no row for counts as finished rather than
// pending: the link names something that can never reach StateClosed, so
// waiting on it is waiting on nothing. That is unreachable today
// (nothing deletes tasks) and errs toward merging rather than toward the
// indefinite hold this whole clock exists to prevent -- the opposite
// direction from fixTaskOverdue's own unreachable case, because what is
// at stake here is a merge that would otherwise never happen rather than
// a person who would otherwise never be asked.
func reviewStateOf(ctx context.Context, store *model.Store, task model.Task,
	obs *model.Observation, now time.Time) (reviewState, error) {

	// A takeover's own reviews come first: they gate the merge until they
	// have passed at the head (takeover.go), whatever else the task has.
	if held, reviewID := takeoverMergeHold(task); held {
		return reviewState{taskID: reviewID, pending: true}, nil
	}

	reviewTaskID, filed := reviewTaskLink(task)
	if !filed {
		if task.ReviewTemplateID == "" {
			return reviewState{}, nil
		}
		s := reviewState{pending: true}
		if obs != nil && obs.CompletedAt != nil {
			s.overdue = overdue(*obs.CompletedAt, now)
		}
		return s, nil
	}

	reviewTask, err := store.GetTask(ctx, reviewTaskID)
	if err != nil {
		return reviewState{}, fmt.Errorf("orchestrator: reading review task %s: %w", reviewTaskID, err)
	}
	if reviewTask == nil {
		return reviewState{}, nil
	}
	state, err := store.State(ctx, reviewTaskID)
	if err != nil {
		return reviewState{}, fmt.Errorf("orchestrator: reading review task %s's state: %w", reviewTaskID, err)
	}
	if state == model.StateClosed || reviewFinishedEmptyHanded(*reviewTask, state) {
		return reviewState{}, nil
	}
	s := reviewState{taskID: reviewTaskID, pending: true}
	if reviewTask.CreatedAt != nil {
		s.overdue = overdue(*reviewTask.CreatedAt, now)
	}
	return s, nil
}

// reviewFinishedEmptyHanded reports whether a review task's run is over
// and left no pull request of its own -- which is the second way a review
// can be finished, and the one the wait above used to have no answer for.
//
// A review that pushed fixes is waited on until its pull request merges
// back into the branch under review, which closes it (StateClosed, via
// recordPullRequestEvents). A review that pushed nothing never reaches
// that state: it completes, with no pull request to merge and so nothing
// left that could ever close it. Reading that as "still pending" held the
// change under review for the whole of defaultReviewTaskDeadline and then
// announced that its review had never finished -- for a review that had
// finished, on time, having found nothing to change or having written
// down what it found instead (relayReviewFeedback).
//
// Still read here rather than left to closeFinishedReviews, which closes
// exactly this task and would make it StateClosed above. The two are not
// the same answer at the same moment: reviews and the merge queue are
// separate reconcilers, so within the cycle that closes the review -- and
// within every cycle between the run finishing and that one -- the review
// still reads 'completed', and a change whose review is over should not
// wait on the order the reconcilers happen to run in.
//
// Completed and link-less together, rather than either alone.
// model.LinkFixes and Observation.CompletedAt are written in the same
// step of the same finish (finishWithPullRequest), so a review that is
// about to have a pull request is never briefly mistaken for one that
// will never have one; and a review that is still running, parked on a
// question, or failing is not finished at all and is still worth waiting
// for.
func reviewFinishedEmptyHanded(reviewTask model.Task, state model.State) bool {
	// model.AwaitsAck excludes a review with ReasonReview from
	// 'awaiting_reply' -- its findings are read here and on the change
	// under review, not off an inbox row of its own -- so a review task
	// fileReviewTask wrote reads 'completed'. A review parked on a
	// question reads 'awaiting_reply' and is not finished yet.
	if state != model.StateCompleted {
		return false
	}
	return !hasPullRequest(reviewTask)
}

// overdue reports whether a wait that started at from has run past
// defaultReviewTaskDeadline. Written as a comparison against the
// deadline rather than as `now.Sub(from) >= deadline`, so a clock that
// jumped backwards reads as "not overdue yet" -- the same direction
// fixTaskOverdue and the CI clocks err in.
func overdue(from, now time.Time) bool {
	return !now.Before(from.Add(defaultReviewTaskDeadline))
}

// escalateUnfinishedReview gives up on waiting for task's review, which
// has been neither finished nor abandoned for longer than
// defaultReviewTaskDeadline.
//
// blockMergeQueue, the same exit escalateUnfinishedFix takes, and with
// the same consequences: task stops being anyone's queue head, so the
// tasks behind it move, and it still merges the moment it reads clean --
// which, since its own checks are what were clean all along, is very
// likely the next cycle. That is the deliberate choice between the two
// ways to be wrong here: merging a change whose review never arrived,
// having said so on the task, is recoverable, where holding a finished
// change out of the repo forever on a review that is not coming is not.
//
// The review task itself is left exactly as it is, for the reason
// escalateUnfinishedFix leaves a fix task alone: nothing here can tell
// which stage it is stuck in, cancelling it would throw work away, and
// if it does finish it still merges into the branch it reviewed
// unconditionally.
func escalateUnfinishedReview(ctx context.Context, store *model.Store,
	task model.Task, ref model.PullRequestRef, reviewTaskID string, now time.Time) error {

	var comment string
	if reviewTaskID == "" {
		// The other way the wait can run out: no review task was ever
		// filed at all, so SyncReviews has been failing for this task for
		// the whole deadline -- a template deleted out from under it is
		// the one way that happens on purpose.
		comment = fmt.Sprintf(
			"%s has been waiting more than %s for the review attached to it, and no "+
				"review task was ever filed -- the template it names (`%s`) may be gone. "+
				"The merge queue has stopped waiting and moved on to the next task in "+
				"%s; %s merges as soon as it reads clean, reviewed or not. Check the "+
				"daemon's log for what filing the review failed on.",
			ref, humanDuration(defaultReviewTaskDeadline), task.ReviewTemplateID, ref.Repo, ref,
		)
	} else {
		comment = fmt.Sprintf(
			"The review filed for %s never finished -- task %s was filed to review it more "+
				"than %s ago and still hasn't run to completion -- so the merge queue has "+
				"stopped waiting on it and moved on to the next task in %s. Have a look at "+
				"%s (its run, or its own checks, may be stuck); %s itself merges as soon as "+
				"it reads clean, reviewed or not, so close or finish the review depending on "+
				"which you want.",
			ref, reviewTaskID, humanDuration(defaultReviewTaskDeadline), ref.Repo,
			reviewTaskID, ref,
		)
	}
	reason := fmt.Sprintf("gave up waiting on the review it declared after %s",
		humanDuration(defaultReviewTaskDeadline))
	return blockMergeQueue(ctx, store, task.ID, comment, reason, now)
}
