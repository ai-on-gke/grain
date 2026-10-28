package cli

// What `grain metrics` asks for and what it prints back. The report
// itself is pkg/metrics' and pkg/ui's to get right; what is only
// decidable here is that the CLI can ask the endpoint's whole question
// -- a span and a scope -- and that a scoped report at a terminal says
// what it was scoped to, since the printout is what gets pasted into a
// pull request comment or a weekly note.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestPrintMetricsSaysWhatItWasScopedTo(t *testing.T) {
	rep := ui.MetricsReport{
		Since: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), WindowSeconds: 31 * 24 * 60 * 60,
		Scope: ui.MetricsScope{Repos: []string{"acme/widgets", "acme/docs"}, Authors: []string{"ada"}},
	}
	out := captureStdout(t, func() { (&printer{}).metrics(rep) })
	// The board's own phrasing, so a report read on a page and one read
	// at a terminal describe themselves the same way -- including the
	// "or", which is what says the two repos are an "any of" rather than
	// a report about tasks somehow in both.
	if !strings.Contains(out, "scope:") ||
		!strings.Contains(out, "tasks in acme/widgets or acme/docs, by ada") {
		t.Errorf("a scoped report does not say what it covers:\n%s", out)
	}
	// And the window is still the first thing on screen: the scope says
	// which tasks, not which measurements.
	if !strings.HasPrefix(out, "window:") {
		t.Errorf("output does not begin with the window:\n%s", out)
	}

	// A report over everything says nothing, which is what a report has
	// always been -- a "scope: every task" line on every printout is
	// noise that trains people to skip the line that matters.
	rep.Scope = ui.MetricsScope{}
	out = captureStdout(t, func() { (&printer{}).metrics(rep) })
	if strings.Contains(out, "scope:") {
		t.Errorf("an unscoped report announced a scope:\n%s", out)
	}
}

// metricsServer starts a real daemon-shaped UI/API over an embedded
// store, with two tasks in two repos filed by two authors, and returns
// its URL.
func metricsServer(t *testing.T) string {
	t.Helper()
	url, _ := metricsServerAndStore(t)
	return url
}

// metricsServerAndStore is metricsServer, also handing back the store
// under it for a test that has to break something in it.
func metricsServerAndStore(t *testing.T) (string, *model.Store) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	for _, task := range []struct{ repo, author string }{
		{"acme/widgets", "ada"},
		{"acme/docs", "grace"},
	} {
		client := ui.NewClient(ui.Config{
			Actor:        ui.DefaultActor(task.author),
			Capabilities: ui.OfferedCapabilities(),
		}, store)
		if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "fix the thing in " + task.repo, Description: "please",
			Approved: true, Repo: task.repo,
		}); err != nil {
			t.Fatalf("creating a task in %s: %v", task.repo, err)
		}
	}
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("tester"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return srv.URL, store
}

// The dispatch itself, against a real server: -repo and -author have to
// reach the endpoint's own ?repo= and ?author= parameters, and the
// report has to come back narrowed to them.
func TestRunCLIMetricsScopesTheReport(t *testing.T) {
	server := metricsServer(t)

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "metrics", "-repo", "acme/widgets"}); err != nil {
			t.Fatalf("grain metrics -repo: %v", err)
		}
	})
	rep := decodeReport(t, out)
	if got, want := rep.Throughput.TasksFiled, 1; got != want {
		t.Errorf("tasks filed in acme/widgets = %d, want %d -- the other repo's task is out of scope", got, want)
	}
	if len(rep.Scope.Repos) != 1 || rep.Scope.Repos[0] != "acme/widgets" {
		t.Errorf("scope = %+v, want the repo the command named", rep.Scope)
	}

	// Repeated rather than comma-joined, and an "any of" when it is.
	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "metrics",
			"-repo", "acme/widgets", "-repo", "acme/docs"}); err != nil {
			t.Fatalf("grain metrics -repo -repo: %v", err)
		}
	})
	if got, want := decodeReport(t, out).Throughput.TasksFiled, 2; got != want {
		t.Errorf("tasks filed over both repos = %d, want %d", got, want)
	}

	// The other half of the scope, and it narrows against the repo
	// rather than replacing it: ada filed nothing in acme/docs.
	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "metrics",
			"-repo", "acme/docs", "-author", "ada"}); err != nil {
			t.Fatalf("grain metrics -repo -author: %v", err)
		}
	})
	if got, want := decodeReport(t, out).Throughput.TasksFiled, 0; got != want {
		t.Errorf("tasks ada filed in acme/docs = %d, want %d", got, want)
	}

	// And a bare invocation is still every task, over the server's own
	// default window.
	out = captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "metrics"}); err != nil {
			t.Fatalf("grain metrics: %v", err)
		}
	})
	rep = decodeReport(t, out)
	if got, want := rep.Throughput.TasksFiled, 2; got != want {
		t.Errorf("tasks filed unscoped = %d, want %d", got, want)
	}
	if got, want := rep.WindowSeconds, ui.DefaultMetricsWindow.Seconds(); got != want {
		t.Errorf("window = %vs, want the server's own default of %vs", got, want)
	}
}

