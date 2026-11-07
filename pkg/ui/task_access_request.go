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
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Access requests (model.PendingAccessRequest): a run's request_access
// call asks for repositories, capability grants or network access its
// task does not hold. The mcpserver checks the items while the run is
// live (POST /api/tasks/{id}/access-request/check, grain's own plumbing,
// for the run's own task), so a run learns of an unknown capability or a
// repository this deployment does not work with in the turn it asked;
// the run's finish parks the task on the request; and a person grants
// some or all of it, or denies it with a reason, from the card on the
// task (POST .../grant and .../deny).
//
// Granting applies exactly the items chosen through the paths the
// task's own editors use -- UpdateTask for reads, writes and network
// access, SetCapabilityGrant for each grant -- so the same validation
// applies, and nothing here is a second way to edit a task. A dangerous
// scope or a catalogue requires_person grant goes through because the
// one deciding is a person, which is the point of asking: both decision
// routes are "internal", so no agent scope reaches them, and both refuse
// any caller that is not a person besides. An agent holding
// api:tasks:admin cannot approve its own request, or anybody's.

// AccessRequestItemView is one requested item as the card shows it.
type AccessRequestItemView struct {
	model.AccessRequestItem
	// Grant is a grant item normalized (GrantCheck.Recorded): what the
	// task would hold, and what GrantChip renders.
	Grant string `json:"grant,omitempty"`
	// Dangerous marks a grant only a person may make
	// (model.IsDangerousGrant), RequiresPerson one the capability
	// catalogue marks as a person's decision.
	Dangerous      bool `json:"dangerous,omitempty"`
	RequiresPerson bool `json:"requiresPerson,omitempty"`
	// Held is an item the task holds already -- granted since it was
	// asked for, say.
	Held bool `json:"held,omitempty"`
	// Error is why the item could not be granted as it stands.
	Error string `json:"error,omitempty"`
}

// AccessHolds is what a task already holds, for the card's context.
type AccessHolds struct {
	Reads         []string      `json:"reads"`
	Writes        []WriteTarget `json:"writes"`
	Grants        []string      `json:"grants"`
	NetworkAccess string        `json:"networkAccess,omitempty"`
}

// AccessRequestView is TaskDetail.PendingAccess.
type AccessRequestView struct {
	Summary   string                  `json:"summary"`
	Reason    string                  `json:"reason"`
	Items     []AccessRequestItemView `json:"items"`
	CreatedAt string                  `json:"createdAt"`
	Holds     AccessHolds             `json:"holds"`
	// Earlier is what people answered earlier requests on this task that
	// asked for any of these items, latest first: a request repeating an
	// item already denied or declined shows the earlier answer.
	Earlier []model.AccessDecision `json:"earlier,omitempty"`
}

// AccessRequestCheck is POST /api/tasks/{id}/access-request/check.
type AccessRequestCheck struct {
	Items []model.AccessRequestItem `json:"items"`
}

// AccessGrantDecision is POST /api/tasks/{id}/access-request/grant: the
// indexes into the pending request's items to grant, nil for all of
// them. The rest are declined.
type AccessGrantDecision struct {
	Items *[]int `json:"items,omitempty"`
}

// AccessDenyDecision is POST /api/tasks/{id}/access-request/deny.
type AccessDenyDecision struct {
	Reason string `json:"reason"`
}

