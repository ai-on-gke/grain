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

// CIStatus is what grain's own CI (pkg/ci) last said about the branch a
// task pushed: which run was started, against which commit, and -- once
// it is over -- how it ended and why, in words.
//
// # Why the answer is stored and not just linked to
//
// Because a link outlives its target. A ci.Run is kept for a while and
// then forgotten -- durably now (CIRun, and ci.DefaultStoredRunHistory
// runs of it), rather than in a map a restart emptied, but still
// forgotten -- and the question this field answers ("did the branch
// pass?") is the one everybody asks about a task long afterwards.
// Conclusion and Detail are therefore copies, not references: they are
// the answer itself, kept where the task is read, and they stay true
// after the run behind them has been pruned. RunID is still carried,
// because while the run does exist it is what GET /api/ci/runs/{id}
// serves the job-by-job detail from -- which, now that a run survives
// the daemon, is a link worth following for far longer than it used to
// be.
//
// # Why one status and not a history
//
// One task pushes one branch (model.BranchName), and a reader wants to
// know about the branch as it stands now. A task that is dispatched
// again pushes more commits to that same branch, and the answer about
// the previous tree is not a second fact to keep -- it is a stale one to
// replace. So this is overwritten in place, keyed by nothing: Commit
// says which tree the verdict is about, and a verdict about a commit
// that is no longer the head is simply out of date and about to be
// replaced. Automatic retries retain the immediately previous verdict only
// long enough to avoid repeating the same comment; they do not turn this
// current-status record into a run history.
//
// # What it holds
//
// The merge, now. This record is what the merge queue reads grain CI's
// answer off (orchestrator.grainCIVerdictOf): a Conclusion of "failure"
// about the commit a pull request would merge makes it failing, and a
// status with no Conclusion yet makes it pending. Which is why Commit is
// not decoration -- it is what keeps a verdict from being applied to a
// tree it was never about -- and why an absent status has to keep
// meaning "nothing known" rather than "not passed": a daemon that has
// forgotten a run writes nothing here, and nothing here must never hold
// a pull request. See orchestrator.withGrainCI for the whole of that
// policy.
type CIStatus struct {
	// RunID is the ci.Run this status came from. Kept for as long as the
	// status is, whether or not the run itself still exists.
	RunID string
	// Commit is the full SHA the run was started against -- which tree
	// the verdict below is about. Empty only where the source could not
	// resolve one.
	Commit string
	// StartedAt is when grain asked for the run.
	StartedAt *time.Time
	// Conclusion is ci.Conclusion's own vocabulary as a string --
	// "success", "failure", "errored", "skipped", "cancelled" -- passed through
	// rather than re-encoded so the word on a task matches the word the
	// CI API serves. Empty means the run has not finished: this package
	// does not import pkg/ci (nothing else in the model needs to, and a
	// store type depending on an engine would be the wrong way round),
	// so the vocabulary travels as a string the way task_run.outcome
	// already does.
	Conclusion string
	// FinishedAt is when it ended, nil while it is still going.
	FinishedAt *time.Time
	// Detail is the human-readable half of the verdict, written once the
	// run concludes: which jobs failed, or why there was nothing to run.
	// It is what makes this record still worth reading after the run has
	// been pruned out of ci_run, and what a task listing can show without
	// a join into a run's jobs.
	Detail string
	// Attempts counts runs started for this commit, including the current
	// one. Errored runs are retried only a bounded number of times.
	Attempts int
	// PreviousConclusion and PreviousDetail retain the verdict replaced by
	// an automatic retry, so an identical infrastructure error is not
	// announced again. They are cleared when a new commit replaces this one.
	PreviousConclusion string
	PreviousDetail     string
	// CancelledBy is who wanted a cancelled run stopped
	// (ci.Run.CancelledBy's vocabulary as a string: empty for grain's own
	// timeout, shutdown or supersede, "operator" for a person asking).
	// Copied onto the task beside the verdict for the reason every other
	// field here is -- the run it came from is pruned long before the
	// task is -- and read by exactly one decision:
	// orchestrator.reconcileTaskCI starts a replacement run for a
	// cancellation grain caused itself and leaves one a person asked for
	// standing.
	CancelledBy string
	// RecheckOf is set while, and after, this status is an uncached
	// recheck of a run that failed: the id of that first run, with
	// RecheckDetail its verdict (orchestrator.startCIRecheck). RunID is
	// then the recheck's own run. Empty on every status that is not a
	// recheck, which is also what makes a red verdict eligible for one:
	// a commit is rechecked once, and only a job that fails both times
	// counts as the branch's failure.
	RecheckOf     string
	RecheckDetail string
}

// Done reports whether this status is a verdict rather than a run still
// in flight.
func (c *CIStatus) Done() bool { return c != nil && c.Conclusion != "" }

// Green reports whether grain CI passed. A status that has not finished
// is not green, and neither is a nil one -- "we do not know" and "no"
// are both answered false here, so a caller reading this as permission
// to do something has to have a verdict in hand.
func (c *CIStatus) Green() bool { return c != nil && c.Conclusion == "success" }
