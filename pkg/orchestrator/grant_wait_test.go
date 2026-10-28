// An admission wait is a derived fact about a queued task, and these are
// the tests for who keeps it true. The fact's inputs are the task's own
// grants and target plus whatever the capability pools held when the
// admitter last looked; only the first two can change without the
// orchestrator running at all, so the store write that changes them is
// what invalidates the fact (model.invalidateStaleGrantWait), and the
// next admission probe derives it again.
//
// The case that made this necessary: a task parked for a missing
// repository-required grain-ci grant kept displaying that reason after
// an operator added the grant, because the only code that cleared the
// row ran inside an admission probe, and a probe only runs for a
// candidate a cycle still has worker capacity for.
package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// requiresScarceGrant files a task granted the "keyed" capability, so
// that its admission waits on that capability's pool.
func requiresScarceGrant(t *testing.T, ctx context.Context, store *model.Store, id string, repo model.RepoRef) model.Task {
	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	task.Grants = []model.Grant{{Capability: "keyed", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestAddingTheRequiredGrantClearsTheWaitWithNoWorkerFree(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := requiresScarceGrant(t, ctx, store, "t1", repo)

	keys := &scarceFakeCapability{&fakeCapability{name: "keyed", content: "key", limited: 5}}
	deps := orchestrator.Deps{
		Store: store, Client: client, MaxWorkers: 1,
		// A backend, because a cycle with nowhere to dispatch into does
		// not dispatch at all (reconcileDispatch) and these tests need
		// the admission probe that a candidate reaches. Nothing is ever
		// built in one: every candidate here is declined for want of a
		// grant before a run is recorded.
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Config: orchestrator.Config{
			Capabilities: model.NewCapabilityRegistry(keys, &fakeCapability{name: "notes"}),
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Resource != "test worker keys" {
		t.Fatalf("grant wait = %+v, want the missing scarce grant", wait)
	}

	// Take the deployment's only worker, so that nothing below can be
	// explained by an admission probe: from here on no candidate is
	// considered at all.
	filedTask(t, ctx, store, "t2", repo)
	if err := store.StartRun(ctx, model.Run{
		ID: "t2-1", TaskID: "t2", Sandbox: "t2-1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	task.Grants = []model.Grant{{Capability: "notes", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	wait, err = store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Fatalf("grant wait after the operator changed grants = %+v, want none", wait)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].TaskID != "t2" {
		t.Fatalf("live runs = %+v, want only the run holding the worker: the wait was cleared without one starting", runs)
	}
	wait, err = store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Fatalf("grant wait after a cycle with no capacity = %+v, want capability %q (not scarce grant wait)", wait, model.QuotaWaitCapability)
	}
}

func TestTakingTheGrantAwayAgainRecreatesTheWait(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := requiresScarceGrant(t, ctx, store, "t1", repo)

	keys := &scarceFakeCapability{&fakeCapability{name: "keyed", content: "key", limited: 5}}
	deps := orchestrator.Deps{
		Store: store, Client: client, MaxWorkers: 1,
		// A backend, because a cycle with nowhere to dispatch into does
		// not dispatch at all (reconcileDispatch) and these tests need
		// the admission probe that a candidate reaches. Nothing is ever
		// built in one: every candidate here is declined for want of a
		// grant before a run is recorded.
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Config: orchestrator.Config{
			Capabilities: model.NewCapabilityRegistry(keys, &fakeCapability{name: "notes"}),
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	task.Grants = []model.Grant{{Capability: "notes", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, task.ID); err != nil || wait != nil {
		t.Fatalf("grant wait after the grant was changed = %+v (%v), want none", wait, err)
	}

	// The operator changes their mind: invalidation is not a promise that
	// the task is admittable, only that the stored reason is no longer
	// evidence. The next probe is what says so again.
	task.Grants = []model.Grant{{Capability: "keyed", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}

	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Resource != "test worker keys" {
		t.Fatalf("grant wait after the grant was restored = %+v, want the scarce resource wait again", wait)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("live runs = %+v, want none while keyed is scarce", runs)
	}
}

// A wait on an exhausted pool is the case invalidation must not lose: an
// edit to the task drops the row, and the very next probe puts back the
// same wait for as long as the pool is still empty -- while an edit that
// changes nothing the wait was derived from leaves it exactly where it
// was.
func TestAnExhaustedScarceWaitSurvivesAnEditAndIsRederived(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Grants = []model.Grant{{Capability: "keyed", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	keys := &scarceFakeCapability{&fakeCapability{name: "keyed", content: "key", limited: 5}}
	deps := orchestrator.Deps{
		Store: store, Client: client, MaxWorkers: 1,
		// A backend, because a cycle with nowhere to dispatch into does
		// not dispatch at all (reconcileDispatch) and these tests need
		// the admission probe that a candidate reaches. Nothing is ever
		// built in one: every candidate here is declined for want of a
		// grant before a run is recorded.
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Config: orchestrator.Config{
			Capabilities: model.NewCapabilityRegistry(keys, &fakeCapability{name: "notes"}),
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Resource != "test worker keys" {
		t.Fatalf("grant wait = %+v, want the exhausted key pool", wait)
	}
	first := *wait

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Title = "fix the thing, properly"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wait, err = store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Resource != "test worker keys" || !wait.WaitingAt.Equal(first.WaitingAt) {
		t.Fatalf("grant wait after a retitle = %+v, want %+v untouched: renaming a task frees no key", wait, first)
	}

	// Now an edit that does move an input. The row goes, because it was
	// derived from a grant set that no longer exists...
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: "notes", Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.GrantWaitOf(ctx, task.ID); err != nil || wait != nil {
		t.Fatalf("grant wait after a grant edit = %+v (%v), want none until it is derived again", wait, err)
	}

	// ...and the next probe derives it again from the pool, which is
	// still empty.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	wait, err = store.GrantWaitOf(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Resource != "test worker keys" {
		t.Fatalf("grant wait after the next probe = %+v, want the exhausted key pool again", wait)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("live runs = %+v, want none while the key pool is empty", runs)
	}
}

func TestQuotaWaitForSandboxCapacityAndUsageLimitPause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	t1 := filedTask(t, ctx, store, "t1", repo)
	t2 := filedTask(t, ctx, store, "t2", repo)
	if err := store.ObserveField(ctx, t2.ID, baseTime, func(o *model.Observation) {
		o.RunNowAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}

	uiClient := &ui.Client{Store: store}
	pause := &orchestrator.Pause{}
	resetAt := baseTime.Add(time.Hour)
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude,
		ResetAt:   resetAt,
		Message:   "rate limit reached",
	})

	var dispatched []string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		dispatched = append(dispatched, cfg.TaskID)
		return pushed(), nil
	})

	ledger := &model.Ledger{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 2,
		Ledger:     ledger,
		Budget:     model.Budget{Host: model.Commitment{CPUs: 4, MemoryMB: 4096}, CPUOvercommit: 1, MemoryOvercommit: 1},
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 2048},
		Config: orchestrator.Config{
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}

	// 1. Held back by provider usage-limit pause (PausedFor / gatedByPause).
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle during usage-limit pause: %v", err)
	}
	for _, id := range []string{t1.ID, t2.ID} {
		detail, err := uiClient.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !detail.ActivityBySetup || !strings.HasPrefix(detail.Activity, "waiting: ") ||
			!strings.Contains(detail.Activity, resetAt.UTC().Format(time.RFC3339)) {
			t.Fatalf("GetTask(%s) activity during usage-limit pause = %q (bySetup=%v), want waiting status with reset time %s",
				id, detail.Activity, detail.ActivityBySetup, resetAt.UTC().Format(time.RFC3339))
		}
	}

	// 2. Lift pause, but exhaust sandbox budget so both ready (t1) and RunNow (t2) wait on sandbox quota.
	pause.Lift()
	releaseBusy := ledger.Charge(model.PoolAgent, "busy-sandbox", model.Commitment{CPUs: 4, MemoryMB: 4096})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle under exhausted sandbox budget: %v", err)
	}
	for _, id := range []string{t1.ID, t2.ID} {
		detail, err := uiClient.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !detail.ActivityBySetup || !strings.HasPrefix(detail.Activity, "waiting: ") ||
			!strings.Contains(detail.Activity, "sandbox budget") {
			t.Fatalf("GetTask(%s) activity under exhausted sandbox budget = %q (bySetup=%v), want waiting status with sandbox budget reason",
				id, detail.Activity, detail.ActivityBySetup)
		}
	}

	// 3. Release the sandbox budget and verify starting the tasks clears their GrantWait.
	releaseBusy()
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("RunCycle after freeing sandbox budget: %v", err)
	}
	for _, id := range []string{t1.ID, t2.ID} {
		runs, err := store.Runs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 {
			t.Fatalf("Runs(%s) after freeing budget = %+v, want 1 run", id, runs)
		}
		if wait, err := store.GrantWaitOf(ctx, id); err != nil || wait != nil {
			t.Fatalf("GrantWaitOf(%s) after task ran = %+v (%v), want nil", id, wait, err)
		}
	}
}

func TestGatedByPauseResolvesPinnedConnectionsAndDoesNotBlockOnUnconnectedPause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutConfig(ctx, model.Config{
		AgentBackends: []model.AgentBackend{
			{
				ID:                  "conn-borrow",
				Framework:           model.AgentFrameworkClaude,
				CredentialBackendID: "conn-owner",
				Models:              []string{"claude-sonnet-4-5"},
			},
			{
				ID:        "conn-owner",
				Framework: model.AgentFrameworkClaude,
				Models:    []string{"claude-sonnet-4-5"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	pinnedTask := filedTask(t, ctx, store, "t-pinned", repo)
	pinnedTask.AgentBackendID = "conn-borrow"
	if err := store.PutTask(ctx, pinnedTask); err != nil {
		t.Fatal(err)
	}

	pause := &orchestrator.Pause{}
	resetAt := baseTime.Add(time.Hour)
	// 1. A no-connection pause on claude must NOT block a task pinned to conn-borrow.
	// Also pause conn-borrow's own ID to verify gatedByPause checks CredentialOwnerID ("conn-owner"), not "conn-borrow".
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude, ResetAt: resetAt, Message: "unconnected limit",
	})
	pause.Begin(baseTime, orchestrator.PauseScope{Connection: "conn-borrow", Team: model.DefaultTeamID, Framework: model.AgentFrameworkClaude}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude, ResetAt: resetAt, Message: "stale self key limit",
	})

	var ran []string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		ran = append(ran, cfg.TaskID)
		return pushed(), nil
	})
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}

	// Because neither the no-connection pause nor the stale "conn-borrow" pause covers
	// conn-borrow's actual credential owner ("conn-owner"), instead suppose a model-scoped
	// pause on conn-owner's default model ("claude-sonnet-4-5") is active first:
	pause.Begin(baseTime, orchestrator.PauseScope{
		Connection: "conn-owner",
		Model:      "claude-sonnet-4-5",
		Team:       model.DefaultTeamID,
		Framework:  model.AgentFrameworkClaude,
	}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude, ResetAt: resetAt, Message: "owner model limit",
	})

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle while conn-owner default model is paused: %v", err)
	}
	if len(ran) != 0 {
		t.Fatalf("ran = %v, want none while conn-owner's effective model is paused", ran)
	}
	wait, err := store.GrantWaitOf(ctx, pinnedTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Capability != model.QuotaWaitCapability || !strings.Contains(wait.Resource, "conn-owner") {
		t.Fatalf("GrantWaitOf(%s) = %+v, want QuotaWaitCapability naming conn-owner", pinnedTask.ID, wait)
	}

	// Lift only conn-owner's pause (leaving the no-connection pause and the stale "conn-borrow" pause active).
	// The pinned task on conn-borrow must now dispatch cleanly!
	pause.LiftWhere(func(p model.UsageLimitPause) bool {
		return p.Connection == "conn-owner"
	})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle after lifting conn-owner pause: %v", err)
	}
	if len(ran) != 1 || ran[0] != pinnedTask.ID {
		t.Fatalf("ran = %v, want [%s] dispatched despite no-connection and stale conn-borrow pauses", ran, pinnedTask.ID)
	}
}
