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

// A person's comment on a closed task reaches no agent: the task stays
// closed (reopenAcknowledgeable). Reopening it is what delivers it -- the
// reopen finds the comment past Observation.DeliveredCommentID and queues
// the run that answers it -- and POST /api/tasks/{id}/comments says, on a
// closed task, that the comment was not delivered unless it was also
// asked to reopen (task 3803).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const otherPerson = "atyburcy@example.com"

// answeredThenClosed is task 3803's record up to the follow-up: a run that
// answered in a comment (the agent's own closing note, past the delivered
// mark the finished run left), and its owner closing the task.
func answeredThenClosed(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	completeForAck(t, store, ctx, id, model.StateAwaitingReply)
	done := baseTime.Add(time.Minute)
	if err := store.AdvanceDeliveredFrom(ctx, id, 0, done); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: id}},
		Body:      "Here is the answer.",
		CreatedAt: done,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, id, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, id); err != nil || state != model.StateClosed {
		t.Fatalf("state = %q, err = %v; want closed", state, err)
	}
}

func reopenDetail(t *testing.T, store *model.Store, ctx context.Context, id string) string {
	t.Helper()
	events, err := store.TaskEvents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == model.TaskEventReopened {
			return events[i].Detail
		}
	}
	t.Fatalf("no reopen event among %+v", events)
	return ""
}

func TestReopenQueuesACommentLeftWhileClosed(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	answeredThenClosed(t, c, store, ctx, task.ID)

	if err := c.AddCommentWithAuthor(ctx, task.ID, "Does this cover the staging cluster too?", nil, otherPerson); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateClosed {
		t.Fatalf("state = %q, err = %v; want closed still after the comment", state, err)
	}
	if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
		t.Fatalf("dispatched %v while closed, want nothing", ids)
	}

	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued by the reopen", state, err)
	}
	// What the run is given is the conversation past the delivered mark,
	// and the question is still past it.
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	question := comments[len(comments)-1]
	if question.Body != "Does this cover the staging cluster too?" || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID >= question.ID {
		t.Fatalf("question = %+v, DeliveredCommentID = %v; want the question still undelivered", question, obs.DeliveredCommentID)
	}
	if ids := dispatchedIDs(t, store, ctx); len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("dispatched %v, want a run of %s", ids, task.ID)
	}
	if got, want := reopenDetail(t, store, ctx, task.ID), "reopened with an unanswered comment from "+otherPerson+"; queued"; got != want {
		t.Fatalf("reopen detail = %q, want %q", got, want)
	}
}

func TestReopenWithNoNewCommentRestoresTheAnswer(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	answeredThenClosed(t, c, store, ctx, task.ID)
	before, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingReply {
		t.Fatalf("state = %q, err = %v; want awaiting_reply as before the close", state, err)
	}
	after, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CompletedAt == nil || !after.CompletedAt.Equal(*before.CompletedAt) || after.RetryRequestedAt != nil {
		t.Fatalf("CompletedAt = %v, RetryRequestedAt = %v; want the completion untouched", after.CompletedAt, after.RetryRequestedAt)
	}
	if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
		t.Fatalf("dispatched %v, want nothing", ids)
	}
	if got := reopenDetail(t, store, ctx, task.ID); got != "" {
		t.Fatalf("reopen detail = %q, want none", got)
	}
	task2, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task2.UnansweredCommentFrom != "" {
		t.Fatalf("UnansweredCommentFrom = %q, want none", task2.UnansweredCommentFrom)
	}
}

// What a run says on a closed task, and what grain says, is never a
// question the reopen owes a run.
func TestReopenIsNotQueuedByAnAgentsOrGrainsComment(t *testing.T) {
	t.Parallel()
	for name, by := range map[string]model.Principal{
		"agent":      {Kind: model.PrincipalAgent, ID: "run-1"},
		"automation": {Kind: model.PrincipalAutomation, ID: "grain"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			task := create(t, c, ctx)
			answeredThenClosed(t, c, store, ctx, task.ID)
			if err := c.AddComment(ui.WithPrincipal(ctx, by), task.ID, "a note", nil); err != nil {
				t.Fatal(err)
			}
			if err := c.Reopen(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingReply {
				t.Fatalf("state = %q, err = %v; want awaiting_reply", state, err)
			}
			if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
				t.Fatalf("dispatched %v, want nothing", ids)
			}
		})
	}
}

// A task with its own pull request keeps to its own flow when reopened,
// whatever was said on it while it was closed.
func TestReopenDoesNotQueueATaskWithAnOpenPullRequest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completeForAck(t, store, ctx, task.ID, model.StateAwaitingSubmit)
	if err := store.AdvanceDeliveredFrom(ctx, task.ID, 0, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddCommentWithAuthor(ctx, task.ID, "can you also rename it?", nil, otherPerson); err != nil {
		t.Fatal(err)
	}
	if err := c.Reopen(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, err = %v; want awaiting_submit still", state, err)
	}
	if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
		t.Fatalf("dispatched %v, want nothing", ids)
	}
}

