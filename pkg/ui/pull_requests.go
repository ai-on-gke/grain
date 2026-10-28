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

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// PullRequestStatus is one task's pull request as
// POST /api/tasks/{id}/pull-request reports it: the pull request now open
// for that task's branch, plus whatever the repo's own CI has said about
// it so far.
//
// Deliberately its own type rather than orchestrator.PullRequestStatus
// itself, for the same reason SandboxSnapshot is not
// orchestrator.SandboxHealth: this package does not import
// pkg/orchestrator (a presentation-layer package importing core dispatch
// logic runs the wrong way), so pkg/cli/daemon.go is the one place both
// types are ever in scope, converting one into the other field for field.
type PullRequestStatus struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	// Checks is what GitHub reports against the pull request's head right
	// now. Empty is a real answer -- CI that has not started yet reports
	// nothing, and a repo with no CI at all never will -- which is why
	// ChecksAvailable is reported separately.
	Checks []CheckStatus `json:"checks"`
	// ChecksAvailable is false when this deployment's GitHub credential
	// cannot read checks at all (the same fact GET /api/config reports
	// deployment-wide as autoMergeDegraded), so a caller can tell "CI has
	// not reported yet" from "grain cannot see CI here".
	ChecksAvailable bool `json:"checksAvailable"`
	// ChecksError is set when the pull request was opened but reading its
	// checks failed. The pull request above is real either way: opening
	// one is not retryable in the way a read is, so a failed read must not
	// cost the caller the number it just asked for.
	ChecksError string `json:"checksError,omitempty"`
	// Note is what grain found about part of the branch already being on
	// its base (orchestrator's landed.go), when part of it was.
	Note string `json:"note,omitempty"`
}

// CheckStatus is one check run (or GitHub Actions workflow run, on a
// deployment whose credential can only read those) against a pull
// request's head. Status is GitHub's own "queued"/"in_progress"/
// "completed"; Conclusion is empty until Status is "completed", and then
// is GitHub's own "success"/"failure"/... verbatim -- neither is narrowed
// here, since which conclusions count as broken is the reader's decision.
type CheckStatus struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

// PullRequests is implemented by whatever can actually open a task's pull
// request on GitHub -- pkg/cli/daemon.go's own pullRequestOpener over
// orchestrator.OpenPullRequestForTask and the daemon's GitHub client, in
// a real deployment. See Config.PullRequests' own doc comment for the
// nil-means-unavailable contract this interface's zero value satisfies.
type PullRequests interface {
	OpenForTask(ctx context.Context, taskID string) (PullRequestStatus, error)
}

// PullRequestReattacher is the optional half of PullRequests that takes
// back, on a reopened task, the pull request still open from its own
// branch (orchestrator.ReattachPullRequest) -- see Client.Reopen.
type PullRequestReattacher interface {
	ReattachForTask(ctx context.Context, taskID string) error
}

// errPullRequestsUnavailable is what handleOpenPullRequest reports when
// Config.PullRequests is nil -- see Config.PullRequests' own doc comment.
var errPullRequestsUnavailable = errors.New(
	"opening a pull request is not available: this deployment's UI is not wired to a GitHub client")

// OpenPullRequest opens (or finds) the pull request for id's own branch
// and reports its current checks, without waiting for the run working on
// that task to end.
//
// This exists for the agent, not for the browser: a dispatched run
// reaches it through the open_pull_request tool its own MCP server
// exposes (pkg/mcp, wired in pkg/cli/mcpserver.go), which is how a run
// gets to see its branch's CI results while it still has turns left to
// fix what they say. Nothing about it is agent-specific, though, and a
// human calling it by hand gets the same pull request the run's own
// finish would have opened.
func (c *Client) OpenPullRequest(ctx context.Context, id string) (PullRequestStatus, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return PullRequestStatus{}, err
	}
	if task == nil {
		return PullRequestStatus{}, &NotFoundError{ID: id}
	}
	if c.Config.PullRequests == nil {
		return PullRequestStatus{}, errPullRequestsUnavailable
	}
	return c.Config.PullRequests.OpenForTask(ctx, id)
}

// handleOpenPullRequest answers POST /api/tasks/{id}/pull-request.
//
// The nil check is here as well as in OpenPullRequest so an unwired
// deployment answers 404 -- "this deployment does not offer that" -- the
// same way handleRebootHost does for a nil Config.Reboot, rather than
// turning a missing feature into a 500 that reads like a failure.
func (s *Server) handleOpenPullRequest(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.PullRequests == nil {
		writeError(w, http.StatusNotFound, errPullRequestsUnavailable)
		return
	}
	status, err := s.tasks.OpenPullRequest(r.Context(), r.PathValue("id"))
	// A branch whose work is already on its base has nothing to propose
	// (orchestrator's alreadyLandedError). That is the answer, not a
	// fault: 409, with the sentence saying where the work landed, rather
	// than a 500 that reads to the agent as grain having broken.
	var landed interface{ NothingToPropose() bool }
	if err != nil && errors.As(err, &landed) && landed.NothingToPropose() {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		// The agent reads why the pull request did not open and acts on it.
		writeClientError(w, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, status)
}