// An absolute span, which is what "how did last month go" is: a span
// that does not end now, and that no -window can say.
func TestRunCLIMetricsTakesAnAbsoluteSpan(t *testing.T) {
	server := metricsServer(t)
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-json", "-server", server, "metrics",
			"-since", "2020-01-01", "-until", "2020-02-01"}); err != nil {
			t.Fatalf("grain metrics -since -until: %v", err)
		}
	})
	rep := decodeReport(t, out)
	if !rep.Since.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!rep.Until.Equal(time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("span = %s -> %s, want the dates the command named", rep.Since, rep.Until)
	}
	// The tasks were filed today, so a report about a month in 2020
	// counts none of them: the span reached the daemon rather than being
	// dropped on the way.
	if got := rep.Throughput.TasksFiled; got != 0 {
		t.Errorf("tasks filed in January 2020 = %d, want 0", got)
	}
}

// A malformed span is a message from the command the operator ran. The
// server here is an address nothing is listening on, so a test that
// passes proves the request was never made.
func TestCmdMetricsRejectsAnImpossibleSpanBeforeAsking(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a window nobody can parse", []string{"-window", "banana"}, "window"},
		{"a date nobody can parse", []string{"-since", "last tuesday"}, "moment"},
		{"a span said twice", []string{"-window", "7d", "-since", "2026-01-31"}, "one or the other"},
		{"a span that runs backwards", []string{"-since", "2026-02-01", "-until", "2026-01-31"}, "no span between them"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runCLIWithAccessCode(append([]string{"-server", "http://127.0.0.1:1", "metrics"}, tc.args...))
			if err == nil {
				t.Fatalf("grain metrics %v: no error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q rather than a failed request", err, tc.want)
			}
		})
	}
}

func decodeReport(t *testing.T, out string) ui.MetricsReport {
	t.Helper()
	var rep ui.MetricsReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return rep
}

// The prompt section printed at a terminal. What it must not do is print
// the distribution without the cut line: the thread a prompt carries is
// capped, so these sizes are what runs were given rather than what they
// would have been given, and only the cut count says how often the cap
// decided that.
func TestPrintMetricsPrintsPromptSizesWithTheirCensorship(t *testing.T) {
	rep := ui.MetricsReport{
		Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), WindowSeconds: 7 * 24 * 60 * 60,
		Prompts: ui.MetricsPrompts{
			Runs: 40,
			Bytes: ui.MetricsSize{
				N: 40, MeanBytes: 30000, MaxBytes: 250000,
				P50AtMost: 32767, P95AtMost: 131071, P99AtMost: 262143,
			},
			Large: 4, LargeShare: 0.1,
			WarnTokens: model.PromptWarnTokens, ContextTokens: model.PromptContextTokens,
			ThreadCut: 8, ThreadCutShare: 0.2,
			Heaviest: []ui.MetricsTaskPrompts{
				{TaskID: "a1b2", Runs: 6, MaxBytes: 250000, MaxTokens: 62500, Attempt: 6, ThreadCut: 3},
			},
		},
	}
	out := captureStdout(t, func() { (&printer{}).metrics(rep) })

	for _, want := range []string{
		"prompts (40 attempt(s)",
		"244 KB", // the largest prompt, in the units a reader judges one by
		"over the warning line",
		"conversation cut to fit",
		"task a1b2", // where to look, which a distribution cannot say
		"(attempt 6)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the prompt section does not say %q:\n%s", want, out)
		}
	}

	// And nothing at all for a deployment whose runs recorded no prompt:
	// zeroes there would read as "grain hands its runs nothing".
	bare := captureStdout(t, func() { (&printer{}).metrics(ui.MetricsReport{}) })
	if strings.Contains(bare, "prompts (") {
		t.Errorf("a report with no prompts measured printed the section anyway:\n%s", bare)
	}
}

