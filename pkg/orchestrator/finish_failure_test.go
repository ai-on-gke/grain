package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// refusingPullRequestClient wraps a real github.Client and refuses to
// open a pull request, the same targeted-injection shape failingClient
// (isolation_test.go) and panickingClient (panic_recovery_test.go)
// already use. It stands in for every persistent reason GitHub declines
// the one call that turns a pushed branch into a finished task -- a base
// branch that no longer exists, a token that lost its write scope, a
// repository archived under the run's feet.
type refusingPullRequestClient struct {
	github.Client
}

func (refusingPullRequestClient) CreatePullRequest(owner, repo, head, base, title, body string) (github.PullRequest, error) {
	return github.PullRequest{}, errors.New("422 Validation Failed: base is invalid")
}

// A run can do everything right and still leave its task un-finished:
// the agent commits and pushes, the framework returns cleanly, and then
// the GitHub call that turns that branch into a pull request fails. The
// task is left un-completed, so dispatch offers it again -- and the run's
// own row said nothing whatsoever about why, because RunDispatch had
// already written its outcome before ProcessResult ever ran and nothing
// afterwards corrected it. All a human saw was an attempt whose detail
// described a run that went fine, repeated.
func TestRunCycleRecordsWhyAFinishedRunNeverBecameAPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	// The shape of a run that worked: the branch is on GitHub and the
	// framework returned no error at all.
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	pushed := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "committed"},
			{Name: "run_command", Text: "pushed"},
		}}, nil
	})

	deps := orchestrator.Deps{
		Store:      store,
		Client:     refusingPullRequestClient{Client: client},
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pushed),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil {
		t.Fatal("expected RunCycle to report the refused pull request")
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if runs[0].Outcome != "finish-failed" {
		t.Errorf("outcome = %q, want finish-failed: the agent's run was fine, the finish was not", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "base is invalid") {
		t.Errorf("detail = %q, want GitHub's own refusal on the row a human reads", runs[0].Detail)
	}

	// Queued, not completed: one refused pull request is not the cap, so
	// the task is offered again -- which is the right answer for a
	// transient failure and the reason the row above has to say what the
	// failure was for a persistent one.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued", st)
	}

	// And it counts as a failure, so the retry backs off rather than
	// spinning on the same refusal every tick.
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 1 {
		t.Fatalf("failure streak = %+v, want a single counted failure", streak)
	}
}

// The other half of the same rule: a run whose finish went fine keeps the
// outcome it earned, and nothing here writes a second time.
func TestRunCycleLeavesASuccessfulFinishAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	// An erroring tool call among the good ones, because that is the
	// ordinary shape of real agentic work (a grep that matched nothing,
	// a test that failed before the agent fixed it) and it must not cost
	// the run its outcome -- see outcomeOf.
	pushed := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "exit=1\nstdout:\n\nstderr:\n", IsError: true},
			{Name: "run_command", Text: "pushed"},
		}}, nil
	})

	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pushed),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if runs[0].Outcome != "succeeded" {
		t.Errorf("outcome = %q, want succeeded: a grep that matched nothing is not a failed run", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "1 of them returned an error") {
		t.Errorf("detail = %q, want the errored call still counted", runs[0].Detail)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit: the finish stood, and its pull request is waiting on a Submit click", st)
	}
	// The whole point of the outcome above: nothing about this task now
	// reads as a failure to back off from.
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak != nil && streak.Count != 0 {
		t.Errorf("failure streak = %+v, want none on a task that pushed and completed", streak)
	}
}
