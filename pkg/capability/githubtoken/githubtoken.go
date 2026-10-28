// Package githubtoken is the capability behind each named GitHub token a
// deployment has configured beyond its default one (grain/task-117): a
// task granted one pushes and pulls through that token instead of the
// one its repo would otherwise fall back to.
//
// **One provider per token, not one provider with a name argument in the
// grant.** A grant is a capability id and nothing else (model.Grant), so
// "use the release-bot token" has to *be* an id -- model.
// GitCredentialCapability's "github-credential:<name>" -- rather than a
// parameter carried alongside one. That is what lets the whole existing
// machinery work unchanged: the picker offers a row per token
// (ui.GitHubTokenCapabilities), a human ticks one, and the id lands in
// task_grant like any other. Providers builds the matching set of
// providers from the same names, so a granted token is one
// model.ResolveGrants finds a provider for rather than refusing as
// unregistered.
//
// **A SELECT capability: it mints nothing and places nothing.** The
// token never enters the sandbox -- pkg/gitproxy's whole shape is that a
// run's route to GitHub is grain's, never the agent's -- so there is
// nothing here to materialize into a file and nothing to revoke.
// What actually takes effect is the proxy asking the store, per request,
// which credential this sandbox's live task named
// (model.Store.GitCredentialOverride -> gitproxy.GitProxy.
// selectCredential), and that reads the grant itself. Materialize
// records the choice as Materialization.CredentialOverride all the same,
// which is the field model/capability.go defines for exactly this kind
// of capability, so a dispatch's own record of what it applied says
// which token the run was routed through.
//
// **Which tokens exist is an operator's file, not a setting.** The
// names come from the credential ladder's own directory
// (gitproxy.CredentialSet.ExtraNames -- a <name>.token or
// <name>.app.json under secrets/github), the same place the default
// token lives, and are read once at startup like the rest of that
// ladder: "replace a file under /data/secrets and restart the one
// service that reads it" (docs/design.md). Adding a token is therefore
// adding a file; what this package adds is that the file is then
// something a human can attach to one task without editing
// credentials.json and changing every repo's default at once.
//
// **One thing here does reach GitHub: the credential check** --
// CheckCredential (check.go), the "test this credential" action Settings
// offers beside every other standing credential. Nothing about a
// dispatch changed for it: the check is handed the same ladder the proxy
// resolves through and asks GitHub what it still accepts, rather than
// this provider growing a client or a token of its own. See check.go's
// own doc comment.
package githubtoken

