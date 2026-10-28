package mcp

// metrics_tools.go is read_grain_metrics: this deployment's own
// measurement report -- the one `grain metrics` prints and the one the
// UI's Metrics pane draws (pkg/metrics, GET /api/metrics) -- served to a
// run that holds the self-debug grant.
//
// It exists because grain dispatches work about its own numbers and
// handed that work no numbers. A task asked to re-size the tool result
// cap from the tool-use census had no way to read a census even in
// principle: `grain metrics` is a client command against a daemon, and a
// sandbox is given a git credential and nothing else -- no deployment
// address, no token -- so the CLI is unusable there even after building
// it from the checkout. The only route left was an operator pasting a
// report into the task's conversation before dispatch, which works
// exactly once, at whatever window they happened to choose, with no way
// to ask a second question of it.
//
// It sits behind self-debug beside the task readers (task_tools.go) and
// for the same reason: everything it reports is already on a page anyone
// with the UI open can read, so the gate is the grant a human attached
// rather than a confirmation per call, and nothing here can change
// anything. What it reports is deployment-wide rather than about this
// run's own task -- so is every one of the task readers, which will
// happily render a task this run has nothing to do with, and so is what
// ci_repo_scope.go hands a self-debug run for the same stated reason:
// the grant already means "this task is here to work out what grain
// itself did", and a question asked of the whole deployment is the same
// kind of question asked of the same record.
//
// Like those tools it holds no store and no renderer: it takes the
// question, and a MetricsReader implemented on the far side of the
// daemon's REST API (pkg/cli/mcpserver.go's daemonMetrics) comes back
// with the report already rendered. The rendering is the CLI's own
// (pkg/cli/metrics.go's renderMetrics), which is the point -- an
// operator and an agent reading the same deployment should be reading
// the same words -- and it cannot live here, since pkg/ui owns the
// report shape and imports this package by way of
// pkg/capability/selfdebug.

import (
	"context"
	"fmt"
	"strings"
)

// The report's sections, by the names read_grain_metrics takes in its
// "section" argument. They are constants here, rather than strings in a
// switch over in cmd/grain, because both sides have to agree: this
// package offers the list to an agent in the tool's schema and
// cmd/grain's renderer decides what each one prints.
const (
	MetricsSectionThroughput   = "throughput"
	MetricsSectionCapacity     = "capacity"
	MetricsSectionCycles       = "cycles"
	MetricsSectionLatency      = "latency"
	MetricsSectionTools        = "tools"
	MetricsSectionPrompts      = "prompts"
	MetricsSectionChecks       = "checks"
	MetricsSectionCIQueueing   = "ci_queueing"
	MetricsSectionGitHubCI     = "github_ci"
	MetricsSectionHost         = "host"
	MetricsSectionChanges      = "changes"
	MetricsSectionPullRequests = "pull_requests"
	MetricsSectionMerges       = "merges"
	MetricsSectionSandboxes    = "sandboxes"
	MetricsSectionBacklog      = "backlog"
	MetricsSectionGitHub       = "github"
	MetricsSectionAgents       = "agents"
)

// MetricsSections is every section name, in the order the whole report
// prints them.
var MetricsSections = []string{
	MetricsSectionThroughput,
	MetricsSectionCapacity,
	MetricsSectionCycles,
	MetricsSectionLatency,
	MetricsSectionTools,
	MetricsSectionPrompts,
	MetricsSectionChecks,
	MetricsSectionCIQueueing,
	MetricsSectionGitHubCI,
	MetricsSectionHost,
	MetricsSectionChanges,
	MetricsSectionPullRequests,
	MetricsSectionMerges,
	MetricsSectionSandboxes,
	MetricsSectionBacklog,
	MetricsSectionGitHub,
	MetricsSectionAgents,
}

