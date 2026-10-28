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

package ui_test

// The measurement surface: Client.Metrics against a real store, and the
// endpoint over it. What a percentile *means* is pkg/metrics' own tests'
// job; what these pin is that a task's real timestamps -- written by
// CreateTask, StartRun, SetRunAgentStarted, FinishRun and Observe, each
// by the path that really writes it -- come back out the other end as the
// stages of that task's life.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestParseMetricsWindow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "", want: ui.DefaultMetricsWindow},
		{in: "36h", want: 36 * time.Hour},
		{in: "90m", want: 90 * time.Minute},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "2w", want: 14 * 24 * time.Hour},
		{in: " 7d ", want: 7 * 24 * time.Hour},
		{in: "banana", wantErr: true},
		{in: "-3d", wantErr: true},
		{in: "0h", wantErr: true},
		{in: "500w", wantErr: true}, // past maxMetricsWindow
	} {
		got, err := ui.ParseMetricsWindow(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMetricsWindow(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMetricsWindow(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMetricsWindow(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMetricsReportsAWholeTaskLifeAsStages(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx) // filed and approved at baseTime

	// One attempt: dispatched half an hour later, five minutes of setup
	// before its agent's first turn, half an hour of agent work, and the
	// completion five minutes after the run finished.
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 1,
		StartedAt: baseTime.Add(30 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunAgentStarted(ctx, "r1", baseTime.Add(35*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(65*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	completed := baseTime.Add(70 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completed}); err != nil {
		t.Fatal(err)
	}

	// The report is taken later than the work it measures, which is the
	// only ordering a real one ever has.
	c.Now = func() time.Time { return baseTime.Add(2 * time.Hour) }
	rep, err := c.Metrics(ctx, ui.MetricsRequest{Window: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if rep.Throughput.TasksFiled != 1 || rep.Throughput.TasksCompleted != 1 {
		t.Errorf("throughput = %+v, want one task filed and one completed", rep.Throughput)
	}
	if got, want := rep.Runs.Outcomes["succeeded"], 1; got != want {
		t.Errorf("Outcomes[succeeded] = %d, want %d", got, want)
	}
	for _, want := range []struct {
		stage   string
		n       int
		seconds float64
	}{
		{"queue_wait", 1, 30 * 60},
		{"sandbox_setup", 1, 5 * 60},
		{"agent_work", 1, 30 * 60},
		{"attempt", 1, 35 * 60},
		{"time_to_finish", 1, 40 * 60},
		{"lead_time", 1, 70 * 60},
		// Approved in the same instant it was filed, so nobody waited.
		{"approval_wait", 0, 0},
		// One attempt, so no retry.
		{"retry_wait", 0, 0},
	} {
		got := metricsStage(t, rep, want.stage)
		if got.N != want.n || got.P50Seconds != want.seconds {
			t.Errorf("%s = (n=%d, p50=%vs), want (n=%d, p50=%vs)",
				want.stage, got.N, got.P50Seconds, want.n, want.seconds)
		}
	}

	// The backlog is a gauge taken at the report's own moment, and counts
	// unfinished work only: this task has completed by then, so nothing
	// is left in the system at all.
	if rep.Backlog.Queued != 0 || len(rep.Backlog.ByState) != 0 {
		t.Errorf("backlog = %+v, want nothing left once the one task completed", rep.Backlog)
	}
}

func TestMetricsReportsOnlyTheOrdinaryCeiling(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	cfg.MaxWorkers = 2
	cfg.MaxMergers = 1
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	rep, err := c.Metrics(ctx, ui.MetricsRequest{Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Console sessions have no count of their own, so the ceiling is
	// workers + mergers and nothing else.
	if rep.Runs.OrdinaryMaxConcurrent != 3 || rep.Runs.MaxConcurrent != 3 {
		t.Errorf("run capacity = %+v, want 3 ordinary = 3 total", rep.Runs)
	}
}

func TestMetricsEndpointServesAReportAndRejectsNonsense(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	create(t, c, context.Background())
	c.Now = func() time.Time { return baseTime.Add(time.Hour) }

	rec := do(t, srv, http.MethodGet, "/api/metrics?window=3d&buckets=4", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)
	if got, want := rep.WindowSeconds, float64(3*24*60*60); got != want {
		t.Errorf("WindowSeconds = %v, want %v", got, want)
	}
	if got, want := len(rep.Throughput.Buckets), 4; got != want {
		t.Errorf("got %d buckets, want %d", got, want)
	}
	if len(rep.Latency) == 0 {
		t.Error("the report carried no latency stages at all")
	}
	if rep.Throughput.TasksFiled != 1 {
		t.Errorf("TasksFiled = %d, want the one task filed above", rep.Throughput.TasksFiled)
	}

	for _, query := range []string{"?window=banana", "?window=0h", "?buckets=0", "?buckets=nine"} {
		if rec := do(t, srv, http.MethodGet, "/api/metrics"+query, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/metrics%s = %d, want 400", query, rec.Code)
		}
	}
}

// stubCycleTimes is ui.CycleTimes over a fixed history -- standing in
// for cmd/grain's own adapter over the ring its reconcile loop writes
// into, which is the only thing that can speak for a tick, since a tick
// leaves no row for the rest of this report to be derived from.
type stubCycleTimes struct{ history metrics.CycleHistory }

func (s stubCycleTimes) CycleTimes() metrics.CycleHistory { return s.history }

// The cycles section is what makes the queue_wait stage above
// attributable: without it, a task that waited because the deployment
// was full and a task that waited on a slow tick are the same number.
func TestMetricsReportsTheDaemonsOwnTick(t *testing.T) {
	t.Parallel()
	client, _, _ := testClient(t)
	client.Now = func() time.Time { return baseTime.Add(time.Hour) }
	start := baseTime.Add(30 * time.Minute)
	client.Config.Cycles = stubCycleTimes{history: metrics.CycleHistory{
		Observed: 900,
		Samples: []metrics.CycleSample{
			{
				Start:        start,
				Duration:     80 * time.Millisecond,
				DispatchWait: 20 * time.Millisecond,
				Reconcilers: []metrics.ReconcilerSample{
					{Name: "schedule", Wait: time.Millisecond, Duration: 19 * time.Millisecond},
					{Name: "dispatch", Wait: 20 * time.Millisecond, Duration: 10 * time.Millisecond},
					{Name: "sync", Wait: 30 * time.Millisecond, Duration: 50 * time.Millisecond, Failed: true},
				},
			},
			{
				Start:        start.Add(30 * time.Second),
				Commits:      4,
				Duration:     120 * time.Millisecond,
				DispatchWait: 40 * time.Millisecond,
				Reconcilers: []metrics.ReconcilerSample{
					{Name: "schedule", Wait: time.Millisecond, Duration: 39 * time.Millisecond},
					{Name: "dispatch", Wait: 40 * time.Millisecond, Duration: 10 * time.Millisecond},
					{Name: "sync", Wait: 50 * time.Millisecond, Duration: 70 * time.Millisecond},
				},
			},
		},
	}}

	srv := ui.NewServerWithClient(client)
	rec := do(t, srv, http.MethodGet, "/api/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	c := decode[ui.MetricsReport](t, rec).Cycles

	if !c.Enabled || c.N != 2 {
		t.Fatalf("cycles = (enabled=%v, n=%d), want the two ticks the daemon reported", c.Enabled, c.N)
	}
	if c.Observed != 900 || !c.Truncated {
		t.Errorf("observed = %d, truncated = %v; want 900 and true -- the ring has forgotten the rest",
			c.Observed, c.Truncated)
	}
	// Everything crosses this boundary in seconds, named as such.
	// Nearest-rank over two samples puts p50 on the lower of them.
	if got, want := c.Tick.P50Seconds, 0.08; got != want {
		t.Errorf("tick p50 = %vs, want %vs", got, want)
	}
	if got, want := c.Tick.MaxSeconds, 0.12; got != want {
		t.Errorf("tick max = %vs, want %vs", got, want)
	}
	if got, want := c.DispatchWait.P50Seconds, 0.02; got != want {
		t.Errorf("dispatch wait p50 = %vs, want %vs", got, want)
	}
	if got, want := c.Interval.P50Seconds, 30.0; got != want {
		t.Errorf("interval p50 = %vs, want the loop's real period (%vs)", got, want)
	}
	if c.CommitFree != 1 || c.Commits.P50 != 0 || c.Commits.Max != 4 {
		t.Errorf("commits = %+v, commit-free = %d; want 0..4 and one commit-free tick", c.Commits, c.CommitFree)
	}
	if len(c.Reconcilers) != 3 || c.Reconcilers[1].Name != "dispatch" {
		t.Fatalf("reconcilers = %+v, want all three in cycle order", c.Reconcilers)
	}
	if c.Reconcilers[2].Failures != 1 {
		t.Errorf("sync failures = %d, want the one cycle it failed in", c.Reconcilers[2].Failures)
	}
}

// A UI with no reconcile loop to ask says so, rather than reporting a
// tick of zero -- "nobody measured it" and "the tick costs nothing" are
// opposite answers, and the second would be read as a deployment whose
// scheduling is free.
func TestMetricsWithNoDaemonToAskReportsCyclesUnavailable(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodGet, "/api/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	c := decode[ui.MetricsReport](t, rec).Cycles
	if c.Enabled || c.N != 0 {
		t.Errorf("cycles = (enabled=%v, n=%d), want an unavailable section", c.Enabled, c.N)
	}
}

// metricsStage picks one stage out of the report by its stable key.
func metricsStage(t *testing.T, rep ui.MetricsReport, stage string) ui.MetricsStage {
	t.Helper()
	for _, s := range rep.Latency {
		if s.Stage == stage {
			return s
		}
	}
	t.Fatalf("no %q stage in the report", stage)
	return ui.MetricsStage{}
}

func TestParseMetricsMoment(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: "", want: time.Time{}},
		{in: "2026-01-31", want: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
		{in: " 2026-01-31 ", want: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
		{in: "2026-01-31T09:30:00Z", want: time.Date(2026, 1, 31, 9, 30, 0, 0, time.UTC)},
		// An offset is honoured and normalized to UTC, so a span asked
		// for from a browser in another zone means the same instant.
		{in: "2026-01-31T09:30:00+02:00", want: time.Date(2026, 1, 31, 7, 30, 0, 0, time.UTC)},
		{in: "31/01/2026", wantErr: true},
		{in: "last tuesday", wantErr: true},
	} {
		got, err := ui.ParseMetricsMoment(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMetricsMoment(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMetricsMoment(%q): %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("ParseMetricsMoment(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A report scoped to a repo, over the endpoint that a report board asks
// (grain/task-83). Two tasks in two repos, and ?repo= picks one of them
// -- and the report says which scope it was computed under, which is
// what lets a page head itself with the question it answers.
func TestMetricsEndpointScopesAReportToARepo(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	for _, repo := range []string{"acme/widgets", "acme/docs"} {
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "fix the thing", Description: "please", Approved: true, Repo: repo,
		}); err != nil {
			t.Fatalf("creating a task in %s: %v", repo, err)
		}
	}
	c.Now = func() time.Time { return baseTime.Add(time.Hour) }

	rec := do(t, srv, http.MethodGet, "/api/metrics?repo=acme/widgets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)
	if got, want := rep.Throughput.TasksFiled, 1; got != want {
		t.Errorf("TasksFiled = %d, want %d -- the other repo's task is out of scope", got, want)
	}
	if len(rep.Scope.Repos) != 1 || rep.Scope.Repos[0] != "acme/widgets" {
		t.Errorf("Scope = %+v, want the repo the request named", rep.Scope)
	}

	// Both repos named, both tasks counted: a scope's own values are an
	// "any of", the way the `repo` filter a board's query carries is.
	rec = do(t, srv, http.MethodGet, "/api/metrics?repo=acme/widgets&repo=acme/docs", "")
	if got, want := decode[ui.MetricsReport](t, rec).Throughput.TasksFiled, 2; got != want {
		t.Errorf("TasksFiled over both repos = %d, want %d", got, want)
	}

	// A blank parameter is no scope at all rather than a scope on "":
	// the UI sends the same URL whether it has a repo to name or not.
	rec = do(t, srv, http.MethodGet, "/api/metrics?repo=&author=", "")
	report := decode[ui.MetricsReport](t, rec)
	if got, want := report.Throughput.TasksFiled, 2; got != want {
		t.Errorf("TasksFiled with blank scope parameters = %d, want %d", got, want)
	}
	if len(report.Scope.Repos) != 0 || len(report.Scope.Authors) != 0 {
		t.Errorf("Scope = %+v, want empty", report.Scope)
	}

	// The author every task here was filed by, and one nobody is.
	if got := decode[ui.MetricsReport](t,
		do(t, srv, http.MethodGet, "/api/metrics?author=alice", "")).Throughput.TasksFiled; got != 2 {
		t.Errorf("TasksFiled by alice = %d, want 2", got)
	}
	if got := decode[ui.MetricsReport](t,
		do(t, srv, http.MethodGet, "/api/metrics?author=nobody", "")).Throughput.TasksFiled; got != 0 {
		t.Errorf("TasksFiled by nobody = %d, want 0", got)
	}
}

// ?since= and ?until= are the other half of what a saved report board
// carries: a span that is not a length ending now, because the query the
// board was built from named actual dates.
func TestMetricsEndpointTakesAnAbsoluteSpan(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	create(t, c, context.Background()) // filed at baseTime
	c.Now = func() time.Time { return baseTime.Add(48 * time.Hour) }

	since := baseTime.Add(-time.Hour).UTC().Format(time.RFC3339)
	until := baseTime.Add(time.Hour).UTC().Format(time.RFC3339)
	rec := do(t, srv, http.MethodGet, "/api/metrics?since="+since+"&until="+until, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)
	if got, want := rep.WindowSeconds, float64(2*60*60); got != want {
		t.Errorf("WindowSeconds = %v, want %v", got, want)
	}
	if rep.Throughput.TasksFiled != 1 {
		t.Errorf("TasksFiled = %d, want the one task filed inside the span", rep.Throughput.TasksFiled)
	}

	// A span that ends before the task was filed measures nothing --
	// which is the point of being able to ask for one.
	before := baseTime.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	rec = do(t, srv, http.MethodGet, "/api/metrics?since="+before+"&until="+
		baseTime.Add(-time.Hour).UTC().Format(time.RFC3339), "")
	if got := decode[ui.MetricsReport](t, rec).Throughput.TasksFiled; got != 0 {
		t.Errorf("TasksFiled before the task existed = %d, want 0", got)
	}

	for _, query := range []string{"?since=last-tuesday", "?until=31/01/2026"} {
		if rec := do(t, srv, http.MethodGet, "/api/metrics"+query, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/metrics%s = %d, want 400", query, rec.Code)
		}
	}
}

// The prompt section end to end: the sizes come off the store's own
// read, the count of cut threads comes off the notice grain wrote into
// the prompt, and the heaviest task is named so a reader has somewhere
// to go.
func TestMetricsReportsPromptSizesAndWhichTasksCarryTheHeaviest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	light := create(t, c, ctx)
	heavy := create(t, c, ctx)

	// Two attempts, one per task, both finished inside the window. The
	// heavy one's prompt carries the notice grain writes where it had to
	// cut the task's conversation to fit.
	prompts := map[string]string{
		light.ID: "a short prompt\n",
		heavy.ID: "- ada: the terms\n" + model.PromptThreadCutNotice +
			"30 message(s) from the middle of it, 40 KB of text, elided\n" +
			strings.Repeat("- ada: and then\n", 1000),
	}
	for i, id := range []string{light.ID, heavy.ID} {
		runID := fmt.Sprintf("r%d", i+1)
		if err := store.StartRun(ctx, model.Run{
			ID: runID, TaskID: id, Sandbox: "s" + runID, Attempt: 1,
			StartedAt: baseTime.Add(30 * time.Minute),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRunPrompt(ctx, runID, prompts[id]); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, runID, baseTime.Add(time.Hour), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}

	c.Now = func() time.Time { return baseTime.Add(2 * time.Hour) }
	rep, err := c.Metrics(ctx, ui.MetricsRequest{Window: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	p := rep.Prompts
	if p.Runs != 2 || p.Bytes.N != 2 {
		t.Fatalf("prompts = %+v, want both attempts measured", p)
	}
	if got, want := p.Bytes.MaxBytes, int64(len(prompts[heavy.ID])); got != want {
		t.Errorf("maxBytes = %d, want %d -- the largest prompt exactly", got, want)
	}
	if p.ThreadCut != 1 {
		t.Errorf("threadCut = %d, want 1 -- the one prompt whose thread grain cut", p.ThreadCut)
	}
	if p.WarnTokens != model.PromptWarnTokens || p.ContextTokens != model.PromptContextTokens {
		t.Errorf("prompts = %+v, want the window the share was taken against on the wire", p)
	}
	if len(p.Heaviest) != 2 || p.Heaviest[0].TaskID != heavy.ID {
		t.Fatalf("heaviest = %+v, want the heavy task first", p.Heaviest)
	}
	if got := p.Heaviest[0]; got.Runs != 1 || got.Attempt != 1 || got.ThreadCut != 1 {
		t.Errorf("heaviest = %+v, want its one attempt, cut", got)
	}
}

func TestMetricsReportIncludesBranchRepairs(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	store := c.Store
	ctx := context.Background()

	task := create(t, c, ctx)
	repairAt := baseTime.Add(30 * time.Minute)
	mergedAt := baseTime.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID:             task.ID,
		MergeQueueRepairAt: &repairAt,
		MergeQueueNote:     "merge conflict resolution",
		PrMergedAt:         &mergedAt,
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 2,
		StartedAt: baseTime.Add(40 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunTelemetry(ctx, "r1", model.RunTelemetry{
		Tools: []model.RunToolUse{
			{Tool: "edit_file", Calls: 3, Errored: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	transcript := "sample repair transcript"
	if err := store.SetRunTranscript(ctx, "r1", transcript); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(50*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	c.Now = func() time.Time { return baseTime.Add(3 * time.Hour) }
	rec := do(t, srv, http.MethodGet, "/api/metrics?window=24h", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)

	r := rep.Merges.Repairs
	if r.Passes != 1 || r.Settled != 1 {
		t.Errorf("Merges.Repairs = %+v, want 1 pass and 1 settled", r)
	}
	if r.Runs != 1 || r.ToolCalls != 3 || r.ErroredToolCalls != 1 {
		t.Errorf("Merges.Repairs telemetry = %+v, want 1 run, 3 calls, 1 error", r)
	}
	if r.TranscriptBytes.N != 1 || r.TranscriptBytes.Mean != float64(len(transcript)) {
		t.Errorf("Merges.Repairs TranscriptBytes = %+v, want Mean=%d", r.TranscriptBytes, len(transcript))
	}
	if rep.Checks.Repairs.Runs != 1 || rep.Checks.Repairs.ToolCalls != 3 {
		t.Errorf("Checks.Repairs = %+v, want 1 run and 3 calls", rep.Checks.Repairs)
	}
}

// A report reads only what the section it was asked for is built from,
// and a whole report that cannot read one input loses the sections built
// from that input rather than the whole report. On 2026-10-06 a failing
// prompt-size query turned every read_grain_metrics call into a 500,
// throughput included, which has nothing to do with prompts.
func TestMetricsSurvivesAFailingPromptRead(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	create(t, c, context.Background())
	c.Now = func() time.Time { return baseTime.Add(time.Hour) }
	// Store.RunPrompts reads task_run.prompt and nothing else here does,
	// so taking the column away fails that one read.
	if _, err := c.Store.DB().Exec("ALTER TABLE `task_run` RENAME COLUMN `prompt` TO `prompt_gone`"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/metrics?window=3d&section=throughput", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("section=throughput: status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rep := decode[ui.MetricsReport](t, rec); rep.Throughput.TasksFiled != 1 || len(rep.Failed) != 0 {
		t.Errorf("section=throughput: TasksFiled = %d, Failed = %+v; want the one task filed and nothing failed",
			rep.Throughput.TasksFiled, rep.Failed)
	}

	if rec := do(t, srv, http.MethodGet, "/api/metrics?window=3d&section=prompts", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("section=prompts: status = %d, want 500 -- the one section asked for cannot be built: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/metrics?window=3d", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("whole report: status = %d, want 200 with the prompts section marked failed: %s", rec.Code, rec.Body)
	}
	rep := decode[ui.MetricsReport](t, rec)
	if len(rep.Failed) != 1 || rep.Failed[0].Section != "prompts" || rep.Failed[0].Ref == "" {
		t.Fatalf("whole report: Failed = %+v, want only prompts, with a log reference", rep.Failed)
	}
	if f := rep.Failed[0]; strings.Contains(f.Error, "prompt_gone") || strings.Contains(f.Error, "no such column") ||
		!strings.Contains(f.Error, f.Ref) {
		t.Errorf("whole report: failure = %q, want the sanitized sentence naming ref %s and not the cause", f.Error, f.Ref)
	}
	if rep.Throughput.TasksFiled != 1 || len(rep.Latency) == 0 {
		t.Errorf("whole report: throughput %+v / latency %d stages; want the rest of the report whole",
			rep.Throughput, len(rep.Latency))
	}

	if rec := do(t, srv, http.MethodGet, "/api/metrics?section=banana", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("section=banana: status = %d, want 400", rec.Code)
	}
}

// Every section read_grain_metrics offers is one the report knows the
// inputs of: a section with none listed would read nothing and render
// empty.
func TestMetricsKnowsEverySectionsInputs(t *testing.T) {
	t.Parallel()
	for _, section := range mcp.MetricsSections {
		if !ui.KnownMetricsSection(section) {
			t.Errorf("section %q has no inputs listed in metricsSectionInputs", section)
		}
	}
}
