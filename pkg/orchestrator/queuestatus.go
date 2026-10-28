package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// The merge queue's own live states, written onto the task
// (Observation.MergeQueueState) every cycle and read by nothing but a
// person -- the row in the UI, and the one line on it that says what
// grain is doing now that the agent's run is over (ui/src/state.js).
//
// They exist because of what the queue looks like from outside when it is
// working correctly and slowly, and what it looked like when it was not
// working at all: both were a row that said "Merging" and never changed.
// The only two states there used to be were waiting_ci and merging, and
// between them they covered one of the several ways a finished task can
// sit for hours with nothing visibly happening. A change waiting its turn
// behind another, a change waiting on its own review, a pull request this
// deployment's credential cannot read, and a merge GitHub keeps refusing
// all said exactly nothing -- or, in the last case, said "Merging" once a
// cycle forever, which is worse than nothing. Each of them now says which
// it is, and mergeQueueNote says it in words.
//
// # The invariant
//
// A task grain is driving to a merge is in exactly one of these states at
// every moment, and each of them is either *grain is waiting on a named
// thing* or *something is working on it*. There is no combination of
// pull-request health, check results, review state, repair state and
// queue position that renders as no activity at all. statusOf is total
// over that cross-product, and TestStatusOfIsTotal is the whole of the
// regression: adding a health value, a check conclusion or an arm without
// giving it a state is a test failure rather than another silent row.
//
// It was not always so, and the shape of the failure is worth keeping.
// statusOf used to end in a bare `return queueStatus{}`, and "the checks
// are over and did not pass" matched no arm above it: waiting_ci wanted
// checks still outstanding, waiting_queue wanted PrClean, merge_failed
// wanted a merge GitHub had actually refused. grain CI on task 149's
// branch was cancelled twice (2026-09-17); a cancelled run is finished,
// so nothing was outstanding, and with no passing verdict nothing was
// clean, so no merge was ever attempted and nothing was ever refused. The
// entry fell out of the bottom with an empty status and stayed there,
// indistinguishable on the row from a task that was fine and quiet.
//
// # The cycle
//
// The states are a cycle rather than a lattice, and they are listed below
// in the order a change goes round it.
//
//  1. *auto-merging* -- grain is driving: waiting on checks (waiting_ci),
//     waiting its turn in its repository's queue (waiting_queue), waiting
//     on the review it declared (waiting_review), or making the merge
//     call (merging). unreadable is this phase with GitHub as the thing
//     being waited on.
//  2. *fixing merge* -- the change did not pass, so an agent gets it:
//     repair_needed until grain files the repair, repair_queued while the
//     requeued task waits for a worker, repairing while a run is on the
//     branch. The three are one phase split where the waits differ, since
//     with max_workers at 3 a repair can sit queued for hours and
//     "waiting for a worker" is a different sentence from "an agent is
//     fixing this now". agent_working is the same phase entered from
//     outside: a person putting an agent back on the task themselves
//     (ui.Client.RunNow), which holds the merge for as long as a repair
//     would and is no more the queue's decision than the push it makes.
//  3. *re-merging* -- the repair pushed, so the change is back in phase 1
//     with the new commit's checks. It needs no state of its own: it is
//     phase 1, and the states there already say which wait it is in.
//  4. *merge_failed* -- terminal. The round went round and did not land
//     it. grain has stopped driving, the note says what it died of, and
//     the only way out is a person pressing "Retry merge"
//     (ui.Client.RetryMerge), which clears what the queue remembers and
//     starts the cycle again from phase 1.
//
// # Where the cycle's bound lives
//
// Nowhere new, which is the point. Phase 2 happens at most twice per pull
// request: Observation.MergeQueueRepairs counts the repairs and nothing in
// the queue ever lowers it, the second is given only when the base moved
// or the failures changed since the first (secondRepairReason), and a
// change that comes out of its last repair still broken goes to phase 4
// rather than round again (requeueForRepair, escalateToUser). A repair that does not take
// buys one more base-branch merge and no more -- MergeQueueRefreshedAt,
// read against the repair stamp, so it is one refresh per repair round --
// and a repair that never finishes at all is bounded by
// defaultRepairDeadline. Phase 1 is bounded by defaultCheckStallDeadline,
// which times an unbroken run of PrPending reads however they are caused,
// a cancelled check included (forge.HealthFrom's "cancelled is neither").
// Three clocks and one stamp, all of them older than this comment; a
// fourth would be a fourth thing to reason about with nothing new to say.
//
// forge.ReadFailure, added later, is not a fourth: it bounds nothing and
// ends nothing. It gates a *sentence* -- how long reads have to go on
// failing before unreadable is written rather than waited out -- and the
// queue goes on driving the pull request identically either way. It is
// built in the shape of the two above so there is one thing to learn
// rather than two, which is the same reason there is no fourth bound.
//
// # Adding to this list
//
// Cheap and safe: an unknown state renders as no activity at all in a
// browser running an older build (state.js keeps its own map of the ones
// it knows), so the daemon and the UI need not be upgraded together. A
// state added here and not there is invisible rather than raw.
//
// Cheap and safe in a running deployment, that is -- not in this
// repository, where a state is expected to arrive in all three of the
// places that name it in the same commit: here, ui/src/state.js's
// MERGE_QUEUE_PHRASES, and pkg/mcp's mergeQueuePhrases, which is the same
// words for an agent describing the row. pkg/mcp's
// merge_queue_phrases_test.go reads this file and that one and fails
// when the three disagree, so the forgotten one is a test failure rather
// than a row that quietly says nothing for a release.
const (
	// mergeStateWaitingCI is CI that has not reported yet -- GitHub's
	// checks, grain's own run, or an empty check list still inside the
	// registration window.
	mergeStateWaitingCI = "waiting_ci"
	// mergeStateWaitingQueue is this change's turn not having come: it is
	// clean and it would merge, but another entry is ahead of it in its
	// repository's queue, or the repository's merges for this cycle are
	// already spent.
	mergeStateWaitingQueue = "waiting_queue"
	// mergeStateWaitingReview is a change held by the review it declared
	// (Task.ReviewTemplateID) until that review's own pull request lands
	// back on this branch.
	mergeStateWaitingReview = "waiting_review"
	// mergeStateMerging is the merge call itself, set before it is made
	// because it can block long enough for a person to see.
	mergeStateMerging = "merging"
	// mergeStateUpdatingBranch is the queue writing this repository's
	// base branch into the head branch before merging it, which it does
	// only where the repository has asked for branches to be up to date
	// (model.RepoConfig.RequireBranchUpToDate, bringHeadUpToDate). It is
	// phase 1 like merging above -- grain is driving, nobody is being
	// waited on -- and it is a state of its own rather than "merging"
	// because the two have opposite consequences: one lands the change,
	// and the other throws away the verdict it was about to land on and
	// starts CI over.
	//
	// Set imperatively by the arm that makes the write rather than
	// returned by statusOf, exactly as merging is: statusOf runs before
	// anything has been asked of GitHub, and until the 201 comes back
	// there is nothing to say this rather than "merging".
	mergeStateUpdatingBranch = "updating_branch"
	// mergeStateBatching and mergeStateGathering are retired merge-queue
	// states kept so stored rows, demo fixtures, MCP phrases, and UI state
	// maps continue to classify cleanly.
	mergeStateBatching  = "batching"
	mergeStateGathering = "gathering"
	// mergeStateChecksUnfixable is a change whose red is not its own: a
	// check that failed before a job of it ran a step, a check that will
	// never pass for the author grain files pull requests as
	// (model.RepoConfig.UnfixableChecks). Nothing pushed to the branch
	// turns it green, so the queue spends neither the base merge nor the
	// repair on it and says so on the row instead
	// (escalateUnfixableChecks, forge.Reds).
	//
	// It is phase 1 with an unusual thing being waited on -- a person,
	// or a setting -- and it is phase 1 for that reason:
	// grain is not driving this change and a live mark on the row would
	// say work is happening to it that is not. What it buys is the
	// sentence every entry in such a repository can carry at once. The
	// queue's own conclusion used to reach one task's comment thread,
	// the one that happened to be at the front, while the three behind
	// it read "Needs repairing" and waited their turn to spend an agent
	// on the same dead end (grain/task-291).
	//
	// The head of the queue does not stay here: it is escalated on the
	// cycle that reads it, in seconds, and moves to merge_failed. The
	// entries behind it wear this until their own turn comes.
	mergeStateChecksUnfixable = "checks_unfixable"
	// mergeStateWaitingGitHubApproval is a clean change GitHub refused to
	// merge for want of an approving review its own rules require --
	// CODEOWNERS with code-owner review required, a protection or
	// ruleset wanting approvals, a ruleset's required reviewers
	// (githubrefusal.go). Nothing is retried while it waits and it is out
	// of its repository's queue, so nothing behind it waits with it; the
	// queue reads the pull request's reviews every few minutes and asks
	// for the merge again when they change (holdRefusedMerge). Not
	// merge_failed: nothing has failed, and no grain button would help --
	// the note names who must approve, on GitHub.
	mergeStateWaitingGitHubApproval = "waiting_github_approval"
	// mergeStateGitHubQueue is a change grain has decided may land, on a
	// base branch whose rules only take changes through GitHub's own merge
	// queue: grain has added the pull request to that queue
	// (githubqueue.go) and GitHub merges it once its merge group passes.
	// The entry keeps its repository's slot while it is there; the cycle
	// that reads the pull request merged closes it out as any merge, and
	// one GitHub's queue drops is queued again a bounded number of times.
	mergeStateGitHubQueue = "github_merge_queue"
	// mergeStateRepairNeeded is a change that did not pass -- conflicted
	// with its base, or checks that failed -- and has not been sent back
	// to an agent yet. It is the entry to phase 2 and grain files the
	// repair on this very cycle, sending the task back to an agent without
	// waiting for it to reach the head of the queue.
	mergeStateRepairNeeded = "repair_needed"
	// mergeStateRepairQueued is the repair asked for and nobody working
	// on it yet: requeueForRepair has cleared CompletedAt, so the task is
	// back in the backlog, and no worker has picked it up. It ends when
	// dispatch starts the run -- which, on a deployment whose workers are
	// all busy, can be hours away.
	mergeStateRepairQueued = "repair_queued"
	// mergeStateRepairing is an agent's run in flight on the very branch
	// the pull request is open from. It ends when that run does, one way
	// or the other; a run that never ends at all is bounded by
	// defaultRepairDeadline.
	mergeStateRepairing = "repairing"
	// mergeStateWorking is the same branch being written to by a run
	// nobody here asked for: a person put an agent back on this task
	// themselves (ui.Client.RunNow). It is phase 2 without the queue
	// having decided anything -- the merge is held for the duration
	// (syncEntry's `working`) exactly as it is for a repair, because what
	// a merge would cut short is the same either way, and it ends when
	// that run ends. It has a word of its own rather than borrowing
	// "repairing" because a person watching would otherwise be told grain
	// asked for a run it did not ask for, about a failure it has not
	// found.
	mergeStateWorking = "agent_working"
	// mergeStateFailed is the terminal state: this round is over and the
	// change did not land. Two things reach it, and they are one state
	// rather than two because nothing further happens in either without a
	// person, and the same button is what a person presses.
	//
	// The first is a merge GitHub would not perform (forge.MergeRefusal).
	// The queue goes on asking, once a cycle -- a refusal is usually a
	// rule on GitHub that somebody fixes there rather than in the branch
	// -- and the note carries GitHub's own words and the count.
	//
	// The second is the queue having given up and said so
	// (Observation.MergeQueueBlockedAt, blockMergeQueue): a repair that
	// ran and did not take, a repair that never ran, checks that never
	// reported, a build that could not start. Those are the "Merge
	// blocked" chip on the row (ui/src/state.js's completionPhase), which
	// is this same state rendered on the badge axis rather than a second
	// state -- the chip says a person owns it, and the note here says
	// what they were handed and why.
	//
	// It is also the one state here that survives its task leaving the
	// queue's watched set (clearStaleQueueState): a verdict already
	// reached goes on being true of a change nobody is driving, and it is
	// a person, not a cycle, who takes it back.
	mergeStateFailed = "merge_failed"
	// mergeStateUnreadable is grain having no answer about this pull
	// request that it can act on. Three routes in: reads of the pull
	// request that have gone on failing for longer than a flake
	// (noteUnreadableEntry), checks this deployment's credential cannot
	// read (model.PrUnknown with !checksKnown), and a health value this build
	// does not recognise. It is the one state here that is not about the
	// branch: the queue is not waiting on anything in the pull request, it is
	// failing to ask.
	//
	// And, uniquely here, it is a statement about the cycle rather than
	// about the change, which is why one failed read does not earn it:
	// GitHub is flaky at a rate that makes a single 5xx mean nothing, and
	// noteUnreadableEntry has the evidence. Like every state here but
	// merge_failed, it is taken back when the queue stops watching the
	// task at all (clearStaleQueueState) -- a cycle that is not asking is
	// not failing to ask.
	mergeStateUnreadable = "unreadable"
)

