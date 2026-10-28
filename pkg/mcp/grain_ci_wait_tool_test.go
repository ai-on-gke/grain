package mcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// testCIRepo is the repository every fake here records its runs against,
// and the one an ordinary run in these tests may read (ciTools).
const testCIRepo = "acme/widgets"

// ciTools is the three tools as an ordinary run gets them: scoped to its
// own repository, which is what grain/task-172 made the default.
func ciTools(runner GrainCIRunner) []Tool {
	return fullCIList(NewGrainCIWaitTools(runner, CIRepoScope{Repo: testCIRepo}))
}

// fullCIList makes tools' list_ci_runs answer in view "full" when a call
// names no view: the tests below are about the full report, which the
// summary default (ci_run_summary.go) leaves out. The summary has tests
// of its own.
func fullCIList(tools []Tool) []Tool {
	tools = append([]Tool(nil), tools...)
	list := tools[1]
	inner := list.Handler
	list.Handler = func(ctx context.Context, args map[string]any) Result {
		if _, ok := args["view"]; !ok {
			args = maps.Clone(args)
			if args == nil {
				args = map[string]any{}
			}
			args["view"] = "full"
		}
		return inner(ctx, args)
	}
	tools[1] = list
	return tools
}

// ciToolsEverywhere is the same three as a run holding the wide grant
// gets them (CIWideReadGrant) -- what the filter vocabulary's own tests
// below use, since they are about the words rather than about the
// boundary.
func ciToolsEverywhere(runner GrainCIRunner) []Tool {
	return fullCIList(NewGrainCIWaitTools(runner, CIRepoScope{AllRepos: true}))
}

type fakeGrainCIRunner struct {
	starts int
	reads  int
	jobs   []string
	// run, when set, is what GrainCIRun answers with instead of the
	// failing run below -- for a test about a run that ended some other
	// way.
	run  *ci.Run
	runs []ci.Run
	// faults is what this deployment has grouped as failing on several
	// branches at once, and faultReads how many times a report asked --
	// a green run must not ask at all.
	faults     []CIFault
	faultReads int
	// retriggerJobs and retriggerAll are what the last retrigger_ci
	// asked to rerun.
	retriggerJobs []string
	retriggerAll  bool
}

func (f *fakeGrainCIRunner) StartGrainCI(_ context.Context, jobs []string) (ci.Run, error) {
	f.starts++
	f.jobs = append([]string(nil), jobs...)
	return ci.Run{ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345"}, nil
}

func (f *fakeGrainCIRunner) RetriggerGrainCI(_ context.Context, workflow, commit string, jobs []string, all bool) (ci.Run, error) {
	f.starts++
	f.retriggerJobs, f.retriggerAll = append([]string(nil), jobs...), all
	c := commit
	if c == "" {
		c = "abcdef012345"
	}
	return ci.Run{
		ID:     "ci-retrigger-1",
		Repo:   testCIRepo,
		Ref:    "grain/task-1",
		Commit: c,
		Jobs: []ci.JobRun{
			{JobID: "tests", Name: "tests"},
		},
	}, nil
}

func (f *fakeGrainCIRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	f.reads++
	if f.run != nil {
		return *f.run, nil
	}
	return ci.Run{
		ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{JobID: "tests", Name: "tests", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{{
			Name: "go test", Conclusion: ci.ConclusionFailure, ExitCode: 1, Output: "0123456789package failed",
		}}}},
	}, nil
}

func (f *fakeGrainCIRunner) GrainCISharedFaults(context.Context) ([]CIFault, error) {
	f.faultReads++
	return f.faults, nil
}

func (f *fakeGrainCIRunner) FindGrainCIRuns(_ context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	if f.runs != nil {
		return ciRunPage(f.runs, filter, limit), nil
	}
	return ciRunPage([]ci.Run{
		{ID: "ci-2", Repo: "acme/widgets", Ref: "grain/task-2", Commit: "2222222", Conclusion: ci.ConclusionSuccess},
		{ID: "ci-1", Repo: "acme/widgets", Ref: "grain/task-1", Commit: "1111111", Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{{JobID: "tests", Name: "tests", Conclusion: ci.ConclusionFailure}}},
	}, filter, limit), nil
}

// ciRunPage is what a deployment answers a narrowed listing with, for a
// fake that has a list of runs rather than a store.
//
// It narrows them through a real ci.MemoryRunStore rather than by hand,
// because that is the point of the seam these fakes stand in for: the
// tool hands its filter and its limit to the deployment and renders the
// page it gets back (ci.RunStore.Find), so what a fake owes it is a page
// with the same rules on it -- the same order, the same limit, and the
// two counts saying what the page leaves out.
func ciRunPage(runs []ci.Run, filter ci.RunFilter, limit int) ci.RunPage {
	store := ci.NewRunStore(len(runs) + 1)
	for _, run := range runs {
		store.Put(run)
	}
	return store.Find(filter, limit)
}

func TestWaitForChecksCanSelectJobsWithoutCallingThemTheRequiredSuite(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{"jobs": []any{"go", "frontend"}})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if got := strings.Join(runner.jobs, ","); got != "go,frontend" {
		t.Fatalf("selected jobs = %q, want go,frontend", got)
	}
	for _, want := range []string{"selective run", "without jobs", "full-suite verdict"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("selective result lacks %q: %s", want, result.Text)
		}
	}
}

func TestWaitForChecksReturnsStatusButNeverStepOutput(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if runner.starts != 1 || runner.reads != 1 {
		t.Fatalf("starts=%d reads=%d, want one explicit start and read", runner.starts, runner.reads)
	}
	for _, want := range []string{"ci-1", "Job tests: failure", "Step go test: failure"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("result lacks %q: %s", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "package failed") {
		t.Fatalf("status call leaked failing step output: %s", result.Text)
	}
	if !strings.Contains(tool.Description, "does not read GitHub checks") || !strings.Contains(tool.Description, "get_ci_log") {
		t.Errorf("description does not distinguish status from logs: %s", tool.Description)
	}
}

// A run somebody stopped by hand reads, to the agent waiting on it,
// exactly like one a supersede or a daemon restart ended: "cancelled",
// and nothing about what to do next. The note whoever stopped it left is
// the answer to that, so the report says it rather than making the run
// infer one from a conclusion that cannot carry it.
func TestWaitForChecksSaysWhyARunWasCancelled(t *testing.T) {
	runner := &fakeGrainCIRunner{run: &ci.Run{
		ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion:   ci.ConclusionCancelled,
		CancelReason: "I am reverting the base branch; do not start another run",
		CancelledBy:  ci.CancelByOperator,
	}}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"cancelled", "cancelled by hand",
		"I am reverting the base branch; do not start another run"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("result lacks %q: %s", want, result.Text)
		}
	}
}

