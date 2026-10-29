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

package daemon

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// metricsRequest turns a span and a scope said in words into the request
// the endpoint takes, refusing the two ways of asking for a span that
// cannot mean anything.
//
// The span is parsed here as well as by the daemon, so a typo is a
// message from the command the operator actually ran rather than a 400
// relayed back from a server -- and the same sentence reaches a run that
// asked read_grain_metrics for a window nobody can parse, since
// mcpserver.go's daemonMetrics builds its request through here too.
//
// An unset field is left unset in the request rather than resolved
// here, so a bare `grain metrics` still asks for the *server's* default
// window and not for whatever this binary was built believing it to be.
//
// The wording names the words rather than the flags, because the caller
// may have typed neither: "window" is -window at a terminal and the
// "window" argument in a tool call.
func MetricsRequest(window, since, until string, repos, authors []string) (ui.MetricsRequest, error) {
	req := ui.MetricsRequest{Repos: repos, Authors: authors}
	var err error
	if strings.TrimSpace(window) != "" {
		if req.Window, err = ui.ParseMetricsWindow(window); err != nil {
			return ui.MetricsRequest{}, err
		}
	}
	if req.Since, err = ui.ParseMetricsMoment(since); err != nil {
		return ui.MetricsRequest{}, err
	}
	if req.Until, err = ui.ParseMetricsMoment(until); err != nil {
		return ui.MetricsRequest{}, err
	}
	// Both say where the report starts, and the endpoint resolves the
	// clash by letting since win. Silently ignoring something somebody
	// typed is how a report gets read as covering a week it does not, so
	// it is a message here instead. (window with until is a different
	// thing and allowed: a week ending on a date.)
	if !req.Since.IsZero() && req.Window > 0 {
		return ui.MetricsRequest{}, errors.New(
			"since and window both say where the report starts: give one or the other")
	}
	// An inverted span is a report of no time at all, which the endpoint
	// answers honestly with zeroes. It is a typo every time, so it is
	// worth a sentence rather than a page of noughts.
	if !req.Since.IsZero() && !req.Until.IsZero() && !req.Until.After(req.Since) {
		return ui.MetricsRequest{}, fmt.Errorf("until %s is not after since %s: there is no span between them",
			req.Until.Format(time.RFC3339), req.Since.Format(time.RFC3339))
	}
	return req, nil
}

// renderMetrics writes the report the way `grain metrics` prints it, to
// whatever is reading: a terminal here, and a tool result for a run that
// holds the self-debug grant (mcp.NewMetricsTools, wired in
// mcpserver.go's daemonMetrics). One renderer for both, because the
// whole point of letting a run read the census is that it reads the same
// numbers, in the same words, as the operator it is answering.
//
// section is "" for the whole report, or one section's name
// (mcp.MetricsSections) for that one alone -- a page of tables is more
// than a tool result can carry, and "what does the tool census say" is
// nearly always the question rather than "print everything". The header
// is written either way: a section quoted without the window and scope
// it was measured over is a number waiting to be attached to the wrong
// question.
func RenderMetrics(w io.Writer, rep ui.MetricsReport, section string) {
	fmt.Fprintf(w, "window: %s -> %s (%s)\n",
		rep.Since.Format(time.RFC3339), rep.Until.Format(time.RFC3339),
		Seconds(rep.WindowSeconds))
	// The scope the report was actually computed under, echoed back by
	// the endpoint and printed under the window for the same reason the
	// report board prints it under its own: every number below is about
	// some of the tasks, and one pasted into a note under a bare
	// "window: ..." header is a number somebody will quote at the wrong
	// question. Nothing is printed for a report over everything, which
	// is what a report has always been.
	if scope := scopeSummary(rep.Scope); scope != "" {
		fmt.Fprintf(w, "scope:  %s\n", scope)
	}
	if section == "" {
		for _, s := range MetricsSections {
			if !printFailedSection(w, rep, s.Name) {
				s.render(w, rep)
			}
		}
		return
	}
	if printFailedSection(w, rep, section) {
		return
	}
	// One section, rendered aside so that a section this report has
	// nothing to say about answers in words rather than with a header
	// and silence. Every printer below is deliberately silent on a
	// window it has no samples for, and silence is the one answer a
	// reader cannot tell from "grain does not measure that".
	var body strings.Builder
	for _, s := range MetricsSections {
		if s.Name == section {
			s.render(&body, rep)
		}
	}
	if body.Len() == 0 {
		fmt.Fprintf(w, "\nNothing to report under %q over this window: nothing in it was measured "+
			"-- no run recorded one, or this deployment records none. Try a wider window, or "+
			"ask for the whole report.\n", section)
		return
	}
	fmt.Fprint(w, body.String())
}

// printFailedSection writes, in a section's place, that it could not be
// built (ui.MetricsReport.Failed) and reports whether it did: an empty
// section printed instead would read as a quiet window.
func printFailedSection(w io.Writer, rep ui.MetricsReport, section string) bool {
	for _, f := range rep.Failed {
		if f.Section == section {
			fmt.Fprintf(w, "\n%s: unavailable -- %s\n", section, f.Error)
			return true
		}
	}
	return false
}

// metricsSections is the report in the order it prints, each part named
// so that one of them can be asked for on its own. The names are
// mcp.MetricsSections -- the list read_grain_metrics offers an agent in
// its own schema -- and metrics_test.go holds the two lists to each
// other, so a section added here without a name there (or the reverse)
// is a failing test rather than an argument that silently renders
// nothing.
var MetricsSections = []struct {
	Name   string
	render func(w io.Writer, rep ui.MetricsReport)
}{
	{mcp.MetricsSectionThroughput, func(w io.Writer, rep ui.MetricsReport) { printThroughput(w, rep) }},
	{mcp.MetricsSectionCapacity, func(w io.Writer, rep ui.MetricsReport) { printCapacity(w, rep.Runs) }},
	// Printed right after capacity and before the latency stages, so
	// "approved -> attempt started" below is read with both of its causes
	// already on screen: the utilization above says whether the
	// deployment was full, and this says what the tick itself cost
	// regardless of that.
	{mcp.MetricsSectionCycles, func(w io.Writer, rep ui.MetricsReport) { printCycles(w, rep.Cycles) }},
	{mcp.MetricsSectionLatency, func(w io.Writer, rep ui.MetricsReport) { printLatency(w, rep.Latency) }},
	{mcp.MetricsSectionTools, func(w io.Writer, rep ui.MetricsReport) { printTools(w, rep.Tools) }},
	{mcp.MetricsSectionPrompts, func(w io.Writer, rep ui.MetricsReport) { printPrompts(w, rep.Prompts) }},
	{mcp.MetricsSectionAgents, func(w io.Writer, rep ui.MetricsReport) { printAgents(w, rep.Agents) }},
	{mcp.MetricsSectionChecks, func(w io.Writer, rep ui.MetricsReport) { printChecks(w, rep.Checks) }},
	{mcp.MetricsSectionCIQueueing, func(w io.Writer, rep ui.MetricsReport) { printCIQueueing(w, rep.CIQueueing) }},
	// Straight after grain's own CI, labelled with its source: the CI
	// beside it that grain does not run.
	{mcp.MetricsSectionGitHubCI, func(w io.Writer, rep ui.MetricsReport) { printGitHubCI(w, rep.GitHubCI) }},
	// Printed straight after the CI numbers, because it is what they are
	// read against: a run-time series that steps halfway across the window
	// and a parameter that moved halfway across the window belong on the
	// same screen.
	// The host goes between them: the CI numbers above say what the jobs
	// took, this says what machine they were given, and the changes below
	// say what was done about it.
	{mcp.MetricsSectionHost, func(w io.Writer, rep ui.MetricsReport) { printHost(w, rep.Host) }},
	{mcp.MetricsSectionChanges, func(w io.Writer, rep ui.MetricsReport) { printChanges(w, rep.Changes) }},
	{mcp.MetricsSectionPullRequests, func(w io.Writer, rep ui.MetricsReport) { printPullRequests(w, rep.PullRequests) }},
	// Printed after the mid-run pull requests because it is where they
	// end up: that section is whether a run opens one, this is what
	// happened to it afterwards -- how long it took to land, and what the
	// queue's batching cost getting it there.
	{mcp.MetricsSectionMerges, func(w io.Writer, rep ui.MetricsReport) { printMerges(w, rep.Merges) }},
	{mcp.MetricsSectionSandboxes, func(w io.Writer, rep ui.MetricsReport) { printSandboxes(w, rep.Sandboxes) }},
	{mcp.MetricsSectionBacklog, func(w io.Writer, rep ui.MetricsReport) { printBacklog(w, rep.Backlog) }},
	{mcp.MetricsSectionGitHub, func(w io.Writer, rep ui.MetricsReport) { printGitHub(w, rep.GitHub, time.Now()) }},
}

