package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A task whose model no connection can serve right now is not dispatched:
// it waits in the queue, the reason on it, and is dispatched on the first
// cycle a connection is free -- the choice admission is asked about being
// the task's own model.
func TestATaskWaitsWithTheReasonWhileNoConnectionServesItsModel(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Model = "claude-x"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reason := "all connections serving claude-x are busy (2 connections at their concurrency limits (6 of 6 runs))"
	var asked []orchestrator.AgentChoice
	sandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
		ModelAvailability: func(_ context.Context, choice orchestrator.AgentChoice) string {
			asked = append(asked, choice)
			return reason
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 0 {
		t.Fatalf("acquired = %v, want nothing dispatched while no connection is free", acquired)
	}
	if len(asked) == 0 || asked[0].Model != "claude-x" {
		t.Fatalf("availability asked about %+v, want the task's own model", asked)
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil || wait == nil {
		t.Fatalf("grant wait = %v, %v; want the task held", wait, err)
	}
	if wait.Capability != model.ModelWaitCapability || !strings.Contains(wait.Resource, "are busy") {
		t.Fatalf("wait = %+v, want the model capability with the busy reason", wait)
	}

	reason = ""
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if acquired, _ := sandboxes.calls(); len(acquired) != 1 {
		t.Fatalf("acquired = %v, want the task dispatched once a connection is free", acquired)
	}
}

// singleSlotPool simulates a connection pool with Concurrency=1: Availability
// checks whether the slot is free, and Build reserves the slot (unless
// choice.Priority is true) and returns a framework whose Run and Release free
// it.
type singleSlotPool struct {
	mu    sync.Mutex
	inUse int
	limit int
	inner agent.Framework
}

type leasedStubFramework struct {
	agent.Framework
	pool     *singleSlotPool
	released bool
}

func (f *leasedStubFramework) Release() {
	if f == nil || f.pool == nil {
		return
	}
	f.pool.mu.Lock()
	defer f.pool.mu.Unlock()
	if !f.released {
		f.released = true
		f.pool.inUse--
	}
}

func (f *leasedStubFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	defer f.Release()
	return f.Framework.Run(ctx, cfg)
}

func (p *singleSlotPool) availability(_ context.Context, choice orchestrator.AgentChoice) string {
	if choice.Priority {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inUse >= p.limit {
		return fmt.Sprintf("all connections are busy (1 connection at its concurrency limit (%d of %d runs))", p.inUse, p.limit)
	}
	return ""
}

func (p *singleSlotPool) build(_ context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
	if choice.Priority {
		return p.inner, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inUse >= p.limit {
		return nil, fmt.Errorf("no configured agent backend is free: of 1 configured, 1 connection at its concurrency limit (%d of %d runs)", p.inUse, p.limit)
	}
	p.inUse++
	return &leasedStubFramework{Framework: p.inner, pool: p}, nil
}

// When multiple worker tasks are ready in the same RunCycle tick (or across
// ticks while the first run's sandbox VM is still booting in meter.Acquire),
// reserving the framework lease at admission prevents the second task from
// claiming the same connection slot, booting a sandbox, and failing setup
// with "1 connection at its concurrency limit". Meanwhile, a console session
// bypasses the connection concurrency limit and dispatches cleanly.
func TestConcurrentCandidatesAndBootingSandboxesWaitOnModelRatherThanFailingSetup(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	t1 := filedTask(t, ctx, store, "t1", repo)
	t1.OrderKey = 1
	if err := store.PutTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	t2 := filedTask(t, ctx, store, "t2", repo)
	t2.OrderKey = 2
	if err := store.PutTask(ctx, t2); err != nil {
		t.Fatal(err)
	}

	// gatingSandboxes blocks t1-1 inside Acquire (simulating a 30s Granule
	// VM boot) so we can observe both same-tick admission of t2 and a
	// subsequent reconcile tick while t1-1's VM is still booting.
	acquireEntered := make(chan string, 4)
	releaseAcquire := make(chan struct{})
	baseSandboxes := &recordingSandboxes{HostSandboxes: orchestrator.NewHostSandboxes(t.TempDir())}
	sandboxes := &gatingAcquireSandboxes{
		Sandboxes:      baseSandboxes,
		blockName:      "t1-1",
		acquireEntered: acquireEntered,
		releaseAcquire: releaseAcquire,
	}

	pool := &singleSlotPool{
		limit: 1,
		inner: stubFramework{result: toolResult(agent.ToolCall{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": "done"},
		})},
	}
	runs := &orchestrator.InFlight{}
	deps := orchestrator.Deps{
		Store:             store,
		Client:            client,
		Sandboxes:         sandboxes,
		Framework:         pool.build,
		ModelAvailability: pool.availability,
		MaxWorkers:        2,
		Runs:              runs,
	}

	// Tick 1: both t1 and t2 are ready, and MaxWorkers=2 allows both, but
	// the connection has Concurrency=1. t1 must take the lease during
	// admission and t2 must cleanly enter Waiting on model without starting
	// a run or acquiring a sandbox.
	cycleAt(t, ctx, deps, baseTime)

	select {
	case got := <-acquireEntered:
		if got != "t1-1" {
			t.Fatalf("Acquire entered for %q, want t1-1", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("t1-1 never entered sandbox Acquire")
	}

	t2Runs, err := store.Runs(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if len(t2Runs) != 0 {
		t.Fatalf("t2 runs in tick 1 = %+v, want 0 (held on model wait, not setup-failed)", t2Runs)
	}
	wait, err := store.GrantWaitOf(ctx, "t2")
	if err != nil || wait == nil || wait.Capability != model.ModelWaitCapability || !strings.Contains(wait.Resource, "1 connection at its concurrency limit") {
		t.Fatalf("t2 grant wait after tick 1 = %+v, err = %v; want Waiting on model at concurrency limit", wait, err)
	}

	// Tick 2 (while t1-1 is still booting its sandbox in Acquire): t2 must
	// still see the connection slot held by t1-1 and remain queued on
	// Waiting on model, while a ready console session bypasses the
	// connection concurrency limit and runs to completion.
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	replyToSession(t, store, director.ID, baseTime.Add(10*time.Second))

	cycleAt(t, ctx, deps, baseTime.Add(10*time.Second))

	t2Runs, err = store.Runs(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if len(t2Runs) != 0 {
		t.Fatalf("t2 runs in tick 2 while t1-1 VM is booting = %+v, want 0", t2Runs)
	}

	// Let t1-1 finish booting its sandbox and complete.
	close(releaseAcquire)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for t1-1 and director: %v", err)
	}

	finishedRun(t, ctx, store, "t1")
	finishedRun(t, ctx, store, director.ID)

	// Tick 3: now that t1-1 has finished and released its connection lease,
	// t2 dispatches and succeeds.
	cycleAt(t, ctx, deps, baseTime.Add(20*time.Second))
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for t2-1: %v", err)
	}
	finishedRun(t, ctx, store, "t2")
}

// If a run's sandbox fails to come up after admission reserved a connection
// lease, runOne releases the lease on exit so the connection slot is not
// stranded.
func TestSetupFailureReleasesPreReservedConnectionLease(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	pool := &singleSlotPool{
		limit: 1,
		inner: stubFramework{result: toolResult(agent.ToolCall{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": "done"},
		})},
	}
	deps := orchestrator.Deps{
		Store:             store,
		Client:            client,
		Sandboxes:         unbuildableSandboxes{err: errors.New("vm boot failed")},
		Framework:         pool.build,
		ModelAvailability: pool.availability,
		MaxWorkers:        1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("expected RunCycle to fail when sandbox Acquire fails")
	}
	pool.mu.Lock()
	inUse := pool.inUse
	pool.mu.Unlock()
	if inUse != 0 {
		t.Fatalf("pool.inUse after setup failure = %d, want 0 (lease released)", inUse)
	}
}

type gatingAcquireSandboxes struct {
	orchestrator.Sandboxes
	blockName      string
	acquireEntered chan<- string
	releaseAcquire <-chan struct{}
}

func (s *gatingAcquireSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	if name == s.blockName {
		s.acquireEntered <- name
		select {
		case <-s.releaseAcquire:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Sandboxes.Acquire(ctx, name, shape)
}
