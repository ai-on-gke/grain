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
	"net/http"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The pull-request routes behind the external CLI's `grain pr adopt`,
// `grain pr review` and `grain pr status` (external-cli spec #XC-28,
// #XC-29): POST /api/pull-requests/adopt, POST /api/pull-requests/review
// and GET /api/pull-requests/status. They name a pull request, not a
// task, so the agent gate reads its repository out of the request's `pr`
// (pullRequestCallRepo), and each route asks for its second scope on that
// repository itself (requirePullRequestScopes). The work is
// orchestrator's (takeover_adopt.go), reached through the daemon's
// Config.PullRequests when it is a PullRequestAdopter.

// PullRequestAdopter is the optional half of PullRequests that adopts,
// reviews and reports on a pull request grain did not open.
type PullRequestAdopter interface {
	AdoptPullRequest(ctx context.Context, in model.AdoptPullRequestInput) (model.PullRequestAdoption, error)
	ReviewPullRequest(ctx context.Context, in model.ReviewPullRequestInput) (model.PullRequestReviewFiling, error)
	PullRequestStatus(ctx context.Context, ref model.PullRequestRef, threads bool) (model.PullRequestReport, error)
}

// AdoptPullRequestRequest is POST /api/pull-requests/adopt.
type AdoptPullRequestRequest struct {
	// PR is "owner/name#n" or the pull request's URL.
	PR string `json:"pr"`
	// Mode is "review-only" (the default) or "finish".
	Mode string `json:"mode,omitempty"`
	// Spec and Design are repository paths under design/.
	Spec   string `json:"spec,omitempty"`
	Design string `json:"design,omitempty"`
}

// ReviewPullRequestRequest is POST /api/pull-requests/review.
type ReviewPullRequestRequest struct {
	PR     string `json:"pr"`
	Design bool   `json:"design,omitempty"`
	Spec   string `json:"spec,omitempty"`
}

var errPullRequestAdoptUnavailable = errors.New(
	"adopting pull requests is not available: this deployment's UI is not wired to a GitHub client")

func (c *Client) pullRequestAdopter() (PullRequestAdopter, error) {
	if a, ok := c.Config.PullRequests.(PullRequestAdopter); ok {
		return a, nil
	}
	return nil, errPullRequestAdoptUnavailable
}

// pullRequestCallRepo is the repository a pull-request route names, for
// the agent gate; ok is false for any other route.
func pullRequestCallRepo(r *http.Request, pattern string, body []byte) (string, bool, error) {
	var raw string
	switch pattern {
	case "POST /api/pull-requests/adopt", "POST /api/pull-requests/review", "POST /api/pull-requests/submit":
		var req struct {
			PR string `json:"pr"`
		}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		raw = req.PR
	case "GET /api/pull-requests/status":
		raw = r.URL.Query().Get("pr")
	default:
		return "", false, nil
	}
	ref, err := model.ParsePullRequestArg(raw)
	if err != nil {
		return "", true, validationErrorf("pr: %v", err)
	}
	return ref.Repo.String(), true, nil
}

// requirePullRequestScopes asks an agent's or limited role's call for the
// scopes beyond its route's own that a pull-request route needs on repo.
func requirePullRequestScopes(ctx context.Context, repo string, scopes ...APIScope) error {
	if a := accessRoleFromContext(ctx); a != nil && !a.Full {
		for _, scope := range scopes {
			if !a.allows(scope, repo) {
				return forbiddenErrorf("%s holds no scope %s on %s", a.label(), scope, repo)
			}
		}
	}
	call, ok := agentCallFromContext(ctx)
	if !ok {
		return nil
	}
	for _, scope := range scopes {
		if !call.grants.Allows(scope, repo) {
			return forbiddenErrorf("%s holds no api grant for scope %s on %s, which %s also needs",
				call.principalID(), scope, repo, call.pattern)
		}
	}
	return nil
}

// pullRequestFiling is who work filed by a pull-request route is for and
// whether it lands approved, under #XC-17's rules: an unlimited person's
// request is approved; a limited role's or an agent's only with
// tasks:admin on the repository, or when the deployment's owner-trust
// policy trusts an external credential's owner there. Everything else
// lands proposed.
func (c *Client) pullRequestFiling(ctx context.Context, repo model.RepoRef) (model.Attribution, bool, error) {
	actor := c.requestActor(ctx)
	if actor.Actor.Kind != model.PrincipalAgent {
		if a := accessRoleFromContext(ctx); a != nil && !a.Full {
			return actor, a.allows("tasks:admin", repo.String()), nil
		}
		return actor, true, nil
	}
	if call, ok := agentCallFromContext(ctx); ok && call.grants.Allows("tasks:admin", repo.String()) {
		return actor, true, nil
	}
	tok := externalTokenFromContext(ctx)
	if tok == nil {
		return actor, false, nil
	}
	owner := tok.OwnerPrincipal()
	if owner == nil {
		return actor, false, nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return actor, false, err
	}
	trusted := cfg != nil && model.OwnerTrustedForTask(cfg.OwnerTrust, owner.ID, repo.String(), nil, nil)
	return actor, trusted, nil
}

// pullRequestRef parses raw with its repository as this deployment
// spells it -- the spelling the agent gate checked the route's scope on
// (agentCanonicalRepo) -- so the scopes, grants and owner trust the
// handler checks after it are read for that same repository.
func (c *Client) pullRequestRef(raw string) (model.PullRequestRef, error) {
	ref, err := model.ParsePullRequestArg(raw)
	if err != nil {
		return model.PullRequestRef{}, validationErrorf("pr: %v", err)
	}
	ref.Repo = c.canonicalRepo(ref.Repo)
	return ref, nil
}

