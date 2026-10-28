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

// Package githubsandbox is the "github-sandbox" capability: a MINT
// provider (docs/data-model.md, model/capability.go) that creates a
// private, single-use GitHub repo under a dedicated bot account and
// pushes a token scoped to just that repo into the sandbox --
// bwsalmon/agents#354.
//
// **A GitHub App, not a bot-account PAT.** GitHub has no API to mint a
// personal access token for an account -- that can only be done
// interactively, through the web UI -- so nothing short of a GitHub App
// installation token can be minted programmatically, per repo, on
// demand. An App installation access token is exactly the shape
// bwsalmon/agents#354 asked this capability to hand the agent: real,
// GitHub-enforced scoping to one repo and a chosen permission set that
// structurally excludes changing that repo's visibility or settings
// (never request the "administration" -- or, for the org-level call
// that creates the repo, "organization_administration" -- permission on
// the token the sandbox actually receives), rather than a broader
// standing credential the agent is merely asked nicely not to misuse.
//
// bwsalmon/agents#159 built exactly this once before, in the v1 Python
// controller, and bwsalmon/agents#186 reverted it back to a single
// broad PAT: JWT-signing by shelling out to openssl, and two
// independent minting call sites that each had to re-derive a fresh
// token on their own clock, for scoping a single deployment did not
// need (see grain/automation/scratch_repo.py's docstring). Neither
// objection carries over here: Go's stdlib-adjacent crypto/rsa (via
// golang-jwt/jwt) signs the App JWT with no subprocess, and app.go is
// this package's one minting call site, called fresh for every
// Materialize/Revoke/Reap the same way gcpkey's own NewMinter is
// (see that package's doc comment) -- so this reopens bwsalmon/
// agents#186's decision deliberately, not by accident, in a setting
// where its reasons no longer apply.
//
// **Setup is still one App, not "username and password"** -- the
// literal secret shape bwsalmon/agents#354 first asked for turned out
// to be unbuildable (no API mints a PAT), and scripting GitHub's own
// login page to drive the App-manifest flow is unreliable against any
// account with two-factor authentication, which a dedicated bot account
// is squarely in scope for GitHub now requiring. `grain controller
// bootstrap-github-app` (pkg/cli/controller.go) is the low-effort
// middle ground that was agreed instead: it prints a pre-filled
// manifest URL an operator opens once, in their own browser, logged in
// as the bot account, and pastes back the resulting code -- no
// password ever reaches grain, no scraping, no 2FA fragility. What
// comes out the other end -- an App ID and a private key -- are this
// capability's two secrets (DefaultAppIDCredential,
// DefaultPrivateKeyCredential).
//
// **No org-level config, deliberately.** Nothing here names which
// account the App belongs to: FindInstallation (app.go) asks GitHub,
// at Materialize/Revoke/Reap time, which single account the App is
// installed on, and refuses (rather than guessing) if that isn't
// exactly one -- one less thing bootstrap-github-app or an operator's
// flags have to get right, and one less place a typo'd org name could
// quietly mint into the wrong account.
//
// **A 1-hour lease is GitHub's own ceiling, not a choice.** An
// installation access token cannot be minted with a longer lifetime,
// so a run needing git access past about an hour will find its
// credential stale. Stray repos follow the daemon's shared trailing
// edge instead; their cleanup timing is independent of the token's
// lifetime. That
// is a real, current limitation of handing the agent a raw token up
// front at Materialize time rather than minting one on demand through
// an MCP tool call mid-run; see bwsalmon/agents#354's own issue thread
// for why this package took the simpler shape instead.
package githubsandbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// capabilityName is model.CapabilitySpec.Name and the string every
// Lease this package mints carries as Lease.Capability.
const capabilityName = "github-sandbox"

// RepoPrefix names every repo this capability creates, "-<repo-safe run
// ID>" appended -- grain/automation/scratch_repo.py's own
// "grain-scratch-<sandbox>" convention, restated for a name Reap can
// recognize as its own the same way geminikey's displayNamePrefix lets
// DeleteExpired tell its keys apart from anyone else's.
const RepoPrefix = "grain-sandbox"

