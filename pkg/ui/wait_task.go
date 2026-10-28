package ui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// waitOnRequest is POST /api/tasks/{id}/wait-on's body: the tasks the run
// working on {id} wants to wait for, and whether it is parking on them
// rather than only looking.
type waitOnRequest struct {
	IDs  []string `json:"ids"`
	Park bool     `json:"park,omitempty"`
}

// waitOnResponse is that route's answer: each target as a waiting run is
// allowed to see it (model.WaitTarget), in the order asked.
type waitOnResponse struct {
	Targets []model.WaitTarget `json:"targets"`
}

// WaitOn is the daemon's half of the wait_for_task tool: it checks that
// the task id may wait on ids at all, describes each of them, and -- with
// park -- makes id wait on them for real.
//
// The refusals are dependsOnLinks', unchanged, and they apply to a
// blocking wait as much as to a park: a task cannot wait on itself, on an
// id that names no task, on a standing chat session that never closes, or
// on a task that already waits on it, since each of those is a wait
// nothing can end. Reusing the picker's own check is what keeps the two
// ways of making a task wait from disagreeing about which waits are
// possible.
//
// Parking is nothing but depends-on links: they go on id through the same
// mutate-and-retry path SetDependency takes, so a parked task is released
// by exactly what releases any queued task -- its targets closing, or a
// person removing the link -- and the scheduler needs to know nothing new
// about it. orchestrator.processResult is what notices, as the run ends,
// that it parked, and leaves the task queued rather than finishing it.
// The comment posted here is for the person reading the task's page.
func (c *Client) WaitOn(ctx context.Context, id string, ids []string, park bool) ([]model.WaitTarget, error) {
	self, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if self == nil {
		return nil, &NotFoundError{ID: id}
	}
	links, err := c.dependsOnLinks(ctx, ids, self.ID)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, validationErrorf("name at least one task to wait on")
	}
	targets := make([]model.WaitTarget, 0, len(links))
	for _, l := range links {
		target, err := c.Store.WaitTargetOf(ctx, l.Target)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, validationErrorf("depends on unknown task %s", l.Target)
		}
		targets = append(targets, *target)
	}
	if !park {
		return targets, nil
	}
	if err := c.mutate(ctx, self.ID, func(task *model.Task) error {
		for _, l := range links {
			if !hasDependency(task.Links, l.Target) {
				task.Links = append(task.Links, l)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    self.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      parkedComment(targets),
		CreatedAt: c.now(),
	}); err != nil {
		return nil, err
	}
	return targets, nil
}

// hasDependency reports whether links already has a depends-on link to
// target.
func hasDependency(links []model.Link, target string) bool {
	for _, l := range links {
		if l.Kind == model.LinkDependsOn && l.Target == target {
			return true
		}
	}
	return false
}

// parkedComment is what a parked task's conversation says about it.
func parkedComment(targets []model.WaitTarget) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = fmt.Sprintf("#%s (%s)", t.ID, t.Title)
	}
	return "grain: this task's run parked it, waiting on " + strings.Join(names, ", ") +
		". It goes back in the queue once every one of them has closed; closing one, or " +
		"removing it from this task's dependencies, releases the wait the same way."
}

// MaxParkUntil is how far off a timed park may set its wake time. A park
// is for a hold -- "hold off for 1 hour while we do repairs" -- and a wake
// time a week away is far more likely a mistyped date than a hold anybody
// asked for, so it is refused rather than left to strand the task.
const MaxParkUntil = 7 * 24 * time.Hour

// parkUntilRequest is POST /api/tasks/{id}/park-until's body.
type parkUntilRequest struct {
	Until time.Time `json:"until"`
}

// parkUntilResponse is that route's answer: the wake time as stored.
type parkUntilResponse struct {
	Until time.Time `json:"until"`
}

// ParkUntil is the daemon's half of wait_for_task's resume_at: the run
// working on id parks its own task until a time, by deferring it with that
// wake time -- model.Task.DeferredUntil, the same column propose_task's
// not_before fills and orchestrator.reconcileWake clears. So a timed park
// is released by exactly what releases any deferral with a date on it:
// the time arriving, or a person pressing Undefer.
//
// Unlike Defer, this is made while the task is running, on purpose: it is
// the run itself asking to stop and be sent back out later. Running
// outranks deferred in model.StateOf, so nothing about the run changes
// until it ends, and orchestrator.processResult then records it as parked
// rather than finishing the task.
func (c *Client) ParkUntil(ctx context.Context, id string, until time.Time) (time.Time, error) {
	self, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	if self == nil {
		return time.Time{}, &NotFoundError{ID: id}
	}
	now := c.now()
	until = until.UTC()
	if !until.After(now) {
		return time.Time{}, validationErrorf("resume time %s is not in the future", until.Format(time.RFC3339))
	}
	if until.Sub(now) > MaxParkUntil {
		return time.Time{}, validationErrorf("resume time %s is more than %s away: a park is for a hold, "+
			"not a deferral -- ask a person with ask_question instead", until.Format(time.RFC3339), MaxParkUntil)
	}
	if err := c.Store.SetDeferred(ctx, self.ID, &now, &until, now); err != nil {
		return time.Time{}, err
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: self.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: "grain: this task's run parked it until " + until.Format(time.RFC3339) +
			". It goes back in the queue once that time has passed; Undefer releases it sooner.",
		CreatedAt: now,
	}); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

