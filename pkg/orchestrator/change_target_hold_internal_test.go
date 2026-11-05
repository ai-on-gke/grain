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

package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// holdTargetKind is a change target kind registered by this file alone,
// standing in for one an edition registers: it implies a workspace
// grant.
const holdTargetKind = "orchestrator-hold-test-target"

func init() {
	model.RegisterChangeTarget(model.ChangeTargetRegistration{
		Kind: holdTargetKind,
		EnsureGrants: func(_ json.RawMessage, grants []model.Grant) ([]model.Grant, error) {
			for _, g := range grants {
				if g.Capability == "hold-workspace" {
					return grants, nil
				}
			}
			return append(grants, model.Grant{Capability: "hold-workspace"}), nil
		},
	})
}

// A queued change-target task that its target refuses as it stands --
// task 1860, an auto-merge change-target task -- is held before any sandbox is
// set up for it, saying why on the task and once in its conversation,
// rather than failing setup every cycle; and the hold lets it go the
// first cycle after somebody fixes it.
func TestChangeTargetHoldHoldsARefusedTaskUntilItIsFixed(t *testing.T) {
	store, ctx := openTreeStore(t)
	deps := Deps{Store: store}
	task := model.Task{ID: "task", Title: "t", AutoMerge: true,
		ChangeTarget: &model.ChangeTarget{Kind: holdTargetKind},
		Grants:       []model.Grant{{Capability: "hold-workspace"}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		why := changeTargetHold(ctx, deps)(task.ID)
		if !strings.Contains(why, "auto_merge cannot be set") {
			t.Fatalf("hold = %q, want it to say auto_merge cannot be set", why)
		}
	}
	wait, err := store.GrantWaitOf(ctx, task.ID)
	if err != nil || wait == nil || wait.Capability != model.ChangeTargetWaitCapability ||
		!strings.Contains(wait.Resource, "auto_merge") {
		t.Fatalf("wait = %+v (%v), want a change-target wait naming auto_merge", wait, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "auto_merge cannot be set") {
		t.Fatalf("comments = %+v, want one saying why, however many cycles held it", comments)
	}

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if why := changeTargetHold(ctx, deps)(task.ID); why != "" {
		t.Fatalf("hold = %q after auto-merge was turned off", why)
	}
	if wait, _ := store.GrantWaitOf(ctx, task.ID); wait != nil {
		t.Fatalf("wait %+v left behind", wait)
	}
}

// A git task is none of this hold's business.
func TestChangeTargetHoldPassesAGitTask(t *testing.T) {
	store, ctx := openTreeStore(t)
	task := model.Task{ID: "task", Title: "t", Target: &widgets, AutoMerge: true}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if why := changeTargetHold(ctx, Deps{Store: store})(task.ID); why != "" {
		t.Fatalf("hold = %q for a git task", why)
	}
}

// A change-target proposal from an auto-merge job does not inherit
// auto-merge: it has no pull request, and a task carrying it is one every
// dispatch refuses (task 1860). The proposal says so.
func TestChangeTargetProposalDoesNotInheritAutoMerge(t *testing.T) {
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	store, ctx := openTreeStore(t)
	source := externalSource("7")
	source.Base = ""
	source.Target = nil
	source.AutoMerge = true
	source.Grants = []model.Grant{{Capability: "hold-workspace"}}
	if err := store.PutTask(ctx, source); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{{
		Name: "propose_task",
		Arguments: map[string]any{"title": "Fix it in the workspace", "body": "And then this.",
			holdTargetKind: true},
	}}}
	if err := relayProposedTasks(ctx, store, nil, nil, source, result, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal *model.Task
	for i := range tasks {
		if tasks[i].Parent() == source.ID {
			proposal = &tasks[i]
		}
	}
	if proposal == nil {
		t.Fatal("the proposal was never filed")
	}
	if proposal.ChangeTarget == nil || proposal.ChangeTarget.Kind != holdTargetKind {
		t.Fatalf("change target = %+v, want %s", proposal.ChangeTarget, holdTargetKind)
	}
	if proposal.AutoMerge {
		t.Error("a change-target proposal inherited auto-merge")
	}
	if !strings.Contains(proposal.Body, "did not inherit") {
		t.Errorf("body does not say why it did not inherit auto-merge:\n%s", proposal.Body)
	}
	if err := dispatchChangeTargetRefusal(*proposal); err != nil {
		t.Errorf("the proposal as filed is refused at dispatch: %v", err)
	}
}
