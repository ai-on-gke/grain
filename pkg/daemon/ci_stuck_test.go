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

package daemon

// ci_stuck_test.go covers ciJobStuck, the deployment's answer to
// ci.SharedGate.Stuck (grain/task-41): a job three gates deep behind a
// task's own agent sandbox, where that task is waiting on the very CI
// run the job belongs to, with no release ever coming from either side.
//
// Deliberately conservative, per ciJobStuck's own doc: only the genuine
// cycle admits. Every other shape of "something live is in the way"
// leaves the gate waiting, because waiting might still work.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openStuckTestStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store, ctx
}

func stuckTask(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	who := model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"}
	tk := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: who}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: who},
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("filing task %s: %v", id, err)
	}
}

func stuckRun(t *testing.T, ctx context.Context, store *model.Store, taskID, runID string, at time.Time) {
	t.Helper()
	r := model.Run{ID: runID, TaskID: taskID, Sandbox: "s", Attempt: 1, StartedAt: at}
	if err := store.StartRun(ctx, r, model.Limits{}); err != nil {
		t.Fatalf("starting run %s: %v", runID, err)
	}
}

func stuckAwaits(t *testing.T, ctx context.Context, store *model.Store, taskID, ciRunID string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, taskID, at, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: ciRunID}
	}); err != nil {
		t.Fatalf("observing %s's CI: %v", taskID, err)
	}
}

// The cycle: the only thing live on the host is the one task's agent
// sandbox, and that task is waiting on exactly the CI run the refused
// job belongs to. No release is coming from either side, so this admits.
func TestCIJobStuckAdmitsTheCycle(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	at := time.Now()
	stuckTask(t, ctx, store, "task-34")
	stuckRun(t, ctx, store, "task-34", "run-task-34", at)
	stuckAwaits(t, ctx, store, "task-34", "ci-run-1", at)

	ledger := &model.Ledger{}
	t.Cleanup(ledger.Charge(model.PoolAgent, "run-task-34", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))
	limits := func() model.Limits { return model.Limits{Ledger: ledger} }

	jobs := &ciStuckJobs{}
	jobs.queued("job-1", "ci-run-1")

	stuck, reason := ciJobStuck(store, limits, jobs, "job-1")
	if !stuck {
		t.Fatalf("want this read as a cycle, got not-stuck")
	}
	if reason == "" {
		t.Error("an admission this surprising needs a reason to log")
	}
}

// Queued behind other CI jobs is an ordinary wait: a live CI job is
// making its own progress and will release on its own, whatever its own
// run is.
func TestCIJobStuckKeepsWaitingBehindOtherCIJobs(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	at := time.Now()
	stuckTask(t, ctx, store, "task-34")
	stuckAwaits(t, ctx, store, "task-34", "ci-run-1", at)

	ledger := &model.Ledger{}
	t.Cleanup(ledger.Charge(model.PoolCI, "job-sibling", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))
	limits := func() model.Limits { return model.Limits{Ledger: ledger} }

	jobs := &ciStuckJobs{}
	jobs.queued("job-1", "ci-run-1")

	if stuck, reason := ciJobStuck(store, limits, jobs, "job-1"); stuck {
		t.Fatalf("a job merely queued behind other CI jobs must keep waiting, got stuck (%q)", reason)
	}
}

// An agent run that is live but whose own task is not waiting on this
// job's run: that sandbox may release for any number of reasons that
// have nothing to do with this job, so waiting might still work.
func TestCIJobStuckKeepsWaitingBehindAnAgentRunNotWaitingOnIt(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	at := time.Now()
	stuckTask(t, ctx, store, "task-other")
	stuckRun(t, ctx, store, "task-other", "run-task-other", at)
	stuckAwaits(t, ctx, store, "task-other", "ci-run-2", at) // a different run

	ledger := &model.Ledger{}
	t.Cleanup(ledger.Charge(model.PoolAgent, "run-task-other", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))
	limits := func() model.Limits { return model.Limits{Ledger: ledger} }

	jobs := &ciStuckJobs{}
	jobs.queued("job-1", "ci-run-1")

	if stuck, reason := ciJobStuck(store, limits, jobs, "job-1"); stuck {
		t.Fatalf("an agent run waiting on a different CI run must not read as stuck, got stuck (%q)", reason)
	}
}

// Partial: one holder genuinely waiting on this job and one that is
// not. One holder outside the accounted set is enough to call the whole
// thing unproven, because admitting here is overcommitting the host and
// that trade is only right when *nothing* live can ever release on its
// own.
func TestCIJobStuckKeepsWaitingOnAPartialMatch(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	at := time.Now()
	stuckTask(t, ctx, store, "task-34")
	stuckRun(t, ctx, store, "task-34", "run-task-34", at)
	stuckAwaits(t, ctx, store, "task-34", "ci-run-1", at)

	stuckTask(t, ctx, store, "task-other")
	stuckRun(t, ctx, store, "task-other", "run-task-other", at)
	stuckAwaits(t, ctx, store, "task-other", "ci-run-2", at)

	ledger := &model.Ledger{}
	t.Cleanup(ledger.Charge(model.PoolAgent, "run-task-34", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))
	t.Cleanup(ledger.Charge(model.PoolAgent, "run-task-other", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}))
	limits := func() model.Limits { return model.Limits{Ledger: ledger} }

	jobs := &ciStuckJobs{}
	jobs.queued("job-1", "ci-run-1")

	if stuck, reason := ciJobStuck(store, limits, jobs, "job-1"); stuck {
		t.Fatalf("one holder not accounted for must keep the whole gate waiting, got stuck (%q)", reason)
	}
}

// A job this registry never heard of -- one at a gate with no Stuck hook
// bothering to record it, or one this hook is simply asked about by
// mistake -- answers false rather than panicking on an empty run id.
func TestCIJobStuckAnswersFalseForAnUnknownJob(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	_ = ctx
	ledger := &model.Ledger{}
	limits := func() model.Limits { return model.Limits{Ledger: ledger} }
	if stuck, _ := ciJobStuck(store, limits, &ciStuckJobs{}, "job-nobody-queued"); stuck {
		t.Fatal("a job this registry has no run for must not be called stuck")
	}
}
