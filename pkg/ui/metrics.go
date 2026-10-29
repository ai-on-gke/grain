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

package ui

// GET /api/metrics: what this deployment delivered over a window, and
// where a task's wall-clock time went getting there. pkg/metrics computes
// it; this is the wire shape and the one place a window string is parsed.
//
// The JSON types below are this package's own rather than pkg/metrics'
// types with tags on them, the same choice SandboxSnapshot makes over
// orchestrator.SandboxHealth: a Distribution is a set of time.Durations,
// which marshal as bare nanosecond counts that no consumer can read
// without knowing that, and an API is worth stating in units it says out
// loud. Everything crossing this boundary is seconds, named as such.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultMetricsWindow is how far back a report reaches when the caller
// does not say. A week covers a deployment's own weekly rhythm without
// making the first number anybody sees an average over a quarter.
const DefaultMetricsWindow = 7 * 24 * time.Hour

// settingsHistorySlack is how far before a window's own start the settings
// history is read. A change made shortly before a window is exactly what a
// series inside it is most often explained by -- an operator turns a dial
// and then goes looking at the next day's numbers -- and reading a little
// further back is also what lets the section say how far the history
// reaches at all rather than only that the window held no changes.
const settingsHistorySlack = 14 * 24 * time.Hour

// maxMetricsWindow bounds ?window=. Not a cost limit -- the read is the
// whole table either way (Store.TaskTimings) -- but a sanity one: a
// window measured in centuries says the caller meant something else.
const maxMetricsWindow = 365 * 24 * time.Hour

// ParseMetricsWindow reads a window string: "" for the default, a Go
// duration ("36h", "90m"), or a count of days or weeks ("7d", "2w"),
// which time.ParseDuration itself has no unit for and which is how anyone
// actually asks for a window this long.
func ParseMetricsWindow(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return DefaultMetricsWindow, nil
	}
	var window time.Duration
	switch unit := text[len(text)-1]; unit {
	case 'd', 'w':
		unitName, per := "days", 24*time.Hour
		if unit == 'w' {
			unitName, per = "weeks", 7*24*time.Hour
		}
		n, err := strconv.Atoi(text[:len(text)-1])
		if err != nil {
			return 0, fmt.Errorf("window %q: expected a whole number of %s, like 7%c", text, unitName, unit)
		}
		window = time.Duration(n) * per
	default:
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return 0, fmt.Errorf("window %q: expected a duration like 36h, or a count of days or weeks like 7d", text)
		}
		window = parsed
	}
	if window <= 0 {
		return 0, fmt.Errorf("window %q: must be positive", text)
	}
	if window > maxMetricsWindow {
		return 0, fmt.Errorf("window %q: at most %s", text, maxMetricsWindow)
	}
	return window, nil
}

// MetricsScope is which tasks a report is about, when it is about some
// of them: the repos their work lands in and who they are attributed to
// (metrics.Scope, grain/task-83). Empty in both fields -- the usual case
// -- is every task, which is what a report has always been.
//
// It is on the wire in both directions. As ?repo= and ?author= it is the
// request; echoed back here it is what the report actually covered, so a
// page drawing one can head it with the question it answers rather than
// with the question its reader thinks they asked.
type MetricsScope struct {
	Repos   []string `json:"repos,omitempty"`
	Authors []string `json:"authors,omitempty"`
}

// MetricsReport is GET /api/metrics' whole body.
type MetricsReport struct {
	Since         time.Time         `json:"since"`
	Until         time.Time         `json:"until"`
	WindowSeconds float64           `json:"windowSeconds"`
	Scope         MetricsScope      `json:"scope"`
	Throughput    MetricsThroughput `json:"throughput"`
	// Latency is a list rather than an object so it arrives in the order
	// a task passes through the stages, which is the order anybody reads
	// them in and the order the CLI prints them.
	Latency []MetricsStage `json:"latency"`
	Runs    MetricsRuns    `json:"runs"`
	Backlog MetricsBacklog `json:"backlog"`
	// Cycles sits beside Runs because it answers the question Runs
	// raises. Runs.Utilization says whether the deployment was full;
	// this says what the tick itself cost regardless -- the two together
	// are what tell a queue_wait spent waiting for capacity from one
	// spent waiting for the scheduler.
	Cycles MetricsCycles `json:"cycles"`
	// GitHub is what this deployment has spent against GitHub's rate
	// limit. Beside Cycles because it answers the other half of the same
	// question -- Cycles is how long a tick took, this is what the tick
	// cost somebody else.
	GitHub MetricsGitHub `json:"github"`
	// Tools and Checks are the inside of a run: what its tools cost it,
	// and how the CI loop every prompt sends it round actually went. Both
	// are empty (n = 0 everywhere) on a deployment whose runs all predate
	// the census, which is a different thing from a deployment whose runs
	// called nothing -- runs says which.
	Tools  MetricsTools  `json:"tools"`
	Checks MetricsChecks `json:"checks"`
	// Prompts is the other side of Tools: what grain spent of a run's
	// context before its first turn rather than what the run spent
	// itself. Empty on a deployment whose runs recorded no prompt.
	Prompts MetricsPrompts `json:"prompts"`
	// PullRequests is the end of that same loop: whether runs open their
	// own pull request while they can still fix what its checks say.
	PullRequests MetricsPullRequests `json:"pullRequests"`
	// CIQueueing is the other side of Checks: how much of the job-time
	// those checks took went on waiting for a machine rather than on
	// running the tree, what held it, and which repositories and jobs
	// wore it (metrics.CIQueueing, grain/task-345).
	//
	// Narrowed by scope.repos like everything else, and *not* by
	// scope.authors: a CI job belongs to a repository and a branch, not
	// to whoever filed a task, so an author-scoped report shows this
	// deployment's whole CI beside that author's tasks. The section says
	// so on the page rather than only here.
	CIQueueing MetricsCIQueueing `json:"ciQueueing"`
	// GitHubCI is the CI beside grain's own -- GitHub Actions jobs and
	// other providers' check runs, every number labelled source "github"
	// -- read from github_ci_job alone, so ciQueueing, checks and merges
	// are the same with or without it (metrics.GitHubCI). Narrowed by
	// scope.repos and not scope.authors, for ciQueueing's reason.
	GitHubCI MetricsGitHubCI `json:"githubCI"`
	// Merges is the far end of the same loop: how much this deployment
	// landed over the window, how long a change took from being filed to
	// being merged, and what the merge queue's batching cost and saved
	// (metrics.Merges, grain/task-556).
	//
	// throughput counts tasks completed, which is an agent stopping;
	// this counts changes merged, which is what somebody waiting on one
	// was waiting for. Its batch half is narrowed by scope.repos and not
	// by scope.authors, for the reason ciQueueing's is: a batch belongs
	// to a repository and a base branch and records no members.
	Merges MetricsMerges `json:"merges"`
	// Changes is when this deployment's own parameters moved during the
	// window, read off the state repository's git history
	// (metrics.Changes, grain/task-495). It is the section that makes
	// every series above answer a tuning question rather than only a
	// diagnostic one: its buckets are the same slices, so a run-time
	// series that steps down halfway across the window can be read
	// against the change that was made halfway across the window.
	//
	// Not narrowed by scope, for Cycles' reason: a setting belongs to the
	// deployment rather than to any repository's tasks.
	Changes MetricsChanges `json:"changes"`
	// Host is the machine underneath all of it, over time
	// (metrics.Host): the series a job that took twice as long has to be
	// read against, since the job did not get slower, it got less of a
	// machine. Sliced the same way as everything else here.
	//
	// The Sandbox health pane's own host reading is the same numbers taken
	// once, now; this is the series, and it reaches back as far as the
	// daemon has been running rather than as far as the window asks.
	Host MetricsHost `json:"host"`
	// Sandboxes is what this deployment's machines have cost it: how
	// many sandboxes it has built, agent and CI, and how many
	// sandbox-hours they came to (sandboxusage.go, grain/task-315).
	//
	// Since it started counting, not over the report's window, for
	// MetricsGitHub's reason and stated the same way: the counts are an
	// aggregate rather than rows, so there is nothing to slice by time,
	// and the section carries its own "since" date. Nor is it narrowed
	// by Scope -- a sandbox is not attributed to a repository here, and
	// a per-repository breakdown is the next step rather than a thing
	// this can fake. nil where there is no store to read (a report
	// computed by something with no deployment behind it).
	Sandboxes *SandboxLifetime `json:"sandboxes,omitempty"`
	Agents    MetricsAgents    `json:"agents"`
	// Failed names each section of a whole report that could not be
	// built because something it is computed from could not be read. Such
	// a section is empty above, and is to be read as unavailable rather
	// than as a quiet window; every other section is whole. Absent when
	// nothing failed.
	Failed []MetricsSectionFailure `json:"failed,omitempty"`
}

// MetricsAgents is the wire representation of metrics.Agents.
type MetricsAgents struct {
	Templates    []MetricsAgentTemplate  `json:"templates,omitempty"`
	ReportChecks MetricsAgentReportCheck `json:"reportChecks"`
}

// MetricsAgentTemplate is the wire representation of metrics.AgentTemplate.
type MetricsAgentTemplate struct {
	TemplateID string                  `json:"templateId"`
	Wakes      int                     `json:"wakes"`
	Calls      int                     `json:"calls"`
	Bytes      int64                   `json:"bytes"`
	CallsP50   int                     `json:"callsP50"`
	CallsP95   int                     `json:"callsP95"`
	BytesP50   int64                   `json:"bytesP50"`
	BytesP95   int64                   `json:"bytesP95"`
	Elisions   int                     `json:"elisions"`
	ReReads    int                     `json:"reReads"`
	Report     MetricsAgentReportCheck `json:"report"`
}

// MetricsAgentReportCheck is the wire representation of metrics.AgentReportCheck.
type MetricsAgentReportCheck struct {
	Wakes        int     `json:"wakes"`
	Claims       int     `json:"claims"`
	Uncited      int     `json:"uncited"`
	Unread       int     `json:"unread"`
	Mismatched   int     `json:"mismatched"`
	UncitedRate  float64 `json:"uncitedRate"`
	UnreadRate   float64 `json:"unreadRate"`
	MismatchRate float64 `json:"mismatchRate"`
}

// MetricsSectionFailure is one section a report could not build. Error
// is what the server is willing to say about it, which over the wire is
// the same sanitized sentence and log reference (Ref) a failed request
// gets (apierror.Body): the cause itself is in the server's log under
// that reference.
type MetricsSectionFailure struct {
	Section string `json:"section"`
	Error   string `json:"error"`
	Ref     string `json:"ref,omitempty"`

	err error
}

// MetricsPullRequests is the mid-run pull request loop: how many of the
// runs that were offered open_pull_request called it, and whether calling
// it went with fewer red builds left behind.
//
// runs is the denominator and is worth reading first: it counts only runs
// that recorded a census and whose task had a repo to push to, since a
// task with no target never got the paragraph that names the tool.
//
// withTool and withoutTool are a comparison and only a comparison. Each
// counts the runs whose task the merge queue later filed a fix task for,
// and that link is on the task rather than the attempt -- so read the
// difference between the two rates, not either rate on its own.
type MetricsPullRequests struct {
	Runs   int `json:"runs"`
	Opened int `json:"opened"`
	Calls  int `json:"calls"`
	// AdoptionRate is opened/runs -- a rate, 0..1. It measures the tool's
	// uptake overall and not prompt wording against a bare tool
	// description: the tool and the prompt sentence naming it landed
	// half an hour apart, so no run ever had one without the other.
	AdoptionRate float64            `json:"adoptionRate"`
	WithTool     MetricsFixTaskRate `json:"withTool"`
	WithoutTool  MetricsFixTaskRate `json:"withoutTool"`
}

// MetricsFixTaskRate is one side of that comparison: of these runs, how
// many belonged to a task the merge queue had to file a fix task for.
type MetricsFixTaskRate struct {
	Runs     int     `json:"runs"`
	FixTasks int     `json:"fixTasks"`
	Rate     float64 `json:"rate"`
}

