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

package ui_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// hold_for_approval can only lower what a proposer's permission does:
// held, a proposal is filed proposed whether or not the permission covers
// it; not held, the permission decides as it always has. The reason is on
// the response either way it waits, for propose_task to relay.
func TestHoldForApprovalOnlyLowersWhatThePermissionWouldDo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		permission bool
		hold       bool
		want       model.State
		heldTag    bool
		reason     string
	}{
		{"auto with permission", true, false, model.StateQueued, false, ""},
		{"auto without permission", false, false, model.StateProposed, false, "your permission does not cover it"},
		{"hold with permission", true, true, model.StateProposed, true, "you asked to hold it"},
		{"hold without permission", false, true, model.StateProposed, false, "you asked to hold it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, ctx := testClient(t)
			parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "the whole job", Approved: true, AutoMerge: tc.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "a piece of it", Description: "d", ProposedBy: parent.ID,
				AutoMerge: true, HoldForApproval: tc.hold,
			})
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != tc.want {
				t.Errorf("state = %s, want %s", filed.State, tc.want)
			}
			if tc.reason == "" && filed.ApprovalNote != "" {
				t.Errorf("approvalNote = %q on a queued proposal, want none", filed.ApprovalNote)
			}
			if !strings.Contains(filed.ApprovalNote, tc.reason) {
				t.Errorf("approvalNote = %q, want it to say %q", filed.ApprovalNote, tc.reason)
			}
			stored := mustGetTask(t, ctx, store, filed.ID)
			if got := model.HeldForApproval(stored); got != tc.heldTag {
				t.Errorf("held tag = %v, want %v -- only a proposal the permission would have "+
					"queued was held back by asking", got, tc.heldTag)
			}
			wantHeldBy := ""
			if tc.heldTag {
				wantHeldBy = parent.ID
				if !strings.Contains(filed.Description, "hold_for_approval") {
					t.Errorf("body = %q, want a note saying it was held on purpose", filed.Description)
				}
			}
			if filed.HeldBy != wantHeldBy {
				t.Errorf("heldBy = %q, want %q", filed.HeldBy, wantHeldBy)
			}
			if tc.hold && stored.Approval != nil {
				t.Errorf("approved by %+v, want nobody", stored.Approval)
			}
		})
	}
}