// queueStatus is one entry's state and the sentence that goes with it.
type queueStatus struct {
	state string
	note  string
	// reason is the state's sub-case (livenessreasons.go), where the
	// writer knows the entry's particulars; setMergeQueueState fills in
	// the state's own when it is left empty (mergeReasonOfState).
	reason string
}

// statusOf works out what to tell a person about e this cycle, before
// anything is done about it. It is total for a task grain is driving: see
// the invariant at the top of this file, and TestStatusOfIsTotal.
//
// The order of the arms is the order the reasons matter in, which is not
// the order syncEntry acts in: giving up outranks everything, because it
// is the one of these that has stopped rather than taken time; a refusal
// comes next for the same reason; and the "waiting on somebody else" arms
// come before the queue position, because a change waiting on its review
// is not waiting for its turn -- its turn is being held for it.
//
// Two answers are deliberately blank, and each is its own arm rather than
// a fallthrough, so that "grain has nothing to say about this" can never
// again be reached by accident:
//
//   - A task that is not auto-merge. grain watches such a pull request to
//     close its task out; it does not drive it, and the task's own state
//     (awaiting_submit, for one with a pull request and no submit yet) is
//     the whole of the answer. A queue activity line beside that badge
//     would claim work nobody asked for.
//   - A pull request that merged or closed. syncEntry is closing the task
//     out on this same cycle, and this is past the point any of it is
//     about a merge that has still to happen.
func statusOf(e queueEntry, isMergeHead, blocked bool, review reviewState, heads map[string]string) queueStatus {
	task, health := e.task, e.health
	if !task.AutoMerge {
		return queueStatus{}
	}
	if health == model.PrMerged || health == model.PrClosed {
		return queueStatus{}
	}
	// A run in flight that the queue did not ask for -- somebody has put
	// an agent back on this task themselves (ui.Client.RunNow). Above
	// everything below it, the terminal state included, because it is the
	// one fact here that outranks the queue's own opinion: syncEntry will
	// not merge under such a run whatever the checks say (its `working`),
	// so a row saying "Merging" or "Merge failed" beside an agent pushing
	// to the branch would be the queue describing a decision it is not
	// making. The task somebody is most likely to run now is exactly the
	// one the queue tried, gave up on and left for a person.
	if e.running && !e.obs.RepairInFlight() {
		return queueStatus{state: mergeStateWorking, note: workingNote(e, blocked)}
	}
	if blocked {
		return blockedStatus(e)
	}
	if refusal, ok := lastMergeRefusal(e.ref, e.detail.HeadSHA); ok && health == model.PrClean {
		return refusalStatus(refusal, e.ref, e.detail.HTMLURL)
	}
	if q, ok := lastGitHubQueued(e.ref, e.detail.HeadSHA); ok && health == model.PrClean {
		return githubQueueStatus(q.entry, e.detail.BaseRef)
	}
	if review.pending {
		return queueStatus{state: mergeStateWaitingReview, note: reviewNote(review)}
	}
	// Phase 2, while the run the queue asked for is the thing that has to
	// happen next. Above the health arms on purpose: the checks on this
	// branch are about a commit an agent is still pushing over, so what
	// the row should say is who is holding it, not what CI last thought.
	if e.obs.RepairInFlight() {
		if e.running {
			return queueStatus{state: mergeStateRepairing, note: repairingNote(e)}
		}
		return queueStatus{state: mergeStateRepairQueued, note: repairQueuedNote(e)}
	}
	switch health {
	case model.PrConflicted, model.PrFailing:
		// Red nobody reaches from this branch, and none of it this
		// branch's own: there is no repair coming, so saying "needs
		// repairing" would be the row promising work the queue has
		// decided not to do. An entry that has *both* kinds keeps
		// repair_needed, because a repair really is coming for its half
		// -- repairNeededNote is what says the other half is there.
		if !e.reds.PathToGreen() && !e.reds.BranchOwns() {
			return queueStatus{state: mergeStateChecksUnfixable, note: unfixableRowNote(e)}
		}
		return queueStatus{state: mergeStateRepairNeeded, note: repairNeededNote(e)}
	case model.PrPending:
		// Every way CI can fail to produce a verdict, including the ones
		// that will never produce one now: a check GitHub cancelled, a
		// grain CI run that was cancelled, a run that could not be done
		// at all. stalledChecksPhrase names whichever it is, and this
		// wait is bounded by the clock that is already running on it
		// (forge.ChecksStalled, escalateStalledChecks).
		return queueStatus{
			state: mergeStateWaitingCI,
			note:  "waiting on " + waitingPhrase(e),
		}
	case model.PrUnknown:
		if !e.checksKnown {
			return queueStatus{state: mergeStateUnreadable, note: unknownHealthNote(e)}
		}
		return queueStatus{state: mergeStateWaitingQueue, note: unknownHealthNote(e)}
	case model.PrClean:
		if isQueueMember(e) && !isMergeHead {
			return queueStatus{state: mergeStateWaitingQueue, note: queuePositionNote(e, heads)}
		}
		// Everything else clean merges on this very cycle: a stacked task
		// (a fix or a review, which is nobody's queue member and merges
		// into the branch it was filed against) or this repository's
		// merge head. syncEntry writes the same state again immediately
		// before the call, which is where the pinning and the refusal
		// flicker are reasoned about; saying it here as well is what
		// keeps this function total rather than leaving one clean case
		// to be named by the arm that acts on it.
		return queueStatus{state: mergeStateMerging}
	}
	// Unreachable today -- the switch covers every model.PrHealth, and
	// merged and closed are answered above -- and deliberately not a
	// panic. A health value added to model, or read off some future
	// GitHub field, must arrive as a row that says grain does not
	// understand it, not as the silence this whole file exists to end.
	return queueStatus{
		state: mergeStateUnreadable,
		note:  fmt.Sprintf("grain does not recognise the state it read for this pull request (%q)", health),
	}
}

