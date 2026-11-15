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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A person's answer that a director or console session relays onto
// another task (task 2588, answered in director chat 2578 and left
// waiting for a week): relay: true writes it as the person's, via the
// session, and wakes the task as their own reply would; anything else an
// agent says there is recorded and says it did not count.

// parkedOnAQuestion is target as a run that called ask_question leaves
// it: the question relayed as a comment on the run's behalf, the task
// parked on it, and the run having seen everything up to it.
func parkedOnAQuestion(t *testing.T, b behalfServer) string {
	t.Helper()
	ctx := context.Background()
	id := b.target(t)
	commentID, err := b.client.Store.AddComment(ctx, model.Comment{
		TaskID: id,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: id},
		},
		Body:      "Use the staging key or the production one?",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("relaying the question: %v", err)
	}
	if err := b.client.Store.ObserveField(ctx, id, time.Now(), func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
		o.DeliveredCommentID = &commentID
	}); err != nil {
		t.Fatalf("parking the task: %v", err)
	}
	if st := taskState(t, b, id); st != model.StateAwaitingReply {
		t.Fatalf("the parked task is %s, want awaiting_reply", st)
	}
	return id
}

func taskState(t *testing.T, b behalfServer, id string) model.State {
	t.Helper()
	st, err := b.client.Store.State(context.Background(), id)
	if err != nil {
		t.Fatalf("state of %s: %v", id, err)
	}
	return st
}

// agentPost is a POST an agent acting as task as makes, answered whatever
// its status.
func agentPost(t *testing.T, b behalfServer, as, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHeaders(t, b.srv, http.MethodPost, path, body, map[string]string{
		ui.ActingTaskHeader: as,
		"Content-Type":      "application/json",
	})
}

type relayCommentAnswer struct {
	State           model.State `json:"state"`
	Relayed         bool        `json:"relayed"`
	CommentDelivery string      `json:"commentDelivery"`
	Notice          string      `json:"notice"`
}

func decodeRelayAnswer(t *testing.T, rec *httptest.ResponseRecorder) relayCommentAnswer {
	t.Helper()
	var ans relayCommentAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body, err)
	}
	return ans
}

func TestASessionRelayingAPersonsAnswerWakesTheTask(t *testing.T) {
	b := newBehalfServer(t)
	ctx := context.Background()
	id := parkedOnAQuestion(t, b)
	b.say(t, "alice@example.com", "tell task "+id+" to use the staging key")

	rec := agentPost(t, b, behalfSession, "/api/tasks/"+id+"/comments", `{"body":"Use the staging key.","relay":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("relay: %d %s", rec.Code, rec.Body)
	}
	ans := decodeRelayAnswer(t, rec)
	if !ans.Relayed || ans.CommentDelivery != "" {
		t.Errorf("relay answered %+v, want relayed with no delivery notice", ans)
	}
	if ans.State == model.StateAwaitingReply {
		t.Errorf("the task is still %s after the relayed answer, want it back in the queue", ans.State)
	}
	obs, err := b.client.Store.GetObservation(ctx, id)
	if err != nil || obs == nil {
		t.Fatalf("observation: %v, %v", obs, err)
	}
	if obs.PendingQuestionCommentID != nil || obs.CompletedAt != nil {
		t.Errorf("the question is still pending (%v) or the task still completed (%v) after the relay", obs.PendingQuestionCommentID, obs.CompletedAt)
	}

	// Recorded as alice's, via the session: her answer, counted as hers.
	got := newestAuthor(t, b.client, id)
	if got.Actor != aliceIAP || behalfOf(got) != sessionSelf {
		t.Errorf("the relayed answer was recorded as %+v via %+v, want %+v via %+v", got.Actor, got.OnBehalfOf, aliceIAP, sessionSelf)
	}
	if model.PersonOf(got) == nil || model.RelayedVia(got) == nil {
		t.Errorf("the relayed answer does not read as alice's relayed answer: PersonOf=%v RelayedVia=%v", model.PersonOf(got), model.RelayedVia(got))
	}
	detail := b.send(t, "alice@example.com", http.MethodGet, "/api/tasks/"+id, ``, nil)
	var view struct {
		Comments []struct {
			AuthorLabel string `json:"authorLabel"`
			OnBehalfOf  string `json:"onBehalfOf"`
			RelayedVia  string `json:"relayedVia"`
			Body        string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &view); err != nil || len(view.Comments) == 0 {
		t.Fatalf("decoding task %s: %v, %s", id, err, detail.Body)
	}
	var last struct {
		AuthorLabel string `json:"authorLabel"`
		OnBehalfOf  string `json:"onBehalfOf"`
		RelayedVia  string `json:"relayedVia"`
		Body        string `json:"body"`
	}
	for _, c := range view.Comments {
		if c.Body == "Use the staging key." {
			last = c
		}
	}
	if last.AuthorLabel != "alice@example.com via director" || last.RelayedVia != behalfSession || last.OnBehalfOf != "" {
		t.Errorf("the relayed answer reads %+v, want \"alice@example.com via director\" relayed via %s", last, behalfSession)
	}
}

func TestAnAgentsCommentOnATaskWaitingForAPersonSaysItDidNotCount(t *testing.T) {
	b := newBehalfServer(t)
	id := parkedOnAQuestion(t, b)
	b.say(t, "alice@example.com", "tell task "+id+" to use the staging key")

	// The session's plain comment -- what director 2578 posted on 2588.
	rec := agentPost(t, b, behalfSession, "/api/tasks/"+id+"/comments", `{"body":"Operator's answers, relayed: use the staging key."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body)
	}
	ans := decodeRelayAnswer(t, rec)
	if ans.Relayed || ans.State != model.StateAwaitingReply || ans.CommentDelivery != ui.CommentAwaitingPerson ||
		!strings.Contains(ans.Notice, "task "+id+" is still waiting for a person's reply") {
		t.Errorf("an agent's comment answered %+v, want it recorded with task %s still awaiting a person", ans, id)
	}
	if st := taskState(t, b, id); st != model.StateAwaitingReply {
		t.Errorf("the task is %s after an agent's comment, want it still awaiting_reply", st)
	}

	// The task page says so.
	detail := b.send(t, "alice@example.com", http.MethodGet, "/api/tasks/"+id, ``, nil)
	var view struct {
		UncountedAgentReplyFrom string `json:"uncountedAgentReplyFrom"`
		UncountedAgentReplyID   int64  `json:"uncountedAgentReplyId"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.UncountedAgentReplyFrom != "director for alice@example.com" || view.UncountedAgentReplyID == 0 {
		t.Errorf("the task page reads %+v, want the director's uncounted reply named", view)
	}

	// A person's reply then counts, and the notice goes.
	b.send(t, "alice@example.com", http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"staging"}`, nil)
	if st := taskState(t, b, id); st == model.StateAwaitingReply {
		t.Errorf("the task is still awaiting_reply after alice's own reply")
	}
}

