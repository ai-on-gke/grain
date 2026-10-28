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
	s.route("GET /api/deployment/repos/{owner}/{name}/config", s.handleGetDeploymentRepoConfig)
	s.route("PUT /api/deployment/repos/{owner}/{name}/config", s.handleSetDeploymentRepoConfig)
	s.route("PUT /api/deployment/repos/{owner}/{name}/git-connection", s.handleSetDeploymentRepoGitConnection)
	s.route("POST /api/deployment/broadcast", s.handleDeploymentBroadcast)
	s.route("GET /api/deployment/secret-audit", s.handleGetDeploymentSecretAudit)
	s.route("GET /api/deployment/audit-logs", s.handleGetDeploymentAuditLogs)
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
	res, err := s.tasks.ReachableRepos(r.Context(), model.DefaultTeamID)
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleListDeploymentGitConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := s.tasks.ListTeamGitConnections(r.Context(), model.DefaultTeamID, requestUser(r))
	writeDeploymentResult(w, conns, err)
}

func (s *Server) handleCreateDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	var req AddTeamGitConnectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	team, err := s.tasks.AddTeamGitConnection(r.Context(), model.DefaultTeamID, req, actingUser(r, req.User))
	writeDeploymentResult(w, team, err)
}

func (s *Server) handleDeleteDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	team, err := s.tasks.DeleteTeamGitConnection(r.Context(), model.DefaultTeamID, r.PathValue("cred"), requestUser(r))
	writeDeploymentResult(w, team, err)
}

func (s *Server) handleCheckDeploymentGitConnection(w http.ResponseWriter, r *http.Request) {
	var req CheckTeamGitConnectionRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.CheckTeamGitConnection(r.Context(), model.DefaultTeamID, req, actingUser(r, req.User))
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
	saved, err := s.tasks.PutTeamAgentBackend(r.Context(), model.DefaultTeamID, r.PathValue("backendId"), req.AgentBackend, actingUser(r, req.User))
	writeDeploymentResult(w, saved, err)
}

func (s *Server) handleDeleteDeploymentAgentBackend(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteTeamAgentBackend(r.Context(), model.DefaultTeamID, r.PathValue("backendId"), requestUser(r)); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetDeploymentRepoConfig(w http.ResponseWriter, r *http.Request) {
	res, err := s.tasks.GetDeploymentRepoConfigWithUser(r.Context(), deploymentRepo(r), requestUser(r))
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleSetDeploymentRepoConfig(w http.ResponseWriter, r *http.Request) {
	var req SetDeploymentRepoConfigRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.SetDeploymentRepoConfigWithUser(r.Context(), deploymentRepo(r), req, actingUser(r, req.User))
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleSetDeploymentRepoGitConnection(w http.ResponseWriter, r *http.Request) {
	var req SetTeamRepoGitConnectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	team, err := s.tasks.SetTeamRepoGitConnection(r.Context(), model.DefaultTeamID,
		r.PathValue("owner"), r.PathValue("name"), req, actingUser(r, req.User))
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
	req.Author = actingUser(r, req.Author)
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
	entries, err := s.tasks.ListTeamSecretAuditWithUser(r.Context(), model.DefaultTeamID, requestUser(r), 100)
	writeDeploymentResult(w, entries, err)
}

// handleGetDeploymentAuditLogs is the deployment's audit trail, newest
// first: secret resolutions and credential accesses, ?limit= of them.
func (s *Server) handleGetDeploymentAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = min(n, maxTeamAuditLogs)
	}
	entries, err := s.tasks.ListTeamAuditLogsWithUser(r.Context(), model.DefaultTeamID, requestUser(r), limit)
	writeDeploymentResult(w, entries, err)
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
		User:          actingUser(r, req.User),
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
