package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The other 422 GitHub answers forever, and base_test.go's sibling: a
// branch that carries no commits its base does not already have. GitHub
// refuses to open a pull request for it every time, so a run that pushed
// one -- because it reverted its own work, committed only what the base
// already had, or pushed without committing at all -- used to leave its
// task in exactly the loop a vanished base used to cause: un-completed,
// offered again after its backoff, working on top of the same branch and
// refused identically.
//
// Unlike a vanished base there is nothing to salvage: no work to open, no
// retarget that helps, no diff for a reviewer. So the answer is not
// "open it anyway" but "stop, and say why" -- see
// orchestrator.noteEmptyBranch. These tests are that ending, from both
// ends of a run.
//
// githubsim refuses this exactly as GitHub does, so a branch left
// unchecked here fails these tests the way it failed on the deployment.

// pushEmptyBranch puts branch on the remote pointing at base's own tip:
// a real branch, really pushed, carrying nothing of its own. It is what
// `git push origin HEAD:refs/heads/grain/task-1` leaves behind when the
// run never committed -- pushBranch's opposite number.
func pushEmptyBranch(t *testing.T, bare, branch, base string) {
	t.Helper()
	run(t, t.TempDir(), "git", "--git-dir", bare, "branch", branch, "refs/heads/"+base)
}

// pendingQuestion is the comment id a task is parked on, or nil when it
// is not parked at all -- the field that decides whether dispatch will
// ever offer this task again (task_state reads it as 'awaiting_reply',
// and task_ready selects only 'queued').
func pendingQuestion(t *testing.T, ctx context.Context, store *model.Store, taskID string) *int64 {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return nil
	}
	return obs.PendingQuestionCommentID
}

// The ending itself: a finished run whose branch adds nothing to its base
// stops the task and explains it, rather than being retried until the
// failure cap swallows it.
func TestAFinishedRunWhoseBranchAddsNothingEndsTheTaskAndSaysWhy(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")

	runID := "t1-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	// Not an error: there is nothing here for a caller to retry, and
	// reporting one would have RunCycle record finish-failed, which means
	// exactly the retry this ending exists to stop.
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult over an empty branch: %v", err)
	}

	if len(sim.PullRequests) != 0 {
		t.Fatalf("pull requests = %+v, want none: GitHub refuses this one every time", sim.PullRequests)
	}

	// Parked, so dispatch does not offer it again -- the whole point.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply: nothing about this branch improves by trying again", st)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ready {
		if id == task.ID {
			t.Fatalf("task %s is still ready to dispatch, want it parked", task.ID)
		}
	}

	// And it says why, in the task's own conversation, rather than only
	// in a run detail quoting GitHub.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("comments = %q, want one explaining the empty branch", bodies)
	}
	for _, want := range []string{model.BranchName(task.ID), "main", "acme/widgets", "no commits"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("comment = %q; want it to name %q", bodies[0], want)
		}
	}
	if pendingQuestion(t, ctx, store, task.ID) == nil {
		t.Error("task is not parked on the comment that explains it")
	}

	// The run's own row says what became of it, and says the word that
	// means "this produced nothing", not the one that means "try again".
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.LastOutcome != "no_action" {
		t.Fatalf("streak = %+v, want a run recorded no_action", streak)
	}
	if !strings.Contains(streak.LastDetail, "no commits") {
		t.Errorf("detail = %q, want it to say what the branch was missing", streak.LastDetail)
	}
}

// A human's reply is the way back: any comment clears the park, and the
// task is queued again with grain's explanation in the thread the next
// run reads. That is why this ends in a park rather than in a close --
// nothing about the task itself is known to be finished.
func TestAParkedEmptyBranchTaskIsQueuedAgainOnceSomebodyReplies(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// What ui.Client.Comment does to an awaiting_reply task, spelled out
	// here rather than reached through the UI package.
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "the revert was a mistake -- redo the change and commit it",
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingQuestionCommentID = nil
	}); err != nil {
		t.Fatal(err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued: a reply is all it takes to try again", st)
	}
}

