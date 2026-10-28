// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator_test

// What a usage limit costs the deployment in resources, rather than in
// outcomes. usagelimit_test.go has the outcomes -- the run row, the
// detail, which frameworks stop dispatching -- asserted against
// RunDispatch on its own, with no sandbox anywhere near it. These drive
// the same wall through a whole RunCycle, which is where the sandboxes
// are: one is built for each dispatch and torn down with it (cycle.go's
// runOne), and a pause ends runs in the middle of holding them.
//
// Two things follow from that and neither is asserted anywhere else.
// First, a run the pause cancels mid-flight has to release its sandbox
// exactly as a run that finished does -- a granule VM, or here a
// directory, held by a run nothing is left to end is a resource an
// outage leaks once per run in flight. Second, the task comes back: a
// paused run spends no attempt of the task's own failure streak, so once
// the provider's window resets the very next tick dispatches it again --
// into a *new* sandbox, as a new run, with the conversation the last
// attempt had gone for good. What survives instead is what the store
// holds, and the prompt has to say so, or the next attempt reads the
// branch its predecessor pushed as somebody else's work.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// sandboxesLeft names what is still on disk under a HostSandboxes base
// directory: one entry per sandbox that was built and not released.
func sandboxesLeft(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("listing sandbox directories under %q: %v", base, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// One run meets the limit while another is in flight on the same
// credential. Both end -- the one that found the wall and the one the
// pause cancelled -- and both give their sandboxes back. The cancelled
// run is the interesting half: nothing about it failed, so nothing about
// it is on any error path, and its release runs from a deferred call on
// a context that was itself cancelled.
func TestAUsageLimitEndsEveryRunInFlightAndLeavesNoSandboxBehind(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	dispatchTask(t, ctx, store, "t2")

	base := t.TempDir()
	var (
		mu       sync.Mutex
		roots    = map[string]string{}
		inFlight = make(chan struct{})
	)
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		mu.Lock()
		roots[cfg.TaskID] = cfg.SandboxRoot
		mu.Unlock()
		if cfg.TaskID == "t2" {
			// The bystander: in flight, spending the same credential,
			// and with nothing wrong with it at all.
			close(inFlight)
			<-runCtx.Done()
			return nil, runCtx.Err()
		}
		// The run that finds the wall, once the other one is definitely
		// running -- otherwise this test would pass without ever having
		// cancelled anything.
		select {
		case <-inFlight:
		case <-time.After(10 * time.Second):
			t.Error("the second task was never dispatched; this cycle cancelled nothing")
		}
		return nil, &agent.UsageLimitError{
			Framework: model.AgentFrameworkClaude,
			Message:   "Claude AI usage limit reached",
			ResetAt:   baseTime.Add(time.Hour),
		}
	})

	pause := &orchestrator.Pause{}
	_, client := newSim(t, "acme", "widgets", "main")
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(base),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}
	// The cycle reports what its dispatches ended as, and a usage limit
	// is one of those: this is the reconciler's own error, not a failure
	// of the cycle to do its work.
	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("RunCycle err = %v, want it to report the limit its dispatch met", err)
	}

	// Both runs are over, and both are the deployment's doing rather
	// than the task's: model.PausedOutcome is the word neither streak
	// counts.
	for _, id := range []string{"t1", "t2"} {
		runs, err := store.Runs(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 {
			t.Fatalf("%s has %d run(s), want exactly one", id, len(runs))
		}
		if runs[0].Outcome != model.PausedOutcome {
			t.Errorf("%s's run ended %q, want %q", id, runs[0].Outcome, model.PausedOutcome)
		}
		if runs[0].FinishedAt == nil {
			t.Errorf("%s's run was left live: a run nothing finishes holds a worker slot forever", id)
		}
	}
	if len(roots) != 2 {
		t.Fatalf("the agent was given %d sandbox root(s), want one per task: %v", len(roots), roots)
	}
	if roots["t1"] == roots["t2"] {
		t.Errorf("both runs were given the same sandbox %q", roots["t1"])
	}

	// The whole point of this file: neither sandbox outlived the runs the
	// pause ended.
	if left := sandboxesLeft(t, base); len(left) != 0 {
		t.Errorf("sandboxes left behind by a usage-limit pause: %v", left)
	}
	for _, root := range roots {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("sandbox %q still on disk after the run holding it was paused (stat err = %v)", root, err)
		}
	}

	// And dispatch onto that credential is shut, which is what stops the
	// next tick walking into the same refusal.
	if gated := pause.Blocked(baseTime); !gated.Covers(orchestrator.PauseScope{Framework: model.AgentFrameworkClaude}) {
		t.Error("claude dispatch was left open after a claude run met its usage limit")
	}
}

