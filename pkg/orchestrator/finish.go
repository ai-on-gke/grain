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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/forkhead"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// firstToolCallArg returns the first argument named key from the first
// non-error call to name in result, and whether one was found. Reading
// straight off agent.Result.ToolCalls rather than a mcp.MockSink is
// deliberate: a Framework.Run constructs and discards its own
// MockSink internally (its own doc comment says so), so ToolCalls is the
// only seam a caller outside that package has -- the same one that
// tests/e2e/harness_test.go's askedQuestion/pushedOK helpers use.
//
// name is matched exactly, against the bare tool name pkg/mcp registers
// ("ask_question", not "mcp__grain-sandbox__ask_question"). That is
// agent.ToolCall.Name's own documented contract, enforced by each
// Framework putting a reported name through mcp.BareToolName -- both CLIs
// namespace an MCP server's tools by the key they loaded it under, and a
// Framework that skipped that step would match nothing here and silently
// cost every run its question and its closing comment (mcp.BareToolName's
// own doc comment has the history). Each framework package has a test on
// that invariant rather than this function tolerating both spellings: a
// prefix leaking this far would also be in the log line and the
// no_action detail below, so the place to stop it is where it is read.
func firstToolCallArg(result *agent.Result, name, key string) (string, bool) {
	for _, c := range result.ToolCalls {
		if c.Name != name || c.IsError || c.Relayed {
			continue
		}
		v, ok := c.Arguments[key].(string)
		return v, ok
	}
	return "", false
}

// lastToolCallArg is firstToolCallArg from the other end: the argument
// named key on the *last* non-error call to name.
//
// One tool is read this way -- write_handover -- and the reason is in
// that tool's own doc comment: a handover is a document a run revises as
// it works out what the thread was actually about, so the draft it threw
// away is not the one to carry forward. Everywhere else the first call
// wins, because everywhere else a second call is a run repeating itself
// and the first thing it said is what it meant.
func lastToolCallArg(result *agent.Result, name, key string) (string, bool) {
	value, found := "", false
	for _, c := range result.ToolCalls {
		if c.Name != name || c.IsError || c.Relayed {
			continue
		}
		if v, ok := c.Arguments[key].(string); ok {
			value, found = v, true
		}
	}
	return value, found
}

// firstToolCallArgs returns all arguments from the first successful call.
func firstToolCallArgs(result *agent.Result, name string) map[string]any {
	for _, c := range result.ToolCalls {
		if c.Name == name && !c.IsError && !c.Relayed {
			return c.Arguments
		}
	}
	return nil
}

// proposedTaskCalls returns every non-error propose_task call's arguments.
func proposedTaskCalls(result *agent.Result) []map[string]any {
	var out []map[string]any
	for _, c := range result.ToolCalls {
		if c.Name == "propose_task" && !c.IsError && !c.Relayed {
			out = append(out, c.Arguments)
		}
	}
	return out
}

// isTruncated reports whether a comment string ends with an ellipsis (… or ...) or is incomplete.
func isTruncated(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, "…") || strings.HasSuffix(s, "...")
}

// effectiveCommentOnIssue returns the comment to relay from a comment_on_issue tool call.
// Antigravity (Gemini via agy) and other agents frequently provide abbreviated
// summaries in the comment_on_issue tool call argument (often ending in …),
// while generating their complete report in the assistant conversational text
// channel (result.FinalText).
//
// When recording the task's comment, we use the final assistant text if
// comment_on_issue is truncated, ends with an ellipsis (… / ...), or when
// FinalText contains the more complete report.
func effectiveCommentOnIssue(result *agent.Result) (string, bool) {
	if result == nil {
		return "", false
	}
	comment, ok := firstToolCallArg(result, "comment_on_issue", "comment")
	comment = strings.TrimSpace(comment)
	finalText := strings.TrimSpace(result.FinalText)

	if !ok || comment == "" {
		return "", false
	}
	if isTruncated(comment) && finalText != "" {
		return finalText, true
	}
	if finalText != "" && len(finalText) > len(comment) {
		return finalText, true
	}
	return comment, true
}

// ProcessResult is what a finished run's tool calls turn into: a comment
// on the task, a pull request, a proposed task -- v1's own "core.py
// writes a file, then turns it into a real effect" split, done here in
// one step since nothing else needs the intermediate file.
//
// Everything a run says now lands in the store. It used to land on the
// task's own GitHub issue, which is why this needed a task repo and an
// ExternalRef to relay to at all. A task has no issue any more (README,
// "Input is a model update, not a GitHub issue"), so a question is a
// model.Comment, a closing remark is a model.Comment, and a proposed task
// is a model.Task with no Approval. GitHub is still reached for the one
// thing that is genuinely GitHub's: the branch a run pushed, and the pull
// request opened for it.
//
// Order matters. A question ends the run's turn by contract (mcp's own
// ask_question doc comment: "after calling this, do not take any further
// actions") so it is what the task ends up parked on, and is returned on
// before anything else -- answering it is the whole reason the run
// stopped, and no PR exists yet for an ask_question turn to have opened
// regardless. A request_secret call parks the task on the same terms and
// through the same field, and is relayed in the same step
// (relayParkingCalls), which is what keeps a run that made both calls
// from having one of them silently dropped. Proposed tasks are relayed independent of how the run
// otherwise ended, since v1's own propose_task can accompany other work
// rather than replacing it.
//
// A comment_on_issue call is therefore relayed before either of those
// two returns rather than after both of them, which is what a run that
// commented *and* did something else used to lose. Both combinations are
// ones the tools themselves invite: comment_on_issue's own description
// says a pull request is opened for pushed commits "regardless of whether
// you also call this", and a run that reports what it found before asking
// what to do next is saying two different things, not the same thing
// twice. Under the old order the question's return and the pushed
// branch's return each came first, and the comment was dropped silently
// and irrecoverably -- agent.Result is never persisted, so the words
// existed nowhere else, and neither path reaches the "nothing to act on"
// log line that would at least have recorded that the call happened.
// Relaying costs one row and puts the words in front of the human reading
// the task and the redispatched run reading the thread back (run.go's
// commentThreadSection).
//
// Only the relaying moved. What the comment means for the task's state is
// still decided by what else the run did: it is a closing note that
// completes the task only when there was no question and no branch, and
// parking is still keyed to the question, whose comment id is the one
// PendingQuestionCommentID names.
//
// add_review_comment is relayed too, and by the same rule: a run's words
// belong wherever a human will read them. It needed a pull request in
// hand to attach a draft review to, which nothing dispatched by grain
// used to have; a review task does (grain/task-284), two links away --
// see relayReviewFeedback, which posts the draft review, puts the same
// findings on the task under review, and takes that task off automatic
// merge so somebody reads them before the change lands. A run that made
// the calls with no pull request behind it has them relayed into its own
// conversation rather than dropped.
//
// runID is d.RunID, the very run result came from -- needed only for the
// "nothing to act on" ending below, to correct that run's own outcome
// (see the comment there for why RunDispatch's own guess is not good
// enough). Every other ending here already has an outcome RunDispatch got
// right without ProcessResult's help.
func ProcessResult(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, result *agent.Result, runID string, now time.Time) error {

	return processResult(ctx, store, nil, client, nil, task, result, runID, now)
}

// processResult is ProcessResult with reach, which the reconcile loop
// passes (Deps.GitReach) so a proposal its team cannot reach is parked
// as it is filed (relayProposedTasks), and appBot so reviews posted as a
// GitHub App bot use submitted reviews rather than invisible PENDING drafts.
func processResult(ctx context.Context, store *model.Store, reach *gitproxy.Reach, client github.Client,
	appBot AppBotIdentityFunc, task model.Task, result *agent.Result, runID string, now time.Time) error {

	return processResultFrom(ctx, store, reach, client, appBot, task, result, runID, "", now)
}

// processResultFrom is processResult for a run whose task branch was
// already on the remote when it was dispatched, at preRunBranchSHA (""
// when it was not, or when nobody read it at dispatch). That head is what
// lets a nothing_needed run be told apart from one that pushed: see
// branchUnmovedSince.
func processResultFrom(ctx context.Context, store *model.Store, reach *gitproxy.Reach, client github.Client,
	appBot AppBotIdentityFunc, task model.Task, result *agent.Result, runID, preRunBranchSHA string, now time.Time) error {

	if err := relayProposedTasks(ctx, store, reach, client, task, result, now); err != nil {
		return err
	}

	// Relayed up front, before either ending that used to return past it.
	// What a comment_on_issue call means for the task's *state* depends on
	// what else the run did -- a closing note on its own, a remark
	// alongside a pull request, a finding in front of a question -- but
	// the words themselves are the run's own and belong in the
	// conversation on every one of those paths.
	// What the run attached by path, read out of its sandbox as it made
	// each call (mcp.AttachmentFiles) -- the calls' own arguments name
	// the path and carry no bytes.
	staged := stagedAttachments(ctx, store, runID)

	comment, hasComment := effectiveCommentOnIssue(result)
	if hasComment {
		attachments, dropped := mcp.ResponseAttachmentsTolerant(firstToolCallArgs(result, "comment_on_issue"), staged)
		if _, err := relayCommentWithAttachments(ctx, store, task, withDroppedAttachments(comment, dropped), attachments, now); err != nil {
			return fmt.Errorf("orchestrator: posting comment for %s: %w", task.ID, err)
		}
		checkAndRecordStewardReport(ctx, store, task, runID, result, now)
	}

	// nothing_needed's reason is relayed up front for the same reason
	// comment_on_issue's is: what it means for the task's state depends on
	// what else the run did (see finishNothingNeeded), but the words are
	// the run's own on every path -- beside a pull request, in front of a
	// question, or as the closing comment of a task it closes.
	nothingNeeded, hasNothingNeeded := nothingNeededReason(result)
	if hasNothingNeeded {
		if _, err := relayComment(ctx, store, task, nothingNeeded, now); err != nil {
			return fmt.Errorf("orchestrator: posting nothing_needed reason for %s: %w", task.ID, err)
		}
	}

	// Relayed on every path too, and on top of whatever else the run
	// said: a handover is written for the next run on this task, so it
	// matters most on exactly the endings that leave the task open -- a
	// question parked on, a session waiting for a reply, a branch pushed
	// for review. It deliberately does not count as a comment: a closing
	// note completes the task below, and a note whose whole purpose is
	// that the task stays open and gets dispatched again must not.
	proxyResult := withProxyReviewCalls(store, runID, result)
	if err := relayHandover(ctx, store, task, proxyResult, now); err != nil {
		return err
	}

	// Relayed here for the same reason, and before the same two returns:
	// a review's findings are the whole product of the run that wrote
	// them, and a run that reported what it found before parking on a
	// question has said two things rather than one.
	findings, err := relayReviewFeedback(ctx, store, client, appBot, task, proxyResult, now)
	if err != nil {
		return err
	}
	completeExternalReviewOutcome(ctx, client, task, comment, nothingNeeded, findings)
	if err := recordTakeoverReviewResult(ctx, store, task, findings); err != nil {
		return err
	}

	parked, err := relayParkingCalls(ctx, store, task, result, staged, now)
	if err != nil {
		return err
	}
	if parked {
		return nil
	}

	// A run can also park its own task *while it is still running*, which
	// nothing here relays because nothing here has to: the chat tool
	// posts a message into the task's conversation and points
	// Observation.PendingQuestionCommentID at it as it goes
	// (ui.Client.PostAgentMessage), so a session whose last word went
	// unanswered is already parked by the time this runs. It is treated
	// exactly as a relayed ask_question is, and for the same reason:
	// there is a question outstanding, and the person's next message
	// un-parks the task (ui.Client.AddComment) and dispatches a fresh run
	// with the whole thread in front of it.
	//
	// Read rather than inferred, because the two endings below would each
	// be wrong about it. CompletedAt outranks a pending question in
	// model.StateOf, so a chat run that also left a comment_on_issue note
	// would mark a task complete while it waits for an answer -- and a
	// completed task is not dispatched by a reply, so the conversation
	// would stop there with nothing saying why. And a session that talked
	// and pushed nothing is not "a run that produced nothing to act on":
	// it produced the conversation it was dispatched to have.
	waiting, err := parkedOnAQuestion(ctx, store, task.ID)
	if err != nil {
		return err
	}
	if waiting {
		return nil
	}

	// A run that stopped through resume_later -- or wait_for_task's park
	// or resume_at, which are forms of it -- has already made its task
	// wait (ui.Client.ResumeLater: depends-on links, a deferral, or
	// nothing for the next cycle). What is left is to leave the task alone
	// rather than opening a pull request off a branch the next attempt is
	// still going to work on, or completing a task whose work is not
	// done, and to record the run as the agent's own suspension
	// (model.SuspendRequested, recorded as parked), which no failure
	// streak counts and which the next attempt's prompt reads to say what
	// it waited on (parkedResumeSection).
	if susp, ok, err := requestedSuspension(ctx, store, task.ID, result); err != nil {
		return err
	} else if ok {
		susp.RunID, susp.CreatedAt = runID, now
		if err := store.Suspend(ctx, susp); err != nil {
			return fmt.Errorf("orchestrator: recording %s's outcome: %w", task.ID, err)
		}
		return nil
	}

	// A required review of a CL that finished without parking, suspending
	// or leaving a finding has passed: its snapshot is cleared for it
	// (clearCLReview), and the provider's gate reads that.
	if err := clearCLReview(ctx, store, task, findings, now); err != nil {
		return err
	}

	// A branch on the remote is only this run's work if this run put it
	// there. One an earlier attempt pushed -- before parking, say, or
	// before running out of turns -- is still there when a later attempt
	// looks, finds the work already done elsewhere, and says
	// nothing_needed; salvaging it then opened a pull request nobody
	// wanted off a stale branch, and dispatched the task again to repair
	// and merge it (grain/task-208's PR #4489). So nothing_needed wins
	// over a branch whose head has not moved since dispatch, and a branch
	// this run did push still wins over nothing_needed, as the tool's own
	// description promises. A branch with a pull request already open off
	// it is not stale either: that is a change in review, and a run sent
	// back to it -- to answer a reviewer, repair CI, resolve a conflict --
	// that finds nothing more to do must leave it for its owner rather
	// than close it on the agent's word.
	//
	// A review working on the reviewed change's own branch has no branch
	// of its own to salvage or retire: the branch is the reviewed task's,
	// and so is the pull request open off it. What it pushed there is
	// recorded instead (recordReviewerHead), and the run ends below like
	// any other review's.
	_, inPlace := reviewsInPlace(task)
	pushedInPlace := false
	if inPlace {
		if pushedInPlace, err = recordReviewerHead(ctx, store, client, task, now); err != nil {
			return err
		}
	}
	staleBranch := false
	if hasNothingNeeded && !inPlace {
		staleBranch, err = branchUnmovedSince(client, task, preRunBranchSHA)
		if err != nil {
			return err
		}
	}
	if !staleBranch && !inPlace {
		pushedCommit := true
		if preRunBranchSHA != "" && client != nil && task.Target != nil {
			if head, herr := client.GetBranchHead(task.Target.Owner, task.Target.Name, task.BranchName()); herr == nil && head != nil {
				pushedCommit = head.SHA != preRunBranchSHA
			}
		}
		handled, err := salvagePushedBranch(ctx, store, client, task, now)
		if empty, ok := emptyBranch(err); ok {
			// The task has already been told, and parked (noteEmptyBranch).
			// What is left is this run's own row, and it must not say
			// "finish-failed": that word means "try again", which is what
			// this ending exists to stop. no_action is what actually
			// happened -- a run that left nothing anyone can act on -- and
			// returning nil is what keeps noteFinishFailure from writing
			// over it on the way out.
			if serr := store.SetRunOutcome(ctx, runID, "no_action", emptyBranchDetail(empty)); serr != nil {
				return fmt.Errorf("orchestrator: recording %s's outcome: %w", task.ID, serr)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if handled {
			if pushedCommit || hasComment || hasNothingNeeded || (result != nil && (len(result.ToolCalls) > 0 || strings.TrimSpace(result.FinalText) != "")) {
				resolveRelayedThreadsAfterOwnerRun(ctx, store, client, task, proxyResult, pushedCommit)
			}
			return nil
		}
	}

	// Somebody sitting with an interactive task may have said something
	// this run was never shown: posted while it was writing its last
	// answer, or after its chat call had been given up on. Whatever the
	// run did, that message is still waiting, so the task is left ready
	// for the next dispatch rather than completed or closed under it
	// (undeliveredHumanComment, grain/task-1774).
	waitingMessage, err := interactiveMessageWaiting(ctx, store, task)
	if err != nil {
		return err
	}
	relayedInProcess := takeInProcessRelayed(store, runID)
	keepRunning := taskKeepRunning(ctx, store, task)

	// A goal task (model.TaskGoal, docs/design/durable-tasks.md#DT-4) is
	// never completed or closed on its agent's word: a closing comment,
	// a plain finish or nothing_needed ("nothing to do now") rests it on
	// its goal or a message instead (restOnGoal). Its words are relayed as
	// an ordinary comment; reconcileTaskGoals closes it once the goal is
	// met, and a person may close it any time.
	if !waitingMessage && !keepRunning {
		finalText := !relayedInProcess && !hasComment && !hasNothingNeeded && result != nil && strings.TrimSpace(result.FinalText) != "" &&
			!taskPaused(ctx, store, task.ID)
		if relayedInProcess || hasComment || hasNothingNeeded || finalText {
			goal, err := store.TaskGoal(ctx, task.ID)
			if err != nil {
				return err
			}
			if goal != nil {
				if finalText {
					if _, err := relayCommentWithAttachments(ctx, store, task, strings.TrimSpace(result.FinalText), nil, now); err != nil {
						return fmt.Errorf("orchestrator: posting closing comment for %s: %w", task.ID, err)
					}
				}
				_, err := restOnGoal(ctx, store, task, runID, now)
				return err
			}
		}
	}

	// A run that said nothing was needed, and pushed nothing and asked
	// nothing, ends here: its reason is already on the task, and what is
	// left is the task's state and the run's own ending.
	if hasNothingNeeded && !waitingMessage {
		if staleBranch {
			if err := retireStaleBranch(ctx, store, task, now); err != nil {
				return err
			}
		}
		if keepRunning {
			markKeepRunningRequeued(store, task.ID, runID, true)
			return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
				o.CompletedAt = nil
			})
		}
		return finishNothingNeeded(ctx, store, task, runID, now)
	}

	// A run that wound itself up because its task was paused (a graceful
	// pause, reconcilePauses) and left no answer is not finished work: it
	// stopped because it was asked to. Recorded as interrupted -- no
	// strike, nothing completed -- so that resuming the task re-queues it
	// to carry on, with whatever it pushed and its handover in hand.
	if !hasComment && !hasNothingNeeded && taskPaused(ctx, store, task.ID) {
		if err := store.SetRunOutcome(ctx, runID, model.InterruptedOutcome, model.OperatorPausedDetail); err != nil {
			return fmt.Errorf("orchestrator: recording %s's outcome: %w", task.ID, err)
		}
		return nil
	}

	// When comment_on_issue was absent, but the run completed an investigation /
	// non-code task with a final assistant response (and did not push a branch or
	// ask a question), use the final assistant text as the closing note.
	if !relayedInProcess && !hasComment && !hasNothingNeeded && result != nil && strings.TrimSpace(result.FinalText) != "" {
		comment := strings.TrimSpace(result.FinalText)
		if _, err := relayCommentWithAttachments(ctx, store, task, comment, nil, now); err != nil {
			return fmt.Errorf("orchestrator: posting closing comment for %s: %w", task.ID, err)
		}
		hasComment = true
	}

	// Its words are in the conversation; its task stays open for the
	// message it never saw. The run keeps the outcome RunDispatch gave
	// it: it answered what it was given, and the next dispatch -- with
	// the whole thread, the unseen message included, in its prompt -- is
	// what answers the rest.
	if waitingMessage {
		log.Printf("orchestrator: task %s run %s: a message was posted that the run was never shown; "+
			"leaving the task ready to be dispatched for it", task.ID, runID)
		return nil
	}

	if keepRunning {
		markKeepRunningRequeued(store, task.ID, runID, false)
		return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = nil
		})
	}

	// A closing comment ends the task, and so does a review that posted
	// findings: a review dispatched to read a change and write down what
	// is wrong with it has done its work whether or not it also pushed a
	// commit, and the findings are already on the pull request and on the
	// task under review. Left un-completed it would be dispatched again
	// after its backoff to review the same change a second time, and --
	// worse -- it would hold that change's own merge until
	// defaultReviewTaskDeadline ran out and the queue announced that the
	// review had never finished, which by then would be untrue.
	if hasComment || findings > 0 || pushedInPlace {
		return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = &now
			o.RetryRequestedAt = nil
		})
	}

	// Neither a push, a question, nor a closing comment: the run produced
	// nothing to act on (the failScript case in the e2e harness this
	// mirrors). Left running-less and un-observed, it is eligible for
	// another attempt the next time dispatch.Cycle looks at task_ready --
	// task_streak (schema.go) and dispatch.Cycle's own backoff are what
	// keep that bounded now rather than unconditional (bwsalmon/agents#403).
	//
	// That bound only works if this run's own outcome says so: RunDispatch
	// already called FinishRun before ProcessResult ever ran, using
	// outcomeOf's own guess -- "succeeded" the moment the agent made any
	// tool call at all, since RunDispatch has no way to check GitHub or
	// task_observation itself. A run that, say, ran a few shell commands
	// and then gave up without pushing, asking, or leaving a comment would
	// otherwise sit in task_run as a permanent "succeeded" that never
	// counts toward the streak, dodging the very cap this ending exists to
	// feed. Overwriting it here, now that a push/question/comment has
	// actually been ruled out, is what keeps task_run's own outcome
	// column meaning what a human reading `grain get` would expect it to.
	// Log what the run *did* do, because nothing else does. agent.Result
	// carries FinalText and ToolCalls, neither of which is persisted --
	// SetRunOutcome stores an outcome string and nothing more -- and
	// neither this package nor the agent framework logs them. So a run
	// ending here left no record at all of the agent's behaviour, and
	// "finished without pushing a branch, asking a question, or leaving a
	// closing comment" was the entire evidence available for diagnosing
	// why: it says what did not happen and nothing about what did.
	//
	// This branch specifically is where that hurts. Every other ending
	// leaves an artefact a human can read -- a pull request, a question, a
	// comment. This one is defined by leaving none.
	//
	// Tool names are a fixed vocabulary and safe to log in full. FinalText
	// is model output and is bounded hard: an agent that read a file is
	// free to quote it back, and a sandbox working directory can hold a
	// .git-credentials. A truncated prefix is enough to tell "I could not
	// find anything to do" from "I hit an API error" without turning the
	// journal into a transcript store.
	log.Printf("orchestrator: task %s run %s ended with no action: %d tool call(s)%s; final text (%d bytes): %s",
		task.ID, runID, len(result.ToolCalls), toolCallSummary(result), len(result.FinalText),
		truncate(result.FinalText, 500))

	if err := store.SetRunOutcome(ctx, runID, "no_action", noActionDetail(result)); err != nil {
		return fmt.Errorf("orchestrator: recording %s's outcome: %w", task.ID, err)
	}
	return nil
}

