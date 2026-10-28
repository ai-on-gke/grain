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
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// testTargetKind stands in for a kind an edition
// registers and core never imports: a workspace grant it implies, and a
// prompt section of its own.
const testTargetKind = "orchestrator-test-target"

const testTargetSection = "Work in the test workspace; there is no checkout."

func init() {
	model.RegisterChangeTarget(model.ChangeTargetRegistration{
		Kind: testTargetKind,
		EnsureGrants: func(_ json.RawMessage, grants []model.Grant) ([]model.Grant, error) {
			for _, g := range grants {
				if g.Capability == "test-workspace" {
					return grants, nil
				}
			}
			return append(grants, model.Grant{Capability: "test-workspace"}), nil
		},
		PromptSection: func(model.Task, []string) string { return testTargetSection },
	})
}

func changeTargetTask(t *testing.T, ctx context.Context, store *model.Store, id string, grants ...model.Grant) model.Task {
	t.Helper()
	task := dispatchTask(t, ctx, store, id)
	task.Target = nil
	task.ChangeTarget = &model.ChangeTarget{Kind: testTargetKind}
	task.Grants = grants
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("storing the change-target task: %v", err)
	}
	return task
}

// A changelist-style task dispatches on the no-repo path: a sandbox with
// nothing cloned into it, and its kind's prompt section in place of the
// branch and push instructions.
func TestRunDispatchPreparesAChangeTargetTaskWithoutCloning(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	changeTargetTask(t, ctx, store, "t1", model.Grant{Capability: "test-workspace"})

	prompt := promptFor(t, ctx, store, orchestrator.Config{}, "t1", "r1")
	if !strings.Contains(prompt, testTargetSection) {
		t.Errorf("prompt does not carry the target's own section:\n%s", prompt)
	}
	for _, git := range []string{"already cloned for you", "Push your change to a new branch", "no repo attached", "git push origin"} {
		if strings.Contains(prompt, git) {
			t.Errorf("a change-target task's prompt says %q:\n%s", git, prompt)
		}
	}
}

// Validation runs again at dispatch: a task whose target implies a grant
// it does not hold is refused before any agent starts, rather than
// having the grant attached without the approval it needs.
func TestRunDispatchRefusesAChangeTargetTaskMissingItsGrant(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := changeTargetTask(t, ctx, store, "t1")

	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	started := false
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		started = true
		return pushed(), nil
	})
	_, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{}, task, d, nil, t.TempDir(), "", nil, baseTime)
	if err == nil || !strings.Contains(err.Error(), "test-workspace") {
		t.Errorf("RunDispatch error = %v, want a refusal naming the missing grant", err)
	}
	if started {
		t.Errorf("the agent was started for a task refused at dispatch")
	}
}

// Git tasks are unchanged: a stored git task reads back with no change
// target, and its dispatched prompt still has the branch and push
// instructions.
func TestGitTaskPromptIsUnchangedByChangeTargets(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	stored, err := store.GetTask(ctx, "t1")
	if err != nil || stored == nil {
		t.Fatalf("reading the task: %v", err)
	}
	if stored.ChangeTarget != nil {
		t.Errorf("a git task reads back with change target %+v", stored.ChangeTarget)
	}
	prompt := promptFor(t, ctx, store, orchestrator.Config{}, "t1", "r1")
	if !strings.Contains(prompt, "Push your change to a new branch") || strings.Contains(prompt, testTargetSection) {
		t.Errorf("git task prompt changed:\n%s", prompt)
	}
	want := orchestrator.BuildPrompt(*stored, "", "", false, 0, orchestrator.History{}, nil)
	stored.ChangeTarget = nil
	if got := orchestrator.BuildPrompt(*stored, "", "", false, 0, orchestrator.History{}, nil); withoutClock(got) != withoutClock(want) {
		t.Errorf("BuildPrompt differs for a git task")
	}
}
