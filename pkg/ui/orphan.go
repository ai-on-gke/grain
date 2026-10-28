package ui

import (
	"context"
	"errors"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// PullRequestComments is implemented by whatever can post grain's own
// words on a pull request -- pkg/cli/daemon.go's own adapter over that
// deployment's GitHub client, in a real deployment. Named here and filled
// there for the reason PullRequests is: this package does not import
// pkg/orchestrator or pkg/github, and cmd/grain is where both ends are in
// scope.
//
// ref is a model.PullRequestRef's String() -- "owner/name#123", the same
// spelling a task's own fixes-link carries -- so the implementation
// parses it back rather than this package handing over a type the wire
// shape does not otherwise use.
type PullRequestComments interface {
	Comment(ctx context.Context, ref string, body string) error
}

// PullRequestCloser is the same shape for the one call in grain that
// destroys work rather than making it: closing a pull request on GitHub
// without merging it.
//
// Deliberately its own interface, and its own Config field, rather than a
// second method on PullRequestComments. Leaving a comment and closing a
// pull request are not the same power, and a deployment that hands its UI
// the first has not thereby handed it the second: a Config with
// PullRequestComments set and this one nil can say everything it has to
// say and cannot shut anything. Splitting them is what makes that
// possible to express at all.
type PullRequestCloser interface {
	Close(ctx context.Context, ref string) error
}

// errNoPullRequestComments is the echo failure recorded on a deployment
// whose UI was never handed a GitHub client (Config.PullRequestComments
// nil). It reads as a reason, because that is what it ends up being: it
// is written into the note left on the task, where "grain could not leave
// this note on the pull request itself: ..." completes the sentence.
var errNoPullRequestComments = errors.New("this deployment's UI is not wired to a GitHub client")

// errNoPullRequestCloser is the same for Config.PullRequestCloser, and
// reads the same way: it completes "GitHub refused: ..." in the note left
// on the task, which is where somebody who ticked the box and found the
// pull request still open should be told why.
var errNoPullRequestCloser = errors.New("this deployment's UI is not wired to a GitHub client " +
	"that can close a pull request")

// noteOrphanedPullRequests says, on a task just closed and on each pull
// request still linked to it, what closing the task has done to it -- the
// telling model.OrphanedPullRequestNote's own doc comment explains the
// absence of -- and, when the human asked for it, closes that pull
// request first.
//
// Called only from setClosed, which is the *human* close: the other
// writer of Observation.ClosedAt is orchestrator.SyncPullRequests, which
// sets it when the pull request itself merged or was closed on GitHub,
// and there is nothing orphaned about a task closed because its work
// landed. A note there would be a lie on every merged task grain has, and
// a close there would be grain shutting a pull request nobody asked it to
// -- which is why the choice is a CloseOptions argument on this one path
// and not a setting anything else could read.
//
// Ordering is deliberate. The pull request is closed first, then told,
// then the task is told last, so that whether each earlier step worked is
// part of what the next one says -- a GitHub call that fails (a
// credential that cannot comment or close, a deployment with no client at
// all) then costs the close nothing at all, and leaves no silence behind
// it either. It is the one arrangement where a best-effort call is
// neither swallowed nor able to fail the close: this package has no
// logger to lose it to, and a close that returned 500 because a comment
// did not post would read as a close that did not happen.
//
// Closing before commenting, specifically, is what keeps the words
// honest: the comment says which of the two things happened, and it
// cannot say that before the attempt. A refused close leaves exactly the
// orphaned pull request grain would have left had nobody asked, and says
// so in those words plus the refusal.
//
// The already-noted check is what keeps a second close (close, reopen,
// close again) from posting a second copy on GitHub, where nothing would
// dedupe it -- Store.NoteOrphanedPullRequest's own write-time check
// covers the task's copy, but only this one comes early enough to cover
// the pull request's. It gates the telling and not the closing: somebody
// who ticked the box on a second close asked for the pull request to be
// shut, and a note already on the task about the first close is no reason
// to ignore them.
func (c *Client) noteOrphanedPullRequests(ctx context.Context, task model.Task,
	opts CloseOptions, now time.Time) error {

	// A pull request that has already finished is not orphaned by
	// anything. orchestrator.SyncPullRequests sets ClosedAt itself the
	// moment one merges or closes on GitHub, so a task with PrMergedAt or
	// PrClosedAt set is a task whose work landed (or was thrown away) --
	// and a human clicking Close on it in the moment before the UI caught
	// up must not be told grain has walked away from a pull request it
	// merged, nor have grain try to close one that is already closed or,
	// worse, merged. This is the same distinction state.js's own
	// orphanedPullRequest draws, from the same two fields.
	obs, err := c.Store.GetObservation(ctx, task.ID)
	if err != nil {
		return err
	}
	if obs != nil && (obs.PrMergedAt != nil || obs.PrClosedAt != nil) {
		return nil
	}
	for _, l := range task.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		// A fixes-link target is always a PullRequestRef.String() (see
		// orchestrator.linkPullRequest, which writes every one of them),
		// so an unparseable one is not a pull request this close has
		// orphaned -- and there is nothing to say about a pull request
		// grain cannot name.
		ref, err := model.ParsePullRequestRef(l.Target)
		if err != nil {
			continue
		}
		outcome := model.PullRequestCloseOutcome{Requested: opts.ClosePullRequest}
		if outcome.Requested {
			outcome.Err = c.closePullRequest(ctx, ref)
		}
		noted, err := c.Store.OrphanedPullRequestNoted(ctx, task.ID, ref, outcome)
		if err != nil {
			return err
		}
		if noted {
			continue
		}
		echo := c.commentOnPullRequest(ctx, task.ID, ref, outcome)
		body := model.OrphanedPullRequestNote(task.ID, ref, outcome, echo)
		if err := c.Store.NoteOrphanedPullRequest(ctx, task.ID, ref, body, now); err != nil {
			return err
		}
	}
	return nil
}

// commentOnPullRequest posts the note on the pull request itself,
// reporting what happened rather than acting on it -- its return value is
// the echo argument model.OrphanedPullRequestNote folds into the task's
// own copy.
func (c *Client) commentOnPullRequest(ctx context.Context, taskID string,
	ref model.PullRequestRef, outcome model.PullRequestCloseOutcome) error {

	if c.Config.PullRequestComments == nil {
		return errNoPullRequestComments
	}
	return c.Config.PullRequestComments.Comment(ctx, ref.String(),
		model.OrphanedPullRequestComment(taskID, ref, outcome))
}

// closePullRequest closes the pull request on GitHub, reporting what
// happened rather than acting on it -- its return value is
// model.PullRequestCloseOutcome.Err, and a refusal only changes the words
// grain then leaves in both places.
//
// A deployment whose UI holds no closer at all lands here too, and is
// told apart from a credential GitHub turned down only by the wording of
// the error: either way the pull request is still open, which is the fact
// the note has to carry.
func (c *Client) closePullRequest(ctx context.Context, ref model.PullRequestRef) error {
	if c.Config.PullRequestCloser == nil {
		return errNoPullRequestCloser
	}
	return c.Config.PullRequestCloser.Close(ctx, ref.String())
}