// blockedStatus is the terminal state for an entry the queue has given up
// on (Observation.MergeQueueBlockedAt).
//
// The note is whatever blockMergeQueue wrote when it gave up, kept rather
// than recomputed: that sentence names which phase of the cycle this died
// in -- a repair that did not take, a repair that never ran, checks that
// never reported -- and nothing readable off the entry this cycle can
// reconstruct it. Only an entry blocked by a build older than that
// sentence, or one whose note has been overwritten since, falls back to
// what can be said from the health in front of us.
//
// This used to be no state at all, on the reasoning that the row already
// carried the "Merge blocked" chip and a live activity line beside it
// would be the queue claiming to do something it had stopped doing. The
// chip is still there and still says a person owns it; what was missing
// was the *why*, which is on the task's own thread and was nowhere on the
// row. A terminal state that says what it died of is not a claim to be
// working -- ui/src/state.js keeps merge_failed out of MERGE_QUEUE_LIVE
// precisely so it cannot read as progress.
func blockedStatus(e queueEntry) queueStatus {
	if e.obs != nil && e.obs.MergeQueueState == mergeStateFailed && e.obs.MergeQueueNote != "" {
		return queueStatus{state: mergeStateFailed, note: e.obs.MergeQueueNote}
	}
	return queueStatus{state: mergeStateFailed, note: gaveUpNote(e.health)}
}

