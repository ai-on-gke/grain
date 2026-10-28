// Package packagepublish provides the "package-publish" CI capability:
// publishing a repository's container images with the GitHub credential
// this deployment already uses for that repository, rather than with a
// registry token of its own.
//
// It is the same grant registry-push is -- an operator turns it on per
// repository, and a job may only receive it for a trusted post-merge
// release or an operator's own workflow_dispatch -- and differs in where
// the material comes from. registry-push reads one JSON secret somebody
// pasted: a second long-lived credential, rotated by hand, held against
// nothing, and belonging to whichever account happened to create it.
// This resolves the credential through the git proxy's own ladder
// (gitproxy.CredentialSet.PackageCredential), so a release publishes as
// the deployment does: an App-backed credential arrives as an
// installation token that expires within the hour, a rotation takes
// effect for pushes and publishes on the same day, and there is one
// fewer secret in the store than there was.
//
// What it still has in common with every other publishing credential is
// that the material reaches the sandbox. A container push speaks the
// registry protocol to ghcr.io directly -- there is no brokered route
// for it the way there is for git (pkg/gitproxy/packages.go says why) --
// so the job gets the token, and the event gate above is what keeps that
// to releases an operator has already trusted with the repository.
package packagepublish

import (
	"context"
	"errors"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// The spelling lives in pkg/model because decisions taken below the
	// capability layer read it: model.GuestCacheFollowsBaseBranch asks
	// whether an operator has already granted this to a repository's CI
	// before grain dispatches a job that would need it.
	CapabilityName = model.CapabilityPackagePublish

	RegistryPath = "/home/debian/.package-publish-registry"
	UsernamePath = "/home/debian/.package-publish-username"
	TokenPath    = "/home/debian/.package-publish-token"
)

// Credential is one registry login, in the shape this package needs it:
// gitproxy.PackageCredential's own fields, restated here for the reason
// githubtoken.Credential is -- a capability provider should not have to
// import the proxy to be handed what the proxy resolved, and the daemon
// that owns both adapts between them (pkg/cli/daemon.go).
type Credential struct {
	Registry string
	Username string
	Token    string
	// Name is which of this deployment's credentials answered. A name,
	// for a log line or a refusal; never material.
	Name string
}

// CredentialSource is the credential ladder, asked for one repository's
// publishing login. The error is the operator-facing reason a
// repository has none, and is repeated verbatim to whoever is looking at
// the failed job.
type CredentialSource interface {
	PackageCredential(registry, owner, repo string) (Credential, error)
}

// Config is what the provider needs beyond the grant itself.
type Config struct {
	// Credentials is the ladder. nil means this deployment cannot
	// resolve a publishing credential at all, which every grant is
	// refused with rather than being allowed to reach a job that would
	// find no files where it expects them.
	Credentials CredentialSource
	// Registry is the package registry these credentials publish to.
	// Empty means gitproxy.DefaultPackageRegistry -- ghcr.io -- which is
	// what a deployment against github.com wants; a GHES deployment
	// names its own.
	Registry string
}

type Provider struct {
	model.BaseCapability
	cfg Config
}

func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name: CapabilityName, Label: "package publish",
		Description: "Publish this repository's packages from release or manual CI, as this deployment's own GitHub credential for it",
		Source:      model.GrantByLabel, Provision: model.ProvisionPlace,
		// Granted to a repository's CI jobs and never to a task: what
		// decides whether this credential may enter a sandbox is the
		// event below, and a task is not an event
		// (model.CapabilitySpec.CIOnly).
		CIOnly: true,
	}
}

// SupportsCIEvent is registry-push's own gate, and for the same reason:
// the material is a credential that can write to this repository's
// packages, so it goes to a commit that is already on the destination
// branch, or to a run an operator asked for by hand -- never to a
// pull-request or branch push, whose workflow file and whose steps are
// whatever a contributor wrote.
func (*Provider) SupportsCIEvent(event string) bool {
	return event == "release" || event == "workflow_dispatch"
}

func (p *Provider) Resolve(ctx context.Context, cc model.CapabilityContext) (model.Resolution, error) {
	if _, err := p.credential(cc); err != nil {
		return model.RefusedBecause(err.Error()), nil
	}
	return model.Honoured(), nil
}

func (p *Provider) Materialize(ctx context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	c, err := p.credential(cc)
	if err != nil {
		return model.Materialization{}, err
	}
	return model.Materialization{Placements: []model.Placement{
		{Side: model.SideSandbox, Path: RegistryPath, Content: c.Registry},
		{Side: model.SideSandbox, Path: UsernamePath, Content: c.Username},
		{Side: model.SideSandbox, Path: TokenPath, Content: c.Token},
	}}, nil
}

// credential is the one lookup Resolve and Materialize share: which
// login publishes the packages of the repository this job is for.
//
// The repository is the job's own target rather than anything the
// workflow names, which is what makes this credential impossible to
// point at somebody else's packages: a job for bwsalmon/grain resolves
// bwsalmon/grain's credential, whatever its steps go on to ask of it.
func (p *Provider) credential(cc model.CapabilityContext) (Credential, error) {
	if p.cfg.Credentials == nil {
		return Credential{}, errors.New(
			"this deployment resolves no GitHub credentials, so there is nothing to publish packages as")
	}
	repo := cc.Task.Target
	if repo == nil || repo.Owner == "" || repo.Name == "" {
		// The whole of this credential is "publish *this repository's*
		// packages", so with no repository there is nothing to resolve
		// -- and the reader of this sentence is whoever is looking at
		// the failure it caused, who needs to be told where the working
		// setting is rather than only that this one did not work. The
		// ordinary route to that failure is closed as of grain/task-382
		// (a task cannot hold this at all, model.CapabilitySpec.CIOnly);
		// this stays the answer for a CI job dispatched without one.
		return Credential{}, errors.New(
			CapabilityName + " publishes the packages of one repository, and this run names no repository, " +
				"so there is no credential to resolve. It is a CI capability: enable it for a repository's " +
				"CI jobs on the repos page, under CI capabilities, where its release and manual-dispatch " +
				"gate applies -- it is not something a task can hold.")
	}
	c, err := p.cfg.Credentials.PackageCredential(p.cfg.Registry, repo.StoredOwner(), repo.Name)
	if err != nil {
		return Credential{}, err
	}
	if c.Registry == "" || c.Username == "" || c.Token == "" {
		return Credential{}, fmt.Errorf("the credential for %s is incomplete: a registry, a username and a token are all needed to log in", repo)
	}
	return c, nil
}
