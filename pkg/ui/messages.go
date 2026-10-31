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

package ui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Message is one entry of a task's conversation as the two /messages
// routes speak it: the comment's own id, who wrote it, what they said and
// when.
//
// It is deliberately not ui.Task's own comment shape (tasks.go's
// TaskDetail carries the whole thread, rendered for a page). The caller
// here is a program -- the forked mcpserver serving one run's chat tool
// -- which reads a conversation in one direction only: "what has been
// said since the id I already have". AuthorKind is on it because that is
// the one thing that caller has to be able to tell: a person replying is
// the thing it is waiting for, and grain's own automation posting a park
// notice is not.
type Message struct {
	ID         int64  `json:"id"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"authorKind"`
	Body       string `json:"body"`
	// Summary is the one line the message is shown by in the inbox, the
	// task list and notifications (model.Comment.Summary). Set on a
	// message a run posted; "" on a person's reply.
	Summary     string       `json:"summary,omitempty"`
	At          time.Time    `json:"at"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// messageRequest is POST /api/tasks/{id}/messages' body: what the run
// wants to say to the human watching its task, and the one line it is
// to be shown by (model.Comment.Summary) -- optional on the wire for an
// older caller, and derived from Body when it is missing.
type messageRequest struct {
	Body        string             `json:"body"`
	Summary     string             `json:"summary"`
	Attachments []AttachmentUpload `json:"attachments"`
}

// errMessageEmpty refuses a message with nothing in it. A
// ValidationError, so it is a 400 rather than a 500: it is the caller's
// own mistake, and the caller is a model reading the error as its next
// instruction.
var errMessageEmpty = validationErrorf("a message needs a body")

// PostAgentMessage says something to the human watching id's task, on
// behalf of the run working on it, and parks the task on the answer.
//
// Two things happen, and the second is the point. The comment itself is
// recorded exactly as orchestrator.relayComment records a run's words --
// (automation, on behalf of agent), grain speaking for the agent, which
// is the attribution the conversation already uses for everything a run
// says. And Observation.PendingQuestionCommentID is pointed at it, so the
// task reads awaiting_reply the instant the run pauses rather than only
// once the run finishes: the tool that calls this blocks, and a person
// looking at the task list is the person it is blocking on.
//
// This is the write half of a chat turn happening inside one run. The
// read half is MessagesSince below; the tool that pairs them is
// mcp.NewChatTools, and the polling between the two is
// pkg/cli/mcpserver.go's daemonChat. It exists here, rather than in the
// tool, for the reason SetTaskActivity does: the row being written lives
// in the daemon's store, and a forked mcpserver holds a transport into a
// sandbox and nothing else.
//
// Unlike SetTaskActivity there is no liveness check. A comment is a
// comment whether or not grain still has the run in flight, and a message
// posted by a run that has just been cancelled is better recorded --
// parked, answerable, and picked up by the next dispatch -- than dropped
// for having arrived a second late.
//
// A human's reply is what un-parks it, through the same
// Client.AddComment every other awaiting_reply task is answered by:
// nothing here has to be undone, and nothing here undoes it.
//
// summary is the message's one line (model.Comment.Summary): refused if
// it is more than one line or too long, and derived from body, counted,
// when it is empty.
func (c *Client) PostAgentMessage(ctx context.Context, id, body, summary string, uploads []AttachmentUpload) (Message, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return Message{}, err
	}
	if task == nil {
		return Message{}, &NotFoundError{ID: id}
	}
	if body == "" {
		return Message{}, errMessageEmpty
	}
	if err := model.ValidateQuestionSummary(summary); err != nil {
		return Message{}, validationErrorf("%v", err)
	}
	summary = model.QuestionSummary(summary, body, "POST /api/tasks/"+id+"/messages")
	now := c.now()
	attachments, err := decodeAttachments(uploads, now)
	if err != nil {
		return Message{}, err
	}
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: id}
	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: id,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      body,
		Summary:   summary,
		CreatedAt: now,
	})
	if err != nil {
		return Message{}, err
	}
	var msgAttachments []Attachment
	for i := range attachments {
		attachments[i].TaskID = id
		attachments[i].CommentID = &commentID
		attID, err := c.Store.AddAttachment(ctx, attachments[i])
		if err != nil {
			return Message{}, err
		}
		msgAttachments = append(msgAttachments, Attachment{
			ID:          attID,
			Filename:    attachments[i].Filename,
			ContentType: attachments[i].ContentType,
			Size:        int64(len(attachments[i].Content)),
		})
	}
	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	}); err != nil {
		return Message{}, err
	}
	return Message{
		ID:          commentID,
		AuthorKind:  string(model.PrincipalAutomation),
		Body:        body,
		Summary:     summary,
		At:          now,
		Attachments: msgAttachments,
	}, nil
}

// MessagesSince is everything a person has said on id's conversation
// after the comment named by after -- the read half of a chat turn.
//
// Human-authored comments only, which is the whole of what the waiting
// side is waiting for. The run already holds its own words (it wrote
// them), and grain's own automation posts notices on a task for reasons
// that have nothing to do with the question standing -- a park, a merge
// queue verdict -- none of which is an answer, and each of which would
// end a wait with nothing to show for it.
//
// after 0 is "everything", which is what a caller with no id in hand
// means; a caller in a chat turn passes the id PostAgentMessage gave it,
// so it can never be handed a reply that predates its own question.
func (c *Client) MessagesSince(ctx context.Context, id string, after int64) ([]Message, error) {
	return c.messagesSince(ctx, id, after, false)
}

// AddendaSince is MessagesSince plus what other tasks' runs have sent
// this task's agent (PostMessageToAgent): everything a live run on id
// should be handed as an addendum on its next tool result. A chat turn
// does not read this -- another agent's words are not the person's
// answer it is waiting on, and the task stays parked until that person
// replies.
func (c *Client) AddendaSince(ctx context.Context, id string, after int64) ([]Message, error) {
	return c.messagesSince(ctx, id, after, true)
}

// isAgentMessage reports whether comment, on taskID's conversation, is
// another task's run speaking to this one's agent: written with that
// run's own agent principal as the actor, which only PostMessageToAgent
// and an agent-API comment on another task write. A run's own words are
// relayed as grain on its behalf (automation actor), so they never match.
func isAgentMessage(comment model.Comment, taskID string) bool {
	return comment.Author.Actor.Kind == model.PrincipalAgent &&
		comment.Author.Actor.ID != "" && comment.Author.Actor.ID != taskID
}

func (c *Client) messagesSince(ctx context.Context, id string, after int64, fromAgents bool) ([]Message, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &NotFoundError{ID: id}
	}
	comments, err := c.Store.Comments(ctx, id)
	if err != nil {
		return nil, err
	}
	attachmentMetas, err := c.Store.AttachmentMetas(ctx, id)
	if err != nil {
		return nil, err
	}
	byComment := map[int64][]Attachment{}
	for _, a := range attachmentMetas {
		if a.CommentID != nil {
			byComment[*a.CommentID] = append(byComment[*a.CommentID], attachmentFrom(a))
		}
	}
	messages := []Message{}
	for _, comment := range comments {
		if comment.ID <= after {
			continue
		}
		author := comment.Author.Actor.ID
		switch {
		case comment.Author.Actor.Kind == model.PrincipalHuman:
		case fromAgents && isAgentMessage(comment, id):
			author = "task " + author
		case fromAgents && (comment.Author.Actor == model.CLThreadsPrincipal ||
			comment.Author.Actor == model.CapabilityChangePrincipal):
			author = "grain"
		default:
			continue
		}
		messages = append(messages, Message{
			ID:          comment.ID,
			Author:      author,
			AuthorKind:  string(comment.Author.Actor.Kind),
			Body:        comment.Body,
			At:          comment.CreatedAt,
			Attachments: byComment[comment.ID],
		})
	}
	return messages, nil
}

// handlePostTaskMessage answers POST /api/tasks/{id}/messages.
func (s *Server) handlePostTaskMessage(w http.ResponseWriter, r *http.Request) {
	var req messageRequest
	if !readAttachmentJSON(w, r, &req, func(req *messageRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	message, err := s.tasks.PostAgentMessage(r.Context(), r.PathValue("id"), req.Body, req.Summary, req.Attachments)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

// agentMessageRequest is POST /api/tasks/{id}/agent-messages' body: what
// the calling run wants to tell the target task's agent.
type agentMessageRequest struct {
	Body        string             `json:"body"`
	Attachments []AttachmentUpload `json:"attachments"`
}

// errAgentMessageNeedsSender refuses POST /api/tasks/{id}/agent-messages
// from anything but a run calling through the agent API: the message is
// signed as the calling task, and a request with no acting task has none.
var errAgentMessageNeedsSender = validationErrorf("a message to another task's agent is sent by a task's run " +
	"through the agent API (api_message_task_agent); a person says something on a task with POST /api/tasks/{id}/comments")

// PostMessageToAgent hands body to the agent of task to, from the run of
// task from: one run telling another something while both get on with
// their work, which is what neither /messages nor /comments is.
//
// The comment is written with from's own agent principal as its actor --
// the run itself speaking, not grain relaying a run's words on its own
// task -- and its first line names the sender, so a person reading to's
// conversation sees who sent it. That actor is also what delivers it:
// AddendaSince hands a live run on to every such comment on its next
// tool result, the way it hands over a person's comment, and a run
// dispatched later reads it in its prompt's conversation like anything
// else said on the task.
//
// Nothing else is touched. It never sets PendingQuestionCommentID (that
// would park to on the operator as if to had asked them something, which
// is what POST /api/tasks/{id}/messages did when an agent called it on
// another task), never lifts one either, and never queues a dispatch: a
// task's state is the same after this as before it.
func (c *Client) PostMessageToAgent(ctx context.Context, from, to, body string, uploads []AttachmentUpload) (Message, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return Message{}, errAgentMessageNeedsSender
	}
	if from == to {
		return Message{}, validationErrorf("task %s cannot send a message to its own agent: "+
			"to say something to the human watching your own task, use the chat tool "+
			"(POST /api/tasks/{id}/messages); to leave a note on it, comment", from)
	}
	if strings.TrimSpace(body) == "" {
		return Message{}, errMessageEmpty
	}
	target, err := c.Store.GetTask(ctx, to)
	if err != nil {
		return Message{}, err
	}
	if target == nil {
		return Message{}, &NotFoundError{ID: to}
	}
	var signature, authorLabel string
	if strings.HasPrefix(from, "external:") {
		signature = fmt.Sprintf("external agent %s (%s)", strings.TrimPrefix(from, "external:"), from)
		authorLabel = from
	} else {
		sender, err := c.Store.GetTask(ctx, from)
		if err != nil {
			return Message{}, err
		}
		signature = "task " + from
		if sender != nil && strings.TrimSpace(sender.Title) != "" {
			signature = fmt.Sprintf("task %s (%s)", from, strings.TrimSpace(sender.Title))
		}
		authorLabel = "task " + from
	}
	now := c.now()
	attachments, err := decodeAttachments(uploads, now)
	if err != nil {
		return Message{}, err
	}
	signed := fmt.Sprintf("Message from %s to this task's agent:\n\n%s", signature, body)
	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    to,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: from}},
		Body:      signed,
		CreatedAt: now,
	})
	if err != nil {
		return Message{}, err
	}
	var msgAttachments []Attachment
	for i := range attachments {
		attachments[i].TaskID = to
		attachments[i].CommentID = &commentID
		attID, err := c.Store.AddAttachment(ctx, attachments[i])
		if err != nil {
			return Message{}, err
		}
		msgAttachments = append(msgAttachments, Attachment{
			ID:          attID,
			Filename:    attachments[i].Filename,
			ContentType: attachments[i].ContentType,
			Size:        int64(len(attachments[i].Content)),
		})
	}
	return Message{
		ID:          commentID,
		Author:      authorLabel,
		AuthorKind:  string(model.PrincipalAgent),
		Body:        signed,
		At:          now,
		Attachments: msgAttachments,
	}, nil
}

// handleMessageTaskAgent answers POST /api/tasks/{id}/agent-messages:
// the calling run's message to task {id}'s agent (PostMessageToAgent).
// The sender is the acting task agentAPIGate authorized, never anything
// the body says.
func (s *Server) handleMessageTaskAgent(w http.ResponseWriter, r *http.Request) {
	var req agentMessageRequest
	if !readAttachmentJSON(w, r, &req, func(req *agentMessageRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	var from string
	if call, ok := agentCallFromContext(r.Context()); ok {
		from = call.principalID()
	}
	message, err := s.tasks.PostMessageToAgent(r.Context(), from, r.PathValue("id"), req.Body, req.Attachments)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

// handleListTaskMessages answers GET /api/tasks/{id}/messages?after=N.
// With agents=true it also returns other tasks' runs' messages to this
// task's agent (AddendaSince) -- the read a run's addenda poll makes,
// which a chat turn waiting on a person's answer does not.
//
// An unparsable "after" is a 400 rather than a silent 0: answering a
// caller that asked the wrong question with the whole conversation would
// hand a chat turn every message ever posted and let it read the oldest
// as this turn's reply.
func (s *Server) handleListTaskMessages(w http.ResponseWriter, r *http.Request) {
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeClientError(w, validationErrorf("after must be a comment id"))
			return
		}
		after = parsed
	}
	read := s.tasks.MessagesSince
	if agents, _ := strconv.ParseBool(r.URL.Query().Get("agents")); agents {
		read = s.tasks.AddendaSince
	}
	messages, err := read(r.Context(), r.PathValue("id"), after)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// deliveredRequest is POST /api/tasks/{id}/delivered's body: the last
// comment the run reporting it has now been shown.
type deliveredRequest struct {
	Through int64 `json:"through"`
}

// RecordDelivered records that the run working on id has been shown
// every comment up to and including through -- the forked mcpserver's
// report, made when it hands a person's words to its run as an addendum
// on a tool result or as the reply to a chat call (pkg/cli/mcpserver.go's
// deliveredCursor). It only ever moves the mark forward
// (model.Store.AdvanceDelivered).
//
// The mark is what an interactive run's ending is judged by
// (Observation.DeliveredCommentID): a person's comment after it has not
// been seen by any run and gets a fresh dispatch, one at or before it
// has. Reporting it from the far end of the delivery, rather than
// inferring it here from which reads were answered, is what keeps a poll
// that timed out on its way back from counting as a message read.
func (c *Client) RecordDelivered(ctx context.Context, id string, through int64) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if through <= 0 {
		return validationErrorf("through must be a comment id")
	}
	// It has to name a comment that is on this task. The mark decides
	// whether a person's message still gets an answer, so a report
	// pointing past the end of the conversation -- or at another task's
	// comment, whose id may be far ahead of this one's -- would mark
	// messages nobody has posted yet as read, and the session would go
	// quiet on every one of them. The mcpserver only ever reports ids it
	// read off this task's own conversation, so a real report always
	// passes.
	comments, err := c.Store.Comments(ctx, id)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(comments, func(comment model.Comment) bool { return comment.ID == through }) {
		return validationErrorf("through must be the id of a comment on task %s", id)
	}
	now := c.now()
	if err := c.Store.AdvanceDelivered(ctx, id, through, now); err != nil {
		return err
	}
	return c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	})
}

// handleRecordDelivered answers POST /api/tasks/{id}/delivered.
func (s *Server) handleRecordDelivered(w http.ResponseWriter, r *http.Request) {
	var req deliveredRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.RecordDelivered(r.Context(), r.PathValue("id"), req.Through); err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"through": req.Through})
}