// MetricsTools is the per-run tool census over the window: how much of a
// run goes on tool calls, how many of them come back as errors, and which
// tool the errors are in.
type MetricsTools struct {
	// Runs is how many of the window's finished runs recorded a census at
	// all, and every rate here is over those runs rather than over
	// throughput.runsFinished.
	Runs    int `json:"runs"`
	Calls   int `json:"calls"`
	Errored int `json:"errored"`
	// ErroredShare is errored/calls across every tool -- a rate, 0..1.
	ErroredShare float64 `json:"erroredShare"`
	// Cut is how many calls came back with an answer grain had shortened,
	// and CutShare is that over calls -- whether the result cap fires at
	// all, which the byte distributions below cannot say once they are
	// themselves bounded by it.
	Cut         int          `json:"cut"`
	CutShare    float64      `json:"cutShare"`
	CallsPerRun MetricsCount `json:"callsPerRun"`
	// ControlCalls and CLICalls split calls by surface: the MCP control
	// roster, and `grain` commands the sandbox proxy served
	// (metrics.Tools).
	ControlCalls int `json:"controlCalls"`
	CLICalls     int `json:"cliCalls"`
	// ByTool is busiest first, which is the order that matters: an error
	// rate over four calls is not an error rate.
	ByTool []MetricsToolUse `json:"byTool"`
}

// MetricsPrompts is how big the prompts this deployment handed its runs
// were, over the window: the distribution, how many crossed the size
// grain warns about, how many had a conversation cut out of them to fit,
// and the few tasks carrying the largest.
//
// runs is the denominator and the first thing to read -- a run that
// never reached its agent recorded no prompt and is in none of these
// numbers. threadCut is the second: the conversation a prompt carries is
// bounded, so the byte distribution is censored at that bound the way a
// tool's result sizes are censored by the result cap, and only the count
// of cut threads says whether the bound is biting. See metrics.Prompts.
type MetricsPrompts struct {
	Runs int `json:"runs"`
	// Bytes is the size distribution. mean and max are exact; the
	// percentiles are bucket bounds, like every other MetricsSize here.
	Bytes MetricsSize `json:"bytes"`
	// Large is how many prompts crossed model.PromptWarnTokens -- a
	// quarter of the context window grain measures against, which is
	// where the daemon logs condition=prompt_size -- and largeShare is
	// that over runs.
	Large      int     `json:"large"`
	LargeShare float64 `json:"largeShare"`
	// WarnTokens and ContextTokens are the two numbers that test against,
	// carried on the wire so that a page drawing this share does not have
	// to hold its own copy of an assumption grain may revise
	// (model.PromptContextTokens says why it travels with the
	// measurement).
	WarnTokens     int     `json:"warnTokens"`
	ContextTokens  int     `json:"contextTokens"`
	ThreadCut      int     `json:"threadCut"`
	ThreadCutShare float64 `json:"threadCutShare"`
	// Heaviest is the few tasks with the largest prompts, largest first
	// -- where to look, which a distribution on its own cannot say.
	Heaviest []MetricsTaskPrompts `json:"heaviest"`
}

// MetricsTaskPrompts is one task's prompts: how many of its attempts in
// the window were measured, the largest prompt any of them got and which
// attempt that was, and how many of them had a conversation cut out.
type MetricsTaskPrompts struct {
	TaskID    string `json:"taskId"`
	Runs      int    `json:"runs"`
	MaxBytes  int64  `json:"maxBytes"`
	MaxTokens int    `json:"maxTokens"`
	Attempt   int    `json:"attempt"`
	ThreadCut int    `json:"threadCut"`
}

// MetricsToolUse is one tool's numbers. errorRate and timeoutRate are
// fractions of that tool's own calls; timeouts are only ever reported for
// a tool that bounds its own work and says so (run_command), and are zero
// elsewhere because nothing else can say, not because nothing else ran
// long.
type MetricsToolUse struct {
	Name string `json:"name"`
	// Surface is "control" (MCP control roster), "cli" (a `grain`
	// command) or empty for a name grain does not publish.
	Surface     string      `json:"surface,omitempty"`
	Runs        int         `json:"runs"`
	Calls       int         `json:"calls"`
	Errored     int         `json:"errored"`
	ErrorRate   float64     `json:"errorRate"`
	TimedOut    int         `json:"timedOut"`
	TimeoutRate float64     `json:"timeoutRate"`
	ResultBytes MetricsSize `json:"resultBytes"`
	Cut         MetricsCut  `json:"cut"`
}

// MetricsCut is how often grain shortened one tool's answers and how much
// it dropped when it did -- the companion to resultBytes, and the only
// half of the pair that still means anything once a cap bounds the other.
// See metrics.Cuts.
type MetricsCut struct {
	Calls int     `json:"calls"`
	Rate  float64 `json:"rate"`
	// N is how many of those cuts said a size that could be read back,
	// and is what the two percentiles below are over.
	N                int   `json:"n"`
	DroppedP50AtMost int64 `json:"droppedP50AtMostBytes"`
	DroppedP95AtMost int64 `json:"droppedP95AtMostBytes"`
}

// MetricsSize is how big one tool's answers were, in bytes. mean and max
// are exact; the percentiles are bounds -- the largest result the bucket
// the p'th sample fell in can hold -- which is why they are named
// atMost. See metrics.Sizes on why a bound rather than a value.
type MetricsSize struct {
	N         int   `json:"n"`
	MeanBytes int64 `json:"meanBytes"`
	MaxBytes  int64 `json:"maxBytes"`
	P50AtMost int64 `json:"p50AtMostBytes"`
	P95AtMost int64 `json:"p95AtMostBytes"`
	P99AtMost int64 `json:"p99AtMostBytes"`
}

// MetricsCount is a distribution of whole numbers -- calls in a run,
// pushes before a green build. Nearest-rank percentiles, like every other
// distribution here, so each number is one that really happened.
type MetricsCount struct {
	N     int     `json:"n"`
	Min   int64   `json:"min"`
	P50   int64   `json:"p50"`
	P90   int64   `json:"p90"`
	P99   int64   `json:"p99"`
	Max   int64   `json:"max"`
	Mean  float64 `json:"mean"`
	Total int64   `json:"total"`
}

// MetricsChecks is the CI loop measured end to end: every run is told to
// push, wait for checks, fix and push again, and this is how that
// actually goes. A deployment where most verdicts are "timed_out" has its
// wait_for_checks default set wrong for its CI; one where most are
// "no_checks" is sending runs to wait for CI that does not exist.
type MetricsChecks struct {
	Waits    int            `json:"waits"`
	Runs     int            `json:"runs"`
	Verdicts map[string]int `json:"verdicts"`
	// Blocked is how long a wait blocked for, in seconds.
	Blocked MetricsDistribution `json:"blocked"`
	// PushesToGreen is how many pushes a run had made when its first
	// passing wait returned, over the greenRuns that got a pass at all.
	PushesToGreen MetricsCount         `json:"pushesToGreen"`
	GreenRuns     int                  `json:"greenRuns"`
	Repairs       MetricsBranchRepairs `json:"repairs"`
	// RedCI is how the window's CI-fixing runs worked a red build
	// (metrics.RedCI): how many there were, and the share that ran a
	// narrowed local test before the fixing push and that re-ran CI
	// selectively.
	RedCI MetricsRedCI `json:"redCI"`
}

// MetricsRedCI is metrics.RedCI on the wire.
type MetricsRedCI struct {
	FixingRuns     int     `json:"fixingRuns"`
	Narrowed       int     `json:"narrowed"`
	NarrowedShare  float64 `json:"narrowedShare"`
	Selective      int     `json:"selective"`
	SelectiveShare float64 `json:"selectiveShare"`
}

// MetricsCIQueueing is this deployment's CI waiting over the window: the
// share of CI job-time spent queueing for a machine, what held it, and
// which repositories and jobs wore it.
//
// jobs is the denominator and the first thing to read: jobs that ended
// inside the window, cache hits excluded and counted apart, since a job
// answered from the cache never asked for a machine. queuedSeconds and
// busySeconds are job-time rather than wall clock, so a deployment
// running four jobs at once spends four minutes of it a minute and both
// can exceed the window.
type MetricsCIQueueing struct {
	Jobs     int `json:"jobs"`
	Waited   int `json:"waited"`
	NeverRan int `json:"neverRan"`
	Cached   int `json:"cached"`
	// QueuedShare is queued / (queued + busy) -- the number the section
	// exists for. A deployment short of sandboxes shows up here long
	// before anybody notices a slow check.
	QueuedSeconds float64 `json:"queuedSeconds"`
	BusySeconds   float64 `json:"busySeconds"`
	QueuedShare   float64 `json:"queuedShare"`
	// Wait is how long a job that waited waited, in seconds, over the
	// jobs that waited at all, and run how long a job that got a machine
	// spent on it. The two move in opposite directions under the same
	// pressure, which is why both are here: cut concurrency and runs get
	// faster while waits get longer, so a reader holding one of them
	// cannot tell which way to turn the dial (metrics.CIQueueing.Run).
	Wait MetricsDistribution `json:"wait"`
	Run  MetricsDistribution `json:"run"`
	// CacheHitShare is cached / (cached + jobs) -- what share of the CI
	// asked for over the window did not have to be run. A rate rather
	// than the cumulative hit and miss counters a health reading carries,
	// which is the difference between being able to see a regression and
	// not.
	CacheHitShare float64 `json:"cacheHitShare"`
	// UnattributedSeconds is how much of the waiting nothing recorded a
	// cause for, and so how much of byCause is missing. Every job stored
	// before grain kept the cause is in it.
	UnattributedSeconds float64 `json:"unattributedSeconds"`
	// ByCause is worst first, and each cause is a different setting to
	// reach for. ByRepo and ByJob are worst first by time waited; byJob
	// keys on the repository as well as the job name, because two
	// repositories' "test" are two different jobs.
	ByCause []MetricsCIQueueCause  `json:"byCause"`
	ByRepo  []MetricsCIQueueGroup  `json:"byRepo"`
	ByJob   []MetricsCIQueueGroup  `json:"byJob"`
	Buckets []MetricsCIQueueBucket `json:"buckets"`
}

// MetricsCIQueueCause is one kind of thing that held jobs. cause is
// ci.WaitCause's own vocabulary ("slot" the sandbox count, "budget" CI's
// share of the host, "hole" a reservation held open for a larger job,
// "grant" a capability, "run" the run's own width, "priority" the order)
// or "unattributed"; reason is the sentence the gate said to the
// longest-waiting job of that cause, which is what says what to set.
type MetricsCIQueueCause struct {
	Cause          string  `json:"cause"`
	Jobs           int     `json:"jobs"`
	QueuedSeconds  float64 `json:"queuedSeconds"`
	Share          float64 `json:"share"`
	LongestSeconds float64 `json:"longestSeconds"`
	Reason         string  `json:"reason,omitempty"`
}

// MetricsCIQueueGroup is one repository's, or one job's, share of the
// waiting. job is empty in byRepo, where the repository is the whole key.
type MetricsCIQueueGroup struct {
	Repo          string              `json:"repo"`
	Job           string              `json:"job,omitempty"`
	Jobs          int                 `json:"jobs"`
	QueuedSeconds float64             `json:"queuedSeconds"`
	BusySeconds   float64             `json:"busySeconds"`
	QueuedShare   float64             `json:"queuedShare"`
	Wait          MetricsDistribution `json:"wait"`
	Run           MetricsDistribution `json:"run"`
	TopCause      string              `json:"topCause,omitempty"`
	// Buckets is this row's own run-and-wait series across the window,
	// carried only by the busiest few rows of a breakdown
	// (metrics.SeriesGroups) and absent rather than empty on the rest --
	// the difference between "not in the top few" and "ran nothing".
	Buckets []MetricsCIQueueBucket `json:"buckets,omitempty"`
}

// MetricsCIQueueBucket is one slice of the window: how the waiting share
// stood during it. The same slices throughput.buckets uses, so the two
// trend lines are points over the same spans.
type MetricsCIQueueBucket struct {
	Since         time.Time `json:"since"`
	Until         time.Time `json:"until"`
	Jobs          int       `json:"jobs"`
	QueuedSeconds float64   `json:"queuedSeconds"`
	BusySeconds   float64   `json:"busySeconds"`
	QueuedShare   float64   `json:"queuedShare"`
	// The medians and worst cases of this slice, in seconds: how long its
	// jobs ran, and how long the ones that waited waited. Both rising is a
	// host that is oversubscribed, run rising alone is jobs fighting each
	// other on machines they already have, and wait rising alone is jobs
	// that cannot get one.
	RunP50Seconds  float64 `json:"runP50Seconds"`
	RunMaxSeconds  float64 `json:"runMaxSeconds"`
	WaitP50Seconds float64 `json:"waitP50Seconds"`
	WaitMaxSeconds float64 `json:"waitMaxSeconds"`
}

