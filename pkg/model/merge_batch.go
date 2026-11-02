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

package model

import "time"

// MergeBatch is one speculative merge the queue is testing: a throwaway
// ref holding a base branch with several queued changes merged into it,
// and grain CI's verdict about that one tree (grain/task-294).
//
// One row per (repo, branch, task). It was one per (repo, branch) while a
// batch of several changes was what the row described: a second batch
// against the same base would have been testing a tree the first was
// about to move. Batching is retired (grain/task-1023), and a row is now
// one change's overlap re-check, which is re-validated against the live
// tip when it concludes (orchestrator.advanceMergeRecheck), so re-checks
// of different changes no longer wait on each other (grain/task-2390).
//
// # What it buys
//
// Without it a repository's queue merges one change per cycle, and each
// merge starts a full suite against the branch it landed on
// (orchestrator.recordPostMergeCI, BaseBranchCI). Three queued changes
// therefore cost three suites, and the semantic conflict between two of
// them -- each green alone, broken together -- is found only *after* both
// have landed, by the very control that then stops the queue and spends
// an agent on repairing `main` (grain/task-198's compensating control).
//
// A batch turns that round. One suite judges base + all the members
// before any of them land; a pass merges the lot; a failure names a
// culprit without anything having reached `main` at all. The post-merge
// run still happens and still holds the queue when it goes red -- nothing
// here replaces it -- but the failure it is there to catch is now mostly
// caught an hour earlier and on a tree nobody has to bisect by hand.
//
// # Members are not judged by this run
//
// Every member arrives already green on its own head: that is what a
// queue entry is (orchestrator.grainCIVerdictOf, and the full-suite pass
// a task has to get before it finishes). The batch run is an *additional*
// verdict about the combination, recorded here and nowhere else.
//
// It is deliberately never written onto a member's own CI status. A
// verdict about base+A+B+C is not a verdict about A, and a member that
// falls out of a batch has to carry nothing away from it -- otherwise a
// change dropped from a failing batch would inherit a red about a tree it
// was never alone in, or a change dropped from a passing one would carry
// a green for a commit that was never built.
//
// # Not a settings table
//
// Off staterepo.SettingsTables, beside base_branch_ci and for its reason:
// grain writes this for itself, cycle by cycle, and a settings import
// landing mid-batch must not be able to resurrect a batch whose ref has
// moved or drop one whose members are being held for it.
type MergeBatch struct {
	Repo   RepoRef
	Branch string
	// TaskID is the change an overlap re-check is about, and the third
	// part of the row's key (grain/task-2390): batching is retired, every
	// row the queue writes now is one change's re-check, and two
	// overlapping changes on one branch are re-checked side by side, each
	// on a ref of its own (MergeRecheckRef). "" is a row written before
	// the key had a task in it.
	TaskID string
	// Ref is the throwaway branch the batch was built on --
	// MergeBatchRef, one per base branch, force-reset to the base each
	// time a batch forms. Nobody's pull request points at it and nothing
	// is ever merged *from* it: what lands is each member's own pull
	// request, exactly as it would have landed on its own.
	//
	// This is the part GitHub's own merge queue gets right and grain did
	// not: a speculative merge belongs on a ref grain owns, not on the
	// contributor's branch. Merging a base branch into somebody's own
	// branch is what turns a conflict into a dispatched repair task and
	// throws away the verdict that branch already held
	// (orchestrator.bringHeadUpToDate).
	Ref string
	// BaseCommit is the tip of Branch the batch was built from, and
	// Commit the tip of Ref once every member had been merged into it --
	// the tree grain CI is judging. A batch whose base has moved since is
	// abandoned rather than believed: the answer is about a tree that is
	// no longer the one a merge would produce.
	BaseCommit string
	Commit     string
	// Members are the queued changes in the batch, in the queue's own
	// order (orchestrator.queueOrder). A member's HeadRef is the branch
	// merged into the throwaway ref and its HeadSHA that branch's tip as
	// it stood at that moment -- which is what its pull request is
	// merged pinned to, so a push that lands while the batch is running
	// takes that member out rather than merging a commit the batch never
	// contained.
	Members []MergeBatchMember
	// RunID is grain CI's run against Commit, StartedAt when it was
	// asked for, FinishedAt when a conclusion was first recorded here.
	RunID      string
	StartedAt  time.Time
	FinishedAt *time.Time
	// Conclusion is pkg/ci's own word for how the run ended, empty while
	// it is still going, and Detail is that verdict in words. Stored as
	// the word for BaseBranchCI.Conclusion's reason: only "failure" is a
	// failure, and the rest are each their own kind of no-answer.
	Conclusion string
	Detail     string
	// Splits is how many times this lineage has been halved to find the
	// change that broke it -- 0 for a batch formed fresh from the queue.
	// It bounds nothing (halving from the front reaches one member on its
	// own); it is what the comment on the culprit is able to say about
	// how many suites finding it cost.
	Splits int
	// Queued is how many changes were in this repository's merge queue
	// when the batch formed. It is carried on the row for one reason: the
	// history written when the batch *ends* (MergeBatchRecord.Queued, and
	// its note on why the count is an upper bound) is what tells a batch
	// of three that was all there was from one the size limit trimmed,
	// and by then the queue it was chosen from has moved on.
	Queued int
	// RecheckOf is set once this batch's run failed and grain asked grain
	// CI the same question again, uncached, before believing it
	// (orchestrator.recheckMergeBatch): the first run's id, with
	// RecheckDetail its verdict and RecheckAt when the recheck was asked
	// for. RunID is then the recheck's run. A batch is rechecked once:
	// only a failure that reproduces is bisected or blamed on a member.
	RecheckOf     string
	RecheckDetail string
	RecheckAt     *time.Time
}

