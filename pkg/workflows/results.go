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

package workflows

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/version"
)

// Conclusion is how a run, a job or a step ended -- GitHub's own word
// for the same thing, and deliberately its own vocabulary rather than a
// boolean: "did not run because what it needed failed" and "failed" are
// different answers to "why is this red", and a reader who cannot tell
// them apart goes looking in the wrong log.
type Conclusion string

const (
	// ConclusionPending is the zero value: not finished, and so not yet
	// anything. A Run carries it while it is running.
	ConclusionPending Conclusion = ""
	// ConclusionSuccess is every step run and none of them failed
	// (a step with continue-on-error that failed still counts, which is
	// what that key is for).
	ConclusionSuccess Conclusion = "success"
	// ConclusionFailure is a workflow definition that could not be planned,
	// a step that exited non-zero, or a job whose sandbox stopped answering
	// once the job was running in it. It is a statement about the tree:
	// either its CI definition is invalid or work from it came back wrong.
	//
	// It is therefore the one conclusion that has consequences for the
	// branch it was reached on -- it is what model.BaseBranchCI.Broken
	// covers, what holds a repository's merge queue, and what earns a
	// pull request a repair task -- and the one nothing but the tree may
	// produce. A step that was not testing the tree cannot reach it: see
	// ConclusionErrored, and Step.Infrastructure for the key that says
	// which steps those are.
	ConclusionFailure Conclusion = "failure"
	// ConclusionErrored is a job that never got as far as judging the
	// tree: no machine to run it in (Sandboxes.Acquire failed), no permit
	// for one, or a step that fetches the environment the job's other
	// steps need and could not (Step.Infrastructure). Nothing of the tree
	// was executed, so this is not a verdict on the commit at all -- it is
	// a verdict on the deployment, and Error says what went wrong.
	//
	// This is where grain's caches are held to being caches. A cache is
	// an optimisation, and a miss is meant to cost time and nothing else;
	// the moment a miss can produce ConclusionFailure it has been
	// promoted to correctness, which no cache was designed for. So
	// everything a miss causes -- the miss itself, the rebuild it forced,
	// however long that rebuild took, a registry that would not answer, a
	// credential the warm did not have -- lands here rather than there.
	// The test suite of this repository has the scars: grain/task-427
	// deleted `real KVM sandboxes` from .grain/workflows/tests.yml
	// because a cold `guest:inputs-<key>` kept reddening it, which is a
	// cache having decided what gets tested.
	//
	// A whole run that had nothing to run is the same answer for the same
	// reason: a repository that declares workflows and matched none of
	// them for this event and this branch executed none of its tree
	// either (ci.Plan.Unmatched). The alternative was what grain did
	// before -- report it as a success, which is a green tick over a
	// commit nothing looked at, and which the merge queue and an agent
	// waiting on a verdict both read as a suite that ran.
	//
	// Its own word rather than a shade of failure, for the reason this
	// type has words at all: the two send a reader to different places
	// and a merge queue to different decisions. A failure is a tree to
	// fix and earns its pull request the repair the queue gives a broken
	// head; this is a machine to fix, and repairing the branch would
	// achieve nothing because there is nothing wrong with it.
	//
	// It is the fix to a real defect rather than a hypothetical one:
	// every CI job on this deployment's first kontur-backed runs died at
	// Acquire because a job's sandbox name was too long to be a VM name,
	// and every one of them was reported as "failure" -- a red verdict
	// on a pull request whose code had never been compiled
	// (grain/task-361).
	ConclusionErrored Conclusion = "errored"
	// ConclusionSkipped is a job whose `needs` did not all succeed, and
	// the steps after the one that failed. Nothing was run.
	ConclusionSkipped Conclusion = "skipped"
	// ConclusionCancelled is the context ending before this did -- the
	// caller giving up, the daemon shutting down, or a job timeout.
	ConclusionCancelled Conclusion = "cancelled"
)

// Done reports whether c is a conclusion at all rather than the pending
// zero value.
func (c Conclusion) Done() bool { return c != ConclusionPending }

// Every timestamp on a result -- a run's, a job's, a step's -- is a
// *time.Time, and nil is the whole point of it: a job that never got a
// machine has no start, a step still running has no finish, and a
// reader has to be able to tell that from an instant.
//
// A plain time.Time cannot say it. Go's zero value is a real-looking
// instant in the year 1, and `json:",omitempty"` does not omit it,
// because omitempty has never applied to a struct: a field tagged that
// way went over the wire as "0001-01-01T00:00:00Z" regardless, and
// every reader of it had to know to disbelieve what it said. The ones
// that did not subtracted it from now: that is how a job which spent
// four minutes waiting for a gcp-key grant and then errored came to be
// drawn on the CI page as having taken 17756979h 39m (grain/task-108,
// which patched both ends of that one wire; grain/task-111, which made
// the wire stop lying). Pointers are the idiom the rest of grain
// already uses for exactly this -- upgrade.Step, ui.Task, model's own
// optional columns.
//
// The stores are where the two spellings meet. model.CIRun keeps plain
// time.Time columns, so nil is written down as the zero time and read
// back as nil again (timePtr and timeValue, used by storedRun and
// runOf): a row written before these fields were optional reads exactly
// like one written after it.

