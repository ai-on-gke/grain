package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// linksTo counts the LinkFixes rows on taskID pointing at ref -- a count,
// not a bool, because "opened it twice and linked it twice" is exactly
// the failure these tests are here to rule out.
func linksTo(t *testing.T, ctx context.Context, store *model.Store, taskID, ref string) int {
	t.Helper()
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes && l.Target == ref {
			n++
		}
	}
	return n
}

// A run that opens its own pull request mid-flight gets a real one, and
// the task it belongs to is *not* ended by it: the run is still going,
// and a task marked completed here would be handed straight to
// SyncPullRequests' merge queue -- which could merge a branch the agent
// is still pushing to.
func TestOpenPullRequestForTaskOpensOneWithoutEndingTheTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if sim.PullRequests[0].Head != branch || sim.PullRequests[0].Base != "main" {
		t.Fatalf("got %+v", sim.PullRequests[0])
	}
	if status.PullRequest.Number != sim.PullRequests[0].Number {
		t.Fatalf("status = %+v, want the pull request just opened", status)
	}

	ref := model.PullRequestRef{Repo: repo, Number: status.PullRequest.Number}.String()
	if n := linksTo(t, ctx, store, task.ID, ref); n != 1 {
		t.Fatalf("links to %s = %d, want 1", ref, n)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st == model.StateCompleted {
		t.Fatal("the task was completed by opening its pull request -- " +
			"the run is still live, and a completed task is a merge queue member")
	}
}

// The finish path adopts the pull request the run already opened rather
// than opening a second one for the same head (which GitHub refuses) or
// linking it twice -- and it is the finish, not the early open, that ends
// the task.
func TestOpenPullRequestForTaskIsAdoptedByTheFinishPath(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	first, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}
	// Called twice, the way an agent polling its own checks would.
	second, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask again: %v", err)
	}
	if second.PullRequest.Number != first.PullRequest.Number {
		t.Fatalf("second call opened %d, want the same pull request as the first (%d)",
			second.PullRequest.Number, first.PullRequest.Number)
	}

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected exactly one pull request across all three calls, got %+v", sim.PullRequests)
	}
	ref := model.PullRequestRef{Repo: repo, Number: first.PullRequest.Number}.String()
	if n := linksTo(t, ctx, store, task.ID, ref); n != 1 {
		t.Fatalf("links to %s = %d, want 1", ref, n)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit once the run itself finished", st)
	}
}

// openedMidRun is the half of a run these tests cannot script through a
// framework stub: the open_pull_request tool call itself, which pkg/mcp
// serves by calling exactly this while the run is still live.
func openedMidRun(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, task model.Task) github.PullRequest {

	t.Helper()
	status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}
	return status.PullRequest
}

// prByHead is the sim's record of one branch's pull request -- what
// actually happened to it on "GitHub", as opposed to what the store
// thinks.
func prByHead(t *testing.T, sim *githubsim.Sim, head string) githubsim.PullRequest {
	t.Helper()
	for _, pr := range sim.PullRequests {
		if pr.Head == head {
			return pr
		}
	}
	t.Fatalf("no pull request for head %s in %+v", head, sim.PullRequests)
	return githubsim.PullRequest{}
}

// A task closed while its run was still going, after that run had already
// opened its own pull request, is salvagePushedBranch's close re-check
// meeting a pull request that already exists. Pinning what that leaves
// behind, because the answer is not the one that func's comment used to
// give ("the branch is left pushed but unopened"): the pull request is
// open, it stays open and unmerged, and grain never looks at it again.
//
// That is the same answer tests/e2e's TestClosingATaskWithAnOpenPull
// RequestDropsItFromTheMergeQueueForGood already pins for a task closed
// one moment later, once the finish path had opened the pull request --
// open_pull_request moved when that moment can arrive, not what a close
// then means. See salvagePushedBranch's own doc comment.
func TestClosingATaskLeavesThePullRequestItsRunAlreadyOpenedAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	pr := openedMidRun(t, ctx, store, client, task)

	// The close lands while the run is still live -- after the tool call,
	// before the finish. This is the whole race.
	closedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("closing task: %v", err)
	}

	result := toolResult(agent.ToolCall{Name: "open_pull_request"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// Nothing was opened a second time, and nothing was linked twice: the
	// finish path found the close and stopped before EnsurePullRequest.
	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the one the run itself opened and no other", sim.PullRequests)
	}
	ref := model.PullRequestRef{Repo: repo, Number: pr.Number}.String()
	if n := linksTo(t, ctx, store, task.ID, ref); n != 1 {
		t.Fatalf("links to %s = %d, want 1", ref, n)
	}

	// The decision, stated: the pull request is left open. grain closes
	// one only where a human asked for that at the moment they closed
	// the task (ui.CloseOptions.ClosePullRequest) -- never here, on a
	// path that has no human in front of it and is only discovering a
	// close that already happened. The work on the branch is real
	// whatever the human decided about the task, so it is left where
	// they can see it, merge it by hand, or close it by hand.
	if got := prByHead(t, sim, branch); got.State != "open" || got.Merged {
		t.Fatalf("pull request = %+v, want it left open and unmerged", got)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: the close outranks the run that was still finishing", st)
	}

	// The other half of the decision, and the reason it is safe: a closed
	// task is not 'completed', so its link never reaches SyncPullRequests
	// at all. Grain will not merge this pull request on any later cycle.
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("OpenPullRequestLinks = %+v, want none: a closed task's pull request is never synced again", links)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if got := prByHead(t, sim, branch); got.State != "open" || got.Merged {
		t.Fatalf("pull request after a sync = %+v, want it still open and unmerged", got)
	}
}

