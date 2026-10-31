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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The other half of translating a workflow's `on: schedule` into a task
// schedule (grain/task-344): when that schedule comes due, somebody has to
// start the run the filed task is there to read, and somebody has to read
// the answer.
//
// # Who starts it, and why it is grain
//
// Two shapes were open. The agent could start the run itself, which would
// give it judgement -- skip a night when `main` has not moved, choose a
// branch, react to what it finds. It is also not possible today: a task
// holding grain-ci can list runs and read logs, and wait_for_checks only
// ever starts a run for the task's own pushed branch, so there is no way
// for a task to ask for a run of somebody else's workflow at all
// (grain/task-221 is open on exactly that). Building a bespoke one inside
// this template would be the thing that idea deserves least -- a
// CI-starting mechanism that only grain's own nightly can use.
//
// So grain starts it, here. That is deliberately not the "cron in pkg/ci"
// this feature exists instead of: the objection to that was never that a
// clock starts a run, it was that nothing reads the answer. Here the
// reader is guaranteed by the same firing and told which run is its own,
// and the whole thing rides the scheduler an operator can already see,
// pause and audit.
//
// # Why the review is filed at the end and not at the start (grain/task-351)
//
// The first cut filed the review task at the due tick, in the same act as
// starting the run, and told the agent to follow a run that had just
// begun. For this deployment's own nightly that is an agent dispatched at
// 02:11 to watch a suite whose longest job alone is allowed two hours,
// against an agent run budget of about the same. Its prompt was explicit
// that it must report what finished and say the rest is unread rather than
// claim a verdict it had not seen -- honest, but the honest report was the
// *expected* outcome of every long suite rather than an edge case.
//
// So the firing and the review are pulled apart, in the shape
// reconcileBaseBranchCI already has and for its reason: the firing writes
// the run down (model.ScheduledWorkflowRun), and a later cycle reads the
// conclusion off the finished run and files the review then, with the
// verdict already in the task's opening comment. The task now has a
// finished run in front of it, which is a job that fits in an agent run.
//
// # Only a night that did not pass is reviewed (grain/task-3160)
//
// That used to end every firing in a task, whatever the run said. On a
// green night that is a sandbox, a queue slot and an agent's tokens spent
// to report that everything passed -- and a green night was most nights.
// The run is the check: a firing whose run passed (every job that ran
// passed, and at least one did -- scheduledWorkflowRunPassed) is settled
// with no task at all (model.ScheduledWorkflowRun.ReviewNotNeeded), and
// stays visible as the schedule's last firing and on the CI page. Every
// other ending -- a failure, an errored or cancelled run, one lost with
// its daemon, one never started, one grain stopped waiting for -- still
// files a review with the verdict in front of the agent, because each of
// those is either a finding or a night in which nothing was tested. A
// schedule can ask for every night reviewed anyway with `review: always`
// (model.WorkflowReviewPolicy).
//
// Every step here is level-triggered off that row and idempotent on both
// sides of its writes: the conclusion is recorded before the firing is
// settled, a green one is settled by a single write, and a review carries
// a tag naming the exact firing, so a daemon restarted between any two of
// them files one review rather than two or none.
//
// # Why every firing asks for a recheck (grain/task-347)
//
// grain CI is content-addressed: the same commit asked about twice is one
// answer, served out of the cache in seconds (ci.jobFingerprint). For a
// push that is the whole point, and for a cron it is the end of the
// feature. On a day the branch has not moved -- a weekend, a quiet day --
// a firing of an unchanged workflow at an unchanged commit would replay
// yesterday's verdict and be reviewed as a green night in four seconds.
// That is a true statement about the commit and a false one about the
// suite: a nightly's jobs are the ones whose *repetition* is the finding,
// a shuffled and repeated Go suite or a soak, and a replayed answer is
// precisely the thing they exist to disagree with. It is also the same
// failure this file was written against -- a report that looks like a
// night's run and is not one.
//
// So the request carries ci.Request.Recheck: no job is served from the
// cache. What the run learns is still written back, as the newest verdict
// for its keys, and those keys carry the schedule event -- so a nightly's
// results never stand in for the push runs that judge a commit.
//
// It is set here rather than exposed to callers because it is not a
// caller's decision in the first place (Request.Recheck is `json:"-"`
// with TrustedRelease and Capabilities): spending sandboxes on work grain
// has already answered is grain's to choose, and grain chooses it for
// every scheduled run, because "run this again on a cadence" is what a
// cron means. A schedule that wanted the cached answer would want no run.
//
// It does not cost the de-duplication Service.Start does of live runs: a
// recheck hashes into its own plan key (ci.planKey), so it will not be
// handed back the very run whose answer it is re-asking, but two rechecks
// of the same plan still share a key and still join. A firing that asks
// twice -- a retry, a second daemon cycle -- joins its own run rather
// than starting a second suite.
//
// # A run that never finishes
//
// Cancelled, interrupted by the daemon that was running it, lost with a
// restart, or simply still going long after any suite could be: each of
// those still files a review saying so. "The nightly did not complete" is
// exactly the report that is invisible from everywhere else, and a firing
// that quietly evaporated would take the next one with it -- the overlap
// rule holds the cadence until a firing is settled, so giving up loudly
// is also what keeps the schedule alive.
//
// The last of those is not the same report as the others, though, and
// the review must not be filed as if it were. A run grain stopped
// waiting for is a run that exists: its finished jobs have logs, and the
// job that never ended is still writing one. "The nightly hangs" is the
// most interesting failure this suite can have and the evidence of where
// it hangs is in that log, so the row says the run is unfinished
// (model.ScheduledWorkflowRun.Unfinished) and the reviewer is sent to
// read it -- where a firing with no run at all is told to report that
// and stop.

