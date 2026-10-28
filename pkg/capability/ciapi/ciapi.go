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

// Package ciapi lets a CI job hold agent API grants -- `api:<scope>` and
// `api:<preset>` (docs/agent-api.md) -- in its workflow's
// `capabilities:`, the same grants a task holds, so that what a job may
// read of this deployment is said in the one vocabulary grain already
// has rather than in a capability of its own per workflow.
//
// A CI container cannot call grain's MCP tools and must not hold grain's
// API: a workflow is a file on a branch, but what it reads is the whole
// deployment's. So a job's api grants are worth one bearer token, minted
// per job when its sandbox is acquired and revoked as it is released,
// and good only against a listener of its own (Endpoint), which answers
// with grain's own API handler, in process:
//
//   - a request is matched against that handler's route table, and
//     refused unless the token holds the route's scope
//     (pkg/ui/route_scopes.go, the agent API's own table);
//   - every request is narrowed to the job's own repository: a `repo`
//     query naming another is refused and one naming none is given the
//     job's, a route whose path names another repository is refused, and
//     whatever the answer holds of other repositories' (an object or
//     list element whose "repo" is another's) is dropped from it;
//   - only the scopes model.CIAPIScopeRefusal admits may be held at all:
//     reads that narrow to one repository, and tasks:from-template, which
//     files a task from the job's own repository's template, optionally
//     only the templates its grant names (model.APIPreset.Templates).
//
// release-gate-read, the release train's capability
// (docs/design/release-from-trunk.md, RFT-3), is the built-in preset
// api:release-gate-read, and the bare name a workflow declares is an
// alias for it (NewAlias).
//
// Like repo-write, a job is given these only on a post-merge `release`,
// an operator's `workflow_dispatch`, and a cron grain itself fired
// (model.TrustedScheduleCapability) -- never on a pull request's or an
// ordinary push's run, whose tree any agent can rewrite -- and only for
// the repositories the deployment configured, which by default is
// grain's own (model.GrainSourceRepo).
package ciapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Dir is where Materialize places the listener's address and the job's
// token. Under the sandbox user's home rather than /run/grain: placements
// are written as that user (mcp.PlaceFileOverSSH), who cannot create
// anything in /run.
const Dir = "/home/debian/.grain-api"

// URLPath, TokenPath and EnvPath are the three files placed in Dir: the
// address, the token, and both as URLEnv and TokenEnv exports for a step
// to source.
const (
	URLPath   = Dir + "/url"
	TokenPath = Dir + "/token"
	EnvPath   = Dir + "/env"
)

// URLEnv and TokenEnv are the variable names EnvPath exports.
const (
	URLEnv   = "GRAIN_API_URL"
	TokenEnv = "GRAIN_API_TOKEN"
)

// maxLease bounds a token nobody revoked: a job does not run for longer
// than this.
const maxLease = 6 * time.Hour

// Grant is what one token is worth: the repository its job runs for and
// the scopes it holds there.
type Grant struct {
	ID      string
	Repo    model.RepoRef
	RunID   string
	Expires time.Time
	Scopes  []model.APIScope
	// Templates limits tasks:from-template to these stems; AnyTemplate
	// is a grant of it with no limit.
	Templates   []string
	AnyTemplate bool
}

// Holds reports whether the grant holds scope.
func (g Grant) Holds(scope model.APIScope) bool { return slices.Contains(g.Scopes, scope) }

// MayFile reports whether the grant may file from the template stem.
func (g Grant) MayFile(stem string) bool {
	return g.Holds("tasks:from-template") && (g.AnyTemplate || slices.Contains(g.Templates, stem))
}

// Tokens is the registry of live tokens. Safe for concurrent use; the
// zero value is not -- use NewTokens.
type Tokens struct {
	mu      sync.Mutex
	byToken map[string]Grant
	now     func() time.Time
}

// NewTokens returns an empty registry.
func NewTokens() *Tokens {
	return &Tokens{byToken: make(map[string]Grant), now: time.Now}
}

