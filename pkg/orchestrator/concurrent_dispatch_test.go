// Whether a cycle can still dispatch while an earlier run is in flight
// -- the thing a deployment loses when RunCycle waits for the agents it
// started. Every test here files its second task *after* the first run
// is already inside framework.Run, which is the ordinary case (a person
// files a task while something is running) and the one that used to wait
// out the whole run however much of MaxWorkers was idle.
package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// blockingFramework is an agent that never finishes on its own: it
// announces the run it was given (by the sandbox directory that run was
// named after) and then waits to be released, so a test can hold a run
// live for as long as it needs to watch what the cycles around it do.
type blockingFramework struct {
	entered chan string
	release chan struct{}
}

func newBlockingFramework() *blockingFramework {
	return &blockingFramework{entered: make(chan string, 8), release: make(chan struct{})}
}

func (f *blockingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.entered <- filepath.Base(cfg.SandboxRoot)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

// releaseAll lets every run still inside Run finish, and every later one
// pass straight through.
func (f *blockingFramework) releaseAll() { close(f.release) }

// enteredRun is the next run blockingFramework was handed, or a failure
// naming what the test was waiting for -- never a hang, which is what a
// blocked cycle would otherwise look like.
func enteredRun(t *testing.T, f *blockingFramework, want string) {
	t.Helper()
	select {
	case got := <-f.entered:
		if got != want {
			t.Fatalf("the agent ran for %s, want %s", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no agent ran for %s", want)
	}
}

// cycle runs one, failing rather than hanging if it does not return --
// "does not return while a run is live" being exactly the bug these
// tests exist for.
func cycle(t *testing.T, ctx context.Context, deps orchestrator.Deps) {
	t.Helper()
	cycleAt(t, ctx, deps, baseTime)
}

// cycleAt is cycle with the deployment's clock somewhere other than
// baseTime -- for the reconcilers that are about a moment arriving
// rather than about work being there (defer_dispatch_test.go's wake
// times).
func cycleAt(t *testing.T, ctx context.Context, deps orchestrator.Deps, now time.Time) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- orchestrator.RunCycle(ctx, deps, now) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunCycle did not return while a dispatched run was still in flight")
	}
}

// asyncDeps is the wiring a deployment has: an InFlight to park runs in,
// so a cycle is over once its decisions are made. Any agent.Framework
// will do -- the tests here hand it a blockingFramework, and
// schedule_overlap_timer_test.go its own slowFramework.
func asyncDeps(t *testing.T, store *model.Store, fw agent.Framework, maxWorkers int) (orchestrator.Deps, *orchestrator.InFlight) {
	t.Helper()
	_, client := newSim(t, "acme", "widgets", "main")
	runs := &orchestrator.InFlight{}
	return orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: maxWorkers,
		Runs:       runs,
	}, runs
}

func TestACycleDispatchesATaskFiledWhileAnotherRunIsLive(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)

	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")

	filedTask(t, ctx, store, "t2", repo)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "t2-1")

	if got, err := store.LiveRunCount(ctx); err != nil {
		t.Fatal(err)
	} else if got != 2 {
		t.Fatalf("live runs = %d, want both t1's and t2's", got)
	}

	fw.releaseAll()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the dispatched runs: %v", err)
	}
	for _, id := range []string{"t1", "t2"} {
		finishedRun(t, ctx, store, id)
	}
}

// The Director is the control-plane exception to full ordinary capacity:
// a real human reply starts promptly beside a live worker, without
// preempting it or allowing either kind to exceed its own limit.
func TestDirectorTurnDispatchesWhileAllOrdinaryWorkersAreLive(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "worker-1", repo)
	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 1)

	cycle(t, ctx, deps)
	enteredRun(t, fw, "worker-1-1")

	// The operator's new message makes the canonical console-session task
	// eligible while the sole ordinary slot remains occupied.
	replyToSession(t, store, director.ID, baseTime.Add(time.Minute))
	cycleAt(t, ctx, deps, baseTime.Add(time.Minute+time.Second))
	enteredRun(t, fw, director.ID+"-1")
	if live, err := store.LiveRunCounts(ctx); err != nil || live != (model.RunCounts{Workers: 1, Directors: 1}) {
		t.Fatalf("live counts = %+v, err = %v; want one worker plus the Director bypass", live, err)
	}

	// Another cycle cannot start the same chat turn twice, and another
	// ordinary task cannot take the bypass while both runs are live.
	filedTask(t, ctx, store, "worker-2", repo)
	cycleAt(t, ctx, deps, baseTime.Add(time.Minute+2*time.Second))
	if got, err := store.Runs(ctx, director.ID); err != nil || len(got) != 1 {
		t.Fatalf("Director runs while its attempt is active = %+v, err = %v; want exactly one", got, err)
	}
	if got, err := store.Runs(ctx, "worker-2"); err != nil || len(got) != 0 {
		t.Fatalf("worker-2 runs while ordinary capacity is full = %+v, err = %v", got, err)
	}

	fw.releaseAll()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the worker and Director: %v", err)
	}
	if live, err := store.LiveRunCounts(ctx); err != nil || live.Total() != 0 {
		t.Fatalf("live counts after both runs ended = %+v, err = %v", live, err)
	}

	// With the bypass empty again, dispatch returns to the ordinary limit.
	cycleAt(t, ctx, deps, baseTime.Add(2*time.Minute))
	enteredRun(t, fw, "worker-2-1")
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for worker-2: %v", err)
	}
	finishedRun(t, ctx, store, "worker-2")
}