// handleParkUntil answers POST /api/tasks/{id}/park-until.
func (s *Server) handleParkUntil(w http.ResponseWriter, r *http.Request) {
	var req parkUntilRequest
	if !readJSON(w, r, &req) {
		return
	}
	until, err := s.tasks.ParkUntil(r.Context(), r.PathValue("id"), req.Until)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, parkUntilResponse{Until: until})
}

// ParkUntil is Client.ParkUntil over the wire, for `grain mcpserver`'s
// wait_for_task tool (pkg/daemon's daemonTaskWaiter).
func (c *HTTPClient) ParkUntil(ctx context.Context, id string, until time.Time) (time.Time, error) {
	var resp parkUntilResponse
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/park-until",
		parkUntilRequest{Until: until}, &resp); err != nil {
		return time.Time{}, err
	}
	return resp.Until, nil
}

// handleWaitOn answers POST /api/tasks/{id}/wait-on.
func (s *Server) handleWaitOn(w http.ResponseWriter, r *http.Request) {
	var req waitOnRequest
	if !readJSON(w, r, &req) {
		return
	}
	targets, err := s.tasks.WaitOn(r.Context(), r.PathValue("id"), req.IDs, req.Park)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, waitOnResponse{Targets: targets})
}

// WaitOn is Client.WaitOn over the wire, for `grain mcpserver`'s
// wait_for_task tool (pkg/daemon's daemonTaskWaiter).
func (c *HTTPClient) WaitOn(ctx context.Context, id string, ids []string, park bool) ([]model.WaitTarget, error) {
	var resp waitOnResponse
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/wait-on",
		waitOnRequest{IDs: ids, Park: park}, &resp); err != nil {
		return nil, err
	}
	return resp.Targets, nil
}

// resumeLaterRequest is POST /api/tasks/{id}/resume-later's body, and its
// answer: the condition as it was taken.
type resumeLaterRequest struct {
	When model.ResumeOn `json:"when"`
}

// ResumeLater is the daemon's half of resume_later
// (docs/design/durable-tasks.md#DT-9), and of wait_for_task's two parks,
// which are forms of it: the run working on id stops and asks for its
// task to be queued again when when is met. What holds the task until
// then is what already held a park:
//
//   - Tasks: depends-on links on them (WaitOn with park), which the
//     scheduler releases once every one has closed;
//   - At alone: a deferral with that wake time (ParkUntil);
//   - CI or Message, with or without At: a deferral with At, or with no
//     wake time at all, released by the condition arriving
//     (model.Store.DueResumeOnCI, WakeOnMessage) or by the time;
//   - nothing: no hold, so the task is queued on the next cycle.
//
// Tasks are a condition of their own: a depends-on link is not a
// deferral, so "any of these" cannot be had across the two, and a call
// that names both is refused rather than silently waiting for all.
//
// The run is recorded as a requested suspension when it ends
// (orchestrator.processResult); nothing here ends it.
func (c *Client) ResumeLater(ctx context.Context, id string, when model.ResumeOn) (model.ResumeOn, error) {
	if len(when.Tasks) > 0 {
		if when.At != nil || when.CI != "" || when.Message {
			return when, validationErrorf("resume on tasks takes no other condition: wait on the tasks, " +
				"or on a time, CI or a message, not both")
		}
		if _, err := c.WaitOn(ctx, id, when.Tasks, true); err != nil {
			return when, err
		}
		return when, nil
	}
	if when.CI == "" && !when.Message {
		if when.At == nil {
			return when, nil
		}
		until, err := c.ParkUntil(ctx, id, *when.At)
		if err != nil {
			return when, err
		}
		when.At = &until
		return when, nil
	}
	self, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return when, err
	}
	if self == nil {
		return when, &NotFoundError{ID: id}
	}
	now := c.now()
	if when.At != nil {
		at := when.At.UTC()
		if !at.After(now) {
			return when, validationErrorf("resume time %s is not in the future", at.Format(time.RFC3339))
		}
		if at.Sub(now) > MaxParkUntil {
			return when, validationErrorf("resume time %s is more than %s away", at.Format(time.RFC3339), MaxParkUntil)
		}
		when.At = &at
	}
	if when.CI != "" && when.CI != model.ResumeOnMyBranch && strings.ContainsAny(when.CI, " \t\n") {
		return when, validationErrorf("ci %q: want a grain CI run id or %q", when.CI, model.ResumeOnMyBranch)
	}
	if err := c.Store.SetDeferred(ctx, self.ID, &now, when.At, now); err != nil {
		return when, err
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: self.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: "grain: this task's run stopped with resume_later. It goes back in the queue once " +
			when.Describe() + "; Undefer releases it sooner.",
		CreatedAt: now,
	}); err != nil {
		return when, err
	}
	return when, nil
}

// handleResumeLater answers POST /api/tasks/{id}/resume-later.
func (s *Server) handleResumeLater(w http.ResponseWriter, r *http.Request) {
	var req resumeLaterRequest
	if !readJSON(w, r, &req) {
		return
	}
	when, err := s.tasks.ResumeLater(r.Context(), r.PathValue("id"), req.When)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resumeLaterRequest{When: when})
}

// ResumeLater is Client.ResumeLater over the wire, for `grain
// mcpserver`'s resume_later and wait_for_task tools (pkg/daemon's
// daemonTaskWaiter).
func (c *HTTPClient) ResumeLater(ctx context.Context, id string, when model.ResumeOn) (model.ResumeOn, error) {
	var resp resumeLaterRequest
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/resume-later",
		resumeLaterRequest{When: when}, &resp); err != nil {
		return when, err
	}
	return resp.When, nil
}