// scheduledWorkflowStarter is the automation principal a run and a comment
// from this path are attributed to, beside "merge-queue" and "schedule": a
// workflow's own cron, translated.
const scheduledWorkflowStarter = "workflow-schedule"

const (
	// scheduledWorkflowStartAttempts bounds how many times grain asks CI
	// for one firing's run before it gives up and reports a dead night.
	// The same shape as ciMaximumAttempts and deliberately smaller: a
	// nightly that cannot be started is worth a handful of tries spread
	// over a few minutes, not an hour of them, because the report is more
	// use than the retry once the night is half gone.
	scheduledWorkflowStartAttempts = 3
	// scheduledWorkflowStartDelay spaces those attempts, so a GitHub API
	// that is briefly unreachable is asked again rather than asked three
	// times in the same second.
	scheduledWorkflowStartDelay = 5 * time.Minute
	// scheduledWorkflowDeadline is how long grain waits for a run to end
	// before it stops waiting and files the review anyway. Far beyond any
	// suite -- ci.DefaultJobTimeout is 36 minutes and a job may ask for
	// more -- because the point is not to cut a long run short: it is that
	// a run whose engine has forgotten to conclude it must not hold the
	// cadence for ever, since nothing fires again until this firing is
	// settled.
	scheduledWorkflowDeadline = 12 * time.Hour
)

// shortestInterval is the least time a recurrence can leave between two
// firings -- the worst case for the bound above, which is what a monthly
// landing in February and a weekly either side of a clock change both
// need it to be. An error is a recurrence kind this cannot measure,
// which is a caller's cue to say nothing rather than to guess.
//
// It began as a helper of
// TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence, which
// still uses it, and is here now because the same question has to be
// asked twice. That test asks it of *this* repository's own workflow
// files, at build time, where an answer can be a failing build. Every
// other repository grain reads declarations from gets its crons
// translated at run time (repodecl's scheduledWorkflows), out of a tree
// no test of ours ever sees, so the only place left to ask is the sync
// itself -- logScheduledWorkflowCadence, which is the other caller.
func shortestInterval(r model.Recurrence) (time.Duration, error) {
	switch r.Kind {
	case model.RecurrenceEveryNHours:
		return time.Duration(r.EveryNHours) * time.Hour, nil
	case model.RecurrenceEveryNMinutes:
		return time.Duration(r.EveryNMinutes) * time.Minute, nil
	case model.RecurrenceDaily:
		return 24 * time.Hour, nil
	case model.RecurrenceWeekly:
		return 7 * 24 * time.Hour, nil
	case model.RecurrenceMonthly:
		return 28 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("recurrence kind %q is not one this check knows how to measure", r.Kind)
	}
}

