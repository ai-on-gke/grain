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

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/change"
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

type reviewCommentReplier interface {
	ReplyToReviewComment(owner, repo string, number, commentID int, body string) (int, error)
}

type reviewThreadResolver interface {
	ResolveReviewThread(owner, repo, threadID string) error
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
	relayed := map[int]bool{}
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkReviewThread:
			seen[l.Target] = true
		case model.LinkDraftReview:
			drafts[l.Target] = true
		case model.LinkRelayedReviewThread:
			if rt, ok := model.ParseRelayedReviewThread(l.Target); ok {
				relayed[rt.CommentID] = true
			}
		}
	}
	var fresh []github.ReviewComment
	for _, c := range comments {
		if c.InReplyTo != 0 || seen[strconv.Itoa(c.ID)] || relayed[c.ID] {
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

// SyncChangeThreads is SyncReviewThreads for a task whose change is a CL
// (changelists.md §5, G3-5): it asks the registered change.Provider for
// each linked CL's new threads -- the provider leaves out the operator's
// own, bots', resolved ones and every one already recorded -- records
// each as model.LinkCLThread, and only then asks for one run for the
// batch. A task waiting on Submit, on a reply or finished gets that run;
// a running one is told in an addendum (model.CLThreadsPrincipal) and
// deals with them before it ends. Any other state -- queued, parked,
// closed -- is left until it is one of those.
func SyncChangeThreads(ctx context.Context, store *model.Store, now time.Time) error {
	if _, ok := change.Lookup(model.LinkChangelist); !ok {
		return nil
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing tasks for CL review threads: %w", err)
	}
	var errs []error
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if change.Primary(task) == "" || isReviewTask(task) {
			continue
		}
		// A change held on its own review's questions waits for its
		// owner first, as SyncReviewThreads' does: their answer sends
		// the run, and that run sees these threads too.
		if _, held := model.ReviewQuestionsOf(task); held {
			continue
		}
		state, err := store.State(ctx, task.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading %s's state: %w", task.ID, err))
			continue
		}
		switch state {
		case model.StateAwaitingSubmit, model.StateAwaitingReply, model.StateCompleted, model.StateRunning:
		default:
			continue
		}
		if err := sendRunForChangeThreads(ctx, store, task, state, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sendRunForChangeThreads(ctx context.Context, store *model.Store, task model.Task, state model.State, now time.Time) error {
	seen := map[string]bool{}
	for _, l := range task.Links {
		if l.Kind == model.LinkCLThread {
			seen[l.Target] = true
		}
	}
	var fresh []change.Thread
	for _, l := range task.Links {
		if l.Kind != model.LinkChangelist {
			continue
		}
		threads, err := change.ThreadsOf(ctx, task, l.Kind, l.Target, nil, now)
		if err != nil {
			// Best effort, every cycle: the next one asks again.
			log.Printf("orchestrator: reading review threads on %s: %v", l.Target, err)
			continue
		}
		for _, th := range threads {
			if th.ID == "" || th.Resolved || seen[th.ID] {
				continue
			}
			seen[th.ID] = true
			fresh = append(fresh, th)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		for _, th := range fresh {
			t.Links = append(t.Links, model.Link{Kind: model.LinkCLThread, Target: th.ID})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: recording the CL review threads on %s: %w", task.ID, err)
	}
	if state != model.StateRunning {
		if err := store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = nil
			o.RetryRequestedAt = &now
		}); err != nil {
			return fmt.Errorf("orchestrator: queueing %s for its CL review threads: %w", task.ID, err)
		}
		log.Printf("orchestrator: %d new review thread(s) on %s; sending %s another run", len(fresh), change.Primary(task), task.ID)
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.CLThreadsPrincipal},
		Body:      changeThreadsNote(fresh, state == model.StateRunning),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: commenting on %s: %w", task.ID, err)
	}
	return nil
}

// changeThreadsNote is what the run sent for new CL threads reads first.
// The bodies are the provider's, already wrapped as external input.
func changeThreadsNote(threads []change.Thread, running bool) string {
	var b strings.Builder
	if running {
		fmt.Fprintf(&b, "%s opened on this task's CL while this run is going; deal with %s before you finish:\n\n",
			countOf(len(threads), "new review thread"), pronounFor(len(threads)))
	} else {
		fmt.Fprintf(&b, "%s opened on this task's CL, so this task is back in the queue to deal with %s:\n\n",
			countOf(len(threads), "new review thread"), pronounFor(len(threads)))
	}
	for _, th := range threads {
		body := strings.ReplaceAll(strings.TrimSpace(th.Body), "\n", "\n  ")
		fmt.Fprintf(&b, "- thread %s by %s -- %s\n", th.ID, userOrUnknownAuthor(th.Author), body)
	}
	b.WriteString("\nFor each one: fix it -- upload the fix with open_pull_request -- or, where it is " +
		"wrong or out of scope, answer it with the reason (reply_to_pull_request_comment with its " +
		"thread_id). Then mark it resolved (resolve_review_thread). Replies are saved as drafts on " +
		"the CL and published when this run ends. Leave nothing you could have fixed as a reply.")
	return b.String()
}

func userOrUnknownAuthor(author string) string {
	if author == "" {
		return "someone"
	}
	return author
}

// discoverRelayedThreads maps the inline review comments just posted by reviewID
// on ref to their GraphQL review thread IDs.
func discoverRelayedThreads(client github.Client, ref model.PullRequestRef, reviewTaskID string,
	reviewID int, decided bool) []model.RelayedReviewThread {

	if client == nil || ref.Number <= 0 || reviewID == 0 {
		return nil
	}
	lister, ok := client.(reviewThreadLister)
	if !ok {
		return nil
	}
	comments, err := client.ListReviewComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return nil
	}
	threads, err := lister.ListReviewThreads(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return nil
	}
	threadByRoot := map[int]string{}
	for _, th := range threads {
		if len(th.CommentIDs) > 0 && th.ID != "" {
			threadByRoot[th.CommentIDs[0]] = th.ID
		}
	}
	var out []model.RelayedReviewThread
	for _, c := range comments {
		if c.InReplyTo != 0 || c.ReviewID != reviewID {
			continue
		}
		threadID := threadByRoot[c.ID]
		if threadID == "" {
			continue
		}
		out = append(out, model.RelayedReviewThread{
			ReviewTaskID: reviewTaskID,
			ReviewID:     reviewID,
			CommentID:    c.ID,
			ThreadID:     threadID,
			Decided:      decided,
		})
	}
	return out
}

// markRelayedReviewThreadsDecided marks relayed review threads on links (for
// reviewTaskID, or all when reviewTaskID is empty) as having a recorded owner
// decision.
func markRelayedReviewThreadsDecided(links []model.Link, reviewTaskID string) []model.Link {
	out := make([]model.Link, len(links))
	for i, l := range links {
		if l.Kind == model.LinkRelayedReviewThread {
			if rt, ok := model.ParseRelayedReviewThread(l.Target); ok {
				if reviewTaskID == "" || rt.ReviewTaskID == "" || rt.ReviewTaskID == reviewTaskID {
					rt.Decided = true
					l.Target = rt.Target()
				}
			}
		}
		out[i] = l
	}
	return out
}

// syncRelayedReviewThreads reads ref's review comments and threads, backfilling
// LinkRelayedReviewThread entries for any root comments belonging to one of
// task's LinkDraftReview reviews that were not yet mapped (for instance, a
// draft review submitted after relayReviewFeedback ran).
func syncRelayedReviewThreads(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef) (model.Task, []github.ReviewComment, []github.ReviewThread) {

	if client == nil || ref.Number <= 0 {
		return task, nil, nil
	}
	lister, ok := client.(reviewThreadLister)
	if !ok {
		return task, nil, nil
	}
	comments, err := client.ListReviewComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return task, nil, nil
	}
	threads, err := lister.ListReviewThreads(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return task, comments, nil
	}

	drafts := map[int]bool{}
	knownComments := map[int]bool{}
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkDraftReview:
			if id, err := strconv.Atoi(l.Target); err == nil && id != 0 {
				drafts[id] = true
			}
		case model.LinkRelayedReviewThread:
			if rt, ok := model.ParseRelayedReviewThread(l.Target); ok {
				knownComments[rt.CommentID] = true
			}
		}
	}
	if len(drafts) == 0 {
		return task, comments, threads
	}

	threadByRoot := map[int]string{}
	for _, th := range threads {
		if len(th.CommentIDs) > 0 && th.ID != "" {
			threadByRoot[th.CommentIDs[0]] = th.ID
		}
	}
	questions, held := model.ReviewQuestionsOf(task)
	reviewTaskID := ""
	if held {
		reviewTaskID = questions.ReviewTaskID
	}
	var added []model.RelayedReviewThread
	for _, c := range comments {
		if c.InReplyTo != 0 || !drafts[c.ReviewID] || knownComments[c.ID] {
			continue
		}
		threadID := threadByRoot[c.ID]
		if threadID == "" {
			continue
		}
		knownComments[c.ID] = true
		added = append(added, model.RelayedReviewThread{
			ReviewTaskID: reviewTaskID,
			ReviewID:     c.ReviewID,
			CommentID:    c.ID,
			ThreadID:     threadID,
			Decided:      !held,
		})
	}
	if len(added) == 0 {
		return task, comments, threads
	}
	for _, rt := range added {
		task.Links = append(task.Links, model.Link{Kind: model.LinkRelayedReviewThread, Target: rt.Target()})
	}
	if store != nil {
		_ = store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			existing := map[int]bool{}
			for _, l := range t.Links {
				if l.Kind == model.LinkRelayedReviewThread {
					if rt, ok := model.ParseRelayedReviewThread(l.Target); ok {
						existing[rt.CommentID] = true
					}
				}
			}
			for _, rt := range added {
				if !existing[rt.CommentID] {
					t.Links = append(t.Links, model.Link{Kind: model.LinkRelayedReviewThread, Target: rt.Target()})
				}
			}
			return nil
		})
	}
	return task, comments, threads
}