// MetricsMerges is what this deployment landed during the window, how
// long each change took to get there, and what the merge queue's
// batching cost and saved (metrics.Merges).
//
// landed is the count to read first: every rate here is over it, and the
// three lead-time distributions below are over the tasks that landed and
// recorded the moment each one measures from, which is not always all of
// them.
type MetricsMerges struct {
	Landed        int     `json:"landed"`
	LandedPerHour float64 `json:"landedPerHour"`
	LandedPerDay  float64 `json:"landedPerDay"`
	// The three stretches of a change's life after it is filed, in
	// seconds: the whole of it, what happened after the agent stopped,
	// and the merge queue's own share. filedToLanded is latency's
	// leadTime carried to the moment that actually matters to whoever
	// filed the task.
	FiledToLanded     MetricsDistribution  `json:"filedToLanded"`
	CompletedToLanded MetricsDistribution  `json:"completedToLanded"`
	OpenedToLanded    MetricsDistribution  `json:"openedToLanded"`
	Batches           MetricsMergeBatches  `json:"batches"`
	Repairs           MetricsBranchRepairs `json:"repairs"`
	// BaseHolds is how many changes each red stretch of a base branch held
	// in base_broken, and for how long (metrics.BaseHolds).
	BaseHolds MetricsBaseHolds `json:"baseHolds"`
	// Ports is how the window's landed ports were made: by grain's own
	// cherry-pick or by an agent (metrics.MergePorts).
	Ports MetricsMergePorts `json:"ports"`
	// Contention is merge queue contention telemetry over the window:
	// batch size distributions, merge gate evaluation latency, queue wait time
	// per PR, and rebase/re-test contention.
	Contention MetricsMergeQueueContention `json:"contention"`
	// Rechecks is what the merge queue's overlap re-check said about the
	// window's merges (metrics.MergeRechecks, grain/task-985): how many
	// it would trigger (or, in enforce mode, did), which clauses fired,
	// and what re-checking caught or skipping missed.
	Rechecks MetricsMergeRechecks `json:"rechecks"`
	// Buckets is the landing rate sliced across the window, oldest first
	// -- the same slices throughput.buckets and ciQueueing.buckets use.
	Buckets []MetricsMergeBucket `json:"buckets"`
}

// MetricsBaseHolds is metrics.BaseHolds on the wire: passes is how many
// base-branch red stretches finished in the window and open how many are
// still going; peak and total are changes held at once and in all per
// pass, prHeld the PR-time each held and length how long each lasted.
type MetricsBaseHolds struct {
	Passes  int                 `json:"passes"`
	Open    int                 `json:"open"`
	Peak    MetricsCount        `json:"peak"`
	Total   MetricsCount        `json:"total"`
	PRHeld  MetricsDistribution `json:"prHeld"`
	Length  MetricsDistribution `json:"length"`
	Reaches *time.Time          `json:"reaches,omitempty"`
}

// MetricsMergePorts is metrics.MergePorts on the wire.
type MetricsMergePorts struct {
	Mechanical     int     `json:"mechanical"`
	Agent          int     `json:"agent"`
	Repaired       int     `json:"repaired"`
	MechanicalRate float64 `json:"mechanicalRate"`
}

// MetricsBranchRepairs is duration and tool telemetry tracking for automated branch repair runs.
type MetricsBranchRepairs struct {
	Runs                   int                 `json:"runs"`
	Passes                 int                 `json:"passes"`
	Settled                int                 `json:"settled"`
	SettleRate             float64             `json:"settleRate"`
	TimeToSettle           MetricsDistribution `json:"timeToSettle"`
	ConflictTimeToSettle   MetricsDistribution `json:"conflictTimeToSettle"`
	ConflictPasses         int                 `json:"conflictPasses"`
	TestRepairTimeToSettle MetricsDistribution `json:"testRepairTimeToSettle"`
	TestRepairPasses       int                 `json:"testRepairPasses"`
	ToolCalls              int                 `json:"toolCalls"`
	ToolCallsPerRun        MetricsCount        `json:"toolCallsPerRun"`
	ErroredToolCalls       int                 `json:"erroredToolCalls"`
	ErroredToolCallRate    float64             `json:"erroredToolCallRate"`
	TranscriptBytes        MetricsCount        `json:"transcriptBytes"`
	TopTools               []MetricsToolUse    `json:"topTools"`
}

// MetricsMergeBucket is one slice of the window: how much landed during
// it, and at what rate. perHour is over this slice's own length, so the
// points are comparable to each other and to landedPerHour above.
type MetricsMergeBucket struct {
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Landed  int       `json:"landed"`
	PerHour float64   `json:"perHour"`
}

// MetricsMergeQueueContention is metrics.MergeQueueContention on the wire.
type MetricsMergeQueueContention struct {
	BatchSizes    MetricsCount                        `json:"batchSizes"`
	GateLatency   MetricsDistribution                 `json:"gateLatency"`
	QueueWaitTime MetricsDistribution                 `json:"queueWaitTime"`
	RebaseRetest  MetricsRebaseRetestContention       `json:"rebaseRetest"`
	Buckets       []MetricsMergeQueueContentionBucket `json:"buckets,omitempty"`
}

// MetricsRebaseRetestContention is metrics.RebaseRetestContention on the wire.
type MetricsRebaseRetestContention struct {
	Rebases         int     `json:"rebases"`
	Retests         int     `json:"retests"`
	TotalContention int     `json:"totalContention"`
	ContentionRate  float64 `json:"contentionRate"`
}

// MetricsMergeQueueContentionBucket is metrics.MergeQueueContentionBucket on the wire.
type MetricsMergeQueueContentionBucket struct {
	Since         time.Time           `json:"since"`
	Until         time.Time           `json:"until"`
	BatchSizes    MetricsCount        `json:"batchSizes"`
	GateLatency   MetricsDistribution `json:"gateLatency"`
	QueueWaitTime MetricsDistribution `json:"queueWaitTime"`
	Rebases       int                 `json:"rebases"`
	Retests       int                 `json:"retests"`
}

// MetricsMergeRechecks is metrics.MergeRechecks on the wire. Read
// triggerRate against triggeredAndCaught and skippedBaseRed: the first is
// the suites enforce mode costs, the second what they buy, the third an
// upper bound on what skipping misses (docs/merge-queue-behind.md).
type MetricsMergeRechecks struct {
	Merges             int            `json:"merges"`
	Triggered          int            `json:"triggered"`
	Skipped            int            `json:"skipped"`
	TriggerRate        float64        `json:"triggerRate"`
	BaseCurrent        int            `json:"baseCurrent"`
	Clauses            map[string]int `json:"clauses,omitempty"`
	Packages           map[string]int `json:"packages,omitempty"`
	Rechecked          int            `json:"rechecked"`
	TriggeredAndCaught int            `json:"triggeredAndCaught"`
	SkippedAndCaught   int            `json:"skippedAndCaught"`
	SkippedBaseRed     int            `json:"skippedBaseWentRedAfter"`
	Modes              map[string]int `json:"modes,omitempty"`
	Reaches            *time.Time     `json:"reaches,omitempty"`
}

// MetricsMergeBatches is the window's batch history: how big the merge
// queue's bets were and how they came out.
//
// batches is the denominator for every share here, and reaches says how
// far back the history behind it goes at all -- it is bounded
// (model.MergeBatchHistoryKept), so a window reaching further back than
// the bound covers fewer batches than its dates suggest.
type MetricsMergeBatches struct {
	Batches int `json:"batches"`
	Changes int `json:"changes"`
	// fresh is the batches formed from the queue rather than from a
	// failed batch's wreck, and firstTryRate is the pass rate worth
	// quoting: a halved batch passing is the bisect working rather than
	// the bet paying off.
	Fresh        int     `json:"fresh"`
	FirstTry     int     `json:"firstTry"`
	FirstTryRate float64 `json:"firstTryRate"`
	Passed       int     `json:"passed"`
	PassRate     float64 `json:"passRate"`
	Split        int     `json:"split"`
	SplitRate    float64 `json:"splitRate"`
	Culprits     int     `json:"culprits"`
	Abandoned    int     `json:"abandoned"`
	// abandonedBy partitions abandoned by why each batch produced no
	// answer (model.Abandon*), most first.
	AbandonedBy []MetricsMergeBatchAbandonReason `json:"abandonedBy,omitempty"`
	MeanSize    float64                          `json:"meanSize"`
	Biggest     int                              `json:"biggest"`
	// limit is the configured batch size now, and atLimit how many
	// batches took every member it allowed while more were queued behind
	// them -- the only evidence that raising it would buy anything.
	Limit        int                     `json:"limit"`
	AtLimit      int                     `json:"atLimit"`
	Suites       int                     `json:"suites"`
	SerialSuites int                     `json:"serialSuites"`
	Saved        int                     `json:"saved"`
	Reaches      *time.Time              `json:"reaches,omitempty"`
	Sizes        []MetricsMergeBatchSize `json:"sizes"`
}

// MetricsMergeBatchAbandonReason is one reason batches were abandoned and
// how many were.
type MetricsMergeBatchAbandonReason struct {
	Reason  string `json:"reason"`
	Batches int    `json:"batches"`
}

// MetricsMergeBatchSize is one size of bet and how it came out: how many
// batches tested that many changes, and how many of them passed.
type MetricsMergeBatchSize struct {
	Size    int     `json:"size"`
	Batches int     `json:"batches"`
	Passed  int     `json:"passed"`
	Share   float64 `json:"share"`
}

// MetricsChanges is what was changed about this deployment during the
// window: how many of its own parameters moved, when, and which.
//
// available is the field to read first. False means there was no settings
// history to read -- a deployment with no state repository, or a UI with
// no daemon to ask -- which is not the same as a deployment nobody has
// changed anything on, and the page says which rather than drawing an
// empty series for both.
type MetricsChanges struct {
	Available bool `json:"available"`
	Changes   int  `json:"changes"`
	Fields    int  `json:"fields"`
	// Events is the newest few in full (metrics.MaxChangeEvents), newest
	// first, and dropped how many older ones the window held that this
	// does not carry.
	Events  []MetricsSettingsChange `json:"events"`
	Dropped int                     `json:"dropped"`
	// Reaches is the oldest change the reader got to, over whatever
	// history it had rather than over the window -- absent when it read
	// none, which is what tells "nothing changed" from "nothing is
	// recorded".
	Reaches *time.Time            `json:"reaches,omitempty"`
	Buckets []MetricsChangeBucket `json:"buckets"`
}

// MetricsSettingsChange is one commit that moved a setting.
type MetricsSettingsChange struct {
	At      time.Time              `json:"at"`
	Commit  string                 `json:"commit"`
	Subject string                 `json:"subject,omitempty"`
	Paths   []string               `json:"paths,omitempty"`
	Fields  []MetricsSettingsField `json:"fields,omitempty"`
	// Truncated is whether the commit moved more fields than are listed --
	// a commit that rewrote a whole row rather than turned a dial.
	Truncated bool `json:"truncated,omitempty"`
}

