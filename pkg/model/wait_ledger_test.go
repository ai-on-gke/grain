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

// The wait ledger a stopping daemon leaves for a run is read back per
// source, replaced whole by a later write (a run adopted and detached
// again), and gone once the run finishes.
func TestRunWaitLedgerIsReplacedAndGoesWithTheRun(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: "t", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "t-1", TaskID: "t", Attempt: 1, StartedAt: at}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	if err := store.SetRunWaitLedger(ctx, "t-1", map[string]time.Duration{
		"": 5 * time.Minute, "cli": 20 * time.Minute, "unused": 0,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.RunWaitLedger(ctx, "t-1")
	if err != nil || len(got) != 2 || got[""] != 5*time.Minute || got["cli"] != 20*time.Minute {
		t.Fatalf("ledger = %v (%v), want mcp 5m and cli 20m, and no zero row", got, err)
	}

	if err := store.SetRunWaitLedger(ctx, "t-1", map[string]time.Duration{
		"": 6 * time.Minute, "before-restart": 21 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.RunWaitLedger(ctx, "t-1")
	if err != nil || len(got) != 2 || got[""] != 6*time.Minute || got["before-restart"] != 21*time.Minute {
		t.Fatalf("ledger = %v (%v), want the second write in place of the first", got, err)
	}

	if err := store.FinishRun(ctx, "t-1", at.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := store.RunWaitLedger(ctx, "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("ledger after finish = %v (%v), want none", got, err)
	}
}
