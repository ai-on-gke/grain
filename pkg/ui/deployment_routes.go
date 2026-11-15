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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The deployment's own connections and configuration, under
// /api/deployment. Single-tenant grain has one owner, the deployment, so
// these routes name no team: what they configure -- git connections, the
// default model and per-model settings, model connections, the
// repositories those connections reach and the per-repo overrides -- is
// still kept on the default team's row, where stage 1 put it, until the
// schema reset moves it to the settings row (docs/single-tenant-reset.md,
// stage 6). Every caller that reaches them got past serveHTTP's access
// gate, which only a trusted caller does, so each handler asks nothing
// more of it: the deployment's own actor configures the deployment.
func (s *Server) deploymentRoutes() {
	s.route("GET /api/deployment", s.handleGetDeployment)
	s.route("PUT /api/deployment", s.handleUpdateDeployment)
	s.route("GET /api/deployment/reachable-repos", s.handleDeploymentReachableRepos)
	s.route("GET /api/deployment/git-connections", s.handleListDeploymentGitConnections)
	s.route("POST /api/deployment/git-connections", s.handleCreateDeploymentGitConnection)
	s.route("DELETE /api/deployment/git-connections/{cred}", s.handleDeleteDeploymentGitConnection)
	s.route("POST /api/deployment/git-connections/check", s.handleCheckDeploymentGitConnection)
	s.route("PUT /api/deployment/agent-backends/{backendId}", s.handlePutDeploymentAgentBackend)
	s.route("DELETE /api/deployment/agent-backends/{backendId}", s.handleDeleteDeploymentAgentBackend)
	s.route("POST /api/deployment/agent-backends/{backendId}/discover", s.handleRefreshDiscoveredModels)
	s.route("GET /api/deployment/repos/{owner}/{name}/config", s.handleGetDeploymentRepoConfig)
	s.route("PUT /api/deployment/repos/{owner}/{name}/config", s.handleSetDeploymentRepoConfig)
	s.route("PUT /api/deployment/repos/{owner}/{name}/git-connection", s.handleSetDeploymentRepoGitConnection)
	s.route("POST /api/deployment/broadcast", s.handleDeploymentBroadcast)
	s.route("GET /api/deployment/secret-audit", s.handleGetDeploymentSecretAudit)
	s.route("GET /api/deployment/audit-logs", s.handleGetDeploymentAuditLogs)
	s.route("GET /api/deployment/config-audit", s.handleGetConfigAudit)
}

// deploymentRepo is the {owner}/{name} a deployment repo route names.
func deploymentRepo(r *http.Request) string {
	return r.PathValue("owner") + "/" + r.PathValue("name")
}

// writeDeploymentResult answers a deployment route with v, or with err.
func writeDeploymentResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	team, err := s.tasks.GetTeam(r.Context(), model.DefaultTeamID)
	writeDeploymentResult(w, team, err)
}

func (s *Server) handleDeploymentReachableRepos(w http.ResponseWriter, r *http.Request) {
	filter, err := reachableReposFilterFrom(r)
	if err != nil {
		writeClientError(w, err)
		return
	}
	res, err := s.reachableReposForRequest(r, filter)
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleListDeploymentGitConnections(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	conns, err := s.tasks.ListTeamGitConnections(r.Context(), model.DefaultTeamID, requestPerson(r.Context()))
	if err != nil {
		writeClientError(w, err)
		return
	}
	if host := strings.TrimSpace(r.URL.Query().Get("host")); host != "" {
		conns = filterList(conns, func(c TeamGitConnectionView) bool {
			h := c.Host
			if h == "" {
				h = "github.com"
			}
			return strings.EqualFold(h, host)
		})
	}
	conns = filterList(conns, func(c TeamGitConnectionView) bool {
		return list.matches(c.Credential, c.Host, c.Kind, c.Scope)
	})
	if list.Paged {
		// Every row's status repeats GitHub's published host keys, the
		// editor's default known_hosts (about 450 bytes): a paged
		// caller is an agent or a script, not the editor, so a page
		// leaves them out and stays under the tool-result cap.
		for i := range conns {
			conns[i].Status.DefaultKnownHosts = ""
		}
	}
	writeList(w, conns, list)
}

func (s *Server) handleCreateDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	var req AddTeamGitConnectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	team, err := s.tasks.AddTeamGitConnection(r.Context(), model.DefaultTeamID, req, requestPerson(r.Context()))
	writeDeploymentResult(w, team, err)
}

func (s *Server) handleDeleteDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	team, err := s.tasks.DeleteTeamGitConnection(r.Context(), model.DefaultTeamID, r.PathValue("cred"), requestPerson(r.Context()))
	writeDeploymentResult(w, team, err)
}

func (s *Server) handleCheckDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	var req CheckTeamGitConnectionRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.CheckTeamGitConnection(r.Context(), model.DefaultTeamID, req, requestPerson(r.Context()))
	writeDeploymentResult(w, res, err)
}

// putDeploymentAgentBackendRequest is PUT
// /api/deployment/agent-backends/{backendId}'s body: the connection, and
// the user saving it.
type putDeploymentAgentBackendRequest struct {
	model.AgentBackend
	User string `json:"user,omitempty"`
}