func TestOnlyASessionActingForAPersonMayRelay(t *testing.T) {
	b := newBehalfServer(t)
	id := parkedOnAQuestion(t, b)

	// A session nobody has spoken to acts for nobody.
	if rec := agentPost(t, b, behalfSession, "/api/tasks/"+id+"/comments", `{"body":"yes","relay":true}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "acting for no signed-in person") {
		t.Errorf("a relay from a session acting for nobody: %d %s, want 403", rec.Code, rec.Body)
	}

	// An ordinary dispatched run, approved by a person, still cannot.
	seedAPITask(t, b.client, "901", "acme/widgets", "api:tasks:write")
	ctx := context.Background()
	run, err := b.client.Store.GetTask(ctx, "901")
	if err != nil || run == nil {
		t.Fatalf("task 901: %v, %v", run, err)
	}
	run.Approval = &model.Attribution{Actor: aliceIAP}
	if err := b.client.Store.PutTask(ctx, *run); err != nil {
		t.Fatal(err)
	}
	if rec := agentPost(t, b, "901", "/api/tasks/"+id+"/comments", `{"body":"yes","relay":true}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "not a console session") {
		t.Errorf("a relay from a dispatched run: %d %s, want 403", rec.Code, rec.Body)
	}
	if st := taskState(t, b, id); st != model.StateAwaitingReply {
		t.Errorf("a refused relay moved the task to %s", st)
	}
	comments, err := b.client.Store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if c.Body == "yes" {
			t.Errorf("a refused relay was written: %+v", c)
		}
	}
}

// The person must be one who could reply on the task themselves: a
// session is no way round their role, asked as it stands now.
func TestARelayIsHeldToThePersonsRole(t *testing.T) {
	b := newBehalfServer(t)
	ctx := context.Background()
	id := parkedOnAQuestion(t, b)
	b.say(t, "alice@example.com", "tell task "+id+" yes")
	if _, _, found, err := b.client.Store.SetAllowedPrincipalRole(ctx, "user:alice@example.com", "grain/viewer"); err != nil || !found {
		t.Fatalf("narrowing alice to grain/viewer: found=%v, %v", found, err)
	}

	rec := agentPost(t, b, behalfSession, "/api/tasks/"+id+"/comments", `{"body":"yes","relay":true}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "alice@example.com may not reply on task "+id) {
		t.Errorf("a relay for a viewer: %d %s, want 403 naming alice", rec.Code, rec.Body)
	}
	if st := taskState(t, b, id); st != model.StateAwaitingReply {
		t.Errorf("a refused relay moved the task to %s", st)
	}
}

// A session cannot relay onto its own conversation: the person is already
// talking to it there.
func TestASessionCannotRelayOntoItself(t *testing.T) {
	b := newBehalfServer(t)
	b.say(t, "alice@example.com", "hello")
	if rec := agentPost(t, b, behalfSession, "/api/tasks/"+behalfSession+"/comments", `{"body":"hi","relay":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a relay onto the session itself: %d %s, want 400", rec.Code, rec.Body)
	}
}
