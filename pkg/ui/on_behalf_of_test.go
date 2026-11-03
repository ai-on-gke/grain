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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The on-behalf-of an agent's writes carry (operator-attribution spec
// §2.4, §3.5, §3.6): grain derives it from its own records -- the person
// a console session's turn is answering, the person who approved a
// dispatched run's task -- and nothing the agent sends can set it.

const behalfSession = "77"

var (
	aliceIAP    = model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}
	bobIAP      = model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com", Source: model.SourceIAP}
	sessionSelf = model.Principal{Kind: model.PrincipalAgent, ID: behalfSession, Source: model.SourceSession}
)

// behalfServer is a single-tenant server whose IAP admits alice and bob,
// with the director's session task holding the api grants its writes
// below need. send issues a request the way the person or agent named
// does it.
type behalfServer struct {
	srv    *ui.Server
	client *ui.Client
	send   func(t *testing.T, as, method, path, body string, extra map[string]string) *httptest.ResponseRecorder
}

func newBehalfServer(t *testing.T) behalfServer {
	t.Helper()
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	client, _, _ := testClient(t)
	client.Config.LocalAPIToken = testAccessCode
	client.Config.AllowedPrincipals = []string{"user:alice@example.com", "user:bob@example.com"}
	srv := ui.NewServerWithClient(client)

	session := model.Task{
		ID:          behalfSession,
		Intent:      model.IntentAnalyze,
		Title:       "director",
		Origin:      model.Origin{Attribution: model.Attribution{Actor: model.ConsoleSessionPrincipal}, Reason: model.ReasonConsoleSession},
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Interactive: true,
		Approval:    &model.Attribution{Actor: model.ConsoleSessionPrincipal},
		Grants:      []model.Grant{{Capability: "api:tasks:write", Via: model.GrantByLabel}},
	}
	if err := client.Store.PutTask(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	send := func(t *testing.T, as, method, path, body string, extra map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		h := map[string]string{"Content-Type": "application/json"}
		remote := ""
		switch as {
		case "alice@example.com", "bob@example.com":
			exp := time.Now().Add(5 * time.Minute)
			h[ui.IAPAssertionHeader] = signIAPToken(t, priv, tunnelKid, tunnelAudience, as, "s", ui.GoogleIAPIssuer, exp)
			remote = "203.0.113.20:443"
		case "access code":
			h["Authorization"] = "Bearer " + testAccessCode
			remote = "198.51.100.7:4242"
		default: // an agent: the task it acts as
			h[ui.ActingTaskHeader] = as
			h[ui.ActingReasonHeader] = "on-behalf-of"
		}
		for k, v := range extra {
			h[k] = v
		}
		var rec *httptest.ResponseRecorder
		if remote == "" {
			rec = doWithHeaders(t, srv, method, path, body, h)
		} else {
			rec = serveFrom(srv, remote, method, path, body, h)
		}
		if rec.Code >= 300 {
			t.Fatalf("%s %s as %s: %d %s", method, path, as, rec.Code, rec.Body)
		}
		return rec
	}
	return behalfServer{srv: srv, client: client, send: send}
}

// say posts body to the director's session as person, the way the chat
// pane does.
func (b behalfServer) say(t *testing.T, person, body string) {
	t.Helper()
	b.send(t, person, http.MethodPost, "/api/tasks/"+behalfSession+"/comments", `{"body":`+mustJSON(t, body)+`}`, nil)
}

// target is a task for the agent's writes to land on.
func (b behalfServer) target(t *testing.T) string {
	t.Helper()
	task, err := b.client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "target", Approved: true, NoRepo: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

// newestAuthor is the attribution of the newest comment on id that is not
// grain's own record of an agent's call.
func newestAuthor(t *testing.T, client *ui.Client, id string) model.Attribution {
	t.Helper()
	comments, err := client.Store.Comments(context.Background(), id)
	if err != nil {
		t.Fatalf("comments on %s: %v", id, err)
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].Author.Actor.Kind != model.PrincipalAutomation {
			return comments[i].Author
		}
	}
	t.Fatalf("no comment on %s: %+v", id, comments)
	return model.Attribution{}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func behalfOf(a model.Attribution) model.Principal {
	if a.OnBehalfOf == nil {
		return model.Principal{}
	}
	return *a.OnBehalfOf
}

// Spec §7.2's agent row: the agent API gate, on a console session whose
// last message is alice's, records {agent, <session>, session} on behalf
// of {human, alice@example.com, iap} -- on a comment, a close, a filed
// task, an approval and a configuration write -- and the task view says
// "director for alice@example.com".
func TestConsoleSessionGateActsForTheLastPersonWhoSpoke(t *testing.T) {
	b := newBehalfServer(t)
	ctx := context.Background()
	b.say(t, "bob@example.com", "an earlier word")
	b.say(t, "alice@example.com", "please look at the target")
	if got := newestAuthor(t, b.client, behalfSession).Actor; got != aliceIAP {
		t.Fatalf("alice's message was recorded as %+v, want %+v", got, aliceIAP)
	}
	want := model.Attribution{Actor: sessionSelf, OnBehalfOf: &aliceIAP}

	id := b.target(t)
	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"on it"}`, nil)
	if got := newestAuthor(t, b.client, id); got.Actor != want.Actor || behalfOf(got) != aliceIAP {
		t.Errorf("the session's comment was recorded as %+v for %+v, want %+v for %+v", got.Actor, got.OnBehalfOf, want.Actor, aliceIAP)
	}

	rec := b.send(t, behalfSession, http.MethodPost, "/api/tasks", `{"title":"filed for alice","noRepo":true}`, nil)
	var filed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filed); err != nil || filed.ID == "" {
		t.Fatalf("filing a task: %v, %s", err, rec.Body)
	}
	task, err := b.client.Store.GetTask(ctx, filed.ID)
	if err != nil || task == nil {
		t.Fatalf("the filed task: %v, %v", task, err)
	}
	if got := task.Origin.Attribution; got.Actor != want.Actor || behalfOf(got) != aliceIAP {
		t.Errorf("the filed task's origin is %+v for %+v, want %+v for %+v", got.Actor, got.OnBehalfOf, want.Actor, aliceIAP)
	}

	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/close", ``, nil)
	events, err := b.client.Store.TaskEvents(ctx, id)
	if err != nil || len(events) == 0 {
		t.Fatalf("task events of %s: %v, %v", id, events, err)
	}
	if e := events[len(events)-1]; e.By != want.Actor || e.OnBehalfOf == nil || *e.OnBehalfOf != aliceIAP {
		t.Errorf("the close was recorded as %+v for %+v, want %+v for %+v", e.By, e.OnBehalfOf, want.Actor, aliceIAP)
	}

	// What a person reads: the session by its title, for alice.
	rec = b.send(t, "alice@example.com", http.MethodGet, "/api/tasks/"+id, ``, nil)
	var detail struct {
		Comments []struct {
			AuthorLabel     string `json:"authorLabel"`
			OnBehalfOfLabel string `json:"onBehalfOfLabel"`
		} `json:"comments"`
		Transitions []struct {
			ActorLabel string `json:"actorLabel"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decoding task %s: %v", id, err)
	}
	var commented, closed bool
	for _, c := range detail.Comments {
		if c.AuthorLabel == "director for alice@example.com" && c.OnBehalfOfLabel == "alice@example.com" {
			commented = true
		}
	}
	for _, tr := range detail.Transitions {
		closed = closed || tr.ActorLabel == "director for alice@example.com"
	}
	if !commented || !closed {
		t.Errorf("task %s reads %s, want its comment and its close by \"director for alice@example.com\"", id, rec.Body)
	}
}

