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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/admincaps"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	consolecap "github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/capability/gitpushanyref"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/imagesigning"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	modelproxycap "github.com/ai-on-gke/grain/pkg/capability/modelproxy"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/prioritydispatch"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/capability/reporead"
	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/capability/selfrepair"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func newCapabilityRegistryWithStore(store *model.Store, local localscripts.Config, providers ...model.CapabilityProvider) *model.CapabilityRegistry {
	reg := model.NewCapabilityRegistry(providers...)
	reg.RegisterDynamic(reporead.DynamicProvider, repowrite.DynamicProvider, gcpkey.DynamicProvider(store),
		localscripts.DynamicProvider(local), ui.AgentAPIDynamicProvider)
	// A CI job's api grants are a token for CIAPI rather than routes an
	// agent may call (pkg/capability/ciapi).
	reg.RegisterCIDynamic(ciapi.DynamicProvider(CIAPI, ciapi.Config{}))
	return reg
}

// capabilityProviders builds every capability provider this deployment
// has enough configuration for. No provider is required: a task that
// grants a capability against a registry that never registered one is
// refused, cleanly, by model.ResolveGrants -- not a crash at startup.
//
// gcp-key and gemini-key both need a GCP project to mint into, so both
// stay gated on cfg.gcpProject exactly as before. github-sandbox needs
// no equivalent deployment-level config -- its two secrets are all it
// resolves, and FindInstallation asks GitHub itself which account to
// act on (pkg/capability/githubsandbox's own doc comment) -- so it
// registers unconditionally rather than sharing that gate; an operator
// who never runs `grain controller bootstrap-github-app` simply leaves
// its two secrets unresolvable, and Materialize fails closed the same
// way any other missing secret does.
//
// githubTokens is this deployment's named GitHub tokens beyond its
// default one (gitHubCredentialLadder below), one provider each, and
// githubCredentials is where a check for one of those reads its material
// -- the ladder itself, which is the only thing that knows both forms a
// named credential comes in (githubtoken.Config.Credentials). It may be
// nil: a dispatch through one of these providers resolves no credential
// at all (they mint nothing and place nothing), so only the check misses
// it, and it says so rather than failing the token.
//
// proxies is the model proxy endpoints the claude-, openai- and
// gemini-proxy providers resolve against (modelproxycap.Providers).
func CapabilityProviders(cfg Config, store *model.Store, proxies map[string]*modelproxy.Endpoint, githubTokens []string, githubCredentials githubCredentialLadder) []model.CapabilityProvider {
	var providers []model.CapabilityProvider
	if len(missingCapabilityProviderConfig(cfg, "gcp-key")) == 0 {
		providers = append(providers, gcpkey.NewProvider(gcpkey.Config{
			ProjectID: cfg.GcpProject, ServiceAccountEmail: cfg.GcpServiceAccountEmail,
			ServiceAccountCount: cfg.GcpServiceAccountCount,
		}))
	}
	if len(missingCapabilityProviderConfig(cfg, "gemini-key")) == 0 {
		providers = append(providers, geminikey.New(cfg.GcpProject, model.CredentialRef{Name: gcpkey.DefaultMinterCredential}))
	}
	providers = append(providers, githubsandbox.NewProvider(githubsandbox.Config{
		Host: cfg.GithubHost, InsecureHTTP: cfg.GithubInsecureHTTP,
	}))
	providers = append(providers, admincaps.Providers()...)
	// git-push-any-ref is a team capability like the ones beside it, not
	// an admin one (grain/task-2536): what it lifts is the proxy's
	// one-branch rule, and the team's own git credential is still what
	// every push goes out with.
	providers = append(providers,
		gitpushanyref.New(),
		bootstrap.New(),
		grainci.New(),
		registrypush.NewWithConfig(registrypush.Config{}),
		consolecap.New(),
		readissues.New(),
		prioritydispatch.New(),
		durable.New(),
	)
	// package-publish registers unconditionally, on the same terms
	// registry-push does: there is no deployment setting to gate it on,
	// and what it needs -- a credential in this deployment's own ladder
	// covering the repository the job is for -- is something it can see
	// for itself at resolve time and refuse with a sentence naming the
	// repository that has none.
	//
	// The registry is left at packagepublish's default, ghcr.io, which
	// is what a GitHub credential publishes to on github.com. A GHES
	// deployment publishes somewhere else and has no setting to say so
	// yet; it gets a login that fails at the registry, which is the
	// honest outcome until there is a configuration field for it.
	providers = append(providers, packagepublish.New(packagepublish.Config{
		Credentials: githubCredentials,
	}))
	// image-signing registers unconditionally too, and for the same
	// reason: what it needs is two secrets, which its own Resolve looks
	// for and refuses by name when they are missing. It answers the
	// release event alone, never workflow_dispatch, so it stays strictly
	// narrower than package-publish beside it (docs/image-signing.md).
	providers = append(providers, imagesigning.New(imagesigning.Config{}))
	// release-gate-read registers unconditionally too: it is the CI alias
	// for the api:release-gate-read preset (pkg/capability/ciapi), and
	// what it needs is this process's CI API listener (CIAPI), which its
	// own Resolve refuses without, and a job of grain's own repository,
	// which it refuses every other one for (design/design/release-from-trunk.design.md §6).
	providers = append(providers, ciapi.NewAlias(CIAPI, ciapi.Config{}, model.ReleaseGateReadCapability,
		ciapi.Aliases[model.ReleaseGateReadCapability]))
	// claude-proxy, openai-proxy and gemini-proxy register
	// unconditionally, like github-sandbox above: there is no deployment
	// setting to gate them on. What each needs is a running proxy and
	// that vendor's credential, and both are things it can see for
	// itself at resolve time (its own Resolve refuses with a sentence
	// naming whichever is missing) rather than facts about this config
	// value. The endpoints are whichever set this registry is being built
	// over -- this process's own modelProxies for every caller in this
	// file -- see pkg/cli/modelproxy.go for why those are a
	// package-level value that is nonetheless passed in.
	for _, p := range modelproxycap.Providers(proxies) {
		providers = append(providers, p)
	}
	// One per named GitHub token beyond this deployment's default one
	// (grain/task-117). Registered last, and unconditionally on the names
	// being there: a token is a capability because an operator's own file
	// under secrets/github says so, not because anything in this build
	// enumerates it -- so without these, granting one would be refused as
	// a capability no provider is registered for (model.ResolveGrants),
	// and the run would never dispatch.
	providers = append(providers, githubtoken.Providers(githubTokens, githubtoken.Config{
		Credentials: githubCredentials,
		// What a check holds the token against beyond "is it alive":
		// the repos this deployment's teams own, read per check rather
		// than here, since teams change while the daemon runs and a
		// registry is built once and kept. (Target repos moved from the
		// deployment to its teams in a5c329900; this was
		// cfg.targetRepos until then.)
		Repos:        teamRepos(store),
		Host:         cfg.GithubHost,
		InsecureHTTP: cfg.GithubInsecureHTTP,
	})...)
	return providers
}