// MergeBatchMember is one queued change in a batch: the task, its pull
// request, and the exact commit the batch contains and the merge will be
// pinned to.
type MergeBatchMember struct {
	TaskID  string `json:"task"`
	Number  int    `json:"number"`
	HeadRef string `json:"headRef"`
	HeadSHA string `json:"head"`
	// Merged is set once this member's own pull request has landed on the
	// batch's word, and Tip is what the base branch's tip became when it
	// did. Together they are the batch's merged prefix: Frontier is what
	// is left, and Tip is what makes the frontier checkable rather than
	// merely believed (see Frontier's own comment).
	//
	// Written on the members rather than as columns of their own because
	// the column is JSON: a deployment upgrading across this reads its
	// batches back with both fields zero, which says "nothing merged
	// yet" and is exactly right for a batch that was in flight when the
	// daemon was replaced.
	Merged bool   `json:"merged,omitempty"`
	Tip    string `json:"tip,omitempty"`
}

// Running reports whether grain CI has yet to report on this batch.
//
// It is the first half of InFlight, not the whole of it: a batch whose
// verdict has arrived and whose members have not all landed yet is still
// holding the branch. See InFlight.
func (b *MergeBatch) Running() bool { return b != nil && b.Conclusion == "" }

// InFlight reports whether this batch holds its base branch: from the
// moment its tree was fixed through to its last member merged.
//
// This is the merge queue's lock (orchestrator's merge predicate,
// grain/task-471), and the reason it spans the merging as well as the
// testing is arithmetic rather than taste. A batch tests base+A+B+C. If
// the lock covered only the seconds spent merging, a change X that is
// not a member could land while the batch's forty-minute suite ran, and
// what the members would then merge onto is base+A+X+C -- a tree nobody
// tested and nobody chose. Holding from "the tree is fixed" onwards is
// what makes "nothing else may land in between" a consequence of the
// lock rather than a condition somebody has to remember to check.
//
// Three states satisfy it: the run is still going (Running); the run
// passed and there are members left to merge (Merging); and that is all
// -- a batch that failed, errored or was cancelled holds nothing, and
// its members go back to the ordinary queue with the greens they
// arrived with.
func (b *MergeBatch) InFlight() bool { return b.Running() || b.Merging() }

// Merging reports whether this batch passed and still has members to
// land: the window the frontier arm of the merge predicate is about.
//
// A row in this state read at the *start* of a cycle is a merge that was
// interrupted -- grain merges a batch's members within the cycle its
// verdict arrives -- which is the one thing the lock must never be able
// to get stuck on (orchestrator.advanceMergeBatch abandons it on sight).
func (b *MergeBatch) Merging() bool {
	return b != nil && b.Conclusion == MergeBatchSuccess && len(b.Frontier()) > 0
}

// Frontier is the maximal unmerged suffix of this batch's own member
// order: the members that have still to land for the tree grain CI
// judged to exist.
//
// Ordered, and only the head of it may merge. base+A+C is as untested as
// base+A -- the batch's verdict is about base+A+B+C and about nothing
// else -- so the frontier is not "any member that has not landed yet",
// it is the next one in the batch's order. Merging along it is the only
// sequence of merges that ends at the tree that was tested, which is
// what makes "merge each branch in order" and "merge the batch" the same
// statement rather than one of them being a series of steps nothing
// authorised.
func (b *MergeBatch) Frontier() []MergeBatchMember {
	if b == nil {
		return nil
	}
	for i, m := range b.Members {
		if !m.Merged {
			return b.Members[i:]
		}
	}
	return nil
}

// FrontierHead is the one member of this batch that may merge, and
// whether there is one.
func (b *MergeBatch) FrontierHead() (MergeBatchMember, bool) {
	frontier := b.Frontier()
	if len(frontier) == 0 {
		return MergeBatchMember{}, false
	}
	return frontier[0], true
}