// Run is one CI run: everything that happened for one repository at one
// commit on one event, and the single answer a reader wants from it.
//
// It is a value, and where a deployment keeps it is RunStore's business
// rather than this type's: in memory for a test, and in model.Store's
// `ci_run` tables for a daemon, which is the swap RunStore was always
// the seam for. Writing that schema *after* the engine had run is what
// this comment used to promise and what grain/task-303 did -- the
// columns are the fields below, because these are the fields a year of
// running turned out to need.
type Run struct {
	// ID is this run's own identifier, unique within the deployment and
	// stable for as long as the run is remembered. It is what the API
	// hands back and what a later cache hit is recorded against.
	ID string `json:"id"`
	// Key is the run-level fingerprint: the same repository at the same
	// commit for the same event produces the same key, whoever asked for
	// it and whenever. It is what a cache would deduplicate whole runs
	// by, and what Service.Start already deduplicates *live* runs by
	// (see its own doc comment).
	Key Key `json:"key"`

	// Source is where the run came from ("grain" or "github") when a caller
	// correlates runs across CI sources; "" means grain's own CI.
	Source string `json:"source,omitempty"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Commit string `json:"commit,omitempty"`
	Event  Event  `json:"event"`

	// RequestedBy is whoever asked -- an operator's principal id, or the
	// name of whatever inside grain asked on its own behalf. Recorded
	// rather than enforced: authorization happens before a run is ever
	// started (see the package doc on the grain-ci capability).
	RequestedBy string `json:"requestedBy,omitempty"`

	// StartedAt and FinishedAt are pointers so that a time nobody has
	// yet is *absent* -- see the note above Run on why.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Conclusion Conclusion `json:"conclusion,omitempty"`
	// Error is why a run has no jobs at all: a workflow definition that
	// could not be parsed or planned. A run that got as far as running jobs
	// reports its failures on the jobs. Transient source-access errors do not
	// become runs; the caller receives them so it can retry.
	Error string `json:"error,omitempty"`
	// Interrupted is a run that never concluded on its own because the
	// daemon running it stopped -- read back out of a durable store by
	// the process that came after (StoredRunStore.RecoverInterrupted),
	// and impossible on a run this process has watched from start to
	// finish.
	//
	// It is set alongside a cancelled conclusion rather than instead of
	// one, because it answers a different question. The conclusion says
	// how the run ended; this says that nobody decided to end it, which
	// is what tells a reader waiting on the answer that there is no
	// answer coming and it should ask again (orchestrator.collectCIRun).
	Interrupted bool `json:"interrupted,omitempty"`
	// CancelReason is the note whoever stopped this run left about why --
	// "the base branch is being reverted, this verdict is about a tree
	// nobody will merge", "wrong branch". Optional, and empty on every
	// run nobody cancelled by hand.
	//
	// It is carried on the run rather than only logged because the reader
	// it is written for is not in front of the person who wrote it: an
	// agent waiting on wait_for_checks is told its run was cancelled, and
	// a bare "cancelled" is indistinguishable from a supersede, a daemon
	// shutdown or a timeout. The note is what tells it which, and what to
	// do next. It survives the run finishing (Service.Cancel), so the
	// record a task's verdict is written from still holds it
	// (orchestrator.ciDetail).
	CancelReason string `json:"cancelReason,omitempty"`
	// CancelledBy says who wanted this run stopped, which the note above
	// cannot: a note is optional, and "cancelled with nothing written on
	// it" is what a person pressing cancel without typing anything and a
	// job grain itself timed out both look like.
	//
	// The difference decides whether anything starts another run.
	// orchestrator.reconcileTaskCI retries a cancellation that was
	// grain's own doing -- a timeout, a daemon stopping under the run, a
	// supersede -- because the branch did nothing to earn it and nothing
	// else will ever produce the verdict the merge queue is waiting for.
	// A cancellation a person asked for is the opposite: retrying it
	// would turn their button into a no-op, so it stands.
	//
	// Empty is CancelBySystem, which is every run recorded before this
	// was written down as well as every one grain stopped itself.
	CancelledBy CancelSource `json:"cancelledBy,omitempty"`
	// Recheck is a run asked for with Request.Recheck: a fresh sample of
	// the jobs it holds, often only the ones that failed before, rather
	// than an answer for the whole suite. Recorded so a reader handed one
	// -- wait_for_checks, while a task's verdict is a recheck still in
	// flight -- can say that the verdict combines it with the run it
	// re-checked.
	Recheck bool `json:"recheck,omitempty"`
	// Quarantined indicates whether the run contained quarantined test failures.
	Quarantined bool `json:"quarantined,omitempty"`
	// Build is which build of grain produced this run -- see Build, and
	// BuildOf for why it is a pointer.
	Build *Build `json:"build,omitempty"`

	Jobs []JobRun `json:"jobs"`
}

// Build is the build of grain that produced a run: the binary that read
// the workflows, planned the jobs, ran them and wrote the conclusion
// down.
//
// It is recorded because a deployment's code and its repository's code
// are two different things. grain upgrades on its own schedule, so a fix
// that is in the tree is not necessarily a fix that is running, and the
// commonest confusing report grain gets is a run that behaves unlike the
// source somebody is reading ("this is handled right here and it did not
// fire"). Without a stamp the only way to tell a wrong implementation
// from a stale deployment was archaeology: find a repository whose runs
// have the same shape every time, then bisect `since` windows over the
// history until the boundary shows up between two runs of that shape --
// which is what grain/task-388 spent a run doing, and which only worked
// because such a repository happened to exist. With it, "this run came
// out of a build that predates the fix" is one line of the run.
//
// The stamp is the one pkg/version reads out of the binary itself, so
// there is nothing for a deployment to set and nothing for it to get
// wrong; it says nothing about how far behind the remote that build is,
// which is a question about a repository nobody here has.
type Build struct {
	// Revision is the full commit the binary was built from. Full rather
	// than shortened for version.Info.Revision's own reason: this is the
	// value that gets pasted into `git show`, and a reader who wants
	// seven characters can take them (Describe does).
	Revision string `json:"revision"`
	// CommittedAt is that commit's own timestamp in UTC -- when the code
	// was written, not when it was built, which Go does not record. A
	// pointer for the reason every other optional instant on a result is
	// one (see the note above Run): absent when the stamp carried no
	// readable time, which Revision alone survives.
	CommittedAt *time.Time `json:"committedAt,omitempty"`
	// Modified says the build ran against uncommitted changes, so
	// Revision does not fully describe what produced this run. Never true
	// of a released build; worth showing when it happens, because it
	// means a run nobody can reproduce from the hash beside it.
	Modified bool `json:"modified,omitempty"`
}

// Describe is the stamp as a line of a report: enough of the revision to
// recognise, and the instant that decides whether this build predates
// the commit whoever is reading has in mind.
func (b Build) Describe() string {
	what := b.Revision
	if len(what) > 7 {
		what = what[:7]
	}
	if b.CommittedAt != nil {
		what += ", committed " + b.CommittedAt.UTC().Format(time.RFC3339)
	}
	if b.Modified {
		what += ", built from a modified tree"
	}
	return what
}

// BuildOf is a build stamp as a run records one, and nil for a binary
// that carries none.
//
// Absence is normal and is not an error: `go build -buildvcs=false`
// stamps nothing and neither does any test binary (pkg/version's own doc
// comment). nil rather than an empty Build so that a run nobody could
// stamp is *absent* from the record rather than present and blank --
// the same reading Run's optional instants take, and the same one
// ui.versionResponseFrom takes for the same fact about the same binary.
func BuildOf(info version.Info) *Build {
	if info.Revision == "" {
		return nil
	}
	build := &Build{Revision: info.Revision, Modified: info.Modified}
	if !info.Time.IsZero() {
		t := info.Time.UTC()
		build.CommittedAt = &t
	}
	return build
}

// CancelSource is who wanted a run stopped (Run.CancelledBy). Two
// answers, and they are told apart by which door the cancellation came
// through rather than by reading the note left with it: everything
// inside grain that stops a run says so at the call site, and the API
// route a person reaches (ui.Server's DELETE /api/ci/runs/{id}) is the
// only one that says CancelByOperator.
type CancelSource string

const (
	// CancelBySystem is grain stopping its own run: a job that ran past
	// ci.DefaultJobTimeout, a daemon shutting down under it, a run the
	// merge queue or a newer run for the same branch superseded. The
	// zero value, because it is also what every run stored before this
	// field existed reads back as -- and because a cancellation nothing
	// claims is far more likely to be a timeout than a person.
	CancelBySystem CancelSource = ""
	// CancelByOperator is a person asking, through grain's own API --
	// the CI pane's cancel button, `grain ci cancel`, a DELETE by hand.
	// Whatever they meant by it, they meant it about this commit, so
	// grain does not quietly start the run again.
	CancelByOperator CancelSource = "operator"
)

// supersededByRun and supersededForTask bracket the run id in
// SupersededByRunReason.
const (
	supersededByRun   = "superseded by run "
	supersededForTask = ", started for the same task"
)

// SupersededByRunReason is the cancel reason grain writes on a task's run
// when a newer run started for the same task, at the same branch, takes
// its place -- a complete run replacing a recheck, or a second
// retrigger_ci replacing the first (ui.Client.cancelSupersededTaskRuns,
// orchestrator.cancelSupersededBranchRuns).
func SupersededByRunReason(id string) string {
	return supersededByRun + id + supersededForTask
}

// SupersedingRun is the id of the run that replaced run, where grain
// cancelled run itself to make way for it (SupersededByRunReason). It is
// how a reader waiting on run -- wait_for_checks -- follows the answer to
// the run that will actually give it, rather than reporting a
// cancellation nobody asked for as the verdict.
func SupersedingRun(run Run) (string, bool) {
	if run.Conclusion != ConclusionCancelled || run.CancelledBy != CancelBySystem {
		return "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(run.CancelReason), supersededByRun)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, supersededForTask)
	if !ok || id == "" || id == run.ID || strings.ContainsAny(id, " ,") {
		return "", false
	}
	return id, true
}

// Duration is how long the run took, or how long it has been going if
// it has not finished.
//
// A run that has not started has no duration at all, and reads as zero
// rather than as the span since the year 1 -- every printer of one
// already renders a non-positive duration as "-" (cicmd's ciDuration,
// CIPage's formatDuration).
func (r Run) Duration(now time.Time) time.Duration {
	if r.StartedAt == nil {
		return 0
	}
	if r.FinishedAt != nil {
		return r.FinishedAt.Sub(*r.StartedAt)
	}
	return now.Sub(*r.StartedAt)
}

// Queued is how long this run's jobs spent waiting for machines, added
// together -- the suite-level half of JobRun.Queued.
//
// Job-time rather than wall clock, and it may well be larger than the
// run's own Duration: eight jobs each held ten minutes is eighty minutes
// of queueing inside a run that took twenty. That is the figure worth
// having. A deployment short of sandboxes is one whose suites spend more
// job-time waiting than working, and no single job's wait says that --
// "this run spent 80m queueing and 30m running" does.
//
// A cached job contributes nothing, because it waited for nothing.
func (r Run) Queued() time.Duration {
	var total time.Duration
	for _, j := range r.Jobs {
		total += j.Queued
	}
	return total
}

// JobRun is one job of one run: the unit a sandbox is built for, the
// unit a cache is keyed by, and the row a reader scans down.
type JobRun struct {
	// Workflow and WorkflowPath name the file this job came from; JobID
	// and Name name the job in it.
	Workflow     string `json:"workflow"`
	WorkflowPath string `json:"workflowPath"`
	JobID        string `json:"jobId"`
	Name         string `json:"name"`
	// Key is this job's fingerprint -- see Fingerprint. Carried on the
	// result so a cache can be written from it without re-deriving it,
	// and so a hit can be explained to whoever is looking at the row.
	Key Key `json:"key"`
	// Cached is whether this result came out of Cache rather than out of
	// a sandbox: the run is this run, the work is an older one's. It is
	// how a reader tells a job that ran from a job that was remembered --
	// the first question anybody asks of a suspiciously fast green tick
	// -- and StartedAt and FinishedAt are the earlier run's, so the row
	// still says what the work costs when it is done.
	Cached bool `json:"cached,omitempty"`
	// Sandbox is the name of the sandbox this job ran in, empty for a
	// job that never got one (skipped, cached, or failed acquiring it).
	// It is what an operator greps the daemon log for.
	Sandbox string `json:"sandbox,omitempty"`

	// StartedAt is when this job got a machine, and is nil for a job
	// that never got one -- one still queueing, one that lost a
	// prerequisite, one whose `needs` failed. FinishedAt is nil while it
	// is still going. Pointers because absent has to be absent; see the
	// note above Run.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Conclusion Conclusion `json:"conclusion,omitempty"`
	// Error is a failure that is not a step's non-zero exit: a sandbox
	// that could not be built (ConclusionErrored), a transport that
	// stopped answering, a job timeout. A failing *step* leaves this
	// empty and says so on the step.
	Error string `json:"error,omitempty"`
	// Activity is grain's current pre-sandbox explanation for a pending
	// job, with ActivityAt when that wait began. It is cleared once the
	// prerequisite is acquired.
	Activity   string     `json:"activity,omitempty"`
	ActivityAt *time.Time `json:"activityAt,omitempty"`
	// Queued is how long this job spent between becoming startable --
	// every job it needs having succeeded -- and getting a machine, and
	// QueuedReason is what held it for the longest of that, in the gate's
	// own words ("for room in this deployment's sandbox budget -- ...").
	//
	// This is the record Activity is not. Activity is live state: it is
	// cleared the instant the job is admitted and cleared again on a job
	// that never ran, so a wait that is over leaves nothing behind, and a
	// job that queued eighteen minutes and ran four is drawn exactly like
	// one that ran for twenty-two. Which means nobody can say afterwards
	// whether a job was starved or slow -- the question three jobs
	// cancelled at their timeout left unanswerable (grain/task-326) --
	// nor whether an ordering and a reservation that were added to stop
	// the starving did anything.
	//
	// Both are set on a job that got a machine and on a job that never
	// did: a job cancelled while queueing has its whole wait here, which
	// is the case the measurement is most needed for. A cached job has
	// neither, whatever the run behind the entry waited for: its
	// timestamps are that earlier run's on purpose (see Cached), but a
	// wait is what this deployment did to this job, and this job waited
	// for nothing.
	//
	// QueuedCause is that same winner as a word rather than a sentence --
	// the WaitCause the reason was said about -- and it is what an
	// aggregate is taken over. The sentence moves while the job waits and
	// is prose besides ("8 GiB free and this job asks for 16 GiB"), so
	// adding up waiting by cause from the sentences would mean matching
	// text that every gate is free to rewrite; the cause is already
	// decided at the source and this is it, kept rather than thrown away
	// (metrics.CIQueueing, grain/task-345).
	//
	// QueuedCause and QueuedReason can both be empty on a job that
	// queued: they are filled from what the gates report, and a job whose
	// whole wait was for something with nothing to say about itself has a
	// length and no cause anybody attributed it to. The duration is the
	// fact; the cause and the reason are the gate's account of it.
	//
	// Queued is a duration, so it crosses JSON as Go writes one --
	// nanoseconds -- which the CI page divides down (queuedOf in
	// ciFilters.js) and the store keeps as milliseconds.
	Queued       time.Duration `json:"queued,omitempty"`
	QueuedCause  WaitCause     `json:"queuedCause,omitempty"`
	QueuedReason string        `json:"queuedReason,omitempty"`

	// Artifacts are the files this job declared and grain copied out of
	// the sandbox before destroying it -- one entry per `artifacts:`
	// declaration, including the ones there turned out to be nothing to
	// keep for (Artifact.Note says which). Only a job that did not
	// succeed has any; see artifacts.go.
	//
	// It is the one field of a result that is not written down with the
	// rest of it. A JobRun is stored as rows in model.CIJob and exported
	// to the state repository as text; what is behind these entries is a
	// file in a directory of the daemon's own, swept on its own schedule,
	// and the honest answer to "what can I download" is the one that
	// directory gives *now* rather than the one that was true when the
	// run finished. So whatever serves a run fills this in from the store
	// (ci.Service.RunArtifacts), and a run read back after a restart says
	// what is really still there.
	Artifacts []Artifact `json:"artifacts,omitempty"`

	// FailureSummary is the structured failure summary extracted from the failing
	// step output (Go tests, Playwright tests, compiler and linter diagnostics).
	FailureSummary *FailureSummary `json:"failureSummary,omitempty"`
	// BaseCommit is the commit SHA of the base branch when this job ran,
	// used to invalidate cached negative verdicts on base transitions.
	BaseCommit string `json:"baseCommit,omitempty"`

	// Retried indicates whether this job was automatically retried after an initial failure.
	Retried bool `json:"retried,omitempty"`
	// RetryCount is the number of retries attempted for this job (bounded to 1).
	RetryCount int `json:"retryCount,omitempty"`
	// RetryReason is the first attempt's failure summary on a job that was
	// retried. It is set as the retry starts, together with Retried and
	// with Conclusion cleared, so a job being retried reads as running --
	// and says why -- rather than as the failure it is retrying from.
	RetryReason string `json:"retryReason,omitempty"`
	// Warning carries warning notes, such as recording a flake when a retry succeeds.
	Warning string `json:"warning,omitempty"`
	// Status provides a descriptive status string (e.g. "passed (retry)").
	Status string `json:"status,omitempty"`
	// Flake indicates whether the job was flaky (failed on first attempt, passed on retry).
	Flake bool `json:"flake,omitempty"`
	// Quarantined indicates whether failures in this job were quarantined (non-blocking).
	Quarantined bool `json:"quarantined,omitempty"`
	// QuarantinedTests lists the quarantined test names that failed in this job.
	QuarantinedTests []string `json:"quarantinedTests,omitempty"`

	Steps []StepRun `json:"steps"`

	// Attempts records every execution attempt of this job (1-indexed:
	// attempts/1, attempts/2, ...) so that when a job is automatically
	// retried, earlier attempts' step logs, failure summaries, and raw
	// go test output survive alongside the retry's.
	Attempts []JobAttempt `json:"attempts,omitempty"`
	// GoTestLog holds the raw `go test` output (/tmp/gotest.log) captured
	// from the job's sandbox before teardown.
	GoTestLog string `json:"goTestLog,omitempty"`

	// faultSig and faultClassified cache FailureSignature for a concluded
	// run's job so that shared-fault grouping can retain a slimmed Run
	// without keeping step output logs in memory or re-reading them from
	// SQLite across cache refreshes.
	faultSig        *Signature
	faultClassified bool
}

// FaultSignatureCache is the FailureSignature cached on a slimmed job
// (pkg/ci's shared-fault grouping), and whether one was cached at all.
func (j JobRun) FaultSignatureCache() (sig *Signature, classified bool) {
	return j.faultSig, j.faultClassified
}

// SetFaultSignatureCache caches sig, nil for "nothing comparable", as
// this job's FailureSignature.
func (j *JobRun) SetFaultSignatureCache(sig *Signature) {
	j.faultSig, j.faultClassified = sig, true
}

// JobAttempt is one execution attempt of a JobRun (1-indexed).
type JobAttempt struct {
	Attempt        int             `json:"attempt"`
	Sandbox        string          `json:"sandbox,omitempty"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	Conclusion     Conclusion      `json:"conclusion,omitempty"`
	Error          string          `json:"error,omitempty"`
	FailureSummary *FailureSummary `json:"failureSummary,omitempty"`
	Steps          []StepRun       `json:"steps,omitempty"`
	Artifacts      []Artifact      `json:"artifacts,omitempty"`
	GoTestLog      string          `json:"goTestLog,omitempty"`
}