func (s *Server) handlePutDeploymentAgentBackend(w http.ResponseWriter, r *http.Request) {
	var req putDeploymentAgentBackendRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.AgentBackend.ID != "" && req.AgentBackend.ID != r.PathValue("backendId") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("connection ID in body (%s) must match path (%s)", req.AgentBackend.ID, r.PathValue("backendId")))
		return
	}
	saved, err := s.tasks.PutTeamAgentBackend(r.Context(), model.DefaultTeamID, r.PathValue("backendId"), req.AgentBackend, requestPerson(r.Context()))
	if err != nil {
		// Strip the "agentBackends[X]: " prefix from the error message.
		if idx := strings.Index(err.Error(), "agentBackends["); idx >= 0 {
			if end := strings.Index(err.Error()[idx:], "]: "); end > 0 {
				err = fmt.Errorf("%s", err.Error()[idx+end+3:])
			}
		}
	}
	writeDeploymentResult(w, saved, err)
}

func (s *Server) handleDeleteDeploymentAgentBackend(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteTeamAgentBackend(r.Context(), model.DefaultTeamID, r.PathValue("backendId"), requestPerson(r.Context())); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetDeploymentRepoConfig(w http.ResponseWriter, r *http.Request) {
	res, err := s.tasks.GetDeploymentRepoConfigWithUser(r.Context(), deploymentRepo(r), requestPerson(r.Context()))
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleSetDeploymentRepoConfig(w http.ResponseWriter, r *http.Request) {
	var req SetDeploymentRepoConfigRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.SetDeploymentRepoConfigWithUser(r.Context(), deploymentRepo(r), req, requestPerson(r.Context()))
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleSetDeploymentRepoGitConnection(w http.ResponseWriter, r *http.Request) {
	var req SetTeamRepoGitConnectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	team, err := s.tasks.SetTeamRepoGitConnection(r.Context(), model.DefaultTeamID,
		r.PathValue("owner"), r.PathValue("name"), req, requestPerson(r.Context()))
	writeDeploymentResult(w, team, err)
}

// handleDeploymentBroadcast delivers a comment to every active task: the
// team-broadcast tool's hop from a run (DaemonTasks.BroadcastTeamMessage).
// A run with an agent principal broadcasts as that task whatever its body
// says; the task a body names is believed only from a caller with no
// principal of its own, a hop serveHTTP already trusts.
func (s *Server) handleDeploymentBroadcast(w http.ResponseWriter, r *http.Request) {
	var req BroadcastTeamRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Author = requestPerson(r.Context())
	if p, ok := PrincipalFromContext(r.Context()); ok {
		req.Task = ""
		if p.Kind == model.PrincipalAgent {
			req.Task = p.ID
		}
	}
	resp, err := s.tasks.BroadcastTeamMessage(r.Context(), req)
	writeDeploymentResult(w, resp, err)
}

// handleGetDeploymentSecretAudit is the deployment's newest secret
// resolutions.
func (s *Server) handleGetDeploymentSecretAudit(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageMedium)
	if err != nil {
		writeClientError(w, err)
		return
	}
	fetchLimit := 100
	if list.Paged {
		fetchLimit = maxTeamAuditLogs
	}
	entries, err := s.tasks.ListTeamSecretAuditWithUser(r.Context(), model.DefaultTeamID, requestPerson(r.Context()), fetchLimit)
	if err != nil {
		writeClientError(w, err)
		return
	}
	entries = filterList(entries, func(e model.SecretAuditEntry) bool {
		return list.matches(e.SecretName, e.Key, e.Principal, e.TaskID)
	})
	writeListBy(w, entries, list, func(e model.SecretAuditEntry) string { return strconv.FormatInt(e.ID, 10) })
}

// handleGetDeploymentAuditLogs is the deployment's audit trail, newest
// first: secret resolutions and credential accesses, ?limit= of them.
func (s *Server) handleGetDeploymentAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	// AuditSection.jsx polls ?limit=100 expecting a bare JSON array when
	// neither cursor nor q is named by a person.
	if !isAgentListCaller(r) && !q.Has("cursor") && !q.Has("q") {
		limit := 100
		if q.Has("limit") {
			if list.Limit == 0 {
				limit = maxTeamAuditLogs
			} else {
				limit = min(list.Limit, maxTeamAuditLogs)
			}
		}
		entries, err := s.tasks.ListTeamAuditLogsWithUser(r.Context(), model.DefaultTeamID, requestPerson(r.Context()), limit)
		writeDeploymentResult(w, entries, err)
		return
	}
	entries, err := s.tasks.ListTeamAuditLogsWithUser(r.Context(), model.DefaultTeamID, requestPerson(r.Context()), maxTeamAuditLogs)
	if err != nil {
		writeClientError(w, err)
		return
	}
	entries = filterList(entries, func(e model.TeamAuditEvent) bool {
		return list.matches(e.Kind, e.Resource, e.Credential, e.Principal, e.TaskID)
	})
	writeList(w, entries, list)
}

// UpdateDeploymentRequest is PUT /api/deployment's body: the deployment's
// default model, per-model settings and target repositories. It carries
// nothing about a team -- no name, members or admins -- so the route
// cannot write any.
type UpdateDeploymentRequest struct {
	DefaultModel  *string   `json:"defaultModel,omitempty"`
	DefaultModels *[]string `json:"defaultModels,omitempty"`
	// ModelSettings replaces the deployment's per-model settings when
	// present.
	ModelSettings *[]model.ModelSetting `json:"modelSettings,omitempty"`
	AgentGitName  *string               `json:"agentGitName,omitempty"`
	AgentGitEmail *string               `json:"agentGitEmail,omitempty"`
	// TargetRepos replaces the repositories the deployment dispatches
	// work into when present (grain init's -github-repo).
	TargetRepos *[]string `json:"targetRepos,omitempty"`
	User        string    `json:"user,omitempty"`
}

func (s *Server) handleUpdateDeployment(w http.ResponseWriter, r *http.Request) {
	var req UpdateDeploymentRequest
	if !readJSON(w, r, &req) {
		return
	}
	team, err := s.tasks.UpdateTeam(r.Context(), model.DefaultTeamID, UpdateTeamRequest{
		DefaultModel:  req.DefaultModel,
		DefaultModels: req.DefaultModels,
		ModelSettings: req.ModelSettings,
		AgentGitName:  req.AgentGitName,
		AgentGitEmail: req.AgentGitEmail,
		TargetRepos:   req.TargetRepos,
		User:          requestPerson(r.Context()),
	})
	writeDeploymentResult(w, team, err)
}

// GetDeployment is GET /api/deployment: the row the deployment's
// connections and default model are kept on.
func (c *HTTPClient) GetDeployment(ctx context.Context) (Team, error) {
	var team Team
	if err := c.do(ctx, http.MethodGet, "/api/deployment", nil, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

// UpdateDeployment is PUT /api/deployment.
func (c *HTTPClient) UpdateDeployment(ctx context.Context, req UpdateDeploymentRequest) (Team, error) {
	var team Team
	if err := c.do(ctx, http.MethodPut, "/api/deployment", req, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

// BroadcastDeploymentMessage is POST /api/deployment/broadcast.
func (c *HTTPClient) BroadcastDeploymentMessage(ctx context.Context, req BroadcastTeamRequest) (BroadcastTeamResponse, error) {
	var resp BroadcastTeamResponse
	if err := c.do(ctx, http.MethodPost, "/api/deployment/broadcast", req, &resp); err != nil {
		return BroadcastTeamResponse{}, err
	}
	return resp, nil
}

// ListDeploymentGitConnections is GET /api/deployment/git-connections.
func (c *HTTPClient) ListDeploymentGitConnections(ctx context.Context) ([]TeamGitConnectionView, error) {
	return doList[TeamGitConnectionView](c, ctx, "/api/deployment/git-connections")
}

// AddDeploymentGitConnection is POST /api/deployment/git-connections.
func (c *HTTPClient) AddDeploymentGitConnection(ctx context.Context, req AddTeamGitConnectionRequest) (Team, error) {
	var team Team
	if err := c.do(ctx, http.MethodPost, "/api/deployment/git-connections", req, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

// SetDeploymentRepoGitConnection picks which of the deployment's git
// connections repo uses, by credential name; an empty name puts it back
// to the owner or host default (Client.SetTeamRepoGitConnection).
func (c *HTTPClient) SetDeploymentRepoGitConnection(ctx context.Context, repo, credential string) (Team, error) {
	parsed, err := model.ParseRepo(repo)
	if err != nil {
		return Team{}, err
	}
	var team Team
	path := deploymentRepoPath(parsed) + "/git-connection"
	req := SetTeamRepoGitConnectionRequest{Credential: credential, Host: parsed.Host}
	if err := c.do(ctx, http.MethodPut, path, req, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

// GetDeploymentRepoConfig is GET /api/deployment/repos/{owner}/{name}/config.
func (c *HTTPClient) GetDeploymentRepoConfig(ctx context.Context, repo string) (DeploymentRepoConfigResponse, error) {
	return c.deploymentRepoConfig(ctx, http.MethodGet, repo, nil)
}

// SetDeploymentRepoConfig is PUT /api/deployment/repos/{owner}/{name}/config.
func (c *HTTPClient) SetDeploymentRepoConfig(ctx context.Context, repo string, req SetDeploymentRepoConfigRequest) (DeploymentRepoConfigResponse, error) {
	return c.deploymentRepoConfig(ctx, http.MethodPut, repo, req)
}

func (c *HTTPClient) deploymentRepoConfig(ctx context.Context, method, repo string, body any) (DeploymentRepoConfigResponse, error) {
	parsed, err := model.ParseRepo(repo)
	if err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	var resp DeploymentRepoConfigResponse
	if err := c.do(ctx, method, deploymentRepoPath(parsed)+"/config", body, &resp); err != nil {
		return DeploymentRepoConfigResponse{}, err
	}
	return resp, nil
}

func deploymentRepoPath(repo model.RepoRef) string {
	return "/api/deployment/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
}