// teamRepos is githubtoken.Config.Repos over store: the deployment's
// TargetRepos (model.Config), without duplicates, read afresh on each
// call. A nil store (a caller with no database behind it) owns no repos,
// which leaves a check reporting liveness alone. Entries that do not
// parse are left in for the check to skip, as it skips them anyway.
func teamRepos(store *model.Store) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		if store == nil {
			return nil, nil
		}
		targets, err := store.TargetRepos(ctx)
		if err != nil {
			return nil, err
		}
		var out []string
		var seen []model.RepoRef
		for _, entry := range targets {
			if repo, err := model.ParseRepo(entry); err == nil {
				if slices.ContainsFunc(seen, repo.Same) {
					continue
				}
				seen = append(seen, repo)
			}
			out = append(out, entry)
		}
		return out, nil
	}
}

// missingCapabilityProviderConfig is the deployment configuration that
// gates the two GCP providers above. Keep the diagnosis for an absent
// provider on this same predicate: if registration gains or loses a
// prerequisite, a credential check must name the same prerequisite.
func missingCapabilityProviderConfig(cfg Config, id string) []string {
	var missing []string
	if id == "gcp-key" || id == "gemini-key" {
		if cfg.GcpProject == "" {
			missing = append(missing, "GCP project ID")
		}
	}
	if id == "gcp-key" && cfg.GcpServiceAccountEmail == "" {
		missing = append(missing, "agent service-account email")
	}
	return missing
}