// The director's case (grain/task-439): a session its deployment lets
// queue its own work files one proposal that runs on its own and one
// that waits, in the same conversation.
func TestSessionMayHoldOneProposalAndQueueAnother(t *testing.T) {
	client, store, ctx := testClient(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{
		{ID: model.DirectorSessionID, Title: "Director", TasksApproved: true},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Director", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, session.ID)
	stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
	if err := store.PutTask(ctx, stored); err != nil {
		t.Fatal(err)
	}

	queued, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "runs on its own", Description: "d", ProposedBy: session.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "waits for the operator", Description: "d", ProposedBy: session.ID, HoldForApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.State != model.StateQueued {
		t.Errorf("unheld state = %s, want queued", queued.State)
	}
	if held.State != model.StateProposed {
		t.Errorf("held state = %s, want proposed", held.State)
	}
	if held.HeldBy != session.ID {
		t.Errorf("heldBy = %q, want %q", held.HeldBy, session.ID)
	}
	if !strings.Contains(held.ApprovalNote, "would otherwise have queued it") {
		t.Errorf("approvalNote = %q, want it to say the permission would have queued it", held.ApprovalNote)
	}
}

// Held composes with the rest of a proposal's arguments: its
// dependencies are still filed, the task it supersedes is still closed,
// and a not_before still defers it -- waking it back into proposed.
func TestHoldForApprovalComposesWithDependsOnSupersedesAndNotBefore(t *testing.T) {
	client, _, ctx := testClient(t)
	parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the whole job", Approved: true, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the old idea", Description: "d", ProposedBy: parent.ID, HoldForApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the new idea", Description: "d", ProposedBy: parent.ID, HoldForApproval: true,
		DependsOn: []string{parent.ID}, Supersedes: stale.ID, DeferredUntil: &until,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filed.State != model.StateDeferred {
		t.Errorf("state = %s, want deferred", filed.State)
	}
	if filed.Approved {
		t.Error("approved = true on a held proposal; it would wake onto the queue")
	}
	if filed.Supersedes != stale.ID {
		t.Errorf("supersedes = %q, want %q", filed.Supersedes, stale.ID)
	}
	if !slices.Contains(filed.DependsOn, parent.ID) {
		t.Errorf("dependsOn = %v, want %s", filed.DependsOn, parent.ID)
	}
	if filed.HeldBy != parent.ID {
		t.Errorf("heldBy = %q, want %q", filed.HeldBy, parent.ID)
	}
	old, err := client.Task(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.State != model.StateClosed {
		t.Errorf("superseded task state = %s, want closed", old.State)
	}
}

// A proposal held for approval and filed against a repo off the target
// repo list is parked awaiting a reply as well as unapproved
// (grain/task-2050). The park's comment asks for a reply once the list
// is widened, and that reply lifts the park only: it is not an approval,
// so the task goes back to proposed rather than onto the queue.
func TestReplyToAHeldProposalParkedOffTheTargetRepoListDoesNotApproveIt(t *testing.T) {
	client, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.ConsoleSessions = []model.ConsoleSession{
			{ID: model.DirectorSessionID, Title: "Director", TasksApproved: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Director", Interactive: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, session.ID)
	stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
	if err := store.PutTask(ctx, stored); err != nil {
		t.Fatal(err)
	}

	held, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "publish the skill", Description: "d", Repo: "sklodowska-dev/skill-publisher",
		ProposedBy: session.ID, HoldForApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.State != model.StateAwaitingReply || held.Approved {
		t.Fatalf("filed %s, approved %v; want awaiting_reply, unapproved", held.State, held.Approved)
	}
	if !strings.Contains(held.ParkNote, "not on this deployment's target repo list") {
		t.Errorf("parkNote = %q, want the park's own comment", held.ParkNote)
	}
	if held.ApprovalNote == "" {
		t.Error("approvalNote is empty, want why it waits for a person")
	}

	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "sklodowska-dev/skill-publisher"})
	if err := client.AddComment(ctx, held.ID, "added it to the list", nil); err != nil {
		t.Fatal(err)
	}
	replied, err := client.Task(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replied.State != model.StateProposed || replied.Approved {
		t.Fatalf("after a reply: %s, approved %v; want proposed, unapproved -- a reply is not an approval",
			replied.State, replied.Approved)
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ready {
		if id == held.ID {
			t.Fatal("the held proposal is ready for dispatch after a reply, want it waiting for an approval")
		}
	}
}

// keep_running and a priority above the proposer's are settings only a
// person may give a task: an auto-merge job's proposal asking for either
// is filed proposed, saying why, where the same proposal without them
// queues. A priority at or below the proposer's own still queues.
func TestProposalWithKeepRunningOrHigherPriorityWaitsForAPerson(t *testing.T) {
	prio := func(n int) *int { return &n }
	for _, tc := range []struct {
		name        string
		keepRunning bool
		priority    *int
		want        model.State
		reason      string
	}{
		{"keep running", true, nil, model.StateProposed, "keep_running"},
		{"priority above the proposer's", false, prio(1000000), model.StateProposed, "priority 1000000"},
		{"priority equal to the proposer's", false, prio(7), model.StateQueued, ""},
		{"priority below the proposer's", false, prio(2), model.StateQueued, ""},
		{"default priority", false, nil, model.StateQueued, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, store, ctx := testClient(t)
			parent, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "the whole job", Approved: true, AutoMerge: true, Priority: prio(7),
			})
			if err != nil {
				t.Fatal(err)
			}
			filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
				Title: "a piece of it", Description: "d", ProposedBy: parent.ID,
				AutoMerge: true, KeepRunning: tc.keepRunning, Priority: tc.priority,
			})
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != tc.want {
				t.Errorf("state = %s, want %s", filed.State, tc.want)
			}
			stored := mustGetTask(t, ctx, store, filed.ID)
			if stored.KeepRunning != tc.keepRunning {
				t.Errorf("KeepRunning = %v, want %v as asked", stored.KeepRunning, tc.keepRunning)
			}
			if tc.priority != nil && stored.Priority != *tc.priority {
				t.Errorf("Priority = %d, want %d as asked", stored.Priority, *tc.priority)
			}
			if tc.reason == "" {
				if filed.ApprovalNote != "" {
					t.Errorf("approvalNote = %q on a queued proposal, want none", filed.ApprovalNote)
				}
				return
			}
			if !strings.Contains(filed.ApprovalNote, tc.reason) {
				t.Errorf("approvalNote = %q, want it to say %q", filed.ApprovalNote, tc.reason)
			}
			if !strings.Contains(filed.Description, "Filed for a person to approve") ||
				!strings.Contains(filed.Description, tc.reason) {
				t.Errorf("body = %q, want a note saying why it waits for a person", filed.Description)
			}
			if model.HeldForApproval(stored) {
				t.Error("held tag set, want none: the run did not choose to hold it")
			}
		})
	}
}

// A person filing a task may give it either setting and have it queued.
func TestPersonMayFileKeepRunningAtAnyPriority(t *testing.T) {
	client, store, ctx := testClient(t)
	n := 1000000
	filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "forever", Description: "d", Approved: true, KeepRunning: true, Priority: &n,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filed.State != model.StateQueued {
		t.Errorf("state = %s, want queued", filed.State)
	}
	if stored := mustGetTask(t, ctx, store, filed.ID); !stored.KeepRunning || stored.Priority != n {
		t.Errorf("KeepRunning = %v, Priority = %d; want true, %d", stored.KeepRunning, stored.Priority, n)
	}
}
