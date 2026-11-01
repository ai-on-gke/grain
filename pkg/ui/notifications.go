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
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Operator notifications over the API (design/design/durable-tasks.design.md,
// DT-5): raise, list, acknowledge, clear and investigate, plus the
// sandbox's two hops -- notify_operator (POST /api/tasks/{id}/notify)
// and message_task (POST /api/tasks/{id}/notes, DT-10).

// The agent limits on notify_operator.
const (
	// MaxAgentRaisesPerRun is how many notify_operator calls one run may
	// make.
	MaxAgentRaisesPerRun = 10
	// MaxAgentOpenNotifications is how many open notifications one task's
	// agents may have raised at once.
	MaxAgentOpenNotifications = 20
)

// InvestigateNotificationTemplateID is the template an Investigate files
// its task from (model.InvestigateNotificationTemplateID).
const InvestigateNotificationTemplateID = model.InvestigateNotificationTemplateID

// RaiseNotificationRequest is POST /api/notifications' body, and
// notify_operator's arguments.
type RaiseNotificationRequest struct {
	Subject    string                   `json:"subject"`
	Title      string                   `json:"title"`
	Body       string                   `json:"body"`
	Severity   string                   `json:"severity,omitempty"`
	Task       string                   `json:"task,omitempty"`
	Repo       string                   `json:"repo,omitempty"`
	ClearsWhen *model.NotificationClear `json:"clearsWhen,omitempty"`
	Actions    []string                 `json:"actions,omitempty"`
}

// NotificationRaiseResponse is what a raise answers.
type NotificationRaiseResponse struct {
	Notification model.Notification `json:"notification"`
	New          bool               `json:"new"`
}

// NoteRequest is POST /api/tasks/{id}/notes' body. From is believed only
// from a caller with no principal of its own -- `grain mcpserver`'s hop
// for message_task, which the access gate already trusts -- the way
// handleDeploymentBroadcast believes its body's Task.
type NoteRequest struct {
	Body string `json:"body"`
	From string `json:"from,omitempty"`
}

// agentRaises counts notify_operator calls per run, for
// MaxAgentRaisesPerRun. In memory: a daemon restart ends every run it
// was counting anyway.
type agentRaises struct {
	mu    sync.Mutex
	count map[string]int
}

var raisesPerRun = agentRaises{count: map[string]int{}}

func (a *agentRaises) take(run string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.count[run] >= MaxAgentRaisesPerRun {
		return false
	}
	a.count[run]++
	return true
}

// isTaskAdmin reports whether t holds every scope of the api:task-admin
// preset, on every repository.
func isTaskAdmin(t *model.Task) bool {
	if t == nil {
		return false
	}
	preset, ok := apiPreset("task-admin")
	if !ok {
		return false
	}
	set := APIGrantsOf(grantNames(t.Grants))
	for _, scope := range preset.Scopes {
		if !set.Has(scope) || !set.Allows(scope, "") {
			return false
		}
	}
	return true
}

// AgentNamespace is the prefix a run's subjects are given unless its task
// holds api:task-admin, so a run cannot clear or overwrite a reconciler's
// notification.
func AgentNamespace(taskID string) string { return "agent." + taskID + ":" }

// RaiseNotification raises req as whoever ctx's actor is. A run's agent
// principal is held to notify_operator's rules (AgentNotify).
func (c *Client) RaiseNotification(ctx context.Context, req RaiseNotificationRequest) (NotificationRaiseResponse, error) {
	actor := c.actor(ctx)
	if actor.Kind == model.PrincipalAgent {
		return c.AgentNotify(ctx, actor.ID, req)
	}
	return c.raise(ctx, req, model.PrincipalString(actor))
}

// AgentNotify is notify_operator: the run working on taskID raises a
// notification. It never parks the task and changes nothing about it.
//
// The subject is namespaced as agent.<task>: unless the task holds
// api:task-admin; critical needs api:task-admin; and a run may raise at
// most MaxAgentRaisesPerRun times, with at most MaxAgentOpenNotifications
// open for its task. Each refusal is a sentence saying which.
func (c *Client) AgentNotify(ctx context.Context, taskID string, req RaiseNotificationRequest) (NotificationRaiseResponse, error) {
	task, err := c.Store.GetTask(ctx, taskID)
	if err != nil {
		return NotificationRaiseResponse{}, err
	}
	if task == nil {
		return NotificationRaiseResponse{}, &NotFoundError{ID: taskID}
	}
	admin := isTaskAdmin(task)
	severity, err := model.ParseNotificationSeverity(req.Severity)
	if err != nil {
		return NotificationRaiseResponse{}, validationErrorf("%v", err)
	}
	if severity == model.NotificationCritical && !admin {
		return NotificationRaiseResponse{}, forbiddenErrorf("only a task holding api:task-admin may raise a critical " +
			"notification; raise it as warning, which still reaches the operator")
	}
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" {
		return NotificationRaiseResponse{}, validationErrorf("a notification needs a subject: a short key naming what it is about, so raising it again bumps it")
	}
	if !admin && !strings.HasPrefix(req.Subject, AgentNamespace(taskID)) {
		req.Subject = AgentNamespace(taskID) + req.Subject
	}
	if req.Task == "" {
		req.Task = taskID
	}
	raisedBy := model.PrincipalString(model.Principal{Kind: model.PrincipalAgent, ID: taskID})
	open, err := c.Store.ListNotifications(ctx, model.NotificationFilter{Open: true, RaisedBy: raisedBy,
		Limit: MaxAgentOpenNotifications + 1})
	if err != nil {
		return NotificationRaiseResponse{}, err
	}
	already := slices.ContainsFunc(open, func(n model.Notification) bool { return n.Subject == req.Subject })
	if !already && len(open) >= MaxAgentOpenNotifications {
		return NotificationRaiseResponse{}, validationErrorf("task %s already has %d open notifications, the most "+
			"one task may have: raise one of the same subjects again instead, or wait for some to clear", taskID, len(open))
	}
	if !raisesPerRun.take(c.liveRunKey(ctx, taskID)) {
		return NotificationRaiseResponse{}, validationErrorf("this run has already raised %d notifications, the most one "+
			"run may: say the rest in your closing note", MaxAgentRaisesPerRun)
	}
	req.Severity = string(severity)
	return c.raise(ctx, req, raisedBy)
}

