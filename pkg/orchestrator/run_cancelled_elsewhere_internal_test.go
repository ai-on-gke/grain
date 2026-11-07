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

// A Stop that lands before a run has registered its canceller finishes
// the run's row itself (ui.Client.cancelActiveRuns). The run reads that
// as the stop it is and ends with ErrTurnStopped -- the same cause the
// canceller would have delivered -- rather than going on to hold the
// task's worker slot, which left a person's Retry dispatching nothing
// (ui/e2e/stopped-turn-retry.spec.js). A live row, a row finished any
// other way, and a run id with no row are all left alone.
func TestCheckTaskClosedEndsARunCancelledElsewhere(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		finish  string // "" leaves the row live
		runID   string
		wantCut bool
	}{
		{name: "stopped before its canceller registered", finish: "cancelled", runID: "7-1", wantCut: true},
		{name: "still live", runID: "7-1"},
		{name: "finished some other way", finish: model.InterruptedOutcome, runID: "7-1"},
		{name: "no such run", finish: "cancelled", runID: "7-9"},
		{name: "no run id", finish: "cancelled", runID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openTestStore(t)
			if err := store.PutTask(ctx, model.Task{ID: "7", Title: "held run", CreatedAt: &start}); err != nil {
				t.Fatal(err)
			}
			if err := store.StartRun(ctx, model.Run{ID: "7-1", TaskID: "7", Attempt: 1, StartedAt: start}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			if tc.finish != "" {
				if err := store.FinishRun(ctx, "7-1", start.Add(time.Second), tc.finish, model.TurnStoppedDetail); err != nil {
					t.Fatal(err)
				}
			}
			runCtx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			got := checkTaskClosed(ctx, store, "7", tc.runID, cancel)
			if got != tc.wantCut {
				t.Fatalf("checkTaskClosed = %v, want %v", got, tc.wantCut)
			}
			cause := context.Cause(runCtx)
			if tc.wantCut && !errors.Is(cause, ErrTurnStopped) {
				t.Errorf("cause = %v, want ErrTurnStopped", cause)
			}
			if !tc.wantCut && cause != nil {
				t.Errorf("cause = %v, want the run left running", cause)
			}
		})
	}
}