// reconcileScheduledWorkflows carries each firing of a workflow-review
// schedule from the tick it came due to the task that reads it: start the
// run, collect its verdict, and file the review -- or, for a run that
// passed, settle the firing with none. One row per schedule
// (model.ScheduledWorkflowRun), written by the firing itself
// (model.Store.fireWorkflowSchedule).
//
// Level-triggered like every reconciler here, and one row failing does not
// stop the others -- a firing this cycle could not move on is one the next
// cycle tries again, exactly as it stood.
func reconcileScheduledWorkflows(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	rows, err := deps.Store.ListScheduledWorkflowRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading scheduled workflow runs: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if err := reconcileOneScheduledWorkflow(ctx, deps, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func reconcileOneScheduledWorkflow(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time) error {

	switch {
	case row.Settled():
		// This firing is over, reviewed or green. The row stays as the
		// schedule's record of it until the next firing replaces it --
		// unless the schedule itself has gone, in which case nothing ever
		// will.
		return forgetUndeclaredWorkflowRun(ctx, deps, row)
	case row.Conclusion != "":
		// Concluded and not settled: a daemon that stopped between
		// recording the verdict and acting on it.
		return settleScheduledWorkflowRun(ctx, deps, row, now)
	case !row.Started():
		return startScheduledWorkflowRun(ctx, deps, row, now)
	default:
		return collectScheduledWorkflowRun(ctx, deps, row, now)
	}
}

// startScheduledWorkflowRun asks grain CI for the run this firing exists
// to review, and writes down which run that is.
//
// A failure to start is not silently retried for ever: after
// scheduledWorkflowStartAttempts the firing is concluded as errored with
// the reason on it, and the review is filed saying the night did not run.
// That is the report a nightly that has stopped running deserves, and it
// is what releases the cadence to try again tomorrow.
func startScheduledWorkflowRun(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time) error {

	if deps.CI == nil {
		return concludeScheduledWorkflowRun(ctx, deps, row, now, ci.ConclusionErrored,
			"this deployment runs no CI of its own, so there is no run to review")
	}
	if !scheduledWorkflowStartDue(row, now) {
		return nil // waiting out the delay between attempts
	}
	// A firing recorded before the repository's standing work was turned
	// off (or this schedule's own item was) is dropped rather than run:
	// the firing path refuses new ones (model.Store.suppressStandingWorkOff),
	// and starting one already recorded would be the backlog that switch
	// promises not to leave (grain/task-3332). The next firing after it
	// is turned back on is an ordinary one.
	if runs, err := runsStandingWork(ctx, deps.Store, row.Repo, model.ScheduleItem(row.ScheduleID)); err != nil {
		return err
	} else if !runs {
		log.Printf("orchestrator: schedule %s's firing of %s skipped: %s",
			row.ScheduleID, row.Stem, model.StandingWorkOffReason(row.Repo))
		return deps.Store.DeleteScheduledWorkflowRun(ctx, row.ScheduleID)
	}
	ref, err := scheduledWorkflowRef(deps, row)
	if err != nil {
		return failedScheduledWorkflowStart(ctx, deps, row, now, err)
	}
	workflow := scheduledWorkflowPath(ctx, deps, row)
	run, err := deps.CI.Start(ctx, scheduledWorkflowRequest(deps, row, ref, workflow))
	if err != nil {
		return failedScheduledWorkflowStart(ctx, deps, row, now, err)
	}
	row.RunID, row.Branch, row.Workflow = run.ID, ref, workflow
	row.Attempts++
	started := run.StartedAt
	if started == nil {
		started = &now
	}
	row.AttemptedAt = started
	if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}
	log.Printf("orchestrator: schedule %s started grain CI run %s of %s on %s %s",
		row.ScheduleID, run.ID, workflow, row.Repo, ref)
	return nil
}

// scheduledWorkflowRequest is the run one firing asks for, on ref and
// narrowed to workflow -- the first run, and any automatic re-run of it
// (retryInfraScheduledWorkflow), which narrows it further.
func scheduledWorkflowRequest(deps Deps, row model.ScheduledWorkflowRun, ref, workflow string) ci.Request {
	return ci.Request{
		Repo:     row.Repo.String(),
		Ref:      ref,
		Event:    ci.EventSchedule,
		Workflow: workflow,
		// A cron asks for the work, not for the answer: a firing at a
		// commit nothing has moved must run the suite again rather than
		// replay the cache in four seconds (see "Why every firing asks
		// for a recheck" above).
		Recheck: true,
		// The cron that fired this is grain's own, not a caller's
		// `-event schedule`, so a capability that opted in to trusted
		// schedules (model.TrustedScheduleCapability, today repo-write)
		// may be handed to the jobs that declare it.
		TrustedSchedule: true,
		RequestedBy:     scheduledWorkflowRequester(row),
		Priority:        scheduledWorkflowPriority(deps, row, ref),
	}
}

func scheduledWorkflowRequester(row model.ScheduledWorkflowRun) string {
	return scheduledWorkflowStarter + ":" + row.ScheduleID
}

// isReleaseGateWorkflow reports whether this firing is of a workflow a
// trunk release is selected on (releaseGateWorkflowStems) and runs on
// the repository's default branch: the firings that rank as release
// gates and whose `infra` verdicts are re-run before they are reviewed
// (docs/design/release-from-trunk.md, RFT-2). The advisory live-provider
// suites are deliberately not among them -- their reds are mostly the
// deployment's credentials and quota already, and re-running a suite
// that is failing on an IAM 429 spends the quota it is short of.
func isReleaseGateWorkflow(deps Deps, row model.ScheduledWorkflowRun, ref string) bool {
	return releaseGateWorkflowStems[row.Stem] && isTrunk(deps, row.Repo, ref)
}

// scheduledWorkflowPriority is the rank a firing's run asks for at the
// job gate: ci.PriorityReleaseGate for a release gate on trunk, so the
// nightly LKG selection waits on is not the job ordinary task load starves.
func scheduledWorkflowPriority(deps Deps, row model.ScheduledWorkflowRun, ref string) ci.Priority {
	if isReleaseGateWorkflow(deps, row, ref) {
		return ci.PriorityReleaseGate
	}
	return ci.PriorityNormal
}

// retryInfraScheduledWorkflow re-runs a release gate's run that ended with
// an `infra` verdict (infraVerdict) rather than filing its review: the
// jobs that did not pass, uncached, up to maxInfraRetries times and
// infraRetryDelay after the run ended. A night lost to a starved or
// OOM-pressured host is otherwise a review an agent spends a run on, and
// a release that cannot advance until tomorrow's nightly.
//
// It reports whether it has dealt with the run -- waiting out the delay,
// or a re-run started and written to the row, which stays unconcluded --
// and false for everything else, which is recorded and reviewed exactly
// as before: a verdict about the tree, re-runs spent, a firing that is not
// a release gate, or a re-run grain could not start. The count is on the
// run (infraRetriesOf), since ScheduledWorkflowRun.Attempts counts starts.
func retryInfraScheduledWorkflow(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	run ci.Run, now time.Time) (bool, error) {

	why, infra := infraVerdict(run)
	if !infra || !isReleaseGateWorkflow(deps, row, row.Branch) {
		return false, nil
	}
	n := infraRetriesOf(run.RequestedBy)
	if n >= maxInfraRetries {
		return false, nil
	}
	// Measured from the run's own end, or -- for one interrupted before it
	// recorded one -- from when it was started, since "now" would move
	// with every cycle and never come due.
	ended := now
	switch {
	case run.FinishedAt != nil:
		ended = *run.FinishedAt
	case row.AttemptedAt != nil:
		ended = *row.AttemptedAt
	}
	if now.Before(ended.Add(infraRetryDelay)) {
		return true, nil
	}
	req := scheduledWorkflowRequest(deps, row, row.Branch, row.Workflow)
	// The same commit the run was judging: a re-run is a second sample of
	// that night, not a new one of wherever the branch has got to since.
	req.Commit = run.Commit
	retry, err := startInfraRetry(ctx, deps, req, run, scheduledWorkflowRequester(row), n+1, why)
	if err != nil {
		log.Printf("orchestrator: schedule %s: %v -- reviewing the run as it ended", row.ScheduleID, err)
		return false, nil
	}
	row.RunID = retry.ID
	row.Attempts++
	started := retry.StartedAt
	if started == nil {
		started = &now
	}
	row.AttemptedAt = started
	return true, deps.Store.PutScheduledWorkflowRun(ctx, row)
}

// scheduledWorkflowStartDue reports whether it is time to ask again for a
// run that has not started: at once for the first attempt, and after
// scheduledWorkflowStartDelay for each one after it.
func scheduledWorkflowStartDue(row model.ScheduledWorkflowRun, now time.Time) bool {
	if row.AttemptedAt == nil {
		return true
	}
	return !now.Before(row.AttemptedAt.Add(scheduledWorkflowStartDelay))
}

// failedScheduledWorkflowStart records one failed attempt at starting a
// firing's run, and concludes the firing once there have been enough of
// them. The error goes back to the cycle as well as onto the row: the
// comment is what the reviewer eventually reads, and the daemon's own log
// is where an operator finds out tonight rather than tomorrow.
func failedScheduledWorkflowStart(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time, cause error) error {

	row.Attempts++
	row.AttemptedAt = &now
	row.Detail = fmt.Sprintf("grain could not start the run: %s", cause)
	if row.Attempts >= scheduledWorkflowStartAttempts {
		if err := concludeScheduledWorkflowRun(ctx, deps, row, now, ci.ConclusionErrored,
			row.Detail); err != nil {
			return err
		}
	} else if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}
	return fmt.Errorf("orchestrator: starting the %s workflow for schedule %s: %w",
		row.Stem, row.ScheduleID, cause)
}

