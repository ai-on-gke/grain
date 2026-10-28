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

// This file is bwsalmon/agents#337's own scenario: ten more e2e tests
// covering the CLI verbs and orchestrator paths the existing package
// never drove end to end. The four here all go through the real grain
// CLI subprocess (cli_test.go's own rig -- buildGrainCLI/runCLI/
// withStore/syncedSim/githubHostServer, reused directly since this file
// lives in the same package), because each one's whole point is a CLI
// verb other than create/get/approve, which is all TestCLICreatesTask
// AgentOpensPRAndUserMergeClosesIt and TestProposedTaskWaitsForApproval
// ThenRunsThroughTheCLI exercise today:
//
//   - TestCLICommentAnswersAParkedQuestionAndResumesTheTask drives the
//     human side of an awaiting_reply task through the real `grain
//     comment` verb -- every other test that parks a task on a question
//     (e2e_test.go's TestAgentQuestionParksTaskThenReplyResumesAndIt
//     Completes) replies with a bare store.Observe, never through
//     ui.Client.AddComment's own dual "append a comment, and clear
//     PendingQuestionCommentID if it was set" contract.
//   - TestCLIClosingAQueuedTaskThenReopeningItDispatchesForReal proves
//     `grain close`/`grain reopen` for real: a closed task drops out of
//     task_ready, and reopening returns it to exactly the state its
//     approval implies (model.StateOf's own precedence, since Reopen
//     only clears ClosedAt) -- queued, here, because it was closed before
//     ever completing -- letting a real dispatch pick it up again.
//   - TestCLICapabilityAttachAndDetachControlWhatARealDispatchMaterializes
//     is the success-path sibling of capability_test.go's own refusal
//     test: a capability attached through `grain capability ... attach`
//     is really materialized, used and revoked by a real dispatch, and
//     one detached through `grain capability ... detach` before dispatch
//     is never touched at all.
//   - TestCLIUpdateChangesBaseAndAutoMergeBeforeDispatchAndBothTakeEffect
//     proves `grain update` (cmdUpdate) -- base_directive_test.go's own
//     -base and auto_merge_test.go's own -auto-merge, both set at create
//     time -- also take effect when set afterward on an already-queued
//     task, before it is ever dispatched, and that both apply together:
//     the PR opens against the updated base and merges itself once clean.
//   - TestCLIDependsOnAttachesAndDetachesThroughTheAPI drives `grain
//     create -depends-on` and `grain update -depends-on/-no-depends-on`
//     with the #N, grain/task-N and task-page-URL forms, and an unknown
//     id the server refuses without writing anything.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// seedBareRepo is the same bare-repo-plus-one-commit-on-main setup
// cli_test.go's own TestCLICreatesTaskAgentOpensPRAndUserMergeClosesIt
// duplicates inline -- pulled out here only because this file needs it
// four times over, not because the package's own duplication discipline
// (harness_test.go's doc comment) has changed.
func seedBareRepo(t *testing.T, upstream, owner, repoName string) string {
	t.Helper()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")
	return bare
}

