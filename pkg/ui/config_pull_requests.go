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
	"path"
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A direct config pull request (design/design/config-repo-settings.design.md,
// section 4.1): the edit a refused configuration write handed back
// (configWriteRehearsal), submitted as it is to open a pull request in
// grain/config. It is validated before it is opened
// (staterepo.ValidateCandidateConfig) and opened held: nothing merges it
// but a person's Submit.

// ConfigPullRequestRequest is POST /api/state-repo/pull-requests.
type ConfigPullRequestRequest struct {
	// Title names the pull request; empty takes one from the files it
	// changes.
	Title string `json:"title,omitempty"`
	// Body is the pull request's description.
	Body string `json:"body,omitempty"`
	// Changes is the edit: the "change" of a config_managed_externally
	// refusal, unaltered.
	Changes []staterepo.FileChange `json:"changes"`
	// User is who asked, filled in by the handler.
	User string `json:"-"`
}

// ConfigPullRequest is the pull request opened.
type ConfigPullRequest struct {
	// TaskID is the task carrying the pull request: a grain/config pull
	// request is the change of a task targeting it, numbered by it.
	TaskID string `json:"taskId"`
	Number int    `json:"number"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Commit string `json:"commit"`
	URL    string `json:"url"`
	// Validation is what the pre-review suite reported.
	Validation staterepo.CandidateReport `json:"validation"`
}

// ConfigPullRequestOpener is what a StateRepoManager implements when it
// can open config pull requests (pkg/daemon's StateManager does).
type ConfigPullRequestOpener interface {
	OpenConfigPullRequest(ctx context.Context, req ConfigPullRequestRequest) (ConfigPullRequest, error)
}

// ConfigPullRequestInvalid is a change the pre-review suite refused:
// answered 422 with the reason, since the request was well formed and
// the configuration it would leave is what is wrong.
type ConfigPullRequestInvalid struct {
	Err        error
	Validation staterepo.CandidateReport
	// Stale is a change that no longer applies to the base branch:
	// grain/config moved since the refusal that produced it, so the
	// person has to make the change again for a patch that does.
	Stale bool
}

// ConfigChangeStale is the code a 422 carries for a stale change.
const ConfigChangeStale = "config_change_stale"

func (e *ConfigPullRequestInvalid) Error() string {
	return "the config change does not pass validation: " + e.Err.Error()
}

func (e *ConfigPullRequestInvalid) Unwrap() error { return e.Err }

// errConfigPullRequestsUnavailable answers a UI with no grain/config
// repository to open a pull request in (`grain demo`).
var errConfigPullRequestsUnavailable = errors.New("this deployment's UI does not manage a config repository to open a pull request in")

// validateConfigChanges refuses a change set no refusal could have
// produced: empty, an unknown op, or a path outside what grain exports.
func validateConfigChanges(changes []staterepo.FileChange) error {
	if len(changes) == 0 {
		return validationErrorf("changes is required: the change of a config_managed_externally refusal")
	}
	seen := map[string]bool{}
	for _, c := range changes {
		p := c.Path
		if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.HasPrefix(p, "../") || p == ".." {
			return validationErrorf("change path %q is not a clean repository-relative path", p)
		}
		if !staterepo.BrowsablePath(p) {
			return validationErrorf("change path %q is not a file grain exports", p)
		}
		if seen[p] {
			return validationErrorf("change path %q appears twice", p)
		}
		seen[p] = true
		switch c.Op {
		case staterepo.ChangeCreate, staterepo.ChangeModify, staterepo.ChangeDelete:
		default:
			return validationErrorf("change %s has op %q, want create, modify or delete", p, c.Op)
		}
		if c.Patch == "" {
			return validationErrorf("change %s has no patch", p)
		}
	}
	return nil
}

// OpenConfigPullRequest opens a held, validated pull request in
// grain/config carrying req.Changes. Only a person may: an agent opens a
// config pull request the way it opens any other, from a task on the
// repository.
func (c *Client) OpenConfigPullRequest(ctx context.Context, req ConfigPullRequestRequest) (ConfigPullRequest, error) {
	if c.actor(ctx).Kind != model.PrincipalHuman {
		return ConfigPullRequest{}, forbiddenErrorf("only a person may open a config pull request here; an agent opens one from a task on %s", internalConfigRepo)
	}
	if err := validateConfigChanges(req.Changes); err != nil {
		return ConfigPullRequest{}, err
	}
	if remote, _ := c.externalConfigRepo(ctx); remote != "" {
		return ConfigPullRequest{}, validationErrorf("configuration is managed in the external repository %s: open the pull request there", remote)
	}
	opener, ok := c.Config.StateRepo.(ConfigPullRequestOpener)
	if c.Config.StateRepo == nil || !ok {
		return ConfigPullRequest{}, errConfigPullRequestsUnavailable
	}
	return opener.OpenConfigPullRequest(ctx, req)
}

func (s *Server) handleOpenConfigPullRequest(w http.ResponseWriter, r *http.Request) {
	var req ConfigPullRequestRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.User = actingUser(r, "")
	pr, err := s.tasks.OpenConfigPullRequest(r.Context(), req)
	var invalid *ConfigPullRequestInvalid
	switch {
	case errors.Is(err, errConfigPullRequestsUnavailable):
		writeError(w, http.StatusNotImplemented, apierror.Public(err.Error()))
	case errors.As(err, &invalid):
		body := map[string]any{
			"error":      invalid.Error(),
			"validation": invalid.Validation,
		}
		if invalid.Stale {
			body["code"] = ConfigChangeStale
		}
		writeJSON(w, http.StatusUnprocessableEntity, body)
	case err != nil:
		writeClientError(w, err)
	default:
		writeJSON(w, http.StatusCreated, pr)
	}
}
