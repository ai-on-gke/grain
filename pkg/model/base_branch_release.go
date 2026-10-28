package model

import "time"

// BaseBranchRelease is grain's note of the trusted release run it started
// on a base branch after a merge, and the commit it started it for -- one
// row per (repo, branch), replaced each time a fresh release is started
// (orchestrator.reconcileBaseBranchRelease).
//
// # Why it is not BaseBranchCI (grain/task-461)
//
// Because publishing is not a verdict about the tree, and until this it
// was the only verdict there was. A base-branch run *was* the trusted
// release event: one run, one row, one conclusion, answering both "is
// this tree good" and "did the image publish" -- and BaseBranchCI.Broken
// is what the merge queue reads before it merges anything, so a flaky
// publisher stopped every merge in the repository until a person cleared
// it. On this deployment that was the dominant failure mode for weeks,
// and an operator ruled on it: *"Release should not be the gate. That is
// the CI's job. If we wanted release to be the gate then we would do it
// as part of the ci jobs."*
//
// So the judging run and the publishing run are now two runs. The
// judging one is a push of the repository's own test plan and it lives
// in BaseBranchCI, where the queue reads it. The publishing one lives
// here, in a table the queue has never heard of, which is what makes
// "a failed release cannot hold a merge" structural rather than
// remembered -- exactly the property BaseBranchDispatch beside this file
// already has for work that merely follows a branch.
//
// # Why it is not BaseBranchDispatch either
//
// It very nearly is: same shape, same rules, same "recorded and never
// acted on". What it cannot be is the same *row*. BaseBranchDispatch
// holds one run per (repo, branch), and RepoConfig.PostMergeDispatchJobs
// is deliberately not deduplicated against the release run (see its own
// doc comment: grain cannot tell what a release workflow publishes by
// looking, and the jobs an operator names may live in a workflow the
// release plan does not contain -- `.grain/workflows/image.yml` here,
// which answers `workflow_dispatch`). Folding the release into that row
// would therefore stop a repository with releases *on* dispatching the
// jobs it named, which is the coupling grain/task-409 took apart. A
// second row per branch would mean a primary key this schema's
// additive-only migrations cannot change, so it is a table of its own.
//
// # What makes it level-triggered
//
// Commit alone. A cycle starts a release when the branch's tip is not
// Commit, and starts nothing while one is in flight -- the row is the
// lock, BaseBranchDispatch's rule and for its reason: a publish is long,
// merges land faster than it finishes, and the newest tip is the tree
// anybody wants published anyway, so intermediate commits are skipped
// rather than queued.
//
// # Not a settings table
//
// Off staterepo.SettingsTables for base_branch_ci's reason: grain writes
// it for itself, cycle by cycle, and a settings import landing mid-cycle
// must not be able to tell grain it has already published a tree it has
// not.
type BaseBranchRelease struct {
	Repo   RepoRef
	Branch string
	// Commit is the tip this row's release was started for -- what the
	// next cycle compares the branch's tip against to decide whether
	// there is anything to publish.
	Commit string
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
	// Recorded and acted on only by a bounded retry: no hold, no repair,
	// and at most MaxReleaseAttempts releases of this commit. A failed release is images that did not ship, which is a
	// thing to notice rather than a reason to stop the repository
	// merging, and the branch's next merge asks again. Noticing it is
	// what Failures below is for, and the surface that says it out loud
	// is ui.BaseBranchPublishing (grain/task-511) -- a reading, not a
	// gate: nothing in this package or the one that writes it has any
	// path to the merge queue.
	Conclusion string
	Detail     string
	// Failures is how many releases in a row have ended anything but
	// success on this branch, counting the one this row names once it has
	// concluded, and FailingSince is when the first of them was started.
	// Zero and nil on a branch whose last release published, which is
	// every branch that is working.
	//
	// They are here because the row itself cannot answer the question a
	// reader has (grain/task-511). One row per (repo, branch), replaced
	// each time a release starts, means the only history there is is the
	// newest attempt: on a repository that merges often a failed row is
	// overwritten by the next cycle's in-flight one within minutes, so
	// "the last release failed" flickers and "this repository has shipped
	// nothing since Tuesday" -- the thing actually worth telling somebody
	// -- is unsayable. Two counters carried across the replace make it
	// sayable, and one failure stays distinguishable from a fortnight of
	// them, which is what keeps the surface off a repository having one
	// bad afternoon.
	//
	// Anything that concluded and is not success counts. A run that never
	// concluded -- interrupted by a daemon restart, or lost to it -- does
	// not: it is forgotten and the branch released again on the next
	// cycle (orchestrator.forgetLostBaseBranchRelease), so a restart
	// landing mid-release inflates no streak and files no triage.
	Failures     int
	FailingSince *time.Time
	// Attempts is how many releases grain has started for Commit: one for
	// the release a merge sets off, two once a failed one has been
	// retried (orchestrator.startBaseBranchRelease, grain/task-4432). Zero
	// on a row written before it was counted, which means one. It starts
	// again at one whenever Commit changes.
	Attempts int
}

// MaxReleaseAttempts is how many releases grain starts on its own for one
// commit: the one its merge sets off and one retry if that one did not
// publish. After that the commit waits for a newer merge, or for somebody
// to start a release run by hand, rather than spending a twenty-minute
// release on a tree that has already failed twice.
const MaxReleaseAttempts = 2

// AttemptsMade is Attempts with a row written before the count existed
// read as the one attempt it records.
func (b BaseBranchRelease) AttemptsMade() int {
	if b.Attempts < 1 && b.RunID != "" {
		return 1
	}
	return b.Attempts
}

// Failing reports whether this branch's releases are in an unbroken run
// of failures -- which is true while the next one is still in flight,
// because a streak is about what has published and not about what is
// running right now.
func (b BaseBranchRelease) Failing() bool { return b.Failures > 0 }

// InFlight reports whether this row names a release that has not
// finished: the one state in which nothing new is started for the
// branch. A row with no RunID is not in flight and has no verdict
// either -- it is a branch nothing has been published from yet.
func (b BaseBranchRelease) InFlight() bool {
	return b.RunID != "" && b.Conclusion == ""
}

// BaseReleaseTriageTag is the idempotency marker a base-branch release triage carries.
func BaseReleaseTriageTag(repo RepoRef, branch, commit string) string {
	return "base-release-triage:" + repo.String() + ":" + branch + ":" + commit
}

// BaseReleaseTriageBranchTag is the branch-scoped tag every base-branch release triage carries.
// Scoped to the branch so at most one active release triage task exists per base branch.
func BaseReleaseTriageBranchTag(repo RepoRef, branch string) string {
	return "base-release-triage:" + repo.String() + ":" + branch
}
