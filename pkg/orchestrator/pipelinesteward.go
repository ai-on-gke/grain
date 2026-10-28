package orchestrator

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// The pipeline steward (model.PipelineStewardTemplateID,
// docs/pipeline-steward.md) is a standing job that wakes every half hour.
// Two things in this file decide when it actually spends a sandbox:
//
//   - pipelineStewardQuiet, the precondition the schedule reconciler asks
//     before firing it. A deployment whose queue is healthy most of the
//     time pays nothing for most of those wakes.
//   - wakePipelineSteward, which the base-branch reconciler calls when a
//     red is confirmed, so the job hears about it in the same cycle
//     rather than up to half an hour later.

// pipelineStewardQuiet reports whether the pipeline steward would find
// nothing to do, and if not, the first thing it would find (why).
//
// Quiet is both of:
//
//   - every base branch grain watches has a green verdict, or none yet,
//     or one being taken -- not a failure (BaseBranchCI.Broken, which a
//     flake a confirmation cleared is not) and not an errored or
//     cancelled run, which is a branch nothing currently vouches for;
//   - no task is stalled: none whose state has outlived its patience
//     (TaskStallNote, the classification list_grain_tasks reports as
//     `stall`), and none the merge queue has stopped driving or flagged
//     as stalled (Observation.MergeQueueBlockedAt, MergeQueueStalledAt).
//
// The pipeline steward's own task is left out of the stall check: it is
// the thing being woken, not something it could unstick.
func pipelineStewardQuiet(ctx context.Context, deps Deps, now time.Time) (quiet bool, why string, err error) {
	rows, err := deps.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return false, "", fmt.Errorf("reading base-branch CI: %w", err)
	}
	for _, row := range rows {
		if row.Broken() {
			return false, fmt.Sprintf("%s %s is red", row.Repo, row.Branch), nil
		}
		switch row.Conclusion {
		case "", "success":
		default:
			return false, fmt.Sprintf("%s %s's last grain CI run ended %s", row.Repo, row.Branch, row.Conclusion), nil
		}
	}

	own, err := deps.Store.OpenTasksWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil {
		return false, "", fmt.Errorf("reading the pipeline steward's own task: %w", err)
	}
	states, err := deps.Store.States(ctx)
	if err != nil {
		return false, "", fmt.Errorf("reading task states: %w", err)
	}
	times, err := deps.Store.StateTimes(ctx)
	if err != nil {
		return false, "", fmt.Errorf("reading task state times: %w", err)
	}
	// A task an operator paused is paused by a person, never stalled
	// (model.TaskPause): it is left out of this scan entirely.
	paused, err := deps.Store.PausedSet(ctx)
	if err != nil {
		return false, "", fmt.Errorf("reading paused tasks: %w", err)
	}
	// Sorted, so which stalled task a skip names does not depend on map
	// order.
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		state := states[id]
		if _, ok := paused[id]; ok || state == model.StateClosed || slices.Contains(own, id) {
			continue
		}
		if since := times[id].Since(state); since != nil && TaskStallNote(state, *since, now) != "" {
			return false, fmt.Sprintf("task %s is stalled in %s", id, state), nil
		}
		if state != model.StateCompleted {
			continue
		}
		obs, err := deps.Store.GetObservation(ctx, id)
		if err != nil {
			return false, "", fmt.Errorf("reading task %s: %w", id, err)
		}
		if obs != nil && (obs.MergeQueueBlockedAt != nil || obs.MergeQueueStalledAt != nil) {
			return false, fmt.Sprintf("task %s is stuck on the merge queue", id), nil
		}
	}
	return true, "", nil
}

// gatePipelineSteward is the schedule reconciler's question before it
// fires the pipeline steward's schedule: skipped reports that it should
// not, because the queue is quiet, and the occurrence has already been
// dropped and the cadence advanced (model.Store.DropQuietFiring).
//
// A gate that cannot read what it is checking fires anyway. One run
// that finds nothing costs a sandbox; a red branch the gate could not
// see costs every merge until the next firing that can.
func gatePipelineSteward(ctx context.Context, deps Deps, sched model.Schedule, now time.Time,
	loc *time.Location) (skipped bool, err error) {

	quiet, why, err := pipelineStewardQuiet(ctx, deps, now)
	if err != nil {
		log.Printf("orchestrator: checking whether the pipeline steward has anything to do: %v; "+
			"firing it anyway", err)
		return false, nil
	}
	if !quiet {
		log.Printf("orchestrator: waking the pipeline steward: %s", why)
		return false, nil
	}
	skip, created, err := deps.Store.DropQuietFiring(ctx, sched, now, loc,
		"nothing for the pipeline steward to do: every base branch is green, nothing is stalled "+
			"and no merge batch is failing or held")
	if err != nil {
		return false, err
	}
	if created {
		logScheduleSkip(sched, skip)
	}
	return true, nil
}

// wakePipelineSteward fires the pipeline steward's schedule now, out of
// its cadence, because something it should hear about straight away has
// happened -- reason says what, and is the note its standing task is
// woken with (model.StandingWakeComment). Today that is a confirmed red
// base branch (confirmBaseBranchCI), which is the one place every way of
// confirming a red ends up, so a later way of confirming one wakes it
// too.
//
// A schedule that is missing or switched off is left alone: the
// deployment has said it does not want the job. A task already running
// is not woken twice (the firing is held, as an overlap is); its next
// wake reads the red for itself. Failures are logged rather than
// returned, because the caller's own work -- holding the queue and filing
// the repair -- must not depend on whether a standing job could be woken.
func wakePipelineSteward(ctx context.Context, deps Deps, reason string, now time.Time) {
	sched, err := deps.Store.GetSchedule(ctx, model.PipelineStewardTemplateID)
	if err != nil {
		log.Printf("orchestrator: reading the pipeline steward's schedule to wake it (%s): %v", reason, err)
		return
	}
	if sched == nil || !sched.Enabled {
		return
	}
	firing, err := deps.Store.FireScheduleWake(ctx, *sched, now, deps.Config.settingsRepo(), reason)
	if err != nil {
		log.Printf("orchestrator: waking the pipeline steward (%s): %v", reason, err)
		return
	}
	switch {
	case firing.Woke, firing.TaskID != "":
		log.Printf("orchestrator: woke the pipeline steward (task %s): %s", firing.TaskID, reason)
	case firing.Skipped:
		log.Printf("orchestrator: the pipeline steward is already busy, not waking it again: %s", reason)
	}
}
