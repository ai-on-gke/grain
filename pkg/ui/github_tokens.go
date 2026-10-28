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
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The named GitHub tokens half of Settings (grain/task-137).
//
// grain/task-117 made every named token beyond the deployment default a
// capability of its own, so a task could be told to push through a
// second one -- but *defining* one was still host-only work: write
// <name>.token into $GRAIN_DATA_DIR/secrets/github and restart. These
// handlers are that work, moved into the pane where every other
// deployment-wide credential is already set.
//
// They write files, not secrets-store rows, which is the decision
// pkg/gitproxy/credentials.go's own doc comment records at length: the
// git proxy reads one thing, and this is the one thing. That is also why
// this is its own endpoint rather than a shape over Config.Secrets the
// way agent_keys.go is -- the store behind it is the credential ladder
// itself (Config.Credentials), whose availability is a separate question
// from the secrets database's.
//
// Everything here is write-only in the same sense the rest of the
// Settings pane is: a token goes in, presence and names come back, and
// no response ever carries a value.

// errGitHubTokensUnavailable is what these handlers report when
// Config.Credentials is nil -- this UI was not told where a GitHub
// credential ladder lives, which is the normal case for `grain demo`'s
// throwaway UI and never for `grain daemon`'s own in-process one. Mapped
// to 404, the same as the secrets pane's own unavailability.
var errGitHubTokensUnavailable = errors.New(
	"GitHub tokens are not available: this UI has no local GitHub credential directory to write to")

// gitHubTokenInfo is one credential in this deployment's ladder.
//
// Present and Offered are deliberately two fields rather than one:
// Present is what is on disk right now, Offered is what this *running
// process* turned into a picker row and a capability provider at startup
// (Config.Capabilities), and the whole reason a newly added token needs
// a restart is that those two can disagree. NeedsRestart below is that
// disagreement, named once so the frontend does not have to re-derive
// it.
type gitHubTokenInfo struct {
	Name string `json:"name"`
	// App is a credential backed by <name>.app.json -- a GitHub App
	// installation, whose three values are not something this pane can
	// write, so it is listed and never edited here.
	App bool `json:"app"`
	// Default is the credential the deployment's default-host git
	// connection names: what every repo with no narrower connection is
	// pushed with.
	Default bool `json:"default"`
	// Connections is the scope of every deployment git connection naming
	// this credential ("" for a host default, "owner" or "owner/repo"),
	// which is what makes deleting one a question rather than a click.
	Connections []string `json:"connections,omitempty"`
	// Capability is the id a task holds to push through this token.
	Capability string `json:"capability,omitempty"`
	// MintError is an App-backed credential this deployment cannot
	// currently mint an installation token from, in GitHub's own words
	// (gitproxy.MintFailure.Describe) -- empty for every credential that
	// is fine, and for every *.token one, which has nothing to mint.
	//
	// It is the difference between a row that says what is true and one
	// that says "present" about a credential every push is failing on
	// (grain/task-158): the ladder falls back to anonymous when a mint
	// fails, which keeps public reads working and makes every private
	// read and every push fail as an ordinary authentication error
	// somewhere else entirely.
	MintError string `json:"mintError,omitempty"`
	// Present is whether a credential file of this name exists.
	Present bool `json:"present"`
	// Offered is whether this process is offering it as a capability.
	Offered bool `json:"offered"`
	// NeedsRestart is Present and Offered disagreeing: a token added
	// since this process started (present, not yet offered) or removed
	// since (offered, no longer present). The ladder is deliberately not
	// hot-reloaded, so this is not a bug to fix, it is a thing to tell
	// whoever just added a token.
	NeedsRestart bool `json:"needsRestart"`
}

// gitHubTokensResponse is GET /api/github-tokens' body, and what setting
// or removing one answers with afterward -- the same
// respond-with-the-current-shape convention every other mutation in this
// package follows.
type gitHubTokensResponse struct {
	Enabled bool `json:"enabled"`
	// Dir is where these files live, for the pane to name when it
	// explains what it just wrote -- a path, never any content of one.
	Dir    string            `json:"dir,omitempty"`
	Tokens []gitHubTokenInfo `json:"tokens"`
	// DefaultName is the credential the default host's default git
	// connection names, empty when this deployment has none -- the state
	// a fresh one starts in, and in it every repo not covered by a
	// narrower connection fails its next clone.
	DefaultName string `json:"defaultName,omitempty"`
	// RestartRequired is any row's NeedsRestart, so the pane can show one
	// banner rather than reading every row to find out whether to.
	RestartRequired bool `json:"restartRequired"`
}

// deploymentGitConnections is the deployment's git connections, nil when
// they cannot be read (logged: this pane still lists what is on disk).
func (c *Client) deploymentGitConnections(ctx context.Context) []model.TeamGitConnection {
	if c.Store == nil {
		return nil
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		log.Printf("ui: reading the deployment's git connections: %v", err)
		return nil
	}
	if cfg == nil {
		return nil
	}
	return cfg.EffectiveGitConnections()
}

// defaultGitCredential is the credential the default host's default
// connection names, "" when there is none.
func (c *Client) defaultGitCredential(conns []model.TeamGitConnection) string {
	defaultHost := ""
	if c.Store != nil {
		defaultHost = c.Store.GitDefaultHost
	}
	for _, raw := range conns {
		conn, err := model.NormalizeTeamGitConnection(raw)
		if err == nil && conn.Scope == "" && model.SameGitHost(conn.Host, "", defaultHost) {
			return conn.Credential
		}
	}
	return ""
}