// The lifetime sandbox figures at a terminal (grain/task-315): the
// operator's half of the same answer an agent gets over the API, and the
// half that has to say what a number quoted out of it is "since".
func TestPrintMetricsReportsWhatSandboxesHaveCost(t *testing.T) {
	counted := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	rep := ui.MetricsReport{
		Since: counted, Until: counted.Add(24 * time.Hour), WindowSeconds: 24 * 60 * 60,
		Sandboxes: &ui.SandboxLifetime{
			Total: ui.SandboxUsageTotals{
				Attempted: 2416, Created: 2413, Failed: 3, Finished: 2410,
				Unaccounted: 3, Seconds: 4334400, Hours: 1204, MeanSeconds: 1798.5,
				Since: counted,
			},
			Kinds: []ui.SandboxUsageTotals{
				{Kind: "agent", Attempted: 46, Created: 46, Finished: 46,
					Seconds: 10800, Hours: 3, MeanSeconds: 234.8, Since: counted},
				{Kind: "ci", Attempted: 2370, Created: 2367, Failed: 3, Finished: 2364,
					Unaccounted: 3, Seconds: 4323600, Hours: 1201, MeanSeconds: 1828.9, Since: counted},
			},
		},
	}
	out := captureStdout(t, func() { (&printer{}).metrics(rep) })

	if !strings.Contains(out, "sandboxes (since 2026-09-19T01:00:00Z") {
		t.Errorf("the section does not say what its figures are since:\n%s", out)
	}
	// Said in the heading, in the loudest terms a column will take: this
	// is the one section of the report that is not over the window
	// printed above it, and a number pasted into a note under "window:
	// 24h" would be wrong by the life of the deployment.
	if !strings.Contains(out, "NOT over the window") {
		t.Errorf("the section does not disclaim the report's own window:\n%s", out)
	}
	for _, want := range []string{"agent", "ci", "all", "2367", "1201", "1204"} {
		if !strings.Contains(out, want) {
			t.Errorf("the section does not mention %q:\n%s", want, out)
		}
	}

	// A deployment that has counted nothing yet prints no section at
	// all, rather than a row of noughts that reads as a deployment
	// building no sandboxes.
	rep.Sandboxes = &ui.SandboxLifetime{}
	if out := captureStdout(t, func() { (&printer{}).metrics(rep) }); strings.Contains(out, "sandboxes (") {
		t.Errorf("an uncounted deployment still printed a sandbox section:\n%s", out)
	}
	rep.Sandboxes = nil
	if out := captureStdout(t, func() { (&printer{}).metrics(rep) }); strings.Contains(out, "sandboxes (") {
		t.Errorf("a report with no sandbox figures still printed a section:\n%s", out)
	}
}