// decidedRelayedThreadMap returns the grain-relayed review threads on task
// whose findings have a recorded decision, keyed by root comment ID.
func decidedRelayedThreadMap(task model.Task) map[int]model.RelayedReviewThread {
	questions, held := model.ReviewQuestionsOf(task)
	out := map[int]model.RelayedReviewThread{}
	for _, rt := range model.RelayedReviewThreadsOf(task) {
		if rt.Decided || !held || (rt.ReviewTaskID != "" && questions.ReviewTaskID != rt.ReviewTaskID) {
			out[rt.CommentID] = rt
		}
	}
	return out
}

// resolveHandledRelayedThreads replies to (if not yet replied to) and resolves
// every unresolved review thread on ref that grain's review relay opened and
// whose finding has a recorded decision.
func resolveHandledRelayedThreads(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef, replyLine string) (int, error) {

	task, comments, threads := syncRelayedReviewThreads(ctx, store, client, task, ref)
	decided := decidedRelayedThreadMap(task)
	if len(decided) == 0 || len(threads) == 0 {
		return 0, nil
	}
	repliedTo := map[int]bool{}
	commentByID := map[int]github.ReviewComment{}
	for _, c := range comments {
		commentByID[c.ID] = c
		if c.InReplyTo != 0 {
			repliedTo[c.InReplyTo] = true
		}
	}
	threadByRoot := map[int]github.ReviewThread{}
	threadByID := map[string]github.ReviewThread{}
	for _, th := range threads {
		threadByID[th.ID] = th
		if len(th.CommentIDs) > 0 {
			threadByRoot[th.CommentIDs[0]] = th
		}
	}
	replier, canReply := client.(reviewCommentReplier)
	resolver, canResolve := client.(reviewThreadResolver)
	if !canResolve {
		return 0, nil
	}

	resolved := 0
	for _, rt := range model.RelayedReviewThreadsOf(task) {
		if _, ok := decided[rt.CommentID]; !ok {
			continue
		}
		th, ok := threadByID[rt.ThreadID]
		if !ok {
			th, ok = threadByRoot[rt.CommentID]
		}
		if !ok || th.ID == "" || th.Resolved {
			continue
		}
		rootAuthor := commentByID[rt.CommentID].User
		if threadHasHumanComment(th, commentByID, rootAuthor) {
			continue
		}
		if canReply && !repliedTo[rt.CommentID] && len(th.CommentIDs) <= 1 && strings.TrimSpace(replyLine) != "" {
			if _, err := replier.ReplyToReviewComment(ref.Repo.Owner, ref.Repo.Name, ref.Number, rt.CommentID, replyLine); err != nil {
				log.Printf("orchestrator: replying to relayed review comment %d on %s before resolving thread %s: %v",
					rt.CommentID, ref, th.ID, err)
			}
		}
		if err := resolver.ResolveReviewThread(ref.Repo.Owner, ref.Repo.Name, th.ID); err != nil {
			return resolved, fmt.Errorf("orchestrator: resolving relayed review thread %s on %s: %w", th.ID, ref, err)
		}
		resolved++
	}
	return resolved, nil
}

