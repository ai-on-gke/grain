// Failure isolation between a cycle's reconcilers, and between the items
// each one works through.
//
// The property every test here pins down is the same one: work that
// could have been done was done, even though something else in the same
// cycle failed. Before Reconcilers() existed, RunCycle was a pipeline --
// the first error returned, and everything after it simply did not
// happen -- so one failure stalled a merge that was ready and a queue
// that needed advancing. Each test below fails one specific thing and
// asserts that the unrelated work still landed.
package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

var errInjected = errors.New("injected failure")

// failingClient wraps a real github.Client and fails exactly the calls a
// test names, delegating everything else. Embedding the interface rather
// than implementing all 20 methods keeps each test's injection to the one
// method it cares about -- and means a method added to github.Client
// later does not need touching here.
type failingClient struct {
	github.Client
	getPRFor int // GetPullRequest fails for this PR number
}

func (c failingClient) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	if c.getPRFor != 0 && number == c.getPRFor {
		return github.PullRequestDetail{}, errInjected
	}
	return c.Client.GetPullRequest(owner, repo, number)
}

// failingSandboxes refuses to build one named sandbox, so exactly one of
// a cycle's dispatches cannot run while the others can.
type failingSandboxes struct {
	inner   orchestrator.Sandboxes
	sandbox string
}

func (s failingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	if name == s.sandbox {
		return nil, errInjected
	}
	return s.inner.Acquire(ctx, name, shape)
}

// stubFramework is agent.Framework's one method returning a fixed result,
// with no model, MCP server or sandbox behind it -- enough for a test
// about which dispatches ran, which is all the ones here ask.
type stubFramework struct{ result *agent.Result }

func (f stubFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f.result, nil
}

// completesWithAComment is a run that ends by commenting rather than
// pushing: ProcessResult observes CompletedAt for it (see finish.go)
// without any branch having to exist, so a test can tell a dispatch that
// ran from one that did not.
func completesWithAComment() func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	result := toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "done"},
	})
	return orchestrator.StaticFramework(stubFramework{result: result})
}

// mergedPullRequestTask files a task whose run has finished and whose
// pull request GitHub has since merged out of band -- the exact state
// SyncPullRequests exists to notice, so a test can assert on whether it
// got the chance to.
func mergedPullRequestTask(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client github.Client, id string, repo model.RepoRef, merge bool) model.Task {

	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatalf("opening a pull request for %s: %v", id, err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	if merge {
		if err := client.MergePullRequest(repo.Owner, repo.Name, pr.Number, ""); err != nil {
			t.Fatal(err)
		}
	}
	return task
}

func stateOf(t *testing.T, ctx context.Context, store *model.Store, id string) model.State {
	t.Helper()
	st, err := store.State(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A cycle whose intake cannot reach GitHub still closes out the pull
// requests it already knows about. This is the headline regression: poll
// runs first, and used to take the whole cycle down with it.
// A cycle whose dispatch cannot build a sandbox still closes out the
// pull requests it already knows about. Dispatch runs first, and used to
// take the whole cycle down with it.
func TestRunCycleSyncsPullRequestsEvenWhenDispatchFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// One task with a merged PR waiting to be closed out, and one queued
	// task whose dispatch will fail.
	done := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, true)
	filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:  store,
		Client: client,
		Sandboxes: failingSandboxes{
			inner:   orchestrator.NewHostSandboxes(t.TempDir()),
			sandbox: "t2-1",
		},
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if !errors.Is(err, errInjected) {
		t.Fatalf("RunCycle error = %v, want it to carry the injected dispatch failure", err)
	}
	if st := stateOf(t, ctx, store, done.ID); st != model.StateClosed {
		t.Fatalf("state = %q, want closed: sync should have run despite dispatch failing", st)
	}
}

// And the other way round: a cycle whose pull-request sync cannot reach
// GitHub still dispatches the work that was ready.
func TestRunCycleDispatchesEvenWhenPullRequestSyncFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	watched := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, false)
	queued := filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     failingClient{Client: client, getPRFor: pullRequestNumber(t, watched)},
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if !errors.Is(err, errInjected) {
		t.Fatalf("RunCycle error = %v, want it to carry the injected sync failure", err)
	}
	// The scripted run answers in a comment and pushes nothing, so its
	// completion reads 'awaiting_ack' (model.AwaitsAck) -- that the
	// dispatch ran at all is the whole of what this asserts.
	if st := stateOf(t, ctx, store, queued.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply: dispatch should have run despite sync failing", st)
	}
}

