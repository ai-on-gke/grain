package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/portpick"
)

// pushFileCommit commits file with content onto branch -- created from
// base when it does not exist yet -- and pushes it to bare.
func pushFileCommit(t *testing.T, bare, branch, base, file, content, message string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	if err := exec.Command("git", "-C", wd, "checkout", "-q", branch).Run(); err != nil {
		run(t, wd, "git", "checkout", "-q", "-b", branch, "origin/"+base)
	}
	if err := os.WriteFile(filepath.Join(wd, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", "-A")
	run(t, wd, "git", "commit", "-q", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// gitOut is git's trimmed output in dir.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// queuedTaskChanging is queuedTaskWithPullRequest with a change that is
// a change: a port is cherry-picked, and an empty commit picks as
// nothing.
func queuedTaskChanging(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, id string, repo model.RepoRef, file, content string) (model.Task, string) {

	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushFileCommit(t, sim.BareRepo, branch, "main", file, content, "Change "+file+"\n\nFor the test.")
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
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
	return task, branch
}

// mechanicalPorter is the real porter, reaching the sim's own bare
// repository.
func mechanicalPorter(t *testing.T, sim *githubsim.Sim) *portpick.Porter {
	return &portpick.Porter{
		Dir: t.TempDir(),
		Remote: func(context.Context, model.RepoRef, string) (portpick.Remote, error) {
			return portpick.Remote{URL: sim.BareRepo}, nil
		},
	}
}

// syncWithPorter is one queue cycle with porter wired, as a deployment
// wires it (Config.Porter).
func syncWithPorter(t *testing.T, ctx context.Context, store *model.Store, client *github.RESTClient, porter orchestrator.Porter) {
	t.Helper()
	gate := orchestrator.StateRepoGate{Porter: porter}
	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil, gate, model.DefaultMaxMergesPerRepo, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
}

func mergeWithPorter(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, porter orchestrator.Porter, task model.Task, branch string) {
	t.Helper()
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("success")}}
	syncWithPorter(t, ctx, store, client, porter)
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed: the pull request should have merged", st, err)
	}
}

// A port that cherry-picks cleanly is made by grain itself: the port task
// is filed already owning its pull request against the other branch,
// approved, completed and tagged, with no agent run. When its CI goes red
// the merge queue sends it to an agent like any other red change, and
// when it merges it is not ported back.
func TestACleanPortIsMadeWithoutAnAgent(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	task, branch := queuedTaskChanging(t, ctx, store, sim, client, "t1", repo, "feature.txt", "new\n")
	porter := mechanicalPorter(t, sim)

	mergeWithPorter(t, ctx, store, sim, client, porter, task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("filed %d ports, want 1", len(ports))
	}
	port := ports[0]
	if !slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Errorf("port tags = %v, want %s", port.Tags, model.TagMechanicalPort)
	}
	if port.Approval == nil || port.ApprovedAt == nil {
		t.Error("mechanical port was left unapproved")
	}
	if st, _ := store.State(ctx, port.ID); st != model.StateCompleted {
		t.Errorf("port state = %q, want completed: it owns its pull request with no agent run", st)
	}
	pr := model.TaskPullRequest(port)
	if pr == nil {
		t.Fatalf("port links = %+v, want its pull request", port.Links)
	}
	var opened *githubsim.PullRequest
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Number == pr.Number {
			opened = &sim.PullRequests[i]
		}
	}
	if opened == nil || opened.Base != "candidate" || opened.Head != port.BranchName() {
		t.Fatalf("port pull request = %+v, want %s onto candidate", opened, port.BranchName())
	}
	msg := gitOut(t, sim.BareRepo, "log", "-1", "--format=%B", "refs/heads/"+port.BranchName())
	if !strings.Contains(msg, "Backported-from: ") || !strings.Contains(msg, "(cherry picked from commit ") {
		t.Errorf("ported commit does not say it is a port:\n%s", msg)
	}
	runs, err := store.Runs(ctx, port.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("mechanical port has %d runs, want none", len(runs))
	}
	if bodies := commentBodies(t, ctx, store, task.ID); !slices.ContainsFunc(bodies, func(b string) bool {
		return strings.Contains(b, port.ID) && strings.Contains(b, "no agent run")
	}) {
		t.Errorf("source comments do not say %s was ported mechanically: %q", port.ID, bodies)
	}

	// Red grain CI -- the gate a port is held to, since it carries the
	// grain-ci grant -- takes the merge queue's own repair path, on the
	// same branch.
	setMergeable(sim, true)
	redGrainCI(t, ctx, store, client, port)
	syncWithPorter(t, ctx, store, client, porter)
	if !repairInFlightNow(t, ctx, store, port.ID) {
		t.Fatal("a red mechanical port was not sent to an agent for repair")
	}
	if !strings.Contains(port.Body, "do not start the port over") {
		t.Errorf("port body does not tell a repairing agent the port is already made:\n%s", port.Body)
	}

	// The repair lands, CI goes green, it merges -- and is not ported back.
	if err := store.ObserveField(ctx, port.ID, baseTime, func(o *model.Observation) { o.CompletedAt = &baseTime }); err != nil {
		t.Fatal(err)
	}
	greenGrainCI(t, ctx, store, client, newFakeCI(), port, repo, port.BranchName())
	mergeWithPorter(t, ctx, store, sim, client, porter, port, port.BranchName())
	if back := portsOf(t, ctx, store, port.ID); len(back) != 0 {
		t.Fatalf("the port's own merge filed %d ports back, want none", len(back))
	}
}

