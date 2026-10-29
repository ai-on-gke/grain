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
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A CL's Submit and Sync buttons (changelists.md §6-§7, G3-6/G3-7): a
// person submitting a task's CL through its change provider, and what
// each outcome does to the task. A pull request's Submit is untouched;
// Client.Submit hands a task whose change is a CL to submitChange.

// ChangeSubmit is the Submit and Sync buttons as the task view and inbox
// draw them beside a CL: whether each applies, and why not.
type ChangeSubmit struct {
	CanSubmit bool `json:"canSubmit"`
	// Reasons is why Submit is disabled: every one has to clear, and
	// there is no submit anyway.
	Reasons []string `json:"reasons,omitempty"`
	CanSync bool     `json:"canSync"`
	// SyncReason is why Sync is disabled.
	SyncReason string `json:"syncReason,omitempty"`
	// Behind is a CL behind head (or conflicting with it), shown as a
	// badge beside the chip.
	Behind     bool   `json:"behind,omitempty"`
	Conflicted bool   `json:"conflicted,omitempty"`
	Target     string `json:"target"`
}

// changeSubmit is task's Submit and Sync for its first live CL, nil for a
// task with none, one whose change is a pull request (a CL linked beside
// it is not what Submit lands), or a deployment with no provider for CLs.
func changeSubmit(task model.Task, state model.State) *ChangeSubmit {
	live := change.LiveChanges(task)
	if len(live) == 0 || !submitsAsCL(task) {
		return nil
	}
	if _, ok := change.Lookup(model.LinkChangelist); !ok {
		return nil
	}
	target := live[0]
	obs, observed := change.LastObserved(target)
	out := &ChangeSubmit{Target: target, Behind: observed && obs.Health == change.HealthBehind,
		Conflicted: observed && obs.Health == change.HealthConflicted}
	switch state {
	case model.StateAwaitingSubmit:
		out.Reasons = change.SubmitBlockers(task, obs, observed)
		out.CanSubmit = len(out.Reasons) == 0
	case model.StateRunning:
		out.Reasons = []string{"a run is going"}
	default:
		out.Reasons = []string{"the task is " + strings.ReplaceAll(string(state), "_", " ") + ", not awaiting submit"}
	}
	switch {
	case state == model.StateRunning:
		out.SyncReason = "a run is going"
	case state == model.StateClosed:
		out.SyncReason = "the task is closed"
	default:
		out.CanSync = true
	}
	return out
}

// personActor is ctx's caller when it is a person, or a refusal: Submit
// and Sync are a person's, never an agent's or an API token's.
func (c *Client) personActor(ctx context.Context, what string) (model.Principal, error) {
	by := c.actor(ctx)
	if by.Kind != model.PrincipalHuman || strings.TrimSpace(by.ID) == "" {
		return model.Principal{}, forbiddenErrorf("only a person may %s a CL; %s %q may not", what, by.Kind, by.ID)
	}
	return by, nil
}

// changeProvider is the provider for task's CLs, refusing when there is
// none or its credentials are blocked.
func changeProvider() (change.Provider, error) {
	p, ok := change.Lookup(model.LinkChangelist)
	if !ok {
		return nil, errNoChangeReview
	}
	if h := change.HealthOf(change.HealthKind(p)); h.Blocked != "" {
		return nil, apierror.Expose(&change.CredentialsExpiredError{Kind: change.HealthKind(p),
			Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339)})
	}
	return p, nil
}

// submitChange is Submit on a task whose change is a CL: refused unless
// a person presses it and the CL is submittable, recorded as that
// person's grant, then handed to the provider. Every outcome but a
// plain error is handled here and answers nil: the task's comments say
// what happened.
func (c *Client) submitChange(ctx context.Context, task model.Task) error {
	by, err := c.personActor(ctx, "submit")
	if err != nil {
		return err
	}
	p, err := changeProvider()
	if err != nil {
		return err
	}
	state, err := c.Store.State(ctx, task.ID)
	if err != nil {
		return err
	}
	view := changeSubmit(task, state)
	if view == nil {
		return validationErrorf("task %s has no live CL to submit", task.ID)
	}
	if !view.CanSubmit {
		return validationErrorf("%s cannot be submitted yet: %s", view.Target, strings.Join(view.Reasons, "; "))
	}
	target, now := view.Target, c.now()
	grant, err := c.recordSubmitGrant(ctx, task.ID, p, target, by, now)
	if err != nil {
		return err
	}
	// The grant is this press's and no one else's: it is gone once Submit
	// has answered, whatever it answered, so no later run or session on
	// the task inherits it (review task 1709).
	defer c.releaseSubmitGrant(context.WithoutCancel(ctx), task.ID, grant)
	if fresh, err := c.Store.GetTask(ctx, task.ID); err == nil && fresh != nil {
		task = *fresh
	}
	res, err := p.Submit(ctx, task, target, change.Person{Name: by.ID})
	if err == nil {
		return change.RecordSubmitted(ctx, c.Store, task, target, res.Target, res.Message, false, c.now())
	}
	change.NoteError(ctx, p, err, c.Store, c.now())
	if change.IsCredentialsExpired(err) {
		_, _ = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: change.SubmitPrincipal},
			Body: change.ErrorNote(target, change.Failure{}, err), CreatedAt: c.now()})
		return apierror.Expose(err)
	}
	obs, _ := change.LastObserved(target)
	f := change.FailureOf(err)
	switch f.Outcome {
	case change.OutcomeNeedsSync:
		return c.syncAfterSubmit(ctx, p, task, target, obs.Snapshot)
	case change.OutcomeConflicts:
		return change.Wake(ctx, c.Store, task, change.ConflictNote(target, f.Files), c.now())
	case change.OutcomePresubmitFailed:
		return change.Wake(ctx, c.Store, task, change.PresubmitNote(target, f.Checks), c.now())
	}
	if _, cerr := c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: change.SubmitPrincipal},
		Body: change.ErrorNote(target, f, err), CreatedAt: c.now()}); cerr != nil {
		return cerr
	}
	return apierror.Expose(fmt.Errorf("submit of %s did not go through: %w", target, err))
}

