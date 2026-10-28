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

package mcp

// The tail of each failing job's own log, hung off whichever CI answer
// named that job as failing.
//
// pull_request_status and wait_for_checks both used to stop at the name:
// "FAILING go (failure)". That is enough for a run to know its build is
// red and not enough to do anything about it, so its next move was always
// to guess at what broke and try to reproduce it -- in a sandbox that is
// not the runner and may not be able to run the failing suite at all.
// github.JobLog's own doc comment is where that gap is written down
// ("a check run says only that a job called 'go' did not pass"), and
// orchestrator's requeueForRepair already closes it for the other reader of a
// red build: the fix task the merge queue files carries the end of each
// failing job's log in its body rather than a job name and an invitation
// to go and find out what it said. A run watching its own CI now gets
// the same thing, rendered the same way and bounded the same way --
// github.JobLogTailBytes over the wire, github.JobLogExcerpt on the page.
//
// Read only once something has failed, never on the way there. This is
// three extra GitHub reads (github.FailedJobLogs walks a commit's runs,
// a failed run's jobs, then each failed job's log), and there is nothing
// for them to find until a job has actually gone red: a wait_for_checks
// call that fetched logs on every fifteen-second poll would spend its
// whole budget reading nothing.
//
// Best effort, deliberately, and for the same reason failingJobLogs is
// in the orchestrator and checkRunsForCommit falls back rather than
// failing: the logs are a credential-dependent bonus on top of an answer
// that already stands up without them. A fine-grained token without
// "Actions" read, or a repo whose CI is Buildkite rather than Actions,
// has to degrade to the answer this tool gave before -- the checks, with
// the failing ones named -- rather than turn a report of a red build
// into an error about logs.
//
// The one thing it may not do is guess at *why* there is no log. It used
// to: an empty github.FailedJobLogs was reported as "the failing check
// is posted by CI that is not Actions", which measurement says is almost
// never what happened. Across 185 runs of one deployment the sentence
// was printed 78 times, and the same check in the same repo went both
// ways -- 39 answers with a log, 38 without -- which rules out the
// credential, the provider and the retention window all three. What it
// correlates with is speed: a median 16 seconds of waiting for the
// answers with no log against 521 for the ones with, and in 15 of the 29
// affected runs the very same check produced a real log later in the
// same session. The log was there. wait_for_checks returns the instant a
// check run reports completed/failure, and at that moment the run behind
// it is often still in progress, so there is no *completed failed run*
// for FailedJobLogs to walk and no downloadable log even if there were.
//
// So: ask again for a while (jobLogRetryDelays), and if the answer is
// still nothing, say which of the possible causes it actually was rather
// than listing them (noJobLogNote, github.ActionsState).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
)

// jobLogRetryDelays is how long a report will wait for a log that is
// probably on its way: five attempts, the last of them fifty seconds
// after the first.
//
// This is a trade, and it is worth naming. wait_for_checks sells itself
// on returning the moment CI fails rather than when CI finishes, and
// this spends up to a minute of that on a report that has already been
// decided. It buys the thing the call was for: a red build the agent can
// act on without leaving its sandbox, instead of a job name. A minute
// against the several turns a run otherwise spends guessing at what
// broke -- or against a whole second dispatch, which is what two of the
// measured runs cost -- is cheap, and the wait only ever happens on a
// build that is already failing.
//
// Rising delays rather than a flat interval because the recoveries
// cluster early: a run that concluded seconds ago usually has its log
// within one or two polls, and the later attempts are there for the slow
// tail rather than the common case. A var so tests can shrink it.
var jobLogRetryDelays = []time.Duration{
	5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second,
}

// minJobLogExcerptBytes is the least one failing job's log is shown in,
// however many of them there are: about twenty lines, which is a Go test
// failure's own output without the package lines around it.
const minJobLogExcerptBytes = 1 << 10