// collectScheduledWorkflowRun reads back a run that had not finished and
// records how it ended -- and then, in the same cycle, files the review of
// it, so that a run which finished at 04:00 is being read at 04:00 rather
// than at whatever the next tick is. A run that passed is settled with no
// review instead, unless its schedule says `review: always`.
//
// A run the service no longer has is not a verdict: pruned past
// ci.DefaultStoredRunHistory, or lost with the daemon that was running it
// (collectCIRun and collectBaseBranchCI make the same distinction). It is
// recorded as errored rather than forgotten, because "last night's run
// disappeared" is a report, and a row left pending for ever would be a
// schedule that never fires again.
func collectScheduledWorkflowRun(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time) error {

	if deps.CI == nil {
		return concludeScheduledWorkflowRun(ctx, deps, row, now, ci.ConclusionErrored,
			"this deployment no longer runs CI of its own, so the run cannot be read")
	}
	run, ok := deps.CI.Get(row.RunID)
	if ok && (run.Interrupted || run.Conclusion.Done()) {
		// A release gate's verdict about nothing is re-run before it is
		// reviewed (retryInfraScheduledWorkflow); every other ending, and
		// every other firing, goes on below as it always did.
		if retried, err := retryInfraScheduledWorkflow(ctx, deps, row, run, now); err != nil || retried {
			return err
		}
	}
	if !ok || run.Interrupted {
		return concludeScheduledWorkflowRun(ctx, deps, row, now, ci.ConclusionErrored,
			scheduledWorkflowLostDetail(ok))
	}
	if !run.Conclusion.Done() {
		if now.Sub(row.FiredAt) < scheduledWorkflowDeadline {
			return nil // still going, and entitled to be
		}
		// Long past any suite. grain stops waiting rather than holding
		// this schedule's cadence for ever; the run is left alone, since
		// cancelling it would destroy the evidence of whatever it is
		// stuck on -- and the row says it is still running, because that
		// evidence is what the review is then for
		// (model.ScheduledWorkflowRun.Unfinished).
		//
		// Errored rather than a word of its own: this is grain saying it
		// never got a verdict, which is exactly what ConclusionErrored
		// means everywhere else here -- not a verdict about the tree.
		// The reviewer is not left to infer that from the word, though;
		// the comment says in as many words that the run has not ended
		// (workflowReviewComment).
		row.Unfinished = true
		return concludeScheduledWorkflowRun(ctx, deps, row, now, ci.ConclusionErrored,
			fmt.Sprintf("the run had still not finished %s after the schedule came due, so grain "+
				"stopped waiting for it", now.Sub(row.FiredAt).Round(time.Minute)))
	}
	finished := run.FinishedAt
	if finished == nil {
		finished = &now
	}
	row.FinishedAt = finished
	row.Conclusion, row.Detail = string(run.Conclusion), ciDetail(run)
	if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}
	startPromotion(ctx, deps, row, run)
	return settleScheduledWorkflowRun(ctx, deps, row, now)
}

// releaseTrainStem is the scheduled workflow that cuts, soaks and stages
// trunk releases (.grain/workflows/release-train.yml,
// docs/design/release-from-trunk.md §6), and promoteWorkflowPath the one
// that publishes a promoted release's images as vX.Y.0 and `latest`.
const (
	releaseTrainStem    = "release-train"
	promoteWorkflowPath = ci.WorkflowDir + "/promote.yml"
)

