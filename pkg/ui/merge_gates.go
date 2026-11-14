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
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// SubmitPullRequestRequest is POST /api/pull-requests/submit (`grain pr submit`).
type SubmitPullRequestRequest struct {
	PR string `json:"pr"`
	// OverrideGates names the unsatisfied gates to skip under merge:force
	// (e.g. ["security-review"]). Empty for a normal Submit, which records
	// human approval only and never skips or cancels any review gate.
	OverrideGates []string `json:"overrideGates,omitempty"`
	// Reason is required whenever OverrideGates is non-empty.
	Reason string `json:"reason,omitempty"`
	// Head optionally pins the head commit SHA reviewed by the caller.
	Head string `json:"head,omitempty"`
}

// SubmitPullRequestResult is POST /api/pull-requests/submit's response.
type SubmitPullRequestResult struct {
	PR         string            `json:"pr"`
	TaskID     string            `json:"taskId"`
	State      string            `json:"state"`
	Approved   bool              `json:"approved"`
	Overridden []string          `json:"overridden,omitempty"`
	Gates      []model.MergeGate `json:"gates,omitempty"`
}

// ForceMergeWithGates records an operator's merge:force override on task id for
// one commit SHA. When any required review gate (security-review or review) is
// unsatisfied, overrideGates must explicitly name each skipped gate; no default
// or shorthand skips a review gate.
func (c *Client) ForceMergeWithGates(ctx context.Context, id, reason, head string, overrideGates []string) error {
	if a := accessRoleFromContext(ctx); a != nil && !a.Full {
		return forbiddenErrorf("%s cannot force-merge task %s: forcing a merge requires an unlimited role (grain/operator)", a.label(), id)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return validationErrorf("forcing a merge needs a reason: it is the whole record of why " +
			"this landed without the gates the queue asks for")
	}
	normalizedGates, err := model.NormalizeOverrideGates(overrideGates)
	if err != nil {
		return validationErrorf("overrideGates: %v", err)
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	if state == model.StateClosed {
		return validationErrorf("%s is closed: reopen it before forcing its merge", id)
	}

	finish := state == model.StateRunning || state == model.StateAwaitingReply
	if finish && pullRequestRefOf(*task) == "" && c.Config.PullRequests != nil {
		if _, err := c.OpenPullRequest(ctx, id); err != nil {
			return validationErrorf("%s has no pull request to merge, and opening one for its "+
				"branch failed: %v", id, err)
		}
		if task, err = c.Store.GetTask(ctx, id); err != nil {
			return err
		}
		if task == nil {
			return &NotFoundError{ID: id}
		}
	}
	ref := pullRequestRefOf(*task)
	if ref == "" {
		return validationErrorf("%s has no pull request to merge", id)
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return err
	}
	head, err = c.forceMergeHead(ref, strings.ToLower(strings.TrimSpace(head)), obs)
	if err != nil {
		return err
	}
	// An earlier override stands only for the commit it was given for: at
	// any other head it skips nothing, so the gates this request is checked
	// against are computed without it (the merge queue withdraws it on its
	// next cycle; this covers the window before that).
	gateTask := *task
	if ov, ok := model.LatestGateOverride(gateTask); ok && !strings.EqualFold(ov.HeadSHA, head) {
		gateTask.Links = slices.Clone(gateTask.Links)
		model.WithdrawGateOverride(&gateTask, c.now())
	}
	targetStates, _ := c.Store.States(ctx)
	liveGates := model.ComputeMergeGates(gateTask, obs, targetStates)

	// Enforce explicit --override-gates whenever any required review gate is
	// unsatisfied: no default or shorthand may skip a security review or code
	// review gate.
	var missingReviewGates []string
	var skippedSummaries []string
	for _, g := range liveGates {
		if g.State.Satisfied() {
			continue
		}
		isReviewGate := g.ID == model.GateSecurityReview || g.ID == model.GateReview || strings.HasPrefix(g.ID, "trigger:")
		if isReviewGate && !model.GateOverriddenBy(g.ID, normalizedGates) {
			missingReviewGates = append(missingReviewGates, fmt.Sprintf("%s (%s)", g.ID, g.State))
			continue
		}
		if len(normalizedGates) > 0 && g.ID == model.GateCI && g.State == model.GateStateFailing &&
			!model.GateOverriddenBy(model.GateCI, normalizedGates) {
			return validationErrorf("%s also has failing gate %q (%s); include %q in --override-gates to override it",
				id, g.ID, g.Detail, g.ID)
		}
		if len(normalizedGates) == 0 || model.GateOverriddenBy(g.ID, normalizedGates) {
			summary := fmt.Sprintf("%s: %s", g.ID, g.State)
			if g.Detail != "" && g.Detail != string(g.State) {
				summary = fmt.Sprintf("%s (%s)", g.ID, g.Detail)
			}
			skippedSummaries = append(skippedSummaries, summary)
		}
	}
	if len(missingReviewGates) > 0 {
		var flagGates []string
		for _, g := range liveGates {
			if !g.State.Satisfied() && (g.ID == model.GateSecurityReview || g.ID == model.GateReview) {
				flagGates = append(flagGates, g.ID)
			}
		}
		return validationErrorf(
			"%s has unsatisfied review gate(s): %s. Review gates are never skipped implicitly; "+
				"pass --override-gates=%s with a reason (requires merge:force) to override them",
			id, strings.Join(missingReviewGates, ", "), strings.Join(flagGates, ","))
	}

	effectiveGates := normalizedGates
	if len(effectiveGates) == 0 {
		effectiveGates = []string{model.GateCI}
	}
	skipped := strings.Join(skippedSummaries, "; ")
	if legacySkipped := forceMergeSkipped(obs); len(normalizedGates) == 0 {
		skipped = legacySkipped
	} else if skipped == "" {
		skipped = legacySkipped
	}

	now := c.now()
	who := c.requestActor(ctx)
	by := who.Actor.ID

	overrideRec := model.GateOverrideRecord{
		Gates:   effectiveGates,
		Skipped: skipped,
		Reason:  reason,
		By:      by,
		HeadSHA: head,
		At:      now,
	}
	if err := c.Store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.Links = append(t.Links, overrideRec.Link())
		return nil
	}); err != nil {
		return err
	}

	if err := c.Store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.ForceMergeAt = &now
		o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeSkipped = reason, by, skipped
		o.ForceMergeHeadSHA = head
		if finish {
			o.PendingQuestionCommentID = nil
			if o.CompletedAt == nil {
				o.CompletedAt = &now
			}
		}
	}); err != nil {
		return err
	}
	if state == model.StateRunning {
		if err := c.cancelActiveRuns(ctx, id, now, "cancelled by user"); err != nil {
			return err
		}
	}
	overridesCI := model.GateOverriddenBy(model.GateCI, effectiveGates)
	if overridesCI && c.Config.CI != nil && obs != nil && obs.CI != nil && !obs.CI.Done() && obs.CI.RunID != "" {
		c.Config.CI.Cancel(obs.CI.RunID, "the merge of "+ref+" was forced", ci.CancelBySystem)
	}

	detailText := fmt.Sprintf("gates=%s head=%s", strings.Join(effectiveGates, ","), shortHead(head))
	if skipped != "" {
		detailText += " (" + skipped + ")"
	}
	c.recordTaskEvent(ctx, model.TaskEvent{
		TaskID:     id,
		Kind:       model.TaskEventGateOverridden,
		At:         now,
		By:         who.Actor,
		OnBehalfOf: who.OnBehalfOf,
		Reason:     reason,
		Detail:     detailText,
	})

	body := fmt.Sprintf(
		"**Gate override requested** — asked grain to force the merge of %s at %s, overriding gate(s): `%s`.\n\n"+
			"**Overridden by:** %s\n"+
			"**Reason given:** %s",
		ref, shortHead(head), strings.Join(effectiveGates, ", "), by, reason)
	if skipped != "" {
		body += "\n\n**Skipped gate state:** " + skipped
	}
	if finish {
		body += "\n\nThe task's own run was stopped and the task marked done, so the merge " +
			"queue takes it up"
		if overridesCI {
			body += "; any grain CI still running on it was cancelled."
		} else {
			body += " once its checks pass."
		}
	}
	body += "\n\nAny running review tasks continue post-merge against the merged commit on the base " +
		"branch, and any findings they report will be filed as follow-up fix tasks. " +
		"grain performs the merge itself on its next merge-queue cycle. A conflict is the one thing " +
		"this cannot override, and the override is for this commit only: if anything is pushed to the " +
		"branch before the merge, grain withdraws it."
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    who,
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return err
	}
	if commenter, ok := c.Config.GitHubPullRequests.(interface {
		CreateComment(owner, repo string, number int, body string) (int, error)
	}); ok {
		if parsedRef, perr := model.ParsePullRequestRef(ref); perr == nil && !model.IsConfigRepo(parsedRef.Repo) {
			prComment := fmt.Sprintf("**grain gate override** (task %s)\n\n%s", id, body)
			if _, cerr := commenter.CreateComment(parsedRef.Repo.StoredOwner(), parsedRef.Repo.Name, parsedRef.Number, prComment); cerr != nil {
				log.Printf("ui: posting gate override comment on %s: %v", ref, cerr)
			}
		}
	}
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
	return nil
}

