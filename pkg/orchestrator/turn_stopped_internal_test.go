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

package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A Stop that lands while a run is still preparing its sandbox finds no
// canceller registered and finishes the run's row instead. The run reads
// that row once it registers and cancels itself with ErrTurnStopped,
// rather than starting an agent turn that holds the task's dispatch slot
// against the Retry or reply that should resume it (stopped-turn-retry
// e2e). A row still open, or one finished some other way, is left alone.
func TestCheckTurnStoppedCancelsARunStoppedBeforeItRegistered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{ID: "7", Title: "t", Body: "b", ApprovedAt: &now, CreatedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "7-1", TaskID: "7", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	check := func() (bool, error) {
		runCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		got := checkTurnStopped(ctx, store, "7", "7-1", cancel)
		return got, context.Cause(runCtx)
	}

	if got, cause := check(); got || cause != nil {
		t.Fatalf("open row: checkTurnStopped = %v, cause %v; want false, nil", got, cause)
	}

	if err := store.FinishRun(ctx, "7-1", now.Add(time.Second), "cancelled", model.TurnStoppedDetail); err != nil {
		t.Fatal(err)
	}
	if got, cause := check(); !got || !errors.Is(cause, ErrTurnStopped) {
		t.Fatalf("stopped row: checkTurnStopped = %v, cause %v; want true, ErrTurnStopped", got, cause)
	}

	if err := store.StartRun(ctx, model.Run{ID: "7-2", TaskID: "7", Attempt: 2, StartedAt: now.Add(2 * time.Second)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "7-2", now.Add(3*time.Second), "failed", "agent exited 1"); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if checkTurnStopped(ctx, store, "7", "7-2", cancel) || context.Cause(runCtx) != nil {
		t.Fatalf("failed row: checkTurnStopped cancelled a run nobody stopped (cause %v)", context.Cause(runCtx))
	}
}