// An agent cannot say who it acts for (spec §6.2): a body or a header
// naming somebody is ignored, and the on-behalf-of is still the person
// grain's records name -- bob, whose message the session is answering.
func TestAnAgentCannotClaimAnOnBehalfOf(t *testing.T) {
	b := newBehalfServer(t)
	b.say(t, "bob@example.com", "close the loop")
	id := b.target(t)
	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/comments",
		`{"body":"done","onBehalfOf":"alice@example.com","onBehalfOfKind":"human"}`,
		map[string]string{
			"X-Grain-On-Behalf-Of": "alice@example.com",
			"X-Grain-User":         "alice",
		})
	got := newestAuthor(t, b.client, id)
	if got.Actor != sessionSelf || behalfOf(got) != bobIAP {
		t.Errorf("the session's comment was recorded as %+v for %+v, want %+v for %+v", got.Actor, got.OnBehalfOf, sessionSelf, bobIAP)
	}

	// Naming a person as the author outright is refused (spec §2.3).
	if rec := doWithHeaders(t, b.srv, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"as alice","author":"alice@example.com"}`,
		map[string]string{ui.ActingTaskHeader: behalfSession, "Content-Type": "application/json"}); rec.Code < 400 {
		t.Errorf("an agent naming alice as its comment's author: %d %s, want it refused", rec.Code, rec.Body)
	}

	// A dispatched run nobody approved acts for nobody, whatever it says.
	seedAPITask(t, b.client, "901", "acme/widgets", "api:tasks:write")
	b.send(t, "901", http.MethodPost, "/api/tasks/"+id+"/comments",
		`{"body":"also done","onBehalfOf":"alice@example.com"}`,
		map[string]string{"X-Grain-On-Behalf-Of": "alice@example.com"})
	got = newestAuthor(t, b.client, id)
	if want := (model.Principal{Kind: model.PrincipalAgent, ID: "901", Source: model.SourceSandbox}); got.Actor != want || got.OnBehalfOf != nil {
		t.Errorf("the run's comment was recorded as %+v for %+v, want %+v for nobody", got.Actor, got.OnBehalfOf, want)
	}
}

// The access code is shared, so a message sent with it names nobody
// (spec §6.3): it is recorded as {human, operator, access-code}, and the
// turn answering it has no on-behalf-of -- even though alice spoke
// before it.
func TestAnAccessCodeMessageGivesNoOnBehalfOf(t *testing.T) {
	b := newBehalfServer(t)
	b.say(t, "alice@example.com", "first")
	b.say(t, "access code", "second, from the shared code")
	if got, want := newestAuthor(t, b.client, behalfSession).Actor, (model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceAccessCode}); got.Kind != want.Kind || got.Source != want.Source {
		t.Fatalf("the access code's message was recorded as %+v, want a human with source %s", got, want.Source)
	}
	id := b.target(t)
	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"on it"}`, nil)
	if got := newestAuthor(t, b.client, id); got.Actor != sessionSelf || got.OnBehalfOf != nil {
		t.Errorf("the session's comment was recorded as %+v for %+v, want %+v for nobody", got.Actor, got.OnBehalfOf, sessionSelf)
	}
}

