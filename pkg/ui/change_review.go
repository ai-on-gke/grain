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
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A CL's review loop (changelists.md §5, G3-5), as the daemon answers it:
// a run's replies and resolutions on the CL's threads, which the
// provider saves as drafts, and a person's Publish replies and Mail.

// The ops a run's mcpserver asks for on POST /api/tasks/{id}/change-review.
const (
	ChangeReviewReply   = "reply"
	ChangeReviewResolve = "resolve"
)

// ChangeReviewRequest is POST /api/tasks/{id}/change-review.
type ChangeReviewRequest struct {
	Op     string `json:"op"`
	Thread string `json:"thread"`
	Body   string `json:"body,omitempty"`
}

// ChangeReviewResponse is its answer.
type ChangeReviewResponse struct {
	Result change.ReplyResult `json:"result"`
}

// changeReviewer is who posts a degraded reply in a task's conversation.
var changeReviewer = model.Principal{Kind: model.PrincipalAutomation, ID: "cl-review"}

var errNoChangeReview = errors.New("this task's change has no review provider on this deployment")

// ChangeReview answers a run's reply or resolve on one of its CL's
// threads through the registered change.Replier, attributed to the
// task's live run. A provider without a write path answers degraded:
// the reply is posted in the task's conversation instead, saying so.
func (c *Client) ChangeReview(ctx context.Context, id string, req ChangeReviewRequest) (ChangeReviewResponse, error) {
	task, p, err := c.changeReviewTask(ctx, id)
	if err != nil {
		return ChangeReviewResponse{}, err
	}
	replier, ok := p.(change.Replier)
	if !ok {
		return ChangeReviewResponse{}, errNoChangeReview
	}
	thread := strings.TrimSpace(req.Thread)
	if thread == "" {
		return ChangeReviewResponse{}, validationErrorf("thread_id is required: pull_request_status lists this CL's threads with their IDs")
	}
	if !ownThread(*task, thread) {
		return ChangeReviewResponse{}, validationErrorf("thread %s is not on this task's CL -- nothing was drafted. "+
			"Thread IDs look like cl/<number>:<comment id>, as pull_request_status lists them.", thread)
	}
	attempt, err := c.liveRunID(ctx, task.ID)
	if err != nil {
		return ChangeReviewResponse{}, err
	}
	var res change.ReplyResult
	switch req.Op {
	case ChangeReviewReply:
		if strings.TrimSpace(req.Body) == "" {
			return ChangeReviewResponse{}, validationErrorf("body is required and must not be empty")
		}
		res, err = replier.Reply(ctx, *task, attempt, thread, req.Body)
	case ChangeReviewResolve:
		res, err = replier.Resolve(ctx, *task, attempt, thread)
	default:
		return ChangeReviewResponse{}, validationErrorf("unknown change-review op %q", req.Op)
	}
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		return ChangeReviewResponse{}, apierror.Expose(err)
	}
	if res.Degraded {
		if err := c.postDegradedReply(ctx, task.ID, thread, req.Op, res); err != nil {
			return ChangeReviewResponse{}, err
		}
	}
	return ChangeReviewResponse{Result: res}, nil
}

// postDegradedReply puts what could not be drafted on the CL in the
// task's conversation, with the provider's note saying why.
func (c *Client) postDegradedReply(ctx context.Context, taskID, thread, op string, res change.ReplyResult) error {
	var b strings.Builder
	if op == ChangeReviewResolve {
		fmt.Fprintf(&b, "This run marks thread %s resolved, but grain cannot write to the CL's threads here, so it is not resolved there.", thread)
	} else {
		fmt.Fprintf(&b, "This run's reply to thread %s, which grain cannot save on the CL here -- copy it there if it should be read:", thread)
	}
	if text := strings.TrimSpace(res.Text); text != "" {
		b.WriteString("\n\n")
		b.WriteString(text)
	}
	if note := strings.TrimSpace(res.Note); note != "" {
		b.WriteString("\n\n_")
		b.WriteString(note)
		b.WriteString("_")
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: model.Attribution{Actor: changeReviewer}, Body: b.String(), CreatedAt: c.now(),
	}); err != nil {
		return fmt.Errorf("posting a reply grain could not draft on the CL: %w", err)
	}
	return nil
}

// ownThread reports whether thread is on one of task's own CLs.
func ownThread(task model.Task, thread string) bool {
	for _, l := range change.Changes(task) {
		if l.Kind == model.LinkChangelist && strings.HasPrefix(thread, l.Target+":") {
			return true
		}
	}
	return false
}