// threadHasHumanComment reports whether any comment in th was written by a
// person other than the grain identity that opened the relayed review thread.
// It fails closed: a comment whose author cannot be read (missing from the
// listing, or with no login) counts as a person's, and so does a login that
// merely looks like grain's -- only the root comment's own author, or a GitHub
// App ("[bot]"), is grain's.
func threadHasHumanComment(th github.ReviewThread, commentByID map[int]github.ReviewComment, rootAuthor string) bool {
	if rootAuthor == "" {
		return true
	}
	for _, id := range th.CommentIDs {
		c, ok := commentByID[id]
		if !ok || c.User == "" {
			return true
		}
		if c.User == rootAuthor || strings.HasSuffix(c.User, "[bot]") {
			continue
		}
		return true
	}
	return false
}

// ownerRunResolutionReply formats the one-line reply posted on a grain-relayed
// review thread before resolving it when the owner's decision run finishes.
func ownerRunResolutionReply(task model.Task, result *agent.Result, pushed bool) string {
	var summary string
	if comment, ok := effectiveCommentOnIssue(result); ok {
		summary = oneLine(comment, 120)
	} else if reason, ok := firstToolCallArg(result, "nothing_needed", "reason"); ok {
		summary = oneLine(reason, 120)
	}
	if pushed {
		if summary != "" {
			return fmt.Sprintf("Handled in the latest push to `%s` (task %s): %s", task.BranchName(), task.ID, summary)
		}
		return fmt.Sprintf("Handled in the latest push to `%s` (task %s).", task.BranchName(), task.ID)
	}
	if summary != "" {
		return fmt.Sprintf("Resolved per owner decision on task %s: %s", task.ID, summary)
	}
	return fmt.Sprintf("Resolved per owner decision on task %s (kept as is).", task.ID)
}