// elisionAdviceJobLog is where the rest of a cut log is, which for this
// one block is two different places for two different readers. Nothing in
// a sandbox can fetch a job log -- there is no `gh` and no credential --
// so a run is pointed at the thing it can actually do, and the page is
// named for whoever reads the report it writes.
const elisionAdviceJobLog = "the job's own page, linked just above, has the whole " +
	"log for whoever reads your report; from the sandbox, reproduce the failure in " +
	"your checkout rather than asking for it again"

// failingJobLogs renders what the failed jobs against sha printed, as a
// block to append to a report that has already named them. It always
// returns something to append: either the logs, or an explanation of why
// there are none, since a reader who has just been told a job failed and
// shown nothing under it cannot tell "no log" from "this tool forgot".
//
// checks is the same check list the report above it was rendered from,
// carried in rather than re-read: when the answer turns out to be "this
// failure is not Actions at all", the failing checks' own pages are the
// only place left pointing at what broke.
func failingJobLogs(ctx context.Context, client PullRequestReader, scope PullRequestScope, sha string, checks []github.CheckRun, attributions ...map[string]ci.JobAttribution) string {
	logs, waited, err := readJobLogs(ctx, client, scope, sha)
	if err != nil {
		return "\n\n" + logsUnreadable(err)
	}
	if len(logs) == 0 {
		return "\n\n" + noJobLogNote(client, scope, sha, checks, waited)
	}

	// One answer's worth of log between all of them, rather than each of
	// them bounded on its own. github.JobLogTailBytes bounds what crosses
	// the wire per job and github.JobLogTailLines what a reader is shown
	// of it, and four of those still add up to four times what a tool
	// answer carries -- so a commit that failed four jobs used to produce
	// a report the backstop would cut blind, losing the fourth job
	// entirely along with the sentence that introduced it. Sharing the
	// budget keeps every failing job's tail visible, which is the one
	// thing this block exists to show.
	perLog := max(int(maxToolResultBytes.Load())*3/4/len(logs), minJobLogExcerptBytes)

	var b strings.Builder
	if github.AnyJobLogRead(logs) {
		b.WriteString("\n\nThe end of each failing job's log:\n")
	} else {
		b.WriteString("\n\nNo failing job has a log to read, so this is the run's own verdict " +
			"rather than anything it printed:\n")
	}
	for _, l := range logs {
		if l.IsRunFailure() {
			fmt.Fprintf(&b, "\n### %s (the workflow run, not a job of it)\n", l.Name)
			if l.URL != "" {
				fmt.Fprintf(&b, "\n%s\n", l.URL)
			}
			fmt.Fprintf(&b, "\n%s\n", github.RunFailureNote(l))
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n", l.Name)
		if len(attributions) > 0 && attributions[0] != nil {
			if attr, ok := attributions[0][l.Name]; ok && attr.Summary() != "" {
				fmt.Fprintf(&b, "Attribution: %s\n\n", attr.Summary())
			}
		}
		if l.URL != "" {
			fmt.Fprintf(&b, "\n%s\n", l.URL)
		}
		if summary := ci.ExtractFailureSummary(l.Log); summary != nil && !summary.Empty() {
			fmt.Fprintf(&b, "\n%s\n", summary.Render())
		}
		// Four backticks, so a log that itself contains a fenced block --
		// any Go test printing three backticks does -- cannot close this
		// one early. requeueForRepair fences the same way, for the same reason.
		fmt.Fprintf(&b, "\n````\n%s\n````\n",
			elideMiddle(github.JobLogExcerpt(l.Log), perLog, elisionAdviceJobLog))
		if l.Truncated {
			b.WriteString("\n(the tail only -- the job's page above has the whole log)\n")
		}
	}
	// A run reported as itself names its workflow and nothing under it,
	// which leaves the reader without even a job name. The steps come
	// off the same jobs endpoint the run failure was read through and
	// need no log at all, so they are worth the one extra read here.
	if !github.AnyJobLogRead(logs) {
		b.WriteString(failedStepsNote(client, scope, sha))
	}
	return strings.TrimRight(b.String(), "\n")
}

// readJobLogs fetches the failing jobs' logs, asking again over
// jobLogRetryDelays for as long as the empty answer looks like one that
// is about to stop being empty (logsMayStillArrive). It returns how long
// it spent doing that, so the report can say so rather than leaving a
// reader wondering why an answer took a minute.
//
// Cancellation ends the retrying rather than the answer: a run being
// torn down should still get whatever the last read produced.
func readJobLogs(ctx context.Context, client PullRequestReader, scope PullRequestScope, sha string) ([]github.JobLog, time.Duration, error) {
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		logs, err := client.FailedJobLogs(scope.Owner, scope.Repo, sha)
		if err != nil {
			return nil, waited, err
		}
		if attempt >= len(jobLogRetryDelays) || !logsMayStillArrive(logs) {
			return logs, waited, nil
		}
		delay := jobLogRetryDelays[attempt]
		if err := sleepUntilCancelled(ctx, delay); err != nil {
			return logs, waited, nil
		}
		waited += delay
	}
}