// grain/task-2387 (docs/test-plans/2026-10-06.md §9): with no Director
// ceiling (grain/task-2323), two standing sessions on two different
// teams are both live at once -- each answering its own reply -- while
// the deployment's only ordinary worker slot is taken. Neither waits for
// the other, and an ordinary task still waits for the worker slot.
func TestTwoSessionsOnTwoTeamsAreLiveAtOnceBesideAFullWorkerSlot(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{
			{ID: model.DirectorSessionID, Title: "Director", Team: "infra", NetworkAccess: model.NetworkOpen},
			{ID: "reviewer", Title: "Reviewer", Team: "security", NetworkAccess: model.NetworkOpen},
		}
	})
	runSessions(t, store, baseTime)
	sessions := sessionTasks(t, store)
	if len(sessions) != 2 {
		t.Fatalf("session tasks = %+v, want two", sessions)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "worker-1", repo)
	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 1)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "worker-1-1")

	said := baseTime.Add(time.Minute)
	for _, s := range sessions {
		replyToSession(t, store, s.ID, said)
	}
	filedTask(t, ctx, store, "worker-2", repo)
	cycleAt(t, ctx, deps, said.Add(time.Second))
	entered := map[string]bool{}
	for range sessions {
		select {
		case got := <-fw.entered:
			entered[got] = true
		case <-time.After(30 * time.Second):
			t.Fatalf("only %v started; want both sessions' runs in the same cycle", entered)
		}
	}
	for _, s := range sessions {
		if !entered[s.ID+"-1"] {
			t.Fatalf("runs started = %v, want %s's", entered, s.ID)
		}
		if state, err := store.State(ctx, s.ID); err != nil || state != model.StateRunning {
			t.Fatalf("session %s state = %q, err = %v; want running", s.ID, state, err)
		}
	}
	if live, err := store.LiveRunCounts(ctx); err != nil || live != (model.RunCounts{Workers: 1, Directors: 2}) {
		t.Fatalf("live counts = %+v, err = %v; want the one worker plus both sessions", live, err)
	}
	if got, err := store.Runs(ctx, "worker-2"); err != nil || len(got) != 0 {
		t.Fatalf("worker-2 runs while the worker slot is full = %+v, err = %v", got, err)
	}

	fw.releaseAll()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the worker and both sessions: %v", err)
	}
}

// The other half of the same property: dispatching without waiting must
// not dispatch past the limit. The store is what enforces it (StartRun
// counts live runs inside its own transaction), and a run's row stays
// live across ticks precisely because the goroutine that finishes it
// outlives the cycle -- so a second tick sees the first run's capacity
// still spent.
func TestACycleDoesNotDispatchPastMaxWorkersWhileARunIsLive(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 1)

	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")

	filedTask(t, ctx, store, "t2", repo)
	cycle(t, ctx, deps)

	if got, err := store.Runs(ctx, "t2"); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("t2 was dispatched (%+v) with the deployment's one run already live", got)
	}

	fw.releaseAll()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for t1's run: %v", err)
	}

	// And the capacity comes back: the very next tick, with nothing else
	// having changed, starts the task that was waiting.
	cycle(t, ctx, deps)
	enteredRun(t, fw, "t2-1")
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for t2's run: %v", err)
	}
	finishedRun(t, ctx, store, "t2")
}

// grain_config's max_workers is a legacy column that schedules nothing
// (grain/task-28). A cycle used to re-read it every tick, which is what
// made it a live setting; adopting it now would put back the
// deployment-wide ceiling this change removed, on top of the teams' own
// counts and the sandbox budget.
//
// This is the same test as before with the expectation inverted: the
// stored value moves, and what the cycle starts does not.
func TestACycleIgnoresAStoredMaxWorkersChange(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	filedTask(t, ctx, store, "t2", repo)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 1)

	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")

	if got, err := store.Runs(ctx, "t2"); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("t2 was dispatched (%+v) with this caller's one run already live", got)
	}

	// A row saying 2, and a row saying 300: neither reaches the cycle,
	// whose own ceiling is still the 1 its caller set.
	for _, workers := range []int{2, 300} {
		if err := store.PutConfig(ctx, model.Config{MaxWorkers: workers}); err != nil {
			t.Fatalf("PutConfig: %v", err)
		}
		cycle(t, ctx, deps)
		if got, err := store.Runs(ctx, "t2"); err != nil {
			t.Fatal(err)
		} else if len(got) != 0 {
			t.Fatalf("a stored max_workers of %d dispatched t2 (%+v)", workers, got)
		}
	}

	fw.releaseAll()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the dispatched runs: %v", err)
	}
	finishedRun(t, ctx, store, "t1")
}

// Deps with no InFlight keeps the old shape -- the cycle waits for the
// run it started -- which every one-shot caller in-tree relies on.
func TestACycleWithNowhereToParkRunsStillWaitsForThem(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	fw := newBlockingFramework()
	fw.releaseAll() // nothing to hold: this run must finish inside the cycle
	deps, _ := asyncDeps(t, store, fw, 1)
	deps.Runs = nil

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, err := store.LiveRunCount(ctx); err != nil {
		t.Fatal(err)
	} else if got != 0 {
		t.Fatalf("live runs after a cycle that should have waited = %d, want 0", got)
	}
	finishedRun(t, ctx, store, "t1")
}

// finishedRun asserts taskID has exactly one run and that it is over.
func finishedRun(t *testing.T, ctx context.Context, store *model.Store, taskID string) {
	t.Helper()
	got, err := store.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%s has %d run(s), want exactly one", taskID, len(got))
	}
	if got[0].FinishedAt == nil {
		t.Errorf("%s's run %s is still live after its agent returned", taskID, got[0].ID)
	}
}
