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

// TestCheckRunStoppedReachesAStopBeforeTheCancellerExisted pins the window
// between StartRun writing a run's row and RunDispatch registering its
// canceller: a Stop there finds no canceller and finishes the stored row
// (ui.Client.cancelActiveRuns), and the run, once it registers, has to
// stop itself rather than go on holding the task. Without it the
// stopped-turn-retry e2e's Retry found no slot to dispatch into.
func TestCheckRunStoppedReachesAStopBeforeTheCancellerExisted(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{ID: "7", Title: "held", CreatedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "7-1", TaskID: "7", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	live, cancelLive := context.WithCancelCause(context.Background())
	defer cancelLive(nil)
	if checkRunStopped(ctx, store, "7-1", cancelLive) || live.Err() != nil {
		t.Fatal("a run whose row is still open was cancelled")
	}
	if checkRunStopped(ctx, store, "no-such-run", cancelLive) || live.Err() != nil {
		t.Fatal("a run with no row was cancelled")
	}

	if err := store.FinishRun(ctx, "7-1", now.Add(time.Second), "cancelled", model.TurnStoppedDetail); err != nil {
		t.Fatal(err)
	}
	stopped, cancelStopped := context.WithCancelCause(context.Background())
	defer cancelStopped(nil)
	if !checkRunStopped(ctx, store, "7-1", cancelStopped) {
		t.Fatal("a run whose row a Stop finished was not cancelled")
	}
	if cause := context.Cause(stopped); !errors.Is(cause, ErrTurnStopped) {
		t.Fatalf("cause = %v, want ErrTurnStopped", cause)
	}
}
