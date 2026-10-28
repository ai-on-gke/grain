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
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// UpgradeStatus is what an HTTPClient caller gets back from the three
// upgrade routes -- GET /api/upgrade, POST /api/upgrade and DELETE
// /api/upgrade/scheduled all answer with the same reading, which is why
// all three methods below return this one shape.
//
// An exported type of its own rather than the wire struct those routes
// marshal (upgradeStatusResponse, upgrade.go): that one is the
// frontend's, unexported on purpose, and it carries the pane's own
// per-step progress, which nothing outside the pane has ever wanted.
// This is the half a program reads -- AgentPauseStatus beside
// agentPauseResponse is the same pairing for the same reason.
type UpgradeStatus struct {
	// Enabled is false, with every other field zero, on a deployment
	// started with neither -upgrade-image nor -upgrade-src-dir. GET says
	// so quietly; POST and DELETE answer 404, which arrives here as a
	// *NotFoundError.
	Enabled bool
	// Mode is which pipeline this deployment upgrades through --
	// upgrade.ModeImage or upgrade.ModeSource, as a string.
	Mode string
	// RunningImage is the image reference this deployment's service
	// starts, and RunningCommit the commit its installed binary was
	// built from: the image path fills the first, the source path the
	// second, and a deployment that cannot say fills neither.
	RunningImage  string
	RunningCommit string
	// Channel is the channel the deployment follows (latest, candidate,
	// main, or a branch), which is what an upgrade that names no branch
	// goes to; empty when it cannot say.
	Channel string
	// Branch, Phase and Detail are the last upgrade *attempt* -- empty
	// on a deployment that has never upgraded through this pipeline.
	Branch     string
	Phase      string
	Detail     string
	Unchanged  bool
	StartedAt  *time.Time
	FinishedAt *time.Time
	// InFlightRuns is every live run, which is what an immediate upgrade
	// would discard.
	InFlightRuns int
	// Scheduled is the upgrade this deployment is holding its queue for,
	// or nil.
	Scheduled *ScheduledUpgradeStatus
}

// ScheduledUpgradeStatus is the pending schedule and the two counts it
// is waiting on: runs an agent is working in, and jobs of grain's own CI
// that have not concluded.
//
// ByKind and ByID are who asked for it: this deployment's own actor for
// an operator pressing the pane's button, and {"agent", <task id>} for a
// task that scheduled it through the self-upgrade capability.
type ScheduledUpgradeStatus struct {
	Branch      string
	RequestedAt time.Time
	ByKind      string
	ByID        string
	BusyRuns    int
	BusyCIRuns  int
}

// UpgradeStatus reads GET /api/upgrade -- where this deployment stands
// on upgrading itself, what it would cost to upgrade right now, and
// whether one is already scheduled.
//
// A deployment with no upgrader answers 200 with enabled false rather
// than an error, which is the route's own convention and is passed
// through unchanged: "this deployment does not upgrade" is an answer,
// not a failure.
func (c *HTTPClient) UpgradeStatus(ctx context.Context) (UpgradeStatus, error) {
	var resp upgradeStatusResponse
	if err := c.do(ctx, http.MethodGet, "/api/upgrade", nil, &resp); err != nil {
		return UpgradeStatus{}, err
	}
	return upgradeStatusFrom(resp), nil
}

// StartUpgrade posts POST /api/upgrade: upgrade this deployment onto
// branch, now or once it has finished what it is doing.
//
// afterTasksFinish is the scheduling mode (model.ScheduledUpgrade): the
// queue shuts, the work under way is left to finish, and the cut-over
// follows. Without it the upgrade begins at once and ends in a restart
// that discards every run in flight -- including, on a real deployment,
// whatever process made this call.
//
// requestedBy is who to record the schedule as -- the zero Principal for
// a caller with nobody to name, which the route reads as this
// deployment's own actor. It is the run behind an agent's
// upgrade_deployment call (cmd/grain's daemonUpgrade), and it is carried
// only as far as a scheduled upgrade: an immediate one writes no row.
//
// A deployment with no upgrader answers 404, which arrives as a
// *NotFoundError carrying the route's own sentence about why.
func (c *HTTPClient) StartUpgrade(
	ctx context.Context, branch string, afterTasksFinish bool, requestedBy model.Principal,
) (UpgradeStatus, error) {
	var resp upgradeStatusResponse
	req := startUpgradeRequest{
		Branch:           branch,
		AfterTasksFinish: afterTasksFinish,
		RequestedBy:      requestedBy.ID,
		RequestedByKind:  string(requestedBy.Kind),
	}
	if err := c.do(ctx, http.MethodPost, "/api/upgrade", req, &resp); err != nil {
		return UpgradeStatus{}, err
	}
	return upgradeStatusFrom(resp), nil
}

// CancelScheduledUpgrade deletes the pending schedule -- DELETE
// /api/upgrade/scheduled -- and reports whether there was one to cancel.
//
// Cancelling nothing is not an error, matching the route: the caller
// wanted a deployment with no upgrade pending and it has none, and
// cancelled false is how it says which of the two happened.
func (c *HTTPClient) CancelScheduledUpgrade(ctx context.Context) (status UpgradeStatus, cancelled bool, err error) {
	var resp cancelScheduledUpgradeResponse
	if err := c.do(ctx, http.MethodDelete, "/api/upgrade/scheduled", nil, &resp); err != nil {
		return UpgradeStatus{}, false, err
	}
	return upgradeStatusFrom(resp.upgradeStatusResponse), resp.Cancelled, nil
}

// upgradeStatusFrom flattens the wire shape into the one a program
// reads: the pane's per-step progress is dropped, and the "running"
// object's two halves become two fields, since a caller that has one
// never has the other.
func upgradeStatusFrom(resp upgradeStatusResponse) UpgradeStatus {
	status := UpgradeStatus{
		Enabled:      resp.Enabled,
		Mode:         resp.Mode,
		Channel:      resp.Channel,
		Branch:       resp.Branch,
		Phase:        resp.Phase,
		Detail:       resp.Detail,
		Unchanged:    resp.Unchanged,
		StartedAt:    resp.StartedAt,
		FinishedAt:   resp.FinishedAt,
		InFlightRuns: resp.InFlightRuns,
	}
	if resp.Running != nil {
		status.RunningImage = resp.Running.Image
		status.RunningCommit = resp.Running.Commit
	}
	if resp.Scheduled != nil {
		status.Scheduled = &ScheduledUpgradeStatus{
			Branch:      resp.Scheduled.Branch,
			RequestedAt: resp.Scheduled.RequestedAt,
			ByKind:      resp.Scheduled.ByKind,
			ByID:        resp.Scheduled.ByID,
			BusyRuns:    resp.Scheduled.BusyRuns,
			BusyCIRuns:  resp.Scheduled.BusyCIRuns,
		}
	}
	return status
}
