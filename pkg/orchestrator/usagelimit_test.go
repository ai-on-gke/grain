package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A run whose framework reports the agent's own usage limit records
// model.PausedOutcome, not "failed": nothing about the task was wrong,
// the deployment ran out of budget. The detail carries both halves an
// operator needs -- what the provider said, and when grain will try
// again.
func TestRunDispatchRecordsAUsageLimitAsPaused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	reset := baseTime.Add(90 * time.Minute)
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return pushed(), &agent.UsageLimitError{
			Framework: "claude", Message: "Claude AI usage limit reached", ResetAt: reset,
		}
	})
	pause := &orchestrator.Pause{}
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }}

	result, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
	if runErr == nil {
		t.Fatal("RunDispatch err = nil, want the limit reported")
	}
	if _, ok := agent.UsageLimit(runErr); !ok {
		t.Errorf("RunDispatch err = %v, want it to still carry the agent.UsageLimitError", runErr)
	}
	// The branch this run pushed before it met the limit still comes
	// back, so runOne can salvage it (agent.Framework's own contract).
	if result == nil {
		t.Error("RunDispatch returned no result, want the work the run did before the limit")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
	if !strings.Contains(runs[0].Detail, "usage limit reached") {
		t.Errorf("run detail = %q, want it to say what the provider said", runs[0].Detail)
	}
	if !strings.Contains(runs[0].Detail, reset.Format(time.RFC3339)) {
		t.Errorf("run detail = %q, want it to name when dispatch resumes (%s)", runs[0].Detail, reset)
	}

	if until, _ := pause.Until(); !until.Equal(reset) {
		t.Errorf("pause until = %s, want the provider's own reset %s", until, reset)
	}
	// This framework names no connection, so the run spent the
	// deployment-level credential of its team (the default) and framework
	// (the deployment's, antigravity), and that is what is paused.
	gated := pause.Blocked(baseTime)
	if !gated.Covers(orchestrator.PauseScope{Framework: model.AgentFrameworkAntigravity}) {
		t.Error("dispatch onto the credential that met the limit was left open")
	}
	// And only that credential: another framework's, or another team's,
	// were not the one that was refused.
	if gated.Covers(orchestrator.PauseScope{Framework: model.AgentFrameworkCodex}) ||
		gated.Covers(orchestrator.PauseScope{Team: "beta", Framework: model.AgentFrameworkAntigravity}) {
		t.Error("a usage limit stopped more than the credential that met it")
	}
}

// The other half of the response: a run already in flight when somebody
// else's limit lands is cancelled rather than left to grind through the
// same refusal, and records the same outcome with a detail that says it
// was not at fault.
func TestRunDispatchCancelsALiveRunWhenAnotherMeetsTheLimit(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	pause := &orchestrator.Pause{}
	started := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	// A claude deployment, so this run is one spending the credential the
	// limit below is about (orchestrator.Config.AgentFramework).
	cfg := orchestrator.Config{
		Pause:          pause,
		AgentFramework: model.AgentFrameworkClaude,
		Now:            func() time.Time { return baseTime },
	}

	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		done <- err
	}()

	<-started
	// Some other run, on some other task, met the limit.
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude},
		&agent.UsageLimitError{Framework: "claude", ResetAt: baseTime.Add(time.Hour)})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunDispatch err = nil, want the cancellation reported")
		}
	case <-time.After(runWaitLimit):
		t.Fatal("RunDispatch did not return -- a live run was not cancelled by the pause")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
	if !strings.Contains(runs[0].Detail, "another run") {
		t.Errorf("run detail = %q, want it to say this run was not the one at fault", runs[0].Detail)
	}
}

// And the run spending another framework's credential is left alone: a
// Gemini quota refusal -- which can be a per-minute rate limit rather
// than an exhausted daily budget -- says nothing about the Claude token,
// and cancelling a live claude run over one would charge a deployment a
// second outage on every one of them (grain/task-104).
func TestRunDispatchLeavesAnotherFrameworksRunAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	pause := &orchestrator.Pause{}
	started := make(chan struct{})
	release := make(chan struct{})
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		select {
		case <-release:
			return pushed(), nil
		case <-runCtx.Done():
			return nil, runCtx.Err()
		}
	})
	cfg := orchestrator.Config{
		Pause:          pause,
		AgentFramework: model.AgentFrameworkClaude,
		Now:            func() time.Time { return baseTime },
	}

	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		done <- err
	}()

	<-started
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkAntigravity}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkAntigravity, ResetAt: baseTime.Add(time.Hour),
	})

	select {
	case err := <-done:
		t.Fatalf("a claude run was cancelled by an antigravity usage limit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != "succeeded" {
		t.Errorf("runs = %+v, want the claude run finished on its own terms", runs)
	}
}