func TestWaitForChecksReturnsImmediatelyOnFirstFailedJobDuringActiveRun(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	run := ci.Run{
		ID:     "ci-1",
		Repo:   testCIRepo,
		Ref:    "grain/task-1",
		Commit: "abcdef012345",
		Jobs: []ci.JobRun{
			{JobID: "lint", Name: "lint", Conclusion: ci.ConclusionFailure, StartedAt: &started, FinishedAt: &started, Steps: []ci.StepRun{
				{Name: "golangci-lint", Conclusion: ci.ConclusionFailure, ExitCode: 1, Error: "lint error"},
			}},
			{JobID: "go", Name: "go", StartedAt: &started, Steps: []ci.StepRun{
				{Name: "go test", StartedAt: &started},
			}},
			{JobID: "ui-e2e", Name: "ui-e2e", StartedAt: &started},
		},
	}
	runner := &fakeGrainCIRunner{run: &run}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if runner.reads != 1 {
		t.Fatalf("reads = %d, want exactly 1 read returning immediately on failed job", runner.reads)
	}
	for _, want := range []string{
		"still running",
		"CI has failed on job \"lint\", so I stopped waiting (2 unfinished jobs still running)",
		"get_ci_log (run_id: \"ci-1\", job: \"lint\")",
		"Job lint: failure",
		"Step golangci-lint: failure (lint error)",
		"Job go: running",
		"Job ui-e2e: running",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("result lacks %q:\n%s", want, result.Text)
		}
	}
}

func TestWaitForChecksReturnsImmediatelyOnErroredJobDuringActiveRun(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	run := ci.Run{
		ID:     "ci-1",
		Repo:   testCIRepo,
		Ref:    "grain/task-1",
		Commit: "abcdef012345",
		Jobs: []ci.JobRun{
			{JobID: "build", Name: "build", Conclusion: ci.ConclusionErrored, StartedAt: &started, FinishedAt: &started, Error: "infra failure"},
			{JobID: "unit", Name: "unit", StartedAt: &started},
		},
	}
	runner := &fakeGrainCIRunner{run: &run}
	tool := ciTools(runner)[0]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if runner.reads != 1 {
		t.Fatalf("reads = %d, want 1 read", runner.reads)
	}
	for _, want := range []string{
		"CI has failed on job \"build\", so I stopped waiting (1 unfinished job still running)",
		"Job build: errored",
		"Job unit: running",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("result lacks %q:\n%s", want, result.Text)
		}
	}
}

func TestListCIRunsShowsEveryBranchAndItsStatus(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"2 of 2 remembered grain CI runs match", "ci-2", "grain/task-2", "success", "ci-1", "grain/task-1", "failure", "Job tests: failure", "acme/widgets"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("list result lacks %q: %s", want, result.Text)
		}
	}
	if !strings.Contains(tool.Description, "other branches") {
		t.Errorf("description does not explain cross-branch diagnosis: %s", tool.Description)
	}
}

// manyRunsCIRunner remembers more runs than a list answer can carry, each
// of them holding the kind of error a report used to quote whole: a
// kontur guest dump, which is kilobytes and which grain CI puts in
// ci.JobRun.Error on purpose.
type manyRunsCIRunner struct {
	fakeGrainCIRunner
	count int
}

func (m *manyRunsCIRunner) FindGrainCIRuns(_ context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	runs := make([]ci.Run, 0, m.count)
	for i := 0; i < m.count; i++ {
		runs = append(runs, ci.Run{
			ID: fmt.Sprintf("ci-%d", m.count-i), Repo: "acme/widgets",
			Ref: fmt.Sprintf("grain/task-%d", m.count-i), Commit: "1111111",
			Conclusion: ci.ConclusionFailure,
			Jobs: []ci.JobRun{{Name: "tests", Conclusion: ci.ConclusionFailure,
				Error: "guest dump follows\n" + strings.Repeat("eth0 has no default route\n", 400)}},
		})
	}
	return ciRunPage(runs, filter, limit), nil
}

func TestListCIRunsReportsTheNewestAndSaysHowManyItLeftOut(t *testing.T) {
	runner := &manyRunsCIRunner{count: defaultCIRunListLimit + 7}
	tool := ciTools(runner)[1]
	result := tool.Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, fmt.Sprintf("%d of %d remembered", runner.count, runner.count)) ||
		!strings.Contains(result.Text, fmt.Sprintf("showing %d", defaultCIRunListLimit)) {
		t.Errorf("list does not say how much of the deployment's memory it reported:\n%s", firstLines(result.Text, 3))
	}
	if !strings.Contains(result.Text, "7 older matching runs are not shown") {
		t.Errorf("list does not say what it left out:\n%s", firstLines(result.Text, 3))
	}
	// The newest are what a run asking this question is after, and the
	// oldest are what it is not.
	if !strings.Contains(result.Text, "ci-27") || strings.Contains(result.Text, "ci-1 ") {
		t.Errorf("list is not the newest runs:\n%s", firstLines(result.Text, 3))
	}
	// The list bounds itself rather than leaving the backstop to do it:
	// twenty guest dumps is the shape that used to arrive whole.
	if len(result.Text) > maxResultTextBytes() {
		t.Errorf("list answered %d bytes, more than one answer carries", len(result.Text))
	}
	if strings.Contains(result.Text, elisionAdviceGeneric) {
		t.Error("the list leaned on the backstop instead of bounding itself")
	}
	if !strings.Contains(result.Text, "get_ci_log") {
		t.Errorf("a cut list does not say where to read a run in full:\n%s", result.Text)
	}
}

func TestListCIRunsHonoursAnExplicitLimit(t *testing.T) {
	runner := &manyRunsCIRunner{count: 30}
	tool := ciTools(runner)[1]
	result := tool.Handler(context.Background(), map[string]any{"limit": float64(3)})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if got := strings.Count(result.Text, "Grain CI run ci-"); got != 3 {
		t.Errorf("got %d runs against limit 3:\n%s", got, result.Text)
	}
}

