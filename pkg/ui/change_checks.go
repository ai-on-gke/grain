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
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/change"
)

// The three things a run's mcpserver asks the daemon about a task whose
// change is a CL rather than a pull request (changelists.md §3, G3-3):
// its checks now, a wait on them, and how grain CI's own tools refuse.
const (
	ChangeChecksStatus  = "status"
	ChangeChecksWait    = "wait"
	ChangeChecksRefusal = "refusal"
)

// ChangeChecksRequest is POST /api/tasks/{id}/change-checks.
type ChangeChecksRequest struct {
	Op         string   `json:"op"`
	Jobs       []string `json:"jobs,omitempty"`
	TimeoutMS  int64    `json:"timeoutMs,omitempty"`
	CreditedMS int64    `json:"creditedMs,omitempty"`
	Tool       string   `json:"tool,omitempty"`
}

// ChangeChecksResponse is its answer: a report, or a refusal's text.
type ChangeChecksResponse struct {
	Report  change.ChecksReport `json:"report"`
	Refusal string              `json:"refusal,omitempty"`
}

var errNoChangeChecks = errors.New("this task's change has no checks provider on this deployment")

// noCLYetNote ends every "no CL yet" answer. A CL made mid-run (with
// citc_cl_create) is linked only when the run ends, so an agent asking
// before then would otherwise read "no CL yet" as that CL never counting.
const noCLYetNote = "A CL this run created with citc_cl_create is not linked yet: " +
	"it becomes this task's CL when the run ends, unless it holds no files or has been deleted by then."

// ChangeChecks answers req for task id's primary change through the
// registered change.Checks. A kind blocked on expired credentials answers
// with the block rather than polling.
func (c *Client) ChangeChecks(ctx context.Context, id string, req ChangeChecksRequest) (ChangeChecksResponse, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return ChangeChecksResponse{}, err
	}
	if task == nil {
		return ChangeChecksResponse{}, &NotFoundError{ID: id}
	}
	checks, p, target, ok := change.ChecksFor(*task)
	if target == "" && req.Op != ChangeChecksRefusal {
		return ChangeChecksResponse{}, validationErrorf("this task has no CL yet, so it has no checks: " +
			"call open_pull_request with a summary and body to create and upload it, then call this again. " + noCLYetNote)
	}
	if !ok {
		return ChangeChecksResponse{}, errNoChangeChecks
	}
	if req.Op == ChangeChecksRefusal {
		return ChangeChecksResponse{Refusal: checks.GrainCIRefusal(req.Tool)}, nil
	}
	if h := change.HealthOf(change.HealthKind(p)); h.Blocked != "" {
		return ChangeChecksResponse{}, apierror.Expose(&change.CredentialsExpiredError{Kind: change.HealthKind(p),
			Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339) + "; stop and resume_later with message: true"})
	}
	var report change.ChecksReport
	switch req.Op {
	case ChangeChecksStatus:
		report, err = checks.ChecksStatus(ctx, *task, target)
	case ChangeChecksWait:
		report, err = checks.WaitChecks(ctx, *task, target, change.ChecksWait{
			Jobs:     req.Jobs,
			Timeout:  time.Duration(req.TimeoutMS) * time.Millisecond,
			Credited: time.Duration(req.CreditedMS) * time.Millisecond,
		})
	default:
		return ChangeChecksResponse{}, validationErrorf("unknown change-checks op %q", req.Op)
	}
	if err != nil {
		change.NoteError(ctx, p, err, c.Store, c.now())
		return ChangeChecksResponse{}, apierror.Expose(err)
	}
	// What the agent just read is the freshest the badge can be. Only
	// the presubmit half is replaced: the review state, LGTMs and
	// needs-mailing the last poll saw are not in a checks report, and a
	// wait narrowed to some checks says nothing about the CL's presubmit.
	if !report.Selective && len(req.Jobs) == 0 {
		obs, _ := change.LastObserved(target)
		if obs.Snapshot != report.Snapshot {
			// Whether a newer snapshot may be submitted is the next
			// poll's to say, not the last one's.
			obs.Submittable, obs.Reasons = false, nil
		}
		obs.Target, obs.Health, obs.Snapshot = target, report.Health, report.Snapshot
		obs.Presubmit, obs.Checks = report.Presubmit, failingChecks(report.Failing)
		change.Remember(obs)
	}
	return ChangeChecksResponse{Report: report}, nil
}

func failingChecks(names []string) []change.Check {
	var out []change.Check
	for _, n := range names {
		out = append(out, change.Check{Name: n, State: "failed"})
	}
	return out
}

// handleChangeChecks answers POST /api/tasks/{id}/change-checks.
func (s *Server) handleChangeChecks(w http.ResponseWriter, r *http.Request) {
	var req ChangeChecksRequest
	if !readJSON(w, r, &req) {
		return
	}
	resp, err := s.tasks.ChangeChecks(r.Context(), r.PathValue("id"), req)
	if errors.Is(err, errNoChangeChecks) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ChangeChecks is Client.ChangeChecks over the wire, for a run's
// mcpserver. A wait blocks for up to req's timeout, which its caller
// keeps under the client's response-header timeout.
func (c *HTTPClient) ChangeChecks(ctx context.Context, id string, req ChangeChecksRequest) (ChangeChecksResponse, error) {
	var resp ChangeChecksResponse
	if err := c.do(ctx, http.MethodPost, "/api/tasks/"+id+"/change-checks", req, &resp); err != nil {
		return ChangeChecksResponse{}, fmt.Errorf("asking grain about this task's change checks: %w", err)
	}
	return resp, nil
}

// ChangePresubmit is a CL's last observed presubmit, as the task view
// and inbox badge it: State is passed, failed, running, not_run or
// unknown, and Failing names the failing checks.
type ChangePresubmit struct {
	State    string   `json:"state"`
	Health   string   `json:"health,omitempty"`
	Snapshot int      `json:"snapshot,omitempty"`
	Failing  []string `json:"failing,omitempty"`
}

func changePresubmit(target string) *ChangePresubmit {
	obs, ok := change.LastObserved(target)
	if !ok {
		return nil
	}
	state := obs.Presubmit
	if state == "" {
		state = change.PresubmitUnknown
	}
	return &ChangePresubmit{State: state, Health: string(obs.Health), Snapshot: obs.Snapshot, Failing: obs.Failing()}
}
