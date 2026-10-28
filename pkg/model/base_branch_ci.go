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

// BaseBranchCI is what grain's own CI made of a branch changes merge
// *into* -- `main`, or a release branch -- as opposed to the
// branches that merge into it. One row per (repo, branch), replaced each
// time a run is started there (orchestrator.startBaseBranchCI).
//
// # One run at a time (grain/task-334)
//
// The row is also the lock. A base-branch run is a release run -- it
// builds and publishes this deployment's own images, 22 minutes to an
// hour and a half of it -- and merges land far faster than that, so a run
// per merge stacked several full image builds on top of each other and
// starved the rest of CI. What starts one now is the reconciler noticing
// that Commit is not the branch's tip, and it will not start one while
// this row names a run that has no Conclusion yet. A merge that lands in
// the meantime moves the tip and leaves PendingTaskID behind; when the
// run in flight finishes, the next cycle starts one against the tip as it
// stands at that moment. Intermediate commits are skipped rather than
// queued: the newer tip's run judges a tree that contains them, so no
// verdict anybody was entitled to is lost, and the images nobody would
// have pulled are never built.
//
// # Why this is written down at all
//
// It is the compensating control for not requiring a branch to be up to
// date before it merges (RepoConfig.RequireBranchUpToDate,
// grain/task-198). Two pull requests can each be green on their own head
// and broken together -- the semantic conflict an up-to-date rule buys
// protection from -- and a queue that merges each of them on its own
// verdict will land both. grain has always run CI on the base branch
// after a merge, so it always found out; what it did about it was
// nothing, and "nothing" is how four more merges end up stacked on a
// broken base with nobody driving.
//
// So the run is recorded where the merge queue can read it: while a
// branch's last post-merge run is a failure, nothing further merges into
// that branch (orchestrator's mergeStateBaseBroken), and one repair task
// is filed naming the merge that broke it -- the same thing a red branch
// has always earned.
//
// # Red, and then red again
//
// Not every red run is a broken branch: a deployment with a flaky job has
// some reds that reproduce and some that do not, and this gate is
// expensive enough to be worth telling them apart (grain/task-202). So a
// failure is held at once and *confirmed* before it costs an agent --
// Confirmation, and the fields around it, are that second opinion.
//
// # Not a settings table
//
// grain writes this for itself, cycle by cycle, so it is off
// staterepo.SettingsTables for pipeline_pause's reason: a settings import
// landing mid-cycle must not be able to overwrite grain's own note about
// what is broken right now.
type BaseBranchCI struct {
	Repo   RepoRef
	Branch string
	// RunID and Commit are the run and the commit it is judging -- the
	// tip of Branch as it stood the moment the merge landed. The commit
	// matters as much as the verdict: a failure recorded against a commit
	// that is no longer the branch's tip is a failure somebody may already
	// have pushed a fix over, which is what lets the reconciler re-ask
	// rather than hold the queue on an answer about a tree that has gone.
	RunID  string
	Commit string
	// StartedAt is when the run was started, and FinishedAt when a
	// conclusion was first recorded here -- nil while the run is still
	// going.
	StartedAt  time.Time
	FinishedAt *time.Time
	// Attempts is how many runs have been started for Commit, counting
	// the one RunID names. It bounds re-asking in the one case where the
	// recorded run said nothing about the tree at all -- errored, or lost
	// with the daemon that was running it -- so a deployment whose CI
	// cannot get a job onto a machine does not answer that with a fresh
	// run every cycle forever (orchestrator.baseCIRetryDue).
	//
	// Zero is a row written before this was counted, which is one run.
	Attempts int
	// Conclusion is pkg/ci's own word for how the run ended ("success",
	// "failure", "errored", "cancelled"), empty while it is still
	// running. Stored as the word rather than as a boolean for
	// CIStatus.Conclusion's reason: only "failure" is a failure, and the
	// other three are each their own kind of no-answer.
	Conclusion string
	// Detail is the verdict in words -- which jobs failed -- for the
	// comment and the repair task's own body.
	Detail string
	// MergedTaskID is the task whose merge this run is the answer to: who
	// to name when this branch goes red, and whose grants and read/write
	// targets the repair inherits. Empty for a run about a commit grain
	// did not merge -- a hand-merge, a push straight to the branch -- since
	// naming a task for those would blame somebody for a tree they never
	// touched.
	MergedTaskID string
	// PendingTaskID is the task whose merge last moved this branch past
	// the commit RunID is judging, and the MergedTaskID the *next* run
	// started here inherits (grain/task-334).
	//
	// It exists because a merge no longer starts a run of its own. One
	// release run is in flight per branch at a time, so a merge that lands
	// while one is going is recorded and nothing else: this is where the
	// record goes, and it is what keeps a failure of the run that does
	// eventually judge that merge able to name it. Several merges
	// coalescing into one run leave the last of them here -- the run
	// judges a tree containing all of them, and the repair's body says so
	// rather than pretending one commit is the whole story.
	PendingTaskID string
	// Covers is how many merges the run RunID names is the answer to, and
	// PendingMerges how many have landed since it started and are waiting
	// for the next one. Both count merges grain made itself
	// (orchestrator.noteBaseBranchMerge); a tip somebody pushed straight
	// to the branch moves neither, so a run covering nought is one no
	// merge of grain's produced.
	//
	// They are here because the number is what the merge queue's
	// arithmetic turns on and nothing was counting it. A batch is
	// credited with saving the post-merge suites its members would each
	// have spent -- but "one run at a time" above means merges that
	// arrive faster than a suite finishes already share one run, so the
	// suites a batch saves are whatever share of them would really have
	// been spent, which is this. Measured on this deployment it is about
	// 2.7 merges to the run, which is enough to take the saving at the
	// default batch size below zero: see docs/merge-queue-batching.md,
	// "How much the post-merge run already coalesces", and
	// MergeBatchRecord.SerialSuites, which is an upper bound for exactly
	// this reason.
	//
	// Zero on a row written before they were counted, which reads as
	// "nobody knows" rather than as a merge that covered nothing: the
	// distinction only matters to a reader of the log line
	// startBaseBranchCI writes, and one line per run is the whole of what
	// this is for. Summing it over a week wants a history table of its
	// own, which these two are deliberately not.
	Covers        int
	PendingMerges int
	// Confirmation is how far the second opinion on a failure has got:
	// "" before one is asked for, BaseCIConfirming while it is being
	// asked, BaseCIConfirmed once a second run agreed, BaseCICleared once
	// one disagreed. Empty on any row whose Conclusion is not a failure,
	// because there is nothing to confirm.
	//
	// It exists because a job's verdict is cached per commit, so
	// re-running the same commit replays the same answer in seconds: an
	// independent sample of a flaky job has to be asked for
	// (ci.Request.Recheck), and until it comes back grain does not know
	// whether this branch is broken or merely unlucky. Holding the queue
	// on the first answer is cheap and reversible, so that happens at
	// once; spending an agent on it is not, so that waits for this.
	Confirmation string
	// ConfirmRunID is the confirming run, and ConfirmJobs the jobs it was
	// narrowed to -- the ones that did not pass, plan-wide
	// "<workflow path>:<job id>" ids, written down when the failure was
	// collected because the run they came from may be pruned before the
	// confirmation is ever started.
	ConfirmRunID string
	ConfirmJobs  []string
	// ConfirmAttempts is how many confirming runs have been started for
	// this commit, bounded so that a deployment which cannot run CI at
	// all does not ask forever.
	ConfirmAttempts int
	// ConfirmDetail is what the confirmation made of it, in words: the
	// second run's own verdict, or why there is not going to be one.
	ConfirmDetail string
	// RunRequestedAt is when an operator asked for a base-branch run here
	// by hand, and RunRequestedBy who -- nil and empty on the ordinary
	// row nobody has asked anything of (grain/task-480).
	//
	// It is a request recorded here rather than a run started on the
	// spot because starting one is the reconciler's alone: the
	// one-run-in-flight bound above and the line that says what a run is
	// made of both live there (orchestrator.startBaseBranchCI), and an
	// API handler that started its own would be a second way past both.
	// The next cycle that finds this branch with nothing in flight starts
	// the run, and the request goes with the row it was written on --
	// PutBaseBranchCI replaces the row wholesale, so a fresh run carries
	// no request and nothing has to clear one.
	//
	// What it is for is a commit grain has stopped re-asking about by
	// itself (orchestrator.baseCIRetryDue: its retries are spent), or
	// one a person wants judged now rather than after the retry delay.
	RunRequestedAt *time.Time
	RunRequestedBy string
	// RepairTaskID is the task filed to fix this branch, empty until one
	// is. It is also the one task the block does not apply to: the repair
	// merges into the very branch nothing else may merge into, which is
	// PipelinePause.UntilTaskID's shape and exists for the same reason --
	// a gate that stopped its own repair would never open again.
	RepairTaskID string
	// RedStreak is how many consecutive tips of Branch grain CI has judged
	// red on the same job(s), RedStreakJobs (ci job names, "tests/frontend"),
	// counting the tip Commit names when its run failed. RedStreakSince is
	// the first red tip of the streak, RedStreakTip the newest one counted
	// (so a re-run of the same commit is not counted twice), and LastGreenCommit the last tip
	// judged green before it, which between them are the range a bisect
	// has to search. RedStreakWoken is the tip at which the pipeline
	// steward was woken for this streak, empty until it has been, so a
	// streak wakes it once however many tips it runs on for.
	//
	// It exists for a branch whose tip moves faster than a confirmation
	// re-run finishes (grain/task-1034). Each run's verdict is about a tip
	// the branch has already left by the time it concludes, so it is
	// never confirmed the ordinary way -- and a red nobody confirms is a
	// red nobody is woken for. The streak is the verdicts of those
	// superseded tips kept instead of discarded: the same job failing on
	// Config.BaseCIRedStreakOr() consecutive tips is its own second
	// opinion (orchestrator.noteBaseCIStreak).
	//
	// Carried across the fresh row each new run writes, since the whole
	// point of it is to outlive the tip it was counted on. A tip that
	// passes the streak's jobs clears it; one that fails only other jobs
	// starts a new one.
	RedStreak       int
	RedStreakJobs   []string
	RedStreakSince  string
	RedStreakTip    string
	LastGreenCommit string
	RedStreakWoken  string
}

