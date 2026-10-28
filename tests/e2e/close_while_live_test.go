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

// TestClosingATaskWhileItsRunIsStillLiveCancelsItAndNeverReDispatchesOrOpensAPullRequest
// is bwsalmon/agents#332's own scenario, filed by bwsalmon/agents#319 as
// one of a dozen siblings extending e2e's whole-pipeline coverage to
// model/state.go's StateOf: a task closed while its run is still live.
// model/simulate_test.go's TestClosingATaskWhileItsRunIsStillLiveOutranks
// Running already proves, at the store level, that such a task reads
// closed even though its slot stays occupied until FinishRun actually
// runs. What no store-level test can check is what happens to the run
// itself once it does finish.
//
// bwsalmon/agents#346 changed what that is: this used to drive a real
// `grain close` through the CLI binary, let the scripted agent's turn
// actually push a branch, and assert RunDispatch let it, on the theory
// that "a real sandbox would not be killed mid-flight just because
// someone closed the issue." That was the gap #346 asked to close --
// orchestrator.RunDispatch now watches store for exactly this race and
// cancels the ctx driving both the agent and its tool calls the moment it
// sees the task closed, so this test now asserts the opposite: the
// scripted agent's own run_command call (a real clone/commit/push against
// this test's own bare upstream repo, over pkg/mcp's real
// exec.CommandContext-backed sandbox tools -- nothing mocked below the
// MCP layer) never gets to run at all, and no branch ever lands upstream.
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestClosingATaskWhileItsRunIsStillLiveCancelsItAndNeverReDispatchesOrOpensAPullRequest(t *testing.T) {
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

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	// File the task the way an operator would, through the real CLI.
	storeDir := t.TempDir()
	created := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "add a NOTES entry",
		"-body", "please add a line to NOTES.md",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}
	if task.State != model.StateQueued {
		t.Fatalf("state after create = %q, want queued", task.State)
	}

	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	// This test drives RunDispatch directly, below the Sandboxes layer, so
	// it builds the one sandbox it needs by hand rather than through a
	// backend.
	root := t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, remote, "unused", mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)

	// Dispatch the task: its run now occupies the slot (dispatch.Cycle
	// calls StartRun), but nothing has run the agent or called FinishRun
	// yet -- a live run, exactly the race this test needs.
	var d dispatch.Dispatch
	var fullTask model.Task
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, baseTime)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != task.ID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, task.ID)
		}
		d = dispatches[0]

		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateRunning {
			t.Fatalf("state after dispatch = %q, want running", st)
		}

		got, err := store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s): %v (nil=%v)", task.ID, err, got == nil)
		}
		fullTask = *got
	})

	// Close the task through the real CLI while its run is still live --
	// the same command an operator would run at a shell, racing an agent
	// whose run is already live.
	closed := runCLIStore(t, bin, storeDir, "-json", "close", task.ID)
	var closedTask ui.Task
	if err := json.Unmarshal([]byte(closed), &closedTask); err != nil {
		t.Fatalf("parsing grain close -json output: %v\n%s", err, closed)
	}
	if closedTask.State != model.StateClosed {
		t.Fatalf("state after grain close = %q, want closed even with a run still live", closedTask.State)
	}

	// RunDispatch runs next, exactly as it would for any other live run --
	// but the task is already closed by the time it starts, so
	// bwsalmon/agents#346's own synchronous checkTaskClosed call (see
	// RunDispatch's own doc comment) must cancel the agent's ctx before
	// framework.Run is ever invoked, stopping the scripted agent's
	// run_command call from ever reaching the real sandbox tools behind
	// it. It should return quickly -- no wall-clock reliance on
	// CancelPollInterval -- with a non-nil result-less error.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state before the run finishes = %q, want closed (the run is still live, but closing outranks running)", st)
		}
		if occ, err := store.LiveRunCount(ctx); err != nil || occ != 1 {
			t.Fatalf("live runs before the run finishes = %d (%v), want the run still live", occ, err)
		}

		fw := antigravity.NewForTest(antigravity.Steps(pushScript(remote, branch, task.ID)...))
		tools := mcp.NewSandboxTools(root)

		start := time.Now()
		result, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, fullTask, d, tools, root, "", nil, baseTime.Add(time.Minute))
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("RunDispatch took %s for an already-closed task, want near-instant cancellation", elapsed)
		}
		if result != nil {
			t.Fatalf("RunDispatch result = %+v, want nil: a task closed before its run started must never let the agent touch a real sandbox", result)
		}
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("RunDispatch err = %v, want an error naming the task's closure", err)
		}

		st, err = store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after FinishRun = %q, want still closed", st)
		}
		if occ, err := store.LiveRunCount(ctx); err != nil || occ != 0 {
			t.Fatalf("occupied slots after FinishRun = %v (%v), want none: FinishRun still frees the slot even for a cancelled run", occ, err)
		}
	})

	// The slot is free again, but dispatch.Cycle must never hand a closed
	// task another run.
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		again, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, baseTime.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		for _, dd := range again {
			if dd.TaskID == task.ID {
				t.Fatalf("Cycle re-dispatched a closed task: %+v", again)
			}
		}
	})

	// Cancelling the run this early means the agent's own commit/push
	// never happened at all -- unlike a task closed only after its work
	// already landed (which #346 leaves untouched: revoking a push
	// already accepted upstream is not "kill the sandbox process," and
	// orchestrator.ProcessResult already refuses to open a pull request
	// for a since-closed task's push either way, per this test's own
	// pre-#346 history), so no branch reaches the upstream repo, and
	// there is no result for ProcessResult to even be called with.
	if branchExistsInBare(t, bare, branch) {
		t.Fatalf("branch %q exists in the upstream repo, want none: closing before the run started should have cancelled it before its first tool call", branch)
	}
	if n := sim.pullRequestCount(); n != 0 {
		t.Fatalf("%d pull request(s) exist for a cancelled, since-closed task, want none", n)
	}

	// Confirm the loop stayed closed the way an operator would actually
	// see it, by asking the CLI itself in a fresh subprocess.
	got := runCLIStore(t, bin, storeDir, "-json", "get", task.ID)
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(got), &detail); err != nil {
		t.Fatalf("parsing grain get -json output: %v\n%s", err, got)
	}
	if detail.State != model.StateClosed {
		t.Fatalf("task %s state = %q as the CLI reports it, want closed", task.ID, detail.State)
	}
}

// branchExistsInBare reports whether ref exists in the bare repo at path
// bare -- world.branchExists' own check, duplicated here since this test
// builds no world (see harness_test.go's own doc comment on why small
// helpers are duplicated per file rather than shared).
func branchExistsInBare(t *testing.T, bare, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", ref)
	return cmd.Run() == nil
}
