// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// external_cli.go is the server half of the external-agent workflow's
// polling and answering verbs (design/spec/external-cli/
// external-cli.spec.md): POST /api/tasks/{id}/reply (#XC-20), GET
// /api/inbox (#XC-21) and GET /api/tasks/wait (#XC-22).

// --- grain reply (#XC-20) ---------------------------------------------

// replyRefusedError is a reply the task is not waiting for: no question
// is parked on it, or what is parked is a secret request whose answer
// must never travel as a conversation comment. A 409, posting nothing.
type replyRefusedError struct{ msg string }

func (e *replyRefusedError) Error() string { return e.msg }

// CheckReply says whether task id is parked on an ask_question prompt
// a reply answers, and why not when it is not.
func (c *Client) CheckReply(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	if obs != nil && obs.ClosedAt == nil && obs.PendingSecret != "" {
		return &replyRefusedError{msg: fmt.Sprintf("task %s is parked on a secret request (%s), not a question: "+
			"set it in the encrypted secret input on /tasks/%s -- grain reply never posts a credential into a conversation",
			id, obs.PendingSecret, id)}
	}
	if obs == nil || obs.ClosedAt != nil || obs.CompletedAt != nil || obs.PendingQuestionCommentID == nil {
		return &replyRefusedError{msg: fmt.Sprintf("task %s is not parked on a question, so there is nothing to reply to: "+
			"use grain comment %s for a general note", id, id)}
	}
	if obs.PendingConfigChange != nil || obs.PendingAccess != nil {
		return &replyRefusedError{msg: fmt.Sprintf("task %s is parked on a config-change or access request, not a question: "+
			"decide it on /tasks/%s", id, id)}
	}
	return nil
}

