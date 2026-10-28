package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// fakeGranter is a WaitCreditReporter that grants what the rule says --
// every reported minute, up to its cap -- and records what it was told,
// which is what pins that credit is *measured* rather than asked for.
type fakeGranter struct {
	cap      time.Duration
	reports  []time.Duration
	dead     bool
	err      error
	granted  time.Duration
	onCredit func()
}

func (g *fakeGranter) CreditWait(_ context.Context, blocked time.Duration) (WaitCredit, error) {
	g.reports = append(g.reports, blocked)
	if g.onCredit != nil {
		g.onCredit()
	}
	if g.err != nil {
		return WaitCredit{}, g.err
	}
	if g.dead {
		return WaitCredit{Live: false}, nil
	}
	g.granted = min(blocked, g.cap)
	return WaitCredit{Credited: g.granted, Live: true}, nil
}

// runWithCreditLeft is runWithLeft's sibling: a ctx for a run the given
// distance from its announced deadline, on a server that can also earn
// credit back for blocking on CI. The clock it reads is returned so a
// test can move it the way a real wait does.
func runWithCreditLeft(left, cap time.Duration, granter WaitCreditReporter) (context.Context, *time.Time) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	clock := &now
	registry := NewRegistry()
	registry.AnnounceDeadline(now.Add(left), func() time.Time { return *clock })
	registry.AnnounceWaitCredit(cap, granter)
	return registry.withRunDeadline(context.Background()), clock
}

// The case the whole feature is for: a run with less nominal budget than
// the wait it wants can still run the whole wait, because the wait pays
// for itself as it blocks. Ten minutes left and a fifteen-minute wait
// used to be an eight-minute wait that saw nothing.
func TestWaitForChecksBudgetCountsTheCreditAWaitWillEarn(t *testing.T) {
	ctx, _ := runWithCreditLeft(10*time.Minute, 45*time.Minute, &fakeGranter{cap: 45 * time.Minute})

	budget := waitForChecksBudget(ctx, nil)
	if budget.timeout != DefaultWaitForChecksTimeout {
		t.Errorf("timeout = %s, want the full %s asked for", budget.timeout, DefaultWaitForChecksTimeout)
	}
	if budget.note != "" || budget.deadlineClamped {
		t.Errorf("wait was reported as clamped when it was not: %q", budget.note)
	}
}

// The credit is finite, and where it runs out the clamp comes back --
// with both halves of the arithmetic in the note, since a run told only
// what is left would read a wait longer than that as a mistake.
func TestWaitForChecksBudgetClampsOnceTheCreditRunsOut(t *testing.T) {
	ctx, _ := runWithCreditLeft(5*time.Minute, 4*time.Minute, &fakeGranter{cap: 4 * time.Minute})

	budget := waitForChecksBudget(ctx, nil)
	if want := 7 * time.Minute; budget.timeout != want {
		t.Errorf("timeout = %s, want %s (5m left plus 4m credit less %s slack)",
			budget.timeout, want, waitForChecksDeadlineSlack)
	}
	if !budget.deadlineClamped {
		t.Error("clamped wait not reported as bounded by the run's own clock")
	}
	for _, want := range []string{"5m0s this run has left", "4m0s of wait credit", "not the 15m0s asked for"} {
		if !strings.Contains(budget.note, want) {
			t.Errorf("note = %q, want it to mention %q", budget.note, want)
		}
	}
}

// A server nobody wired a granter into must behave exactly as it did
// before any of this existed: no credit, and the old clamp. "Nobody
// grants credit here" is not "credit is unlimited" any more than "nobody
// announced a deadline" is "no time left".
func TestWaitForChecksBudgetEarnsNoCreditWithoutAGranter(t *testing.T) {
	budget := waitForChecksBudget(runWithLeft(10*time.Minute), nil)
	if want := 8 * time.Minute; budget.timeout != want {
		t.Errorf("timeout = %s, want %s", budget.timeout, want)
	}
	if strings.Contains(budget.note, "credit") {
		t.Errorf("note offers credit this server cannot grant: %q", budget.note)
	}
}

