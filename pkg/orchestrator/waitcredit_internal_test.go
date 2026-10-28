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
)

// The clock keeps what context.WithTimeoutCause gave RunDispatch before
// it: a run that outlives its budget is cancelled, and the cause names
// the wall clock rather than looking like a failure of the run's own.
func TestRunClockCancelsAtItsBudget(t *testing.T) {
	t.Parallel()
	ctx, clock := newRunClock(context.Background(), 20*time.Millisecond, RunWaitCreditCap)
	defer clock.stop()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the run was never cancelled")
	}
	if !errors.Is(context.Cause(ctx), errRunTimedOut) {
		t.Errorf("cause = %v, want errRunTimedOut", context.Cause(ctx))
	}
}

// And what it adds: a run that spends its budget blocked on CI is not
// cancelled at the end of it, because that time was credited back while
// it was being spent.
func TestRunClockCreditedBlockedTimeDefersTheCancellation(t *testing.T) {
	t.Parallel()
	ctx, clock := newRunClock(context.Background(), 30*time.Millisecond, RunWaitCreditCap)
	defer clock.stop()

	credit := clock.credit(3 * time.Second)
	if !credit.Live {
		t.Fatal("credit reported the run as not live")
	}
	if credit.Credited != 3*time.Second {
		t.Errorf("credited = %s, want the whole 3s blocked", credit.Credited)
	}
	select {
	case <-ctx.Done():
		t.Fatalf("the run was cancelled at its uncredited deadline: %v", context.Cause(ctx))
	case <-time.After(200 * time.Millisecond):
	}
}

// The cap is the ceiling on a run's whole life, and nothing a run reports
// can raise it: 45 minutes of credit is 45 minutes however many hours it
// claims to have spent waiting.
func TestRunClockHoldsTheCap(t *testing.T) {
	t.Parallel()
	_, clock := newRunClock(context.Background(), time.Hour, 5*time.Minute)
	defer clock.stop()

	credit := clock.credit(2 * time.Hour)
	if credit.Credited != 5*time.Minute {
		t.Errorf("credited = %s, want the 5m cap", credit.Credited)
	}
	if want := clock.base.Add(5 * time.Minute); !credit.Deadline.Equal(want) {
		t.Errorf("deadline = %s, want %s", credit.Deadline, want)
	}
	if blocked, credited := clock.ledger(); blocked != 2*time.Hour || credited != 5*time.Minute {
		t.Errorf("ledger = (%s, %s), want the blocked time recorded in full beside the capped credit",
			blocked, credited)
	}
}

// Reports are cumulative and applying them is idempotent, which is what
// makes a caller free to repeat itself after a dropped request and unable
// to inflate its credit by doing so.
func TestRunClockKeepsTheLargestTotalRatherThanASum(t *testing.T) {
	t.Parallel()
	_, clock := newRunClock(context.Background(), time.Hour, RunWaitCreditCap)
	defer clock.stop()

	for _, blocked := range []time.Duration{time.Minute, time.Minute, 2 * time.Minute, time.Minute} {
		clock.credit(blocked)
	}
	if blocked, credited := clock.ledger(); blocked != 2*time.Minute || credited != 2*time.Minute {
		t.Errorf("ledger = (%s, %s), want 2m0s of blocked time credited once", blocked, credited)
	}
}

// A run that is already over cannot be credited: its deadline has nothing
// left to move, and the caller is told so rather than being given a
// number that means nothing (RunWaitCredit.Live).
func TestRunClockReportsAStoppedRunAsNotLive(t *testing.T) {
	t.Parallel()
	_, clock := newRunClock(context.Background(), time.Hour, RunWaitCreditCap)
	clock.stop()

	if credit := clock.credit(time.Minute); credit.Live {
		t.Error("a stopped clock reported its run as still live")
	}
}