// interactiveMessageWaiting reports whether a person has said something
// on an interactive task that no run has been given
// (undeliveredHumanComment). Always false for a background task, whose
// mid-run comments are folded into its next dispatch whenever that
// comes, and for a task with no delivery mark recorded yet.
func interactiveMessageWaiting(ctx context.Context, store *model.Store, task model.Task) (bool, error) {
	if !task.Interactive {
		return false, nil
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's conversation: %w", task.ID, err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's observation: %w", task.ID, err)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's runs: %w", task.ID, err)
	}
	pending, _ := undeliveredHumanComment(comments, obs, runs)
	return pending != nil, nil
}

// nothingNeededReason is the reason the run's first successful
// nothing_needed call gave, if it made one.
func nothingNeededReason(result *agent.Result) (string, bool) {
	if result == nil {
		return "", false
	}
	reason, ok := firstToolCallArg(result, "nothing_needed", "reason")
	reason = strings.TrimSpace(reason)
	return reason, ok && reason != ""
}

// finishNothingNeeded ends a task whose run called nothing_needed and did
// nothing else that outranks it.
//
// The rule, which the tool's own description also states: a question the
// run asked, or a conversation it is waiting on, parks the task as it
// would have anyway, and a branch it pushed goes through the ordinary pull
// request flow. ProcessResult returns on both of those before reaching
// here, so nothing_needed never discards work -- its reason is simply
// posted beside it, and the run keeps the outcome RunDispatch gave it.
// "Pushed" means pushed by this run: a branch an earlier attempt left,
// whose head has not moved since dispatch, does not outrank it
// (branchUnmovedSince) unless a pull request is open off it, and is
// noted on the task on the way here (retireStaleBranch).
//
// Only a run with nothing else to show is recorded as
// model.EndingNothingNeeded, and what happens to its task is the task's
// own choice (model.Task.NothingNeededAction): by default it is closed
// outright, so a deliberate no-op never waits in anybody's awaiting_ack
// inbox beside reports that do need reading; NothingNeededAck completes
// it there instead, the way a closing comment does.
func finishNothingNeeded(ctx context.Context, store *model.Store, task model.Task,
	runID string, now time.Time) error {

	if err := store.SetRunOutcome(ctx, runID, model.NothingNeededOutcome,
		"the agent found nothing needed"); err != nil {
		return fmt.Errorf("orchestrator: recording %s's outcome: %w", task.ID, err)
	}
	if task.NothingNeededAction.Resolved() == model.NothingNeededAck {
		return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = &now
			o.RetryRequestedAt = nil
		})
	}
	if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.ClosedAt = &now
		o.RetryRequestedAt = nil
	}); err != nil {
		return err
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      model.NothingNeededClosedNote,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: noting %s's close: %w", task.ID, err)
	}
	return nil
}

// branchUnmovedSince is whether task's branch is still at sha, the head
// it had when the run was dispatched -- that is, whether the run pushed
// nothing to it -- with no pull request open off it. An empty sha is a
// branch nobody read at dispatch, or one that was not there yet, and says
// nothing about who pushed it, so it is never unmoved: the caller then
// salvages as it always has, and a push this run may have made is not
// thrown away on a guess. A branch with a pull request open off it is a
// change somebody may be reviewing, and is never stale either: salvage
// finds that pull request again and leaves the task waiting on it, as it
// did before nothing_needed could outrank a branch at all.
func branchUnmovedSince(client github.Client, task model.Task, sha string) (bool, error) {
	if sha == "" || client == nil || task.Target == nil {
		return false, nil
	}
	head, err := client.GetBranchHead(task.Target.Owner, task.Target.Name, task.BranchName())
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading %s's branch head: %w", task.ID, err)
	}
	if head == nil || head.SHA != sha {
		return false, nil
	}
	pr, err := client.FindOpenPullRequestForBranch(task.Target.Owner, task.Target.Name, task.BranchName())
	if err != nil {
		return false, fmt.Errorf("orchestrator: looking for a pull request open off %s's branch: %w", task.ID, err)
	}
	return pr == nil, nil
}

// retireStaleBranch is what is left of an earlier attempt's branch once a
// later run has said nothing_needed over it without pushing: the task is
// told which branch was left behind. The branch itself is not deleted:
// its commits are an earlier attempt's real work, and whether anybody
// wants them is not grain's to decide. No pull request is open off it
// (branchUnmovedSince), so there is none to close.
func retireStaleBranch(ctx context.Context, store *model.Store, task model.Task, now time.Time) error {
	note := fmt.Sprintf("This run found nothing needed and pushed nothing, so grain did not open a pull "+
		"request off `%s`, the branch an earlier attempt of task %s left on %s. The branch is left "+
		"in place.", task.BranchName(), task.ID, task.Target)
	return noteOnTask(ctx, store, task.ID, note, now)
}

// salvagePushedBranch turns a branch the run left on GitHub into a pull
// request, reporting whether there was one to turn. It is the only part
// of a run's outcome that survives the run itself: the sandbox is
// recreated and agent.Result is never persisted, but a pushed branch is
// on GitHub whatever happened to the process that pushed it.
//
// Three callers need exactly this and used to have two-and-a-half copies
// of it: ProcessResult for a run that ended cleanly, recoverRun for one
// whose process died, and runOne for one whose framework returned an
// error -- the case that had no copy at all, and stranded every branch a
// run pushed before it ran out of turns.
//
// The close re-check is not incidental. task was read at the top of this
// cycle, before the run finished, so it cannot see a close that landed
// while the run was still live; re-reading the observation here is what
// model/state.go's StateOf precedence (ClosedAt outranks a live run)
// means for a run that already pushed. Nobody wants a closed task's work
// merged, so nothing is opened here: the branch is left pushed, and left
// unopened unless the run itself already opened a pull request for it.
// It still counts as handled either way -- there was a branch, and the
// decision about it has been made.
//
// That qualifier is what a run calling open_pull_request (pullrequest.go)
// mid-flight changed, and it is a real case: the close can land after the
// tool call and before this runs, on all three of this function's callers.
// The pull request then already exists on GitHub and is already linked to
// the task, and this function undoes neither. Nothing in grain closes a
// pull request, and the commits on the branch are real work whichever way
// the human went on the task, so it is left open and left linked, for a
// person to merge or close by hand.
//
// What that leaves behind is bounded, which is why it is the choice made:
// grain itself never touches that pull request again.
// Store.OpenPullRequestLinks only returns links belonging to a task whose
// run is over and which has not closed, so a closed task never becomes a
// merge queue entry and SyncPullRequests never so much as reads it. The thing "nobody wants a closed task's work merged" is protecting
// is protected either way; all that remains is a pull request visible on
// a task somebody just closed.
//
// What remains is now also *said*, on the task and on the pull request
// itself, rather than only being true: noteOrphanedPullRequests, on both
// sides of the finish -- for a close this function found, and for one
// that landed just after it looked. The decision is unchanged (nothing is
// closed, nothing is merged, nothing is unlinked); what changed is that
// nobody has to work it out for themselves from a pull request that has
// simply gone quiet.
//
// It is also the answer already given to the same question one moment
// later in a task's life -- a task closed after its pull request was
// opened at the finish keeps that pull request open and unmerged forever
// (tests/e2e's TestClosingATaskWithAnOpenPullRequestDropsItFromTheMerge
// QueueForGood). A run opening its own pull request only moved when that
// moment can arrive. pullrequest_test.go's
// TestClosingATaskLeavesThePullRequestItsRunAlreadyOpenedAlone pins both
// halves of it here.
func salvagePushedBranch(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) (bool, error) {

	if task.Target == nil {
		return false, nil
	}
	if _, inPlace := reviewsInPlace(task); inPlace {
		// The branch is the reviewed task's, and so is its pull request
		// (recordReviewerHead is this review's ending instead).
		return false, nil
	}
	pushed, err := targetBranchExists(client, *task.Target, task.BranchName())
	if err != nil {
		return false, fmt.Errorf("orchestrator: checking %s's branch: %w", task.ID, err)
	}
	if !pushed {
		return false, nil
	}
	closed, err := taskClosed(ctx, store, task.ID)
	if err != nil {
		return true, err
	}
	if closed {
		return true, noteOrphanedPullRequests(ctx, store, client, task.ID, now)
	}
	if rc, ok := client.(*github.RESTClient); ok {
		if src, ok := rc.Tokens.(github.GitOnlySource); ok {
			if reason := src.GitOnly(task.Target.StoredOwner(), task.Target.Name); reason != "" {
				host := task.Target.GitHost()
				note := fmt.Sprintf("Branch %s pushed to %s; no pull request opened because %s is a plain git host.", task.BranchName(), task.Target, host)
				if !strings.Contains(reason, "a plain git host") {
					note = fmt.Sprintf("Branch %s pushed to %s; no pull request opened: %s.", task.BranchName(), task.Target, reason)
				}
				if err := noteOnTask(ctx, store, task.ID, note, now); err != nil {
					return true, err
				}
				if taskKeepRunning(ctx, store, task) {
					markKeepRunningRequeued(store, task.ID, "", false)
					return true, observeField(ctx, store, task.ID, now, func(o *model.Observation) {
						o.CompletedAt = nil
					})
				}
				return true, observeField(ctx, store, task.ID, now, func(o *model.Observation) {
					o.CompletedAt = &now
					o.RetryRequestedAt = nil
				})
			}
		}
	}
	if err := finishWithPullRequest(ctx, store, client, task, now); err != nil {
		// Work that already landed is an ending, not a failure: no pull
		// request, the task told and marked done (finishAlreadyLanded).
		if landed, ok := alreadyLanded(err); ok {
			if taskKeepRunning(ctx, store, task) {
				markKeepRunningRequeued(store, task.ID, "", false)
				return true, noteOnTask(ctx, store, task.ID, landed.Error(), now)
			}
			return true, finishAlreadyLanded(ctx, store, task, landed, now)
		}
		// A branch with nothing on it is the one failure here that is not
		// worth reporting as a failure: it is answered, on the task, by
		// noteEmptyBranch. The error is still handed back afterwards, so
		// each caller can record what it means for the *run* -- see
		// ProcessResult, RunCycle and recoverRun, which want three
		// different outcomes on the row and the same thing on the task.
		if empty, ok := emptyBranch(err); ok {
			if taskKeepRunning(ctx, store, task) {
				return false, nil
			}
			if noteErr := noteEmptyBranch(ctx, store, task, empty, now); noteErr != nil {
				// Deliberately not wrapping err: the task was *not*
				// told, so this is an ordinary failure to report and
				// retry, not the settled ending below.
				return true, noteErr
			}
		}
		return true, err
	}
	return true, noteOrphanedIfClosed(ctx, store, client, task.ID, now)
}

// noActionDetail says what the run did, not only what it did not do.
//
// This overwrites whatever RunDispatch's outcomeOf already recorded, and
// the sentence alone used to throw that away. outcomeOf distinguishes a
// run that called tools and achieved nothing from one that "made no tool
// calls at all" -- a real diagnosis, since an agent that never called a
// tool did not fail at the work so much as never attempt it -- and
// replacing it with a fixed string about pushes and questions left both
// looking identical to whoever read `grain get` afterwards.
//
// Names and counts only. The detail is a stored column read in a task
// listing, not a transcript: the run's final text goes to the log line
// above, bounded, for the same reason.
func noActionDetail(result *agent.Result) string {
	const ending = "finished without pushing a branch, asking a question, or leaving a closing comment"
	if len(result.ToolCalls) == 0 {
		return "the run made no tool calls at all, and " + ending
	}
	return fmt.Sprintf("the run made %d tool call(s)%s, and %s",
		len(result.ToolCalls), toolCallSummary(result), ending)
}

// toolCallSummary names every tool the run called and how often, in a
// stable order, or empty when it called none -- which is itself the most
// informative case, since an agent that called nothing at all did not
// fail to act so much as never start.
func toolCallSummary(result *agent.Result) string {
	if len(result.ToolCalls) == 0 {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, c := range result.ToolCalls {
		name := c.Name
		if c.IsError {
			name += "(error)"
		}
		if _, seen := counts[name]; !seen {
			order = append(order, name)
		}
		counts[name]++
	}
	sort.Strings(order)
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, fmt.Sprintf("%s x%d", name, counts[name]))
	}
	return " [" + strings.Join(parts, ", ") + "]"
}

// truncate bounds model output on its way to the journal. Marks that it
// cut rather than silently eliding, so a short final text is never
// mistaken for a truncated one.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "<empty>"
	}
	if len(s) <= max {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:max]) + "... (truncated)"
}

