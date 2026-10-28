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

// Whether the daemon's own loop keeps working while a run is in flight.
//
// pkg/orchestrator proves RunCycle hands a dispatch off rather than
// waiting for it (concurrent_dispatch_test.go); this proves the half
// that only exists here: reconcile ticks again while an agent is still
// running, so a task filed a moment after a run started is dispatched
// within a poll interval rather than after however long that run takes,
// and drainInFlight waits for what is still live once the loop stops.
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// blockingAgent never finishes on its own: it announces the run it was
// given (by the sandbox directory that run is named after) and waits, so
// this test can hold a run live while it watches what the loop around it
// does.
type blockingAgent struct {
	entered chan string
	release chan struct{}
}

func (a *blockingAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	a.entered <- filepath.Base(cfg.SandboxRoot)
	select {
	case <-a.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &agent.Result{ToolCalls: []agent.ToolCall{{
		Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"},
	}}}, nil
}

func TestReconcileDispatchesWhileAnEarlierRunIsStillLive(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"

	// A real bare repo for the Sim to shell out to, seeded the same way
	// daemon_live_test.go seeds its own.
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, repoName+".git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, upstream, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	runLive(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	runLive(t, seed, "git", "push", "-q", "origin", "main")

	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentStub := &blockingAgent{entered: make(chan string, 4), release: make(chan struct{})}
	runs := &orchestrator.InFlight{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     github.NewClient(githubsim.New(owner, repoName, bare, "main"), nil),
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(agentStub),
		MaxWorkers: 2,
		Runs:       runs,
	}

	seedReconcileTask(t, store, "t1", owner, repoName)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		daemon.ReconcileWith(ctx, deps, daemon.NewLiveConfig(store, deps.Sandboxes, daemon.Config{PollInterval: 20 * time.Millisecond}, nil), nil, nil)
	}()
	awaitRun(t, agentStub, "t1-1")

	// The case a deployment hits constantly, and the one the loop used to
	// sleep through: a task filed while something is already running.
	seedReconcileTask(t, store, "t2", owner, repoName)
	awaitRun(t, agentStub, "t2-1")

	// Both runs finish on their own, with the loop still ticking around
	// them -- a run's own row is finished by the goroutine that outlived
	// the cycle, not by anything the loop does afterwards.
	close(agentStub.release)
	waitCtx, waitCancel := context.WithTimeout(ctx, daemonWaitLimit)
	defer waitCancel()
	if err := runs.Wait(waitCtx); err != nil {
		// Not fatal: the store is what says what actually happened, and
		// the assertions below say more about it than this error does.
		// A wait that times out here means something is still being
		// dispatched, which is exactly what they report.
		t.Errorf("waiting for the dispatched runs: %v", err)
	}

	// Stop the loop before reading the store, and only once the runs it
	// was meant to have outlived are over. InFlight.Wait's condition is
	// "nothing is live", not "these two runs ended" (its own doc
	// comment), so a loop left ticking here would leave the assertions
	// below racing whatever it dispatched next -- and this test is about
	// what the loop does while a run is live, not about how it behaves
	// once nothing is.
	cancel()
	select {
	case <-loopDone:
	case <-time.After(daemonWaitLimit):
		t.Fatal("reconcile did not return after its context was cancelled")
	}
	daemon.DrainInFlight(runs)

	// One run each, and no second attempt at either. Both tasks were
	// completed by their own run (the agent's comment_on_issue is a
	// closing note, orchestrator.ProcessResult), so a task dispatched
	// again here is a task run twice over: the cycle that did it would
	// have been deciding from a ready list read before that completion
	// landed, which is the window dispatch.stillReady closes and which
	// this loop, ticking every 20ms around two runs that end together,
	// is the most likely thing in the tree to hit.
	byTask := map[string][]model.Run{}
	for _, id := range []string{"t1", "t2"} {
		got, err := store.Runs(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		byTask[id] = got
		if len(got) != 1 || got[0].FinishedAt == nil {
			t.Errorf("%s's runs = %+v, want exactly one, finished", id, got)
		}
	}

	// The claim in this test's name, as the store recorded it: t2's run
	// started before t1's had finished. awaitRun proved it of the agent
	// (t2 entered while t1 was blocked inside it); this is the same fact
	// about the rows a human reads afterwards.
	if len(byTask["t1"]) > 0 && len(byTask["t2"]) > 0 {
		first, second := byTask["t1"][0], byTask["t2"][0]
		if first.FinishedAt != nil && !second.StartedAt.Before(*first.FinishedAt) {
			t.Errorf("%s started at %s, after %s finished at %s: the two runs never overlapped",
				second.ID, second.StartedAt, first.ID, *first.FinishedAt)
		}
	}
}

// A run cancelled by a shutdown still has cleanup to do -- its sandbox is
// released on a context detached from that cancellation -- and
// drainInFlight is what gives it the chance. Without the drain the
// process exits first and leaves the sandbox (a granule VM, in a
// deployment) behind for the next start to reap.
func TestDrainInFlightWaitsForACancelledRunToReleaseItsSandbox(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"

	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, repoName+".git"))
	seed := filepath.Join(t.TempDir(), "seed")
	runLive(t, upstream, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	runLive(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	runLive(t, seed, "git", "push", "-q", "origin", "main")

	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Never released: this run is still inside the agent when the
	// shutdown reaches it.
	agentStub := &blockingAgent{entered: make(chan string, 4), release: make(chan struct{})}
	sandboxDir := t.TempDir()
	runs := &orchestrator.InFlight{}
	deps := orchestrator.Deps{
		Store:      store,
		Client:     github.NewClient(githubsim.New(owner, repoName, bare, "main"), nil),
		Sandboxes:  orchestrator.NewHostSandboxes(sandboxDir),
		Framework:  orchestrator.StaticFramework(agentStub),
		MaxWorkers: 1,
		Runs:       runs,
	}

	seedReconcileTask(t, store, "t1", owner, repoName)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		daemon.ReconcileWith(ctx, deps, daemon.NewLiveConfig(store, deps.Sandboxes, daemon.Config{PollInterval: 20 * time.Millisecond}, nil), nil, nil)
	}()
	awaitRun(t, agentStub, "t1-1")

	cancel()
	<-loopDone
	daemon.DrainInFlight(runs)

	if got := runs.Len(); got != 0 {
		t.Fatalf("%d run(s) still in flight after the drain: %v", got, runs.Runs())
	}
	if _, err := os.Stat(filepath.Join(sandboxDir, "t1-1")); !os.IsNotExist(err) {
		t.Errorf("run t1-1's sandbox directory survived the drain (stat: %v)", err)
	}
}

// awaitRun fails, rather than hanging, if the agent is not handed the run
// it is waiting for -- a loop that never ticks again looks exactly like a
// hang otherwise.
func awaitRun(t *testing.T, a *blockingAgent, want string) {
	t.Helper()
	select {
	case got := <-a.entered:
		if got != want {
			t.Fatalf("the agent ran for %s, want %s", got, want)
		}
	case <-time.After(daemonWaitLimit):
		t.Fatalf("no agent ran for %s while an earlier run was still live", want)
	}
}

func seedReconcileTask(t *testing.T, store *model.Store, id, owner, repoName string) {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "tester"}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "a task", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &model.RepoRef{Owner: owner, Name: repoName},
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
}
