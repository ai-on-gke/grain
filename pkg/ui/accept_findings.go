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
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// POST /api/tasks/{id}/accept-findings (`grain accept-findings`, the task
// pane's "Accept findings…") is a person's decision to merge task {id}'s
// change over one review's findings on one merge gate, as they are, with a
// reason (model/review_findings_accept.go). It sits between Submit, which
// never clears findings, and Force merge, which overrides every gate it
// names: it clears that review's findings on that gate and nothing else.
// An agent is refused, whatever it holds, as Submit refuses one.

// AcceptFindingsRequest is POST /api/tasks/{id}/accept-findings's body.
type AcceptFindingsRequest struct {
	// Gate is the merge gate the findings hold: "security-review",
	// "review" (which covers gating trigger reviews), or one trigger's
	// own "trigger:<name>".
	Gate string `json:"gate"`
	// ReviewTask is the review whose findings are accepted. It may be
	// left out when exactly one review has findings on Gate.
	ReviewTask string `json:"reviewTask,omitempty"`
	// Reason is required: why the change merges without fixing them.
	Reason string `json:"reason"`
	// Head optionally pins the head commit the caller looked at; the
	// request is refused if the pull request has moved since.
	Head string `json:"head,omitempty"`
}

// AcceptFindingsResult is what the route answers: the acceptance recorded
// and the change's merge gates as they stand after it.
type AcceptFindingsResult struct {
	Task       string                         `json:"task"`
	Acceptance model.ReviewFindingsAcceptance `json:"acceptance"`
	Gates      []model.MergeGate              `json:"gates,omitempty"`
}

// AcceptReviewFindings records a person's acceptance of req.ReviewTask's
// findings on req.Gate for task id's change, at its current head.
func (c *Client) AcceptReviewFindings(ctx context.Context, id string, req AcceptFindingsRequest) (AcceptFindingsResult, error) {
	if a := accessRoleFromContext(ctx); a != nil && !a.Full {
		return AcceptFindingsResult{}, forbiddenErrorf("%s cannot accept review findings on task %s: "+
			"letting a change merge over a review's findings requires an unlimited role (grain/operator)", a.label(), id)
	}
	who := c.requestActor(ctx)
	if who.Actor.Kind != model.PrincipalHuman {
		return AcceptFindingsResult{}, forbiddenErrorf("only a person may accept a review's findings: " +
			"an agent that thinks they need no fix asks the owner (ask_question)")
	}
	gate := strings.ToLower(strings.TrimSpace(req.Gate))
	if gate != model.GateSecurityReview && gate != model.GateReview &&
		(!strings.HasPrefix(gate, "trigger:") || strings.TrimSpace(strings.TrimPrefix(gate, "trigger:")) == "") {
		return AcceptFindingsResult{}, validationErrorf("gate %q: name the review gate whose findings you accept "+
			"(%s, %s or trigger:<name>)", req.Gate, model.GateSecurityReview, model.GateReview)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return AcceptFindingsResult{}, validationErrorf("accepting a review's findings needs a reason: it is " +
			"the whole record of why this change merges without fixing them")
	}
	if utf8.RuneCountInString(reason) > model.MaxAcceptFindingsReasonLength {
		return AcceptFindingsResult{}, validationErrorf("reason must be at most %d characters", model.MaxAcceptFindingsReasonLength)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return AcceptFindingsResult{}, err
	}
	if task == nil {
		return AcceptFindingsResult{}, &NotFoundError{ID: id}
	}
	if state, err := c.Store.State(ctx, id); err != nil {
		return AcceptFindingsResult{}, err
	} else if state == model.StateClosed {
		return AcceptFindingsResult{}, validationErrorf("%s is closed: reopen it before accepting its findings", id)
	}
	ref := pullRequestRefOf(*task)
	if ref == "" {
		return AcceptFindingsResult{}, validationErrorf("%s has no pull request whose findings to accept", id)
	}
	reviewTask := strings.TrimPrefix(strings.TrimSpace(req.ReviewTask), "#")
	if reviewTask == "" {
		reviews := findingsReviews(*task, gate)
		switch len(reviews) {
		case 0:
			return AcceptFindingsResult{}, validationErrorf("%s has no review findings on gate %s", id, gate)
		case 1:
			reviewTask = reviews[0]
		default:
			return AcceptFindingsResult{}, validationErrorf("%s has findings from more than one review on gate %s "+
				"(%s): name the one you accept", id, gate, strings.Join(reviews, ", "))
		}
	}
	records, ok := model.AcceptableReviewFindings(*task, gate, reviewTask)
	if !ok {
		return AcceptFindingsResult{}, validationErrorf("review %s has no findings standing on %s's %s gate",
			reviewTask, id, gate)
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return AcceptFindingsResult{}, err
	}
	head, err := c.acceptFindingsHead(ref, strings.ToLower(strings.TrimSpace(req.Head)), obs)
	if err != nil {
		return AcceptFindingsResult{}, err
	}
	var summaries []string
	for _, r := range records {
		if r.Summary != "" && !slices.Contains(summaries, r.Summary) {
			summaries = append(summaries, r.Summary)
		}
	}
	now := c.now()
	acc := model.ReviewFindingsAcceptance{
		Gate:       gate,
		ReviewTask: reviewTask,
		Reason:     reason,
		By:         who.Actor.ID,
		HeadSHA:    head,
		Findings:   strings.Join(summaries, "; "),
		At:         now,
	}
	accepted := false
	if err := c.mutate(model.WithAutoMergeChange(ctx, who.Actor, "review findings accepted"), id, func(t *model.Task) error {
		accepted = model.AcceptReviewFindings(t, acc)
		if accepted && reviewGatesSettled(*t) {
			// What a clean re-review hands back (orchestrator's
			// clearReviewFindingsOnCleanReview): the findings took the
			// change's auto-merge, and with them settled and nothing else
			// holding it, it merges itself as it was going to.
			t.AutoMerge = true
		}
		return nil
	}); err != nil {
		return AcceptFindingsResult{}, err
	}
	if !accepted {
		return AcceptFindingsResult{}, validationErrorf("review %s's findings on %s's %s gate were settled meanwhile",
			reviewTask, id, gate)
	}

	detail := fmt.Sprintf("gate=%s review=%s head=%s", gate, reviewTask, shortHead(head))
	if acc.Findings != "" {
		detail += " (" + acc.Findings + ")"
	}
	c.recordTaskEvent(ctx, model.TaskEvent{
		TaskID:     id,
		Kind:       model.TaskEventReviewFindingsAccepted,
		At:         now,
		By:         who.Actor,
		OnBehalfOf: who.OnBehalfOf,
		Reason:     reason,
		Detail:     detail,
	})
	body := fmt.Sprintf("**Review findings accepted** — %s accepted the findings of review task %s on the `%s` "+
		"gate of %s at %s, as they are.\n\n**Reason given:** %s", acc.By, reviewTask, gate, ref, shortHead(head), reason)
	if acc.Findings != "" {
		body += "\n\n**Findings accepted:** " + acc.Findings
	}
	body += "\n\nThis clears that review's findings on that gate only. CI, the other gates, any approval a " +
		"trigger asks for and any security hold still apply; a later push is classified and reviewed as " +
		"usual, and findings a new review leaves need a new decision."
	if _, err := c.Store.AddComment(ctx, model.Comment{TaskID: id, Author: who, Body: body, CreatedAt: now}); err != nil {
		return AcceptFindingsResult{}, err
	}
	if commenter, ok := c.Config.GitHubPullRequests.(interface {
		CreateComment(owner, repo string, number int, body string) (int, error)
	}); ok {
		if parsedRef, perr := model.ParsePullRequestRef(ref); perr == nil && !model.IsConfigRepo(parsedRef.Repo) {
			prComment := fmt.Sprintf("**grain: review findings accepted** (task %s)\n\n%s", id, body)
			if _, cerr := commenter.CreateComment(parsedRef.Repo.Owner, parsedRef.Repo.Name, parsedRef.Number, prComment); cerr != nil {
				log.Printf("ui: posting findings acceptance comment on %s: %v", ref, cerr)
			}
		}
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}

	out := AcceptFindingsResult{Task: id, Acceptance: acc}
	if updated, err := c.Store.GetTask(ctx, id); err == nil && updated != nil {
		obs, _ := c.Store.GetObservation(ctx, id)
		states, _ := c.Store.States(ctx)
		out.Gates = model.ComputeMergeGates(*updated, obs, states)
	}
	return out, nil
}

