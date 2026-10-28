package mcp

// wait_for_checks is pull_request_status with the polling loop moved
// inside grain.
//
// pull_request_status answers "what does CI say right now", which is the
// honest answer to a question nobody actually asks: a run that has just
// pushed wants to know how CI *ended*, and the only way to get that out
// of a status read is to call it, be told "nothing has failed, but the
// unfinished checks carry no verdict yet", spend a turn sleeping, and
// call it again. Every one of those turns costs a model round trip and
// burns the run's turn budget on waiting rather than on work, and a run
// that guesses the interval wrong either wastes turns or -- worse --
// reads one queued check, decides it has waited long enough, and
// finishes on a build it never saw a verdict for. This tool blocks
// instead: one call, one answer, and the answer is always a verdict
// (failed, passed, nothing registered, or "still running when the clock
// ran out") rather than a snapshot.
//
// It returns the moment the outcome is settled, which is not the same as
// the moment CI is finished:
//
//   - any check failing ends the wait immediately, without waiting for
//     the rest. There is nothing left for the agent to learn by watching
//     a build it already has to fix, and the sooner it is told the more
//     of its turn budget is left to fix it in.
//   - every check completing with none failing ends it too. That is the
//     one green light in here.
//
// Everything else is bounded by the timeout, and by two shorter clocks.
//
// GitHub reports no checks at all for a commit both when CI has not
// registered them yet and when the repo has no CI, and the two are
// indistinguishable in the moment. Blocking the full timeout on a repo
// with no CI would spend fifteen minutes learning nothing, so the empty
// answer is given firstCheckGrace to become non-empty and then reported
// as what it most likely is.
//
// And the run's own wall clock, when grain has told this server about
// one (run_deadline.go). A wait is only useful if the run outlives it:
// eight minutes from cancellation, a fifteen-minute wait is a run
// spending the whole of its remaining life inside one tool call, being
// killed mid-wait, and never seeing the verdict -- when the same eight
// minutes could have carried a push. So each wait is clamped to what is
// left minus waitForChecksDeadlineSlack, the report says when that is
// what cut it short, and a call with no room left for a wait at all
// answers at once instead of pretending to watch (waitForChecksBudget).
//
// That clock is not fixed, though, and this tool is the reason: blocked
// time is credited back to the run's deadline as it is spent, up to a
// per-run cap (wait_credit.go). So the room a wait has is what is left
// *plus* the credit still to earn, and the wait keeps crediting itself
// while it runs -- which is what lets a run with ten minutes of nominal
// budget start and finish a fifteen-minute wait rather than being cut
// short at the only moment the answer would have been worth having.
//
// The scope is fixed at process start exactly as pull_request_status's
// is (pullrequest_tools.go's doc comment): a run waits on CI for its own
// branch or for nothing. The commit is pinned too -- the branch head is
// read once, at the start, and the whole wait is about that SHA, so an
// answer can never be a mix of verdicts from two different pushes.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

// How long a wait can run for, and the knobs that are not an agent's to
// set. The default is sized for this repo's own CI, which builds
// container images and takes minutes rather than seconds; the maximum
// exists because a tool call that blocks for hours is indistinguishable
// from a wedged run to everything watching it.
//
// MaxWaitForChecksTimeout is exported because a client with a tool-call
// timeout of its own has to be told a number larger than this one, or it
// kills the call before the tool's own clock can produce the report that
// is the entire point (agent/claude's mcpToolTimeout).
const (
	DefaultWaitForChecksTimeout = 15 * time.Minute
	MaxWaitForChecksTimeout     = 60 * time.Minute
	minWaitForChecksTimeout     = 30 * time.Second
)

// waitForChecksDeadlineSlack is how much of the run's remaining time a
// wait refuses to spend: the answer is worth nothing unless there is
// still a turn left to act on it, and a wait that ran right up to the
// wall would be cancelled with the report still unwritten.
//
// Two minutes, which is a turn to read the verdict and one push or one
// comment_on_issue note -- the only two things left worth doing that
// late in a run. It is not sized to fit a fix: a run this close to the
// wall is not going to repair a red build, and pretending otherwise
// would be an argument for leaving *more* time unspent than the wait
// itself is worth.
const waitForChecksDeadlineSlack = 2 * time.Minute