// The other end of the same window. A paused run spends none of the
// task's own retry budget, so the tick after the provider's window
// resets dispatches the task again -- and what it gets is a new run in a
// new sandbox, not the one that was cancelled. The conversation the
// first attempt had is gone with its sandbox; what carries across is the
// store's own record of it, which is what the prompt has to say.
func TestATaskPausedByAUsageLimitIsRedispatchedIntoAFreshSandbox(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	base := t.TempDir()
	var (
		mu      sync.Mutex
		roots   []string
		prompts []string
	)
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		mu.Lock()
		roots = append(roots, cfg.SandboxRoot)
		prompts = append(prompts, cfg.Prompt)
		attempt := len(roots)
		mu.Unlock()
		if attempt == 1 {
			return nil, &agent.UsageLimitError{
				Framework: model.AgentFrameworkClaude,
				Message:   "Claude AI usage limit reached",
				ResetAt:   baseTime.Add(time.Hour),
			}
		}
		return pushed(), nil
	})

	pause := &orchestrator.Pause{}
	sim, client := newSim(t, "acme", "widgets", "main")
	// The branch the second attempt's own push lands on, so the run that
	// carries the task ends as a run that pushed something rather than
	// as one whose branch GitHub has never heard of.
	pushBranch(t, sim.BareRepo, model.BranchName("t1"))
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(base),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			Pause:          pause,
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}
	// The cycle reports the limit its dispatch met; that is the
	// reconciler saying what happened, not the cycle failing.
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil ||
		!strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("RunCycle err = %v, want it to report the limit its dispatch met", err)
	}
	// The window has not reset yet, so this tick dispatches nothing at
	// all -- and in particular does not start a second attempt into the
	// same refusal.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(30*time.Minute)); err != nil {
		t.Fatalf("RunCycle inside the paused window: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("the agent ran %d time(s) before the window reset, want 1", len(roots))
	}

	after := baseTime.Add(2 * time.Hour)
	if err := orchestrator.RunCycle(ctx, deps, after); err != nil {
		t.Fatalf("RunCycle after the window reset: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("the agent ran %d time(s) in all, want 2 -- the paused attempt and the one after the window", len(roots))
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want two", runs)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("the first run ended %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
	if runs[1].Outcome != "succeeded" {
		t.Errorf("the second run ended %q, want it to have succeeded", runs[1].Outcome)
	}
	if runs[1].Attempt != 2 {
		t.Errorf("the second run is attempt %d, want 2", runs[1].Attempt)
	}
	if runs[0].ID == runs[1].ID {
		t.Error("the redispatch reused the paused run's row; a resumed task is a new run")
	}

	// A new sandbox, and the old one gone: the second attempt is not
	// picking anything up from the first one's disk, and the first one's
	// disk is not still there either.
	if roots[0] == roots[1] {
		t.Errorf("both attempts ran in %q; a sandbox belongs to one run and is torn down with it", roots[0])
	}
	if left := sandboxesLeft(t, base); len(left) != 0 {
		t.Errorf("sandboxes left behind after both runs finished: %v", left)
	}

	// What does carry across is the record. The second attempt is told
	// that the first one ended paused rather than failed -- the branch it
	// is about to find commits on was pushed by a run that was stopped
	// for budget, and a prompt that did not say so would leave it reading
	// its predecessor's work as somebody else's.
	if strings.Contains(prompts[0], "attempted before") {
		t.Errorf("the first attempt was told about earlier attempts it did not have:\n%s", prompts[0])
	}
	for _, want := range []string{"attempted before", model.PausedOutcome, "usage limit"} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("the redispatched attempt's prompt does not mention %q:\n%s", want, prompts[1])
		}
	}
}