// ExpectedTip is where the base branch must be for this batch's frontier
// to mean anything: the batch's own base commit while nothing has
// merged, and the tip the last merged member produced after that.
//
// The frontier is valid only while the branch is exactly "batch base +
// the merged prefix". Anywhere else and the batch is void: somebody
// pushed, something else merged, and the tree the remaining members
// would complete is not the tree that was judged. Voided says so.
func (b *MergeBatch) ExpectedTip() string {
	if b == nil {
		return ""
	}
	tip := b.BaseCommit
	for _, m := range b.Members {
		if !m.Merged {
			break
		}
		if m.Tip != "" {
			tip = m.Tip
		}
	}
	return tip
}

// Voided reports whether the branch's tip has left the path this batch
// was judged along, given the tip as it actually stands. An empty tip --
// a branch grain could not read this cycle -- voids nothing: not knowing
// where the branch is is not the same as knowing it has moved.
func (b *MergeBatch) Voided(tip string) bool {
	if b == nil || tip == "" {
		return false
	}
	want := b.ExpectedTip()
	return want != "" && tip != want
}

// MergedCount is how many of this batch's members have landed on its
// word, for the history and for the sentence a partly-merged batch
// leaves behind.
func (b *MergeBatch) MergedCount() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, m := range b.Members {
		if m.Merged {
			n++
		}
	}
	return n
}

// Failed reports the one conclusion that means the combination is broken
// and a culprit has to be found. Everything else -- a pass, a run that
// errored, one that was cancelled -- is not a statement about the tree
// (BaseBranchCI.Conclusion's own note), and a batch ends on any of them.
func (b *MergeBatch) Failed() bool { return b != nil && b.Conclusion == MergeBatchFailure }

// TaskIDs is the members' task ids, for the caller holding them out of
// the ordinary merge path.
func (b *MergeBatch) TaskIDs() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.Members))
	for _, m := range b.Members {
		out = append(out, m.TaskID)
	}
	return out
}

// MergeBatchFailure and MergeBatchSuccess are the two ci.Conclusions this
// package names, for BaseBranchCIFailure's reason: pkg/model sits
// underneath pkg/ci rather than beside it.
const (
	MergeBatchFailure = "failure"
	MergeBatchSuccess = "success"
)

// MergeBatchRef is the throwaway ref a batch for one base branch is built
// on. One name per base branch, reused and force-reset rather than a
// fresh ref per batch, so a repository accumulates one of these per
// branch things merge into rather than one per batch grain ever ran.
//
// The prefix is grain's own, deliberately not GitHub's
// `gh-readonly-queue/`: a repository may be in GitHub's merge queue as
// well, and two queues writing the same namespace would be two things
// fighting over one ref. The same goes for two grains pointed at one
// repository, so the deployment id (Store.DeploymentID, the {deployment}
// task branches carry) sits between the prefix and the branch: without
// it each deployment's queue would force-reset the other's batch out from
// under its CI run. It is one path segment (ValidateDeploymentID), so
// grain-merge-queue/<deployment>/<branch> cannot be mistaken for another
// deployment's ref. Empty keeps the historical grain-merge-queue/<branch>.
//
// A batch records the ref it was built on (MergeBatch.Ref), and that
// record is what is cleaned up, so a batch begun under the old name is
// still destroyed under it.
func MergeBatchRef(deploymentID, branch string) string {
	if deploymentID == "" {
		return "grain-merge-queue/" + branch
	}
	return "grain-merge-queue/" + deploymentID + "/" + branch
}

// MergeRecheckRef is the throwaway ref one change's overlap re-check is
// built on: branch's tip with taskID's pull request merged into it. One
// per change rather than one per branch (MergeBatchRef), so overlapping
// changes on one branch are re-checked side by side (grain/task-2390).
//
// It keeps MergeBatchRef's prefix, which is how a person or an agent finds
// the queue's own runs (`grain ci list -ref grain-merge-queue/`), with a
// `recheck` segment after the deployment. Git cannot hold a ref and a ref
// beneath it at once, so the per-change refs sit under a segment of their
// own rather than under MergeBatchRef's grain-merge-queue/<deployment>/main,
// which a deployment upgrading across this may still hold. Only a
// base branch literally named `recheck` could collide, and only with a
// branch-wide ref left over from before. The task id comes before the
// branch because a branch name may contain slashes and a task id does not.
func MergeRecheckRef(deploymentID, taskID, branch string) string {
	return MergeBatchRef(deploymentID, "recheck/"+taskID+"/"+branch)
}

