package model

import "time"

// ScheduledWorkflowRun is one firing of a schedule translated out of a
// workflow's own `on: schedule` cron (WorkflowReviewTemplateID,
// grain/task-344): the run grain started for it, and the review that run
// is owed.
//
// One row per schedule, replaced by the next firing -- a schedule can
// only have one of these in flight at a time, because a firing that has
// not finished is exactly what stops the next one starting
// (Schedule.Overlap).
//
// That holds because every one of these schedules is OverlapSkip and
// nothing can change it: the translation writes that policy
// (repodecl.workflowSchedule) and a declared row is read-only here
// (ui.Client.refuseDeclaredEdit). A workflow schedule that was ever
// allowed to overlap would need this keyed per firing rather than per
// schedule, since the second firing's row would otherwise replace the
// first and leave that run with nobody coming to read it.
//
// # Why the firing needs a record at all (grain/task-351)
//
// Because the run outlasts the agent that reads it. The first cut of
// this filed the review task at the due tick, in the same act as
// starting the run, and told the agent to follow the run it had just
// started. For this deployment's own nightly that meant an agent
// dispatched at 02:11 watching a suite whose longest job alone is
// allowed two hours, against an agent run budget of about the same: the
// honest report its prompt asks for -- "here is what finished, the rest
// is unread" -- was the *expected* outcome rather than the edge case.
//
// So the two halves are pulled apart, which is what BaseBranchCI already
// does for the run that follows a merge: the firing starts the run and
// writes it down here, and a later cycle reads the verdict off the
// finished run and files the review then, with the conclusion already in
// the task's own comment. The agent's job becomes reading a run that has
// ended, which is a job that fits in an agent run.
//
// # Not a settings table
//
// grain writes this for itself, cycle by cycle, so it is off
// staterepo.SettingsTables for base_branch_ci's reason: a settings
// import landing between the run and its review must not resurrect a
// firing that has already been reviewed, or drop one whose run is still
// going.
type ScheduledWorkflowRun struct {
	// ScheduleID is the schedule that fired, and the row's identity.
	ScheduleID string
	// Stem is the workflow file's stem ("nightly"), carried on the row so
	// that a review can still be filed for a schedule whose declaration
	// has gone -- a workflow whose cron was removed this morning still
	// owes a report on the run it started last night.
	Stem string
	// Repo and Branch are what the run is against. Branch is empty until
	// the run is started, because a schedule that names no base means the
	// repository's default branch and that is a question for GitHub
	// (orchestrator.scheduledWorkflowRef).
	Repo   RepoRef
	Branch string
	// Workflow is the workflow file the run was narrowed to, filled in
	// when the run is started.
	Workflow string
	// RunID is the grain CI run, empty until one has been started --
	// which is the state a firing is in for the tick between being
	// recorded and being started, and the state it stays in if starting
	// it never works.
	RunID string
	// FiredAt is when the schedule came due. AttemptedAt is when grain
	// last asked CI for a run -- the run's own start when that worked,
	// and the last failed attempt when it did not, which is what spaces
	// the retries. FinishedAt is when a conclusion was first recorded
	// here.
	FiredAt     time.Time
	AttemptedAt *time.Time
	FinishedAt  *time.Time
	// Attempts is how many times grain has asked CI to start this
	// firing's run. It bounds the asking: a deployment whose CI cannot be
	// reached at all must report one dead night rather than retry it
	// until the next one (orchestrator.scheduledWorkflowStartDue).
	Attempts int
	// Conclusion is pkg/ci's own word for how the run ended ("success",
	// "failure", "errored", "cancelled"), empty while it is still going.
	// A run that was never started at all, or that grain gave up waiting
	// for, is recorded as errored: no verdict is a verdict about nothing,
	// and the review still gets filed to say so.
	Conclusion string
	// Detail is the verdict in words -- which jobs failed, or why there
	// is no run -- for the comment the review task is filed with.
	Detail string
	// Unfinished says that the conclusion above is one grain wrote over
	// a run that had not ended: grain stopped waiting for it
	// (orchestrator.scheduledWorkflowDeadline) and filed the review
	// anyway. It is the one conclusion of grain's own that still has a
	// run behind it to read -- the jobs that did finish have logs, and
	// the one that did not is still writing one -- where a firing that
	// was never started has no run at all and a run lost with its daemon
	// has nothing left to read. The reviewer is told which of those it
	// has (orchestrator.workflowReviewComment) and the two want opposite
	// things from it, so the distinction cannot live in Detail's prose:
	// the review is often filed by a later cycle than the one that
	// reached the conclusion, which has nothing but this row to go on.
	Unfinished bool
	// ReviewTaskID is the task filed to read this run, empty until one
	// is. It is, with ReviewNotNeeded, what says this firing is over:
	// while both are unset the schedule's overlap policy treats the
	// firing as unfinished, because a nightly must not stack on a morning
	// whose run has not been read.
	ReviewTaskID string
	// ReviewNotNeeded says this firing is over with no review filed, and
	// none ever will be: its run passed, and its schedule only asks for
	// a review when there is something to look at (WorkflowReviewPolicy,
	// grain/task-3160). An explicit state rather than an empty
	// ReviewTaskID, because empty already means "not filed yet" -- and
	// a firing waiting for a review that will never exist would hold
	// its schedule's cadence for ever.
	ReviewNotNeeded bool
}

// Settled reports whether this firing is finished: the run has ended
// one way or another, and either somebody has been sent to read it or
// it passed and nobody needs to.
func (r *ScheduledWorkflowRun) Settled() bool {
	return r != nil && (r.ReviewTaskID != "" || r.ReviewNotNeeded)
}

// Started reports whether grain CI has a run for this firing. A firing
// that is not started is either one recorded this very tick or one whose
// run could not be started at all -- Conclusion tells those apart.
func (r *ScheduledWorkflowRun) Started() bool { return r != nil && r.RunID != "" }

// ScheduledWorkflowFiringTag is the idempotency marker the review task
// for one firing carries, scoped to that firing rather than to the
// schedule: ScheduleFiringTag is on the task too and means "this
// schedule filed it", which cannot tell last night's review from
// tonight's. Checked before filing, so a daemon restarted between the
// task being written and the row being updated files one review rather
// than two.
//
// The firing's own time is what scopes it, the way a base-branch
// repair's tag is scoped by the commit that went red: a firing is
// identified by when it came due, and seconds are plenty -- two firings
// of one schedule in the same second is not a cadence this scheduler can
// express.
func ScheduledWorkflowFiringTag(scheduleID string, firedAt time.Time) string {
	return "workflow-run:" + scheduleID + ":" + firedAt.UTC().Format("20060102T150405Z")
}