func (c *Client) changeReviewTask(ctx context.Context, id string) (*model.Task, change.Provider, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, &NotFoundError{ID: id}
	}
	p, target := change.ReviewOf(*task)
	if target == "" {
		return nil, nil, validationErrorf("this task has no CL yet, so it has nothing to review: " +
			"call open_pull_request with a summary and body to create and upload it. " + noCLYetNote)
	}
	if p == nil {
		return nil, nil, errNoChangeReview
	}
	if h := change.HealthOf(change.HealthKind(p)); h.Blocked != "" {
		return nil, nil, apierror.Expose(&change.CredentialsExpiredError{Kind: change.HealthKind(p),
			Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339)})
	}
	return task, p, nil
}

// liveRunID is task id's live run, or its latest when none is live: the
// attempt a draft is attributed to and published with.
func (c *Client) liveRunID(ctx context.Context, id string) (string, error) {
	runs, err := c.Store.Runs(ctx, id)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", validationErrorf("task %s has no run to attribute a draft to", id)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Attempt < runs[j].Attempt })
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].FinishedAt == nil {
			return runs[i].ID, nil
		}
	}
	return runs[len(runs)-1].ID, nil
}

// PublishReplies is the Publish replies button: every draft the task's
// runs left on its CLs, published as by.
func (c *Client) PublishReplies(ctx context.Context, id, by string) (change.PublishResult, error) {
	task, p, err := c.changeReviewTask(ctx, id)
	if err != nil {
		return change.PublishResult{}, err
	}
	pub, ok := p.(change.Publisher)
	if !ok {
		return change.PublishResult{}, errNoChangeReview
	}
	res, err := pub.PublishReplies(ctx, *task, change.Person{Name: by})
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		return change.PublishResult{}, apierror.Expose(err)
	}
	change.RememberPublish(task.ID, res)
	// Like Sync, the press is the person's, so it is in the conversation.
	if _, err := c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: c.actor(ctx)},
		Body: publishedNote(res), CreatedAt: c.now()}); err != nil {
		return change.PublishResult{}, err
	}
	return res, nil
}

// publishedNote is a Publish replies press as the task's conversation
// says it: how many replies went out on which CLs, and what is left.
func publishedNote(res change.PublishResult) string {
	published, cls := 0, []string{}
	for _, c := range res.CLs {
		if c.Published > 0 {
			published += c.Published
			cls = append(cls, c.CL)
		}
	}
	var b strings.Builder
	switch published {
	case 0:
		b.WriteString("Pressed Publish replies; no replies were published.")
	case 1:
		fmt.Fprintf(&b, "Published 1 reply on %s.", strings.Join(cls, ", "))
	default:
		fmt.Fprintf(&b, "Published %d replies on %s.", published, strings.Join(cls, ", "))
	}
	if n, reason := res.Waiting(); n > 0 {
		fmt.Fprintf(&b, " %d still waiting", n)
		if reason != "" {
			b.WriteString(": " + reason)
		}
		b.WriteString(".")
	}
	return b.String()
}

// MailChangeRequest is POST /api/tasks/{id}/change-mail.
type MailChangeRequest struct {
	Reviewers []string `json:"reviewers,omitempty"`
	CC        []string `json:"cc,omitempty"`
}

// MailChange is the Mail button: the task's primary CL mailed to its
// reviewers, as by. With no reviewers named, the provider applies the
// deployment's reviewer policy.
func (c *Client) MailChange(ctx context.Context, id, by string, req MailChangeRequest) (change.Result, error) {
	task, p, err := c.changeReviewTask(ctx, id)
	if err != nil {
		return change.Result{}, err
	}
	mailer, ok := p.(change.Mailer)
	if !ok {
		return change.Result{}, errNoChangeReview
	}
	reviewers := req.Reviewers
	if len(reviewers) == 0 {
		reviewers = task.Reviewers
	}
	target := change.Primary(*task)
	res, err := mailer.Mail(ctx, *task, target, reviewers, req.CC, change.Person{Name: by})
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		return change.Result{}, apierror.Expose(err)
	}
	// The CL is mailed now, whatever the last poll saw: the Mail button
	// goes until the next observation says otherwise, rather than
	// offering a second mail for up to a poll interval.
	if obs, ok := change.LastObserved(target); ok && obs.NeedsMailing {
		obs.NeedsMailing = false
		if obs.ReviewState == change.ReviewStateDraft {
			obs.ReviewState = change.ReviewStateInReview
		}
		change.Remember(obs)
	}
	body := "Mailed " + target
	if len(reviewers) > 0 {
		body += " to " + strings.Join(reviewers, ", ")
	}
	if len(req.CC) > 0 {
		body += " (cc " + strings.Join(req.CC, ", ") + ")"
	}
	body += "."
	if res.Message != "" {
		body += " " + res.Message
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: c.actor(ctx)},
		Body: body, CreatedAt: c.now()}); err != nil {
		return change.Result{}, err
	}
	return res, nil
}

