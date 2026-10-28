// Package console is the `console` capability: the switch that says a
// task is driven by hand rather than run unattended.
//
// It is a GRANT provider in grainci's own shape -- it mints nothing,
// places nothing and writes no prompt section, so every method but Spec
// is model.BaseCapability's default. What it grants is not material in a
// sandbox: it is that this task's dispatch starts its agent CLI on a
// pseudo-terminal and leaves it there for somebody to type into
// (pkg/console), instead of handing it a prompt and reading the answer
// back.
//
// # Why a capability rather than a column
//
// Because "how is this task driven" needs to be sayable at exactly the
// granularities a capability is already sayable at, with no new column,
// no new pane and no new vocabulary: per task by ticking it in the
// picker when the task is filed (ui.OfferedCapabilities), and per repo
// or deployment-wide by listing it in DefaultCapabilities for somebody
// who wants every task in a repository to be one they sit in front of.
// The task model already carries an Interactive flag, and this is
// deliberately not it: that one marks a task filed as a live chat --
// dispatched ahead of the backlog and opened in the chat view -- and
// changes nothing about how its run is driven.
//
// # What it does not grant
//
// Anything the agent could not already do. A console session reaches the
// same sandbox through the same MCP server with the same tool allowlist
// an unattended run of the same task would get
// (agent.TerminalFramework's own doc comment): what changes is who is
// typing, not what the typing can reach.
package console

import (
	"github.com/bwsalmon/grain/pkg/model"
)

// CapabilityName is the grant id, and the string ui.OfferedCapabilities,
// model.Config.DefaultCapabilities and
// model.RepoConfig.DefaultCapabilities all name it by.
const CapabilityName = "console"

// Provider is the console capability. Spec is the whole of it -- see the
// package doc on why there is nothing to mint, place or revoke.
type Provider struct {
	model.BaseCapability
}

// New returns a console Provider, ready to register.
func New() *Provider { return &Provider{} }

// Spec implements model.CapabilityProvider.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Interactive console",
		Description: "Run this task's agent CLI on a terminal in the UI, and keep it running until somebody closes it",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Granted reports whether a task holds this capability -- the one
// question orchestrator asks before deciding how to drive its dispatch.
func Granted(grants []model.Grant) bool {
	for _, g := range grants {
		if g.Capability == CapabilityName {
			return true
		}
	}
	return false
}