// MetricsSettingsField is one setting that moved. from and to are present
// only for a value that is a number or a boolean: a settings row holds
// prose, paths and credential names beside its dials, and this report is
// read by agents and by anyone with the UI open, so anything that is not a
// dial is named without its value (staterepo's own rule).
type MetricsSettingsField struct {
	Path string `json:"path"`
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// MetricsChangeBucket is one slice of the window: how many parameters
// moved during it. The same slices throughput.buckets and
// ciQueueing.buckets use, which is what lets a reader line a change up
// against the numbers around it.
type MetricsChangeBucket struct {
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Changes int       `json:"changes"`
	Fields  int       `json:"fields"`
}

// MetricsHost is how hard the machine underneath this deployment was
// working during the window.
//
// available false is "no sampler to ask" -- a UI not colocated with the
// daemon, or a host with no /proc -- and not an idle machine. hours and
// samples are the denominators for every mean; reaches and ringHours say
// how far back the series can go at all, which for an in-memory sampler is
// however long this daemon has been up.
type MetricsHost struct {
	Available bool `json:"available"`
	Hours     int  `json:"hours"`
	Samples   int  `json:"samples"`
	// CPUs and memoryTotalMB are the machine as last seen in the window --
	// the denominator load means anything over.
	CPUs          int `json:"cpus"`
	MemoryTotalMB int `json:"memoryTotalMB"`
	// LoadPerCPUMean is load1Mean over cpus, which is the figure that
	// compares two machines: 1.0 is exactly enough work to keep every core
	// busy.
	Load1Mean          float64             `json:"load1Mean"`
	Load1Max           float64             `json:"load1Max"`
	LoadPerCPUMean     float64             `json:"loadPerCpuMean"`
	CPUUtilizationMean float64             `json:"cpuUtilizationMean"`
	CPUUtilizationMax  float64             `json:"cpuUtilizationMax"`
	MemoryUsedMeanMB   float64             `json:"memoryUsedMeanMB"`
	MemoryUsedMaxMB    int                 `json:"memoryUsedMaxMB"`
	SwapUsedMaxMB      int                 `json:"swapUsedMaxMB"`
	Reaches            *time.Time          `json:"reaches,omitempty"`
	RingHours          int                 `json:"ringHours,omitempty"`
	Buckets            []MetricsHostBucket `json:"buckets"`
	// Overcommit is the auto overcommit controller as it stands
	// (Config.Overcommit): the mode, the effective ratios, its last
	// reading of host pressure, and the adjustments it made inside the
	// window -- the series an overcommit change is read against
	// throughput with. Absent where no controller runs.
	Overcommit *model.OvercommitState `json:"overcommit,omitempty"`
}

// MetricsHostBucket is one slice of the window: how hard the machine was
// working during it. The same slices as throughput.buckets and
// ciQueueing.buckets, which is what makes the three readable as one
// picture. hours is the denominator: a slice nothing was sampled in
// reports zeroes and is not a quiet one.
type MetricsHostBucket struct {
	Since              time.Time `json:"since"`
	Until              time.Time `json:"until"`
	Hours              int       `json:"hours"`
	Samples            int       `json:"samples"`
	Load1Mean          float64   `json:"load1Mean"`
	Load1Max           float64   `json:"load1Max"`
	CPUUtilizationMean float64   `json:"cpuUtilizationMean"`
	CPUUtilizationMax  float64   `json:"cpuUtilizationMax"`
	MemoryUsedMeanMB   float64   `json:"memoryUsedMeanMB"`
	MemoryUsedMaxMB    int       `json:"memoryUsedMaxMB"`
}

// MetricsThroughput is how much crossed each line during the window, and
// the same counts as a daily rate.
type MetricsThroughput struct {
	TasksFiled         int             `json:"tasksFiled"`
	TasksCompleted     int             `json:"tasksCompleted"`
	TasksClosed        int             `json:"tasksClosed"`
	RunsStarted        int             `json:"runsStarted"`
	RunsFinished       int             `json:"runsFinished"`
	FiledPerDay        float64         `json:"filedPerDay"`
	CompletedPerDay    float64         `json:"completedPerDay"`
	RunsFinishedPerDay float64         `json:"runsFinishedPerDay"`
	Buckets            []MetricsBucket `json:"buckets"`
}

// MetricsBucket is one slice of the window's own time series, oldest
// first -- what a trend line is drawn from.
type MetricsBucket struct {
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	Filed        int       `json:"filed"`
	Completed    int       `json:"completed"`
	RunsFinished int       `json:"runsFinished"`
}

// MetricsStage is one latency distribution: what it measures, and the
// shape of the samples that ended inside the window. N is worth reading
// first -- a p99 over four samples is the maximum wearing a percentile's
// name, and stages legitimately carry different sample counts (see
// pkg/metrics on why a missing moment is skipped rather than guessed).
type MetricsStage struct {
	Stage       string  `json:"stage"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	N           int     `json:"n"`
	MinSeconds  float64 `json:"minSeconds"`
	P50Seconds  float64 `json:"p50Seconds"`
	P90Seconds  float64 `json:"p90Seconds"`
	P99Seconds  float64 `json:"p99Seconds"`
	MaxSeconds  float64 `json:"maxSeconds"`
	MeanSeconds float64 `json:"meanSeconds"`
}

// MetricsRuns is how the window's attempts ended, and how much of the
// deployment's dispatch capacity they used.
type MetricsRuns struct {
	Outcomes map[string]int `json:"outcomes"`
	// Endings counts the same runs by how they actually ended, which the
	// outcome words above cannot say: "cancelled" is both a human closing
	// the task and the run hitting its wall-clock cap, and "failed" is
	// both a broken framework and a run that used up its turns. See
	// model.RunEnding for the vocabulary.
	Endings map[model.RunEnding]int `json:"endings"`
	// ExecTunnelFailures is metrics.Runs.ExecTunnelFailures: runs that
	// never reached their agent because the cluster's exec transport
	// failed -- the cluster's health, counted apart from the tasks'.
	ExecTunnelFailures int `json:"execTunnelFailures"`
	// ExecTunnelToolCalls is metrics.Runs.ExecTunnelToolCalls: sandbox
	// tool calls the same tunnel failed under runs that had reached
	// their agent.
	ExecTunnelToolCalls   int     `json:"execTunnelToolCalls"`
	AttemptsPerCompletion float64 `json:"attemptsPerCompletion"`
	MeanConcurrent        float64 `json:"meanConcurrent"`
	// MaxConcurrent is the ordinary worker and merger ceiling, and
	// OrdinaryMaxConcurrent says the same thing on its own. Console
	// sessions run outside it with no count of their own
	// (model.Limits), so live runs -- and Utilization -- may exceed it
	// while sessions are answering.
	MaxConcurrent         int     `json:"maxConcurrent"`
	OrdinaryMaxConcurrent int     `json:"ordinaryMaxConcurrent"`
	Utilization           float64 `json:"utilization"`
	Live                  int     `json:"live"`
}

// MetricsBacklog is what was still in the system when the report was
// taken -- a gauge, not a window. Utilization next to a deep backlog is
// the pair worth reading together: work waiting while capacity sat idle
// is a scheduling problem, not a capacity one.
type MetricsBacklog struct {
	ByState             map[model.State]int `json:"byState"`
	Queued              int                 `json:"queued"`
	OldestQueuedSeconds float64             `json:"oldestQueuedSeconds"`
	OldestQueuedTaskID  string              `json:"oldestQueuedTaskId,omitempty"`
}

// MetricsGitHub is what this daemon has spent against GitHub's REST rate
// limit since it started.
//
// Since it started, not over the report's window: the counts live in the
// transport rather than in rows, so there is nothing to slice by time.
// That is the honest shape for what they are for -- "is this deployment
// near the limit, what is spending it, and is revalidation working" are
// all questions about now and about the whole process.
//
// Enabled false is a UI that was not handed a transport to ask
// (Config.GitHubCalls nil), which is a different answer from a
// deployment that has made no calls, and the reason this is not just a
// zeroed struct.
type MetricsGitHub struct {
	Enabled bool `json:"enabled"`
	// Sent is requests that left the host; Revalidated is the subset
	// GitHub answered 304, which cost nothing against the primary limit.
	// Spent is the difference, and is the number that matters.
	Sent        int64 `json:"sent"`
	Revalidated int64 `json:"revalidated"`
	Spent       int64 `json:"spent"`
	// Declined is calls grain refused itself while GitHub was throttling
	// it. Non-zero means this deployment is being throttled, or was.
	Declined int64 `json:"declined"`
	// Failed is refusals GitHub made -- any 4xx or 5xx.
	Failed int64 `json:"failed"`
	// Retried is reads grain sent a second time because GitHub answered
	// the first with a 5xx. Part of Sent, and part of Spent: it is what
	// absorbing those flakes costs against the budget.
	Retried int64 `json:"retried"`
	// Endpoints is the same, per call shape, most spent first, so the
	// first row names whatever an operator should look at.
	Endpoints []MetricsGitHubEndpoint `json:"endpoints,omitempty"`
	// Limit is the hour's budget as GitHub last reported it, and
	// LimitSeen whether any response has carried one yet.
	Limit         int       `json:"limit"`
	Remaining     int       `json:"remaining"`
	LimitResetsAt time.Time `json:"limitResetsAt,omitempty"`
	LimitSeen     bool      `json:"limitSeen"`
	// Callers is the spend by who made it -- each reconciler by name,
	// "ui", "agent tools" -- most spent first, with each caller's kinds
	// of call (head read, PR read, checks, review comments...) under it.
	// Process-wide, across every transport, since the budget belongs to
	// the credential (github/budget.go).
	Callers []MetricsGitHubCaller `json:"callers,omitempty"`
	// Budget is the tightest credential's hour: what is left, how fast
	// it has been falling over the last ten minutes, and when it runs
	// out at that rate (zero when it lasts until the reset). Low is the
	// throttle: under 15% left, idle and background polling stops.
	Budget MetricsGitHubBudget `json:"budget"`
	// Polls summarizes the adaptive poll schedule across tracked pull
	// requests: how many are tracked, how many are backed off past the
	// base cadence, and the median and longest poll interval in force.
	Polls github.PollStats `json:"polls"`
	// Cache is the ETag cache's occupancy against its bounds, and how
	// many entries it has had to evict to stay inside them -- non-zero
	// and growing means the working set does not fit.
	Cache github.CacheStats `json:"cache"`
}

// MetricsGitHubCaller is one caller's share.
type MetricsGitHubCaller struct {
	Caller      string                    `json:"caller"`
	Sent        int64                     `json:"sent"`
	Revalidated int64                     `json:"revalidated"`
	Spent       int64                     `json:"spent"`
	Failed      int64                     `json:"failed"`
	Declined    int64                     `json:"declined"`
	Kinds       []MetricsGitHubCallerKind `json:"kinds,omitempty"`
}

// MetricsGitHubCallerKind is one kind of call within a caller.
type MetricsGitHubCallerKind struct {
	Kind        string `json:"kind"`
	Sent        int64  `json:"sent"`
	Revalidated int64  `json:"revalidated"`
	Spent       int64  `json:"spent"`
}

// MetricsGitHubBudget is github.BudgetSnapshot as the report says it.
type MetricsGitHubBudget struct {
	Limit        int              `json:"limit"`
	Remaining    int              `json:"remaining"`
	ResetsAt     time.Time        `json:"resetsAt,omitempty"`
	Credentials  int              `json:"credentials"`
	SpendPerHour float64          `json:"spendPerHour"`
	ExhaustedAt  time.Time        `json:"exhaustedAt,omitempty"`
	Low          bool             `json:"low"`
	LowFraction  float64          `json:"lowFraction"`
	Polls        github.PollStats `json:"polls"`
}

// MetricsGitHubEndpoint is one call shape's share of the above.
type MetricsGitHubEndpoint struct {
	Endpoint    string `json:"endpoint"`
	Sent        int64  `json:"sent"`
	Revalidated int64  `json:"revalidated"`
	Spent       int64  `json:"spent"`
	Failed      int64  `json:"failed"`
}

// githubMetrics renders a transport's counts, ordered so the first row
// is the one worth reading: most *spent* first, since a call shape that
// revalidates a thousand times costs nothing and one that spends ten
// times costs ten.
func githubMetrics(source GitHubCallStats) MetricsGitHub {
	if source == nil {
		return MetricsGitHub{}
	}
	s := source.Stats()
	out := MetricsGitHub{
		Enabled:     true,
		Sent:        s.Sent,
		Revalidated: s.Revalidated,
		Spent:       s.Sent - s.Revalidated,
		Declined:    s.Declined,
		Failed:      s.Failed,
		Retried:     s.Retried,
		Limit:       s.Limit.Limit,
		Remaining:   s.Limit.Remaining,
		LimitSeen:   !s.Limit.Seen.IsZero(),
	}
	if !s.Limit.Resets.IsZero() {
		out.LimitResetsAt = s.Limit.Resets
	}
	for endpoint, e := range s.Endpoints {
		out.Endpoints = append(out.Endpoints, MetricsGitHubEndpoint{
			Endpoint: endpoint, Sent: e.Sent, Revalidated: e.Revalidated,
			Spent: e.Sent - e.Revalidated, Failed: e.Failed,
		})
	}
	sort.Slice(out.Endpoints, func(i, j int) bool {
		a, b := out.Endpoints[i], out.Endpoints[j]
		if a.Spent != b.Spent {
			return a.Spent > b.Spent
		}
		// Ties by name, so a report read twice reads the same way.
		return a.Endpoint < b.Endpoint
	})
	for name, c := range s.Callers {
		row := MetricsGitHubCaller{Caller: name, Sent: c.Sent, Revalidated: c.Revalidated,
			Spent: c.Sent - c.Revalidated, Failed: c.Failed, Declined: c.Declined}
		for kind, k := range c.Kinds {
			row.Kinds = append(row.Kinds, MetricsGitHubCallerKind{Kind: kind, Sent: k.Sent,
				Revalidated: k.Revalidated, Spent: k.Sent - k.Revalidated})
		}
		sort.Slice(row.Kinds, func(i, j int) bool {
			a, b := row.Kinds[i], row.Kinds[j]
			if a.Spent != b.Spent {
				return a.Spent > b.Spent
			}
			return a.Kind < b.Kind
		})
		out.Callers = append(out.Callers, row)
	}
	sort.Slice(out.Callers, func(i, j int) bool {
		a, b := out.Callers[i], out.Callers[j]
		if a.Spent != b.Spent {
			return a.Spent > b.Spent
		}
		return a.Caller < b.Caller
	})
	polls := s.Polls
	if polls.Tracked == 0 {
		polls = s.Budget.Polls
	}
	out.Polls = polls
	out.Budget = MetricsGitHubBudget{
		Limit: s.Budget.Limit, Remaining: s.Budget.Remaining, ResetsAt: s.Budget.Resets,
		Credentials: s.Budget.Credentials, SpendPerHour: s.Budget.SpendPerHour,
		ExhaustedAt: s.Budget.ExhaustedAt, Low: s.Budget.Low, LowFraction: github.LowBudgetFraction,
		Polls: polls,
	}
	out.Cache = s.Cache
	return out
}

// MetricsCycles is how this daemon's own RunCycle ticks have been
// behaving -- the one section of the report that describes the process
// serving it rather than the store behind it, and so the one that is
// empty right after a restart and reaches back only as far as that
// daemon's own in-memory ring does (pkg/metrics' Cycles, and
// orchestrator.CycleTimes behind it).
//
// Enabled is false when this UI was not handed a daemon to ask
// (Config.Cycles nil), which is a different answer from a daemon that
// has run no ticks yet -- the same nil-means-unavailable convention
// sandboxHealthResponse.Enabled already gives its own pane.
type MetricsCycles struct {
	Enabled  bool `json:"enabled"`
	N        int  `json:"n"`
	Observed int  `json:"observed"`
	// Truncated says the daemon has run more ticks than it still
	// remembers, so First below is where its ring begins rather than
	// where this deployment's history does.
	Truncated bool `json:"truncated"`
	// First and Last are the oldest and newest tick this section covers
	// -- the real span behind N, which is not the requested window when
	// the ring is shorter than it (the usual case). Both are the zero
	// time when N is 0; there is no omitempty for a time.Time, and a
	// pointer here would only move the same check.
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`

	Tick     MetricsDistribution `json:"tick"`
	Interval MetricsDistribution `json:"interval"`
	// DispatchWait is how far into a tick the dispatch decision was
	// reached. Interval plus this is the queue wait a task pays for
	// grain's own scheduling with no contention involved at all -- the
	// floor under the queue_wait stage in Latency above.
	DispatchWait MetricsDistribution      `json:"dispatchWait"`
	Commits      MetricsCount             `json:"commits"`
	CommitFree   int                      `json:"commitFree"`
	Reconcilers  []MetricsCycleReconciler `json:"reconcilers"`
}

// MetricsCycleReconciler is one reconciler's share of a tick. They arrive
// in the order the cycle runs them, which is the order that explains
// them: they run in sequence, so a slow one delays every decision behind
// it.
type MetricsCycleReconciler struct {
	Name     string              `json:"name"`
	Wait     MetricsDistribution `json:"wait"`
	Duration MetricsDistribution `json:"duration"`
	Failures int                 `json:"failures"`
}

// MetricsDistribution is a distribution on the wire, in seconds --
// MetricsStage above without the stage/label/description a latency stage
// carries, since the cycles section names each of its own numbers with a
// field rather than with a row in a list. Kept as a separate type rather
// than factored out of MetricsStage so the latency stages' existing flat
// shape is untouched.
type MetricsDistribution struct {
	N           int     `json:"n"`
	MinSeconds  float64 `json:"minSeconds"`
	P50Seconds  float64 `json:"p50Seconds"`
	P90Seconds  float64 `json:"p90Seconds"`
	P99Seconds  float64 `json:"p99Seconds"`
	MaxSeconds  float64 `json:"maxSeconds"`
	MeanSeconds float64 `json:"meanSeconds"`
}

// countOf is distributionOf for a series of whole numbers: no unit
// conversion, since a count is already the unit it is stated in.
func countOf(c metrics.Counts) MetricsCount {
	return MetricsCount{
		N: c.N, Min: c.Min, P50: c.P50, P90: c.P90, P99: c.P99,
		Max: c.Max, Mean: c.Mean, Total: c.Total,
	}
}

func fixTaskRateOf(f metrics.FixTaskRate) MetricsFixTaskRate {
	return MetricsFixTaskRate{Runs: f.Runs, FixTasks: f.FixTasks, Rate: f.Rate}
}

func distributionOf(d metrics.Distribution) MetricsDistribution {
	return MetricsDistribution{
		N:           d.N,
		MinSeconds:  d.Min.Seconds(),
		P50Seconds:  d.P50.Seconds(),
		P90Seconds:  d.P90.Seconds(),
		P99Seconds:  d.P99.Seconds(),
		MaxSeconds:  d.Max.Seconds(),
		MeanSeconds: d.Mean.Seconds(),
	}
}

func branchRepairsWireFrom(r metrics.BranchRepairs) MetricsBranchRepairs {
	out := MetricsBranchRepairs{
		Runs:                   r.Runs,
		Passes:                 r.Passes,
		Settled:                r.Settled,
		SettleRate:             r.SettleRate,
		TimeToSettle:           distributionOf(r.TimeToSettle),
		ConflictTimeToSettle:   distributionOf(r.ConflictTimeToSettle),
		ConflictPasses:         r.ConflictPasses,
		TestRepairTimeToSettle: distributionOf(r.TestRepairTimeToSettle),
		TestRepairPasses:       r.TestRepairPasses,
		ToolCalls:              r.ToolCalls,
		ToolCallsPerRun:        countOf(r.ToolCallsPerRun),
		ErroredToolCalls:       r.ErroredToolCalls,
		ErroredToolCallRate:    r.ErroredToolCallRate,
		TranscriptBytes:        countOf(r.TranscriptBytes),
		TopTools:               make([]MetricsToolUse, 0, len(r.TopTools)),
	}
	for _, use := range r.TopTools {
		out.TopTools = append(out.TopTools, MetricsToolUse{
			Name:        use.Name,
			Surface:     use.Surface,
			Runs:        use.Runs,
			Calls:       use.Calls,
			Errored:     use.Errored,
			ErrorRate:   use.ErrorRate,
			TimedOut:    use.TimedOut,
			TimeoutRate: use.TimeoutRate,
			ResultBytes: MetricsSize{
				N:         use.ResultBytes.N,
				MeanBytes: use.ResultBytes.Mean,
				MaxBytes:  use.ResultBytes.Max,
				P50AtMost: use.ResultBytes.P50AtMost,
				P95AtMost: use.ResultBytes.P95AtMost,
				P99AtMost: use.ResultBytes.P99AtMost,
			},
			Cut: MetricsCut{
				Calls:            use.Cut.Calls,
				Rate:             use.Cut.Rate,
				N:                use.Cut.N,
				DroppedP50AtMost: use.Cut.DroppedP50AtMost,
				DroppedP95AtMost: use.Cut.DroppedP95AtMost,
			},
		})
	}
	return out
}

func mergeQueueContentionWireFrom(c metrics.MergeQueueContention) MetricsMergeQueueContention {
	out := MetricsMergeQueueContention{
		BatchSizes:    countOf(c.BatchSizes),
		GateLatency:   distributionOf(c.GateLatency),
		QueueWaitTime: distributionOf(c.QueueWaitTime),
		RebaseRetest: MetricsRebaseRetestContention{
			Rebases:         c.RebaseRetest.Rebases,
			Retests:         c.RebaseRetest.Retests,
			TotalContention: c.RebaseRetest.TotalContention,
			ContentionRate:  c.RebaseRetest.ContentionRate,
		},
		Buckets: make([]MetricsMergeQueueContentionBucket, 0, len(c.Buckets)),
	}
	for _, b := range c.Buckets {
		out.Buckets = append(out.Buckets, MetricsMergeQueueContentionBucket{
			Since:         b.Since,
			Until:         b.Until,
			BatchSizes:    countOf(b.BatchSizes),
			GateLatency:   distributionOf(b.GateLatency),
			QueueWaitTime: distributionOf(b.QueueWaitTime),
			Rebases:       b.Rebases,
			Retests:       b.Retests,
		})
	}
	return out
}

// MetricsRequest is what a report is asked for: how far back, how finely
// the trend lines are sliced, and which tasks it is about.
//
// The span is said one of two ways. Window is a length ending now, which
// is what a picker offers and what `grain metrics -window` takes; Since
// and Until are an absolute span, which is what a saved report board
// carries when the query it was built from names actual dates
// (grain/task-83). Since set wins over Window, and Until unset means now
// -- so "since the first of the month" needs one field.
//
// A struct rather than four more arguments because three of these fields
// are optional and a call site saying `nil, 0, "", ""` says nothing about
// what it wanted.
type MetricsRequest struct {
	Window  time.Duration
	Since   time.Time
	Until   time.Time
	Buckets int
	Repos   []string
	Authors []string
	// Section narrows the report to one section (mcp.MetricsSections),
	// and with it what is read to build it: only that section's inputs
	// (metricsSectionInputs). Every other section of the report comes
	// back empty. "" is the whole report.
	Section string
}

// span is the window a request asks for, resolved against the clock:
// the absolute one when it names a start, and otherwise a length ending
// now. An inverted or empty absolute span is left exactly as asked --
// metrics.Window.Duration answers zero for one, every rate divides by it
// and reports zero, and a report of nothing is the honest answer to a
// request for no time at all.
func (r MetricsRequest) span(now time.Time) metrics.Window {
	if !r.Since.IsZero() {
		until := r.Until
		if until.IsZero() {
			until = now
		}
		return metrics.Window{Since: r.Since, Until: until}
	}
	window := r.Window
	if window <= 0 {
		window = DefaultMetricsWindow
	}
	until := r.Until
	if until.IsZero() {
		until = now
	}
	return metrics.Window{Since: until.Add(-window), Until: until}
}

// Metrics computes a report over the span the request names. Buckets is
// how many points the time series carries; 0 takes pkg/metrics' own
// default.
//
// It reads the store and derives everything else, so it takes no lock,
// caches nothing, and cannot be stale: a report is exactly what the rows
// said at the moment it was asked for. The one exception is the cycles
// section, which no row could ever say -- see Config.Cycles.
//
// A scoped request reads exactly the same rows and narrows them in
// pkg/metrics rather than in SQL (metrics.Scope): the scan is the whole
// table either way (Store.TaskTimings), and doing it there is what keeps
// the five slices a report is computed from narrowed to one another.
func (c *Client) Metrics(ctx context.Context, req MetricsRequest) (MetricsReport, error) {
	in := metrics.Input{
		Window:  req.span(c.now()),
		Buckets: req.Buckets,
		Scope:   metrics.Scope{Repos: req.Repos, Authors: req.Authors},
	}
	// Only what the asked-for section is computed from is read
	// (metricsSectionInputs): a report about throughput has no business
	// failing because the prompt sizes could not be read, and a store
	// that cannot answer one query is not a reason to stop answering the
	// others. A whole report reads everything and names each section an
	// unreadable input cost it (MetricsReport.Failed) instead of failing
	// outright.
	needs := metricsAllInputs
	if req.Section != "" {
		needs = metricsSectionInputs[req.Section]
	}
	var cfg *model.Config
	reads := []metricsRead{
		{metricsTasks, "task timings", into(&in.Tasks, c.Store.TaskTimings)},
		{metricsRuns, "run timings", into(&in.Runs, c.Store.RunTimings)},
		{metricsStates, "task states", into(&in.States, c.Store.States)},
		// What each run recorded about its own tool use. A deployment
		// whose runs all predate these rows reads them as empty and gets
		// the rest of the report unchanged -- which is the honest answer,
		// since nothing measured what those runs did inside themselves.
		{metricsToolUses, "run tool use", into(&in.ToolUses, c.Store.RunToolUses)},
		{metricsCheckWaits, "run CI waits", into(&in.CheckWaits, c.Store.RunCheckWaits)},
		{metricsCIHabits, "run CI habits", into(&in.CIHabits, c.Store.RunCIHabits)},
		// How big each run's own prompt was. Sized in the database rather
		// than read back as text (Store.RunPrompts): a deployment's
		// prompts are the largest thing in its database and a report
		// wants none of the words.
		{metricsPrompts, "run prompt sizes", into(&in.Prompts, c.Store.RunPrompts)},
		{metricsTranscripts, "run transcript sizes", into(&in.Transcripts, c.Store.RunTranscripts)},
		// Grain's own CI jobs, which are the one input here that is not a
		// task's row: a job belongs to a repository and a branch, and
		// this deployment runs CI for base branches and for operators as
		// well as for tasks (metrics.CIQueueing).
		{metricsCIJobs, "CI job timings", into(&in.CIJobs, c.Store.CIJobTimings)},
		// And the CI beside it that grain does not run (github_ci_job),
		// under an input of its own: grain CI's sections never read it,
		// and a store that cannot answer it costs them nothing. The base
		// branches grain watches are what its red and green time is
		// measured on.
		{metricsGitHubCI, "GitHub CI jobs", into(&in.GitHubCIJobs, c.Store.GitHubCIOutcomes)},
		{metricsGitHubCI, "base branches", into(&in.GitHubBaseBranches, c.gitHubBaseBranches)},
		// And the batches the merge queue formed out of what those jobs
		// judged. A bounded history (model.MergeBatchHistoryKept) read
		// entire and windowed in pkg/metrics, like everything else here;
		// a deployment that has never batched reads it as empty and gets
		// the landing rate and lead times beside it unchanged
		// (metrics.Merges).
		{metricsMergeBatches, "the merge batch history", into(&in.MergeBatches, c.Store.MergeBatchRecords)},
		// And what the queue had to do over again -- rebases and retests
		// (model.MergeQueueContentionEvent) -- read under the batches' own
		// input, since the same one section is computed from both.
		{metricsMergeBatches, "the merge queue contention history",
			into(&in.MergeQueueEvents, c.Store.MergeQueueContentionEvents)},
		// And what the overlap re-check said about each merge
		// (model.MergeRecheckRecord), under the same input: the same one
		// section is computed from it.
		{metricsMergeBatches, "the merge re-check history",
			into(&in.MergeRechecks, c.Store.MergeRecheckRecords)},
		// And what each red base branch held (model.BaseHoldPass), for the
		// same section.
		{metricsMergeBatches, "the base branch hold history",
			into(&in.BaseHolds, c.Store.BaseHoldPasses)},
		{metricsAPICalls, "run API calls", into(&in.APICalls, c.Store.RunAPICalls)},
		{metricsReportChecks, "steward report checks", into(&in.ReportChecks, c.Store.RunReportChecks)},
		{metricsConfig, "deployment settings", into(&cfg, c.Store.GetConfig)},
	}
	failed := map[metricsInput]error{}
	for _, r := range reads {
		if needs&r.input == 0 {
			continue
		}
		if err := r.read(ctx); err != nil {
			err = fmt.Errorf("reading %s: %w", r.what, err)
			if req.Section != "" {
				// The one section asked for cannot be computed, and an
				// empty one in its place would read as "nothing happened".
				return MetricsReport{}, err
			}
			failed[r.input] = err
		}
	}
	asked := req.Repos
	// A deployment whose settings have never been written has no
	// concurrency limit to compare occupancy against; the rest of the
	// report is unaffected, so this reports no utilization rather than
	// no report.
	ordinaryMaxConcurrent := 0
	if cfg != nil {
		// Workers+Mergers is the only ceiling there is. Console sessions
		// run beside it with no count of their own (model.Limits), so
		// they are in MeanConcurrent and not in the denominator, and the
		// UI says so rather than inventing a figure for them.
		ordinaryMaxConcurrent = cfg.MaxWorkers + cfg.MaxMergers
		in.MaxConcurrent = ordinaryMaxConcurrent
		// What the batch size is set to now, which is what "batches that
		// took every member the limit allowed" is measured against: no
		// batch row carries the limit it was formed under
		// (metrics.MergeBatches.AtLimit).
		in.MergeBatchSize = cfg.MergeBatchSizeOr()
	}
	// The one input that is not a row: a tick leaves no record, so the
	// daemon that ran it is the only thing that can say how long it took
	// (Config.Cycles). nil means this UI has no reconcile loop to ask,
	// which the report says rather than reporting zero ticks.
	if c.Config.Cycles != nil {
		in.Cycles = c.Config.Cycles.CycleTimes()
	}
	// And the other input that is not a row: when this deployment's own
	// parameters moved (Config.SettingsHistory). Read from the window's
	// start rather than from the whole history, with a little slack before
	// it -- a change made just before the window is what a series inside
	// the window is most often explained by, and metrics.Changes windows
	// the events itself while using the ones outside to say how far back
	// the history reaches.
	//
	// A failure here costs the section and not the report. The history is
	// a git command in a directory that may be mid-clone, mid-reset or on
	// a volume that has gone away, and none of that is a reason to stop
	// answering "how is this deployment doing" -- the section reports
	// itself unavailable, exactly as it does for a deployment that has no
	// history at all.
	if c.Config.SettingsHistory != nil && needs&metricsSettingsHistory != 0 {
		changes, err := c.Config.SettingsHistory.SettingsChanges(ctx, in.Window.Since.Add(-settingsHistorySlack))
		if err != nil {
			log.Printf("grain ui: reading this deployment's settings history: %v -- "+
				"the metrics report will not say when a parameter last changed", err)
		} else {
			in.SettingsChanges, in.SettingsHistory = changes, true
		}
	}
	// And what the machine was doing while all of it happened
	// (Config.HostLoad): the same kind of input as the two above -- a
	// reading that leaves no row -- and the same nil-means-unavailable
	// contract.
	if c.Config.HostLoad != nil {
		in.Host, in.HostSampled = c.Config.HostLoad.HostLoad(), true
	}

	report := metricsReportFrom(metrics.Compute(in), c.Config.Cycles != nil)
	// The heading is the question the caller asked.
	report.Scope.Repos = asked
	report.Runs.OrdinaryMaxConcurrent = ordinaryMaxConcurrent
	report.GitHub = githubMetrics(c.Config.GitHubCalls)
	if c.Config.Overcommit != nil {
		st := c.Config.Overcommit()
		inWindow := st.Adjustments[:0:0]
		for _, a := range st.Adjustments {
			if !a.At.Before(in.Window.Since) {
				inWindow = append(inWindow, a)
			}
		}
		st.Adjustments = inWindow
		report.Host.Overcommit = &st
	}
	// Read after the report rather than computed into it, exactly as the
	// GitHub counts are and for the same reason: a lifetime meter is not
	// a derivation of the window's rows, so metrics.Compute -- which
	// reads its input and nothing else -- has no business holding it.
	if needs&metricsSandboxes != 0 {
		report.Sandboxes = c.sandboxLifetime(ctx)
	}
	report.Failed = failedMetricsSections(reads, failed)
	return report, nil
}

// metricsInput is one thing a report is computed from, as a bit, so a
// section's needs are a mask (metricsSectionInputs).
type metricsInput uint

const (
	metricsTasks metricsInput = 1 << iota
	metricsRuns
	metricsStates
	metricsToolUses
	metricsCheckWaits
	metricsPrompts
	metricsTranscripts
	metricsCIJobs
	metricsMergeBatches
	metricsConfig
	metricsSettingsHistory
	metricsSandboxes
	metricsCIHabits
	metricsGitHubCI
	metricsAPICalls
	metricsReportChecks

	metricsAllInputs = metricsReportChecks<<1 - 1
)

// metricsSectionInputs is what each section of the report (by
// mcp.MetricsSections' names) is computed from in metrics.Compute. Every
// section built over tasks' runs needs the tasks too, because a scoped
// report narrows runs -- and every per-run row -- by the task they
// belong to (metrics.Input.scoped). Cycles and host are readings held in
// memory rather than rows, so they need nothing read.
//
// TestMetricsKnowsEverySectionsInputs holds this to the section
// list, so a section added there without its inputs here is a failing
// test rather than a section that silently reads nothing.
var metricsSectionInputs = map[string]metricsInput{
	mcp.MetricsSectionThroughput:   metricsTasks | metricsRuns,
	mcp.MetricsSectionCapacity:     metricsTasks | metricsRuns | metricsConfig,
	mcp.MetricsSectionCycles:       0,
	mcp.MetricsSectionLatency:      metricsTasks | metricsRuns,
	mcp.MetricsSectionTools:        metricsTasks | metricsRuns | metricsToolUses,
	mcp.MetricsSectionPrompts:      metricsTasks | metricsRuns | metricsPrompts,
	mcp.MetricsSectionAgents:       metricsTasks | metricsRuns | metricsToolUses | metricsAPICalls | metricsReportChecks,
	mcp.MetricsSectionChecks:       metricsTasks | metricsRuns | metricsCheckWaits | metricsCIHabits,
	mcp.MetricsSectionCIQueueing:   metricsCIJobs,
	mcp.MetricsSectionGitHubCI:     metricsGitHubCI,
	mcp.MetricsSectionHost:         0,
	mcp.MetricsSectionChanges:      metricsSettingsHistory,
	mcp.MetricsSectionPullRequests: metricsTasks | metricsRuns | metricsToolUses,
	mcp.MetricsSectionMerges: metricsTasks | metricsRuns | metricsToolUses | metricsTranscripts |
		metricsMergeBatches | metricsConfig,
	mcp.MetricsSectionSandboxes: metricsSandboxes,
	mcp.MetricsSectionBacklog:   metricsTasks | metricsStates,
	mcp.MetricsSectionGitHub:    0,
}

// KnownMetricsSection is whether name is a section a report can be
// narrowed to.
func KnownMetricsSection(name string) bool {
	_, ok := metricsSectionInputs[name]
	return ok
}

// metricsRead is one input of a report and how to read it.
type metricsRead struct {
	input metricsInput
	what  string
	read  func(context.Context) error
}

// into adapts one store read to the loop in Client.Metrics: the value
// lands in dst only when the read succeeded, so a failed input is left
// empty rather than half-filled.
func into[T any](dst *T, read func(context.Context) (T, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		v, err := read(ctx)
		if err == nil {
			*dst = v
		}
		return err
	}
}

// failedMetricsSections names every section an unreadable input cost a
// whole report, in the report's own order, each with the first of its
// inputs (in reading order) that failed.
func failedMetricsSections(reads []metricsRead, failed map[metricsInput]error) []MetricsSectionFailure {
	if len(failed) == 0 {
		return nil
	}
	var out []MetricsSectionFailure
	for _, section := range mcp.MetricsSections {
		needs := metricsSectionInputs[section]
		for _, r := range reads {
			if err := failed[r.input]; err != nil && needs&r.input != 0 {
				out = append(out, MetricsSectionFailure{Section: section, Error: err.Error(), err: err})
				break
			}
		}
	}
	return out
}

// ciQueueBucketsOf converts one run-and-wait series to the wire. Shared by
// the section's own series and by the per-job ones, which are the same
// shape and have to stay the same shape: a reader comparing one job
// against the deployment is comparing two of these.
//
// An empty series converts to nil rather than to an empty list, which is
// what MetricsCIQueueGroup.Buckets' omitempty is for: a row outside the
// busiest few carries no series at all.
func ciQueueBucketsOf(buckets []metrics.CIQueueBucket) []MetricsCIQueueBucket {
	if len(buckets) == 0 {
		return nil
	}
	out := make([]MetricsCIQueueBucket, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, MetricsCIQueueBucket{
			Since: b.Since, Until: b.Until,
			Jobs:           b.Jobs,
			QueuedSeconds:  b.Queued.Seconds(),
			BusySeconds:    b.Busy.Seconds(),
			QueuedShare:    b.QueuedShare,
			RunP50Seconds:  b.RunP50.Seconds(),
			RunMaxSeconds:  b.RunMax.Seconds(),
			WaitP50Seconds: b.WaitP50.Seconds(),
			WaitMaxSeconds: b.WaitMax.Seconds(),
		})
	}
	return out
}