// AdoptPullRequest is POST /api/pull-requests/adopt.
func (c *Client) AdoptPullRequest(ctx context.Context, req AdoptPullRequestRequest) (model.PullRequestAdoption, error) {
	adopter, err := c.pullRequestAdopter()
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	ref, err := c.pullRequestRef(req.PR)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	if err := requirePullRequestScopes(ctx, ref.Repo.String(), "tasks:write"); err != nil {
		return model.PullRequestAdoption{}, err
	}
	mode, err := model.ParseTakeoverMode(req.Mode)
	if err != nil {
		return model.PullRequestAdoption{}, validationErrorf("mode: %v", err)
	}
	requester, approved, err := c.pullRequestFiling(ctx, ref.Repo)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	return adopter.AdoptPullRequest(ctx, model.AdoptPullRequestInput{
		PR: ref, Mode: mode, Spec: strings.TrimSpace(req.Spec), Design: strings.TrimSpace(req.Design),
		Requester: requester, Approved: approved,
	})
}

// ReviewPullRequest is POST /api/pull-requests/review.
func (c *Client) ReviewPullRequest(ctx context.Context, req ReviewPullRequestRequest) (model.PullRequestReviewFiling, error) {
	adopter, err := c.pullRequestAdopter()
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	ref, err := c.pullRequestRef(req.PR)
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	if err := requirePullRequestScopes(ctx, ref.Repo.String(), "repos:read"); err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	requester, approved, err := c.pullRequestFiling(ctx, ref.Repo)
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	return adopter.ReviewPullRequest(ctx, model.ReviewPullRequestInput{
		PR: ref, Design: req.Design, Spec: strings.TrimSpace(req.Spec), Requester: requester, Approved: approved,
	})
}

// PullRequestStatus is GET /api/pull-requests/status.
func (c *Client) PullRequestStatus(ctx context.Context, pr string, threads bool) (model.PullRequestReport, error) {
	adopter, err := c.pullRequestAdopter()
	if err != nil {
		return model.PullRequestReport{}, err
	}
	ref, err := c.pullRequestRef(pr)
	if err != nil {
		return model.PullRequestReport{}, err
	}
	if err := requirePullRequestScopes(ctx, ref.Repo.String(), "tasks:read"); err != nil {
		return model.PullRequestReport{}, err
	}
	return adopter.PullRequestStatus(ctx, ref, threads)
}

// writePullRequestError answers a pull-request route's failure.
func writePullRequestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPullRequestAdoptUnavailable), errors.Is(err, model.ErrPullRequestNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, model.ErrPullRequestConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, model.ErrPullRequestForbidden):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, model.ErrPullRequestInvalid):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeClientError(w, err)
	}
}

func (s *Server) handleAdoptPullRequest(w http.ResponseWriter, r *http.Request) {
	var req AdoptPullRequestRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.AdoptPullRequest(r.Context(), req)
	if err != nil {
		writePullRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReviewPullRequest(w http.ResponseWriter, r *http.Request) {
	var req ReviewPullRequestRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.ReviewPullRequest(r.Context(), req)
	if err != nil {
		writePullRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePullRequestStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	threads := q.Get("threads") == "1" || strings.EqualFold(q.Get("threads"), "true")
	out, err := s.tasks.PullRequestStatus(r.Context(), q.Get("pr"), threads)
	if err != nil {
		writePullRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSubmitPullRequest(w http.ResponseWriter, r *http.Request) {
	var req SubmitPullRequestRequest
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.tasks.SubmitPullRequest(r.Context(), req)
	if err != nil {
		writePullRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AdoptPullRequest is POST /api/pull-requests/adopt over the wire.
func (c *HTTPClient) AdoptPullRequest(ctx context.Context, req AdoptPullRequestRequest) (model.PullRequestAdoption, error) {
	var out model.PullRequestAdoption
	err := c.do(ctx, http.MethodPost, "/api/pull-requests/adopt", req, &out)
	return out, err
}

// ReviewPullRequest is POST /api/pull-requests/review over the wire.
func (c *HTTPClient) ReviewPullRequest(ctx context.Context, req ReviewPullRequestRequest) (model.PullRequestReviewFiling, error) {
	var out model.PullRequestReviewFiling
	err := c.do(ctx, http.MethodPost, "/api/pull-requests/review", req, &out)
	return out, err
}

// PullRequestStatus is GET /api/pull-requests/status over the wire.
func (c *HTTPClient) PullRequestStatus(ctx context.Context, pr string, threads bool) (model.PullRequestReport, error) {
	q := url.Values{"pr": {pr}}
	if threads {
		q.Set("threads", "1")
	}
	var out model.PullRequestReport
	err := c.do(ctx, http.MethodGet, "/api/pull-requests/status?"+q.Encode(), nil, &out)
	return out, err
}

// SubmitPullRequest is POST /api/pull-requests/submit over the wire.
func (c *HTTPClient) SubmitPullRequest(ctx context.Context, req SubmitPullRequestRequest) (SubmitPullRequestResult, error) {
	var out SubmitPullRequestResult
	err := c.do(ctx, http.MethodPost, "/api/pull-requests/submit", req, &out)
	return out, err
}
