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

package model

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// cancelsAfterFirstCheck is a context that is live the first time anybody
// asks and cancelled from then on: a caller giving up in the moment
// between flushCIStepOutputBatch's own check and its statements.
type cancelsAfterFirstCheck struct {
	context.Context
	asked atomic.Bool
	done  chan struct{}
}

func (c *cancelsAfterFirstCheck) Err() error {
	if c.asked.CompareAndSwap(false, true) {
		return nil
	}
	return context.Canceled
}

func (c *cancelsAfterFirstCheck) Done() <-chan struct{} {
	if !c.asked.Load() {
		return nil
	}
	return c.done
}

// One caller cancelling mid-batch must not roll back the other callers'
// writes that share its transaction.
func TestStepOutputBatchSurvivesOneCallerCancelling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run := CIRun{
		ID: "run1", Key: "key", Repo: "owner/repo", Ref: "main", Commit: "abc", Event: "push",
		StartedAt: started,
		Jobs: []CIJob{{
			Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: "unit", StartedAt: started,
			Steps: []CIStep{{Name: "a", StartedAt: started}, {Name: "b", StartedAt: started}},
		}},
	}
	if err := store.PutCIRun(ctx, run); err != nil {
		t.Fatalf("PutCIRun: %v", err)
	}

	closed := make(chan struct{})
	close(closed)
	quitter := &stepOutputWriteReq{
		ctx: &cancelsAfterFirstCheck{Context: ctx, done: closed}, runID: "run1", stepIndex: 0,
		output: "gave up\n", done: make(chan struct{}),
	}
	stayer := &stepOutputWriteReq{
		ctx: ctx, runID: "run1", stepIndex: 1, output: "still here\n", done: make(chan struct{}),
	}
	store.flushCIStepOutputBatch([]*stepOutputWriteReq{quitter, stayer})

	if stayer.err != nil {
		t.Fatalf("a write sharing the batch failed with another caller's cancellation: %v", stayer.err)
	}
	got, err := store.GetCIRun(ctx, "run1")
	if err != nil || got == nil {
		t.Fatalf("GetCIRun = %v, %v", got, err)
	}
	if out := got.Jobs[0].Steps[1].Output; out != "still here\n" {
		t.Errorf("step b output = %q, want it written", out)
	}
}
