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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamGitConnectionView is the JSON representation of one team connection,
// including its live credential status (auth type, configured state).
type TeamGitConnectionView struct {
	Credential string              `json:"credential"`
	Host       string              `json:"host,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Scope      string              `json:"scope,omitempty"`
	AuthType   string              `json:"authType,omitempty"`
	Status     GitCredentialStatus `json:"status"`
	Legacy     bool                `json:"legacy,omitempty"`
}

// AddTeamGitConnectionRequest is the body of POST /api/deployment/git-connections.
type AddTeamGitConnectionRequest struct {
	Host string `json:"host,omitempty"`
	Kind string `json:"kind,omitempty"`
	// BaseURL is the host's scheme://host[:port], when that is not https
	// on the default port: recorded on the credential's host record.
	BaseURL string `json:"baseUrl,omitempty"`
	// Proxy is the http(s)://host:port proxy the host is reached through
	// (gitproxy.CredentialHost.Proxy). Empty on an ambient connection to a
	// command-backed forge is filled in from its tool's own configuration
	// when the tool says what it is.
	Proxy string              `json:"proxy,omitempty"`
	Scope string              `json:"scope,omitempty"`
	Type  string              `json:"type,omitempty"`
	Value string              `json:"value,omitempty"`
	App   *AppCredentialInput `json:"app,omitempty"`
	// SSH is an SSH key connection's key, and APIToken the PAT paired
	// with it on a GitHub-kind host (GitCredentialInput).
	SSH      *SSHCredentialInput `json:"ssh,omitempty"`
	APIToken string              `json:"apiToken,omitempty"`
	// Source is an ambient connection's: "gh", "git" or "auto".
	Source     string `json:"source,omitempty"`
	Credential string `json:"credential,omitempty"`
	User       string `json:"user,omitempty"`
}

// CheckTeamGitConnectionRequest is the body of POST /api/deployment/git-connections/check.
type CheckTeamGitConnectionRequest struct {
	Host       string              `json:"host,omitempty"`
	Kind       string              `json:"kind,omitempty"`
	Scope      string              `json:"scope,omitempty"`
	Type       string              `json:"type,omitempty"`
	Value      string              `json:"value,omitempty"`
	App        *AppCredentialInput `json:"app,omitempty"`
	Source     string              `json:"source,omitempty"`
	Credential string              `json:"credential,omitempty"`
	User       string              `json:"user,omitempty"`
}

func teamConnectionRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ListTeamGitConnections returns the team's effective git connections.
func (c *Client) ListTeamGitConnections(ctx context.Context, id, user string) ([]TeamGitConnectionView, error) {
	if _, err := c.existingTeam(ctx, id); err != nil {
		return nil, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return c.teamGitConnectionViews(settings), nil
}

// teamGitConnectionViews is the deployment's git connections
// (Config.GitConnections, and the legacy Config.GitCredential) as the
// connection editor lists them.
func (c *Client) teamGitConnectionViews(settings model.Config) []TeamGitConnectionView {
	var out []TeamGitConnectionView
	seenCreds := make(map[string]bool)

	for _, conn := range settings.GitConnections {
		status := c.gitCredentialStatus(conn.Credential)
		kind := status.Kind
		if kind == "" {
			kind = string(model.GitHostGitHub)
		}
		host := conn.Host
		if host == "" {
			host = status.Host
		}
		if host == "" {
			host = model.DefaultGitHost
		}
		authType := status.Type
		if authType == "" {
			authType = "pat"
		}
		out = append(out, TeamGitConnectionView{
			Credential: conn.Credential,
			Host:       host,
			Kind:       kind,
			Scope:      conn.Scope,
			AuthType:   authType,
			Status:     status,
		})
		seenCreds[conn.Credential] = true
	}

	// Legacy Config.GitCredential if not already in GitConnections
	legacy := strings.TrimSpace(settings.GitCredential)
	if legacy != "" && !seenCreds[legacy] {
		status := c.gitCredentialStatus(legacy)
		kind := status.Kind
		if kind == "" {
			kind = string(model.GitHostGitHub)
		}
		host := status.Host
		if host == "" {
			host = model.DefaultGitHost
		}
		authType := status.Type
		if authType == "" {
			authType = "pat"
		}
		out = append([]TeamGitConnectionView{{
			Credential: legacy,
			Host:       host,
			Kind:       kind,
			Scope:      "",
			AuthType:   authType,
			Status:     status,
			Legacy:     true,
		}}, out...)
	}

	return out
}

// AddTeamGitConnection adds a connection to the team.
func (c *Client) AddTeamGitConnection(ctx context.Context, id string, req AddTeamGitConnectionRequest, user string) (Team, error) {
	if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return Team{}, err
	}
	if c.Config.Credentials == nil {
		return Team{}, errGitHubTokensUnavailable
	}
	_, err := c.existingTeam(ctx, id)
	if err != nil {
		return Team{}, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return Team{}, err
	}

	host, err := model.NormalizeGitHost(req.Host)
	if err != nil {
		return Team{}, validationErrorf("invalid host: %w", err)
	}
	kind, err := model.ParseGitHostKind(req.Kind)
	if err != nil {
		return Team{}, validationErrorf("invalid kind: %w", err)
	}
	if base := strings.TrimSpace(req.BaseURL); base != "" {
		// Refused here, as the client's mistake, rather than saved and
		// then refused by the git proxy on every clone.
		h, err := gitproxy.NormalizeCredentialHost(gitproxy.CredentialHost{Host: host, Kind: kind, BaseURL: base})
		if err != nil {
			return Team{}, validationErrorf("%s", err)
		}
		if err := h.CheckTransport(); err != nil {
			return Team{}, validationErrorf("%s", err)
		}
	}

	// A connection is a credential on a host, and "anonymous" (type
	// "none", or the selector named outright) is the absence of one:
	// there is nothing to record a host against, and a connection naming
	// it would count as the team's GitHub credential (hasGitCredential)
	// while holding none. Refused as the client's mistake before anything
	// is written, rather than reaching SetHost's reserved-name error as a
	// 500.
	if (GitCredentialInput{Type: req.Type}).authType() == gitproxy.AuthTypeAnonymous ||
		strings.TrimSpace(req.Credential) == "anonymous" {
		return Team{}, validationErrorf("a git connection needs a credential: type pat, ssh, app or ambient, not none")
	}
	if (GitCredentialInput{Type: req.Type}).authType() == gitproxy.AuthTypeAmbient {
		// Checked before a name is allocated or anything is written:
		// the team gets an ambient connection only from a system admin,
		// and only on a daemon that runs on the host.
		if err := c.requireAmbientAvailable(); err != nil {
			return Team{}, err
		}
	}

	credName := strings.TrimSpace(req.Credential)
	if credName == "" {
		// Allocate a deployment-owned credential name
		// (model.DeploymentOwnsGitCredential).
		suffix, err := teamConnectionRandomHex(6)
		if err != nil {
			return Team{}, fmt.Errorf("generating credential name: %w", err)
		}
		credName = model.GitConnectionCredentialPrefix + "c-" + suffix

		input := GitCredentialInput{
			Type:     req.Type,
			Value:    req.Value,
			App:      req.App,
			SSH:      req.SSH,
			APIToken: req.APIToken,
			Source:   req.Source,
			Host:     host,
			Kind:     string(kind),
			BaseURL:  req.BaseURL,
			Proxy:    req.Proxy,
		}
		savedName, err := c.saveTypedCredential(credName, input)
		if err != nil {
			return Team{}, err
		}
		credName = savedName
	} else {
		// An ambient credential under the team's own name is still the
		// host's login, which a system admin handed the team for one
		// host: re-adding it rewrites its host record below -- where the
		// proxy and the REST client send that login's token -- and only
		// whoever may create one may do that (SetHost also refuses a
		// host other than the ambient file's outright).
		if f, ok := c.Config.Credentials.AmbientOf(credName); ok || c.Config.Credentials.IsAmbient(credName) {
			if err := c.requireAmbientAvailable(); err != nil {
				return Team{}, err
			}
			if ok && !model.SameGitHost(host, f.Host, model.DefaultGitHost) {
				return Team{}, validationErrorf("%q is an ambient credential for %s: it cannot be a connection to %s",
					credName, f.Host, host)
			}
		}
	}

	// Always write host record. A proxy the request does not name is the
	// one already recorded for the same host (saveTypedCredential's,
	// read from the forge's tool): rewriting the record must not drop it.
	proxy := strings.TrimSpace(req.Proxy)
	if rec := c.Config.Credentials.HostOf(credName); proxy == "" && rec.Recorded && model.SameGitHost(rec.Host, host, model.DefaultGitHost) {
		proxy = rec.Proxy
	}
	if err := c.Config.Credentials.SetHost(credName, gitproxy.CredentialHost{
		Host:    host,
		Kind:    kind,
		BaseURL: strings.TrimSpace(req.BaseURL),
		Proxy:   proxy,
	}); err != nil {
		return Team{}, fmt.Errorf("setting credential host: %w", err)
	}

	newConn := model.TeamGitConnection{
		Credential: credName,
		Host:       host,
		Scope:      req.Scope,
	}
	normConn, err := model.NormalizeTeamGitConnection(newConn)
	if err != nil {
		return Team{}, validationErrorf("invalid connection: %w", err)
	}

	// Migrate legacy credential and append new connection
	var updatedConns []model.TeamGitConnection
	legacy := strings.TrimSpace(settings.GitCredential)
	hasLegacy := false
	for _, conn := range settings.GitConnections {
		if legacy != "" && conn.Credential == legacy {
			hasLegacy = true
		}
		updatedConns = append(updatedConns, conn)
	}
	if legacy != "" && !hasLegacy {
		updatedConns = append([]model.TeamGitConnection{{Credential: legacy}}, updatedConns...)
	}
	updatedConns = append(updatedConns, normConn)

	emptyLegacy := ""
	return c.UpdateTeam(ctx, id, UpdateTeamRequest{
		GitConnections: &updatedConns,
		GitCredential:  &emptyLegacy,
		User:           user,
	})
}

// DeleteTeamGitConnection removes a connection from the team.
func (c *Client) DeleteTeamGitConnection(ctx context.Context, id, credName, user string) (Team, error) {
	if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return Team{}, err
	}
	if c.Config.Credentials == nil {
		return Team{}, errGitHubTokensUnavailable
	}
	_, err := c.existingTeam(ctx, id)
	if err != nil {
		return Team{}, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return Team{}, err
	}

	credName = strings.TrimSpace(credName)
	if credName == "" {
		return Team{}, validationErrorf("credential name cannot be empty")
	}

	var updatedConns []model.TeamGitConnection
	found := false
	for _, conn := range settings.GitConnections {
		if conn.Credential == credName {
			found = true
			continue
		}
		updatedConns = append(updatedConns, conn)
	}

	emptyLegacy := ""
	legacy := strings.TrimSpace(settings.GitCredential)
	if legacy == credName {
		found = true
	} else if legacy != "" {
		// Migrate legacy if it was kept
		updatedConns = append([]model.TeamGitConnection{{Credential: legacy}}, updatedConns...)
	}

	if !found {
		return Team{}, validationErrorf("this deployment has no git connection %q", credName)
	}

	// Remove credential files grain minted for the connection
	if model.DeploymentOwnsGitCredential(credName) {
		_ = c.Config.Credentials.Remove(credName)
	}

	return c.UpdateTeam(ctx, id, UpdateTeamRequest{
		GitConnections: &updatedConns,
		GitCredential:  &emptyLegacy,
		User:           user,
	})
}

// SetTeamRepoGitConnectionRequest is the body of PUT
// /api/deployment/repos/{owner}/{name}/git-connection: which of the
// team's connections the repository uses. An empty Credential is
// "Default" -- whatever the team's owner or host-default connection on
// Host resolves to.
type SetTeamRepoGitConnectionRequest struct {
	Credential string `json:"credential"`
	Host       string `json:"host,omitempty"`
	User       string `json:"user,omitempty"`
}

// SetTeamRepoGitConnection points one of team id's repositories at one of
// the connections it already holds, by recording that credential as a
// connection scoped to owner/repo -- the team's connection list stays the
// one place its git access is recorded (grain/task-3929). An empty
// credential removes the repository-scoped entry, so the repository falls
// back to the team's owner or host-default connection. Any credential the
// repository's legacy per-repository override still named is cleared,
// since it would otherwise outrank the choice.
func (c *Client) SetTeamRepoGitConnection(ctx context.Context, id, owner, repo string, req SetTeamRepoGitConnectionRequest, user string) (Team, error) {
	if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return Team{}, err
	}
	_, err := c.existingTeam(ctx, id)
	if err != nil {
		return Team{}, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return Team{}, err
	}
	host, err := model.NormalizeGitHost(req.Host)
	if err != nil {
		return Team{}, validationErrorf("invalid host: %w", err)
	}
	if model.SameGitHost(host, "", model.DefaultGitHost) {
		host = ""
	}
	scoped, err := model.NormalizeTeamGitConnection(model.TeamGitConnection{
		Credential: "-", Host: host, Scope: owner + "/" + repo,
	})
	if err != nil || !strings.Contains(scoped.Scope, "/") {
		return Team{}, validationErrorf("invalid repository %s/%s", owner, repo)
	}
	credName := strings.TrimSpace(req.Credential)
	effective := settings.EffectiveGitConnections()
	if credName != "" {
		held, covers := false, false
		lowOwner := strings.ToLower(owner)
		for _, raw := range effective {
			conn, err := model.NormalizeTeamGitConnection(raw)
			if err != nil || conn.Credential != credName || !model.SameGitHost(conn.Host, host, model.DefaultGitHost) {
				continue
			}
			if !held {
				// Spelled as the connection it copies spells its host.
				held, host = true, raw.Host
			}
			if conn.Scope == "" || conn.Scope == lowOwner || conn.Scope == scoped.Scope {
				covers = true
			}
		}
		if held && !covers {
			// Pointing a repository at a credential the team holds only for
			// other repositories widens what that credential reaches, so it
			// is held to the same bar as adding the connection was
			// (AddTeamGitConnection): a team's own token is its admins'
			// call, an ambient login or a credential the team does not own
			// a system admin's.
			ambient := false
			if creds := c.Config.Credentials; creds != nil {
				_, isFile := creds.AmbientOf(credName)
				ambient = isFile || creds.IsAmbient(credName)
			}
			if ambient {
				if err := c.requireAmbientAvailable(); err != nil {
					return Team{}, err
				}
			}
		}
		if !held {
			onHost := host
			if onHost == "" {
				onHost = model.DefaultGitHost
			}
			return Team{}, validationErrorf("this deployment has no git connection %q on %s: add it under Settings -> Git connections first",
				credName, onHost)
		}
	}

	var updated []model.TeamGitConnection
	if credName != "" {
		updated = append(updated, model.TeamGitConnection{Credential: credName, Host: host, Scope: scoped.Scope})
	}
	for _, conn := range settings.GitConnections {
		norm, err := model.NormalizeTeamGitConnection(conn)
		if err == nil && norm.Scope == scoped.Scope && model.SameGitHost(norm.Host, host, model.DefaultGitHost) {
			continue
		}
		updated = append(updated, conn)
	}
	// The legacy team credential keeps its place: last, the default
	// host's default (model.Config.EffectiveGitConnections).
	if legacy := strings.TrimSpace(settings.GitCredential); legacy != "" {
		updated = append(updated, model.TeamGitConnection{Credential: legacy})
	}

	ref := model.RepoRef{Host: host, Owner: owner, Name: repo}
	if existing, err := c.Store.GetRepoConfig(ctx, ref); err != nil {
		return Team{}, err
	} else if existing != nil && existing.GitCredential != "" {
		existing.GitCredential = ""
		if err := c.Store.PutRepoConfig(ctx, *existing); err != nil {
			return Team{}, err
		}
	}

	emptyLegacy := ""
	return c.UpdateTeam(ctx, id, UpdateTeamRequest{
		GitConnections: &updated,
		GitCredential:  &emptyLegacy,
		User:           user,
	})
}

// CheckTeamGitConnection tests a connection (saved or unsaved) against the host and scope.
func (c *Client) CheckTeamGitConnection(ctx context.Context, id string, req CheckTeamGitConnectionRequest, user string) (TeamGitHubCredentialCheck, error) {
	if c.Config.Credentials == nil {
		return TeamGitHubCredentialCheck{}, errGitHubTokensUnavailable
	}
	team, err := c.existingTeam(ctx, id)
	if err != nil {
		return TeamGitHubCredentialCheck{}, err
	}
	settings, err := c.deploymentSettings(ctx)
	if err != nil {
		return TeamGitHubCredentialCheck{}, err
	}

	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = model.DefaultGitHost
	}
	normHost, err := model.NormalizeGitHost(host)
	if err != nil {
		return TeamGitHubCredentialCheck{}, validationErrorf("invalid host: %w", err)
	}

	// A saved credential is tested where it is used, and only by someone
	// who may use it: the probe sends its token to normHost, so naming
	// another team's or the deployment's credential, or another host for
	// one the team uses, would hand that token to whoever asked.
	saved := ""
	if strings.TrimSpace(req.Value) == "" && (req.App == nil || req.App.PrivateKey == "") &&
		!((GitCredentialInput{Type: req.Type}).authType() == gitproxy.AuthTypeAmbient && strings.TrimSpace(req.Credential) == "") {
		saved = strings.TrimSpace(req.Credential)
		if saved == "" {
			saved = strings.TrimSpace(settings.GitCredential)
			if saved == "" && len(settings.GitConnections) > 0 {
				saved = settings.GitConnections[0].Credential
			}
			if saved == "" {
				return TeamGitHubCredentialCheck{}, validationErrorf("no credential provided to test")
			}
		}
		recorded := c.Config.Credentials.HostOf(saved).Host
		if strings.TrimSpace(req.Host) == "" {
			normHost = recorded
		} else if !model.SameGitHost(normHost, recorded, model.DefaultGitHost) {
			return TeamGitHubCredentialCheck{}, validationErrorf(
				"credential %q is recorded on %s, so it cannot be tested against %s", saved, recorded, normHost)
		}
	}

	// Determine repos to test
	var reposToCheck []string
	scope := strings.TrimSpace(req.Scope)
	if scope != "" && strings.Contains(scope, "/") {
		reposToCheck = []string{scope}
	} else {
		for _, tr := range team.TargetRepos {
			ref, err := model.ParseRepo(tr)
			if err == nil {
				refHost := ref.Host
				if refHost == "" {
					refHost = model.DefaultGitHost
				}
				if model.SameGitHost(refHost, normHost, model.DefaultGitHost) {
					if scope == "" || strings.EqualFold(ref.Owner, scope) {
						reposToCheck = append(reposToCheck, ref.String())
					}
				}
			}
		}
	}

	name := strings.TrimSpace(req.Credential)
	value := strings.TrimSpace(req.Value)

	// What an unsaved credential is tested as: the host as this
	// deployment already records it (its base URL and proxy), of the
	// kind the editor names. A plain git host has no API, so a token for
	// one is tested by reading its repositories, never as GitHub.
	unsavedHost := gitproxy.CredentialHost{Host: normHost, Kind: model.GitHostGitHub}
	if rec := c.Config.Credentials.HostOf(normHost); rec.Recorded && model.SameGitHost(rec.Host, normHost, model.DefaultGitHost) {
		unsavedHost = rec
	}
	if strings.TrimSpace(req.Kind) != "" {
		kind, err := model.ParseGitHostKind(req.Kind)
		if err != nil {
			return TeamGitHubCredentialCheck{}, validationErrorf("invalid kind: %w", err)
		}
		unsavedHost.Kind = kind
	}

	// An ambient connection typed into the editor and not saved yet: ask
	// the host's own login for a token now, and probe with it.
	if (GitCredentialInput{Type: req.Type}).authType() == gitproxy.AuthTypeAmbient && name == "" {
		if err := c.requireAmbientAvailable(); err != nil {
			return TeamGitHubCredentialCheck{}, err
		}
		if unsavedHost.Kind == model.GitHostGit {
			token, user, err := gitproxy.AmbientLogin(normHost, unsavedHost.BaseURL, req.Source)
			if err != nil {
				return TeamGitHubCredentialCheck{Host: normHost, CheckedAt: c.now(),
					Report: githubtoken.Report{Error: err.Error()}}, nil
			}
			return c.checkPlainGitCredential(unsavedHost, gitproxy.Credential{Token: &token,
				Ambient: &gitproxy.AmbientCredential{Host: normHost, Source: req.Source, User: user}}, reposToCheck, false), nil
		}
		token, _, err := gitproxy.AmbientToken(normHost, req.Source)
		if err != nil {
			return TeamGitHubCredentialCheck{Host: normHost, CheckedAt: c.now(),
				Report: githubtoken.Report{Error: err.Error()}}, nil
		}
		return c.checkGitHubCredentialForHost(ctx, "", token, normHost, reposToCheck)
	}

	// If App is provided unsaved
	if req.App != nil && req.App.PrivateKey != "" {
		// Save to temporary test credential or mint directly
		tempName := model.GitConnectionCredentialPrefix + "test-tmp"
		defer c.Config.Credentials.Remove(tempName)
		if err := c.Config.Credentials.SetApp(tempName, req.App.AppID, req.App.InstallationID, req.App.PrivateKey); err != nil {
			return TeamGitHubCredentialCheck{
				Host:      normHost,
				CheckedAt: c.now(),
				Report:    githubtoken.Report{Error: err.Error()},
			}, nil
		}
		if err := c.Config.Credentials.SetHost(tempName, gitproxy.CredentialHost{Host: normHost, Kind: model.GitHostGitHub}); err != nil {
			return TeamGitHubCredentialCheck{
				Host:      normHost,
				CheckedAt: c.now(),
				Report:    githubtoken.Report{Error: err.Error()},
			}, nil
		}
		return c.checkGitHubCredentialForHost(ctx, tempName, "", normHost, reposToCheck)
	}

	if value == "" {
		name = saved
	} else if unsavedHost.Kind == model.GitHostGit {
		return c.checkPlainGitCredential(unsavedHost, gitproxy.Credential{Token: &value}, reposToCheck, false), nil
	}

	return c.checkGitHubCredentialForHost(ctx, name, value, normHost, reposToCheck)
}

func (c *Client) checkGitHubCredentialForHost(ctx context.Context, name, value, host string, repos []string) (TeamGitHubCredentialCheck, error) {
	deploymentHost, insecure := c.gitHubHost(ctx)
	out := TeamGitHubCredentialCheck{Host: host, CheckedAt: c.now()}
	// A connection on github.com is probed where this deployment's GitHub
	// API is (its github_host), as the retired team token's check always
	// was: the connection is now the only place that token can live
	// (grain/task-3929).
	probeHost := host
	if model.SameGitHost(host, githubtoken.DefaultHost, model.DefaultGitHost) {
		probeHost = deploymentHost
	}
	token := strings.TrimSpace(value)
	app := false
	if token == "" {
		out.Saved = true
		cred, ok := c.Config.Credentials.Get(name)
		if !ok {
			out.Error = "the saved credential is missing from this deployment's credential directory; save the token again"
			return out, nil
		}
		app = c.Config.Credentials.IsApp(name)
		if cred.SSH != nil {
			return c.checkSSHCredential(ctx, cred, repos, true), nil
		}
		if cred.Unusable != "" {
			out.Error = cred.Unusable
			return out, nil
		}
		if cred.MintFailure != nil {
			out.App = true
			out.Error = "no installation token could be minted from this GitHub App credential: the App's " +
				"private key was regenerated, or its installation suspended or removed"
			return out, nil
		}
		if cred.Token == nil || strings.TrimSpace(*cred.Token) == "" {
			out.Error = "the saved credential holds no token"
			return out, nil
		}
		token = strings.TrimSpace(*cred.Token)
		if h := c.Config.Credentials.HostOf(name); h.Kind == model.GitHostGit {
			// A plain git host has no GitHub API to probe: the
			// token is tested the way the git proxy uses it.
			return c.checkPlainGitCredential(h, cred, repos, true), nil
		}
	}

	transport := githubtoken.ProbeTransport(probeHost, insecure)
	if out.Saved && c.Config.Credentials.ForgeViaCommand(name) {
		// A command-backed forge's login is checked the way it is used:
		// through its tool.
		if t, err := c.Config.Credentials.ForgeTransport(name); err == nil {
			transport = t
		}
	}
	if out.Saved && c.Config.GitReach != nil {
		if listing, ok, err := c.Config.GitReach.Repos(name); err == nil && ok {
			out.Report = githubtoken.Probe(transport, token, app, nil)
			if out.OK {
				out.FromReach = true
				out.Repos, out.Skipped = c.reachVerdicts(transport, name, token, repos, listing)
			}
			return out, nil
		}
	}
	out.Report = githubtoken.Probe(transport, token, app, repos)
	return out, nil
}

// Server HTTP handlers