// pullRequestNumber reads the PR a task's own LinkFixes link names.
func pullRequestNumber(t *testing.T, task model.Task) int {
	t.Helper()
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			ref, err := model.ParsePullRequestRef(l.Target)
			if err != nil {
				t.Fatal(err)
			}
			return ref.Number
		}
	}
	t.Fatal("task has no pull request link")
	return 0
}

// Every reconciler's failure reaches the caller, not just the first --
// graind logs one line per cycle, so a failure hidden behind another is a
// failure nobody sees.
func TestRunCycleReportsEveryFailingReconcilerNotJustTheFirst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	watched := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, false)
	filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:  store,
		Client: failingClient{Client: client, getPRFor: pullRequestNumber(t, watched)},
		Sandboxes: failingSandboxes{
			inner:   orchestrator.NewHostSandboxes(t.TempDir()),
			sandbox: "t2-1",
		},
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil {
		t.Fatal("RunCycle returned nil, want both failures")
	}
	// Both reconcilers failed for the same injected reason, so the join is
	// what carries them; naming each one is what tells them apart.
	msg := err.Error()
	for _, want := range []string{"dispatch:", "sync:"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("RunCycle error = %q, want it to name the %q reconciler", msg, want)
		}
	}
}

// involved -- this is purely about the per-entry loop.
func TestSyncPullRequestsClosesOutEveryTaskWhenOneFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	first := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, true)
	second := mergedPullRequestTask(t, ctx, store, sim, client, "t2", repo, true)

	firstPR := pullRequestNumber(t, first)

	err := orchestrator.SyncPullRequests(ctx, store,
		failingClient{Client: client, getPRFor: firstPR}, baseTime)
	if !errors.Is(err, errInjected) {
		t.Fatalf("SyncPullRequests error = %v, want the injected read failure", err)
	}

	if st := stateOf(t, ctx, store, second.ID); st != model.StateClosed {
		t.Fatalf("second task state = %q, want closed: one unreadable PR should not strand the others", st)
	}
	if st := stateOf(t, ctx, store, first.ID); st != model.StateAwaitingSubmit {
		t.Fatalf("first task state = %q, want it left where it was for the next tick to retry", st)
	}
}

// A slot whose sandbox cannot be built does not cost the other slots
// their dispatch: dispatch.Cycle has already durably recorded a run for
// each, so skipping the rest would idle them for a tick over a failure
// that is not theirs.
func TestRunCycleRunsEveryDispatchWhenOneSandboxFails(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	first := filedTask(t, ctx, store, "t1", repo)
	second := filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:  store,
		Client: client,
		Sandboxes: failingSandboxes{
			inner:   orchestrator.NewHostSandboxes(t.TempDir()),
			sandbox: "t1-1",
		},
		Framework:  completesWithAComment(),
		MaxWorkers: 2,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if !errors.Is(err, errInjected) {
		t.Fatalf("RunCycle error = %v, want the injected sandbox failure", err)
	}

	// t1's sandbox is the one that cannot be built; t2's dispatch must
	// still have run rather than being abandoned along with it. The
	// scripted run answers in a comment and pushes nothing, so the
	// dispatch that got through reads 'awaiting_ack' (model.AwaitsAck).
	completed := 0
	for _, id := range []string{first.ID, second.ID} {
		if stateOf(t, ctx, store, id) == model.StateAwaitingReply {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("completed %d of 2 dispatches, want exactly 1: the dispatch whose sandbox built fine should still have run", completed)
	}
}