// Several people talking to one session: a live turn acts for the most
// recent person's message it has been shown, not for one that arrived
// while it was answering.
func TestASessionTurnActsForTheMessageItIsAnswering(t *testing.T) {
	b := newBehalfServer(t)
	ctx := context.Background()
	b.say(t, "alice@example.com", "what about the target?")
	runStart := time.Now().Add(time.Second)
	if err := b.client.Store.StartRun(ctx, model.Run{ID: "run-77", TaskID: behalfSession, Attempt: 1, StartedAt: runStart}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	comments, err := b.client.Store.Comments(ctx, behalfSession)
	if err != nil {
		t.Fatal(err)
	}
	// bob writes while the turn is answering alice, after the run started.
	if _, err := b.client.Store.AddComment(ctx, model.Comment{
		TaskID: behalfSession, Author: model.Attribution{Actor: bobIAP}, Body: "and me", CreatedAt: runStart.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	id := b.target(t)
	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"for alice"}`, nil)
	if got := newestAuthor(t, b.client, id); behalfOf(got) != aliceIAP {
		t.Errorf("the turn answering alice was recorded for %+v, want %+v", got.OnBehalfOf, aliceIAP)
	}

	// Once bob's message is handed to the turn, the turn is answering him.
	all, err := b.client.Store.Comments(ctx, behalfSession)
	if err != nil || len(all) <= len(comments) {
		t.Fatalf("session comments: %v, %v", all, err)
	}
	if err := b.client.Store.AdvanceDelivered(ctx, behalfSession, all[len(all)-1].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	b.send(t, behalfSession, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"for bob"}`, nil)
	if got := newestAuthor(t, b.client, id); behalfOf(got) != bobIAP {
		t.Errorf("the turn shown bob's message was recorded for %+v, want %+v", got.OnBehalfOf, bobIAP)
	}
}

// A dispatched run acts for the verified person who approved its task,
// and for nobody when grain or the shared access code approved it.
func TestADispatchedRunActsForItsApprover(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approval *model.Attribution
		want     *model.Principal
	}{
		{"approved by alice", &model.Attribution{Actor: aliceIAP}, &aliceIAP},
		{"approved by the access code", &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}}, nil},
		{"approved by grain", &model.Attribution{Actor: model.AutoSubmitPrincipal}, nil},
		{"not approved", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBehalfServer(t)
			ref, _ := model.ParseRepo("acme/widgets")
			run := model.Task{ID: "902", Intent: model.IntentImplement, Title: "a run", Target: &ref, Approval: tc.approval,
				Grants: []model.Grant{{Capability: "api:tasks:write", Via: model.GrantByLabel}}}
			if err := b.client.Store.PutTask(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			id := b.target(t)
			b.send(t, "902", http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"a word"}`, nil)
			got := newestAuthor(t, b.client, id)
			if want := (model.Principal{Kind: model.PrincipalAgent, ID: "902", Source: model.SourceSandbox}); got.Actor != want {
				t.Errorf("the run's comment was by %+v, want %+v", got.Actor, want)
			}
			switch {
			case tc.want == nil && got.OnBehalfOf != nil:
				t.Errorf("the run's comment was for %+v, want nobody", *got.OnBehalfOf)
			case tc.want != nil && behalfOf(got) != *tc.want:
				t.Errorf("the run's comment was for %+v, want %+v", got.OnBehalfOf, *tc.want)
			}
		})
	}
}
