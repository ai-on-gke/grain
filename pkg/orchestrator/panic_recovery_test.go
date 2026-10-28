// bwsalmon/agents#550: cmd/grain's own daemon.go now keeps the UI/API
// server up even when the rest of the daemon fails, but that only helps
// if RunCycle itself never takes the whole process down first. Before
// recoverReconcile/recoverRunOne (cycle.go) existed, a panic anywhere in
// a Reconciler, or in any one dispatch's own goroutine, was completely
// unrecovered: it would have crashed this test binary the same way it
// would have crashed a real grain daemon (UI/API server included, since
// bwsalmon/agents#363 folded them into one process) rather than merely
// failing the one thing that panicked. The two tests below each panic
// somewhere RunCycle reaches and prove the process survives to report a
// normal error instead -- and, following isolation_test.go's own pattern,
// that unrelated work in the same cycle still lands.
package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// panickingClient wraps a real github.Client and panics on GetPullRequest
// for one specific PR number, the same targeted-injection shape
// failingClient (isolation_test.go) uses for an ordinary error.
type panickingClient struct {
	github.Client
	panicOn int
}

func (c panickingClient) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	if c.panicOn != 0 && number == c.panicOn {
		panic("simulated panic reading a pull request")
	}
	return c.Client.GetPullRequest(owner, repo, number)
}

// panickingFramework is agent.Framework's one method panicking instead of
// returning -- standing in for a real agent/tool bug crashing mid-run,
// which nothing between it and RunCycle's own caller (cmd/grain's
// reconcile loop) had ever recovered from before recoverRunOne.
type panickingFramework struct{}

func (panickingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	panic("simulated panic running an agent")
}

// A Reconciler panicking (here, "sync" reading a pull request) must not
// take the rest of the cycle -- "dispatch," in particular -- down with
// it, the same "one reconciler's problem doesn't cost the others"
// contract isolation_test.go's own TestRunCycleSyncsPullRequestsEvenWhenDispatchFails
// already holds errors to. The headline assertion is simply that this
// test function returns at all: an unrecovered panic here would have
// crashed the whole `go test` binary instead of leaving a *testing.T
// failure to report.
func TestRunCycleRecoversFromAPanickingReconciler(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	watched := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, false)
	queued := filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     panickingClient{Client: client, panicOn: pullRequestNumber(t, watched)},
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil {
		t.Fatal("RunCycle returned nil, want the recovered panic reported as an error")
	}
	if !strings.Contains(err.Error(), "sync:") || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("RunCycle error = %q, want it to name the sync reconciler and say panic", err.Error())
	}
	// The scripted run answers in a comment and pushes nothing, so its
	// completion reads 'awaiting_ack' (model.AwaitsAck) -- that the
	// dispatch ran at all is the whole of what this asserts.
	if st := stateOf(t, ctx, store, queued.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply: dispatch should have run despite sync panicking", st)
	}
}

// A single dispatch's own goroutine panicking (a bug in the agent
// framework, mid-run) must not take the rest of the cycle down with it
// either -- and, since it panics on a goroutine of its own, RunCycle's
// per-Reconciler recover (recoverReconcile) could never have caught it in
// the first place; only recoverRunOne, deferred inside that same
// goroutine, can. Mirrors TestRunCycleDispatchesEvenWhenPullRequestSyncFails's
// own "and the other way round" shape: this time dispatch is the one that
// blows up, and sync's own unrelated work must still land.
func TestRunCycleRecoversFromAPanickingDispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	done := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, true)
	filedTask(t, ctx, store, "t2", repo)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(panickingFramework{}),
		MaxWorkers: 1,
	}

	err := orchestrator.RunCycle(ctx, deps, baseTime)
	if err == nil {
		t.Fatal("RunCycle returned nil, want the recovered panic reported as an error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("RunCycle error = %q, want it to say panic", err.Error())
	}
	if st := stateOf(t, ctx, store, done.ID); st != model.StateClosed {
		t.Fatalf("state = %q, want closed: sync should have run despite dispatch panicking", st)
	}
}