// gitHubCredentialLadder loads dataDir's GitHub credential ladder -- the
// names capabilityProviders above turns into providers and startUIServer
// into picker rows (ExtraNames), the credentials the Settings pane
// writes, and the material a check for one of them authenticates with.
//
// A second load of the same ladder (runDaemon's own git proxy and its
// GitHub REST client share one more, cheap and not hot-reloaded either):
// run() needs this before the reconcile loop exists, and one reading
// shared by everything above that loop is what keeps the picker, the
// registry, the pane and the check all talking about the same files.
func GitHubCredentialLadder(dataDir string) (*gitproxy.CredentialSet, error) {
	credentials, err := gitproxy.LoadCredentialSet(filepath.Join(dataDir, "secrets", "github"))
	if err != nil {
		return nil, fmt.Errorf("loading GitHub credential ladder for named-token capabilities: %w", err)
	}
	return credentials, nil
}

// defaultSourceDir is where the deployment image carries the source it
// was built from (Dockerfile's own COPY --from=build /src-export). Keep
// the two in step.
const defaultSourceDir = "/usr/local/share/grain/src"

// defaultCoreSourceDir is where an image whose own module builds on
// core, rather than being core, carries core's source at the version it
// was built against, for src://core/... (namespace.CoreSource). Core's
// own image has none: its src/ already is core.
const defaultCoreSourceDir = "/usr/local/share/grain/core-src"

// sourceDir is the tree the self-debug capability reads.
//
// The image's own copy wins over anything a flag names, which is the
// opposite of the usual precedence and is the whole point: read_grain_source
// answers "what is the binary I am running made of", and only the tree
// baked in beside that binary is guaranteed to be the commit it was built
// from. The flag names a host checkout that tracks a *branch* -- an
// upgrade repoints the image without touching it, so it drifts, and a
// rollback leaves it strictly newer than the code actually running.
//
// The flag remains the answer for a deployment that is not a container:
// a source-built install has no /usr/local/share/grain/src, and its
// checkout genuinely is what it is running.
func sourceDir(flagged string) string {
	if info, err := os.Stat(defaultSourceDir); err == nil && info.IsDir() {
		return defaultSourceDir
	}
	return flagged
}

// grantTools wires selfdebug/selfrepair's own tool-building functions
// into orchestrator.Config.GrantTools -- see that field's own doc
// comment for why this indirection exists instead of a method on
// model.CapabilityProvider. srcDir is whatever sourceDir resolved: the
// image's baked-in copy of grain's own source, or the checkout
// -upgrade-src-dir names on a deployment that has no such image.
func grantTools(srcDir, secretsDir string) map[string]func(store *model.Store, taskID string) []mcp.Tool {
	return map[string]func(store *model.Store, taskID string) []mcp.Tool{
		// Built per run rather than once here, so that a secret set
		// since this process started is redacted by the next run that
		// reads the journal rather than by the next restart.
		grainlog.CapabilityName: func(_ *model.Store, taskID string) []mcp.Tool {
			return grainlog.Tools(grainlog.Config{
				TaskID:   taskID,
				Redactor: grainLogRedactor(secretsDir),
			})
		},
		grainserver.JournalGrant: func(_ *model.Store, taskID string) []mcp.Tool {
			return grainlog.Tools(grainlog.Config{
				TaskID:   taskID,
				Redactor: grainLogRedactor(secretsDir),
			})
		},
		selfrepair.CapabilityName: func(store *model.Store, taskID string) []mcp.Tool {
			return selfrepair.HostCommandTools(store, taskID, 0, 0)
		},
		bootstrap.CapabilityName: func(*model.Store, string) []mcp.Tool {
			return bootstrap.PlaybookTools()
		},
	}
}