// logsMayStillArrive reports whether a fetch that produced no readable
// job log is one worth asking again for.
//
// Two shapes are, and they are the two the race produces. Nothing at all
// means GitHub has no completed failed run against the commit -- which
// is what a run still in progress looks like from FailedJobLogs, and
// what every measured occurrence of this was. A run reported as itself
// with jobs that failed under it means the jobs are there and no log
// was served for any of them, which is the same race one step later: the
// run has concluded but the logs endpoint has not caught up.
//
// A run no job of which ever ran a step is not worth waiting for -- no
// job ran, so no job log is ever coming -- and neither is an answer that
// already has a log in it. That covers both shapes of it: a run concluded
// `startup_failure` with no jobs at all, and a run concluded `failure`
// whose jobs GitHub created and then declined to start
// (github.JobLog.NoJobStarted). The second one has failing jobs under it
// and would otherwise be waited on for the full minute, every time, for a
// log that does not exist.
func logsMayStillArrive(logs []github.JobLog) bool {
	if len(logs) == 0 {
		return true
	}
	for _, l := range logs {
		if !l.IsRunFailure() {
			return false
		}
		if l.RunJobsFailed > 0 && !l.NoJobStarted() {
			return true
		}
	}
	return false
}

// noJobLogNote is what stands where the logs would be when there are
// none: which of the reasons for that it actually was, and whatever
// GitHub will say about the failure without a log.
//
// Everything here is a fact read back from GitHub rather than a
// possibility offered to the reader. The three-way "not Actions, or
// expired, or a credential" sentence this replaced was wrong nearly
// every time it was printed, and a reader who cannot tell which of three
// stories they are in cannot act on any of them.
func noJobLogNote(client PullRequestReader, scope PullRequestScope, sha string, checks []github.CheckRun, waited time.Duration) string {
	state, err := client.ActionsState(scope.Owner, scope.Repo, sha)
	if err != nil {
		return fmt.Sprintf("No failing GitHub Actions job could be read for this commit, and the "+
			"read that would have said why failed too: the check names above are all there "+
			"is. GitHub said: %v", err)
	}

	var b strings.Builder
	if state.Runs == 0 {
		b.WriteString("GitHub Actions has no workflow run against this commit, so there is no " +
			"job log to fetch: the failing check above comes through the Checks API from " +
			"something else -- Buildkite, CircleCI, a review bot -- which keeps its own output.")
		b.WriteString(failingCheckPages(checks))
		return b.String()
	}

	if len(state.Jobs) == 0 {
		if state.Unfinished > 0 {
			fmt.Fprintf(&b, "GitHub Actions has %s against this commit, %d of them %s still "+
				"running, and no job has failed yet%s: the check above is ahead of the "+
				"Actions API, which is ordinary. Call this again shortly -- the job, its "+
				"failed step and its log turn up in that order, usually within a minute.",
				runCount(state.Runs), state.Unfinished, plural(state.Unfinished, "is", "are"),
				afterWaiting(waited))
			return b.String()
		}
		fmt.Fprintf(&b, "GitHub Actions has %s against this commit, all finished, with no failed "+
			"job among them%s -- so the failing check above is not from Actions and has no "+
			"job log behind it. Its own page has its output.",
			runCount(state.Runs), afterWaiting(waited))
		b.WriteString(failingCheckPages(checks))
		return b.String()
	}

	fmt.Fprintf(&b, "GitHub Actions served no log for this failure%s, but it names the job that "+
		"broke and the step that did it:\n", afterWaiting(waited))
	b.WriteString(github.FailedJobsNote(state.Jobs))
	if state.Unfinished > 0 {
		fmt.Fprintf(&b, "\n%d of this commit's runs %s still going, which is why there is no log "+
			"yet -- Actions serves one only once the run has finished. Call this again in a "+
			"minute or two.", state.Unfinished, plural(state.Unfinished, "is", "are"))
	} else {
		b.WriteString("\nThose runs have all finished and their logs are still not served: a log " +
			"past Actions' 90-day retention, or a credential that may not fetch one. The " +
			"failed steps above and the job pages are what there is.")
	}
	return b.String()
}

