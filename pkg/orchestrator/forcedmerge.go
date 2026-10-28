package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// The operator's override, from the stamp a person leaves on a task to
// the merge grain performs and the record it leaves behind
// (grain/task-479). The rule itself is in mergepredicate.go and is not
// re-derived here: this file is the machinery around the one exception
// that file already sanctions.
//
// The override existed before any of this did. It was a person merging
// the pull request underneath grain by hand -- half a dozen in a day, on
// the deployment this was written for -- and every one of those left
// grain's own state stale, because grain did not do the merge and did not
// know it had happened: the task sat on the queue, the row went on saying
// what it was waiting for, the post-merge dispatch and the base-branch
// run never fired. So the question was never whether there is an
// override. It was whether it is grain's.
//
// It is an operator's action and nothing else's. The only thing that
// writes Observation.ForceMergeAt is ui.Client.ForceMerge, behind
// POST /api/tasks/{id}/force-merge; there is no MCP tool for it, no task
// field that asks for one, and nothing a running agent can do to force
// its own change in. That is stated here because it is the kind of
// property that decays silently: the next person to add a tool has to
// read this and decide to break it rather than not notice it.
//
// A forced merge is still refused where GitHub would refuse it. mayMerge
// turns a conflicted pull request down however loudly it is asked,
// because a merge GitHub will not perform is not something an override
// can authorise, and the stamp simply stays outstanding until the
// branch merges cleanly.
//
// And it is for one commit. The stamp records the head the operator
// reviewed, and a branch that has moved since -- any push, a conflict
// fix included -- has its override withdrawn rather than performed
// (forcedMerge.movedSince, bwsalmon/grain#2399): what an operator vouched
// for is a commit, not whatever the branch points at a cycle later.

// forcedMergeOf is the operator's outstanding override as the predicate
// takes it, or nil where there is none -- the one place a stored request
// becomes mergeRequest.forced.
//
// A request with no reason is not a force merge. ui.Client.ForceMerge
// refuses an empty one at the door, so this is belt and braces: an
// override whose reason was lost is one nobody can account for
// afterwards, and the safe reading of that is that it was never asked
// for.
func forcedMergeOf(obs *model.Observation) *forcedMerge {
	if obs == nil || obs.ForceMergeAt == nil {
		return nil
	}
	reason := strings.TrimSpace(obs.ForceMergeReason)
	if reason == "" {
		return nil
	}
	return &forcedMerge{reason: reason, by: obs.ForceMergeBy, skipped: obs.ForceMergeSkipped,
		headSHA: strings.TrimSpace(obs.ForceMergeHeadSHA)}
}

// clearForceMerge spends or withdraws an override: every field of the
// stamp, so that nothing of one request is left to be read as part of
// the next.
func clearForceMerge(o *model.Observation) {
	o.ForceMergeAt = nil
	o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeSkipped = "", "", ""
	o.ForceMergeHeadSHA = ""
}

// dropMovedForcedMerge withdraws an override whose branch has moved since
// it was given (forcedMerge.movedSince) and says so on the task.
//
// Withdrawn rather than left outstanding: the commit it was for is not
// coming back, so an override left in place would only sit there until
// somebody pushed that exact commit again -- and a stamp that can never
// act reads, on the task, like one that is about to. The note is what
// tells the operator their request did not land and why; forcing again
// once they have looked at the new head is one click.
//
// The comment goes after the write, the way every note in this package
// does: a note in front of a write that then failed says something
// untrue.
func dropMovedForcedMerge(ctx context.Context, store *model.Store, taskID string,
	f forcedMerge, head string, now time.Time) error {

	if err := store.ObserveField(ctx, taskID, now, clearForceMerge); err != nil {
		return fmt.Errorf("orchestrator: withdrawing the force merge on %s: %w", taskID, err)
	}
	by := f.by
	if by == "" {
		by = forcedMergeBy
	}
	body := fmt.Sprintf("Did not force the merge %s asked for: %s.", by, forcedMergeMovedWhy(f, head))
	body += "\n\nThe override was for the commit that was reviewed and nothing pushed after it, " +
		"so grain has withdrawn it. The merge queue treats this change as it would any other " +
		"until somebody forces it again."
	return queueComment(ctx, store, taskID, body, now)
}

// forcedMergeBy is who to name in the record when the request carried no
// actor -- which should not happen, since the request is made through a
// client that has one, and is worth saying rather than leaving a blank
// where a person's name belongs.
const forcedMergeBy = "an operator"

