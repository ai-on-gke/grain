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

// Creating a repository from grain: Settings → Repos' "New repository"
// and POST /api/repos/create. The forge half -- making the repository
// and making sure the deployment's credential reaches it -- is the
// host's repocreate.Creator; connecting it (targetRepos, the repo_config
// row) is here, the same for every host. The answer is the steps, each
// with the manual fix for one that could not be done, so the operator
// is never left with a bare 403.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repocreate"
)

// CreateRepoRequest is POST /api/repos/create's body.
type CreateRepoRequest struct {
	// Host is the forge, github.com without one (GET /api/repo-hosts).
	Host  string `json:"host,omitempty"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	// Visibility is private (the default) or public.
	Visibility  string `json:"visibility,omitempty"`
	Description string `json:"description,omitempty"`
	// Init creates a README on the default branch; on unless false.
	Init *bool `json:"init,omitempty"`
	// Template is an owner/repo to create the repository from.
	Template string `json:"template,omitempty"`
	// CIMode is the new repo_config row's ci mode (grain, github or none).
	CIMode string `json:"ci_mode,omitempty"`
	// DefaultCapabilities are granted to every task on the repository.
	DefaultCapabilities []string `json:"default_capabilities,omitempty"`
	// Reads are repositories (owner/name) the repository's grain CI
	// jobs may read: repo-read: CI capabilities on the row, the one
	// per-repository read grant repo_config holds.
	Reads []string `json:"reads,omitempty"`
	User  string   `json:"user,omitempty"`
}

// CreateRepoResult is POST /api/repos/create's answer.
type CreateRepoResult struct {
	// Repo is the repository as the target list now names it.
	Repo string `json:"repo"`
	Host string `json:"host"`
	// Created is true when this call made it, Existed when it was there
	// already and was only connected.
	Created bool `json:"created"`
	Existed bool `json:"existed"`
	// Connected is true when the repository is now one the deployment
	// works in.
	Connected     bool   `json:"connected"`
	DefaultBranch string `json:"default_branch,omitempty"`
	// Complete is true when no step failed.
	Complete bool              `json:"complete"`
	Steps    []repocreate.Step `json:"steps"`
}

// RepoHostsResult is GET /api/repo-hosts' answer.
type RepoHostsResult struct {
	// Hosts is every host a repository can be created on: one with a
	// registered creator, and github.com first when it is one.
	Hosts []string `json:"hosts"`
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	var req CreateRepoRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.CreateRepo(r.Context(), req, actingUser(r, req.User))
	writeDeploymentResult(w, res, err)
}

func (s *Server) handleRepoHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tasks.RepoHosts(r.Context()))
}

// RepoHosts is the hosts a repository can be created on: every one with
// a registered repocreate.Creator, among them the hosts the deployment's
// git connections are on that a registered pattern covers.
func (c *Client) RepoHosts(ctx context.Context) RepoHostsResult {
	var known []string
	if c.Store != nil {
		if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
			for _, conn := range cfg.EffectiveGitConnections() {
				known = append(known, conn.Host)
			}
		}
	}
	hosts := repocreate.Hosts(known)
	out := RepoHostsResult{Hosts: []string{}}
	for _, h := range hosts {
		if h == model.DefaultGitHost {
			out.Hosts = append([]string{h}, out.Hosts...)
		} else {
			out.Hosts = append(out.Hosts, h)
		}
	}
	return out
}

// CreateRepo creates req's repository through its host's creator and
// connects it. A step the creator could not do is reported, not
// returned as an error: the error is for a request that names nothing
// creatable.
func (c *Client) CreateRepo(ctx context.Context, req CreateRepoRequest, user string) (CreateRepoResult, error) {
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if host == "" {
		host = model.DefaultGitHost
	}
	if h, err := model.NormalizeGitHost(host); err == nil && h != "" {
		host = h
	}
	creator, ok := repocreate.For(host)
	if !ok {
		return CreateRepoResult{}, validationErrorf("no repository creator is registered for %s; it can create on %s",
			host, strings.Join(c.RepoHosts(ctx).Hosts, ", "))
	}
	visibility := strings.ToLower(strings.TrimSpace(req.Visibility))
	if visibility != "" && visibility != "private" && visibility != "public" {
		return CreateRepoResult{}, validationErrorf("visibility must be private or public, not %q", req.Visibility)
	}
	mode := strings.ToLower(strings.TrimSpace(req.CIMode))
	if mode != "" && mode != string(model.CIModeGrain) && mode != string(model.CIModeGitHub) && mode != string(model.CIModeNone) {
		return CreateRepoResult{}, validationErrorf("invalid ci_mode %q", req.CIMode)
	}
	caps := make([]string, 0, len(req.DefaultCapabilities))
	for _, cp := range req.DefaultCapabilities {
		if cp = strings.TrimSpace(cp); cp != "" {
			caps = append(caps, cp)
		}
	}
	reads := make([]string, 0, len(req.Reads))
	for _, read := range req.Reads {
		ref, err := model.ParseRepo(strings.TrimSpace(read))
		if err != nil {
			return CreateRepoResult{}, validationErrorf("reads: %v", err)
		}
		reads = append(reads, model.RepoReadCapability(ref))
	}
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return CreateRepoResult{}, err
	}
	creq := repocreate.Request{
		Host:        host,
		Owner:       strings.TrimSpace(req.Owner),
		Name:        strings.TrimSpace(req.Name),
		Private:     visibility != "public",
		Description: strings.TrimSpace(req.Description),
		Init:        req.Init == nil || *req.Init,
		Template:    strings.TrimSpace(req.Template),
	}
	if err := repocreate.Validate(creq); err != nil {
		return CreateRepoResult{}, validationErrorf("%v", err)
	}

	out, err := creator.Create(ctx, c.repoCreateEnv(ctx, host, creq.Owner, creq.Name), creq)
	if err != nil {
		return CreateRepoResult{}, validationErrorf("%v", err)
	}
	repo := out.Repo
	if repo == "" {
		repo = creq.Repo()
	}
	if host != model.DefaultGitHost && !model.SameGitHost(host, c.gitDefaultHost(), model.DefaultGitHost) {
		repo = host + "/" + repo
	}
	res := CreateRepoResult{Repo: repo, Host: host, Created: out.Created, Existed: out.Existed,
		DefaultBranch: out.DefaultBranch, Steps: out.Steps}
	if out.Connectable() {
		c.connectCreatedRepo(ctx, &res, model.CIMode(mode), caps, reads, user)
	}
	res.Complete = res.Connected
	for _, st := range res.Steps {
		if st.Status == repocreate.StepFailed {
			res.Complete = false
		}
	}
	return res, nil
}

// connectCreatedRepo adds res.Repo to targetRepos and writes its
// repo_config row, as steps of res.
func (c *Client) connectCreatedRepo(ctx context.Context, res *CreateRepoResult, mode model.CIMode, caps, reads []string, user string) {
	var targets []string
	var err error
	if c.Store == nil {
		err = errors.New("this grain has no store")
	} else if cfg, cerr := c.Store.GetConfig(ctx); cerr != nil {
		err = cerr
	} else if cfg != nil {
		targets = cfg.TargetRepos
	}
	switch {
	case err != nil:
		res.Steps = append(res.Steps, repocreate.Step{Name: "connect", Status: repocreate.StepFailed,
			Detail: "Could not read the deployment's settings: " + err.Error() + ".",
			Fix:    `Add the repository with "Add repo" under Settings → Repos.`})
		return
	case len(targets) == 0:
		// An empty list is what means unrestricted: adding the first repo
		// would narrow the deployment to it, which a create must not do
		// behind the operator's back.
		res.Steps = append(res.Steps, repocreate.Step{Name: "connect", Status: repocreate.StepDone,
			Detail: "This deployment's target repo list is empty, which allows every repository, so it was left as it is: " +
				res.Repo + " is already one grain works in."})
	default:
		if _, err := c.AddTargetRepoForTeam(ctx, model.DefaultTeamID, res.Repo, ""); err != nil {
			res.Steps = append(res.Steps, repocreate.Step{Name: "connect", Status: repocreate.StepFailed,
				Detail: "Could not add " + res.Repo + " to the target repo list: " + err.Error() + ".",
				Fix:    `Add the repository with "Add repo" under Settings → Repos.`})
			return
		}
		res.Steps = append(res.Steps, repocreate.Step{Name: "connect", Status: repocreate.StepDone,
			Detail: "Added " + res.Repo + " to the deployment's target repos."})
	}
	res.Connected = true

	cfgReq := SetDeploymentRepoConfigRequest{User: user}
	if mode != "" {
		m := string(mode)
		cfgReq.CIMode = &m
	}
	if len(caps) > 0 {
		cfgReq.DefaultCapabilities = &caps
	}
	if len(reads) > 0 {
		cfgReq.CICapabilities = &reads
	}
	if _, err := c.SetDeploymentRepoConfigWithUser(ctx, res.Repo, cfgReq, user); err != nil {
		detail := err.Error()
		res.Steps = append(res.Steps, repocreate.Step{Name: "repo-config", Status: repocreate.StepFailed,
			Detail: "The repository is connected, but its settings were not saved: " + detail + ".",
			Fix:    "Set them on the repository's own page."})
		return
	}
	detail := "Saved the repository's settings"
	var parts []string
	if mode != "" {
		parts = append(parts, "CI mode "+string(mode))
	}
	if len(caps) > 0 {
		parts = append(parts, "default capabilities "+strings.Join(caps, ", "))
	}
	if len(reads) > 0 {
		parts = append(parts, "CI reads "+strings.Join(reads, ", "))
	}
	if len(parts) > 0 {
		detail += ": " + strings.Join(parts, "; ")
	}
	res.Steps = append(res.Steps, repocreate.Step{Name: "repo-config", Status: repocreate.StepDone, Detail: detail + "."})
}

// repoCreateEnv is what a creator may use from this deployment for a
// repository on host owned by owner.
func (c *Client) repoCreateEnv(ctx context.Context, host, owner, name string) repocreate.Env {
	env := repocreate.Env{Credentials: c.Config.Credentials}
	if c.Store != nil {
		hostArg := host
		if model.SameGitHost(host, c.gitDefaultHost(), model.DefaultGitHost) {
			hostArg = ""
		}
		if cred, err := c.Store.TeamGitCredentialOn(ctx, model.DefaultTeamID, hostArg, owner, name); err == nil {
			env.Credential = cred
		}
	}
	if reach := c.Config.GitReach; reach != nil {
		if env.Credentials == nil {
			env.Credentials = reach.Credentials
		}
		var api github.Transport
		if t, ok := reach.Transports[host]; ok {
			api = t
		} else if model.SameGitHost(host, reach.Host, model.DefaultGitHost) {
			api = reach.API
		}
		env.API = api
	}
	if store := c.Config.Secrets; store != nil {
		env.Secret = func(ctx context.Context, name string) (string, bool) {
			// A secret that is not set and a store that cannot be read
			// both leave the creator without the token, which its step
			// then says.
			v, err := store.Resolve(ctx, name)
			return v, err == nil
		}
	}
	return env
}

// CreateRepo is POST /api/repos/create.
func (c *HTTPClient) CreateRepo(ctx context.Context, req CreateRepoRequest) (CreateRepoResult, error) {
	var res CreateRepoResult
	if err := c.do(ctx, http.MethodPost, "/api/repos/create", req, &res); err != nil {
		return CreateRepoResult{}, err
	}
	return res, nil
}