// gaveUpNote says what a blocked entry looks like now, for the one case
// blockedStatus has nothing better: a task blocked before the queue
// started recording its reason. It says the state of the branch rather
// than guessing at the history, and points at the two things that are
// still true of every blocked entry -- the queue's own comment says why,
// and "Retry merge" starts the round again.
func gaveUpNote(health model.PrHealth) string {
	var what string
	switch health {
	case model.PrConflicted:
		what = "its branch conflicts with its base"
	case model.PrFailing:
		what = "its checks are failing"
	case model.PrPending:
		what = "its checks have not reported a verdict"
	case model.PrClean:
		what = "it reads clean now, so the next cycle will merge it"
	default:
		what = "grain cannot read what state it is in"
	}
	return "grain has stopped driving this merge and handed it to a person -- " + what +
		"; the queue's own comment on this task says why, and \"Retry merge\" starts the round again"
}

// repairingNote, repairQueuedNote and repairNeededNote are phase 2 in
// words. Each names what is wrong where that is known, because a row that
// says only "Repairing" leaves a person to go and find out what is being
// repaired -- and the three are separate sentences rather than one with a
// clause in it because what they are waiting on is genuinely different: a
// run, a worker, and this change's turn.
func repairingNote(e queueEntry) string {
	return "an agent is repairing this branch now" + repairCause(e)
}