// recordForcedMerge is everything that happens *because* a merge was
// forced, run once GitHub has actually performed it.
//
// Four things, in this order, because that is the order in which losing
// one costs least:
//
//  1. The history row (model.ForceMergeRecord), which is what makes "it
//     should be rare" checkable. Written first: it is the only one of
//     these a later reader cannot reconstruct from anywhere else.
//  2. The stamp cleared, so an override is worth exactly one merge.
//  3. The note on the task, where whoever reads the task afterwards --
//     agents included, since the thread is rendered into their prompts --
//     finds out that this landed on somebody's say-so rather than on a
//     verdict.
//  4. The same note on the pull request, which is where a reader who
//     never opens grain is looking.
//
// Errors are joined rather than returned at the first one: the merge has
// already happened, so there is no undoing it by giving up half way, and
// the most useful thing is to get as much of the record written as can
// be.
func recordForcedMerge(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, obs *model.Observation, ref model.PullRequestRef,
	commit, branch string, f forcedMerge, now time.Time) error {

	by := f.by
	if by == "" {
		by = forcedMergeBy
	}
	requestedAt := now
	if obs != nil && obs.ForceMergeAt != nil {
		requestedAt = *obs.ForceMergeAt
	}
	var errs []error
	if err := store.RecordForceMerge(ctx, model.ForceMergeRecord{
		TaskID: task.ID, Repo: ref.Repo, Number: ref.Number, Branch: branch,
		Commit: commit, Reason: f.reason, By: by, Skipped: f.skipped,
		RequestedAt: requestedAt, MergedAt: now,
	}); err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: recording the forced merge of %s: %w", ref, err))
	}
	if err := store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		clearForceMerge(o)
	}); err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: clearing the force merge stamp on %s: %w", task.ID, err))
	}
	body := forcedMergeNote(ref, branch, commit, by, f, forcedMergesLately(ctx, store, now))
	errs = append(errs, queueComment(ctx, store, task.ID, body, now))
	if _, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, body); err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: noting the forced merge on %s: %w", ref, err))
	}
	return errors.Join(errs...)
}

// forcedMergeWindow is the window the count in every forced merge's own
// note is taken over. A week, because that is the unit the rarity was
// claimed in ("I hand approved a half dozen jobs in the last day") and
// because a count over a window a reader can hold in their head is the
// only kind anybody acts on.
const forcedMergeWindow = 7 * 24 * time.Hour

// forcedMergesLately is how many merges this deployment has forced in the
// last forcedMergeWindow, the one just performed included.
//
// Best-effort: a count that could not be read reports 0 and the note
// leaves the sentence out rather than the whole note going unwritten. It
// is a measurement beside a record, and the record is the part that has
// to survive.
func forcedMergesLately(ctx context.Context, store *model.Store, now time.Time) int {
	records, err := store.ForceMergeRecords(ctx)
	if err != nil {
		return 0
	}
	return model.CountForceMergesSince(records, now.Add(-forcedMergeWindow))
}

// forcedMergeNote is what a forced merge says, in one wording said twice
// -- once on the task and once on the pull request -- because the two
// audiences want exactly the same four facts: that this was forced, by
// whom, why, and what was not green at the time.
//
// The count is the fifth, and it is here rather than on a dashboard for
// the reason the whole record is: the moment somebody is reading about
// one forced merge is the moment "and there have been six others this
// week" changes what they do about it.
func forcedMergeNote(ref model.PullRequestRef, branch, commit, by string,
	f forcedMerge, lately int) string {

	body := fmt.Sprintf("Force merged by %s. grain performed this merge itself, on an "+
		"operator's explicit override of the merge queue's own decision -- %s into `%s` at %s.",
		by, ref, branch, shortSHA(commit))
	body += "\n\n**Reason given:** " + f.reason
	if skipped := strings.TrimSpace(f.skipped); skipped != "" {
		body += "\n\n**What it skipped:** " + skipped
	}
	body += "\n\nThe override does not make the change green and nothing here says it was: " +
		"what it authorised is a merge. grain did the merge rather than watching somebody do " +
		"it underneath it, so its own state is consistent -- this task is merged, its queue " +
		"row is closed, and `" + branch + "` is tested after it exactly as it is after any " +
		"other merge."
	if lately > 0 {
		body += fmt.Sprintf(" This is the %s merge grain has forced in the last %d days; forcing "+
			"is meant to be rare, and that count is how anybody checks.",
			ordinal(lately), int(forcedMergeWindow/(24*time.Hour)))
	}
	return body
}

// ordinal is "1st", "2nd", "3rd", "4th" -- English's own rule, teens
// included, for the one sentence in this file that counts something.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