// resolveRelayedThreadsAfterOwnerRun resolves any unresolved review threads
// that grain's own review tasks opened on task's pull request once the owner's
// decision run has finished handling them.
func resolveRelayedThreadsAfterOwnerRun(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, result *agent.Result, pushed bool) {

	if store == nil || client == nil || isReviewTask(task) {
		return
	}
	current, err := store.GetTask(ctx, task.ID)
	if err != nil || current == nil {
		return
	}
	if _, held := model.ReviewQuestionsOf(*current); held {
		return
	}
	hasRelayed := false
	for _, l := range current.Links {
		if l.Kind == model.LinkRelayedReviewThread || l.Kind == model.LinkDraftReview {
			hasRelayed = true
			break
		}
	}
	if !hasRelayed {
		return
	}
	pr := lastPullRequest(*current)
	if pr == nil {
		return
	}
	_ = store.UpdateTask(ctx, current.ID, func(t *model.Task) error {
		t.Links = markRelayedReviewThreadsDecided(t.Links, "")
		return nil
	})
	current.Links = markRelayedReviewThreadsDecided(current.Links, "")
	replyLine := ownerRunResolutionReply(*current, result, pushed)
	if _, err := resolveHandledRelayedThreads(ctx, store, client, *current, *pr, replyLine); err != nil {
		log.Printf("orchestrator: %v", err)
	}
}

