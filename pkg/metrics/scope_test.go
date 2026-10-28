package metrics_test

// Scope: a report about some of this deployment's tasks rather than all
// of them (grain/task-83). What these test is the one thing a scope can
// get wrong that nothing else would catch -- a report whose sections are
// each over a different set of tasks, because the runs, the states and
// the census rows were narrowed by a different rule from the tasks.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// twoRepos is a deployment with one task in each of two repos, each with
// one attempt, each attempt with a tool call and a CI wait recorded, and
// one task with no repo at all.
func twoRepos() metrics.Input {
	return metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			{TaskID: "1", Repo: "acme/widgets", Author: "ada",
				CreatedAt: at(time.Hour), ApprovedAt: at(time.Hour), CompletedAt: at(2 * time.Hour), Targeted: true},
			{TaskID: "2", Repo: "acme/docs", Author: "grace",
				CreatedAt: at(time.Hour), ApprovedAt: at(time.Hour), CompletedAt: at(3 * time.Hour), Targeted: true},
			// No repo to push to, which is not the same as belonging to
			// every repo: a scoped report must leave it out.
			{TaskID: "3", Author: "ada", CreatedAt: at(time.Hour), ApprovedAt: at(time.Hour),
				CompletedAt: at(4 * time.Hour)},
		},
		Runs: []model.RunTiming{
			{RunID: "r1", TaskID: "1", Attempt: 1, StartedAt: *at(time.Hour),
				AgentStartedAt: at(time.Hour + time.Minute), FinishedAt: at(2 * time.Hour), Outcome: "succeeded"},
			{RunID: "r2", TaskID: "2", Attempt: 1, StartedAt: *at(time.Hour),
				AgentStartedAt: at(time.Hour + time.Minute), FinishedAt: at(3 * time.Hour), Outcome: "failed"},
			{RunID: "r3", TaskID: "3", Attempt: 1, StartedAt: *at(time.Hour),
				AgentStartedAt: at(time.Hour + time.Minute), FinishedAt: at(4 * time.Hour), Outcome: "succeeded"},
		},
		States: map[string]model.State{
			"1": model.StateCompleted, "2": model.StateQueued, "3": model.StateQueued,
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "run_command", Calls: 3},
			{RunID: "r2", Tool: "run_command", Calls: 5},
			{RunID: "r3", Tool: "run_command", Calls: 7},
		},
		CheckWaits: []model.RunCheckWait{
			{RunID: "r1", Seq: 1, Verdict: "passed", Waited: time.Minute, PushesBefore: 1},
			{RunID: "r2", Seq: 1, Verdict: "failed", Waited: 2 * time.Minute, PushesBefore: 1},
			{RunID: "r3", Seq: 1, Verdict: "passed", Waited: 3 * time.Minute, PushesBefore: 1},
		},
	}
}

func TestAnEmptyScopeIsEveryTask(t *testing.T) {
	in := twoRepos()
	whole := metrics.Compute(in)
	in.Scope = metrics.Scope{}
	if got := metrics.Compute(in); got.Throughput.TasksCompleted != whole.Throughput.TasksCompleted {
		t.Errorf("an empty scope changed the report: %d completions, want %d",
			got.Throughput.TasksCompleted, whole.Throughput.TasksCompleted)
	}
	if !whole.Scope.Empty() {
		t.Errorf("Report.Scope = %+v, want empty", whole.Scope)
	}
	if got, want := whole.Throughput.TasksCompleted, 3; got != want {
		t.Errorf("unscoped TasksCompleted = %d, want %d", got, want)
	}
}

