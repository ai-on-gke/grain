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

// One repository's task lifecycle, as an operator sees it
// (docs/task-lifecycle-sandbox.md, "Grain's own API").
//
// Everything here is operator-facing, and nothing here is reachable by a
// definition: a reducer runs in a sandbox with no network at all, so the
// only thing it can reach is the socket grain exec's its way to.
//
// There is deliberately no build endpoint and no build log. A build is a
// release run, so it is already an object with a log and a live view
// (pkg/ci's live.go), reachable where every other run of that repository
// is. POST .../lifecycle/refresh only re-resolves the tag and pulls --
// what an operator needs after publishing an image out of band --
// and rebuilding is POST /api/repos/{owner}/{name}/release-runs, which
// already exists.

import (
	"context"
	"net/http"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Lifecycles is what this package needs of a lifecycle registry
// (pkg/lifecycle's Registry, which satisfies it), kept as an interface so
// that pkg/ui does not depend on a sandbox pool to serve a settings pane.
//
// A nil one is a deployment with no lifecycle registry wired up, which
// answers `none` for every repository: exactly what a repository with no
// declaration gets, and what every repository gets today.
type Lifecycles interface {
	Status(ctx context.Context, repo model.RepoRef) model.LifecycleStatus
	Refresh(ctx context.Context, repo model.RepoRef) (model.LifecycleStatus, error)
}

// RepoLifecycle reads one repository's lifecycle.
func (c *Client) RepoLifecycle(ctx context.Context, repo string) (model.LifecycleStatus, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return model.LifecycleStatus{}, validationErrorf("repo: %v", err)
	}
	if c.Lifecycles == nil {
		return model.LifecycleStatus{State: model.LifecycleNone}, nil
	}
	return c.Lifecycles.Status(ctx, parsed), nil
}

// RefreshRepoLifecycle re-resolves the declared image and pulls it.
//
// A refresh that fails answers the status rather than only the error,
// because the status is where the failure is recorded and is what the
// pane is about to show either way: `unpublished` with the tag that did
// not resolve is a more useful thing to look at than a bare 500.
func (c *Client) RefreshRepoLifecycle(ctx context.Context, repo string) (model.LifecycleStatus, error) {
	parsed, err := c.parseRepo(repo)
	if err != nil {
		return model.LifecycleStatus{}, validationErrorf("repo: %v", err)
	}
	if c.Lifecycles == nil {
		return model.LifecycleStatus{State: model.LifecycleNone}, nil
	}
	return c.Lifecycles.Refresh(ctx, parsed)
}

func (s *Server) handleGetRepoLifecycle(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.RepoLifecycle(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRefreshRepoLifecycle(w http.ResponseWriter, r *http.Request) {
	out, refreshErr := s.tasks.RefreshRepoLifecycle(r.Context(), r.PathValue("owner")+"/"+r.PathValue("name"))
	if out.State == "" {
		writeClientError(w, refreshErr)
		return
	}
	// The status carries the error on its own `error` field, which is
	// where the pane reads it from, so a refusal is 200 with a state
	// somebody can act on rather than a status code they have to
	// interpret. A request that could not even be attributed to a
	// repository is the case above.
	writeJSON(w, http.StatusOK, out)
}