func TestCLICommentAnswersAParkedQuestionAndResumesTheTask(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := seedBareRepo(t, upstream, owner, repoName)

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	sandboxes := credentialed(t, remote)

	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json", "create",
		"-title", "needs a decision", "-body", "should this touch the public API or stay internal?",
		"-repo", owner+"/"+repoName, "-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}

	// Step 1: the agent parks on a question, and grain relays it as a
	// comment on the task -- no CLI subprocess yet, since nothing but the
	// scripted agent's own turn happens here.
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(askScript("should this touch the public API or stay internal?")),
	}
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (asks a question): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingReply {
			t.Fatalf("state after the question = %q, want awaiting_reply", st)
		}
	})

	// Step 2: a human answers through the real `grain comment` verb, not
	// a direct store.Observe -- ui.Client.AddComment's own doc comment is
	// what this proves: replying is what clears PendingQuestionCommentID,
	// with no separate "re-trigger" step needed.
	replied := runCLIStore(t, bin, storeDir,
		"-json", "comment", task.ID,
		"stay", "internal", "for", "now",
	)
	var repliedTask ui.Task
	if err := json.Unmarshal([]byte(replied), &repliedTask); err != nil {
		t.Fatalf("parsing grain comment -json output: %v\n%s", err, replied)
	}
	if repliedTask.State != model.StateQueued {
		t.Fatalf("state after the reply = %q, want queued", repliedTask.State)
	}

	// Step 3: dispatch picks the resumed task back up, and this time the
	// agent pushes for real.
	branch := model.BranchName(task.ID)
	deps.Framework = scriptedFramework(pushScript(remote, branch, task.ID))
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (push after reply): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after the resumed push = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected exactly one pull request, got %d", sim.pullRequestCount())
	}
	prNumber := sim.firstPullRequestNumber()

	// Step 4: submit the PR, exactly cli_test.go's own step 3, and sync
	// closes the task out.
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "github@example.com")
	run(t, mergeWd, "git", "config", "user.name", "github (simulated merge)")
	run(t, mergeWd, "git", "fetch", "origin", branch)
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	run(t, mergeWd, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("submitting (merging) the pull request: %v", err)
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("RunCycle (sync): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after the merge = %q, want closed", st)
		}
	})

	// The reply itself must be readable back through the CLI too, not
	// just have had the right side effect on state.
	got := runCLIStore(t, bin, storeDir, "-json", "get", task.ID)
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(got), &detail); err != nil {
		t.Fatalf("parsing grain get -json output: %v\n%s", err, got)
	}
	foundReply := false
	for _, c := range detail.Comments {
		if strings.Contains(c.Body, "stay internal for now") {
			foundReply = true
		}
	}
	if !foundReply {
		t.Fatalf("expected the human's reply among the task's comments, got %+v", detail.Comments)
	}
}

func TestCLIClosingAQueuedTaskThenReopeningItDispatchesForReal(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := seedBareRepo(t, upstream, owner, repoName)

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	sandboxes := credentialed(t, remote)

	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json", "create",
		"-title", "close me then bring me back", "-body", "...",
		"-repo", owner+"/"+repoName, "-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}

	// Step 1: close the task before anything ever dispatches it -- a
	// change of mind, not a run that finished.
	closed := runCLIStore(t, bin, storeDir, "-json", "close", task.ID)
	var closedTask ui.Task
	if err := json.Unmarshal([]byte(closed), &closedTask); err != nil {
		t.Fatalf("parsing grain close -json output: %v\n%s", err, closed)
	}
	if closedTask.State != model.StateClosed {
		t.Fatalf("state after close = %q, want closed", closedTask.State)
	}

	// A closed task is not dispatchable, however free the slot.
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(pushScript(remote, model.BranchName(task.ID), task.ID)),
	}
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (task closed): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state while closed = %q, want it to stay closed (never dispatched)", st)
		}
	})
	if sim.pullRequestCount() != 0 {
		t.Fatalf("a closed task must never be dispatched, but a pull request was opened: %d", sim.pullRequestCount())
	}

	// Step 2: reopen through the real CLI verb. Reopen only clears
	// ClosedAt (pkg/ui/client.go's own setClosed), so this task -- closed
	// before it ever completed -- returns to exactly queued, per
	// model.StateOf's own precedence, ready for a real dispatch.
	reopened := runCLIStore(t, bin, storeDir, "-json", "reopen", task.ID)
	var reopenedTask ui.Task
	if err := json.Unmarshal([]byte(reopened), &reopenedTask); err != nil {
		t.Fatalf("parsing grain reopen -json output: %v\n%s", err, reopened)
	}
	if reopenedTask.State != model.StateQueued {
		t.Fatalf("state after reopen = %q, want queued", reopenedTask.State)
	}

	// Step 3: it dispatches, runs a real push/PR/merge/close cycle of its
	// own, exactly like any other task -- reopening did not leave it in
	// some half-broken state a real run cannot finish from.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (after reopen): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after the reopened task's push = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected exactly one pull request after reopening, got %d", sim.pullRequestCount())
	}
	sim.markPullRequestClean(sim.firstPullRequestNumber())

	// This task never had -auto-merge set, so a human still has to merge
	// it -- confirming reopen did not silently grant it anything it
	// wasn't created with.
	prNumber := sim.firstPullRequestNumber()
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "github@example.com")
	run(t, mergeWd, "git", "config", "user.name", "github (simulated merge)")
	run(t, mergeWd, "git", "fetch", "origin", model.BranchName(task.ID))
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+model.BranchName(task.ID), "-m", "Merge")
	run(t, mergeWd, "git", "push", "origin", "main")
	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("submitting (merging) the reopened task's pull request: %v", err)
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("RunCycle (sync): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("final state = %q, want closed", st)
		}
	})
}