// connectionScopesFor is the scope of every connection in conns naming
// credential, in order.
func connectionScopesFor(conns []model.TeamGitConnection, credential string) []string {
	var out []string
	for _, raw := range conns {
		conn, err := model.NormalizeTeamGitConnection(raw)
		if err == nil && conn.Credential == credential {
			scope := conn.Scope
			if conn.Host != "" {
				scope = conn.Host + ":" + scope
			}
			out = append(out, scope)
		}
	}
	return out
}

// gitHubTokens is every credential on disk as this pane reports it,
// plus any this process still offers that has since been deleted, sorted
// by name.
func (c *Client) gitHubTokens(ctx context.Context) []gitHubTokenInfo {
	credentials := c.Config.Credentials
	if credentials == nil {
		return nil
	}
	present := credentials.Names()
	offered := map[string]bool{}
	for _, capability := range c.Config.Capabilities {
		if name, ok := model.GitCredentialName(capability.ID); ok {
			offered[name] = true
		}
	}
	names := slices.Clone(present)
	for name := range offered {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	conns := c.deploymentGitConnections(ctx)
	defaultName := c.defaultGitCredential(conns)
	out := make([]gitHubTokenInfo, 0, len(names))
	for _, name := range names {
		info := gitHubTokenInfo{
			Name:        name,
			Default:     name == defaultName,
			Connections: connectionScopesFor(conns, name),
			Present:     slices.Contains(present, name),
			Offered:     offered[name],
		}
		if info.Present {
			info.App = credentials.IsApp(name)
			// Only ever asked of an App credential, and answered from
			// the ladder's own cache once it has been asked once -- see
			// AppMintFailure on why listing these credentials is allowed
			// to mint, and what it costs.
			if info.App {
				if failure, failed := credentials.AppMintFailure(name); failed {
					info.MintError = failure.Describe()
				}
			}
		}
		if !(info.Present && credentials.IsAmbient(name)) {
			info.Capability = model.GitCredentialCapability(name)
			info.NeedsRestart = info.Present != info.Offered
		}
		out = append(out, info)
	}
	return out
}

// handleListGitHubTokens answers GET /api/github-tokens: the
// deployment's GitHub credentials, the connections naming them and the
// local directory they live in -- a system administrator's to read.
func (s *Server) handleListGitHubTokens(w http.ResponseWriter, r *http.Request) {
	s.respondWithGitHubTokens(w, r)
}

type setGitHubTokenRequest struct {
	Value string `json:"value"`
}

// handleSetGitHubToken writes one named token. The value travels in the
// body, never in the path or a query parameter, for the same reason
// handleSetAgentKey's does: neither belongs anywhere it would be logged.
func (s *Server) handleSetGitHubToken(w http.ResponseWriter, r *http.Request) {
	credentials := s.tasks.Config.Credentials
	if credentials == nil {
		writeError(w, http.StatusNotFound, errGitHubTokensUnavailable)
		return
	}
	var req setGitHubTokenRequest
	if !readJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	if err := credentials.SetToken(name, req.Value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// The first credential a deployment is given becomes its default-host
	// git connection: on a fresh deployment, pasting a token is otherwise
	// a credential no connection names, which is the state every clone
	// fails closed in. Once there is a default, adding a second token
	// never touches it.
	ctx := r.Context()
	if s.tasks.settingsWritable(ctx) && s.tasks.defaultGitCredential(s.tasks.deploymentGitConnections(ctx)) == "" && s.tasks.Store != nil {
		if err := s.tasks.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
			cfg.GitConnections = append(cfg.GitConnections, model.TeamGitConnection{Credential: name})
			return nil
		}); err != nil {
			// The token is written either way -- worth a log line, not
			// a failed write.
			log.Printf("ui: making %q the default GitHub credential: %v", name, err)
		}
	}
	s.respondWithGitHubTokens(w, r)
}

// handleDeleteGitHubToken removes one named token's files.
//
// A credential a deployment git connection still names is refused: every
// push that connection covers would fail closed, which is an outage
// bought with one click. The error names the connections and leaves the
// order of the two decisions to whoever is making them.
func (s *Server) handleDeleteGitHubToken(w http.ResponseWriter, r *http.Request) {
	credentials := s.tasks.Config.Credentials
	if credentials == nil {
		writeError(w, http.StatusNotFound, errGitHubTokensUnavailable)
		return
	}
	name := r.PathValue("name")
	if scopes := connectionScopesFor(s.tasks.deploymentGitConnections(r.Context()), name); len(scopes) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"the deployment's git connections %q still name %q: repoint or remove them first, "+
				"or every repo they cover fails its next push", scopes, name))
		return
	}
	if err := credentials.Remove(name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.respondWithGitHubTokens(w, r)
}

func (s *Server) respondWithGitHubTokens(w http.ResponseWriter, r *http.Request) {
	credentials := s.tasks.Config.Credentials
	if credentials == nil {
		writeJSON(w, http.StatusOK, gitHubTokensResponse{Enabled: false, Tokens: []gitHubTokenInfo{}})
		return
	}
	tokens := s.tasks.gitHubTokens(r.Context())
	restart := false
	for _, token := range tokens {
		restart = restart || token.NeedsRestart
	}
	writeJSON(w, http.StatusOK, gitHubTokensResponse{
		Enabled:         true,
		Dir:             credentials.Dir(),
		Tokens:          tokens,
		DefaultName:     s.tasks.defaultGitCredential(s.tasks.deploymentGitConnections(r.Context())),
		RestartRequired: restart,
	})
}
