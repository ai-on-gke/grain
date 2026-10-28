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

// Time a run spends blocked on CI, credited back to its wall-clock
// deadline -- the half that holds the clock a run is actually killed by.
//
// Config.MaxRunRuntime is a budget for *working*, and a run standing
// still inside wait_for_checks is not working. That distinction was worth
// nothing for as long as the deadline was fixed: a run ten minutes from
// cancellation would have its fifteen-minute wait clamped to eight, learn
// nothing from the eight, and be cancelled having opened a pull request
// whose CI verdict nobody read. The wait did not use the budget up. It
// was the one thing the budget had no room left for, at the one moment
// the answer was worth having.
//
// So the deadline moves:
//
//	effective = start + MaxRunRuntime + min(blocked, RunWaitCreditCap)
//
// where blocked is time the run has actually spent inside
// wait_for_checks. Credit is measured rather than granted per call -- a
// wait that returns at once earns nothing, so calling the tool in a loop
// buys a run no time at all -- and it is capped per run rather than per
// wait, so the ceiling on any run's life is MaxRunRuntime plus the cap
// and nothing can raise it.
//
// The measuring is done in the agent's own mcpserver, which is a
// different process (pkg/mcp's wait_credit.go): it reports the blocked
// time it has run up so far, as it runs it, and this file is what that
// report reaches. It has to reach *here* because this is where the timer
// is. A deadline the tools moved on their own would be a fork telling an
// agent about time the thing that cancels it does not agree exists --
// and the run would be killed mid-wait anyway, which is the failure this
// is fixing rather than a new way to suffer it.
//
// Live, for the same reason. A report is credited while the wait it
// belongs to is still running, so the deadline advances in step with the
// wait rather than after it: credit that landed only on a wait's return
// would arrive after the clamp it exists to prevent.

import (
	"context"
	"sync"
	"time"
)

// RunWaitCreditCap is the most wall-clock time any one run can earn back
// by blocking on CI, however long it blocks for. With the two-hour
// default budget it puts the ceiling on a run's whole life at 2h45m.
//
// Sized off what grain's own runs have actually needed rather than off a
// round number: the waits that were cut short by the run's clock rather
// than by slow CI ran seven to fourteen minutes against a fifteen-minute
// default, and forty-five minutes covers a run doing that three times
// over. It is deliberately a constant rather than a grain_config column
// -- a knob costs a schema migration and an operator a decision, and this
// number is re-derivable from the waits every run already records
// (model.RunWaitCredit, beside task_run_check_wait.waited_ms). If the
// recorded credit ever runs up against this cap in the ordinary course of
// work, that is the evidence to re-price it with.
const RunWaitCreditCap = 45 * time.Minute

// RunWaitCredit is what one report earned: how long the run is now
// recorded as having spent blocked, how much of that was credited back to
// its deadline, and where that deadline now stands.
//
// Live false is a task with no run in flight to credit -- a report that
// crossed with the run being finished or cancelled. It is not an error,
// for the reason ui.Activity.Live is not: nothing has gone wrong, there
// is simply no deadline left to move, and the tool that asked passes that
// on to the agent rather than reading a failure into it.
type RunWaitCredit struct {
	Blocked  time.Duration
	Credited time.Duration
	Deadline time.Time
	Live     bool
}

// RunWaitCredits is the set of live runs whose deadline can still be
// moved, keyed by task ID.
//
// Keyed by task rather than by run for SandboxRecreations' reason: the
// task id is all the asking process is ever told (`grain mcpserver
// -task`), and one task has at most one live run at a time.
//
// The zero value is not usable; NewRunWaitCredits builds one. A nil
// *RunWaitCredits is: every method tolerates one, so a deployment or test
// that wires none simply grants no credit and every run is bounded by
// MaxRunRuntime exactly as it was before this existed.
type RunWaitCredits struct {
	mu   sync.Mutex
	live map[string]*runClock
}

// NewRunWaitCredits returns an empty registry, ready for runs to register
// themselves in as they are dispatched.
func NewRunWaitCredits() *RunWaitCredits {
	return &RunWaitCredits{live: map[string]*runClock{}}
}

// register makes taskID's live run creditable and returns the function
// that stops it being so, which the dispatch goroutine calls once
// framework.Run has returned.
func (r *RunWaitCredits) register(taskID string, clock *runClock) func() {
	if r == nil || clock == nil {
		return func() {}
	}
	r.mu.Lock()
	r.live[taskID] = clock
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		// Compared, not deleted blindly: a redispatch of the same task
		// registers under the same key, and a slow unregister from the
		// previous run must not drop the new run's entry -- the same
		// care SandboxRecreations.register takes.
		if r.live[taskID] == clock {
			delete(r.live, taskID)
		}
		r.mu.Unlock()
	}
}