// checkPollInterval is how often the wait re-reads CI, and
// firstCheckGrace is how long an entirely empty check list is given to
// become a real one before it is reported as "this repo has no CI".
//
// Fifteen seconds is fast enough that the report lands within a turn of
// the build finishing and slow enough that a full-length wait costs a
// couple of hundred reads against a rate limit measured in thousands per
// hour. Neither is an argument: an agent choosing its own poll interval
// can only get this wrong, and both are only variables so a test can
// shrink them.
var (
	checkPollInterval = 15 * time.Second
	firstCheckGrace   = 3 * time.Minute
)

// checkReadAttempts is how many *consecutive* failed reads of CI end the
// wait. A single 500 or a dropped connection in the middle of a
// fifteen-minute wait is not a reason to throw away the wait -- the next
// poll is fifteen seconds away and will most likely succeed -- but a
// credential that cannot see checks at all fails every read, and a wait
// that retried that forever would block until the deadline and then
// report a timeout, hiding the real cause behind a wrong one.
const checkReadAttempts = 3

// NewWaitForChecksTools returns the blocking half of the CI loop:
// wait_for_checks, and nothing else. Registered on the same terms as
// pull_request_status, including the nil-client case -- see
// NewPullRequestTools.
func NewWaitForChecksTools(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) []Tool {
	// Wrapped here as well as in NewPullRequestTools, since this one is
	// exported and a caller may register the blocking half on its own;
	// wrapping twice is a no-op (readable_github.go).
	return []Tool{waitForChecksTool(readableErrors(client), grainCI, scope)}
}

func waitForChecksTool(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) Tool {
	return Tool{
		Name: "wait_for_checks",
		// The two numbers are formatted from the constants rather than
		// written out, since a description that disagrees with the
		// clamp teaches an agent a timeout it cannot actually have.
		Description: fmt.Sprintf("Block until CI has an actual verdict on the commit at the tip "+
			"of your branch, then report it -- instead of calling "+
			"pull_request_status over and over and spending turns waiting. "+
			"It returns as soon as any check fails -- with the end of each "+
			"failing job's own log, so you can start fixing without waiting "+
			"for the rest and without reproducing the failure first (a log "+
			"GitHub has not finished publishing is worth up to a minute of "+
			"asking again, so a failing answer can take that much longer "+
			"than the failure did) -- or "+
			"once every check has finished with none failing, or when it "+
			"times out. Push first: it reports on your latest pushed "+
			"commit and does not watch for new ones. "+
			"Optional timeout_seconds bounds the wait (default %d, maximum "+
			"%d); a timeout is reported, not an error, along with what each "+
			"check was doing when the clock ran out. Time spent blocked "+
			"here is credited back to the run's own deadline as it is "+
			"spent, up to a per-run cap, so waiting on CI does not come "+
			"out of your budget for working; a wait is still cut to the "+
			"time this run has left before grain cancels it once that "+
			"credit runs out, and the report says so when that is what "+
			"bounded it -- so you never have to "+
			"budget the wait against the run yourself.%s This only reads; "+
			"it pushes nothing and changes nothing.",
			int(DefaultWaitForChecksTimeout.Seconds()), int(MaxWaitForChecksTimeout.Seconds()),
			grainCIWaitClause(grainCI)),
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"timeout_seconds": map[string]any{
					"type": "number",
					"description": fmt.Sprintf("How long to wait before giving up and reporting "+
						"what CI was doing at that moment. Defaults to %d; values outside "+
						"%d..%d are clamped into that range.",
						int(DefaultWaitForChecksTimeout.Seconds()),
						int(minWaitForChecksTimeout.Seconds()),
						int(MaxWaitForChecksTimeout.Seconds())),
				},
			},
		},
		ElisionAdvice: elisionAdviceCIReport,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if client == nil || !scope.complete() {
				return Result{
					Text: "There is no CI to wait for: this run has no GitHub repository " +
						"configured for it. Do the work in the sandbox and report what " +
						"you found instead.",
					IsError: true,
				}
			}
			budget := waitForChecksBudget(ctx, args)
			if budget.tooLate {
				return Result{Text: noTimeToWait(budget.remaining, scope.Branch)}
			}
			waiter := newCheckWaiter(client, grainCI, scope)
			waiter.credit = startWaitCredit(ctx)
			waiter.deadlineClamped = budget.deadlineClamped
			text, err := waiter.wait(ctx, budget.timeout)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: budget.note + text}
		},
	}
}