// Without a Pause wired (a test, a one-shot cycle) the limit is still
// recorded on the run that met it -- there is simply nothing to pause.
// RunDispatch calls every Pause method unguarded, so this is also the
// nil-safety check for that path.
func TestRunDispatchToleratesNoPause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return nil, &agent.UsageLimitError{Framework: "antigravity", Message: "quota exceeded"}
	})

	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, *task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the limit reported")
	}
	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
	if strings.Contains(runs[0].Detail, "paused until") {
		t.Errorf("run detail = %q, want no resume time named when nothing is paused", runs[0].Detail)
	}
}

// The queue half: while a framework's window is shut, a cycle
// dispatches none of that framework's tasks -- and everything else it
// does goes on as before, since syncing pull requests costs no agent
// tokens.
func TestRunCycleDispatchesNothingWhilePaused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	var runs int
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		runs++
		return pushed(), nil
	})
	pause := &orchestrator.Pause{}
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude},
		&agent.UsageLimitError{Framework: "claude", ResetAt: baseTime.Add(time.Hour)})

	_, client := newSim(t, "acme", "widgets", "main")
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			// t1 names no framework of its own, so this is the one it is
			// dispatched onto -- and the one that just met a limit.
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if runs != 0 {
		t.Errorf("dispatched %d run(s) while paused, want none", runs)
	}
	if got, err := store.Runs(ctx, "t1"); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("runs = %+v, want none started while paused", got)
	}

	// And once the provider's window has passed, the very next tick
	// dispatches again with nothing else having to happen.
	after := baseTime.Add(2 * time.Hour)
	if err := orchestrator.RunCycle(ctx, deps, after); err != nil {
		t.Fatalf("RunCycle after the window reset: %v", err)
	}
	if runs != 1 {
		t.Errorf("dispatched %d run(s) after the pause expired, want 1", runs)
	}
}

// The other half of that, and the point of grain/task-104: a task driven
// by a framework whose own credential is fine is dispatched while
// another framework's window is shut. A deployment running three
// frameworks on three credentials should lose one of them to a quota
// refusal, not all three.
func TestRunCycleKeepsDispatchingUnpausedFrameworks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	// t1 rides the deployment default (claude, below); t2 names codex
	// for itself.
	dispatchTask(t, ctx, store, "t1")
	codex := dispatchTask(t, ctx, store, "t2")
	codex.AgentFramework = model.AgentFrameworkCodex
	if err := store.PutTask(ctx, codex); err != nil {
		t.Fatal(err)
	}

	var dispatched []string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		dispatched = append(dispatched, cfg.TaskID)
		return pushed(), nil
	})
	pause := &orchestrator.Pause{}
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude, ResetAt: baseTime.Add(time.Hour),
	})

	_, client := newSim(t, "acme", "widgets", "main")
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0] != "t2" {
		t.Errorf("dispatched %v, want only the codex task t2 -- claude is the framework that is paused", dispatched)
	}

	// And nothing about the paused task was spent: no run row, so no
	// attempt and no place in its own backoff.
	if got, err := store.Runs(ctx, "t1"); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("runs for the paused framework's task = %+v, want none", got)
	}
}

// A run stopped by a usage limit must not spend the task's own retry
// budget: model.FailureStreak passes it over, so the task is dispatchable
// again the moment the pause lifts rather than backing off as though it
// had failed.
func TestAPausedRunDoesNotCountAgainstTheTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	if err := store.FinishRun(ctx, d.RunID, baseTime.Add(time.Minute), model.PausedOutcome,
		"the agent's usage limit was reached"); err != nil {
		t.Fatal(err)
	}

	streak, err := store.FailureStreak(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil {
		t.Fatal("FailureStreak = nil, want the run reported with a zero count")
	}
	if streak.Count != 0 {
		t.Errorf("FailureStreak.Count = %d after a paused run, want 0", streak.Count)
	}

	// And the task is offered for dispatch again immediately: no
	// backoff, because nothing about it failed.
	ready, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, baseTime.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].TaskID != "t1" {
		t.Errorf("dispatch.Cycle = %+v, want t1 dispatched again with no backoff", ready)
	}
}

// A run cancelled by a pause is not a run the wall-clock/task-closed
// paths should claim: their causes and this one have to stay distinct,
// since only this one means "the deployment is out of budget".
func TestUsageLimitCancellationIsItsOwnCause(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	pause := &orchestrator.Pause{}
	started := make(chan struct{})
	var cause error
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		close(started)
		<-runCtx.Done()
		cause = context.Cause(runCtx)
		return nil, runCtx.Err()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{Pause: pause}, *task, d, nil, t.TempDir(), "", nil, baseTime)
	}()
	<-started
	pause.Begin(time.Now(), orchestrator.PauseScope{}, &agent.UsageLimitError{})
	<-done

	if cause == nil || errors.Is(cause, context.Canceled) && strings.Contains(cause.Error(), "closed") {
		t.Fatalf("cause = %v, want the usage-limit cause", cause)
	}
	if !strings.Contains(cause.Error(), "usage limit") {
		t.Errorf("cause = %v, want it to name the usage limit", cause)
	}
}