// resolveOrClassifyUnresolvedThreadsOnMerge is the merge queue's belt-and-braces
// pass when a merge is refused: any unresolved review threads that were opened
// by grain's review relay and have a recorded decision are replied to and
// resolved, and resolvedAll reports whether every unresolved thread on ref was
// thus resolved so the caller can immediately retry the merge. Threads written
// by people are never auto-resolved.
func resolveOrClassifyUnresolvedThreadsOnMerge(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, ref model.PullRequestRef) bool {

	task, comments, threads := syncRelayedReviewThreads(ctx, store, client, task, ref)
	if len(threads) == 0 {
		return false
	}
	unresolvedTotal := 0
	for _, th := range threads {
		if !th.Resolved {
			unresolvedTotal++
		}
	}
	if unresolvedTotal == 0 {
		return false
	}

	replyLine := fmt.Sprintf("Resolved per recorded decision on task %s.", task.ID)
	resolvedGrain, err := resolveHandledRelayedThreads(ctx, store, client, task, ref, replyLine)
	if err != nil {
		log.Printf("orchestrator: %v", err)
		return false
	}
	_ = comments
	return resolvedGrain > 0 && resolvedGrain == unresolvedTotal
}

// unresolvedReviewThreads returns the number of remaining unresolved review
// threads on ref and their authors (in order of appearance, deduplicated).
func unresolvedReviewThreads(client github.Client, task model.Task, ref model.PullRequestRef) (int, []string) {
	if client == nil || ref.Number <= 0 {
		return 0, nil
	}
	lister, ok := client.(reviewThreadLister)
	if !ok {
		return 0, nil
	}
	threads, err := lister.ListReviewThreads(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return 0, nil
	}
	comments, _ := client.ListReviewComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	commentByID := map[int]github.ReviewComment{}
	for _, c := range comments {
		commentByID[c.ID] = c
	}
	drafts := map[int]bool{}
	relayed := map[int]bool{}
	for _, l := range task.Links {
		switch l.Kind {
		case model.LinkDraftReview:
			if id, err := strconv.Atoi(l.Target); err == nil && id != 0 {
				drafts[id] = true
			}
		case model.LinkRelayedReviewThread:
			if rt, ok := model.ParseRelayedReviewThread(l.Target); ok {
				relayed[rt.CommentID] = true
			}
		}
	}

	count := 0
	seenAuthors := map[string]bool{}
	var authors []string
	for _, th := range threads {
		if th.Resolved {
			continue
		}
		count++
		author := "someone"
		if len(th.CommentIDs) > 0 {
			root := commentByID[th.CommentIDs[0]]
			if root.User != "" {
				author = root.User
			}
			// If a person commented inside a thread opened by the bot, attribute
			// the human participant.
			if relayed[root.ID] || (root.ReviewID != 0 && drafts[root.ReviewID]) {
				for _, cid := range th.CommentIDs {
					if c, ok := commentByID[cid]; ok && c.User != "" && c.User != root.User && !strings.HasSuffix(c.User, "[bot]") {
						author = c.User
						break
					}
				}
			}
		}
		if !seenAuthors[author] {
			seenAuthors[author] = true
			authors = append(authors, author)
		}
	}
	return count, authors
}

// relayedReviewThreadsSection is the prompt paragraph instructing an owner's
// decision run to resolve each grain-relayed review thread once handled.
func relayedReviewThreadsSection(task model.Task) string {
	if _, held := model.ReviewQuestionsOf(task); held {
		return ""
	}
	relayed := model.RelayedReviewThreadsOf(task)
	var decided []model.RelayedReviewThread
	for _, rt := range relayed {
		if rt.Decided {
			decided = append(decided, rt)
		}
	}
	if len(decided) == 0 {
		return ""
	}
	var items []string
	for _, rt := range decided {
		items = append(items, fmt.Sprintf("comment %d (thread `%s`)", rt.CommentID, rt.ThreadID))
	}
	return fmt.Sprintf(
		"\n\nOnce you have handled each finding from the review (%s) -- whether by pushing a "+
			"fix or by carrying out the owner's decision to keep the code as is -- reply to its "+
			"review comment with one line saying what was done (`reply_to_pull_request_comment` or "+
			"`grain pr reply`) and resolve the review thread (`resolve_review_thread` or `grain pr resolve`).",
		strings.Join(items, ", "))
}
