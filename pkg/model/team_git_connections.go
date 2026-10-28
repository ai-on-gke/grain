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

package model

// The deployment's git connections: every credential it clones and
// pushes with, each on one host and optionally scoped to an owner or a
// single repository (docs/git-hosts.md). They are Config.GitConnections,
// the grain_config.git_connections column, since schema 22 moved them off
// the default team's row (docs/single-tenant-reset.md, 6-ii-b2). They
// replace Config.GitCredential, which is still read as one more
// connection -- the default for the deployment's default host -- so a
// deployment configured before connections existed resolves exactly as
// it did.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TeamGitConnection is one credential a team holds.
//
// Credential names a credential in the deployment's credential store
// (gitproxy.CredentialSet), which records that credential's own host,
// kind and auth type. Host is where the team uses it: empty means the
// deployment's default host (DefaultGitHost, or the git proxy's
// ForwardHost). The proxy refuses a connection whose credential's own
// host is not the host the request goes to, so the two cannot disagree
// silently.
//
// Scope narrows the connection on its host: empty is the team's default
// for that host, "owner" (or "owner/*") an owner or organization, and
// "owner/repo" one repository. A team wanting one credential for several
// specific repositories lists one connection per repository.
type TeamGitConnection struct {
	Credential string `json:"credential" yaml:"credential"`
	Host       string `json:"host,omitempty" yaml:"host,omitempty"`
	Scope      string `json:"scope,omitempty" yaml:"scope,omitempty"`
}

// connectionSpecificity ranks a scope: a repository beats an owner beats
// the host default, and an unusable scope ranks nowhere.
const (
	scopeNoMatch = iota
	scopeHostDefault
	scopeOwner
	scopeRepo
)

// NormalizeTeamGitConnection is c as grain stores it: trimmed, host
// normalized (NormalizeGitHost) and scope spelled "", "owner" or
// "owner/repo" in lowercase, or an error naming what is wrong.
func NormalizeTeamGitConnection(c TeamGitConnection) (TeamGitConnection, error) {
	c.Credential = strings.TrimSpace(c.Credential)
	if c.Credential == "" {
		return c, fmt.Errorf("a git connection needs a credential")
	}
	host, err := NormalizeGitHost(c.Host)
	if err != nil {
		return c, err
	}
	c.Host = host
	scope := strings.ToLower(strings.Trim(strings.TrimSpace(c.Scope), "/"))
	scope = strings.TrimSuffix(scope, "/*")
	if scope != "" {
		owner, repo, hasRepo := strings.Cut(scope, "/")
		repo = strings.TrimSuffix(repo, ".git")
		if !validRepoSegment(owner) || (hasRepo && !validRepoSegment(repo)) {
			return c, fmt.Errorf("git connection scope must be an owner or owner/repo, got %q", c.Scope)
		}
		scope = owner
		if hasRepo {
			scope = owner + "/" + repo
		}
	}
	c.Scope = scope
	return c, nil
}

// EncodeTeamGitConnections is grain_config.git_connections as stored: JSON, or
// empty for none.
func EncodeTeamGitConnections(v []TeamGitConnection) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeTeamGitConnections reads grain_config.git_connections back. A value that
// does not parse reads as no connections: every request then fails
// closed with a refusal naming the host, rather than falling back to a
// credential nobody chose.
func DecodeTeamGitConnections(v string) []TeamGitConnection {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []TeamGitConnection
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

// EffectiveGitConnections is every git connection the deployment holds,
// in precedence order: its own list, then the legacy single
// Config.GitCredential as the default for the deployment's default host
// -- the in-place migration of a deployment configured before
// connections existed.
func (c Config) EffectiveGitConnections() []TeamGitConnection {
	out := append([]TeamGitConnection(nil), c.GitConnections...)
	if cred := strings.TrimSpace(c.GitCredential); cred != "" {
		out = append(out, TeamGitConnection{Credential: cred})
	}
	return out
}

// HasGitConnectionOn reports whether the deployment holds any git
// connection on host -- its own list or the legacy Config.GitCredential,
// at any scope -- by
// MatchTeamGitConnection's host rule (SameGitHost, an empty host standing
// for defaultHost). An empty host asks about the deployment's default
// host, which is what "the team has no GitHub credential" means: a team
// whose connections are all on another host still has none there.
//
// A connection naming "anonymous" -- the team git editor's "None
// (public)" -- does not count: it reads public repositories and pushes
// nothing, so a team holding only that still has no credential, and the
// "has no GitHub credential" warning is still the one to show.
func (c Config) HasGitConnectionOn(host, defaultHost string) bool {
	for _, raw := range c.EffectiveGitConnections() {
		conn, err := NormalizeTeamGitConnection(raw)
		if err == nil && conn.Credential != "anonymous" && SameGitHost(conn.Host, host, defaultHost) {
			return true
		}
	}
	return false
}

// MatchTeamGitConnection is the most specific connection in conns for
// owner/repo on host -- one scoped to that repository, then to its
// owner, then the host's default -- with the earlier entry winning a tie.
// host and every connection's own Host resolve an empty value to
// defaultHost (ResolveGitHost). A connection on another host never
// matches, however broad: there is no fallback across hosts.
func MatchTeamGitConnection(conns []TeamGitConnection, host, defaultHost, owner, repo string) (TeamGitConnection, bool) {
	owner, repo = strings.ToLower(owner), strings.TrimSuffix(strings.ToLower(repo), ".git")
	var best TeamGitConnection
	bestRank := scopeNoMatch
	for _, raw := range conns {
		c, err := NormalizeTeamGitConnection(raw)
		if err != nil || !SameGitHost(c.Host, host, defaultHost) {
			continue
		}
		rank := scopeNoMatch
		switch {
		case c.Scope == "":
			rank = scopeHostDefault
		case owner != "" && repo != "" && c.Scope == owner+"/"+repo:
			rank = scopeRepo
		case owner != "" && c.Scope == owner:
			rank = scopeOwner
		}
		if rank > bestRank {
			best, bestRank = c, rank
		}
	}
	return best, bestRank != scopeNoMatch
}
