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
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The two doors of a human-review hold (model/human_review_hold.go):
//
//   - POST /api/tasks/{id}/human-review, which a review run calls through
//     its request_human_review tool (or the agent API's
//     api_request_human_review) with {id} its own task. It holds the
//     change the review was filed against -- the task its LinkProposedBy
//     names -- for a person's decision. Only a review may call it: a
//     security review or a template review. The implementing run is not
//     offered it: an agent that wants a person to decide something about
//     its own work asks them (ask_question), which parks it until they
//     answer; a hold is a reviewer's second opinion on somebody else's
//     change.
//   - POST /api/tasks/{id}/human-review/release, which a person presses
//     (the task page's Release hold) with {id} the held task. An agent is
//     refused, whatever it holds. Submit releases the hold too
//     (humanSubmitted).

// HumanReviewRequestBody is POST /api/tasks/{id}/human-review's body.
type HumanReviewRequestBody struct {
	// Reason is one short line: what a person has to decide.
	Reason string `json:"reason"`
	// Details is markdown, said on the held task's conversation.
	Details string `json:"details,omitempty"`
	// Category is one of model.HumanReviewCategories, "other" by default.
	Category string `json:"category,omitempty"`
}

// HumanReviewView is one reason of a hold, on the wire.
type HumanReviewView struct {
	Source     string    `json:"source"`
	Category   string    `json:"category,omitempty"`
	Reason     string    `json:"reason"`
	ReviewTask string    `json:"reviewTask,omitempty"`
	Run        string    `json:"run,omitempty"`
	At         time.Time `json:"at"`
}

func humanReviewViews(rs []model.HumanReviewReason) []HumanReviewView {
	var out []HumanReviewView
	for _, r := range rs {
		out = append(out, HumanReviewView{Source: string(r.Source), Category: r.Category, Reason: r.Reason,
			ReviewTask: r.ReviewTask, Run: r.Run, At: r.At})
	}
	return out
}

// HumanReviewResult is what both doors answer: the held task and the
// reasons it is held for now (none once released). Added is whether a
// request added a new reason rather than repeating one.
type HumanReviewResult struct {
	Task    string            `json:"task"`
	Added   bool              `json:"added,omitempty"`
	Reasons []HumanReviewView `json:"reasons"`
}

// RequestHumanReview holds the change review task reviewID reviews for a
// person's decision, for req. See this file's doc comment.
func (c *Client) RequestHumanReview(ctx context.Context, reviewID string, req HumanReviewRequestBody) (HumanReviewResult, error) {
	actor := c.requestActor(ctx).Actor
	if actor.Kind == model.PrincipalAgent && actor.ID != reviewID {
		return HumanReviewResult{}, forbiddenErrorf("a run may only ask for human review as its own task, not %s", reviewID)
	}
	review, err := c.Store.GetTask(ctx, reviewID)
	if err != nil {
		return HumanReviewResult{}, err
	}
	if review == nil {
		return HumanReviewResult{}, &NotFoundError{ID: reviewID}
	}
	if review.Origin.Reason != model.ReasonReview {
		return HumanReviewResult{}, validationErrorf("task %s is not a review, so it has no change to hold: "+
			"request_human_review is for security and template reviews. To put a decision to a person about "+
			"your own work, use ask_question", reviewID)
	}
	reviewedID := ""
	for _, l := range review.Links {
		if l.Kind == model.LinkProposedBy {
			reviewedID = l.Target
		}
	}
	if reviewedID == "" {
		return HumanReviewResult{}, validationErrorf("review %s names no task it reviews", reviewID)
	}
	if utf8.RuneCountInString(req.Details) > model.MaxHumanReviewDetailsLength {
		return HumanReviewResult{}, validationErrorf("details must be at most %d characters", model.MaxHumanReviewDetailsLength)
	}
	reason, err := model.NormalizeHumanReviewReason(model.HumanReviewReason{
		Source: model.HumanReviewRequested, Category: req.Category, Reason: req.Reason,
		ReviewTask: reviewID, Run: c.Store.ActiveRunID(ctx, reviewID), At: c.now(),
	})
	if err != nil {
		return HumanReviewResult{}, validationErrorf("%v", err)
	}
	by := actor
	if by.Kind != model.PrincipalAgent {
		// The run's own MCP server reaches this door as the daemon; the
		// request is the review's agent's all the same.
		by = model.Principal{Kind: model.PrincipalAgent, ID: reviewID}
	}
	added, err := c.Store.RaiseHumanReviewHold(ctx, reviewedID, model.HumanReviewRequest{
		Reason: reason, Details: req.Details, By: by})
	if errors.Is(err, model.ErrHumanReviewRateLimited) {
		return HumanReviewResult{}, validationErrorf("%v", err)
	}
	if err != nil {
		return HumanReviewResult{}, err
	}
	held, err := c.Store.GetTask(ctx, reviewedID)
	if err != nil {
		return HumanReviewResult{}, err
	}
	out := HumanReviewResult{Task: reviewedID, Added: added}
	if held != nil {
		out.Reasons = humanReviewViews(model.HumanReviewReasons(*held))
	}
	return out, nil
}

// ReleaseHumanReviewHold ends task id's human-review hold on a person's
// say-so, recording who released it. An agent is refused.
func (c *Client) ReleaseHumanReviewHold(ctx context.Context, id string) (HumanReviewResult, error) {
	if a := accessRoleFromContext(ctx); a != nil && !a.Full {
		return HumanReviewResult{}, forbiddenErrorf("%s cannot release human-review hold on task %s: releasing a human-review hold requires an unlimited role (grain/operator)", a.label(), id)
	}
	actor := c.requestActor(ctx).Actor
	if !mayReleaseHumanReview(actor) {
		return HumanReviewResult{}, forbiddenErrorf("%v", model.ErrAgentCannotReleaseHumanReview)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return HumanReviewResult{}, err
	}
	if task == nil {
		return HumanReviewResult{}, &NotFoundError{ID: id}
	}
	if !model.HumanReviewHeld(*task) {
		return HumanReviewResult{}, validationErrorf("task %s is not held for a person's review", id)
	}
	if _, err := c.Store.ReleaseHumanReviewHold(ctx, id, actor, "released"); err != nil {
		if errors.Is(err, model.ErrAgentCannotReleaseHumanReview) {
			return HumanReviewResult{}, forbiddenErrorf("%v", err)
		}
		return HumanReviewResult{}, err
	}
	return HumanReviewResult{Task: id, Reasons: []HumanReviewView{}}, nil
}

func (s *Server) handleRequestHumanReview(w http.ResponseWriter, r *http.Request) {
	var req HumanReviewRequestBody
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.RequestHumanReview(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleReleaseHumanReview(w http.ResponseWriter, r *http.Request) {
	res, err := s.tasks.ReleaseHumanReviewHold(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// RequestHumanReview is Client.RequestHumanReview over the wire, for
// `grain mcpserver` serving review task id's run.
func (c *HTTPClient) RequestHumanReview(ctx context.Context, id string, req HumanReviewRequestBody) (HumanReviewResult, error) {
	var out HumanReviewResult
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+strings.TrimSpace(id)+"/human-review", req, &out); err != nil {
		return HumanReviewResult{}, err
	}
	return out, nil
}
