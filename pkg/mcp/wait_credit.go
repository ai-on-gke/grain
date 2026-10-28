package mcp

// Time a run spends blocked on CI is not time it spent working, and this
// file is the half of "so do not charge it for it" that lives inside the
// tools.
//
// wait_for_checks bounds itself by the run's own deadline
// (run_deadline.go): a run eight minutes from cancellation cannot afford
// a fifteen-minute wait, so it does not start one. That is the right
// arithmetic and it produced the wrong outcome often enough to be worth
// fixing -- a run that has pushed a fix and has ten minutes left is a run
// that opens a pull request whose CI verdict nobody ever reads, because
// the last thing it did was stand still and be cut short. The wait is not
// what used the budget up; it is what the budget had no room left for.
//
// So blocked time is credited back to the deadline, up to a per-run cap.
// Two properties do the whole of the work here:
//
//   - it is *measured*, never granted per call. Credit is the time a wait
//     actually spent blocked, so a run cannot farm it by calling the tool
//     repeatedly: a wait that returns at once earns nothing.
//   - it accrues *live*, while the wait is still running, rather than on
//     its return. This is the part that decides whether any of it is
//     worth anything. The first wait a squeezed run starts is clamped by
//     the deadline as it stands when the call begins, so credit that
//     landed only afterwards would move a deadline the run has already
//     been cut short by -- which is exactly the case this exists for.
//     The deadline therefore advances in step with the wait, and a run
//     with ten minutes of nominal budget and unspent credit can start and
//     finish a fifteen-minute wait.
//
// Granting is not this process's to do. An mcpserver is a fork holding a
// transport into a sandbox; what actually cancels the run is a timer in
// the daemon that dispatched it, and a deadline moved here alone would be
// this process telling the agent about time the thing that kills it does
// not agree exists. So the ledger below reports the blocked time it has
// measured to the daemon (pkg/cli/mcpserver.go's daemonWaitCredit, over
// the same REST hop update_status takes) and adopts the credit the daemon
// says it granted. A report that fails grants nothing, deliberately: the
// safe direction to be wrong in is the one where the agent believes it
// has less time than it does.

import (
	"context"
	"time"
)

// WaitCredit is what the granter says one report earned: the total
// credit this run now holds, and whether the granter still has a live run
// to hold it against.
//
// Live false is not an error, for the reason StatusReport.Live is not: a
// run whose report lands after grain has already finished it has done
// nothing wrong, and there is no deadline left to move. Credited is then
// meaningless and is not adopted.
type WaitCredit struct {
	Credited time.Duration
	Live     bool
}

// WaitCreditReporter tells whoever actually enforces this run's deadline
// how long the run has now spent blocked on CI, and reports back the
// credit that bought.
//
// blocked is cumulative for the whole run rather than a delta, so that a
// report lost to a failed request costs nothing but its own lateness: the
// next one carries the same total again. An implementation is expected to
// be idempotent in the same way -- crediting max(seen, reported), never a
// sum.
//
// It names no run for the reason StatusReporter names no task: which run
// a report belongs to is fixed at process start (`grain mcpserver
// -task`), never by anything in a tool call.
type WaitCreditReporter interface {
	CreditWait(ctx context.Context, blocked time.Duration) (WaitCredit, error)
}

// AnnounceWaitCredit tells r that time this run spends blocked inside
// wait_for_checks is credited back to the deadline AnnounceDeadline
// announced -- at most cap over the whole run -- and who to ask for it.
//
// Both halves are required and neither is guessed at. A registry with no
// deadline has nothing to credit; a cap of zero is a deployment that
// grants none; and a nil reporter is a server with no daemon to ask
// (pkg/mcp's own tests, tests/e2e, a `grain mcpserver` run by hand),
// which must behave exactly as it did before this existed rather than
// moving a deadline nobody else would honour. Each of those leaves the
// deadline standing exactly where it was announced.
func (r *Registry) AnnounceWaitCredit(cap time.Duration, reporter WaitCreditReporter) {
	if r.deadline == nil || cap <= 0 || reporter == nil {
		return
	}
	r.deadline.cap = cap
	r.deadline.reporter = reporter
}

// creditLeft is how much credit this run could still earn -- what a wait
// deciding how long it may block adds to the time it can see, since a
// wait that is about to block for ten minutes will have earned ten
// minutes back by the time it ends.
//
// Zero for a server that was told no cap, which is the "nobody grants
// credit here" case AnnounceWaitCredit's own doc comment describes.
func (d *runDeadline) creditLeft() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if left := d.cap - d.credited; left > 0 {
		return left
	}
	return 0
}

// creditWait records that this run has now spent blocked in total, asks
// the granter for the credit that earns, and returns how much of the run
// is left once whatever was granted has moved the deadline.
//
// blocked is the whole run's blocked time, not this call's, and only ever
// grows: a report that is not larger than the last one is not sent at
// all, so a wait that ends the moment it starts costs no round trip and
// earns nothing.
//
// Credit already granted is never given back. The granter's number is
// authoritative and monotonic on its side too, but a report that crossed
// with a run being torn down can answer Live false with a zero total, and
// treating that as "your deadline just moved backwards" would retract
// time the agent has already been told about and may have committed to a
// wait on the strength of.
//
// A failed report is not an error here and does not stop the wait: it
// simply grants nothing. What that costs is the wait being cut short by
// the uncredited deadline, which is what would have happened anyway
// before any of this existed.
func (d *runDeadline) creditWait(ctx context.Context, blocked time.Duration) time.Duration {
	d.mu.Lock()
	reporter := d.reporter
	if reporter == nil || blocked <= d.blocked {
		d.mu.Unlock()
		return d.remaining()
	}
	d.blocked = blocked
	cap := d.cap
	d.mu.Unlock()

	credit, err := reporter.CreditWait(ctx, blocked)
	if err != nil || !credit.Live {
		return d.remaining()
	}
	d.mu.Lock()
	if granted := min(credit.Credited, cap); granted > d.credited {
		d.credited = granted
	}
	d.mu.Unlock()
	return d.remaining()
}

// blockedSoFar is the whole run's blocked time as last reported -- the
// total every wait that has already returned ran up between them.
func (d *runDeadline) blockedSoFar() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.blocked
}

// startWaitCredit is checkWaiter.credit's real implementation, made once
// per call at the moment that call starts to block: what it returns
// credits that call's own blocked time against the run's deadline, if this
// server was told about a deadline at all, and says how much of the run
// is left.
//
// A wait only knows how long it has itself been blocked, and creditWait
// wants the whole run's total, so the run's total at the start of this
// call is added on. Without it every call reported its own time as the
// run's -- and since a report no larger than the last is not sent, and
// the granter keeps max(seen, reported), a run's second wait earned
// nothing until it outlasted its first, and a run that waited three
// times for ten minutes was credited ten minutes rather than thirty.
//
// The false case is every server that was told none, and a wait reading
// it must go on bounding itself exactly as it did before -- "nobody said"
// is not "no time left", the same rule runDeadlineRemaining states.
func startWaitCredit(ctx context.Context) func(ctx context.Context, blocked time.Duration) (time.Duration, bool) {
	deadline, ok := runDeadlineFromContext(ctx)
	if !ok {
		return func(context.Context, time.Duration) (time.Duration, bool) { return 0, false }
	}
	before := deadline.blockedSoFar()
	return func(ctx context.Context, blocked time.Duration) (time.Duration, bool) {
		return deadline.creditWait(ctx, before+blocked), true
	}
}