// EffectiveAttempts returns the 1-indexed execution attempts for this job.
// For jobs recorded before multi-attempt tracking (or while a single attempt
// is running), it synthesizes attempt 1 from the job's own fields. While a
// retry is in flight, it returns the completed earlier attempts followed by
// the live retry attempt.
func (j JobRun) EffectiveAttempts() []JobAttempt {
	if len(j.Attempts) == 0 {
		if j.StartedAt == nil && len(j.Steps) == 0 && j.Conclusion == "" && j.Error == "" && j.GoTestLog == "" {
			return nil
		}
		return []JobAttempt{{
			Attempt:        1,
			Sandbox:        j.Sandbox,
			StartedAt:      j.StartedAt,
			FinishedAt:     j.FinishedAt,
			Conclusion:     j.Conclusion,
			Error:          j.Error,
			FailureSummary: j.FailureSummary,
			Steps:          j.Steps,
			Artifacts:      j.Artifacts,
			GoTestLog:      j.GoTestLog,
		}}
	}
	out := make([]JobAttempt, len(j.Attempts))
	copy(out, j.Attempts)
	for i := range out {
		if out[i].Attempt <= 0 {
			out[i].Attempt = i + 1
		}
	}
	expectedTotal := 1 + j.RetryCount
	if j.Retried && expectedTotal < 2 {
		expectedTotal = 2
	}
	if len(out) < expectedTotal && !j.Conclusion.Done() && (len(j.Steps) > 0 || j.Sandbox != "" || j.Error != "" || j.GoTestLog != "") {
		out = append(out, JobAttempt{
			Attempt:        len(out) + 1,
			Sandbox:        j.Sandbox,
			StartedAt:      j.StartedAt,
			FinishedAt:     j.FinishedAt,
			Conclusion:     j.Conclusion,
			Error:          j.Error,
			FailureSummary: j.FailureSummary,
			Steps:          j.Steps,
			Artifacts:      j.Artifacts,
			GoTestLog:      j.GoTestLog,
		})
		return out
	}
	last := &out[len(out)-1]
	if len(last.Steps) == 0 && len(j.Steps) > 0 {
		last.Steps = j.Steps
	}
	if last.GoTestLog == "" && j.GoTestLog != "" {
		last.GoTestLog = j.GoTestLog
	}
	if last.FailureSummary == nil && j.FailureSummary != nil && last.Conclusion == j.Conclusion {
		last.FailureSummary = j.FailureSummary
	}
	if len(last.Artifacts) == 0 && len(j.Artifacts) > 0 {
		last.Artifacts = j.Artifacts
	}
	return out
}