// credentialTokenSource adapts gitproxy's own owner/repo credential
// ladder into a github.TokenSource, so the REST client polling issues and
// pull requests and the git proxy pushing to it authenticate off the one
// ladder an operator configures under secrets/github/, rather than a
// second copy of the same decision.
//
// teamCredential, when set, comes first: the credential of the team that
// owns owner/repo (repoTeamCredential). A team's work is pushed with its
// own token (model.Store.TaskGitCredential, through the git proxy), so
// the pull request opened for that push, and the comments and merges
// that follow it, have to be posted with the same token -- otherwise
// they go out as whichever account the deployment's ladder names, which
// is not the identity the team set in Team settings, Git identity. A
// repository no custom team owns is the default team's and posted with
// its connection; one whose team has no credential this deployment holds
// falls back to the ladder exactly as before.
type credentialTokenSource struct {
	credentials    *gitproxy.CredentialSet
	teamCredential func(host, owner, repo string) (name string, ok bool)
	// hostOf, when set, is the host a repository named by a bare owner
	// is on (storeRepoHosts), or "" for the default host: what lets a
	// caller that dropped a RepoRef's host still be authenticated with
	// the credential for the host forgeRouter sends it to.
	hostOf func(owner, repo string) string
}

// ref is owner/repo as a RepoRef: host-qualified as stored
// (model.RepoFromStored), or resolved through hostOf when it is bare.
func (c credentialTokenSource) ref(owner, repo string) model.RepoRef {
	ref := model.RepoFromStored(owner, repo)
	if ref.Host == "" && c.hostOf != nil {
		ref.Host = c.hostOf(owner, repo)
	}
	return ref
}

// selectCredential is the credential a REST call against owner/repo
// authenticates as: the owning team's, otherwise the ladder's answer.
//
// An SSH key reaches git and nothing else, so a REST call for a
// repository cloned with one goes out under the PAT or App paired with it
// (gitproxy.CredentialSet.APICredential), or with no token at all when
// none is -- GitOnly then says why, before a pull request is attempted.
func (c credentialTokenSource) selectCredential(owner, repo string) (gitproxy.Credential, bool) {
	cred, ok := c.selectGitCredential(owner, repo)
	if ok && cred.SSH != nil {
		api, _ := c.credentials.APICredential(cred)
		return api, true
	}
	return cred, ok
}

// GitOnly is gitproxy.GitOnlyReason for owner/repo's credential, or a
// sentence explaining that owner/repo is on a plain git host: the
// sentence a pull request there is refused with, or "".
func (c credentialTokenSource) GitOnly(owner, repo string) string {
	cred, ok := c.selectGitCredential(owner, repo)
	if ok {
		credHost := c.credentials.HostOf(cred.Name)
		if _, _, hasAPI := credHost.APIBase(); !hasAPI {
			return fmt.Sprintf("repository %s/%s is on %s, a plain git host with no forge API or pull requests", owner, repo, credHost.Host)
		}
	} else {
		ref := c.ref(owner, repo)
		host := ref.GitHost()
		credHost := c.credentials.HostOf(host)
		if _, _, hasAPI := credHost.APIBase(); !hasAPI {
			return fmt.Sprintf("repository %s/%s is on %s, a plain git host with no forge API or pull requests", owner, repo, credHost.Host)
		}
	}
	if !ok || cred.SSH == nil {
		return ""
	}
	if _, paired := c.credentials.APICredential(cred); paired {
		return ""
	}
	return gitproxy.GitOnlyReason(cred, owner, repo)
}