// branchExistsRetries and branchExistsBackoff bound how long a negative
// answer is re-checked before it is believed. Small: this is covering a
// read that trails a write by a moment, not an outage.
var (
	branchExistsRetries = 3
	branchExistsBackoff = 2 * time.Second
	branchExistsSleep   = time.Sleep
)

// targetBranchExists is whether branch is on target: read over git when
// target's host has no forge API (github.GitBranchSource), and from the
// REST API through branchExistsSettled otherwise. The API names a
// repository by owner/name alone, so asking it about a repository on a
// plain git host asks about a different repository on GitHub, and a 404
// from that read every pushed branch there as one nobody could check.
func targetBranchExists(client github.Client, target model.RepoRef, branch string) (bool, error) {
	if rc, ok := client.(*github.RESTClient); ok {
		if src, ok := rc.Tokens.(github.GitBranchSource); ok {
			if exists, ok, err := src.GitBranchExists(target.StoredOwner(), target.Name, branch); ok {
				return exists, err
			}
		}
	}
	return branchExistsSettled(client, target.Owner, target.Name, branch)
}

// branchExistsSettled re-checks a "no such branch" answer before acting
// on it, because this call runs moments after the agent's own `git push`
// and GitHub's REST reads can trail a push briefly.
//
// The asymmetry is the whole point. A false positive here is caught
// immediately -- finishWithPullRequest asks GitHub to open a pull request
// against a branch, and GitHub refuses if it is not there. A false
// negative is silent and permanent: the run is recorded no_action, the
// task counts another failed attempt toward its streak cap, and the work
// the agent actually did sits pushed on the remote with nothing pointing
// at it. That happened to a task on the v2 staging deployment whose
// branch was on GitHub, at the right commit, with a push timestamp one
// second after the commit it carried.
//
// So a positive is taken at once and only a negative is paid for, at a
// few seconds against a dispatch that took minutes. An error is not
// retried at all: BranchExists already distinguishes 404 (a real "not
// there") from every other status, and a 500 or a refused connection is
// the caller's to report rather than to sit on.
func branchExistsSettled(client github.Client, owner, repo, branch string) (bool, error) {
	for attempt := 0; attempt < branchExistsRetries; attempt++ {
		if attempt > 0 {
			branchExistsSleep(branchExistsBackoff)
		}
		exists, err := client.BranchExists(owner, repo, branch)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}

	// Every attempt said no. Confirm the client can see the repository at
	// all before believing it: BranchExists reads a 404 as "no such
	// branch", and every other way of failing to reach a repo -- a
	// transport aimed at the wrong host, a revoked or unscoped token, a
	// repo turned private -- 404s in exactly the same way. A negative
	// that means "I cannot look" is then indistinguishable from one that
	// means "I looked and it is not there", and this caller acts on the
	// difference: the second ends the task as no_action, the first throws
	// away work the agent really did push.
	//
	// That is not hypothetical. github.NewRealTransport pointed the REST
	// client at github.com rather than api.github.com, so every API path
	// 404'd, so every branch read as unpushed, and runs that had just
	// pushed were recorded as having done nothing at all -- with no error
	// anywhere to say so. A cheap read of the repo itself, on the
	// negative path only, is what turns that class of failure from a
	// wrong answer into a reported one.
	if _, err := client.DefaultBranch(owner, repo); err != nil {
		return false, fmt.Errorf(
			"orchestrator: cannot read %s/%s, so %q cannot be treated as unpushed: %w",
			owner, repo, branch, err)
	}
	return false, nil
}

// parkedOnAQuestion reports whether the task is already waiting on a
// human as its run ends -- Observation.PendingQuestionCommentID pointing
// at something somebody has yet to answer.
//
// Only a run itself can have put it there while the run was live: a task
// parked before the run started is not dispatchable in the first place
// (model.StateOf reads it as awaiting_reply, and task_ready excludes it),
// so anything this finds was set during the run. Today that means the
// chat tool; pkg/capability/selfrepair's Confirm parks the same field on
// the same terms, and would be found the same way.
//
// A failed read is an error rather than a false: guessing "not parked"
// would let the endings below complete or overwrite a task that is in
// fact waiting for somebody, which is the one outcome this check exists
// to prevent.
func parkedOnAQuestion(ctx context.Context, store *model.Store, taskID string) (bool, error) {
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading whether %s is awaiting a reply: %w", taskID, err)
	}
	return obs != nil && obs.PendingQuestionCommentID != nil, nil
}

// relayParkingCalls relays the two escape hatches that park a task on a
// human -- request_secret and ask_question -- and reports whether it
// parked it. Both write a comment and both leave the task in
// awaiting_reply through the same Observation.PendingQuestionCommentID,
// so they are decided together rather than in two branches that would
// each return past the other.
//
// A run is meant to call at most one of them: each says "after calling
// this, do not take any further actions". A run that called both is
// nonetheless relayed both, because a call that reached this far and is
// dropped is dropped in silence -- agent.Result is never persisted, and
// the words exist nowhere else (the same argument the comment_on_issue
// relay above makes). The task then parks on the question, which is the
// later of the two and the one that ended the run's turn, while the
// secret is still recorded and its box still offered: setting it or
// replying in words both un-park the task, and either is a human having
// answered.
//
// Ordering inside is what a reader of the conversation gets: the secret
// request first, the question after it, both stamped now.
func relayParkingCalls(ctx context.Context, store *model.Store, task model.Task,
	result *agent.Result, staged map[string]mcp.ResponseAttachment, now time.Time) (bool, error) {

	secret, _ := firstToolCallArg(result, "request_secret", "secret")
	question, _ := firstToolCallArg(result, "ask_question", "question")
	if secret == "" && question == "" {
		return false, nil
	}

	var pending int64
	if secret != "" {
		reason, _ := firstToolCallArg(result, "request_secret", "reason")
		summary, _ := firstToolCallArg(result, "request_secret", "summary")
		if strings.TrimSpace(summary) == "" && strings.TrimSpace(reason) == "" {
			// Nothing of the run's own to summarize: grain's sentence
			// about the box is not a summary of what is wanted.
			summary = "Set the secret " + secret
		}
		body := secretRequestBody(secret, reason)
		summary = model.QuestionSummary(summary, reason, "request_secret on "+task.ID)
		commentID, err := relayQuestion(ctx, store, task, body, summary, nil, now)
		if err != nil {
			return false, fmt.Errorf("orchestrator: posting the secret request for %s: %w", task.ID, err)
		}
		pending = commentID
	}
	if question != "" {
		attachments, dropped := mcp.ResponseAttachmentsTolerant(firstToolCallArgs(result, "ask_question"), staged)
		summary, _ := firstToolCallArg(result, "ask_question", "summary")
		summary = model.QuestionSummary(summary, question, "ask_question on "+task.ID)
		commentID, err := relayQuestion(ctx, store, task, withDroppedAttachments(question, dropped), summary, attachments, now)
		if err != nil {
			return false, fmt.Errorf("orchestrator: posting question for %s: %w", task.ID, err)
		}
		pending = commentID
	}

	if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &pending
		if secret != "" {
			o.PendingSecret = secret
		}
	}); err != nil {
		return false, err
	}
	return true, nil
}

// secretRequestBody is what a request_secret call reads as in the task's
// conversation: the run's own reason first, in its own words, then
// grain's sentence about what the box underneath does with what is typed
// into it.
//
// The second half is grain's to say and not the agent's, because it is
// the part a human has to be able to trust: that the value is not going
// into the thread they are reading, and not back to the run that asked.
// An agent could claim it; only grain can mean it.
func secretRequestBody(secret, reason string) string {
	body := fmt.Sprintf(
		"**This run asked for the secret `%s`.** Set it in the box on this task: the value "+
			"goes straight into grain's encrypted secret store, is never shown to the agent "+
			"that asked for it, and never appears in this conversation. Setting it -- or "+
			"replying here instead -- puts this task back in the queue.", secret)
	if reason == "" {
		return body
	}
	return reason + "\n\n" + body
}

// relayComment records something a dispatched run said, attributed as
// grain relaying an agent: (automation, on behalf of agent).
//
// That attribution is the whole reason model.Comment carries an
// Attribution rather than a bare Principal. v1 could only gesture at it
// by looking for a signature substring in an issue comment's body, since
// GitHub knows one entity -- the account the token belongs to -- and
// grain has three.
func relayComment(ctx context.Context, store *model.Store, task model.Task,
	body string, now time.Time) (int64, error) {
	return relayCommentWithAttachments(ctx, store, task, body, nil, now)
}

// withDroppedAttachments is body with a line appended for each attachment
// that could not be decoded. By the time a run's result is processed the
// agent can no longer fix one, so a bad attachment is dropped and said so
// rather than failing the finish: an error here would throw away the
// words, the pull request and the attempt together (grain/task-1064).
func withDroppedAttachments(body string, dropped []mcp.DroppedAttachment) string {
	if len(dropped) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n")
	for _, d := range dropped {
		fmt.Fprintf(&b, "\n_[grain] attachment `%s` was dropped: %s._", d.Filename, d.Reason)
	}
	return b.String()
}

// stagedAttachments is every file runID attached by path, keyed by the
// path its call named (model.Store.StagedAttachments). A store that
// cannot be read is logged and answered with none: each attachment that
// needed one is then dropped with a note saying so, and the words it came
// with are still relayed.
func stagedAttachments(ctx context.Context, store *model.Store, runID string) map[string]mcp.ResponseAttachment {
	if runID == "" {
		return nil
	}
	rows, err := store.StagedAttachments(ctx, runID)
	if err != nil {
		log.Printf("orchestrator: run %s: %v", runID, err)
		return nil
	}
	out := make(map[string]mcp.ResponseAttachment, len(rows))
	for p, a := range rows {
		out[p] = mcp.ResponseAttachment{Filename: a.Filename, ContentType: a.ContentType, Content: a.Content}
	}
	return out
}

func relayCommentWithAttachments(ctx context.Context, store *model.Store, task model.Task,
	body string, attachments []mcp.ResponseAttachment, now time.Time) (int64, error) {
	return relayQuestion(ctx, store, task, body, "", attachments, now)
}

// relayQuestion is relayCommentWithAttachments for a question a run asked:
// the comment carries summary, the one line the inbox, the task list and
// notifications show of it (model.Comment.Summary). summary "" is an
// ordinary comment.
func relayQuestion(ctx context.Context, store *model.Store, task model.Task,
	body, summary string, attachments []mcp.ResponseAttachment, now time.Time) (int64, error) {

	now, err := answeringTime(ctx, store, task.ID, now)
	if err != nil {
		return 0, err
	}
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: task.ID}
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      body,
		Summary:   summary,
		CreatedAt: now,
	})
	if err != nil {
		return 0, err
	}
	for _, attachment := range attachments {
		if _, err := store.AddAttachment(ctx, model.Attachment{
			TaskID: task.ID, CommentID: &commentID, Filename: attachment.Filename,
			ContentType: attachment.ContentType, Size: int64(len(attachment.Content)),
			Content: attachment.Content, CreatedAt: now,
		}); err != nil {
			return 0, err
		}
	}
	return commentID, nil
}

// answeringTime is when something a run said is stamped as said: now,
// unless the task's conversation already holds a comment stamped later,
// in which case that comment's time.
//
// now is the moment the cycle that dispatched the run began, and a
// person can post in the moment after: the cycle still reads their
// message, dispatches a run for it, and relays what the run said --
// which, stamped now, came before the message it answers, and the chat
// (ui/src/chat.js), which sorts by time, drew the answer above the
// question (grain/task-3782). Never stamping a run's words earlier than
// the newest comment in the thread keeps them after everything the run
// could have read. A tie keeps the order too: Store.Comments, and the
// chat's stable sort after it, order equal times by id, the order they
// were written in. The clock is not consulted instead, because now is
// also the timeline every other timestamp this run leaves is on.
func answeringTime(ctx context.Context, store *model.Store, taskID string, now time.Time) (time.Time, error) {
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		return now, err
	}
	for _, c := range comments {
		if c.CreatedAt.After(now) {
			now = c.CreatedAt
		}
	}
	return now, nil
}

// relayHandover posts a run's write_handover call as a comment marked as
// this task's handover (model.Comment.Handover), and does nothing at all
// for the overwhelming majority of runs, which make no such call.
//
// It is the second half of the exchange conversation.go opens: a prompt
// whose thread had to be cut asks the run to write down what the part it
// cannot see settled, and what comes back is carried into every later
// prompt outside the head/tail budget the rest of the thread is bounded
// by. The mark is the whole mechanism -- the body is an ordinary comment,
// in the conversation, in order, where a human reads it too.
//
// The last call wins (lastToolCallArg), which is write_handover's own
// documented rule rather than a convention this function invents.
//
// Nothing about this ends the run or the task. A handover accompanies
// whatever else the run did rather than replacing it, which is why this
// returns only an error and why ProcessResult neither reads nor branches
// on the result: a run that wrote a handover and pushed nothing else has
// still produced nothing anybody asked for, and must still reach the
// "nothing to act on" ending that says so.
func relayHandover(ctx context.Context, store *model.Store, task model.Task,
	result *agent.Result, now time.Time) error {

	if result == nil {
		return nil
	}
	handover, ok := lastToolCallArg(result, "write_handover", "handover")
	handover = strings.TrimSpace(handover)
	if !ok || handover == "" {
		return nil
	}
	existing, err := store.Comments(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading comments before posting handover for %s: %w", task.ID, err)
	}
	for i := len(existing) - 1; i >= 0; i-- {
		if existing[i].Handover {
			if strings.TrimSpace(existing[i].Body) == handover {
				return nil
			}
			break
		}
	}
	now, err = answeringTime(ctx, store, task.ID, now)
	if err != nil {
		return fmt.Errorf("orchestrator: posting handover for %s: %w", task.ID, err)
	}
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: task.ID}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      handover,
		CreatedAt: now,
		Handover:  true,
	}); err != nil {
		return fmt.Errorf("orchestrator: posting handover for %s: %w", task.ID, err)
	}
	return nil
}

// proposalConnection is the deployment's connection whose ID is name, when
// name is not also a model that connection advertises -- a proposal's
// `model` naming a connection rather than a model. nil when there is no
// such connection or the settings cannot be read, in which case name
// stays the model choice it was given.
func proposalConnection(ctx context.Context, store *model.Store, name string) *model.AgentBackend {
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return nil
	}
	for i := range cfg.AgentBackends {
		if b := cfg.AgentBackends[i]; b.ID == name && !b.SupportsModel(name) {
			return &b
		}
	}
	return nil
}

