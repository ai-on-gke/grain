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
	ctx, clock := newAdoptedRunClock(context.Background(), base, time.Hour, 2*time.Minute, nil)
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
	ctx, clock := newAdoptedRunClock(context.Background(), time.Now().Add(-time.Hour), time.Minute, 2*time.Hour, nil)
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

// The wait time a run was credited before the restart is carried into
// the adopted clock, and applied before the deadline is checked: a run
// that spent most of its budget in grain ci wait is not killed as timed
// out the moment it is adopted.
func TestAdoptedRunClockCarriesThePreviousDaemonsLedger(t *testing.T) {
	t.Parallel()
	// The budget ran out 20m ago, but 25m of grain ci wait and 5m of
	// wait_for_checks had been credited before the restart.
	base := time.Now().Add(-20 * time.Minute)
	ledger := map[string]time.Duration{WaitSourceMCP: 5 * time.Minute, WaitSourceCLI: 25 * time.Minute}
	ctx, clock := newAdoptedRunClock(context.Background(), base, time.Hour, 2*time.Minute, ledger)
	defer clock.stop()
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("the carried ledger was not credited before the deadline check: cancelled (%v)", context.Cause(ctx))
	}
	if blocked, credited := clock.ledger(); blocked != 32*time.Minute || credited != 32*time.Minute {
		t.Fatalf("ledger = %v/%v, want 2m gap + 5m mcp + 25m cli", blocked, credited)
	}
	if !clock.deadline().Equal(base.Add(32 * time.Minute)) {
		t.Fatalf("deadline = %v, want base + 32m", clock.deadline())
	}

	// The mcpserver survived with the run, so its next report is still
	// its whole-run total: it supersedes the carried 5m, not adds to it.
	if got := clock.credit(6 * time.Minute); got.Blocked != 33*time.Minute {
		t.Fatalf("after mcp's cumulative 6m, blocked = %v, want 33m (not 39m)", got.Blocked)
	}
	// A report smaller than the carried total takes nothing away.
	if got := clock.credit(time.Minute); got.Blocked != 33*time.Minute {
		t.Fatalf("after a stale mcp report, blocked = %v, want 33m", got.Blocked)
	}
	// The new daemon's CLI ledger starts from zero, so its totals add to
	// what was carried rather than maxing against it.
	if got := clock.creditFrom(WaitSourceCLI, 3*time.Minute); got.Blocked != 36*time.Minute {
		t.Fatalf("after the new proxy's 3m, blocked = %v, want 36m", got.Blocked)
	}
}

// What a clock leaves for the next daemon, adopted again from that, loses
// nothing and counts nothing twice -- across a second restart too.
func TestAdoptedRunClockLedgerSurvivesTwoRestarts(t *testing.T) {
	t.Parallel()
	_, first := newRunClock(context.Background(), time.Hour, time.Hour)
	first.credit(5 * time.Minute)
	first.creditFrom(WaitSourceCLI, 10*time.Minute)
	first.stop()
	left := first.sources()
	if len(left) != 2 || left[WaitSourceMCP] != 5*time.Minute || left[WaitSourceCLI] != 10*time.Minute {
		t.Fatalf("first daemon left %v", left)
	}

	_, second := newAdoptedRunClock(context.Background(), time.Now().Add(time.Hour), time.Hour, time.Minute, left)
	second.credit(7 * time.Minute)                  // mcp carries on: +2m
	second.creditFrom(WaitSourceCLI, 4*time.Minute) // a fresh proxy: +4m
	second.stop()
	if blocked, _ := second.ledger(); blocked != 22*time.Minute {
		t.Fatalf("second daemon's blocked = %v, want 1m gap + 7m mcp + 10m + 4m cli", blocked)
	}
	left = second.sources()

	_, third := newAdoptedRunClock(context.Background(), time.Now().Add(time.Hour), time.Hour, 30*time.Second, left)
	defer third.stop()
	if blocked, _ := third.ledger(); blocked != 22*time.Minute+30*time.Second {
		t.Fatalf("third daemon's blocked = %v, want the second's 22m plus its own 30s gap", blocked)
	}
	if got := third.credit(7 * time.Minute); got.Blocked != 22*time.Minute+30*time.Second {
		t.Fatalf("mcp repeating its 7m total moved blocked to %v", got.Blocked)
	}
}

// A ledger that cannot be read, or was never left, falls back to the gap
// alone, which is what newAdoptedRunClock credited before it existed.
func TestAdoptedWaitLedgerRoundTripsThroughTheStore(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	if got := adoptedWaitLedger(ctx, store, "nobody"); len(got) != 0 {
		t.Fatalf("a run with no ledger read back %v", got)
	}
	if got := adoptedWaitLedger(ctx, nil, "nobody"); got != nil {
		t.Fatalf("a nil store read back %v", got)
	}
	_, clock := newAdoptedRunClock(context.Background(), time.Now().Add(time.Hour), time.Hour, time.Minute,
		map[string]time.Duration{WaitSourceMCP: 2 * time.Minute})
	clock.creditFrom(WaitSourceCLI, 3*time.Minute)
	clock.stop()
	leaveWaitLedger(ctx, store, "r-1", clock)
	got := adoptedWaitLedger(ctx, store, "r-1")
	want := map[string]time.Duration{WaitSourceMCP: 2 * time.Minute, WaitSourceCLI: 3 * time.Minute, WaitSourceRestartGap: time.Minute}
	if len(got) != len(want) {
		t.Fatalf("read back %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("read back %v, want %v", got, want)
		}
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