// The other two salvagePushedBranch callers, on the same "the run already
// opened its own pull request" setup that
// TestOpenPullRequestForTaskIsAdoptedByTheFinishPath covers for the clean
// finish. First: a run whose framework returned an error (ran out of
// turns, crashed) -- cycle.go's own salvage call, which reaches
// finishWithPullRequest with a pull request already open for the head.
func TestRunCycleAdoptsThePullRequestAFailedRunAlreadyOpened(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	// Pushed, opened its own pull request to watch CI, then spent the
	// rest of its turn budget and was cut off -- agent.Framework's own
	// contract, which hands back what the run managed to do first. The
	// tool call is served inline rather than through openedMidRun,
	// because this closure runs on reconcileDispatch's own goroutine and
	// a t.Fatal from there would not stop this test.
	var opened github.PullRequest
	var openErr error
	ranOutOfTurns := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
		opened, openErr = status.PullRequest, err
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "pushed"},
			{Name: "open_pull_request", Text: "opened"},
		}}, errors.New("gemini: exceeded max turns (2) without a final answer")
	})}

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(ranOutOfTurns),
		MaxWorkers: 1,
	}
	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if openErr != nil {
		t.Fatalf("the run's own open_pull_request call failed: %v", openErr)
	}
	if err == nil {
		t.Fatal("expected RunCycle to report the framework's failure")
	}
	if !strings.Contains(err.Error(), "exceeded max turns") {
		t.Errorf("error = %v, want the framework's own diagnosis preserved", err)
	}

	assertAdoptedNotReopened(t, ctx, store, sim, task, repo, opened, branch)

	// The task ends the way a failed run that nonetheless pushed ends:
	// out of the queue with an open pull request, because the branch is
	// real and now has one (cycle.go's own "only the ending failed"),
	// while the run itself keeps its failure and its reason.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit: the branch was salvaged even though the run failed", st)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if runs[0].Outcome != "failed" {
		t.Errorf("outcome = %q, want failed: adopting the pull request does not make the run a success", runs[0].Outcome)
	}
}

// Second: a run whose process died between the tool call and the finish,
// recovered at the next daemon startup. recoverRun reaches the same
// salvage with the same pull request already open -- the crash cost the
// result, not the pull request.
func TestRecoverOrphanedRunsAdoptsThePullRequestTheRunAlreadyOpened(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	d := dispatch.Dispatch{TaskID: task.ID, RunID: "t1-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	opened := openedMidRun(t, ctx, store, client, task)

	if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RecoverOrphanedRuns: %v", err)
	}

	assertAdoptedNotReopened(t, ctx, store, sim, task, repo, opened, branch)

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit: the branch and its pull request outlived the process", st)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.InterruptedOutcome {
		t.Fatalf("runs = %+v, want the one run finished as interrupted", runs)
	}
}

// assertAdoptedNotReopened is what "the finish path adopted it" means on
// every salvage path: one pull request on GitHub, and one link in the
// store, both the ones the run itself made.
func assertAdoptedNotReopened(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	task model.Task, repo model.RepoRef, opened github.PullRequest, branch string) {

	t.Helper()
	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want only the one the run opened for itself", sim.PullRequests)
	}
	if got := prByHead(t, sim, branch); got.Number != opened.Number {
		t.Fatalf("pull request = %+v, want the one the run opened (#%d)", got, opened.Number)
	}
	ref := model.PullRequestRef{Repo: repo, Number: opened.Number}.String()
	if n := linksTo(t, ctx, store, task.ID, ref); n != 1 {
		t.Fatalf("links to %s = %d, want exactly 1: the finish path must not link what the run already linked", ref, n)
	}
}