// relayProposedTasks files each propose_task call as a real task with no
// Approval -- which model.StateOf reads as 'proposed', so it sits in the
// store waiting for a human to approve it and is never dispatchable
// meanwhile. That is exactly proposeTaskTool's own contract ("requires a
// human to ... before the agent set will ever attempt it"), enforced by
// the state machine now rather than by withholding a label.
//
// With one exception, and it is the same permission the rest of this
// comment is about: a proposal from a task that auto-merges, landing
// where that task was allowed to land work and asking for no capability
// it did not hold, is filed already approved (model.AutoSubmits, which
// has the argument for why a gate in front of that task's follow-up work
// stops nothing). The other exception is a standing console session
// whose deployment says it may queue its own work
// (consoleSessionApprovesProposals).
//
// The first of those is bounded, because a proposal filed that way
// inherits its parent's auto-merge and so may file more of the same:
// model.AutoSubmitLimits caps how deep below the last human approval a
// self-approved task may sit and how many one task may have, and a
// proposal past either bound is filed proposed with its body saying so
// (model.AutoSubmitLimitNote) rather than refused. It is a bound on how
// far one permission propagates, not on the permission -- see the switch
// below for why the session is outside it and the tasks below the
// session are not.
//
// The proposal is linked back to the task that made it
// (model.LinkProposedBy, provenance only -- it blocks nothing), which the
// GitHub-issue version had no way to record.
//
// AutoMerge inherits from task, the job that proposed it, whenever the
// proposal asks for nothing beyond what task itself was granted --
// model.GrantsSubsetOf's own doc comment is the reasoning. A proposal
// cannot yet ask for a capability at all (proposeTaskTool's own input
// schema has no such field, so Grants is always empty here), which makes
// the check trivially true today; it is written as a real comparison
// rather than a bare `task.AutoMerge` so it stays correct the day a
// capability request is added to that schema. An explicit auto_merge
// argument overrides that inheritance in one direction only: false opts a
// proposal out (separate work an agent thinks deserves its own review),
// while true still cannot exceed task's own setting, since a run that
// could mark its own proposals auto-merge would be granting itself the
// unreviewed merge a human withheld. What it decides is only what
// happens once the proposal has run and its pull request is clean;
// whether the proposal runs at all without a human is the separate
// question model.AutoSubmits answers, and an explicit auto_merge: false
// does not change that answer -- a run saying a proposal's own pull
// request deserves review has said nothing about who queues it.
//
// Inheritance is capped a second way, alongside the capabilities: a
// proposal auto-merges only if it lands where task was given permission
// to land work (model.LandsWhereGranted) -- task's own branch, or one of
// the extra write targets it declares, on that target's own base. That
// second half is what makes a task with several writeable repos worth
// having: the whole way work reaches one of those repositories is a
// proposal filed against it (proposedTarget), and if such a proposal
// could never inherit auto-merge, a deployment that lands its own
// unreviewed work in two repositories could not say so.
//
// Everything below is that rule's first half, and none of it is loosened
// by the second: a proposal filed against a repository nobody declared,
// or against a declared one on some other branch, still inherits
// nothing. Auto-merge is a permission a human gave one branch
// to take commits nobody reviewed, and a proposal that names no
// repository of its own is filed with no Base either -- it lands on the
// parent's target repo's default branch, whatever branch its parent
// works against. So a task with a `/base` of its own,
// including every merge queue fix task, passes on no auto-merge: it was
// never given anything about the default branch, and a run whose
// proposals merged themselves there unreviewed would have turned a
// permission for a release branch into one for the trunk.
//
// The alternative -- filing the proposal against task.Base, so the two
// really are the same branch -- is worse than it looks. A proposal sits
// unapproved for as long as a human takes, and the branches tasks work
// against are not all long-lived: a fix task's Base is the pull request
// branch it repairs, which is gone once that pull request merges, and
// prepareCheckout refuses to start a run whose base has vanished. A
// proposal pointed at the trunk and left for a human to retarget is
// dispatchable whenever they get to it; one pointed at a branch that has
// since merged is not dispatchable at all.
//
// otherBranchAutoMergeNote is how the proposal says so, for the same
// reason unresolvedDependencyNote exists: silently filing work an agent
// expected to auto-merge as work that does not leaves the human
// approving it with no idea a decision was made.
//
// depends_on becomes real model.LinkDependsOn links (proposedDependency
// resolves each entry), so a proposal that names work it has to follow is
// blocked until that work is done rather than dispatchable the moment a
// human approves it. Resolution is against tasks that already exist --
// including task itself, the usual case for a piece split out of the run
// doing the splitting -- and against the local `id` of an earlier
// propose_task call in the same run. Earlier, not any: a batch resolved
// in call order cannot contain a cycle, and a cycle in LinkDependsOn is
// two tasks neither of which is ever dispatchable again. An entry naming
// a task that never completes is refused for that same reason and said
// out loud in the body (proposedDependencies): "task itself" is the
// usual case for most runs and is exactly the wrong one for a standing
// chat session, whose task is never finished.
//
// Each proposal joins the backlog at whichever end model.Config.NewestFirst
// says new work joins -- Store.OrderKeyForNewTask, the same call
// ui.CreateTask makes for a task a human files -- so by default (that
// setting's own false) a proposal is filed at the end of the backlog,
// behind everything already queued. It used to be filed with no OrderKey
// at all, which is not "no position" but position zero: ahead of every
// task filed since the backlog started, so a proposal an agent split out
// as an afterthought was dispatched, once approved, before work that had
// been waiting for it.
func relayProposedTasks(ctx context.Context, store *model.Store, reach *gitproxy.Reach, client github.Client,
	task model.Task, result *agent.Result, now time.Time) error {

	proposals := proposedTaskCalls(result)
	if len(proposals) == 0 {
		return nil
	}
	if latest, err := store.GetTask(ctx, task.ID); err == nil && latest != nil {
		task = *latest
	}
	atFront, err := newestFirst(ctx, store)
	if err != nil {
		return err
	}
	// A standing session may be trusted to queue its own work, if this
	// deployment says so (model.ConsoleSession.TasksApproved). Read once
	// for the batch rather than per proposal: it is a settings row, and
	// it cannot change between two calls of the same run.
	approved, err := consoleSessionApprovesProposals(ctx, store, task)
	if err != nil {
		return err
	}
	// And how far a tree of self-approved work may grow from the last
	// human approval above this task (model.AutoSubmitLimits). Read once
	// for the batch for the same reason, and for the same reason
	// newestFirst is: it is a settings row. What each proposal is
	// measured against it, though, is asked per proposal, since the
	// siblings filed earlier in this loop count toward the fan-out.
	limits, err := autoSubmitLimits(ctx, store)
	if err != nil {
		return err
	}

	// Local `id` of a propose_task call -> the task id it was filed
	// under, filled in as each proposal lands so only earlier calls are
	// in reach. First claim on an id wins: a run that reuses one has
	// already made the reference ambiguous, and picking the earlier
	// keeps this pass order-dependent in only one direction.
	filed := map[string]string{}
	// Proposals the parameter allowlist refused, said on task's own
	// conversation once the batch is through (noteRefusedTuningProposals):
	// a refusal that is only a log line leaves the person reading the
	// runner's task one recommendation short with no word why.
	var refusedTuning []refusedTuningProposal

	// The capability catalogue (settings/capabilities.yml), read once:
	// each proposal is judged against it below, as ui.CreateTask judges
	// the identical call filed live.
	catalogueCfg, err := store.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading the capability catalogue for proposed tasks: %w", err)
	}
	var catalogue *model.CapabilityCatalogue
	if catalogueCfg != nil {
		catalogue = catalogueCfg.CapabilityPolicy.Catalogue
	}
	for _, p := range proposals {
		title, _ := p["title"].(string)
		body, _ := p["body"].(string)
		var tmplID string
		if v, ok := p["template_id"].(string); ok {
			tmplID = strings.TrimSpace(v)
		}
		var fieldValues map[string]string
		if tf, ok := p["template_fields"].(map[string]any); ok {
			fieldValues = make(map[string]string, len(tf))
			for k, val := range tf {
				if str, ok := val.(string); ok {
					fieldValues[k] = str
				} else if val != nil {
					fieldValues[k] = fmt.Sprintf("%v", val)
				}
			}
		} else if tfStr, ok := p["template_fields"].(map[string]string); ok {
			fieldValues = tfStr
		}

		// Parameter allowlist validation in the system tuning runner:
		// verify proposed changes target recognized grain_config parameters
		// before filing state-repo pull requests or proposed tasks.
		// Code proposals from infra-review that explicitly target a source
		// repository (e.g. reliability bug fixes) are not tuning
		// recommendations unless they explicitly name a parameter field.
		isTuningTask := task.TemplateID == model.InfraReviewTemplateID &&
			!proposalTargetsSourceRepo(ctx, store, p, task)
		// Only the tuning runner's own tuning proposals are read for a
		// parameter in their title: any other task's split-out work (or a
		// source-repo code proposal) that merely says "tune" there is not
		// a recommendation, and reading it as one refused it, unfiled, as
		// a parameter outside the allowlist.
		tuningTitle, tuningBody := "", ""
		if isTuningTask {
			tuningTitle, tuningBody = title, body
		}
		param, val := ExtractTuningProposal(p, tuningTitle, tuningBody)
		if isTuningTask || param != "" {
			if param != "" {
				if err := ValidateTuningProposal(param); err != nil {
					log.Printf("orchestrator: refusing tuning proposal %q: %v", title, err)
					refusedTuning = append(refusedTuning, refusedTuningProposal{title: title, param: param, err: err})
					continue
				}
				// Valid parameter proposed: record structured recommendation in store
				cooldown := time.Duration(model.InfraReviewCooldownDays) * 24 * time.Hour
				if _, err := RecordTuningRecommendation(ctx, store, task.ID, param, val, cooldown, now); err != nil {
					log.Printf("orchestrator: recording tuning recommendation for %s: %v", param, err)
				}
			} else if isTuningTask {
				// The infra-review task submitted a proposal with no identifiable target parameter
				log.Printf("orchestrator: infra-review proposal %q does not target a recognized grain_config parameter", title)
				refusedTuning = append(refusedTuning, refusedTuningProposal{title: title,
					err: errors.New("it names no grain_config parameter to change")})
				continue
			}
		}

		target, base, unknownRepo, err := proposedTarget(ctx, store, p, task)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving the repository for proposed task %q: %w", title, err)
		}

		// An access grain cannot build is dropped and said out loud, not
		// filed: every sandbox backend withholds the network only for
		// model.NetworkNone exactly, so an unknown word kept here would
		// be dispatched with an open network.
		var netAccess model.NetworkAccess
		var droppedNetAccess string
		if v, ok := p["network_access"].(string); ok && strings.TrimSpace(v) != "" {
			netAccess = model.NetworkAccess(strings.TrimSpace(v))
			if !model.ValidNetworkAccess(netAccess) {
				droppedNetAccess, netAccess = string(netAccess), ""
			}
		}
		// An unknown value is dropped rather than refusing the proposal:
		// the default it falls back to is the tool's own documented one.
		var nothingNeeded model.NothingNeededAction
		if v, ok := p["nothing_needed_action"].(string); ok {
			nothingNeeded, _ = model.ParseNothingNeededAction(strings.TrimSpace(v))
		}
		noPort, _ := p["no_port"].(bool)
		keepRunning, keepRunningSet := p["keep_running"].(bool)
		fixesBase, _ := p["fixes_base_branch"].(bool)
		priority := model.DefaultTaskPriority
		var hasExplicitPriority bool
		if v, ok := p["priority"]; ok {
			if parsed, ok := argInt(v); ok && parsed > 0 {
				priority = parsed
				hasExplicitPriority = true
			}
		}
		goal, goalErr := model.ParseGoalArg(p["goal"])
		if goalErr != nil {
			body += fmt.Sprintf("\n\n_grain could not use this proposal's goal: %v. It was filed without one._", goalErr)
		}
		namedOrigin, _ := p["origin"].(string)
		explicitOrigin, originErr := model.ExplicitOrigin(namedOrigin, target, &task)
		if originErr != nil {
			body += fmt.Sprintf("\n\n_grain could not use this proposal's origin: %v. It inherits its parent's._", originErr)
		}
		body += model.OriginKeptNote(namedOrigin, target, &task)

		var interactive bool
		if tmplID != "" {
			tmpl, err := store.GetTemplate(ctx, tmplID)
			if err != nil {
				return fmt.Errorf("orchestrator: resolving template for proposed task %q: %w", tmplID, err)
			}
			if tmpl != nil {
				var targetRef model.RepoRef
				if target != nil {
					targetRef = *target
				}
				comp := model.ComposeTemplateTask(*tmpl, fieldValues, targetRef, base, false)
				if title == "" {
					title = comp.Title
				}
				if body == "" {
					body = comp.Body
				}
				noRepo, _ := p["no_repo"].(bool)
				if target == nil && !noRepo && comp.Target != nil {
					target = comp.Target
					base = comp.Base
				}
				if netAccess == "" && comp.NetworkAccess != "" {
					netAccess = comp.NetworkAccess
				}
				if !hasExplicitPriority && comp.Priority != 0 {
					priority = comp.Priority
				}
				interactive = comp.Interactive
				if !keepRunningSet {
					keepRunning = comp.KeepRunning
				}
			}
		}

		if title == "" || body == "" {
			continue
		}
		key := mcp.ProposalKeyArgs(task.ID, p)
		if existing, err := store.TaskWithTag(ctx, key); err != nil {
			return fmt.Errorf("orchestrator: finding already-filed proposed task %q: %w", title, err)
		} else if existing != nil {
			if local, _ := p["id"].(string); local != "" {
				if _, taken := filed[local]; !taken {
					filed[local] = existing.ID
				}
			}
			continue
		}
		id, err := store.NewTaskID(ctx)
		if err != nil {
			return fmt.Errorf("orchestrator: allocating an id for proposed task %q: %w", title, err)
		}
		links := []model.Link{{Kind: model.LinkProposedBy, Target: task.ID}}
		deps, unresolved, neverEnding, err := proposedDependencies(ctx, store, p, filed)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving depends_on for proposed task %q: %w", title, err)
		}
		links = append(links, deps...)
		if len(unresolved) > 0 {
			body += unresolvedDependencyNote(unresolved)
		}
		if len(neverEnding) > 0 {
			body += model.NeverEndingDependencyNote(neverEnding)
		}
		if unknownRepo != "" {
			body += unknownProposalRepoNote(task, unknownRepo, target)
		}
		// The proposal's team is not its proposer's (single-tenant reset,
		// stage 4c): nothing is inherited across teams, and it files as
		// any task naming no team does.
		grants, err := defaultTaskGrants(ctx, store, target, model.DefaultTeamID)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving default capabilities for proposed task %q: %w", title, err)
		}
		// A {capability, with} entry the run wrote is read as the
		// grant string it stands for; one that does not parse is
		// dropped here, as a non-string entry always was, since the
		// live propose_task call already refused it.
		capabilities, _ := model.CapabilityEntries(p["capabilities"])
		for _, cap := range capabilities {
			cap = strings.TrimSpace(cap)
			// A review grant is grain's to give, on a review it files
			// itself (SyncCLReviews); the live call refused it too.
			if model.GrantNamesReviewOf(cap) {
				body += reviewOfGrantRefusedNote(cap)
				continue
			}
			// grain-ci is passed over, as ui.CreateTask passes it over
			// for the same call filed live: the target repository's CI
			// Mode decides it (defaultTaskGrants, above), and naming it
			// here would otherwise file a proposal against a non-grain
			// repo holding a grant its proposer was given by grain, and
			// so unapproved (model.EscalatedCapabilities).
			if cap == "" || cap == grainci.CapabilityName {
				continue
			}
			found := false
			for _, g := range grants {
				if g.Capability == cap {
					found = true
					break
				}
			}
			if !found {
				grants = append(grants, model.Grant{
					Capability: cap,
					Via:        model.GrantByLabel,
				})
			}
		}
		customFields, droppedFields, err := proposedCustomFields(ctx, store, p)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving custom fields for proposed task %q: %w", title, err)
		}
		if len(droppedFields) > 0 {
			body += droppedCustomFieldNote(droppedFields)
		}
		if droppedNetAccess != "" {
			body += droppedNetworkAccessNote(droppedNetAccess)
		}
		// Whether the task this proposal replaces may be closed is
		// decided before the proposal is written and the close is done
		// after it: the note belongs in the body a human approves, and
		// the link belongs on a task that exists. Store.Supersede asks
		// again at the moment it writes, so the window between the two
		// cannot close a task that has started running in it.
		supersedes, supersedeRefusal, err := proposedSupersedes(ctx, store, p, target)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving supersedes for proposed task %q: %w", title, err)
		}
		if supersedeRefusal != "" {
			named, _ := p["supersedes"].(string)
			body += model.SupersedeRefusedNote(
				strings.TrimPrefix(strings.TrimSpace(named), "#"), supersedeRefusal)
		}
		if supersedes != "" {
			links = append(links, model.Link{Kind: model.LinkSupersedes, Target: supersedes})
		}
		var cpus, memoryMB, diskGB int
		if v, ok := p["sandbox_cpus"]; ok {
			cpus, _ = argInt(v)
		}
		if v, ok := p["sandbox_memory_mb"]; ok {
			memoryMB, _ = argInt(v)
		}
		if v, ok := p["sandbox_disk_gb"]; ok {
			diskGB, _ = argInt(v)
		}
		// A proposal chooses a model (model.Task.Model) and, separately, may
		// pin a connection; the store's own migration and dispatch read a
		// pin naming no connection as the model choice it used to be.
		var agentBackendID, agentModel string
		if v, ok := p["model"].(string); ok && strings.TrimSpace(v) != "" {
			agentModel = strings.TrimSpace(v)
		}
		if v, ok := p["agent_backend_id"].(string); ok && strings.TrimSpace(v) != "" {
			agentBackendID = strings.TrimSpace(v)
		}
		// propose_task's own schema offers `model` as "the model or agent
		// backend", and has no other way to pin one, so a `model` naming a
		// connection is that connection's pin -- the same reading
		// ui.Client.CreateTask gives a request (splitModelChoice).
		if agentBackendID == "" && agentModel != "" {
			if b := proposalConnection(ctx, store, agentModel); b != nil {
				agentBackendID, agentModel = b.ID, ""
			}
		}
		var agentFramework string
		if v, ok := p["agent_framework"].(string); ok && strings.TrimSpace(v) != "" {
			agentFramework = model.NormalizeAgentFrameworkName(strings.TrimSpace(v))
		}
		// Once per proposal rather than once for the batch: each one is
		// past the previous one's key by then, so a run that proposes
		// several files them in call order behind each other rather than
		// all on the same key with the id left to break the tie.
		proposalAtFront := atFront
		if front, ok := p["at_front"].(bool); ok {
			proposalAtFront = front
		} else if pos, ok := p["position"].(string); ok {
			if pos == "front" {
				proposalAtFront = true
			} else if pos == "end" {
				proposalAtFront = false
			}
		}
		orderKey, err := store.OrderKeyForNewTask(ctx, proposalAtFront)
		if err != nil {
			return fmt.Errorf("orchestrator: placing proposed task %q in the backlog: %w", title, err)
		}
		reads := proposedReads(p, target)
		writes := proposedWrites(p, target, base)
		reviewTemplateID, reviewTemplateNote, err := proposedReviewTemplate(ctx, store, task, p, target)
		if err != nil {
			return fmt.Errorf("orchestrator: resolving review template for proposed task %q: %w", title, err)
		}
		body += reviewTemplateNote
		proposal := model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  title,
			Body:   body,
			Origin: model.Origin{
				Attribution: model.Attribution{
					Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
					OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID},
				},
				Reason: model.ReasonDirect,
			},
			Target:              target,
			Binding:             model.BindingDirective,
			Base:                base,
			Priority:            priority,
			Interactive:         interactive,
			Grants:              grants,
			SandboxCPUs:         cpus,
			SandboxMemoryMB:     memoryMB,
			SandboxDiskGB:       diskGB,
			NetworkAccess:       netAccess,
			AgentFramework:      agentFramework,
			AgentBackendID:      agentBackendID,
			Model:               agentModel,
			NothingNeededAction: nothingNeeded,
			KeepRunning:         keepRunning,
			NoPort:              noPort,
			WorkOrigin:          explicitOrigin,
			// What the run asked this proposal to say about this
			// deployment's own declared fields (grain/task-105) -- the
			// values that survived being checked against those
			// declarations, and nothing else.
			CustomFields:     customFields,
			Reads:            reads,
			Writes:           writes,
			ReviewTemplateID: reviewTemplateID,
			Tags:             []string{key},
			Links:            links,
			CreatedAt:        &now,
			OrderKey:         orderKey,
		}
		// The proposing task's root requester's work, as ui.CreateTask
		// records the identical call filed live.
		model.InheritRequester(&proposal, task)
		// Marked as fixing its red base branch (model.FixesBaseBranch,
		// baserepairlink.go): a proposal with no repo has no base to fix,
		// and is filed unmarked rather than not at all.
		if fixesBase {
			model.SetFixesBaseBranch(&proposal, true)
		}
		// A change target (propose_task's change-target argument): validated as the
		// live call validates it (ui.CreateTask), with the task's own
		// workspace grant attached. One that cannot be used is filed with
		// no target, for a person, with a note; one that names another
		// workspace or a CL to continue is held for a person below.
		var needsPersonWhy string
		if ct, err := mcp.ProposedChangeTarget(p); err != nil || ct != nil {
			kind := "change"
			if ct != nil && ct.Kind != "" {
				kind = ct.Kind
			}
			if err == nil {
				proposal.ChangeTarget = ct
				err = model.ValidateTaskChangeTarget(&proposal)
			}
			if err != nil {
				proposal.ChangeTarget = nil
				needsPersonWhy = fmt.Sprintf("grain could not use its %s target (%v), so it was filed with no repo", kind, err)
			} else if needs, why := model.ChangeTargetNeedsPersonApproval(proposal.ChangeTarget); needs {
				needsPersonWhy = why
			}
			if needsPersonWhy != "" {
				proposal.Body += fmt.Sprintf("\n\n_Filed for a person to approve: %s._", needsPersonWhy)
			}
		}
		// A GitHub fork head label as its branch, resolved to the pull
		// request it heads as ui.CreateTask resolves the identical call
		// filed live. One that cannot be resolved is filed for a person,
		// on its repository's default branch, saying why: the run that
		// asked for it has ended, so there is nobody else to tell.
		if why := resolveProposedForkHead(client, &proposal); why != "" {
			needsPersonWhy = why
			proposal.Body += fmt.Sprintf("\n\n_Filed for a person to approve: %s._", why)
		}
		// The capability catalogue, as ui.CreateTask applies it to the
		// identical call filed live: a default it refuses this proposal is
		// dropped, since nobody asked for it, and a grant it marks as a
		// person's decision (requires_person) files the proposal for a
		// person whatever this task's permission would have said. A grant
		// the run asked for that it refuses is kept, and holds the
		// proposal at dispatch (catalogueHold), saying why -- and files it
		// for a person too, since a refusal says nothing of whether the
		// grant needs one: dispatch judges only refusals, so a catalogue
		// later loosened to admit it as requires_person would otherwise
		// let it run on its proposer's permission.
		if catalogue != nil {
			kept := proposal.Grants[:0:0]
			var needsPerson, refused []string
			for _, g := range proposal.Grants {
				gc := model.TaskGrantContext(proposal)
				gc.Grants = []string{g.Capability}
				verdict, err := catalogue.ValidateGrants(gc)
				if err != nil {
					if g.Via == model.GrantByDefault {
						continue
					}
					refused = append(refused, err.Error())
				}
				kept = append(kept, g)
				needsPerson = append(needsPerson, verdict.RequiresPerson...)
			}
			proposal.Grants = kept
			var whys []string
			if len(needsPerson) > 0 {
				whys = append(whys, model.CatalogueRequiresPersonNote(needsPerson))
			}
			if len(refused) > 0 {
				whys = append(whys, "settings/capabilities.yml refuses what it asked for ("+strings.Join(refused, "; ")+")")
			}
			if len(whys) > 0 {
				why := strings.Join(whys, "; ")
				proposal.Body += fmt.Sprintf("\n\n_Filed for a person to approve: %s._", why)
				if needsPersonWhy == "" {
					needsPersonWhy = why
				} else {
					needsPersonWhy += "; " + why
				}
			}
		}
		// Scheduled for later: filed deferred with the wake time the
		// call asked for, so reconcileWake picks it back up then and not
		// before -- ui.CreateTask's rule for the identical call filed
		// live. The handler refused a value mcp.ParseNotBefore cannot
		// read, and proposedTaskCalls drops refused calls, so a parse
		// failure here is a call recorded by something older and is
		// filed undeferred rather than not at all.
		if raw, _ := p["not_before"].(string); strings.TrimSpace(raw) != "" {
			if until, err := mcp.ParseNotBefore(raw); err == nil && until.After(now) {
				proposal.DeferredAt = &now
				proposal.DeferredUntil = &until
			}
		}
		// Approved for a session whose deployment asked for that -- one
		// a person configured deliberately, in front of the chat it
		// applies to -- and otherwise for a proposal a task that
		// auto-merges made within its own permission
		// (model.AutoSubmits) and within the bounds on how far that
		// permission may propagate (model.AutoSubmitLimits). Every
		// other proposal is filed for a human to decide on, which is
		// propose_task's whole contract.
		//
		// The session is not bounded here and the tasks below it are.
		// Its permission is not a run's inherited one but a person
		// sitting in front of the chat: every task it files is one
		// somebody just asked for in a message, so a cap on how many
		// would be a cap on how much a human may ask for in a day. What
		// that person cannot see is what the work they asked for goes
		// on to queue for itself, so a session-queued task counts as a
		// level of self-approved work like any other
		// (model.SelfApproved) and everything below it is bounded
		// exactly as an auto-merge job's proposals are.
		//
		// Neither reaches a proposal that asked for a capability this task
		// does not hold (model.EscalatedCapabilities): it is filed holding
		// it, for a person, with a note saying so -- ui.CreateTask's rule
		// for the identical call filed live.
		escalated := model.EscalatedCapabilities(proposal, task)
		// Write targets the run asked for that this task was never given
		// are filed as asked -- a person may well agree the work belongs
		// there too -- but they keep it from being queued or merging on
		// this task's permission (model.LandsWhereGranted, #2378), and
		// the person deciding is told which ones they are. That holds
		// for a session's approval as much as for AutoSubmits, for the
		// reason escalated capabilities do.
		ungranted := model.UngrantedWrites(proposal, task)
		if len(ungranted) > 0 {
			proposal.Body += model.UngrantedWritesNote(task.ID, ungranted)
		}
		// Read access the run asked for beyond what this task can fetch
		// itself is the same question for reads: filed as asked, for a
		// person, with a note naming the repositories. It does not touch
		// the proposal's auto-merge -- what it reads is not where it lands.
		ungrantedReads := model.UngrantedReads(proposal, task)
		if len(ungrantedReads) > 0 {
			proposal.Body += model.UngrantedReadsNote(task.ID, ungrantedReads)
		}
		// Held (propose_task's hold_for_approval): filed for a person
		// whatever the permission below would have said, and marked as
		// held only if it would have said yes -- ui.CreateTask's rule for
		// the identical call filed live. Per call: the held task's own
		// proposals are judged on its own permission when it runs. A
		// proposal grain itself files for a person (needsPersonWhy) waits
		// too, but its note above already says why, and it is not marked
		// as held: the run made no choice there.
		heldByRun, _ := p["hold_for_approval"].(bool)
		hold := heldByRun || needsPersonWhy != ""
		switch {
		case len(escalated) > 0:
			proposal.Body += model.ProposalEscalationNote(task.ID, proposal, escalated)
		case len(ungranted) > 0, len(ungrantedReads) > 0:
			// Filed for a person; the notes above say why.
		case heldByRun && (approved || model.AutoSubmits(proposal, task)):
			proposal.Body += model.HeldForApprovalNote(task.ID)
			proposal.Tags = append(proposal.Tags, model.HeldForApprovalTag)
		case hold:
			// Waiting for a person anyway; nothing was held back.
		case approved:
			proposal.Approval = &model.Attribution{Actor: consoleSessionPrincipal}
			proposal.ApprovedAt = &now
		case model.AutoSubmits(proposal, task):
			limit, err := store.ExceededAutoSubmitLimit(ctx, task.ID, limits)
			if err != nil {
				return fmt.Errorf("orchestrator: bounding the self-approval of proposed task %q: %w", title, err)
			}
			if limit == model.AutoSubmitWithinLimits {
				proposal.Approval = &model.Attribution{Actor: model.AutoSubmitPrincipal}
				proposal.ApprovedAt = &now
			} else {
				// Filed for a person instead of refused, and saying
				// why: the work is not wrong for the tree having got
				// deep, and this is the only place the human approving
				// it can be told they are now the bound.
				proposal.Body += model.AutoSubmitLimitNote(task.ID, limit, limits)
			}
		}
		asked := proposedAutoMerge(p, task) &&
			model.GrantsSubsetOf(proposal.Grants, task.Grants)
		proposal.AutoMerge = asked && model.LandsWhereGranted(proposal, task)
		if proposal.ChangeTarget != nil {
			// A change target's change is a CL, not a pull request, so
			// there is nothing for auto-merge to merge and
			// model.ValidateTaskChangeTarget refuses it -- checked above
			// before this was decided, so inheriting it here filed a task
			// every dispatch refused (task 1860). Its CL is submitted by
			// a person through its provider instead.
			if proposal.AutoMerge {
				proposal.Body += changeTargetAutoMergeNote(task, proposal)
			}
			proposal.AutoMerge = false
		} else if asked && !proposal.AutoMerge && len(ungranted) == 0 {
			proposal.Body += otherBranchAutoMergeNote(task, proposal)
		}
		if err := store.PutTask(ctx, proposal); err != nil {
			return fmt.Errorf("orchestrator: filing proposed task %q: %w", title, err)
		}
		if goal != nil {
			if err := store.SetTaskGoal(ctx, proposal.ID, *goal,
				model.PrincipalString(model.Principal{Kind: model.PrincipalAgent, ID: task.ID}), now); err != nil {
				return fmt.Errorf("orchestrator: declaring proposed task %q's goal: %w", title, err)
			}
		}
		if supersedes != "" {
			superseded, err := store.Supersede(ctx, supersedes, proposal, task.ID, now)
			refusal := superseded.Refusal
			if err != nil {
				return fmt.Errorf("orchestrator: superseding task %s with proposed task %q: %w",
					supersedes, title, err)
			}
			if refusal != "" {
				// It qualified a moment ago and does not now -- see
				// Store.Supersede. The proposal keeps its link, which
				// is still true, and the thing that changed is said
				// where the people reading this pair will see it.
				if _, err := store.AddComment(ctx, model.Comment{
					TaskID:    proposal.ID,
					Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
					Body:      model.SupersedeRefusedLine(supersedes, refusal),
					CreatedAt: now,
				}); err != nil {
					return fmt.Errorf("orchestrator: noting the refused supersede of task %s: %w",
						supersedes, err)
				}
			}
		}
		// Filed, then parked if its team cannot reach a repository it
		// names: the same rule ui.CreateTask gives the identical call
		// made live, and for the same reason -- refusing would lose the
		// work the run described (parkUnreached).
		if err := parkUnreached(ctx, store, reach, []string{proposal.ID}, now); err != nil {
			return err
		}
		if local, _ := p["id"].(string); local != "" {
			if _, taken := filed[local]; !taken {
				filed[local] = proposal.ID
			}
		}
	}
	return noteRefusedTuningProposals(ctx, store, task, refusedTuning, now)
}

