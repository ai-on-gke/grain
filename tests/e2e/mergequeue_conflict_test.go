// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

// TestSyncPullRequestsRepairsAndLandsAConflictedMergeQueueHead is
// bwsalmon/agents#328's own scenario: the one merge-queue flow
// pkg/orchestrator/mergequeue_test.go cannot prove by itself, because
// that file's own
// TestSyncPullRequestsSendsAConflictedQueueHeadBackForRepair stops at
// asking for the repair -- it never dispatches it, so it never has to
// resolve anything. Here the conflict is a genuine one: an AutoMerge
// task's branch and a commit pushed straight to main both add the same
// file with different content, so a real `git merge` between them really
// fails. SyncPullRequests notices that (through a Mergeable flag this test
// computes with a real merge attempt, not one it simply asserts), sends
// the task itself back to work, and a second, scripted run of that same
// task really merges main into the branch its pull request is already
// open from and pushes it there. The payoff this file adds is checking
// that the real content that lands on main afterward is the one the
// repair resolved to -- not just that some merge commit exists, and not
// on a second branch that had to be merged in first.
import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// worldSandboxes builds each run's sandbox out of this world's own
// already-credentialed directories (world.sandboxRoot mints that
// sandbox's proxy token and points its git config at this world's real
// gitproxy) rather than through orchestrator.HostSandboxes, which hands
// out uncredentialed ones -- the wrong tool here, since this test needs
// every push RunCycle makes, including the repair run's own, to actually
// go through the proxy.
type worldSandboxes struct{ w *world }

func (s worldSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	return worldSandbox{name: name, root: s.w.sandboxRoot(name)}, nil
}

// worldSandbox is one run's directory in this world. Release is a no-op:
// the directory is under the test's own TempDir, and keeping it lets an
// assertion read back what the run wrote.
type worldSandbox struct{ name, root string }

func (s worldSandbox) Name() string { return s.name }

func (s worldSandbox) Root() (string, error) { return s.root, nil }

func (s worldSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) {
	return mcp.NewSandboxTools(s.root), nil
}

// ConfigureGitCredentials is a no-op: world.sandboxRoot has already
// pointed this directory at the world's proxy with its own minted token.
func (s worldSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	return nil
}

func (s worldSandbox) Release(ctx context.Context) error { return nil }

// configPushScript is the scripted turn the original task's own agent
// takes: push a branch that adds CONFIG.md with content, the file this
// whole test's conflict is built around.
func configPushScript(remote, branch, content string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo '" + content + "' > CONFIG.md && " +
		"git add CONFIG.md && git commit -q -m 'agent commit for " + branch + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// resolveScript is the scripted turn the repair run takes: check out the
// task's own branch -- the one its pull request is already open from,
// which is the whole of what requeueForRepair changed -- and really merge
// main into it, favoring the branch's own side of the conflict
// SyncPullRequests detected, then push it back. This is a real git merge
// that has to resolve a real conflict, not a canned file write standing
// in for one, and it lands on the branch under review rather than on a
// second one somebody would then have to merge.
func resolveScript(remote, branch string) []antigravity.Step {
	cmd := "rm -rf work && git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " origin/" + branch + " && " +
		"git fetch origin main && " +
		"git merge origin/main -X ours -m 'resolve conflict with main' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("resolved the conflict on " + branch),
	}
}

// setMergeable sets the Mergeable field of the pull request whose head is
// head. It is always called just after realConflict has actually checked,
// never with a value the test simply asserts -- githubsim.Sim's own doc
// comment says it has no merge engine of its own, so this is a caller
// telling it what a real GitHub would have computed.
func setMergeable(sim *githubsim.Sim, head string, mergeable bool) {
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == head {
			sim.PullRequests[i].Mergeable = &mergeable
			return
		}
	}
}

// realConflict reports whether merging branch into base would genuinely
// conflict, by actually attempting that merge against a throwaway clone of
// owner/name's bare repo -- the same git machinery a real GitHub would run
// to compute its own Mergeable field, rather than a value this test simply
// declares.
func (w *world) realConflict(owner, name, base, branch string) bool {
	w.t.Helper()
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	dir := w.t.TempDir()
	run(w.t, dir, "git", "clone", bare, "check")
	wd := filepath.Join(dir, "check")
	run(w.t, wd, "git", "config", "user.email", "mergecheck@example.com")
	run(w.t, wd, "git", "config", "user.name", "merge check")
	run(w.t, wd, "git", "fetch", "-q", "origin", base, branch)
	run(w.t, wd, "git", "checkout", "-q", "-B", "check-base", "origin/"+base)
	cmd := exec.Command("git", "merge", "--no-commit", "--no-ff", "origin/"+branch)
	cmd.Dir = wd
	return cmd.Run() != nil
}