import (
	"context"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultHost is Config.Host's default, the same "github.com" default
// pkg/cli/daemon.go's -github-host flag and githubsandbox.DefaultHost
// use -- restated here because this package is constructed
// independently of that flag in tests.
const DefaultHost = "github.com"

// credentialDir is where these credentials live, relative to a
// deployment's data directory -- named in what a failed check says,
// never opened here (this package reads no files; Config.Credentials
// does).
const credentialDir = "secrets/github"

// CredentialFile is the file backing the credential named name, in
// either form the ladder keeps one in -- what a check names as the
// credential it authenticated as (model.CredentialCheck.Credentials) and
// as the thing to replace when GitHub refuses it. A path, and it says
// nothing about what is in it.
func CredentialFile(name string, app bool) string {
	if app {
		return credentialDir + "/" + name + ".app.json"
	}
	return credentialDir + "/" + name + ".token"
}

// Credential is one named credential's current material, as the ladder
// resolves it right now: the token a push through this credential would
// carry this minute, and which of the two forms it came from.
//
// Token is empty for a credential the ladder serves anonymously -- an
// empty <name>.token, or a <name>.app.json whose installation token
// could not be minted, both of which gitproxy.CredentialSet.load
// deliberately fails soft to. That is a real, checkable state, not an
// error: every push through it goes out unauthenticated.
type Credential struct {
	Token string
	// App reports whether this is a <name>.app.json credential -- a
	// GitHub App installation whose token is re-minted rather than a
	// token read as-is. It changes only what a refusal tells an
	// operator to go and replace, since the two forms are fixed in
	// different places.
	App bool
	// MintError is why an App credential has no token right now, in
	// GitHub's own words where it gave any (gitproxy.MintFailure.
	// Describe). Empty for a token read as-is, and for an App credential
	// whose last mint worked.
	//
	// Without it the check could say only that this credential was being
	// served anonymously and that the daemon's log knew why, which is a
	// remedy that needs shell access to the host to carry out -- the one
	// thing a check pressed from Settings exists to avoid.
	MintError string
}

// CredentialSource resolves a named GitHub credential to the material it
// currently authenticates with -- this deployment's own credential
// ladder (gitproxy.CredentialSet, behind pkg/cli/daemon.go's adapter),
// which is the one thing that already knows both forms and does the App
// re-minting.
//
// Deliberately not the ladder's own type: this package needs one
// question answered about one name, and taking the whole of pkg/gitproxy
// to ask it would tie a capability provider to the proxy's internals.
// The second return is false when no credential of that name is
// configured at all.
type CredentialSource interface {
	GitHubCredential(name string) (Credential, bool)
}

// Config is what a named-token provider needs beyond the name itself,
// all of it for CheckCredential and none of it for a dispatch: a
// dispatch records a choice and touches nothing (Materialize below).
type Config struct {
	// Credentials is where the check reads this token's material from.
	// nil means no check can be made here, which the check reports as
	// exactly that rather than failing the credential.
	Credentials CredentialSource
	// Repos is the repos this deployment's teams own (the union of every
	// team's TargetRepos) -- what the check holds the token against
	// beyond "is it alive", since a token that has lost access to the
	// repo it exists for fails in the same place a dead one does. A
	// function rather than a list because teams change while the daemon
	// runs and a registry outlives the teams it was built over: it is
	// called once per check, so the check reads the teams as they are
	// when somebody presses the button. Nil, or an empty answer (no team
	// names a repo), leaves the check reporting liveness alone; an error
	// fails the check with it, since the question asked could not be
	// put.
	Repos func(context.Context) ([]string, error)
	// Host is the GitHub host the check asks. Empty means DefaultHost.
	Host string
	// InsecureHTTP speaks plain HTTP to Host instead of HTTPS -- mock
	// servers only, mirroring github.RealTransport.UseTLS and
	// githubsandbox.Config's field of the same name.
	InsecureHTTP bool
}

// Provider is the capability for one named GitHub token.
//
// Resolve, Revoke and (below) most of the contract are
// model.BaseCapability's defaults: there is no credential to withhold at
// resolve time -- the token was named by an operator's own file, and a
// name with no file behind it is never offered in the first place -- and
// nothing minted to give back.
type Provider struct {
	model.BaseCapability
	name string
	cfg  Config
	// newTransport builds the transport CheckCredential's own calls go
	// out over. New sets this to the real one; tests substitute a
	// github.FakeTransport, the same seam githubsandbox.Provider.newClient
	// gives its own checks.
	newTransport func(host string, insecureHTTP bool) github.Transport
}

// New returns the provider for the credential named name --
// gitproxy.CredentialSet's own name for it, which is the file name under
// secrets/github with its .token/.app.json suffix removed -- configured
// with cfg, which only CheckCredential reads.
func New(name string, cfg Config) *Provider {
	return &Provider{name: name, cfg: cfg, newTransport: realTransport}
}

func realTransport(host string, insecureHTTP bool) github.Transport {
	rt := github.NewRealTransport(host)
	rt.UseTLS = !insecureHTTP
	return rt
}

func (p *Provider) host() string {
	if p.cfg.Host != "" {
		return p.cfg.Host
	}
	return DefaultHost
}

func (p *Provider) transport() github.Transport {
	build := p.newTransport
	if build == nil {
		build = realTransport
	}
	return build(p.host(), p.cfg.InsecureHTTP)
}

// Providers is one Provider per name, in the order given, each with the
// same cfg -- what pkg/cli/daemon.go registers alongside the
// capabilities grain ships unconditionally. An empty or duplicate name
// is skipped rather than registered: the first would be a capability id
// no human could tell apart from the prefix itself, and the second would
// silently replace the provider registered before it
// (model.CapabilityRegistry.Register).
func Providers(names []string, cfg Config) []model.CapabilityProvider {
	seen := make(map[string]bool, len(names))
	out := make([]model.CapabilityProvider, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, New(name, cfg))
	}
	return out
}

// Name is the credential this provider stands for.
func (p *Provider) Name() string { return p.name }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        model.GitCredentialCapability(p.name),
		Label:       "grain-github-" + p.name,
		Description: Description(p.name),
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionSelect,
	}
}

// Description is what a picker shows beside this capability, shared with
// ui.GitHubTokenCapabilities so the row a human ticks and the spec the
// daemon registers say the same thing about the same token.
func Description(name string) string {
	return fmt.Sprintf(
		"Push and pull through the %q GitHub token instead of this deployment's default one", name)
}

// Materialize routes this run's git traffic through the named credential
// -- see this package's doc comment on why that is a recorded choice
// rather than an effect applied here.
func (p *Provider) Materialize(context.Context, model.CapabilityContext) (model.Materialization, error) {
	return model.Materialization{CredentialOverride: &model.CredentialRef{Name: p.name}}, nil
}

// PromptSection tells the agent which token its pushes carry, since that
// is the difference it can actually observe: a push that the default
// token would have been refused for is one this run may be allowed to
// make, and the reverse is just as possible.
func (p *Provider) PromptSection(context.Context, model.CapabilityContext, []model.Placement) (string, error) {
	return fmt.Sprintf(
		"Git operations in this sandbox are authenticated with the %q GitHub token, which this "+
			"task was granted in place of the default one. You never hold the token itself: git "+
			"traffic goes through grain's proxy, which attaches it. If a push or fetch is refused, "+
			"it is that token's access that is missing, and saying so is more useful than retrying.",
		p.name), nil
}
