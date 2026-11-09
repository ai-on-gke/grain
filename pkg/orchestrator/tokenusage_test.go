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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The usage cap only ever lowers the worker limit, and never below one.
func TestUsageCappedWorkersStaysWithinMaxWorkers(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		max, capped, want int
	}{{6, 3, 3}, {6, 60, 6}, {6, 0, 1}, {6, -4, 1}, {0, 3, 0}} {
		deps := Deps{MaxWorkers: c.max, UsageCap: func(context.Context, time.Time, int) int { return c.capped }}
		if got := usageCappedWorkers(context.Background(), deps, now); got != c.want {
			t.Errorf("max %d, cap %d: workers %d, want %d", c.max, c.capped, got, c.want)
		}
	}
	if got := usageCappedWorkers(context.Background(), Deps{MaxWorkers: 5}, now); got != 5 {
		t.Errorf("no cap: workers %d, want 5", got)
	}
}

// A pause opening is reported to SetOnOpen's hook once; extending it is
// not.
func TestPauseReportsOpeningOnce(t *testing.T) {
	p := &Pause{}
	opened := make(chan model.UsageLimitPause, 4)
	p.SetOnOpen(func(w model.UsageLimitPause, _ time.Time) { opened <- w })
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	scope := PauseScope{Connection: "conn", Model: "opus"}
	p.Begin(now, scope, &agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	select {
	case w := <-opened:
		if w.Connection != "conn" || w.Model != "opus" {
			t.Fatalf("opened = %+v", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no opening reported")
	}
	p.Begin(now.Add(time.Minute), scope, &agent.UsageLimitError{ResetAt: now.Add(2 * time.Hour)})
	select {
	case w := <-opened:
		t.Fatalf("extension reported as an opening: %+v", w)
	case <-time.After(100 * time.Millisecond):
	}
}