// countingCapability is a model.CapabilityProvider that always resolves
// and records how many times it was actually materialized and revoked --
// the success-path counterpart to capability_test.go's own
// refusingCapability, and pkg/orchestrator/run_test.go's own
// fakeCapability with the placement machinery stripped out, since this
// test only needs to know the grant was really used, not what it wrote.
type countingCapability struct {
	name string

	mu               sync.Mutex
	materializeCalls int
	revoked          []model.Lease
}

func (c *countingCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: c.name, Label: "grain-" + c.name, Source: model.GrantByLabel, Provision: model.ProvisionMint}
}

func (c *countingCapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}

func (c *countingCapability) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c.mu.Lock()
	c.materializeCalls++
	c.mu.Unlock()
	return model.Materialization{
		Lease: &model.Lease{Capability: c.name, Resource: "res-" + cc.Task.ID, MintedBy: model.CredentialRef{Name: "test"}, IssuedAt: cc.Now},
	}, nil
}

func (c *countingCapability) PromptSection(context.Context, model.CapabilityContext, []model.Placement) (string, error) {
	return "capability " + c.name + " is ready", nil
}

func (c *countingCapability) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, lease)
	return nil
}

func (c *countingCapability) calls() (materialized, revoked int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.materializeCalls, len(c.revoked)
}

func TestCLICapabilityAttachAndDetachControlWhatARealDispatchMaterializes(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := seedBareRepo(t, upstream, owner, repoName)

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	sandboxes := credentialed(t, remote)

	storeDir := t.TempDir()
	cap := &countingCapability{name: "gemini-key"}
	cfg := orchestrator.Config{Capabilities: model.NewCapabilityRegistry(cap)}

	// Task A: attached through the real CLI verb, left attached, and
	// actually dispatched -- the capability must be materialized once and
	// revoked once by the time its run finishes.
	createdA := runCLIStore(t, bin, storeDir, "-json", "create",
		"-title", "uses a capability", "-body", "...", "-repo", owner+"/"+repoName, "-approve")
	var taskA ui.Task
	if err := json.Unmarshal([]byte(createdA), &taskA); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, createdA)
	}
	attached := runCLIStore(t, bin, storeDir, "-json", "capability", taskA.ID, "gemini-key", "attach")
	var attachedTask ui.Task
	if err := json.Unmarshal([]byte(attached), &attachedTask); err != nil {
		t.Fatalf("parsing grain capability attach -json output: %v\n%s", err, attached)
	}
	if len(attachedTask.Capabilities) != 1 || attachedTask.Capabilities[0] != "gemini-key" {
		t.Fatalf("capabilities after attach = %+v, want [gemini-key]", attachedTask.Capabilities)
	}

	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, Config: cfg,
		MaxWorkers: 1,
		Framework:  scriptedFramework(pushScript(remote, model.BranchName(taskA.ID), taskA.ID)),
	}
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (task A, capability attached): %v", err)
		}
		st, err := store.State(ctx, taskA.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("task A state = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if materialized, revoked := cap.calls(); materialized != 1 || revoked != 1 {
		t.Fatalf("attached capability materialize/revoke calls = %d/%d, want 1/1", materialized, revoked)
	}

	// Task B: attached, then detached before it is ever dispatched --
	// SetCapability's own doc comment says detaching is what a second
	// grants write is for, and the point here is that a detached grant
	// leaves no trace on the run at all, not even a Materialize call.
	createdB := runCLIStore(t, bin, storeDir, "-json", "create",
		"-title", "never actually uses a capability", "-body", "...", "-repo", owner+"/"+repoName, "-approve")
	var taskB ui.Task
	if err := json.Unmarshal([]byte(createdB), &taskB); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, createdB)
	}
	runCLIStore(t, bin, storeDir, "-json", "capability", taskB.ID, "gemini-key", "attach")
	detached := runCLIStore(t, bin, storeDir, "-json", "capability", taskB.ID, "gemini-key", "detach")
	var detachedTask ui.Task
	if err := json.Unmarshal([]byte(detached), &detachedTask); err != nil {
		t.Fatalf("parsing grain capability detach -json output: %v\n%s", err, detached)
	}
	if len(detachedTask.Capabilities) != 0 {
		t.Fatalf("capabilities after detach = %+v, want none", detachedTask.Capabilities)
	}

	// Nothing to reset between the two dispatches: each run builds its own
	// sandbox, so task B's clone cannot land on top of task A's. This used
	// to need a second slot handed to deps, because a slot's directory was
	// reused and A's "work" clone was still sitting in it.
	deps.Framework = scriptedFramework(pushScript(remote, model.BranchName(taskB.ID), taskB.ID))
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (task B, capability detached): %v", err)
		}
		st, err := store.State(ctx, taskB.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("task B state = %q, want awaiting_submit: the pull request is open and unsubmitted", st)
		}
	})
	if materialized, revoked := cap.calls(); materialized != 1 || revoked != 1 {
		t.Fatalf("materialize/revoke calls after task B (capability detached before dispatch) = %d/%d, want them unchanged at 1/1", materialized, revoked)
	}
}

