// Package quarantine implements the test-quarantine capability, under which a run
// can quarantine failing or flaky tests in .grain/quarantine.yml via the quarantine_test MCP tool
// and file follow-up repair tasks.
package quarantine

import "github.com/bwsalmon/grain/pkg/model"

// CapabilityName is the grant name ui.OfferedCapabilities lists as "test-quarantine".
const CapabilityName = "test-quarantine"

// Provider is the test quarantine capability. Every method but Spec is
// model.BaseCapability's default: there is no credential to withhold, no
// lease to mint, nothing to place and nothing to revoke.
type Provider struct {
	model.BaseCapability
}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// Spec is the capability's identity.
func (p Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Test quarantine",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: "Quarantine flaky or failing tests and file follow-up repair tasks",
	}
}
