// cycleTimesAdapter is the one place orchestrator's own CycleTiming and
// pkg/metrics' CycleSample are ever both in scope -- pkg/ui imports
// neither pkg/orchestrator nor a third representation of a tick -- so a
// field dropped here would silently blank a column of GET /api/metrics'
// cycles section rather than fail to build anywhere.
package daemon

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func TestCycleTimesAdapterCarriesEveryFieldAcross(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ring := orchestrator.NewCycleTimes(4)
	ring.Observe(orchestrator.CycleTiming{
		Start:        start,
		Commits:      7,
		Duration:     80 * time.Millisecond,
		DispatchWait: 20 * time.Millisecond,
		Reconcilers: []orchestrator.ReconcilerTiming{
			{Name: "schedule", Wait: time.Millisecond, Duration: 19 * time.Millisecond},
			{Name: "dispatch", Wait: 20 * time.Millisecond, Duration: 10 * time.Millisecond},
			{Name: "sync", Wait: 30 * time.Millisecond, Duration: 50 * time.Millisecond, Failed: true},
		},
	})

	history := cycleTimesAdapter{ring}.CycleTimes()
	if history.Observed != 1 || len(history.Samples) != 1 {
		t.Fatalf("history = (observed=%d, %d sample(s)), want 1 and 1", history.Observed, len(history.Samples))
	}
	got := history.Samples[0]
	if !got.Start.Equal(start) || got.Commits != 7 || got.Duration != 80*time.Millisecond || got.DispatchWait != 20*time.Millisecond {
		t.Errorf("sample = %+v, want the tick as the cycle measured it", got)
	}
	if len(got.Reconcilers) != 3 {
		t.Fatalf("reconcilers = %d, want all three, in the order the cycle ran them", len(got.Reconcilers))
	}
	sync := got.Reconcilers[2]
	if sync.Name != "sync" || sync.Wait != 30*time.Millisecond || sync.Duration != 50*time.Millisecond || !sync.Failed {
		t.Errorf("sync = %+v, want name, wait, duration and the failure flag all carried across", sync)
	}
}

// The ring is a package-level value allocated at process start precisely
// so the UI/API server -- which comes up before runDaemon builds its Deps
// -- has something to read. Asking it before a single tick has happened
// is the ordinary state of a daemon that has just started (or whose
// reconcile loop has died, see reconcilerDown), and must report no ticks
// rather than panic.
func TestCycleTimesAdapterBeforeTheFirstTick(t *testing.T) {
	t.Parallel()
	history := cycleTimesAdapter{orchestrator.NewCycleTimes(0)}.CycleTimes()
	if history.Observed != 0 || len(history.Samples) != 0 {
		t.Errorf("history = (observed=%d, %d sample(s)), want nothing measured yet",
			history.Observed, len(history.Samples))
	}
}
