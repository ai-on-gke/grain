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
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// reviewFinding is one add_review_comment call: what the run wants said,
// and -- when it gave both path and line -- where on the diff it wants it
// said. The tool refuses one without the other, so Located is the whole
// of the distinction between an inline comment and a general remark.
type reviewFinding struct {
	Body    string
	Path    string
	Line    int
	Located bool
}

// reviewFindings recovers every non-error add_review_comment call from a
// finished run, in the order the run made them -- the same recovery off
// agent.Result.ToolCalls that firstToolCallArg does for the hatches that
// only ever fire once, except that every call counts here: a review is a
// list of findings, not one answer.
//
// A call whose arguments do not survive the trip (an empty body, a path
// with no line) is dropped rather than guessed at. pkg/mcp's own handler
// already refused those with IsError set, so what reaches here in that
// shape is a client that made the call some other way.
func reviewFindings(result *agent.Result) []reviewFinding {
	var out []reviewFinding
	for _, c := range result.ToolCalls {
		if c.Name != "add_review_comment" || c.IsError {
			continue
		}
		body, _ := c.Arguments["body"].(string)
		if strings.TrimSpace(body) == "" {
			continue
		}
		finding := reviewFinding{Body: body}
		path, _ := c.Arguments["path"].(string)
		path = strings.TrimSpace(path)
		line, hasLine := argInt(c.Arguments["line"])
		if path != "" && hasLine && line > 0 {
			finding.Path, finding.Line, finding.Located = path, line, true
		}
		out = append(out, finding)
	}
	return out
}

// withProxyReviewCalls is result with the add_review_comment calls the
// run made from inside its sandbox (grain pr review-comment, which the
// sandbox proxy keeps per run: model.Store.TakeProxyToolCalls) appended
// to the ones it made as tool calls, so that both are relayed as one
// review. result itself is left as it is: its ToolCalls are the run's
// transcript, counted elsewhere, and the proxy has already counted its
// own.
func withProxyReviewCalls(store *model.Store, runID string, result *agent.Result) *agent.Result {
	if store == nil || result == nil || runID == "" {
		return result
	}
	var extra []agent.ToolCall
	for _, c := range store.TakeProxyToolCalls(runID) {
		if c.Tool == "add_review_comment" {
			extra = append(extra, agent.ToolCall{Name: c.Tool, Arguments: c.Arguments})
		}
	}
	if len(extra) == 0 {
		return result
	}
	merged := *result
	merged.ToolCalls = append(append([]agent.ToolCall(nil), result.ToolCalls...), extra...)
	return &merged
}