func repairQueuedNote(e queueEntry) string {
	return "waiting for a worker to pick this task up and repair the branch" + repairCause(e)
}

func repairNeededNote(e queueEntry) string {
	cause := healthReason(e.health, e.detail, e.checks, e.grain)
	// The half of it nobody here can fix, where there is one. A repair is
	// still coming -- this entry owns some of its red -- but a row that
	// said only "needs repairing" would let a person read the repair as
	// the end of the story, when in fact the merge waits on them
	// afterwards whatever the agent does.
	if !e.reds.PathToGreen() && e.reds.BranchOwns() {
		cause += fmt.Sprintf(", of which `%s` is red for a reason no commit on this branch "+
			"reaches and needs a person even after the repair", strings.Join(e.reds.Inherited, "`, `"))
	}
	return cause + ", so grain is sending this task back to an agent to repair it"
}

// unfixableRowNote is the row for an entry with no path to green and
// nothing of its own to fix: what is red, grain's evidence for saying
// nothing pushed here clears it, and -- where the finding is about the
// repository rather than this branch -- that it is true of every entry
// beside it. That last clause is the whole point of concluding this once
// per repository rather than per pull request: the three entries behind
// the one being escalated say the same sentence on the same cycle,
// instead of waiting their turn to rediscover it (grain/task-291).
func unfixableRowNote(e queueEntry) string {
	note := "nothing pushed to this branch turns " + unfixableChecksPhrase(e.reds) + " green"
	if e.unfixable.Why != "" {
		note += " (" + e.unfixable.Why + ")"
	}
	if e.unfixable.ChecksNeverStart {
		return note + ", which is true of every pull request open against " +
			e.ref.Repo.String() + " and clears for all of them at once"
	}
	return note + ", so this waits on a person rather than on a repair"
}

// workingNote is a run the queue did not ask for, said in words. Where
// the queue had already given up, it says so: that is the likeliest way
// this state is ever reached, and a person arriving at the row wants to
// know both that somebody is on it now and that the thing they are on is
// a merge grain had stopped driving.
func workingNote(e queueEntry, blocked bool) string {
	note := "an agent is working on this branch -- a run somebody asked for rather " +
		"than one the merge queue filed, so nothing merges until it ends"
	if blocked {
		note += "; grain had given up driving this merge, and will read what the run leaves"
	}
	return note
}

// repairCause is what the repair is for, where the entry still reads
// broken. It often does not: a repair that has already pushed leaves the
// branch pending on its new commit's checks, and there is nothing useful
// to say about that beyond the fact that an agent owns it.
func repairCause(e queueEntry) string {
	// Which attempt this is, and for a second why it was allowed
	// (repairAttemptNote): "automatic repair 2 of 2, allowed because ...".
	attempt := repairAttemptNote(e.obs, repairCapOf(entryPolicy(e)).max)
	switch e.health {
	case model.PrConflicted, model.PrFailing:
		cause := healthReason(e.health, e.detail, e.checks, e.grain)
		if attempt != "" {
			cause += "; " + attempt
		}
		return " (" + cause + ")"
	}
	if attempt != "" {
		return " (" + attempt + ")"
	}
	return ""
}