// The registry is what a report from the sandbox actually lands on, and
// its answer for a task with no live run is "nobody is listening" rather
// than an error -- a report that crossed with the run finishing has done
// nothing wrong.
func TestRunWaitCreditsAnswersForLiveRunsOnly(t *testing.T) {
	t.Parallel()
	credits := NewRunWaitCredits()
	_, clock := newRunClock(context.Background(), time.Hour, RunWaitCreditCap)
	defer clock.stop()
	unregister := credits.register("t1", clock)

	credit, err := credits.Credit(context.Background(), "t1", 90*time.Second)
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if !credit.Live || credit.Credited != 90*time.Second {
		t.Errorf("credit = %+v, want 1m30s granted to a live run", credit)
	}
	if other, err := credits.Credit(context.Background(), "t2", time.Minute); err != nil || other.Live {
		t.Errorf("Credit for an unknown task = %+v, %v; want a dead answer and no error", other, err)
	}

	unregister()
	if after, err := credits.Credit(context.Background(), "t1", 2*time.Minute); err != nil || after.Live {
		t.Errorf("Credit after the run ended = %+v, %v; want a dead answer and no error", after, err)
	}
}

// A deployment that wires no registry grants no credit and errors at
// nobody -- every run bounded by MaxRunRuntime exactly as it was before
// any of this existed (Config.RunWaitCredits).
func TestNilRunWaitCreditsGrantsNothing(t *testing.T) {
	t.Parallel()
	var credits *RunWaitCredits
	credits.register("t1", nil)()
	credit, err := credits.Credit(context.Background(), "t1", time.Minute)
	if err != nil || credit.Live || credit.Credited != 0 {
		t.Errorf("Credit = %+v, %v; want nothing granted and no error", credit, err)
	}
}

// A capability bounded by capabilityDeadline must outlast every run its
// clock can let live: setup, the whole budget, and every minute of wait
// credit -- and be nowhere near the flat day a model proxy token used to
// be good for regardless (bwsalmon/grain#2418).
func TestCapabilityDeadlineCoversTheLongestARunCanLive(t *testing.T) {
	at := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	budget := 2 * time.Hour
	got := capabilityDeadline(at, budget)
	if longest := at.Add(budget + RunWaitCreditCap); got.Before(longest) {
		t.Fatalf("capabilityDeadline = %v, before %v -- a run with all its credit would lose its capabilities", got, longest)
	}
	if got.After(at.Add(6 * time.Hour)) {
		t.Fatalf("capabilityDeadline = %v, more than 6h after a 2h run was dispatched at %v", got, at)
	}
}

// Blocked time reported from the sandbox CLI adds to what the run's own
// mcpserver reports, rather than the larger of the two winning: each
// source is cumulative on its own, repeating a report buys nothing, and
// the sum is still held to the cap.
func TestRunWaitCreditsSumsSources(t *testing.T) {
	t.Parallel()
	credits := NewRunWaitCredits()
	_, clock := newRunClock(context.Background(), time.Hour, 10*time.Minute)
	defer clock.stop()
	defer credits.register("7", clock)()
	ctx := context.Background()

	if got, _ := credits.Credit(ctx, "7", 3*time.Minute); got.Credited != 3*time.Minute {
		t.Fatalf("mcp credit = %s", got.Credited)
	}
	got, _ := credits.CreditSource(ctx, "7", WaitSourceCLI, 2*time.Minute)
	if got.Blocked != 5*time.Minute || got.Credited != 5*time.Minute {
		t.Errorf("after a CLI report: blocked %s credited %s, want 5m each", got.Blocked, got.Credited)
	}
	// The same cumulative report again, and a smaller one, change nothing.
	credits.CreditSource(ctx, "7", WaitSourceCLI, 2*time.Minute)
	got, _ = credits.CreditSource(ctx, "7", WaitSourceCLI, time.Minute)
	if got.Blocked != 5*time.Minute {
		t.Errorf("repeated reports moved blocked to %s", got.Blocked)
	}
	got, _ = credits.CreditSource(ctx, "7", WaitSourceCLI, time.Hour)
	if got.Credited != 10*time.Minute {
		t.Errorf("credited = %s, want the 10m cap", got.Credited)
	}
	if got, _ := credits.CreditSource(ctx, "8", WaitSourceCLI, time.Minute); got.Live {
		t.Errorf("a task with no live run was credited: %+v", got)
	}
}