// startPromotion starts .grain/workflows/promote.yml as a trusted
// `release` of trunk once a release-train run on it has finished, so the
// `latest` image matches the git `latest` tag without waiting for the
// next merge to fire promote.yml's own `release` trigger. The train
// itself stops at vX.Y.0-pending and never moves `latest`
// (docs/design/release-from-trunk.md RFT-8): a person's approval does,
// through promote-release.yml, whose own promote job publishes it. What
// this start adds is a republish of the release `latest` already names. It is a release rather than the
// schedule's own event because that is what package-publish and
// image-signing are released to, and what it asserts is true: the
// commit is already on trunk.
//
// Every finished run asks, whatever it concluded: a run can push its
// tags and fail a later job, and promote.yml publishes whatever the git
// `latest` tag names, so a night that promoted nothing costs a few
// manifest copies onto the tags they already hold. A run that could not
// be started is logged and left to that next merge.
func startPromotion(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun, run ci.Run) {
	if row.Stem != releaseTrainStem || deps.CI == nil || !isTrunk(deps, row.Repo, row.Branch) {
		return
	}
	if run.Conclusion == ci.ConclusionCancelled {
		return
	}
	started, err := deps.CI.Start(ctx, promotionRequest(row, run))
	if err != nil {
		log.Printf("orchestrator: schedule %s: starting %s after release-train run %s: %v",
			row.ScheduleID, promoteWorkflowPath, run.ID, err)
		return
	}
	log.Printf("orchestrator: schedule %s: release-train run %s finished; started grain CI run %s of %s on %s %s",
		row.ScheduleID, run.ID, started.ID, promoteWorkflowPath, row.Repo, row.Branch)
}

// promotionRequest is the promote.yml run startPromotion asks for: a
// trusted release of the commit release-train ran on.
func promotionRequest(row model.ScheduledWorkflowRun, run ci.Run) ci.Request {
	return ci.Request{
		Repo: row.Repo.String(), Ref: row.Branch, Commit: run.Commit,
		Event: ci.EventRelease, TrustedRelease: true,
		Workflow:    promoteWorkflowPath,
		RequestedBy: "promote:" + row.ScheduleID + ":" + run.ID,
		Priority:    ci.PriorityReleaseGate,
	}
}

// settleScheduledWorkflowRun ends a firing whose conclusion is recorded:
// with no task at all for a run that passed, unless its schedule asks for
// every night reviewed (model.WorkflowReviewPolicy), and with a review
// for anything else.
//
// The run is read again rather than judged off the row, because a
// recorded "success" is not the whole question -- a run with no jobs in
// it concludes as success too (scheduledWorkflowRunPassed). A run CI no
// longer has cannot be shown to have passed, so it is reviewed: the
// cheaper mistake is an agent reading a green night, not a night nobody
// reads. This is the step a restarted daemon re-enters, so it decides
// the same way however many times it is asked: a green firing is settled
// by one write, and a review is guarded by its firing's tag.
func settleScheduledWorkflowRun(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time) error {

	if row.Conclusion == string(ci.ConclusionSuccess) && !row.Unfinished && deps.CI != nil {
		if run, ok := deps.CI.Get(row.RunID); ok && scheduledWorkflowRunPassed(run) {
			policy, err := scheduledWorkflowReviewPolicy(ctx, deps, row)
			if err != nil {
				return err
			}
			if !policy.Always() {
				row.ReviewNotNeeded = true
				if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
					return err
				}
				log.Printf("orchestrator: schedule %s's grain CI run %s of %s on %s %s passed; no review filed",
					row.ScheduleID, row.RunID, row.Workflow, row.Repo, row.Branch)
				return nil
			}
		}
	}
	return fileWorkflowReview(ctx, deps, row, now)
}

// scheduledWorkflowRunPassed reports whether a finished run is a green
// night: concluded as success, with at least one job that passed in it.
// A run with nothing in it concludes as success too -- a workflow that
// narrowed to no jobs -- and nothing was tested by it, so it is reviewed
// like any other night that is not clearly green.
func scheduledWorkflowRunPassed(run ci.Run) bool {
	if run.Conclusion != ci.ConclusionSuccess {
		return false
	}
	for _, j := range run.Jobs {
		if j.Conclusion == ci.ConclusionSuccess {
			return true
		}
	}
	return false
}

// scheduledWorkflowReviewPolicy is when this firing's schedule asks for
// a review (model.Schedule.WorkflowReview). A schedule that has gone
// since it fired asks for the default -- the run happened, but nobody
// declared anything about it any more.
func scheduledWorkflowReviewPolicy(ctx context.Context, deps Deps,
	row model.ScheduledWorkflowRun) (model.WorkflowReviewPolicy, error) {

	sched, err := deps.Store.GetSchedule(ctx, row.ScheduleID)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading schedule %s: %w", row.ScheduleID, err)
	}
	if sched == nil {
		return model.WorkflowReviewOnFailure, nil
	}
	return sched.WorkflowReview, nil
}

// concludeScheduledWorkflowRun writes a verdict grain reached about the
// run rather than one the run reached about the tree -- never started,
// lost, or waited out -- and files the review of it at once. Recorded
// before the review is filed, so a restart between the two re-files
// nothing and loses nothing.
func concludeScheduledWorkflowRun(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time, conclusion ci.Conclusion, detail string) error {

	row.Conclusion, row.Detail = string(conclusion), detail
	row.FinishedAt = &now
	if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}
	log.Printf("orchestrator: schedule %s has no verdict on its %s workflow run: %s",
		row.ScheduleID, row.Stem, detail)
	return fileWorkflowReview(ctx, deps, row, now)
}

func scheduledWorkflowLostDetail(known bool) string {
	if known {
		return "the run was interrupted by the daemon that was running it stopping"
	}
	return "the run is no longer known to this daemon"
}