// unknownHealthNote separates the two pull requests that read PrUnknown,
// because they are opposite news. GitHub not having finished computing
// mergeability is seconds old and resolves by itself; a credential that
// cannot read check runs is a fact about the whole deployment that never
// resolves on its own, and is what pkg/ui's own degraded-auto-merge
// banner is about (forge.ChecksUnavailable).
func unknownHealthNote(e queueEntry) string {
	if !e.checksKnown {
		return "grain's GitHub credential cannot read this repository's checks, " +
			"so nothing here can tell a green pull request from an untested one"
	}
	return "waiting for GitHub to work out whether this branch merges cleanly"
}

// refusalNote is what a refused merge says on the row. GitHub's own
// message is the whole of the information -- "Base branch was modified",
// "At least 1 approving review is required" -- so it is quoted rather
// than summarised, on one line and bounded, since this is rendered beside
// a task title.
func refusalNote(r forge.MergeRefusal) string {
	attempts := "once"
	if r.Count > 1 {
		attempts = fmt.Sprintf("%d times", r.Count)
	}
	return fmt.Sprintf("GitHub has refused this merge %s: %s", attempts, refusalLine(r.Reason, 160))
}

// reviewNote names the review a change is waiting on, or says that one is
// owed and has not been filed yet -- which is a real state a change can
// sit in (reviewState.pending's own doc comment) and one nothing else on
// the row would explain.
func reviewNote(review reviewState) string {
	if review.taskID == "" {
		return "waiting for its review to be filed"
	}
	return "waiting for review task " + review.taskID + " to finish"
}

// queuePositionNote says who is ahead. A head that is not merging this
// cycle has spent its lane's merges rather than lost its place, and those
// are different waits to be in: the first ends when somebody else's change
// lands, the second on the very next cycle.
//
// Both are about e's own lane -- its repository and base branch
// (mergelane.go) -- so a `candidate` change names the `candidate` change in
// front of it, never a `main` one it was never waiting for.
//
// heads is mergeWalk's: keyed by lane, and by the bare repository for a
// head nobody could place in a lane (mergeWalk.claimHeadOf).
func queuePositionNote(e queueEntry, heads map[string]string) string {
	name := laneName(e)
	if head := headIn(heads, laneOf(e)); head != "" && head != e.task.ID {
		return fmt.Sprintf("behind task %s in the %s queue", head, name)
	}
	return fmt.Sprintf("waiting for the next cycle: the %s queue has had all its merges for this one", name)
}

// oneLine renders an error or a GitHub message for a row: first line
// only, collapsed, and cut to n runes with an ellipsis. A refusal's full
// text still reaches a person in the comment escalateRefusedMerge leaves.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "..."
	}
	return s
}

// lastMergeRefusal, recordMergeRefusal and forgetMergeRefusals are
// pkg/forge's merge-refusal record in this package's spelling, the same
// adapters the two check clocks have at the top of sync.go.
func lastMergeRefusal(ref model.PullRequestRef, headSHA string) (forge.MergeRefusal, bool) {
	return forge.LastMergeRefusal(forgeRef(ref), headSHA)
}

func recordMergeRefusal(ref model.PullRequestRef, headSHA, reason string, now time.Time) forge.MergeRefusal {
	return forge.RecordMergeRefusal(forgeRef(ref), headSHA, reason, now)
}

func forgetMergeRefusals(ref model.PullRequestRef) { forge.ForgetMergeRefusals(forgeRef(ref)) }

