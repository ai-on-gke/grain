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
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

const refSummaryRoute = "GET /api/refs/{ref}/summary"

// RefSummary, RefCheckCounts, RefQuestion and RefPullRequestCounts are
// GET /api/refs/{ref}/summary's response shape, re-exported here so callers
// consuming pkg/ui (including external planning layers and HTTPClient) need
// not import pkg/model separately.
type (
	RefSummary           = model.RefSummary
	RefCheckCounts       = model.RefCheckCounts
	RefQuestion          = model.RefQuestion
	RefPullRequestCounts = model.RefPullRequestCounts
)

// RefSummary aggregates the state counts, CI checks, open questions, pull
// requests and latest activity timestamp across every readable task carrying
// ref, using a single indexed store query and zero transcript reads.
func (c *Client) RefSummary(ctx context.Context, ref string, readable func(string) bool) (RefSummary, error) {
	normalized, err := model.NormalizeExternalRef(ref)
	if err != nil {
		return RefSummary{}, validationErrorf("%v", err)
	}
	if normalized == "" {
		return RefSummary{}, validationErrorf("external reference is required")
	}
	rows, err := c.Store.RefTaskRollupRows(ctx, normalized)
	if err != nil {
		return RefSummary{}, err
	}
	return model.BuildRefSummary(normalized, rows, readable), nil
}

// handleRefSummary answers GET /api/refs/{ref}/summary?repo=.
func (s *Server) handleRefSummary(w http.ResponseWriter, r *http.Request) {
	rawRef := r.PathValue("ref")
	if unescaped, err := url.PathUnescape(rawRef); err == nil {
		rawRef = unescaped
	}
	readable := s.taskReadable(r)
	q := r.URL.Query()
	if len(q["repo"]) > 1 {
		writeClientError(w, validationErrorf("at most one repo query parameter may be given"))
		return
	}
	if rawRepo := strings.TrimSpace(q.Get("repo")); rawRepo != "" {
		parsed, err := s.tasks.parseRepo(rawRepo)
		if err != nil {
			writeClientError(w, err)
			return
		}
		wantRepo := parsed.String()
		base := readable
		readable = func(repo string) bool {
			return strings.EqualFold(repo, wantRepo) && base(repo)
		}
	}
	summary, err := s.tasks.RefSummary(r.Context(), rawRef, readable)
	if err != nil {
		writeClientError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, summary)
}

// taskReadable reports which task repositories the caller's tasks:read
// permission reaches across agent grants, external-agent tokens and limited
// verified-identity RBAC roles.
func (s *Server) taskReadable(r *http.Request) func(string) bool {
	ctx := r.Context()
	call, isAgent := agentCallFromContext(ctx)
	tok := externalTokenFromContext(ctx)
	role := accessRoleFromContext(ctx)
	if role != nil && role.Full {
		role = nil
	}
	var tokSet APIGrantSet
	var hasTok bool
	if !isAgent && tok != nil {
		_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
		if err != nil {
			return func(string) bool { return false }
		}
		tokSet = set
		hasTok = true
	}
	if !isAgent && !hasTok && role == nil {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
			return func(string) bool { return false }
		}
		return func(string) bool { return true }
	}
	return func(repo string) bool {
		if isAgent && !call.grants.Allows("tasks:read", repo) {
			return false
		}
		if hasTok && !tokSet.Allows("tasks:read", repo) {
			return false
		}
		if role != nil && !role.allows("tasks:read", repo) {
			return false
		}
		return true
	}
}

// taskReadableContext attaches s.taskReadable(r) as a lineage-inheritance
// guard on ctx whenever r is made by a scoped principal (an agent, an
// external-agent token, or a limited verified-identity RBAC role), leaving
// unrestricted operator and full-role callers untouched.
func (s *Server) taskReadableContext(r *http.Request) context.Context {
	if !taskReadScoped(r) {
		return r.Context()
	}
	return model.WithExternalRefReadable(r.Context(), s.taskReadable(r))
}

// taskReadScoped reports whether r is made by a scoped principal (an agent,
// an external-agent token, or a limited verified-identity RBAC role) whose
// tasks:read reach s.taskReadable(r) narrows, rather than an unrestricted
// operator or full-role caller.
func taskReadScoped(r *http.Request) bool {
	ctx := r.Context()
	_, isAgent := agentCallFromContext(ctx)
	tok := externalTokenFromContext(ctx)
	role := accessRoleFromContext(ctx)
	if role != nil && role.Full {
		role = nil
	}
	p, hasP := PrincipalFromContext(ctx)
	return isAgent || tok != nil || role != nil || (hasP && p.Kind == model.PrincipalAgent)
}

// isRepoNarrowedTasksRead reports whether pattern's handler narrows its
// answer to s.taskReadable(r) when no ?repo= is given, so a caller holding
// tasks:read on a subset of repositories may call it without naming one.
func isRepoNarrowedTasksRead(pattern string, r *http.Request) bool {
	q := r.URL.Query()
	if repos := q["repo"]; len(repos) > 1 || (len(repos) == 1 && strings.TrimSpace(repos[0]) != "") {
		return false
	}
	switch pattern {
	case refSummaryRoute, "GET /api/notifications":
		return true
	case "GET /api/tasks":
		refs := q["externalRef"]
		return len(refs) == 1 && strings.TrimSpace(refs[0]) != ""
	default:
		return false
	}
}

// RefSummary calls GET /api/refs/{ref}/summary, optionally narrowed to repo.
func (c *HTTPClient) RefSummary(ctx context.Context, ref, repo string) (RefSummary, error) {
	path := "/api/refs/" + url.PathEscape(strings.TrimSpace(ref)) + "/summary"
	if strings.TrimSpace(repo) != "" {
		q := url.Values{}
		q.Set("repo", strings.TrimSpace(repo))
		path += "?" + q.Encode()
	}
	var out RefSummary
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}