// fileWorkflowReview files the task that reads a firing whose run has
// ended, and tells it -- as the task's first comment -- which run it is
// about and how that run ended.
//
// The task is filed before the row is marked as reviewed, and filing is
// guarded by a tag naming this exact firing
// (model.ScheduledWorkflowFiringTag), so a daemon that stops between the
// two comes back and finds the task rather than filing a second one.
func fileWorkflowReview(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun,
	now time.Time) error {

	sched, err := workflowReviewSchedule(ctx, deps, row)
	if err != nil {
		return err
	}
	taskID, filed, err := deps.Store.FileWorkflowReview(ctx, sched, row, deps.Config.settingsRepo(), now)
	if err != nil {
		return fmt.Errorf("orchestrator: filing the review of %s's %s run: %w",
			row.ScheduleID, row.Stem, err)
	}
	if filed {
		if err := tellTheReviewer(ctx, deps, taskID, now, workflowReviewComment(row)); err != nil {
			return err
		}
		log.Printf("orchestrator: filed %s to read schedule %s's %s run (%s)",
			taskID, row.ScheduleID, row.Stem, row.Conclusion)
	}
	// Recorded second on purpose, the way a base-branch repair is: a
	// crash between the two leaves a task the tag check finds, rather
	// than a row naming a review that was never filed -- and a row with
	// no review on it is a firing the overlap rule holds the cadence for.
	row.ReviewTaskID = taskID
	return deps.Store.PutScheduledWorkflowRun(ctx, row)
}

// workflowReviewSchedule is the schedule this firing came from, or one
// reconstructed from the firing's own row when that schedule has gone
// since -- a workflow whose cron was removed, or a repository whose
// declarations were switched off, between the run and its review. The run
// happened either way and still owes a report, and the row carries
// everything composing the review needs.
func workflowReviewSchedule(ctx context.Context, deps Deps,
	row model.ScheduledWorkflowRun) (model.Schedule, error) {

	sched, err := deps.Store.GetSchedule(ctx, row.ScheduleID)
	if err != nil {
		return model.Schedule{}, fmt.Errorf("orchestrator: reading schedule %s: %w", row.ScheduleID, err)
	}
	if sched != nil {
		return *sched, nil
	}
	templateID := model.WorkflowReviewTemplateID(row.Stem)
	return model.Schedule{
		ID:                   row.ScheduleID,
		Target:               row.Repo,
		Base:                 row.Branch,
		TemplateID:           &templateID,
		AutoApproveProposals: true,
	}, nil
}

// forgetUndeclaredWorkflowRun drops a settled firing whose schedule no
// longer exists. A settled row is otherwise left where it is until the
// next firing replaces it -- it is the schedule's own record of what it
// last did -- but a schedule that has gone will never fire again, so
// nothing would ever replace this one.
func forgetUndeclaredWorkflowRun(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun) error {
	sched, err := deps.Store.GetSchedule(ctx, row.ScheduleID)
	if err != nil {
		return fmt.Errorf("orchestrator: reading schedule %s: %w", row.ScheduleID, err)
	}
	if sched != nil {
		return nil
	}
	return deps.Store.DeleteScheduledWorkflowRun(ctx, row.ScheduleID)
}

// workflowReviewComment is what the reviewer is told about its own
// firing: which run, where, and how it ended -- the fact that makes this
// task worth a run's judgement rather than a wait.
//
// Whatever happened, the task hears it. A run that could not be started,
// or that nothing ever concluded, is a comment saying so rather than a
// silence, because the alternative is an agent hunting for a run that
// was never asked for.
//
// Three shapes, because the three want opposite work out of the agent
// and the prompt cannot tell which it has (model.workflowReviewBody
// names the same three and defers to this comment for which): a firing
// with no run is a report and nothing else; a run grain stopped waiting
// for is a run that is still there, still going, and worth more reading
// than any other night this suite can have; and an ended run is the
// ordinary case.
func workflowReviewComment(row model.ScheduledWorkflowRun) string {
	if !row.Started() {
		return fmt.Sprintf("grain could not start a run of the `%s` workflow on %s, though its "+
			"schedule `%s` came due at %s: %s. There is no run to review, so report that and "+
			"change nothing -- a nightly that has stopped running is worth saying out loud.",
			row.Stem, row.Repo, row.ScheduleID, row.FiredAt.UTC().Format(time.RFC3339), row.Detail)
	}
	if row.Unfinished {
		// The one conclusion of grain's that leaves something to read,
		// and the one worth reading hardest: a suite that hangs is
		// invisible from everywhere else, and where it hangs is in the
		// log of the job that never ended -- a log get_ci_log will
		// follow while it is still being written.
		return fmt.Sprintf("grain CI run `%s` of `%s` on %s, branch `%s`, is the run this task is "+
			"about: grain started it because its schedule `%s` came due at %s. "+
			"**It has not finished**: %s. The run is still there, and as far as grain knows it is "+
			"still going -- it was left alone rather than cancelled, because whatever it is stuck "+
			"on is the evidence. It is recorded as **%s**, which is grain saying it never got a "+
			"verdict out of this run; it is not a verdict the run reached about the tree, so do "+
			"not report it as one. Find it with `list_ci_runs` (narrow by repo and ref, or look "+
			"for this id). Read the jobs that did finish with `get_ci_log`, and then read the log "+
			"of the one that did not -- `get_ci_log` follows a log that is still being written. "+
			"Which job never ended, and what it was doing when it stopped producing output, is "+
			"your report.",
			row.RunID, row.Workflow, row.Repo, row.Branch, row.ScheduleID, row.FiredAt.UTC().Format(time.RFC3339),
			row.Detail, row.Conclusion)
	}
	when := ""
	if row.FinishedAt != nil && row.AttemptedAt != nil {
		when = fmt.Sprintf(" It ran for %s.", row.FinishedAt.Sub(*row.AttemptedAt).Round(time.Second))
	}
	// "Recorded as" rather than "finished as", because this line also
	// carries a conclusion grain reached about a run rather than one the
	// run reached about the tree -- a run lost with the daemon that was
	// running it -- and that is exactly the one a reviewer must not read
	// as a verdict on the code.
	return fmt.Sprintf("grain CI run `%s` of `%s` on %s, branch `%s`, is the run this task is "+
		"about: grain started it because its schedule `%s` came due at %s. It is "+
		"recorded as **%s** (%s).%s Find it with `list_ci_runs` (narrow by repo and ref, or look "+
		"for this id) and read any job's output with `get_ci_log` -- the conclusion here is grain's "+
		"own record of how the run ended, and the logs are what it means.",
		row.RunID, row.Workflow, row.Repo, row.Branch, row.ScheduleID, row.FiredAt.UTC().Format(time.RFC3339),
		row.Conclusion, row.Detail, when)
}