// maxLease is GitHub's own installation-access-token ceiling -- see
// this package's doc comment. Also model.CapabilitySpec.MaxLease.
const maxLease = time.Hour

// DefaultHost is Config.Host's default -- the same "github.com" default
// pkg/cli/daemon.go's own -github-host flag uses, restated here since
// this package is constructed independently of that flag in tests.
const DefaultHost = "github.com"

// DefaultAppIDCredential and DefaultPrivateKeyCredential are
// Config.AppIDCredential/PrivateKeyCredential's defaults: the two keys
// an operator's secrets store is expected to hold under one "github-app"
// secret (pkg/secrets' own "<secret>/<key>" shape) once `grain
// controller bootstrap-github-app` has produced them.
const (
	DefaultAppIDCredential      = "github-app/app-id"
	DefaultPrivateKeyCredential = "github-app/private-key"
)

// SandboxTokenPath and SandboxRepoPath are where Materialize places the
// agent's token and the repo it names -- fixed, not configurable,
// because PromptSection's own text names them verbatim.
const (
	SandboxTokenPath = "/home/debian/.github-sandbox-token"
	SandboxRepoPath  = "/home/debian/.github-sandbox-repo"
)

// agentPermissions is the permission set the token placed in the
// sandbox carries -- contents, issues, pull requests, Actions
// secrets (bwsalmon/agents#354's "must be able to manage PRs and
// secrets"), and packages (so disposable sandbox repos can receive
// package publish permissions), plus the metadata read every token
// needs to address a repo at all. Deliberately omits "administration"
// (would let the token change this repo's visibility or settings)
// and "workflows" (docs/design.md's "Scopes to withhold" gives no
// App token here either).
var agentPermissions = map[string]string{
	"contents":      "write",
	"issues":        "write",
	"pull_requests": "write",
	"secrets":       "write",
	"packages":      "write",
	"metadata":      "read",
}

// createRepoPermissions is the permission an installation-wide token
// (no repositories named -- see MintToken) needs to create a new repo
// in the org the App is installed on, or list every repo in it for
// Reap. Never handed to a sandbox: only app.go's own controller-side
// calls (CreateRepo, ListRepos) ever see a token minted with this.
var createRepoPermissions = map[string]string{"organization_administration": "write"}

// deleteRepoPermissions is the permission a token scoped to exactly one
// repo (via MintToken's repos argument) needs to delete it --
// controller-side only, minted fresh by Revoke and Reap, never placed
// in a sandbox.
var deleteRepoPermissions = map[string]string{"administration": "write"}

// repoNameDisallowed matches everything a GitHub repo name may not
// contain -- only alphanumerics, ".", "_", and "-" are allowed.
var repoNameDisallowed = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// repoName is the repo Materialize creates for run, deterministic in
// run.ID alone -- the same "deterministic, not self-reported" reasoning
// model.BranchName's own doc comment gives for the branch an agent
// pushes to, so PromptSection can describe a repo it never round-trips
// through a placement's Content (Placement's own doc comment: Content
// is "never handed to PromptSection").
func repoName(runID string) string {
	safe := repoNameDisallowed.ReplaceAllString(runID, "-")
	if max := 100 - len(RepoPrefix) - 1; len(safe) > max {
		safe = safe[:max]
	}
	return RepoPrefix + "-" + safe
}

// Config is this deployment's github-sandbox settings, held on the
// Provider itself rather than threaded through CapabilityContext --
// the same reason gcpkey.Config is, since v2's CapabilityContext
// carries nothing capability-specific.
type Config struct {
	// Host is the GitHub API host. Empty means DefaultHost.
	Host string
	// InsecureHTTP speaks plain HTTP to Host instead of HTTPS -- mock
	// servers only, mirroring github.RealTransport.UseTLS.
	InsecureHTTP bool
	// AppIDCredential and PrivateKeyCredential are resolved through
	// CapabilityContext.Credentials to the GitHub App's own ID and RSA
	// private key. Empty means the Default* constants above.
	AppIDCredential      string
	PrivateKeyCredential string
}