// A pick that conflicts is aborted and the agent port task is filed as
// before, told which commit conflicted and on which paths; nothing is
// pushed.
func TestAConflictingPortFallsBackToAnAgentWithTheConflict(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	pushFileCommit(t, sim.BareRepo, "candidate", "candidate", "shared.txt", "candidate's\n", "candidate's own")
	task, branch := queuedTaskChanging(t, ctx, store, sim, client, "t1", repo, "shared.txt", "main's\n")
	head := gitOut(t, sim.BareRepo, "rev-parse", "refs/heads/"+branch)

	mergeWithPorter(t, ctx, store, sim, client, mechanicalPorter(t, sim), task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("filed %d ports, want 1", len(ports))
	}
	port := ports[0]
	if slices.Contains(port.Tags, model.TagMechanicalPort) {
		t.Error("a conflicted port was tagged mechanical")
	}
	if st, _ := store.State(ctx, port.ID); st != model.StateQueued {
		t.Errorf("port state = %q, want queued for an agent", st)
	}
	for _, want := range []string{"conflict", head, "`shared.txt`", "cherry-pick -x"} {
		if !strings.Contains(port.Body, want) {
			t.Errorf("port body does not say %q:\n%s", want, port.Body)
		}
	}
	if out := gitOut(t, sim.BareRepo, "branch", "--list", port.BranchName()); out != "" {
		t.Errorf("a conflicted port pushed %s", out)
	}
}

// A change git finds already on the other branch -- the same patch under
// another commit -- files no port at all, and says so on the source.
func TestAnAlreadyPortedChangeFilesNothing(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	pushFileCommit(t, sim.BareRepo, "candidate", "candidate", "feature.txt", "new\n", "the same change, made on candidate")
	task, branch := queuedTaskChanging(t, ctx, store, sim, client, "t1", repo, "feature.txt", "new\n")

	mergeWithPorter(t, ctx, store, sim, client, mechanicalPorter(t, sim), task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
		t.Fatalf("filed %d ports of a change already on candidate, want none", len(ports))
	}
	if bodies := commentBodies(t, ctx, store, task.ID); !slices.ContainsFunc(bodies, func(b string) bool {
		return strings.Contains(b, "nothing to port")
	}) {
		t.Errorf("source comments do not say there was nothing to port: %q", bodies)
	}
}

// A porter that cannot run at all (no credential, git failing) leaves the
// port to an agent exactly as before.
func TestAPortThatCannotBePickedFallsBackToAnAgent(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	task, branch := queuedTaskChanging(t, ctx, store, sim, client, "t1", repo, "feature.txt", "new\n")
	broken := &portpick.Porter{
		Dir: t.TempDir(),
		Remote: func(context.Context, model.RepoRef, string) (portpick.Remote, error) {
			return portpick.Remote{}, os.ErrPermission
		},
	}

	mergeWithPorter(t, ctx, store, sim, client, broken, task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("filed %d ports, want 1", len(ports))
	}
	if st, _ := store.State(ctx, ports[0].ID); st != model.StateQueued {
		t.Errorf("port state = %q, want queued for an agent", st)
	}
	if !strings.Contains(ports[0].Body, "could not finish it") {
		t.Errorf("port body does not say the cherry-pick was tried:\n%s", ports[0].Body)
	}
}