// liveRunKey is taskID's live run id, or the task id itself when grain
// cannot see one (a hop that arrived as the run ended), qualified by the
// store so two daemons in one process never share a count.
func (c *Client) liveRunKey(ctx context.Context, taskID string) string {
	key := "task:" + taskID
	if runs, err := c.Store.LiveRuns(ctx); err == nil {
		for _, r := range runs {
			if r.TaskID == taskID {
				key = r.ID
				break
			}
		}
	}
	return fmt.Sprintf("%p/%s", c.Store, key)
}

func (c *Client) raise(ctx context.Context, req RaiseNotificationRequest, raisedBy string) (NotificationRaiseResponse, error) {
	severity, err := model.ParseNotificationSeverity(req.Severity)
	if err != nil {
		return NotificationRaiseResponse{}, validationErrorf("%v", err)
	}
	if strings.TrimSpace(req.Title) == "" {
		return NotificationRaiseResponse{}, validationErrorf("a notification needs a title")
	}
	if req.Task != "" {
		req.Task = model.NormalizeTaskID(req.Task)
	}
	raised, err := c.Store.RaiseNotification(ctx, model.Notification{
		Subject: req.Subject, Severity: severity, Title: strings.TrimSpace(req.Title), Body: req.Body,
		TaskID: req.Task, Repo: strings.TrimSpace(req.Repo), RaisedBy: raisedBy, RaisedAt: c.now(),
		ClearsWhen: req.ClearsWhen, Actions: req.Actions,
	})
	if err != nil {
		if strings.Contains(err.Error(), "clears_when") || strings.Contains(err.Error(), "subject") {
			return NotificationRaiseResponse{}, validationErrorf("%v", err)
		}
		return NotificationRaiseResponse{}, err
	}
	c.afterRaise(ctx, raised)
	return NotificationRaiseResponse{Notification: raised.Notification, New: raised.New}, nil
}