// Credit records that taskID's live run has now spent blocked in total
// and moves its deadline out by whatever that earns.
//
// blocked is cumulative for the whole run rather than one wait's own
// figure, so a report lost to a failed request costs nothing but its
// lateness: the next one carries the same total. Applying it is
// idempotent in the same way -- the run keeps the largest total it has
// been told, never a sum -- which is what makes a caller free to repeat
// itself and unable to inflate its credit by doing so.
//
// A task with no live run answers Live false rather than an error, for
// the reason RunWaitCredit gives.
func (r *RunWaitCredits) Credit(ctx context.Context, taskID string, blocked time.Duration) (RunWaitCredit, error) {
	return r.CreditSource(ctx, taskID, WaitSourceMCP, blocked)
}

// The sources a run's blocked time is reported from. Each keeps its own
// cumulative total, and the run's blocked time is their sum: a run that
// waits in its mcpserver's wait_for_checks and then in the sandbox's
// grain ci wait has waited for both, and keeping only the larger of two
// unrelated totals would credit it for one.
const (
	// WaitSourceMCP is the run's own mcpserver (pkg/mcp's wait_credit.go),
	// reaching here over POST /api/tasks/{id}/wait-credit.
	WaitSourceMCP = ""
	// WaitSourceCLI is the sandbox proxy serving the in-sandbox grain
	// CLI's blocking commands (pkg/gitproxy's cli_actions.go: grain ci
	// wait, grain ci run-job), which runs inside this daemon and reports
	// in-process.
	WaitSourceCLI = "cli"
)

// CreditSource is Credit for blocked time reported by source: blocked is
// that source's own cumulative total for the run, kept monotonically
// exactly as Credit keeps the whole run's, and the run is credited for
// the sum of every source's total, still capped at RunWaitCreditCap.
func (r *RunWaitCredits) CreditSource(ctx context.Context, taskID, source string, blocked time.Duration) (RunWaitCredit, error) {
	if r == nil {
		return RunWaitCredit{}, nil
	}
	r.mu.Lock()
	clock := r.live[taskID]
	r.mu.Unlock()
	if clock == nil {
		return RunWaitCredit{}, nil
	}
	return clock.creditFrom(source, blocked), nil
}

// runClock is one run's wall-clock deadline: the ctx framework.Run is
// given, the timer that cancels it, and the ledger that decides when.
//
// It replaces the context.WithTimeoutCause RunDispatch used to derive
// that ctx with, because a context deadline cannot be moved once it is
// set and the whole of this file is about moving one. What it keeps is
// every property that call had -- a child of runCtx, so a task closed
// underneath the run still cancels it; errRunTimedOut as the cause, so
// the run is recorded as having hit the wall rather than as having failed
// -- and adds one: base, the deadline as it stands with no credit, which
// is the number the agent is told (agent.RunConfig.Deadline) and the one
// its own tools credit upwards from.
type runClock struct {
	// base is the uncredited deadline, fixed when the clock is made.
	base time.Time
	// cap is the most credit this run can ever be granted.
	cap time.Duration
	// gap is blocked time this clock starts with that no report from the
	// run's own tools covers: the restart an adopted run sat through
	// with no daemon reading it (newAdoptedRunClock). Added to every
	// cumulative report, so the run's own waits are credited on top of
	// it rather than instead of it. Zero for a run this daemon started.
	gap time.Duration

	// ctx is the run's own, watched only so that a credit arriving after
	// cancellation cannot restart a timer that has already fired.
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu       sync.Mutex
	blocked  time.Duration
	credited time.Duration
	// reported is each source's largest cumulative report (CreditSource);
	// blocked is gap plus their sum, never less than it already was.
	reported map[string]time.Duration
	timer    *time.Timer
	stopped  bool
}

