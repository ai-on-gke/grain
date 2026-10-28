package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// UpgradePrincipal is the automation principal an upgrade acts as --
// both when it files a check of itself afterwards (upgradecheck.go) and
// when it holds the queue while it runs (UpgradeHold below).
//
// Exported, unlike the other principals here, because the hold and its
// release are taken by two different processes: the one performing the
// upgrade closes the gate, and the one its restart brings up opens it
// again. The principal recorded in the row is the only thing tying those
// two acts together, so it has to be nameable from outside this package
// (cmd/grain wires both halves).
var UpgradePrincipal = model.UpgradePrincipal

// upgradeHoldTimeout bounds each of the two store calls a hold makes.
// They are a single-row read and a single-row write against an embedded
// store, so this is not a budget for them to spend -- it is there so
// that a store which has stopped answering entirely cannot leave an
// upgrade waiting on it.
const upgradeHoldTimeout = 30 * time.Second

// UpgradeHold is upgrade.Config.Hold for a deployment with a store: it
// closes the dispatch gate for the duration of an upgrade and hands back
// the release.
//
// An upgrade ends in a restart, and a restart discards every run in
// flight -- RecoverOrphanedRuns finishes their rows as orphaned on the
// way back up, which is correct and does not give the agent's work back.
// Nothing can be done about the runs that were already going when the
// button was pressed. What can be stopped is dispatch handing out *new*
// work in the minutes an upgrade takes, which is what this does: a run
// dispatched ten minutes into an upgrade is a run killed six minutes
// later, having achieved nothing.
//
// It takes no hold at all -- returning a nil release, which upgrade's
// own hold reads as "nothing to let go of" -- when this deployment is
// already paused by somebody else: the queue is shut either way,
// SetPipelinePause would replace the row rather than stack on it, and
// the release below would then be lifting a pause this upgrade never
// took.
//
// A pause that is one of grain's own upgrade holds is a different matter
// and is taken over rather than deferred to (heldForUpgrade). Two shapes
// reach this: a scheduled upgrade's own hold, which has been keeping the
// queue shut precisely so that this upgrade could start
// (reconcileScheduledUpgrade), and a hold this same upgrade path took
// moments ago. Neither is anybody's decision to preserve, and treating
// them as such is what used to leave the queue shut after a scheduled
// upgrade failed -- the hold was already there, so this took none, so
// there was no release to call on the way out, and nothing else lifts a
// pause naming no task.
func UpgradeHold(store *model.Store) func(string) (func(), error) {
	if store == nil {
		return nil
	}
	return func(branch string) (func(), error) {
		// context.Background() and a deadline of its own, deliberately
		// not whatever the caller is doing about deadlines: the gate has
		// to close, and later has to open, regardless.
		ctx, cancel := context.WithTimeout(context.Background(), upgradeHoldTimeout)
		defer cancel()

		pause, err := store.GetPipelinePause(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading whether this deployment is already paused: %w", err)
		}
		if pause != nil && !heldForUpgrade(pause) {
			log.Printf("orchestrator: upgrading to %s without holding the queue: it is already paused (%s)",
				branch, pause.Reason)
			return nil, nil
		}
		if err := store.SetPipelinePause(ctx, model.PipelinePause{
			PausedAt: time.Now().UTC(),
			Reason: fmt.Sprintf(
				"upgrading to %s -- a restart is coming, and a run started now would be discarded by it",
				branch),
			By: UpgradePrincipal,
			// No task to wait for: this hold is lifted by the upgrade
			// ending, one way or the other, and not by anything
			// finishing. That is also what keeps reconcilePipelinePause
			// from lifting it out from under the upgrade -- it leaves a
			// pause naming no task alone.
			UntilTaskID: "",
		}); err != nil {
			return nil, fmt.Errorf("holding the queue while this deployment upgrades: %w", err)
		}
		log.Printf("orchestrator: holding the queue while this deployment upgrades to %s", branch)
		return func() { releaseUpgradeHold(store, branch) }, nil
	}
}

// releaseUpgradeHold is the release UpgradeHold hands back, for the
// paths that end with this deployment still running the build it had
// (upgrade.Config.Hold's own table of them).
//
// It is the same conditional clear ReleaseUpgradeHold performs, and for
// the same reason: between the hold being taken and this running, an
// operator may have paused this deployment themselves -- through a UI
// this process never stopped serving, since this is a path where no
// restart happened -- and their pause is not an upgrade's to lift.
//
// Errors are logged rather than returned: nothing is waiting on the
// answer (upgrade calls this for its side effect, on its way out), and
// the worst case is a gate a person has to open with `grain pause
// -resume`, which the banner is already telling them about.
func releaseUpgradeHold(store *model.Store, branch string) {
	ctx, cancel := context.WithTimeout(context.Background(), upgradeHoldTimeout)
	defer cancel()
	cleared, err := store.ClearPipelinePauseTakenBy(ctx, UpgradePrincipal)
	if err != nil {
		log.Printf("orchestrator: releasing the queue held for the upgrade to %s: %v", branch, err)
		return
	}
	if cleared {
		log.Printf("orchestrator: the upgrade to %s is over -- the pipeline is picking work up again", branch)
	}
}

// ReleaseUpgradeHold opens a gate an upgrade closed and its own restart
// carried it past -- the far side of UpgradeHold, and a startup pass
// beside RecoverOrphanedRuns rather than anything the cycle does.
//
// It has to be a startup pass because the success path of an upgrade
// cannot release its own hold: it ends by exec'ing the restart command,
// which kills the process holding the release. And nothing else would
// ever lift it -- reconcilePipelinePause returns early on a pause naming
// no task, since a pause naming no task is an operator's own and only a
// person lifts one. Without this, the first successful upgrade would
// leave the deployment paused forever.
//
// Clearing unconditionally would be wrong, and this does not: it clears
// only a pause this deployment's own upgrades take (UpgradePrincipal,
// naming no task), leaving an operator's pause and the upgrade check's
// own task-scoped pause exactly where they are. What makes even that
// narrow clear safe is that nobody can have taken a pause in the gap:
// `grain pause -set` and the banner both go through this daemon's API,
// which is not serving while it is down.
//
// It runs before the first cycle for the same reason RecoverOrphanedRuns
// does -- both are cleaning up after the process that used to hold this
// data directory, and a queue still held when the first cycle ticks is a
// queue that does not move.
func ReleaseUpgradeHold(ctx context.Context, store *model.Store) error {
	if store == nil {
		return nil
	}
	cleared, err := store.ClearPipelinePauseTakenBy(ctx, UpgradePrincipal)
	if err != nil {
		return fmt.Errorf("orchestrator: releasing the queue an upgrade held: %w", err)
	}
	if cleared {
		log.Print("orchestrator: an upgrade held the queue over this restart -- the pipeline is picking work up again")
	}
	return nil
}