// grainCIWaitClause is the sentence wait_for_checks' description carries
// for a run grain CI judges, and nothing at all for every other run.
//
// It is worded as a warning about the green answer on purpose. The wait
// ends on GitHub's checks, and this tool is what a run leans on to decide
// it is finished -- so the case worth naming in the roster is the one
// where the wait comes back green and the branch still does not merge.
func grainCIWaitClause(grainCI GrainCIReader) string {
	if grainCI == nil {
		return ""
	}
	return " The report also says what grain's own CI (.grain/workflows) " +
		"makes of the same commit, which is not a GitHub check and is not " +
		"what this waits for: a wait can end green while grain CI is still " +
		"running against your branch or has already failed it, and either of " +
		"those keeps the pull request out of grain's merge queue."
}

// waitBudget is how long one call may actually block, and what its
// answer has to say about that number.
type waitBudget struct {
	// timeout is the wait to run, after every clamp.
	timeout time.Duration
	// requested is the wait the call asked for, before any of them --
	// the number the notes quote back, so that "not what you asked for"
	// names what was actually asked rather than an intermediate clamp.
	requested time.Duration
	// note prefixes the report when timeout is not requested, and is ""
	// when the call got exactly the wait it named.
	note string
	// deadlineClamped records that what shortened the wait was the run's
	// own wall clock rather than this tool's limits. It changes what a
	// timeout has to tell the run to do next: "call this again with a
	// larger timeout_seconds" is the wrong advice when the thing that
	// ran out is the run.
	deadlineClamped bool
	// tooLate is a call there is no room to wait on at all, answered
	// immediately by noTimeToWait rather than by blocking; remaining is
	// what was left of the run when that was decided.
	tooLate   bool
	remaining time.Duration
}

// waitForChecksBudget works out how long this call gets: what it asked
// for, clamped to what the tool allows, then clamped again to what is
// left of the run (run_deadline.go, via the ctx the handler was given).
//
// The room the run has is what is left of it plus the wait credit still
// to earn, because a wait pays for itself as it runs: every minute this
// call spends blocked is a minute credited back to the deadline it is
// being measured against, until the cap is reached (wait_credit.go). A
// run with ten minutes left and forty-five of unspent credit can afford
// a fifteen-minute wait, and clamping it to ten would be this tool
// refusing time grain has already decided to give it.
//
// It is a ceiling rather than a promise: the credit is granted by the
// daemon, report by report, as the wait actually blocks. A wait whose
// reports do not land is cut short by the deadline as it stands, from
// inside the loop (checkWaiter.wait) rather than here.
//
// A server that was told no deadline -- pkg/mcp's own tests, tests/e2e,
// a `grain mcpserver` run by hand -- clamps nothing, since "nobody said"
// is not "no time left".
func waitForChecksBudget(ctx context.Context, args map[string]any) waitBudget {
	budget := waitForChecksTimeout(args)
	deadline, ok := runDeadlineFromContext(ctx)
	if !ok {
		return budget
	}
	remaining, credit := deadline.remaining(), deadline.creditLeft()
	room := remaining + credit - waitForChecksDeadlineSlack
	switch {
	case room < minWaitForChecksTimeout:
		// Not even the shortest wait this tool will run fits. Half a
		// minute of blocking here would report nothing anybody could act
		// on and would cost the run the turn it has left.
		return waitBudget{tooLate: true, remaining: remaining, requested: budget.requested}
	case room < budget.timeout:
		return waitBudget{
			timeout:   room,
			requested: budget.requested,
			// Said out loud, and with every part of the arithmetic,
			// because a clamp a run cannot see is a clamp it reads as CI
			// being slow -- and would answer by asking for a longer wait
			// it has even less room for. The credit is named for the
			// same reason: a run told only "12m left" and given a 40m
			// wait would read one of the two numbers as a mistake.
			note:            clampNote(room, remaining, credit, budget.requested),
			deadlineClamped: true,
		}
	}
	return budget
}

