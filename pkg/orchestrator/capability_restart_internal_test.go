package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestCapabilityNeedsSandbox(t *testing.T) {
	for _, tc := range []struct {
		old, now []string
		want     bool
	}{
		{[]string{"memory"}, []string{"memory"}, false},
		{[]string{"memory"}, []string{"api:tasks-read", "memory"}, false},
		{[]string{"api:tasks-read", "memory"}, []string{"memory"}, false},
		{[]string{"memory"}, []string{"gcp-key", "memory"}, true},
		{[]string{"gcp-key", "memory"}, []string{"memory"}, true},
		{nil, []string{"api:tasks-read", "self-debug"}, true},
	} {
		if got := capabilityNeedsSandbox(tc.old, tc.now); got != tc.want {
			t.Errorf("capabilityNeedsSandbox(%v, %v) = %v, want %v", tc.old, tc.now, got, tc.want)
		}
	}
}

// A grant added to a task with a live run tells the run, waits the
// grace, then cancels it with errCapabilityChanged, which ends it as a
// capability suspension. An api:* preset is applied in place.
func TestAGrantChangeRestartsTheLiveRun(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := stdTask("subject")
	tk.Grants = []model.Grant{{Capability: "memory", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "subject-1", TaskID: tk.ID, Attempt: 1, StartedAt: standingNow},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	cancellers := NewTaskCancellers()
	runCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	defer cancellers.register(tk.ID, cancel)()
	deps := Deps{Store: store, Config: Config{TaskCancellers: cancellers, CapabilityRestarts: NewCapabilityRestarts()}}
	notes := func() int {
		t.Helper()
		comments, err := store.Comments(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range comments {
			if strings.Contains(c.Body, "a capability of this task changed") {
				n++
			}
		}
		return n
	}
	reconcile := func(at time.Time) {
		t.Helper()
		if err := reconcileCapabilityChanges(ctx, deps, at); err != nil {
			t.Fatal(err)
		}
	}

	// An api:* preset: in place, nobody told, nothing cancelled.
	tk.Grants = append(tk.Grants, model.Grant{Capability: model.APIGrantPrefix + "tasks-read", Via: model.GrantByLabel})
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	reconcile(standingNow.Add(time.Minute))
	reconcile(standingNow.Add(time.Hour))
	if n := notes(); n != 0 || runCtx.Err() != nil {
		t.Fatalf("an api:* preset told the run %d times and cancelled it: %v", n, runCtx.Err())
	}

	// A grant the sandbox has to carry: told at once, cancelled after the grace.
	tk.Grants = append(tk.Grants, model.Grant{Capability: "gcp-key", Via: model.GrantByLabel})
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	changed := standingNow.Add(2 * time.Hour)
	reconcile(changed)
	reconcile(changed.Add(capabilityRestartGrace - time.Second))
	if n := notes(); n != 1 || runCtx.Err() != nil {
		t.Fatalf("within the grace: told %d times, cancelled %v; want told once and still running", n, runCtx.Err())
	}
	reconcile(changed.Add(capabilityRestartGrace))
	if !errors.Is(context.Cause(runCtx), errCapabilityChanged) {
		t.Fatalf("after the grace the run's cause is %v, want errCapabilityChanged", context.Cause(runCtx))
	}

	end := classifyRunEnd(ctx, store, Config{}, tk, "subject-1", nil, context.Canceled, context.Cause(runCtx),
		0, PauseScope{}, standingNow)
	if end.suspend != model.SuspendCapability || end.outcome != model.SuspendedOutcome {
		t.Fatalf("the restarted run ends (%q, %q), want a capability suspension", end.outcome, end.suspend)
	}
}