func TestCLIUpdateChangesBaseAndAutoMergeBeforeDispatchAndBothTakeEffect(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)

	const owner, repoName = "acme", "widgets"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")
	// release diverges from main, the same as base_directive_test.go's
	// own seeding, so a branch built on it -- and only one built on it --
	// carries this line.
	run(t, seed, "git", "checkout", "-b", "release")
	if err := os.WriteFile(filepath.Join(seed, "NOTES.md"), []byte("seed\nrelease-only line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "NOTES.md")
	run(t, seed, "git", "commit", "-q", "-m", "release branch commit")
	run(t, seed, "git", "push", "origin", "release")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	sandboxes := credentialed(t, remote)

	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir, "-json", "create",
		"-title", "add a NOTES entry on release", "-body", "...", "-repo", owner+"/"+repoName, "-approve")
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.Base != "" || task.AutoMerge {
		t.Fatalf("task as created = %+v, want neither a base nor auto-merge yet", task)
	}

	// The verb this test exists for: change both fields on an
	// already-queued, not-yet-dispatched task.
	updated := runCLIStore(t, bin, storeDir, "-json", "update", "-base", "release", "-auto-merge", task.ID)
	var updatedTask ui.Task
	if err := json.Unmarshal([]byte(updated), &updatedTask); err != nil {
		t.Fatalf("parsing grain update -json output: %v\n%s", err, updated)
	}
	if updatedTask.Base != "release" {
		t.Fatalf("base after update = %q, want release", updatedTask.Base)
	}
	if !updatedTask.AutoMerge {
		t.Fatal("auto-merge after update = false, want true")
	}

	branch := model.BranchName(task.ID)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(releaseBranchPushScript(remote, branch, task.ID)),
	}
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle (push): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateCompleted {
			t.Fatalf("state after the push = %q, want completed", st)
		}
	})
	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected exactly one pull request, got %d", sim.pullRequestCount())
	}
	if gotBase := sim.firstPullRequestBase(); gotBase != "release" {
		t.Fatalf("pull request base = %q, want release (the updated base)", gotBase)
	}
	prNumber := sim.firstPullRequestNumber()
	sim.markPullRequestClean(prNumber)

	// No human ever merges this one: the updated auto-merge flag is what
	// must make SyncPullRequests do it on its own.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (auto-merge sync): %v", err)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after auto-merge = %q, want closed", st)
		}
	})
	if sim.pullRequestState(prNumber) != "closed" {
		t.Fatalf("pull request state = %q, want closed (auto-merged)", sim.pullRequestState(prNumber))
	}

	mainNotes := runOutput(t, upstream, "git", "--git-dir", bare, "show", "main:NOTES.md")
	if strings.Contains(mainNotes, "change for "+task.ID) {
		t.Fatalf("main:NOTES.md contains the agent's change, want it untouched by a release-targeted auto-merge:\n%s", mainNotes)
	}
	releaseNotes := runOutput(t, upstream, "git", "--git-dir", bare, "show", "release:NOTES.md")
	if !strings.Contains(releaseNotes, "change for "+task.ID) {
		t.Fatalf("release:NOTES.md does not contain the agent's change:\n%s", releaseNotes)
	}
}

