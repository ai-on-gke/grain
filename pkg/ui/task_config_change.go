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
	"log"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A config change a session proposed in its own chat
// (docs/design/config-repo-settings.md, section 4.3). The session's
// propose_config_change tool pushes a branch of grain/config and asks the
// daemon here to diff and validate it (POST /api/tasks/{id}/config-change,
// grain's own plumbing, called by the mcpserver for the run's own task);
// the task then parks on it the way it parks on a request_secret, and a
// person decides inline: approve merges exactly the commit they were shown
// into grain/config's main and applies it at once, reject deletes the
// branch. Either posts the decision into the conversation, which is what
// wakes the session.
//
// Approving and rejecting are a person's alone. Both routes are
// "internal", so no agent scope reaches them, and both refuse any caller
// that is not a person besides -- an agent never approves its own config
// change, or another agent's.

// ConfigChangeProposal is POST /api/tasks/{id}/config-change: a proposal
// branch the task's run has pushed, to be diffed and validated.
type ConfigChangeProposal struct {
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Branch      string `json:"branch"`
	// HeadSHA is what the run pushed; the daemon refuses a branch whose
	// tip is anything else.
	HeadSHA string `json:"headSha"`
}

// ConfigChangeDecision is the approve and reject routes' body.
type ConfigChangeDecision struct {
	// HeadSHA pins the decision to the commit the person was shown.
	HeadSHA string `json:"headSha"`
	// Reason is a rejection's, relayed to the session.
	Reason string `json:"reason,omitempty"`
}

// ConfigChangeManager is what a StateRepoManager implements when it can
// carry session-proposed config changes (pkg/daemon's StateManager does).
type ConfigChangeManager interface {
	// ProposeConfigChange diffs and validates proposal's branch against
	// grain/config's base branch.
	ProposeConfigChange(ctx context.Context, proposal ConfigChangeProposal) (*model.PendingConfigChange, error)
	// MergeConfigChange merges pending.HeadSHA into the base branch,
	// refusing with ErrConfigChangeMoved when the branch's tip is no
	// longer that commit and with *ConfigPullRequestInvalid when the
	// merge no longer validates, and is the merge commit.
	MergeConfigChange(ctx context.Context, pending model.PendingConfigChange) (string, error)
	// DiscardConfigChange deletes pending's branch.
	DiscardConfigChange(ctx context.Context, pending model.PendingConfigChange) error
}

// ErrConfigChangeMoved is a decision about a commit that is no longer the
// proposal's: answered 409.
var ErrConfigChangeMoved = errors.New("branch tip changed since diff was rendered")

var errConfigChangesUnavailable = errors.New("this deployment's UI does not manage a config repository to propose changes to")

func (c *Client) configChangeManager(ctx context.Context) (ConfigChangeManager, error) {
	if remote, _ := c.externalConfigRepo(ctx); remote != "" {
		return nil, validationErrorf("configuration is managed in the external repository %s: propose the change there", remote)
	}
	manager, ok := c.Config.StateRepo.(ConfigChangeManager)
	if c.Config.StateRepo == nil || !ok {
		return nil, errConfigChangesUnavailable
	}
	return manager, nil
}

// ProposeConfigChange records the change task id's run proposed and is
// about to park on. Only a task holding grain/config in its Writes may,
// and only from its own proposal branches.
func (c *Client) ProposeConfigChange(ctx context.Context, id string, proposal ConfigChangeProposal) (*model.PendingConfigChange, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &NotFoundError{ID: id}
	}
	writable := false
	for _, w := range task.Writes {
		if model.IsConfigRepo(w.Repo) {
			writable = true
		}
	}
	if !writable {
		return nil, forbiddenErrorf("task %s does not hold %s in its writes", id, model.ConfigRepo())
	}
	proposal.Summary = strings.TrimSpace(proposal.Summary)
	if proposal.Summary == "" {
		return nil, validationErrorf("summary is required")
	}
	if !model.IsConfigProposalBranch(id, proposal.Branch) {
		return nil, validationErrorf("branch %q is not one of task %s's proposal branches (%s<name>)",
			proposal.Branch, id, model.ConfigProposalBranchPrefix(id))
	}
	manager, err := c.configChangeManager(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := manager.ProposeConfigChange(ctx, proposal)
	if err != nil {
		return nil, err
	}
	pending.CreatedAt = c.now()
	if err := c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.PendingConfigChange = pending
	}); err != nil {
		return nil, err
	}
	return pending, nil
}