// connectionFramework is an agentFunc on a model connection, the way the
// daemon's pool builds every framework (daemon.BackendFramework): it says
// which connection's credential it spends -- the one it borrows from,
// where it borrows -- and that connection's team.
type connectionFramework struct {
	agentFunc
	credential, team string
}

func (f connectionFramework) CredentialConnection() (string, string) { return f.credential, f.team }

// The point of keying a pause by connection: team alpha's connection
// meeting its limit cancels the runs spending that key -- its own, and a
// connection of alpha's that borrows it -- and leaves team beta's run on
// the same framework, on a key of its own, running. The framework-wide
// pause this replaced cancelled all three.
func TestRunDispatchPausesOnlyTheConnectionsCredential(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	cfg := orchestrator.Config{Pause: &orchestrator.Pause{}, AgentFramework: model.AgentFrameworkClaude,
		Now: func() time.Time { return baseTime }}

	type live struct {
		d    dispatch.Dispatch
		task model.Task
		done chan error
	}
	start := func(id string, fw agent.Framework, started chan struct{}) live {
		task := dispatchTask(t, ctx, store, id)
		d := dispatch.Dispatch{TaskID: id, RunID: id + "-r1", Attempt: 1}
		startRun(t, ctx, store, d, baseTime)
		l := live{d: d, task: task, done: make(chan error, 1)}
		go func() {
			_, err := orchestrator.RunDispatch(ctx, store, fw, cfg, task, d, nil, t.TempDir(), "", nil, baseTime)
			l.done <- err
		}()
		<-started
		return l
	}
	waitLive := func(started chan struct{}) agentFunc {
		return func(runCtx context.Context, _ agent.RunConfig) (*agent.Result, error) {
			close(started)
			<-runCtx.Done()
			return nil, runCtx.Err()
		}
	}

	borrowStarted, betaStarted := make(chan struct{}), make(chan struct{})
	// alpha-borrow borrows alpha-claude's key, so it reports alpha-claude.
	borrower := start("borrow", connectionFramework{waitLive(borrowStarted), "alpha-claude", "alpha"}, borrowStarted)
	beta := start("beta", connectionFramework{waitLive(betaStarted), "beta-claude", "beta"}, betaStarted)

	limitedStarted := make(chan struct{})
	limited := start("alpha", connectionFramework{func(context.Context, agent.RunConfig) (*agent.Result, error) {
		close(limitedStarted)
		return nil, &agent.UsageLimitError{Framework: "claude", ResetAt: baseTime.Add(time.Hour)}
	}, "alpha-claude", "alpha"}, limitedStarted)

	if err := <-limited.done; err == nil {
		t.Fatal("the run that met the limit reported no error")
	}
	select {
	case err := <-borrower.done:
		if err == nil {
			t.Error("the borrowing run reported no error, want its cancellation")
		}
	case <-time.After(runWaitLimit):
		t.Fatal("the run on a connection borrowing alpha-claude's key was not cancelled")
	}
	select {
	case err := <-beta.done:
		t.Fatalf("team beta's claude run was cancelled by team alpha's limit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	for _, id := range []string{"alpha", "borrow"} {
		runs, err := store.Runs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].Outcome != model.PausedOutcome {
			t.Errorf("%s runs = %+v, want one recorded as paused", id, runs)
		} else if !strings.Contains(runs[0].Detail, "alpha-claude") {
			t.Errorf("%s detail = %q, want it to name the paused connection", id, runs[0].Detail)
		}
	}
	if !cfg.Pause.ConnectionPaused("alpha-claude", "alpha", baseTime) {
		t.Error("alpha-claude is not paused after it met its limit")
	}
	if cfg.Pause.ConnectionPaused("beta-claude", "beta", baseTime) {
		t.Error("beta-claude is paused by alpha's limit")
	}
	pauses := cfg.Pause.Pauses(baseTime)
	if len(pauses) != 1 || pauses[0].Connection != "alpha-claude" || pauses[0].Team != "alpha" {
		t.Errorf("Pauses = %+v, want alpha-claude of team alpha alone", pauses)
	}
	cfg.Pause.Lift()
	select {
	case err := <-beta.done:
		t.Fatalf("beta's run ended: %v", err)
	default:
	}
}
