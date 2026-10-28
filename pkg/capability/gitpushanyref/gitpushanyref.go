// Package gitpushanyref is the team capability that lifts the git
// proxy's one-branch push rule for a run that holds it.
//
// Every other run may push only its own task's branch, plus whatever the
// repository-declared template its task was filed from lists under
// `push:` (gitproxy.ModelAuthorizer.AuthorizeRefUpdates, grain/task-2536).
// Standing work -- the nightly release cut
// (.grain/templates/release-candidate.yml) -- says what it pushes that
// way, in the repository. This grant is for the ad-hoc task with no such
// template behind it: a team grants it to one of its own tasks, the way
// it grants any other team capability, and the team's own git credential
// is still what every push goes out with. It is not admin-only: nothing
// it allows reaches past what that team's credential can already do.
// It provisions nothing in the sandbox: the proxy reads it off the live
// task (model.Store.GitPushAnyRef) when a push names another ref.
package gitpushanyref

import "github.com/bwsalmon/grain/pkg/model"

// CapabilityName is the grant name: "git-push-any-ref".
const CapabilityName = model.GitPushAnyRefCapability

// Provider implements model.CapabilityProvider for git-push-any-ref.
type Provider struct {
	model.BaseCapability
}

// New returns a git-push-any-ref Provider ready to register.
func New() *Provider { return &Provider{} }

// Spec describes this provider's capability.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:   CapabilityName,
		Label:  "Push any ref",
		Source: model.GrantByLabel,
		// Nothing is placed in the sandbox; the git proxy reads the grant.
		Provision: model.ProvisionGrant,
		Description: "Let this task's run push any branch or tag of the repositories it may write -- the default " +
			"branch, force-pushes and deletions included -- rather than only its own task branch, with the " +
			"team's own git credential. Standing jobs declare the refs they push under `push:` in their " +
			".grain template instead.",
	}
}
