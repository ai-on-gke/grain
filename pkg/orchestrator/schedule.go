package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// reconcileSchedule fires every schedule whose interval has come due --
// filing a task, starting a suite run, or recording a workflow run to be
// started and reviewed (reconcileScheduledWorkflows), depending on which
// of the three that schedule names (model.Store.FireSchedule, and the
// firing itself lives there rather than here so that pkg/ui's "Run now"
// can make the same firing without importing this package --
// model.ScheduleFiring's own doc comment on why).
//
// One schedule failing to file (or a store error while checking it)
// does not stop the others -- Reconciler's own doc comment on why each
// one re-reads the store every cycle applies again one level down:
// a schedule this cycle could not fire is one next cycle tries again,
// exactly as it stood before.
func reconcileSchedule(ctx context.Context, deps Deps, now time.Time) error {
	due, err := deps.Store.DueSchedules(ctx, now)
	if err != nil {
		return fmt.Errorf("orchestrator: listing due schedules: %w", err)
	}
	var errs []error
	if err := recordPausedSkips(ctx, deps, now); err != nil {
		errs = append(errs, err)
	}
	if len(due) == 0 {
		return errors.Join(errs...)
	}

	stateRepo := deps.Config.settingsRepo()

	for _, sched := range due {
		loc := sched.Location()
		if sched.ID == model.PipelineStewardTemplateID {
			// The precondition, before any sandbox is spent: a quiet
			// queue drops the occurrence (gatePipelineSteward).
			skipped, err := gatePipelineSteward(ctx, deps, sched, now, loc)
			if err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, err))
				continue
			}
			if skipped {
				continue
			}
		}
		firing, err := deps.Store.FireSchedule(ctx, sched, now, loc, stateRepo)
		if err != nil {
			// Written down on the schedule, and logged once per distinct
			// failure rather than every cycle the same failure repeats
			// (model.Store.RecordScheduleFiringFailure). Only a failure
			// that cannot be written down either is this cycle's error.
			news, rerr := deps.Store.RecordScheduleFiringFailure(ctx, sched, now, err)
			switch {
			case rerr != nil:
				errs = append(errs, fmt.Errorf("orchestrator: firing schedule %s: %w", sched.ID, err),
					fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, rerr))
			case news:
				log.Printf("orchestrator: firing schedule %s failed: %v; its %s firing is held and "+
					"retried every cycle, and this is not logged again until the failure changes",
					sched.ID, err, sched.NextRunAt.UTC().Format(time.RFC3339))
			}
			continue
		}
		// What the firing filed is parked, rather than dispatched into a
		// refused clone, when the schedule's team cannot reach a
		// repository it names (parkUnreached).
		if err := parkScheduleFiring(ctx, deps, firing, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, err))
		}
		if firing.OpenReview != "" && !firing.Skipped {
			log.Printf("orchestrator: schedule %s came due with task %s -- the review of its previous "+
				"firing -- still open, and that review has been open past the hold; firing anyway "+
				"rather than letting an unread review stop the schedule, and saying so on %s",
				sched.ID, firing.OpenReview, firing.OpenReview)
		}
		if firing.Skip != nil && firing.SkipRecorded {
			logScheduleSkip(sched, *firing.Skip)
		}
		// Every cycle rather than only the first: the skip's blocker and
		// its state are re-read each time, so a blocker that only comes
		// to wait on a person later (a run that finishes into
		// awaiting_submit) is noted then. The store notes each blocker once.
		if firing.Skip != nil {
			if _, err := deps.Store.NoteHeldScheduleOnBlocker(ctx, sched, *firing.Skip, now); err != nil {
				errs = append(errs, fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, err))
			}
		}
		if firing.OffTargetRepos != nil {
			log.Printf("orchestrator: schedule %s fires into %s, which is no longer on this deployment's "+
				"target repo list; skipping this firing rather than filing a task that would only park. "+
				"It fires again by itself once the repo is back on the list",
				sched.ID, *firing.OffTargetRepos)
		}
		if firing.StandingWorkOff != nil {
			log.Printf("orchestrator: schedule %s skipped: %s; its cadence moved on to the next occurrence",
				sched.ID, model.StandingWorkOffReason(*firing.StandingWorkOff))
		}
	}
	return errors.Join(errs...)
}

// logScheduleSkip is the one line a skipped occurrence gets in the log
// (model.ScheduleSkip, grain/task-3560) -- once per occurrence, not once
// per poll that re-skips it.
func logScheduleSkip(sched model.Schedule, skip model.ScheduleSkip) {
	what := "held: it fires once as soon as that clears"
	if skip.Outcome == model.ScheduleSkipDropped {
		what = "dropped: its cadence moved on to the next occurrence"
	}
	log.Printf("orchestrator: schedule %s %s; the %s firing is %s",
		sched.ID, skip.Summary(), skip.Occurrence.UTC().Format(time.RFC3339), what)
}

// recordPausedSkips records the occurrence of every paused schedule that
// has come due (model.SkippedPaused). DueSchedules never returns them, so
// without this a paused schedule's missed firings are the one skip
// nothing writes down. Its NextRunAt stays where it was while paused, so
// this is one record per pause, and resuming fires it once.
func recordPausedSkips(ctx context.Context, deps Deps, now time.Time) error {
	paused, err := deps.Store.PausedDueSchedules(ctx, now)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	var errs []error
	for _, sched := range paused {
		skip := model.ScheduleSkip{ScheduleID: sched.ID, Occurrence: sched.NextRunAt, At: now,
			Reason: model.SkippedPaused}
		created, err := deps.Store.RecordScheduleSkip(ctx, skip)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: schedule %s: %w", sched.ID, err))
			continue
		}
		if created {
			logScheduleSkip(sched, skip)
		}
	}
	return errors.Join(errs...)
}

// parkScheduleFiring is parkUnreached for whatever one firing filed: its
// task, or the first pass of the suite run it started.
func parkScheduleFiring(ctx context.Context, deps Deps, firing model.ScheduleFiring, now time.Time) error {
	if deps.GitReach == nil {
		return nil
	}
	var ids []string
	if firing.TaskID != "" {
		ids = append(ids, firing.TaskID)
	}
	if firing.SuiteRunID != 0 {
		run, err := deps.Store.GetSuiteRun(ctx, firing.SuiteRunID)
		if err != nil {
			return fmt.Errorf("reading suite run %d: %w", firing.SuiteRunID, err)
		}
		if run != nil {
			ids = append(ids, suiteRunTaskIDs(*run, run.CurrentPass())...)
		}
	}
	return parkUnreached(ctx, deps.Store, deps.GitReach, ids, now)
}