// failedStepsNote is the same job-and-step detail, appended to a report
// that did render something (a workflow run standing in for jobs whose
// logs were not served). Silent when there is nothing to add: this hangs
// off an answer that already stands on its own.
func failedStepsNote(client PullRequestReader, scope PullRequestScope, sha string) string {
	state, err := client.ActionsState(scope.Owner, scope.Repo, sha)
	if err != nil || len(state.Jobs) == 0 {
		return ""
	}
	return "\nWhich jobs those runs failed, and at which step:\n" + github.FailedJobsNote(state.Jobs)
}

// failingCheckPages points at the failing checks' own pages, for the two
// answers where the failure is not Actions and there is nothing else
// left to offer. Written for whoever reads the report rather than for
// the run itself: a sandbox has no `gh` and no GitHub credential, so an
// agent that goes looking for these pages from inside one only wastes
// turns discovering that.
func failingCheckPages(checks []github.CheckRun) string {
	var b strings.Builder
	for _, c := range checks {
		if checkFailed(c) && c.HTMLURL != "" {
			fmt.Fprintf(&b, "\n- %s: %s", c.Name, c.HTMLURL)
		}
	}
	if b.Len() == 0 {
		return " Nothing in this sandbox can read that provider: say what CI reported and that " +
			"you could not reach its output, rather than pushing at it blind."
	}
	return "\n" + b.String() +
		"\n\nThose pages are for whoever reads this report -- the sandbox has no `gh` and no " +
		"GitHub credential. If the check names do not say what broke, say so rather than " +
		"pushing at it blind."
}

// runCount and plural keep the sentences above reading like English for
// the one-run case, which is the commonest one there is.
func runCount(n int) string {
	if n == 1 {
		return "1 workflow run"
	}
	return fmt.Sprintf("%d workflow runs", n)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// afterWaiting names the retrying, so a reader is not left thinking the
// report gave up on the first ask.
func afterWaiting(waited time.Duration) string {
	// Rounded to whole seconds like every other duration in these
	// answers, so anything under one has nothing to say.
	if waited < time.Second {
		return ""
	}
	return fmt.Sprintf(" (asked again over %s)", roundDuration(waited))
}

// logsUnreadable is the sentence that replaces the logs when GitHub would
// not give them up. A refused read is named as a property of this
// deployment's credential rather than of the build, so a run does not
// read it as "the failure has no output" and go looking for one.
func logsUnreadable(err error) string {
	if github.IsPermissionDenied(err) {
		return "This grain deployment's GitHub credential may not read Actions job logs, so the " +
			"check names above are all there is until an operator grants it. Reproduce the " +
			"failure in your checkout instead."
	}
	return fmt.Sprintf(
		"I could not read the failing jobs' logs, so the check names above are all there "+
			"is; reproduce the failure in your checkout instead. GitHub said: %v", err)
}