// afterRaise delivers a new notification to the durable tasks subscribed
// to its family. A delivery that fails is logged, not returned -- the
// notification is raised either way.
func (c *Client) afterRaise(ctx context.Context, raised model.NotificationRaise) {
	if !raised.New {
		return
	}
	woke, err := c.Store.WakeNotificationSubscribers(ctx, raised.Notification, c.now())
	if err != nil {
		log.Printf("grain: waking subscribers of notification %d: %v", raised.Notification.ID, err)
	}
	if len(woke) > 0 && c.Config.Wake != nil {
		c.Config.Wake()
	}
}

// ListNotifications is GET /api/notifications.
func (c *Client) ListNotifications(ctx context.Context, f model.NotificationFilter) ([]model.Notification, error) {
	return c.Store.ListNotifications(ctx, f)
}

func (c *Client) notification(ctx context.Context, id int64) (*model.Notification, error) {
	n, err := c.Store.GetNotification(ctx, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, &NotFoundError{ID: strconv.FormatInt(id, 10)}
	}
	return n, nil
}

// AckNotification acknowledges id: it leaves the Alerts count and stays
// in the Inbox until it clears. A person's act: a run is refused.
func (c *Client) AckNotification(ctx context.Context, id int64) (model.Notification, error) {
	actor := c.actor(ctx)
	if actor.Kind == model.PrincipalAgent {
		return model.Notification{}, forbiddenErrorf("acknowledging a notification is a person's act")
	}
	if _, err := c.notification(ctx, id); err != nil {
		return model.Notification{}, err
	}
	if err := c.Store.AckNotification(ctx, id, model.PrincipalString(actor), c.now()); err != nil {
		return model.Notification{}, err
	}
	n, err := c.notification(ctx, id)
	if err != nil {
		return model.Notification{}, err
	}
	return *n, nil
}

// ClearNotification clears id. A run whose task does not hold
// api:task-admin may only clear what its own task raised.
func (c *Client) ClearNotification(ctx context.Context, id int64) (model.Notification, error) {
	actor := c.actor(ctx)
	n, err := c.notification(ctx, id)
	if err != nil {
		return model.Notification{}, err
	}
	if actor.Kind == model.PrincipalAgent && !strings.HasPrefix(n.Subject, AgentNamespace(actor.ID)) {
		task, err := c.Store.GetTask(ctx, actor.ID)
		if err != nil {
			return model.Notification{}, err
		}
		if !isTaskAdmin(task) {
			return model.Notification{}, forbiddenErrorf("task %s may only clear its own notifications (%s...) "+
				"without api:task-admin", actor.ID, AgentNamespace(actor.ID))
		}
	}
	if _, err := c.Store.ClearNotificationByID(ctx, id, model.PrincipalString(actor)); err != nil {
		return model.Notification{}, err
	}
	n, err = c.notification(ctx, id)
	if err != nil {
		return model.Notification{}, err
	}
	return *n, nil
}

// InvestigateResponse is POST /api/notifications/{id}/investigate's
// answer: the investigation's task, and whether this call filed it.
type InvestigateResponse struct {
	TaskID string `json:"taskId"`
	Filed  bool   `json:"filed"`
}

