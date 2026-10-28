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

// An adopted run's clock keeps the deadline the previous daemon set and
// credits the restart: a run whose budget ran out during the gap is still
// inside its credited deadline, the gap counts towards the cap like any
// other wait, and the run's own later reports are credited on top of it.
func TestAdoptedRunClockKeepsTheDeadlineAndCreditsTheGap(t *testing.T) {
	t.Parallel()
	base := time.Now().Add(-time.Minute) // the budget ran out in the gap
	ctx, clock := newAdoptedRunClock(context.Background(), base, time.Hour, 2*time.Minute)
	defer clock.stop()
	if !clock.base.Equal(base) {
		t.Fatalf("base = %v, want the carried deadline %v", clock.base, base)
	}
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("the gap was not credited: the run was cancelled (%v)", context.Cause(ctx))
	}
	got := clock.credit(30 * time.Second)
	if got.Blocked != 150*time.Second || got.Credited != 150*time.Second || !got.Deadline.Equal(base.Add(150*time.Second)) {
		t.Fatalf("credit = %+v, want the run's own 30s on top of the 2m gap", got)
	}
	if blocked, credited := clock.ledger(); blocked != 150*time.Second || credited != 150*time.Second {
		t.Fatalf("ledger = %v/%v", blocked, credited)
	}
}

// The credit is capped like any other, and a deadline already past with
// the credit included ends the run as timed out, not as anything else.
func TestAdoptedRunClockPastItsDeadlineTimesOut(t *testing.T) {
	t.Parallel()
	ctx, clock := newAdoptedRunClock(context.Background(), time.Now().Add(-time.Hour), time.Minute, 2*time.Hour)
	defer clock.stop()
	if _, credited := clock.ledger(); credited != time.Minute {
		t.Fatalf("credited %v, want the cap", credited)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a run past its deadline was not cancelled")
	}
	if !errors.Is(context.Cause(ctx), errRunTimedOut) {
		t.Fatalf("cause = %v, want errRunTimedOut", context.Cause(ctx))
	}
}

// A run this daemon started has no gap, and credits exactly what its
// tools report, as before.
func TestRunClockWithoutAGapIsUnchanged(t *testing.T) {
	t.Parallel()
	_, clock := newRunClock(context.Background(), time.Hour, time.Hour)
	defer clock.stop()
	if got := clock.credit(time.Minute); got.Blocked != time.Minute || got.Credited != time.Minute {
		t.Fatalf("credit = %+v", got)
	}
}

// An adopted run's deadline is carried from where its own clock started,
// its agent's first turn, so its setup is not charged a second time; a
// row the previous daemon never stamped falls back to when it started.
func TestAdoptedClockStartIsTheAgentsStart(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	agentStarted := started.Add(7 * time.Minute)
	if got := adoptedClockStart(model.Run{StartedAt: started, AgentStartedAt: &agentStarted}); !got.Equal(agentStarted) {
		t.Fatalf("clock start = %v, want the agent's start %v", got, agentStarted)
	}
	if got := adoptedClockStart(model.Run{StartedAt: started}); !got.Equal(started) {
		t.Fatalf("clock start = %v, want the row's start %v for an unstamped run", got, started)
	}
}

// The earlier attempts of an adopted run are what its live transcript
// holds before the attempt in flight, found by the head of that attempt's
// own stream as Attach returned it.
func TestEarlierAttemptsAreTheTranscriptBeforeTheAttemptInFlight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, live, spool string
		cursor            int64
		want              string
	}{
		{"read up to the cursor", "a1\na2\nb1\n", "b1\nb2\n", 3, "a1\na2\n"},
		{"read past the cursor", "a1\nb1\nb2\n", "b1\nb2\nb3\n", 3, "a1\n"},
		{"nothing of it read", "a1\na2\n", "b1\nb2\n", 0, "a1\na2\n"},
		{"read with no cursor persisted", "a1\nb1\n", "b1\nb2\n", 0, "a1\n"},
		{"no earlier attempt", "b1\n", "b1\nb2\n", 3, ""},
		{"a resumed attempt repeating a line", "x\nb1\nx\n", "x\ny\n", 2, "x\nb1\n"},
		{"a transcript that does not hold the head", "a1\n", "b1\nb2\n", 3, ""},
		{"no transcript", "", "b1\n", 0, ""},
		{"an attach that returned nothing", "a1\nb1\n", "", 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlierAttempts("r", []byte(tc.live), []byte(tc.spool), tc.cursor); got != tc.want {
				t.Fatalf("earlierAttempts = %q, want %q", got, tc.want)
			}
		})
	}
}
