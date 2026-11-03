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

package dispatch_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestATaskClosedAndReopenedUnderItsRunIsDispatchedAtOnce is the
// 2026-10-30 regression: five tasks closed and reopened to get fresh
// sandboxes sat out a retry backoff ("backing off after failed run")
// because the run each close cancelled was charged to the task as a
// failure. A turn a person stopped is the same kind of decision. Either
// way the next poll after the reopen must dispatch the task.
func TestATaskClosedAndReopenedUnderItsRunIsDispatchedAtOnce(t *testing.T) {
	for _, detail := range []string{model.TaskClosedDetail, model.TurnStoppedDetail} {
		t.Run(detail, func(t *testing.T) {
			store, ctx := open(t)
			putTasks(t, store, ctx, task("t0", true))

			d, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
			if err != nil || len(d) != 1 {
				t.Fatalf("dispatch: %v, %+v", err, d)
			}
			closed := now.Add(time.Minute)
			if detail == model.TaskClosedDetail {
				if err := store.ObserveField(ctx, "t0", closed, func(o *model.Observation) { o.ClosedAt = &closed }); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FinishRun(ctx, d[0].RunID, closed, "cancelled", detail); err != nil {
				t.Fatal(err)
			}
			if detail == model.TaskClosedDetail {
				if err := store.ObserveField(ctx, "t0", closed, func(o *model.Observation) { o.ClosedAt = nil }); err != nil {
					t.Fatal(err)
				}
			}

			streak, err := store.FailureStreak(ctx, "t0")
			if err != nil {
				t.Fatal(err)
			}
			if streak == nil || streak.Count != 0 || streak.BackoffCount != 0 {
				t.Fatalf("FailureStreak = %+v, want Count and BackoffCount 0", streak)
			}
			again, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, closed.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if len(again) != 1 || again[0].TaskID != "t0" {
				t.Fatalf("dispatch a second after the reopen = %+v, want t0 at once", again)
			}
		})
	}
}

// A real failure before the close still counts, and still spaces the
// next attempt: the close neither adds a strike nor clears one.
func TestAFailureBeforeACloseStillBacksOff(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	d, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(d) != 1 {
		t.Fatalf("dispatch: %v, %+v", err, d)
	}
	if err := store.FinishRun(ctx, d[0].RunID, now.Add(time.Minute), "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	clock := now.Add(time.Hour)
	d, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock)
	if err != nil || len(d) != 1 {
		t.Fatalf("dispatch after the backoff: %v, %+v", err, d)
	}
	if err := store.FinishRun(ctx, d[0].RunID, clock.Add(time.Minute), "cancelled", model.TaskClosedDetail); err != nil {
		t.Fatal(err)
	}

	streak, err := store.FailureStreak(ctx, "t0")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 1 || streak.BackoffCount != 1 {
		t.Fatalf("FailureStreak = %+v, want Count and BackoffCount 1: the failure before the close", streak)
	}
	if streak.LastFailureDetail != "boom" {
		t.Fatalf("LastFailureDetail = %q, want the failure's own %q", streak.LastFailureDetail, "boom")
	}
	tooSoon, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, clock.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(tooSoon) != 0 {
		t.Fatalf("dispatch = %+v, want nothing yet: the earlier failure still backs off", tooSoon)
	}
}