// The report rendered one section at a time, which is what
// read_grain_metrics asks for (mcp.MetricsSections): a run wanting the
// tool census should not have to spend its context on nine other tables
// to reach it.
func TestRenderMetricsSectionRendersOneSectionUnderTheHeader(t *testing.T) {
	t.Parallel()
	rep := ui.MetricsReport{
		Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), WindowSeconds: 7 * 24 * 60 * 60,
		Scope: ui.MetricsScope{Repos: []string{"acme/widgets"}},
		Tools: ui.MetricsTools{
			Runs: 23, Calls: 1254, Errored: 31, ErroredShare: 0.025,
			ByTool: []ui.MetricsToolUse{{Name: "run_command", Runs: 23, Calls: 1162}},
		},
		Backlog: ui.MetricsBacklog{ByState: map[model.State]int{model.StateQueued: 4}},
	}

	whole := renderedMetrics(rep, "")
	if !strings.Contains(whole, "tool use (") || !strings.Contains(whole, "backlog (") {
		t.Fatalf("the whole report is missing a section:\n%s", whole)
	}

	only := renderedMetrics(rep, mcp.MetricsSectionTools)
	if !strings.Contains(only, "tool use (23 run(s)") || !strings.Contains(only, "run_command") {
		t.Errorf("the tools section is not in its own rendering:\n%s", only)
	}
	if strings.Contains(only, "backlog (") || strings.Contains(only, "throughput") {
		t.Errorf("asking for one section rendered others too:\n%s", only)
	}
	// The window and the scope stay: a census quoted without them is a
	// number waiting to be attached to the wrong question.
	if !strings.HasPrefix(only, "window:") || !strings.Contains(only, "acme/widgets") {
		t.Errorf("a single section lost the header it was measured under:\n%s", only)
	}
}

// A section this report has nothing to say about answers in words.
// Every printer is deliberately silent where nothing was measured, and
// silence under a bare header is the one answer a reader cannot tell
// apart from "grain does not measure that".
func TestRenderMetricsSaysWhenASectionHasNothingInIt(t *testing.T) {
	t.Parallel()
	out := renderedMetrics(ui.MetricsReport{}, mcp.MetricsSectionTools)
	if !strings.Contains(out, "Nothing to report under \"tools\"") {
		t.Errorf("an empty section said nothing about being empty:\n%s", out)
	}
	if !strings.Contains(out, "wider window") {
		t.Errorf("an empty section does not say what to try instead:\n%s", out)
	}
}

// The renderer's sections and the names read_grain_metrics offers an
// agent are one list. A section added on one side only is either a name
// that renders nothing or a table no tool can ask for, and both are
// silent failures.
func TestMetricsSectionsMatchTheToolsOwnList(t *testing.T) {
	t.Parallel()
	rendered := make(map[string]bool, len(daemon.MetricsSections))
	for _, s := range daemon.MetricsSections {
		if rendered[s.Name] {
			t.Errorf("section %q is rendered twice", s.Name)
		}
		rendered[s.Name] = true
	}
	offered := make(map[string]bool, len(mcp.MetricsSections))
	for _, name := range mcp.MetricsSections {
		offered[name] = true
		if !rendered[name] {
			t.Errorf("read_grain_metrics offers section %q, which nothing renders", name)
		}
	}
	for name := range rendered {
		if !offered[name] {
			t.Errorf("the renderer has a section %q that read_grain_metrics cannot ask for", name)
		}
	}
}

// The hop read_grain_metrics actually takes: a real daemon in front of a
// real store, asked through the same mcp.MetricsReader the tool calls.
// pkg/mcp's own tests answer only for the question; this answers for the
// report coming back.
func TestDaemonMetricsReadsTheDeploymentsOwnReport(t *testing.T) {
	server := metricsServer(t)
	reader := daemon.DaemonMetrics{Client: accessCodeClient(server)}

	res := callMetricsTool(t, reader, map[string]any{"window": "30d"})
	if res.IsError {
		t.Fatalf("read_grain_metrics reported an error: %s", res.Text)
	}
	for _, want := range []string{"window:", "throughput", "tasks filed"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the report does not contain %q:\n%s", want, res.Text)
		}
	}

	// The scope reaches the daemon, and comes back said in words.
	res = callMetricsTool(t, reader, map[string]any{"repo": "acme/widgets"})
	if !strings.Contains(res.Text, "tasks in acme/widgets") {
		t.Errorf("a scoped report does not say what it covers:\n%s", res.Text)
	}

	// And a span nobody can parse is refused with the sentence `grain
	// metrics` refuses it with, before any request is made.
	res = callMetricsTool(t, reader, map[string]any{"window": "banana"})
	if !res.IsError || !strings.Contains(res.Text, "window \"banana\"") {
		t.Errorf("a malformed window = %q, want the CLI's own explanation", res.Text)
	}
}