// clampNote is the sentence in front of a report whose wait was cut to
// fit the run rather than to the timeout it asked for.
func clampNote(room, remaining, credit, requested time.Duration) string {
	if credit <= 0 {
		return fmt.Sprintf(
			"(Waited up to %s, not the %s asked for: %s this run has left before grain cancels "+
				"it, less %s to act on the answer.)\n\n",
			roundDuration(room), roundDuration(requested), roundDuration(remaining),
			waitForChecksDeadlineSlack)
	}
	return fmt.Sprintf(
		"(Waited up to %s, not the %s asked for: %s this run has left before grain cancels it, "+
			"plus %s of wait credit blocking here earns back, less %s to act on the answer.)\n\n",
		roundDuration(room), roundDuration(requested), roundDuration(remaining),
		roundDuration(credit), waitForChecksDeadlineSlack)
}

// waitForChecksTimeout reads the one argument, clamped, and words the
// sentence to put in front of the report when the number used is not the
// number asked for.
//
// Clamping rather than refusing: an agent that asks for two hours has
// made a judgement about its own CI, not a mistake worth failing a call
// over, and the answer it gets after an hour is still the answer. Saying
// so on the report is what keeps the clamp from silently looking like a
// CI that finished early.
func waitForChecksTimeout(args map[string]any) waitBudget {
	seconds, ok := argFloat(args, "timeout_seconds")
	if !ok {
		if _, present := args["timeout_seconds"]; present {
			return waitBudget{
				timeout:   DefaultWaitForChecksTimeout,
				requested: DefaultWaitForChecksTimeout,
				note: fmt.Sprintf(
					"(timeout_seconds was not a number, so I waited up to %s.)\n\n",
					DefaultWaitForChecksTimeout),
			}
		}
		return waitBudget{timeout: DefaultWaitForChecksTimeout, requested: DefaultWaitForChecksTimeout}
	}
	requested := time.Duration(seconds * float64(time.Second))
	switch {
	case requested > MaxWaitForChecksTimeout:
		return waitBudget{
			timeout:   MaxWaitForChecksTimeout,
			requested: requested,
			note: fmt.Sprintf(
				"(Asked for %s; %s is the longest wait allowed, so that is what I waited.)\n\n",
				roundDuration(requested), MaxWaitForChecksTimeout),
		}
	case requested < minWaitForChecksTimeout:
		return waitBudget{
			timeout:   minWaitForChecksTimeout,
			requested: requested,
			note: fmt.Sprintf(
				"(Asked for %s; %s is the shortest wait allowed, so that is what I waited.)\n\n",
				roundDuration(requested), minWaitForChecksTimeout),
		}
	}
	return waitBudget{timeout: requested, requested: requested}
}

// noTimeToWait is the whole answer to a call there is no time left to
// wait on, and it is an answer rather than an error: "there is nothing
// to wait with" is a true and actionable thing to say about CI, and the
// run needs its last turn for pushing, not for working out why a tool
// call failed.
//
// It says what to do instead, in the one sentence a run this close to
// the wall will act on. The alternative -- a thirty-second wait that
// reports "1 check still running" and then cancellation -- costs the
// same turn and leaves the run knowing nothing.
func noTimeToWait(remaining time.Duration, branch string) string {
	if remaining <= 0 {
		return fmt.Sprintf(
			"There is no time to wait on CI: this run is already past the deadline grain "+
				"cancels it at and stops at any moment. `git push origin %s` now -- nothing left only "+
				"in this sandbox survives.", branch)
	}
	return fmt.Sprintf(
		"There is no time to wait on CI: %s is left before grain cancels this run, so a wait "+
			"would be cut off mid-call. Push what works -- `git push origin %s` -- and leave "+
			"anything unfinished in a comment_on_issue note.",
		humanRemaining(remaining), branch)
}