// The safety property OpenPullRequestForTask's doc comment claims, stated
// against the thing it is protecting against rather than against the
// mechanism: a pull request opened by a run that is still going is not a
// merge queue member, so nothing merges a branch the agent may still be
// pushing to. TestOpenPullRequestForTaskOpensOneWithoutEndingTheTask
// asserts the task is not 'completed', which is *how* that holds; this
// drives a real SyncPullRequests over it with everything else about it
// begging to be merged -- auto-merge on, GitHub reporting it clean, and
// the task sitting at the very front of the backlog, where a queue member
// would be its repo's head.
func TestSyncPullRequestsDoesNotMergeAPullRequestWhoseRunIsStillGoing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	running := filedTask(t, ctx, store, "t1", repo)
	running.AutoMerge = true
	running.CreatedAt = &baseTime
	running.OrderKey = 100
	if err := store.PutTask(ctx, running); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(running.ID)
	pushBranch(t, sim.BareRepo, branch)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: running.ID, RunID: "t1-1", Attempt: 1}, baseTime)
	opened := openedMidRun(t, ctx, store, client, running)

	// A control in the same repo, behind it in the backlog, whose run is
	// over: it merges on this very cycle. Without it, "nothing merged"
	// would be just as consistent with a setup that could never merge
	// anything.
	finished, finishedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	placeInBacklog(t, ctx, store, finished, 200)
	setMergeable(sim, true)

	if st, err := store.State(ctx, running.ID); err != nil || st != model.StateRunning {
		t.Fatalf("state before the sync = %q (%v), want running", st, err)
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if got := prByHead(t, sim, branch); got.Merged || got.State != "open" {
		t.Fatalf("pull request #%d = %+v, want it untouched: its run is still live and may still be pushing",
			opened.Number, got)
	}
	if got := prByHead(t, sim, finishedBranch); !got.Merged {
		t.Fatalf("the control pull request = %+v, want merged -- otherwise this cycle merged nothing at all "+
			"and proves nothing about the running task", got)
	}
	if st, err := store.State(ctx, running.ID); err != nil || st != model.StateRunning {
		t.Fatalf("state after the sync = %q (%v), want still running", st, err)
	}
}

// An agent that asks before it pushes is told to push, rather than
// getting GitHub's own 422 about a head that does not exist -- and
// nothing is opened.
func TestOpenPullRequestForTaskRefusesAnUnpushedBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err == nil {
		t.Fatal("expected an error for a branch that was never pushed")
	}
	if !strings.Contains(err.Error(), model.BranchName(task.ID)) {
		t.Errorf("error = %q, want it to name the branch that is missing", err)
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected no pull request, got %+v", sim.PullRequests)
	}
}

// The checks are the whole reason a run opens its pull request early:
// what comes back has to say what CI is doing right now, including a
// check that has not finished.
func TestOpenPullRequestForTaskReportsChecks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "tests", Status: "in_progress"},
	}

	status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}
	if status.ChecksError != "" {
		t.Fatalf("ChecksError = %q, want none", status.ChecksError)
	}
	if !status.ChecksKnown {
		t.Fatal("ChecksKnown = false, want true -- this client can read check runs")
	}
	if len(status.Checks) != 2 {
		t.Fatalf("Checks = %+v, want the two seeded check runs", status.Checks)
	}
	if status.Checks[0].Name != "lint" || status.Checks[0].Conclusion == nil ||
		*status.Checks[0].Conclusion != "failure" {
		t.Errorf("Checks[0] = %+v, want lint/failure", status.Checks[0])
	}
	if status.Checks[1].Status != "in_progress" {
		t.Errorf("Checks[1] = %+v, want the still-running check reported as such", status.Checks[1])
	}
}

// A human closing the task while its run is still going is answered the
// same way the finish path answers it: the branch stays pushed and no
// pull request is opened for work nobody asked to keep.
func TestOpenPullRequestForTaskRefusesAClosedTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	closedAt := baseTime
	if err := store.ObserveField(ctx, task.ID, closedAt, func(o *model.Observation) {
		o.ClosedAt = &closedAt
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task); err == nil {
		t.Fatal("expected an error for a task closed while its run was live")
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected no pull request, got %+v", sim.PullRequests)
	}
}

// A task with no repo has no branch, so there is nothing to open -- and
// saying so beats a nil dereference on task.Target.
func TestOpenPullRequestForTaskRefusesATaskWithNoRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "think about it", Body: "no repo",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if _, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task); err == nil {
		t.Fatal("expected an error for a task with no repo attached")
	}
}