// One input the store cannot read costs the sections built from it and
// nothing else. On 2026-10-06 a failing prompt-size query made every
// read_grain_metrics call fail, section=throughput included, because the
// report read every input whichever section was asked for.
func TestDaemonMetricsSurvivesAFailingPromptRead(t *testing.T) {
	server, store := metricsServerAndStore(t)
	// Store.RunPrompts reads task_run.prompt and nothing else in the
	// report does, so taking the column away fails that one read.
	if _, err := store.DB().Exec("ALTER TABLE `task_run` RENAME COLUMN `prompt` TO `prompt_gone`"); err != nil {
		t.Fatal(err)
	}
	reader := daemon.DaemonMetrics{Client: accessCodeClient(server)}

	res := callMetricsTool(t, reader, map[string]any{"window": "30d", "section": "throughput"})
	if res.IsError || !strings.Contains(res.Text, "tasks filed") {
		t.Fatalf("section=throughput failed with the prompt read broken:\n%s", res.Text)
	}

	// The whole report says which section it lost, and where to look,
	// in that section's place -- and is whole everywhere else.
	res = callMetricsTool(t, reader, map[string]any{"window": "30d"})
	if res.IsError {
		t.Fatalf("the whole report failed with only the prompt read broken:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "prompts: unavailable -- internal server error (ref ") {
		t.Errorf("the whole report does not mark the prompts section failed:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "tasks filed") {
		t.Errorf("the whole report lost its throughput section too:\n%s", res.Text)
	}

	// The one section that cannot be built is an error, not an empty page.
	if res := callMetricsTool(t, reader, map[string]any{"window": "30d", "section": "prompts"}); !res.IsError {
		t.Errorf("section=prompts succeeded with its only input unreadable:\n%s", res.Text)
	}
}

// The prompt read above is one input; the same has to hold for whichever
// input breaks. Each case takes one input away and asks for every section
// by name, over GET /api/metrics?section= and over read_grain_metrics:
// only the section built from that input may fail, and every other one
// answers whole. A section whose inputs list (metricsSectionInputs) named
// an input it does not need, or missed one it does, would fail here
// (docs/test-runs/2026-10-07-daemon-demo-move.md).
func TestDaemonMetricsEverySectionSurvivesAnotherSectionsFailingInput(t *testing.T) {
	for _, tc := range []struct {
		name, breakSQL, fails string
	}{
		// Store.RunPrompts.
		{"prompt sizes", "ALTER TABLE `task_run` RENAME COLUMN `prompt` TO `prompt_gone`", mcp.MetricsSectionPrompts},
		// Store.RunCheckWaits.
		{"CI waits", "ALTER TABLE `task_run_check_wait` RENAME TO `task_run_check_wait_gone`", mcp.MetricsSectionChecks},
		// Store.CIJobTimings.
		{"CI jobs", "ALTER TABLE `ci_job` RENAME TO `ci_job_gone`", mcp.MetricsSectionCIQueueing},
		// Store.RunTranscripts.
		{"transcript sizes", "ALTER TABLE `task_run` RENAME COLUMN `transcript` TO `transcript_gone`", mcp.MetricsSectionMerges},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := metricsServerAndStore(t)
			if _, err := store.DB().Exec(tc.breakSQL); err != nil {
				t.Fatal(err)
			}
			reader := daemon.DaemonMetrics{Client: accessCodeClient(server)}
			for _, section := range mcp.MetricsSections {
				resp, err := accessGet(server + "/api/metrics?window=30d&section=" + section)
				if err != nil {
					t.Fatal(err)
				}
				var rep ui.MetricsReport
				decodeErr := json.NewDecoder(resp.Body).Decode(&rep)
				resp.Body.Close()
				res := callMetricsTool(t, reader, map[string]any{"window": "30d", "section": section})

				if section == tc.fails {
					if resp.StatusCode != http.StatusInternalServerError {
						t.Errorf("section=%s: status = %d, want 500 with its input unreadable", section, resp.StatusCode)
					}
					if !res.IsError {
						t.Errorf("read_grain_metrics section=%s succeeded with its input unreadable:\n%s", section, res.Text)
					}
					continue
				}
				if resp.StatusCode != http.StatusOK || decodeErr != nil || len(rep.Failed) != 0 {
					t.Errorf("section=%s: status = %d (decode: %v), Failed = %+v; want 200 and nothing failed",
						section, resp.StatusCode, decodeErr, rep.Failed)
				}
				if res.IsError || strings.Contains(res.Text, "unavailable -- internal server error") {
					t.Errorf("read_grain_metrics section=%s failed with only the %s read broken:\n%s", section, tc.name, res.Text)
				}
			}

			// The whole report loses that one section and no other.
			res := callMetricsTool(t, reader, map[string]any{"window": "30d"})
			if res.IsError {
				t.Fatalf("the whole report failed with only the %s read broken:\n%s", tc.name, res.Text)
			}
			if got := strings.Count(res.Text, ": unavailable -- internal server error"); got != 1 ||
				!strings.Contains(res.Text, tc.fails+": unavailable -- internal server error (ref ") {
				t.Errorf("the whole report marks %d sections failed, want only %s:\n%s", got, tc.fails, res.Text)
			}
		})
	}
}

// The merges section: what landed, how long it took to land, and what
// the queue's batching cost getting it there. It is the section a nightly
// tuning job asks for by name when it is deciding the batch size, so it
// has to render on its own -- and it has to print the wait beside the
// pass rate, since raising the size trades one for the other.
func TestRenderMetricsRendersTheMergesSection(t *testing.T) {
	t.Parallel()
	reaches := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rep := ui.MetricsReport{
		Since: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), WindowSeconds: 24 * 60 * 60,
		Merges: ui.MetricsMerges{
			Landed: 17, LandedPerHour: 0.71, LandedPerDay: 17,
			FiledToLanded:     ui.MetricsDistribution{N: 17, P50Seconds: 7200, P90Seconds: 18000, MaxSeconds: 36000},
			CompletedToLanded: ui.MetricsDistribution{N: 17, P50Seconds: 1800, P90Seconds: 5400, MaxSeconds: 9000},
			Batches: ui.MetricsMergeBatches{
				Batches: 9, Changes: 17, Fresh: 7, FirstTry: 5, FirstTryRate: 5.0 / 7,
				Passed: 6, PassRate: 6.0 / 9, Split: 2, SplitRate: 2.0 / 9, Culprits: 1, Abandoned: 3,
				AbandonedBy: []ui.MetricsMergeBatchAbandonReason{
					{Reason: "interrupted", Batches: 2}, {Reason: "base_moved", Batches: 1},
				},
				MeanSize: 2.4, Biggest: 3, Limit: 3, AtLimit: 4,
				Suites: 15, SerialSuites: 34, Saved: 19, Reaches: &reaches,
				Sizes: []ui.MetricsMergeBatchSize{
					{Size: 2, Batches: 4, Passed: 3, Share: 4.0 / 9},
					{Size: 3, Batches: 5, Passed: 3, Share: 5.0 / 9},
				},
			},
			BaseHolds: ui.MetricsBaseHolds{
				Passes: 4, Open: 1,
				Peak:   ui.MetricsCount{N: 4, P50: 2, P90: 5, Max: 6},
				Total:  ui.MetricsCount{N: 4, P50: 3, P90: 7, Max: 8, Total: 15},
				PRHeld: ui.MetricsDistribution{N: 4, P50Seconds: 2.5 * 3600, P90Seconds: 9 * 3600, MaxSeconds: 12 * 3600},
				Length: ui.MetricsDistribution{N: 4, P50Seconds: 3600, P90Seconds: 4 * 3600, MaxSeconds: 5 * 3600},
			},
			Rechecks: ui.MetricsMergeRechecks{
				Merges: 10, Triggered: 4, Skipped: 6, TriggerRate: 0.4,
				Rechecked: 3, TriggeredAndCaught: 1, SkippedAndCaught: 0,
				SkippedBaseRed: 2,
				Clauses: map[string]int{
					"same-package": 3,
					"same-file":    1,
				},
				Packages: map[string]int{
					"pkg/orchestrator": 3,
				},
				Modes: map[string]int{
					"observe": 7,
					"enforce": 3,
				},
				Reaches: &reaches,
			},
			Buckets: []ui.MetricsMergeBucket{{Landed: 8, PerHour: 0.67}, {Landed: 9, PerHour: 0.75}},
		},
		Tools: ui.MetricsTools{Runs: 3, Calls: 9, ByTool: []ui.MetricsToolUse{{Name: "run_command", Calls: 9}}},
	}

	only := renderedMetrics(rep, mcp.MetricsSectionMerges)
	for _, want := range []string{
		"merges (17 change(s) landed",
		"filed -> landed",
		"landed per slice",
		"passed first try",
		"of 3 change(s)",
		"reached the limit",
		"abandoned                     3  (by reason: interrupted 2, base_moved 1)",
		"base-branch holds (4 repair pass(es) finished, 1 still open)",
		"peak PRs held            p50 2, p90 5, max 6",
		"total PRs held           p50 3, p90 7, max 8 (15 across all passes)",
		"PR-hours held            p50 2.5, p90 9.0, max 12.0",
		"overlap re-checks (10 merge(s) evaluated)",
		"triggered                   4  (4 triggered, 40%; 6 skipped)",
		"clauses                same-package 3, same-file 1",
		"hot Go packages        pkg/orchestrator 3",
		"re-checked                  3  (caught 1 triggered, 0 skipped)",
		"skipped, base went red      2",
		"modes                  observe 7, enforce 3",
	} {
		if !strings.Contains(only, want) {
			t.Errorf("the merges section does not say %q:\n%s", want, only)
		}
	}
	if strings.Contains(only, "tool use (") {
		t.Errorf("asking for the merges section rendered the census too:\n%s", only)
	}
	if !strings.Contains(renderedMetrics(rep, ""), "merges (") {
		t.Errorf("the whole report is missing the merges section")
	}

	// A window whose only merges activity is an evaluated overlap
	// re-check still renders the section rather than claiming nothing
	// happened.
	recheckOnly := renderedMetrics(ui.MetricsReport{
		Merges: ui.MetricsMerges{
			Rechecks: ui.MetricsMergeRechecks{Merges: 1, Triggered: 1, TriggerRate: 1.0},
		},
	}, mcp.MetricsSectionMerges)
	if !strings.Contains(recheckOnly, "overlap re-checks (1 merge(s) evaluated)") {
		t.Errorf("a merges section with only recheck history did not render it:\n%s", recheckOnly)
	}

	// A window in which nothing landed and no batch finished prints
	// nothing at all, so the single-section path says so in words rather
	// than showing a table of zeroes.
	quiet := renderedMetrics(ui.MetricsReport{}, mcp.MetricsSectionMerges)
	if !strings.Contains(quiet, "Nothing to report under \"merges\"") {
		t.Errorf("an empty merges section printed something:\n%s", quiet)
	}
}