// GetAttempt returns the 1-indexed attempt n of this job.
func (j JobRun) GetAttempt(n int) (JobAttempt, bool) {
	if n <= 0 {
		return JobAttempt{}, false
	}
	attempts := j.EffectiveAttempts()
	for _, a := range attempts {
		if a.Attempt == n {
			return a, true
		}
	}
	if n <= len(attempts) {
		return attempts[n-1], true
	}
	return JobAttempt{}, false
}

// LatestAttempt returns the most recent attempt of this job.
func (j JobRun) LatestAttempt() (JobAttempt, bool) {
	attempts := j.EffectiveAttempts()
	if len(attempts) == 0 {
		return JobAttempt{}, false
	}
	return attempts[len(attempts)-1], true
}

// FailingAttempt returns the attempt most relevant for failure diagnosis.
// When a job failed on attempt 1 and its automatic retry was subsequently
// cancelled or errored before running steps, FailingAttempt returns attempt 1
// so that wait_for_checks and get_ci_log surface the actual test failure
// rather than an empty cancelled retry.
func (j JobRun) FailingAttempt() (JobAttempt, bool) {
	attempts := j.EffectiveAttempts()
	if len(attempts) == 0 {
		return JobAttempt{}, false
	}
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Conclusion == ConclusionFailure || a.FailureSummary != nil {
			return a, true
		}
		for _, s := range a.Steps {
			if s.Conclusion == ConclusionFailure {
				return a, true
			}
		}
	}
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Conclusion == ConclusionErrored || a.Error != "" {
			return a, true
		}
	}
	return attempts[len(attempts)-1], true
}