// findingsReviews lists the review tasks with findings, questions or
// security-findings hold reasons standing on gate of t, in the order first
// recorded.
func findingsReviews(t model.Task, gate string) []string {
	var out []string
	add := func(id string) {
		if id != "" && !slices.Contains(out, id) {
			if _, ok := model.AcceptableReviewFindings(t, gate, id); ok {
				out = append(out, id)
			}
		}
	}
	for _, r := range model.ReviewFindings(t) {
		add(r.ReviewTask)
	}
	if q, ok := model.ReviewQuestionsOf(t); ok {
		add(q.ReviewTaskID)
	}
	for _, r := range model.HumanReviewReasons(t) {
		add(r.ReviewTask)
	}
	return out
}

// reviewGatesSettled reports whether nothing but a person's acceptance was
// keeping t from merging itself: no security or trigger hold, no hold only
// a person may end, no review findings left, no approver still owed.
func reviewGatesSettled(t model.Task) bool {
	for _, l := range t.Links {
		switch l.Kind {
		case model.LinkSecurityHold, model.LinkPathTriggerHold, model.LinkHumanOnlyHold:
			return false
		}
	}
	return !model.AutoMergeDeclined(t) && !model.AutoMergeAwaitsPerson(t) && !model.HumanReviewHeld(t) &&
		!model.HasReviewFindings(t) && len(model.PendingApprovals(t)) == 0
}

// acceptFindingsHead is the head commit an acceptance is recorded at: the
// pull request's head as GitHub reports it (grain CI's last commit where
// GitHub cannot say), refusing a pinned asked that is no longer it.
func (c *Client) acceptFindingsHead(ref, asked string, obs *model.Observation) (string, error) {
	known := c.pullRequestHead(ref)
	if known == "" && obs != nil && obs.CI != nil {
		known = strings.TrimSpace(obs.CI.Commit)
	}
	switch {
	case asked == "":
		return known, nil
	case known == "":
		return asked, nil
	case known == asked || (len(asked) >= 7 && strings.HasPrefix(known, asked)):
		return known, nil
	default:
		return "", validationErrorf("%s has moved since you looked: you read the findings at %s and its head "+
			"is now %s -- read what the review says about that head and decide again", ref, shortHead(asked), shortHead(known))
	}
}

func (s *Server) handleAcceptFindings(w http.ResponseWriter, r *http.Request) {
	var req AcceptFindingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.AcceptReviewFindings(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// AcceptReviewFindings is Client.AcceptReviewFindings over the wire, for
// `grain accept-findings`.
func (c *HTTPClient) AcceptReviewFindings(ctx context.Context, id string, req AcceptFindingsRequest) (AcceptFindingsResult, error) {
	var out AcceptFindingsResult
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+strings.TrimSpace(id)+"/accept-findings", req, &out); err != nil {
		return AcceptFindingsResult{}, err
	}
	return out, nil
}
