package mcp

// run_deadline.go is the half of "a run knows how long it has" that
// reaches an agent at turn 200.
//
// orchestrator.BuildPrompt states the budget in the prompt, which is the
// right place to state it and the wrong place to leave it: the prompt is
// read once, hours before the deadline it describes actually matters,
// and by the turn where committing early would have saved the work it is
// the furthest thing from the model's attention. A tool result is the
// opposite -- it is the one piece of text a run reads every single turn.
//
// So once the run is inside RunDeadlineNoticeWindow of its wall-clock
// deadline, every tool result carries a line saying how much is left and
// what to do about it. The "what to do" half is deliberate: a bare
// number is a fact a run can note and ignore, while "push what works
// now" is the actual behaviour the deadline calls for, and grain knows
// which it wants. What is at stake is specific -- salvagePushedBranch
// rescues a branch that was pushed when the clock runs out, and nothing
// rescues a commit that was not -- so the line says that too rather than
// leaving an agent to guess how much of its work the cancellation eats.
//
// The same deadline is also handed to the handlers themselves, on their
// ctx (runDeadlineContextKey). A notice is advice a tool prints after
// the fact; a tool that *blocks* has to know the number beforehand, to
// bound itself by it -- see wait_for_checks, which is the reason that
// half exists.
//
// And it moves, which is the other half of the same story: time a run
// spends blocked inside wait_for_checks is credited back to its deadline
// as it is spent, up to a cap, so that waiting on CI does not come out
// of the budget for working (wait_credit.go).

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RunDeadlineNoticeWindow is how much of a run's wall-clock budget has
// to be left before its tool results start saying so. Sized against
// orchestrator.DefaultMaxRunRuntime (two hours): a sixth of it, which is
// long enough for a run to finish the change it is in the middle of,
// commit it, push it and watch one round of CI -- the sequence the
// notice exists to get started before it is impossible.
//
// It is an absolute window rather than a fraction of the budget because
// what a run has to do with the time is absolute: pushing and waiting on
// CI takes as long as it takes whether the budget was two hours or
// twenty minutes. A deployment that sets a very short MaxRunRuntime
// therefore has every tool result carry the notice from the first turn,
// which is not a bug -- such a run is near the wall for the whole of its
// life, and that is worth saying on every turn of it.
const RunDeadlineNoticeWindow = 20 * time.Minute

// runDeadlineFinalWindow is the point where the advice changes from
// "finish this piece, then push" to "push now". Inside it there is no
// longer room for another edit/build/test cycle, so a run still starting
// one is a run whose remaining work is about to be destroyed unpushed.
const runDeadlineFinalWindow = 5 * time.Minute

// runDeadline is the deadline a Registry announces on its tool results,
// and the clock it reads to know how far off it is. now is a field so a
// test can drive the notice without sleeping; AnnounceDeadline fills it
// with time.Now for every real server.
//
// base is the deadline as announced -- what the run would be cancelled
// at having waited on nothing. The moment it is actually cancelled at is
// base plus whatever wait credit grain has granted so far, which is what
// at() returns and what every remaining() in this package is measured
// against; credit is only ever added, never taken back, so that number
// only moves outwards. See wait_credit.go for who grants it and why the
// granting is not this process's to do alone.
type runDeadline struct {
	base time.Time
	now  func() time.Time

	// cap and reporter are AnnounceWaitCredit's, and both being unset is
	// "this server has no credit to earn": a bare `grain mcpserver`, a
	// test, any dispatch with no daemon to ask. Credit then stays zero
	// and the deadline stands exactly where it was announced.
	cap      time.Duration
	reporter WaitCreditReporter

	// mu guards the ledger below. A Registry serves one call at a time,
	// but a wait credits itself while it is still running -- so the
	// handler holding these numbers and the deadline arithmetic reading
	// them are the same goroutine only by today's accident, and this is
	// too cheap to make an assumption of.
	mu       sync.Mutex
	blocked  time.Duration
	credited time.Duration
}

// AnnounceDeadline tells r the moment grain will cancel the run it is
// serving -- the deadline on the ctx orchestrator.RunDispatch hands
// framework.Run, carried here as an absolute time through the forked
// mcpserver's -run-deadline flag (agent.RunDeadlineArgs).
//
// An absolute time rather than a duration because this process is not
// the one the clock started for: an mcpserver is forked by the CLI some
// way into the run it serves, so a budget measured from its own start
// would quietly hand the run back time it has already spent.
//
// A zero time is "nobody told this server about a deadline", which is
// every caller that has none to give -- pkg/mcp's own tests, tests/e2e,
// a `grain mcpserver` run by hand -- and leaves every tool result
// exactly as it was. now may be nil for the wall clock.
func (r *Registry) AnnounceDeadline(at time.Time, now func() time.Time) {
	if at.IsZero() {
		return
	}
	if now == nil {
		now = time.Now
	}
	r.deadline = &runDeadline{base: at, now: now}
}

// at is the moment the run is cancelled as things stand: the announced
// deadline plus the wait credit granted so far.
func (d *runDeadline) at() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.base.Add(d.credited)
}

// remaining is how much of the run is left as of now.
func (d *runDeadline) remaining() time.Duration {
	return d.at().Sub(d.now())
}