func printThroughput(w io.Writer, rep ui.MetricsReport) {
	t := rep.Throughput
	fmt.Fprintln(w, "\nthroughput")
	fmt.Fprintf(w, "  tasks filed              %6d  (%.1f/day)\n", t.TasksFiled, t.FiledPerDay)
	fmt.Fprintf(w, "  tasks completed          %6d  (%.1f/day)\n", t.TasksCompleted, t.CompletedPerDay)
	fmt.Fprintf(w, "  tasks closed             %6d\n", t.TasksClosed)
	fmt.Fprintf(w, "  attempts started         %6d\n", t.RunsStarted)
	fmt.Fprintf(w, "  attempts finished        %6d  (%.1f/day)\n", t.RunsFinished, t.RunsFinishedPerDay)
	if outcomes := outcomeSummary(rep.Runs.Outcomes); outcomes != "" {
		fmt.Fprintf(w, "  attempt outcomes         %s\n", outcomes)
	}
	// Printed under the outcomes rather than instead of them: the words
	// above are what the rows say, and this is what they mean. The two
	// disagree on purpose -- one "cancelled" is a human closing a task
	// and the next is a run that hit the two-hour wall.
	if endings := endingSummary(rep.Runs.Endings); endings != "" {
		fmt.Fprintf(w, "  ...how they ended        %s\n", endings)
	}
	fmt.Fprintf(w, "  attempts per completion  %6.2f\n", rep.Runs.AttemptsPerCompletion)
}

func printCapacity(w io.Writer, runs ui.MetricsRuns) {
	fmt.Fprintln(w, "\ncapacity")
	if runs.MaxConcurrent > 0 {
		fmt.Fprintf(w, "  mean concurrent runs     %6.2f of %d  (%.0f%% of the limit)\n",
			runs.MeanConcurrent, runs.MaxConcurrent, runs.Utilization*100)
	} else {
		fmt.Fprintf(w, "  mean concurrent runs     %6.2f\n", runs.MeanConcurrent)
	}
	fmt.Fprintf(w, "  live now                 %6d\n", runs.Live)
}

// printLatency renders the stages a task passes through. Its own header
// says what a window means for it: these are the samples that *ended*
// inside it, so a task filed last month and completed this morning is in
// here at its full lead time.
func printLatency(w io.Writer, stages []ui.MetricsStage) {
	if len(stages) == 0 {
		return
	}
	fmt.Fprintln(w, "\nlatency (stages that ended inside the window)")
	fmt.Fprintf(w, "  %-40s %5s %10s %10s %10s\n", "stage", "n", "p50", "p90", "max")
	for _, s := range stages {
		fmt.Fprintf(w, "  %-40s %5d %10s %10s %10s\n", s.Label, s.N,
			Seconds(s.P50Seconds), Seconds(s.P90Seconds), Seconds(s.MaxSeconds))
	}
}

func printBacklog(w io.Writer, backlog ui.MetricsBacklog) {
	if len(backlog.ByState) == 0 {
		return
	}
	fmt.Fprintln(w, "\nbacklog (right now, not over the window)")
	states := make([]string, 0, len(backlog.ByState))
	for state, n := range backlog.ByState {
		states = append(states, fmt.Sprintf("%s=%d", state, n))
	}
	sort.Strings(states)
	fmt.Fprintf(w, "  %s\n", strings.Join(states, "  "))
	if backlog.OldestQueuedTaskID != "" {
		fmt.Fprintf(w, "  oldest queued: task %s, waiting %s\n",
			backlog.OldestQueuedTaskID, Seconds(backlog.OldestQueuedSeconds))
	}
}

// printGitHub is the "github" section: the hour's budget and its
// projection first, since "when does it run out" is the question this
// section exists for, then who is spending it.
func printGitHub(w io.Writer, g ui.MetricsGitHub, now time.Time) {
	if !g.Enabled {
		return
	}
	fmt.Fprintln(w, "\ngithub (since this daemon started, not over the window)")
	b := g.Budget
	if b.Limit > 0 {
		state := "ok"
		if b.Low {
			state = fmt.Sprintf("LOW (under %.0f%%): idle and background polling cut back", b.LowFraction*100)
		}
		fmt.Fprintf(w, "  budget             %d of %d left  [%s]\n", b.Remaining, b.Limit, state)
		if !b.ResetsAt.IsZero() {
			fmt.Fprintf(w, "  resets             in %s\n", b.ResetsAt.Sub(now).Round(time.Second))
		}
		if b.SpendPerHour > 0 {
			fmt.Fprintf(w, "  spending           %.0f/h over the last ten minutes\n", b.SpendPerHour)
		}
		if !b.ExhaustedAt.IsZero() {
			fmt.Fprintf(w, "  runs out           in %s at that rate, before the reset\n",
				b.ExhaustedAt.Sub(now).Round(time.Second))
		} else if b.SpendPerHour > 0 {
			fmt.Fprintln(w, "  runs out           not before the reset at that rate")
		}
	} else {
		fmt.Fprintln(w, "  budget             no response has reported one yet")
	}
	fmt.Fprintf(w, "  requests           %d sent, %d free (304), %d spent, %d failed, %d declined\n",
		g.Sent, g.Revalidated, g.Spent, g.Failed, g.Declined)
	fmt.Fprintf(w, "  etag cache         %d of %d entries, %d evicted\n",
		g.Cache.Entries, g.Cache.MaxEntries, g.Cache.Evicted)
	polls := g.Polls
	if polls.Tracked == 0 {
		polls = g.Budget.Polls
	}
	if polls.Tracked > 0 {
		fmt.Fprintf(w, "  poll schedule      %d of %d PR(s) backed off (median %s, max %s; %s..%s)\n",
			polls.BackedOff, polls.Tracked,
			time.Duration(polls.MedianSeconds)*time.Second,
			time.Duration(polls.MaxSeconds)*time.Second,
			time.Duration(polls.BaseSeconds)*time.Second,
			time.Duration(polls.CapSeconds)*time.Second)
	}
	if len(g.Callers) > 0 {
		fmt.Fprintln(w, "  by caller (spent / sent):")
		for _, c := range g.Callers {
			var kinds []string
			for _, k := range c.Kinds {
				kinds = append(kinds, fmt.Sprintf("%s %d/%d", k.Kind, k.Spent, k.Sent))
			}
			fmt.Fprintf(w, "    %-24s %6d / %-6d  %s\n", c.Caller, c.Spent, c.Sent, strings.Join(kinds, ", "))
		}
	}
}