// Every section narrowed by the same rule, which is the whole of what a
// scope has to get right: a throughput over one repo beside a tool census
// over the deployment would be two reports wearing one heading.
func TestAScopeNarrowsEverySectionToTheSameTasks(t *testing.T) {
	in := twoRepos()
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets"}}
	rep := metrics.Compute(in)

	if got, want := rep.Throughput.TasksCompleted, 1; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
	if got, want := rep.Throughput.RunsFinished, 1; got != want {
		t.Errorf("RunsFinished = %d, want %d", got, want)
	}
	if got, want := rep.Runs.Outcomes["succeeded"], 1; got != want {
		t.Errorf("succeeded runs = %d, want %d", got, want)
	}
	if _, unwanted := rep.Runs.Outcomes["failed"]; unwanted {
		t.Errorf("the other repo's failed run is in a report scoped away from it: %v", rep.Runs.Outcomes)
	}
	// The census rows follow the runs, which follow the tasks: three
	// calls is r1's alone, and one wait is r1's alone.
	if got, want := rep.Tools.Calls, 3; got != want {
		t.Errorf("tool calls = %d, want %d", got, want)
	}
	if got, want := rep.Checks.Waits, 1; got != want {
		t.Errorf("CI waits = %d, want %d", got, want)
	}
	// And the backlog, which is read off States rather than off the
	// tasks -- the one input where forgetting to narrow would leave the
	// whole deployment's backlog under a scoped heading.
	if got, want := rep.Backlog.Queued, 0; got != want {
		t.Errorf("queued = %d, want %d (the queued tasks are in the other repo)", got, want)
	}
	if got, want := rep.Scope.Repos[0], "acme/widgets"; got != want {
		t.Errorf("Report.Scope.Repos = %v, want %q", rep.Scope.Repos, want)
	}
}

func TestAScopeMatchesAnyOfTheReposItNames(t *testing.T) {
	in := twoRepos()
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets", "acme/docs"}}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 2; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
}

// A repo and an author together are both conditions, not either: "what
// has Ada landed in acme/widgets" is one task here and not two.
func TestRepoAndAuthorNarrowTogether(t *testing.T) {
	in := twoRepos()
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets"}, Authors: []string{"grace"}}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 0; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets"}, Authors: []string{"ada"}}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 1; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
}

// A task with no repo to push to is in no repo-scoped report -- it
// carries "" rather than belonging to everything -- and is still in an
// author-scoped one, because it has an author like every other task.
func TestATaskWithNoRepoIsInNoRepoScopedReport(t *testing.T) {
	in := twoRepos()
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets", "acme/docs"}}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 2; got != want {
		t.Errorf("TasksCompleted = %d, want %d (the repo-less task is not in either repo)", got, want)
	}
	in.Scope = metrics.Scope{Authors: []string{"ada"}}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 2; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
}

// The two inputs a scope deliberately leaves alone: a tick belongs to the
// deployment rather than to any set of tasks, and the concurrency ceiling
// utilization is a fraction of is the deployment's too.
func TestAScopeLeavesTheDaemonsOwnTicksAlone(t *testing.T) {
	in := twoRepos()
	in.MaxConcurrent = 4
	in.Cycles = metrics.CycleHistory{
		Observed: 2,
		Samples: []metrics.CycleSample{
			{Start: since.Add(time.Hour), Duration: time.Second, DispatchWait: time.Millisecond},
			{Start: since.Add(2 * time.Hour), Duration: 2 * time.Second, DispatchWait: time.Millisecond},
		},
	}
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets"}}
	rep := metrics.Compute(in)
	if got, want := rep.Cycles.N, 2; got != want {
		t.Errorf("Cycles.N = %d, want %d -- a scope must not narrow the daemon's own ticks", got, want)
	}
	// Utilization is still a fraction of the whole deployment's ceiling,
	// which is what makes a scoped one readable as "the share of
	// capacity these tasks took".
	if rep.Runs.Utilization <= 0 {
		t.Errorf("Utilization = %v, want the scoped runs' share of the ceiling", rep.Runs.Utilization)
	}
}

// A bound scope with no repositories in it is a reader whose teams have
// none on record: they are shown nothing, not the whole deployment an
// empty Repos otherwise means.
func TestABoundScopeWithNoReposIsNoTask(t *testing.T) {
	in := twoRepos()
	in.Scope = metrics.Scope{ReposBound: true}
	rep := metrics.Compute(in)
	if got := rep.Throughput.TasksCompleted; got != 0 {
		t.Errorf("TasksCompleted = %d, want 0", got)
	}
	if got := rep.Tools.Calls; got != 0 {
		t.Errorf("tool calls = %d, want 0", got)
	}
	in.Scope = metrics.Scope{Repos: []string{"acme/docs"}, ReposBound: true}
	if got, want := metrics.Compute(in).Throughput.TasksCompleted, 1; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
}