// refusedMerge is what syncEntry does with a merge GitHub would not
// perform, and it is this change's whole answer to a task that sits in
// "merging" without moving.
//
// Three things happen, in the order they matter. The refusal is recorded
// (forge.RecordMergeRefusal), which is what gives the wait a clock and a
// count. The row is made to say so, with GitHub's own words, so that the
// next cycle's "Merging" is not the only thing anyone can see. And once
// the run of refusals is older than the deadline, the queue stops driving
// this pull request and asks a person, exactly as it does for a repair
// that did not take or checks that never reported -- which also takes the
// entry out of its repository's queue (isQueueMember), so one pull
// request GitHub will not merge stops holding up the ones behind it.
//
// The merge itself is still retried every cycle afterwards, blocked or
// not, because a refusal is so often something a person fixes on GitHub
// rather than in the branch: an approving review, a protection rule, a
// permission. That is the same promise blockMergeQueue makes everywhere
// else -- what is given up is the queue position and the automatic fix,
// never the merge.
//
// cause is returned wrapped, so the cycle logs what it always logged and
// the walk closes this repository's merge slot behind a member that did
// not merge.
func refusedMerge(ctx context.Context, store *model.Store, client github.Client, e queueEntry,
	ref model.PullRequestRef, detail github.PullRequestDetail, cause error, now time.Time) error {

	task, obs, headSHA := e.task, e.obs, detail.HeadSHA
	merge := fmt.Errorf("orchestrator: auto-merging %s (head %q): %w", ref, headSHA, cause)
	before, waiting := lastMergeRefusal(ref, headSHA)
	waiting = waiting && before.Kind == github.RefusalApproval
	recordMergeRefusal(ref, headSHA, github.RefusalMessage(cause), now)
	// What it was refused for: GitHub's words first, then its rules for a
	// refusal the words could not place (githubrefusal.go).
	req, read := readMergeRequirements(client, ref, detail.BaseRef)
	checks := e.checks
	if read && len(req.RequiredChecks) > 0 {
		checks = githubCheckRuns(client, ref, headSHA, checks)
	}
	kind := refineRefusal(github.ClassifyMergeRefusal(cause), cause, req, read, checks)
	refusal, _ := forge.ClassifyLastMergeRefusal(forgeRef(ref), headSHA, kind, req)
	// What GitHub's rules say has just been read: the wait's first poll.
	forge.NoteMergeRefusalPolled(forgeRef(ref), now)
	status := refusalStatus(refusal, ref, detail.HTMLURL)
	if err := setMergeQueueState(ctx, store, obs, task.ID, status, now); err != nil {
		return errors.Join(err, merge)
	}
	if kind == github.RefusalApproval {
		// A wait, not a failure: said once on the thread, and not
		// reported as the cycle's error again each time the queue looks.
		if !waiting {
			if err := queueComment(ctx, store, task.ID, approvalComment(ref, refusal, detail.HTMLURL), now); err != nil {
				return errors.Join(err, merge)
			}
		}
		return nil
	}
	if refusal.Exhausted(now) {
		forge.MarkMergeRefusalEscalated(forgeRef(ref))
		// Once per pull request, like every other way the queue gives
		// up: a task it has already handed to somebody is not handed to
		// them again.
		if obs == nil || obs.MergeQueueBlockedAt == nil {
			if err := blockMergeQueue(ctx, store, task.ID, refusalComment(ref, refusal, now), status.note, now); err != nil {
				return errors.Join(err, merge)
			}
		}
	}
	return merge
}

// lastReadFailure is pkg/forge's read-failure record in this package's
// spelling, the same adapter the merge-refusal record and the two check
// clocks have. There is no "forget" adapter to go with it: the record is
// ended by a read that answered, which is forge.Observe's own business
// and happens at the point of the read.
func lastReadFailure(ref model.PullRequestRef) (forge.ReadFailure, bool) {
	return forge.LastReadFailure(forgeRef(ref))
}

// noteUnreadableEntry is what a cycle that could not read a pull request
// leaves on the task's row -- which, for the first couple of minutes of
// failed reads, is nothing at all.
//
// Until this existed such a cycle returned early, before the row was
// written at all, so whatever the queue last said stood indefinitely --
// "Merging", most often, since that is the last thing written before the
// call that reads GitHub again. A credential that has lost its access, a
// repository that has been renamed, a pull request somebody deleted: each
// of them leaves every watched task frozen mid-sentence, which is the
// shape of the complaint this whole change came from.
//
// # Why a failed read is not, by itself, an unreadable pull request
//
// Because GitHub is flaky, at a rate that makes one failed read mean
// nothing. Every occurrence of this state in this deployment's daemon log
// over 16-17 September 2026 was a GitHub 5xx -- roughly one every few
// hours, across eight pull requests, on both of the reads Observe makes
// -- and every one of them was answered fine by the next cycle ten
// seconds later, with every change merging. The only lasting effect was
// the sentence below on somebody's row, which an operator reported as a
// fault in a deployment that was working.
//
// So the run of failures is timed (forge.ReadFailure, recorded by
// forge.Observe at the point of the read) and nothing is written until it
// outlasts the deadline, in the style of the check-stall and
// merge-refusal clocks rather than as a fourth mechanism. Saying nothing
// is a return to what this function replaced -- the row standing at
// whatever the queue last said -- but bounded now, to a couple of minutes
// rather than to forever, and to the one case where standing still is
// honest: grain really is about to read this again, and expects it to
// work.
//
// # And why a 5xx and a 404 do not get the same sentence
//
// They call for different things from whoever reads the row. "GitHub is
// having trouble and grain is retrying" is not something to act on at
// all; "the credential cannot see this repository" is somebody's
// afternoon. forge.TransientRead is the split, and unreadableNote is the
// two sentences.
func noteUnreadableEntry(ctx context.Context, store *model.Store, e queueEntry,
	cause error, now time.Time) error {

	failure, ok := lastReadFailure(e.ref)
	if !ok {
		// The read failed somewhere that did not keep a record, which
		// nothing does today (forge.Observe records both of its reads).
		// One unrecorded failure is one failed read, which is exactly the
		// thing this function no longer reports.
		return nil
	}
	if !failure.Persistent(now) {
		return nil
	}
	return setMergeQueueState(ctx, store, e.obs, e.task.ID, queueStatus{
		state: mergeStateUnreadable,
		note:  unreadableNote(e.ref, failure, now),
	}, now)
}