// argInt reads a tool argument that is meant to be a whole number. JSON
// has one number type, so a line arrives as a float64 through an ordinary
// decode and as a json.Number through a decoder that was asked to keep
// the literal -- both are the same integer, and a relay that understood
// only one of them would drop half the inline comments an agent wrote.
func argInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// relayReviewFeedback turns a run's add_review_comment calls into a draft
// review on the pull request that run was reviewing, and reports how many
// findings it posted there.
//
// This is the hatch that used to be relayed nowhere. ProcessResult's own
// doc comment said why: attaching a review needs a pull request in hand,
// and no dispatch had one. A review task does (grain/task-284): it is
// filed against a task whose work is already proposed in a pull request,
// it carries model.LinkProposedBy back to that task, and that task
// carries model.LinkFixes to the pull request itself. So the pull request
// is two links away from the run, and the findings have somewhere to go.
//
// A draft, not a submitted review, for the reason pkg/github's own
// package doc comment gives and this call does not revisit: grain's
// credential wrote the change under review, so a submitted review from it
// would be grain approving its own work. What a draft costs is
// visibility -- GitHub keeps a PENDING review to the credential that
// created it until a human opens it on github.com and submits it -- and
// that cost is paid twice over below: the same findings are relayed into
// the reviewed task's own conversation, in grain's UI, which is where the
// person the feedback is for is actually looking. The draft is what makes
// them line-anchored on the diff; the comment is what makes them read.
//
// A run that made these calls with no pull request to attach them to --
// an ordinary task's run reaching for the wrong tool, a review whose
// parent has since lost its link -- is not silently dropped either: the
// findings go into that run's own task conversation instead. The words
// exist nowhere else once agent.Result is discarded, which is the same
// argument the comment_on_issue relay makes for being unconditional.
//
// Nothing here fails the finish. A draft review GitHub refused is worth
// one log line and the fallback comment; it is not worth losing the pull
// request the same ProcessResult is about to open, or the question the
// same run is about to park on.
func relayReviewFeedback(ctx context.Context, store *model.Store, client github.Client,
	appBot AppBotIdentityFunc, task model.Task, result *agent.Result, now time.Time) (int, error) {

	findings := reviewFindings(result)
	if len(findings) == 0 {
		return 0, nil
	}
	// Read before either hold is dropped below, since dropping one turns
	// auto-merge off: a change that was merging itself, or held under a
	// hold grain would have released, goes through again once its owner
	// has answered (reviewquestions.go).
	wantedAutoMerge, err := reviewedWantsAutoMerge(ctx, store, task)
	if err != nil {
		return 0, err
	}
	// First, and whatever becomes of the findings below: a review that
	// found something must never leave a security hold for a clean
	// review to release (securityhold.go).
	if err := dropSecurityHoldOnFindings(ctx, store, client, task, now); err != nil {
		return 0, err
	}
	if err := dropPathTriggerHoldOnFindings(ctx, store, client, task, now); err != nil {
		return 0, err
	}

	reviewed, ref, ok, err := reviewedPullRequest(ctx, store, task)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, relayFindingsToConversation(ctx, store, task, findings, noPullRequestPreamble(task), now)
	}

	isApp := false
	if appBot != nil {
		_, isApp = appBot(ctx, ref.Repo)
	}
	// Under a GitHub App bot identity (`<slug>[bot]`) nobody can log into
	// github.com to submit a PENDING draft review, and an external pull
	// request review (`reviewed == nil`) has no internal task conversation
	// for its author to read. Submit the review with COMMENT when the
	// client supports ReviewSubmitter; keep draft reviews for PAT/ambient
	// credentials reviewing grain's own tasks.
	submitted := false
	var reviewID int
	if submitter, canSubmit := client.(github.ReviewSubmitter); canSubmit && (isApp || reviewed == nil) {
		submitted = true
		reviewID, err = submitter.SubmitReview(ref.Repo.Owner, ref.Repo.Name, ref.Number,
			github.ReviewEventComment, submittedReviewBody(task, findings), inlineComments(findings))
	} else {
		reviewID, err = client.CreateReview(ref.Repo.Owner, ref.Repo.Name, ref.Number,
			draftReviewBody(task, findings), inlineComments(findings))
	}
	if err != nil {
		if reviewed != nil && hasBaseRepairBypass(*reviewed) {
			if err := relayFindingsToConversation(ctx, store, task, findings,
				fmt.Sprintf("Posting these findings as a review on %s failed (%v), so they are "+
					"recorded here instead:", ref, err), now); err != nil {
				return 0, err
			}
			return len(findings), fileBaseRepairReviewFollowUp(ctx, store, client, task, *reviewed, ref, findings, 0, now)
		}

		log.Printf("orchestrator: task %s could not post its %d review finding(s) as a review on %s (%v); "+
			"relaying them into the task's conversation instead", task.ID, len(findings), ref, err)
		return 0, relayFindingsToConversation(ctx, store, task, findings,
			fmt.Sprintf("Posting these findings as a review on %s failed (%v), so they are "+
				"recorded here instead:", ref, err), now)
	}

	if err := noteFindingsOnReview(ctx, store, task, ref, findings, submitted, now); err != nil {
		return 0, err
	}
	if reviewed == nil {
		return len(findings), nil
	}
	if hasBaseRepairBypass(*reviewed) {
		return len(findings), fileBaseRepairReviewFollowUp(ctx, store, client, task, *reviewed, ref, findings, reviewID, now)
	}
	return len(findings), noteFindingsOnReviewedTask(ctx, store, task, *reviewed, ref, findings,
		wantedAutoMerge, reviewID, submitted, now)
}

