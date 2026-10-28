package ui

// The deployment-wide "pick up no new work" gate (model.PipelinePause),
// as an operator sees it: GET /api/pipeline-pause is the reading,
// POST closes the gate and DELETE opens it again, and GET /api/config
// carries the same reading so a banner can stand on every page beside
// the one the agent usage-limit pause already puts there.
//
// It is a separate resource from /api/pause, and deliberately so, even
// though both answer "why is nothing dispatching?". That one is a window
// a provider named, which expires on its own and lives in one process's
// memory (orchestrator.Pause); this one is somebody's decision, which
// lives in the store and survives a restart. Folding them into one route
// would mean an operator lifting one and getting the other, and a
// deployment that is under both -- entirely possible -- having no way to
// say so.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// PipelinePauseStatus is one reading of that gate: what
// GET /api/pipeline-pause reports, and what rides on GET /api/config as
// pipelinePause.
//
// Every field but Paused is empty when nothing is paused. UntilTask is
// the one task the pause does not apply to and whose finishing lifts it
// -- empty for an operator's own pause, which applies to everything and
// which only a person lifts.
type PipelinePauseStatus struct {
	Paused    bool      `json:"paused"`
	Since     time.Time `json:"since,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	ByKind    string    `json:"byKind,omitempty"`
	ByID      string    `json:"byId,omitempty"`
	UntilTask string    `json:"untilTask,omitempty"`
	// RunNowPasses is whether a task somebody asks to run now is started
	// in spite of this pause -- true for every pause but an upgrade's
	// hold (model.PipelinePause.AdmitsRunNow). It is what lets a banner
	// say "only tasks you Run now will start" and mean it.
	RunNowPasses bool `json:"runNowPasses,omitempty"`
	// CancelledUpgrade names the branch a resume just called an upgrade
	// off for -- see ResumePipeline, which is the only thing that ever
	// sets it, and why resuming does that. Empty everywhere else,
	// including on a resume that lifted an ordinary pause.
	CancelledUpgrade string `json:"cancelledUpgrade,omitempty"`
}

// pipelinePauseResponse is what all three routes return: the reading,
// plus -- on the two that change something -- whether this call is what
// changed it, as opposed to finding the deployment already that way.
// Both are the answer whoever pressed the button actually asked for.
type pipelinePauseResponse struct {
	Pause PipelinePauseStatus `json:"pause"`
	// Paused is POST's own report and Resumed is DELETE's; each is
	// absent on the routes it does not belong to.
	Paused  *bool `json:"pausedNow,omitempty"`
	Resumed *bool `json:"resumed,omitempty"`
}

// pausePipelineRequest is POST /api/pipeline-pause's body. Everything
// about it is optional: a caller that says nothing is pausing the
// deployment for no stated reason, which is a perfectly ordinary thing
// for an operator to do in a hurry.
type pausePipelineRequest struct {
	Reason string `json:"reason"`
	// RequestedBy and RequestedByKind name the agent pausing, for a run
	// holding dispatch-control -- the same pair POST /api/upgrade takes
	// (withAgentRequester). Empty for a person, who is the request's own
	// actor.
	RequestedBy     string `json:"requestedBy,omitempty"`
	RequestedByKind string `json:"requestedByKind,omitempty"`
}

// defaultPipelinePauseReason is what a pause taken with nothing said
// carries, so that a banner and a `grain pause` reading always have a
// sentence rather than sometimes having one.
const defaultPipelinePauseReason = "paused by hand"

// PipelinePause reports whether this deployment is picking new work up.
func (c *Client) PipelinePause(ctx context.Context) (PipelinePauseStatus, error) {
	pause, err := c.Store.GetPipelinePause(ctx)
	if err != nil {
		return PipelinePauseStatus{}, err
	}
	status := pipelinePauseStatus(pause)
	if status.RunNowPasses && c.upgradeUnderway(ctx) {
		status.RunNowPasses = false
	}
	return status, nil
}

// upgradeUnderway is orchestrator's own test of the same name, for
// RunNowPasses to say what the cycle will do: an upgrade running or
// scheduled holds a Run now back under any pause, not only under the
// hold that upgrade took itself. Not knowing reads as underway, as it
// does there.
func (c *Client) upgradeUnderway(ctx context.Context) bool {
	scheduled, err := c.Store.GetScheduledUpgrade(ctx)
	if err != nil || scheduled != nil {
		return true
	}
	if c.Config.Upgrader == nil {
		return false
	}
	status, err := c.Config.Upgrader.Status()
	return err != nil || status.Phase == upgrade.PhaseRunning
}

// PausePipeline stops this deployment picking new tasks up, and reports
// the state it left behind along with whether this call is what stopped
// it (as opposed to finding it already paused).
//
// Runs already in flight are left alone: this is a gate on dispatch, not
// a cancellation, so an operator taking a deployment out of service gets
// a queue that stops growing shorter and work that finishes rather than
// work that is thrown away. Whoever needs a run stopped closes its task,
// which is what that has always meant.
//
// A pause taken here applies to everything -- it names no task -- and so
// only a person lifts it. That is the difference from the pause grain
// takes for itself around an upgrade check, which names the check and is
// let go when the check is done (orchestrator's own reconcilers).
func (c *Client) PausePipeline(ctx context.Context, reason string, by model.Principal) (
	status PipelinePauseStatus, paused bool, err error) {
	existing, err := c.Store.GetPipelinePause(ctx)
	if err != nil {
		return PipelinePauseStatus{}, false, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = defaultPipelinePauseReason
	}
	pause := model.PipelinePause{PausedAt: c.now(), Reason: reason, By: by}
	if err := c.Store.SetPipelinePause(ctx, pause); err != nil {
		return PipelinePauseStatus{}, false, err
	}
	status = pipelinePauseStatus(&pause)
	if status.RunNowPasses && c.upgradeUnderway(ctx) {
		status.RunNowPasses = false
	}
	return status, existing == nil, nil
}

// ResumePipeline lets the deployment pick work up again, reporting
// whether there was a pause to lift. Lifting one that is not there is
// not an error, for the reason LiftAgentPause gives: whoever pressed the
// button wanted a deployment that is running, and it is.
//
// It lifts grain's own upgrade-check pause as readily as an operator's:
// somebody who has decided the check has told them enough is holding
// information this process does not have, and the check itself carries
// on running either way -- nothing here touches the task.
//
// The hold a scheduled upgrade takes is the one exception, and it is not
// an exception to that principle but an application of it: that hold is
// re-taken by the reconcile loop on every tick for as long as the
// schedule exists (orchestrator's reconcileScheduledUpgrade is
// level-triggered, which is what makes it survive a restart). Lifting it
// alone would shut the queue again within seconds and leave an operator
// pressing a button that appears not to work, so resuming a deployment
// that is paused only because it is waiting to upgrade calls that
// upgrade off as well, and says so. Asking for the queue back and asking
// to keep waiting for an empty one are not two things somebody can want
// at the same time.
func (c *Client) ResumePipeline(ctx context.Context) (status PipelinePauseStatus, resumed bool, err error) {
	pause, err := c.Store.GetPipelinePause(ctx)
	if err != nil {
		return PipelinePauseStatus{}, false, err
	}
	var cancelled string
	if pause != nil && pause.UntilTaskID == "" && pause.By == model.ScheduledUpgradePrincipal {
		scheduled, err := c.Store.GetScheduledUpgrade(ctx)
		if err != nil {
			return PipelinePauseStatus{}, false, err
		}
		if scheduled != nil {
			if _, err := c.Store.ClearScheduledUpgrade(ctx); err != nil {
				return PipelinePauseStatus{}, false, err
			}
			cancelled = scheduled.Branch
		}
	}
	resumed, err = c.Store.ClearPipelinePause(ctx)
	if err != nil {
		return PipelinePauseStatus{}, false, err
	}
	return PipelinePauseStatus{CancelledUpgrade: cancelled}, resumed, nil
}

// pipelinePauseStatus turns a stored pause (or its absence) into the
// wire shape.
func pipelinePauseStatus(pause *model.PipelinePause) PipelinePauseStatus {
	if pause == nil {
		return PipelinePauseStatus{}
	}
	return PipelinePauseStatus{
		Paused:    true,
		Since:     pause.PausedAt.UTC(),
		Reason:    pause.Reason,
		ByKind:    string(pause.By.Kind),
		ByID:      pause.By.ID,
		UntilTask: pause.UntilTaskID,

		RunNowPasses: pause.AdmitsRunNow(),
	}
}

func (s *Server) handleGetPipelinePause(w http.ResponseWriter, r *http.Request) {
	status, err := s.tasks.PipelinePause(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, pipelinePauseResponse{Pause: status})
}

// handlePausePipeline answers POST /api/pipeline-pause. The principal is
// this API's own human one: everything reaching these routes is somebody
// driving the UI or the CLI, and grain's own pauses are taken in the
// reconcile loop against the store directly rather than over HTTP.
func (s *Server) handlePausePipeline(w http.ResponseWriter, r *http.Request) {
	var req pausePipelineRequest
	if r.ContentLength != 0 && !readJSON(w, r, &req) {
		return
	}
	ctx, err := withAgentRequester(r.Context(), "a pause's requester", req.RequestedBy, req.RequestedByKind)
	if err != nil {
		writeClientError(w, err)
		return
	}
	status, paused, err := s.tasks.PausePipeline(ctx, req.Reason, s.tasks.actor(ctx))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pipelinePauseResponse{Pause: status, Paused: &paused})
}

func (s *Server) handleResumePipeline(w http.ResponseWriter, r *http.Request) {
	status, resumed, err := s.tasks.ResumePipeline(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pipelinePauseResponse{Pause: status, Resumed: &resumed})
}
