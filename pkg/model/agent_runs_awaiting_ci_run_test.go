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

package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// AgentRunsAwaitingCIRun is the one query ci.SharedGate's Stuck hook
// needs and cannot do without: which live agent runs -- the PoolAgent
// holders a gate's ledger charges under their own run id (LiveRuns'
// r.ID) -- belong to a task whose own CIStatus still names the given CI
// run as unanswered.
func TestAgentRunsAwaitingCIRun(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	mk := func(id string) {
		t.Helper()
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: human},
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
	}
	startRun := func(id string) {
		t.Helper()
		r := model.Run{ID: id + "-run", TaskID: id, Sandbox: "s", Attempt: 1, StartedAt: at}
		if err := store.StartRun(ctx, r, model.Limits{}); err != nil {
			t.Fatalf("starting %s's run: %v", id, err)
		}
	}
	observeCI := func(id string, status *model.CIStatus) {
		t.Helper()
		if err := store.ObserveField(ctx, id, at, func(o *model.Observation) { o.CI = status }); err != nil {
			t.Fatalf("observing %s's CI: %v", id, err)
		}
	}

	// A live run whose task is still waiting on run-1: the one row this
	// should answer with.
	mk("waiting")
	startRun("waiting")
	observeCI("waiting", &model.CIStatus{RunID: "run-1"})

	// A live run whose task's own run-1 has already concluded -- it must
	// not still read as waiting on it, or a run this answers for would
	// never drain once its verdict actually arrives.
	mk("finished")
	startRun("finished")
	done := at
	observeCI("finished", &model.CIStatus{RunID: "run-1", FinishedAt: &done, Conclusion: "success"})

	// A live run waiting on some other CI run entirely.
	mk("otherrun")
	startRun("otherrun")
	observeCI("otherrun", &model.CIStatus{RunID: "run-2"})

	// A live run whose task has no CI status at all.
	mk("norun")
	startRun("norun")

	got, err := store.AgentRunsAwaitingCIRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("AgentRunsAwaitingCIRun: %v", err)
	}
	if len(got) != 1 || got[0] != "waiting-run" {
		t.Fatalf("want only the waiting task's own run, got %v", got)
	}
}
