package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// grain/task-1660's merge-rule options at the points the queue acts on
// them: repairs when a broken change would be sent to an agent,
// flake_recheck when a red run would be rechecked, and port when a merge
// would be ported.

// classifyAs records on taskID the policy its repository's stored rules
// give a change touching files at branch's head, the way syncEntry does
// before anything else reads it.
func classifyAs(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	repo model.RepoRef, taskID, branch string, files ...string) model.EffectiveMergePolicy {

	t.Helper()
	rules, err := store.MergeRulesOf(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	p := rules.Effective(files, true, model.CIModeGrain)
	p.Head = headSHA(t, client, repo, branch)
	if err := store.UpdateTask(ctx, taskID, func(task *model.Task) error {
		task.Links = append(task.Links, model.MergePolicyLink(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

// `repairs: 0`: a change that fails goes straight to a person, and the
// block says which rule sent it there.
func TestRepairsZeroEscalatesImmediately(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	zero := 0
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "release", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{Repairs: &zero}}}})
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/main.tf", "resource {}\n")
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, flakyJob())
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	greenOnGitHub(sim, branch)
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a `repairs: 0` change was sent for repair")
	}
	obs := repairObservationOf(t, ctx, store, task.ID)
	if obs.MergeQueueBlockedAt == nil || obs.RepairsSpent() != 0 {
		t.Fatalf("blocked at %v with %d repair(s) spent, want blocked with none", obs.MergeQueueBlockedAt,
			obs.RepairsSpent())
	}
	bodies := strings.Join(commentBodies(t, ctx, store, task.ID), "\n")
	if !strings.Contains(bodies, "`repairs: 0` for the paths this pull request touches (from rule `release`)") {
		t.Fatalf("the block does not name the rule:\n%s", bodies)
	}
}

// `repairs: 1` caps the PR path at one: what would have been the second
// is not given, and the block says the rules are why.
func TestRepairsOneSaysOneOfOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	one := 1
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "orchestrator", Paths: []string{"pkg/orchestrator/**"},
			MergePolicy: model.MergePolicy{Repairs: &one}}}})
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "pkg/orchestrator/sync.go", "package orchestrator\n")
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, flakyJob())
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	greenOnGitHub(sim, branch)
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "automatic repair 1 of 1") {
		t.Fatalf("the repair does not say it is the only one:\n%s", last)
	}
}

// `flake_recheck: false`: a red run is the change's failure the first
// time, with no uncached recheck.
func TestFlakeRecheckFalseSkipsTheRecheck(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	no := false
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "strict", Paths: []string{"pkg/**"},
			MergePolicy: model.MergePolicy{FlakeRecheck: &no}}}})
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	classifyAs(t, ctx, store, client, repo, task.ID, branch, "pkg/x.go")
	classified, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeCI()
	runner.answerRechecks(recheckPasses) // a recheck would have called it a flake
	first := startRequiredCI(t, ctx, store, client, runner, *classified, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, flakyJob())
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if n := runner.rechecks(); n != 0 {
		t.Fatalf("%d recheck(s) asked for under `flake_recheck: false`, want none", n)
	}
	if status := ciStatusOf(t, ctx, store, task.ID); !status.Done() || status.Green() {
		t.Fatalf("status = %+v, want the first failure as the verdict", status)
	}
}

// `port: false`: a merge touching those paths files no port, while one
// touching only `port: true` paths does.
func TestPortFalseFilesNoPortTask(t *testing.T) {
	for _, c := range []struct {
		file  string
		ports int
	}{
		{"tools/release.sh", 0},
		{"docs/readme.md", 1},
	} {
		t.Run(c.file, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
			yes, no := true, false
			declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
				Rules: []model.MergeRule{
					{ID: "docs", Paths: []string{"docs/**"}, MergePolicy: model.MergePolicy{Port: &yes}},
					{ID: "tools", Paths: []string{"tools/**"}, MergePolicy: model.MergePolicy{Port: &no}},
				}})
			task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
			pushFile(t, sim.BareRepo, branch, c.file, "x\n")

			mergeQueued(t, ctx, store, sim, client, task, branch)

			if ports := portsOf(t, ctx, store, task.ID); len(ports) != c.ports {
				t.Fatalf("%d port(s) filed, want %d", len(ports), c.ports)
			}
		})
	}
}
