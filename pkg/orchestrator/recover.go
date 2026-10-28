package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// RecoverOrphanedRuns finishes every task_run a previous process left
// with no `finished_at` -- bwsalmon/agents#425's gap: task_state
// (schema.go) reads a run with a live row as 'running' regardless of
// whether the process that started it still exists, so a daemon killed
// mid-run otherwise leaves that task stuck there forever, with no route
// back to 'queued' and no way for dispatch.Cycle to ever try it again.
//
// This is a startup pass, not a periodic Reconciler, and deliberately
// so: a daemon calls it exactly once, in pkg/cli/daemon.go's own run(),
// before RunCycle ever runs for the first time. At that specific moment a
// live task_run row cannot belong to this process -- it hasn't dispatched
// anything yet -- so it can only be left over from whatever last held
// this store's own -data-dir, crashed or otherwise stopped mid-run. A
// periodic reconciler re-run on every tick would have no such guarantee:
// the very run reconcileDispatch is in the middle of driving right now
// also has no `finished_at` yet, and there is nothing in the row itself
// that distinguishes "still legitimately running" from "abandoned" once
// a cycle is free to interleave with this pass. Restricting recovery to
// startup sidesteps needing a lease/heartbeat mechanism to tell the two
// apart at all.
//
// Finishing the run (model.InterruptedOutcome) is what actually frees
// the task: task_state stops reading it as 'running' the moment
// `finished_at` is set, which drops it back to 'queued' for dispatch.Cycle
// to pick up on its own next tick. Interrupted rather than failed, and so
// no strike against task_streak: what ended the run was the daemon
// driving it stopping -- an upgrade, a restart, a drain that ran out --
// which says nothing about the task. The conversation the run was in is
// left exactly where the run recorded it (model.SuspendedSession, written
// as soon as the agent named it), so the next dispatch resumes it, and
// the task is told why it went back to the queue (noteInterrupted).
//
// What ProcessResult would have done with that
// run's result -- relaying a comment, opening a pull request -- is gone
// with the crashed process, except for the one part GitHub itself still
// remembers: a branch the run may have already pushed before it died.
// recoverRun checks for exactly that, mirroring ProcessResult's own
// "pushed but not yet turned into a PR" handling, so a run that crashed
// after pushing but before finishWithPullRequest ever got to run does not
// leave a real branch permanently stranded and unopened.
func RecoverOrphanedRuns(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	return RecoverOrphanedRunsExcept(ctx, store, client, now, nil)
}

// RecoverOrphanedRunsExcept is RecoverOrphanedRuns sparing the live rows
// adopted names: runs RecoverRunHosts matched to a run host that is still
// going, which this daemon resumes (ResumeAdoptedRun) rather than finishes.
func RecoverOrphanedRunsExcept(ctx context.Context, store *model.Store, client github.Client, now time.Time,
	adopted map[string]bool) error {
	all, err := store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	var runs []model.Run
	for _, r := range all {
		if !adopted[r.ID] {
			runs = append(runs, r)
		}
	}

	if len(runs) > 0 {
		// The other half of the journal line the stopping daemon wrote
		// (daemon.DrainInFlight): the runs it gave up waiting on, or every
		// run, if it was killed before it could drain at all.
		log.Printf("orchestrator: recovering %d run(s) the previous daemon was stopped in the middle of; "+
			"each is finished as %s and its task queued again without a strike", len(runs), model.InterruptedOutcome)
	}
	var errs []error
	for _, r := range runs {
		if err := recoverRun(ctx, store, client, r, now, RecoveredInterruptedDetail); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: recovering orphaned run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// recoverRun is RecoverOrphanedRuns' per-run decision: finish r as
// interrupted first -- so a task_id lookup or a GitHub call failing below
// still leaves the task dispatchable again rather than stuck exactly
// where it started -- then check whether its run had already pushed a
// branch worth turning into a pull request.
func recoverRun(ctx context.Context, store *model.Store, client github.Client, r model.Run, now time.Time, detail string) error {
	if err := store.FinishRun(ctx, r.ID, now, model.InterruptedOutcome, detail); err != nil {
		return fmt.Errorf("finishing: %w", err)
	}
	noteInterrupted(ctx, store, r.TaskID, r.ID, now)

	task, err := store.GetTask(ctx, r.TaskID)
	if err != nil {
		return fmt.Errorf("reading task %s: %w", r.TaskID, err)
	}
	if task == nil {
		return nil
	}
	obs, err := store.GetObservation(ctx, r.TaskID)
	if err != nil {
		return fmt.Errorf("reading observation for %s: %w", r.TaskID, err)
	}
	if obs.RepairInFlight() || (r.Attempt > 1 && hasFixesLink(*task)) {
		return nil
	}
	_, err = salvagePushedBranch(ctx, store, client, *task, now)
	// A branch with no commits over its base is not a recovery failure to
	// report: salvagePushedBranch has already said so on the task and
	// parked it (noteEmptyBranch), and this run's own row was finished as
	// interrupted above -- the truthful outcome for a process that died,
	// whatever its branch turned out to hold. Reporting it would put a
	// permanent condition into RecoverOrphanedRuns' joined error on every
	// daemon startup, describing a task nothing is going to retry.
	if _, empty := emptyBranch(err); empty {
		return nil
	}
	return err
}

func hasFixesLink(task model.Task) bool {
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			return true
		}
	}
	return false
}

// RecoveredInterruptedDetail is the task_run.detail recoverRun records:
// the run was not finished by the process that started it, but by the
// next one, at startup.
const RecoveredInterruptedDetail = "the daemon driving this run was stopped before it finished " +
	"(an upgrade or a restart); recovered at the next daemon startup and re-queued without a strike"

// noteInterrupted tells a task, in its own conversation, that its run was
// cut off by the daemon stopping rather than by anything the task did --
// the one thing a person reading the task later needs to make sense of a
// run that ended half way and a task that went straight back to the
// queue. The next run reads it too, and it is true for that run as well:
// the sandbox it would have found is gone, though the conversation is
// not.
//
// Best effort, and logged rather than returned: the note explains an
// interruption, and failing to write it must not stand in the way of the
// task being queued again.
func noteInterrupted(ctx context.Context, store *model.Store, taskID, runID string, now time.Time) {
	body := fmt.Sprintf(
		"Run %s was interrupted: the grain daemon running it was stopped for an upgrade or a restart "+
			"before it finished. Nothing about this task caused it, so it does not count against the "+
			"task's failure streak, and the task has been queued again. The next run picks the "+
			"conversation back up where the agent's framework kept it, but in a fresh sandbox: "+
			"anything the interrupted run wrote and did not push is gone.", runID)
	ctx, cancel := context.WithTimeout(ctx, runCleanupTimeout)
	defer cancel()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		log.Printf("orchestrator: run %s: noting on task %s that the daemon stopped under it: %v", runID, taskID, err)
	}
}