// TestCIStatusQuotesAnErrorWithoutBeingBuriedByIt: a job whose error
// carries a guest dump is the commonest big answer grain CI produces, and
// a status report is not where the whole of it belongs.
func TestCIStatusQuotesAnErrorWithoutBeingBuriedByIt(t *testing.T) {
	run := ci.Run{ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{Name: "tests", Conclusion: ci.ConclusionFailure,
			Error: "the guest answers but has no default route\n" + strings.Repeat("dump line\n", 1000)}}}
	text := renderGrainCIRun(run, time.Second, false, nil)
	if len(text) > maxCIRunErrorBytes*2 {
		t.Errorf("one job's error rendered %d bytes, want near the %d-byte bound", len(text), maxCIRunErrorBytes)
	}
	// Its head is the sentence somebody reads; its tail is where a dump
	// says how it ended.
	if !strings.Contains(text, "no default route") || !strings.Contains(text, "dump line") {
		t.Errorf("the error lost both ends of itself:\n%s", text)
	}
	if !strings.Contains(text, "get_ci_log") {
		t.Errorf("the cut error does not say where the whole of it is:\n%s", text)
	}
}

// Which build of grain produced a run is on the run's headline, because
// an agent reading a run that behaves unlike the code in its checkout
// has no other way to tell a bug from a deployment running older code
// (ci.Build, and grain/task-388, which bisected a fortnight of run
// history to answer exactly that).
func TestCIStatusSaysWhichBuildProducedTheRun(t *testing.T) {
	committed := time.Date(2026, 9, 18, 11, 22, 33, 0, time.UTC)
	run := ci.Run{ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionErrored,
		Build:      &ci.Build{Revision: "acc97d35b0f1e2d3", CommittedAt: &committed}}
	text := renderGrainCIRun(run, time.Second, false, nil)
	if !strings.Contains(text, "acc97d3") {
		t.Errorf("the report does not say which build made the run:\n%s", text)
	}
	if !strings.Contains(text, "2026-09-18T11:22:33Z") {
		t.Errorf("the report does not say when that build's commit was made:\n%s", text)
	}

	// A run no build stamped says nothing rather than "unknown": a
	// deployment whose binary carries no stamp would otherwise repeat
	// that on every run of every list forever.
	run.Build = nil
	if text := renderGrainCIRun(run, time.Second, false, nil); strings.Contains(text, "build") {
		t.Errorf("an unstamped run talked about builds anyway:\n%s", text)
	}
}

func TestRenderGrainCIRun_FailureSummary(t *testing.T) {
	run := ci.Run{
		ID:         "ci-123",
		Ref:        "grain/task-652",
		Commit:     "1234567890ab",
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				JobID:      "unit-tests",
				Name:       "unit-tests",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "go test",
						Conclusion: ci.ConclusionFailure,
						Output: "=== RUN   TestFailureSummary\n" +
							"    diagnostics_test.go:42: assertion failed: want 10, got 20\n" +
							"--- FAIL: TestFailureSummary (0.01s)\nFAIL\n",
					},
				},
			},
		},
	}
	text := renderGrainCIRun(run, time.Second, false, nil)
	if !strings.Contains(text, "Failure summary:") {
		t.Errorf("expected failure summary in rendered run:\n%s", text)
	}
	if !strings.Contains(text, "TestFailureSummary") || !strings.Contains(text, "diagnostics_test.go:42") {
		t.Errorf("expected failed test details in rendered run:\n%s", text)
	}
}

func firstLines(text string, n int) string {
	lines := strings.SplitN(text, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func TestGetCILogReturnsRequestedByteRangeAndMetadata(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{
		"run_id": "ci-1", "job": "tests", "step": "go test", "offset": float64(10), "size": float64(7),
	})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"offset: 10", "returned: 7", "total_size: 24", "next_offset: 17", "complete: false", "package"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("range result lacks %q: %s", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "0123456789") || strings.Contains(result.Text, " failed") {
		t.Fatalf("range result contains bytes outside the requested range: %s", result.Text)
	}
}

func TestGetCILogCanPageToTheEnd(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{
		"run_id": "ci-1", "job": "tests", "step": "go test", "offset": float64(17), "size": float64(100),
	})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"offset: 17", "returned: 7", "total_size: 24", "next_offset: 24", "complete: true", " failed"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("tail result lacks %q: %s", want, result.Text)
		}
	}
}

func TestGetCILogRejectsReadsAboveThePerCallCap(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{
		"run_id": "ci-1", "job": "tests", "size": float64(maxCILogReadBytes + 1),
	})
	if !result.IsError || !strings.Contains(result.Text, fmt.Sprintf("at most %d", maxCILogReadBytes)) {
		t.Fatalf("oversized read = %#v, want a bounded-read error", result)
	}
}

// runningGrainCIRunner is a run in flight: one job on a machine with a
// step that has printed some of what it is going to print.
type runningGrainCIRunner struct {
	fakeGrainCIRunner
	output string
	jobs   []ci.JobRun
}

func (f *runningGrainCIRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	started := time.Unix(1_700_000_000, 0)
	if f.jobs != nil {
		return ci.Run{ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345", Jobs: f.jobs}, nil
	}
	return ci.Run{
		ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Jobs: []ci.JobRun{{JobID: "go", Name: "go", StartedAt: &started, Steps: []ci.StepRun{{
			Name: "go test", StartedAt: &started, ExitCode: -1, Output: f.output,
		}}}},
	}, nil
}

// The whole of what tailing a running job is for: the log is readable
// before the job ends, reaching its end is not "that is all there is", and
// the answer says where to carry on from.
func TestGetCILogFollowsAJobThatIsStillRunning(t *testing.T) {
	runner := &runningGrainCIRunner{output: "compiling\ntesting\n"}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"still running", "still_running: true", "complete: false", "compiling", "testing", "get_ci_log again with offset"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("a running job's log lacks %q: %s", want, result.Text)
		}
	}
}

// A job nobody named: the one that is running is the one a caller tailing
// a run means, and having to list the jobs first would defeat the point of
// asking again every few seconds.
func TestGetCILogPicksTheRunningJobWhenNoneIsNamed(t *testing.T) {
	done := time.Unix(1_700_000_000, 0)
	runner := &runningGrainCIRunner{jobs: []ci.JobRun{
		{JobID: "lint", Name: "lint", StartedAt: &done, FinishedAt: &done, Conclusion: ci.ConclusionSuccess,
			Steps: []ci.StepRun{{Name: "golangci-lint", Conclusion: ci.ConclusionSuccess, Output: "no issues\n"}}},
		{JobID: "go", Name: "go", StartedAt: &done,
			Steps: []ci.StepRun{{Name: "go test", StartedAt: &done, ExitCode: -1, Output: "ok pkg/ci\n"}}},
	}}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "job go") || !strings.Contains(result.Text, "ok pkg/ci") {
		t.Errorf("the running job is the one read: %s", result.Text)
	}
	if strings.Contains(result.Text, "no issues") {
		t.Errorf("the finished job's log was read instead: %s", result.Text)
	}
}

