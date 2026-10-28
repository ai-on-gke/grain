package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// PullRequestStatus is one task's pull request as OpenPullRequestForTask
// found or left it: the request itself, plus whatever CI has reported on
// its head so far -- the half a run asking for its own pull request early
// actually wants, since the whole reason to open one before the run ends
// is to see what the repo's own checks make of the branch.
type PullRequestStatus struct {
	PullRequest github.PullRequest
	// Checks is every check run (or Actions workflow run, on a
	// deployment whose credential can only read those -- checkRunsFor's
	// own fallback) GitHub reports against the pull request's head.
	// Empty is a real answer: CI that has not started yet reports
	// nothing, and a repo with no CI at all never will.
	Checks []github.CheckRun
	// ChecksKnown is false when this deployment's GitHub credential can
	// read neither check runs nor workflow runs (ChecksUnavailable's own
	// condition). It is what tells "nothing has reported yet" apart from
	// "nothing here can ever see what reported", which are the same empty
	// list otherwise.
	ChecksKnown bool
	// ChecksError carries a failure to read checks without failing the
	// call that opened the pull request: the pull request is open either
	// way, and its number is the thing a caller cannot get back by
	// retrying a failed call. Empty when the read succeeded.
	ChecksError string
	// Note is what the already-landed check (landed.go) found about a
	// pull request opened for a branch part of which was already on its
	// base -- which part -- and empty otherwise.
	Note string
}

