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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTeamBroadcastByAPersonKeepsTheirName(t *testing.T) {
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Deployment work", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if rec := do(t, srv, "POST", "/api/deployment/broadcast", `{"message":"freeze","author":"alice"}`); rec.Code != http.StatusOK {
		t.Fatalf("broadcast: code %d, body: %s", rec.Code, rec.Body)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != "freeze" ||
		comments[0].Author != (model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}) {
		t.Errorf("comments = %+v, want alice's unprefixed broadcast", comments)
	}
}

func TestBroadcastTeamMessageRunningOnly(t *testing.T) {
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)

	running, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Running work", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{ID: dispatch.RunID(running.ID, 1), TaskID: running.ID, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatalf("StartRun running: %v", err)
	}

	queued, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Queued work", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask queued: %v", err)
	}

	parked, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Parked work", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask parked: %v", err)
	}
	completeForAck(t, store, ctx, parked.ID, model.StateAwaitingReply)

	sessions, err := client.CreateConsoleSession(ctx, ui.CreateConsoleSessionRequest{
		ID:    "ops",
		Title: "Ops",
	})
	if err != nil {
		t.Fatalf("CreateConsoleSession: %v", err)
	}
	var opsTaskID string
	for _, s := range sessions {
		if s.ID == "ops" {
			opsTaskID = s.TaskID
			break
		}
	}
	if opsTaskID == "" {
		t.Fatalf("ops console session task not created: %+v", sessions)
	}
	if err := store.StartRun(ctx, model.Run{ID: dispatch.RunID(opsTaskID, 1), TaskID: opsTaskID, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatalf("StartRun console: %v", err)
	}

	rec := do(t, srv, "POST", "/api/deployment/broadcast", `{"message":"Hold off on pushing","running_only":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("broadcast running_only: code %d, body: %s", rec.Code, rec.Body)
	}
	var res ui.BroadcastTeamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res.DeliveredCount != 1 || len(res.TaskIDs) != 1 || res.TaskIDs[0] != running.ID {
		t.Fatalf("response = %+v, want delivered only to running task %s", res, running.ID)
	}

	// Running agents poll MessagesSince via daemonAddenda on every tool call.
	msgs, err := client.MessagesSince(ctx, running.ID, 0)
	if err != nil {
		t.Fatalf("MessagesSince: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "Hold off on pushing" {
		t.Errorf("MessagesSince(%s) = %+v, want notice", running.ID, msgs)
	}

	for _, untouched := range []string{queued.ID, parked.ID, opsTaskID} {
		humanMsgs, err := client.MessagesSince(ctx, untouched, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(humanMsgs) != 0 {
			t.Errorf("task %s MessagesSince = %+v, want none", untouched, humanMsgs)
		}
	}
	if st, err := store.State(ctx, parked.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("parked task state = %q, err = %v; want %q", st, err, model.StateAwaitingReply)
	}
}

// actingAs is ctx carrying user as its verified human principal -- the
// only way a caller is anybody for a team or system-admin check. A
// "user" a request names is ignored (grain/task-2078).
func actingAs(ctx context.Context, user string) context.Context {
	return ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: user})
}