// Two jobs running at once is the ordinary shape of a run, and picking one
// of them silently would be a log read as an answer about the other.
func TestGetCILogRefusesToGuessBetweenTwoRunningJobs(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	runner := &runningGrainCIRunner{jobs: []ci.JobRun{
		{JobID: "go", Name: "go", StartedAt: &started},
		{JobID: "ui", Name: "ui", StartedAt: &started},
	}}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if !result.IsError {
		t.Fatalf("an ambiguous read should be refused: %s", result.Text)
	}
	for _, want := range []string{"name one with job", "go (running)", "ui (running)"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the refusal lacks %q: %s", want, result.Text)
		}
	}
}

// On a run that is over, the job nobody named is the one that failed --
// which is what a caller arriving from a red wait_for_checks came for.
func TestGetCILogPicksTheFailedJobOnAFinishedRun(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"job tests (failure)", "package failed", "complete: true"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("a finished run's log lacks %q: %s", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "still_running") {
		t.Errorf("a finished job is not still running: %s", result.Text)
	}
}

// tail is the read a follower actually wants: the newest bytes, without
// having to know how far the log has got.
func TestGetCILogTailReadsTheNewestBytes(t *testing.T) {
	runner := &runningGrainCIRunner{output: "0123456789abcdefghij"}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1", "job": "go", "step": "go test", "tail": float64(5)})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"offset: 15", "returned: 5", "total_size: 20", "fghij"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("tail result lacks %q: %s", want, result.Text)
		}
	}
	if strings.Contains(result.Text, "01234") {
		t.Errorf("tail returned the head of the log: %s", result.Text)
	}
}

func TestGetCILogRefusesTailAndOffsetTogether(t *testing.T) {
	runner := &runningGrainCIRunner{output: "abc"}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1", "tail": float64(5), "offset": float64(1)})
	if !result.IsError || !strings.Contains(result.Text, "give one or the other") {
		t.Fatalf("tail with offset = %#v, want a refusal", result)
	}
}

func TestGetCILogOffsetPastEndReturnsAnEmptyCompleteRange(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tool := ciTools(runner)[2]
	result := tool.Handler(context.Background(), map[string]any{
		"run_id": "ci-1", "job": "tests", "step": "go test", "offset": float64(1000), "size": float64(10),
	})
	if result.IsError {
		t.Fatal(result.Text)
	}
	for _, want := range []string{"offset: 24", "returned: 0", "total_size: 24", "next_offset: 24", "complete: true"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("past-end result lacks %q: %s", want, result.Text)
		}
	}
}

// --- list_ci_runs' filters -------------------------------------------
//
// The tool and the CI pane narrow by the same words (ci.RunFilter), and
// these are about what this end of that does with them: that they
// compose, that the answer says what it left out, and that an empty
// answer says it is empty rather than looking broken.

// filteredCIRunner is a deployment's memory with enough shape in it to
// ask a narrow question of: two repos, two branches, a job that fails on
// one of them and passes on the other, and a run still going.
type filteredCIRunner struct{ now time.Time }

func (f *filteredCIRunner) StartGrainCI(context.Context, []string) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("not used by these tests")
}

func (f *filteredCIRunner) RetriggerGrainCI(context.Context, string, string, []string, bool) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("not used by these tests")
}

func (f *filteredCIRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("not used by these tests")
}

func (f *filteredCIRunner) GrainCISharedFaults(context.Context) ([]CIFault, error) {
	return nil, nil
}

func (f *filteredCIRunner) FindGrainCIRuns(_ context.Context, filter ci.RunFilter, limit int) (ci.RunPage, error) {
	ago := func(m int) *time.Time { t := f.now.Add(-time.Duration(m) * time.Minute); return &t }
	return ciRunPage([]ci.Run{
		{ID: "ci-4", Repo: "acme/widgets", Ref: "main", Commit: "4444444", StartedAt: ago(5),
			Jobs: []ci.JobRun{{Name: "ui e2e", JobID: "e2e", StartedAt: ago(5)}}},
		{ID: "ci-3", Repo: "acme/widgets", Ref: "main", Commit: "3333333", Conclusion: ci.ConclusionFailure,
			StartedAt: ago(60), FinishedAt: ago(18),
			Jobs: []ci.JobRun{
				{Name: "go tests", JobID: "go", Conclusion: ci.ConclusionSuccess, StartedAt: ago(60), FinishedAt: ago(57)},
				{Name: "ui e2e", JobID: "e2e", Conclusion: ci.ConclusionFailure, StartedAt: ago(60), FinishedAt: ago(18)},
			}},
		{ID: "ci-2", Repo: "acme/widgets", Ref: "grain/task-231", Commit: "2222222", Conclusion: ci.ConclusionSuccess,
			StartedAt: ago(200), FinishedAt: ago(197),
			Jobs: []ci.JobRun{{Name: "ui e2e", JobID: "e2e", Conclusion: ci.ConclusionSuccess, StartedAt: ago(200), FinishedAt: ago(197)}}},
		{ID: "ci-1", Repo: "acme/gadgets", Ref: "main", Commit: "1111111", Conclusion: ci.ConclusionErrored,
			StartedAt: ago(5000), FinishedAt: ago(4999)},
	}, filter, limit), nil
}

func listCIRuns(t *testing.T, args map[string]any) string {
	t.Helper()
	tool := ciToolsEverywhere(&filteredCIRunner{now: time.Now()})[1]
	result := tool.Handler(context.Background(), args)
	if result.IsError {
		t.Fatalf("list_ci_runs(%v): %s", args, result.Text)
	}
	return result.Text
}

func listedRunIDs(text string) []string {
	var ids []string
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "Grain CI run "); ok {
			ids = append(ids, strings.Fields(rest)[0])
		}
	}
	return ids
}

