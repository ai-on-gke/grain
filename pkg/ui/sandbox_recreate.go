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

// SandboxRecreation is one run's rebuilt sandbox as
// POST /api/tasks/{id}/sandbox/recreate reports it: the sandbox that was
// destroyed and built again, and how much of what grain had set up in
// the old one it managed to put back.
//
// Deliberately its own type rather than orchestrator.SandboxRecreation
// itself, for the same reason PullRequestStatus and SandboxSnapshot are
// not orchestrator's own types: this package does not import
// pkg/orchestrator, so pkg/cli/daemon.go is the one place both are
// ever in scope, converting one into the other field for field.
type SandboxRecreation struct {
	// Sandbox is the rebuilt sandbox's own name -- the run's ID, which
	// the rebuild does not change.
	Sandbox string `json:"sandbox"`
	// CheckoutDir is where the task's repo was cloned again, relative to
	// the sandbox's own working directory, or empty when there was
	// nothing to clone or the clone failed (Warnings then says which).
	CheckoutDir string `json:"checkoutDir,omitempty"`
	// Restored names each piece of the run's setup that is back in
	// place; Warnings each piece that is not, with the reason. A warning
	// never fails the call: the old sandbox is gone by the time any of
	// them can happen, and a caller most needs to know what it is now
	// sitting in front of -- the same reasoning PullRequestStatus.
	// ChecksError follows.
	Restored []string `json:"restored,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// SandboxRecreator is implemented by whatever can actually destroy and
// rebuild a live run's sandbox -- pkg/cli/daemon.go's own adapter over
// orchestrator.SandboxRecreations, in a real deployment. See
// Config.SandboxRecreate's doc comment for the nil-means-unavailable
// contract this interface's zero value satisfies.
type SandboxRecreator interface {
	RecreateForTask(ctx context.Context, taskID string) (SandboxRecreation, error)
}

// errSandboxRecreateUnavailable is what handleRecreateSandbox reports
// when Config.SandboxRecreate is nil -- see that field's own doc comment.
var errSandboxRecreateUnavailable = errors.New(
	"rebuilding a sandbox is not available: this deployment's UI is not wired to the daemon that owns them")

// RecreateSandbox destroys the sandbox of id's live run and builds an
// empty one in its place, putting back what grain itself had set up in
// it -- git credentials, the task's attachments, whatever its
// capabilities placed, and a fresh clone of its repo.
//
// This exists for the agent, not for the browser: a dispatched run
// reaches it through the recreate_sandbox tool its own MCP server
// exposes (pkg/mcp, wired in pkg/cli/mcpserver.go), which is how a run
// that has wedged its sandbox -- or landed on one that is failing for
// reasons of its own -- gets a clean one without losing the turns it has
// left. Nothing about it is agent-specific, though, and a human calling
// it by hand against a stuck run gets the same clean sandbox.
//
// It refuses a task with no live run here rather than doing nothing
// quietly: a sandbox exists only while a run does, so there is nothing
// to rebuild and nothing that would be improved by pretending otherwise.
func (c *Client) RecreateSandbox(ctx context.Context, id string) (SandboxRecreation, error) {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return SandboxRecreation{}, err
	}
	if task == nil {
		return SandboxRecreation{}, &NotFoundError{ID: id}
	}
	if c.Config.SandboxRecreate == nil {
		return SandboxRecreation{}, errSandboxRecreateUnavailable
	}
	return c.Config.SandboxRecreate.RecreateForTask(ctx, id)
}

// handleRecreateSandbox answers
// POST /api/tasks/{id}/sandbox/recreate.
//
// The nil check is here as well as in RecreateSandbox so an unwired
// deployment answers 404 -- "this deployment does not offer that" --
// exactly as handleOpenPullRequest does for a nil Config.PullRequests,
// rather than turning a missing feature into a 500 that reads like a
// failure.
func (s *Server) handleRecreateSandbox(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.SandboxRecreate == nil {
		writeError(w, http.StatusNotFound, errSandboxRecreateUnavailable)
		return
	}
	recreation, err := s.tasks.RecreateSandbox(r.Context(), r.PathValue("id"))
	if err != nil {
		// The agent reads why the sandbox was not rebuilt and acts on it.
		writeClientError(w, apierror.Expose(err))
		return
	}
	writeJSON(w, http.StatusOK, recreation)
}
