package model

import "time"

// BaseBranchDispatch is grain's note of the jobs it has dispatched on a
// base branch after a merge, and the commit it dispatched them for --
// one row per (repo, branch), replaced each time a fresh dispatch is
// started (orchestrator.reconcileBaseBranchDispatch,
// RepoConfig.PostMergeDispatchJobs).
//
// # Why it is not BaseBranchCI
//
// Because nothing about it is a verdict. BaseBranchCI is the merge
// queue's gate: its failure holds every merge into the branch and earns
// an agent. What is recorded here is work that follows the branch --
// this repository's content-addressed sandbox guest cache is the case it
// was written for (grain/task-409) -- and a failure of it is a slower CI
// job later, not a broken tree. Keeping it in a row of its own is what
// makes that difference structural rather than remembered: the queue
// reads BaseBranchCI and has never heard of this table, so there is no
// path by which a cache warm that fails can stop a merge.
//
// The two also move at different speeds and hold different locks.
// BaseBranchCI holds a branch to one judging run at a time and skips the
// commits a longer run merged past; this holds a branch to one dispatch
// at a time for the same reason (a guest build is ~25 minutes on a miss,
// and merges land faster than that), and it likewise skips rather than
// queues -- the newest tip is the tree whose inputs a consumer will
// derive its key from, so a dispatch for an intermediate commit would be
// work nobody was ever going to pull.
//
// # What makes it level-triggered
//
// Commit and Jobs together. A cycle dispatches when the branch's tip is
// not Commit, or when the repository now names jobs this row was not
// dispatched with -- which is also what makes turning the setting on
// take effect without waiting for the next merge. Everything else on the
// row is the run and how it ended, kept so that an operator asking why
// the cache is cold gets an answer rather than a silence.
//
// # Not a settings table
//
// Off staterepo.SettingsTables for base_branch_ci's reason: grain writes
// it for itself, cycle by cycle, and a settings import landing mid-cycle
// must not be able to tell grain it has already dispatched work it has
// not.
type BaseBranchDispatch struct {
	Repo   RepoRef
	Branch string
	// Commit is the tip this row's dispatch was started for, and Jobs
	// the jobs it asked for -- the pair the next cycle compares against
	// to decide whether there is anything to do.
	Commit string
	Jobs   []string
	// RunID is the run, StartedAt when it was asked for, and FinishedAt
	// when a conclusion was first recorded here -- nil while it is still
	// going.
	RunID      string
	StartedAt  time.Time
	FinishedAt *time.Time
	// Conclusion is pkg/ci's own word for how the run ended ("success",
	// "failure", "errored", "cancelled"), empty while it is still
	// running, and Detail the same thing in words.
	//
	// Recorded and never acted on. A failed dispatch is not retried for
	// the same commit: what it was filling is a cache, the branch's next
	// merge asks again, and a reconciler that re-dispatched a failing
	// 25-minute build every cycle would spend a deployment's whole CI
	// capacity on it. It is written down so that "the cache is cold" has
	// somewhere to be read from.
	Conclusion string
	Detail     string
}

// InFlight reports whether this row names a dispatch that has not
// finished: the one state in which nothing new is started for the
// branch. A row with no RunID is not in flight and has no verdict
// either -- it is a branch nothing has been dispatched on yet.
func (b BaseBranchDispatch) InFlight() bool {
	return b.RunID != "" && b.Conclusion == ""
}