func TestListCIRunsFiltersCompose(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"what is red right now", map[string]any{"conclusion": []any{"failure", "errored"}}, "ci-3 ci-1"},
		{"one repo", map[string]any{"repo": "acme/gadgets"}, "ci-1"},
		{"one branch", map[string]any{"ref": "task-231"}, "ci-2"},
		{"still going", map[string]any{"conclusion": "running"}, "ci-4"},
		{"a window", map[string]any{"since": "2h"}, "ci-4 ci-3"},
		// One condition would answer with more; all three together are
		// the question somebody actually has.
		{"repo and conclusion and job at once", map[string]any{
			"repo": "acme/widgets", "conclusion": "failure", "job": "ui e2e",
		}, "ci-3"},
		{"longest first", map[string]any{"sort": "duration"}, "ci-3 ci-4 ci-2 ci-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(listedRunIDs(listCIRuns(t, tc.args)), " ")
			if got != tc.want {
				t.Errorf("listed %q, want %q", got, tc.want)
			}
		})
	}
}

// An answer that matched nothing says so, says what it was asked, and
// says the deployment is not empty -- three facts a bare "no runs" left
// a reader to guess between.
func TestListCIRunsSaysWhenNothingMatches(t *testing.T) {
	text := listCIRuns(t, map[string]any{"repo": "acme/widgets", "conclusion": "cancelled"})
	for _, want := range []string{"No grain CI run matches", "repo acme/widgets", "result cancelled", "remembers 4 runs"} {
		if !strings.Contains(text, want) {
			t.Errorf("an empty answer does not say %q:\n%s", want, text)
		}
	}
}