// unreadableNote says which kind of unreadable this is, in the words the
// person reading the row needs.
//
// Both sentences carry how long it has gone on and how many reads that
// is, for the reason refusalNote does: one is a race, forty is a rule,
// and a row that says only "could not read" cannot be told apart from the
// same row five seconds or five hours in. The cause is quoted in both,
// because whichever it is, it is the thing an operator will search for.
func unreadableNote(ref model.PullRequestRef, f forge.ReadFailure, now time.Time) string {
	said := oneLine(f.Reason, 140)
	tried := fmt.Sprintf("%d failed reads over %s", f.Count, humanDuration(now.Sub(f.First)))
	if f.Transient {
		return fmt.Sprintf("GitHub is not answering for this pull request (%s; last said: %s). "+
			"grain is still asking, every cycle, and merges as soon as it gets an answer.",
			tried, said)
	}
	return fmt.Sprintf("grain cannot read this pull request from GitHub at all (%s; %s). "+
		"Check that %s is still there, and that this deployment's credential still has access to %s.",
		tried, said, ref, ref.Repo)
}

// clearStaleQueueState takes the merge queue's account of a task off
// every row whose pull request this cycle is no longer reading.
//
// It is the terminal write these states were missing. Every one of them
// is written by SyncPullRequests for a task in Store.OpenPullRequestLinks
// and overwritten by the next cycle that covers that task -- so a task
// that leaves the watched set keeps whatever the queue last said about
// it, for good, because the cycle that would correct it never comes. The
// row then claims work nobody is doing: the case that shows is a task
// somebody puts an agent back on themselves (ui.Client.RunNow), whose
// cleared completion takes it out of the watched set while its row goes
// on saying "Merging" or "Waiting for CI" over a live run.
//
// Which states go is not a judgement this makes for itself: it is
// liveness.go's classification, read back (outlivesTheQueuesAttention).
// A state grain drives, or one waiting on an answer from GitHub, is a
// claim that something is going to happen to this change -- and nothing
// is, once no cycle covers the task. A state that is terminal by design
// is the opposite, and is kept.
//
// The watched set is the cycle's own links (Store.OpenPullRequestLinks),
// not the entries built from them: a link whose ref would not parse is
// still a pull request grain is watching, it is simply one nothing can be
// said about, and clearing its row here would be this function inventing
// the silence it exists to end.
//
// Nothing is written for a task whose state this keeps or whose row is
// still watched, which is all of them almost all of the time, so the cost
// of this is one read of a small table per cycle.
func clearStaleQueueState(ctx context.Context, store *model.Store,
	watched map[string]bool, now time.Time) error {

	states, err := store.MergeQueueStates(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading merge queue states: %w", err)
	}
	var errs []error
	for id, activity := range states {
		if watched[id] || outlivesTheQueuesAttention(activity.State) {
			continue
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: reading observation for %s: %w", id, err))
			continue
		}
		if err := setMergeQueueState(ctx, store, obs, id, queueStatus{}, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// outlivesTheQueuesAttention reports whether a state is still true of a
// task the queue has stopped watching -- the one judgement
// clearStaleQueueState makes, and deliberately not a second table of its
// own: it is the liveness classification asked a different question.
//
// A livenessDriven state names the component that is going to leave it,
// and a livenessAwaitingExternal state is a read grain is still making;
// both are promises about work in flight, and both are false about a
// task no cycle covers -- the driver named in the table is not running
// for it and the read is not being made. A livenessAwaitingPerson state
// promises nothing: it is a verdict already reached, standing still on
// purpose until somebody presses the control it names
// (ui.Client.RetryMerge). Nothing about leaving the watched set makes
// that less true, and taking the words away would leave the "Merge
// blocked" chip (Observation.MergeQueueBlockedAt, which the sweep never
// touches) standing over a row with nothing to say why.
//
// Deriving it rather than listing it is what stops the two drifting: a
// state added to the vocabulary must be classified (liveness.go's
// TestEveryStateIsClassified), and classifying it answers this as
// well. A state with no classification at all -- a word from a daemon
// this build does not know -- is swept, which is the safe direction: a
// row that has forgotten something true is a far smaller fault than one
// asserting something false.
func outlivesTheQueuesAttention(state string) bool {
	liveness, ok := mergeLane.of(state)
	return ok && liveness.class == livenessAwaitingPerson
}

// waitingPhrase is what a pending entry is waiting on. A required GitHub
// check that is not on the commit at all is the whole of the wait where
// nothing else is outstanding, and is said as that rather than after
// "no check reported against it at all" -- which, with `build` green
// beside it, was not true (docs/test-runs/2026-10-05-merge-rules.md).
func waitingPhrase(e queueEntry) string {
	phrase, missing := stalledChecksPhrase(e.checks, e.grain), missingChecksNote(e)
	if missing != "" && phrase == noChecksReportedPhrase {
		return strings.TrimPrefix(missing, "; ")
	}
	return phrase + missing
}