// handleReplyTask answers a parked question: the reply is a comment,
// written exactly as POST /api/tasks/{id}/comments writes one (its
// author, filed_via and the re-queue a human's comment on an
// awaiting_reply task brings), but only once CheckReply has said the
// task is waiting for one.
func (s *Server) handleReplyTask(w http.ResponseWriter, r *http.Request) {
	r = withFiledVia(r)
	var req addCommentRequest
	if !readAttachmentJSON(w, r, &req, func(req *addCommentRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(req.Body) == "" && len(req.Attachments) == 0 {
		writeClientError(w, validationErrorf("a reply needs a body or an attachment"))
		return
	}
	if err := s.tasks.CheckReply(r.Context(), id); err != nil {
		var refused *replyRefusedError
		if errors.As(err, &refused) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeClientError(w, err)
		return
	}
	if err := s.tasks.AddCommentWithAuthor(r.Context(), id, req.Body, req.Attachments, requestPerson(r.Context())); err != nil {
		writeClientError(w, err)
		return
	}
	_ = s.tasks.noteRequesterTimeZone(r.Context(), id, requestTimeZone(r))
	s.respondWithTask(w, r, id)
}

// --- grain inbox (#XC-21) ---------------------------------------------

// The kinds an InboxItem is.
const (
	InboxQuestion     = "question"
	InboxSecret       = "secret"
	InboxNotification = "notification"
	InboxMergeHeld    = "merge-held"
	InboxProposal     = "proposal"
)

// InboxKinds is every InboxItem.Kind, in the order GET /api/inbox lists
// them.
var InboxKinds = []string{InboxQuestion, InboxSecret, InboxNotification, InboxMergeHeld, InboxProposal}

// InboxItem is one actionable thing the UI's Inbox surfaces. Path is the
// root-relative UI route it is answered on.
type InboxItem struct {
	Kind         string     `json:"kind"`
	Task         string     `json:"task,omitempty"`
	Title        string     `json:"title,omitempty"`
	Repo         string     `json:"repo,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	Path         string     `json:"path"`
	Notification int64      `json:"notification,omitempty"`
	Severity     string     `json:"severity,omitempty"`
	Subject      string     `json:"subject,omitempty"`
	PR           string     `json:"pr,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	Proposer     string     `json:"proposer,omitempty"`
	At           *time.Time `json:"at,omitempty"`
}

// InboxResponse is GET /api/inbox's body.
type InboxResponse struct {
	Items      []InboxItem `json:"items"`
	Total      int         `json:"total,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

// handleInbox answers GET /api/inbox?repo=&kind=: the questions and
// secret requests tasks are parked on, open warning and critical
// notifications, pull requests held at a gate that needs a person, and
// unapproved proposals -- only on repositories the caller may read.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	if kind != "" && !slices.Contains(InboxKinds, kind) {
		writeClientError(w, validationErrorf("kind %q is not one of %s", kind, strings.Join(InboxKinds, ", ")))
		return
	}
	readable := s.inboxReadable(r)
	if repo := strings.TrimSpace(q.Get("repo")); repo != "" {
		all := readable
		readable = func(r string) bool { return strings.EqualFold(r, repo) && all(r) }
	}
	items, err := s.tasks.inbox(r.Context(), &s.taskList, readable)
	if err != nil {
		writeClientError(w, err)
		return
	}
	filtered := make([]InboxItem, 0, len(items))
	for _, it := range items {
		if (kind == "" || it.Kind == kind) && list.matches(it.Task, it.Title, it.Repo, it.Summary, it.Subject, it.PR) {
			filtered = append(filtered, it)
		}
	}
	out := InboxResponse{Items: filtered}
	if list.Paged {
		page := pageOfBy(filtered, list, inboxItemKey)
		out.Items = page.Items
		out.Total = page.Total
		out.Truncated = page.Truncated
		out.NextCursor = page.NextCursor
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, out)
}

// inboxItemKey names an inbox row for paging (pageOfBy): its kind with
// the task, notification and pull request it is about, which together
// tell any two rows apart.
func inboxItemKey(it InboxItem) string {
	return it.Kind + "\x00" + it.Task + "\x00" + strconv.FormatInt(it.Notification, 10) + "\x00" + it.PR + "\x00" + it.Path
}

// inboxReadable is which repositories the caller's tasks:read reaches:
// an external-agent token's own grants; a limited role's, or an agent
// run's, as the event stream reads them (eventsReadable); and everything
// for anyone else (the access code, the local CLI). A role limited to
// some repositories was once shown every repository's inbox -- titles,
// questions and pending secret names included (task 3981).
func (s *Server) inboxReadable(r *http.Request) func(string) bool {
	tok := externalTokenFromContext(r.Context())
	if tok == nil {
		readable, _ := s.eventsReadable(r)
		return readable
	}
	_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
	if err != nil {
		return func(string) bool { return false }
	}
	return func(repo string) bool { return set.Allows("tasks:read", repo) }
}

func (c *Client) inbox(ctx context.Context, cache *taskListCache, readable func(string) bool) ([]InboxItem, error) {
	tasks, err := cache.list(ctx, c)
	if err != nil {
		return nil, err
	}
	sortTasksByFiling(tasks)
	var questions, secrets, held, proposals []InboxItem
	for _, t := range tasks {
		if t.State == model.StateClosed || !readable(t.Repo) {
			continue
		}
		path := "/tasks/" + t.ID
		switch {
		case t.State == model.StateAwaitingReply:
			obs, err := c.Store.GetObservation(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			if obs != nil && obs.PendingSecret != "" {
				secrets = append(secrets, InboxItem{Kind: InboxSecret, Task: t.ID, Title: t.Title, Repo: t.Repo,
					Summary: "set " + obs.PendingSecret + " in the encrypted secret input", Path: path, At: t.EndedAt})
				continue
			}
			summary := t.QuestionSummary
			if summary == "" {
				summary = t.NoteSummary
			}
			if summary == "" {
				summary = "reply to run it again, or acknowledge to close it"
			}
			// A question's item links to the question itself, not just
			// to the top of the task's conversation.
			if obs != nil && obs.PendingQuestionCommentID != nil {
				path = model.CommentPath(t.ID, *obs.PendingQuestionCommentID)
			}
			questions = append(questions, InboxItem{Kind: InboxQuestion, Task: t.ID, Title: t.Title, Repo: t.Repo,
				Summary: summary, Path: path, At: t.EndedAt})
		case t.State == model.StateProposed:
			proposer := t.GeneratedFrom
			if proposer != "" {
				proposer = "task " + proposer
			} else {
				proposer = t.Creator
			}
			reason := t.ApprovalNote
			if reason == "" && t.HeldBy != "" {
				reason = "held for " + t.HeldBy
			}
			proposals = append(proposals, InboxItem{Kind: InboxProposal, Task: t.ID, Title: t.Title, Repo: t.Repo,
				Proposer: proposer, Reason: reason, Path: path, At: t.CreatedAt})
		case t.PullRequest != "":
			if reason := mergeHeldReason(t); reason != "" {
				held = append(held, InboxItem{Kind: InboxMergeHeld, Task: t.ID, Title: t.Title, Repo: t.Repo,
					PR: t.PullRequest, Reason: reason, Path: path, At: t.MergeQueueStateAt})
			}
		}
	}
	notes, err := c.ListNotifications(ctx, model.NotificationFilter{Open: true})
	if err != nil {
		return nil, err
	}
	heldTasks := map[string]bool{}
	for _, h := range held {
		heldTasks[h.Task] = true
	}
	var notifications []InboxItem
	for _, n := range notes {
		if n.Severity != model.NotificationWarning && n.Severity != model.NotificationCritical {
			continue
		}
		if heldTasks[n.TaskID] && (n.Subject == model.HumanReviewNotificationSubject(n.TaskID) ||
			n.Subject == model.NotificationSubject(model.FamilyMergeNeedsPerson, n.TaskID)) {
			// The merge-held item already says it, with the reason.
			continue
		}
		repo := n.Repo
		if repo == "" && n.TaskID != "" && !readable("") {
			// A task's notice raised without a repository (grain notify)
			// is that task's repository's, and a token held to others
			// must not read it.
			t, err := c.Store.GetTask(ctx, n.TaskID)
			if err != nil {
				return nil, err
			}
			if t == nil || !readable(taskRepo(t)) {
				continue
			}
		}
		if repo != "" && !readable(repo) {
			continue
		}
		if repo == "" && !readable("") && n.TaskID == "" {
			// A deployment-wide notice is for whoever reads the whole
			// deployment, not a token held to some repositories.
			continue
		}
		path := "/inbox"
		if n.TaskID != "" {
			path = "/tasks/" + n.TaskID
		}
		at := n.LastRaisedAt
		notifications = append(notifications, InboxItem{Kind: InboxNotification, Notification: n.ID, Task: n.TaskID,
			Repo: n.Repo, Severity: string(n.Severity), Subject: n.Subject, Title: n.Title, Path: path, At: &at})
	}
	out := make([]InboxItem, 0, len(questions)+len(secrets)+len(notifications)+len(held)+len(proposals))
	out = append(out, questions...)
	out = append(out, secrets...)
	out = append(out, notifications...)
	out = append(out, held...)
	out = append(out, proposals...)
	return out, nil
}

// mergeHeldReason is why t's pull request is held at a merge gate a
// person has to act on, or "" when nothing holds it there.
func mergeHeldReason(t Task) string {
	switch {
	case len(t.HumanReviewHold) > 0:
		return "held for a person's review: " + humanReviewReasonsLine(t.HumanReviewHold)
	case t.SecurityHold != "":
		return "held for a security review (task " + t.SecurityHold + ")"
	case t.PathTriggerHold != "":
		return "held by a path trigger (task " + t.PathTriggerHold + ")"
	case t.MergeQueueBlockedAt != nil:
		return cmpOr(t.MergeQueueNote, "the merge queue could not land it")
	case t.MergeQueueStalledAt != nil:
		return cmpOr(t.MergeQueueNote, "the merge queue has stalled on it")
	case t.State == model.StateAwaitingSubmit:
		return "waiting for a person to Submit it to the merge queue"
	}
	return ""
}

// humanReviewReasonsLine is a hold's reasons on one line.
func humanReviewReasonsLine(rs []HumanReviewView) string {
	var parts []string
	for _, r := range rs {
		if !slices.Contains(parts, r.Reason) {
			parts = append(parts, r.Reason)
		}
	}
	return strings.Join(parts, "; ")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- GET /api/tasks/wait (#XC-22) -------------------------------------

// The conditions GET /api/tasks/wait's until names.
const (
	WaitUntilClosed        = "closed"
	WaitUntilMerged        = "merged"
	WaitUntilAwaitingReply = "awaiting-reply"
	WaitUntilCIDone        = "ci-done"
	WaitUntilAny           = "any"
)

// WaitUntilConditions is every condition, in the order they are checked.
var WaitUntilConditions = []string{WaitUntilClosed, WaitUntilMerged, WaitUntilAwaitingReply, WaitUntilCIDone, WaitUntilAny}

// MaxTasksWaitHold is the longest one GET /api/tasks/wait holds its
// request; `grain wait` re-issues slices of it up to its own --timeout.
const MaxTasksWaitHold = 60 * time.Second

// maxTasksWaitIDs bounds how many tasks one wait names.
const maxTasksWaitIDs = 100

// TaskWaitCI is a task's latest grain CI verdict, as a wait reports it.
type TaskWaitCI struct {
	RunID      string `json:"runId,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// TaskWaitState is one task as GET /api/tasks/wait reports it.
type TaskWaitState struct {
	model.WaitTarget
	PRUrl         string      `json:"prUrl,omitempty"`
	PullRequest   string      `json:"pullRequest,omitempty"`
	Question      string      `json:"question,omitempty"`
	PendingSecret string      `json:"pendingSecret,omitempty"`
	CI            *TaskWaitCI `json:"ci,omitempty"`
	Revision      string      `json:"revision"`

	// liveAttempt is whether the task has a run that has not finished.
	// Not reported: it is what keeps until=awaiting-reply from waking on
	// a question a run is still in the middle of parking on.
	liveAttempt bool
}

// TasksWaitResponse is GET /api/tasks/wait's body. Met is whether one of
// the conditions holds, Condition which, and Failed that the one met is
// a terminal state the caller can never get what it waited for out of
// (a task closed unmerged under until=merged, or failed under closed).
// Revision is the whole set's: pass it back as since= so a wait until
// "any" wakes on the next change rather than on one already seen.
type TasksWaitResponse struct {
	Met       bool            `json:"met"`
	Condition string          `json:"condition,omitempty"`
	Failed    bool            `json:"failed,omitempty"`
	TimedOut  bool            `json:"timedOut,omitempty"`
	Revision  string          `json:"revision"`
	Tasks     []TaskWaitState `json:"tasks"`
}

// tasksWaitQuery is GET /api/tasks/wait's query, read and checked.
type tasksWaitQuery struct {
	ids     []string
	until   []string
	timeout time.Duration
	since   string
}

func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
				out = append(out, part)
			}
		}
	}
	return out
}