// checkWaiter is the loop itself. now and sleep are fields rather than
// direct calls into the time package so a test can run a whole
// fifteen-minute wait instantly and deterministically -- there is no
// sleeping to be done in a test, and a test that really slept would
// either be slow or would be testing the clock.
type checkWaiter struct {
	client PullRequestReader
	scope  PullRequestScope

	// grainCI is grain's own verdict on the same commit, read once when
	// the report is written rather than on every poll: the wait is about
	// GitHub's checks, and what this adds is a paragraph on the answer
	// (grain_ci.go). nil is silence, which is every run grain CI does not
	// judge.
	grainCI GrainCIReader

	poll  time.Duration
	grace time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	// credit reports how long this call has now blocked, so the run's
	// deadline can move with it, and answers with what is left of the run
	// once it has (wait_credit.go). The false half is a server that was
	// told no deadline, whose waits are bounded by their own timeout and
	// nothing else. A field so a test can drive both without a daemon.
	credit func(ctx context.Context, blocked time.Duration) (time.Duration, bool)

	// deadlineClamped is why the timeout is what it is, carried this far
	// only so that a wait which ran out of time can tell the run whether
	// waiting longer is even an option (waitBudget.deadlineClamped).
	deadlineClamped bool
}

func newCheckWaiter(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) checkWaiter {
	return checkWaiter{
		client:  client,
		grainCI: grainCI,
		scope:   scope,
		poll:    checkPollInterval,
		grace:   firstCheckGrace,
		now:     time.Now,
		sleep:   sleepUntilCancelled,
	}
}

// creditBlocked is credit with the nil case answered: a waiter that was
// never given a startWaitCredit -- pkg/mcp's own tests -- credits
// nothing and is bounded by its timeout alone, which is what a server
// with no deadline announced does anyway.
func (w checkWaiter) creditBlocked(ctx context.Context, blocked time.Duration) (time.Duration, bool) {
	if w.credit == nil {
		return 0, false
	}
	return w.credit(ctx, blocked)
}

