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
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/release"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func newCapabilityRegistryWithStore(store *model.Store, providers ...model.CapabilityProvider) *model.CapabilityRegistry {
	reg := model.NewCapabilityRegistry(providers...)
	reg.RegisterDynamic(ui.AgentAPIDynamicProvider)
	// A CI job's api grants are a token for CIAPI rather than routes an
	// agent may call (pkg/capability/ciapi), offered to the repositories
	// grain_config.ci_api_repos lists. Read here, from the store, because
	// grain CI builds a registry for every job it admits (ciProviders in
	// run.go): a changed setting reaches the next job without a restart.
	reg.RegisterCIDynamic(ciapi.DynamicProvider(CIAPI, ciapi.Config{Repos: ciAPIRepos(store)}))
	return reg
}

// ciAPIRepos is the repositories whose trusted CI jobs may hold api
// grants: grain's own (model.GrainSourceRepo), always, and whatever
// grain_config.ci_api_repos adds to it. Always rather than only when
// the list is empty, so that an operator offering the API to one more
// repository does not take it away from grain's own release train
// (release-gate-read) by not repeating it. nil -- grain's own only
// (ciapi.Config.Repos) -- when there is no store, no config row, or
// nothing listed. An entry that is not a repository is logged and left
// out rather than failing every job's resolution: leaving it out only
// ever offers less.
func ciAPIRepos(store *model.Store) []model.RepoRef {
	if store == nil {
		return nil
	}
	cfg, err := store.GetConfig(context.Background())
	if err != nil {
		log.Printf("grain daemon: reading grain_config.ci_api_repos: %v; offering grain's API to %s's CI jobs only", err, model.GrainSourceRepo())
		return nil
	}
	if cfg == nil {
		return nil
	}
	var repos []model.RepoRef
	for _, entry := range cfg.CIAPIRepos {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		repo, err := model.ParseRepo(entry)
		if err != nil {
			log.Printf("grain daemon: grain_config.ci_api_repos: ignoring %q: %v", entry, err)
			continue
		}
		repos = append(repos, repo)
	}
	if len(repos) == 0 {
		return nil
	}
	return append([]model.RepoRef{model.GrainSourceRepo()}, repos...)
}