// A run that signs off in a comment and pushes an empty branch has said
// two things that do not agree. The comment is relayed -- it is the run's
// own account and belongs in the thread -- but it does not complete the
// task the way a closing comment with no branch behind it would: nothing
// landed on the remote, and a person reading both is the whole reason
// this parks.
func TestAClosingCommentDoesNotCompleteATaskWhosePushedBranchIsEmpty(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		agent.ToolCall{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": "nothing needed changing after all"},
		},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply, not a task completed with nothing on the remote", st)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 2 {
		t.Fatalf("comments = %q, want the run's own sign-off and grain's account of the branch", bodies)
	}
	if !strings.Contains(bodies[0], "nothing needed changing") {
		t.Errorf("first comment = %q, want the run's own words kept", bodies[0])
	}
}

// The same condition, mid-run, is a different answer. A run that asks for
// its own pull request before committing anything still has turns left,
// so it is told to commit something -- and nothing is written to the task
// and nothing is parked, since the branch is not final yet.
func TestAskingForAPullRequestOnAnEmptyBranchTellsTheRunToCommitSomething(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err == nil {
		t.Fatal("expected open_pull_request to refuse a branch with nothing on it")
	}
	for _, want := range []string{model.BranchName(task.ID), "no commits", "commit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q; want it to mention %q", err, want)
		}
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Errorf("comments = %q, want none while the run is still going", bodies)
	}
	if pendingQuestion(t, ctx, store, task.ID) != nil {
		t.Error("task was parked mid-run, want the run left free to fix this itself")
	}
	if len(sim.PullRequests) != 0 {
		t.Errorf("pull requests = %+v, want none", sim.PullRequests)
	}
}

// blindCompareClient answers every compare with an error -- a token that
// cannot read the compare endpoint, a transient 502 -- while everything
// else works. It is what makes GitHub's own refusal the second line of
// defence rather than a case nothing covers.
type blindCompareClient struct {
	github.Client
}

func (blindCompareClient) CompareCommits(owner, repo, base, head string) ([]github.Commit, error) {
	return nil, errors.New("502 Bad Gateway")
}

// So the ending does not depend on the compare succeeding: a refusal
// GitHub reports is read as the same condition and answered the same way.
func TestAnEmptyBranchIsRecognisedFromGitHubsOwnRefusalToo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")

	blind := blindCompareClient{Client: client}
	if err := orchestrator.ProcessResult(ctx, store, blind, task, toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult with an unreadable compare: %v", err)
	}

	if pendingQuestion(t, ctx, store, task.ID) == nil {
		t.Error("task was not parked, so GitHub's own 422 went unrecognised")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 {
		t.Fatalf("comments = %q, want the one explaining the empty branch", bodies)
	}
	if len(sim.PullRequests) != 0 {
		t.Errorf("pull requests = %+v, want none", sim.PullRequests)
	}
}

// A compare that cannot be read must not by itself end a task: a branch
// that really is ahead is opened exactly as before, whatever the compare
// endpoint says. This is the guard on the check above -- reading a failed
// read as "empty" would end tasks over an API hiccup.
func TestAnUnreadableCompareStillOpensAPullRequestForARealBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	blind := blindCompareClient{Client: client}
	if err := orchestrator.ProcessResult(ctx, store, blind, task, toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"}), "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the one this finish opened", sim.PullRequests)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit: the pull request is open and nobody has submitted it", st)
	}
}

// And the whole loop, through RunCycle: an agent that pushes a branch
// with nothing on it gets one attempt that ends, explains itself and
// stops, where it used to get attempt after attempt saying nothing.
func TestRunCycleEndsATaskWhoseRunPushedABranchWithNothingOnIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	// The shape of the run this is about: it pushed, it returned cleanly,
	// and what it pushed adds nothing to main.
	pushEmptyBranch(t, sim.BareRepo, model.BranchName(task.ID), "main")
	pushed := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "reverted the change"},
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
	if runs[0].Outcome != "no_action" {
		t.Errorf("outcome = %q, want no_action: nothing openable came out of this run", runs[0].Outcome)
	}
	if !strings.Contains(runs[0].Detail, "no commits") {
		t.Errorf("detail = %q, want the reason on the row a human reads", runs[0].Detail)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}

	// The second cycle is the point: it offers this task nothing at all,
	// where before it offered the same run again.
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	runs, err = store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want still one: a parked task is not dispatched again", runs)
	}
}
