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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
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
	return c.OpenChange(ctx, id, OpenChangeRequest{})
}

// OpenChangeRequest is what open_pull_request carries for a task whose
// change is not a git branch (a changelist CL): the description's summary
// line and body, which the agent writes as it would a commit message.
// A git task's pull request ignores both: its title and body come from
// the branch's commits.
type OpenChangeRequest struct {
	Summary string `json:"summary,omitempty"`
	Body    string `json:"body,omitempty"`
}

// OpenChange is OpenPullRequest with a change description: on a task
// with a change target (changelist), the registered change.Opener creates
// or updates its CL and uploads a snapshot, and the CL is linked to the
// task (model.LinkChangelist) as its change.
func (c *Client) OpenChange(ctx context.Context, id string, req OpenChangeRequest) (PullRequestStatus, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return PullRequestStatus{}, err
	}
	if task == nil {
		return PullRequestStatus{}, &NotFoundError{ID: id}
	}
	if task.ChangeTarget != nil {
		return c.openTargetChange(ctx, *task, req)
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
	if s.tasks.Config.PullRequests == nil && len(change.Kinds()) == 0 {
		writeError(w, http.StatusNotFound, errPullRequestsUnavailable)
		return
	}
	// A body is optional: a changelist task's open_pull_request carries its
	// CL description's summary and body (OpenChangeRequest).
	var req OpenChangeRequest
	if body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decoding the request: %w", err))
			return
		}
	}
	status, err := s.tasks.OpenChange(r.Context(), r.PathValue("id"), req)
	// A branch whose work is already on its base has nothing to propose
	// (orchestrator's alreadyLandedError). That is the answer, not a
	// fault: 409, with the sentence saying where the work landed, rather
	// than a 500 that reads to the agent as grain having broken.
	var landed interface{ NothingToPropose() bool }
	if err != nil && errors.As(err, &landed) && landed.NothingToPropose() {
		writeError(w, http.StatusConflict, err)
		return
	}
	// A git task on a deployment with no pull-request opener, reached
	// because a change provider (changelist) is registered: still 404.
	if errors.Is(err, errPullRequestsUnavailable) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		// The agent reads why the pull request did not open and acts on it.
		writeClientError(w, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// openTargetChange is open_pull_request on a task whose change target is
// not git: the description refused if it holds any of the deployment's
// own secret values (a CL is visible to all of Google once uploaded),
// then handed to the target kind's change.Opener, and the CL it answers
// with linked to the task. The first CL linked is the task's primary;
// later calls update that one (change.Primary).
func (c *Client) openTargetChange(ctx context.Context, task model.Task, req OpenChangeRequest) (PullRequestStatus, error) {
	kind := task.ChangeTarget.Kind
	opener, ok := change.OpenerFor(kind)
	if !ok {
		return PullRequestStatus{}, validationErrorf("this deployment has no change provider for %s tasks, so open_pull_request cannot create their change here", kind)
	}
	if strings.TrimSpace(req.Summary) == "" {
		return PullRequestStatus{}, validationErrorf("a %s task's open_pull_request needs a summary (the change description's first line) and a body, written as you would a commit message", kind)
	}
	if c.Config.LogRedactor != nil {
		r, err := c.Config.LogRedactor()
		if err != nil {
			return PullRequestStatus{}, fmt.Errorf("checking the description for secrets: %w", err)
		}
		if _, n := r.Text(req.Summary + "\n" + req.Body); n > 0 {
			return PullRequestStatus{}, validationErrorf("the change description contains %d of this deployment's secret values; a %s change is visible far beyond this deployment, so it was not uploaded. Remove them and call open_pull_request again", n, kind)
		}
	}
	var trailers []string
	if task.WorkOrigin != "" {
		trailers = append(trailers, "Origin: "+task.WorkOrigin.Trailer())
	}
	if parent := task.Parent(); parent != "" {
		trailers = append(trailers, "Filed-by: task "+parent)
	}
	taskURL := ""
	if base := strings.TrimRight(strings.TrimSpace(c.Config.ExternalBase), "/"); base != "" {
		taskURL = base + "/tasks/" + task.ID
	}
	p, _ := opener.(change.Provider)
	if p != nil {
		if h := change.HealthOf(kind); h.Blocked != "" {
			return PullRequestStatus{}, &change.CredentialsExpiredError{Kind: kind, Detail: "blocked since " + h.Since.UTC().Format(time.RFC3339) + "; stop and resume_later with message: true"}
		}
	}
	res, err := opener.Open(ctx, task, change.OpenRequest{Summary: req.Summary, Body: req.Body, Trailers: trailers, TaskURL: taskURL})
	if err != nil {
		if p != nil {
			change.NoteError(ctx, p, err, c.Store, c.now())
		}
		if change.IsCredentialsExpired(err) {
			return PullRequestStatus{}, apierror.Expose(fmt.Errorf("%w. Stop here and call resume_later with message: true; grain resumes the task when the credentials are renewed", err))
		}
		return PullRequestStatus{}, apierror.Expose(err)
	}
	// A new upload is something a failed automatic sync may try again on.
	change.Planner.NoteUpload(res.Target)
	link := model.Link{Kind: model.LinkChangelist, Target: res.Target}
	if err := c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if !slices.Contains(t.Links, link) {
			t.Links = append(t.Links, link)
		}
		return nil
	}); err != nil {
		return PullRequestStatus{}, fmt.Errorf("%s was uploaded but linking it to task %s failed: %w", res.Target, task.ID, err)
	}
	number, _ := strconv.Atoi(strings.TrimPrefix(res.Target, "cl/"))
	verb := "updated"
	if res.Created {
		verb = "created"
	}
	note := fmt.Sprintf("%s %s and uploaded", res.Target, verb)
	if res.Snapshot > 0 {
		note += fmt.Sprintf(" (snapshot %d)", res.Snapshot)
	}
	note += fmt.Sprintf(". Presubmit: %s. It has not been mailed: nobody has been asked to review it.", res.Presubmit)
	return PullRequestStatus{Repo: kind, Number: number, URL: res.URL, Checks: []CheckStatus{}, Note: note}, nil
}