// deadlineNotice is the line to append to a tool result answered now, or
// "" when there is no deadline or it is still comfortably far off.
func (r *Registry) deadlineNotice() string {
	if r.deadline == nil {
		return ""
	}
	return runDeadlineNotice(r.deadline.remaining())
}

// runDeadlineContextKey is how a tool that blocks gets at the number
// itself, rather than only at the notice above.
//
// The notice is enough for a tool that answers immediately: it tells the
// run what to do next. It is not enough for wait_for_checks, which
// decides how long to block *before* it answers -- a run with eight
// minutes left that asks for a fifteen-minute wait spends the whole of
// its remaining life inside one tool call, is cancelled mid-wait, and so
// never sees the verdict it blocked for nor gets the turn it would have
// used to act on it. So the deadline the registry holds is put on the
// ctx every handler runs under, and a tool that can bound itself by it
// reads it from there (waitForChecksBudget).
//
// On the ctx rather than through the tool constructors because the
// registry is already the one place AnnounceDeadline puts it: a second
// copy threaded into NewPullRequestTools would be a second thing to keep
// in step, at every call site including the ones that build the tools
// only to read their names (agent/claude's allowedTools).
//
// A context *value*, deliberately, and not context.WithDeadline: this
// must tell a handler how much time there is, never cancel it. The run's
// own cancellation already arrives on this ctx from outside, and a
// second deadline layered on here would cut tool calls short of it.
type runDeadlineContextKey struct{}

// withRunDeadline hands the announced deadline to the handler about to
// run. A registry that was told none leaves the ctx exactly as it is,
// which is what makes runDeadlineRemaining's "nobody said" case honest.
func (r *Registry) withRunDeadline(ctx context.Context) context.Context {
	if r.deadline == nil {
		return ctx
	}
	return context.WithValue(ctx, runDeadlineContextKey{}, r.deadline)
}

// runDeadlineRemaining is how long the run this call belongs to has
// left, and whether anything told this server about a deadline at all.
//
// The false case is every caller with none to give -- pkg/mcp's own
// tests, tests/e2e, a `grain mcpserver` run by hand -- and a tool
// reading it must then behave exactly as it did before rather than
// inventing a bound of its own: "no deadline announced" is not "no time
// left". The duration may be negative, for a run already past it.
func runDeadlineRemaining(ctx context.Context) (time.Duration, bool) {
	deadline, ok := runDeadlineFromContext(ctx)
	if !ok {
		return 0, false
	}
	return deadline.remaining(), true
}

// runDeadlineFromContext is the deadline itself, for the one caller that
// needs more of it than a number: wait_for_checks, which both reads the
// clock and moves it (wait_credit.go).
func runDeadlineFromContext(ctx context.Context) (*runDeadline, bool) {
	deadline, ok := ctx.Value(runDeadlineContextKey{}).(*runDeadline)
	return deadline, ok
}

// runDeadlineNotice words the notice for a run with remaining left.
//
// Every branch names grain as the thing that ends the run, and the
// sandbox as what goes with it: the failure this exists to prevent is a
// run that treats its own cancellation as something that might not
// happen, or as something that leaves its work behind.
func runDeadlineNotice(remaining time.Duration) string {
	switch {
	case remaining > RunDeadlineNoticeWindow:
		return ""
	case remaining <= 0:
		return "[grain] This run is past its wall-clock deadline and will be cancelled at " +
			"any moment. Push whatever is committed, right now, before anything else: " +
			"nothing that is only in this sandbox survives."
	case remaining <= runDeadlineFinalWindow:
		return fmt.Sprintf(
			"[grain] %s left before grain cancels this run and destroys its sandbox. There "+
				"is no time for another edit-and-test cycle: commit and push what already "+
				"works now, and leave what is unfinished in a comment_on_issue note. Work "+
				"that was not pushed is lost.",
			humanRemaining(remaining),
		)
	default:
		return fmt.Sprintf(
			"[grain] %s left before grain cancels this run and destroys its sandbox. Only a "+
				"pushed branch survives that, so finish the piece you are on, commit it and "+
				"push -- then decide what else fits, rather than starting work you will not "+
				"be able to push.",
			humanRemaining(remaining),
		)
	}
}

// humanRemaining renders what is left the way somebody acting on it
// reads it: whole minutes while there are minutes to spend, and seconds
// once there are not, where the difference between 40 and 10 is the
// difference between one more push and none.
//
// Rounded down, always. The number is used to decide what still fits,
// and a run told "15m" with fourteen and a half minutes left has been
// handed thirty seconds it does not have.
//
// The switch to seconds happens at two minutes rather than at one: "1m"
// covers everything from sixty seconds to a hundred and nineteen, and
// those are not the same situation to be in.
func humanRemaining(d time.Duration) string {
	if d >= 2*time.Minute {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.Truncate(time.Second).String()
}

// withDeadlineNotice puts notice at the end of a tool result, blank-line
// separated so it reads as grain speaking rather than as the last line
// of the command's own output -- and appended rather than prepended so
// that a client which truncates a long result keeps the answer's head,
// which is where a tool's own verdict is. "" for either half leaves the
// other untouched.
func withDeadlineNotice(text, notice string) string {
	switch {
	case notice == "":
		return text
	case text == "":
		return notice
	}
	return text + "\n\n" + notice
}
