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

package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gritcache "github.com/ai-on-gke/grain/pkg/grit/cache"
	gritsteps "github.com/ai-on-gke/grain/pkg/grit/steps"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// DefaultConcurrency is a modest per-run job concurrency limit for callers
// that want to cap one run without configuring a deployment-wide gate.
// Engine itself imposes no per-run limit when Concurrency and
// ConcurrencyLimit are both unset (0), leaving Engine.Jobs (the deployment's
// sandbox budget and any team CI job limit) as the bound.
const DefaultConcurrency = 4

// Engine runs a Plan: one sandbox per job, as many at a time as
// Concurrency and Jobs between them allow, in an order that honours
// `needs`.
//
// It holds no state between runs -- everything one run needs is in the
// Plan it was handed and the Run it returns -- so a caller may run
// several at once against one Engine, and a test may drive it with no
// clock, no store and no daemon behind it.
type Engine struct {
	// NoteOOM is told of every job sandbox found OOM-killed -- the
	// daemon's model.OvercommitController.NoteOOM. nil tells nobody.
	NoteOOM func(time.Time)
	// Sandboxes builds each job's machine. Required: an Engine without
	// one fails every run it is given, rather than pretending a job with
	// nowhere to run succeeded.
	Sandboxes gritsteps.Sandboxes
	// Admission reserves prerequisites whose scarcity should leave the job
	// outside both sandbox limits. nil means no pre-sandbox prerequisite.
	Admission gritsteps.Admission
	// ExplicitOnlyCapability reports a capability a job is given only by
	// declaring it, never as part of the whole set an undeclared job
	// gets (jobCapabilities): the scarce ones Admission mints before the
	// sandbox exists. nil treats every capability the old way.
	ExplicitOnlyCapability func(id string) bool
	// GuestCache warms the sandbox guest a job will resolve before that
	// job takes a machine, so that a tree whose guest nothing has
	// published costs one build rather than one per job
	// (guestcache.go). nil means no warming: every job resolves its own
	// guest, which is what they all did before this existed, and so does
	// every job that declares no `guest-cache:`.
	GuestCache gritcache.GuestCacheWarmer
	// Artifacts is where a failing job's declared files are kept
	// (artifacts.go). nil means a deployment with nowhere to put them:
	// every job runs exactly as it did before this existed, and a job
	// that declares `artifacts:` keeps nothing -- which is the same
	// treatment a missing Cache gets, and for the same reason. A file
	// left behind is worth having and is never worth failing a job over.
	Artifacts workflows.ArtifactStore
	// Cache is consulted before a job is started and written after it
	// finishes. nil means noCache -- every job runs, every time, which is
	// what a deployment that has nowhere to keep entries falls back to
	// (see DirCache, and pkg/cli/ci.go for the wiring).
	Cache workflows.Cache
	// Concurrency is how many jobs of one run may hold a sandbox at once;
	// zero (the default) imposes no per-run limit, so all ready jobs in a
	// run proceed directly to Jobs.
	//
	// It bounds one run. Jobs below is the bound across every run this
	// Engine is driving at once, which is the one a deployment sharing
	// its sandboxes with anything else actually needs -- see Gate.
	Concurrency int
	// ConcurrencyLimit, when set, is the live form of Concurrency. It is
	// read when each run starts so a deployment can resize CI without
	// rebuilding its Engine (model.Config.CIJobsPerRun). Answering 0
	// leaves Concurrency (or 0 = unlimited when Concurrency is also 0) as
	// the width of one run.
	ConcurrencyLimit func() int
	// Jobs, when set, is the bound every run of this Engine shares: each
	// job charges its shape there before it asks for a machine and gives
	// it back when the machine is released, so several runs at once cannot
	// between them commit more of the host than the deployment's budget
	// allows (grain/task-296, grain/task-28).
	//
	// nil means Concurrency is the whole bound, which is the right shape
	// for a test and for anything driving one run at a time.
	Jobs Gate
	// JobQueued and JobDequeued let something outside this package track
	// which run owns a job id Jobs has assigned, for exactly as long as
	// that job might still be asked about by SharedGate.Stuck -- from the
	// moment a job joins Jobs' queue (JobQueued, with the job's own id
	// and this run's) to the moment its Acquire call returns, admitted or
	// not (JobDequeued). Neither is this package's business to keep: a
	// gate knows nothing about runs, and this is how cmd/grain's own
	// Stuck hook learns which run a refused job's id belongs to without
	// this package learning anything about tasks in return.
	//
	// Both nil, the default, tracks nothing, which is every Jobs gate
	// without a Stuck hook of its own.
	JobQueued   func(jobID, runID string)
	JobDequeued func(jobID string)
	// Priority, when set, is what this run's jobs are worth at that gate
	// against every other run's -- the answer to "which of the branches
	// queueing for this deployment's sandboxes matters most". grain's own
	// deployment answers it out of the merge queue: the change at the
	// front of a repository's queue is the one whose verdict unblocks
	// everything behind it (cmd/grain's ciMergeQueuePriority,
	// ci.PriorityMergeHead).
	//
	// nil ranks every run the same, which is what a test and a laptop
	// want and what this gate did before it had an order at all.
	//
	// It is asked per queueing job per attempt rather than once when the
	// run starts, because the answer moves while a job waits -- see
	// GateRequest.Priority.
	Priority func(workflows.Request) workflows.Priority
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Log, when set, is called with one line per job as it starts,
	// finishes or is served from cache -- the daemon's own log, in a
	// real deployment. nil discards.
	Log func(format string, args ...any)
	// Stopping, when non-nil, is closed when the hosting daemon begins
	// shutting down. Once closed, the engine stops starting new jobs, new
	// steps, or automatic retries, and records jobs that fail in the
	// shutdown window as interrupted rather than errored or failed.
	Stopping <-chan struct{}

	// flightMu and inFlight coalesce concurrent cacheable executions of the
	// same job Key across runs on this Engine.
	//
	// Before `paths:` (fingerprintVersion = 3), two runs shared a job Key
	// only when they were for the same commit and branch, which
	// Service.Start already deduplicated at the plan level while in flight.
	// A job keyed on the files it reads (FingerprintIn) shares its Key
	// across commits and branches whenever those files are untouched -- a
	// docs-only commit cut while an earlier run's 47-minute `go` job is
	// still executing has a different planKey and the identical `go` Key.
	// Without this flight book, the faster sibling jobs that already
	// finished in the earlier run hit Cache while `go` misses and starts a
	// second sandbox from scratch.
	flightMu sync.Mutex
	inFlight map[workflows.Key]*jobFlight
}

// jobFlight is one cacheable job Key's in-flight execution on an Engine.
//
// done is closed after the driving job has written its verdict to Cache (or
// returned without one) and released its sandbox and gate permits, so a
// waiter waking from it either finds the cached entry ready to replay or
// finds the capacity free to run the job itself.
type jobFlight struct {
	done  chan struct{}
	runID string
}

type detachKey struct{}

// WithDetach returns a child context and a detach function. Calling detach
// cancels the returned context and marks it as detaching for a daemon restart
// or upgrade rather than cancelling the CI run, so Engine leaves running
// sandboxes and unconcluded jobs intact for the next daemon to readopt.
func WithDetach(parent context.Context) (context.Context, context.CancelFunc) {
	flag := &atomic.Bool{}
	ctx, cancel := context.WithCancel(context.WithValue(parent, detachKey{}, flag))
	return ctx, func() {
		flag.Store(true)
		cancel()
	}
}

func IsDetaching(ctx context.Context) bool {
	if flag, ok := ctx.Value(detachKey{}).(*atomic.Bool); ok && flag != nil {
		return flag.Load()
	}
	return false
}

func (e *Engine) daemonStopping() bool {
	if e == nil || e.Stopping == nil {
		return false
	}
	select {
	case <-e.Stopping:
		return true
	default:
		return false
	}
}

func (e *Engine) stopping(ctx context.Context) bool {
	return IsDetaching(ctx) || e.daemonStopping()
}