// metricsReportFrom is the whole of the conversion to the wire: durations
// become seconds, and the Latency struct becomes the ordered list a
// reader walks a task's life through.
//
// cycles is whether this deployment had a reconcile loop to ask about
// its own ticks at all. It is a separate argument rather than something
// read off the report because metrics.Compute cannot tell "no daemon to
// ask" from "a daemon that has run no ticks yet", and those are
// different answers to give an operator.
func metricsReportFrom(rep metrics.Report, cycles bool) MetricsReport {
	out := MetricsReport{
		Since:         rep.Window.Since,
		Until:         rep.Window.Until,
		WindowSeconds: rep.Window.Duration().Seconds(),
		Scope:         MetricsScope{Repos: rep.Scope.Repos, Authors: rep.Scope.Authors},
		Throughput: MetricsThroughput{
			TasksFiled:         rep.Throughput.TasksFiled,
			TasksCompleted:     rep.Throughput.TasksCompleted,
			TasksClosed:        rep.Throughput.TasksClosed,
			RunsStarted:        rep.Throughput.RunsStarted,
			RunsFinished:       rep.Throughput.RunsFinished,
			FiledPerDay:        rep.Throughput.FiledPerDay,
			CompletedPerDay:    rep.Throughput.CompletedPerDay,
			RunsFinishedPerDay: rep.Throughput.RunsFinishedPerDay,
			Buckets:            make([]MetricsBucket, 0, len(rep.Throughput.Buckets)),
		},
		Runs: MetricsRuns{
			Outcomes:              rep.Runs.Outcomes,
			Endings:               rep.Runs.Endings,
			ExecTunnelFailures:    rep.Runs.ExecTunnelFailures,
			ExecTunnelToolCalls:   rep.Runs.ExecTunnelToolCalls,
			AttemptsPerCompletion: rep.Runs.AttemptsPerCompletion,
			MeanConcurrent:        rep.Runs.MeanConcurrent,
			MaxConcurrent:         rep.Runs.MaxConcurrent,
			Utilization:           rep.Runs.Utilization,
			Live:                  rep.Runs.Live,
		},
		Backlog: MetricsBacklog{
			ByState:             rep.Backlog.ByState,
			Queued:              rep.Backlog.Queued,
			OldestQueuedSeconds: rep.Backlog.OldestQueuedWait.Seconds(),
			OldestQueuedTaskID:  rep.Backlog.OldestQueuedTaskID,
		},
		Cycles: MetricsCycles{
			Enabled:      cycles,
			N:            rep.Cycles.N,
			Observed:     rep.Cycles.Observed,
			Truncated:    rep.Cycles.Truncated,
			First:        rep.Cycles.First,
			Last:         rep.Cycles.Last,
			Tick:         distributionOf(rep.Cycles.Tick),
			Interval:     distributionOf(rep.Cycles.Interval),
			DispatchWait: distributionOf(rep.Cycles.DispatchWait),
			Commits:      countOf(rep.Cycles.Commits),
			CommitFree:   rep.Cycles.CommitFree,
			Reconcilers:  make([]MetricsCycleReconciler, 0, len(rep.Cycles.Reconcilers)),
		},
	}
	for _, r := range rep.Cycles.Reconcilers {
		out.Cycles.Reconcilers = append(out.Cycles.Reconcilers, MetricsCycleReconciler{
			Name:     r.Name,
			Wait:     distributionOf(r.Wait),
			Duration: distributionOf(r.Duration),
			Failures: r.Failures,
		})
	}
	for _, b := range rep.Throughput.Buckets {
		out.Throughput.Buckets = append(out.Throughput.Buckets, MetricsBucket{
			Since: b.Since, Until: b.Until,
			Filed: b.Filed, Completed: b.Completed, RunsFinished: b.RunsFinished,
		})
	}
	if out.Runs.Outcomes == nil {
		out.Runs.Outcomes = map[string]int{}
	}
	if out.Runs.Endings == nil {
		out.Runs.Endings = map[model.RunEnding]int{}
	}
	out.Tools = MetricsTools{
		Runs:         rep.Tools.Runs,
		Calls:        rep.Tools.Calls,
		Errored:      rep.Tools.Errored,
		ErroredShare: rep.Tools.ErroredShare,
		Cut:          rep.Tools.Cut,
		CutShare:     rep.Tools.CutShare,
		CallsPerRun:  countOf(rep.Tools.CallsPerRun),
		ControlCalls: rep.Tools.ControlCalls,
		CLICalls:     rep.Tools.CLICalls,
		ByTool:       make([]MetricsToolUse, 0, len(rep.Tools.ByTool)),
	}
	for _, use := range rep.Tools.ByTool {
		out.Tools.ByTool = append(out.Tools.ByTool, MetricsToolUse{
			Name:        use.Name,
			Runs:        use.Runs,
			Calls:       use.Calls,
			Errored:     use.Errored,
			ErrorRate:   use.ErrorRate,
			TimedOut:    use.TimedOut,
			TimeoutRate: use.TimeoutRate,
			ResultBytes: MetricsSize{
				N:         use.ResultBytes.N,
				MeanBytes: use.ResultBytes.Mean,
				MaxBytes:  use.ResultBytes.Max,
				P50AtMost: use.ResultBytes.P50AtMost,
				P95AtMost: use.ResultBytes.P95AtMost,
				P99AtMost: use.ResultBytes.P99AtMost,
			},
			Cut: MetricsCut{
				Calls:            use.Cut.Calls,
				Rate:             use.Cut.Rate,
				N:                use.Cut.N,
				DroppedP50AtMost: use.Cut.DroppedP50AtMost,
				DroppedP95AtMost: use.Cut.DroppedP95AtMost,
			},
		})
	}
	out.Prompts = MetricsPrompts{
		Runs: rep.Prompts.Runs,
		Bytes: MetricsSize{
			N:         rep.Prompts.Bytes.N,
			MeanBytes: rep.Prompts.Bytes.Mean,
			MaxBytes:  rep.Prompts.Bytes.Max,
			P50AtMost: rep.Prompts.Bytes.P50AtMost,
			P95AtMost: rep.Prompts.Bytes.P95AtMost,
			P99AtMost: rep.Prompts.Bytes.P99AtMost,
		},
		Large:          rep.Prompts.Large,
		LargeShare:     rep.Prompts.LargeShare,
		WarnTokens:     model.PromptWarnTokens,
		ContextTokens:  model.PromptContextTokens,
		ThreadCut:      rep.Prompts.ThreadCut,
		ThreadCutShare: rep.Prompts.ThreadCutShare,
		Heaviest:       make([]MetricsTaskPrompts, 0, len(rep.Prompts.Heaviest)),
	}
	for _, task := range rep.Prompts.Heaviest {
		out.Prompts.Heaviest = append(out.Prompts.Heaviest, MetricsTaskPrompts{
			TaskID:    task.TaskID,
			Runs:      task.Runs,
			MaxBytes:  task.MaxBytes,
			MaxTokens: task.MaxTokens,
			Attempt:   task.Attempt,
			ThreadCut: task.ThreadCut,
		})
	}
	out.Checks = MetricsChecks{
		Waits:         rep.Checks.Waits,
		Runs:          rep.Checks.Runs,
		Verdicts:      rep.Checks.Verdicts,
		Blocked:       distributionOf(rep.Checks.Blocked),
		PushesToGreen: countOf(rep.Checks.PushesToGreen),
		GreenRuns:     rep.Checks.GreenRuns,
		Repairs:       branchRepairsWireFrom(rep.Checks.Repairs),
		RedCI: MetricsRedCI{
			FixingRuns:     rep.Checks.RedCI.FixingRuns,
			Narrowed:       rep.Checks.RedCI.Narrowed,
			NarrowedShare:  rep.Checks.RedCI.NarrowedShare,
			Selective:      rep.Checks.RedCI.Selective,
			SelectiveShare: rep.Checks.RedCI.SelectiveShare,
		},
	}
	if out.Checks.Verdicts == nil {
		out.Checks.Verdicts = map[string]int{}
	}
	out.CIQueueing = MetricsCIQueueing{
		Jobs:                rep.CIQueueing.Jobs,
		Waited:              rep.CIQueueing.Waited,
		NeverRan:            rep.CIQueueing.NeverRan,
		Cached:              rep.CIQueueing.Cached,
		QueuedSeconds:       rep.CIQueueing.Queued.Seconds(),
		BusySeconds:         rep.CIQueueing.Busy.Seconds(),
		QueuedShare:         rep.CIQueueing.QueuedShare,
		Wait:                distributionOf(rep.CIQueueing.Wait),
		Run:                 distributionOf(rep.CIQueueing.Run),
		CacheHitShare:       rep.CIQueueing.CacheHitShare,
		UnattributedSeconds: rep.CIQueueing.Unattributed.Seconds(),
		ByCause:             make([]MetricsCIQueueCause, 0, len(rep.CIQueueing.ByCause)),
		ByRepo:              make([]MetricsCIQueueGroup, 0, len(rep.CIQueueing.ByRepo)),
		ByJob:               make([]MetricsCIQueueGroup, 0, len(rep.CIQueueing.ByJob)),
		Buckets:             make([]MetricsCIQueueBucket, 0, len(rep.CIQueueing.Buckets)),
	}
	for _, c := range rep.CIQueueing.ByCause {
		out.CIQueueing.ByCause = append(out.CIQueueing.ByCause, MetricsCIQueueCause{
			Cause:          c.Cause,
			Jobs:           c.Jobs,
			QueuedSeconds:  c.Queued.Seconds(),
			Share:          c.Share,
			LongestSeconds: c.Longest.Seconds(),
			Reason:         c.Reason,
		})
	}
	for _, groups := range []struct {
		from []metrics.CIQueueGroup
		into *[]MetricsCIQueueGroup
	}{{rep.CIQueueing.ByRepo, &out.CIQueueing.ByRepo}, {rep.CIQueueing.ByJob, &out.CIQueueing.ByJob}} {
		for _, g := range groups.from {
			*groups.into = append(*groups.into, MetricsCIQueueGroup{
				Repo:          g.Repo,
				Job:           g.Job,
				Jobs:          g.Jobs,
				QueuedSeconds: g.Queued.Seconds(),
				BusySeconds:   g.Busy.Seconds(),
				QueuedShare:   g.QueuedShare,
				Wait:          distributionOf(g.Wait),
				Run:           distributionOf(g.Run),
				TopCause:      g.TopCause,
				Buckets:       ciQueueBucketsOf(g.Buckets),
			})
		}
	}
	out.CIQueueing.Buckets = ciQueueBucketsOf(rep.CIQueueing.Buckets)
	out.GitHubCI = gitHubCIWireFrom(rep.GitHubCI)
	out.Merges = MetricsMerges{
		Landed:            rep.Merges.Landed,
		LandedPerHour:     rep.Merges.LandedPerHour,
		LandedPerDay:      rep.Merges.LandedPerDay,
		FiledToLanded:     distributionOf(rep.Merges.FiledToLanded),
		CompletedToLanded: distributionOf(rep.Merges.CompletedToLanded),
		OpenedToLanded:    distributionOf(rep.Merges.OpenedToLanded),
		Repairs:           branchRepairsWireFrom(rep.Merges.Repairs),
		BaseHolds: MetricsBaseHolds{
			Passes:  rep.Merges.BaseHolds.Passes,
			Open:    rep.Merges.BaseHolds.Open,
			Peak:    countOf(rep.Merges.BaseHolds.Peak),
			Total:   countOf(rep.Merges.BaseHolds.Total),
			PRHeld:  distributionOf(rep.Merges.BaseHolds.PRHeld),
			Length:  distributionOf(rep.Merges.BaseHolds.Length),
			Reaches: rep.Merges.BaseHolds.Reaches,
		},
		Ports:      MetricsMergePorts(rep.Merges.Ports),
		Contention: mergeQueueContentionWireFrom(rep.Merges.Contention),
		Rechecks:   MetricsMergeRechecks(rep.Merges.Rechecks),
		Batches: MetricsMergeBatches{
			Batches:      rep.Merges.Batches.Batches,
			Changes:      rep.Merges.Batches.Changes,
			Fresh:        rep.Merges.Batches.Fresh,
			FirstTry:     rep.Merges.Batches.FirstTry,
			FirstTryRate: rep.Merges.Batches.FirstTryRate,
			Passed:       rep.Merges.Batches.Passed,
			PassRate:     rep.Merges.Batches.PassRate,
			Split:        rep.Merges.Batches.Split,
			SplitRate:    rep.Merges.Batches.SplitRate,
			Culprits:     rep.Merges.Batches.Culprits,
			Abandoned:    rep.Merges.Batches.Abandoned,
			MeanSize:     rep.Merges.Batches.MeanSize,
			Biggest:      rep.Merges.Batches.Biggest,
			Limit:        rep.Merges.Batches.Limit,
			AtLimit:      rep.Merges.Batches.AtLimit,
			Suites:       rep.Merges.Batches.Suites,
			SerialSuites: rep.Merges.Batches.SerialSuites,
			Saved:        rep.Merges.Batches.Saved,
			Reaches:      rep.Merges.Batches.Reaches,
			Sizes:        make([]MetricsMergeBatchSize, 0, len(rep.Merges.Batches.Sizes)),
		},
		Buckets: make([]MetricsMergeBucket, 0, len(rep.Merges.Buckets)),
	}
	for _, a := range rep.Merges.Batches.AbandonedBy {
		out.Merges.Batches.AbandonedBy = append(out.Merges.Batches.AbandonedBy,
			MetricsMergeBatchAbandonReason{Reason: a.Reason, Batches: a.Batches})
	}
	for _, s := range rep.Merges.Batches.Sizes {
		out.Merges.Batches.Sizes = append(out.Merges.Batches.Sizes, MetricsMergeBatchSize{
			Size: s.Size, Batches: s.Batches, Passed: s.Passed, Share: s.Share,
		})
	}
	for _, b := range rep.Merges.Buckets {
		out.Merges.Buckets = append(out.Merges.Buckets, MetricsMergeBucket{
			Since: b.Since, Until: b.Until, Landed: b.Landed, PerHour: b.PerHour,
		})
	}
	out.Changes = MetricsChanges{
		Available: rep.Changes.Available,
		Changes:   rep.Changes.N,
		Fields:    rep.Changes.Fields,
		Dropped:   rep.Changes.Dropped,
		Reaches:   rep.Changes.Reaches,
		Events:    make([]MetricsSettingsChange, 0, len(rep.Changes.Events)),
		Buckets:   make([]MetricsChangeBucket, 0, len(rep.Changes.Buckets)),
	}
	for _, e := range rep.Changes.Events {
		event := MetricsSettingsChange{
			At: e.At, Commit: e.Commit, Subject: e.Subject,
			Paths: e.Paths, Truncated: e.Truncated,
			Fields: make([]MetricsSettingsField, 0, len(e.Fields)),
		}
		for _, f := range e.Fields {
			event.Fields = append(event.Fields, MetricsSettingsField{
				Path: f.Path, Name: f.Name, From: f.From, To: f.To,
			})
		}
		out.Changes.Events = append(out.Changes.Events, event)
	}
	for _, b := range rep.Changes.Buckets {
		out.Changes.Buckets = append(out.Changes.Buckets, MetricsChangeBucket{
			Since: b.Since, Until: b.Until, Changes: b.Changes, Fields: b.Fields,
		})
	}
	out.Host = MetricsHost{
		Available:          rep.Host.Available,
		Hours:              rep.Host.Hours,
		Samples:            rep.Host.Samples,
		CPUs:               rep.Host.CPUs,
		MemoryTotalMB:      rep.Host.MemTotalMB,
		Load1Mean:          rep.Host.Load1Mean,
		Load1Max:           rep.Host.Load1Max,
		LoadPerCPUMean:     rep.Host.LoadPerCPUMean,
		CPUUtilizationMean: rep.Host.CPUUtilizationMean,
		CPUUtilizationMax:  rep.Host.CPUUtilizationMax,
		MemoryUsedMeanMB:   rep.Host.MemUsedMeanMB,
		MemoryUsedMaxMB:    rep.Host.MemUsedMaxMB,
		SwapUsedMaxMB:      rep.Host.SwapUsedMaxMB,
		Reaches:            rep.Host.Reaches,
		RingHours:          rep.Host.RingHours,
		Buckets:            make([]MetricsHostBucket, 0, len(rep.Host.Buckets)),
	}
	for _, b := range rep.Host.Buckets {
		out.Host.Buckets = append(out.Host.Buckets, MetricsHostBucket{
			Since: b.Since, Until: b.Until,
			Hours:              b.Hours,
			Samples:            b.Samples,
			Load1Mean:          b.Load1Mean,
			Load1Max:           b.Load1Max,
			CPUUtilizationMean: b.CPUUtilizationMean,
			CPUUtilizationMax:  b.CPUUtilizationMax,
			MemoryUsedMeanMB:   b.MemUsedMeanMB,
			MemoryUsedMaxMB:    b.MemUsedMaxMB,
		})
	}
	out.PullRequests = MetricsPullRequests{
		Runs:         rep.PullRequests.Runs,
		Opened:       rep.PullRequests.Opened,
		Calls:        rep.PullRequests.Calls,
		AdoptionRate: rep.PullRequests.AdoptionRate,
		WithTool:     fixTaskRateOf(rep.PullRequests.WithTool),
		WithoutTool:  fixTaskRateOf(rep.PullRequests.WithoutTool),
	}
	out.Agents = MetricsAgents{
		Templates:    make([]MetricsAgentTemplate, 0, len(rep.Agents.Templates)),
		ReportChecks: agentReportCheckOf(rep.Agents.ReportCheck),
	}
	for _, t := range rep.Agents.Templates {
		out.Agents.Templates = append(out.Agents.Templates, MetricsAgentTemplate{
			TemplateID: t.TemplateID,
			Wakes:      t.Wakes,
			Calls:      t.Calls,
			Bytes:      t.ResultBytes,
			CallsP50:   t.CallsP50,
			CallsP95:   t.CallsP95,
			BytesP50:   t.BytesP50,
			BytesP95:   t.BytesP95,
			Elisions:   t.Elisions,
			ReReads:    t.ReReads,
			Report:     agentReportCheckOf(t.ReportCheck),
		})
	}

	l := rep.Latency
	for _, s := range []struct {
		stage, label, description string
		d                         metrics.Distribution
	}{
		{"approval_wait", "filed -> approved",
			"how long a proposed task sat before a human accepted it (tasks approved as they were filed are not counted)", l.ApprovalWait},
		{"queue_wait", "approved -> attempt started",
			"the backlog wait: grain's own scheduling. Read against runs.utilization and the cycles section, " +
				"which are its two possible causes -- a deployment with no headroom, or a slow tick", l.QueueWait},
		{"sandbox_setup", "attempt started -> agent's first turn",
			"a sandbox built, a repo cloned, capabilities minted -- the cost a golden image or a warm spare would cut", l.SandboxSetup},
		{"agent_work", "agent's first turn -> attempt finished",
			"what the agent framework itself spent; grain does not control it", l.AgentWork},
		{"attempt", "one whole attempt",
			"setup plus agent work plus the finish path -- the one stage every run has", l.Attempt},
		{"retry_wait", "attempt finished -> next attempt started",
			"dispatch's failure backoff, plus another turn through the queue", l.RetryWait},
		{"time_to_finish", "first attempt started -> completed",
			"how long the work took once it began, across every attempt, question and reply it needed", l.TimeToFinish},
		{"lead_time", "filed -> completed",
			"the whole of what whoever filed the task actually waited", l.LeadTime},
	} {
		out.Latency = append(out.Latency, MetricsStage{
			Stage: s.stage, Label: s.label, Description: s.description,
			N:           s.d.N,
			MinSeconds:  s.d.Min.Seconds(),
			P50Seconds:  s.d.P50.Seconds(),
			P90Seconds:  s.d.P90.Seconds(),
			P99Seconds:  s.d.P99.Seconds(),
			MaxSeconds:  s.d.Max.Seconds(),
			MeanSeconds: s.d.Mean.Seconds(),
		})
	}
	return out
}

