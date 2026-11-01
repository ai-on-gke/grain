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

// The two /messages routes: one chat turn, held inside the run having it
// (pkg/mcp's chat tool, pkg/cli/mcpserver.go's daemonChat). Like
// activity_test.go next door there is nothing behind these to fake -- the
// conversation is in the store the daemon already holds -- so these drive
// a real store and assert on what the task and its thread read back as.

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The whole point of the write half: what the run said is in the
// conversation, attributed as grain relaying an agent, and the task is
// parked on it *now* rather than when the run eventually ends.
func TestPostAgentMessageParksTheTaskOnIt(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	message, err := client.PostAgentMessage(ctx, task.ID, "Two ways to do this. Which did you mean?", "", []ui.AttachmentUpload{{
		Filename: "diagram.png", ContentType: "image/png",
		Content: base64.StdEncoding.EncodeToString([]byte("png")),
	}})
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if message.ID == 0 || message.Body != "Two ways to do this. Which did you mean?" {
		t.Fatalf("message = %+v, want the comment it wrote", message)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := comments[len(comments)-1]
	if last.Body != message.Body {
		t.Errorf("last comment = %q, want what the run said", last.Body)
	}
	attachments, err := store.AttachmentMetas(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].CommentID == nil ||
		*attachments[0].CommentID != message.ID || attachments[0].Filename != "diagram.png" {
		t.Fatalf("attachments = %+v, want diagram.png on the agent message", attachments)
	}
	if last.Author.Actor.Kind != model.PrincipalAutomation ||
		last.Author.OnBehalfOf == nil || last.Author.OnBehalfOf.Kind != model.PrincipalAgent {
		t.Errorf("author = %+v, want grain on behalf of the agent", last.Author)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != message.ID {
		t.Fatalf("observation = %+v, want the task parked on the message", obs)
	}
	// Which is what a person watching the list actually sees: the run has
	// not ended, and the task already says it is waiting on them.
	if got, err := client.Task(ctx, task.ID); err != nil || got.State != model.StateAwaitingReply {
		t.Errorf("state = %v (%v), want awaiting_reply while the run waits", got.State, err)
	}
}

// The read half, and the two things it filters: everything already said
// before the run's own message, and everything said by anything but a
// person. A wait ends on an answer, and grain's own automation posting a
// notice is not one.
func TestMessagesSinceReturnsOnlyLaterHumanReplies(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	if err := client.AddComment(ctx, task.ID, "said before the run ever asked", nil); err != nil {
		t.Fatal(err)
	}
	message, err := client.PostAgentMessage(ctx, task.ID, "which did you mean?", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing yet: the run's own message is not a reply to itself.
	messages, err := client.MessagesSince(ctx, task.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("MessagesSince = %+v, want nothing before anybody answers", messages)
	}

	// grain talking to itself is not an answer either.
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:   "the merge queue says something unrelated",
	}); err != nil {
		t.Fatal(err)
	}
	if messages, err = client.MessagesSince(ctx, task.ID, message.ID); err != nil || len(messages) != 0 {
		t.Fatalf("MessagesSince = %+v (%v), want automation's own comments left out", messages, err)
	}

	// A person answering is.
	if err := client.AddComment(ctx, task.ID, "the second one", nil); err != nil {
		t.Fatal(err)
	}
	messages, err = client.MessagesSince(ctx, task.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Body != "the second one" ||
		messages[0].AuthorKind != string(model.PrincipalHuman) {
		t.Fatalf("MessagesSince = %+v, want just the human reply", messages)
	}
}

func TestMessagesSinceIncludesAuthorForMultiUserReplies(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	message, err := client.PostAgentMessage(ctx, task.ID, "who is reviewing this?", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := client.AddCommentWithAuthor(ctx, task.ID, "alice reviewing", nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := client.AddCommentWithAuthor(ctx, task.ID, "bob also reviewing", nil, "bob"); err != nil {
		t.Fatal(err)
	}

	messages, err := client.MessagesSince(ctx, task.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("MessagesSince returned %d messages, want 2", len(messages))
	}
	if messages[0].Author != "alice" || messages[0].Body != "alice reviewing" {
		t.Errorf("messages[0] = %+v, want author alice", messages[0])
	}
	if messages[1].Author != "bob" || messages[1].Body != "bob also reviewing" {
		t.Errorf("messages[1] = %+v, want author bob", messages[1])
	}
}

// The park is lifted by the ordinary reply every other awaiting_reply
// task is answered by (AddComment), which is what lets a run whose chat
// wait ran out be continued by the person's next message rather than
// needing anything of its own.
func TestReplyToAnAgentMessageUnparksTheTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)

	if _, err := client.PostAgentMessage(ctx, task.ID, "still there?", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := client.AddComment(ctx, task.ID, "sorry, yes", nil); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Fatalf("observation = %+v, want the reply to have un-parked the task", obs)
	}
}

// A message with nothing in it is the caller's own mistake, and the
// caller is a model reading the error as its next instruction.
func TestPostAgentMessageRefusesAnEmptyMessage(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	task := create(t, client, ctx)

	if _, err := client.PostAgentMessage(ctx, task.ID, "", "", nil); err == nil ||
		!strings.Contains(err.Error(), "needs a body") {
		t.Fatalf("err = %v, want it refused for having nothing in it", err)
	}
}

// Both routes over the wire, which is how they are actually reached: the
// forked mcpserver holds an HTTPClient and nothing else.
func TestTaskMessagesOverHTTP(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	srv := httptest.NewServer(ui.NewServerWithClient(client))
	defer srv.Close()
	remote := accessCodeClient(client, srv.URL)

	message, err := remote.PostTaskMessage(ctx, task.ID, "which did you mean?", "", nil)
	if err != nil {
		t.Fatalf("PostTaskMessage: %v", err)
	}
	messages, err := remote.TaskMessages(ctx, task.ID, message.ID)
	if err != nil {
		t.Fatalf("TaskMessages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("TaskMessages = %+v, want nothing yet", messages)
	}
	if err := client.AddComment(ctx, task.ID, "the second one", nil); err != nil {
		t.Fatal(err)
	}
	if messages, err = remote.TaskMessages(ctx, task.ID, message.ID); err != nil || len(messages) != 1 ||
		messages[0].Body != "the second one" {
		t.Fatalf("TaskMessages = %+v (%v), want the reply", messages, err)
	}
}

func TestMessagesSinceIncludesAttachments(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	message, err := client.PostAgentMessage(ctx, task.ID, "need clarification", "", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}

	if err := client.AddComment(ctx, task.ID, "check this diagram", []ui.AttachmentUpload{{
		Filename:    "diagram.png",
		ContentType: "image/png",
		Content:     base64.StdEncoding.EncodeToString([]byte("png-data")),
	}}); err != nil {
		t.Fatal(err)
	}

	messages, err := client.MessagesSince(ctx, task.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if len(messages[0].Attachments) != 1 {
		t.Fatalf("len(attachments) = %d, want 1", len(messages[0].Attachments))
	}
	if messages[0].Attachments[0].Filename != "diagram.png" {
		t.Errorf("attachment filename = %q, want diagram.png", messages[0].Attachments[0].Filename)
	}

	srv := httptest.NewServer(ui.NewServerWithClient(client))
	defer srv.Close()
	remote := accessCodeClient(client, srv.URL)

	att, err := remote.Attachment(ctx, task.ID, messages[0].Attachments[0].ID)
	if err != nil {
		t.Fatalf("Attachment: %v", err)
	}
	if string(att.Content) != "png-data" {
		t.Errorf("attachment content = %q, want 'png-data'", string(att.Content))
	}
}

// What a run has been shown of its conversation only ever moves forward:
// a late or repeated report from the run's mcpserver, or one seeded from
// an older prompt, must not make a message the run already answered look
// unread again (Observation.DeliveredCommentID, grain/task-1774).
func TestRecordDeliveredOnlyMovesForward(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)

	var ids []int64
	for _, body := range []string{"one", "two", "three"} {
		id, err := store.AddComment(ctx, model.Comment{
			TaskID: task.ID, Body: body,
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, through := range []int64{ids[0], ids[2], ids[1]} {
		if err := client.RecordDelivered(ctx, task.ID, through); err != nil {
			t.Fatalf("RecordDelivered(%d): %v", through, err)
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil || *obs.DeliveredCommentID != ids[2] {
		t.Fatalf("observation = %+v, want the delivered mark at %d", obs, ids[2])
	}
	if err := client.RecordDelivered(ctx, task.ID, 0); err == nil {
		t.Errorf("RecordDelivered(0) succeeded, want it refused as no comment at all")
	}
	// A mark past the end of the conversation would count messages not
	// yet posted as read, and the session would never answer them.
	if err := client.RecordDelivered(ctx, task.ID, ids[2]+1000); err == nil {
		t.Errorf("RecordDelivered past the last comment succeeded, want it refused")
	}
	other := create(t, client, ctx)
	elsewhere, err := store.AddComment(ctx, model.Comment{
		TaskID: other.ID, Body: "on another task",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RecordDelivered(ctx, task.ID, elsewhere); err == nil {
		t.Errorf("RecordDelivered with another task's comment succeeded, want it refused")
	}
	if obs, err := store.GetObservation(ctx, task.ID); err != nil || *obs.DeliveredCommentID != ids[2] {
		t.Errorf("delivered mark moved to %v (%v) after refused reports, want it still %d",
			obs.DeliveredCommentID, err, ids[2])
	}
	if err := client.RecordDelivered(ctx, "no-such-task", 3); err == nil {
		t.Errorf("RecordDelivered on a missing task succeeded, want not found")
	}
}

// A message carries the one line it is shown by in the inbox and the task
// list: on the comment, on the message the route answers with, and as
// the task's own questionSummary in its detail and in the list.
func TestPostAgentMessageCarriesItsSummary(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	message, err := client.PostAgentMessage(ctx, task.ID, "Two ways to do this. Which did you mean?",
		"Pick one of two ways: rewrite or patch", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if message.Summary != "Pick one of two ways: rewrite or patch" {
		t.Errorf("message summary = %q", message.Summary)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := detail.Comments[len(detail.Comments)-1]
	if last.Summary != "Pick one of two ways: rewrite or patch" {
		t.Errorf("comment summary = %q", last.Summary)
	}
	if detail.QuestionSummary != "Pick one of two ways: rewrite or patch" {
		t.Errorf("detail questionSummary = %q", detail.QuestionSummary)
	}
	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list {
		if row.ID == task.ID {
			found = true
			if row.QuestionSummary != "Pick one of two ways: rewrite or patch" {
				t.Errorf("list questionSummary = %q", row.QuestionSummary)
			}
		}
	}
	if !found {
		t.Fatalf("task %s not in the list", task.ID)
	}
}

// An older caller sends no summary: one is derived from the message's
// first sentence. One that is not a single line is refused, as a 400 the
// calling model can read.
func TestPostAgentMessageDerivesAMissingSummaryAndRefusesABadOne(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	message, err := client.PostAgentMessage(ctx, task.ID, "Which did you mean? The flag or the file.", "", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if message.Summary != "Which did you mean?" {
		t.Errorf("derived summary = %q, want the first sentence", message.Summary)
	}
	if _, err := client.PostAgentMessage(ctx, task.ID, "which?", "one\ntwo", nil); err == nil ||
		!strings.Contains(err.Error(), "single line") {
		t.Errorf("err = %v, want a two-line summary refused", err)
	}
	if _, err := client.PostAgentMessage(ctx, task.ID, "which?", strings.Repeat("x", model.MaxQuestionSummaryLen+1), nil); err == nil {
		t.Errorf("an over-long summary was accepted")
	}
}

// RecordDelivered clears PendingQuestionCommentID and PendingSecret only when
// the delivered mark actually advances across at least one PrincipalHuman
// reply: a duplicate/stale delivery report or an agent-to-agent message must
// never lift the task's parked question or pending secret prompt.
func TestRecordDeliveredOnlyClearsPendingQuestionOnNewHumanReply(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	seedHuman, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "initial instruction",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RecordDelivered(ctx, task.ID, seedHuman); err != nil {
		t.Fatalf("RecordDelivered(seedHuman): %v", err)
	}

	asked, err := client.PostAgentMessage(ctx, task.ID, "Need confirmation and secret", "Confirm and set secret", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if err := store.ObserveField(ctx, task.ID, client.Now(), func(o *model.Observation) {
		o.PendingSecret = "stripe-api-key"
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Stale/duplicate RecordDelivered at or below DeliveredCommentID must NOT clear pending fields.
	if err := client.RecordDelivered(ctx, task.ID, seedHuman); err != nil {
		t.Fatalf("RecordDelivered(stale): %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != asked.ID || obs.PendingSecret != "stripe-api-key" {
		t.Fatalf("after stale RecordDelivered: PendingQuestionCommentID=%v PendingSecret=%q, want %d and stripe-api-key",
			obs.PendingQuestionCommentID, obs.PendingSecret, asked.ID)
	}

	// 2. Advancing DeliveredCommentID over a non-human (agent) comment must NOT clear pending fields.
	peerComment, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "peer agent note",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "peer-task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RecordDelivered(ctx, task.ID, peerComment); err != nil {
		t.Fatalf("RecordDelivered(peerComment): %v", err)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.DeliveredCommentID == nil || *obs.DeliveredCommentID != peerComment {
		t.Fatalf("DeliveredCommentID = %v, want %d", obs.DeliveredCommentID, peerComment)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != asked.ID || obs.PendingSecret != "stripe-api-key" {
		t.Fatalf("after non-human RecordDelivered: PendingQuestionCommentID=%v PendingSecret=%q, want %d and stripe-api-key",
			obs.PendingQuestionCommentID, obs.PendingSecret, asked.ID)
	}

	// 3. Advancing DeliveredCommentID across a PrincipalHuman reply DOES clear both pending fields.
	humanReply, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "yes, proceed",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RecordDelivered(ctx, task.ID, humanReply); err != nil {
		t.Fatalf("RecordDelivered(humanReply): %v", err)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.DeliveredCommentID == nil || *obs.DeliveredCommentID != humanReply {
		t.Fatalf("DeliveredCommentID = %v, want %d", obs.DeliveredCommentID, humanReply)
	}
	if obs.PendingQuestionCommentID != nil || obs.PendingSecret != "" {
		t.Fatalf("after human RecordDelivered: PendingQuestionCommentID=%v PendingSecret=%q, want nil and empty",
			obs.PendingQuestionCommentID, obs.PendingSecret)
	}
}

// A person's comment said before the run asked its question, and only
// marked delivered afterwards, does not answer that question: the
// delivered report must leave it parked.
func TestRecordDeliveredKeepsAQuestionAskedAfterTheHumanComment(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	earlier, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "an earlier note",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Parked directly, as a question relayed when a run ends is: a chat
	// post would not park on a comment already waiting
	// (TestPostAgentMessageDoesNotParkOnAQuestionAlreadyAnswered).
	asked := ui.Message{}
	asked.ID, err = store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "Which region?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
		o.PendingQuestionCommentID = &asked.ID
	}); err != nil {
		t.Fatal(err)
	}
	if asked.ID <= earlier {
		t.Fatalf("question %d is not after the earlier comment %d", asked.ID, earlier)
	}
	if err := client.RecordDelivered(ctx, task.ID, asked.ID); err != nil {
		t.Fatalf("RecordDelivered(question): %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != asked.ID {
		t.Fatalf("PendingQuestionCommentID = %v, want %d: a comment from before the question un-parked it",
			obs.PendingQuestionCommentID, asked.ID)
	}
}

// A chat post made while a person's comment is already waiting, unshown
// to the run, is answered by that comment the moment the chat call polls,
// so it must not leave the task parked on a question nobody is left to
// answer.
func TestPostAgentMessageDoesNotParkOnAQuestionAlreadyAnswered(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	// With no delivered mark nothing says what a run has been shown, so
	// an answered question followed by a new one parks as it always has.
	answer, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "use main",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unmarked, err := client.PostAgentMessage(ctx, task.ID, "And which reviewer?", "Pick a reviewer", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != unmarked.ID {
		t.Fatalf("PendingQuestionCommentID = %v, want %d with no delivered mark", obs, unmarked.ID)
	}
	if err := client.RecordDelivered(ctx, task.ID, answer); err != nil {
		t.Fatalf("RecordDelivered: %v", err)
	}

	waiting, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Body: "ok, did that do it?",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PostAgentMessage(ctx, task.ID, "Done; anything else?", "Anything else?", nil); err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if obs, err = store.GetObservation(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Fatalf("PendingQuestionCommentID = %d, want nil: a waiting comment already answers the chat post", *obs.PendingQuestionCommentID)
	}

	// Once that comment has been delivered, the next chat post parks as usual.
	if err := client.RecordDelivered(ctx, task.ID, waiting); err != nil {
		t.Fatalf("RecordDelivered: %v", err)
	}
	asked, err := client.PostAgentMessage(ctx, task.ID, "Which region?", "Pick a region", nil)
	if err != nil {
		t.Fatalf("PostAgentMessage: %v", err)
	}
	if obs, err = store.GetObservation(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != asked.ID {
		t.Fatalf("PendingQuestionCommentID = %v, want %d once nothing is waiting", obs, asked.ID)
	}
}
