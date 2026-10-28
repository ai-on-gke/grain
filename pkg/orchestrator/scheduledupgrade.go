package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// scheduledUpgradePrincipal is who holds the queue while a deployment
// waits to apply an upgrade somebody scheduled
// (model.ScheduledUpgradePrincipal, which is where it lives and why).
//
// A principal of its own, beside UpgradePrincipal rather than reusing
// it, because the two holds are let go by different things. An upgrade's
// own hold is lifted by that upgrade ending -- by the release it hands
// back, or by ReleaseUpgradeHold after the restart it performs. This one
// is lifted by the schedule going away, whether that is an operator
// cancelling it or the upgrade it was waiting for finally starting, and
// nothing about a restart should open it: a deployment restarted while
// waiting to drain is a deployment that is still waiting to drain, and
// ReleaseUpgradeHold would otherwise let the queue go the moment the
// process came back.
var scheduledUpgradePrincipal = model.ScheduledUpgradePrincipal

// heldForUpgrade reports whether pause is one of the two holds this
// deployment's own upgrade paths take, as against an operator's decision
// or the upgrade check's own task-scoped pause.
//
// Naming no task is half of the test: the upgrade check pauses as
// UpgradePrincipal too, and its pause is lifted by the check finishing
// (reconcilePipelinePause). Taking that one over would cut a check short
// and leave the queue open under it.
func heldForUpgrade(pause *model.PipelinePause) bool {
	return pause.HeldForUpgrade()
}

// upgradeUnderway reports whether an upgrade of this deployment is
// running or scheduled, whoever's pause is in the row. It is the other
// half of keeping Run now behind an upgrade (reconcileDispatch): the
// row only says HeldForUpgrade while one of the upgrade paths owns it,
// and neither takes the row over from an operator's pause (UpgradeHold,
// reconcileScheduledUpgrade) -- nor keeps it, once somebody pauses again
// on top of the hold. In manual mode that is the usual case, not an
// edge, and a Run now let through it would be a run the restart discards
// (or, for a scheduled upgrade, a drain that never finishes).
func upgradeUnderway(ctx context.Context, deps Deps) (bool, error) {
	if deps.Store != nil {
		scheduled, err := deps.Store.GetScheduledUpgrade(ctx)
		if err != nil {
			return false, fmt.Errorf("orchestrator: %w", err)
		}
		if scheduled != nil {
			return true, nil
		}
	}
	if deps.Config.UpgradeStatus == nil {
		return false, nil
	}
	status, err := deps.Config.UpgradeStatus()
	if err != nil {
		return false, fmt.Errorf("orchestrator: reading whether an upgrade is running: %w", err)
	}
	return status.Phase == upgrade.PhaseRunning, nil
}