func agentReportCheckOf(r metrics.AgentReportCheck) MetricsAgentReportCheck {
	return MetricsAgentReportCheck{
		Wakes:        r.CheckedWakes,
		Claims:       r.Claims,
		Uncited:      r.Uncited,
		Unread:       r.Unread,
		Mismatched:   r.Mismatched,
		UncitedRate:  r.UncitedRate,
		UnreadRate:   r.UnreadRate,
		MismatchRate: r.MismatchRate,
	}
}

// maxMetricsBuckets caps ?buckets= the same way pkg/metrics does its own,
// so an absurd request is a 400 here rather than a silently different
// answer there.
const maxMetricsBuckets = 200

// ParseMetricsMoment reads one end of an absolute span: a full RFC 3339
// timestamp, or a bare date, which is the spelling a date filter in a
// board's query carries ("2026-01-31", ui/src/taskFilters.js) and the one
// anybody types by hand.
//
// A bare date is read as midnight UTC rather than in the deployment's own
// zone. The alternative is a report whose boundaries move when a setting
// changes, and a span is a technical bound on a measurement rather than
// a day in somebody's diary -- a report over the last thirty days does
// not become a different report because the office moved.
func ParseMetricsMoment(text string) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, nil
	}
	if at, err := time.Parse(time.RFC3339, text); err == nil {
		return at.UTC(), nil
	}
	at, err := time.Parse(time.DateOnly, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("moment %q: expected a date like 2026-01-31, or an RFC 3339 timestamp", text)
	}
	return at.UTC(), nil
}