// The other thing a run has usually done by the time it meets the wall:
// pushed something. The sandbox goes, and with it the checkout and the
// conversation, but the commits are on GitHub -- so a run stopped by a
// quota refusal has its branch salvaged into a pull request exactly as a
// run that ran out of turns does, and keeps model.PausedOutcome while
// doing it. Recording it as "failed" to explain the pull request would
// put the outage on the task's own streak, which is the one thing
// PausedOutcome exists to prevent.
func TestAPausedRunsPushedBranchIsStillSalvaged(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	dispatchTask(t, ctx, store, "t1")
	pushBranch(t, sim.BareRepo, model.BranchName("t1"))

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		// agent.Framework's own contract: the work comes back alongside
		// the refusal, never instead of it.
		return pushed(), &agent.UsageLimitError{
			Framework: model.AgentFrameworkClaude,
			Message:   "Claude AI usage limit reached",
			ResetAt:   baseTime.Add(time.Hour),
		}
	})
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			Pause:          &orchestrator.Pause{},
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil ||
		!strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("RunCycle err = %v, want it to report the limit its dispatch met", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the paused run's branch salvaged into one", sim.PullRequests)
	}
	st, err := store.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Errorf("state = %q, want awaiting_submit: the branch is real whatever stopped the run", st)
	}
	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("run outcome = %q, want %q: salvaging the branch must not turn an outage into this task's failure",
			runs[0].Outcome, model.PausedOutcome)
	}
	if streak, err := store.FailureStreak(ctx, "t1"); err != nil {
		t.Fatal(err)
	} else if streak != nil && streak.Count != 0 {
		t.Errorf("FailureStreak.Count = %d after a paused run that pushed, want 0", streak.Count)
	}
}

// The race the pause has to cover on its own: a run dispatched by the
// same tick that another run's limit landed in. reconcileDispatch will
// not dispatch a framework that is already paused, so this one arrives
// through the gap between that check and the run starting -- and it is
// stopped as it registers rather than after an agent's worth of
// wall-clock time spent meeting the same refusal for itself.
func TestARunStartingIntoAnOpenPauseIsStoppedBeforeItWorks(t *testing.T) {
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
	pause.Begin(baseTime, orchestrator.PauseScope{Framework: model.AgentFrameworkClaude}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkClaude, ResetAt: baseTime.Add(time.Hour),
	})

	var (
		ran       bool
		cancelled bool
	)
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		ran = true
		// A framework is handed a context that is already done: it does
		// not have to notice, but this is what one that does sees, and
		// what every real framework's first exec would be killed by.
		select {
		case <-runCtx.Done():
			cancelled = true
		default:
		}
		return nil, runCtx.Err()
	})
	cfg := orchestrator.Config{
		Pause:          pause,
		AgentFramework: model.AgentFrameworkClaude,
		Now:            func() time.Time { return baseTime },
	}

	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the run reported as stopped by the pause")
	}
	if !ran {
		t.Fatal("the framework was never called; this test proves nothing about what it was handed")
	}
	if !cancelled {
		t.Error("the agent was started with a live context while its framework's dispatch was paused")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.PausedOutcome {
		t.Fatalf("runs = %+v, want one recorded %q", runs, model.PausedOutcome)
	}
	if !strings.Contains(runs[0].Detail, "another run") {
		t.Errorf("run detail = %q, want it to say this run was not the one at fault", runs[0].Detail)
	}
	// And the window it was stopped by is the one that was already in
	// force: registering into a pause must not extend it, or a queue
	// dispatching into a shut gate would keep pushing the reset away.
	if until, _ := pause.Until(); !until.Equal(baseTime.Add(time.Hour)) {
		t.Errorf("pause until = %s, want the window that was already in force (%s)", until, baseTime.Add(time.Hour))
	}
}