// refusedTuningProposal is one propose_task call of the tuning runner that
// relayProposedTasks did not file: param is the grain_config parameter it
// named ("" when it named none) and err why the allowlist refused it.
type refusedTuningProposal struct {
	title string
	param string
	err   error
}

// noteRefusedTuningProposals posts one comment on task naming each
// proposal the parameter allowlist (model.RecognizedGrainConfigParameters)
// refused, with the parameter and the reason, so the runner's task says
// why it filed fewer recommendations than its run made. Nothing is posted
// when nothing was refused.
func noteRefusedTuningProposals(ctx context.Context, store *model.Store, task model.Task,
	refused []refusedTuningProposal, now time.Time) error {

	if len(refused) == 0 {
		return nil
	}
	var b strings.Builder
	if len(refused) == 1 {
		b.WriteString("grain did not file 1 proposal from this run: it is not a change to a parameter grain recognizes, so nothing was filed and no tuning recommendation was recorded for it.\n")
	} else {
		fmt.Fprintf(&b, "grain did not file %d proposals from this run: they are not changes to parameters grain recognizes, so nothing was filed and no tuning recommendation was recorded for them.\n", len(refused))
	}
	for _, r := range refused {
		if r.param != "" {
			fmt.Fprintf(&b, "\n- %q (parameter `%s`): %v", r.title, r.param, r.err)
		} else {
			fmt.Fprintf(&b, "\n- %q: %v", r.title, r.err)
		}
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      b.String(),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: noting refused tuning proposals on %s: %w", task.ID, err)
	}
	return nil
}

// defaultTaskGrants resolves the capability defaults a newly filed task
// receives: deployment-wide defaults plus the target repository's additions.
// teamID is no longer read: the default team's repository additions are
// repo_config's own since schema 23 (docs/single-tenant-reset.md, 6-ii-b3).
// Proposed tasks have no capability set of their own, so they follow the same
// rule as a task filed by a person instead of silently bypassing repository
// policy. The resolved grants are pinned onto the task; later config changes do
// not rewrite an existing task's authority.
func defaultTaskGrants(ctx context.Context, store *model.Store, target *model.RepoRef, teamID string) ([]model.Grant, error) {
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	if cfg != nil {
		ids = append(ids, cfg.DefaultCapabilities...)
	}
	var repoCfg *model.RepoConfig
	if target != nil {
		repoCfg, err = store.GetRepoConfig(ctx, *target)
		if err != nil {
			return nil, err
		}
		if repoCfg != nil {
			ids = append(ids, repoCfg.DefaultCapabilities...)
		}
	}
	seen := make(map[string]bool, len(ids))
	grants := make([]model.Grant, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || id == grainci.CapabilityName {
			continue
		}
		seen[id] = true
		grants = append(grants, model.Grant{Capability: id, Via: model.GrantByDefault})
	}
	if target != nil {
		usesGrainCI, err := store.RepoUsesGrainCI(ctx, *target)
		if err == nil && usesGrainCI {
			grants = append(grants, model.Grant{Capability: grainci.CapabilityName, Via: model.GrantByDefault})
		} else if err != nil && grainci.RepoUsesGrainCI(repoCfg, *target) {
			grants = append(grants, model.Grant{Capability: grainci.CapabilityName, Via: model.GrantByDefault})
		}
	}
	return grants, nil
}

// newestFirst is model.Config.NewestFirst, read fresh from the store --
// ui.Client's own method of the same name, for the same reason it reads
// it per call there: a setting the settings pane toggles is expected to
// decide where the very next task lands, not where the next process's
// first one does. A fresh deployment with no config row (nil) reads as
// false, the backlog order grain has always defaulted to.
func newestFirst(ctx context.Context, store *model.Store) (bool, error) {
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading the backlog order: %w", err)
	}
	return cfg != nil && cfg.NewestFirst, nil
}

// autoSubmitLimits is model.Config's own pair of auto-submit bounds,
// read fresh from the store the way newestFirst above reads its setting
// and resolved through model.AutoSubmitLimitsOf -- so a deployment with
// no config row yet, and one whose row predates those columns, is
// bounded by grain's own defaults rather than by nothing.
func autoSubmitLimits(ctx context.Context, store *model.Store) (model.AutoSubmitLimits, error) {
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		return model.AutoSubmitLimits{}, fmt.Errorf(
			"orchestrator: reading the limits on self-approved proposals: %w", err)
	}
	return model.AutoSubmitLimitsOf(cfg), nil
}

// proposedAutoMerge reads one propose_task call's auto_merge argument
// against task, the job that made it: unset inherits task's own setting,
// and an explicit answer is honoured only as far down as that -- see
// relayProposedTasks' own doc comment for why true cannot raise it.
func proposedAutoMerge(args map[string]any, task model.Task) bool {
	asked, ok := args["auto_merge"].(bool)
	if !ok {
		return task.AutoMerge
	}
	return asked && task.AutoMerge
}

// proposedDependencies turns one propose_task call's depends_on into
// links, alongside every entry it refused to make one from: the ones it
// could not resolve to a task at all, and the ones naming a task that
// never completes.
//
// Neither is dropped silently: an agent naming a task that does not
// exist (a GitHub issue number, say, from the v1 spelling of this tool,
// or a local `id` it only used later in the run) has said something
// about the order this work has to happen in, and so has one naming a
// task nothing will ever finish. It goes into the proposal's body
// instead, where a human approving it can see it --
// unresolvedDependencyNote and model.NeverEndingDependencyNote.
//
// The second refusal is the same judgement proposedDependency's own
// makes about an id that was never real, for the same stated reason:
// such a link "would block the proposal forever with nothing to unblock
// it". A standing chat session is that failure arrived at from the other
// direction -- the task is real, and it is never going to close
// (model.Store.DependencyNeverSatisfiable) -- and it cost this
// deployment eight undispatchable tasks in a day before it was refused
// here (grain/task-487).
func proposedDependencies(ctx context.Context, store *model.Store, args map[string]any,
	filed map[string]string) (links []model.Link, unresolved, neverEnding []string, err error) {

	seen := map[string]bool{}
	for _, ref := range argStrings(args["depends_on"]) {
		target, ok, err := proposedDependency(ctx, store, ref, filed)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			unresolved = append(unresolved, ref)
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		never, err := store.DependencyNeverSatisfiable(ctx, target)
		if err != nil {
			return nil, nil, nil, err
		}
		if never {
			neverEnding = append(neverEnding, target)
			continue
		}
		links = append(links, model.Link{Kind: model.LinkDependsOn, Target: target})
	}
	return links, unresolved, neverEnding, nil
}

// proposedDependency resolves one depends_on entry to a real task id.
//
// A local `id` from an earlier propose_task call in the same run wins
// over a task id that happens to read the same, since a run that named
// one of its own proposals "12" meant that proposal, not task 12.
// Otherwise the entry has to name a task that already exists: this is the
// one place a proposal's own claim about ordering is checked against
// something, and a link to a task id that was never real would block the
// proposal forever with nothing to unblock it.
//
// A leading "#" is tolerated because the tool's v1 spelling asked for
// issue numbers and agents still write them that way.
func proposedDependency(ctx context.Context, store *model.Store, ref string,
	filed map[string]string) (string, bool, error) {

	raw := strings.TrimSpace(ref)
	if raw == "" {
		return "", false, nil
	}
	if id, ok := filed[raw]; ok {
		return id, true, nil
	}
	norm := model.NormalizeTaskID(ref)
	if norm != "" {
		if id, ok := filed[norm]; ok {
			return id, true, nil
		}
		existing, err := store.GetTask(ctx, norm)
		if err != nil {
			return "", false, err
		}
		if existing != nil {
			return existing.ID, true, nil
		}
	}
	existing, err := store.GetTask(ctx, raw)
	if err != nil {
		return "", false, err
	}
	if existing != nil {
		return existing.ID, true, nil
	}
	return "", false, nil
}