// reviewedWantsAutoMerge reports whether the task review was filed
// against is on its way to merging by itself: auto-merge on, or a
// security or path-trigger hold that a clean review would have handed
// auto-merge back from. A hold only a human may release never carries
// either link (holdForTrustBoundary, applyPathTriggerGate), so such a
// change reads false here and stays a human's to Submit after its
// questions are answered, exactly as it would have after a clean review.
func reviewedWantsAutoMerge(ctx context.Context, store *model.Store, review model.Task) (bool, error) {
	if !isReviewTask(review) {
		return false, nil
	}
	id := linkTarget(review, model.LinkProposedBy)
	if id == "" {
		return false, nil
	}
	reviewed, err := store.GetTask(ctx, id)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading the task %s reviews: %w", review.ID, err)
	}
	if reviewed == nil {
		return false, nil
	}
	if hasPathTriggerHold(*reviewed) &&
		model.TriggerHuman(linkTarget(*reviewed, model.LinkPathTriggerHold)) == model.TriggerHumanRequired {
		return false, nil
	}
	return reviewed.AutoMerge || hasSecurityHold(*reviewed) || hasPathTriggerHold(*reviewed), nil
}

// reviewedPullRequest resolves the pull request task is a review of: the
// task it was filed against (model.LinkProposedBy, written by
// fileReviewTask) and that task's own pull request (model.LinkFixes,
// written by linkPullRequest when the run under review finished).
//
// Only for a review task. LinkProposedBy is also what a propose_task
// proposal carries back to the run that proposed it, and a proposal is
// not a review of anything -- Origin.Reason is what tells the two apart
// (isReviewTask), the same field syncEntry reads to keep a review out of
// its repo's merge queue.
//
// Every way of not finding one is "no pull request", not an error: a
// review whose parent has been deleted, or whose parent's pull request
// link is malformed, is a review whose findings have to land somewhere
// else, and failing the finish over it would lose them along with
// everything else the run did.
func reviewedPullRequest(ctx context.Context, store *model.Store, task model.Task) (
	*model.Task, model.PullRequestRef, bool, error) {

	if !isReviewTask(task) {
		return nil, model.PullRequestRef{}, false, nil
	}
	var parentID string
	for _, l := range task.Links {
		if l.Kind == model.LinkProposedBy {
			parentID = l.Target
			break
		}
	}
	if parentID == "" {
		if ext := linkTarget(task, model.LinkExternalPR); ext != "" {
			ref, err := model.ParsePullRequestRef(ext)
			if err == nil {
				return nil, ref, true, nil
			}
		}
		return nil, model.PullRequestRef{}, false, nil
	}
	reviewed, err := store.GetTask(ctx, parentID)
	if err != nil {
		return nil, model.PullRequestRef{}, false,
			fmt.Errorf("orchestrator: reading the task %s reviews: %w", task.ID, err)
	}
	if reviewed == nil {
		return nil, model.PullRequestRef{}, false, nil
	}
	for _, l := range reviewed.Links {
		if l.Kind != model.LinkFixes {
			continue
		}
		ref, err := model.ParsePullRequestRef(l.Target)
		if err != nil {
			continue
		}
		return reviewed, ref, true, nil
	}
	return nil, model.PullRequestRef{}, false, nil
}

// noPullRequestPreamble says why findings ended up in a conversation
// rather than on a diff, and the two reasons are not the same thing to
// whoever reads them. An ordinary task's run reached for a tool meant for
// a review, which is worth knowing but is nobody's bug; a review with no
// pull request behind it is something that should not happen, and saying
// only "this run is not reviewing a pull request" about it would hide it.
func noPullRequestPreamble(task model.Task) string {
	if isReviewTask(task) {
		return "This run recorded review feedback, but grain could not find the pull request " +
			"it is reviewing -- the task it was filed against may have lost its own pull " +
			"request link. The findings are here instead:"
	}
	return "This run recorded review feedback, but it is not reviewing a pull request, so " +
		"there was nothing to attach a review to. The findings are here instead:"
}