// InvestigateNotification files one task per notification instance from
// model.InvestigateNotificationTemplateID, approved as the operator's own
// act, and records it on the notification so a second click opens the
// same task. A person's act: a run is refused.
func (c *Client) InvestigateNotification(ctx context.Context, id int64) (InvestigateResponse, error) {
	if c.actor(ctx).Kind == model.PrincipalAgent {
		return InvestigateResponse{}, forbiddenErrorf("investigating a notification files an approved task, which is a person's act")
	}
	n, err := c.notification(ctx, id)
	if err != nil {
		return InvestigateResponse{}, err
	}
	if n.InvestigationTaskID != "" {
		return InvestigateResponse{TaskID: n.InvestigationTaskID}, nil
	}
	tmpl := model.SystemTemplate(model.InvestigateNotificationTemplateID, model.RepoRef{})
	caps := make([]string, 0, len(tmpl.Grants))
	for _, g := range tmpl.Grants {
		caps = append(caps, g.Capability)
	}
	task, err := c.CreateTask(ctx, CreateTaskRequest{
		Title:       "Investigate: " + n.Title,
		Description: model.InvestigateNotificationBody(*n),
		NoRepo:      true,
		// One task per instance, however often the button is pressed
		// before the first press has recorded it.
		ProposalKey:  fmt.Sprintf("notification-investigate:%d", n.ID),
		Capabilities: &caps,
	})
	if err != nil {
		return InvestigateResponse{}, err
	}
	if stored, err := c.Store.GetTask(ctx, task.ID); err == nil && stored != nil && stored.Approval == nil {
		if err := c.Approve(ctx, task.ID); err != nil {
			return InvestigateResponse{}, err
		}
	}
	recorded, err := c.Store.SetNotificationInvestigation(ctx, n.ID, task.ID)
	if err != nil {
		return InvestigateResponse{}, err
	}
	return InvestigateResponse{TaskID: recorded, Filed: recorded == task.ID}, nil
}

// PostTaskNote is message_task: a note on target's conversation from the
// agent working on from. It is authored as from's agent principal, never
// sets a pending question and never changes target's state; a live run
// is handed it as an addendum (MessagesSince), and a resting durable
// target is re-queued (model.Store.WakeRestingTask).
//
// It needs api:task-admin, or a sender that is the target's parent or
// child, so an arbitrary run cannot steer arbitrary tasks.
func (c *Client) PostTaskNote(ctx context.Context, target, from, body string) (Message, error) {
	from = model.NormalizeTaskID(strings.TrimSpace(from))
	target = model.NormalizeTaskID(target)
	if from == "" {
		return Message{}, validationErrorf("a note is from a task's agent; a person comments instead")
	}
	if strings.TrimSpace(body) == "" {
		return Message{}, errMessageEmpty
	}
	if from == target {
		return Message{}, validationErrorf("a note is for another task; to speak in your own task's conversation, just say it")
	}
	sender, err := c.Store.GetTask(ctx, from)
	if err != nil {
		return Message{}, err
	}
	if sender == nil {
		return Message{}, &NotFoundError{ID: from}
	}
	recipient, err := c.Store.GetTask(ctx, target)
	if err != nil {
		return Message{}, err
	}
	if recipient == nil {
		return Message{}, &NotFoundError{ID: target}
	}
	if !isTaskAdmin(sender) && !related(sender, recipient) {
		return Message{}, forbiddenErrorf("task %s may only message its parent or its children, and task %s is neither: "+
			"message_task to any other task needs api:task-admin", from, target)
	}
	now := c.now()
	id, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    target,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: from}},
		Body:      body,
		CreatedAt: now,
	})
	if err != nil {
		return Message{}, err
	}
	if model.IsDurableTask(*recipient) {
		reason := "a note from task " + from
		if recipient.IsConsoleSession() {
			reason = ""
		}
		woke, err := c.Store.WakeRestingTask(ctx, target, reason, now)
		if err != nil {
			return Message{}, err
		}
		if woke && c.Config.Wake != nil {
			c.Config.Wake()
		}
	}
	return Message{ID: id, Author: from, AuthorKind: string(model.PrincipalAgent), Body: body, At: now}, nil
}