// Credit accrues while the wait is still blocking, not on its return:
// the deadline the *next* decision is measured against has already moved
// by the time this call ends. Reported cumulatively, so a lost report
// costs nothing but its lateness.
func TestCreditWaitReportsCumulativeBlockedTimeAndMovesTheDeadline(t *testing.T) {
	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, clock := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)
	at := deadline.at()

	start := *clock
	for _, blocked := range []time.Duration{time.Minute, 4 * time.Minute} {
		*clock = start.Add(blocked)
		if left := deadline.creditWait(ctx, blocked); left <= 0 {
			t.Fatalf("remaining = %s after %s blocked, want the run still live", left, blocked)
		}
	}
	if want := []time.Duration{time.Minute, 4 * time.Minute}; !slices.Equal(granter.reports, want) {
		t.Errorf("reports = %v, want the cumulative totals %v", granter.reports, want)
	}
	if got, want := deadline.at(), at.Add(4*time.Minute); !got.Equal(want) {
		t.Errorf("deadline = %s, want it moved to %s", got, want)
	}
	// Four of the run's ten minutes were spent blocked and all four came
	// back, so it has as long left as it started with.
	if got, want := deadline.remaining(), 10*time.Minute; got != want {
		t.Errorf("remaining = %s, want %s", got, want)
	}
}

// Credit is measured, never granted per call: a report no larger than the
// last is not sent at all, so calling the tool in a loop -- or a wait that
// returns the moment it starts -- earns a run nothing.
func TestCreditWaitEarnsNothingForTimeNotSpent(t *testing.T) {
	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, _ := runWithCreditLeft(30*time.Minute, 45*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)

	deadline.creditWait(ctx, 2*time.Minute)
	for range 5 {
		deadline.creditWait(ctx, 0)
		deadline.creditWait(ctx, time.Minute)
	}
	if want := []time.Duration{2 * time.Minute}; !slices.Equal(granter.reports, want) {
		t.Errorf("reports = %v, want only %v -- nothing was spent after the first", granter.reports, want)
	}
	if got := deadline.creditLeft(); got != 43*time.Minute {
		t.Errorf("credit left = %s, want 43m0s", got)
	}
}

// The granter is what decides, and a granter that cannot be reached
// decides nothing. The safe direction to be wrong in is the one where the
// agent believes it has less time than it does: a deadline this process
// moved on its own would be time the thing that cancels the run does not
// agree exists.
func TestCreditWaitGrantsNothingWhenTheDaemonCannotBeReached(t *testing.T) {
	for name, granter := range map[string]*fakeGranter{
		"the report failed":       {err: errors.New("connection refused")},
		"the run is already over": {dead: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)
			deadline, _ := runDeadlineFromContext(ctx)
			at := deadline.at()

			if got := deadline.creditWait(ctx, 5*time.Minute); got != 10*time.Minute {
				t.Errorf("remaining = %s, want the uncredited 10m0s", got)
			}
			if !deadline.at().Equal(at) {
				t.Errorf("deadline moved to %s on a report that granted nothing", deadline.at())
			}
		})
	}
}

// Credit already granted is never taken back. A report that crosses with
// the run being finished answers Live false with nothing in it, and
// reading that as "your deadline just moved backwards" would retract time
// the agent has already committed a wait to.
func TestCreditWaitNeverRetractsCreditAlreadyGranted(t *testing.T) {
	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, _ := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)

	deadline.creditWait(ctx, 6*time.Minute)
	credited := deadline.at()
	granter.dead = true
	deadline.creditWait(ctx, 7*time.Minute)

	if got := deadline.at(); !got.Equal(credited) {
		t.Errorf("deadline = %s, want it left at %s", got, credited)
	}
}

// The cap is the whole ceiling: however long a run blocks, it cannot earn
// more than it was announced, and the granter's own answer is held to it
// too.
func TestCreditWaitHoldsTheCap(t *testing.T) {
	granter := &fakeGranter{cap: time.Hour}
	ctx, _ := runWithCreditLeft(10*time.Minute, 5*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)
	at := deadline.at()

	deadline.creditWait(ctx, 30*time.Minute)
	if got, want := deadline.at(), at.Add(5*time.Minute); !got.Equal(want) {
		t.Errorf("deadline = %s, want it capped at %s", got, want)
	}
	if got := deadline.creditLeft(); got != 0 {
		t.Errorf("credit left = %s, want none", got)
	}
}