// inlineComments is the half of the findings GitHub can anchor to the
// diff. The rest are in the review's own body (draftReviewBody), because
// a comments[] entry with no path is a request GitHub rejects outright --
// and a rejected call would take the located findings down with it.
func inlineComments(findings []reviewFinding) []github.NewReviewComment {
	var out []github.NewReviewComment
	for _, f := range findings {
		if !f.Located {
			continue
		}
		out = append(out, github.NewReviewComment{Path: f.Path, Line: f.Line, Body: f.Body})
	}
	return out
}

// draftReviewBody is what the review itself says, above the inline
// comments: who wrote it and why it is a draft, then any finding that
// named no line of its own.
//
// The provenance line is grain's rather than the agent's on purpose. A
// draft review shows up under the account grain's credential belongs to,
// which is the same account that pushed the change being reviewed, and a
// human opening it is owed the sentence that tells those two apart.
func draftReviewBody(task model.Task, findings []reviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review from grain task %s. This is a draft: nobody but grain's own "+
		"credential sees it until you submit it here on GitHub.", task.ID)

	var general []reviewFinding
	for _, f := range findings {
		if !f.Located {
			general = append(general, f)
		}
	}
	if len(general) > 0 {
		b.WriteString("\n\n")
		b.WriteString(findingsList(general))
	}
	return b.String()
}

// submittedReviewBody is the review body when grain posts a submitted
// review directly on GitHub (as a GitHub App bot or on an external PR).
func submittedReviewBody(task model.Task, findings []reviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review from grain task %s.", task.ID)

	var general []reviewFinding
	for _, f := range findings {
		if !f.Located {
			general = append(general, f)
		}
	}
	if len(general) > 0 {
		b.WriteString("\n\n")
		b.WriteString(findingsList(general))
	}
	return b.String()
}