// related reports whether a and b are parent and child: one proposed the
// other, or one is the other's child-of.
func related(a, b *model.Task) bool {
	links := func(t *model.Task, other string) bool {
		return slices.ContainsFunc(t.Links, func(l model.Link) bool {
			return (l.Kind == model.LinkProposedBy || l.Kind == model.LinkChildOf) &&
				model.NormalizeTaskID(l.Target) == other
		})
	}
	return links(a, b.ID) || links(b, a.ID)
}

// --- handlers ---------------------------------------------------------

func notificationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientError(w, validationErrorf("notification id must be a number"))
		return 0, false
	}
	return id, true
}

// handleRaiseNotification answers POST /api/notifications.
func (s *Server) handleRaiseNotification(w http.ResponseWriter, r *http.Request) {
	var req RaiseNotificationRequest
	if !readJSON(w, r, &req) {
		return
	}
	resp, err := s.tasks.RaiseNotification(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListNotifications answers GET /api/notifications?open=1&after=&family=.
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.NotificationFilter{Family: strings.TrimSpace(q.Get("family"))}
	switch strings.ToLower(q.Get("open")) {
	case "1", "true", "yes":
		f.Open = true
	}
	if raw := q.Get("after"); raw != "" {
		after, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeClientError(w, validationErrorf("after must be a notification id"))
			return
		}
		f.After = after
	}
	list, err := s.tasks.ListNotifications(r.Context(), f)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAckNotification answers POST /api/notifications/{id}/ack.
func (s *Server) handleAckNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	n, err := s.tasks.AckNotification(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// handleClearNotification answers POST /api/notifications/{id}/clear.
func (s *Server) handleClearNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	n, err := s.tasks.ClearNotification(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// handleInvestigateNotification answers POST /api/notifications/{id}/investigate.
func (s *Server) handleInvestigateNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	resp, err := s.tasks.InvestigateNotification(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAgentNotify answers POST /api/tasks/{id}/notify: the
// notify_operator tool's hop from `grain mcpserver`, for the task fixed
// at process start.
func (s *Server) handleAgentNotify(w http.ResponseWriter, r *http.Request) {
	var req RaiseNotificationRequest
	if !readJSON(w, r, &req) {
		return
	}
	taskID := r.PathValue("id")
	if p, ok := PrincipalFromContext(r.Context()); ok && !(p.Kind == model.PrincipalAgent && p.ID == taskID) {
		writeClientError(w, forbiddenErrorf("only task %s's own run may raise notifications as it", taskID))
		return
	}
	resp, err := s.tasks.AgentNotify(r.Context(), taskID, req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePostTaskNote answers POST /api/tasks/{id}/notes. A run with an
// agent principal sends as its own task whatever the body says.
func (s *Server) handlePostTaskNote(w http.ResponseWriter, r *http.Request) {
	var req NoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if p, ok := PrincipalFromContext(r.Context()); ok {
		req.From = ""
		if p.Kind == model.PrincipalAgent {
			req.From = p.ID
		}
	}
	msg, err := s.tasks.PostTaskNote(r.Context(), r.PathValue("id"), req.From, req.Body)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// --- the sandbox's hops -------------------------------------------------

// Notify is AgentNotify over the wire, for `grain mcpserver`'s
// notify_operator tool. The hop carries no principal and the server
// believes its path, so every id in it is escaped: an id is never a way
// to name another route.
func (c *HTTPClient) Notify(ctx context.Context, taskID string, req RaiseNotificationRequest) (NotificationRaiseResponse, error) {
	var resp NotificationRaiseResponse
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(taskID)+"/notify", req, &resp)
	return resp, err
}

// PostTaskNote is PostTaskNote over the wire, for `grain mcpserver`'s
// message_task tool. target is the agent's own argument, so it is escaped:
// "42/messages?" would otherwise reach POST /api/tasks/42/messages, with
// the trust the hop's missing principal is given.
func (c *HTTPClient) PostTaskNote(ctx context.Context, from, target, body string) (Message, error) {
	var resp Message
	err := c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(target)+"/notes", NoteRequest{Body: body, From: from}, &resp)
	return resp, err
}