// OpenPullRequestForTask opens (or finds) the pull request for task's own
// branch and reports its current checks -- what a still-running dispatch
// asks for through the open_pull_request tool (pkg/mcp), so an agent can
// watch the repo's own CI run against the branch it just pushed rather
// than exiting blind and learning nothing until a human reads the pull
// request the finish path opened for it.
//
// It is deliberately the same pull request finishWithPullRequest would
// have opened at the end of the run: same branch (model.BranchName), same
// base, same title, same body, through the same EnsurePullRequest. A run
// that calls this has moved *when* its pull request appears, not what
// appears -- and because EnsurePullRequest finds an already-open pull
// request for the branch before opening one, the finish path that follows
// adopts this very pull request rather than trying (and failing) to open
// a second one for the same head.
//
// What it deliberately does not do is anything that ends the task.
// CompletedAt stays unset -- the run is still live, and setting it here
// would hand a still-running task to SyncPullRequests' merge queue, which
// could merge a branch the agent is still pushing to. Ending the task is
// finishWithPullRequest's job, on the finish path, once the run is
// actually over. (Store.OpenPullRequestLinks only returns links belonging
// to a task whose run is over, so a pull request opened here is not
// synced or merged by anything until that has happened.)
//
// A branch that is there but empty is answered in the same spirit and
// for the same reason, a little further down: "commit something first"
// rather than GitHub's 422 about a head with no commits over its base.
// The finish path ends a task over that condition; this one deliberately
// does not touch the task at all, since the run asking is still running
// and committing something is a thing it can still do.
//
// Two checks come first, both mirroring the finish path's own. The
// branch, through the same branchExistsSettled, so an agent that calls
// this before pushing gets "push it first" back rather than GitHub's own
// 422 about a head that does not exist. And the task's own closure, read
// fresh from the store, because a human closing a task mid-run is
// answered the same way salvagePushedBranch answers it: nobody wants a
// closed task's work turned into a pull request, and the run this call
// came from is about to be cancelled for the same reason.
func OpenPullRequestForTask(ctx context.Context, store *model.Store, client github.Client,
	task model.Task) (PullRequestStatus, error) {

	if task.Target == nil {
		return PullRequestStatus{}, fmt.Errorf(
			"orchestrator: task %s has no repo attached, so there is no branch to open a pull request for", task.ID)
	}
	if rc, ok := client.(*github.RESTClient); ok {
		if src, ok := rc.Tokens.(github.GitOnlySource); ok {
			if reason := src.GitOnly(task.Target.StoredOwner(), task.Target.Name); reason != "" {
				return PullRequestStatus{}, fmt.Errorf("orchestrator: %s", reason)
			}
		}
	}
	branch := task.BranchName()
	if reviewedID, inPlace := reviewsInPlace(task); inPlace {
		return PullRequestStatus{}, fmt.Errorf(
			"orchestrator: task %s pushes to %s, the branch of task %s under review, whose own pull request "+
				"already carries what you push there -- there is no second pull request to open",
			task.ID, branch, reviewedID)
	}
	pushed, err := targetBranchExists(client, *task.Target, branch)
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: checking %s's branch: %w", task.ID, err)
	}
	if !pushed {
		return PullRequestStatus{}, fmt.Errorf(
			"orchestrator: %s is not on %s yet -- commit and push it before asking for a pull request",
			branch, task.Target)
	}
	closed, err := taskClosed(ctx, store, task.ID)
	if err != nil {
		return PullRequestStatus{}, err
	}
	if closed {
		return PullRequestStatus{}, fmt.Errorf(
			"orchestrator: task %s was closed, so no pull request is being opened for %s", task.ID, branch)
	}

	// time.Now, where the finish path passes the cycle's own now: this
	// call is a live one, made by a run mid-flight through the
	// open_pull_request tool, and the only thing the timestamp reaches is
	// a comment noteBaseRetarget may write about a vanished base.
	now := time.Now().UTC()
	pr, landedWork, err := ensurePullRequest(ctx, store, client, task, now)
	if landed, ok := alreadyLanded(err); ok {
		// Nothing opened, and the run is told why in the words the task
		// is: the work is already on the base, so the pull request it
		// asked for would be a duplicate. The task is not ended here --
		// the run is still live -- and the finish path reaches the same
		// finding and marks it done.
		if nerr := noteOnTask(ctx, store, task.ID, landed.Error(), now); nerr != nil {
			return PullRequestStatus{}, nerr
		}
		return PullRequestStatus{}, landed
	}
	if _, empty := emptyBranch(err); empty {
		// The same condition the finish path ends a task over, answered
		// differently because of when this is: the run is still live and
		// still has turns, so this is a thing it can fix itself. Nothing
		// is written to the task and nothing is parked -- that decision
		// belongs to the finish, once the branch is final and this
		// agent's chance to commit something has actually passed.
		return PullRequestStatus{}, fmt.Errorf(
			"orchestrator: %w -- commit your work and push it before asking for a pull request", err)
	}
	if conflict, ok := conflictError(err); ok {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: %w", conflict)
	}
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("orchestrator: opening a pull request for %s: %w", task.ID, err)
	}
	if pr.Number == 0 {
		return PullRequestStatus{
			ChecksKnown: true,
			Note: fmt.Sprintf("Initialized %s on %s at %s and marked task %s done.",
				landedWork.Base, task.Target, shortSHA(landedWork.MergedSHA), task.ID),
		}, nil
	}
	ref := model.PullRequestRef{Repo: *task.Target, Number: pr.Number}
	// Linked now rather than only at the finish: a pull request the store
	// does not know about is one a human reading the task while the run is
	// still going cannot see. The link is written the same idempotent way
	// the finish path writes it, so whichever of the two runs second is a
	// no-op rather than a duplicate.
	if err := linkPullRequest(ctx, store, task.ID, ref); err != nil {
		return PullRequestStatus{}, err
	}

	// From here on the pull request exists, so nothing below turns this
	// into a failed call: a caller told only "reading checks failed" has
	// lost the number it asked for, and asking again would open nothing
	// new to recover it. See PullRequestStatus.ChecksError.
	status := PullRequestStatus{PullRequest: pr}
	if landedWork.partial() {
		status.Note = landedWork.partialSentence()
		if err := noteOnTask(ctx, store, task.ID, fmt.Sprintf("Opened %s. %s", ref, status.Note), now); err != nil {
			return status, err
		}
	}
	detail, err := client.GetPullRequest(task.Target.Owner, task.Target.Name, pr.Number)
	if err != nil {
		status.ChecksError = fmt.Sprintf("reading %s: %v", ref, err)
		return status, nil
	}
	checks, known, err := checkRunsFor(client, ref, detail.HeadRef, detail.HeadSHA)
	if err != nil {
		status.ChecksError = err.Error()
		return status, nil
	}
	status.Checks, status.ChecksKnown = checks, known
	return status, nil
}

// linkPullRequest records ref on taskID as the pull request that fixes
// it, idempotently -- shared by the finish path (finishWithPullRequest)
// and by a run opening its own pull request mid-flight
// (OpenPullRequestForTask), which is exactly why it has to be idempotent:
// on a run that did both, the second call finds the link the first wrote.
//
// Through UpdateTask rather than by writing back a task struct the caller
// is holding: that copy was read at the top of a cycle, and a person
// editing the task from the UI in between would lose their edit to this
// write. Re-checking the link inside the closure is also what makes it
// idempotent across a retry.
func linkPullRequest(ctx context.Context, store *model.Store, taskID string, ref model.PullRequestRef) error {
	if err := store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		for _, l := range t.Links {
			if l.Kind == model.LinkFixes && l.Target == ref.String() {
				return nil
			}
		}
		t.Links = append(t.Links, model.Link{Kind: model.LinkFixes, Target: ref.String()})
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: linking %s to %s: %w", taskID, ref, err)
	}
	return nil
}