// Provider is the github-sandbox capability. The zero value is not
// usable -- construct one with NewProvider, which fills in newClient.
type Provider struct {
	model.BaseCapability
	Config Config
	// newClient builds an appClient authenticated as the App named by
	// appID/privateKeyPEM. NewProvider sets this to newAppClient; tests
	// substitute a fake with no network involved, the same "no sandbox
	// and no cloud" bar model/capability_test.go's own test capabilities
	// hold to. Always rebuilt, never cached -- gcpkey.Provider.NewMinter's
	// own doc comment gives the same reason: a fresh client per call
	// keeps two credentials from ever leaving which one "wins" to
	// ambient state.
	newClient func(ctx context.Context, appID, privateKeyPEM, host string, insecureHTTP bool) (appClient, error)
}

// NewProvider builds a Provider wired to the real GitHub API.
func NewProvider(cfg Config) *Provider {
	return &Provider{Config: cfg, newClient: newAppClient}
}

func (p *Provider) appIDCredential() string {
	if p.Config.AppIDCredential != "" {
		return p.Config.AppIDCredential
	}
	return DefaultAppIDCredential
}

func (p *Provider) privateKeyCredential() string {
	if p.Config.PrivateKeyCredential != "" {
		return p.Config.PrivateKeyCredential
	}
	return DefaultPrivateKeyCredential
}

func (p *Provider) host() string {
	if p.Config.Host != "" {
		return p.Config.Host
	}
	return DefaultHost
}

func (p *Provider) client(ctx context.Context, creds model.CredentialResolver) (appClient, error) {
	appID, err := creds.Resolve(ctx, p.appIDCredential())
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: resolving %q: %w", p.appIDCredential(), err)
	}
	privateKey, err := creds.Resolve(ctx, p.privateKeyCredential())
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: resolving %q: %w", p.privateKeyCredential(), err)
	}
	build := p.newClient
	if build == nil {
		build = newAppClient
	}
	client, err := build(ctx, appID, privateKey, p.host(), p.Config.InsecureHTTP)
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: authenticating as the App: %w", err)
	}
	return client, nil
}

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        capabilityName,
		Label:       "grain-github-sandbox",
		Description: "Create a private, single-use GitHub sandbox repo for this task",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionMint,
		MaxLease:    maxLease,
		Requires:    []string{p.appIDCredential(), p.privateKeyCredential()},
	}
}

// Resolve refuses when this deployment has no GitHub App configured,
// rather than reporting the grant honoured and failing later.
//
// The provider registers unconditionally (pkg/cli/daemon.go's
// capabilityProviders): it needs no deployment-level config beyond its
// two secrets, and FindInstallation asks GitHub itself which account to
// act on. So on a deployment where `grain controller
// bootstrap-github-app` was never run, this capability is offered,
// attaches like any other, and only the run finds out.
//
// Without this, that discovery came from BaseCapability.Resolve saying
// Honoured and Materialize then failing on a missing credential --
// which prepareCapabilities reports as "materializing capabilities:
// resolving credential github-app/app-id", true but naming neither the
// capability's own state nor what to do. Both endings refuse to run the
// agent, so nothing about the safety changes here; what changes is that
// the refusal arrives at the point built for saying a deployment cannot
// offer something, carrying the command that would make it able to.
func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if missing, ok := p.unconfigured(ctx, cc.Credentials); ok {
		return model.RefusedBecause(missing), nil
	}
	return model.Honoured(), nil
}

// unconfigured reports whether this deployment has no GitHub App at all,
// and a human-readable reason if so. Distinguishes "never set up" from
// "set up and failing": only the absence of a credential counts here, so
// a real API failure still surfaces as an error wherever it happens.
func (p *Provider) unconfigured(ctx context.Context, creds model.CredentialResolver) (string, bool) {
	if creds == nil {
		return "no credential resolver is configured, so this deployment cannot authenticate as a GitHub App", true
	}
	for _, name := range []string{p.appIDCredential(), p.privateKeyCredential()} {
		if _, err := creds.Resolve(ctx, name); err != nil {
			return fmt.Sprintf(
				"this deployment has no GitHub App configured (%s is unset) -- run `grain controller bootstrap-github-app` on the host, or detach this capability from the task",
				name), true
		}
	}
	return "", false
}