// When main has moved ahead with non-conflicting commits, OpenPullRequestForTask
// automatically merges/fast-forwards main into the task branch before opening the PR.
func TestOpenPullRequestForTaskRebasesCleanlyOnBaseBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)

	pushBranch(t, sim.BareRepo, branch)
	pushFileToBranch(t, sim.BareRepo, branch, "FEATURE.md", "feature work", "feat: branch feature")

	// Main moves ahead with unrelated work
	pushFileToBranch(t, sim.BareRepo, "main", "OTHER.md", "unrelated work", "feat: main advance")

	status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask failed: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want 1", sim.PullRequests)
	}
	if status.PullRequest.Number != sim.PullRequests[0].Number {
		t.Fatalf("status = %+v, want PR #%d", status, sim.PullRequests[0].Number)
	}

	// Verify the branch head contains the commit from main (was fast-forwarded / merged)
	commits, err := client.CompareCommits("acme", "widgets", "main", branch)
	if err != nil {
		t.Fatalf("CompareCommits: %v", err)
	}
	if len(commits) == 0 {
		t.Fatalf("commits = %v, want branch commits", commits)
	}
}

// When main has moved ahead with conflicting changes, OpenPullRequestForTask
// fails with a descriptive error naming the conflicting file(s) and prompting
// the agent to resolve the conflict locally.
func TestOpenPullRequestForTaskFailsWithDescriptiveConflictMessage(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)

	pushBranch(t, sim.BareRepo, branch)
	pushFileToBranch(t, sim.BareRepo, branch, "SHARED.md", "branch content", "branch change")

	// Main modifies the same file to create a conflict
	pushFileToBranch(t, sim.BareRepo, "main", "SHARED.md", "main conflicting content", "main change")

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err == nil {
		t.Fatal("expected OpenPullRequestForTask to fail due to merge conflict")
	}

	for _, want := range []string{
		`branch "grain/task-t1" conflicts with "main"`,
		"in file(s): SHARED.md",
		"resolve the merge conflict locally and push before opening a pull request",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}

	if len(sim.PullRequests) != 0 {
		t.Fatalf("expected no pull requests opened on conflict, got %+v", sim.PullRequests)
	}
}

// EnsurePullRequest in the finish workflow fails when the branch conflicts with base branch.
func TestEnsurePullRequestInFinishWorkflowFailsWithConflictMessage(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)

	pushBranch(t, sim.BareRepo, branch)
	pushFileToBranch(t, sim.BareRepo, branch, "FILE.txt", "branch text", "branch change")
	pushFileToBranch(t, sim.BareRepo, "main", "FILE.txt", "main text", "main change")

	_, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err == nil {
		t.Fatal("expected EnsurePullRequest to fail due to merge conflict")
	}
	if !strings.Contains(err.Error(), `branch "grain/task-t1" conflicts with "main"`) ||
		!strings.Contains(err.Error(), "FILE.txt") {
		t.Errorf("error = %q, want conflict description naming FILE.txt", err.Error())
	}
}

type fakeGitOnlyTokens struct {
	wantOwner string
	reason    string
}

func (f fakeGitOnlyTokens) TokenFor(owner, repo string) *string { return nil }
func (f fakeGitOnlyTokens) GitOnly(owner, repo string) string {
	if f.wantOwner != "" && owner != f.wantOwner {
		return ""
	}
	return f.reason
}

func TestOpenPullRequestForTaskRefusesPlainGitHost(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, _ := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Host: "git.example.com", Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	fakeClient := &github.RESTClient{
		Transport: sim,
		Tokens: fakeGitOnlyTokens{
			wantOwner: "git.example.com/acme",
			reason:    "repository git.example.com/acme/widgets is on a plain git host",
		},
	}

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, fakeClient, task)
	if err == nil || !strings.Contains(err.Error(), "plain git host") {
		t.Fatalf("OpenPullRequestForTask error = %v, want plain git host refusal", err)
	}
}

