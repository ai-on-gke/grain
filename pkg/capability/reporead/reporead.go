// Package reporead implements the parameterized CI capability that grants
// a CI sandbox read-only git proxy access to a specific repository
// (repo-read:<owner>/<repo>).
package reporead

import (
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Provider is the capability provider for one repository's read access in CI.
type Provider struct {
	model.BaseCapability
	repo model.RepoRef
}

// New returns a provider for repo.
func New(repo model.RepoRef) *Provider {
	return &Provider{repo: repo}
}

// Repo returns the repository this provider grants read access to.
func (p *Provider) Repo() model.RepoRef {
	return p.repo
}

// Spec describes this provider's capability.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        model.RepoReadCapability(p.repo),
		Label:       "grain-repo-read-" + p.repo.String(),
		Description: fmt.Sprintf("Grant CI read-only fetch access to %s via the git proxy", p.repo),
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionPlace,
		CIOnly:      true,
	}
}

// DynamicProvider resolves repo-read:<owner>/<repo> capability IDs dynamically.
func DynamicProvider(name string) (model.CapabilityProvider, bool) {
	repo, ok := model.RepoReadTarget(name)
	if !ok {
		return nil, false
	}
	return New(repo), true
}
