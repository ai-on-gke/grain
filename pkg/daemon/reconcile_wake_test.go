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

package daemon_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type stubWakeAgent struct {
	onRun func(context.Context, agent.RunConfig) (*agent.Result, error)
}

func (a *stubWakeAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return a.onRun(ctx, cfg)
}

func TestReconcileWithDispatchesAfterSlowInFlightUnwindAndDefersBackgroundCycle(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	t1Entered := make(chan struct{})
	releaseT1 := make(chan struct{})
	t2Dispatched := make(chan time.Time, 1)
	t3Dispatched := make(chan struct{})
	var t3Once sync.Once
	var t1Once sync.Once
	var t2Once sync.Once

	ag := &stubWakeAgent{
		onRun: func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			if cfg.TaskID == "t1" {
				t1Once.Do(func() { close(t1Entered) })
				select {
				case <-releaseT1:
				case <-runCtx.Done():
					return nil, runCtx.Err()
				}
			}
			if cfg.TaskID == "t2" {
				t2Once.Do(func() { t2Dispatched <- time.Now() })
			}
			if cfg.TaskID == "t3" {
				t3Once.Do(func() { close(t3Dispatched) })
			}
			return &agent.Result{
				FinalText:        "done",
				Transcript:       "done",
				ResumableSession: "sess-1",
			}, nil
		},
	}

	sbx := orchestrator.NewHostSandboxes(t.TempDir())
	inFlight := &orchestrator.InFlight{}
	t.Cleanup(func() { _ = inFlight.Wait(context.Background()) })
	cycleTimes := orchestrator.NewCycleTimes(10)

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 1,
		Runs:       inFlight,
		CycleTimes: cycleTimes,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(),
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	wakes := make(chan struct{}, 8)
	wake := func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	}

	live := daemon.NewLiveConfig(store, sbx, daemon.Config{PollInterval: 30 * time.Second}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.ReconcileWith(ctx, deps, live, wakes, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, observed := cycleTimes.History(); observed >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for initial reconcile cycle")
		}
		time.Sleep(10 * time.Millisecond)
	}

	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	putInteractive := func(id string) {
		now := time.Now().UTC()
		if err := store.PutTask(ctx, model.Task{
			ID:          id,
			Intent:      model.IntentImplement,
			Title:       "Wake test " + id,
			Body:        "prompt",
			Interactive: true,
			Filing:      model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval:    &model.Attribution{Actor: actor},
			CreatedAt:   &now,
		}); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}

	// Start t1 via a fast wake.
	putInteractive("t1")
	wake()
	select {
	case <-t1Entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for t1 to start")
	}
	t1Start := time.Now()

	// Queue t2 and wake while t1 holds the single worker slot for longer than
	// minWakeInterval (200ms > 50ms). Without InFlight.SubscribeDone, a single
	// 50ms timer retry fired while t1 was still running and stranded t2 when
	// Ledger was nil.
	putInteractive("t2")
	wake()
	time.Sleep(200 * time.Millisecond)

	// Time t2's dispatch from the moment t1's run has actually finished --
	// InFlight's own done signal, which ReconcileWith's SubscribeDone wake
	// is driven by -- not from when the stub agent was released: t1's
	// unwind in between (ProcessResult, releasing its sandbox and
	// capabilities) is not what this test measures, and under -race on a
	// loaded runner it alone takes over a second.
	t1Done, stopT1Done := inFlight.SubscribeDone()
	defer stopT1Done()
	close(releaseT1)
	var t1DoneAt time.Time
	select {
	case <-t1Done:
		t1DoneAt = time.Now()
	case <-time.After(10 * time.Second):
		t.Fatal("t1's run did not finish after it was released")
	}

	select {
	case gotAt := <-t2Dispatched:
		if elapsed := gotAt.Sub(t1DoneAt); elapsed >= 3*time.Second {
			t.Errorf("t2 dispatched %v after t1 finished, want prompt dispatch via SubscribeDone (< 3s, PollInterval=30s)", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("t2 was not dispatched after t1 finished")
	}

	// Neither fast dispatch should have forced an immediate second full
	// 47-reconciler RunCycle within the interactive exchange window.
	if _, observed := cycleTimes.History(); observed != 1 {
		t.Errorf("CycleTimes observed %d full RunCycles during rapid woken dispatches, want 1 (initial tick only)", observed)
	}

	// A later dispatch must not postpone the woken full cycle t1's dispatch
	// already scheduled (t1Start + dispatchedWakeHeadStart): re-arming it on
	// every dispatch let a busy deployment fall back to the poll interval.
	for time.Since(t1Start) < 3*time.Second {
		time.Sleep(20 * time.Millisecond)
	}
	putInteractive("t3")
	wakeAt := time.Now()
	wake()
	select {
	case <-t3Dispatched:
	case <-time.After(10 * time.Second):
		t.Fatal("t3 was not dispatched")
	}
	// The second full cycle is the one t1's dispatch scheduled. Compare when
	// it started, not when it finished (a full cycle under -race on a loaded
	// runner takes seconds): kept, it starts about head-start after t1's
	// dispatch, at most ~2s after t3's wake; re-armed by t3's dispatch, it
	// could start no sooner than a whole head start after that wake.
	var second orchestrator.CycleTiming
	deadline = time.Now().Add(25 * time.Second)
	for {
		if cycles, observed := cycleTimes.History(); observed >= 2 {
			second = cycles[1]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no woken full RunCycle at all within 25s of t3's dispatch")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if limit := wakeAt.Add(daemon.DispatchedWakeHeadStartForTest - time.Second); !second.Start.Before(limit) {
		t.Errorf("woken full RunCycle started %v after t3's wake, want < %v: t3's dispatch postponed the cycle t1's dispatch had scheduled",
			second.Start.Sub(wakeAt), daemon.DispatchedWakeHeadStartForTest-time.Second)
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	if err := inFlight.Wait(waitCtx); err != nil {
		t.Fatalf("inFlight.Wait: %v", err)
	}
}
