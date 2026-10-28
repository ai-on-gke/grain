package ui_test

// One run talking to another task's agent: POST /api/tasks/{id}/messages
// is the chat tool's write half and only ever the caller's own task's,
// and POST /api/tasks/{id}/agent-messages is the route for the other
// thing -- signed as the sender, read back to the target's live run as an
// addendum, and leaving the target's state exactly as it was.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// What task 768 did to tasks 796, 657 and 736: /messages on somebody
// else's task. It is refused, the refusal names the route to use, and
// the target is neither parked nor written to.
func TestAgentAPIRefusesMessagesOnAnotherTask(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "768", "acme/widgets", "api:tasks:write")
	seedAPITask(t, client, "796", "acme/widgets")
	running(t, client.Store, ctx, "796")
	before, err := client.Store.State(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}

	code, body := asAgent(t, srv, "768", http.MethodPost, "/api/tasks/796/messages", `{"body":"try rebasing first"}`, "")
	if code < 400 || code >= 500 {
		t.Fatalf("/messages on another task answered %d, want a 4xx: %s", code, body)
	}
	if !strings.Contains(body, "/api/tasks/{id}/agent-messages") {
		t.Errorf("the refusal does not name the route to use instead: %s", body)
	}

	if after, err := client.Store.State(ctx, "796"); err != nil || after != before {
		t.Errorf("state = %v (%v), want %v as it was", after, err, before)
	}
	obs, err := client.Store.GetObservation(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Errorf("task 796 was parked: %+v", obs)
	}
	if comments, err := client.Store.Comments(ctx, "796"); err != nil || len(comments) != 0 {
		t.Errorf("comments = %+v (%v), want nothing written on task 796", comments, err)
	}
}

// The chat tool's own path: a run's /messages on its own task still
// parks it on the person watching.
func TestAgentAPIMessagesOnOwnTaskStillParks(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "5", "acme/widgets", "api:tasks:write")
	running(t, client.Store, ctx, "5")

	code, body := asAgent(t, srv, "5", http.MethodPost, "/api/tasks/5/messages", `{"body":"which did you mean?"}`, "")
	if code != http.StatusOK {
		t.Fatalf("/messages on the run's own task answered %d: %s", code, body)
	}
	var message ui.Message
	if err := json.Unmarshal([]byte(body), &message); err != nil {
		t.Fatal(err)
	}
	obs, err := client.Store.GetObservation(ctx, "5")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != message.ID {
		t.Fatalf("observation = %+v, want task 5 parked on its own question", obs)
	}
	if state, err := client.Store.State(ctx, "5"); err != nil || state != model.StateAwaitingReply {
		t.Errorf("state = %v (%v), want awaiting_reply", state, err)
	}
}

// The route that does what task 768 wanted: the message lands on the
// target's conversation signed as the sender, a live run's addenda read
// hands it over (and a chat turn's read does not), and the target is not
// parked, un-parked or re-queued by it.
func TestAgentMessageReachesTheTargetsAgentWithoutParkingIt(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "768", "acme/widgets", "api:tasks:write")
	seedAPITask(t, client, "796", "acme/widgets")
	running(t, client.Store, ctx, "796")
	before, err := client.Store.State(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}

	code, body := asAgent(t, srv, "768", http.MethodPost, "/api/tasks/796/agent-messages",
		`{"body":"main is red until task 900 merges; rebase after that"}`, "")
	if code != http.StatusOK {
		t.Fatalf("agent-messages answered %d: %s", code, body)
	}
	var sent ui.Message
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}

	if after, err := client.Store.State(ctx, "796"); err != nil || after != before {
		t.Errorf("state = %v (%v), want %v as it was", after, err, before)
	}
	obs, err := client.Store.GetObservation(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Errorf("task 796 was parked: %+v", obs)
	}

	comments, err := client.Store.Comments(ctx, "796")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %+v, want the message alone (no second audit note)", comments)
	}
	c := comments[0]
	if c.Author.Actor != (model.Principal{Kind: model.PrincipalAgent, ID: "768"}) || c.Author.OnBehalfOf != nil {
		t.Errorf("author = %+v, want task 768's own agent", c.Author)
	}
	if !strings.HasPrefix(c.Body, "Message from task 768 (task 768)") ||
		!strings.Contains(c.Body, "rebase after that") {
		t.Errorf("body = %q, want it signed by task 768", c.Body)
	}

	// A chat turn waiting on a person is not answered by it.
	if chat, err := client.MessagesSince(ctx, "796", 0); err != nil || len(chat) != 0 {
		t.Errorf("MessagesSince = %+v (%v), want nothing for a chat turn", chat, err)
	}
	// The live run's addenda poll, over the wire as the forked mcpserver
	// makes it, hands it over attributed to the sender.
	ts := httptest.NewServer(srv)
	defer ts.Close()
	remote := serverAccessClient(srv, ts.URL)
	addenda, err := remote.TaskAddenda(ctx, "796", 0)
	if err != nil {
		t.Fatalf("TaskAddenda: %v", err)
	}
	if len(addenda) != 1 || addenda[0].ID != sent.ID || addenda[0].Author != "task 768" ||
		addenda[0].AuthorKind != string(model.PrincipalAgent) {
		t.Fatalf("addenda = %+v, want task 768's message", addenda)
	}
}

// A target already parked on its own question stays parked: the message
// is not the person's answer.
func TestAgentMessageLeavesAParkedTargetParked(t *testing.T) {
	client, store, ctx := testClient(t)
	sender := create(t, client, ctx)
	target := create(t, client, ctx)
	question, err := client.PostAgentMessage(ctx, target.ID, "which branch?", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PostMessageToAgent(ctx, sender.ID, target.ID, "use main", nil); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != question.ID {
		t.Fatalf("observation = %+v, want the target still parked on its own question", obs)
	}
}

// The two wrong ways to call it: on the run's own task, and from
// anything but a run.
func TestAgentMessageRefusals(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:write")
	seedAPITask(t, client, "2", "acme/widgets")

	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/1/agent-messages", `{"body":"hi me"}`, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "chat tool") {
		t.Errorf("agent-messages to the run's own task answered %d: %s", code, body)
	}
	if code, body := asAgent(t, srv, "1", http.MethodPost, "/api/tasks/2/agent-messages", `{"body":"  "}`, ""); code != http.StatusBadRequest {
		t.Errorf("an empty message answered %d: %s", code, body)
	}
	if _, err := client.PostMessageToAgent(context.Background(), "", "2", "who am I?", nil); err == nil ||
		!strings.Contains(err.Error(), "/comments") {
		t.Errorf("a message with no sending task = %v, want it refused pointing at /comments", err)
	}
}

// The tool descriptions say which route does what, since the old one
// ("Post a message to a task's running agent") is what sent task 768 to
// the wrong one.
func TestAgentMessageToolDescriptions(t *testing.T) {
	var messages, agentMessages ui.APIRoute
	for _, r := range ui.AgentAPIRoutes() {
		switch r.Pattern {
		case "POST /api/tasks/{id}/messages":
			messages = r
		case "POST /api/tasks/{id}/agent-messages":
			agentMessages = r
		}
	}
	if !strings.Contains(messages.Description, "your own task") ||
		!strings.Contains(messages.Description, "agent-messages") {
		t.Errorf("/messages is described as %q", messages.Description)
	}
	if agentMessages.Tool != "api_message_task_agent" || agentMessages.Scope != "tasks:write" {
		t.Errorf("/agent-messages route = %+v", agentMessages)
	}
}
