package model

import "time"

// CIRun is one run of grain's own CI (pkg/ci) as this store keeps it:
// the run, the jobs it planned, and the steps each job ran.
//
// # Why the model has a shape of its own for this
//
// Because the dependency only goes one way. pkg/ci is the engine and
// this is the store; the engine may read and write rows here (ci.
// NewStoredRunStore does), and nothing here may know what an engine is
// -- so the vocabulary travels as strings, the way CIStatus.Conclusion
// and task_run.outcome already do, and ci.Run is converted at the seam
// rather than stored directly. The cost is one conversion in one file
// (pkg/ci/runstore_stored.go) and the benefit is that a change to how a
// run is *executed* is not a change to the schema.
//
// # What is not here
//
// A run's plan, its workflows' text, its cache entries. This is what
// happened, not what was asked for: enough for `grain ci show` and the
// CI page to serve a finished run in full, and nothing that would let a
// run be replayed out of the database.
type CIRun struct {
	// ID is the run's own identifier, the one the API serves it under and
	// the one CIStatus.RunID points at.
	ID string
	// Key is the run-level fingerprint (ci.Key as a string): the same
	// repository at the same commit for the same event has the same key,
	// which is how "is this exact run already in flight?" is asked.
	Key string

	Repo   string
	Ref    string
	Commit string
	Event  string
	// RequestedBy is whoever asked -- a principal id, or a task id for a
	// run grain started for a branch of its own. Recorded, never
	// enforced.
	RequestedBy string

	StartedAt  time.Time
	FinishedAt time.Time
	// Conclusion is ci.Conclusion's own vocabulary as a string, empty
	// while the run is still going. Empty in a *stored* row means the
	// process that was running it has not written a conclusion yet --
	// which, for a process that is no longer there, is what
	// RecoverInterruptedCIRuns exists to resolve.
	Conclusion string
	// Interrupted is set on a run that never concluded on its own because
	// the daemon running it stopped. It is the difference between "this
	// was cancelled" and "nobody will ever finish this", and the reader
	// that cares is orchestrator.collectCIRun: a task waiting on an
	// interrupted run needs a fresh run, not a verdict.
	Interrupted bool
	// Error is why a run has no jobs at all -- workflows that could not be
	// read or did not parse. A run that got as far as running jobs
	// reports its failures on the jobs.
	Error string
	// CancelReason is the note whoever stopped this run left about why,
	// empty on every run nobody cancelled by hand. It is kept with the
	// run because the reader it was written for -- an agent told its CI
	// run was cancelled, a person looking at the run next week -- arrives
	// long after the person who wrote it has gone (ci.Run.CancelReason).
	CancelReason string
	// CancelledBy is who wanted the run stopped -- ci.CancelSource's own
	// vocabulary as a string, empty for grain's own doing (a timeout, a
	// daemon stopping under it, a supersede) and "operator" for a person
	// asking through grain's API. The note above is optional and this is
	// not: it is what tells the merge queue's reconciler whether to start
	// another run for the same commit or leave the cancellation standing
	// (ci.Run.CancelledBy).
	CancelledBy string

	// Build, BuildTime and BuildModified are which build of grain
	// produced this run -- the commit the binary was built from, that
	// commit's own timestamp, and whether the tree it was built in was
	// dirty (ci.Build, which says why a run carries this at all).
	//
	// Empty is a run no build stamped: every run recorded before these
	// columns existed, and any run of a binary built with
	// -buildvcs=false. That is a reading a row cannot distinguish from
	// the other, and does not need to: both mean "this record cannot say
	// which build made it", which is what a reader is told.
	Build         string
	BuildTime     time.Time
	BuildModified bool

	// Recheck is ci.Run.Recheck: the run was an uncached recheck. Kept
	// because a reader of the run has nothing else to tell a recheck of a
	// commit's failed jobs from an ordinary run of those jobs alone --
	// wait_for_checks' note on a full-suite wait it handed a recheck reads
	// it (grain/task-2388).
	Recheck bool

	Jobs []CIJob
}

// Done reports whether this run has a conclusion rather than being one
// still in flight.
func (r CIRun) Done() bool { return r.Conclusion != "" }