// A comment from somebody other than the task's owner on an answered,
// open task is a "continue" like the owner's own.
func TestANonOwnersCommentQueuesAnAnsweredTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	completeForAck(t, store, ctx, task.ID, model.StateAwaitingReply)
	if err := c.AddCommentWithAuthor(ctx, task.ID, "and for staging?", nil, otherPerson); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued", state, err)
	}
}

// Where something did leave a person's comment undelivered on an
// answered task, its row names who is waiting rather than reading as a
// plain answer for its owner.
func TestAnAnsweredTaskWithAnUndeliveredCommentSaysWhose(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	completeForAck(t, store, ctx, task.ID, model.StateAwaitingReply)
	if err := store.AdvanceDeliveredFrom(ctx, task.ID, 0, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Written past the Client, as a path that never reached
	// reopenAcknowledgeable would.
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: otherPerson}},
		Body:      "and for staging?",
		CreatedAt: baseTime.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	one, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.UnansweredCommentFrom != otherPerson {
		t.Fatalf("Task.UnansweredCommentFrom = %q, want %q", one.UnansweredCommentFrom, otherPerson)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.UnansweredCommentFrom != otherPerson {
		t.Fatalf("TaskDetail.UnansweredCommentFrom = %q, want %q", detail.UnansweredCommentFrom, otherPerson)
	}
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list {
		if row.ID == task.ID && row.UnansweredCommentFrom != otherPerson {
			t.Fatalf("list row UnansweredCommentFrom = %q, want %q", row.UnansweredCommentFrom, otherPerson)
		}
	}
}

type commentAnswer struct {
	State           model.State `json:"state"`
	CommentDelivery string      `json:"commentDelivery"`
	Notice          string      `json:"notice"`
}

func postComment(t *testing.T, srv *ui.Server, id, body string) (int, commentAnswer, string) {
	t.Helper()
	rec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks/"+id+"/comments", body,
		map[string]string{"Content-Type": "application/json"})
	var got commentAnswer
	if rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, got, rec.Body.String()
}

func TestACommentOnAClosedTaskSaysItWasNotDelivered(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	task := create(t, c, ctx)
	answeredThenClosed(t, c, c.Store, ctx, task.ID)

	code, got, raw := postComment(t, srv, task.ID, `{"body":"one more question"}`)
	if code != http.StatusOK {
		t.Fatalf("POST comments = %d %s", code, raw)
	}
	if got.State != model.StateClosed || got.CommentDelivery != ui.CommentNotDelivered || !strings.Contains(got.Notice, "not delivered") {
		t.Fatalf("answer = %+v, want a closed task and a not-delivered notice", got)
	}
}

func TestReopenAndSendInOneCall(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	task := create(t, c, ctx)
	answeredThenClosed(t, c, c.Store, ctx, task.ID)

	code, got, raw := postComment(t, srv, task.ID, `{"body":"one more question","reopen":true}`)
	if code != http.StatusOK {
		t.Fatalf("POST comments = %d %s", code, raw)
	}
	if got.State != model.StateQueued || got.CommentDelivery != "" || got.Notice != "" {
		t.Fatalf("answer = %+v, want the task queued and no notice", got)
	}
	if ids := dispatchedIDs(t, c.Store, ctx); len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("dispatched %v, want a run of %s", ids, task.ID)
	}
	// On an open task reopen is nothing at all.
	code, got, raw = postComment(t, srv, task.ID, `{"body":"and another","reopen":true}`)
	if code != http.StatusOK || got.State == model.StateClosed || got.CommentDelivery != "" {
		t.Fatalf("POST comments on an open task = %d %+v %s", code, got, raw)
	}
}

// An agent's comment cannot carry a reopen: nothing an agent says sends a
// task to the queue, and a run may not reopen its own task.
func TestAnAgentsCommentCannotReopen(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := context.Background()
	task := create(t, c, ctx)
	answeredThenClosed(t, c, c.Store, ctx, task.ID)
	seedAPITask(t, c, "900", "acme/widgets", "api:tasks:write")

	if code, body := asAgent(t, srv, "900", http.MethodPost, "/api/tasks/"+task.ID+"/comments",
		`{"body":"reopening","reopen":true}`, "testing"); code != http.StatusForbidden {
		t.Fatalf("agent reopen-and-send = %d %s, want 403", code, body)
	}
	if state, err := c.Store.State(ctx, task.ID); err != nil || state != model.StateClosed {
		t.Fatalf("state = %q, err = %v; want closed still", state, err)
	}
	comments, err := c.Store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cm := range comments {
		if cm.Body == "reopening" {
			t.Fatalf("the refused comment was written: %+v", cm)
		}
	}
}