// Materialize creates a fresh private repo, named for cc.Run.ID alone
// (repoName), under whatever single account the App is installed on,
// and mints a second, narrower token -- scoped by repo name, not by
// numeric id, so this needs no round trip to look one up -- carrying
// only agentPermissions before placing it in the sandbox.
//
// A repo-creation token is minted installation-wide (no repos named)
// rather than scoped to the not-yet-existing repo, since nothing can
// name a repo id before CreateRepo returns one. If minting the second,
// agent-facing token fails after CreateRepo already succeeded, the repo
// is left behind rather than cleaned up here -- Materialize returning
// an error means this run never dispatches (prepareCapabilities' own
// doc comment: "a failed materialize means no dispatch"), so no Lease
// is ever recorded for it either, and only Reap's independent listing
// will find and delete it. The same trade gcpkey's own Materialize
// accepts for a CreateKey that succeeds just before some later step
// fails.
func (p *Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	client, err := p.client(ctx, cc.Credentials)
	if err != nil {
		return model.Materialization{}, err
	}
	installation, err := client.FindInstallation(ctx)
	if err != nil {
		return model.Materialization{}, fmt.Errorf("githubsandbox: %w", err)
	}
	repo := repoName(cc.Run.ID)
	createToken, err := client.MintToken(ctx, installation.ID, nil, createRepoPermissions)
	if err != nil {
		return model.Materialization{}, fmt.Errorf("githubsandbox: minting a repo-creation token: %w", err)
	}
	if _, err := client.CreateRepo(ctx, createToken.Token, installation.Account, repo); err != nil {
		return model.Materialization{}, fmt.Errorf("githubsandbox: creating repo %s/%s: %w", installation.Account, repo, err)
	}
	agentToken, err := client.MintToken(ctx, installation.ID, []string{repo}, agentPermissions)
	if err != nil {
		return model.Materialization{}, fmt.Errorf(
			"githubsandbox: minting the agent's scoped token for %s/%s: %w", installation.Account, repo, err,
		)
	}
	resource := installation.Account + "/" + repo
	expires := cc.Now.Add(maxLease)
	return model.Materialization{
		Lease: &model.Lease{
			Capability: capabilityName,
			Resource:   resource,
			// Both AppIDCredential and PrivateKeyCredential authenticate
			// as the one App, rotating as a pair -- unlike gcpkey's
			// MintedBy (a single minter-account credential, resolved
			// fresh from the Lease rather than current Config so a
			// rotated minter never strands an old lease), Revoke and
			// Reap below always resolve p's *current* Config instead of
			// this field: deleting a repo needs any valid credential for
			// the same App and installation, not the specific key that
			// happened to sign the JWT that created it. MintedBy is
			// still set, for the same audit trail every other lease
			// carries one for.
			MintedBy:  model.CredentialRef{Name: p.privateKeyCredential()},
			IssuedAt:  cc.Now,
			ExpiresAt: &expires,
		},
		Placements: []model.Placement{
			{Side: model.SideSandbox, Path: SandboxTokenPath, Content: agentToken.Token},
			{Side: model.SideSandbox, Path: SandboxRepoPath, Content: resource},
		},
	}, nil
}

