package dispatch_test

// Console sessions under a pipeline pause: an operator can always reach
// the deployment, a paused one included, except while an upgrade's own
// hold is shutting the queue in front of a restart (PriorityPassesPause).

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestASessionPassesAPauseThatAdmitsSessionsAndOrdinaryWorkDoesNot(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), directorTask("director"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now,
		dispatch.Paused(), dispatch.PriorityPassesPause())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "director" {
		t.Fatalf("a pause admitting sessions dispatched %+v, want the director alone", got)
	}
}

func TestAnUpgradesHoldStillHoldsASessionAndSaysSo(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), directorTask("director"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now, dispatch.Paused())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an upgrade's hold dispatched %+v, want nothing", got)
	}
	wait, err := store.GrantWaitOf(ctx, "director")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || !strings.Contains(wait.Resource, "dispatch is paused") || !strings.Contains(wait.Resource, "upgrade") {
		t.Fatalf("the held session's wait = %+v, want one saying an upgrade holds it", wait)
	}

	// Lifting the hold starts the session, and clears what it said.
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("after the hold dispatched %+v, want the director and t0", got)
	}
	if wait, _ := store.GrantWaitOf(ctx, "director"); wait != nil {
		t.Fatalf("the started session still waits: %+v", wait)
	}
}

// A session asked for by name under a manual pause starts too, and an
// ordinary request beside it is held without RunNowPassesPause: the two
// options say different things.
func TestARunNowRequestForASessionPassesAManualPause(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), directorTask("director"))
	requestRunNow(t, ctx, store, "t0", now)
	requestRunNow(t, ctx, store, "director", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(time.Minute),
		dispatch.Paused(), dispatch.PriorityPassesPause())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "director" {
		t.Fatalf("dispatched %+v, want the requested session alone", got)
	}
}

// PriorityPassesPause passes the pause and nothing else.
func TestPriorityPassesPauseDoesNotPassAHold(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, directorTask("director"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now,
		dispatch.Paused(), dispatch.PriorityPassesPause(),
		dispatch.Hold(func(string) string { return "held by local policy: test" }))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a held session was dispatched: %+v", got)
	}
}
