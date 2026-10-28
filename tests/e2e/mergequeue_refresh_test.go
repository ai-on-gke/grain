// The end-to-end half of docs/merge-queue-staleness.md: a queue head
// whose CI last ran against a base that has since moved is brought up to
// date by the queue itself, with no agent run and no fix task, and merges
// once CI has answered about the tree that would actually land. This is
// the test that would have covered every commit that document lists --
// each of them an agent dispatched into a sandbox to type `git merge
// origin/main`.
//
// Its companion is the case that write cannot fix: a branch that
// genuinely conflicts with its base, where the fix task is still filed --
// and now says the merge queue tried the merge and watched it fail, which
// is a different job from the one an agent was previously handed.
//
// Both run against githubsim over the same real bare repository the rest
// of this package uses, so "behind", "up to date" and "conflicts" are
// answered by real git: the whole reason the queue asks GitHub to make
// the merge is that its answer is authoritative.
package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestMergeQueueMergesMainIntoAStaleHeadAndLandsItWithNoFixTask(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "stale"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "task1",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName("t1")
	clock := baseTime
	deps.Framework = scriptedFramework(cleanPushScript(w.remote(owner, repoName), branch, "t1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateCompleted {
		t.Fatalf("t1 state = %q (%v), want completed", st, err)
	}

	// main moves on under the open pull request, and the checks that ran
	// against the older main are red. Nothing here conflicts: the file
	// landing on main is one the task never touched, which is the shape
	// of nearly every fix this deployment has ever filed.
	w.pushConflictingCommit(owner, repoName, "main", "MAIN.md", "unrelated work", "an unrelated change on main")
	setMergeable(sim, branch, true)
	failure := "failure"
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &failure}}

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (refreshes the stale head): %v", err)
	}

	// The queue made the merge itself: the branch really contains main
	// now, right down to the file that landed there.
	if !w.branchContains(owner, repoName, branch, "main") {
		t.Fatal("the head branch does not contain main: nothing was actually merged")
	}
	if got := w.fileAt(owner, repoName, branch, "MAIN.md"); got != "unrelated work" {
		t.Fatalf("MAIN.md on %s = %q, want main's own content carried in by the refresh", branch, got)
	}
	obs, err := w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueRepairAt != nil {
		t.Fatal("asked for a repair of a head that was only behind main")
	}
	// And no agent was dispatched for one either -- the whole saving.
	if tasks := w.workTasks(); len(tasks) != 1 {
		t.Fatalf("the store holds %d tasks, want only t1: the refresh should have cost no task at all", len(tasks))
	}
	obs, err = w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRefreshedAt == nil {
		t.Fatal("the refresh was not recorded, so the next cycle would merge all over again")
	}

	// CI re-runs on the tree that would actually merge, and passes. The
	// head merges on that verdict.
	success := "success"
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: &success}}
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (merges the refreshed head): %v", err)
	}
	if st, err := w.store.State(w.ctx, "t1"); err != nil || st != model.StateClosed {
		t.Fatalf("t1 state = %q (%v), want closed", st, err)
	}
	if !w.branchContains(owner, repoName, "main", branch) {
		t.Fatal("main does not contain the task's branch: the pull request never really merged")
	}
	if got := w.fileAt(owner, repoName, "main", "NOTES.md"); !strings.Contains(got, "t1") {
		t.Fatalf("NOTES.md on main = %q, want the task's own change landed", got)
	}
	if obs, err := w.store.GetObservation(w.ctx, "t1"); err != nil {
		t.Fatal(err)
	} else if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("t1 was escalated to a human: nothing here ever needed one")
	}
	// One comment, and it is the queue saying it made the merge commit --
	// which is the only place a human who did not want one can find out
	// who did.
	comments, err := w.store.Comments(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected exactly the one comment about the refresh, got %+v", comments)
	}
	if !strings.Contains(comments[0].Body, "merge queue merged `main`") {
		t.Fatalf("the comment does not say the queue merged main in:\n%s", comments[0].Body)
	}
}

// The variant the refresh cannot repair, and the one this design makes
// better rather than merely survivable: the queue tries the merge, GitHub
// refuses it over a real conflict, and the fix task is filed in that same
// cycle saying so -- so the agent starts from "resolve this" instead of
// from the merge the queue has just proved will not apply.
func TestMergeQueueFilesAFixNamingTheConflictWhenItsOwnMergeIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "conflicted"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1}

	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "add config",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human("alice")},
		Target:   &repo, Binding: model.BindingDirective, AutoMerge: true, CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName("t1")
	clock := baseTime
	deps.Framework = scriptedFramework(configPushScript(w.remote(owner, repoName), branch, "setting: from-task1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (push): %v", err)
	}

	// The same file, differently, straight onto main: a merge either way
	// really conflicts.
	w.pushConflictingCommit(owner, repoName, "main", "CONFIG.md", "setting: from-main", "a conflicting change on main")
	if !w.realConflict(owner, repoName, "main", branch) {
		t.Fatal("expected a genuine git conflict between main and the task's branch")
	}
	setMergeable(sim, branch, false)
	headBefore := w.log1(owner, repoName, branch, "%H")

	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (files the fix): %v", err)
	}

	if got := w.log1(owner, repoName, branch, "%H"); got != headBefore {
		t.Fatalf("the head branch moved to %q: a conflicting merge landed anyway", got)
	}
	obs, err := w.store.GetObservation(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !obs.RepairInFlight() {
		t.Fatalf("observation = %+v, want the head the queue could not merge sent back for repair", obs)
	}
	comments, err := w.store.Comments(w.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %+v", comments)
	}
	if !strings.Contains(comments[0].Body, "conflicted") || !strings.Contains(comments[0].Body, "real resolution") {
		t.Errorf("the repair comment does not describe the merge the queue watched fail:\n%s", comments[0].Body)
	}
	if !strings.Contains(comments[0].Body, branch) {
		t.Errorf("the repair comment does not name the branch to work on:\n%s", comments[0].Body)
	}
	if obs.MergeQueueRefreshedAt == nil {
		t.Fatal("the attempt was not recorded: the queue would re-try a conflict every cycle")
	}
}