// scopeSummary is a report's scope as a phrase -- "tasks in acme/widgets
// or acme/docs, by ada" -- or "" for a report over every task, which
// needs no note.
//
// The wording is the report board's own (scopeSummary in
// ui/src/report.js), down to the "or" between the values of one
// attribute, so the same report described at a terminal and on a page
// says the same sentence: within an attribute a scope is an "any of",
// and between them it narrows.
func scopeSummary(scope ui.MetricsScope) string {
	var parts []string
	if len(scope.Repos) > 0 {
		parts = append(parts, "in "+strings.Join(scope.Repos, " or "))
	}
	if len(scope.Authors) > 0 {
		parts = append(parts, "by "+strings.Join(scope.Authors, " or "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "tasks " + strings.Join(parts, ", ")
}

// printTools renders the tool census: what a run spends its turns on, and
// which tool its errors are in.
//
// Nothing is printed when no run in the window recorded one -- a
// deployment whose runs all predate the census, or a window with no runs
// in it. A table of zeroes there would read as "the tools never fail",
// which is the opposite of "nobody measured them".
func printTools(w io.Writer, t ui.MetricsTools) {
	if t.Runs == 0 || t.Calls == 0 {
		return
	}
	fmt.Fprintf(w, "\ntool use (%d run(s) in the window recorded what they called)\n", t.Runs)
	fmt.Fprintf(w, "  calls                    %6d  (%.0f per run at the median, %d at p90)\n",
		t.Calls, float64(t.CallsPerRun.P50), t.CallsPerRun.P90)
	fmt.Fprintf(w, "  errored calls            %6d  (%.1f%% of them -- a handful is the ordinary shape of this work)\n",
		t.Errored, t.ErroredShare*100)
	fmt.Fprintf(w, "  cut answers              %6d  (%.1f%% of them -- calls whose answer grain shortened to fit the cap)\n",
		t.Cut, t.CutShare*100)
	if t.ControlCalls > 0 || t.CLICalls > 0 {
		fmt.Fprintf(w, "  by surface               %6d MCP control tool call(s), %d grain CLI command(s)\n",
			t.ControlCalls, t.CLICalls)
	}
	fmt.Fprintf(w, "  %-16s %8s %8s %8s %9s %11s %11s %9s %11s\n",
		"tool", "runs", "calls", "errors", "timed out", "mean bytes", "p95 bytes", "cut", "cut p50")
	for _, use := range t.ByTool {
		timeouts := "-"
		if use.TimedOut > 0 {
			timeouts = fmt.Sprintf("%d (%.0f%%)", use.TimedOut, use.TimeoutRate*100)
		}
		// A tool nothing was ever cut on prints "-" in both columns
		// rather than a nought and an empty bound: it is the same "what
		// was never measured is not a zero" rule the timeout column
		// above follows, and the distinction the whole section exists
		// for is between a cap that fires and one that does not.
		cuts, droppedP50 := "-", "-"
		if use.Cut.Calls > 0 {
			cuts = fmt.Sprintf("%d (%.0f%%)", use.Cut.Calls, use.Cut.Rate*100)
			droppedP50 = atMost(use.Cut.DroppedP50AtMost)
		}
		fmt.Fprintf(w, "  %-16s %8d %8d %7d%% %9s %11d %11s %9s %11s\n",
			use.Name, use.Runs, use.Calls, int(use.ErrorRate*100+0.5), timeouts,
			use.ResultBytes.MeanBytes, atMost(use.ResultBytes.P95AtMost),
			cuts, droppedP50)
	}
	fmt.Fprintln(w, "  (p95 bytes is an upper bound: sizes are kept in base-2 buckets, so the real"+
		"\n   number is inside the octave below it. Read it with the cut columns rather than"+
		"\n   on its own -- the cap bounds these sizes, so a p95 under the cap means a tool"+
		"\n   whose answers fit or one whose answers are being cut down to fit, and only"+
		"\n   \"cut\" tells the two apart. cut p50 is how much the median cut dropped, taken"+
		"\n   from what each elision notice said it was dropping.)")
}

// printPrompts renders the other side of the tool census: not what a run
// spent its own context on, but what grain spent of it before the run's
// first turn.
//
// The cut line is printed under the distribution rather than beside it
// because it is what the distribution has to be read against. The
// conversation a prompt carries is bounded, so these sizes stop at that
// bound whatever the thread behind them was -- exactly the censoring the
// tool result cap does to the sizes above, and the same answer to it.
//
// Silent for a window whose runs recorded no prompt at all, for
// printTools' reason: zeroes there would read as "grain hands its runs
// nothing" where the truth is that nobody measured.
func printPrompts(w io.Writer, p ui.MetricsPrompts) {
	if p.Runs == 0 {
		return
	}
	fmt.Fprintf(w, "\nprompts (%d attempt(s) in the window recorded what they were handed)\n", p.Runs)
	// The percentile is printed in the same units as the mean and the
	// max beside it rather than in bare bytes like the tool table's
	// atMost column: a prompt is judged against a context window, and
	// nobody reads 131071 as "an eighth of what a run gets".
	fmt.Fprintf(w, "  size                     mean %s, p95 <=%s, max %s\n",
		model.PromptBytes(int(p.Bytes.MeanBytes)), model.PromptBytes(int(p.Bytes.P95AtMost)),
		model.PromptBytes(int(p.Bytes.MaxBytes)))
	fmt.Fprintf(w, "  over the warning line    %6d  (%.0f%% of them -- past ~%d tokens, a quarter of the %d-token window grain measures against)\n",
		p.Large, p.LargeShare*100, p.WarnTokens, p.ContextTokens)
	fmt.Fprintf(w, "  conversation cut to fit  %6d  (%.0f%% of them)\n",
		p.ThreadCut, p.ThreadCutShare*100)
	if len(p.Heaviest) > 0 {
		fmt.Fprintf(w, "  %-16s %8s %12s %12s %9s\n", "heaviest task", "attempts", "largest", "~tokens", "cut")
		for _, task := range p.Heaviest {
			cut := "-"
			if task.ThreadCut > 0 {
				cut = strconv.Itoa(task.ThreadCut)
			}
			fmt.Fprintf(w, "  task %-11s %8d %12s %12d %9s  (attempt %d)\n",
				task.TaskID, task.Runs, model.PromptBytes(int(task.MaxBytes)),
				task.MaxTokens, cut, task.Attempt)
		}
	}
	fmt.Fprintln(w, "  (p95 is an upper bound, for the reason the tool sizes above are."+
		"\n   Read these sizes with the cut line rather than on their own: the thread a"+
		"\n   prompt carries is capped, so this is what runs were given and not what they"+
		"\n   would have been given, and only \"conversation cut to fit\" says how often the"+
		"\n   cap decided that.)")
}

// printSandboxes renders what this deployment's machines have cost it:
// how many sandboxes it has built, agent and CI, and how many
// sandbox-hours they came to.
//
// It carries its own header saying "since", in the loudest terms the
// column will take, because it is the one section of this report that is
// *not* over the window printed at the top -- the counters are an
// aggregate and there is nothing to slice by time (ui.MetricsReport's own
// note). A number quoted out of here under "window: 7d" would be wrong by
// however long the deployment has been running.
//
// Silent where there is no counter to read (a report from a build
// without one) and where nothing has been counted yet, for printTools'
// reason: a row of noughts reads as "this deployment builds no
// sandboxes", which is never true of one that answered this at all.
func printSandboxes(w io.Writer, s *ui.SandboxLifetime) {
	if s == nil || s.Total.Attempted == 0 {
		return
	}
	since := "the start"
	if !s.Total.Since.IsZero() {
		since = s.Total.Since.Format(time.RFC3339)
	}
	fmt.Fprintf(w, "\nsandboxes (since %s, NOT over the window above)\n", since)
	fmt.Fprintf(w, "  %-8s %10s %10s %10s %14s %12s\n", "kind", "built", "failed", "hours", "mean each", "unaccounted")
	// The kinds, then their sum under them: a reader comparing agent
	// against CI wants the two side by side, and a reader answering "how
	// many altogether" wants one line, and this is both.
	rows := append([]ui.SandboxUsageTotals{}, s.Kinds...)
	rows = append(rows, s.Total)
	for _, row := range rows {
		name := row.Kind
		if name == "" {
			name = "all"
		}
		fmt.Fprintf(w, "  %-8s %10d %10d %10.1f %14s %12d\n",
			name, row.Created, row.Failed, row.Hours, Seconds(row.MeanSeconds), row.Unaccounted)
	}
	fmt.Fprintln(w, "  (built is the sandboxes that came up; failed is the ones asked for that never"+
		"\n   did, counted because they spent something too. hours is how long the ones grain"+
		"\n   saw stop lived, and unaccounted is how many it did not see stop -- live right"+
		"\n   now, or taken by a restart -- whose time is missing from that figure.)")
}

// printChecks renders the CI loop every prompt sends a run around: how
// each wait ended, how long it blocked, and how many pushes a run took to
// go green. Silent for a window whose runs never waited on CI, for the
// same reason printTools is.
func printChecks(w io.Writer, c ui.MetricsChecks) {
	if c.Waits == 0 && c.Repairs.Runs == 0 && c.Repairs.Passes == 0 && c.RedCI.FixingRuns == 0 {
		return
	}
	if c.Waits > 0 {
		fmt.Fprintf(w, "\nCI waits (%d wait(s) across %d run(s))\n", c.Waits, c.Runs)
		if verdicts := outcomeSummary(c.Verdicts); verdicts != "" {
			fmt.Fprintf(w, "  verdicts                 %s\n", verdicts)
		}
		fmt.Fprintf(w, "  blocked                  p50 %s, p90 %s, max %s\n",
			Seconds(c.Blocked.P50Seconds), Seconds(c.Blocked.P90Seconds), Seconds(c.Blocked.MaxSeconds))
		if c.GreenRuns > 0 {
			fmt.Fprintf(w, "  pushes before green      %.1f on average, %d at worst (over %d run(s) that went green)\n",
				c.PushesToGreen.Mean, c.PushesToGreen.Max, c.GreenRuns)
		}
	}
	if r := c.RedCI; r.FixingRuns > 0 {
		fmt.Fprintf(w, "\nred-CI procedure (%d CI-fixing run(s): repairs, and runs that pushed after a red wait)\n", r.FixingRuns)
		fmt.Fprintf(w, "  narrowed test first      %d (%.0f%%) ran a narrowed local test before the fixing push\n",
			r.Narrowed, r.NarrowedShare*100)
		fmt.Fprintf(w, "  selective CI             %d (%.0f%%) used run_ci_job, retrigger_ci or wait_for_checks with jobs\n",
			r.Selective, r.SelectiveShare*100)
	}
	if c.Repairs.Runs > 0 || c.Repairs.Passes > 0 {
		printBranchRepairs(w, c.Repairs)
	}
}

// printBaseHolds renders what each base-branch repair pass held: while a
// base branch is red the changes queued for it sit in base_broken, and
// these are how many, and for how long, per red stretch. Silent for a
// window with no pass in it, finished or open.
func printBaseHolds(w io.Writer, h ui.MetricsBaseHolds) {
	if h.Passes == 0 && h.Open == 0 {
		return
	}
	fmt.Fprintf(w, "\nbase-branch holds (%d repair pass(es) finished, %d still open)\n", h.Passes, h.Open)
	if h.Passes > 0 {
		fmt.Fprintf(w, "  peak PRs held            p50 %d, p90 %d, max %d\n", h.Peak.P50, h.Peak.P90, h.Peak.Max)
		fmt.Fprintf(w, "  total PRs held           p50 %d, p90 %d, max %d (%d across all passes)\n",
			h.Total.P50, h.Total.P90, h.Total.Max, h.Total.Total)
		fmt.Fprintf(w, "  PR-hours held            p50 %.1f, p90 %.1f, max %.1f\n",
			h.PRHeld.P50Seconds/3600, h.PRHeld.P90Seconds/3600, h.PRHeld.MaxSeconds/3600)
		fmt.Fprintf(w, "  pass length              p50 %s, p90 %s, max %s\n",
			Seconds(h.Length.P50Seconds), Seconds(h.Length.P90Seconds), Seconds(h.Length.MaxSeconds))
	}
	if h.Reaches != nil {
		fmt.Fprintf(w, "  (the hold history is bounded at %d passes and reaches back to %s.)\n",
			model.BaseHoldHistoryKept, h.Reaches.Format(time.RFC3339))
	}
}

// printBranchRepairs renders duration and tool telemetry for automated branch repairs.
func printBranchRepairs(w io.Writer, r ui.MetricsBranchRepairs) {
	if r.Passes == 0 && r.Runs == 0 {
		return
	}
	fmt.Fprintf(w, "\nbranch repairs (%d pass(es), %d settled (%.0f%%), across %d run(s))\n",
		r.Passes, r.Settled, r.SettleRate*100, r.Runs)
	if r.TimeToSettle.N > 0 {
		fmt.Fprintf(w, "  time to settle           p50 %s, p90 %s, max %s (over %d pass(es))\n",
			Seconds(r.TimeToSettle.P50Seconds), Seconds(r.TimeToSettle.P90Seconds), Seconds(r.TimeToSettle.MaxSeconds), r.TimeToSettle.N)
	}
	if r.ConflictPasses > 0 && r.ConflictTimeToSettle.N > 0 {
		fmt.Fprintf(w, "  conflict resolution      p50 %s, max %s (%d pass(es))\n",
			Seconds(r.ConflictTimeToSettle.P50Seconds), Seconds(r.ConflictTimeToSettle.MaxSeconds), r.ConflictPasses)
	}
	if r.TestRepairPasses > 0 && r.TestRepairTimeToSettle.N > 0 {
		fmt.Fprintf(w, "  test / CI repairs        p50 %s, max %s (%d pass(es))\n",
			Seconds(r.TestRepairTimeToSettle.P50Seconds), Seconds(r.TestRepairTimeToSettle.MaxSeconds), r.TestRepairPasses)
	}
	if r.ToolCalls > 0 {
		fmt.Fprintf(w, "  tool calls               %d total (%.1f/run), %d errored (%.1f%%)\n",
			r.ToolCalls, r.ToolCallsPerRun.Mean, r.ErroredToolCalls, r.ErroredToolCallRate*100)
	}
	if r.TranscriptBytes.N > 0 {
		fmt.Fprintf(w, "  transcript size          %s mean, %s max\n",
			model.PromptBytes(int(r.TranscriptBytes.Mean)), model.PromptBytes(int(r.TranscriptBytes.Max)))
	}
	if len(r.TopTools) > 0 {
		parts := make([]string, 0, len(r.TopTools))
		for _, t := range r.TopTools {
			parts = append(parts, fmt.Sprintf("%s (%d calls)", t.Name, t.Calls))
		}
		fmt.Fprintf(w, "  top tools                %s\n", strings.Join(parts, ", "))
	}
}

// printCIQueueing renders the other side of that loop: how much of this
// deployment's CI job-time went on waiting for a machine rather than on
// running the tree, what held it, and which repositories and jobs wore
// it.
//
// The first line is the whole section in one number, and the breakdown
// under it is what to do about it: each cause is a different setting --
// the sandbox count, CI's share of the host, a reservation held open for
// a larger job, a capability grant, a run's own width, the priority
// order -- and the gate's own sentence is printed beside the worst of
// each, because "budget" says where to look and "8 GiB free and this job
// asks for 16 GiB" says what to set.
//
// The jobs table is capped at the worst few. The question it answers is
// "which jobs wear this", and a deployment's whole job list printed in
// full would bury the answer in the jobs that wait for nothing.
//
// Silent for a window whose CI ran nothing, for the same reason
// printTools is: a row of zeroes would read as a deployment that never
// queues.
func printCIQueueing(w io.Writer, q ui.MetricsCIQueueing) {
	if q.Jobs == 0 {
		return
	}
	fmt.Fprintf(w, "\nCI queueing (%d job(s) ended in the window", q.Jobs)
	if q.Cached > 0 {
		// Said out loud because it is the denominator: a cache hit never
		// asked for a machine, so counting it would let a deployment
		// improve this share by caching more.
		fmt.Fprintf(w, "; %d cache hit(s) left out", q.Cached)
	}
	fmt.Fprintln(w, ")")
	fmt.Fprintf(w, "  waiting                  %5.0f%% of job-time  (%s waiting, %s working)\n",
		q.QueuedShare*100, Seconds(q.QueuedSeconds), Seconds(q.BusySeconds))
	if q.Waited > 0 {
		fmt.Fprintf(w, "  the %d that waited       p50 %s, p90 %s, max %s\n",
			q.Waited, Seconds(q.Wait.P50Seconds), Seconds(q.Wait.P90Seconds), Seconds(q.Wait.MaxSeconds))
	}
	// And the other half of the same job. Printed next to the waiting
	// rather than further down because the two are read against each
	// other: waiting longer says raise the concurrency, running longer
	// says lower it, and one of them on its own says neither.
	if q.Run.N > 0 {
		fmt.Fprintf(w, "  the %d that ran          p50 %s, p90 %s, max %s\n",
			q.Run.N, Seconds(q.Run.P50Seconds), Seconds(q.Run.P90Seconds), Seconds(q.Run.MaxSeconds))
	}
	if q.Cached > 0 {
		fmt.Fprintf(w, "  answered from cache      %5.0f%% of the jobs asked for\n", q.CacheHitShare*100)
	}
	if q.NeverRan > 0 {
		fmt.Fprintf(w, "  never got a machine      %6d  (queued, then given up on)\n", q.NeverRan)
	}
	for _, c := range q.ByCause {
		fmt.Fprintf(w, "  %-12s %4.0f%% %8s over %d job(s)\n",
			c.Cause, c.Share*100, Seconds(c.QueuedSeconds), c.Jobs)
		if c.Reason != "" {
			fmt.Fprintf(w, "               worst %s: %s\n", Seconds(c.LongestSeconds), c.Reason)
		}
	}
	if q.UnattributedSeconds > 0 {
		fmt.Fprintln(w, "  (unattributed is waiting nothing recorded a cause for -- every job stored"+
			"\n   before grain kept the cause, and any wait at a gate with nothing to say.)")
	}
	if len(q.ByJob) > 0 {
		fmt.Fprintf(w, "  %-44s %5s %9s %7s %9s %s\n", "worst jobs", "n", "waited", "share", "ran p50", "held by")
		for i, g := range q.ByJob {
			if i >= maxCIQueueJobs || g.QueuedSeconds <= 0 {
				break
			}
			fmt.Fprintf(w, "  %-44s %5d %9s %6.0f%% %9s %s\n",
				g.Repo+" "+g.Job, g.Jobs, Seconds(g.QueuedSeconds), g.QueuedShare*100,
				Seconds(g.Run.P50Seconds), g.TopCause)
		}
	}
	printCIQueueTrend(w, q)
}

// printCIQueueTrend renders the series: for the deployment as a whole and
// then for the busiest few jobs, how long a job ran and how long it waited
// in each slice of the window.
//
// This is the part of the report a tuning decision is actually made from,
// and it is two numbers per slice rather than a distribution because of
// what it has to fit into: the same lines are what an agent reads through
// read_grain_metrics, inside a bounded answer. A slice that held no job
// prints a dash rather than a zero -- a quiet Sunday is not a fast one.
func printCIQueueTrend(w io.Writer, q ui.MetricsCIQueueing) {
	if len(q.Buckets) == 0 {
		return
	}
	fmt.Fprintln(w, "  ran/waited per slice of the window, oldest first (p50 of the jobs in each):")
	printCIQueueTrendLine(w, "all jobs", q.Buckets)
	for i, g := range q.ByJob {
		if i >= maxCIQueueJobs {
			break
		}
		if len(g.Buckets) > 0 {
			printCIQueueTrendLine(w, g.Repo+" "+g.Job, g.Buckets)
		}
	}
}

func printCIQueueTrendLine(w io.Writer, label string, buckets []ui.MetricsCIQueueBucket) {
	var cells strings.Builder
	for _, b := range buckets {
		if b.Jobs == 0 {
			cells.WriteString("      -")
			continue
		}
		fmt.Fprintf(&cells, " %6s", briefly(b.RunP50Seconds)+"/"+briefly(b.WaitP50Seconds))
	}
	fmt.Fprintf(w, "    %-30s%s\n", label, cells.String())
}

// briefly is a duration short enough to put a dozen of them on one line:
// "31m" rather than "31m0s", and a dash for nothing at all. seconds()
// stays what every single figure in this report is printed with -- this is
// only for the series, where the alternative is a line that wraps.
func briefly(s float64) string {
	d := time.Duration(s * float64(time.Second))
	switch {
	case d <= 0:
		return "-"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
	default:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int((d%time.Hour).Round(time.Minute)/time.Minute))
	}
}

// printHost renders how hard the machine underneath this deployment was
// working: the window's figures, then the same thing per slice, which is
// the line the CI series above is read against.
//
// A sampler that has not been running as long as the window asked for says
// so rather than reporting a quiet fortnight: this history is in memory and
// starts again with the daemon (pkg/sysstat's History).
func printHost(w io.Writer, h ui.MetricsHost) {
	if !h.Available {
		return
	}
	fmt.Fprintf(w, "\nhost (%d hour(s) of readings in the window", h.Hours)
	if h.CPUs > 0 {
		fmt.Fprintf(w, ", on %d core(s)", h.CPUs)
	}
	fmt.Fprintln(w, ")")
	if h.Hours == 0 {
		if h.Reaches != nil {
			fmt.Fprintf(w, "  nothing sampled in this window; the sampler's own history starts at %s\n",
				h.Reaches.Format(time.RFC3339))
			return
		}
		fmt.Fprintf(w, "  nothing sampled yet -- this history is kept in memory and starts with the"+
			"\n  daemon, reaching back at most %d hour(s) once it has been running that long.\n", h.RingHours)
		return
	}
	fmt.Fprintf(w, "  load                     %6.2f mean, %.2f worst  (%.2f per core)\n",
		h.Load1Mean, h.Load1Max, h.LoadPerCPUMean)
	fmt.Fprintf(w, "  cpu                      %5.0f%% mean, %.0f%% worst\n",
		h.CPUUtilizationMean*100, h.CPUUtilizationMax*100)
	if h.MemoryTotalMB > 0 {
		fmt.Fprintf(w, "  memory used              %6.0f MiB mean, %d MiB worst of %d MiB\n",
			h.MemoryUsedMeanMB, h.MemoryUsedMaxMB, h.MemoryTotalMB)
	}
	if h.SwapUsedMaxMB > 0 {
		fmt.Fprintf(w, "  swap used                %6d MiB at worst  (a host this size should not swap)\n",
			h.SwapUsedMaxMB)
	}
	if len(h.Buckets) > 0 {
		var load, cpu strings.Builder
		for _, b := range h.Buckets {
			if b.Samples == 0 {
				load.WriteString("      -")
				cpu.WriteString("      -")
				continue
			}
			fmt.Fprintf(&load, " %6.1f", b.Load1Mean)
			fmt.Fprintf(&cpu, " %5.0f%%", b.CPUUtilizationMean*100)
		}
		fmt.Fprintf(w, "    %-30s%s\n", "mean load per slice", load.String())
		fmt.Fprintf(w, "    %-30s%s\n", "mean cpu per slice", cpu.String())
	}
	printOvercommit(w, h.Overcommit)
}

// printOvercommit renders the auto overcommit controller under the host
// it reads: the mode, the ratios admitted against, its last reading and
// every adjustment in the window, newest last.
func printOvercommit(w io.Writer, o *model.OvercommitState) {
	if o == nil {
		return
	}
	fmt.Fprintf(w, "  overcommit               %s: cpu %.2fx, memory %.2fx", o.Mode, o.CPUOvercommit, o.MemoryOvercommit)
	if o.Mode == model.OvercommitAuto {
		fmt.Fprintf(w, "  (bounds cpu %.2f-%.2f, memory %.2f-%.2f)",
			o.Settings.CPUMin, o.Settings.CPUMax, o.Settings.MemoryMin, o.Settings.MemoryMax)
	}
	fmt.Fprintln(w)
	if l := o.Last; l != nil {
		fmt.Fprintf(w, "  pressure (avg10)         cpu %.1f%%, memory %.1f%% some / %.1f%% full, io %.1f%% some; swap-in %.1f MB/s\n",
			l.CPUSome, l.MemorySome, l.MemoryFull, l.IOSome, l.SwapInMBps)
	}
	if o.OOMKills > 0 {
		fmt.Fprintf(w, "  oom kills                %6d since the daemon started, last at %s\n", o.OOMKills, o.LastOOMAt.Format(time.RFC3339))
	}
	for _, a := range o.Adjustments {
		fmt.Fprintf(w, "    %s  %-6s %.2fx -> %.2fx  %s\n", a.At.Format(time.RFC3339), a.Dimension, a.From, a.To, a.Reason)
	}
}

// printChanges renders when this deployment's own parameters moved: the
// series first, so it can be read against every other series above, and
// then the changes themselves, newest first.
//
// It says out loud when there is no history to read. "Nothing has been
// changed here" and "nobody can say what was changed here" are different
// answers, and a tuning agent that cannot tell them apart will confidently
// attribute a week's improvement to nothing at all.
func printChanges(w io.Writer, c ui.MetricsChanges) {
	if !c.Available {
		fmt.Fprintln(w, "\nsettings changes")
		fmt.Fprintln(w, "  no settings history to read -- this deployment keeps no state repository,"+
			"\n  or this report was taken somewhere with no access to it.")
		return
	}
	fmt.Fprintf(w, "\nsettings changes (%d change(s) moving %d parameter(s) in the window", c.Changes, c.Fields)
	if c.Reaches != nil {
		fmt.Fprintf(w, "; history read back to %s", c.Reaches.Format(time.RFC3339))
	}
	fmt.Fprintln(w, ")")
	if len(c.Buckets) > 0 {
		var cells strings.Builder
		for _, b := range c.Buckets {
			fmt.Fprintf(&cells, " %6d", b.Fields)
		}
		fmt.Fprintf(w, "    %-30s%s\n", "parameters moved per slice", cells.String())
	}
	for _, e := range c.Events {
		fmt.Fprintf(w, "  %s  %s\n", e.At.Format(time.RFC3339), e.Subject)
		for _, f := range e.Fields {
			// A value is printed only where it is a dial; anything else is
			// named and left unquoted (ui.MetricsSettingsField says why).
			if f.From != "" || f.To != "" {
				fmt.Fprintf(w, "      %s %s: %s -> %s\n", f.Path, f.Name, dashIfEmpty(f.From), dashIfEmpty(f.To))
				continue
			}
			fmt.Fprintf(w, "      %s %s changed\n", f.Path, f.Name)
		}
		if e.Truncated {
			fmt.Fprintf(w, "      ...and more, in %s\n", e.Commit)
		}
	}
	if c.Dropped > 0 {
		fmt.Fprintf(w, "  ...and %d older change(s) in the window, not listed\n", c.Dropped)
	}
}

// dashIfEmpty prints a value that was not there as a dash: a field
// appearing or disappearing is "- -> 7" and "7 -> -", which is what
// happened, rather than an empty column a reader has to guess at.
func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// maxCIQueueJobs is how many of the worst-hit jobs the section above
// prints. Enough to show a repository's expensive jobs against its cheap
// ones, which is the comparison the breakdown exists for, and short
// enough that the answer is not buried.
const maxCIQueueJobs = 8

// printPullRequests renders the last stretch of that loop: whether runs
// take the pull request grain offers them mid-flight, and whether the
// ones that do leave fewer red builds behind than the ones that do not.
//
// The two fix-task rates are printed as a pair on purpose, with both
// denominators on the line. Either alone is a number with no scale --
// some share of branches go red for reasons no amount of watching would
// have caught -- and the fix-task link is on the task rather than the
// attempt, so the pair is a comparison between two populations measured
// the same coarse way rather than a rate anybody should quote.
//
// Silent for a window in which nobody was offered the tool, for the same
// reason printTools is: a row of zeroes would read as "no run ever calls
// it" where the truth is "no run in this window could have".
func printPullRequests(w io.Writer, p ui.MetricsPullRequests) {
	if p.Runs == 0 {
		return
	}
	fmt.Fprintf(w, "\nmid-run pull requests (%d run(s) in the window could have opened one)\n", p.Runs)
	fmt.Fprintf(w, "  opened one themselves    %6d  (%.0f%% of them, %d call(s) in all)\n",
		p.Opened, p.AdoptionRate*100, p.Calls)
	fmt.Fprintf(w, "  repaired by the queue after %3.0f%% of the %d that did, %.0f%% of the %d that did not\n",
		p.WithTool.Rate*100, p.WithTool.Runs, p.WithoutTool.Rate*100, p.WithoutTool.Runs)
	fmt.Fprintln(w, "  (a repair is the merge queue cleaning up a red build the run left behind."+
		"\n   It is recorded on the task, not the attempt: read the difference, not either rate.)")
}

// printMerges renders the far end of the loop: what landed, how long it
// took to land, and what the merge queue's batching cost getting it
// there.
//
// The three lead times are printed above the batch counters because they
// are what the counters are for. Raising the batch size lands more per
// suite and makes a failure cost every change in the batch a bisect, so
// the honest way to read a pass rate is beside the wait it bought or
// spent -- and the wait is the half somebody outside this deployment
// actually feels.
//
// Silent for a window in which nothing landed and no batch finished, for
// printTools' reason: a table of zeroes reads as a queue that never
// merges anything rather than as a quiet week, and the single-section
// path says so in words instead.
func printMerges(w io.Writer, m ui.MetricsMerges) {
	if m.Landed == 0 && m.Batches.Batches == 0 && m.Repairs.Passes == 0 && m.Repairs.Runs == 0 &&
		m.BaseHolds.Passes == 0 && m.BaseHolds.Open == 0 && m.Rechecks.Merges == 0 {
		return
	}
	fmt.Fprintf(w, "\nmerges (%d change(s) landed in the window)\n", m.Landed)
	fmt.Fprintf(w, "  landed                   %6d  (%.2f/hour, %.1f/day)\n",
		m.Landed, m.LandedPerHour, m.LandedPerDay)
	for _, stage := range []struct {
		label string
		d     ui.MetricsDistribution
	}{
		{"filed -> landed", m.FiledToLanded},
		{"agent finished -> landed", m.CompletedToLanded},
		{"PR opened -> landed", m.OpenedToLanded},
	} {
		// A stage nothing recorded the far end of prints nothing rather
		// than zeroes: a task that never opened a pull request is in no
		// distribution here, which is why each carries its own n.
		if stage.d.N == 0 {
			continue
		}
		fmt.Fprintf(w, "  %-25s%6d  p50 %s, p90 %s, max %s\n", stage.label, stage.d.N,
			Seconds(stage.d.P50Seconds), Seconds(stage.d.P90Seconds), Seconds(stage.d.MaxSeconds))
	}
	if len(m.Buckets) > 0 {
		var cells strings.Builder
		for _, b := range m.Buckets {
			fmt.Fprintf(&cells, " %6d", b.Landed)
		}
		fmt.Fprintf(w, "    %-30s%s\n", "landed per slice", cells.String())
	}
	if p := m.Ports; p.Mechanical+p.Agent > 0 {
		fmt.Fprintf(w, "  %-25s%6d  mechanical %d (%.0f%%, %d sent on to an agent), by agent %d\n", "ports landed",
			p.Mechanical+p.Agent, p.Mechanical, 100*p.MechanicalRate, p.Repaired, p.Agent)
	}
	if m.Repairs.Passes > 0 || m.Repairs.Runs > 0 {
		printBranchRepairs(w, m.Repairs)
	}
	printBaseHolds(w, m.BaseHolds)
	printMergeQueueContention(w, m.Contention)
	printMergeRechecks(w, m.Rechecks)
	printMergeBatches(w, m.Batches)
}

func printMergeRechecks(w io.Writer, r ui.MetricsMergeRechecks) {
	if r.Merges == 0 {
		return
	}
	fmt.Fprintf(w, "\n  overlap re-checks (%d merge(s) evaluated)\n", r.Merges)
	fmt.Fprintf(w, "    %-23s%6d  (%d triggered, %.0f%%; %d skipped)\n",
		"triggered", r.Triggered, r.Triggered, r.TriggerRate*100, r.Skipped)
	if len(r.Clauses) > 0 {
		fmt.Fprintf(w, "    %-23s%s\n", "clauses", formatCountMap(r.Clauses))
	}
	if len(r.Packages) > 0 {
		fmt.Fprintf(w, "    %-23s%s\n", "hot Go packages", formatCountMap(r.Packages))
	}
	fmt.Fprintf(w, "    %-23s%6d  (caught %d triggered, %d skipped)\n",
		"re-checked", r.Rechecked, r.TriggeredAndCaught, r.SkippedAndCaught)
	fmt.Fprintf(w, "    %-23s%6d\n", "skipped, base went red", r.SkippedBaseRed)
	if len(r.Modes) > 0 {
		fmt.Fprintf(w, "    %-23s%s\n", "modes", formatCountMap(r.Modes))
	}
	if r.Reaches != nil {
		fmt.Fprintf(w, "    (the merge recheck history is bounded at %d rows and reaches back to %s.)\n",
			model.MergeRecheckHistoryKept, r.Reaches.Format(time.RFC3339))
	}
}

func formatCountMap(counts map[string]int) string {
	type entry struct {
		key   string
		count int
	}
	entries := make([]entry, 0, len(counts))
	for k, v := range counts {
		entries = append(entries, entry{key: k, count: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].key < entries[j].key
	})
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s %d", e.key, e.count))
	}
	return strings.Join(parts, ", ")
}

func printMergeQueueContention(w io.Writer, c ui.MetricsMergeQueueContention) {
	if c.BatchSizes.N == 0 && c.GateLatency.N == 0 && c.QueueWaitTime.N == 0 && c.RebaseRetest.TotalContention == 0 {
		return
	}
	if c.GateLatency.N > 0 {
		fmt.Fprintf(w, "  %-25s%6d  p50 %s, p90 %s, max %s\n", "merge gate latency", c.GateLatency.N,
			Seconds(c.GateLatency.P50Seconds), Seconds(c.GateLatency.P90Seconds), Seconds(c.GateLatency.MaxSeconds))
	}
	if c.QueueWaitTime.N > 0 {
		fmt.Fprintf(w, "  %-25s%6d  p50 %s, p90 %s, max %s\n", "PR queue wait time", c.QueueWaitTime.N,
			Seconds(c.QueueWaitTime.P50Seconds), Seconds(c.QueueWaitTime.P90Seconds), Seconds(c.QueueWaitTime.MaxSeconds))
	}
	if c.RebaseRetest.TotalContention > 0 {
		fmt.Fprintf(w, "  %-25s%6d  rebases %d, retests %d (rate %.2f)\n", "contention events",
			c.RebaseRetest.TotalContention, c.RebaseRetest.Rebases, c.RebaseRetest.Retests, c.RebaseRetest.ContentionRate)
	}
}

// printMergeBatches renders the batch half: the dial, and the evidence
// for moving it.
//
// The pass rate printed first is the first-try one -- batches formed
// fresh from the queue that passed -- because a halved batch passing is
// the bisect working rather than the bet paying off, and the two get
// confused every time they are printed as one number.
func printMergeBatches(w io.Writer, b ui.MetricsMergeBatches) {
	if b.Batches == 0 {
		if b.Reaches != nil {
			fmt.Fprintf(w, "  no batch finished in this window; the batch history starts at %s\n",
				b.Reaches.Format(time.RFC3339))
			return
		}
		fmt.Fprintln(w, "  no merge batches recorded -- this deployment has formed none, or none"+
			"\n  since it started keeping the history.")
		return
	}
	fmt.Fprintf(w, "  batches                  %6d  (%d change(s) landed on one of their verdicts)\n",
		b.Batches, b.Changes)
	fmt.Fprintf(w, "  passed first try         %6d of %d fresh  (%.0f%%; %.0f%% of all %d passed)\n",
		b.FirstTry, b.Fresh, b.FirstTryRate*100, b.PassRate*100, b.Batches)
	fmt.Fprintf(w, "  had to be bisected       %6d  (%.0f%% of them; %d named a culprit, %d answered nothing)\n",
		b.Split, b.SplitRate*100, b.Culprits, b.Abandoned)
	if len(b.AbandonedBy) > 0 {
		parts := make([]string, 0, len(b.AbandonedBy))
		for _, a := range b.AbandonedBy {
			parts = append(parts, fmt.Sprintf("%s %d", a.Reason, a.Batches))
		}
		fmt.Fprintf(w, "  abandoned                %6d  (by reason: %s)\n", b.Abandoned, strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "  size                     %6.2f mean, %d biggest, limit %d\n",
		b.MeanSize, b.Biggest, b.Limit)
	// The distribution behind that mean, printed under it: a mean of 2.4
	// made of threes and ones is a different queue from one made of twos,
	// and the pass rate per size is the comparison the decision is
	// actually made from.
	for _, s := range b.Sizes {
		passed := "-"
		if s.Batches > 0 {
			passed = fmt.Sprintf("%d passed (%.0f%%)", s.Passed, float64(s.Passed)/float64(s.Batches)*100)
		}
		fmt.Fprintf(w, "    of %d change(s)         %6d  (%2.0f%% of batches, %s)\n",
			s.Size, s.Batches, s.Share*100, passed)
	}
	if b.AtLimit > 0 {
		fmt.Fprintf(w, "  reached the limit        %6d  (with more queued behind them -- the only\n"+
			"                                   evidence a bigger limit would buy anything)\n", b.AtLimit)
	} else {
		fmt.Fprintln(w, "  reached the limit             0  (nothing was held back by the limit, so\n"+
			"                                   raising it would have changed nothing)")
	}
	fmt.Fprintf(w, "  suites                   %6d spent, %d a one-at-a-time queue would have spent (%+d)\n",
		b.Suites, b.SerialSuites, b.Saved)
	if b.Reaches != nil {
		fmt.Fprintf(w, "  (the batch history is bounded at %d rows and reaches back to %s;"+
			"\n   a window older than that covers fewer batches than its dates suggest.)\n",
			model.MergeBatchHistoryKept, b.Reaches.Format(time.RFC3339))
	}
}

// atMost renders a bucketed percentile as the bound it is, so nobody
// reads it as a measured byte count.
func atMost(bytes int64) string {
	if bytes == 0 {
		return "-"
	}
	return "<=" + strconv.FormatInt(bytes, 10)
}

// printCycles renders the daemon's own tick -- the section that says
// whether the queue_wait stage printed below it was capacity or
// scheduling.
//
// The line the whole section builds to is the scheduling floor:
// tick-to-tick p50 plus dispatch p50 is what a task pays for grain's own
// scheduling with no contention at all, so a queue_wait near it is the
// tick and a queue_wait far above it is the deployment being full. The
// per-reconciler table under it is what says which reconciler to look at
// when the tick itself is the problem, since they run in sequence and a
// slow one delays every decision behind it.
//
// Nothing is printed for a report whose daemon had no ticks to speak
// for: `grain metrics` against a deployment serving a UI without a
// reconcile loop (Enabled false), or against one that has only just
// restarted (n == 0). An empty table there would read as "the tick takes
// no time", which is the opposite of "nobody measured it".
func printCycles(w io.Writer, c ui.MetricsCycles) {
	if !c.Enabled || c.N == 0 {
		return
	}
	fmt.Fprintln(w, "\nreconcile tick (this daemon, since it started -- not stored, so not over the window)")
	fmt.Fprintf(w, "  ticks measured           %6d", c.N)
	if c.Truncated {
		fmt.Fprintf(w, "  (of %d run; older ones forgotten)", c.Observed)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-24s %10s %10s %10s\n", "", "p50", "p90", "max")
	fmt.Fprintf(w, "  %-24s %10s %10s %10s\n", "tick duration",
		preciseSeconds(c.Tick.P50Seconds), preciseSeconds(c.Tick.P90Seconds), preciseSeconds(c.Tick.MaxSeconds))
	fmt.Fprintf(w, "  %-24s %10s %10s %10s\n", "tick to tick",
		preciseSeconds(c.Interval.P50Seconds), preciseSeconds(c.Interval.P90Seconds), preciseSeconds(c.Interval.MaxSeconds))
	fmt.Fprintf(w, "  %-24s %10s %10s %10s\n", "cycle start -> dispatch",
		preciseSeconds(c.DispatchWait.P50Seconds), preciseSeconds(c.DispatchWait.P90Seconds), preciseSeconds(c.DispatchWait.MaxSeconds))
	fmt.Fprintf(w, "  %-24s %10d %10d %10d\n", "daemon commits per tick", c.Commits.P50, c.Commits.P90, c.Commits.Max)
	fmt.Fprintf(w, "  commit-free ticks: %d/%d (%.1f%%)\n", c.CommitFree, c.N, 100*float64(c.CommitFree)/float64(c.N))
	fmt.Fprintf(w, "  scheduling floor: %s  (tick-to-tick p50 + dispatch p50 -- the queue wait a task pays\n"+
		"    for grain's own scheduling with no contention at all)\n",
		preciseSeconds(c.Interval.P50Seconds+c.DispatchWait.P50Seconds))

	if len(c.Reconcilers) > 0 {
		fmt.Fprintf(w, "\n  %-16s %10s %10s %10s %8s\n", "reconciler", "wait p50", "p50", "p90", "failed")
		for _, r := range c.Reconcilers {
			fmt.Fprintf(w, "  %-16s %10s %10s %10s %8d\n", r.Name,
				preciseSeconds(r.Wait.P50Seconds), preciseSeconds(r.Duration.P50Seconds),
				preciseSeconds(r.Duration.P90Seconds), r.Failures)
		}
	}
}

// preciseSeconds renders one of the cycles section's own second counts.
// It is seconds() with a finer hand: a healthy tick is measured in
// milliseconds, and rounding to the whole second the way every other
// number in this report does would print it as "0s" -- which is the one
// answer this section must never give, since "too fast to matter" and
// "nobody measured it" would then look the same.
func preciseSeconds(s float64) string {
	d := time.Duration(s * float64(time.Second))
	switch {
	case d >= time.Minute:
		return d.Round(time.Second).String()
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}

// seconds renders one of the report's own second counts as a duration,
// rounded to the second: a p50 printed as "1h4m12s" is read at a glance,
// where "3852.4" is arithmetic homework.
func Seconds(s float64) string {
	return (time.Duration(s * float64(time.Second))).Round(time.Second).String()
}

// outcomeSummary renders the outcome counts in a stable order -- by count
// descending, so the ending that dominates a window is first, and by name
// where counts tie so two runs of the same report never disagree.
func outcomeSummary(outcomes map[string]int) string {
	if len(outcomes) == 0 {
		return ""
	}
	names := make([]string, 0, len(outcomes))
	for name := range outcomes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if outcomes[names[i]] != outcomes[names[j]] {
			return outcomes[names[i]] > outcomes[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, outcomes[name]))
	}
	return strings.Join(parts, " ")
}

// endingSummary is outcomeSummary over the endings a report splits the
// outcome words into (model.RunEnding), in the same count-descending
// order.
func endingSummary(endings map[model.RunEnding]int) string {
	counts := make(map[string]int, len(endings))
	for ending, n := range endings {
		counts[string(ending)] = n
	}
	return outcomeSummary(counts)
}

// maxGitHubCIRows is how many jobs, flakes and base branches the GitHub
// CI section prints, worst first, for printCIQueueing's reason.
const maxGitHubCIRows = 8

// printGitHubCI renders the CI beside grain's own: GitHub Actions jobs
// and other providers' check runs, labelled with their source so they
// are never read as grain CI's numbers (which the sections above print
// from ci_run, unchanged by any of this).
//
// Silent for a window with no GitHub results, for printCIQueueing's
// reason.
func printGitHubCI(w io.Writer, g ui.MetricsGitHubCI) {
	if g.Jobs == 0 && len(g.BaseBranches) == 0 {
		return
	}
	fmt.Fprintf(w, "\nGitHub CI (source: %s; %d job(s) concluded in the window)\n", g.Source, g.Jobs)
	fmt.Fprintf(w, "  passed / failed / cancelled  %5.0f%% / %3.0f%% / %3.0f%%  (%d / %d / %d)\n",
		g.PassRate*100, g.FailRate*100, g.CancelRate*100, g.Passed, g.Failed, g.Cancelled)
	if g.Skipped+g.Neutral+g.Other > 0 {
		fmt.Fprintf(w, "  in no rate                   %d skipped, %d neutral, %d other\n", g.Skipped, g.Neutral, g.Other)
	}
	if t := g.TimeToGreen; t.Recovered > 0 || t.StillRed > 0 {
		fmt.Fprintf(w, "  time to green                %d commit(s) recovered", t.Recovered)
		if t.Recovered > 0 {
			fmt.Fprintf(w, ", p50 %s, p90 %s, max %s",
				Seconds(t.Duration.P50Seconds), Seconds(t.Duration.P90Seconds), Seconds(t.Duration.MaxSeconds))
		}
		fmt.Fprintf(w, "; %d still red\n", t.StillRed)
		if l := t.Longest; l != nil {
			fmt.Fprintf(w, "    slowest: %s %s @ %s, %s\n", l.Repo, l.Ref, shortSHA(l.Commit), Seconds(l.TookSeconds))
		}
	}
	if len(g.ByJob) > 0 {
		fmt.Fprintf(w, "  %-44s %5s %6s %6s %6s %5s\n", "jobs, most failures first", "n", "pass", "fail", "cancel", "flaky")
		for i, j := range g.ByJob {
			if i >= maxGitHubCIRows {
				break
			}
			fmt.Fprintf(w, "  %-44s %5d %6s %6s %6s %5d\n", j.Repo+" "+j.Job, j.Jobs,
				decidedPercent(j.MetricsGitHubCIOutcomes, j.PassRate), decidedPercent(j.MetricsGitHubCIOutcomes, j.FailRate),
				decidedPercent(j.MetricsGitHubCIOutcomes, j.CancelRate), j.Flaky)
		}
		if more := len(g.ByJob) - maxGitHubCIRows + g.MoreJobs; more > 0 {
			fmt.Fprintf(w, "  ... and %d more job(s)\n", more)
		}
	}
	if len(g.Flaky) > 0 {
		fmt.Fprintf(w, "  flaky: %d commit(s) saw a job fail and then pass\n", g.FlakyCommits)
		for i, f := range g.Flaky {
			if i >= maxGitHubCIRows {
				break
			}
			fmt.Fprintf(w, "    %-44s on %d commit(s), last %s\n", f.Repo+" "+f.Job, f.Commits, shortSHA(f.Last))
		}
	}
	for i, b := range g.BaseBranches {
		if i >= maxGitHubCIRows {
			break
		}
		now := "green now"
		if b.RedNow {
			now = "red now"
		}
		fmt.Fprintf(w, "  base %-39s red %4.0f%% (%s red, %s green), %d red stretch(es), longest %s, %s\n",
			b.Repo+" "+b.Branch, b.RedShare*100, Seconds(b.RedSeconds), Seconds(b.GreenSeconds),
			b.RedStretches, Seconds(b.LongestRedSeconds), now)
	}
}

// decidedPercent is one of a GitHub CI rate's three shares, or "-" when
// nothing it is a share of was decided: a job that only ever skipped or
// came back neutral has no pass rate, not a 0% one.
func decidedPercent(o ui.MetricsGitHubCIOutcomes, rate float64) string {
	if o.Passed+o.Failed+o.Cancelled == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", rate*100)
}

// shortSHA is a commit as people write one.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func printAgents(w io.Writer, a ui.MetricsAgents) {
	if len(a.Templates) == 0 && a.ReportChecks.Wakes == 0 {
		return
	}
	fmt.Fprintln(w, "\nagents")
	for _, t := range a.Templates {
		fmt.Fprintf(w, "  %-24s %4d wake(s)  calls p50 %d, p95 %d  bytes p50 %s, p95 %s  elisions %d  re-reads %d\n",
			t.TemplateID, t.Wakes, t.CallsP50, t.CallsP95,
			model.PromptBytes(int(t.BytesP50)), model.PromptBytes(int(t.BytesP95)),
			t.Elisions, t.ReReads)
		if t.Report.Wakes > 0 {
			fmt.Fprintf(w, "    report checks          %4d wake(s), %d claim(s): uncited %d (%.1f%%), unread %d (%.1f%%), mismatched %d (%.1f%%)\n",
				t.Report.Wakes, t.Report.Claims,
				t.Report.Uncited, t.Report.UncitedRate*100,
				t.Report.Unread, t.Report.UnreadRate*100,
				t.Report.Mismatched, t.Report.MismatchRate*100)
		}
	}
	if r := a.ReportChecks; r.Wakes > 0 {
		fmt.Fprintf(w, "  report checks            %4d wake(s), %d claim(s): uncited %d (%.1f%%), unread %d (%.1f%%), mismatched %d (%.1f%%)\n",
			r.Wakes, r.Claims,
			r.Uncited, r.UncitedRate*100,
			r.Unread, r.UnreadRate*100,
			r.Mismatched, r.MismatchRate*100)
	}
}
