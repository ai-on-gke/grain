// Package capabilitytest registers a made-up capability provider, so that
// core packages can test what they do with a capability contributed from
// outside core (capability.RegisterProvider) without knowing any real
// environment's.
package capabilitytest

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ID, Name and Description are the made-up capability's.
const (
	ID          = "made-up-research"
	Name        = "Made-up research"
	Description = "Let this task look things up in a made-up research service"
)

// Provider is the made-up capability: a grant with nothing to mint.
type Provider struct {
	model.BaseCapability
}

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        ID,
		Label:       Name,
		Description: Description,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Register registers the made-up capability, as a run grant, until t
// ends. A test that calls it must not be parallel: every listing of
// grain's capabilities includes it while it is registered, so a parallel
// test asserting on one of those listings would see it come and go.
func Register(t testing.TB) {
	t.Helper()
	t.Cleanup(capability.RegisterProvider(capability.Registration{
		ID:          ID,
		Name:        Name,
		Description: Description,
		New: func() model.CapabilityProvider {
			return &Provider{}
		},
		RunGrant: true,
	}))
}