// TasksWaitIDs is the task ids GET /api/tasks/wait names, repeated or
// comma-separated -- what the agent API gate checks every one of.
func TasksWaitIDs(r *http.Request) []string { return splitList(r.URL.Query()["ids"]) }

func parseTasksWaitQuery(r *http.Request) (tasksWaitQuery, error) {
	q := r.URL.Query()
	out := tasksWaitQuery{ids: TasksWaitIDs(r), until: splitList(q["until"]), timeout: MaxTasksWaitHold,
		since: strings.TrimSpace(q.Get("since"))}
	if len(out.ids) == 0 {
		return out, validationErrorf("name at least one task in ids")
	}
	if len(out.ids) > maxTasksWaitIDs {
		return out, validationErrorf("at most %d tasks in one wait", maxTasksWaitIDs)
	}
	if len(out.until) == 0 {
		out.until = []string{WaitUntilClosed}
	}
	for _, u := range out.until {
		if !slices.Contains(WaitUntilConditions, u) {
			return out, validationErrorf("until %q is not one of %s", u, strings.Join(WaitUntilConditions, ", "))
		}
	}
	if raw := strings.TrimSpace(q.Get("timeout")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return out, validationErrorf("timeout must be a whole number of seconds")
		}
		out.timeout = time.Duration(min(max(n, 1), int(MaxTasksWaitHold/time.Second))) * time.Second
	}
	return out, nil
}