// GitBranchExists is github.GitBranchSource: owner/repo's branch read
// over git (gitproxy.RemoteBranchExists), with the credential and the
// transport the git proxy pushes it with, when owner/repo's host has no
// forge API -- a plain git host, where asking the GitHub API for owner/repo
// asks about some other repository altogether.
func (c credentialTokenSource) GitBranchExists(owner, repo, branch string) (bool, bool, error) {
	if c.credentials == nil {
		return false, false, nil
	}
	ref := c.ref(owner, repo)
	cred, found := c.selectGitCredential(owner, repo)
	var host gitproxy.CredentialHost
	if found {
		host = c.credentials.HostOf(cred.Name)
	} else {
		host = c.credentials.HostOf(ref.GitHost())
		cred = gitproxy.Credential{Name: "anonymous"}
	}
	if _, _, hasAPI := host.APIBase(); hasAPI {
		return false, false, nil
	}
	exists, err := gitproxy.RemoteBranchExists(host, cred, ref.Owner, ref.Name, branch)
	return exists, true, err
}

// GitBranches is GitBranchExists's listing: every branch of owner/repo
// read over git (gitproxy.RemoteBranches) when its host has no forge API,
// and ok false -- ask the API -- when it has one.
func (c credentialTokenSource) GitBranches(owner, repo string) ([]string, bool, error) {
	if c.credentials == nil {
		return nil, false, nil
	}
	ref := c.ref(owner, repo)
	cred, found := c.selectGitCredential(owner, repo)
	var host gitproxy.CredentialHost
	if found {
		host = c.credentials.HostOf(cred.Name)
	} else {
		host = c.credentials.HostOf(ref.GitHost())
		cred = gitproxy.Credential{Name: "anonymous"}
	}
	if _, _, hasAPI := host.APIBase(); hasAPI {
		return nil, false, nil
	}
	names, err := gitproxy.RemoteBranches(host, cred, ref.Owner, ref.Name)
	return names, true, err
}

// selectGitCredential is the credential owner/repo is cloned and pushed
// with: the deployment's git connection for it (teamCredential), or none.
// The credentials.json ladder is not asked (docs/single-tenant-reset.md,
// stage 1).
func (c credentialTokenSource) selectGitCredential(owner, repo string) (gitproxy.Credential, bool) {
	ref := c.ref(owner, repo)
	host := ref.GitHost()
	if c.teamCredential == nil || c.credentials == nil {
		return gitproxy.Credential{}, false
	}
	name, ok := c.teamCredential(host, ref.Owner, ref.Name)
	if !ok {
		return gitproxy.Credential{}, false
	}
	return c.credentials.GetForRepoOn(name, host, ref.Owner, ref.Name)
}

// HasCredential mirrors TokenFor's choice without exposing the token. An
// anonymous credential still counts as configured; a missing ladder entry does
// not, which lets optional UI reads fail closed instead of spending an
// unauthenticated request on a private repository.
func (c credentialTokenSource) HasCredential(owner, repo string) bool {
	_, ok := c.selectCredential(owner, repo)
	return ok
}

// TokenRefused is github.TokenRefusedSource: a 401 on a token an ambient
// credential handed out drops it, so the next call asks the host's login
// again (gitproxy.CredentialSet.RefusedToken).
func (c credentialTokenSource) TokenRefused(token string) {
	if c.credentials != nil {
		c.credentials.RefusedToken(token)
	}
}

func (c credentialTokenSource) TokenFor(owner, repo string) *string {
	cred, ok := c.selectCredential(owner, repo)
	if !ok {
		return nil
	}
	return cred.Token
}

