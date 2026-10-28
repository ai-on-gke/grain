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

package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A task filed onto a branch grain has been asked to create
// (grain/task-2317's "create a new branch") is not dispatched while that
// branch is still pending -- its clone would find no such base and park
// it -- and says what it waits for, including why the create has not
// landed. It starts on the first cycle after the branch is cut.
func TestDispatchHoldsATaskUntilItsRequestedBranchIsCut(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Base = "hotfix"
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RequestBranchFrom(ctx, repo, "hotfix", "release/1.2", baseTime); err != nil {
		t.Fatal(err)
	}

	var asked int
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			asked++
			return stubFramework{result: toolResult(agent.ToolCall{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	cycle := func(at time.Time) *model.GrantWait {
		t.Helper()
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		w, err := store.GrantWaitOf(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	// release/1.2 is not there yet, so the reconciler cannot cut hotfix
	// and the second cycle's wait carries its reason.
	cycle(baseTime)
	w := cycle(baseTime.Add(time.Minute))
	if asked != 0 {
		t.Fatal("a task whose branch is still pending was dispatched")
	}
	if w == nil || w.Capability != model.BranchWaitCapability ||
		!strings.Contains(w.Resource, `"hotfix"`) || !strings.Contains(w.Resource, "release/1.2") {
		t.Fatalf("wait = %+v; want a branch wait naming hotfix and the missing release/1.2", w)
	}

	pushCommitSaying(t, sim.BareRepo, "release/1.2", "the release")
	cycle(baseTime.Add(2 * time.Minute)) // still held; the reconciler cuts hotfix after dispatch
	if asked != 0 {
		t.Fatal("dispatched in the cycle that cut the branch, before it existed")
	}
	cycle(baseTime.Add(3 * time.Minute))
	if asked != 1 {
		t.Fatalf("once the branch is cut, Deps.Framework was asked %d times, want 1", asked)
	}
}