// Duration is how long this job took, or how long it has been going,
// under exactly Run.Duration's rule -- a job that never got a machine
// has no duration at all rather than a span since the year 1.
//
// A cached job's timestamps are the earlier run's (see Cached), so this
// is what the work costs when it is really done rather than the instant
// the cache answered. That is the honest number for the question a
// duration is read for: whether the job is slow, not whether this
// particular run got lucky.
func (j JobRun) Duration(now time.Time) time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	if j.FinishedAt != nil {
		return j.FinishedAt.Sub(*j.StartedAt)
	}
	return now.Sub(*j.StartedAt)
}

// StepRun is one step of one job.
type StepRun struct {
	Name string `json:"name"`
	// Conclusion is success for exit 0, failure for anything else,
	// skipped for a step after a failure, cancelled for one the context
	// ended under.
	Conclusion Conclusion `json:"conclusion,omitempty"`
	// ExitCode is the script's own status. -1 means it never produced
	// one -- the transport failed, or the step was never run.
	ExitCode int `json:"exitCode"`
	// Output is stdout and stderr together, in the order the sandbox
	// reported them, truncated in the middle past MaxStepOutput with a
	// line saying so. Together rather than apart because that is what a
	// reader wants to see and what every sandbox transport grain has
	// already hands back that way (mcp's run_command).
	Output string `json:"output,omitempty"`
	// Error is a failure of the *call* rather than of the script.
	Error string `json:"error,omitempty"`

	// StartedAt is nil for a step that was never run (skipped, or the
	// steps after a failure) and FinishedAt while it is running; see the
	// note above Run.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// MaxStepOutput is how much of one step's output a result keeps. A CI
// step can print hundreds of megabytes (a verbose test suite, a build
// with -x), and a Run is held in memory and served as JSON: unbounded
// output here is a deployment whose daemon dies of one badly behaved
// workflow.
//
// The middle is what gets dropped rather than the tail, because both
// ends carry meaning: the head says what the step was doing, and the
// tail is where the failure is.
const MaxStepOutput = 256 << 10

// MaxStoredStepOutput is how much of one step's output survives being
// written down (StoredRunStore), and it is deliberately a quarter of
// what a result carries in memory.
//
// They are different questions. MaxStepOutput bounds what one run costs
// a daemon that is holding it and serving it, and is forgotten on the
// next restart. This bounds what one run costs *forever*: a stored run
// is a row in the database, and the database is exported into the state
// repository (pkg/staterepo), where git keeps every version of every
// file it has ever been shown. 64KB is some eight hundred lines, which
// is more than anybody reads of a failing step and far more than the
// 2000 bytes the pull request comment quotes -- and it is the difference
// between a workflow that prints too much costing a few megabytes and it
// costing a state repository.
//
// Measured since (grain/task-360): the noisiest step of grain's own
// suite prints 51KB, so this is a bound the CI grain runs on itself does
// not reach, and the shape it does not cover -- a run with a great many
// steps -- is what MaxStoredRunOutput was added for.
const MaxStoredStepOutput = 64 << 10

// MaxStoredRunOutput is how much of one run's output survives being
// written down, counted across every step of every job it has.
//
// A per-step bound on its own bounds nothing, because grain does not
// decide how many steps a run has -- the repository's workflows do, and
// a repository is free to declare a dozen jobs of a dozen steps that
// each print a megabyte. Measured on 2026-09-08
// (staterepo.TestCIRunStorageCost, which drives this very code): grain's
// own two-job suite stores 57KB across its eight steps, and a suite of
// twelve jobs of six chatty steps stored 4.5MB -- which at the
// DefaultStoredRunHistory runs a deployment keeps is a 2.3GiB
// tables/ci_step.json -- which, at the time it was measured, was
// rewritten whole into the state repository, where git never forgets a
// version of anything. That was the hole this closes. The repository
// carries no such file any more (grain/task-395) and the 2.3GiB is now
// the store's alone, which is reason enough to go on bounding it.
//
// 256KB is four steps at the full per-step bound and four and a half
// times what a green run of grain's own suite stores, so nothing grain
// runs on itself is touched by it; it is a ceiling for the workflows
// nobody here writes.
//
// It is the default rather than the rule since grain/task-269:
// StoredRunRetention.RunOutputBytes is a deployment's own per-run cap,
// and this is what it comes to where nobody has chosen. What the whole
// history costs is bounded separately, by bytes and by age
// (DefaultStoredRunOutputBytes), because a per-run cap times a count of
// runs was never the number an operator was actually budgeting. A run
// that would store more than that whole ceiling has its output declined
// rather than sweeping every older run's away.
const MaxStoredRunOutput = 256 << 10

// MinStoredStepOutput is the smallest share of a run's budget worth
// giving a step. Below this a truncation is mostly the note explaining
// itself, so the note is kept and the fragment is not.
const MinStoredStepOutput = 2 << 10

func TruncateOutput(s string) string { return TruncateOutputTo(s, MaxStepOutput) }

// TruncateOutputTo keeps the head and the tail of s and says what it
// dropped. The middle goes rather than the tail, because both ends carry
// meaning: the head says what the step was doing, and the tail is where
// the failure is.
//
// A step's output is cut twice on the way to a reader -- once to
// MaxStepOutput as the step ends, and again to MaxStoredStepOutput on
// its way into the database -- so what arrives here is often already a
// truncation carrying a note of its own. The count is carried across
// that: a second cut says how much of the *step's* output is missing
// rather than how much of the first cut it took. Until it did, 40MB of
// guest build stored as "192KB elided", which is the difference between
// a reader who knows to go looking elsewhere and one who thinks they are
// reading a log with a page missing from it (grain/task-385).
func TruncateOutputTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	body, already := splitElided(s)
	head := max / 4
	tail := max - head
	// Only reachable when it was the earlier note's own bytes that put s
	// over the bound: keep all of the text there is and let the note
	// account for what an earlier cut took.
	if head+tail > len(body) {
		if tail > len(body) {
			head, tail = 0, len(body)
		} else {
			head = len(body) - tail
		}
	}
	var b strings.Builder
	b.WriteString(body[:head])
	b.WriteString("\n\n[grain ci] ")
	b.WriteString(ElisionNote(len(body) - head - tail + already))
	b.WriteString("\n\n")
	b.WriteString(body[len(body)-tail:])
	return b.String()
}