// sleepUntilCancelled waits out d, or returns early with ctx's error if
// the call is cancelled first. Cancellation has to reach in here: this
// is the only place a wait_for_checks call spends any time at all, so a
// cancelled run whose sleep ignored ctx would sit here for the rest of
// its timeout after everything else had been torn down.
func sleepUntilCancelled(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// wait blocks until CI on the branch's tip commit has a verdict, timeout
// elapses, or ctx is cancelled, and renders whichever of those happened.
//
// The returned error is for the cases where the tool could not do its
// job at all: an unreadable branch, an unreadable CI, a cancelled run.
// A build that failed, or one that never finished, is a perfectly good
// answer to the question asked and comes back as text.
func (w checkWaiter) wait(ctx context.Context, timeout time.Duration) (string, error) {
	// Read once, up front, and for the same reason pullRequestStatus
	// does: "you have not pushed anything yet" is a different answer
	// from anything CI could say, and blocking for fifteen minutes on a
	// branch that does not exist would be a particularly slow way to
	// not say it.
	head, err := w.client.GetBranchHead(w.scope.Owner, w.scope.Repo, w.scope.Branch)
	if err != nil {
		return "", fmt.Errorf("reading %s/%s's %s branch: %w", w.scope.Owner, w.scope.Repo, w.scope.Branch, err)
	}
	if head == nil {
		return fmt.Sprintf(
			"Branch %s does not exist on %s/%s yet, so there is no CI to wait for. "+
				"Commit and `git push origin %s`, then call this again.",
			w.scope.Branch, w.scope.Owner, w.scope.Repo, w.scope.Branch), nil
	}
	sha := shortSHA(head.SHA)

	started := w.now()
	// However this wait ends, the time it spent blocked is credited back
	// to the run (wait_credit.go). The crediting inside the loop below is
	// what keeps a wait alive while it runs, and only ever covers whole
	// polls; this covers the last stretch of one -- including the whole
	// of a wait that ended on its first read, which is worth nothing and
	// is reported as the nothing it is.
	defer func() { w.creditBlocked(ctx, w.now().Sub(started)) }()

	deadline := started.Add(timeout)
	var (
		seen      []github.CheckRun
		everSeen  bool
		failures  int
		lastError error
	)
	for {
		checks, err := checkRunsForCommit(w.client, w.scope, head.SHA)
		if err != nil {
			failures++
			lastError = err
			if failures >= checkReadAttempts {
				return "", fmt.Errorf("gave up after %d consecutive failed reads of CI for %s: %w",
					failures, sha, err)
			}
		} else {
			failures, lastError = 0, nil
			seen = checks
			everSeen = everSeen || len(checks) > 0
			tally := tallyChecks(checks)
			switch {
			case tally.failing > 0:
				// The logs are read here and nowhere else in the loop.
				// Until something has failed there is no log to fetch, and
				// a wait that went looking every fifteen seconds would
				// spend a full-length wait reading a lot of nothing
				// (failing_job_logs.go).
				return w.report(ctx, head.SHA, w.now().Sub(started), tally, waitVerdictFailed) +
					failingJobLogs(ctx, w.client, w.scope, head.SHA, checks), nil
			case len(checks) > 0 && tally.pending == 0:
				return w.report(ctx, head.SHA, w.now().Sub(started), tally, waitVerdictPassed), nil
			case !everSeen && w.now().Sub(started) >= w.grace:
				return w.report(ctx, head.SHA, w.now().Sub(started), tally, waitVerdictNoChecks), nil
			}
		}

		// The blocked time this call has run up so far, credited back to
		// the run's deadline before that deadline is consulted -- the
		// whole point of doing it here rather than when the wait returns
		// (wait_credit.go). What comes back is what is left of the run
		// with the credit already applied, so a wait that is paying for
		// itself sees that number stand still while it runs.
		//
		// It is only a floor on the wait, never an extension: `timeout`
		// still ends it. What this can do is end it early, when the
		// credit has run out (or was never granted, a report at a time)
		// and the run itself is about to be cancelled -- which is a
		// different thing to tell the agent than a slow CI, and is what
		// deadlineClamped makes the report say.
		if left, ok := w.creditBlocked(ctx, w.now().Sub(started)); ok && left <= waitForChecksDeadlineSlack {
			w.deadlineClamped = true
			verdict := waitVerdictTimedOut
			if !everSeen {
				verdict = waitVerdictNoChecks
			}
			return w.report(ctx, head.SHA, w.now().Sub(started), tallyChecks(seen), verdict), nil
		}

		remaining := deadline.Sub(w.now())
		if remaining <= 0 {
			verdict := waitVerdictTimedOut
			if !everSeen {
				verdict = waitVerdictNoChecks
			}
			text := w.report(ctx, head.SHA, w.now().Sub(started), tallyChecks(seen), verdict)
			if lastError != nil {
				text += fmt.Sprintf("\n\nThe last read of CI also failed, which may be why "+
					"nothing here is newer: %v", lastError)
			}
			return text, nil
		}
		// The last nap is only as long as there is left: sleeping a whole
		// interval past the deadline would report a verdict up to a poll
		// interval staler than the one that was asked for.
		nap := w.poll
		if remaining < nap {
			nap = remaining
		}
		if err := w.sleep(ctx, nap); err != nil {
			return "", fmt.Errorf("the wait for CI on %s was cancelled after %s: %w",
				sha, roundDuration(w.now().Sub(started)), err)
		}
	}
}

// The four ways a wait ends, each with its own closing paragraph: what
// happened, and what the agent should do next.
type waitVerdict int

const (
	waitVerdictFailed waitVerdict = iota
	waitVerdictPassed
	waitVerdictTimedOut
	waitVerdictNoChecks
)

// The phrases a report is built from, and the phrases ReadCheckWait
// matches it by. They are constants rather than literals inside the
// format strings below for one reason: what reads this report afterwards
// is not only an agent -- orchestrator records which verdict each wait
// ended in (model.RunCheckWait), and a reworded report that left a reader
// matching the old words would quietly measure a deployment's whole CI
// loop as never having happened. Sharing the phrase is what makes that
// impossible; telemetry.go's own tests walk every verdict through both
// halves.
const (
	waitHeaderPrefix   = "Waited "
	waitHeaderSuffix   = " for CI on"
	waitFailedMarker   = "CI has failed, so I stopped waiting"
	waitPassedMarker   = "finished and none of them failed"
	waitTimedOutMarker = "The wait timed out with"
	waitNoChecksMarker = "still reports no checks at all against"
)

// report renders one whole answer: how long it waited, what every check
// was doing when it stopped waiting, what that means -- and what grain's
// own CI makes of the same commit, which none of the check lines can say
// (grain_ci.go).
//
// The grain CI paragraph goes on every verdict, the green one most of
// all: "every check finished and none of them failed" is exactly the
// answer a run finishes on, and a branch grain CI is still running
// against or has already failed is one that does not merge on it.
//
// It is appended rather than woven in, and says none of the four
// verdicts' marker phrases, so the report is still read back as the
// verdict it is (telemetry.go's ReadCheckWait, which is what records a
// deployment's whole CI loop).
func (w checkWaiter) report(ctx context.Context, headSHA string, waited time.Duration,
	tally checkTally, verdict waitVerdict) string {

	return w.reportBody(shortSHA(headSHA), waited, tally, verdict) +
		grainCIReport(ctx, w.grainCI, headSHA, w.scope.Branch)
}

// reportBody is that answer as far as GitHub's checks take it.
func (w checkWaiter) reportBody(sha string, waited time.Duration, tally checkTally, verdict waitVerdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s %s at %s.\n\n",
		waitHeaderPrefix, roundDuration(waited), waitHeaderSuffix, w.scope.Branch, sha)

	if verdict == waitVerdictNoChecks {
		fmt.Fprintf(&b, "GitHub "+waitNoChecksMarker+" %s after %s: either this repo has no CI, "+
			"or its checks only register once a pull request is open. Either way "+
			"nobody looked, which is not a pass.", sha, roundDuration(waited))
		return b.String()
	}

	fmt.Fprintf(&b, "Checks against %s:\n%s", sha, tally.lines)
	fmt.Fprintf(&b, "\n%s\n\n", tally.counts())

	switch verdict {
	case waitVerdictFailed:
		fmt.Fprint(&b, waitFailedMarker)
		if tally.pending > 0 {
			fmt.Fprintf(&b, " (%d unfinished check(s) still running, which may fail too)", tally.pending)
		}
		fmt.Fprintf(&b, ". Fix them, commit, `git push origin %s`, and call this again for the "+
			"verdict on the fix. "+baseComparisonPointer, w.scope.Branch)
	case waitVerdictPassed:
		fmt.Fprintf(&b, "Every check against %s "+waitPassedMarker+". Green is not the same as "+
			"mergeable: call pull_request_status if you also need to know the branch still "+
			"merges into its base.", sha)
		if tally.cancelled > 0 {
			// The wait still ends here, and still ends as a pass: a
			// cancelled check will report nothing more however long this
			// blocks, so there is nothing left to wait for, and nothing
			// failed. What the marker sentence above cannot say is that
			// the pull request will not merge on it -- which is the whole
			// of cancelledHold, and the reason a wait that ends this way
			// is not a wait a run should finish on.
			//
			// Left as waitVerdictPassed deliberately: the verdict words
			// are what orchestrator records a run's CI loop by
			// (model.RunCheckWait, telemetry.go), and a fifth verdict is
			// a change to that record rather than to this sentence.
			b.WriteString("\n\n" + cancelledHold(tally.cancelled, w.scope.Branch))
		}
	case waitVerdictTimedOut:
		fmt.Fprintf(&b, waitTimedOutMarker+" %d check(s) still running: no verdict yet, and an "+
			"unfinished check has not passed. ", tally.pending)
		if w.deadlineClamped {
			fmt.Fprintf(&b, "What ran out is this run's own clock, not a timeout you can raise: get "+
				"everything committed and pushed to %s, and leave the checks for whoever "+
				"reads them next.", w.scope.Branch)
		} else {
			fmt.Fprintf(&b, "Call this again to keep waiting (larger timeout_seconds if this CI "+
				"is simply slow) rather than finishing on a build you have not seen end.")
		}
	}
	return b.String()
}

// roundDuration renders a wait the way a human reads one: whole seconds,
// since nothing here is measured finely enough for a nanosecond tail to
// mean anything.
func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d.Round(time.Second)
}