// proposedSupersedes resolves one propose_task call's `supersedes`
// argument: the id of the task this proposal replaces and is about to
// close, or the reason grain will not close it (model.SupersedeRefusal,
// which states every one of them and why).
//
// target is where this proposal is being filed (proposedTarget), not the
// parent's own repo: the bound is that a supersede closes a task in the
// repository its replacement lands in, so a proposal redirected by a
// `repo` argument is measured against where it actually went.
//
// A leading "#" is tolerated for proposedDependency's reason -- agents
// still write task references the way the v1 tool asked for them.
func proposedSupersedes(ctx context.Context, store *model.Store, args map[string]any,
	target *model.RepoRef) (id, refusal string, err error) {

	named, _ := args["supersedes"].(string)
	named = model.NormalizeTaskID(named)
	if named == "" {
		return "", "", nil
	}
	old, refusal, err := store.SupersedeCandidate(ctx, named, target)
	if err != nil {
		return "", "", err
	}
	if refusal != "" {
		return "", refusal, nil
	}
	return old.ID, "", nil
}

// proposedTarget is where one propose_task call's task gets filed: the
// repository and the base branch, resolved from the call's own `repo`
// argument against the write targets its parent declares
// (model.WriteTargets) and, failing those, against the repositories this
// deployment works in at all (model.Store.WorksInRepo).
//
// A call that names no repo -- the ordinary case, and every call grain
// relayed until this argument existed -- files against the parent's own
// target with no Base at all, which is the behaviour relayProposedTasks'
// doc comment argues for at length: the parent's Base may be a branch
// that is gone by the time a human approves the proposal, and a task
// pointed at a vanished base cannot be dispatched at all.
//
// A call that names one of the parent's *extra* write targets
// (model.Task.Writes) inherits that entry's Base. The argument above
// does not apply to it: a Writes base is a standing declaration a human
// wrote on the task -- "work for this repository goes on this branch" --
// not the incidental branch some earlier run happened to be repairing,
// so it is exactly as durable as the write target itself. Filing the
// proposal anywhere else would also defeat the point: the branch is half
// of what the human chose when they added the repository
// (model.WriteTarget).
//
// A call naming the parent's own target resolves to it and behaves like
// a call that named nothing -- an agent restating where it is working is
// not asking for anything.
//
// A call naming any *other* repository this deployment works in is filed
// there too, on that repository's default branch (no Base), and this is
// the case the argument was widened for. Restricting it to the parent's
// declared write targets made the tool wrong most of the time it
// mattered: a run reading a week of this deployment's own history in the
// state repository, asked for the agent-ergonomics work worth doing,
// proposed eight tasks of which seven were changes to grain's own code
// -- and every one of them was filed against the state repository, where
// a Go change cannot land. Nobody had declared a write target, because
// nobody knew in advance which repository the findings would be about.
// Where a proposal *lands* is a human's decision either way: it is filed
// unapproved, and the approver sees the repository on it.
//
// What such a proposal does not get is any of its parent's auto-merge:
// it lands on a branch nobody granted, so model.LandsWhereGranted says
// no and otherBranchAutoMergeNote says why on the proposal itself. That
// is the difference between naming a declared write target and naming
// one of the deployment's other repositories, and it is the whole reason
// declaring one still means something.
//
// Anything else is a repository this deployment has never worked in, and
// unknown comes back naming it. The proposal is still filed, against the
// parent's target, rather than dropped: it is work an agent thought was
// worth doing, and losing a proposal outright over an argument nobody
// would ever see is worse than filing it in the wrong place with a note
// saying so -- the same call unresolvedDependencyNote makes for a
// depends_on entry that resolved to nothing.
func proposedTarget(ctx context.Context, store *model.Store, args map[string]any,
	task model.Task) (target *model.RepoRef, base, unknown string, err error) {

	if noRepo, _ := args["no_repo"].(bool); noRepo {
		return nil, "", "", nil
	}
	if ct, err := mcp.ProposedChangeTarget(args); err != nil || ct != nil {
		return nil, "", "", nil
	}

	named, _ := args["repo"].(string)
	named = strings.TrimSpace(named)
	branchNamed, _ := args["branch"].(string)
	branchNamed = strings.TrimSpace(branchNamed)

	if repoPart, branchPart, hasAt := strings.Cut(named, "@"); hasAt {
		named = strings.TrimSpace(repoPart)
		if branchNamed == "" {
			branchNamed = strings.TrimSpace(branchPart)
		}
	}

	if named == "" {
		return task.Target, branchNamed, "", nil
	}
	repo, err := model.ParseRepo(named)
	if err != nil {
		return task.Target, branchNamed, named, nil
	}
	if task.Target != nil && repo.Same(*task.Target) {
		return task.Target, branchNamed, "", nil
	}
	if w, ok := model.WriteTargetFor(task, repo); ok {
		if branchNamed != "" {
			return &w.Repo, branchNamed, "", nil
		}
		return &w.Repo, w.Base, "", nil
	}
	// The deployment's own allowlist (the default team's TargetRepos), not the
	// proposer's team's: a proposal is checked against no team of its
	// proposer's since the single-tenant reset (stage 4c).
	known, err := store.WorksInRepoForTeam(ctx, model.DefaultTeamID, repo)
	if err != nil {
		return nil, "", "", err
	}
	if !known {
		return task.Target, branchNamed, named, nil
	}
	return &repo, branchNamed, "", nil
}

// proposalTargetsSourceRepo reports whether a propose_task call p explicitly
// targets a source repository rather than the deployment's configuration/state
// repository (or no repository at all).
func proposalTargetsSourceRepo(ctx context.Context, store *model.Store, p map[string]any, task model.Task) bool {
	if noRepo, _ := p["no_repo"].(bool); noRepo {
		return false
	}
	if ct, err := mcp.ProposedChangeTarget(p); err == nil && ct != nil {
		return true
	}
	named, _ := p["repo"].(string)
	named = strings.TrimSpace(named)
	if repoPart, _, hasAt := strings.Cut(named, "@"); hasAt {
		named = strings.TrimSpace(repoPart)
	}
	if named == "" {
		return false
	}
	repo, err := model.ParseRepo(named)
	if err != nil || isSettingsRepo(store, repo) {
		return false
	}
	hasSettingsRepo := store != nil && store.SettingsRepo != nil && store.SettingsRepo() != (model.RepoRef{})
	if !hasSettingsRepo {
		for _, r := range task.Reads {
			if r.Same(repo) {
				return false
			}
		}
	}
	if task.Target != nil && repo.Same(*task.Target) {
		return true
	}
	if _, ok := model.WriteTargetFor(task, repo); ok {
		return true
	}
	if store == nil {
		return false
	}
	known, err := store.WorksInRepoForTeam(ctx, model.DefaultTeamID, repo)
	return err == nil && known
}

// unknownProposalRepoNote is what a human approving the proposal reads
// when the run asked for it to be filed against a repository this
// deployment does not work in -- attributed to grain, since the rest of
// the body is the agent's own words.
//
// It names both repositories for otherBranchAutoMergeNote's reason: the
// approver's options are to retarget this task at the repository the run
// meant or to leave it where it landed, and neither is a decision that
// can be made without knowing both.
func unknownProposalRepoNote(task model.Task, named string, filedAt *model.RepoRef) string {
	where := "no repository at all"
	if filedAt != nil {
		where = "`" + filedAt.String() + "`"
	}
	return fmt.Sprintf("\n\n---\n\ngrain: the run that proposed this asked for it to be filed "+
		"against `%s`, which is not a repository this deployment works in -- not one task %s "+
		"declares, and not one anything here has been filed against. It is filed "+
		"against %s instead -- retarget it by hand if the run was right.",
		named, task.ID, where)
}

// unresolvedDependencyNote is what a human approving the proposal reads
// in place of a link grain could not make -- attributed to grain, since
// the rest of the body is the agent's own words.
func unresolvedDependencyNote(unresolved []string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: the run that proposed this said it depends on %s, "+
		"which named no task that exists and no other proposal from the same run. "+
		"Nothing blocks this task on them -- resolve them by hand if they matter.",
		"`"+strings.Join(unresolved, "`, `")+"`")
}

// proposedCustomFields is what one propose_task call's custom_fields
// argument actually puts on the task it files: the values this
// deployment still declares, canonicalized by their own declarations,
// alongside the names of the ones it would not accept.
//
// The declarations are the same gate a human's own filing goes through
// (model.NormalizeCustomFieldValues, ui.Client.normalizeCustomFields) --
// an agent's "TRUE" is stored as "true" and its "urgent" is checked
// against the enum's choices, exactly as a value typed into the new-task
// form is. What differs is the answer to a value that does not pass:
// there is no request in flight to fail and no human at a form to fix
// it, so it is dropped and said out loud rather than erroring, which
// would cost the whole batch of proposals a run made
// (model.FilterCustomFieldValues).
//
// A call that named no fields reads no declarations -- the ordinary
// proposal, on the ordinary deployment, costs nothing.
func proposedCustomFields(ctx context.Context, store *model.Store,
	args map[string]any) (map[string][]string, []string, error) {

	stated := argCustomFields(args["custom_fields"])
	if len(stated) == 0 {
		return nil, nil, nil
	}
	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		return nil, nil, err
	}
	kept, dropped := model.FilterCustomFieldValues(fields, stated)
	return kept, dropped, nil
}

// argCustomFields is propose_task's custom_fields argument as it
// arrives from encoding/json: an object keyed by field name whose values
// are a string or an array of them, which is the shape the task API
// takes too (ui.CustomFieldValue) because a field may be declared to
// hold a set (model.CustomField.Multiple).
//
// A value that is neither is kept, deliberately unstorable, so that
// FilterCustomFieldValues drops it and the note below names it: it is a
// value this deployment could not have stored under any declaration, and
// the name is what a human needs to go and set it by hand.
func argCustomFields(v any) map[string][]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for name, item := range raw {
		out[name] = argCustomFieldValue(item)
	}
	return out
}

// argCustomFieldValue is one such value: a string is a set of one, an
// array of strings is itself, and anything else is rendered as the text
// it was so the name still reaches the note.
func argCustomFieldValue(item any) []string {
	switch v := item.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, one := range v {
			if s, ok := one.(string); ok {
				out = append(out, s)
				continue
			}
			out = append(out, fmt.Sprintf("%v", one))
		}
		return out
	default:
		return []string{fmt.Sprintf("%v", item)}
	}
}

// droppedCustomFieldNote is what a human approving the proposal reads in
// place of a value the run asked for and this deployment would not take
// -- a field nobody declares (a name the run guessed at, or one retired
// since the prompt that named it), or a value its declaration does not
// allow (a choice that is not one of an enum's, a date that is not a
// date).
//
// It exists for unresolvedDependencyNote's reason exactly: the run said
// something about this work, grain did not act on it, and a proposal
// filed silently without it leaves the human approving it with no idea
// anything was said. The names, not the values: what the approver does
// about it is open the task and set the field, and the name is what
// their own field list is keyed by.
func droppedCustomFieldNote(dropped []string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: the run that proposed this asked to set %s on it, "+
		"which named no field this deployment declares or no value the declaration allows. "+
		"Nothing was recorded for them -- set them by hand if they matter.",
		"`"+strings.Join(dropped, "`, `")+"`")
}

// droppedNetworkAccessNote is what a human approving the proposal reads
// in place of a network_access the run asked for and grain has no
// sandbox for. The proposal carries no override instead, so it runs on
// the deployment's default -- which may be more network than the run
// meant to ask for, and is why this is said rather than left silent.
func droppedNetworkAccessNote(asked string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: the run that proposed this asked for network_access %q, "+
		"which is not one grain can build a sandbox for (%s). Nothing was recorded, so it "+
		"runs on the deployment's default network -- set it by hand if that matters.",
		asked, model.NetworkAccessNames())
}

// otherBranchAutoMergeNote is what a human approving the proposal reads
// in place of the auto-merge it would have inherited had it landed where
// the run that proposed it does -- attributed to grain, since the rest of
// the body is the agent's own words.
//
// It says which two branches those are rather than just "a different
// branch": the whole decision is that they differ, and the one thing the
// approver can do about it -- file this work against the parent's branch
// instead, or turn auto-merge on where it is -- needs both names.
func otherBranchAutoMergeNote(task, proposal model.Task) string {
	return fmt.Sprintf("\n\n---\n\ngrain: task %s, which proposed this, is an auto-merge job on "+
		"%s. This task lands on %s instead, so it did not inherit that -- auto-merge is a "+
		"permission for one branch, not for the run holding it. Turn it on when you approve "+
		"this if it belongs here too.",
		task.ID, landingBranch(task), landingBranch(proposal))
}

// changeTargetAutoMergeNote is otherBranchAutoMergeNote for a proposal
// with a change target: it would have inherited auto-merge, and has no
// pull request for it to apply to.
func changeTargetAutoMergeNote(task, proposal model.Task) string {
	return fmt.Sprintf("\n\n---\n\ngrain: task %s, which proposed this, is an auto-merge job, but this "+
		"task's change is a %s change with no git pull request, so it did not inherit that. "+
		"Its change is submitted by a person through its provider.",
		task.ID, proposal.ChangeTarget.Kind)
}

// landingBranch names the branch a task's work lands on, for the sentence
// above: its Base when it has one, and otherwise the target repo's
// default branch, unnamed for the same reason baseDescription leaves it
// unnamed -- grain does not know which branch that is here, and naming
// the wrong one would be worse than saying nothing.
//
// The repository comes with it, since a proposal can now land in a
// different one from its parent (proposedTarget): "`main`" and
// "`main`" are the two branches in that sentence, and without the repo
// names the note reads as though grain refused an inheritance between
// two identical things.
func landingBranch(task model.Task) string {
	if task.Target == nil {
		if task.Base != "" {
			return "`" + task.Base + "`"
		}
		return "its repository's default branch"
	}
	if task.Base != "" {
		return fmt.Sprintf("`%s` in `%s`", task.Base, task.Target)
	}
	return fmt.Sprintf("the default branch of `%s`", task.Target)
}

// argStrings reads a tool argument that should have arrived as an array
// of strings, skipping anything in it that did not. Tool arguments reach
// here as map[string]any straight out of the transcript, so this repeats
// mcp.argStringSlice rather than importing it: agent.ToolCall.Arguments
// is whatever the framework reported, not something mcp validated.
func argStrings(v any) []string {
	if already, ok := v.([]string); ok {
		return already
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func proposedReads(args map[string]any, target *model.RepoRef) []model.RepoRef {
	var out []model.RepoRef
	for _, raw := range argStrings(args["reads"]) {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		repo, err := model.ParseRepo(text)
		if err != nil {
			continue
		}
		if target != nil && repo.Same(*target) {
			continue
		}
		seen := false
		for _, already := range out {
			if already.Same(repo) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, repo)
		}
	}
	return out
}

func proposedWrites(args map[string]any, target *model.RepoRef, base string) []model.WriteTarget {
	v, ok := args["writes"]
	if !ok || v == nil {
		return nil
	}
	var rawItems []any
	switch val := v.(type) {
	case []any:
		rawItems = val
	case []map[string]any:
		for _, m := range val {
			rawItems = append(rawItems, m)
		}
	case []mcp.WriteTarget:
		for _, wt := range val {
			rawItems = append(rawItems, map[string]any{"repo": wt.Repo, "base": wt.Base})
		}
	}
	var out []model.WriteTarget
	for _, item := range rawItems {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		repoStr, _ := obj["repo"].(string)
		baseStr, _ := obj["base"].(string)
		repoStr = strings.TrimSpace(repoStr)
		if repoStr == "" {
			continue
		}
		repo, err := model.ParseRepo(repoStr)
		if err != nil {
			continue
		}
		baseStr = strings.TrimSpace(baseStr)
		if target != nil && repo.Same(*target) && (baseStr == base || baseStr == "") {
			continue
		}
		dup := false
		for _, already := range out {
			if already.Repo.Same(repo) && already.Base == baseStr {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, model.WriteTarget{Repo: repo, Base: baseStr})
		}
	}
	return out
}

// proposedReviewTemplate is the review template a proposal from task is
// filed with: the review_template_id the run named, if it exists and is
// within task's own grants (model.ReviewTemplateWithinTask), and
// otherwise target's declared default review, if it has one. A named
// template refused for its grants falls back to that default with a note
// for the proposal's body saying so, rather than failing the proposal:
// the review is filed approved and auto-merge on the branch the run
// wrote (fileReviewTask), so a template carrying more than the run's own
// task is that much handed to work nobody looked at.
func proposedReviewTemplate(ctx context.Context, store *model.Store, task model.Task,
	args map[string]any, target *model.RepoRef) (id, note string, err error) {
	var fallback string
	if target != nil {
		def := model.DefaultReviewTemplateID(*target)
		tmpl, err := store.GetTemplate(ctx, def)
		if err != nil {
			return "", "", err
		}
		if tmpl != nil {
			fallback = def
		}
	}
	named, _ := args["review_template_id"].(string)
	named = strings.TrimSpace(named)
	if named != "" {
		tmpl, err := store.GetTemplate(ctx, named)
		if err != nil {
			return "", "", err
		}
		if tmpl != nil {
			if why := model.ReviewTemplateWithinTask(task, *tmpl, target); why != nil {
				return fallback, model.ReviewTemplateRefusedNote(task.ID, named, fallback, why), nil
			}
			return named, "", nil
		}
	}
	return fallback, "", nil
}

// finishWithPullRequest records that task's run pushed a real branch:
// find or open its PR, link it onto the task, and observe the task
// completed -- StateOf's own precedence means a task with CompletedAt set
// and no ClosedAt yet reads as one of the two post-run states
// ('awaiting_submit' until somebody submits it, 'completed' once they
// have), both of which SyncPullRequests watches.
func finishWithPullRequest(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) error {

	pr, landed, err := ensurePullRequest(ctx, store, client, task, now)
	if err != nil {
		return fmt.Errorf("orchestrator: opening a pull request for %s: %w", task.ID, err)
	}
	if pr.Number == 0 {
		return nil
	}
	if landed.partial() {
		if err := noteOnTask(ctx, store, task.ID, fmt.Sprintf("Opened %s/%s#%d. %s",
			task.Target.Owner, task.Target.Name, pr.Number, landed.partialSentence()), now); err != nil {
			return err
		}
	}

	// linkPullRequest (pullrequest.go), not an UpdateTask written out
	// here: a run that opened its own pull request mid-flight through
	// open_pull_request has already recorded this very link, and both
	// paths writing it the same idempotent way is what keeps the second
	// one a no-op rather than a duplicate.
	ref := model.PullRequestRef{Repo: *task.Target, Number: pr.Number}
	if err := linkPullRequest(ctx, store, task.ID, ref); err != nil {
		return err
	}
	// The images the run attached to what it said, which its commit
	// messages cannot carry (images.go). By the time this runs the note
	// has been relayed: processResult relays before it salvages.
	publishImages(ctx, store, client, task, pr.Number)

	// Before the task reads completed, which is what puts it in front of
	// the merge queue: a change that touches the trust boundary must
	// already have lost its auto-merge by then (securityhold.go).
	if err := holdForTrustBoundary(ctx, store, client, task, ref, "", now); err != nil {
		return err
	}
	// And for the same reason, before it reads completed: a change a
	// repository's gating trigger holds must have lost its auto-merge by
	// then (pathtriggerhold.go). A failure here is said, not returned --
	// the merge queue judges the head again before it merges it.
	if err := judgePullRequestTriggers(ctx, store, client, task, ref, "", now); err != nil {
		log.Printf("orchestrator: judging %s against its repository's triggers: %v -- "+
			"it is judged again before it merges", ref, err)
	}

	if taskKeepRunning(ctx, store, task) {
		markKeepRunningRequeued(store, task.ID, "", false)
		return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = nil
		})
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.RetryRequestedAt = nil
	})
}