// checkAccessItem is one item judged for task: the view, with Error set
// for one that cannot be granted as it stands.
func (c *Client) checkAccessItem(ctx context.Context, task model.Task, it model.AccessRequestItem) AccessRequestItemView {
	view := AccessRequestItemView{AccessRequestItem: it}
	fail := func(err error) AccessRequestItemView {
		view.Error = err.Error()
		return view
	}
	switch it.Kind {
	case model.AccessRead, model.AccessWrite:
		repo, err := c.parseRepo(it.Repo)
		if err != nil {
			return fail(err)
		}
		view.Repo = repo.String()
		if err := c.refuseOffAllowlistForTeam(ctx, model.DefaultTeamID, repo, "it cannot be granted to a task"); err != nil {
			return fail(err)
		}
		if it.Kind == model.AccessRead {
			if task.Target != nil && task.Target.Same(repo) {
				return fail(fmt.Errorf("%s is this task's own repository, which it reads already", repo))
			}
			view.Held = slices.ContainsFunc(task.Reads, repo.Same) ||
				slices.ContainsFunc(task.Writes, func(w model.WriteTarget) bool { return w.Repo.Same(repo) })
			return view
		}
		if err := validateBase(fmt.Sprintf("base for %s", repo), it.Base); err != nil {
			return fail(err)
		}
		if task.Target != nil && task.Target.Same(repo) && it.Base == task.Base {
			return fail(fmt.Errorf("%s is this task's own repository and base branch, which it writes to already", repo))
		}
		view.Held = slices.ContainsFunc(task.Writes, func(w model.WriteTarget) bool {
			return w.Repo.Same(repo) && w.Base == it.Base
		})
		return view
	case model.AccessNetwork:
		if err := validateNetworkAccess(it.Access); err != nil {
			return fail(err)
		}
		view.Held = string(task.NetworkAccess) == it.Access
		return view
	case model.AccessGrant:
		checks, err := c.CheckCapabilityGrants(ctx, []GrantRequest{{Capability: it.Capability, With: it.With}})
		if err != nil {
			return fail(err)
		}
		check := checks[0]
		if check.Error != "" {
			return fail(fmt.Errorf("%s", check.Error))
		}
		view.Grant = check.Recorded
		if ciOnlyCapability(view.Grant) {
			return fail(fmt.Errorf("%s", model.CIOnlyRefusal(view.Grant)))
		}
		// A nil catalogue still applies the built-in director floor.
		if cat, err := c.capabilityCatalogue(ctx); err == nil {
			verdict, err := cat.ValidateGrants(c.taskGrantContext(ctx, task, []string{view.Grant}))
			if err != nil {
				return fail(err)
			}
			view.RequiresPerson = len(verdict.RequiresPerson) > 0
		}
		view.Dangerous = model.IsDangerousGrant(view.Grant)
		view.Held = slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == view.Grant })
		return view
	}
	return fail(fmt.Errorf("kind %q is not read, write, grant or network", it.Kind))
}

// CheckAccessRequest judges the items task id's run is asking for, as a
// request_access call is checked before it is relayed: any item that
// could not be granted, or that the task already holds, is refused with
// what is wrong with it, so the run can fix its request.
func (c *Client) CheckAccessRequest(ctx context.Context, id string, items []model.AccessRequestItem) ([]AccessRequestItemView, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, &NotFoundError{ID: id}
	}
	if len(items) == 0 {
		return nil, validationErrorf("requests is required: name at least one thing to ask for")
	}
	views := make([]AccessRequestItemView, 0, len(items))
	var problems []string
	for i, it := range items {
		view := c.checkAccessItem(ctx, *task, it)
		switch {
		case view.Error != "":
			problems = append(problems, fmt.Sprintf("requests[%d] (%s): %s", i, it.Label(), view.Error))
		case view.Held:
			problems = append(problems, fmt.Sprintf("requests[%d] (%s): task %s holds this already", i, it.Label(), id))
		}
		views = append(views, view)
	}
	if len(problems) > 0 {
		return nil, validationErrorf("%s", strings.Join(problems, "; "))
	}
	return views, nil
}

// accessHoldsOf is what task holds, for the card.
func accessHoldsOf(task model.Task) AccessHolds {
	holds := AccessHolds{Reads: []string{}, Writes: []WriteTarget{}, Grants: []string{},
		NetworkAccess: string(task.NetworkAccess)}
	for _, r := range task.Reads {
		holds.Reads = append(holds.Reads, r.String())
	}
	for _, w := range task.Writes {
		holds.Writes = append(holds.Writes, WriteTarget{Repo: w.Repo.String(), Base: w.Base})
	}
	for _, g := range task.Grants {
		holds.Grants = append(holds.Grants, g.Capability)
	}
	return holds
}

