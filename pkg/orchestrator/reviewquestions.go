package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A change held on its review's open questions (model.LinkReviewQuestions,
// written by noteFindingsOnReviewedTask) is a question to its owner, and
// this is what an answer does.
//
// The review fixed what it could on its stacked branch; what it left are
// the findings it could not settle without the owner -- a design
// decision, a trade-off, a behaviour change the brief did not settle --
// each written as a question with its recommended answer. The owner
// answers by replying, on the held task or on the review itself, and
// SyncReviewQuestions sends the held task back to the queue: the
// questions and the reply are both on its conversation, so the run that
// picks it up has them in its prompt, carries out the decision, and the
// change goes through the way it would have after a clean review --
// auto-merge back on when it was merging itself before the questions held
// it (ReviewQuestions.RestoreAutoMerge), and judged afresh by the security
// hold and the repository's gating triggers when that run finishes, so a
// change only a human may release still waits for one.
//
// Submit is the other answer, "ship as is": it sets auto-merge back, the
// change is no longer awaiting anybody, and the link is dropped here on
// the next cycle without a run.
//
// Only a human's comment counts. Grain's own notes, the review's relayed
// findings and anything an agent says leave the hold exactly where it is.
//
// Level-triggered, like SyncSecurityHolds: what to do is read off the
// store every cycle, so a reply written while the daemon was down is acted
// on by the first cycle after it comes back.
func SyncReviewQuestions(ctx context.Context, store *model.Store, now time.Time) error {
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
		if task == nil {
			continue
		}
		questions, ok := model.ReviewQuestionsOf(*task)
		if !ok {
			continue
		}
		if err := settleReviewQuestions(ctx, store, *task, questions, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func settleReviewQuestions(ctx context.Context, store *model.Store, task model.Task,
	questions model.ReviewQuestions, now time.Time) error {

	state, err := store.State(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading %s's state: %w", task.ID, err)
	}
	switch {
	case state == model.StateClosed || task.AutoMerge:
		// Shipped as is (Submit), or gone: there is nobody left to ask.
		return dropReviewQuestions(ctx, store, task.ID, questions)
	case state != model.StateAwaitingSubmit:
		// Already running again, or parked on something else; the hold
		// is read again once it is back to waiting on its owner.
		return nil
	}

	replies, err := ownerReplies(ctx, store, task.ID, questions.AfterCommentID)
	if err != nil {
		return err
	}
	reviewReplies, err := ownerReplies(ctx, store, questions.ReviewTaskID, questions.AfterCommentID)
	if err != nil {
		return err
	}
	if len(replies) == 0 && len(reviewReplies) == 0 {
		return nil
	}

	// A reply on the review is carried onto the held task, where the run
	// that acts on it reads its conversation.
	for _, c := range reviewReplies {
		body := fmt.Sprintf("%s answered the review's questions on task %s:\n\n%s",
			c.Author.Actor.ID, questions.ReviewTaskID, quoteBlock(c.Body))
		if err := reviewComment(ctx, store, task.ID, body, now); err != nil {
			return err
		}
	}

	reopened, restored := false, false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		reopened, restored = false, false
		current, ok := model.ReviewQuestionsOf(*t)
		if !ok || current != questions {
			return nil
		}
		reopened = true
		t.Links = withoutLinks(t.Links, model.LinkReviewQuestions)
		// Answering a review's questions is not the Submit a human-only
		// trust-boundary hold waits for.
		if questions.RestoreAutoMerge && !heldForHuman(*t) {
			t.AutoMerge, restored = true, true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: taking %s off its review-questions hold: %w", task.ID, err)
	}
	if !reopened {
		return nil
	}
	if err := store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	}); err != nil {
		return fmt.Errorf("orchestrator: queueing %s to carry out its owner's decision: %w", task.ID, err)
	}

	then := "Once it has pushed, the change goes through as it would have after a clean review."
	switch {
	case !questions.RestoreAutoMerge:
		then = "It was not merging by itself before the review, so it waits for a Submit again " +
			"once that run has pushed."
	case !restored:
		then = "It touches a path of grain's own trust boundary that only a human may release, " +
			"so it waits for a Submit again once that run has pushed."
	}
	body := fmt.Sprintf(
		"The owner answered the open questions from review task %s, so this task is back in the "+
			"queue: the next run carries out that decision -- fix what was decided, commit and "+
			"push to this task's branch, and leave nothing it could have fixed as a comment. %s",
		questions.ReviewTaskID, then)
	log.Printf("orchestrator: %s's owner answered the questions from review %s; redispatching it",
		task.ID, questions.ReviewTaskID)
	return reviewComment(ctx, store, task.ID, body, now)
}

// ownerReplies is every comment a person wrote on taskID after the
// comment with ID after.
func ownerReplies(ctx context.Context, store *model.Store, taskID string, after int64) ([]model.Comment, error) {
	if taskID == "" {
		return nil, nil
	}
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading %s's conversation: %w", taskID, err)
	}
	var out []model.Comment
	for _, c := range comments {
		if c.ID > after && c.Author.Actor.Kind == model.PrincipalHuman && strings.TrimSpace(c.Body) != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// dropReviewQuestions removes a hold that no longer waits on anybody.
func dropReviewQuestions(ctx context.Context, store *model.Store, taskID string,
	questions model.ReviewQuestions) error {

	if err := store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		if current, ok := model.ReviewQuestionsOf(*t); ok && current == questions {
			t.Links = withoutLinks(t.Links, model.LinkReviewQuestions)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: clearing %s's review questions: %w", taskID, err)
	}
	return nil
}

// quoteBlock renders body as a markdown quotation.
func quoteBlock(body string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n> ")
}