// capabilityProviders builds every capability provider this deployment
// has enough configuration for. No provider is required: a task that
// grants a capability against a registry that never registered one is
// refused, cleanly, by model.ResolveGrants -- not a crash at startup.
//
// proxies and githubTokens are unused since the claude-, openai- and
// gemini-proxy providers were retired onto the in-process model-proxy server
// (modelProxyMethod) and github-credential:<name> was retired onto the
// in-process repo server (grainserver.RepoCredentialRef, schema migration 120);
// the parameters stay for the callers that pass them.
func CapabilityProviders(cfg Config, _ *model.Store, _ map[string]*modelproxy.Endpoint, _ []string, githubCredentials githubCredentialLadder) []model.CapabilityProvider {
	var providers []model.CapabilityProvider
	// gemini-key is no longer a provider here: it was retired onto the
	// gcp server's gemini-key (schema migration 103), which mints with
	// the same gcp-key-minter secret and reaps keys older than 24h.
	// github-sandbox is no longer a provider here: it was retired onto
	// the in-process github server's sandbox-repo method (schema migration
	// 124), which mints and revokes through githubsandbox.Current() and
	// reaps leaked grain-sandbox-* repositories in ReapCapabilitiesBefore.
	githubsandbox.Configure(githubsandbox.Config{
		Host: cfg.GithubHost, InsecureHTTP: cfg.GithubInsecureHTTP,
	})
	// bootstrap-playbooks is no longer a provider here: it was retired onto
	// the config-map server's config-map {"config_maps":["bootstrap"]}
	// (schema migration 118), which places the same embedded playbooks.
	// read-issues is no longer a provider here: it was retired onto the
	// in-process github server's read-issues method (schema migration 121).
	// console is no longer a provider here: it was retired onto the task's
	// hosting setting (schema migration 123).
	// image-signing, registry-push and package-publish are no longer
	// providers here: they were retired onto the in-process release
	// server (pkg/capability/release, schema migration 101), whose
	// methods place what they placed, on the events they answered. What
	// package-publish resolves is this deployment's live credential
	// ladder, so the server is handed it here, as the registry is built.
	//
	// The registry is left at packagepublish's default, ghcr.io, which
	// is what a GitHub credential publishes to on github.com. Its
	// refresh route is mounted on the CI API listener (CIAPI), so a job
	// whose release outlasts the ladder's hour-long token re-mints it
	// there rather than failing at its last push.
	if githubCredentials != nil {
		release.Configure(packagepublish.Config{
			Credentials: githubCredentials,
			Refresh:     PackagePublishRefresh,
			RefreshURL:  CIAPI.URL,
		})
	}
	// release-gate-read is no longer a provider here: it is the bare CI
	// alias for the https://grain.dev/capabilities/api#release-gate-read
	// preset, and a CI job's api grants, the alias among them
	// (ciapi.Aliases), resolve through ciapi.DynamicProvider registered
	// above (design/design/unified-grants.design.md §3, slice 4).
	// claude-proxy, openai-proxy and gemini-proxy are no longer
	// providers here: they were retired onto the in-process model-proxy
	// server (modelProxyMethod below, schema migration 100), which mints
	// the same token on the same endpoints with the run's connection.
	// github-credential:<name> is no longer a provider here either: it was
	// retired onto the in-process repo server's repo#credential (schema
	// migration 120).
	return providers
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

// grantTools wires grainlog/bootstrap's own tool-building functions
// into orchestrator.Config.GrantTools -- see that field's own doc
// comment for why this indirection exists instead of a method on
// model.CapabilityProvider. srcDir is whatever sourceDir resolved: the
// image's baked-in copy of grain's own source, or the checkout
// -upgrade-src-dir names on a deployment that has no such image.
//
// grain#host-exec's run_host_command is not here: it is served by the
// run's forked mcpserver, which confirms each command over the daemon
// (daemonHostExecConfirm), and an entry here would only build a second
// copy no framework reads.
func grantTools(srcDir, secretsDir string, secretStore *secrets.Store) map[string]func(store *model.Store, taskID string) []mcp.Tool {
	return map[string]func(store *model.Store, taskID string) []mcp.Tool{
		// Built per run rather than once here, so that a secret set
		// since this process started is redacted by the next run that
		// reads the journal rather than by the next restart.
		grainlog.CapabilityName: func(_ *model.Store, taskID string) []mcp.Tool {
			return grainlog.Tools(grainlog.Config{
				TaskID:   taskID,
				Redactor: grainLogRedactorFor(secretsDir, secretStore),
			})
		},
		grainserver.JournalGrant: func(_ *model.Store, taskID string) []mcp.Tool {
			return grainlog.Tools(grainlog.Config{
				TaskID:   taskID,
				Redactor: grainLogRedactorFor(secretsDir, secretStore),
			})
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
	// mergeCredential, when set, is the credential name owner/repo's pull
	// requests are merged with when that is not its push credential
	// (model.Store.GitMergeCredentialOn), or false.
	mergeCredential func(host, owner, repo string) (name string, ok bool)
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

// GitRefSHA resolves ref in owner/repo over smart-HTTP git when its host has
// no forge API, returning ok false when it has one so the caller asks the API.
func (c credentialTokenSource) GitRefSHA(owner, repo, rev string) (string, bool, error) {
	if c.credentials == nil {
		return "", false, nil
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
		return "", false, nil
	}
	sha, err := gitproxy.RemoteRefSHA(host, cred, ref.Owner, ref.Name, rev)
	return sha, true, err
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

// AppSlugFor is github.AppSlugSource: the slug of the GitHub App behind
// owner/repo's credential (gitproxy.CredentialSet.AppBotLogin), or ""
// when that credential is not an App's.
func (c credentialTokenSource) AppSlugFor(owner, repo string) (string, error) {
	if c.credentials == nil {
		return "", nil
	}
	cred, ok := c.selectCredential(owner, repo)
	if !ok {
		return "", nil
	}
	login, isApp, err := c.credentials.AppBotLogin(cred.Name)
	if err != nil || !isApp {
		return "", err
	}
	return strings.TrimSuffix(login, "[bot]"), nil
}

// selectMergeCredential is owner/repo's merge credential's name, and the
// credential itself when the store holds it (github.MergeTokenSource).
func (c credentialTokenSource) selectMergeCredential(owner, repo string) (string, gitproxy.Credential, bool) {
	if c.mergeCredential == nil {
		return "", gitproxy.Credential{}, false
	}
	ref := c.ref(owner, repo)
	host := ref.GitHost()
	name, ok := c.mergeCredential(host, ref.Owner, ref.Name)
	if !ok || name == "" {
		return "", gitproxy.Credential{}, false
	}
	if c.credentials == nil {
		return name, gitproxy.Credential{}, true
	}
	cred, _ := c.credentials.GetForRepoOn(name, host, ref.Owner, ref.Name)
	return name, cred, true
}

// MergeTokenFor is github.MergeTokenSource: the token owner/repo's merge
// credential gives, nil when it names one the store does not hold or
// cannot mint a token for -- which refuses the merge rather than making it
// under the push credential -- and nil too when the push credential's
// forge traffic goes through its host's own tool (MergeRefusalFor).
func (c credentialTokenSource) MergeTokenFor(owner, repo string) (*string, bool) {
	_, cred, ok := c.selectMergeCredential(owner, repo)
	if !ok {
		return nil, false
	}
	if c.mergeRefusal(owner, repo) != "" {
		return nil, true
	}
	return cred.Token, true
}

// MergeRefusalFor is github.MergeRefusalSource: why owner/repo's merge
// credential gave no token for the merge it was asked for, or "" to let
// the generic ErrNoMergeToken stand.
func (c credentialTokenSource) MergeRefusalFor(owner, repo string) string {
	if _, _, ok := c.selectMergeCredential(owner, repo); !ok {
		return ""
	}
	return c.mergeRefusal(owner, repo)
}

// mergeRefusal is a sentence when owner/repo's push credential sends its
// forge traffic through its host's tool (`<tool> api`): forgeRouter picks
// the transport by the push credential, and that transport drops the
// request's Authorization header and authenticates with the tool's own
// login, so a merge sent with the merge credential's token would be made
// as that login -- the person's -- and recorded as the merge credential's.
func (c credentialTokenSource) mergeRefusal(owner, repo string) string {
	if c.credentials == nil {
		return ""
	}
	push, ok := c.selectGitCredential(owner, repo)
	if !ok || !c.credentials.ForgeViaCommand(push.Name) {
		return ""
	}
	return fmt.Sprintf("%s/%s is pushed with %s, whose forge calls go through its host's own tool as that tool's login, "+
		"which a merge credential cannot replace", owner, repo, push.Name)
}

// MergeIdentityFor is github.MergeTokenSource: the merge credential's
// name, with the App's bot login when it is an App's.
func (c credentialTokenSource) MergeIdentityFor(owner, repo string) string {
	name, _, ok := c.selectMergeCredential(owner, repo)
	if !ok {
		return ""
	}
	if c.credentials != nil {
		if login, isApp, err := c.credentials.AppBotLogin(name); err == nil && isApp && login != "" {
			return fmt.Sprintf("merge credential %s (%s)", name, login)
		}
	}
	return "merge credential " + name
}

func (c credentialTokenSource) TokenFor(owner, repo string) *string {
	cred, ok := c.selectCredential(owner, repo)
	if !ok {
		return nil
	}
	return cred.Token
}

// AuthorSignature is github.AuthorSignatureSource: the human user whose
// delegated credential authenticates REST calls against owner/repo, so new
// pull requests and comments are signed with their name via Grain.
func (c credentialTokenSource) AuthorSignature(owner, repo string) string {
	if c.credentials == nil {
		return ""
	}
	cred, ok := c.selectCredential(owner, repo)
	if !ok {
		return ""
	}
	return c.credentials.AuthorOf(cred.Name)
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

// repoMergeCredential is credentialTokenSource.mergeCredential over
// store (model.Store.GitMergeCredentialOn), trusting one answer for
// repoTeamCredentialTTL as repoTeamCredential does. A store error is
// logged and answered as no merge credential, which leaves the merge on
// the push credential exactly as before the setting existed.
func repoMergeCredential(store *model.Store) func(host, owner, repo string) (string, bool) {
	if store == nil {
		return nil
	}
	type entry struct {
		name string
		at   time.Time
	}
	var (
		mu    sync.Mutex
		cache = map[string]entry{}
	)
	return func(host, owner, repo string) (string, bool) {
		key := strings.ToLower(host + "/" + owner + "/" + repo)
		now := getRepoTeamCredentialNow()
		mu.Lock()
		if e, hit := cache[key]; hit && now.Sub(e.at) < getRepoTeamCredentialTTL() {
			mu.Unlock()
			return e.name, e.name != ""
		}
		mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		name, err := store.GitMergeCredentialOn(ctx, host, owner, repo)
		if err != nil {
			log.Printf("grain daemon: finding the merge credential for %s/%s: %v", owner, repo, err)
			return "", false
		}
		mu.Lock()
		cache[key] = entry{name: name, at: now}
		mu.Unlock()
		return name, name != ""
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

// PackagePublishRefresh is the route a CI job holding package-publish
// re-mints its registry login through, mounted on CIAPI's listener: a
// package-level value for the reason CIAPI is one.
var PackagePublishRefresh = packagepublish.NewRefresher()

func init() { CIAPI.Mount(packagepublish.RefreshRoute, PackagePublishRefresh) }
