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

// A task with resume_on_deadline that runs out of wall clock is a
// deadline suspension, at most model.MaxDeadlineResumes times in a row;
// after that, and for a task without the flag, the deadline is the
// failure it always was.
func TestResumeOnDeadlineSuspendsABoundedNumberOfTimes(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := stdTask("large")
	tk.ResumeOnDeadline = true
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	timedOut := func(attempt int) runEnding {
		t.Helper()
		runID := tk.ID + "-" + string(rune('0'+attempt))
		at := standingNow.Add(time.Duration(attempt) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: tk.ID, Attempt: attempt, StartedAt: at},
			model.Limits{}); err != nil {
			t.Fatal(err)
		}
		cfg := Config{Now: func() time.Time { return at.Add(time.Minute) }}
		end := classifyRunEnd(ctx, store, cfg, tk, runID, nil, context.DeadlineExceeded, errRunTimedOut,
			0, PauseScope{}, at)
		if !errors.Is(end.runErr, errRunTimedOut) {
			t.Fatalf("attempt %d: runErr = %v, want the timeout", attempt, end.runErr)
		}
		obs, err := store.GetObservation(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordRunEnd(ctx, ctx, store, cfg, tk, runID, obs, nil, end, nil, false, "", ""); err != nil {
			t.Fatal(err)
		}
		return end
	}
	for attempt := 1; attempt <= model.MaxDeadlineResumes; attempt++ {
		if end := timedOut(attempt); end.suspend != model.SuspendDeadline || end.outcome != model.SuspendedOutcome {
			t.Fatalf("attempt %d ended (%q, %q), want a deadline suspension", attempt, end.outcome, end.suspend)
		}
	}
	if n := failureCount(t, ctx, store, tk.ID); n != 0 {
		t.Fatalf("after %d deadline suspensions the streak is %d, want 0", model.MaxDeadlineResumes, n)
	}
	if end := timedOut(model.MaxDeadlineResumes + 1); end.suspend != "" || end.outcome != "cancelled" {
		t.Fatalf("attempt past the limit ended (%q, %q), want a counted cancellation", end.outcome, end.suspend)
	}
	if n := failureCount(t, ctx, store, tk.ID); n != 1 {
		t.Fatalf("the deadline past the limit left the streak at %d, want 1", n)
	}

	plain := stdTask("plain")
	if err := store.PutTask(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if end := classifyRunEnd(ctx, store, Config{}, plain, "plain-1", nil, context.DeadlineExceeded,
		errRunTimedOut, 0, PauseScope{}, standingNow); end.suspend != "" {
		t.Fatalf("a task without resume_on_deadline was suspended (%q)", end.suspend)
	}
}