// pendingAccessFor is TaskDetail.PendingAccess: the request task is
// parked on, each item judged afresh, with what the task holds and the
// earlier answers to the same request. nil when it is parked on none.
func (c *Client) pendingAccessFor(ctx context.Context, task model.Task, obs *model.Observation) *AccessRequestView {
	if obs == nil || obs.PendingAccess == nil {
		return nil
	}
	req := obs.PendingAccess
	view := &AccessRequestView{
		Summary: req.Summary, Reason: req.Reason,
		Items:     make([]AccessRequestItemView, 0, len(req.Items)),
		CreatedAt: req.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Holds:     accessHoldsOf(task),
	}
	for _, it := range req.Items {
		view.Items = append(view.Items, c.checkAccessItem(ctx, task, it))
	}
	asked := make(map[string]bool, len(req.Items))
	for _, it := range req.Items {
		asked[it.Key()] = true
	}
	for i := len(obs.AccessDecisions) - 1; i >= 0; i-- {
		d := obs.AccessDecisions[i]
		for _, it := range d.Request.Items {
			if asked[it.Key()] {
				view.Earlier = append(view.Earlier, d)
				break
			}
		}
	}
	return view
}

// pendingAccessToDecide is the request task id is parked on, refusing a
// caller that is not a person.
func (c *Client) pendingAccessToDecide(ctx context.Context, id, verb string) (*model.Task, *model.Observation, error) {
	if isDelegatedRequest(ctx) {
		return nil, nil, forbiddenCodeErrorf("requires_person", "only a person acting directly may %s an access request", verb)
	}
	if c.requestActor(ctx).Actor.Kind != model.PrincipalHuman {
		return nil, nil, forbiddenErrorf("only a person may %s an access request", verb)
	}
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, &NotFoundError{ID: id}
	}
	obs, err := c.Store.GetObservation(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if obs == nil || obs.PendingAccess == nil {
		return nil, nil, validationErrorf("task %s is not waiting on an access request", id)
	}
	return task, obs, nil
}

// GrantAccessRequest applies the items of task id's pending access
// request that decision chooses, records who granted what, and posts
// the decision into the conversation, which re-queues the task.
func (c *Client) GrantAccessRequest(ctx context.Context, id string, decision AccessGrantDecision) error {
	task, obs, err := c.pendingAccessToDecide(ctx, id, "grant")
	if err != nil {
		return err
	}
	req := *obs.PendingAccess
	chosen := make([]bool, len(req.Items))
	if decision.Items == nil {
		for i := range chosen {
			chosen[i] = true
		}
	} else {
		for _, i := range *decision.Items {
			if i < 0 || i >= len(req.Items) {
				return validationErrorf("item %d is not one of the request's %d", i, len(req.Items))
			}
			chosen[i] = true
		}
	}
	var granted, declined []model.AccessRequestItem
	for i, it := range req.Items {
		if chosen[i] {
			granted = append(granted, it)
		} else {
			declined = append(declined, it)
		}
	}
	if len(granted) == 0 {
		return validationErrorf("choose at least one item to grant, or deny the request")
	}

	// Reads, writes and network access in one edit, the task pane's own;
	// then each grant through the grants editor's path.
	var update UpdateTaskRequest
	reads := accessHoldsOf(*task).Reads
	writes := accessHoldsOf(*task).Writes
	for _, it := range granted {
		view := c.checkAccessItem(ctx, *task, it)
		if view.Error != "" {
			return validationErrorf("%s: %s", it.Label(), view.Error)
		}
		if view.Held {
			continue
		}
		switch it.Kind {
		case model.AccessRead:
			reads = append(reads, view.Repo)
			update.Reads = &reads
		case model.AccessWrite:
			writes = append(writes, WriteTarget{Repo: view.Repo, Base: it.Base})
			update.Writes = &writes
		case model.AccessNetwork:
			access := it.Access
			update.NetworkAccess = &access
		}
	}
	if update.Reads != nil || update.Writes != nil || update.NetworkAccess != nil {
		if _, err := c.UpdateTask(ctx, id, update); err != nil {
			return err
		}
	}
	for _, it := range granted {
		if it.Kind != model.AccessGrant {
			continue
		}
		if err := c.SetCapabilityGrant(ctx, id, GrantRequest{Capability: it.Capability, With: it.With}, true, ""); err != nil {
			return fmt.Errorf("granting %s: %w", it.Label(), err)
		}
	}

	who := c.requestActor(ctx).Actor
	if err := c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.PendingAccess = nil
		o.AccessDecisions = model.AppendAccessDecision(o.AccessDecisions, model.AccessDecision{
			Request: req, Granted: granted, Declined: declined, By: who, At: c.now(),
		})
	}); err != nil {
		return err
	}
	body := accessDecisionBody(req, granted, declined, "")
	if err := c.AddComment(ctx, id, body, nil); err != nil {
		return err
	}
	c.noteTaskAction(ctx, id, model.TaskEventAccessGranted, accessItemsLabel(granted))
	return nil
}