func taskKeepRunning(ctx context.Context, store *model.Store, task model.Task) bool {
	if store != nil && task.ID != "" {
		if fresh, err := store.GetTask(ctx, task.ID); err == nil && fresh != nil {
			return fresh.KeepRunning
		}
	}
	return task.KeepRunning
}

type keepRunningPending struct {
	runID         string
	nothingNeeded bool
}

var keepRunningRequeued sync.Map // map[*model.Store]*sync.Map (taskID -> keepRunningPending)

func markKeepRunningRequeued(store *model.Store, taskID, runID string, nothingNeeded bool) {
	if store == nil || taskID == "" {
		return
	}
	v, _ := keepRunningRequeued.LoadOrStore(store, &sync.Map{})
	v.(*sync.Map).Store(taskID, keepRunningPending{runID: runID, nothingNeeded: nothingNeeded})
}

func clearKeepRunningRequeued(store *model.Store, taskID string) {
	if store == nil || taskID == "" {
		return
	}
	if v, ok := keepRunningRequeued.Load(store); ok {
		v.(*sync.Map).Delete(taskID)
	}
}

func settleStoppedKeepRunningTasks(ctx context.Context, store *model.Store, now time.Time) {
	if store == nil {
		return
	}
	v, ok := keepRunningRequeued.Load(store)
	if !ok {
		return
	}
	m := v.(*sync.Map)
	m.Range(func(key, value any) bool {
		taskID := key.(string)
		pending := value.(keepRunningPending)
		t, err := store.GetTask(ctx, taskID)
		if err != nil || t == nil {
			m.Delete(taskID)
			return true
		}
		if t.KeepRunning {
			return true
		}
		m.Delete(taskID)
		st, err := store.State(ctx, taskID)
		if err != nil || st != model.StateQueued {
			return true
		}
		if goal, _ := store.TaskGoal(ctx, taskID); goal != nil {
			_, _ = restOnGoal(ctx, store, *t, pending.runID, now)
			return true
		}
		if pending.nothingNeeded {
			_ = finishNothingNeeded(ctx, store, *t, pending.runID, now)
			return true
		}
		_ = observeField(ctx, store, taskID, now, func(o *model.Observation) {
			o.CompletedAt = &now
			o.RetryRequestedAt = nil
		})
		return true
	})
}

// taskClosed reports whether taskID's current observation already has
// ClosedAt set -- read fresh from the store rather than off the caller's
// task/result, since a close racing a still-live run is exactly the case
// this exists to catch.
func taskClosed(ctx context.Context, store *model.Store, taskID string) (bool, error) {
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("orchestrator: checking whether %s closed while its run was live: %w", taskID, err)
	}
	return obs != nil && obs.ClosedAt != nil, nil
}

// observeField is Store.ObserveField with this package's error prefix.
// The read-modify-write itself moved to the store once the CLI and the UI
// needed the same thing -- Observe REPLACEs the whole row rather than
// patching one column (its own doc comment on task_observation's schema,
// and simulate_test.go's
// TestGitHubSyncObservationsReplaceTheWholeRowNotJustTheChangedField),
// so every caller changing one field without erasing the others has to
// read the row first, and that is not per-package logic.
func observeField(ctx context.Context, store *model.Store, taskID string, now time.Time,
	set func(*model.Observation)) error {

	if err := store.ObserveField(ctx, taskID, now, set); err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	return nil
}

// EnsurePullRequest finds task's already-open PR for its own branch, or
// opens one -- FindOpenPullRequestForBranch first, since GitHub allows at
// most one open PR per head branch and a retried finish (this cycle
// crashed after CreatePullRequest but before the link was recorded) must
// not try to open a second one.
//
// Either way it ends with a title and a description built from the
// branch's own commit messages (description.go): written at creation, and
// refreshed on a pull request that was already there, since the one a run
// opened mid-flight was named and described before it had finished
// pushing. Neither can fail this call -- see branchCommits and
// refreshPullRequest on why a pull request's prose is never worth failing
// a finish over.
//
// The store is here for one reason: a base branch that is no longer
// there. pullRequestBase retargets that at the repo's default branch and
// noteBaseRetarget writes down that it did, on the task itself -- see
// both of their doc comments for why the work is opened rather than
// refused, and why the retarget is never silent.
//
// The other 422 GitHub answers forever -- a head that carries no commits
// its base does not already have -- comes back as a *noCommitsError
// rather than as GitHub's own words, both when the compare above saw it
// coming and when only the refusal did. Nothing is retargeted and nothing
// is opened, because there is nothing to open: what to do about it is the
// caller's, and the two callers answer differently (salvagePushedBranch
// ends the task, OpenPullRequestForTask tells the run to commit
// something).
func EnsurePullRequest(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) (github.PullRequest, error) {

	pr, _, err := ensurePullRequest(ctx, store, client, task, now)
	return pr, err
}

// ensurePullRequest is EnsurePullRequest, also returning what the
// already-landed check (landed.go) found about a pull request it newly
// opened -- partial() when part of the branch was already on the base,
// which is what the caller tells the run and the task. A branch that
// landed in full opens nothing and comes back as an *alreadyLandedError,
// checked before the base is merged into it so that a branch with nothing
// left to propose is not given a merge commit either.
func ensurePullRequest(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) (github.PullRequest, landedWork, error) {

	branch := task.BranchName()
	if existing, err := client.FindOpenPullRequestForBranch(task.Target.Owner, task.Target.Name, branch); err != nil {
		return github.PullRequest{}, landedWork{}, err
	} else if existing != nil {
		refreshPullRequest(ctx, store, client, task, existing.Number)
		labelOrigin(client, task, existing.Number)
		return *existing, landedWork{}, nil
	}

	base, gone, err := pullRequestBase(ctx, store, client, task)
	if err != nil {
		return github.PullRequest{}, landedWork{}, err
	}
	if base == branch {
		return seedEmptyRepoBase(ctx, store, client, task, branch, now)
	}
	if gone != "" {
		if err := noteBaseRetarget(ctx, store, task, gone, base, now); err != nil {
			return github.PullRequest{}, landedWork{}, err
		}
	}

	// Before anything is written to the branch or opened for it: is the
	// work already on the base, by content (landed.go)? A squash merge
	// done by hand leaves the branch "ahead" by commit for ever, and that
	// is how gke-labs/granule task 147 got a second pull request for
	// work its first had already landed.
	landed := landedForTask(client, task, base, nil)
	if landed.all() {
		return github.PullRequest{}, landed, &alreadyLandedError{Work: landed}
	}

	if task.Target != nil {
		if behind, err := branchIsBehind(client, *task.Target, base, branch); err == nil && behind {
			_, mergeErr := client.MergeBranch(task.Target.Owner, task.Target.Name, branch, base, fmt.Sprintf("Merge %s into %s", base, branch))
			if mergeErr != nil {
				if github.IsMergeConflict(mergeErr) {
					files := github.ConflictFiles(mergeErr)
					return github.PullRequest{}, landedWork{}, &mergeConflictError{
						Repo:   *task.Target,
						Branch: branch,
						Base:   base,
						Files:  files,
					}
				}
				if !notFound(mergeErr) {
					return github.PullRequest{}, landedWork{}, mergeErr
				}
			}
		}
	}

	// One compare, read twice: for what the branch proposes, and for
	// whether it proposes anything at all. GitHub refuses a pull request
	// whose head adds nothing to its base -- see noCommitsError -- and
	// asking here is what turns that refusal into something this
	// package can explain rather than something it repeats. A compare
	// that could not be read (known false) is not an empty branch: the
	// pull request is attempted anyway, and the 422 below is the second
	// line of defence.
	commits, known := branchCommits(client, *task.Target, base, branch)
	if known && len(commits) == 0 {
		if store != nil {
			if closed, err := taskClosed(ctx, store, task.ID); err == nil && closed {
				return github.PullRequest{}, landedWork{Base: base, Branch: branch, Known: true}, nil
			}
		}
		return github.PullRequest{}, landedWork{}, &noCommitsError{Repo: *task.Target, Branch: branch, Base: base}
	}

	// Named after the change rather than after the task that asked for it
	// -- titleFromCommits' own doc comment has why, and why that is the
	// only way an agent gets to name its pull request. The task's title
	// is the fallback for a branch whose commits say nothing, which is
	// what every pull request grain opened was called before this.
	title := titleFromCommits(commits)
	if title == "" {
		title = task.Title
	}
	if title == "" {
		title = "grain: " + task.ID
	}
	body := withLineage(pullRequestBody(describeCommits(commits), task.ID, task.Issue), taskLineage(ctx, store, task))
	if landed.partial() {
		body = landed.partialSentence() + "\n\n" + body
	}
	pr, err := client.CreatePullRequest(task.Target.Owner, task.Target.Name, branch, base, title, body)
	if err != nil && github.IsNoCommitsBetween(err) {
		// The same condition, learnt from GitHub instead of from the
		// compare: the read above failed, or the branch moved between the
		// two calls (a run's last push landing after this cycle looked).
		// Either way the answer is the one below, not a bare 422 nobody
		// downstream can act on.
		return github.PullRequest{}, landedWork{}, &noCommitsError{Repo: *task.Target, Branch: branch, Base: base}
	}
	if err != nil {
		return pr, landedWork{}, err
	}
	labelOrigin(client, task, pr.Number)
	return pr, landed, nil
}

// noCommitsError is a pushed branch that carries no commits its base does
// not already have. GitHub answers that with a 422 every time, so it is
// the vanished base's sibling: permanent, unaffected by retrying, and --
// until it was recognised here -- invisible except as a run detail
// quoting GitHub's own words.
//
// A typed error rather than a sentinel because the branch and the base
// are what the task has to be told (noteEmptyBranch): "no commits between
// main and grain/task-158" is a fact about two branches, and neither is
// in reach of the callers that have to act on it.
type noCommitsError struct {
	Repo   model.RepoRef
	Branch string
	Base   string
}

func (e *noCommitsError) Error() string {
	return fmt.Sprintf("%s on %s carries no commits %q does not already have, "+
		"so GitHub will not open a pull request for it", e.Branch, e.Repo, e.Base)
}

// emptyBranch reports whether err is that, anywhere in its chain -- the
// one question every caller of the finish path asks about an error it
// gets back, since this is the single failure that will not come out
// differently next time.
func emptyBranch(err error) (*noCommitsError, bool) {
	var e *noCommitsError
	ok := errors.As(err, &e)
	return e, ok
}

// mergeConflictError is a head branch that conflicts with its base branch when
// attempting an automatic merge before opening a pull request.
type mergeConflictError struct {
	Repo   model.RepoRef
	Branch string
	Base   string
	Files  []string
}

func (e *mergeConflictError) Error() string {
	if len(e.Files) > 0 {
		return fmt.Sprintf("branch %q conflicts with %q on %s in file(s): %s; resolve the merge conflict locally and push before opening a pull request",
			e.Branch, e.Base, e.Repo, strings.Join(e.Files, ", "))
	}
	return fmt.Sprintf("branch %q conflicts with %q on %s; resolve the merge conflict locally and push before opening a pull request",
		e.Branch, e.Base, e.Repo)
}

func conflictError(err error) (*mergeConflictError, bool) {
	var e *mergeConflictError
	ok := errors.As(err, &e)
	return e, ok
}

// branchIsBehind reports whether head is behind base on repo -- that is,
// whether base carries commits that head does not yet contain.
func branchIsBehind(client github.Client, repo model.RepoRef, base, head string) (bool, error) {
	comp, err := client.CompareRefs(repo.Owner, repo.Name, base, head)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	return comp.Status == "behind" || comp.Status == "diverged", nil
}

// pullRequestBase is the branch task's pull request opens against: its
// own Base when that is still a branch on the remote, and otherwise the
// repo's default branch -- returned alongside the base it replaced ("" on
// the ordinary path, where nothing was replaced).
//
// task.Base used to be passed to CreatePullRequest unread, and GitHub
// answers a base that does not exist with a 422 every time. A task
// pointed at a dead base could then never be finished at all: the agent
// cloned, worked, committed and pushed, the branch was real on the
// remote, and the one call that would turn it into a pull request was
// refused -- so the task stayed un-completed, dispatch offered it again
// after its backoff, and the next attempt did the same thing on top of
// the same branch, forever.
//
// A task pointed at a dead base is not exotic. New task prefills Base
// from the repo's last task (bwsalmon/agents#641), so a branch that
// merges between one task being filed and the next being dispatched
// leaves a task pointed at nothing -- prepareCheckout's own doc comment
// makes the same point about the clone.
//
// Retargeting rather than refusing is a decision about *when* this runs.
// By the time anything here is called the work exists: it is committed
// and pushed to a branch on the remote, and refusing it forever helps
// nobody. The overwhelmingly likely reason a base vanished is that it
// merged, in which case its commits are in the default branch already and
// the pull request's diff is the same either way. When it is not -- a
// misspelled base, or work that was abandoned -- the pull request is
// aimed at the wrong branch, which is exactly why noteBaseRetarget says
// so on the task rather than letting this be a quiet fallback. The place
// to catch that case cheaply is before any work happens, and
// prepareCheckout still does: it refuses to start a run whose base is
// gone and there is nothing pushed yet to lose.
//
// The DefaultBranch call is also what keeps a broken client from being
// read as a vanished branch. BranchExists reports a 404 as "no such
// branch", and every other way of failing to reach a repo -- a transport
// aimed at the wrong host, a token that lost its scope, a repo turned
// private -- 404s identically (branchExistsSettled's own doc comment has
// the history). A client that cannot see the repo cannot read its default
// branch either, so that read fails and this reports the failure instead
// of retargeting a base that is perfectly fine.
func pullRequestBase(ctx context.Context, store *model.Store, client github.Client, task model.Task) (base, retargetedFrom string, err error) {
	if task.Base != "" {
		exists, err := client.BranchExists(task.Target.Owner, task.Target.Name, task.Base)
		if err != nil {
			return "", "", fmt.Errorf("orchestrator: checking %s's base branch %q: %w", task.ID, task.Base, err)
		}
		if exists {
			return task.Base, "", nil
		}
		if store != nil {
			if targetBase, _, merged, err := store.MergedUpstreamTaskBase(ctx, *task.Target, task.Base); err == nil && merged && targetBase != "" && targetBase != task.Base && targetBase != task.BranchName() {
				if targetExists, err := client.BranchExists(task.Target.Owner, task.Target.Name, targetBase); err == nil && targetExists {
					return targetBase, task.Base, nil
				}
			}
		}
	}
	def, err := client.DefaultBranch(task.Target.Owner, task.Target.Name)
	if err != nil {
		return "", "", err
	}
	if task.Base == "" || def == task.BranchName() {
		return def, "", nil
	}
	return def, task.Base, nil
}