// handleTasksWait holds the request until one of until's conditions
// holds over ids, or timeout (at most MaxTasksWaitHold) passes. It
// evaluates on entry, so a condition already true answers at once, and
// again each time the store commits a write (model.Store.Changed) --
// never on a timer of its own. Nothing here touches wait credit: that is
// an in-grain run's (orchestrator.RunWaitCredits), and an external
// caller has no attempt deadline to credit.
func (s *Server) handleTasksWait(w http.ResponseWriter, r *http.Request) {
	q, err := parseTasksWaitQuery(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	ctx := r.Context()
	for _, id := range q.ids {
		t, err := s.tasks.Store.GetTask(ctx, id)
		if err != nil {
			writeClientError(w, err)
			return
		}
		if t == nil {
			writeClientError(w, &NotFoundError{ID: id})
			return
		}
	}
	timer := time.NewTimer(q.timeout)
	defer timer.Stop()
	for {
		changed := s.tasks.Store.Changed()
		resp, err := s.tasks.evaluateTasksWait(ctx, q)
		if err != nil {
			writeClientError(w, err)
			return
		}
		if q.since == "" {
			// "any" waits for a change from what it saw on entry.
			q.since = resp.Revision
		}
		if resp.Met {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			resp.TimedOut = true
			writeJSON(w, http.StatusOK, resp)
			return
		case <-ctx.Done():
			return
		}
	}
}

// evaluateTasksWait reads every task q names and says whether any of
// its conditions holds.
func (c *Client) evaluateTasksWait(ctx context.Context, q tasksWaitQuery) (TasksWaitResponse, error) {
	now := c.now()
	resp := TasksWaitResponse{Tasks: make([]TaskWaitState, 0, len(q.ids))}
	sum := sha256.New()
	for _, id := range q.ids {
		st, err := c.taskWaitState(ctx, id, now)
		if err != nil {
			return resp, err
		}
		resp.Tasks = append(resp.Tasks, st)
		fmt.Fprintf(sum, "%s=%s;", id, st.Revision)
	}
	resp.Revision = hex.EncodeToString(sum.Sum(nil))[:16]
	for _, u := range q.until {
		met, failed := tasksWaitMet(u, resp.Tasks, q.since, resp.Revision)
		if met {
			resp.Met, resp.Condition, resp.Failed = true, u, failed
			break
		}
	}
	return resp, nil
}

func (c *Client) taskWaitState(ctx context.Context, id string, now time.Time) (TaskWaitState, error) {
	target, err := c.Store.WaitTargetAt(ctx, id, now)
	if err != nil {
		return TaskWaitState{}, err
	}
	if target == nil {
		return TaskWaitState{}, &NotFoundError{ID: id}
	}
	st := TaskWaitState{WaitTarget: *target}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return st, err
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return st, err
	}
	if task != nil {
		for _, l := range task.Links {
			if l.Kind == model.LinkFixes && !model.LinkOffTarget(*task, l) {
				st.PullRequest = l.Target
				st.PRUrl = pullRequestWebURL(l.Target)
			}
		}
	}
	pendingQ := ""
	if obs != nil {
		if obs.CI != nil {
			st.CI = &TaskWaitCI{RunID: obs.CI.RunID, Commit: obs.CI.Commit, Conclusion: obs.CI.Conclusion, Detail: obs.CI.Detail}
		}
		if obs.ClosedAt == nil && obs.PendingQuestionCommentID != nil {
			pendingQ = strconv.FormatInt(*obs.PendingQuestionCommentID, 10)
			st.PendingSecret = obs.PendingSecret
			if cm, err := c.Store.Comment(ctx, id, *obs.PendingQuestionCommentID); err == nil && cm != nil {
				st.Question = cm.Body
			}
		}
	}
	runs, err := c.Store.Runs(ctx, id)
	if err != nil {
		return st, err
	}
	attempts := len(runs)
	for _, r := range runs {
		if r.FinishedAt == nil {
			st.liveAttempt = true
		}
	}
	ci := ""
	if st.CI != nil {
		ci = st.CI.RunID + "/" + st.CI.Conclusion
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%t|%t|%s|%s|%s|%d|%t",
		st.State, st.Closed, st.Merged, st.Outcome, ci, pendingQ, attempts, st.liveAttempt)))
	st.Revision = hex.EncodeToString(sum[:])[:16]
	return st, nil
}