// ChangeReview is a CL's review as the task view and inbox show it
// beside the CL chip (the edition's ReviewBadge shape), with whether
// its Mail and Publish replies buttons apply and why not.
type ChangeReview struct {
	State         string   `json:"state"`
	LGTM          []string `json:"lgtm,omitempty"`
	NeedsApproval []string `json:"needsApproval,omitempty"`
	NeedsMailing  bool     `json:"needsMailing,omitempty"`
	// CanMail is false with MailReason saying why: a CL already mailed,
	// or a deployment whose provider cannot mail.
	CanMail    bool   `json:"canMail"`
	MailReason string `json:"mailReason,omitempty"`
	// Drafts is how many replies wait to be published; PublishReason is
	// why they did not publish at the run's end, or why the button does
	// not apply.
	Drafts        int    `json:"drafts,omitempty"`
	CanPublish    bool   `json:"canPublish"`
	PublishReason string `json:"publishReason,omitempty"`
}

// changeReview is task's primary CL's review, nil until it is observed.
func changeReview(taskID, target string) *ChangeReview {
	obs, ok := change.LastObserved(target)
	if !ok || obs.ReviewState == "" {
		return nil
	}
	out := &ChangeReview{State: obs.ReviewState, LGTM: obs.LGTM, NeedsApproval: obs.NeedsApproval, NeedsMailing: obs.NeedsMailing}
	p, _ := change.Lookup(model.LinkChangelist)
	if _, ok := p.(change.Mailer); !ok {
		out.MailReason = "this deployment cannot mail CLs"
	} else if !obs.NeedsMailing && obs.ReviewState != change.ReviewStateDraft {
		// A draft has not been mailed, whatever its provider said of
		// NeedsMailing: mailing is what takes a CL out of draft
		// (MailChange), and a draft refused Mail as "already mailed"
		// told its reader something false (task 1860's cl/1010132826).
		out.MailReason = "already mailed"
	} else {
		out.CanMail = true
	}
	out.Drafts, out.PublishReason = change.WaitingDrafts(taskID)
	switch _, ok := p.(change.Publisher); {
	case !ok:
		out.PublishReason = "this deployment cannot publish CL replies"
	case out.Drafts == 0:
		out.PublishReason = "no replies are waiting"
	default:
		out.CanPublish = true
	}
	return out
}

// handleChangeReview answers POST /api/tasks/{id}/change-review.
func (s *Server) handleChangeReview(w http.ResponseWriter, r *http.Request) {
	var req ChangeReviewRequest
	if !readJSON(w, r, &req) {
		return
	}
	resp, err := s.tasks.ChangeReview(r.Context(), r.PathValue("id"), req)
	if errors.Is(err, errNoChangeReview) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePublishReplies answers POST /api/tasks/{id}/change-publish.
func (s *Server) handlePublishReplies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.tasks.PublishReplies(r.Context(), id, actingUser(r, "")); err != nil {
		if errors.Is(err, errNoChangeReview) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleMailChange answers POST /api/tasks/{id}/change-mail.
func (s *Server) handleMailChange(w http.ResponseWriter, r *http.Request) {
	var req MailChangeRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.tasks.MailChange(r.Context(), id, actingUser(r, ""), req); err != nil {
		if errors.Is(err, errNoChangeReview) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// ChangeReview is Client.ChangeReview over the wire, for a run's mcpserver.
func (c *HTTPClient) ChangeReview(ctx context.Context, id string, req ChangeReviewRequest) (ChangeReviewResponse, error) {
	var resp ChangeReviewResponse
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/change-review", req, &resp); err != nil {
		return ChangeReviewResponse{}, fmt.Errorf("asking grain to answer this CL's review thread: %w", err)
	}
	return resp, nil
}