// pushConflictingCommit pushes a commit straight to branch, bypassing any
// pull request, standing in for the unrelated change the scenario asks
// for: something that lands on main out from under an already-open PR.
func (w *world) pushConflictingCommit(owner, name, branch, path, content, message string) {
	w.t.Helper()
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	dir := w.t.TempDir()
	run(w.t, dir, "git", "clone", bare, "conflict")
	wd := filepath.Join(dir, "conflict")
	run(w.t, wd, "git", "config", "user.email", "human@example.com")
	run(w.t, wd, "git", "config", "user.name", "human")
	run(w.t, wd, "git", "checkout", branch)
	if err := os.WriteFile(filepath.Join(wd, path), []byte(content+"\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
	run(w.t, wd, "git", "add", path)
	run(w.t, wd, "git", "commit", "-q", "-m", message)
	run(w.t, wd, "git", "push", "origin", branch)
}

// fileAt reads path's real content at ref in owner/name's bare repo --
// this test's only way to confirm what actually landed, independent of
// anything the store or the sim believes happened.
func (w *world) fileAt(owner, name, ref, path string) string {
	w.t.Helper()
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	return strings.TrimSpace(runOutput(w.t, w.upstreamDir, "git", "--git-dir", bare, "show", ref+":"+path))
}

func TestSyncPullRequestsRepairsAndLandsAConflictedMergeQueueHead(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	const owner, repoName = "acme", "widgets"
	w.newRepo(owner, repoName)
	repo := model.RepoRef{Owner: owner, Name: repoName}

	bare := filepath.Join(w.upstreamDir, owner, repoName+".git")
	sim := githubsim.New(owner, repoName, bare, "main")
	client := github.NewClient(sim, nil)

	deps := orchestrator.Deps{
		Store: w.store, Client: client, Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
	}

	task1 := model.Task{
		ID:     "t1",
		Intent: model.IntentImplement,
		Title:  "add config",
		Body:   "please add CONFIG.md",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human("alice")},
			Reason:      model.ReasonDirect,
		},
		Approval:  &model.Attribution{Actor: human("alice")},
		Target:    &repo,
		Binding:   model.BindingDirective,
		AutoMerge: true,
		CreatedAt: &baseTime,
	}
	if err := w.store.PutTask(w.ctx, task1); err != nil {
		t.Fatal(err)
	}

	branch1 := model.BranchName(task1.ID)
	clock := baseTime

	// Step 1: the agent pushes task1's branch and grain opens its PR.
	deps.Framework = scriptedFramework(configPushScript(w.remote(owner, repoName), branch1, "setting: from-task1"))
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (task1 push): %v", err)
	}
	if st, err := w.store.State(w.ctx, task1.ID); err != nil || st != model.StateCompleted {
		t.Fatalf("task1 state = %q (%v), want completed", st, err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", sim.PullRequests)
	}
	if got := w.fileAt(owner, repoName, branch1, "CONFIG.md"); got != "setting: from-task1" {
		t.Fatalf("CONFIG.md on %s = %q, want the agent's own content", branch1, got)
	}

	// Step 2: before anyone merges it, an unrelated commit lands straight
	// on main and genuinely conflicts with task1's branch -- a real git
	// conflict, not a scripted PullRequest.Mergeable.
	w.pushConflictingCommit(owner, repoName, "main", "CONFIG.md", "setting: from-main", "unrelated change conflicting with t1")
	if !w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("expected a genuine git conflict between main and task1's branch")
	}
	setMergeable(sim, branch1, false)

	// Step 3: SyncPullRequests notices the real conflict -- its own merge
	// of main into the branch is what proves it -- and sends task1 itself
	// back to work on that branch.
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (asks for the repair): %v", err)
	}
	obs, err := w.store.GetObservation(w.ctx, task1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRepairAt == nil {
		t.Fatalf("observation = %+v, want the repair recorded", obs)
	}
	if !obs.RepairInFlight() {
		t.Fatal("the repair does not read as in flight")
	}
	if st, err := w.store.State(w.ctx, task1.ID); err != nil || st != model.StateQueued {
		t.Fatalf("task1 state = %q (%v), want queued: the task itself repairs its branch", st, err)
	}
	if tasks := w.workTasks(); len(tasks) != 1 {
		t.Fatalf("expected no separate fix task to have been filed, got %d tasks", len(tasks))
	}

	// Step 4: dispatch task1 again, with a scripted agent that actually
	// resolves the conflict -- merges main into task1's own branch, for
	// real, and pushes it back to that same branch.
	deps.Framework = scriptedFramework(resolveScript(w.remote(owner, repoName), branch1))
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (dispatches the repair): %v", err)
	}
	if st, err := w.store.State(w.ctx, task1.ID); err != nil || st != model.StateCompleted {
		t.Fatalf("task1 state = %q (%v), want completed once the repair ran", st, err)
	}
	// The whole point: one pull request, still, and the resolution is on
	// its own head branch rather than on a second one waiting to be
	// merged in.
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected still exactly one pull request, got %+v", sim.PullRequests)
	}
	if got := w.fileAt(owner, repoName, branch1, "CONFIG.md"); got != "setting: from-task1" {
		t.Fatalf("CONFIG.md on %s after the repair = %q, want the resolved content", branch1, got)
	}

	// Step 5: GitHub -- played by the test, the same way
	// mergeBranchIntoDefault always has to for the git side of any merge
	// in this harness -- merges task1's own branch into main, which now
	// really is possible.
	if w.realConflict(owner, repoName, "main", branch1) {
		t.Fatal("expected task1's branch to be genuinely mergeable into main once the repair landed")
	}
	w.mergeBranchIntoDefault(owner, repoName, branch1, "main")
	setMergeable(sim, branch1, true)

	// Step 6: SyncPullRequests closes the task out, having really merged
	// -- no human ever needed to step in.
	clock = clock.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, deps, clock); err != nil {
		t.Fatalf("RunCycle (closes out): %v", err)
	}
	if st, err := w.store.State(w.ctx, task1.ID); err != nil || st != model.StateClosed {
		t.Fatalf("task1 state = %q (%v), want closed", st, err)
	}

	obs, err = w.store.GetObservation(w.ctx, task1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("task1 should never have been escalated to a human -- the automatic repair genuinely resolved it")
	}
	comments, err := w.store.Comments(w.ctx, task1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected exactly the one repair comment (no escalation), got %d: %+v", len(comments), comments)
	}

	if got := w.fileAt(owner, repoName, "main", "CONFIG.md"); got != "setting: from-task1" {
		t.Fatalf("CONFIG.md on main = %q, want the conflict resolved to task1's own content", got)
	}
}