// Mint records a new token worth grant (whose ID it sets) and returns
// the token's id, which is all Revoke needs and all a Lease records, and
// the token itself, which is placed in the sandbox.
func (t *Tokens) Mint(grant Grant) (id, token string, err error) {
	if id, err = randomHex(8); err != nil {
		return "", "", err
	}
	secret, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token = "ga_" + id + "_" + secret
	grant.ID = id
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(t.now())
	t.byToken[token] = grant
	return id, token, nil
}

// ForRun returns the live token minted for runID, if there is one.
func (t *Tokens) ForRun(runID string) (Grant, string, bool) {
	if t == nil || runID == "" {
		return Grant{}, "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(t.now())
	for token, g := range t.byToken {
		if g.RunID == runID {
			return g, token, true
		}
	}
	return Grant{}, "", false
}

// Lookup returns the grant token is worth, and false for a token that
// was never minted, has been revoked, or has expired.
func (t *Tokens) Lookup(token string) (Grant, bool) {
	if t == nil || token == "" {
		return Grant{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(t.now())
	var matched Grant
	var found bool
	for known, g := range t.byToken {
		if subtle.ConstantTimeCompare([]byte(known), []byte(token)) == 1 {
			matched = g
			found = true
		}
	}
	return matched, found
}

// Revoke drops the token with id. Idempotent.
func (t *Tokens) Revoke(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for known, g := range t.byToken {
		if g.ID == id {
			delete(t.byToken, known)
		}
	}
}

// Len is how many tokens are live.
func (t *Tokens) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(t.now())
	return len(t.byToken)
}

func (t *Tokens) sweepLocked(now time.Time) {
	for token, g := range t.byToken {
		if !g.Expires.IsZero() && !now.Before(g.Expires) {
			delete(t.byToken, token)
		}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ciapi: reading random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Upstream is the API handler a listener answers with: grain's own
// (ui.Server), which also says which of its routes a request would reach
// and the scope that route needs.
type Upstream interface {
	http.Handler
	APIRoute(r *http.Request) (pattern string, scope model.APIScope, ok bool)
}

// Endpoint is the listener: the token registry, where sandboxes reach
// it, and the API handler allowed requests are answered by.
type Endpoint struct {
	Tokens *Tokens
	// Log receives one line per request, allowed or refused. nil is
	// log.Printf.
	Log func(format string, args ...any)

	mu       sync.RWMutex
	url      string
	upstream Upstream
}

// NewEndpoint returns an endpoint with an empty registry, no address and
// no upstream: every request is refused until both are set.
func NewEndpoint() *Endpoint { return &Endpoint{Tokens: NewTokens()} }

// SetURL records where sandboxes reach this endpoint.
func (e *Endpoint) SetURL(u string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.url = strings.TrimRight(u, "/")
}

// URL is where sandboxes reach this endpoint, "" while it is not served.
func (e *Endpoint) URL() string {
	if e == nil {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.url
}

// SetUpstream records the API handler allowed requests are answered by:
// grain's own (ui.Server), called in process so that it answers the
// deployment's own actor.
func (e *Endpoint) SetUpstream(h Upstream) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.upstream = h
}

func (e *Endpoint) handler() Upstream {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.upstream
}

func (e *Endpoint) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Config is the provider's configuration.
type Config struct {
	// Repos are the repositories whose jobs may hold api grants. Empty
	// is grain's own (model.GrainSourceRepo).
	Repos []model.RepoRef
}

// Provider is one api grant name, held by a CI job.
type Provider struct {
	model.BaseCapability
	endpoint *Endpoint
	repos    []model.RepoRef
	// name is what the job declared; grant is what it means.
	name  string
	grant model.APIGrant
	err   error
}

// New returns the provider for the api grant name, minting into
// endpoint, which may be nil: every job is then refused, with a
// sentence. ok is false for a name that is not an api grant.
func New(endpoint *Endpoint, cfg Config, name string) (*Provider, bool) {
	g, ok, err := model.ParseAPIGrant(name)
	if !ok {
		return nil, false
	}
	return newProvider(endpoint, cfg, name, g, err), true
}

// NewAlias returns the provider for a bare capability name standing for
// the api grant target -- release-gate-read for api:release-gate-read.
func NewAlias(endpoint *Endpoint, cfg Config, name, target string) *Provider {
	g, _, err := model.ParseAPIGrant(target)
	return newProvider(endpoint, cfg, name, g, err)
}

func newProvider(endpoint *Endpoint, cfg Config, name string, g model.APIGrant, err error) *Provider {
	repos := cfg.Repos
	if len(repos) == 0 {
		repos = []model.RepoRef{model.GrainSourceRepo()}
	}
	return &Provider{endpoint: endpoint, repos: repos, name: name, grant: g, err: err}
}

// Aliases are the bare capability names that stand for an api grant in
// a CI job's `capabilities:`.
var Aliases = map[string]string{
	model.ReleaseGateReadCapability: model.APIGrantPrefix + model.ReleaseGateReadCapability,
}

// DynamicProvider resolves every api grant name, and every alias, to its
// CI provider (model.CapabilityRegistry.RegisterCIDynamic).
func DynamicProvider(endpoint *Endpoint, cfg Config) model.DynamicProvider {
	return func(name string) (model.CapabilityProvider, bool) {
		if target, ok := Aliases[name]; ok {
			return NewAlias(endpoint, cfg, name, target), true
		}
		p, ok := New(endpoint, cfg, name)
		if !ok {
			return nil, false
		}
		return p, true
	}
}

// Spec implements model.CapabilityProvider.
func (p *Provider) Spec() model.CapabilitySpec {
	label := "Grain API (CI): " + strings.TrimPrefix(p.grant.Name, model.APIGrantPrefix)
	description := "Let a trusted CI job call grain's API for its own repository, through a per-job token " +
		"worth the routes of " + p.grant.Name + " only"
	if p.name == model.ReleaseGateReadCapability {
		label = "Release gate (read)"
		description = "Let a release workflow's job read this deployment's CI runs, persistent sandboxes and tasks " +
			"for its own repository, and file its release-notes task: the api:release-gate-read preset, held through a per-job token"
	}
	return model.CapabilitySpec{
		Name:        p.name,
		Label:       label,
		Description: description,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionMint,
		MaxLease:    maxLease,
		CIOnly:      true,
	}
}

// SupportsCIEvent admits a post-merge release and an operator's
// workflow_dispatch, as repo-write does.
func (*Provider) SupportsCIEvent(event string) bool {
	return event == "release" || event == "workflow_dispatch"
}

// SupportsTrustedSchedule admits the cron grain itself fired from a
// workflow on the default branch: release-train.yml is one
// (docs/design/release-from-trunk.md §6). A caller's own `-event
// schedule` run is still refused (ci.Request.TrustedSchedule).
func (*Provider) SupportsTrustedSchedule() bool { return true }

// refusal is why this grant may not be held by a job of repo, or "".
func (p *Provider) refusal(repo model.RepoRef) string {
	if p.err != nil {
		return p.err.Error()
	}
	if !p.allows(repo) {
		return fmt.Sprintf("%s is not offered to %s's jobs: it reads deployment-wide state, and grain's API is "+
			"offered to CI for %s only", p.name, repo, p.repoList())
	}
	if p.grant.Repo != "" && !strings.EqualFold(p.grant.Repo, repo.String()) {
		return fmt.Sprintf("%s names %s, and a CI job's token reaches its own repository (%s) only", p.name, p.grant.Repo, repo)
	}
	if p.grant.RequiresPerson {
		return fmt.Sprintf("%s is a scope set only a person may grant, which no CI job is ever given", p.name)
	}
	for _, scope := range p.grant.Scopes {
		if why := model.CIAPIScopeRefusal(scope); why != "" {
			return p.name + ": " + why
		}
	}
	return ""
}

// Resolve refuses a job of any repository this was not configured for,
// a grant holding any scope a CI job may not hold, and any job while the
// listener is not being served.
func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if p.endpoint == nil || p.endpoint.URL() == "" {
		return model.RefusedBecause(p.name + " needs grain's CI API listener, which this daemon " +
			"is not serving; an operator finds why in grain's own log"), nil
	}
	if cc.Task.Target == nil {
		return model.RefusedBecause(p.name + " is held for one repository's job, and this one names none"), nil
	}
	if why := p.refusal(*cc.Task.Target); why != "" {
		return model.RefusedBecause(why), nil
	}
	return model.Honoured(), nil
}

func (p *Provider) allows(repo model.RepoRef) bool {
	return slices.ContainsFunc(p.repos, repo.Same)
}

func (p *Provider) repoList() string {
	names := make([]string, len(p.repos))
	for i, r := range p.repos {
		names[i] = r.String()
	}
	return strings.Join(names, ", ")
}

// ErrNotServed is Materialize's error while the listener has no address.
var ErrNotServed = errors.New("ciapi: grain's CI API listener is not being served")

// jobGrant is the one token's worth for every api grant the job holds
// that this provider's own rules honour: a job holds one token, however
// many api grants it declared.
func (p *Provider) jobGrant(cc model.CapabilityContext, repo model.RepoRef, expires time.Time) Grant {
	out := Grant{Repo: repo, RunID: cc.Run.ID, Expires: expires}
	for _, g := range cc.Task.Grants {
		name := g.Capability
		var held *Provider
		if target, ok := Aliases[name]; ok {
			held = NewAlias(p.endpoint, Config{Repos: p.repos}, name, target)
		} else if q, ok := New(p.endpoint, Config{Repos: p.repos}, name); ok {
			held = q
		} else {
			continue
		}
		if held.refusal(repo) != "" {
			continue
		}
		for _, scope := range held.grant.Scopes {
			if !slices.Contains(out.Scopes, scope) {
				out.Scopes = append(out.Scopes, scope)
			}
			if scope != "tasks:from-template" {
				continue
			}
			if len(held.grant.Templates) == 0 {
				out.AnyTemplate = true
			}
			for _, t := range held.grant.Templates {
				if !slices.Contains(out.Templates, t) {
					out.Templates = append(out.Templates, t)
				}
			}
		}
	}
	slices.Sort(out.Scopes)
	return out
}

// Materialize mints this job's token -- once per job, worth every api
// grant it holds -- and places it with the listener's address.
func (p *Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	if p.endpoint == nil || p.endpoint.URL() == "" {
		return model.Materialization{}, ErrNotServed
	}
	if cc.Task.Target == nil {
		return model.Materialization{}, fmt.Errorf("ciapi: %s is held for one repository's job, and this one names none", p.name)
	}
	repo := *cc.Task.Target
	if why := p.refusal(repo); why != "" {
		return model.Materialization{}, fmt.Errorf("ciapi: %s", why)
	}
	listener := p.endpoint.URL()
	if _, token, ok := p.endpoint.Tokens.ForRun(cc.Run.ID); ok && cc.Run.ID != "" {
		// Another of the job's api grants minted it already: place the
		// same token again, and leave revoking it to that grant's lease.
		return model.Materialization{Placements: placements(listener, token)}, nil
	}
	now := cc.Now
	if now.IsZero() {
		now = time.Now()
	}
	expires := now.Add(maxLease)
	if !cc.Deadline.IsZero() && cc.Deadline.Before(expires) {
		expires = cc.Deadline
	}
	id, token, err := p.endpoint.Tokens.Mint(p.jobGrant(cc, repo, expires))
	if err != nil {
		return model.Materialization{}, err
	}
	return model.Materialization{
		Lease:      &model.Lease{Capability: p.name, Resource: id, IssuedAt: now, ExpiresAt: &expires},
		Placements: placements(listener, token),
	}, nil
}

func placements(listener, token string) []model.Placement {
	return []model.Placement{
		{Side: model.SideSandbox, Path: URLPath, Content: listener + "\n"},
		{Side: model.SideSandbox, Path: TokenPath, Content: token + "\n"},
		{Side: model.SideSandbox, Path: EnvPath, Content: fmt.Sprintf("export %s=%q\nexport %s=%q\n",
			URLEnv, listener, TokenEnv, token)},
	}
}

// Revoke drops the token the lease names.
func (p *Provider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	if p.endpoint != nil {
		p.endpoint.Tokens.Revoke(lease.Resource)
	}
	return nil
}