// pendingConfigChangeFor is the change task id is parked on, refusing a
// caller that is not a person and a decision about any other commit.
func (c *Client) pendingConfigChangeFor(ctx context.Context, id, headSHA, verb string) (*model.PendingConfigChange, error) {
	if c.actor(ctx).Kind != model.PrincipalHuman {
		return nil, forbiddenErrorf("only a human operator may %s a configuration change", verb)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &NotFoundError{ID: id}
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return nil, err
	}
	if obs == nil || obs.PendingConfigChange == nil {
		return nil, validationErrorf("task %s is not waiting on a config change", id)
	}
	if strings.TrimSpace(headSHA) == "" {
		return nil, validationErrorf("headSha is required: the commit you were shown")
	}
	if headSHA != obs.PendingConfigChange.HeadSHA {
		return nil, ErrConfigChangeMoved
	}
	return obs.PendingConfigChange, nil
}

// ApproveConfigChange merges the change task id is parked on into
// grain/config, applies it, and wakes the session with the news.
func (c *Client) ApproveConfigChange(ctx context.Context, id string, decision ConfigChangeDecision) error {
	pending, err := c.pendingConfigChangeFor(ctx, id, decision.HeadSHA, "approve")
	if err != nil {
		return err
	}
	manager, err := c.configChangeManager(ctx)
	if err != nil {
		return err
	}
	merged, err := manager.MergeConfigChange(ctx, *pending)
	if err != nil {
		return err
	}
	// Applied before the session hears of it, so the turn it wakes into
	// already runs on the new configuration. A failed sync is not a
	// failed approval: the merge is in, and the next tick applies it.
	body := fmt.Sprintf("Approved and applied config change (%s): %s.", shortSHA(pending.HeadSHA), pending.Summary)
	if _, err := c.Config.StateRepo.Sync(ctx); err != nil {
		log.Printf("ui: task %s: applying approved config change %s: %v", id, shortSHA(merged), err)
		body = fmt.Sprintf("Approved config change (%s): %s. It is merged into %s, but applying it "+
			"failed (%v); grain applies it on its next sync.", shortSHA(pending.HeadSHA), pending.Summary, model.ConfigRepo(), err)
	}
	return c.decideConfigChange(ctx, id, body)
}

// RejectConfigChange deletes the change task id is parked on and wakes
// the session with the person's reason.
func (c *Client) RejectConfigChange(ctx context.Context, id string, decision ConfigChangeDecision) error {
	pending, err := c.pendingConfigChangeFor(ctx, id, decision.HeadSHA, "reject")
	if err != nil {
		return err
	}
	manager, err := c.configChangeManager(ctx)
	if err != nil {
		return err
	}
	if err := manager.DiscardConfigChange(ctx, *pending); err != nil {
		return err
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	return c.decideConfigChange(ctx, id, fmt.Sprintf("Rejected config change (%s): %s", shortSHA(pending.HeadSHA), reason))
}

// decideConfigChange posts the decision -- the person's own comment,
// which un-parks the task as a reply does (SetPendingSecret's reasoning)
// -- and stops offering the change.
func (c *Client) decideConfigChange(ctx context.Context, id, body string) error {
	if err := c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.PendingConfigChange = nil
	}); err != nil {
		return err
	}
	return c.AddComment(ctx, id, body, nil)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (s *Server) handleProposeConfigChange(w http.ResponseWriter, r *http.Request) {
	var req ConfigChangeProposal
	if !readJSON(w, r, &req) {
		return
	}
	pending, err := s.tasks.ProposeConfigChange(r.Context(), r.PathValue("id"), req)
	if s.configChangeError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, pending)
}

func (s *Server) handleApproveConfigChange(w http.ResponseWriter, r *http.Request) {
	s.handleConfigChangeDecision(w, r, s.tasks.ApproveConfigChange)
}

func (s *Server) handleRejectConfigChange(w http.ResponseWriter, r *http.Request) {
	s.handleConfigChangeDecision(w, r, s.tasks.RejectConfigChange)
}

func (s *Server) handleConfigChangeDecision(w http.ResponseWriter, r *http.Request,
	decide func(context.Context, string, ConfigChangeDecision) error) {
	// An agent's bearer token is refused before anything is read: the
	// route is internal, so no scope grants it, and this says so again
	// in the one place that decides.
	if externalTokenFromContext(r.Context()) != nil {
		writeError(w, http.StatusForbidden, apierror.Public("only a human operator may approve or reject a configuration change"))
		return
	}
	var req ConfigChangeDecision
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if s.configChangeError(w, decide(r.Context(), id, req)) {
		return
	}
	s.respondWithTask(w, r, id)
}

// configChangeError answers err, reporting whether there was one.
func (s *Server) configChangeError(w http.ResponseWriter, err error) bool {
	var invalid *ConfigPullRequestInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrConfigChangeMoved):
		writeError(w, http.StatusConflict, apierror.Public(err.Error()))
	case errors.Is(err, errConfigChangesUnavailable):
		writeError(w, http.StatusNotImplemented, apierror.Public(err.Error()))
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":      invalid.Error(),
			"validation": invalid.Validation,
		})
	default:
		writeClientError(w, err)
	}
	return true
}