// CIJob is one job of one run, in the order the run planned it.
type CIJob struct {
	Workflow     string
	WorkflowPath string
	JobID        string
	Name         string
	// Key is this job's own fingerprint, which is what the job cache is
	// keyed by -- kept so a row can say why a job that took no time at
	// all took no time at all.
	Key string
	// Cached is whether the result was served from that cache rather than
	// run: the run is this run, the work was an earlier one's.
	Cached bool
	// Sandbox names the machine the job ran in, empty for one that never
	// got a machine.
	Sandbox string

	StartedAt  time.Time
	FinishedAt time.Time
	Conclusion string
	// Error is a failure that is not a step's non-zero exit -- a sandbox
	// that could not be built, a transport that stopped answering.
	Error      string
	Activity   string
	ActivityAt time.Time
	// Queued is how long this job waited for a machine before it got one
	// -- or, for a job that never got one, before it was given up on --
	// QueuedCause is the kind of thing that held it longest
	// (ci.WaitCause: "slot", "budget", "hole", "grant", "run",
	// "priority"), and QueuedReason is that same thing in the words
	// whatever was holding it used (ci.JobRun.Queued).
	//
	// The cause is a column of its own rather than something read back
	// out of the sentence because only the cause can be added up: the
	// sentence is prose a gate rewrites as the deployment moves under a
	// waiting job, so a deployment's waiting bucketed by cause has to
	// come from a stored word (metrics.CIQueueing, grain/task-345). Both
	// are empty on a job whose wait nothing attributed -- including every
	// job recorded before this build, which contributes its duration and
	// no cause rather than a guessed one.
	//
	// Activity above is the live version of the same fact and is cleared
	// when the wait ends; this is what is left afterwards, and it is the
	// only thing in the row that can tell a job that was starved from a
	// job that was slow. Kept in the store as milliseconds, since a
	// column of nanoseconds is a column nobody can read.
	Queued       time.Duration
	QueuedCause  string
	QueuedReason string

	// RetryCount is how many times grain ran this job again in a fresh
	// sandbox after its first attempt failed (ci.JobRun.RetryCount, at
	// most 1), and RetryReason is that first attempt's failure summary.
	// A job retrying right now has a RetryCount and no Conclusion yet,
	// which is what lets a reader draw it as running rather than as the
	// failure it is being retried from.
	RetryCount  int
	RetryReason string

	// Flake is a job that failed its first attempt and passed its retry,
	// Quarantined one that passed only because every test it failed is
	// listed in .grain/quarantine.yml, and QuarantinedTests those tests
	// (ci.JobRun). Status is the job's own word for either ("passed
	// (retry)", "passed (quarantined)") and Warning the sentence it says
	// it in. Without them a job that passed on a retry or a quarantine
	// reads back from the store as a clean pass, which is exactly what it
	// was not.
	Flake            bool
	Quarantined      bool
	QuarantinedTests []string
	Status           string
	Warning          string

	// FailureSummary is ci.JobRun.FailureSummary as JSON, already bounded
	// by whoever wrote it (ci.MaxStoredFailureSummary) -- opaque here, the
	// way a step's Output is. Empty on a job with no summary.
	FailureSummary string

	// BaseCommit is the base branch commit the job's verdict was reached
	// against (ci.JobRun.BaseCommit), empty for a run that named no base.
	// The job cache keeps it to judge a cached failure stale once the base
	// has moved; here it is what lets run history say which base a
	// verdict was against.
	BaseCommit string

	// Attempts is ci.JobRun.Attempts as JSON, already bounded by whoever
	// wrote it. Empty on a single-attempt job with no extra attempt logs.
	Attempts string

	Steps []CIStep
}

// CIStep is one step of one job, in the order the job ran them.
type CIStep struct {
	Name       string
	Conclusion string
	// ExitCode is the script's own status; -1 means it never produced
	// one.
	ExitCode int
	// Output is stdout and stderr together, already bounded by whoever
	// wrote it (ci.MaxStoredStepOutput). Nothing here truncates: a store
	// that silently shortened what it was handed would make the row
	// disagree with the run the caller still has in hand.
	Output string
	Error  string

	StartedAt  time.Time
	FinishedAt time.Time
}