// tasksWaitMet is whether condition holds over tasks, and whether what
// holds is a terminal failure.
func tasksWaitMet(condition string, tasks []TaskWaitState, since, revision string) (met, failed bool) {
	switch condition {
	case WaitUntilClosed:
		for _, t := range tasks {
			if !t.Closed && t.Outcome != model.WaitOutcomeFailed {
				return false, false
			}
			if t.Outcome == model.WaitOutcomeFailed {
				failed = true
			}
		}
		return true, failed
	case WaitUntilMerged:
		all := true
		for _, t := range tasks {
			if t.Merged {
				continue
			}
			all = false
			if t.Outcome == model.WaitOutcomeFailed || (t.Closed && !t.PullRequestOpen) {
				return true, true
			}
		}
		return all, false
	case WaitUntilAwaitingReply:
		// Parked, not merely asking. A run's question comment and its
		// PendingQuestionCommentID are written while the run is still
		// finishing (orchestrator.relayParkingCalls, or the chat tool
		// mid-run), and the state, observation and runs are separate
		// reads, so a question can be seen beside a state that is still
		// running or queued. Waking then hands the caller a task a reply
		// may yet be refused on. Only the derived awaiting_reply with no
		// attempt left live counts; the commit that finishes the run or
		// sets the marker wakes the wait again to see it.
		for _, t := range tasks {
			if t.State == model.StateAwaitingReply && !t.liveAttempt {
				return true, false
			}
		}
	case WaitUntilCIDone:
		for _, t := range tasks {
			if t.CI != nil && t.CI.Conclusion != "" {
				return true, false
			}
		}
	case WaitUntilAny:
		return since != "" && since != revision, false
	}
	return false, false
}