// seedEmptyRepoBase initializes an empty repository's initial trunk branch at
// the pushed task branch's tip and completes the task as landed there.
//
// When a task runs against an empty repository (0 commits), prepareCheckout
// starts on the task's own branch unborn, and the agent's first git push makes
// that task branch the repository's first branch -- which GitHub automatically
// adopts as the repository's default_branch. pullRequestBase then returns
// base == branch, which GitHub would refuse to open a pull request for (or
// compare as 0 commits between branch and itself). Creating the intended base
// branch (task.Base if set, otherwise "main") at the pushed commit, setting it
// as the repository's default_branch, and marking the task done lands the
// initial commit where it belongs without opening a meaningless pull request.
func seedEmptyRepoBase(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, branch string, now time.Time) (github.PullRequest, landedWork, error) {

	targetBase := task.Base
	if targetBase == "" || targetBase == branch {
		targetBase = "main"
	}
	head, err := client.GetBranchHead(task.Target.Owner, task.Target.Name, branch)
	if err != nil {
		return github.PullRequest{}, landedWork{}, fmt.Errorf("reading %s's tip on %s: %w", branch, task.Target, err)
	}
	if head == nil || head.SHA == "" {
		return github.PullRequest{}, landedWork{}, fmt.Errorf("branch %q on %s has no head commit", branch, task.Target)
	}
	exists, err := client.BranchExists(task.Target.Owner, task.Target.Name, targetBase)
	if err != nil {
		return github.PullRequest{}, landedWork{}, fmt.Errorf("checking %q on %s: %w", targetBase, task.Target, err)
	}
	if !exists {
		if err := client.CreateBranch(task.Target.Owner, task.Target.Name, targetBase, head.SHA); err != nil {
			return github.PullRequest{}, landedWork{}, fmt.Errorf("initializing %q on empty repository %s: %w", targetBase, task.Target, err)
		}
	}
	if err := client.SetDefaultBranch(task.Target.Owner, task.Target.Name, targetBase); err != nil {
		return github.PullRequest{}, landedWork{}, fmt.Errorf("setting default branch of %s to %q: %w", task.Target, targetBase, err)
	}
	if store != nil {
		body := fmt.Sprintf(
			"Branch `%s` was the first branch pushed to empty repository %s, so grain initialized `%s` at `%s` and set it as the repository's default branch rather than opening a pull request against itself.\n\ngrain has marked this task done as landed on `%s`.",
			branch, task.Target, targetBase, shortSHA(head.SHA), targetBase,
		)
		if err := noteOnTask(ctx, store, task.ID, body, now); err != nil {
			return github.PullRequest{}, landedWork{}, err
		}
		if err := observeField(ctx, store, task.ID, now, func(o *model.Observation) {
			o.CompletedAt = &now
			o.ClosedAt = &now
			o.RetryRequestedAt = nil
		}); err != nil {
			return github.PullRequest{}, landedWork{}, err
		}
	}
	return github.PullRequest{}, landedWork{
		Base:       targetBase,
		Branch:     branch,
		Known:      true,
		MergedSHA:  head.SHA,
		MergedHead: true,
	}, nil
}

// noteBaseRetarget records that task's pull request was opened against
// `to` because `from`, the base it asked for, is no longer on the remote.
//
// Both halves matter. The task's own row is changed, so that everything
// downstream of it agrees with the pull request that is about to exist:
// BuildPrompt tells a run to keep merging its base into its branch and
// would otherwise send the next attempt at a branch that is not there,
// prepareCheckout would refuse that attempt outright, and the task's Base
// as shown in the UI would name a branch nobody can look at. And a
// comment says what happened in the human's own conversation, because
// changing a field somebody set, without saying so, is how a task quietly
// ends up meaning something other than what it was filed as. The comment
// names the branch that went missing, so the change is legible and
// reversible by hand.
//
// It is attributed to grain itself rather than on behalf of the agent
// (relayComment's attribution): the run said nothing about any of this,
// grain decided it.
//
// The row is only rewritten while it still says `from`, and the comment
// only follows a row that this call actually changed -- so a retried
// finish that reaches here twice leaves one comment, not two.
func noteBaseRetarget(ctx context.Context, store *model.Store, task model.Task,
	from, to string, now time.Time) error {

	changed := false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if t.Base == from {
			t.Base, changed = to, true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("orchestrator: retargeting %s at %q: %w", task.ID, to, err)
	}
	if !changed {
		return nil
	}

	log.Printf("orchestrator: task %s's base branch %q is gone from %s; opening its pull request against %q instead",
		task.ID, from, task.Target, to)

	body := fmt.Sprintf(
		"This task's base branch `%s` no longer exists on %s, so its pull request "+
			"was opened against `%s`, the repository's default branch, and the task's "+
			"own base was changed to match.\n\n"+
			"A base branch usually disappears because it merged and was deleted, in "+
			"which case its commits are in `%s` already and this pull request's diff "+
			"is the same either way. If `%s` went missing for some other reason -- a "+
			"typo, or work that was abandoned -- then this pull request is aimed at "+
			"the wrong branch: retarget it on GitHub, or close it and point this task "+
			"at a branch that exists.",
		from, task.Target, to, to, from,
	)
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: recording %s's retargeted base: %w", task.ID, err)
	}
	return nil
}

// retargetTaskBase updates task.Base when task is stacked on an upstream task
// branch that has since merged (or when GitHub has retargeted task's open pull
// request after the upstream branch was deleted), so CI gating and merge-queue
// reconciliation read workflows from the live base branch rather than cloning
// a deleted feature branch.
func retargetTaskBase(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, prBaseRef string) (model.Task, bool, error) {

	if task.Target == nil || task.Base == "" {
		return task, false, nil
	}
	oldBase := task.Base
	var targetBase string
	var merged bool
	if store != nil {
		targetBase, _, merged, _ = store.MergedUpstreamTaskBase(ctx, *task.Target, oldBase)
	}
	if prBaseRef == "" && client != nil {
		if pr := model.TaskPullRequest(task); pr != nil && pr.Repo.Same(*task.Target) {
			if detail, err := client.GetPullRequest(pr.Repo.Owner, pr.Repo.Name, pr.Number); err == nil {
				prBaseRef = detail.BaseRef
			}
		} else if existing, err := client.FindOpenPullRequestForBranch(task.Target.Owner, task.Target.Name, task.BranchName()); err == nil && existing != nil {
			if detail, err := client.GetPullRequest(task.Target.Owner, task.Target.Name, existing.Number); err == nil {
				prBaseRef = detail.BaseRef
			}
		}
	}
	newBase := targetBase
	if merged {
		if prBaseRef != "" && prBaseRef != oldBase {
			newBase = prBaseRef
		} else if newBase == "" && client != nil {
			if def, err := client.DefaultBranch(task.Target.Owner, task.Target.Name); err == nil && def != "" && def != task.BranchName() {
				newBase = def
			}
		}
	} else if prBaseRef != "" && prBaseRef != oldBase && client != nil {
		if exists, err := client.BranchExists(task.Target.Owner, task.Target.Name, oldBase); err == nil && !exists {
			merged = true
			newBase = prBaseRef
		}
	}
	if !merged || newBase == oldBase {
		return task, false, nil
	}
	unattended := skipsClassification(task)
	if store != nil {
		if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
			if t.Base == oldBase {
				t.Base = newBase
				if skipsClassification(*t) {
					// A review or fix is never classified on its own account
					// (holdForTrustBoundary skips it: it merges into the branch
					// it was stacked on). Moved onto that branch's own base,
					// its pull request would land there unclassified and
					// unattended, so it waits for a person instead -- as
					// closeObsoleteReviews and closeSecurityReviewOnSettle do.
					t.AutoMerge = false
				}
			}
			return nil
		}); err != nil {
			return task, false, err
		}
	}
	log.Printf("orchestrator: retargeted task %s's base on %s from %q to %q",
		task.ID, task.Target, oldBase, newBase)
	task.Base = newBase
	if unattended {
		task.AutoMerge = false
	}
	return task, true, nil
}

// skipsClassification reports whether task is one holdForTrustBoundary
// never classifies, because it merges back into the branch it was stacked
// on rather than into a branch of its own choosing.
func skipsClassification(task model.Task) bool {
	return task.Origin.Reason == model.ReasonReview || task.Origin.Reason == model.ReasonFix
}

// noteEmptyBranch is what a task gets instead of a pull request when the
// branch its run pushed carries no commits its base does not already
// have: an explanation in its own conversation, and a stop.
//
// The stop is the judgement call, and it is a different one from the
// vanished base's. There, work existed and only its target was wrong, so
// pullRequestBase retargets and the pull request opens. Here there is no
// work: nothing to salvage, nothing to retarget, nothing for a reviewer
// to look at. Left alone, the task stayed un-completed and dispatch
// offered it again after its backoff, and every attempt continued the
// same empty branch and was refused in exactly the same way -- five full
// agent runs (model.MaxConsecutiveFailures) burnt on a call that cannot
// succeed, ending in a task that reads 'failed' with GitHub's own 422 as
// the only account of why.
//
// So it stops after one, by parking the task on this comment
// (Observation.PendingQuestionCommentID -- the same field ask_question's
// own handling sets, and selfrepair.Confirm's). Parking rather than
// closing, because which of the three ways a branch ends up empty this
// was -- a run that reverted its own work, one that committed only what
// the base already had, one that pushed without committing at all -- is
// not something grain can tell, and only one of them means the task
// itself is finished. Parking costs nothing and is undone by one reply:
// any comment on the task clears the pending question (ui.Client's own
// Reply) and it is queued again, with this explanation in the thread the
// next run reads back. Closing would have thrown a live task away on
// grain's own guess; another four attempts would have thrown money at a
// refusal nobody was watching.
//
// A closing comment from the run itself does not change the answer,
// though it is relayed first and sits above this one in the thread. A run
// that signs off while the branch it pushed carries nothing has made two
// claims that do not agree, and a person reading both is the point of
// parking; completing the task on the strength of the sign-off alone
// would file "done" against a remote where nothing landed.
//
// Attributed to grain itself rather than on behalf of the agent, for
// noteBaseRetarget's reason: the run said none of this, grain worked it
// out.
func noteEmptyBranch(ctx context.Context, store *model.Store, task model.Task,
	empty *noCommitsError, now time.Time) error {

	log.Printf("orchestrator: task %s pushed %s to %s with no commits over %q; "+
		"no pull request can be opened for it, so the task is parked rather than retried",
		task.ID, empty.Branch, empty.Repo, empty.Base)

	body := fmt.Sprintf(
		"This task's run pushed `%s` to %s, but that branch carries no commits `%s` "+
			"does not already have -- GitHub refuses to open a pull request for it "+
			"(\"No commits between %s and %s\"), and refuses it the same way every time.\n\n"+
			"There is nothing here to open, retarget or merge. A branch ends up like this "+
			"when the run reverted its own work, committed only changes `%s` already had, or "+
			"pushed without committing anything at all. Another attempt would continue this "+
			"same branch, so grain has stopped rather than repeating the refusal.\n\n"+
			"Reply on this task to have it dispatched again -- saying what should be "+
			"different is worth more than \"try again\", since the next run reads this "+
			"conversation -- or close it if the change turned out not to be needed.",
		empty.Branch, empty.Repo, empty.Base, empty.Base, empty.Branch, empty.Base,
	)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: recording that %s's branch is empty: %w", task.ID, err)
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}

// emptyBranchDetail is the same finding on the run's own row, where
// `grain get` shows it: short, and about this run rather than about the
// task, since the task has the comment above.
func emptyBranchDetail(empty *noCommitsError) string {
	return fmt.Sprintf("the run pushed %s, but it carries no commits %q does not already have, "+
		"so there was no pull request to open; the task is parked on grain's own comment about it",
		empty.Branch, empty.Base)
}

// requestedSuspension is the agent's own exit a run took, if it took one
// (docs/design/durable-tasks.md#DT-9): a wait_for_task park on tasks
// (parkedOn), a timed one (parkedUntil), or a resume_later call
// (mcp.ResumeLaterTaken) -- the last that the daemon took, since a later
// call replaces what an earlier one asked for. Its detail keeps the
// words a park always had (model.ParkedDetail, ParkedUntilDetail), which
// the next prompt reads back, for the two forms that are parks.
func requestedSuspension(ctx context.Context, store *model.Store, taskID string,
	result *agent.Result) (model.Suspension, bool, error) {
	susp := model.Suspension{Kind: model.SuspendRequested}
	targets, err := parkedOn(ctx, store, taskID, result)
	if err != nil {
		return susp, false, err
	}
	if len(targets) > 0 {
		when, reason, _ := resumedLater(result)
		susp.Detail = model.ParkedDetail(targets)
		if reason = strings.TrimSpace(reason); reason != "" {
			susp.Detail += ": " + reason
		}
		susp.ResumeOn = model.ResumeOn{Tasks: targets, UntilMerged: when.UntilMerged}.JSON()
		return susp, true, nil
	}
	if until, ok := parkedUntil(result); ok {
		susp.Detail = model.ParkedUntilDetail(until)
		susp.ResumeOn = model.ResumeOn{At: &until}.JSON()
		susp.ResumeAt = &until
		return susp, true, nil
	}
	when, reason, ok := resumedLater(result)
	if !ok {
		return susp, false, nil
	}
	susp.Detail = model.RequestedDetail(reason, when)
	if when.At != nil && when.CI == "" && !when.Message {
		susp.Detail = model.ParkedUntilDetail(*when.At)
		if r := strings.TrimSpace(reason); r != "" {
			susp.Detail += ": " + r
		}
	}
	susp.ResumeOn = when.JSON()
	susp.ResumeAt = when.At
	return susp, true, nil
}

// resumedLater is the condition and reason of the last resume_later call
// in result the daemon took, or false for a run that made none. Its
// task form is read by parkedOn, with wait_for_task's.
func resumedLater(result *agent.Result) (model.ResumeOn, string, bool) {
	if result == nil {
		return model.ResumeOn{}, "", false
	}
	var when model.ResumeOn
	var reason string
	found := false
	for _, c := range result.ToolCalls {
		if c.Name != "resume_later" || c.IsError {
			continue
		}
		if taken, ok := mcp.ResumeLaterTaken(c.Text); ok {
			when, found = taken, true
			reason, _ = c.Arguments["reason"].(string)
		}
	}
	return when, reason, found
}

// parkedUntil is the wake time a run parked its task until through
// wait_for_task's resume_at, or false if it did not.
//
// Unlike parkedOn, the call's own word is enough and the store is not
// read back: the deferral could already have been cleared by the time
// this runs -- a short park the wake reconciler reached while the run was
// still ending, or a person pressing Undefer -- and a task that is queued
// again is what a timed park was going to leave anyway. What must not
// happen is the run finishing the task it asked to come back to.
func parkedUntil(result *agent.Result) (time.Time, bool) {
	if result == nil {
		return time.Time{}, false
	}
	var until time.Time
	found := false
	for _, c := range result.ToolCalls {
		if c.Name != "wait_for_task" || c.IsError {
			continue
		}
		if t, ok := mcp.WaitForTaskParkedUntil(c.Arguments, c.Text); ok {
			until, found = t, true
		}
	}
	return until, found
}

// parkedOn is the tasks a run parked its task on through wait_for_task,
// or resume_later's task form, or nil if it did not park.
//
// Two facts, both required. The run's own call has to say it parked
// (mcp.WaitForTaskParked, off its arguments and result), and the task
// has to carry the depends-on links that park wrote -- read back from the
// store, since the task processResult was handed predates them. The
// second is what keeps a call that only looked, or one whose park the
// daemon refused, from leaving a task queued with nothing holding it.
func parkedOn(ctx context.Context, store *model.Store, taskID string, result *agent.Result) ([]string, error) {
	if result == nil {
		return nil, nil
	}
	var asked []string
	for _, c := range result.ToolCalls {
		if c.Name == "resume_later" && !c.IsError {
			if taken, ok := mcp.ResumeLaterTaken(c.Text); ok {
				asked = append(asked, taken.Tasks...)
			}
			continue
		}
		if c.Name != "wait_for_task" || c.IsError || !mcp.WaitForTaskParked(c.Arguments, c.Text) {
			continue
		}
		asked = append(asked, mcp.WaitForTaskIDs(c.Arguments)...)
	}
	if len(asked) == 0 {
		return nil, nil
	}
	current, err := store.GetTask(ctx, taskID)
	if err != nil || current == nil {
		return nil, err
	}
	var parked []string
	seen := map[string]bool{}
	for _, id := range asked {
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, l := range current.Links {
			if l.Kind == model.LinkDependsOn && l.Target == id {
				parked = append(parked, id)
				break
			}
		}
	}
	return parked, nil
}

// reviewOfGrantRefusedNote is the line added to a proposal whose grant
// named a review (model.ReviewOfArg) and was dropped for it.
func reviewOfGrantRefusedNote(grant string) string {
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal asked for `%s` with %q, which only a review "+
		"grain files itself may hold, so it was filed without that grant.",
		model.CapabilityGrantRef(grant), model.ReviewOfArg)
}

// resolveProposedForkHead resolves proposal's base when it is a GitHub
// fork head label (model.ForkHead): based on what the pull request it
// heads merges into, linked to that pull request and told about it
// (forkhead.PR). It returns why it could not, with the base cleared, and
// "" otherwise -- including for every plain branch, which is left alone.
func resolveProposedForkHead(client github.Client, proposal *model.Task) string {
	head, ok, err := model.ParseForkHead(proposal.Base)
	if !ok {
		return ""
	}
	label := proposal.Base
	proposal.Base = ""
	if err != nil {
		return fmt.Sprintf("its branch is not usable (%v), so it was filed on its repository's default branch", err)
	}
	if proposal.Target == nil {
		return fmt.Sprintf("its branch %q is a GitHub fork head, which names a pull request on a repository, "+
			"and it was filed with none", label)
	}
	finder, _ := client.(forkhead.Finder)
	if finder == nil {
		return fmt.Sprintf("its branch %q is a GitHub fork head, and grain cannot read %s's pull requests here "+
			"to find the one it heads, so it was filed on the repository's default branch", label, proposal.Target)
	}
	r, err := forkhead.Resolve(finder, *proposal.Target, head)
	if err != nil {
		return fmt.Sprintf("%v; it was filed on the repository's default branch", err)
	}
	proposal.Base = r.Base
	proposal.Links = append(proposal.Links, r.Link())
	proposal.Body += r.Note()
	return ""
}