// Broken reports whether this branch's last post-merge run failed, which
// is the one conclusion that holds the merge queue. A nil receiver -- a
// branch nothing has ever merged into under grain's eye -- is not broken,
// and neither is a run still going or one that ended any other way: see
// Conclusion.
//
// A failure a second run of the failing jobs did not reproduce
// (BaseCICleared) is not broken either. That is the whole of what the
// confirmation buys the queue: the flaky job whose failure everybody
// recognises stops holding every merge in the repository as soon as the
// re-run of it passes, and the row still says it happened.
func (b *BaseBranchCI) Broken() bool {
	return b != nil && b.Conclusion == BaseBranchCIFailure && b.Confirmation != BaseCICleared
}

// Confirmed reports a failure a second, independently-run sample agreed
// with -- the state that earns a repair task. Everything Broken covers
// and this does not is a branch the queue is holding while it finds out,
// which is deliberately the cheaper half of the answer: latency, and no
// agent.
func (b *BaseBranchCI) Confirmed() bool {
	return b.Broken() && b.Confirmation == BaseCIConfirmed
}

// BaseBranchCIFailure is the one ci.Conclusion this package needs to
// name. pkg/model deliberately does not import pkg/ci -- the store is
// underneath the engine, not beside it -- and CIStatus already carries
// the same word the same way.
const BaseBranchCIFailure = "failure"

// Where BaseBranchCI.Confirmation can be. Written down as words rather
// than as two booleans because "nobody has asked yet" and "the answer is
// in flight" hold the queue for different lengths of time and read
// differently on a row (orchestrator.baseBrokenStatus).
const (
	// BaseCIConfirming: a second run of the jobs that failed is going.
	BaseCIConfirming = "confirming"
	// BaseCIConfirmed: it failed too, or there is not going to be an
	// answer and the failure stands unchallenged. Either way this is a
	// red branch with a repair against it.
	BaseCIConfirmed = "confirmed"
	// BaseCICleared: it passed. The first failure was a flake, the queue
	// reopens, and nobody is sent to fix a branch that is not broken.
	BaseCICleared = "cleared"
)