// recordSubmitGrant records by's Submit of target on the task: the
// provider's submit method, granted with the CL as its argument, and a
// comment attributed to by at now saying so, which is the lasting record
// of who pressed it and when. It answers the grant string ("" when the
// provider names none) for releaseSubmitGrant: the grant itself lasts
// only as long as the Submit it was pressed for.
func (c *Client) recordSubmitGrant(ctx context.Context, id string, p change.Provider, target string, by model.Principal, now time.Time) (string, error) {
	grant := ""
	if g, ok := p.(change.SubmitGranter); ok {
		capability, with := g.SubmitGrant(target)
		var err error
		grant, err = model.EncodeCapabilityGrant(protocol.GrantSpec{Capability: capability, With: with})
		if err != nil {
			return "", fmt.Errorf("recording the submit grant: %w", err)
		}
		if err := c.Store.UpdateTask(ctx, id, func(t *model.Task) error {
			if !slices.ContainsFunc(t.Grants, func(have model.Grant) bool { return have.Capability == grant }) {
				t.Grants = append(t.Grants, model.Grant{Capability: grant, Via: model.GrantByLabel})
			}
			return nil
		}); err != nil {
			return "", fmt.Errorf("recording the submit grant: %w", err)
		}
	}
	_, err := c.Store.AddComment(ctx, model.Comment{TaskID: id, Author: model.Attribution{Actor: by},
		Body: fmt.Sprintf("Submitted %s.", target), CreatedAt: now})
	return grant, err
}

// releaseSubmitGrant takes a Submit's grant back off the task once the
// Submit has answered. A person's Submit authorizes that one press; it is
// not carried into the task's later runs or sessions. (A task's
// auto-merge is a standing decision and is not touched here.)
func (c *Client) releaseSubmitGrant(ctx context.Context, id, grant string) {
	if grant == "" {
		return
	}
	if err := c.Store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.Grants = slices.DeleteFunc(t.Grants, func(g model.Grant) bool { return g.Capability == grant })
		return nil
	}); err != nil {
		log.Printf("ui: releasing the submit grant on task %s: %v", id, err)
	}
}

// syncAfterSubmit is a Submit that came back needing a sync: one sync,
// and the task left for the person to press Submit again. It never
// resubmits on its own.
func (c *Client) syncAfterSubmit(ctx context.Context, p change.Provider, task model.Task, target string, snapshot int) error {
	res, err := p.Sync(ctx, task, "")
	change.Planner.Synced(target, snapshot, err != nil)
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		if f := change.FailureOf(err); f.Outcome == change.OutcomeConflicts {
			return change.Wake(ctx, c.Store, task, "Submit said "+target+" needed a sync, and the sync conflicted.\n\n"+
				change.ConflictNote(target, f.Files), c.now())
		}
		_, _ = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: change.SubmitPrincipal},
			Body: "Submit said " + target + " needed a sync, and the sync failed: " + err.Error(), CreatedAt: c.now()})
		return apierror.Expose(err)
	}
	msg := res.Message
	if msg == "" {
		msg = "synced to head"
	}
	_, err = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: change.SubmitPrincipal},
		Body:      fmt.Sprintf("Submit said %s needed a sync: %s. Press Submit again once presubmit has passed on the new snapshot.", target, msg),
		CreatedAt: c.now()})
	return err
}

// SyncChange is the Sync button: the task's CL synced to head, as a
// person asked. Refused while the task has a run going.
func (c *Client) SyncChange(ctx context.Context, id string) error {
	by, err := c.personActor(ctx, "sync")
	if err != nil {
		return err
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	p, err := changeProvider()
	if err != nil {
		return err
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	view := changeSubmit(*task, state)
	if view == nil {
		return validationErrorf("task %s has no live CL to sync", id)
	}
	if !view.CanSync {
		return validationErrorf("%s cannot be synced now: %s", view.Target, view.SyncReason)
	}
	res, err := p.Sync(ctx, *task, "")
	change.Planner.NoteUpload(view.Target)
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		if f := change.FailureOf(err); f.Outcome == change.OutcomeConflicts {
			return change.Wake(ctx, c.Store, *task, fmt.Sprintf("%s pressed Sync and it conflicted.\n\n%s", by.ID,
				change.ConflictNote(view.Target, f.Files)), c.now())
		}
		return apierror.Expose(err)
	}
	msg := res.Message
	if msg == "" {
		msg = "synced to head"
	}
	_, err = c.Store.AddComment(ctx, model.Comment{TaskID: id, Author: model.Attribution{Actor: by},
		Body: "Synced " + view.Target + ": " + msg + ".", CreatedAt: c.now()})
	return err
}

// handleSyncChange answers POST /api/tasks/{id}/change-sync.
func (s *Server) handleSyncChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.SyncChange(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// submitsAsCL reports whether task's Submit is a CL's: it has a live CL
// and no pull request.
func submitsAsCL(task model.Task) bool {
	if len(change.LiveChanges(task)) == 0 {
		return false
	}
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			return false
		}
	}
	return true
}