// PromptSection tells the run what it has been given and how to drive it,
// and drives it with `git` and `curl` rather than with `gh`.
//
// It used to say `gh auth setup-git`, `gh pr ...`, `gh secret set ...`,
// which was instructions that fail on their first line: no sandbox image
// grain builds carries the GitHub CLI, and none is going to -- see
// docs/design.md's "The sandbox image has no `gh`", which is the decision
// this text was found out of step with (grain/task-281). Nothing about
// what the token may do changed; only what the run is told to spend its
// turns typing. The two things it needs are in the guest already: git,
// with the token as the password half of HTTPS basic auth the way GitHub
// documents an App installation token, and curl for everything that is a
// REST call rather than a fetch -- pull requests, issues, Actions secrets.
//
// The token goes in the clone URL rather than into a credential helper
// because the helper the guest already has (mcp.ConfigureGitCredentials)
// is configured for the git proxy's host, not for github.com, and a run
// holding this capability may have no repo of its own and so no helper at
// all. A URL works either way, and the token is a one-hour credential the
// agent is being handed in a file regardless.
func (p *Provider) PromptSection(ctx context.Context, cc model.CapabilityContext, placements []model.Placement) (string, error) {
	var tokenPath, repoPath string
	for _, pl := range placements {
		switch pl.Path {
		case SandboxTokenPath:
			tokenPath = pl.Path
		case SandboxRepoPath:
			repoPath = pl.Path
		}
	}
	if tokenPath == "" || repoPath == "" {
		return "", fmt.Errorf("githubsandbox: expected a token and a repo placement, got %d placements", len(placements))
	}
	return fmt.Sprintf(
		"A private GitHub sandbox repo has been created for this task. Its owner/repo "+
			"name is in %s; a token scoped to only that repo (contents, issues, pull "+
			"requests, packages, and Actions secrets -- never its visibility or settings), valid "+
			"for about an hour, is in %s, readable only by you. This sandbox has no "+
			"`gh` -- use the token with git and curl:\n\n"+
			"    export GITHUB_TOKEN=\"$(cat %s)\"\n"+
			"    export SANDBOX_REPO=\"$(cat %s)\"\n"+
			"    git clone \"https://x-access-token:$GITHUB_TOKEN@github.com/$SANDBOX_REPO.git\"\n"+
			"    curl -sS -H \"Authorization: Bearer $GITHUB_TOKEN\" \\\n"+
			"      -H \"Accept: application/vnd.github+json\" \\\n"+
			"      \"https://api.github.com/repos/$SANDBOX_REPO/pulls\"\n\n"+
			"You can push branches, open and manage pull requests, publish packages, and manage this "+
			"repo's Actions secrets -- everything but a fetch or a push is a REST call "+
			"to api.github.com with that same header -- but you cannot "+
			"make the repo public or grant yourself any wider access; the token "+
			"structurally carries no permission to change its settings or visibility. "+
			"The repo and the token are deleted once this sandbox exits.\n",
		repoPath, tokenPath, tokenPath, repoPath,
	), nil
}

// Revoke deletes the repo Materialize created, minting a fresh,
// repo-scoped deleteRepoPermissions token to do it with -- see this
// package's doc comment on why Revoke resolves p's current Config
// rather than lease.MintedBy. Idempotent: DeleteRepo treats an
// already-gone repo as success, the same tolerance docs/data-model.md
// asks of every provider's Revoke.
func (p *Provider) Revoke(ctx context.Context, cc model.CapabilityContext, lease model.Lease) error {
	owner, repo, ok := strings.Cut(lease.Resource, "/")
	if !ok || owner == "" || repo == "" {
		return fmt.Errorf("githubsandbox: lease resource %q is not owner/repo", lease.Resource)
	}
	client, err := p.client(ctx, cc.Credentials)
	if err != nil {
		return err
	}
	installation, err := client.FindInstallation(ctx)
	if err != nil {
		return fmt.Errorf("githubsandbox: %w", err)
	}
	token, err := client.MintToken(ctx, installation.ID, []string{repo}, deleteRepoPermissions)
	if err != nil {
		return fmt.Errorf("githubsandbox: minting a delete token for %s: %w", lease.Resource, err)
	}
	if err := client.DeleteRepo(ctx, token.Token, owner, repo); err != nil {
		return fmt.Errorf("githubsandbox: deleting repo %s: %w", lease.Resource, err)
	}
	return nil
}

