package demo

// demo_schedules.go is `grain demo -schedules`: the daemon's own
// "schedule" reconciler, run every -reconcile interval (default 2s)
// against the demo's store, so a schedule that comes due fires -- or,
// into a repository whose standing work is switched off, records a skip
// (model.Store.suppressStandingWorkOff) -- with nobody pressing Run now.
//
// Without it nothing in a demo fires a schedule on its cadence: Run now
// is an extra firing that records no skip and moves no cadence, so the
// standing work switch's "a skip is recorded instead, with no catch-up on
// return" could not be watched on a running demo
// (docs/test-runs/2026-10-15-repo-tabs-standing-work.md). It is opt-in
// because the ordinary demo's seeded schedules would otherwise file tasks
// other specs count, the moment they come due. What a firing files is
// still never dispatched: the demo runs no agents.

import (
	"context"
	"log"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// demoScheduleReconcilers is what -schedules runs, by the names
// orchestrator.Reconcilers gives them.
var demoScheduleReconcilers = []string{"schedule"}

// runDemoSchedules runs the schedule reconciler once straight away and
// then every interval until ctx is done, logging a failed cycle as
// runDemoReconcile does.
func runDemoSchedules(ctx context.Context, store *model.Store, interval time.Duration) {
	deps := orchestrator.Deps{Store: store}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := runDemoReconcilers(ctx, deps, time.Now().UTC(), demoScheduleReconcilers...); err != nil {
			log.Printf("grain demo: schedules cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