// A wait keeps crediting itself as it runs, which is what makes the
// budget above a real one rather than a promise: fifteen minutes of
// blocking on a run with ten minutes left ends with the wait's own
// verdict rather than with the run being cancelled mid-call.
func TestWaitCreditsItselfWhileItBlocks(t *testing.T) {
	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, clock := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)

	client := &scriptedChecks{
		head:   pushed(),
		rounds: [][]github.CheckRun{{running("build")}, {done("build", "success")}},
	}
	fake := &fakeClock{}
	waiter := waiterFor(client, fake)
	// The waiter's own clock and the deadline's are the same one, since
	// a wait that blocked without the run's clock moving would be
	// measuring nothing.
	waiter.now = func() time.Time { *clock = fake.t; return fake.t }
	waiter.credit = func(ctx context.Context, blocked time.Duration) (time.Duration, bool) {
		return deadline.creditWait(ctx, blocked), true
	}
	// Every poll blocks for a whole minute, so the wait runs well past
	// the ten minutes the run nominally had left.
	waiter.poll = time.Minute

	text, err := waiter.wait(ctx, DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, waitPassedMarker) {
		t.Fatalf("wait did not reach a verdict:\n%s", text)
	}
	if len(granter.reports) == 0 {
		t.Fatal("the wait credited itself nothing while it blocked")
	}
	if deadline.remaining() <= 0 {
		t.Errorf("remaining = %s: the run was left past its deadline by a wait it paid for",
			deadline.remaining())
	}
}

// And when the credit cannot be had -- a daemon that stopped answering --
// the wait stops itself rather than blocking into the cancellation, and
// says which clock ran out: "call this again with a larger timeout" is
// the wrong advice when the thing that ran out is the run.
func TestWaitStopsWhenItsCreditIsRefused(t *testing.T) {
	granter := &fakeGranter{err: errors.New("connection refused")}
	ctx, clock := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)
	deadline, _ := runDeadlineFromContext(ctx)

	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{running("build")}}}
	fake := &fakeClock{}
	waiter := waiterFor(client, fake)
	waiter.now = func() time.Time { *clock = fake.t; return fake.t }
	waiter.credit = func(ctx context.Context, blocked time.Duration) (time.Duration, bool) {
		return deadline.creditWait(ctx, blocked), true
	}
	waiter.poll = time.Minute

	text, err := waiter.wait(ctx, DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, waitTimedOutMarker) || !strings.Contains(text, "this run's own clock") {
		t.Fatalf("wait did not report the run's own clock as what stopped it:\n%s", text)
	}
	if left := deadline.remaining(); left < waitForChecksDeadlineSlack {
		t.Errorf("remaining = %s, want the wait to have left the run %s to act in",
			left, waitForChecksDeadlineSlack)
	}
}

// A wait measures only its own blocked time, but the granter is told the
// whole run's: a second wait carries on from the first one's total rather
// than starting again from zero, which the granter -- keeping the largest
// total it has seen -- would take as no new blocking at all until the
// second wait outlasted the first.
func TestSecondWaitIsCreditedOnTopOfTheFirst(t *testing.T) {
	granter := &fakeGranter{cap: 45 * time.Minute}
	ctx, _ := runWithCreditLeft(10*time.Minute, 45*time.Minute, granter)

	first := startWaitCredit(ctx)
	first(ctx, 4*time.Minute)
	second := startWaitCredit(ctx)
	second(ctx, time.Minute)
	second(ctx, 3*time.Minute)

	if want := []time.Duration{4 * time.Minute, 5 * time.Minute, 7 * time.Minute}; !slices.Equal(granter.reports, want) {
		t.Errorf("reports = %v, want the run's running totals %v", granter.reports, want)
	}
	if granter.granted != 7*time.Minute {
		t.Errorf("granted = %s, want both waits' 7m", granter.granted)
	}
}

// With no deadline announced there is nothing to credit, and a wait must
// be told so rather than told it has no time left.
func TestStartWaitCreditWithNoDeadline(t *testing.T) {
	if left, ok := startWaitCredit(context.Background())(context.Background(), time.Minute); ok || left != 0 {
		t.Errorf("credit = %s, %v; want 0, false", left, ok)
	}
}