// metricsSectionSummary is what each section is about, said in one
// clause, so that an agent choosing between them in the tool's schema
// does not have to fetch the whole report to find out what "cycles"
// means.
var metricsSectionSummary = map[string]string{
	MetricsSectionThroughput: "tasks filed, completed and attempted, and how attempts ended",
	MetricsSectionCapacity:   "mean concurrent runs against the limit, and how many are live now",
	MetricsSectionCycles:     "the daemon's own reconcile tick, and the scheduling floor under every queue wait",
	MetricsSectionLatency:    "how long each stage of a task's life took",
	MetricsSectionTools:      "the tool-use census: calls per run, errors, timeouts, result sizes and what the cap cut, over the runs that recorded one",
	MetricsSectionPrompts:    "how big the prompts runs were handed came out, and how often the conversation was cut to fit, over the attempts that recorded a prompt",
	MetricsSectionChecks:     "CI waits: how they ended, how long they blocked, pushes before green",
	MetricsSectionCIQueueing: "how much of this deployment's CI job-time went on waiting for a machine rather than on running the tree, what held it, which jobs wore it, and how long each of them ran and waited across the window",
	MetricsSectionGitHubCI: "the CI beside grain's own, sliced by source (github; grain CI is ci_queueing, checks and merges, " +
		"which it never changes): GitHub Actions jobs and other providers' check runs -- pass, fail and cancel rates " +
		"per workflow/job (skipped and neutral apart), time to green per commit, jobs that failed then passed on the " +
		"same commit (flaky), and how long each base branch was red by its GitHub results",
	MetricsSectionHost:         "how hard the machine underneath this deployment was working over the window -- load, cpu and memory, sliced the same way as everything else, so a job that took twice as long can be read against the host that gave it half a machine",
	MetricsSectionChanges:      "when this deployment's own parameters last moved, and which -- the series to read every other series here against",
	MetricsSectionPullRequests: "whether runs open a pull request mid-flight, and whether the ones that do leave fewer red builds",
	MetricsSectionMerges: "what actually landed: changes merged per hour over the window and slice by slice, " +
		"how long a change took from being filed to being merged, and the merge queue's batches -- " +
		"their size distribution, their pass rate, and the suites batching saved or wasted -- " +
		"and the overlap re-check's counters (rechecks): merges it triggered on or skipped, which " +
		"clause fired, what a re-check caught, and skipped merges whose base went red after",
	MetricsSectionSandboxes: "how many sandboxes this deployment has built and what they came to in hours (all time, not the window)",
	MetricsSectionBacklog:   "what is queued right now, and the oldest thing waiting",
	MetricsSectionGitHub: "what this daemon has spent of GitHub's hourly REST budget since it started: by caller " +
		"(reconciler, UI, agent tools) and kind of call, how much was free (304), what is left, how fast it is " +
		"falling and when it runs out at that rate, and whether the ETag cache holds the working set",
	MetricsSectionAgents: "per-template agent telemetry and steward report checks: wakes, calls and result bytes per wake (p50/p95), elided results, re-reads (same id read more than twice in a wake), and report-check claim/uncited/unread/mismatch rates",
}

// MetricsQuery is one call's question: a span, a scope, and which
// section of the report to render.
//
// The three span fields are the words the caller wrote, not parsed
// values, because the parsing is the CLI's (ui.ParseMetricsWindow,
// ui.ParseMetricsMoment) and this package cannot import pkg/ui. The
// reader parses them and answers with the same sentence an operator
// would have got for the same typo.
type MetricsQuery struct {
	// Window is how far back to measure -- "36h", "7d", "2w" -- or "" for
	// the daemon's own default, which is what a bare `grain metrics`
	// asks for too.
	Window string
	// Since and Until are an absolute span instead: a date, or an
	// RFC 3339 timestamp. Until on its own means "a window ending then".
	Since string
	Until string
	// Repos and Authors narrow which tasks the numbers are about, each
	// an "any of", exactly as ?repo= and ?author= do.
	Repos   []string
	Authors []string
	// Section is one of MetricsSections, or "" for the whole report.
	Section string
}

// MetricsReader is the one read these tools need: the report, rendered.
//
// Rendered rather than structured, unlike TaskReader next door, because
// there is nothing here for this package to decide. A report is a page
// of tables whose every column carries a caveat about what it does and
// does not count, the CLI already writes all of it, and re-describing
// that shape here to re-render it identically would be a second copy of
// a page of prose to keep in step with the first.
type MetricsReader interface {
	Metrics(ctx context.Context, q MetricsQuery) (string, error)
}

// NewMetricsTools returns the one tool a self-debug run gets for reading
// this deployment's own measurements.
//
// reader nil returns it anyway, refusing every call with a sentence
// saying why -- what lets each agent framework's allowedTools enumerate
// the names this package registers without holding a live reader, the
// same contract NewTaskTools keeps.
func NewMetricsTools(reader MetricsReader) []Tool {
	return []Tool{readMetricsTool(reader)}
}

// noMetricsReader is the answer when there is no route to the daemon
// that computes the report -- a sentence about this run rather than
// "unknown tool", which would read like a bug in grain rather than a
// fact about how this one was dispatched.
const noMetricsReader = "This run cannot read grain's own measurements: it holds the self-debug grant " +
	"but was dispatched with no route back to the grain daemon that computes the report. Say so if " +
	"it matters to what you were asked to do; nothing you can do from here will change it."