// CheckCredential implements model.CredentialChecker: it signs a JWT
// with the App's stored private key and asks GitHub which installation
// it has, which is the cheapest call this package makes and the first
// one every Materialize makes anyway.
//
// The App's private key is a standing credential like gcp-key's minter,
// and goes stale the same silent way: a key regenerated in the App's own
// settings, an installation revoked, an App deleted -- none of which
// changes anything in this deployment's secrets, so a configuration pane
// goes on reporting the same two secrets set and **Ready**. Only GitHub
// knows, and only when something authenticates.
//
// FindInstallation reads; it mints no token and creates no repo. It is
// also the call that answers the one question beyond "is the key live"
// worth asking here -- that the App is installed on exactly one account,
// which is what this capability's whole "no config names an account"
// design rests on, and which FindInstallation itself errors on when it
// is not true.
func (p *Provider) CheckCredential(ctx context.Context, creds model.CredentialResolver) (model.CredentialCheck, error) {
	check := model.CredentialCheck{Credentials: []string{p.appIDCredential(), p.privateKeyCredential()}}
	if reason, ok := p.unconfigured(ctx, creds); ok {
		return check, errors.New("githubsandbox: " + reason)
	}
	client, err := p.client(ctx, creds)
	if err != nil {
		return check, err
	}
	installation, err := client.FindInstallation(ctx)
	if err != nil {
		return check, fmt.Errorf(
			"githubsandbox: %s would not answer for the App this deployment's `%s`/`%s` "+
				"secrets hold: the App's private key has been regenerated or the App "+
				"deleted, or its installation was revoked. Generate a fresh private key in "+
				"the App's settings and paste it into Settings -> Capabilities (or re-run "+
				"`grain controller bootstrap-github-app` on the host): %w",
			p.host(), p.appIDCredential(), p.privateKeyCredential(), err)
	}
	check.Detail = fmt.Sprintf(
		"%s accepted this deployment's GitHub App and reports it installed on %s (installation %d).",
		p.host(), installation.Account, installation.ID)
	return check, nil
}

// Reap implements model.Reaper: it lists every repo in the App's
// installation account and deletes Grain sandbox repos created before the
// caller's shared trailing edge. The daemon derives that edge once from the
// oldest live agent or CI run and applies one grace period across capabilities.
//
// Deletion is best-effort across repos, but failures are returned alongside
// successful deletions so an incomplete sweep remains visible.
//
// ListRepos reads at most 100 repos (app.go's own doc comment).
func (p *Provider) Reap(ctx context.Context, creds model.CredentialResolver, before time.Time) ([]string, error) {
	// Nothing to reap on a deployment that never had a GitHub App: it has
	// created no sandbox repos, so there are none to clean up. Returning
	// an error instead made the daemon's hourly sweep log
	// `reaping capability "github-sandbox": ...` forever on every
	// deployment that does not use this capability -- a recurring error
	// nobody can act on, which is how the ones that matter get missed.
	//
	// Only absence is treated this way. An App that is configured and
	// failing still errors, because that is a real problem with real
	// leaked repos behind it.
	if _, ok := p.unconfigured(ctx, creds); ok {
		return nil, nil
	}
	client, err := p.client(ctx, creds)
	if err != nil {
		return nil, err
	}
	installation, err := client.FindInstallation(ctx)
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: %w", err)
	}
	listToken, err := client.MintToken(ctx, installation.ID, nil, createRepoPermissions)
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: minting a listing token: %w", err)
	}
	repos, err := client.ListRepos(ctx, listToken.Token, installation.Account)
	if err != nil {
		return nil, fmt.Errorf("githubsandbox: listing repos for %s: %w", installation.Account, err)
	}
	var deleted []string
	var deleteErrs []error
	for _, r := range repos {
		if !strings.HasPrefix(r.Name, RepoPrefix+"-") {
			continue
		}
		if r.CreatedAt.IsZero() || !r.CreatedAt.Before(before) {
			continue
		}
		token, err := client.MintToken(ctx, installation.ID, []string{r.Name}, deleteRepoPermissions)
		if err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("minting delete token for %s: %w", r.Name, err))
			continue
		}
		if err := client.DeleteRepo(ctx, token.Token, installation.Account, r.Name); err != nil {
			deleteErrs = append(deleteErrs, fmt.Errorf("deleting repo %s/%s: %w", installation.Account, r.Name, err))
			continue
		}
		deleted = append(deleted, installation.Account+"/"+r.Name)
	}
	return deleted, errors.Join(deleteErrs...)
}