func (e *Engine) withStoppingCancel(parent context.Context) (context.Context, context.CancelFunc) {
	if e == nil || e.Stopping == nil {
		return context.WithCancel(parent)
	}
	if e.daemonStopping() {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-e.Stopping:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// AdoptedJob carries the pre-existing state and readopted sandbox for an
// in-flight CI job across a daemon restart.
type AdoptedJob struct {
	Job     workflows.JobRun
	Sandbox gritsteps.Sandbox
}

func (e *Engine) claimFlight(key workflows.Key, runID string) (flight *jobFlight, mine bool) {
	e.flightMu.Lock()
	defer e.flightMu.Unlock()
	if e.inFlight == nil {
		e.inFlight = make(map[workflows.Key]*jobFlight)
	}
	if existing, ok := e.inFlight[key]; ok {
		return existing, false
	}
	flight = &jobFlight{done: make(chan struct{}), runID: runID}
	e.inFlight[key] = flight
	return flight, true
}

func (e *Engine) releaseFlight(key workflows.Key, flight *jobFlight) {
	e.flightMu.Lock()
	if e.inFlight[key] == flight {
		delete(e.inFlight, key)
		close(flight.done)
	}
	e.flightMu.Unlock()
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// instant is e.now() as a result records one: the timestamps on a Run,
// a JobRun and a StepRun are pointers so that a time nobody has is
// absent rather than the year 1 (see the note above Run).
func (e *Engine) instant() *time.Time {
	t := e.now()
	return &t
}

func (e *Engine) log(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

func (e *Engine) cache() workflows.Cache {
	if e.Cache == nil {
		return noCache{}
	}
	return e.Cache
}

func (e *Engine) concurrency() int {
	if e.ConcurrencyLimit != nil {
		if n := e.ConcurrencyLimit(); n > 0 {
			return n
		}
	}
	if e.Concurrency > 0 {
		return e.Concurrency
	}
	return 0
}

// Run executes plan and reports what happened. It returns a Run rather
// than an error for everything that is a fact about the run --
// including "this engine has no sandboxes to run in", which lands as
// Run.Error -- because a caller's next move is the same either way:
// record it, show it, and let whoever reads it see which part failed.
//
// runID is what the result is filed under; it is the caller's to
// generate (Service does), since the caller is what has to hand it back
// before this returns.
//
// observe, when it is not nil, is called as a job gets its machine and
// again as that job finishes, from the scheduler's own goroutine and so
// never concurrently with itself. It is what makes a run readable while
// it is still going -- a run that only reports itself at the end is one
// nobody can watch -- and it is a parameter rather than a field on the
// Engine because an Engine is shared by every run a deployment does and
// this belongs to one of them.
//
// The progress calls carry a job with a sandbox and a start time but no
// conclusion, which is what a job in flight *is*. As its steps start and
// finish they carry those too, so a long job remains observable rather than
// looking idle until its final result arrives.
func (e *Engine) Run(ctx context.Context, runID string, plan workflows.Plan, observe func(workflows.JobRun)) workflows.Run {
	run := workflows.Run{
		ID:          runID,
		Key:         plan.Key,
		Repo:        plan.Request.Repo,
		Ref:         plan.Request.Ref,
		Commit:      plan.Request.Commit,
		Event:       plan.Request.Event,
		RequestedBy: plan.Request.RequestedBy,
		Recheck:     plan.Request.Recheck,
		StartedAt:   e.instant(),
	}
	if plan.Unmatched != "" {
		run.FinishedAt = e.instant()
		// Errored for ConclusionErrored's own reason: nothing of this
		// tree was executed, so this is not a verdict on the commit. It
		// is emphatically not a success -- a run that matched no workflow
		// concluded green is a pass nothing earned, and the merge queue
		// cannot tell it from a suite that ran (Plan.Unmatched).
		run.Conclusion = workflows.ConclusionErrored
		if plan.NothingToPublish {
			// Except a release no workflow answers: there was nothing to
			// publish and nothing failed, so it is not red either. It
			// gates nothing -- no merge waits on a release run.
			run.Conclusion = workflows.ConclusionSkipped
		}
		run.Error = plan.Unmatched
		return run
	}
	if e.Sandboxes == nil {
		run.FinishedAt = e.instant()
		// Errored rather than failure: nothing about this commit was
		// tried, so nothing about it failed. See ConclusionErrored.
		run.Conclusion = workflows.ConclusionErrored
		run.Error = "ci: this deployment has no sandbox backend wired for CI, so there is nowhere to run a job"
		return run
	}

	results := e.runJobs(ctx, runID, plan, observe)
	return e.finishRun(ctx, plan, run, results)
}

// Resume continues an unconcluded Run across a daemon restart, preserving
// already-finished jobs from run.Jobs, reattaching to in-flight jobs in
// adopted (keyed by PlannedJob.ID or Job.ID), and scheduling any remaining
// unstarted jobs.
func (e *Engine) Resume(ctx context.Context, run workflows.Run, plan workflows.Plan, adopted map[string]AdoptedJob, observe func(workflows.JobRun)) workflows.Run {
	if e.Sandboxes == nil {
		run.FinishedAt = e.instant()
		run.Conclusion = workflows.ConclusionErrored
		run.Error = "ci: this deployment has no sandbox backend wired for CI, so there is nowhere to run a job"
		return run
	}
	results := e.runJobsWithAdopted(ctx, run.ID, plan, run.Jobs, adopted, observe)
	return e.finishRun(ctx, plan, run, results)
}

func (e *Engine) finishRun(ctx context.Context, plan workflows.Plan, run workflows.Run, results map[string]workflows.JobRun) workflows.Run {
	hasDetachedStep := false
	allDone := len(plan.Jobs) > 0
	hasDaemonServiceFailure := false
	for _, pj := range plan.Jobs {
		jobRes, ok := results[pj.ID]
		if !ok || !jobRes.Conclusion.Done() {
			allDone = false
		}
		if ok && !jobRes.Conclusion.Done() && jobRes.StartedAt != nil && jobRes.Sandbox != "" {
			hasDetachedStep = true
		}
		if ok && jobRes.Conclusion != workflows.ConclusionSuccess && JobFailedOnDaemonService(jobRes) {
			hasDaemonServiceFailure = true
		}
	}
	if IsDetaching(ctx) && (!e.daemonStopping() || hasDetachedStep) {
		return run
	}
	if e.daemonStopping() && (!allDone || hasDaemonServiceFailure) {
		run.Jobs = nil
		now := e.instant()
		for _, pj := range plan.Jobs {
			jobRes, ok := results[pj.ID]
			if !ok || jobRes.Name == "" {
				jobRes = NewJobRun(pj)
			}
			if jobRes.Quarantined {
				run.Quarantined = true
			}
			if !jobRes.Conclusion.Done() || (jobRes.Conclusion != workflows.ConclusionSuccess && JobFailedOnDaemonService(jobRes)) {
				jobRes.Conclusion = workflows.ConclusionCancelled
				jobRes.Error = workflows.InterruptedRunError
				jobRes.Sandbox = ""
				if jobRes.StartedAt != nil && jobRes.FinishedAt == nil {
					jobRes.FinishedAt = now
				}
			}
			run.Jobs = append(run.Jobs, jobRes)
		}
		run.FinishedAt = now
		run.Conclusion = workflows.ConclusionCancelled
		run.Interrupted = true
		run.Error = workflows.InterruptedRunError
		return run
	}
	run.Jobs = nil
	for _, pj := range plan.Jobs {
		jobRes := results[pj.ID]
		if jobRes.Quarantined {
			run.Quarantined = true
		}
		run.Jobs = append(run.Jobs, jobRes)
	}
	run.FinishedAt = e.instant()
	run.Conclusion = ConcludeRun(run.Jobs)
	return run
}

// concludeRun folds every job's conclusion into the run's own, in the
// order a reader cares about: a cancelled run is cancelled whatever
// else happened in it, a failure anywhere is a failure, and a run whose
// jobs were all skipped is still a success -- there is nothing red
// about a repository that declares no workflow at all. A run whose
// repository declared workflows and matched none of them never reaches
// here: Run answers it above, from Plan.Unmatched.
//
// A failure outranks an errored job on purpose. A run in which one job
// tested the tree and did not pass while another never got a machine has
// found something wrong with the tree, and "errored" would send whoever
// reads it to the deployment instead; the job's own conclusion still
// says which of the two it was. A run is errored only when nothing in it
// found anything wrong with the commit, because that is the only case in
// which there is nothing about the commit to report.
func ConcludeRun(jobs []workflows.JobRun) workflows.Conclusion {
	conclusion := workflows.ConclusionSuccess
	for _, j := range jobs {
		switch j.Conclusion {
		case workflows.ConclusionCancelled:
			return workflows.ConclusionCancelled
		case workflows.ConclusionFailure:
			conclusion = workflows.ConclusionFailure
		case workflows.ConclusionErrored:
			if conclusion != workflows.ConclusionFailure {
				conclusion = workflows.ConclusionErrored
			}
		}
	}
	return conclusion
}

// runJobs is the scheduler: it starts every job whose `needs` have all
// succeeded, up to Concurrency at a time, and marks as skipped whatever
// is left once nothing can start any more.
//
// One goroutine per running job and one channel of completions, rather
// than a worker pool: the interesting bound is the number of sandboxes
// in flight, which a semaphore expresses exactly, and a pool would add
// a queue whose order is a second thing to reason about.
//
// A job counted here as running may be doing nothing but waiting for one
// of the deployment's own job sandboxes (Engine.Jobs), which is on
// purpose: a job that has been started and is queueing is still this
// run's business, where one this loop had refused to start would leave
// it with nothing running and nothing startable -- the state the guard
// below reports as "nothing this job needs could run", which would be a
// lie about a deployment that was merely busy.
func (e *Engine) runJobs(ctx context.Context, runID string, plan workflows.Plan, observe func(workflows.JobRun)) map[string]workflows.JobRun {
	return e.runJobsWithAdopted(ctx, runID, plan, nil, nil, observe)
}

func matchJobRun(pj workflows.PlannedJob, j workflows.JobRun) bool {
	if j.WorkflowPath != "" && pj.WorkflowPath != "" {
		return j.WorkflowPath == pj.WorkflowPath && j.JobID == pj.Job.ID
	}
	return j.JobID == pj.Job.ID || j.JobID == pj.ID
}

func lookupAdoptedJob(pj workflows.PlannedJob, adopted map[string]AdoptedJob) *AdoptedJob {
	if len(adopted) == 0 {
		return nil
	}
	if aj, ok := adopted[pj.ID]; ok {
		return &aj
	}
	if aj, ok := adopted[pj.Job.ID]; ok {
		return &aj
	}
	for _, aj := range adopted {
		if matchJobRun(pj, aj.Job) {
			copyAJ := aj
			return &copyAJ
		}
	}
	return nil
}

func (e *Engine) runJobsWithAdopted(ctx context.Context, runID string, plan workflows.Plan, prior []workflows.JobRun, adopted map[string]AdoptedJob, observe func(workflows.JobRun)) map[string]workflows.JobRun {
	report := func(result workflows.JobRun) {
		if observe != nil {
			observe(result)
		}
	}
	byID := make(map[string]workflows.PlannedJob, len(plan.Jobs))
	for _, pj := range plan.Jobs {
		byID[pj.ID] = pj
	}

	// event is one thing a job's own goroutine has to say: it has got a
	// machine and is working (inFlight), or it is done. Both come back
	// through this one channel rather than being reported from that
	// goroutine, so observe keeps the single-threaded contract Run's own
	// doc comment gives it -- two jobs can perfectly well get their
	// sandboxes in the same instant.
	type event struct {
		id       string
		result   workflows.JobRun
		inFlight bool
	}
	var (
		results = make(map[string]workflows.JobRun, len(plan.Jobs))
		started = make(map[string]bool, len(plan.Jobs))
		events  = make(chan event)
		running int
		limit   = e.concurrency()
		perRun  Gate
	)
	if limit > 0 {
		perRun = &runGate{limit: limit}
	}

	var finished atomic.Int64
	for _, pj := range plan.Jobs {
		for _, prev := range prior {
			if prev.Conclusion != "" && matchJobRun(pj, prev) {
				started[pj.ID] = true
				results[pj.ID] = prev
				finished.Add(1)
				break
			}
		}
	}

	// What this run's jobs are worth at the deployment's gate, asked
	// afresh on every attempt one of them makes: what the caller ranks the
	// run at, plus a step once this run has finished something.
	//
	// That second term is "finish what is started before starting more".
	// Four branches each needing a half-hour suite and sharing the
	// deployment fairly all finish at T; serialising them finishes the
	// first at T/4, and the first *finished* run is a merge where four
	// part-done ones are only progress (PriorityUnderway).
	//
	// An atomic rather than len(results), which belongs to this goroutine
	// alone while the closure is read from every job's.
	rank := func() workflows.Priority {
		// The request's own floor first (Request.Priority), which the
		// deployment's ranking can raise but never lower.
		p := plan.Request.Priority
		if e.Priority != nil {
			p = max(p, e.Priority(plan.Request))
		}
		if finished.Load() > 0 {
			p += workflows.PriorityUnderway
		}
		return p
	}

	// ready reports whether pj may start now, and whether it can never
	// start at all (a need that did not succeed).
	ready := func(pj workflows.PlannedJob) (start, dead bool) {
		for _, need := range pj.Needs {
			r, done := results[need]
			if !done {
				return false, false
			}
			if r.Conclusion != workflows.ConclusionSuccess {
				return false, true
			}
		}
		return true, false
	}

	for len(results) < len(plan.Jobs) {
		progress := false
		if !e.stopping(ctx) {
			for _, pj := range plan.Jobs {
				if started[pj.ID] {
					continue
				}
				aj := lookupAdoptedJob(pj, adopted)
				start, dead := ready(pj)
				if aj != nil {
					start, dead = true, false
				}
				switch {
				case dead:
					started[pj.ID] = true
					results[pj.ID] = skippedJob(pj, "a job it needs did not succeed")
					finished.Add(1)
					report(results[pj.ID])
					progress = true
				case start:
					started[pj.ID] = true
					running++
					progress = true
					go func(pj workflows.PlannedJob, aj *AdoptedJob) {
						result := e.runJob(ctx, runID, plan, pj, perRun, rank, aj, func(snapshot workflows.JobRun) {
							events <- event{id: pj.ID, result: snapshot, inFlight: true}
						})
						events <- event{id: pj.ID, result: result}
					}(pj, aj)
				}
			}
		}
		if running == 0 && !progress {
			if e.stopping(ctx) {
				break
			}
			// Nothing running and nothing startable, with jobs left over:
			// impossible, since a `needs` cycle is refused at parse and
			// every other reason to be unstartable is a finished
			// dependency. Recorded rather than deadlocked on, because a
			// scheduler that hangs is far worse to debug than one that
			// says what it could not do.
			for _, pj := range plan.Jobs {
				if !started[pj.ID] {
					results[pj.ID] = skippedJob(pj, "nothing this job needs could run")
					report(results[pj.ID])
				}
			}
			break
		}
		if running > 0 {
			ev := <-events
			if !e.stopping(ctx) || (!ev.inFlight && ev.result.Conclusion == "" && ev.result.StartedAt == nil) {
				report(ev.result)
			}
			// A progress snapshot is still a running job: it has no result
			// to file and nothing to free. The loop goes round again and
			// waits for the next thing any of them has to say.
			if ev.inFlight {
				continue
			}
			results[ev.id] = ev.result
			finished.Add(1)
			running--
		}
	}
	// Every goroutine still in flight has a send to make; nothing above
	// can leave the loop with one outstanding, since the loop only ends
	// once every job has a result and only a completed send produces
	// one.
	return results
}

func skippedJob(pj workflows.PlannedJob, why string) workflows.JobRun {
	result := NewJobRun(pj)
	result.Conclusion = workflows.ConclusionSkipped
	result.Error = why
	for _, step := range pj.Job.Steps {
		result.Steps = append(result.Steps, workflows.StepRun{Name: step.DisplayName(), Conclusion: workflows.ConclusionSkipped, ExitCode: -1})
	}
	return result
}

// neverRan records a job that lost a prerequisite before it ever got a
// machine -- its capability grants, this run's sandbox allowance, one of
// the deployment's CI slots. The conclusion and the reason are the
// caller's; what is the same every time is the shape of the record.
//
// It is finished and it never started, and the record says exactly
// that: FinishedAt is the instant this was decided and StartedAt stays
// nil, which is a thing a JobRun can say now that its timestamps are
// pointers (see the note above Run). Every reader of a duration already
// reads a job with no start as having none -- Run.Duration, cicmd's
// ciJobDuration, CIPage's spanOf -- where the older shape gave them a
// zero time.Time that `omitempty` did not omit and they subtracted from
// now: that is how a job which spent four minutes waiting for a gcp-key
// grant and then errored was drawn on the CI page as having taken
// 17756979h 39m.
//
// The steps go down as skipped for the same reason: whichever
// prerequisite it was, the honest answer is that every step of this job
// was passed over, and a reader scanning the run should not have to know
// which gate a job stopped at to recognise one that did no work.
func neverRan(result *workflows.JobRun, pj workflows.PlannedJob, queue *queueClock, at time.Time, conclusion workflows.Conclusion, why string) {
	result.StartedAt, result.FinishedAt = nil, &at
	// This is the job the whole queueing record exists for: it waited
	// and then it was cancelled, and without a wait written down a
	// reader cannot tell it from a job that ran for the same length of
	// time (grain/task-326). So the span is closed here rather than lost
	// with the Activity.
	result.Queued, result.QueuedCause, result.QueuedReason = queue.stop(at)
	// Whatever it was queueing for, it is not queueing any more: an
	// Activity left behind on a finished job reads as a wait still going
	// on. What it was waiting for is in Error and in QueuedReason now.
	result.Activity, result.ActivityAt = "", nil
	result.Conclusion = conclusion
	result.Error = why
	for _, step := range pj.Job.Steps {
		result.Steps = append(result.Steps, workflows.StepRun{Name: step.DisplayName(), Conclusion: workflows.ConclusionSkipped, ExitCode: -1})
	}
}

func NewJobRun(pj workflows.PlannedJob) workflows.JobRun {
	return workflows.JobRun{
		Workflow:     pj.Workflow,
		WorkflowPath: pj.WorkflowPath,
		JobID:        pj.Job.ID,
		Name:         pj.Job.DisplayName(),
		Key:          pj.Key,
	}
}

// runJob is one job end to end: the cache, then a permit, then a
// sandbox, then the steps, then the cache again.
//
// The cache comes before the permit deliberately: a job that is already
// answered needs no machine, and making it queue for one behind jobs
// that do would spend a deployment's scarcest thing on reading a file.
// A recheck skips the first of them, being a request for the work itself
// rather than for the answer, and keeps the second: what it learned is the
// newest verdict for the key and replaces the one it re-ran -- see
// Request.Recheck.
//
// progress, when it is not nil, is called with this job as it stands when
// it gets a machine, when each step starts, and when each step finishes, so
// that whoever is watching the run can see what the sandbox is doing rather
// than only the record of the job after it has finished.
// A job answered from the cache, or one that never gets a machine at
// all, never calls it: there is no sandbox to say anything about.
func (e *Engine) runJob(ctx context.Context, runID string, plan workflows.Plan, pj workflows.PlannedJob, perRun Gate,
	rank func() workflows.Priority, adopted *AdoptedJob, progress func(workflows.JobRun)) workflows.JobRun {

	// The wait starts here, because here is where this job became
	// startable: every `needs` it has has succeeded, the scheduler has
	// given it a goroutine, and everything between now and its StartedAt
	// is time it spent queueing rather than working (JobRun.Queued).
	queue := newQueueClock(e.now())

	// What this job needs and this deployment does not give this
	// repository, answered before anything at all is spent -- before the
	// cache, before the guest warm, before the grant admission and both
	// sandbox gates (requirements.go).
	//
	// Before the cache in particular, and deliberately: a run in which
	// the credential is missing is not a run whose question has already
	// been answered, and replaying a green from a run that had the
	// credential would report success for work that cannot be done here
	// now. Nothing is written back either, so the moment the grant
	// arrives the job runs rather than replaying this.
	if adopted == nil {
		if unmet := workflows.UnmetRequirements(pj.Job, plan.Request); len(unmet) > 0 {
			result := NewJobRun(pj)
			// Errored rather than failed, for the reason a sandbox that
			// could not be built is: this job never ran a line of the tree,
			// so a red verdict on the commit would be a lie about what went
			// wrong. What went wrong is this deployment's configuration for
			// this repository, and the sentence says which name is missing.
			neverRan(&result, pj, queue, e.now(), conclusionFor(ctx, workflows.ConclusionErrored), workflows.UnmetRequirementsError(unmet))
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}
	}

	result := NewJobRun(pj)
	if adopted != nil {
		result = adopted.Job
		result.Workflow = pj.Workflow
		result.WorkflowPath = pj.WorkflowPath
		result.JobID = pj.Job.ID
		result.Name = pj.Job.DisplayName()
		result.Key = pj.Key
	}
	// What this job is queueing for, said as it changes: the in-flight
	// coalescing below, the capability admission, and both sandbox gates
	// after it report through the same callback, so "held for memory" and
	// "held for a grant" reach whoever is watching in the same place and
	// the same way.
	//
	// It is also where the wait is measured: the clock is told what is
	// holding this job at the same moment whoever is watching is, so the
	// record afterwards (JobRun.Queued) and the live line (Activity) can
	// never disagree about what the job was doing.
	waiting := func(w workflows.Wait) {
		queue.waiting(e.now(), w)
		result.Activity, result.ActivityAt = w.Reason, e.instant()
		if w.Reason == "" {
			result.ActivityAt = nil
		}
		if progress != nil {
			progress(jobRunSnapshot(result))
		}
	}

	// A recheck re-runs what it named and reuses a dependency's cached
	// pass (Request.Recheck); a dependency whose entry is anything but a
	// pass runs, since what waits behind it could not run otherwise.
	dependencyOfRecheck := plan.Request.Recheck && pj.DependencyOnly
	useCache := e.Cache != nil && pj.Job.Cache && (!plan.Request.Recheck || dependencyOfRecheck)
	queueCtx, stopQueue := e.withStoppingCancel(ctx)
	defer stopQueue()
	if useCache {
		if adopted != nil {
			if flight, mine := e.claimFlight(pj.Key, runID); mine {
				defer e.releaseFlight(pj.Key, flight)
			}
		} else {
			if hit, ok := e.lookupCache(ctx, runID, plan, pj, dependencyOfRecheck); ok {
				return hit
			}
			for {
				flight, mine := e.claimFlight(pj.Key, runID)
				if mine {
					// Registered before Admission, perRun, e.Jobs and
					// releaseSandbox so LIFO order runs this last: e.cache().Save
					// and every resource release complete before flight.done
					// wakes any run waiting on this Key.
					defer e.releaseFlight(pj.Key, flight)
					if hit, ok := e.lookupCache(ctx, runID, plan, pj, dependencyOfRecheck); ok {
						return hit
					}
					break
				}
				waiting(workflows.Wait{
					Reason: fmt.Sprintf("for identical %s job in flight in run %s", pj.Job.DisplayName(), flight.runID),
				})
				select {
				case <-flight.done:
					waiting(workflows.Wait{})
					if hit, ok := e.lookupCache(ctx, runID, plan, pj, dependencyOfRecheck); ok {
						return hit
					}
				case <-queueCtx.Done():
					if e.stopping(ctx) {
						return NewJobRun(pj)
					}
					neverRan(&result, pj, queue, e.now(), conclusionFor(ctx, workflows.ConclusionErrored),
						fmt.Sprintf("waiting for identical %s job in flight in run %s: %v", pj.Job.DisplayName(), flight.runID, ctx.Err()))
					e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
					return result
				}
			}
		}
	}

	request := gritsteps.SandboxRequest{
		// What the deployment provides for this repository, narrowed to
		// what this job said it needs -- and the whole of it for a job
		// that said nothing (jobCapabilities). The narrowing is not just
		// tidiness: this same list is what Admission reserves finite
		// credentials from, so a job that does not use the scarce one no
		// longer queues for it.
		Capabilities:    workflows.JobCapabilities(pj.Job, plan.Request.Capabilities, e.ExplicitOnlyCapability),
		ReleaseSecrets:  workflows.JobSecrets(pj.Job, plan.Request.ReleaseSecrets),
		Event:           plan.Request.Event,
		TrustedSchedule: plan.Request.TrustedSchedule,
		Name:            SandboxName(runID, plan, pj),
		RunID:           runID,
		JobAttempt:      jobIndex(plan, pj),
		Untrusted:       plan.Request.UntrustedCommit() != "",
		Repo:            plan.Request.Repo,
		Ref:             plan.Request.Ref,
		Commit:          plan.Request.Commit,
		Shape:           pj.Job.Sandbox,
		Builtin:         pj.Job.Builtin,
	}
	if adopted == nil && e.GuestCache != nil && pj.GuestKey != "" {
		warmCtx, cancel := context.WithTimeout(queueCtx, pj.Job.Timeout())
		err := e.GuestCache.Warm(warmCtx, gritcache.GuestCacheRequest{
			RunID: runID, Repo: plan.Request.Repo, Ref: plan.Request.Ref,
			Commit: plan.Request.Commit, Key: pj.GuestKey, Job: pj.ID,
		}, waiting)
		cancel()
		if e.stopping(ctx) {
			return NewJobRun(pj)
		}
		if err != nil {
			e.log("ci: run %s: job %s: the sandbox guest it resolves was not warmed (%v): "+
				"the job runs and resolves its own", runID, pj.ID, err)
		}
		if result.Activity != "" {
			waiting(workflows.Wait{})
		}
	}

	var keepLeaseOnDetach bool
	if adopted == nil && e.Admission != nil {
		release, err := e.Admission.Acquire(queueCtx, request, waiting)
		if err != nil {
			if e.stopping(ctx) {
				return NewJobRun(pj)
			}
			neverRan(&result, pj, queue, e.now(), conclusionFor(ctx, workflows.ConclusionErrored),
				fmt.Sprintf("waiting for this job's capability grants: %v", err))
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}
		defer func() {
			if !keepLeaseOnDetach {
				release()
			}
		}()
		if result.Activity != "" {
			waiting(workflows.Wait{})
		}
	}

	if perRun != nil {
		release, err := perRun.Acquire(queueCtx, GateRequest{Wait: waiting})
		if err != nil {
			if e.stopping(ctx) {
				return NewJobRun(pj)
			}
			neverRan(&result, pj, queue, e.now(), conclusionFor(ctx, workflows.ConclusionErrored),
				fmt.Sprintf("waiting for this run's CI sandbox capacity: %v", err))
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}
		defer release()
	}

	if e.Jobs != nil {
		// The job's own shape goes with the request: this gate is the
		// deployment's, and what it rations is the machine as well as
		// the count (grain/task-173).
		var jobID string
		release, err := e.Jobs.Acquire(queueCtx, GateRequest{
			Want: workflows.SandboxCommitment(pj.Job.Sandbox), Priority: rank, Wait: waiting,
			Adopted: adopted != nil,
			Assigned: func(id string) {
				jobID = id
				if e.JobQueued != nil {
					e.JobQueued(id, runID)
				}
			},
		})
		// Whatever Acquire answered, this job is no longer something
		// Stuck might be asked about: it has either got a machine, which
		// means it is no longer queueing, or it never will. Forgotten
		// immediately rather than left for some later sweep, so the
		// registry JobQueued and JobDequeued back onto never holds more
		// than this engine's own queueing jobs at once.
		if e.JobDequeued != nil && jobID != "" {
			e.JobDequeued(jobID)
		}
		if err != nil {
			if e.stopping(ctx) {
				return NewJobRun(pj)
			}
			// Only ctx's own error: the run was cancelled, or it hit
			// Service.MaxRunTime, while this job was still queueing for
			// a sandbox. It never ran, and saying so beats a "failed"
			// nobody could act on.
			neverRan(&result, pj, queue, e.now(), conclusionFor(ctx, workflows.ConclusionErrored),
				fmt.Sprintf("waiting for one of this deployment's CI sandboxes: %v", err))
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}
		defer release()
		if result.Activity != "" {
			waiting(workflows.Wait{})
		}
	}

	if adopted == nil && useCache {
		if hit, ok := e.lookupCache(ctx, runID, plan, pj, dependencyOfRecheck); ok {
			return hit
		}
	}

	var (
		sandbox gritsteps.Sandbox
		jobCtx  context.Context
		cancel  context.CancelFunc
	)
	if adopted != nil {
		if result.StartedAt == nil {
			result.StartedAt = e.instant()
		}
		elapsed := e.now().Sub(*result.StartedAt)
		remaining := pj.Job.Timeout() - elapsed
		if remaining <= 0 {
			remaining = time.Second
		}
		jobCtx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
		sandbox = adopted.Sandbox
		result.Sandbox = sandbox.Name()
		e.log("ci: run %s: job %s: resumed in sandbox %s", runID, pj.ID, sandbox.Name())
	} else {
		if e.stopping(ctx) {
			return NewJobRun(pj)
		}
		// After the permit, so that a job made to queue reads as having
		// started when it got a machine rather than when it got in line --
		// and so that its own timeout (below) is the time it had to work in,
		// not the time it spent waiting.
		result.StartedAt = e.instant()
		// And the wait it did instead, closed against the same instant: what
		// is above this line is queueing and what is below it is work, which
		// is the whole distinction "18m queued, 4m running" is there to draw.
		result.Queued, result.QueuedCause, result.QueuedReason = queue.stop(*result.StartedAt)
		e.log("ci: run %s: job %s: starting", runID, pj.ID)

		jobCtx, cancel = context.WithTimeout(ctx, pj.Job.Timeout())
		defer cancel()

		var err error
		acquireCtx, cancelAcquire := e.withStoppingCancel(jobCtx)
		sandbox, err = e.Sandboxes.Acquire(acquireCtx, request)
		cancelAcquire()
		if err != nil {
			if e.stopping(ctx) {
				return NewJobRun(pj)
			}
			// Errored, not failure: this job never ran a line of the tree,
			// so what went wrong is the deployment's and a red verdict on
			// the commit would be a lie about it (ConclusionErrored).
			result.FinishedAt = e.instant()
			result.Conclusion = conclusionFor(jobCtx, workflows.ConclusionErrored)
			result.Error = fmt.Sprintf("building a sandbox for this job: %v", err)
			for _, step := range pj.Job.Steps {
				result.Steps = append(result.Steps, workflows.StepRun{Name: step.DisplayName(), Conclusion: workflows.ConclusionSkipped, ExitCode: -1})
			}
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}
		result.Sandbox = sandbox.Name()
		if progress != nil && !e.stopping(ctx) {
			progress(jobRunSnapshot(result))
		}
	}

	releaseSandbox := func(sb gritsteps.Sandbox) {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxReleaseTimeout)
		defer releaseCancel()
		if err := sb.Release(releaseCtx); err != nil {
			e.log("ci: run %s: job %s: releasing sandbox %s: %v", runID, pj.ID, sb.Name(), err)
		}
	}
	activeSandbox := sandbox
	defer func() {
		if activeSandbox != nil {
			releaseSandbox(activeSandbox)
		}
	}()
	if adopted == nil && e.stopping(ctx) {
		return NewJobRun(pj)
	}

	// Run steps in initial (or readopted) sandbox
	initialAttempt := len(result.Attempts) + 1
	attempt1StartedAt := result.StartedAt
	e.runSteps(ctx, newJobClock(jobCtx, pj.Job), plan, pj, sandbox, initialAttempt, &result, progress)
	if IsDetaching(ctx) && result.Conclusion == "" {
		activeSandbox = nil
		keepLeaseOnDetach = true
		return result
	}
	if result.Conclusion != workflows.ConclusionSuccess && e.stopping(ctx) {
		return NewJobRun(pj)
	}
	e.collectGoTestLog(jobCtx, runID, pj, sandbox, &result)

	// A first attempt whose sandbox the kernel OOM-killed is retried
	// whatever the job declares: under memory overcommit that is the
	// deployment's doing, not the tree's (docs/design/auto-overcommit.md).
	oomHow, oomKilled := e.sandboxOOM(ctx, runID, pj, sandbox, result)
	// So is one whose sandbox the platform took away mid-job -- a pod
	// preempted for an interactive session's, on a full cluster: the
	// tree did nothing to earn that either.
	var preemptHow string
	var preempted bool
	if !oomKilled {
		preemptHow, preempted = e.sandboxPreempted(ctx, runID, pj, sandbox, result)
	}
	lost := oomKilled || preempted

	if result.Conclusion != workflows.ConclusionSuccess && e.stopping(ctx) {
		return NewJobRun(pj)
	}

	// Check if this job failed on attempt 1 and is eligible for automatic retry
	if initialAttempt == 1 && (lost || IsJobRetryEligible(pj.Job, result, 0)) && ctx.Err() == nil && !e.stopping(ctx) {
		firstFailureSummary := SummarizeFailure(result)
		if oomKilled {
			firstFailureSummary = OOMRetryReasonPrefix + oomHow
		} else if preempted {
			firstFailureSummary = PreemptedRetryReasonPrefix + preemptHow
		}
		e.log("ci: run %s: job %s: initial attempt failed (%s), automatically retrying once in fresh sandbox",
			runID, pj.ID, firstFailureSummary)

		e.collectArtifacts(jobCtx, runID, pj, sandbox, &result)
		attempt1FinishedAt := e.instant()
		result.Attempts = append(result.Attempts, workflows.JobAttempt{
			Attempt:        1,
			Sandbox:        result.Sandbox,
			StartedAt:      attempt1StartedAt,
			FinishedAt:     attempt1FinishedAt,
			Conclusion:     result.Conclusion,
			Error:          result.Error,
			FailureSummary: result.FailureSummary,
			Steps:          append([]workflows.StepRun(nil), result.Steps...),
			Artifacts:      append([]workflows.Artifact(nil), result.Artifacts...),
			GoTestLog:      result.GoTestLog,
		})

		// Release initial sandbox before acquiring a fresh one
		activeSandbox = nil
		releaseSandbox(sandbox)
		if e.stopping(ctx) {
			return NewJobRun(pj)
		}

		// Acquire fresh sandbox for retry attempt with a fresh job timeout budget
		attempt2StartedAt := e.instant()
		retryCtx, retryCancel := context.WithTimeout(ctx, pj.Job.Timeout())
		defer retryCancel()
		retryAcquireCtx, cancelRetryAcquire := e.withStoppingCancel(retryCtx)
		sandbox2, err2 := e.Sandboxes.Acquire(retryAcquireCtx, request)
		cancelRetryAcquire()
		if err2 != nil {
			if e.stopping(ctx) {
				return NewJobRun(pj)
			}
			result.FinishedAt = e.instant()
			result.Retried = true
			result.RetryCount = 1
			result.RetryReason = firstFailureSummary
			result.Conclusion = conclusionFor(retryCtx, workflows.ConclusionErrored)
			result.Error = fmt.Sprintf("building retry sandbox for this job: %v", err2)
			result.Attempts = append(result.Attempts, workflows.JobAttempt{
				Attempt:    2,
				StartedAt:  attempt2StartedAt,
				FinishedAt: result.FinishedAt,
				Conclusion: result.Conclusion,
				Error:      result.Error,
			})
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
			return result
		}

		activeSandbox = sandbox2
		if e.stopping(ctx) {
			return NewJobRun(pj)
		}
		result.Sandbox = sandbox2.Name()
		// The job is running again, so it must not read as finished: a
		// conclusion left over from the first attempt makes every reader
		// (the CI page, wait_for_checks, the merge queue) draw a job that
		// is still working as one that is done. Retried and RetryReason go
		// out with the same snapshot so the retry is visible as one.
		result.Steps = nil
		result.Artifacts = nil
		result.GoTestLog = ""
		result.Conclusion = ""
		result.FinishedAt = nil
		result.Error = ""
		result.FailureSummary = nil
		result.Retried = true
		result.RetryCount = 1
		result.RetryReason = firstFailureSummary
		if progress != nil && !e.stopping(ctx) {
			progress(jobRunSnapshot(result))
		}

		e.runSteps(ctx, newJobClock(retryCtx, pj.Job), plan, pj, sandbox2, 2, &result, progress)
		if IsDetaching(ctx) && result.Conclusion == "" {
			activeSandbox = nil
			keepLeaseOnDetach = true
			return result
		}
		if result.Conclusion != workflows.ConclusionSuccess && e.stopping(ctx) {
			return NewJobRun(pj)
		}
		e.collectGoTestLog(retryCtx, runID, pj, sandbox2, &result)

		secondHow, secondOOM := e.sandboxOOM(ctx, runID, pj, sandbox2, result)
		var secondPreemptHow string
		var secondPreempted bool
		if !secondOOM {
			secondPreemptHow, secondPreempted = e.sandboxPreempted(ctx, runID, pj, sandbox2, result)
		}
		switch {
		case oomKilled && result.Conclusion == workflows.ConclusionSuccess:
			// Infrastructure, not a flake: nothing about the tree
			// failed the first time, so nothing is recorded against it.
			result.Status = "passed (retry after OOM kill)"
			result.Warning = "infrastructure: " + firstFailureSummary + "; passed in a fresh sandbox"
			e.log("ci: run %s: job %s: passed on retry after its sandbox was OOM-killed", runID, pj.ID)
		case preempted && result.Conclusion == workflows.ConclusionSuccess:
			// The same: the first attempt lost its machine, not a test.
			result.Status = "passed (retry after preemption)"
			result.Warning = "infrastructure: " + firstFailureSummary + "; passed in a fresh sandbox"
			e.log("ci: run %s: job %s: passed on retry after its sandbox was preempted", runID, pj.ID)
		case secondPreempted:
			// Taken away again: errored, which the CI layer re-runs
			// (ciReplacementFor) rather than reading as a red verdict.
			result.Conclusion = conclusionFor(jobCtx, workflows.ConclusionErrored)
			result.Status = "errored (preempted)"
			result.Warning = "infrastructure: " + firstFailureSummary
			result.Error = PreemptedRetryReasonPrefix + secondPreemptHow + " (on retry, too)"
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
		case secondOOM:
			// Killed again: still the machine's answer rather than the
			// tree's, so errored rather than a red verdict.
			result.Conclusion = conclusionFor(retryCtx, workflows.ConclusionErrored)
			result.Status = "errored (OOM-killed)"
			result.Warning = "infrastructure: " + firstFailureSummary
			result.Error = OOMRetryReasonPrefix + secondHow + " (on retry, too)"
			e.log("ci: run %s: job %s: %s", runID, pj.ID, result.Error)
		case result.Conclusion == workflows.ConclusionSuccess:
			result.Flake = true
			result.Status = "passed (retry)"
			result.Warning = fmt.Sprintf("passed (retry): flaky failure on initial attempt: %s", firstFailureSummary)
			e.log("ci: run %s: job %s: passed on retry (flake: %s)", runID, pj.ID, firstFailureSummary)
		default:
			e.log("ci: run %s: job %s: retry failed with definitive failure (%s)", runID, pj.ID, SummarizeFailure(result))
		}
		e.collectArtifacts(retryCtx, runID, pj, sandbox2, &result)
		result.FinishedAt = e.instant()
		result.Attempts = append(result.Attempts, workflows.JobAttempt{
			Attempt:        2,
			Sandbox:        result.Sandbox,
			StartedAt:      attempt2StartedAt,
			FinishedAt:     result.FinishedAt,
			Conclusion:     result.Conclusion,
			Error:          result.Error,
			FailureSummary: result.FailureSummary,
			Steps:          append([]workflows.StepRun(nil), result.Steps...),
			Artifacts:      append([]workflows.Artifact(nil), result.Artifacts...),
			GoTestLog:      result.GoTestLog,
		})
	} else {
		if initialAttempt > 1 {
			firstFailureSummary := result.RetryReason
			switch {
			case strings.HasPrefix(firstFailureSummary, OOMRetryReasonPrefix) && result.Conclusion == workflows.ConclusionSuccess:
				result.Status = "passed (retry after OOM kill)"
				result.Warning = "infrastructure: " + firstFailureSummary + "; passed in a fresh sandbox"
			case strings.HasPrefix(firstFailureSummary, PreemptedRetryReasonPrefix) && result.Conclusion == workflows.ConclusionSuccess:
				result.Status = "passed (retry after preemption)"
				result.Warning = "infrastructure: " + firstFailureSummary + "; passed in a fresh sandbox"
			case preempted:
				result.Conclusion = conclusionFor(jobCtx, workflows.ConclusionErrored)
				result.Status = "errored (preempted)"
				result.Warning = "infrastructure: " + firstFailureSummary
				result.Error = PreemptedRetryReasonPrefix + preemptHow + " (on retry, too)"
			case oomKilled:
				result.Conclusion = conclusionFor(jobCtx, workflows.ConclusionErrored)
				result.Status = "errored (OOM-killed)"
				result.Warning = "infrastructure: " + firstFailureSummary
				result.Error = OOMRetryReasonPrefix + oomHow + " (on retry, too)"
			case result.Conclusion == workflows.ConclusionSuccess:
				result.Flake = true
				result.Status = "passed (retry)"
				result.Warning = fmt.Sprintf("passed (retry): flaky failure on initial attempt: %s", firstFailureSummary)
			}
		}
		e.collectArtifacts(jobCtx, runID, pj, sandbox, &result)
		result.FinishedAt = e.instant()
		result.Attempts = append(result.Attempts, workflows.JobAttempt{
			Attempt:        initialAttempt,
			Sandbox:        result.Sandbox,
			StartedAt:      attempt1StartedAt,
			FinishedAt:     result.FinishedAt,
			Conclusion:     result.Conclusion,
			Error:          result.Error,
			FailureSummary: result.FailureSummary,
			Steps:          append([]workflows.StepRun(nil), result.Steps...),
			Artifacts:      append([]workflows.Artifact(nil), result.Artifacts...),
			GoTestLog:      result.GoTestLog,
		})
	}
	if result.Conclusion != workflows.ConclusionSuccess && e.stopping(ctx) {
		return NewJobRun(pj)
	}
	// Both are set: this is the path where the job got a machine.
	e.log("ci: run %s: job %s: %s in %s", runID, pj.ID, result.Conclusion,
		result.FinishedAt.Sub(*result.StartedAt).Round(time.Second))

	if plan.Request.BaseCommit != "" {
		result.BaseCommit = plan.Request.BaseCommit
	} else if pj.BaseCommit != "" {
		result.BaseCommit = pj.BaseCommit
	}

	if pj.Job.Cache && workflows.Cacheable(result) {
		if err := e.cache().Save(ctx, pj.Key, result); err != nil {
			e.log("ci: run %s: job %s: saving to the cache: %v", runID, pj.ID, err)
		}
	}
	return result
}

// OOMRetryReasonPrefix starts a job's RetryReason (and Error, Warning)
// when its sandbox was OOM-killed, so a reader can tell an infrastructure
// retry from a flaky one.
const OOMRetryReasonPrefix = "sandbox was OOM-killed: "

// PreemptedRetryReasonPrefix starts a job's RetryReason (and Error,
// Warning) when its sandbox was taken away mid-job -- preempted by the
// scheduler for a higher-priority pod, evicted, or gone -- so a reader can
// tell an infrastructure retry from a flaky one.
const PreemptedRetryReasonPrefix = "sandbox was preempted: "

// PreemptionReporter is implemented by a Sandbox the platform can take
// away while a job runs in it -- orchestrator.SandboxPreemptionReporter,
// forwarded by the daemon's CI sandbox. Optional, like OOMReporter.
type PreemptionReporter interface {
	SandboxPreempted(ctx context.Context) (detail string, preempted bool)
}

// sandboxPreempted asks sb, after an attempt that did not pass, whether
// it was taken away from under the job.
func (e *Engine) sandboxPreempted(ctx context.Context, runID string, pj workflows.PlannedJob, sb gritsteps.Sandbox, result workflows.JobRun) (string, bool) {
	if result.Conclusion != workflows.ConclusionFailure && result.Conclusion != workflows.ConclusionErrored {
		return "", false
	}
	reporter, ok := sb.(PreemptionReporter)
	if !ok {
		return "", false
	}
	how, preempted := reporter.SandboxPreempted(ctx)
	if !preempted {
		return "", false
	}
	e.log("ci: run %s: job %s: %s%s", runID, pj.ID, PreemptedRetryReasonPrefix, how)
	return how, true
}

// OOMReporter is implemented by a Sandbox that can say whether the kernel
// OOM-killed it -- orchestrator.SandboxOOMReporter, forwarded by the
// daemon's CI sandbox. Optional, like StreamingSandbox.
type OOMReporter interface {
	SandboxOOM(ctx context.Context) (detail string, killed bool)
}

// sandboxOOM asks sb, after an attempt that did not pass, whether it was
// OOM-killed, and tells NoteOOM when it was.
func (e *Engine) sandboxOOM(ctx context.Context, runID string, pj workflows.PlannedJob, sb gritsteps.Sandbox, result workflows.JobRun) (string, bool) {
	if result.Conclusion != workflows.ConclusionFailure && result.Conclusion != workflows.ConclusionErrored {
		return "", false
	}
	reporter, ok := sb.(OOMReporter)
	if !ok {
		return "", false
	}
	how, killed := reporter.SandboxOOM(ctx)
	if !killed {
		return "", false
	}
	e.log("ci: run %s: job %s: %s%s", runID, pj.ID, OOMRetryReasonPrefix, how)
	if e.NoteOOM != nil {
		e.NoteOOM(e.now())
	}
	return how, true
}

func (e *Engine) lookupCache(ctx context.Context, runID string, plan workflows.Plan, pj workflows.PlannedJob, dependencyOfRecheck bool) (workflows.JobRun, bool) {
	hit, ok, err := e.cache().Lookup(ctx, pj.Key)
	if err != nil {
		// A broken cache must never fail a build -- see Cache's contract.
		e.log("ci: run %s: job %s: cache lookup failed, running it: %v", runID, pj.ID, err)
		return workflows.JobRun{}, false
	}
	if !ok {
		return workflows.JobRun{}, false
	}
	if dependencyOfRecheck && hit.Conclusion != workflows.ConclusionSuccess {
		e.log("ci: run %s: job %s: a dependency of the recheck, cached as %s, so running it", runID, pj.ID, hit.Conclusion)
		return workflows.JobRun{}, false
	}
	if hit.Conclusion == workflows.ConclusionFailure && plan.Request.BaseCommit != "" && hit.BaseCommit != "" && hit.BaseCommit != plan.Request.BaseCommit {
		e.log("ci: run %s: job %s: cached failure tied to base commit %s invalidated (base advanced to %s)",
			runID, pj.ID, hit.BaseCommit, plan.Request.BaseCommit)
		if dc, ok := e.cache().(*gritcache.DirCache); ok {
			dc.Discard(pj.Key, fmt.Sprintf("base commit advanced from %s to %s", hit.BaseCommit, plan.Request.BaseCommit))
		}
		return workflows.JobRun{}, false
	}
	e.log("ci: run %s: job %s: %s from cache", runID, pj.ID, hit.Conclusion)
	hit.Cached = true
	hit.Key = pj.Key
	hit.Workflow, hit.WorkflowPath = pj.Workflow, pj.WorkflowPath
	hit.JobID, hit.Name = pj.Job.ID, pj.Job.DisplayName()
	// This job got no sandbox, whatever machine the run behind the
	// entry used (JobRun.Sandbox). The times stay as they were: they
	// say how long this work took when it was done, which is the
	// honest answer to "why was that so quick" next to Cached.
	hit.Sandbox = ""
	// The queueing does not stay, though, which is the difference
	// between the two. A duration says what the work costs and is
	// the earlier run's to report; a wait says what this
	// deployment did to this job, and this job waited for
	// nothing -- carrying the old run's half-hour forward would
	// put a queue on a deployment that had none.
	hit.Queued, hit.QueuedCause, hit.QueuedReason = 0, "", ""
	return hit, true
}

// jobRunSnapshot breaks the Steps slice away from a job that is still being
// mutated. Progress crosses a channel to an observer which may persist it
// while the job starts its next step; sharing the backing array would make a
// live status update race with the work it describes.
func jobRunSnapshot(job workflows.JobRun) workflows.JobRun {
	job.Steps = append([]workflows.StepRun(nil), job.Steps...)
	if len(job.Attempts) > 0 {
		attempts := make([]workflows.JobAttempt, len(job.Attempts))
		for i, a := range job.Attempts {
			a.Steps = append([]workflows.StepRun(nil), a.Steps...)
			a.Artifacts = append([]workflows.Artifact(nil), a.Artifacts...)
			attempts[i] = a
		}
		job.Attempts = attempts
	}
	return job
}

// sandboxReleaseTimeout bounds the teardown of a job's sandbox. Long
// enough for a VM delete, short enough that a backend that has stopped
// answering does not hold a run open.
const sandboxReleaseTimeout = 2 * time.Minute

// runSteps runs pj's steps in order into result, stopping at the first
// failure that is not continue-on-error.
//
// ctx is the run's, not the job's: what ends it is the run being
// cancelled or the daemon going down. The job's own `timeout-minutes`
// lives in clock, because one kind of step is deliberately not measured
// against it (jobClock, Step.Infrastructure).
//
// The job's conclusion is decided at the bottom of this function, and it
// is the one place in grain where "the tree came back wrong" and "we
// never found out" are told apart for a job that actually ran. The rule,
// stated where it is decided: a step that does not test the tree cannot
// produce a verdict about the tree. Everything a cache miss causes --
// the miss, the slow rebuild it forced, the registry that would not
// answer, the credential the warm did not have -- is an infrastructure
// condition, and an infrastructure condition is ConclusionErrored.
func (e *Engine) runSteps(ctx context.Context, clock *jobClock, plan workflows.Plan, pj workflows.PlannedJob, sandbox gritsteps.Sandbox, attempt int, result *workflows.JobRun, progress func(workflows.JobRun)) {
	var (
		failed bool
		// cancelled is the run ending under this job, or the job spending
		// its own bound -- overBound says which, because only one of the
		// two is worth a sentence on the result.
		cancelled bool
		overBound bool
		// environment is the infrastructure step that did not succeed, if
		// one did not: the job errored rather than failed, and this is the
		// step a reader is to be sent to.
		environment    string
		environmentWhy string
	)
	for i, step := range pj.Job.Steps {
		if e.stopping(ctx) {
			return
		}
		if i < len(result.Steps) && result.Steps[i].Conclusion != "" {
			if result.Steps[i].Conclusion != workflows.ConclusionSuccess && !step.ContinueOnError {
				failed = true
			}
			continue
		}
		if failed || ctx.Err() != nil {
			if e.stopping(ctx) {
				return
			}
			conclusion := workflows.ConclusionSkipped
			if ctx.Err() != nil {
				conclusion = conclusionFor(ctx, workflows.ConclusionSkipped)
			}
			result.Steps = append(result.Steps, workflows.StepRun{Name: step.DisplayName(), Conclusion: conclusion, ExitCode: -1})
			if progress != nil {
				progress(jobRunSnapshot(*result))
			}
			continue
		}

		var record workflows.StepRun
		if i < len(result.Steps) {
			record = result.Steps[i]
			record.Name = step.DisplayName()
			if record.StartedAt == nil {
				record.StartedAt = e.instant()
			}
			record.ExitCode = -1
			result.Steps[i] = record
			result.Steps = result.Steps[:i+1]
		} else {
			record = workflows.StepRun{Name: step.DisplayName(), StartedAt: e.instant(), ExitCode: -1}
			result.Steps = append(result.Steps, record)
			if progress != nil {
				progress(jobRunSnapshot(*result))
			}
		}
		// The bound this step runs under: what is left of the job's, or --
		// for a step that is fetching the job's environment rather than
		// testing the tree -- one of its own, whose time the clock hands
		// back to the job afterwards.
		stepCtx, spent := clock.bound(ctx, pj.Job, step)
		started := e.now()
		out, err := e.execStep(stepCtx, sandbox, gritsteps.Command{
			Script:  step.Run,
			Dir:     step.WorkingDirectory,
			Env:     stepEnv(plan, pj, step),
			Timeout: stepTimeout(stepCtx, step),
			StepID:  gritsteps.StepIDFor(attempt, i),
		}, jobRunSnapshot(*result), progress)
		// Read before the bound is released, since releasing it cancels
		// the context and would make every expiry look the same. Also
		// check the wall-clock deadline directly in case the guest-side
		// timeout exits on the boundary just before the runtime timer
		// marks stepCtx.Err().
		expired := errors.Is(stepCtx.Err(), context.DeadlineExceeded)
		if !expired && ctx.Err() == nil {
			if deadline, ok := stepCtx.Deadline(); ok && !time.Now().Before(deadline) {
				expired = true
			}
		}
		spent()
		if IsDetaching(ctx) && err != nil && !IsDaemonServiceError(out.Output) && !IsDaemonServiceError(err.Error()) {
			return
		}
		if e.stopping(ctx) && (err != nil || out.ExitCode != 0) && !step.ContinueOnError {
			result.Conclusion = workflows.ConclusionCancelled
			return
		}
		// A step's own `timeout-minutes` is enforced by the sandbox through
		// Command.Timeout, not by stepCtx, so a step stopped by it comes
		// back as an ordinary non-zero exit. Measured rather than read off
		// a context, because the guest-side bound is the one that ends the
		// command and the call is deliberately left running past it.
		ownBound := time.Duration(0)
		if !step.Infrastructure && step.TimeoutMinutes > 0 {
			ownBound = time.Duration(step.TimeoutMinutes) * time.Minute
		}
		ownExpired := ownBound > 0 && !expired && ctx.Err() == nil &&
			!e.now().Before(started.Add(ownBound))
		record.FinishedAt = e.instant()
		record.Output = workflows.TruncateOutput(out.Output)
		record.ExitCode = out.ExitCode
		// What this step's failure is a statement about. An ordinary step
		// judges the tree, so its failure is the tree's (ConclusionFailure);
		// an infrastructure step judges this deployment's ability to get
		// the job an environment, so its failure is the deployment's
		// (ConclusionErrored) -- see Step.Infrastructure.
		notSuccess := workflows.ConclusionFailure
		if step.Infrastructure {
			notSuccess = workflows.ConclusionErrored
		}
		switch {
		case err != nil:
			record.Conclusion = notSuccess
			record.Error = err.Error()
		case out.ExitCode == 0:
			record.Conclusion = workflows.ConclusionSuccess
		default:
			record.Conclusion = notSuccess
		}
		if record.Conclusion != workflows.ConclusionSuccess {
			switch {
			case ctx.Err() != nil:
				// The run ended under this job -- cancelled, superseded, a
				// daemon shutting down. Whatever the step was doing, this
				// is not its doing (conclusionFor).
				record.Conclusion = workflows.ConclusionCancelled
			case expired && !step.Infrastructure:
				// The job spent its own `timeout-minutes`, which is grain
				// stopping the job rather than the step going wrong.
				record.Conclusion = workflows.ConclusionCancelled
			case ownExpired && record.Error == "":
				record.Error = fmt.Sprintf("this step ran past its own %s bound (timeout-minutes)", ownBound)
			}
		}
		result.Steps[len(result.Steps)-1] = record
		if progress != nil {
			progress(jobRunSnapshot(*result))
		}

		if record.Conclusion != workflows.ConclusionSuccess && !step.ContinueOnError {
			summary := ExtractFailureSummary(record.Output)
			// Quarantine only ever excuses a step that ran to the end and
			// failed. A cancelled step (the job's own timeout, a run
			// cancelled under it) or an infrastructure step that errored
			// did not finish judging the tree, so the test failures it
			// printed before it stopped are not the whole story and must
			// not turn it green. Nor does a step stopped by its own
			// `timeout-minutes`: it fails rather than being cancelled, but
			// it did not finish either.
			var qCfg *workflows.QuarantineConfig
			if record.Conclusion == workflows.ConclusionFailure && !ownExpired {
				if plan.Quarantine != nil {
					qCfg = plan.Quarantine
				} else {
					// Only the paths trustboundary's "ci-suppressions"
					// category guards: a quarantine file anywhere else
					// would be one an agent could add to its own branch
					// without a human ever being asked.
					qRes, qErr := sandbox.Exec(ctx, gritsteps.Command{
						Script: "cat .grain/quarantine.yml 2>/dev/null || cat .grain/quarantine.yaml 2>/dev/null || true",
					})
					if qErr == nil && strings.TrimSpace(qRes.Output) != "" {
						qCfg, _ = workflows.LoadQuarantine([]byte(qRes.Output))
					}
				}
			}

			allQuarantined, quarantinedNames, _ := workflows.EvaluateQuarantine(summary, qCfg, e.now())
			if allQuarantined {
				record.Conclusion = workflows.ConclusionSuccess
				result.Quarantined = true
				result.QuarantinedTests = append(result.QuarantinedTests, quarantinedNames...)
				if result.Status == "" {
					result.Status = "passed (quarantined)"
				}
				result.Warning = fmt.Sprintf("passed (quarantined): %d test(s) failed but are quarantined: %s", len(quarantinedNames), strings.Join(quarantinedNames, ", "))
				// The tests still failed, and the summary (its tests
				// marked Quarantined by EvaluateQuarantine) is what
				// QueryTestHistory reads once the step output is gone:
				// without it a quarantined test's history reads as
				// never having failed at all.
				if result.FailureSummary == nil {
					result.FailureSummary = summary
				} else {
					result.FailureSummary.Tests = append(result.FailureSummary.Tests, summary.Tests...)
				}
				result.Steps[len(result.Steps)-1] = record
				if progress != nil {
					progress(jobRunSnapshot(*result))
				}
			} else {
				switch {
				case result.FailureSummary == nil:
					result.FailureSummary = summary
				case !failed && summary != nil:
					// All the summary has so far is quarantined failures
					// from earlier steps that passed. This step's failure
					// is the one that decides the job, so it leads, and
					// the quarantined tests are kept after it.
					merged := *summary
					merged.Tests = append(append([]workflows.TestFailure(nil), summary.Tests...), result.FailureSummary.Tests...)
					result.FailureSummary = &merged
				}
				failed = true
				switch record.Conclusion {
				case workflows.ConclusionCancelled:
					cancelled = true
					overBound = expired && ctx.Err() == nil
				case workflows.ConclusionErrored:
					environment = record.Name
					environmentWhy = "it did not succeed"
					if expired {
						environmentWhy = fmt.Sprintf("it had %s and did not finish",
							infrastructureBound(pj.Job, step))
					}
				}
			}
		}
	}

	switch {
	case cancelled:
		result.Conclusion = workflows.ConclusionCancelled
		if overBound {
			result.Error = fmt.Sprintf("this job ran past its %s bound", pj.Job.Timeout())
		}
	case environment != "":
		// Errored rather than failure, and this is the whole point of the
		// key that got it here: the step that did not succeed is one that
		// fetches what this job needs to run, so nothing in this job
		// judged the tree and a red verdict on the commit would be a lie
		// about it (ConclusionErrored, Step.Infrastructure).
		result.Conclusion = workflows.ConclusionErrored
		result.Error = fmt.Sprintf("%q could not get this job the environment its steps run in: %s. "+
			"Nothing here tested the commit, so this is not a verdict on it",
			environment, environmentWhy)
	case failed:
		result.Conclusion = workflows.ConclusionFailure
	default:
		result.Conclusion = workflows.ConclusionSuccess
	}
}

// jobClock is a job's `timeout-minutes` while its steps run: a deadline
// this package can move rather than a context deadline it cannot.
//
// It moves for one reason. A job's bound is meant to bound the work the
// job does, and a step that fetches the environment the job's other
// steps need (Step.Infrastructure) is not that work -- it is the job
// waiting for a machine to be ready, which happens to be spelled as a
// step because the thing it fetches is too big to put in an image. So an
// infrastructure step runs on a bound of its own and gives the time it
// took back to the job, and a job whose cache was cold has exactly as
// long to run its tests as one whose cache was warm.
//
// Charging that time to the job is what turned a cold cache into a red
// branch: the ~1GB guest build a missed `guest:inputs-<key>` forced took
// 50-110 minutes of a job bounded at DefaultJobTimeout, and what came
// out the other end was a verdict about a branch that was fine
// (grain/task-464). Both halves of that are fixed here and in runSteps:
// the miss no longer eats the bound, and if it goes wrong anyway it is
// errored rather than failed.
//
// Wall clock rather than Engine.Now on purpose. This bounds a machine
// grain is holding, and context deadlines are real time whatever a test's
// clock says; a fake clock that jumps an hour per call must not be able
// to expire a job's steps.
type jobClock struct{ deadline time.Time }

// newJobClock starts the clock at wherever the job's bound already is --
// jobCtx carries it, so the time spent building the machine has already
// come off it. A context with no deadline (nothing in the engine builds
// one, but a caller could) gives the job its whole bound from here.
func newJobClock(jobCtx context.Context, job workflows.Job) *jobClock {
	if deadline, ok := jobCtx.Deadline(); ok {
		return &jobClock{deadline: deadline}
	}
	return &jobClock{deadline: time.Now().Add(job.Timeout())}
}

// bound is the context one step runs under, and the func that closes it
// and charges what it used.
//
// An ordinary step is bounded by what is left of the job's own time, and
// spends it. An infrastructure step is bounded by its own time and spends
// none of the job's: the deadline is pushed back by however long it took,
// so the steps after it are where they would have been had the cache it
// was filling been warm.
func (c *jobClock) bound(parent context.Context, job workflows.Job, step workflows.Step) (context.Context, func()) {
	if !step.Infrastructure {
		ctx, cancel := context.WithDeadline(parent, c.deadline)
		return ctx, cancel
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, infrastructureBound(job, step))
	return ctx, func() {
		cancel()
		c.deadline = c.deadline.Add(time.Since(started))
	}
}

// infrastructureBound is how long a step that fetches the job's
// environment may take: its own `timeout-minutes` where it declares one
// -- which is how a step that pulls a 1GB image out of a registry, or
// builds one because nothing published it, says so -- and the job's own
// bound where it does not.
//
// The job's own bound rather than what is left of it: the point of the
// key is that this is not the job's time, so it gets a fresh one of the
// same length. It is a bound rather than no bound at all for the reason
// Job.TimeoutMinutes gives for never meaning "unbounded" -- a step that
// can hang forever eventually holds every sandbox a deployment has.
func infrastructureBound(job workflows.Job, step workflows.Step) time.Duration {
	if step.TimeoutMinutes > 0 {
		return time.Duration(step.TimeoutMinutes) * time.Minute
	}
	return job.Timeout()
}

// execStep runs one step, and -- where the backend can stream and there
// is somebody watching -- publishes what that step is printing while it
// prints it, so a job in flight is something a reader can follow rather
// than a name with a spinner beside it (live.go).
//
// running is the job as it stands with this step already appended and
// still unfinished: the snapshot each publication is made from, taken
// once here rather than read off the live result, because the chunks
// arrive on whatever goroutine the transport reads its pipe on and the
// result is being written on this one. A copy per publication keeps the
// observer's own copy its own, exactly as every other progress call does.
func (e *Engine) execStep(ctx context.Context, sandbox gritsteps.Sandbox, cmd gritsteps.Command, running workflows.JobRun, progress func(workflows.JobRun)) (gritsteps.CommandResult, error) {
	streamer, ok := sandbox.(gritsteps.StreamingSandbox)
	if !ok || progress == nil || len(running.Steps) == 0 {
		return sandbox.Exec(ctx, cmd)
	}
	live := gritsteps.NewLiveOutput(e.now, gritsteps.LiveOutputInterval, func(text string) {
		snapshot := jobRunSnapshot(running)
		snapshot.Steps[len(snapshot.Steps)-1].Output = text
		progress(snapshot)
	})
	// Closed before the caller records the finished step, so the last
	// thing said about it is its real output and its status rather than a
	// straggling fragment of the same text.
	defer live.Close()
	return streamer.ExecStream(ctx, cmd, live.Write)
}

// conclusionFor turns "this went wrong" into the right word for *why*:
// a failure under a context that has already ended is the context's
// doing, not the step's, and calling it a failure sends whoever reads
// it looking for a bug in the build.
//
// A deadline is reported as cancelled too. A job timeout is grain
// stopping the job, which is what cancelled means here; the job's own
// Error says it was the bound rather than a caller.
func conclusionFor(ctx context.Context, otherwise workflows.Conclusion) workflows.Conclusion {
	if ctx.Err() != nil {
		return workflows.ConclusionCancelled
	}
	return otherwise
}

// stepTimeout is the bound handed to the sandbox for one command: the
// step's own where it set one, otherwise whatever is left of the job's.
func stepTimeout(ctx context.Context, step workflows.Step) time.Duration {
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if step.TimeoutMinutes > 0 {
		own := time.Duration(step.TimeoutMinutes) * time.Minute
		if remaining == 0 || own < remaining {
			return own
		}
	}
	return remaining
}

// stepEnv is the environment one step sees: grain's own variables at
// the bottom, then the workflow's, the job's and the step's, each
// layering over the last -- GitHub's own precedence.
//
// grain's own go at the bottom deliberately: a workflow that wants to
// override GRAIN_CI_REF for a step of its own is doing something odd
// but not dangerous, and nothing here reads these back.
func stepEnv(plan workflows.Plan, pj workflows.PlannedJob, step workflows.Step) map[string]string {
	env := workflows.RunEnv(plan.Request, pj.Workflow, pj.Job.ID)
	for _, layer := range []map[string]string{pj.WorkflowEnv, pj.Job.Env, step.Env} {
		for k, v := range layer {
			env[k] = v
		}
	}
	return env
}

// MaxSandboxName is how many bytes a job's sandbox name may take, and
// it is a bound this package cannot see for itself.
//
// A kontur VM is named after its sandbox -- orchestrator.VMNamePrefix
// ("g-") plus the sandbox's own name -- and netshim derives "tap-<vm
// name>" and "ctl-<vm name>" from that, which Linux caps at 15 bytes
// (IFNAMSIZ-1). Eleven bytes for a VM name, nine once the prefix has
// taken its two. An agent run's sandbox is named "<task id>-<attempt>"
// (dispatch.RunID) and spends the same nine.
//
// It used to be sixty, which is what a *hostname* affords and is the
// only limit this package can derive on its own -- so every CI job on a
// kontur deployment died at Acquire with "over the 11-byte limit"
// before it ran a step, no matter which workflow it came from. pkg/ci
// imports neither the orchestrator nor kontur, deliberately (see
// pkg/cli/ci.go), so the number is restated here rather than derived;
// cmd/grain's TestCISandboxNamesFitAKonturVMName holds the two ends
// together by asking the real GranuleSandboxes what it makes of a name
// this long.
const MaxSandboxName = 9

// sandboxName is what a job's sandbox is called: "c", the run it belongs
// to, and the job's own position in the plan -- "c9f2c1a-0". It has to
// be unique per job per run, has to survive being a hostname, and has to
// fit MaxSandboxName, which leaves no room for the workflow path and job
// id the name used to carry. Those are on the JobRun beside the sandbox
// name (JobRun.Sandbox), which is where a reader who needs them looks.
//
// The leading "c" is not decoration: a CI job's sandbox and an agent
// run's are named in one namespace -- the backend's live sandboxes, the
// git proxy's read scopes and its minted tokens are all keyed by this
// string -- and an agent run's name is digits and a hyphen. Starting
// every CI name with a letter is what keeps a run of task 123456 and a
// CI job from ever being handed the same key.
//
// The run id is truncated to whatever is left rather than hashed, so the
// name still names the run: "c9f2c1a-0" is a prefix match away from run
// 9f2c1a3b8e4d5f60 in the run list. Two runs alive at once whose ids
// share that prefix would collide, which the backend refuses rather than
// confuses; at 16 hex digits of run id (Service.newID) that is a
// coincidence nobody will see.
func SandboxName(runID string, plan workflows.Plan, pj workflows.PlannedJob) string {
	job := strconv.FormatInt(int64(jobIndex(plan, pj)), 36)
	run := hostnameSafe(runID)
	if budget := MaxSandboxName - len("c-") - len(job); budget < 0 {
		run = ""
	} else if len(run) > budget {
		run = strings.Trim(run[:budget], "-")
	}
	return "c" + run + "-" + job
}

// jobIndex is pj's place in plan, 0 for a job it does not hold.
func jobIndex(plan workflows.Plan, pj workflows.PlannedJob) int {
	for i, other := range plan.Jobs {
		if other.ID == pj.ID {
			return i
		}
	}
	return 0
}

// hostnameSafe folds a string into something a hostname may hold:
// lower case, and everything that is not a letter or a digit becomes a
// single hyphen, with none left at either end.
func hostnameSafe(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// SortJobRuns puts a run's jobs in the order a reader wants them:
// failures first (that is what they came to look at), then the jobs
// grain could not run, then everything else in plan order. It is a helper for whatever renders a Run, not
// something the engine does to its own results -- Run.Jobs stays in
// plan order, because that is the order the run happened in.
func SortJobRuns(jobs []workflows.JobRun) []workflows.JobRun {
	out := append([]workflows.JobRun(nil), jobs...)
	rank := func(c workflows.Conclusion) int {
		switch c {
		case workflows.ConclusionFailure:
			return 0
		case workflows.ConclusionErrored:
			return 1
		case workflows.ConclusionCancelled:
			return 2
		case workflows.ConclusionSuccess:
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Conclusion) < rank(out[j].Conclusion) })
	return out
}

// ExtractFailureSummary is the hook grain's pkg/ci installs so the engine
// populates structured failure summaries without importing grain's diagnostics.
var ExtractFailureSummary = func(string) *workflows.FailureSummary { return nil }

// JobCapabilities is the capabilities a sandbox for job is handed out of
// provided (the run's Request.Capabilities): JobCapabilities under this
// engine's ExplicitOnlyCapability, the same narrowing runJob applies to
// the SandboxRequest it acquires a job's sandbox with. Whatever re-wires
// a job's sandbox outside runJob -- a restarted daemon readopting it --
// grants from this rather than from the run's whole set, so a job holds
// after a restart exactly what it was given before one.
func (e *Engine) JobCapabilities(job workflows.Job, provided []string) []string {
	return workflows.JobCapabilities(job, provided, e.ExplicitOnlyCapability)
}
