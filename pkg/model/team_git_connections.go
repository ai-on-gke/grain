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
//
// User optionally binds the connection to a specific task author/owner
// account (normalized via NormalizeTaskOwner, e.g. "alice@example.com").
// Empty is the deployment-wide default connection for (Host, Scope).
type TeamGitConnection struct {
	Credential string `json:"credential" yaml:"credential"`
	Host       string `json:"host,omitempty" yaml:"host,omitempty"`
	Scope      string `json:"scope,omitempty" yaml:"scope,omitempty"`
	User       string `json:"user,omitempty" yaml:"user,omitempty"`
}

// SameGitConnectionUser reports whether two connection User fields name
// the same user binding (or are both empty, for the deployment default).
// If one identifier is an email address and the other is a bare username
// without "@", the email's local-part is compared case-insensitively.
func SameGitConnectionUser(a, b string) bool {
	a, b = NormalizeIdentity(a), NormalizeIdentity(b)
	if a == "" && b == "" {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	aLocal, _, aHasAt := strings.Cut(a, "@")
	bLocal, _, bHasAt := strings.Cut(b, "@")
	if aHasAt != bHasAt && aLocal != "" && bLocal != "" {
		return strings.EqualFold(aLocal, bLocal)
	}
	return false
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
// normalized (NormalizeGitHost), scope spelled "", "owner" or
// "owner/repo" in lowercase, and user normalized via NormalizeTaskOwner
// when present, or an error naming what is wrong.
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
	if rawUser := strings.TrimSpace(c.User); rawUser != "" {
		normUser, err := NormalizeTaskOwner(rawUser)
		if err != nil {
			return c, fmt.Errorf("git connection user: %w", err)
		}
		c.User = normUser
	} else {
		c.User = ""
	}
	return c, nil
}

type storedTeamGitConnections struct {
	Connections           []TeamGitConnection `json:"connections,omitempty"`
	RequireUserCredential bool                `json:"requireUserCredential,omitempty"`
}

// EncodeTeamGitConnections is grain_config.git_connections as stored: JSON, or
// empty for none.
func EncodeTeamGitConnections(v []TeamGitConnection) string {
	return EncodeTeamGitConnectionsWithOptions(v, false)
}

// EncodeTeamGitConnectionsWithOptions encodes v and requireUser for
// grain_config.git_connections. When requireUser is false, it writes the
// plain JSON array (or "" when empty) for backward compatibility.
func EncodeTeamGitConnectionsWithOptions(v []TeamGitConnection, requireUser bool) string {
	if !requireUser {
		if len(v) == 0 {
			return ""
		}
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	b, err := json.Marshal(storedTeamGitConnections{
		Connections:           v,
		RequireUserCredential: true,
	})
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
	conns, _ := DecodeTeamGitConnectionsWithOptions(v)
	return conns
}

// DecodeTeamGitConnectionsWithOptions reads grain_config.git_connections back,
// returning both the connection list and whether per-user credentials are
// explicitly required.
func DecodeTeamGitConnectionsWithOptions(v string) ([]TeamGitConnection, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	if strings.HasPrefix(v, "{") {
		var wrapped storedTeamGitConnections
		if err := json.Unmarshal([]byte(v), &wrapped); err != nil {
			return nil, false
		}
		return wrapped.Connections, wrapped.RequireUserCredential
	}
	var out []TeamGitConnection
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, false
	}
	return out, false
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

// HasUserGitConnectionOn reports whether conns holds any non-anonymous git
// connection on host that is bound to a specific user (TeamGitConnection.User != "").
func HasUserGitConnectionOn(conns []TeamGitConnection, host, defaultHost string) bool {
	for _, raw := range conns {
		conn, err := NormalizeTeamGitConnection(raw)
		if err == nil && conn.User != "" && conn.Credential != "anonymous" && SameGitHost(conn.Host, host, defaultHost) {
			return true
		}
	}
	return false
}

// HasUserGitConnectionOn reports whether the deployment holds any user-bound
// git connection on host.
func (c Config) HasUserGitConnectionOn(host, defaultHost string) bool {
	return HasUserGitConnectionOn(c.EffectiveGitConnections(), host, defaultHost)
}

// HasAnyUserGitConnection reports whether the deployment holds any user-bound
// git connection on any host.
func (c Config) HasAnyUserGitConnection() bool {
	for _, raw := range c.EffectiveGitConnections() {
		conn, err := NormalizeTeamGitConnection(raw)
		if err == nil && conn.User != "" && conn.Credential != "anonymous" {
			return true
		}
	}
	return false
}

// EnforcesUserGitCredentialOn reports whether tasks targeting host must
// resolve a git connection belonging to the task author (and must be blocked
// when the task author has no assigned token). This is true when
// RequireUserGitCredential is explicitly enabled or when any user-bound git
// connection is configured on host.
func (c Config) EnforcesUserGitCredentialOn(host, defaultHost string) bool {
	return c.RequireUserGitCredential || c.HasUserGitConnectionOn(host, defaultHost)
}

// MatchTeamGitConnection is the most specific connection in conns for
// owner/repo on host -- one scoped to that repository, then to its
// owner, then the host's default -- with the earlier entry winning a tie.
// Connections with User == "" (deployment-wide defaults) are preferred;
// when only user-bound connections exist on host, the most specific one is
// returned for deployment-level read checks (while task execution goes through
// TaskGitCredentialOn and strictly enforces task-author ownership).
// host and every connection's own Host resolve an empty value to
// defaultHost (ResolveGitHost). A connection on another host never
// matches, however broad: there is no fallback across hosts.
func MatchTeamGitConnection(conns []TeamGitConnection, host, defaultHost, owner, repo string) (TeamGitConnection, bool) {
	owner, repo = strings.ToLower(owner), strings.TrimSuffix(strings.ToLower(repo), ".git")
	var bestDefault TeamGitConnection
	bestDefaultRank := scopeNoMatch
	var bestAny TeamGitConnection
	bestAnyRank := scopeNoMatch
	for _, raw := range conns {
		c, err := NormalizeTeamGitConnection(raw)
		if err != nil || !SameGitHost(c.Host, host, defaultHost) {
			continue
		}
		rank := scopeRankFor(c.Scope, owner, repo)
		if c.User == "" && rank > bestDefaultRank {
			bestDefault, bestDefaultRank = c, rank
		}
		if rank > bestAnyRank {
			bestAny, bestAnyRank = c, rank
		}
	}
	if bestDefaultRank != scopeNoMatch {
		return bestDefault, true
	}
	return bestAny, bestAnyRank != scopeNoMatch
}

// MatchUserGitConnection is the most specific connection in conns bound
// to user (SameGitConnectionUser(c.User, user)) for owner/repo on host -- one
// scoped to that repository, then to its owner, then the host's default
// -- with the earlier entry winning a tie. Returns false when user is
// empty or holds no matching connection on host.
func MatchUserGitConnection(conns []TeamGitConnection, user, host, defaultHost, owner, repo string) (TeamGitConnection, bool) {
	normUser := NormalizeIdentity(user)
	if normUser == "" {
		return TeamGitConnection{}, false
	}
	owner, repo = strings.ToLower(owner), strings.TrimSuffix(strings.ToLower(repo), ".git")
	var best TeamGitConnection
	bestRank := scopeNoMatch
	for _, raw := range conns {
		c, err := NormalizeTeamGitConnection(raw)
		if err != nil || c.User == "" || !SameGitConnectionUser(c.User, normUser) || !SameGitHost(c.Host, host, defaultHost) {
			continue
		}
		rank := scopeRankFor(c.Scope, owner, repo)
		if rank > bestRank {
			best, bestRank = c, rank
		}
	}
	return best, bestRank != scopeNoMatch
}

func scopeRankFor(scope, owner, repo string) int {
	switch {
	case scope == "":
		return scopeHostDefault
	case owner != "" && repo != "" && scope == owner+"/"+repo:
		return scopeRepo
	case owner != "" && scope == owner:
		return scopeOwner
	default:
		return scopeNoMatch
	}
}