// findingsList renders findings as one markdown bullet each, prefixed
// with the line they are about when they name one. Multi-line bodies are
// indented under their own bullet so a finding that quotes code is still
// one item rather than the list ending at its first newline.
func findingsList(findings []reviewFinding) string {
	var b strings.Builder
	for _, f := range findings {
		body := strings.TrimSpace(f.Body)
		if f.Located {
			body = fmt.Sprintf("`%s:%d` -- %s", f.Path, f.Line, body)
		}
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(body, "\n", "\n  "))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// relayFindingsToConversation is where findings go when no draft review
// can hold them: the conversation of the task whose run wrote them,
// attributed as grain relaying an agent, exactly like the closing comment
// beside it.
func relayFindingsToConversation(ctx context.Context, store *model.Store, task model.Task,
	findings []reviewFinding, preamble string, now time.Time) error {

	body := preamble + "\n\n" + findingsList(findings)
	if _, err := relayComment(ctx, store, task, body, now); err != nil {
		return fmt.Errorf("orchestrator: recording %s's review findings: %w", task.ID, err)
	}
	return nil
}

// noteFindingsOnReview records, in the review task's own conversation,
// that its findings went to GitHub -- so the review's own thread says
// what the run produced rather than reading as a run that did nothing.
// It is the review machinery talking about itself, so it is attributed to
// the reviewer principal rather than relayed on the agent's behalf.
func noteFindingsOnReview(ctx context.Context, store *model.Store, task model.Task,
	ref model.PullRequestRef, findings []reviewFinding, submitted bool, now time.Time) error {

	var body string
	if submitted {
		body = fmt.Sprintf("Posted %s as a review on %s:\n\n%s",
			countOf(len(findings), "finding"), ref, findingsList(findings))
	} else {
		body = fmt.Sprintf(
			"Posted %s as a draft review on %s. A draft is visible only to grain's own "+
				"credential until somebody submits it on GitHub, so the same findings are on "+
				"the task under review as well:\n\n%s",
			countOf(len(findings), "finding"), ref, findingsList(findings))
	}
	return reviewComment(ctx, store, task.ID, body, now)
}

// noteFindingsOnReviewedTask is the other half, and the half that decides
// something: the change under review stops merging by itself.
//
// A review is dispatched to fix what it finds, and its own branch merges
// back into the branch under review for exactly that (fileReviewTask).
// What reaches this function is the remainder -- the findings it wrote
// down instead of fixing, which are by construction the ones it judged
// not to be its to make: a design question, a trade-off, something that
// needs whoever asked for the change. Letting the change merge itself
// past those the moment the review's own pull request lands would answer
// every one of them with "ship it", and would do so within a cycle or
// two, while the draft review sits unread.
//
// So auto-merge is withdrawn, and the task is held as a question to its
// owner (model.LinkReviewQuestions) rather than as a bare Submit: the
// findings are put to them on the task as open questions, each with the
// reviewer's recommended answer. Their reply -- here or on the review --
// sends the task back to the queue with the questions and the answer in
// its prompt (SyncReviewQuestions), and that run fixes what was decided
// and goes through the way the change would have after a clean review:
// wantedAutoMerge, read before any hold was dropped, is what it gets
// back. Submit is still "ship as is". model.AwaitsSubmit reads a
// completed task with a pull request and no auto-merge as
// 'awaiting_submit', and the inbox and task pane show one carrying the
// link as a review that needs a decision.
//
// Nothing else in its repo waits on that person. Auto-merge is what puts
// a task in the queue at all (isQueueMember), so withdrawing it drops
// this one out rather than parking it at the head: whatever was behind it
// moves up the same cycle. It is deliberately not blockMergeQueue: that
// gives up the queue position and then merges anyway on the next clean
// cycle, which is the opposite of what findings ask for. And nothing here
// starts a clock, because the thing being waited on is a person who has
// been asked, in their own conversation.
//
// draftID, when the draft review was posted, is recorded so the review
// thread reconciler (reviewthreads.go) never reads the draft's own inline
// comments as threads somebody else opened.
//
// A task that has already closed -- its pull request merged or closed
// while the review was still running -- is left alone: there is nothing
// left to hold, and a question about a merge that already happened would
// only mislead. The draft review is still posted, since a finding about
// code that just landed is still worth having.
func noteFindingsOnReviewedTask(ctx context.Context, store *model.Store, review, reviewed model.Task,
	ref model.PullRequestRef, findings []reviewFinding, wantedAutoMerge bool, draftID int,
	submitted bool, now time.Time) error {

	closed, err := taskClosed(ctx, store, reviewed.ID)
	if err != nil {
		return err
	}
	if closed {
		return nil
	}

	kind := "a draft review"
	if submitted {
		kind = "a review"
	}
	// The question first, so the hold can name it: a human comment newer
	// than this one is the owner's answer (reviewquestions.go).
	body := fmt.Sprintf(
		"**Review needs your decision.** The review of this change (task %s) fixed what it "+
			"could and left %s on %s that it could not settle without you:\n\n%s\n\n"+
			"Reply here (or on task %s) with your decision, and grain sends another run on "+
			"this task to carry it out -- it fixes what you decided and the change then goes "+
			"through the way it would have after a clean review. Or ship it as it stands "+
			"with **Ship as is** (Submit), leaving these open. The same questions are on %s as %s, "+
			"on the lines they are about.",
		review.ID, countOf(len(findings), "open question"), ref, findingsList(findings),
		review.ID, ref, kind)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    reviewed.ID,
		Author:    model.Attribution{Actor: reviewer},
		Body:      body,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: putting %s's review questions to its owner: %w", reviewed.ID, err)
	}

	questions := model.ReviewQuestions{
		ReviewTaskID: review.ID, AfterCommentID: commentID, RestoreAutoMerge: wantedAutoMerge,
	}
	if err := store.UpdateTask(ctx, reviewed.ID, func(t *model.Task) error {
		t.AutoMerge = false
		t.Links = withoutLinks(t.Links, model.LinkReviewQuestions)
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewQuestions, Target: questions.Target()})
		if draftID != 0 {
			t.Links = append(t.Links, model.Link{Kind: model.LinkDraftReview, Target: strconv.Itoa(draftID)})
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: holding %s for its review's questions: %w", reviewed.ID, err)
	}
	log.Printf("orchestrator: task %s's review (task %s) left %d open question(s) on %s, so %s "+
		"waits on its owner's decision", reviewed.ID, review.ID, len(findings), ref, reviewed.ID)
	return nil
}

// countOf is "1 finding" / "3 findings" -- a count read by a human,
// which is why it is not fmt's own "%d finding(s)".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