// metricsScopeValues reads one repeatable scope parameter -- ?repo= or
// ?author=, once per value -- dropping blanks so that a UI that always
// sends the parameter and sometimes has nothing to put in it asks for
// every task rather than for tasks belonging to "".
func metricsScopeValues(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	window, err := ParseMetricsWindow(query.Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	since, err := ParseMetricsMoment(query.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	until, err := ParseMetricsMoment(query.Get("until"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	buckets := 0
	if raw := query.Get("buckets"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > maxMetricsBuckets {
			writeError(w, http.StatusBadRequest,
				errors.New("buckets must be a positive integer no greater than "+strconv.Itoa(maxMetricsBuckets)))
			return
		}
		buckets = parsed
	}
	section := strings.TrimSpace(strings.ToLower(query.Get("section")))
	if section != "" && !KnownMetricsSection(section) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("there is no %q section in this report; it has %s",
			section, strings.Join(mcp.MetricsSections, ", ")))
		return
	}
	report, err := s.tasks.Metrics(r.Context(), MetricsRequest{
		Window:  window,
		Since:   since,
		Until:   until,
		Buckets: buckets,
		Repos:   metricsScopeValues(query["repo"]),
		Authors: metricsScopeValues(query["author"]),
		Section: section,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// A failed section says what a failed request would have said, and
	// no more: the cause goes to the log under the reference it carries.
	for i, f := range report.Failed {
		body := apierror.Body(http.StatusInternalServerError, f.err)
		report.Failed[i].Error, report.Failed[i].Ref = body.Error, body.Ref
	}
	writeJSON(w, http.StatusOK, report)
}
