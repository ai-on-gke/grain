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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// queueingDirector stores a director this deployment lets queue its own
// work (TasksApproved), holding grain/config in its Writes as the shipped
// director does for propose_config_change, and files its task.
func queueingDirector(t *testing.T, client *ui.Client, store *model.Store, ctx context.Context) model.Task {
	t.Helper()
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID: model.DirectorSessionID, Title: "Director", TasksApproved: true,
		Writes: []model.WriteTarget{{Repo: model.ConfigRepo()}},
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Director", Interactive: true, NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustGetTask(t, ctx, store, session.ID)
	stored.Tags = append(stored.Tags, model.ConsoleSessionTag(model.DirectorSessionID))
	stored.Writes = []model.WriteTarget{{Repo: model.ConfigRepo()}}
	if err := store.PutTask(ctx, stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// A TasksApproved session holding grain/config for propose_config_change
// still cannot queue a child that targets or writes grain/config: it is
// filed for a person, saying why, while the session's other proposals
// queue as before (director-capabilities spec §3.1.3, gap G5).
func TestSessionHoldingConfigWritesCannotQueueConfigRepoProposals(t *testing.T) {
	client, store, ctx := testClient(t)
	director := queueingDirector(t, client, store, ctx)

	plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "look into it", Description: "d", ProposedBy: director.ID, NoRepo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.State != model.StateQueued {
		t.Fatalf("a repo-less proposal from a queueing session is %s, want queued -- the test below would prove nothing", plain.State)
	}

	for _, tc := range []struct {
		name string
		req  ui.CreateTaskRequest
		want string
	}{
		{"writes grain/config", ui.CreateTaskRequest{
			Title: "push a config change", Description: "d", ProposedBy: director.ID, NoRepo: true,
			Writes: []ui.WriteTarget{{Repo: model.ConfigRepo().String()}},
		}, "it writes grain/config"},
		{"targets grain/config", ui.CreateTaskRequest{
			Title: "edit the settings", Description: "d", ProposedBy: director.ID,
			Repo: model.ConfigRepo().String(),
		}, "it targets grain/config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filed, err := client.CreateTask(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if filed.State != model.StateProposed {
				t.Errorf("state = %s, want proposed", filed.State)
			}
			if !strings.Contains(filed.ApprovalNote, tc.want) {
				t.Errorf("approvalNote = %q, want it to say %q", filed.ApprovalNote, tc.want)
			}
			if !strings.Contains(filed.Description, "always waits for a person") {
				t.Errorf("body = %q, want a note saying it waits for a person", filed.Description)
			}
			stored := mustGetTask(t, ctx, store, filed.ID)
			if stored.Approval != nil {
				t.Errorf("approved by %+v, want nobody", stored.Approval)
			}
			if model.HeldForApproval(stored) {
				t.Error("tagged held-for-approval: grain held it, the run did not ask")
			}
		})
	}
}

// A session's own permission is bounded per conversation turn (gap
// G12): past model.DefaultConsoleSessionTurnFanOut proposals since the
// last message a person sent it, the rest are filed for a person with a
// note; the next message starts a fresh turn.
func TestSessionQueuedProposalsAreBoundedPerTurn(t *testing.T) {
	client, store, ctx := testClient(t)
	now := baseTime
	client.Now = func() time.Time { return now }
	director := queueingDirector(t, client, store, ctx)

	ask := func() {
		t.Helper()
		now = now.Add(time.Minute)
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: director.ID, Body: "file the pieces",
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	propose := func(title string) ui.Task {
		t.Helper()
		now = now.Add(time.Second)
		filed, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: title, Description: "d", ProposedBy: director.ID, NoRepo: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return filed
	}

	ask()
	for i := range model.DefaultConsoleSessionTurnFanOut {
		if filed := propose(fmt.Sprintf("piece %d", i)); filed.State != model.StateQueued {
			t.Fatalf("piece %d is %s, want queued within the turn's bound", i, filed.State)
		}
	}
	over := propose("one too many")
	if over.State != model.StateProposed {
		t.Errorf("proposal past the turn's bound is %s, want proposed", over.State)
	}
	if !strings.Contains(over.Description, "since the last message a person sent it") {
		t.Errorf("body = %q, want the turn limit note", over.Description)
	}
	if !strings.Contains(over.ApprovalNote, "limit") {
		t.Errorf("approvalNote = %q, want it to say a limit was reached", over.ApprovalNote)
	}
	if stored := mustGetTask(t, ctx, store, over.ID); stored.Approval != nil {
		t.Errorf("approved by %+v, want nobody", stored.Approval)
	}

	ask()
	if filed := propose("next turn"); filed.State != model.StateQueued {
		t.Errorf("first proposal of a new turn is %s, want queued", filed.State)
	}
}