// pullRequestWebURL is a stored owner/name#number pull request reference
// as the link a person opens: github.com's for owner/name, the host's
// own for host/owner/name.
func pullRequestWebURL(ref string) string {
	repo, num, ok := strings.Cut(ref, "#")
	if !ok || repo == "" || num == "" {
		return ""
	}
	if strings.Count(repo, "/") == 1 {
		repo = "github.com/" + repo
	}
	return "https://" + repo + "/pull/" + num
}

// --- HTTPClient -----------------------------------------------------------

// Reply is POST /api/tasks/{id}/reply: answer the question task id is
// parked on. A task with no question parked, or parked on a secret
// request, is a 409 that posts nothing.
func (c *HTTPClient) Reply(ctx context.Context, id, body string, attachments []AttachmentUpload) error {
	return c.do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/reply",
		addCommentRequest{Body: body, Attachments: attachments}, nil)
}

// Inbox is GET /api/inbox, narrowed to repo and kind when they are set.
func (c *HTTPClient) Inbox(ctx context.Context, repo, kind string) (InboxResponse, error) {
	return c.InboxPage(ctx, repo, kind, "", 0, "")
}

// InboxPage is Inbox asking for one page: at most limit items (0 for the
// route's own default), starting at cursor (a previous answer's
// NextCursor), narrowed by the substring search.
func (c *HTTPClient) InboxPage(ctx context.Context, repo, kind, search string, limit int, cursor string) (InboxResponse, error) {
	q := url.Values{}
	if search != "" {
		q.Set("q", search)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	path := "/api/inbox"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out InboxResponse
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// WaitTasks is one slice of GET /api/tasks/wait: held up to timeout
// (at most MaxTasksWaitHold) for one of until to hold over ids.
func (c *HTTPClient) WaitTasks(ctx context.Context, ids, until []string, timeout time.Duration, since string) (TasksWaitResponse, error) {
	q := url.Values{}
	q.Set("ids", strings.Join(ids, ","))
	q.Set("until", strings.Join(until, ","))
	q.Set("timeout", strconv.Itoa(max(1, int(timeout.Round(time.Second)/time.Second))))
	if since != "" {
		q.Set("since", since)
	}
	var out TasksWaitResponse
	err := c.do(ctx, http.MethodGet, "/api/tasks/wait?"+q.Encode(), nil, &out)
	return out, err
}

// GetTaskCommentsSince is GetTask with only the conversation items
// created strictly after since (?comments_since=).
func (c *HTTPClient) GetTaskCommentsSince(ctx context.Context, id string, since time.Time) (TaskDetail, error) {
	var detail TaskDetail
	path := "/api/tasks/" + url.PathEscape(id) + "?comments_since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	err := c.do(ctx, http.MethodGet, path, nil, &detail)
	return detail, err
}