// newRunClock derives the ctx framework.Run gets from parent, cancelled
// with errRunTimedOut once budget has passed -- plus whatever credit the
// run earns along the way, up to cap.
//
// Real time throughout, deliberately, and not Config.Now: this is a
// timer, the deadline it enforces is the one the agent's own tool results
// count down (mcp.Registry.AnnounceDeadline, via the RFC3339 timestamp
// the framework passes its mcpserver), and a run driven by a test's fake
// clock would otherwise be handed a deadline hours away from the one it
// is actually killed at. context.WithTimeoutCause, which this replaces,
// read the same clock.
//
// The returned clock's stop method stops the timer and cancels the ctx,
// and is what the dispatch goroutine defers: exactly the pair RunDispatch
// already called cancelAgentCtx for.
func newRunClock(parent context.Context, budget, cap time.Duration) (context.Context, *runClock) {
	ctx, cancel := context.WithCancelCause(parent)
	if budget <= 0 {
		// A durable run (durableDispatch): no deadline, so no timer and
		// nothing to credit. base stays zero, which every reader of the
		// deadline -- the forked mcpserver's -run-deadline, the run
		// host's label -- already takes as none.
		return ctx, &runClock{ctx: ctx, cancel: cancel}
	}
	clock := &runClock{
		base:   time.Now().Add(budget),
		cap:    cap,
		ctx:    ctx,
		cancel: cancel,
	}
	clock.timer = time.AfterFunc(budget, func() { cancel(errRunTimedOut) })
	return ctx, clock
}

// newAdoptedRunClock is newRunClock for a run this daemon adopted from
// the one that started it (ResumeAdoptedRun): the deadline is not reset
// but carried, as base, and gap -- how long no daemon was reading the
// run -- is credited at once as blocked time, up to cap, since the
// restart was grain's doing and not the run's. A run whose deadline,
// credit included, has already passed is cancelled with errRunTimedOut
// straight away.
func newAdoptedRunClock(parent context.Context, base time.Time, cap, gap time.Duration) (context.Context, *runClock) {
	ctx, cancel := context.WithCancelCause(parent)
	if base.IsZero() {
		// Adopting a durable run: no deadline to carry (newRunClock).
		return ctx, &runClock{ctx: ctx, cancel: cancel}
	}
	clock := &runClock{
		base:     base,
		cap:      cap,
		gap:      gap,
		ctx:      ctx,
		cancel:   cancel,
		blocked:  gap,
		credited: min(gap, cap),
	}
	clock.timer = time.AfterFunc(time.Until(base.Add(clock.credited)), func() { cancel(errRunTimedOut) })
	return ctx, clock
}

// ledger is what this run has spent blocked and been credited for, read
// once the run is over so it can be recorded (model.RunWaitCredit).
func (c *runClock) ledger() (blocked, credited time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blocked, c.credited
}

// credit applies a cumulative blocked-time report and moves the timer.
//
// Monotonic in both directions it could move: a report smaller than one
// already applied is ignored rather than taken as time given back, and
// credit already granted is never revoked. A run that has been told it
// has until a certain moment has usually already committed a wait to it.
//
// A clock whose run is already over -- stopped, or cancelled by anything
// at all -- records the blocked time and moves nothing. Resetting the
// timer of a run that is being torn down would at best be pointless and
// at worst restart a timer whose cancellation has already been decided.
func (c *runClock) credit(blocked time.Duration) RunWaitCredit {
	return c.creditFrom(WaitSourceMCP, blocked)
}

// creditFrom is credit for one source's cumulative report.
func (c *runClock) creditFrom(source string, blocked time.Duration) RunWaitCredit {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reported == nil {
		c.reported = map[string]time.Duration{}
	}
	if blocked > c.reported[source] {
		c.reported[source] = blocked
	}
	total := c.gap
	for _, d := range c.reported {
		total += d
	}
	if total > c.blocked {
		c.blocked = total
	}
	if granted := min(c.blocked, c.cap); granted > c.credited {
		c.credited = granted
		if !c.stopped && c.ctx.Err() == nil && c.timer != nil {
			c.timer.Reset(time.Until(c.base.Add(c.credited)))
		}
	}
	deadline := c.base
	if !deadline.IsZero() {
		deadline = deadline.Add(c.credited)
	}
	return RunWaitCredit{
		Blocked:  c.blocked,
		Credited: c.credited,
		Deadline: deadline,
		Live:     !c.stopped && c.ctx.Err() == nil,
	}
}

// stop ends the clock and the ctx it derived, whether or not the timer
// ever fired. Cancelling with no cause of its own leaves whatever cause
// the timer (or an ancestor) already recorded in place, which is what
// keeps context.Cause able to tell a run that hit the wall from one that
// simply finished -- exactly what cancelAgentCtx did before.
func (c *runClock) stop() {
	c.mu.Lock()
	c.stopped = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	c.cancel(nil)
}