// DefaultMergeBatchSize is how many queued changes grain will speculate
// on at once by default.
//
// Three, and deliberately small. A batch of N is a bet that all N are
// good: it costs an extra suite -- plus the latency of the changes that
// were fine -- when it does not pay off, and saves at most N-2 when it
// does. At most, because the post-merge run on the base branch happens
// either way and a batch pays it once where the one-at-a-time queue pays
// it per change (MergeBatchRecord.Suites) -- but only one post-merge run
// goes at a time per branch, so merges landing faster than a suite
// finishes already share one (BaseBranchCI.Covers). Write c for how many
// merges a run really covers and the saving is (N-1)/c - 1: N-2 on a
// quiet branch where c is 1, and nothing at all on a busy one. Measured
// on this deployment c is about 2.7, which puts a batch of three a
// quarter of a suite in the red.
//
// So three is not the smallest batch that saves a suite here -- nothing
// under four is -- and it is not chosen on capacity at all. It is chosen
// because the bet is small and the worst case stays cheap: a failing
// batch of three is isolated in at most two further runs by halving from
// the front, so the bad case is four suites against the three the queue
// would have spent anyway. What batching buys on a deployment whose
// post-merge runs coalesce is the combination tested before it lands
// rather than after, and N changes landing on one queue cycle rather
// than on N -- which is what it was written for.
//
// It is a setting rather than a constant because the right number depends
// on how often this deployment's changes actually break each other, which
// is a thing to measure rather than to guess at (grain/task-294's own
// note: start at three, watch it, raise it if batches keep passing).
// What does the watching is MergeBatchRecord, and what it has said so far
// is in docs/merge-queue-batching.md: on this deployment batches hardly
// ever form, because two changes are rarely ready at the same tick, so
// the size has not yet been what limits anything.
// One turns batching off and leaves the ordinary one-at-a-time queue,
// which is what a deployment whose changes conflict constantly should
// choose.
const DefaultMergeBatchSize = 3

// MaxMergeBatchSize bounds what an operator can ask for. A batch holds
// every member out of the queue until it reports, so an over-large one
// trades the whole queue's latency for a saving that gets worse the
// bigger the bet is.
const MaxMergeBatchSize = 20

// DefaultMergeGatherWindow is how long the merge queue will hold a lone
// ready change at the front of a branch's queue, hoping a second becomes
// ready and the two can be tested together (Config.MergeGatherWindow,
// orchestrator's gather window).
//
// It exists because the size above turned out not to be what limits
// batching. Measured on this deployment over eleven hours (grain/task-300,
// docs/merge-queue-batching.md): one batch formed and seventeen changes
// merged one at a time, not because three was too small a limit but
// because two changes are almost never ready on the same reconcile tick.
// The queue drains faster than it fills, so a batch only forms when two
// changes happen to arrive together.
//
// Five minutes, and it is a cap rather than a wait: the queue only holds
// a change at all while something else on the same branch has a grain CI
// run actually in flight, and it lets go the moment that run reports --
// green, and the two are batched; anything else, and the held change
// merges on the very next cycle. So the five minutes is what a solitary
// change pays when the run it was waiting for turns out to be slower than
// that, not what every merge pays. It is well under a full suite
// (20-45 minutes) on purpose: waiting out a whole suite to save one is no
// saving at all, and most of what is worth catching is a run that is
// nearly finished when the change in hand becomes ready.
//
// A negative window turns the wait off and leaves the queue merging a
// lone ready change the moment it is ready, which is the right answer for
// a deployment with CI capacity to spare: there, a suite saved is worth
// less than a merge landed sooner.
const DefaultMergeGatherWindow = 5 * time.Minute

// MaxMergeGatherWindow bounds what an operator can ask for. The wait is
// latency added to a change that is ready now, in exchange for a saving
// that is at most one suite, so half an hour is already far past the
// point where the trade makes sense.
const MaxMergeGatherWindow = 30 * time.Minute

// MaxMergeBatchAge bounds how long a batch may hold its members. A full
// suite on this deployment is 20-45 minutes; ninety is comfortably past
// any of them, and what it protects against is not a slow suite but a run
// that will never report at all -- a daemon restarted mid-batch, a CI
// service that has lost the run. Past it the batch is abandoned and the
// ordinary queue takes over, which is the same direction every other
// deadline in the merge queue errs in.
//
// It lives here rather than beside the reconciler that enforces it
// (orchestrator.advanceMergeBatch) because it is also the denominator a
// person reading a live batch needs: "22m" says nothing on its own, and
// "22m of 90m" says how much rope is left before the queue gives up and
// falls back to merging one at a time. pkg/ui does not import
// pkg/orchestrator -- a presentation-layer package importing core
// dispatch logic runs the wrong way -- and pkg/model is what both of them
// already read the batch itself out of.
const MaxMergeBatchAge = 90 * time.Minute