// The answer names all three ways a run can be missing from it: not
// remembered, not matching, and past the limit.
func TestListCIRunsSaysWhatItLeftOut(t *testing.T) {
	text := listCIRuns(t, map[string]any{"repo": "acme/widgets", "limit": float64(1)})
	for _, want := range []string{
		"Filter: repo acme/widgets",
		"3 of 4 remembered grain CI runs match",
		"showing 1 (newest first)",
		"2 older matching runs are not shown",
		`raise "limit"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer does not say %q:\n%s", want, text)
		}
	}
}

// Asking about one job is asking to read down a column of that job's own
// results and durations -- not the run's other jobs, and not a number
// that cannot tell a timeout from an assertion.
func TestListCIRunsAboutOneJobReportsThatJobAndItsDuration(t *testing.T) {
	text := listCIRuns(t, map[string]any{"job": "ui e2e"})
	if strings.Contains(text, "go tests") {
		t.Errorf("a list narrowed to one job reported the others too:\n%s", text)
	}
	if !strings.Contains(text, "Job ui e2e: failure in 42m") {
		t.Errorf("the named job's own result and duration are missing:\n%s", text)
	}
	if !strings.Contains(text, "Job ui e2e: running for 5m") {
		t.Errorf("a job still going does not say how long it has been going:\n%s", text)
	}
}

// A word the vocabulary does not have is refused with the words it does.
// Answering with every run in the deployment reads as a fact about CI
// rather than as a typo, which is the whole failure this filtering is
// here to end.
func TestListCIRunsRefusesAWordItDoesNotKnow(t *testing.T) {
	tool := ciToolsEverywhere(&filteredCIRunner{now: time.Now()})[1]
	for _, args := range []map[string]any{
		{"conclusion": "failed"},
		{"since": "last tuesday"},
		{"sort": "length"},
		{"order": "sideways"},
	} {
		result := tool.Handler(context.Background(), args)
		if !result.IsError {
			t.Errorf("list_ci_runs(%v) was answered rather than refused:\n%s", args, result.Text)
		}
	}
}

// A job cancelled at its timeout is either starved or slow, and an agent
// told only "cancelled" reads it as its own change. The job's duration
// cannot say which: it starts when the job gets a machine, and a job
// that never got one has none at all (grain/task-326).
func TestWaitForChecksSaysHowLongAJobQueuedAndWhatFor(t *testing.T) {
	runner := &fakeGrainCIRunner{run: &ci.Run{
		ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345", Conclusion: ci.ConclusionCancelled,
		Jobs: []ci.JobRun{{
			JobID: "image", Name: "image", Conclusion: ci.ConclusionCancelled,
			Queued: 36 * time.Minute,
			QueuedReason: "for room in this deployment's sandbox budget -- memory: 52.0 GiB is " +
				"committed of this deployment's 64.0 GiB sandbox budget",
		}},
	}}
	result := ciTools(runner)[0].Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "Queued for 36m") {
		t.Errorf("a job that spent its whole life queueing does not say so:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "sandbox budget") {
		t.Errorf("and does not say what held it, which is what makes it the deployment's fault "+
			"rather than the branch's:\n%s", result.Text)
	}
}

func TestWaitForChecksAnnotatesRegression(t *testing.T) {
	currentRun := ci.Run{
		ID: "ci-branch", Repo: testCIRepo, Ref: "grain/task-654", Commit: "def567890123", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				JobID: "unit-tests", Name: "unit-tests", Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{{Name: "test", Conclusion: ci.ConclusionFailure, Output: "FAIL: TestNewFeature\nassertion failed"}},
			},
		},
	}
	baseRun := ci.Run{
		ID: "ci-base", Repo: testCIRepo, Ref: "main", Commit: "abc123456789", Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{
			{JobID: "unit-tests", Name: "unit-tests", Conclusion: ci.ConclusionSuccess},
		},
	}

	runner := &fakeGrainCIRunner{
		run: &currentRun,
	}
	// Configure FindGrainCIRuns to return baseRun and currentRun
	tools := ciTools(runner)
	// We can directly call renderGrainCIRun with attribution or waitForGrainCI
	text := renderGrainCIRun(currentRun, 5*time.Second, false, nil, map[string]ci.JobAttribution{
		"unit-tests": {
			Kind:       ci.AttributionRegression,
			JobID:      "unit-tests",
			JobName:    "unit-tests",
			BaseRef:    "main",
			BaseCommit: baseRun.Commit,
			Reason:     "The job passed on the base branch at commit abc1234 but failed on this commit.",
			Guidance:   ci.GuidanceRegression,
		},
	})

	if !strings.Contains(text, "Attribution: [REGRESSION]") {
		t.Errorf("expected [REGRESSION] attribution, got:\n%s", text)
	}
	if !strings.Contains(text, "This failure was introduced by your changes.") {
		t.Errorf("expected regression guidance, got:\n%s", text)
	}
	_ = tools
}

func TestWaitForChecksAnnotatesSharedBaseFault(t *testing.T) {
	currentRun := ci.Run{
		ID: "ci-branch", Repo: testCIRepo, Ref: "grain/task-654", Commit: "def567890123", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				JobID: "integration", Name: "integration", Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{{Name: "db-migrate", Conclusion: ci.ConclusionFailure, Output: "FATAL: db unavailable"}},
			},
		},
	}

	text := renderGrainCIRun(currentRun, 5*time.Second, false, nil, map[string]ci.JobAttribution{
		"integration": {
			Kind:       ci.AttributionSharedBaseFault,
			JobID:      "integration",
			JobName:    "integration",
			BaseRef:    "main",
			BaseCommit: "111122233344",
			Reason:     "The job failed with the same signature on main at commit 1111222.",
			Guidance:   ci.GuidanceSharedBaseFault,
		},
	})

	if !strings.Contains(text, "Attribution: [SHARED / BASE FAULT]") {
		t.Errorf("expected [SHARED / BASE FAULT] attribution, got:\n%s", text)
	}
	if !strings.Contains(text, "This is a shared upstream issue on the base branch, not caused by your changes. Do not attempt to fix this locally.") {
		t.Errorf("expected shared fault guidance, got:\n%s", text)
	}
}

func TestGetCILogAliasesAndDefaults(t *testing.T) {
	runner := &fakeGrainCIRunner{
		run: &ci.Run{
			ID:     "ci-123",
			Repo:   testCIRepo,
			Ref:    "grain/task-1",
			Commit: "abcdef012345",
			Jobs: []ci.JobRun{
				{
					JobID: "build",
					Name:  "build",
					Steps: []ci.StepRun{
						{
							Name:   "compile",
							Output: "line 1\nline 2\nline 3\nline 4\nline 5\n",
						},
					},
				},
			},
		},
	}
	tools := ciTools(runner)
	var getCILogTool Tool
	for _, tool := range tools {
		if tool.Name == "get_ci_log" {
			getCILogTool = tool
			break
		}
	}
	if getCILogTool.Name == "" {
		t.Fatal("get_ci_log tool not found")
	}

	// 1. Omitted run_id defaults to newest in scope
	res := getCILogTool.Handler(context.Background(), map[string]any{
		"job_id":    "build",
		"step_name": "compile",
		"limit":     float64(20),
	})
	if res.IsError {
		t.Fatalf("get_ci_log failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "line 1") {
		t.Errorf("expected output to contain 'line 1', got:\n%s", res.Text)
	}

	// 2. tail = true
	resTail := getCILogTool.Handler(context.Background(), map[string]any{
		"run_id": "ci-123",
		"job":    "build",
		"step":   "compile",
		"tail":   true,
		"limit":  float64(10),
	})
	if resTail.IsError {
		t.Fatalf("get_ci_log tail: true failed: %s", resTail.Text)
	}
	if !strings.Contains(resTail.Text, "line 5") {
		t.Errorf("expected tail to contain 'line 5', got:\n%s", resTail.Text)
	}
}

func TestRetriggerCI(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	tools := ciTools(runner)
	var retriggerTool Tool
	for _, tool := range tools {
		if tool.Name == "retrigger_ci" {
			retriggerTool = tool
			break
		}
	}
	if retriggerTool.Name == "" {
		t.Fatal("retrigger_ci tool not found")
	}

	res := retriggerTool.Handler(context.Background(), map[string]any{
		"workflow": ".grain/workflows/ci.yml",
		"commit":   "feedface0000",
	})
	if res.IsError {
		t.Fatalf("retrigger_ci failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Triggered fresh uncached Grain CI run") {
		t.Errorf("expected success message, got:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "feedface0000") {
		t.Errorf("expected commit SHA in output, got:\n%s", res.Text)
	}
}

// A recheck's verdict replaces the cached one it re-ran (ci.Engine saves
// a Recheck's cacheable result). Before that, a flake's cached red
// outlived the recheck that cleared it, and agents learned to make
// cosmetic edits to change the fingerprint. The descriptions are what an
// agent reads, so they have to say the cache now moves.
func TestRecheckDescriptionsSayTheVerdictReplacesTheCachedOne(t *testing.T) {
	runner := &fakeGrainCIRunner{}
	descs := map[string]string{}
	for _, tool := range ciTools(runner) {
		descs[tool.Name] = tool.Description
	}
	for name, want := range map[string][]string{
		"retrigger_ci": {"Rerun just the failed jobs", "saved as the cached verdict for its own fingerprint",
			"passed jobs' verdicts stay as they are", "merge-queue gate read combines every job's latest result"},
		"wait_for_checks": {"a cached red that a retrigger_ci run has since cleared is no longer replayed", "no need to make cosmetic edits",
			"merge gate that reads it, combine each job's latest result"},
	} {
		desc, ok := descs[name]
		if !ok {
			t.Fatalf("%s tool not found", name)
		}
		for _, phrase := range want {
			if !strings.Contains(desc, phrase) {
				t.Errorf("%s's description does not say %q:\n%s", name, phrase, desc)
			}
		}
	}

	res := namedTool(t, ciTools(runner), "retrigger_ci").Handler(context.Background(), map[string]any{})
	if !strings.Contains(res.Text, "replaces the cached verdict for its fingerprint") {
		t.Errorf("retrigger_ci's answer does not say its result replaces the cached one:\n%s", res.Text)
	}
}

// retrigger_ci reruns just the failed jobs unless told otherwise
// (grain/task-2124): no jobs is the failed ones, a list is that list, and
// "*" is every job of the workflow.
func TestRetriggerCIJobSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     map[string]any
		wantJobs []string
		wantAll  bool
	}{
		{name: "default reruns the failed jobs", args: map[string]any{}},
		{name: "an explicit list", args: map[string]any{"jobs": []any{"ui e2e", " lint "}}, wantJobs: []string{"ui e2e", "lint"}},
		{name: "star reruns everything", args: map[string]any{"jobs": []any{"*"}}, wantAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeGrainCIRunner{}
			res := namedTool(t, ciTools(runner), "retrigger_ci").Handler(context.Background(), tc.args)
			if res.IsError {
				t.Fatalf("retrigger_ci failed: %s", res.Text)
			}
			if !slices.Equal(runner.retriggerJobs, tc.wantJobs) || runner.retriggerAll != tc.wantAll {
				t.Errorf("asked to rerun jobs %q (all %v), want %q (all %v)",
					runner.retriggerJobs, runner.retriggerAll, tc.wantJobs, tc.wantAll)
			}
		})
	}
}

// A full-suite wait handed the recheck its verdict is waiting on says
// that it lists only the failed jobs, and that the verdict combines them
// with the ones that passed.
func TestRecheckRunNote(t *testing.T) {
	recheck := ci.Run{ID: "ci-2", Recheck: true}
	if note := recheckRunNote(nil, recheck); !strings.Contains(note, "combine it with the jobs that had already passed") {
		t.Errorf("full-suite wait on a recheck says %q", note)
	}
	if note := recheckRunNote([]string{"go"}, recheck); note != "" {
		t.Errorf("selective wait says %q, want nothing", note)
	}
	if note := recheckRunNote(nil, ci.Run{ID: "ci-3"}); note != "" {
		t.Errorf("complete run says %q, want nothing", note)
	}
}

func TestQueryTestHistoryTool(t *testing.T) {
	now := time.Now()
	t1 := now.Add(-1 * time.Hour)
	runner := &fakeGrainCIRunner{
		runs: []ci.Run{
			{
				ID:     "ci-hist-1",
				Repo:   testCIRepo,
				Ref:    "main",
				Commit: "aaaa1111",
				Jobs: []ci.JobRun{
					{
						JobID:      "go",
						Name:       "go test",
						Conclusion: ci.ConclusionSuccess,
						StartedAt:  &t1,
						Steps: []ci.StepRun{
							{Output: "=== RUN   TestSample\n--- PASS: TestSample (0.01s)\nPASS\n"},
						},
					},
				},
			},
		},
	}
	tools := ciTools(runner)
	var queryTool Tool
	for _, tool := range tools {
		if tool.Name == "query_test_history" {
			queryTool = tool
			break
		}
	}
	if queryTool.Name == "" {
		t.Fatal("query_test_history tool not found")
	}

	res := queryTool.Handler(context.Background(), map[string]any{
		"test_name": "TestSample",
	})
	if res.IsError {
		t.Fatalf("query_test_history failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Test History: TestSample") {
		t.Errorf("expected report heading, got:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "Passed:  1 (100.0%)") {
		t.Errorf("expected pass stat, got:\n%s", res.Text)
	}
}

// unreadableCIRunner starts a run and then cannot read it back.
type unreadableCIRunner struct{ twoRepoCIRunner }

func (r *unreadableCIRunner) StartGrainCI(context.Context, []string) (ci.Run, error) {
	return ci.Run{ID: "ci-started"}, nil
}

func (r *unreadableCIRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	return ci.Run{}, fmt.Errorf("daemon unreachable")
}

// A failed poll names the run it could not read, rather than the empty
// id of the zero run the failed read handed back.
func TestWaitForGrainCIErrorNamesTheRun(t *testing.T) {
	_, err := waitForGrainCI(context.Background(), &unreadableCIRunner{}, nil, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "ci-started") {
		t.Errorf("err = %v, want it to name run ci-started", err)
	}
	_, err = runSingleCIJob(context.Background(), &unreadableCIRunner{}, "go", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "ci-started") {
		t.Errorf("run_ci_job err = %v, want it to name run ci-started", err)
	}
}

// A job that passed because its failing test is quarantined keeps that
// test in its FailureSummary for test history; wait_for_checks does not
// print it under the green job as a failed test (grain/task-1572).
func TestCIStatusDoesNotListAQuarantinedPassAsFailedTests(t *testing.T) {
	run := ci.Run{ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345", Conclusion: ci.ConclusionSuccess,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess, Quarantined: true,
			QuarantinedTests: []string{"TestFlaky"},
			FailureSummary: &ci.FailureSummary{Tests: []ci.TestFailure{
				{Name: "TestFlaky", Message: "timed out", Quarantined: true},
			}}}}}
	text := renderGrainCIRun(run, time.Second, false, nil)
	if strings.Contains(text, "Failed tests") || strings.Contains(text, "TestFlaky") {
		t.Errorf("a quarantined pass was reported with failed tests:\n%s", text)
	}
}

// newestRunCIRunner remembers two runs of the repository, the older one
// listed first, and records which run a tool went on to read.
type newestRunCIRunner struct {
	fakeGrainCIRunner
	asked []string
}

func (n *newestRunCIRunner) GrainCIRun(ctx context.Context, id string) (ci.Run, error) {
	n.asked = append(n.asked, id)
	return n.fakeGrainCIRunner.GrainCIRun(ctx, id)
}

// get_ci_log with no run_id reads the newest run, as its description
// says. The zero ci.RunFilter is oldest first (ci.SortRuns), so a default
// that asked for one run with it read the repository's oldest run
// instead (grain/task-2388).
func TestGetCILogDefaultsToTheNewestRun(t *testing.T) {
	older, newer := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC()
	runner := &newestRunCIRunner{fakeGrainCIRunner: fakeGrainCIRunner{runs: []ci.Run{
		{ID: "ci-old", Repo: testCIRepo, Ref: "grain/task-1", StartedAt: &older, Conclusion: ci.ConclusionFailure},
		{ID: "ci-new", Repo: testCIRepo, Ref: "grain/task-1", StartedAt: &newer, Conclusion: ci.ConclusionFailure},
	}}}
	for _, tool := range ciTools(runner) {
		if tool.Name != "get_ci_log" {
			continue
		}
		tool.Handler(context.Background(), map[string]any{})
		if len(runner.asked) != 1 || runner.asked[0] != "ci-new" {
			t.Fatalf("get_ci_log with no run_id read %v, want the newest run ci-new", runner.asked)
		}
		return
	}
	t.Fatal("no get_ci_log tool")
}

// When two branches have runs, get_ci_log defaults to the newest run on
// the task's own branch, even if another branch has a newer run. If the
// task's branch has no runs, it falls back to the newest run in the repo.
func TestGetCILogDefaultsToNewestRunOnBranch(t *testing.T) {
	t.Parallel()
	t1, t2, t3 := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC(), time.Unix(300, 0).UTC()
	runs := []ci.Run{
		{ID: "branchA-old", Repo: testCIRepo, Ref: "grain/task-1", StartedAt: &t1, Conclusion: ci.ConclusionFailure},
		{ID: "branchA-new", Repo: testCIRepo, Ref: "grain/task-1", StartedAt: &t2, Conclusion: ci.ConclusionFailure},
		{ID: "branchB-newest", Repo: testCIRepo, Ref: "grain/task-2", StartedAt: &t3, Conclusion: ci.ConclusionFailure},
	}

	// Task on branch grain/task-1 should pick branchA-new, not branchB-newest.
	runnerA := &newestRunCIRunner{fakeGrainCIRunner: fakeGrainCIRunner{runs: runs}}
	toolsA := NewGrainCIWaitTools(runnerA, CIRepoScope{Repo: testCIRepo}, "grain/task-1")
	for _, tool := range toolsA {
		if tool.Name != "get_ci_log" {
			continue
		}
		res := tool.Handler(context.Background(), map[string]any{})
		if res.IsError {
			t.Fatalf("get_ci_log failed: %s", res.Text)
		}
		if len(runnerA.asked) != 1 || runnerA.asked[0] != "branchA-new" {
			t.Fatalf("get_ci_log on task-1 picked %v, want branchA-new", runnerA.asked)
		}
	}

	// Task on branch with no runs (grain/task-3) falls back to newest in repo (branchB-newest).
	runnerFallback := &newestRunCIRunner{fakeGrainCIRunner: fakeGrainCIRunner{runs: runs}}
	toolsFallback := NewGrainCIWaitTools(runnerFallback, CIRepoScope{Repo: testCIRepo}, "grain/task-3")
	for _, tool := range toolsFallback {
		if tool.Name != "get_ci_log" {
			continue
		}
		res := tool.Handler(context.Background(), map[string]any{})
		if res.IsError {
			t.Fatalf("get_ci_log fallback failed: %s", res.Text)
		}
		if len(runnerFallback.asked) != 1 || runnerFallback.asked[0] != "branchB-newest" {
			t.Fatalf("get_ci_log on task-3 fallback picked %v, want branchB-newest", runnerFallback.asked)
		}
	}
}

// A scheduled workflow's run says which schedule started it: a green
// night files no review (grain/task-3160), so the run list is one of the
// two places it is seen.
func TestRenderGrainCIRunNamesTheScheduleThatStartedIt(t *testing.T) {
	run := ci.Run{ID: "run-7", Ref: "candidate", Conclusion: ci.ConclusionSuccess,
		Event: ci.EventSchedule, RequestedBy: "workflow-schedule:repo.schedule.acme/widgets.workflow.nightly"}
	if text := renderGrainCIRun(run, -1, false, nil); !strings.Contains(text,
		"Started by schedule repo.schedule.acme/widgets.workflow.nightly.") {
		t.Fatalf("run does not name its schedule: %s", text)
	}
	run.RequestedBy = "someone"
	if text := renderGrainCIRun(run, -1, false, nil); strings.Contains(text, "Started by schedule") {
		t.Fatalf("a run nobody scheduled names a schedule: %s", text)
	}
}

func TestRetriedJobWithCancelledRetryReportsAttempt1FailureAndLogs(t *testing.T) {
	run := ci.Run{
		ID:         "883a3337aa7850f7",
		Repo:       testCIRepo,
		Ref:        "grain/task-796",
		Commit:     "abcdef012345",
		Conclusion: ci.ConclusionCancelled,
		Jobs: []ci.JobRun{
			{
				JobID:       "go",
				Name:        "go",
				Conclusion:  ci.ConclusionCancelled,
				Retried:     true,
				RetryCount:  1,
				RetryReason: `step "Test" failed (exit 1)`,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionCancelled,
						Output:     "attempt 2 running tests before timeout cancellation\n",
					},
				},
				Attempts: []ci.JobAttempt{
					{
						Attempt:    1,
						Conclusion: ci.ConclusionFailure,
						FailureSummary: &ci.FailureSummary{
							Tests: []ci.TestFailure{
								{Name: "pkg/e2e.TestFlakyRetry", Location: "e2e_test.go:99", Message: "expected 200 got 500"},
							},
						},
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionFailure,
								ExitCode:   1,
								Output:     "=== RUN   TestFlakyRetry\n    e2e_test.go:99: expected 200 got 500\n--- FAIL: TestFlakyRetry (0.50s)\nFAIL\n",
							},
						},
					},
					{
						Attempt:    2,
						Conclusion: ci.ConclusionCancelled,
						Steps: []ci.StepRun{
							{
								Name:       "Test",
								Conclusion: ci.ConclusionCancelled,
								Output:     "attempt 2 running tests before timeout cancellation\n",
							},
						},
					},
				},
			},
		},
	}

	runner := &fakeGrainCIRunner{run: &run}
	tools := ciTools(runner)

	waitRes := tools[0].Handler(context.Background(), map[string]any{})
	if waitRes.IsError {
		t.Fatalf("wait_for_checks failed: %s", waitRes.Text)
	}
	for _, want := range []string{
		`/fs/ci/acme/widgets/883a3337aa7850f7/go/attempts/1/`,
		`TestFlakyRetry`,
		`Attempt 1 step Test: failure`,
	} {
		if !strings.Contains(waitRes.Text, want) {
			t.Errorf("wait_for_checks missing %q in:\n%s", want, waitRes.Text)
		}
	}

	// Default get_ci_log (without attempt) should select the failing attempt 1 rather than the cancelled retry.
	logRes := tools[2].Handler(context.Background(), map[string]any{"run_id": run.ID})
	if logRes.IsError {
		t.Fatalf("get_ci_log failed: %s", logRes.Text)
	}
	for _, want := range []string{
		`attempt 1 of 2`,
		`http://127.0.0.1:9728/fs/ci/acme/widgets/883a3337aa7850f7/go/attempts/1/`,
		`expected 200 got 500`,
	} {
		if !strings.Contains(logRes.Text, want) {
			t.Errorf("default get_ci_log missing %q in:\n%s", want, logRes.Text)
		}
	}

	// Explicit attempt 2 reads the retry's log.
	logRes2 := tools[2].Handler(context.Background(), map[string]any{"run_id": run.ID, "job": "go", "attempt": float64(2)})
	if logRes2.IsError {
		t.Fatalf("get_ci_log attempt 2 failed: %s", logRes2.Text)
	}
	for _, want := range []string{
		`attempt 2 of 2`,
		`attempt 2 running tests before timeout cancellation`,
	} {
		if !strings.Contains(logRes2.Text, want) {
			t.Errorf("attempt 2 get_ci_log missing %q in:\n%s", want, logRes2.Text)
		}
	}
}