// elidedPattern matches the note TruncateOutputTo and liveOutput.text
// leave behind, so that a later cut of the same text can take the
// earlier one's count with it rather than reporting only its own.
var elidedPattern = regexp.MustCompile(`\n\n\[grain ci] (\d+)KB of this step's output elided from the middle by grain[^\n]*\n\n`)

// splitElided is s without the note an earlier truncation left in it,
// and the number of bytes that note said were already gone. Output
// nothing has cut yet comes back unchanged, with nothing dropped.
//
// There is at most one note to find, because this is what puts one
// there: every truncation removes the note it found before writing its
// own, so the count accumulates in one place instead of the text
// growing a note per cut.
func splitElided(s string) (string, int) {
	m := elidedPattern.FindStringSubmatchIndex(s)
	if m == nil {
		return s, 0
	}
	kb, err := strconv.Atoi(s[m[2]:m[3]])
	if err != nil {
		return s, 0
	}
	return s[:m[0]] + s[m[1]:], kb << 10
}

// ElisionNote is what stands in the middle in place of what was dropped,
// counted in KB and rounded up: "0KB elided" reads as a bug in grain
// rather than as the few hundred bytes it is.
func ElisionNote(dropped int) string {
	return strconv.Itoa((dropped+1023)/1024) + "KB of this step's output elided from the middle by grain, not by the step: " +
		"a result is kept in memory, written to a database and served as JSON, so one workflow cannot spend " +
		"the deployment's. Have the step write what matters to a file and print that instead."
}