// scheduledWorkflowPath is the workflow file to run, taken from the
// provenance grain already keeps for the declaration this schedule came out
// of (model.RepoDeclaration.Path) -- which is the workflow file itself, and
// is therefore the authoritative answer to "which file declared this
// cadence".
//
// The fallback reconstructs it from the stem, for a schedule whose
// provenance row has gone missing (a repository whose declarations were
// turned off between two reads), and for one declared under
// `.grain/schedules` with `workflow:`, whose provenance is that schedule
// file rather than the workflow (repodecl.scheduledWorkflowStem, which is
// why that key only accepts a workflow kept as `<stem>.yml`). Narrowing a
// run to a workflow that does not exist matches no jobs, so a wrong guess
// here is a run with nothing in it rather than a run of the wrong thing.
func scheduledWorkflowPath(ctx context.Context, deps Deps, row model.ScheduledWorkflowRun) string {
	if declared, err := deps.Store.GetRepoDeclaration(ctx, row.ScheduleID); err == nil && declared != nil &&
		path.Dir(declared.Path) == ci.WorkflowDir {
		return declared.Path
	}
	return ci.WorkflowDir + "/" + row.Stem + ".yml"
}

// scheduledWorkflowRef is the branch to run against: the schedule's own
// base if it names one, and otherwise the repository's default branch --
// which is the branch GitHub runs a scheduled workflow on, and the only
// branch a cron in a file could mean.
func scheduledWorkflowRef(deps Deps, row model.ScheduledWorkflowRun) (string, error) {
	if row.Branch != "" {
		return row.Branch, nil
	}
	if deps.Client == nil {
		return "", fmt.Errorf("no GitHub client to ask %s's default branch of", row.Repo)
	}
	def, err := deps.Client.DefaultBranch(row.Repo.Owner, row.Repo.Name)
	if err != nil {
		return "", fmt.Errorf("reading %s's default branch: %w", row.Repo, err)
	}
	return def, nil
}

func tellTheReviewer(ctx context.Context, deps Deps, taskID string, now time.Time, body string) error {
	if _, err := deps.Store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: model.Attribution{Actor: model.Principal{
			Kind: model.PrincipalAutomation, ID: scheduledWorkflowStarter,
		}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("orchestrator: telling %s which run to review: %w", taskID, err)
	}
	return nil
}

// fireGitHubWorkflowRunSchedules checks every enabled github_workflow_run
// schedule in run.Repo and fires matching ones for the concluded GitHub
// Actions workflow run.
func fireGitHubWorkflowRunSchedules(ctx context.Context, deps Deps, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob, now time.Time) error {
	if deps.Store == nil || run.RunID == 0 {
		return nil
	}
	schedules, err := deps.Store.ListSchedules(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing schedules for %s: %w", run.Repo, err)
	}
	for _, sched := range schedules {
		if !sched.Enabled || !sched.Target.Same(run.Repo) ||
			sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun || sched.Recurrence.GitHubWorkflowRun == nil {
			continue
		}
		if err := fireOneGitHubWorkflowRunSchedule(ctx, deps, sched, run, jobs, now); err != nil {
			log.Printf("orchestrator: firing github_workflow_run schedule %s (%s) for run %d: %v",
				sched.ID, sched.Title, run.RunID, err)
		}
	}
	return nil
}

