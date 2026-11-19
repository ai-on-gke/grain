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

package gateobservationtest

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// readOnlyBudget bounds the reads expected to resolve from the read-only
// snapshot while another transaction holds the writer lock. It is deliberately
// generous so a loaded CI runner (race detector, parallel packages) cannot turn
// a slow but lock-free read into a failure; it still distinguishes the writer
// path, which cannot complete until the held writer lock is released and so
// fails at this deadline (or at the engine's own lock timeout) instead.
const readOnlyBudget = 10 * time.Second

// RunReadOnlyPrecheck verifies that steady-state gate observation reads
// (unchanged source fence inside the diagnostic lease, persisted error backoff,
// and transient access-limited health views) resolve from a read-only snapshot
// transaction without taking SQLite's write lock or Postgres's advisory writer
// lock, while fence changes and expired leases still fall through to the
// serialized writer path and re-evaluate.
func RunReadOnlyPrecheck(t *testing.T, s *model.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedReport := func(t *testing.T, id string) {
		t.Helper()
		task := model.Task{
			ID:        id,
			Intent:    model.IntentAnalyze,
			Title:     "Read-only precheck " + id,
			Filing:    model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: human},
			Binding:   model.BindingDirective,
			CreatedAt: &base,
		}
		must(t, s.PutTask(ctx, task))
		runID := "run-" + id
		must(t, s.StartRun(ctx, model.Run{ID: runID, TaskID: id, Attempt: 1, StartedAt: base.Add(time.Second), Sandbox: "test"}, model.Limits{}))
		author := model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: id}}
		_, err := s.AddComment(ctx, model.Comment{TaskID: id, Author: author, Body: "Durable report for " + id, CreatedAt: base.Add(2 * time.Second)})
		must(t, err)
		done := base.Add(2 * time.Second)
		must(t, s.FinishRun(ctx, runID, done, "succeeded", ""))
		must(t, s.ObserveField(ctx, id, done, func(o *model.Observation) { o.CompletedAt = &done }))
	}
	shadowCounts := func(t *testing.T, id string) (gen, evals uint64) {
		t.Helper()
		must(t, db.QueryRowContext(ctx, "SELECT `generation`,`evaluations` FROM `gate_observation_shadow` WHERE `task_id` = ?", id).Scan(&gen, &evals))
		return gen, evals
	}

	t.Run("steady-state-and-changed-fence", func(t *testing.T) {
		id := "readonly-steady"
		seedReport(t, id)
		now := base.Add(5 * time.Second)
		initialView, initialDiag, err := s.ObserveTaskGates(ctx, id, now)
		must(t, err)
		if initialView.Results[0].Result.Status != "met" || initialView.Results[1].Result.Status != "pending" {
			t.Fatalf("initial results: %+v", initialView.Results)
		}
		gen1, evals1 := shadowCounts(t, id)
		if gen1 != 1 || evals1 != 1 {
			t.Fatalf("initial gen=%d evals=%d, want 1/1", gen1, evals1)
		}

		// Hold the real writer lock (SQLite BEGIN IMMEDIATE / Postgres advisory
		// writer lock). Steady-state reads inside the 60s lease must succeed
		// without waiting on or taking the writer lock, and without bumping
		// generation, evaluations, CommitCount or wake signals.
		blocker, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker.Rollback()

		commits, wake := s.CommitCount(), s.Changed()
		withinLease := now.Add(20 * time.Second)

		readCtx, cancel := context.WithTimeout(ctx, readOnlyBudget)
		gotView, gotDiag, err := s.ObserveTaskGates(readCtx, id, withinLease)
		cancel()
		must(t, err)
		if !reflect.DeepEqual(gotView, initialView) || !reflect.DeepEqual(gotDiag, initialDiag) {
			t.Fatalf("steady-state read diverged from cached observation:\ngot view=%+v\nwant view=%+v", gotView, initialView)
		}

		// Scoped (no history) prediction read must also bypass the writer lock.
		predCtx, cancelPred := context.WithTimeout(ctx, readOnlyBudget)
		predView, err := s.ObserveTaskGatePredictions(predCtx, id, withinLease)
		cancelPred()
		must(t, err)
		if !reflect.DeepEqual(predView, initialView) {
			t.Fatalf("steady-state prediction read diverged: %+v", predView)
		}

		// Concurrent steady-state readers must all complete without blocking on
		// the held writer lock.
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		restarted := model.New(db)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, stop := context.WithTimeout(ctx, readOnlyBudget)
				defer stop()
				v, d, e := restarted.ObserveTaskGates(c, id, withinLease)
				if e == nil && (v.Results[0].EvaluationID != initialView.Results[0].EvaluationID || d.Evaluations != evals1) {
					e = fmt.Errorf("concurrent steady-state read re-evaluated: evaluation %s (want %s), evaluations %d (want %d)", v.Results[0].EvaluationID, initialView.Results[0].EvaluationID, d.Evaluations, evals1)
				}
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			must(t, e)
		}

		// An expired lease must NOT return the stale cached view; it must fall
		// through to the writer path and block on the held writer lock.
		expCtx, cancelExp := context.WithTimeout(ctx, 30*time.Millisecond)
		_, _, expErr := s.ObserveTaskGates(expCtx, id, now.Add(2*time.Minute))
		cancelExp()
		if expErr == nil {
			t.Fatal("expired lease read bypassed the writer path while writer lock was held")
		}

		must(t, blocker.Rollback())

		gen2, evals2 := shadowCounts(t, id)
		if gen2 != gen1 || evals2 != evals1 {
			t.Fatalf("steady-state reads bumped gen=%d/%d evals=%d/%d", gen2, gen1, evals2, evals1)
		}
		if s.CommitCount() != commits {
			t.Fatal("steady-state reads bumped Store.CommitCount")
		}
		select {
		case <-wake:
			t.Fatal("steady-state reads woke store listeners")
		default:
		}

		// Now mutate the source fence (a verified close creates a receipt) while
		// still inside the 60s lease. While the writer lock is held, the read
		// must detect the changed fence and fall through to the writer path
		// rather than returning the stale cached view.
		closedAt := now.Add(25 * time.Second)
		must(t, s.ObserveClose(ctx, id, closedAt, human, "verified receipt", func(o *model.Observation) { o.ClosedAt = &closedAt }))

		blocker2, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker2.Rollback()

		changedCtx, cancelChanged := context.WithTimeout(ctx, 30*time.Millisecond)
		_, _, changedErr := s.ObserveTaskGates(changedCtx, id, closedAt)
		cancelChanged()
		if changedErr == nil {
			t.Fatal("changed source fence returned stale cached view while writer lock was held")
		}
		must(t, blocker2.Rollback())

		// With the writer lock released, the changed fence re-evaluates and
		// bumps generation/evaluations.
		updatedView, updatedDiag, err := s.ObserveTaskGates(ctx, id, closedAt)
		must(t, err)
		if updatedView.SourceRevision == initialView.SourceRevision {
			t.Fatal("changed fence kept old source revision")
		}
		if updatedView.Results[1].Result.Status != "met" {
			t.Fatalf("receipt status after verified close = %s, want met", updatedView.Results[1].Result.Status)
		}
		gen3, evals3 := shadowCounts(t, id)
		if gen3 != gen1+1 || evals3 != evals1+1 || updatedDiag.Evaluations != evals3 {
			t.Fatalf("re-evaluation gen=%d evals=%d diag=%d, want %d", gen3, evals3, updatedDiag.Evaluations, gen1+1)
		}

		// Subsequent read inside the new lease is again read-only.
		blocker3, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker3.Rollback()
		afterCtx, cancelAfter := context.WithTimeout(ctx, readOnlyBudget)
		replayed, _, err := s.ObserveTaskGates(afterCtx, id, closedAt.Add(10*time.Second))
		cancelAfter()
		must(t, err)
		if !reflect.DeepEqual(replayed, updatedView) {
			t.Fatalf("post-update steady-state read diverged: %+v", replayed)
		}
		must(t, blocker3.Rollback())

		// After the lease expires with the writer lock free, re-evaluation
		// refreshes ComparedAt and bumps generation/evaluations.
		refreshedAt := closedAt.Add(2 * time.Minute)
		refreshedView, refreshedDiag, err := s.ObserveTaskGates(ctx, id, refreshedAt)
		must(t, err)
		if !refreshedView.ComparedAt.After(updatedView.ComparedAt) {
			t.Fatalf("expired lease did not refresh ComparedAt: %v <= %v", refreshedView.ComparedAt, updatedView.ComparedAt)
		}
		gen4, evals4 := shadowCounts(t, id)
		if gen4 != gen3+1 || evals4 != evals3+1 || refreshedDiag.Evaluations != evals4 {
			t.Fatalf("lease refresh gen=%d evals=%d diag=%d, want %d", gen4, evals4, refreshedDiag.Evaluations, gen3+1)
		}
	})

	t.Run("error-backoff-readonly-until-retry", func(t *testing.T) {
		id := "readonly-backoff"
		seedReport(t, id)
		now := base.Add(10 * time.Minute)
		failed := gates.UnavailableObservation(id, now)
		must(t, s.RecordGateObservationFailure(ctx, id, now, &failed))
		if len(failed.Results) == 0 || failed.Results[0].RetryAt == nil {
			t.Fatalf("recorded failure missing retry deadline: %+v", failed)
		}
		retryAt := *failed.Results[0].RetryAt
		genBefore, evalsBefore := shadowCounts(t, id)

		blocker, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker.Rollback()

		// Inside retry backoff: served read-only without taking the writer lock.
		inBackoffCtx, cancelIn := context.WithTimeout(ctx, readOnlyBudget)
		replayed, diag, err := s.ObserveTaskGates(inBackoffCtx, id, now.Add(100*time.Millisecond))
		cancelIn()
		must(t, err)
		if !replayed.Dirty || diag.Evaluations != evalsBefore {
			t.Fatalf("backoff replay unexpected: dirty=%v evals=%d", replayed.Dirty, diag.Evaluations)
		}

		// Once RetryAt arrives: must fall through to the writer path.
		dueCtx, cancelDue := context.WithTimeout(ctx, 30*time.Millisecond)
		_, _, dueErr := s.ObserveTaskGates(dueCtx, id, retryAt)
		cancelDue()
		if dueErr == nil {
			t.Fatal("expired error backoff bypassed the writer path while writer lock was held")
		}
		must(t, blocker.Rollback())

		genMid, evalsMid := shadowCounts(t, id)
		if genMid != genBefore || evalsMid != evalsBefore {
			t.Fatalf("backoff replay bumped gen=%d/%d evals=%d/%d", genMid, genBefore, evalsMid, evalsBefore)
		}

		recovered, _, err := s.ObserveTaskGates(ctx, id, retryAt)
		must(t, err)
		if recovered.Dirty || recovered.Results[0].Result.Status != "met" {
			t.Fatalf("retry did not recover: %+v", recovered)
		}
	})

	t.Run("health-steady-state-and-transient-readonly", func(t *testing.T) {
		id := "readonly-health"
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := model.Task{
			ID:        id,
			Intent:    model.IntentImplement,
			Title:     "Keep base green",
			Target:    &repo,
			Base:      "main",
			Filing:    model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: human},
			Binding:   model.BindingDirective,
			CreatedAt: &base,
		}
		must(t, s.PutTask(ctx, task))
		now := base.Add(20 * time.Minute)
		must(t, s.SetTaskGoal(ctx, id, model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: "main"}}, "alice", now))

		fullAccess := model.GateHealthAccess{
			AuthorizationRevision: 1,
			Repository:            func(string) bool { return true },
			PublicationRepository: func(string) bool { return true },
			PipelineRepository:    func(string) bool { return true },
			DeploymentWide:        true,
		}
		deniedAccess := model.GateHealthAccess{
			ScopeLimited:          true,
			AuthorizationRevision: 2,
			Repository:            func(string) bool { return false },
			PublicationRepository: func(string) bool { return false },
			PipelineRepository:    func(string) bool { return false },
		}

		fullCtx := model.WithGateHealthAccess(ctx, fullAccess)
		initial, _, err := s.ObserveTaskGates(fullCtx, id, now)
		must(t, err)
		gen1, evals1 := shadowCounts(t, id)

		blocker, err := db.BeginTx(ctx, nil)
		must(t, err)
		defer blocker.Rollback()

		// Full-access steady-state health read resolves read-only.
		readCtx, cancel := context.WithTimeout(fullCtx, readOnlyBudget)
		got, _, err := s.ObserveTaskGates(readCtx, id, now.Add(15*time.Second))
		cancel()
		must(t, err)
		if !reflect.DeepEqual(got, initial) {
			t.Fatalf("health steady-state read diverged: %+v", got)
		}

		// Access-limited transient health read also resolves read-only.
		deniedCtx, cancelDenied := context.WithTimeout(model.WithGateHealthAccess(ctx, deniedAccess), readOnlyBudget)
		transient, diag, err := s.ObserveTaskGates(deniedCtx, id, now.Add(15*time.Second))
		cancelDenied()
		must(t, err)
		if transient == nil || diag != nil {
			t.Fatalf("transient health read unexpected: view=%v diag=%v", transient != nil, diag != nil)
		}
		must(t, blocker.Rollback())

		gen2, evals2 := shadowCounts(t, id)
		if gen2 != gen1 || evals2 != evals1 {
			t.Fatalf("health readonly reads bumped gen=%d/%d evals=%d/%d", gen2, gen1, evals2, evals1)
		}
	})
}