// reconcileScheduledUpgrade is the whole of "upgrade once this
// deployment has finished what it is doing" (grain/task-156).
//
// An upgrade ends in a restart and a restart discards every run in
// flight: the agents' work is lost and their tasks are queued again
// (UpgradeHold's own doc comment, and the run count the upgrade pane
// warns with). On a busy deployment that leaves an operator two bad
// options -- pay that, or sit watching the count and press the button by
// hand when it reaches zero. A scheduled upgrade is the third: the queue
// shuts now, the work already under way is left to finish, and the
// upgrade starts when there is nothing left to lose.
//
// What it does each tick, level-triggered like every reconciler here so
// that a daemon restarted mid-wait reaches the same conclusions the
// process that took the decision would have:
//
//   - Nothing scheduled: let go of the hold, if the hold standing there
//     is this reconciler's own. That is how a cancellation takes effect
//     -- the API clears the row and says so, and the queue opens on the
//     next tick without the API having to know what a pause is.
//   - Scheduled, and this deployment is not paused: hold the queue. This
//     is also what re-closes it after a restart, since ReleaseUpgradeHold
//     opens an upgrade's hold at startup and cannot tell one of those
//     from this.
//   - Scheduled, and still busy: wait. No deadline, deliberately -- see
//     scheduledUpgradeWait.
//   - Scheduled and idle: hand the hold over to the upgrade itself,
//     forget the schedule, and start it.
//
// It runs before "dispatch" for the reason reconcilePipelinePause does:
// a hold taken this tick is a queue that does not move this tick.
func reconcileScheduledUpgrade(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Config.StartUpgrade == nil {
		return nil
	}
	scheduled, err := deps.Store.GetScheduledUpgrade(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	pause, err := pipelinePause(ctx, deps)
	if err != nil {
		return err
	}
	ours := pause != nil && pause.UntilTaskID == "" && pause.By == scheduledUpgradePrincipal

	if scheduled == nil {
		if !ours {
			return nil
		}
		cleared, err := deps.Store.ClearPipelinePauseTakenBy(ctx, scheduledUpgradePrincipal)
		if err != nil {
			return fmt.Errorf("orchestrator: releasing the queue held for a scheduled upgrade: %w", err)
		}
		if cleared {
			log.Print("orchestrator: no upgrade is scheduled any more -- the pipeline is picking work up again")
		}
		return nil
	}

	waiting, err := scheduledUpgradeWait(ctx, deps)
	if err != nil {
		return err
	}

	// Somebody else's pause -- an operator's, or the upgrade check's --
	// is left exactly where it is: the queue is shut either way, so
	// there is nothing this needs from replacing it, and replacing it
	// would mean lifting a pause that was never this reconciler's when
	// the schedule ends.
	if pause == nil {
		if err := deps.Store.SetPipelinePause(ctx, model.PipelinePause{
			PausedAt: now.UTC(),
			Reason: fmt.Sprintf(
				"waiting for this deployment to finish its work before upgrading to %s", scheduled.Branch),
			By: scheduledUpgradePrincipal,
			// No task to wait for: this hold ends when the schedule does,
			// which is what keeps reconcilePipelinePause from lifting it
			// -- it leaves a pause naming no task alone.
			UntilTaskID: "",
		}); err != nil {
			return fmt.Errorf("orchestrator: holding the queue for the upgrade scheduled to %s: %w",
				scheduled.Branch, err)
		}
		ours = true
		held := "this deployment is already idle"
		if waiting != "" {
			held = waiting
		}
		log.Printf("orchestrator: holding the queue until this deployment is idle, then upgrading to %s (%s)",
			scheduled.Branch, held)
	}

	why := "this deployment is idle"
	if waiting != "" {
		if deps.Config.UpgradeDrainWindow > 0 && !scheduled.RequestedAt.IsZero() && now.Sub(scheduled.RequestedAt) >= deps.Config.UpgradeDrainWindow {
			log.Printf("orchestrator: upgrade drain window %s elapsed (%s); proceeding with upgrade to %s",
				deps.Config.UpgradeDrainWindow, waiting, scheduled.Branch)
			why = fmt.Sprintf("the %s drain window has elapsed with %s", deps.Config.UpgradeDrainWindow, waiting)
		} else {
			return nil
		}
	}
	return startScheduledUpgrade(ctx, deps, *scheduled, ours, why, now)
}

// startScheduledUpgrade is what happens the tick a waiting deployment
// turns out to be idle, or its drain window runs out: the schedule stops
// being pending, the hold changes hands, and pkg/upgrade is asked to do
// the thing. why is which of the two it was, for the journal -- which
// used to say "this deployment is idle" over an upgrade that was about to
// interrupt a run.
//
// The order is the careful part.
//
// The schedule is cleared first, before anything is started. It is the
// row that means "still waiting", and an upgrade that has begun is not
// waiting: left in place across the restart the upgrade ends in, it
// would have the deployment that came up hold its queue and upgrade
// itself onto the same branch all over again, forever. If Start then
// refuses -- an upgrade an operator kicked off by hand is already
// running (upgrade.ErrUpgradeInProgress) -- the row goes straight back
// and a later tick tries again.
//
// The hold changes hands before Start rather than being left to the
// upgrade's own Hold, which would take it over a moment later from its
// own goroutine. Doing it here is what makes the handover happen inside
// this tick: with the schedule gone, the *next* tick's "nothing is
// scheduled" branch above would otherwise race that goroutine and could
// open the queue in front of a restart. Only when the hold standing there
// is this reconciler's own, though -- an operator's pause is not an
// upgrade's to relabel.
func startScheduledUpgrade(ctx context.Context, deps Deps, scheduled model.ScheduledUpgrade,
	ours bool, why string, now time.Time) error {
	if _, err := deps.Store.ClearScheduledUpgrade(ctx); err != nil {
		return fmt.Errorf("orchestrator: taking the scheduled upgrade to %s off the books: %w",
			scheduled.Branch, err)
	}
	if ours {
		if err := deps.Store.SetPipelinePause(ctx, model.PipelinePause{
			PausedAt: now.UTC(),
			Reason: fmt.Sprintf(
				"upgrading to %s -- a restart is coming, and a run started now would be discarded by it",
				scheduled.Branch),
			By:          UpgradePrincipal,
			UntilTaskID: "",
		}); err != nil {
			// Logged rather than returned, and the upgrade goes ahead:
			// the queue is still held (by this reconciler's own pause,
			// which is what failed to be replaced), and the upgrade's own
			// Hold takes it over from there.
			log.Printf("orchestrator: handing the queue's hold over to the upgrade to %s: %v",
				scheduled.Branch, err)
		}
	}
	if err := deps.Config.StartUpgrade(scheduled.Branch); err != nil {
		log.Printf("orchestrator: %s, but the upgrade to %s could not be started (%v) "+
			"-- it stays scheduled and will be tried again", why, scheduled.Branch, err)
		if rerr := deps.Store.SetScheduledUpgrade(ctx, scheduled); rerr != nil {
			return fmt.Errorf("orchestrator: putting the upgrade to %s back on the books: %w",
				scheduled.Branch, rerr)
		}
		return nil
	}
	log.Printf("orchestrator: %s -- starting the upgrade to %s scheduled by %s",
		why, scheduled.Branch, scheduled.By.ID)
	return nil
}

// scheduledUpgradeWait says what is still being waited for, or "" for a
// deployment with nothing left to lose to a restart.
//
// What counts as busy is exactly what a restart would throw away and
// have to be done again: a run an agent is working in
// (Store.BusyRunCount), and a job of grain's own CI that has not
// concluded (Store.LiveCIRunCount). What does not count is everything a
// restart costs nothing -- a task waiting on somebody to answer a
// question, a pull request sitting in the merge queue, a finished task,
// a console session whose agent is waiting for a person to type. That
// last one is the reason BusyRunCount is not simply LiveRunCount: a
// standing session is a live run that may never end, and a deployment
// with the Director open would otherwise never be idle and a scheduled
// upgrade would never land.
//
// There is deliberately no deadline. A pause taken to check an upgrade
// has one (pipelinePauseDeadline) because the thing it waits for may
// never happen; this waits for runs to finish, which they do -- every
// run is bounded by Config.maxRunRuntime, and CI jobs by their own
// timeouts -- so "still busy" is a deployment that is working, not one
// that is stuck. Giving up after some hours would mean either abandoning
// an upgrade an operator asked for or restarting on top of the very work
// this was supposed to protect, and both are worse than a wait an
// operator can end at any time by cancelling the schedule or pressing
// Upgrade. An operator who would rather have the deadline sets one:
// -upgrade-drain-window (Config.UpgradeDrainWindow), which
// reconcileScheduledUpgrade reads; the runs it cuts short end
// model.InterruptedOutcome and are dispatched again after the restart.
func scheduledUpgradeWait(ctx context.Context, deps Deps) (string, error) {
	runs, err := deps.Store.BusyRunCount(ctx)
	if err != nil {
		return "", fmt.Errorf("orchestrator: %w", err)
	}
	ciRuns, err := deps.Store.LiveCIRunCount(ctx)
	if err != nil {
		return "", fmt.Errorf("orchestrator: %w", err)
	}
	return busyDescription(runs, ciRuns), nil
}

// busyDescription is the journal's sentence for what a scheduled
// upgrade is waiting on, and "" for nothing at all -- which is also how
// its caller spells "this deployment is idle". The pane says the same
// thing in its own words from the counts GET /api/upgrade reports; there
// is nothing here for it to import.
func busyDescription(runs, ciRuns int) string {
	switch {
	case runs > 0 && ciRuns > 0:
		return fmt.Sprintf("%s and %s still to finish", plural(runs, "run"), plural(ciRuns, "CI run"))
	case runs > 0:
		return plural(runs, "run") + " still to finish"
	case ciRuns > 0:
		return plural(ciRuns, "CI run") + " still to finish"
	}
	return ""
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