func renderedMetrics(rep ui.MetricsReport, section string) string {
	var out strings.Builder
	daemon.RenderMetrics(&out, rep, section)
	return out.String()
}

func callMetricsTool(t *testing.T, reader mcp.MetricsReader, args map[string]any) mcp.Result {
	t.Helper()
	for _, tool := range mcp.NewMetricsTools(reader) {
		if tool.Name == "read_grain_metrics" {
			return tool.Handler(context.Background(), args)
		}
	}
	t.Fatalf("NewMetricsTools registered no read_grain_metrics")
	return mcp.Result{}
}

// The CI queueing section came in from main after this branch split the
// report into sections, and its own section name is what a run asking
// "is this deployment short of machines?" reaches for. It renders on its
// own, under the header, and nothing else does.
func TestRenderMetricsRendersTheCIQueueingSection(t *testing.T) {
	t.Parallel()
	rep := ui.MetricsReport{
		Since: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), WindowSeconds: 24 * 60 * 60,
		CIQueueing: ui.MetricsCIQueueing{
			Jobs: 64, Waited: 21, Cached: 9,
			QueuedSeconds: 3600, BusySeconds: 7200, QueuedShare: 1.0 / 3.0,
			ByCause: []ui.MetricsCIQueueCause{{Cause: "memory", Share: 0.8, QueuedSeconds: 2880, Jobs: 15}},
		},
		Tools: ui.MetricsTools{Runs: 3, Calls: 9, ByTool: []ui.MetricsToolUse{{Name: "run_command", Calls: 9}}},
	}

	only := renderedMetrics(rep, mcp.MetricsSectionCIQueueing)
	for _, want := range []string{"CI queueing (64 job(s)", "9 cache hit(s) left out", "memory"} {
		if !strings.Contains(only, want) {
			t.Errorf("the CI queueing section does not say %q:\n%s", want, only)
		}
	}
	if strings.Contains(only, "tool use (") {
		t.Errorf("asking for the CI queueing section rendered the census too:\n%s", only)
	}
	// And it is in the whole report, in main's own order: after the CI
	// waits it belongs beside, before the pull requests.
	whole := renderedMetrics(rep, "")
	if !strings.Contains(whole, "CI queueing (") {
		t.Errorf("the whole report is missing the CI queueing section:\n%s", whole)
	}
}