// DenyAccessRequest declines task id's pending access request with the
// person's reason, which is posted into the conversation -- re-queueing
// the task -- and so reaches the next run.
func (c *Client) DenyAccessRequest(ctx context.Context, id string, decision AccessDenyDecision) error {
	_, obs, err := c.pendingAccessToDecide(ctx, id, "deny")
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		return validationErrorf("reason is required: the next run is told why it was denied")
	}
	req := *obs.PendingAccess
	who := c.requestActor(ctx).Actor
	if err := c.Store.ObserveField(ctx, id, c.now(), func(o *model.Observation) {
		o.PendingAccess = nil
		o.AccessDecisions = model.AppendAccessDecision(o.AccessDecisions, model.AccessDecision{
			Request: req, Declined: req.Items, Denied: true, Reason: reason, By: who, At: c.now(),
		})
	}); err != nil {
		return err
	}
	if err := c.AddComment(ctx, id, accessDecisionBody(req, nil, req.Items, reason), nil); err != nil {
		return err
	}
	c.noteTaskAction(ctx, id, model.TaskEventAccessDenied, reason)
	return nil
}

func accessItemsLabel(items []model.AccessRequestItem) string {
	labels := make([]string, 0, len(items))
	for _, it := range items {
		labels = append(labels, it.Label())
	}
	return strings.Join(labels, "; ")
}

// accessDecisionBody is the decision as the conversation -- and so the
// next run's prompt -- reads it. A denial says plainly not to ask again.
func accessDecisionBody(req model.PendingAccessRequest, granted, declined []model.AccessRequestItem, reason string) string {
	var b strings.Builder
	list := func(items []model.AccessRequestItem) {
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it.Label())
		}
	}
	if len(granted) == 0 {
		fmt.Fprintf(&b, "**Access request denied:** %s\n\nWhy: %s\n\nDeclined:\n", req.Summary, reason)
		list(declined)
		b.WriteString("\nDo not ask for this access again: change your approach to work without it, " +
			"or explain in a question why it is unavoidable.")
		return b.String()
	}
	fmt.Fprintf(&b, "**Access request granted:** %s\n\nGranted to this task:\n", req.Summary)
	list(granted)
	if len(declined) > 0 {
		b.WriteString("\nDeclined (not granted -- do not ask for these again):\n")
		list(declined)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Server) handleCheckAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req AccessRequestCheck
	if !readJSON(w, r, &req) {
		return
	}
	views, err := s.tasks.CheckAccessRequest(r.Context(), r.PathValue("id"), req.Items)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (s *Server) handleGrantAccessRequest(w http.ResponseWriter, r *http.Request) {
	if s.refuseAgentAccessDecision(w, r) {
		return
	}
	var req AccessGrantDecision
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.tasks.GrantAccessRequest(r.Context(), id, req); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

func (s *Server) handleDenyAccessRequest(w http.ResponseWriter, r *http.Request) {
	if s.refuseAgentAccessDecision(w, r) {
		return
	}
	var req AccessDenyDecision
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.tasks.DenyAccessRequest(r.Context(), id, req); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// refuseAgentAccessDecision refuses an agent's bearer token before
// anything is read: the decision routes are internal, so no scope grants
// them, and this says so again in the one place that decides.
func (s *Server) refuseAgentAccessDecision(w http.ResponseWriter, r *http.Request) bool {
	if externalTokenFromContext(r.Context()) == nil {
		return false
	}
	writeError(w, http.StatusForbidden, apierror.Public("only a person may grant or deny an access request"))
	return true
}
