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

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// listedHostd is a grain hostd that says hello as peer, lists procs, and
// records what it was told to cancel.
type listedHostd struct {
	peer     hostd.Hello
	helloErr error
	procs    []hostd.Process

	mu        sync.Mutex
	cancelled []string
}

func (l *listedHostd) Hello(context.Context) (hostd.Hello, error) { return l.peer, l.helloErr }
func (l *listedHostd) Processes(context.Context) ([]hostd.Process, error) {
	return l.procs, nil
}
func (l *listedHostd) Cancel(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelled = append(l.cancelled, id)
	return nil
}

func liveRunIDs(t *testing.T, ctx context.Context, store *model.Store) []string {
	t.Helper()
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids
}

// The hostd adoption pass: a live row with a hostd cursor whose process
// still runs is adopted from that cursor with the gap credited; one whose
// process is gone, or past its deadline, is finished with the listing's
// reason; a process no live row claims is cancelled; a row driven in a
// run host, or never driven anywhere, is not this pass's; and an adopted
// row is spared by RecoverOrphanedRunsExcept, so it is never re-dispatched.
func TestRecoverHostdAdoptsReleasesAndFinishes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, id := range []string{"kept", "gone", "late", "runhost", "local"} {
		task := filedTask(t, ctx, store, id, repo)
		startRun(t, ctx, store, dispatch.Dispatch{TaskID: task.ID, RunID: id + "-1", Attempt: 1}, baseTime)
	}
	for run, cursor := range map[string]int64{"kept-1": 4096, "gone-1": 10, "late-1": 0} {
		if err := store.SetHostdCursor(ctx, run, cursor, baseTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSpoolCursor(ctx, "runhost-1", 0, baseTime); err != nil {
		t.Fatal(err)
	}
	now := baseTime.Add(2 * time.Minute)
	hd := &listedHostd{peer: hostd.ThisHello("next"), procs: []hostd.Process{
		{ID: "p-kept", Run: "kept-1", Task: "kept", Detached: true, PID: 41, Deadline: now.Add(time.Hour)},
		{ID: "p-late", Run: "late-1", Task: "late", Detached: true, PID: 42, Deadline: now.Add(-time.Second)},
		{ID: "p-stray", Run: "finished-long-ago", Detached: true, PID: 43},
		// A protocol-1 start is not adoptable, and not this pass's.
		{ID: "p-legacy", Run: "local-1", PID: 44},
	}}
	var adopted []string
	var cursor int64
	plan, err := orchestrator.RecoverHostd(ctx, store, client, hd, "this", now,
		func(a runhost.Adoption, c int64) bool {
			adopted = append(adopted, a.Claim.Run)
			cursor = c
			if id, _ := runhost.HostdID(a.Host); id != "p-kept" {
				t.Errorf("adopted process %q, want p-kept", id)
			}
			return true
		})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(adopted, ",") != "kept-1" || cursor != 4096 || len(plan.Adopt) != 1 || plan.Adopt[0].Gap != 2*time.Minute {
		t.Fatalf("adopted %v at cursor %d, plan %+v", adopted, cursor, plan)
	}
	slices.Sort(hd.cancelled)
	if strings.Join(hd.cancelled, ",") != "p-late,p-stray" {
		t.Fatalf("cancelled %v, want the expired and the unclaimed process", hd.cancelled)
	}
	if got := strings.Join(liveRunIDs(t, ctx, store), ","); got != "kept-1,local-1,runhost-1" {
		t.Fatalf("live runs after the pass = %v", got)
	}
	if err := orchestrator.RecoverOrphanedRunsExcept(ctx, store, client, now, map[string]bool{"kept-1": true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(liveRunIDs(t, ctx, store), ","); got != "kept-1" {
		t.Fatalf("live runs after orphan recovery = %v, want only the adopted one", got)
	}
	for _, task := range []string{"gone", "late"} {
		if st, err := store.State(ctx, task); err != nil || st != model.StateQueued {
			t.Fatalf("task %s is %q (%v), want queued again", task, st, err)
		}
	}
}

// A hostd this build cannot talk to finishes the rows it holds a process
// for, naming the mismatch, and releases the processes; a hostd that does
// not answer decides nothing.
func TestRecoverHostdMismatchAndSilence(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}, baseTime)
	if err := store.SetHostdCursor(ctx, "t-1", 0, baseTime); err != nil {
		t.Fatal(err)
	}
	silent := &listedHostd{helloErr: errors.New("connection refused")}
	if _, err := orchestrator.RecoverHostd(ctx, store, client, silent, "this", baseTime, nil); err == nil {
		t.Fatal("a hostd that did not answer was taken as an empty listing")
	}
	if got := liveRunIDs(t, ctx, store); len(got) != 1 {
		t.Fatalf("live runs = %v, want the row left to orphan recovery", got)
	}
	future := &listedHostd{peer: hostd.Hello{Protocol: 9, MinCompatible: 8},
		procs: []hostd.Process{{ID: "p", Run: "t-1", Detached: true}}}
	plan, err := orchestrator.RecoverHostd(ctx, store, client, future, "this", baseTime, func(runhost.Adoption, int64) bool {
		t.Error("adopted from a hostd this build cannot talk to")
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Finish) != 1 || !strings.Contains(plan.Finish[0].Reason, "could not be re-attached") {
		t.Fatalf("plan %+v", plan)
	}
	if strings.Join(future.cancelled, ",") != "p" {
		t.Fatalf("cancelled %v", future.cancelled)
	}
	if got := liveRunIDs(t, ctx, store); len(got) != 0 {
		t.Fatalf("live runs = %v, want none", got)
	}
}

// wakingHostd is a grain hostd that does not answer hello the first
// silentFor times it is asked -- restarting onto a new build, or a hostd
// that dials in and has not yet -- and answers as listedHostd after.
type wakingHostd struct {
	listedHostd
	silentFor int
	hellos    int
}

func (w *wakingHostd) Hello(ctx context.Context) (hostd.Hello, error) {
	w.mu.Lock()
	w.hellos++
	silent := w.hellos <= w.silentFor
	w.mu.Unlock()
	if silent {
		return hostd.Hello{}, errors.New("dial unix hostd.sock: connect: no such file or directory")
	}
	return w.listedHostd.Hello(ctx)
}

// A hostd that is mid-restart at startup decides nothing, but names the
// runs it leaves to RecoverOrphanedRuns; once it answers again, the
// processes of those runs that were finished are cancelled, so none
// holds a host CLI slot until its own deadline or runs beside its run's
// next attempt -- whose process, like one of a run still live, is left
// alone.
func TestRecoverHostdCancelsAbandonedRunsWhenHostdIsBack(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, id := range []string{"away", "kept"} {
		task := filedTask(t, ctx, store, id, repo)
		startRun(t, ctx, store, dispatch.Dispatch{TaskID: task.ID, RunID: id + "-1", Attempt: 1}, baseTime)
		if err := store.SetHostdCursor(ctx, id+"-1", 128, baseTime); err != nil {
			t.Fatal(err)
		}
	}
	hd := &wakingHostd{silentFor: 3, listedHostd: listedHostd{peer: hostd.ThisHello("next")}}
	now := baseTime.Add(time.Minute)
	_, err := orchestrator.RecoverHostd(ctx, store, client, hd, "this", now, func(runhost.Adoption, int64) bool {
		t.Error("adopted from a hostd that did not answer")
		return true
	})
	var away *orchestrator.HostdAwayError
	if !errors.As(err, &away) {
		t.Fatalf("err = %v, want a HostdAwayError", err)
	}
	slices.Sort(away.Runs)
	if strings.Join(away.Runs, ",") != "away-1,kept-1" {
		t.Fatalf("runs left = %v", away.Runs)
	}
	// "kept-1" stands for a run something else took back; "away-1" is
	// finished as interrupted and its task queued again.
	if err := orchestrator.RecoverOrphanedRunsExcept(ctx, store, client, now, map[string]bool{"kept-1": true}); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, "away"); err != nil || st != model.StateQueued {
		t.Fatalf("task away is %q (%v), want queued again", st, err)
	}
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: "away", RunID: "away-2", Attempt: 2}, now)

	hd.procs = []hostd.Process{
		{ID: "p-away-1", Run: "away-1", Task: "away", Detached: true, PID: 41},
		{ID: "p-kept-1", Run: "kept-1", Task: "kept", Detached: true, PID: 42},
		{ID: "p-away-2", Run: "away-2", Task: "away", Detached: true, PID: 43},
	}
	reconnects := 0
	cancelled, err := orchestrator.CancelWhenBack(ctx, store, hd, away.Runs, func(context.Context) error {
		reconnects++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cancelled, ",") != "p-away-1" || strings.Join(hd.cancelled, ",") != "p-away-1" {
		t.Fatalf("cancelled %v (hostd told %v), want only the abandoned run's process", cancelled, hd.cancelled)
	}
	if hd.hellos != 4 || reconnects != 3 {
		t.Fatalf("hello asked %d time(s) over %d wait(s), want once at startup and until it answered", hd.hellos, reconnects)
	}
}

// CancelWhenBack gives up when its context does, cancelling nothing.
func TestCancelWhenBackGivesUpWithItsContext(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	hd := &wakingHostd{silentFor: 1 << 30, listedHostd: listedHostd{peer: hostd.ThisHello("next")}}
	wctx, cancel := context.WithCancel(ctx)
	cancelled, err := orchestrator.CancelWhenBack(wctx, store, hd, []string{"r-1"}, func(context.Context) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(cancelled) != 0 || len(hd.cancelled) != 0 {
		t.Fatalf("cancelled %v, err %v", cancelled, err)
	}
}
