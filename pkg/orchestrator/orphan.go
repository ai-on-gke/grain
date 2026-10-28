package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// noteOrphanedPullRequests says, on a closed task and on each pull
// request still linked to it, that grain has stopped watching it.
//
// This is the run-side half of the telling model.OrphanedPullRequestNote
// describes; ui.Client.noteOrphanedPullRequests is the other, for a close
// arriving from a human rather than discovered by a finishing run. Both
// exist because either one can be first: a close that lands while a run
// is still live is read here, at the finish, on a link the human's own
// close could not yet see, and a close that lands afterwards is read
// there, on a link this run has already written. Whichever runs second
// finds the note already there and says nothing more (Store.
// OrphanedPullRequestNoted).
//
// The task is re-read rather than taken from the caller's copy for the
// same reason taskClosed re-reads the observation: that copy was read at
// the top of the cycle, before the run that just finished had opened --
// and linked -- its own pull request through open_pull_request, which is
// precisely the case this exists for.
//
// A failed GitHub call is not a failed note. It is folded into the words
// left on the task (the echo argument), so the record survives a
// credential that cannot comment, and logged, because this package has a
// journal to log it to and a note nobody read is worth a line in it.
func noteOrphanedPullRequests(ctx context.Context, store *model.Store, client github.Client,
	taskID string, now time.Time) error {

	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	for _, l := range task.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		// Unparseable targets skipped for the reason ui's own copy skips
		// them: every fixes-link grain writes is a PullRequestRef's
		// String(), and there is nothing to say about a pull request that
		// cannot be named.
		ref, err := model.ParsePullRequestRef(l.Target)
		if err != nil {
			continue
		}
		// The zero outcome, always: closing a pull request needs somebody
		// to have asked for it at the moment they closed the task
		// (ui.CloseOptions), and there is no human in front of this path
		// at all -- it is a run discovering, as it finishes, that its
		// task was closed underneath it. Nothing grain decides for
		// itself closes a pull request.
		outcome := model.PullRequestCloseOutcome{}
		noted, err := store.OrphanedPullRequestNoted(ctx, taskID, ref, outcome)
		if err != nil {
			return err
		}
		if noted {
			continue
		}
		echo := commentOnPullRequest(client, taskID, ref)
		if echo != nil {
			log.Printf("orchestrator: task %s closed with %s still open, "+
				"but grain could not say so on the pull request itself: %v", taskID, ref, echo)
		}
		body := model.OrphanedPullRequestNote(taskID, ref, outcome, echo)
		if err := store.NoteOrphanedPullRequest(ctx, taskID, ref, body, now); err != nil {
			return err
		}
	}
	return nil
}

// commentOnPullRequest posts the note on the pull request itself and
// reports what happened, rather than acting on it -- what
// model.OrphanedPullRequestNote folds into the task's own copy.
func commentOnPullRequest(client github.Client, taskID string, ref model.PullRequestRef) error {
	_, err := client.CreateComment(ref.Repo.Owner, ref.Repo.Name, ref.Number,
		model.OrphanedPullRequestComment(taskID, ref, model.PullRequestCloseOutcome{}))
	return err
}

// noteOrphanedIfClosed is the same, for the narrower window on the far
// side of a close check that already passed.
//
// finishWithPullRequest opens, links and completes; a close landing in
// the middle of that finds the check above already made and leaves a task
// that is closed, completed and linked -- with nobody told, since the
// human's own close read the task before the link existed. One more read
// of the observation, once the link is definitely written, is what closes
// that window. It costs a query per finished run and answers "no" on
// nearly all of them.
func noteOrphanedIfClosed(ctx context.Context, store *model.Store, client github.Client,
	taskID string, now time.Time) error {

	closed, err := taskClosed(ctx, store, taskID)
	if err != nil || !closed {
		return err
	}
	return noteOrphanedPullRequests(ctx, store, client, taskID, now)
}