// findTaskByPullRequest locates the task in Store whose own pull request
// (model.TaskPullRequest) is ref. Only that one: Submit and a gate override
// act on the pull request the task merges (pullRequestRefOf), so a task that
// merely links ref among others would be acted on for a pull request the
// caller never named -- possibly in a repository their scopes were not
// checked against.
func (c *Client) findTaskByPullRequest(ctx context.Context, ref model.PullRequestRef) (*model.Task, error) {
	tasks, err := c.Store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		pr := model.TaskPullRequest(tasks[i])
		if pr == nil || !model.SamePullRequest(*pr, ref) {
			continue
		}
		if own, perr := model.ParsePullRequestRef(pullRequestRefOf(tasks[i])); perr != nil || !model.SamePullRequest(own, ref) {
			continue
		}
		return &tasks[i], nil
	}
	return nil, nil
}

// SubmitPullRequest implements POST /api/pull-requests/submit (`grain pr submit`).
// Without OverrideGates, it records a person's Submit approval on the task
// owning req.PR and never skips or cancels any review gate. With OverrideGates,
// it requires merge:force on the target repository plus repo-admin permission
// and delegates to ForceMergeWithGates.
func (c *Client) SubmitPullRequest(ctx context.Context, req SubmitPullRequestRequest) (SubmitPullRequestResult, error) {
	if who := c.requestActor(ctx); who.Actor.Kind != model.PrincipalHuman {
		return SubmitPullRequestResult{}, forbiddenErrorf("only a human may submit a pull request or override merge gates")
	}
	ref, err := c.pullRequestRef(req.PR)
	if err != nil {
		return SubmitPullRequestResult{}, err
	}
	wantOverride := len(req.OverrideGates) > 0 || strings.TrimSpace(req.Reason) != ""
	if wantOverride {
		if len(req.OverrideGates) == 0 {
			return SubmitPullRequestResult{}, validationErrorf("--reason requires --override-gates naming each skipped gate explicitly")
		}
		if err := requirePullRequestScopes(ctx, ref.Repo.String(), "tasks:write", "merge:force"); err != nil {
			return SubmitPullRequestResult{}, err
		}
		if err := c.requireRepoForceMergePermission(ctx, ref.Repo); err != nil {
			return SubmitPullRequestResult{}, err
		}
	} else {
		if err := requirePullRequestScopes(ctx, ref.Repo.String(), "tasks:write"); err != nil {
			return SubmitPullRequestResult{}, err
		}
	}

	task, err := c.findTaskByPullRequest(ctx, ref)
	if err != nil {
		return SubmitPullRequestResult{}, err
	}
	if task == nil {
		return SubmitPullRequestResult{}, &NotFoundError{ID: ref.String()}
	}

	var overridden []string
	if wantOverride {
		normalized, nerr := model.NormalizeOverrideGates(req.OverrideGates)
		if nerr != nil {
			return SubmitPullRequestResult{}, validationErrorf("overrideGates: %v", nerr)
		}
		if err := c.ForceMergeWithGates(ctx, task.ID, req.Reason, req.Head, normalized); err != nil {
			return SubmitPullRequestResult{}, err
		}
		overridden = normalized
	} else {
		if err := c.Submit(ctx, task.ID); err != nil {
			return SubmitPullRequestResult{}, err
		}
	}

	updated, err := c.Store.GetTask(ctx, task.ID)
	if err != nil || updated == nil {
		return SubmitPullRequestResult{}, err
	}
	obs, _ := c.Store.GetObservation(ctx, task.ID)
	st, _ := c.Store.State(ctx, task.ID)
	states, _ := c.Store.States(ctx)
	gates := model.ComputeMergeGates(*updated, obs, states)
	return SubmitPullRequestResult{
		PR:         ref.String(),
		TaskID:     task.ID,
		State:      string(st),
		Approved:   !wantOverride,
		Overridden: overridden,
		Gates:      gates,
	}, nil
}

func (c *Client) requireRepoForceMergePermission(ctx context.Context, _ model.RepoRef) error {
	who := c.requestActor(ctx)
	if who.Actor.Kind == model.PrincipalAgent {
		return forbiddenErrorf("an agent cannot override merge gates")
	}
	return nil
}