// runCLIStoreFails is runCLIStore for a command that must be refused: it
// fails the test if the CLI exits zero, and returns what it printed to
// stderr so the caller can check the refusal says why.
func runCLIStoreFails(t *testing.T, bin, storeDir string, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		cfg := ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}
		srv := httptest.NewServer(ui.NewServer(withAccessCode(cfg), store))
		defer srv.Close()
		cmd := exec.Command(bin, append([]string{"-server", srv.URL}, args...)...)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatalf("grain %s: succeeded, want it refused", strings.Join(args, " "))
		}
	})
	return stderr.String()
}

func TestCLIDependsOnAttachesAndDetachesThroughTheAPI(t *testing.T) {
	t.Parallel()
	bin := buildGrainCLI(t)
	storeDir := t.TempDir()

	create := func(args ...string) ui.Task {
		t.Helper()
		out := runCLIStore(t, bin, storeDir, append([]string{"-json", "create", "-no-repo"}, args...)...)
		var task ui.Task
		if err := json.Unmarshal([]byte(out), &task); err != nil {
			t.Fatalf("parsing grain create -json output: %v\n%s", err, out)
		}
		return task
	}
	update := func(id string, args ...string) ui.Task {
		t.Helper()
		out := runCLIStore(t, bin, storeDir, append([]string{"-json", "update", id}, args...)...)
		var task ui.Task
		if err := json.Unmarshal([]byte(out), &task); err != nil {
			t.Fatalf("parsing grain update -json output: %v\n%s", err, out)
		}
		return task
	}
	// Read back from the store rather than trusting the CLI's own echo:
	// the links are what the dispatcher gates on.
	stored := func(id string) []string {
		t.Helper()
		var deps []string
		withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
			task, err := store.GetTask(ctx, id)
			if err != nil || task == nil {
				t.Fatalf("reading task %s back: %v", id, err)
			}
			for _, l := range task.Links {
				if l.Kind == model.LinkDependsOn {
					deps = append(deps, l.Target)
				}
			}
		})
		return deps
	}
	want := func(what string, got []string, ids ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(ids, ",") {
			t.Fatalf("%s: depends on %v, want %v", what, got, ids)
		}
	}

	first := create("-title", "lands first")
	second := create("-title", "lands second")

	// #N on create.
	dependent := create("-title", "waits on both", "-depends-on", "#"+first.ID)
	want("create -depends-on #N (echo)", dependent.DependsOn, first.ID)
	want("create -depends-on #N (store)", stored(dependent.ID), first.ID)

	// A task page's URL on update adds to the set rather than replacing it.
	got := update(dependent.ID, "-depends-on", "http://grain.example/tasks/"+second.ID)
	want("update -depends-on URL (echo)", got.DependsOn, first.ID, second.ID)
	want("update -depends-on URL (store)", stored(dependent.ID), first.ID, second.ID)

	// Detaching takes the same forms; grain/task-N here.
	got = update(dependent.ID, "-no-depends-on", "grain/task-"+first.ID)
	want("update -no-depends-on (echo)", got.DependsOn, second.ID)
	want("update -no-depends-on (store)", stored(dependent.ID), second.ID)

	// An unknown id is refused by the server, and nothing is written.
	stderr := runCLIStoreFails(t, bin, storeDir, "update", dependent.ID, "-depends-on", "#9999")
	if !strings.Contains(stderr, "unknown task 9999") {
		t.Errorf("update -depends-on #9999: stderr %q, want it to name the unknown task", stderr)
	}
	want("after refused update", stored(dependent.ID), second.ID)

	stderr = runCLIStoreFails(t, bin, storeDir, "create", "-no-repo", "-title", "never filed", "-depends-on", "#9999")
	if !strings.Contains(stderr, "unknown task 9999") {
		t.Errorf("create -depends-on #9999: stderr %q, want it to name the unknown task", stderr)
	}
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 3 {
			t.Errorf("after refused create: %d tasks in the store, want 3", len(tasks))
		}
	})
}
