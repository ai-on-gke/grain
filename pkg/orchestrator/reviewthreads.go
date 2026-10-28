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
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Review threads somebody else opens on a grain-owned pull request -- a
// bot reviewer like gemini-code-assist, or a person reading the diff on
// github.com -- get the handling grain's own review findings get: a run
// on the task that owns the pull request, told to fix each one or answer
// it with a reason, and to resolve it (pkg/mcp's
// pullrequest_review_tools.go: reply_to_pull_request_comment and
// resolve_review_thread, scoped to the run's own pull request).
//
// One run per new batch, never one per cycle a thread stays open. Every
// thread a run is sent for is recorded on the task (model.LinkReviewThread,
// by the REST ID of the comment that opened it) before the run is asked
// for, so a thread the run answered without resolving, a bot answering
// the run's reply in the same thread, or a thread somebody re-opens never
// sends another one. Only a thread nobody has sent a run for yet does.
//
// Which threads count:
//
//   - opened by a comment (not a reply) on the task's own pull request;
//   - not part of a draft review grain posted itself for a review task's
//     findings (model.LinkDraftReview) -- those are the review-questions
//     hold's to put to the owner (reviewquestions.go), not a run's to fix;
//   - not resolved and not outdated, when the thread view (GitHub's
//     GraphQL API) can be read. A credential or host without it falls back
//     to counting every unrecorded thread, which errs towards one run too
//     many rather than a reviewer nobody answers.
//
// Which tasks: one whose run is over and whose change is waiting to land
// (completed and on the merge queue, or awaiting Submit) -- never a
// running, parked or closed one, and never a review or fix stacked on
// somebody else's branch, whose pull request is grain merging into a task
// branch rather than anything a reviewer reads. A change held on its own
// review's questions waits for its owner first: their answer sends the
// run, and that run will see these threads in pull_request_status too.
func SyncReviewThreads(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	if client == nil {
		return nil
	}
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
		if task == nil || isReviewTask(*task) || task.Origin.Reason == model.ReasonFix {
			continue
		}
		if _, held := model.ReviewQuestionsOf(*task); held {
			continue
		}
		ref, err := model.ParsePullRequestRef(link.PullRequest)
		if err != nil {
			continue
		}
		state, err := store.State(ctx, task.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's state: %w", task.ID, err))
			continue
		}
		if state != model.StateCompleted && state != model.StateAwaitingSubmit {
			continue
		}
		if err := sendRunForReviewThreads(ctx, store, client, *task, ref, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reviewThreadLister is the thread view github.RESTClient offers beside
// github.Client; a client without it is read through the REST comments
// alone.
type reviewThreadLister interface {
	ListReviewThreads(owner, repo string, number int) ([]github.ReviewThread, error)
}

func sendRunForReviewThreads(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, now time.Time) error {

	// A finished pull request nobody is commenting on is read less and
	// less often (github.PollSchedule): any new comment, or anything
	// grain writes to it, puts it back on every cycle.
	poll := github.PollKey{Scope: storeScope(store),
		Subject: github.PullRequestSubject(ref.Repo.Owner, ref.Repo.Name, ref.Number), Aspect: "review comments"}
	if !github.Polls().Due(poll, github.PollNormal, false, now) {
		return nil
	}
	comments, err := client.ListReviewComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		// Best effort, every cycle: the next one reads them again.
		log.Printf("orchestrator: reading review comments on %s: %v", ref, err)
		return nil
	}
	github.Polls().Observe(poll, reviewCommentsFingerprint(comments), now)
	seen := map[string]bool{}
	drafts := map[string]bool{}
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkReviewThread:
			seen[l.Target] = true
		case model.LinkDraftReview:
			drafts[l.Target] = true
		}
	}
	var fresh []github.ReviewComment
	for _, c := range comments {
		if c.InReplyTo != 0 || seen[strconv.Itoa(c.ID)] {
			continue
		}
		if c.ReviewID != 0 && drafts[strconv.Itoa(c.ReviewID)] {
			continue
		}
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return nil
	}

	// Threads already resolved or outdated are recorded with the rest, so
	// they cost a thread read once rather than every cycle, but send no run.
	open := fresh
	if lister, ok := client.(reviewThreadLister); ok {
		threads, err := lister.ListReviewThreads(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			log.Printf("orchestrator: reading review threads on %s (%v); counting every new one as open", ref, err)
		} else {
			closed := map[int]bool{}
			for _, th := range threads {
				if (th.Resolved || th.Outdated) && len(th.CommentIDs) > 0 {
					closed[th.CommentIDs[0]] = true
				}
			}
			open = nil
			for _, c := range fresh {
				if !closed[c.ID] {
					open = append(open, c)
				}
			}
		}
	}

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		for _, c := range fresh {
			t.Links = append(t.Links, model.Link{Kind: model.LinkReviewThread, Target: strconv.Itoa(c.ID)})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: recording the review threads on %s: %w", task.ID, err)
	}
	if len(open) == 0 {
		return nil
	}

	if err := store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
	}); err != nil {
		return fmt.Errorf("orchestrator: queueing %s for its review threads: %w", task.ID, err)
	}
	log.Printf("orchestrator: %d new review thread(s) on %s; sending %s another run", len(open), ref, task.ID)
	return reviewComment(ctx, store, task.ID, reviewThreadsNote(ref, open), now)
}

// reviewThreadsNote is what the run sent for new threads reads first.
func reviewThreadsNote(ref model.PullRequestRef, threads []github.ReviewComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s opened on %s, so this task is back in the queue to deal with %s:\n\n",
		countOf(len(threads), "new review thread"), ref, pronounFor(len(threads)))
	for _, c := range threads {
		where := c.Path
		if c.Line != nil {
			where = fmt.Sprintf("%s:%d", c.Path, *c.Line)
		}
		body := strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n  ")
		fmt.Fprintf(&b, "- comment %d by %s on `%s` -- %s\n", c.ID, c.User, where, body)
	}
	b.WriteString("\nFor each one: fix it -- commit and push to this task's branch -- or, where it is " +
		"wrong or out of scope, answer it with the reason (reply_to_pull_request_comment with its " +
		"comment_id). Then mark it resolved (resolve_review_thread). pull_request_status lists the " +
		"threads with their IDs. Leave nothing you could have fixed as a reply.")
	return b.String()
}

func pronounFor(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// reviewCommentsFingerprint is what a poll of review comments compares:
// every comment's id, so a new one, a deleted one or a reply all count
// as a change.
func reviewCommentsFingerprint(comments []github.ReviewComment) string {
	var b strings.Builder
	for _, c := range comments {
		b.WriteString(strconv.Itoa(c.ID))
		b.WriteByte(',')
	}
	return b.String()
}