func fireOneGitHubWorkflowRunSchedule(ctx context.Context, deps Deps, sched model.Schedule, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob, now time.Time) error {
	trig := sched.Recurrence.GitHubWorkflowRun
	if trig == nil {
		return nil
	}
	if !trig.MatchesWorkflow(run.WorkflowName, run.WorkflowPath) {
		return nil
	}
	if !trig.MatchesConclusion(run.Conclusion) {
		return nil
	}
	branch := strings.TrimPrefix(run.Ref, "refs/heads/")
	if !trig.MatchesBranch(branch, sched.Base) {
		return nil
	}

	stem := trig.WorkflowStem()
	if stem == "" {
		base := path.Base(run.WorkflowPath)
		stem = strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
		if stem == "" {
			stem = run.WorkflowName
		}
	}
	wfPath := run.WorkflowPath
	if wfPath == "" {
		wfPath = ".github/workflows/" + stem + ".yml"
	}

	conclusion := gitHubConclusionToCI(run.Conclusion)
	detail := gitHubWorkflowRunDetail(run, jobs)
	runIDStr := strconv.FormatInt(run.RunID, 10)
	started := now
	if run.StartedAt != nil && !run.StartedAt.IsZero() {
		started = *run.StartedAt
	}
	finished := now
	if run.CompletedAt != nil && !run.CompletedAt.IsZero() {
		finished = *run.CompletedAt
	}

	hasSuite := sched.SuiteID != nil && *sched.SuiteID != ""
	tmplID := ""
	if sched.TemplateID != nil {
		tmplID = *sched.TemplateID
	}
	usesWorkflowReviewTemplate := !hasSuite &&
		(tmplID == "" || tmplID == model.WorkflowReviewTemplateID(stem))

	// When a schedule uses the default workflow-review template and has no
	// explicit conclusions filter on the trigger, respect sched.WorkflowReview
	// (default OnFailure). If the user explicitly listed conclusions on the
	// trigger (e.g. conclusions: [failure, success]), trig.MatchesConclusion
	// above already checked that and the user asked for a task on success too.
	if usesWorkflowReviewTemplate && len(trig.Conclusions) == 0 &&
		conclusion == ci.ConclusionSuccess && !sched.WorkflowReview.Always() {
		row := model.ScheduledWorkflowRun{
			ScheduleID:      sched.ID,
			Repo:            run.Repo,
			Branch:          branch,
			Workflow:        wfPath,
			Stem:            stem,
			FiredAt:         now,
			RunID:           runIDStr,
			Attempts:        max(1, run.Attempt),
			AttemptedAt:     &started,
			FinishedAt:      &finished,
			Conclusion:      string(conclusion),
			Detail:          detail,
			ReviewNotNeeded: true,
		}
		if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
			return err
		}
		sched.LastRunAt = &now
		return deps.Store.PutSchedule(ctx, sched)
	}

	// Overlap check: skip if an earlier review/firing for this schedule is still open.
	if sched.Overlap != model.OverlapAllow {
		openTask, err := deps.Store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(sched.ID))
		if err != nil {
			return err
		}
		if openTask != nil {
			resolved := now
			_, _ = deps.Store.RecordScheduleSkip(ctx, model.ScheduleSkip{
				ScheduleID: sched.ID,
				Occurrence: now,
				At:         now,
				Reason:     model.SuppressedByOpenTask,
				TaskID:     openTask.ID,
				Outcome:    model.ScheduleSkipDropped,
				ResolvedAt: &resolved,
			})
			return nil
		}
	}

	row := model.ScheduledWorkflowRun{
		ScheduleID:  sched.ID,
		Repo:        run.Repo,
		Branch:      branch,
		Workflow:    wfPath,
		Stem:        stem,
		FiredAt:     now,
		RunID:       runIDStr,
		Attempts:    max(1, run.Attempt),
		AttemptedAt: &started,
		FinishedAt:  &finished,
		Conclusion:  string(conclusion),
		Detail:      detail,
	}
	if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}

	if hasSuite {
		_, err := deps.Store.FireScheduleNow(ctx, sched, now, deps.Config.settingsRepo())
		return err
	}

	summarySection := gitHubWorkflowRunSummarySection(run, jobs)
	schedForFiling := sched
	if usesWorkflowReviewTemplate && tmplID == "" && schedForFiling.Title == "" && schedForFiling.Body == "" {
		defaultTmpl := model.WorkflowReviewTemplateID(stem)
		schedForFiling.TemplateID = &defaultTmpl
	}

	taskID, filed, err := deps.Store.FileWorkflowReview(ctx, schedForFiling, row, deps.Config.settingsRepo(), now)
	if err != nil {
		return fmt.Errorf("filing workflow review for schedule %s: %w", sched.ID, err)
	}
	if filed {
		if task, err := deps.Store.GetTask(ctx, taskID); err == nil && task != nil {
			if !strings.Contains(task.Body, summarySection) {
				task.Body = strings.TrimRight(task.Body, "\n") + "\n\n" + summarySection
				_ = deps.Store.PutTask(ctx, *task)
			}
		}
		if err := tellTheReviewer(ctx, deps, taskID, now, gitHubWorkflowReviewComment(row, run, jobs)); err != nil {
			return err
		}
	}
	row.ReviewTaskID = taskID
	if err := deps.Store.PutScheduledWorkflowRun(ctx, row); err != nil {
		return err
	}
	sched.LastRunAt = &now
	return deps.Store.PutSchedule(ctx, sched)
}

func gitHubConclusionToCI(conclusion string) ci.Conclusion {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "success":
		return ci.ConclusionSuccess
	case "cancelled":
		return ci.ConclusionCancelled
	case "skipped", "neutral":
		return ci.ConclusionSkipped
	default:
		return ci.ConclusionFailure
	}
}

func gitHubWorkflowReviewComment(row model.ScheduledWorkflowRun, run ConcludedGitHubWorkflowRun, jobs []model.GitHubCIJob) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub Actions workflow `%s` (`%s`) on `%s` concluded **`%s`**",
		row.Stem, row.Workflow, row.Branch, run.Conclusion)
	if run.Attempt > 1 {
		fmt.Fprintf(&b, " on attempt %d", run.Attempt)
	}
	fmt.Fprintf(&b, ": run `%s`", row.RunID)
	if run.HTMLURL != "" {
		fmt.Fprintf(&b, " (%s)", run.HTMLURL)
	}
	b.WriteString(".\n\n")
	fmt.Fprintf(&b, "Run `grain ci log %s` to read the stored job failure tails.\n\n", row.RunID)
	b.WriteString(gitHubWorkflowRunSummarySection(run, jobs))
	return b.String()
}