// The GitHub CI section renders on its own under its own name, labelled
// with its source, and in the whole report straight after grain's own CI
// queueing -- the two sources side by side, never mixed.
func TestRenderMetricsRendersTheGitHubCISection(t *testing.T) {
	t.Parallel()
	rep := ui.MetricsReport{
		Since: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), WindowSeconds: 24 * 60 * 60,
		CIQueueing: ui.MetricsCIQueueing{Jobs: 4, QueuedSeconds: 60, BusySeconds: 60, QueuedShare: 0.5},
		GitHubCI: ui.MetricsGitHubCI{
			Source: "github",
			MetricsGitHubCIOutcomes: ui.MetricsGitHubCIOutcomes{
				Jobs: 10, Passed: 6, Failed: 2, Cancelled: 1, Skipped: 1, PassRate: 6.0 / 9, FailRate: 2.0 / 9, CancelRate: 1.0 / 9,
			},
			ByJob: []ui.MetricsGitHubCIJob{{Source: "github", Repo: "acme/widgets", Job: "CI / test",
				MetricsGitHubCIOutcomes: ui.MetricsGitHubCIOutcomes{Jobs: 5, Failed: 2, FailRate: 0.4}, Flaky: 1}},
			TimeToGreen: ui.MetricsGitHubCITimeToGreen{Recovered: 1, StillRed: 1,
				Duration: ui.MetricsDistribution{N: 1, P50Seconds: 1800, P90Seconds: 1800, MaxSeconds: 1800}},
			Flaky:        []ui.MetricsGitHubCIFlake{{Source: "github", Repo: "acme/widgets", Job: "CI / test", Commits: 1, Last: "0123456789abcdef"}},
			FlakyCommits: 1,
			BaseBranches: []ui.MetricsGitHubCIBaseBranch{{Source: "github", Repo: "acme/widgets", Branch: "main",
				RedSeconds: 3600, GreenSeconds: 82800, RedShare: 1.0 / 24, RedStretches: 1, LongestRedSeconds: 3600}},
		},
	}

	only := renderedMetrics(rep, mcp.MetricsSectionGitHubCI)
	for _, want := range []string{"GitHub CI (source: github; 10 job(s)", "1 skipped", "acme/widgets CI / test",
		"1 commit(s) recovered", "0123456789ab", "base acme/widgets main", "green now"} {
		if !strings.Contains(only, want) {
			t.Errorf("the GitHub CI section does not say %q:\n%s", want, only)
		}
	}
	if strings.Contains(only, "CI queueing (") {
		t.Errorf("asking for the GitHub CI section rendered grain's CI queueing too:\n%s", only)
	}
	whole := renderedMetrics(rep, "")
	q, g := strings.Index(whole, "CI queueing ("), strings.Index(whole, "GitHub CI (")
	if q < 0 || g < q {
		t.Errorf("the whole report should print GitHub CI after grain's CI queueing:\n%s", whole)
	}
}