func TestSalvagePushedBranchCompletesTaskOnPlainGitHostWithoutPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, _ := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Host: "git.example.com", Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)

	fakeClient := &github.RESTClient{
		Transport: sim,
		Tokens: fakeGitOnlyTokens{
			wantOwner: "git.example.com/acme",
			reason:    "repository git.example.com/acme/widgets is on a plain git host",
		},
	}

	now := time.Now().UTC()
	handled, err := orchestrator.SalvagePushedBranch(ctx, store, fakeClient, task, now)
	if err != nil {
		t.Fatalf("SalvagePushedBranch: %v", err)
	}
	if !handled {
		t.Fatalf("SalvagePushedBranch handled = false, want true")
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.CompletedAt == nil {
		t.Errorf("expected task to be marked completed")
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	wantComment := "Branch grain/task-t1 pushed to git.example.com/acme/widgets; no pull request opened because git.example.com is a plain git host."
	if len(comments) == 0 || comments[0].Body != wantComment {
		t.Fatalf("task comments = %+v, want %q", comments, wantComment)
	}
}

func TestRunCycleDoesNotSalvageAnUntouchedBranchWhenARepairRunFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	_ = openedMidRun(t, ctx, store, client, task)

	repairedAt := baseTime.Add(-time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:             task.ID,
		MergeQueueRepairAt: &repairedAt,
		MergeQueueRepairs:  1,
	}); err != nil {
		t.Fatalf("recording repair: %v", err)
	}

	pause := &orchestrator.Pause{}
	pausedBeforePush := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
				{Name: "run_command", Text: "git status"},
			}}, &agent.UsageLimitError{
				Framework: model.AgentFrameworkClaude,
				Message:   "hit your limit",
			}
	})}

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pausedBeforePush),
		MaxWorkers: 1,
		Config:     orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }},
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("expected RunCycle to report the usage limit refusal")
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if !obs.RepairInFlight() || obs.CompletedAt != nil {
		t.Fatalf("RepairInFlight = %v, CompletedAt = %v; want repair to remain in flight when the paused run pushed no new commit",
			obs.RepairInFlight(), obs.CompletedAt)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued so the repair resumes once the pause clears", st)
	}

	// Lift the pause and run a second repair attempt that fails with max turns without pushing.
	pause.Lift()
	failedBeforePush := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "go test ./..."},
		}}, errors.New("gemini: exceeded max turns (20) without a final answer")
	})}
	deps.Framework = orchestrator.StaticFramework(failedBeforePush)
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err == nil {
		t.Fatal("expected RunCycle to report the max-turns failure")
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation after failed repair attempt: %v", err)
	}
	if !obs.RepairInFlight() || obs.CompletedAt != nil {
		t.Fatalf("RepairInFlight = %v, CompletedAt = %v; want repair to remain in flight when a failed repair attempt pushed no new commit",
			obs.RepairInFlight(), obs.CompletedAt)
	}
}

func TestRunCycleSalvagesANewlyPushedBranchWhenFirstAttemptFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	branch := model.BranchName(task.ID)

	pushedThenFailed := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		pushBranch(t, sim.BareRepo, branch)
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "git push origin " + branch},
		}}, errors.New("gemini: exceeded max turns (20) without a final answer")
	})}

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pushedThenFailed),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("expected RunCycle to report the max-turns failure")
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.CompletedAt == nil {
		t.Fatal("CompletedAt = nil; want first attempt's newly pushed branch salvaged on failure")
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("PullRequests = %+v, want 1 salvaged PR", sim.PullRequests)
	}
}

func TestRunCycleSalvagesARepairBranchWhenTheFailedRunPushedANewCommit(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	_ = openedMidRun(t, ctx, store, client, task)

	repairedAt := baseTime.Add(-time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:             task.ID,
		MergeQueueRepairAt: &repairedAt,
		MergeQueueRepairs:  1,
	}); err != nil {
		t.Fatalf("recording repair: %v", err)
	}

	cloneDir := t.TempDir()
	pushedNewCommit := openingFramework{agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		wd := filepath.Join(cloneDir, "work")
		for _, args := range [][]string{
			{"clone", "-q", "-b", branch, sim.BareRepo, wd},
			{"-C", wd, "config", "user.email", "agent@example.com"},
			{"-C", wd, "config", "user.name", "agent"},
			{"-C", wd, "commit", "-q", "--allow-empty", "-m", "repair commit"},
			{"-C", wd, "push", "-q", "origin", branch},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("git %v: %v: %s", args, err, out)
			}
		}
		return &agent.Result{ToolCalls: []agent.ToolCall{
			{Name: "run_command", Text: "pushed repair"},
		}}, errors.New("gemini: exceeded max turns (20) without a final answer")
	})}

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(pushedNewCommit),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("expected RunCycle to report the max-turns failure")
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs.RepairInFlight() || obs.CompletedAt == nil {
		t.Fatalf("RepairInFlight = %v, CompletedAt = %v; want repair completed when the run pushed a new commit before failing",
			obs.RepairInFlight(), obs.CompletedAt)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateCompleted {
		t.Fatalf("state = %q, want completed so the merge queue re-evaluates the repaired branch", st)
	}
}