// elisionAdviceMetrics is what to do about a report too long to carry
// back. The generic sentence ("narrow the request") is no use here: the
// report's length is a page of tables rather than a long string, and the
// two things that actually shorten it are asking for one section and
// asking over less time.
const elisionAdviceMetrics = "ask for one part of it at a time with \"section\" -- \"tools\" is the " +
	"tool-use census, \"prompts\" the prompt sizes, \"checks\" the CI waits -- or ask for a narrower " +
	"window, which drops the sections nothing was measured in"

var metricsInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"window": map[string]any{
			"type": "string",
			"description": "how far back to measure: a duration like 36h, or a count of days or " +
				"weeks like 7d. Omit for this deployment's default window (7 days).",
		},
		"since": map[string]any{
			"type": "string",
			"description": "measure from this moment instead of a window: a date like 2026-01-31, " +
				"or an RFC 3339 timestamp (a bare date is midnight UTC). Do not give both this and " +
				"\"window\".",
		},
		"until": map[string]any{
			"type":        "string",
			"description": "measure up to this moment rather than up to now, said the same way",
		},
		"repo": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "string",
			},
			"description": "only count tasks whose work lands in these repos, owner/name (any of them)",
		},
		"author": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "string",
			},
			"description": "only count tasks attributed to these authors (any of them)",
		},
		"section": map[string]any{
			"type":        "string",
			"enum":        MetricsSections,
			"description": metricsSectionArgDescription(),
		},
	},
}

// metricsSectionArgDescription spells out what each section holds, built
// from the summaries above so that the schema an agent reads and the
// list the renderer walks cannot drift apart.
func metricsSectionArgDescription() string {
	parts := make([]string, 0, len(MetricsSections))
	for _, name := range MetricsSections {
		parts = append(parts, fmt.Sprintf("%s (%s)", name, metricsSectionSummary[name]))
	}
	return "which part of the report to render, for a narrower answer than the whole page of " +
		"tables: " + strings.Join(parts, "; ") + ". Omit for all of it. The window and scope are " +
		"printed above whichever section you ask for."
}

func readMetricsTool(reader MetricsReader) Tool {
	return Tool{
		Name:          "read_grain_metrics",
		ElisionAdvice: elisionAdviceMetrics,
		Description: "Read this grain deployment's own measurements -- the report `grain metrics` " +
			"prints and the Metrics pane draws, computed fresh from the task store: throughput and " +
			"latency, how full the deployment was, the tool-use census (calls per run, error rates, " +
			"result sizes and what the result cap cut), prompt sizes, CI waits and what sandboxes " +
			"have cost. Read-only, and it needs no permission. This is the answer to any question " +
			"of the form \"what do this deployment's numbers actually say\" -- do not reason from " +
			"guesses about how runs behave when you can measure them. Narrow it with \"window\" (or " +
			"\"since\"/\"until\"), with \"repo\"/\"author\", and with \"section\" for one part of " +
			"the report rather than the whole page. Read each section's own denominator before you " +
			"quote a share from it: a section counts the runs that recorded what it measures, which " +
			"is not every run in the window -- each measurement started being recorded at some " +
			"point in this deployment's life, so a window reaching back before that reports a share " +
			"of a smaller population than its dates suggest, and every section says in its header " +
			"how many samples it actually had.",
		InputSchema: metricsInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			if reader == nil {
				return Result{Text: noMetricsReader, IsError: true}
			}
			section, _ := argString(args, "section")
			section = strings.TrimSpace(strings.ToLower(section))
			if section != "" && !knownMetricsSection(section) {
				return Result{Text: fmt.Sprintf("There is no %q section in this report. It has %s "+
					"-- or omit \"section\" for all of them.", section,
					strings.Join(MetricsSections, ", ")), IsError: true}
			}
			window, _ := argString(args, "window")
			since, _ := argString(args, "since")
			until, _ := argString(args, "until")
			report, err := reader.Metrics(ctx, MetricsQuery{
				Window:  window,
				Since:   since,
				Until:   until,
				Repos:   argStrings(args, "repo"),
				Authors: argStrings(args, "author"),
				Section: section,
			})
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading grain's own measurements: %v", err), IsError: true}
			}
			if strings.TrimSpace(report) == "" {
				return Result{Text: "This deployment's report came back empty, which should not " +
					"happen: it always carries at least the window it covers. Treat it as a fault " +
					"in grain rather than as a deployment that has measured nothing."}
			}
			return Result{Text: elideMiddle(report, int(maxToolResultBytes.Load()), elisionAdviceMetrics)}
		},
	}
}

func knownMetricsSection(name string) bool {
	for _, known := range MetricsSections {
		if known == name {
			return true
		}
	}
	return false
}