// repoTeamCredentialTTL is how long repoTeamCredential trusts one
// answer: long enough that the reconcile loop's many calls against one
// repository cost a store read rather than one each, short enough that a
// token a team admin has just set is posted with within the minute.
const repoTeamCredentialTTL = 30 * time.Second

func getRepoTeamCredentialTTL() time.Duration {
	return repoTeamCredentialTTL
}

func getRepoTeamCredentialNow() time.Time {
	return time.Now()
}

// repoTeamCredential is credentialTokenSource.teamCredential over store:
// the credential name the team owning owner/repo clones and pushes it
// with (model.Store.TeamGitCredentialOn). The owning team is the first
// custom team that targets the repository (model.Store.RepoTeams), and otherwise the default team,
// whose repositories are every one no custom team claims -- its work is
// pushed with its own connection like any other team's (there is no
// deployment-wide fallback past the team, grain/task-1592), so the pull
// request opened for that push has to be posted with the same one. A
// host-mode install whose only credential is the default team's ambient
// connection has no ladder at all, and without this rung every REST call
// for its repositories went out unauthenticated and read a private
// repository as a 404. false when the owning team has no credential,
// which leaves the call on the deployment's ladder; a store error is
// logged and answered the same way, so a failed read degrades to the old
// behaviour rather than to an unauthenticated request.
func repoTeamCredential(store *model.Store) func(host, owner, repo string) (string, bool) {
	if store == nil {
		return nil
	}
	type entry struct {
		name string
		ok   bool
		at   time.Time
	}
	var (
		mu    sync.Mutex
		cache = map[string]entry{}
	)
	return func(host, owner, repo string) (string, bool) {
		key := strings.ToLower(host + "/" + owner + "/" + repo)
		now := getRepoTeamCredentialNow()
		ttl := getRepoTeamCredentialTTL()
		mu.Lock()
		if e, hit := cache[key]; hit && now.Sub(e.at) < ttl {
			mu.Unlock()
			return e.name, e.ok
		}
		mu.Unlock()

		name, ok := lookupRepoTeamCredential(store, host, owner, repo)
		mu.Lock()
		cache[key] = entry{name: name, ok: ok, at: now}
		mu.Unlock()
		return name, ok
	}
}

func lookupRepoTeamCredential(store *model.Store, host, owner, repo string) (string, bool) {
	if owner == "" || repo == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref := model.RepoRef{Host: host, Owner: owner, Name: repo}
	name, err := store.GitConnectionOn(ctx, host, owner, repo)
	if errors.Is(err, model.ErrNoTeamGitCredential) {
		return "", false
	}
	if err != nil {
		log.Printf("grain daemon: finding the deployment's git connection for %s: %v", ref, err)
		return "", false
	}
	return name, name != ""
}

// restClientHost is the git host a REST client built for githubHost (the
// daemon's -github-host) reaches: githubHost normalized, or github.com.
func restClientHost(githubHost string) string {
	if host, err := model.ResolveGitHost(githubHost, ""); err == nil && host != "" {
		return host
	}
	return model.DefaultGitHost
}

// storeGitConnections is a gitproxy.ConnectionResolver over the
// deployment's git connections on the default host.
func storeGitConnections(ctx context.Context, store *model.Store) gitproxy.ConnectionResolver {
	return func(owner, repo string) (string, bool) {
		ref := model.RepoFromStored(owner, repo)
		name, err := store.GitConnectionOn(ctx, ref.Host, ref.Owner, ref.Name)
		if err != nil || name == "" {
			return "", false
		}
		return name, true
	}
}

// CIAPI is this process's CI API listener (pkg/capability/ciapi): the
// token registry a CI job's api grants (release-gate-read among them)
// mint into and the listener its jobs call grain's API through. A
// package-level value for the reason ModelProxies is one -- the
// capability registry is rebuilt from live configuration, and every
// build has to mint into the one registry being served.
var CIAPI = ciapi.NewEndpoint()
