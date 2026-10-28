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

// bwsalmon/agents#514's own live confirmation: finish_test.go's
// TestRunCycleOpensAPullRequestForABranchAFailedRunAlreadyPushed already
// proves the salvage path with a scripted agent.Framework standing in for
// the model; this drives the exact same "pushed, then ran out of turns"
// scenario through a real agent/antigravity run instead, gated on
// GEMINI_API_KEY and an installed agy binary (liveFramework) the same way
// tests/e2e/live_test.go's TestLiveIssueCompletesEndToEnd is, so it
// runs in CI (where neither is present) without failing but proves,
// against the real model deciding its own tool calls, that a task
// completed this way stops reporting a stale failure once it has (see
// client.go's own guard added for this issue).
//
//	GEMINI_API_KEY=... go test ./pkg/orchestrator/... -run TestLiveRunSalvagedAfterExceedingMaxTurnsReportsNoStaleFailure -v
package orchestrator_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestLiveRunSalvagedAfterExceedingMaxTurnsReportsNoStaleFailure(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets514", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets514"}

	// The task body hands the model the one thing BuildPrompt itself
	// cannot: the actual filesystem path standing in for a remote it can
	// clone. deps.Config.GitRemoteBase is left unset (as every other test
	// in this file leaves it), so RunDispatch does no checkout of its own
	// and the model must clone this path itself, exactly the way real
	// tasks that do set GitRemoteBase would clone through gitproxy
	// instead.
	tasks, task := fileTask(t, ctx, store, repo, "add a NOTES file",
		"You have exactly ONE turn before you are cut off: this is your only chance to call a "+
			"tool, so do not reply with plain text first, and do not split this across multiple "+
			"tool calls. Call run_command exactly once, with a single shell command (chain steps "+
			"with &&) that clones "+sim.BareRepo+" into a directory named work, creates and checks "+
			"out the branch named above inside it, appends the line 'the agent was here' to NOTES.md "+
			"(creating it if needed), commits that change, and pushes the branch to the origin remote.")

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())

	framework := liveFramework(t)

	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialingSandboxes{inner: sandboxes, t: t}, MaxWorkers: 1,
		Framework: orchestrator.StaticFramework(framework),
		// The real repro: a budget so tight the model cannot possibly
		// also reply with a final answer once it has spent its one turn
		// on the shell command that matters -- turning "exceeded max
		// turns" from a scripted certainty (finish_test.go's
		// ranOutOfTurns) into a real one, driven by a real model choosing
		// its own tool calls.
		Config: orchestrator.Config{MaxAgentTurns: 1},
	}

	clock := baseTime
	if err := orchestrator.RunCycle(ctx, deps, clock); err == nil {
		t.Fatal("RunCycle: expected the 1-turn budget to end this run in error, got none")
	} else {
		t.Logf("RunCycle ended (expected): %v", err)
	}

	branch := string(model.BranchName(task.ID))
	if exists, err := client.BranchExists(repo.Owner, repo.Name, branch); err != nil {
		t.Fatalf("BranchExists: %v", err)
	} else if !exists {
		t.Fatalf("expected the model to have pushed %s before its turn budget ran out", branch)
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateCompleted {
		t.Fatalf("state = %q, want completed: a pushed branch must still be salvaged into a pull "+
			"request even though the run that pushed it ended in error "+
			"(orchestrator/cycle.go's own \"only the ending failed\")", st)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("expected a pull request to have been opened for the salvaged branch, got %+v", sim.PullRequests)
	}

	detail, err := tasks.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateCompleted {
		t.Fatalf("detail.State = %q, want completed", detail.State)
	}
	// This is the bug itself: the run that pushed this branch keeps its
	// own outcome "failed" forever (it really did exceed its turn
	// budget), which used to leave GetTask reporting "1 consecutive
	// failed attempt" on a task that had, in every other respect, plainly
	// succeeded.
	if detail.FailedAttempts != 0 || detail.LastFailureAt != nil || detail.LastFailureReason != "" {
		t.Fatalf("FailedAttempts = %d, LastFailureAt = %v, LastFailureReason = %q, want all zero on a completed task",
			detail.FailedAttempts, detail.LastFailureAt, detail.LastFailureReason)
	}
}
