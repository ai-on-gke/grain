// Package teambroadcast is a GRANT model.CapabilityProvider: a task
// holding it is given broadcast_team_message, which comments on every
// active task of the deployment -- un-parking the ones awaiting a reply and
// delivering the text into the live sessions of the ones running.
//
// A capability rather than something every run can do, because what it
// writes lands in other tasks' conversations, and every run that reads
// them is steered by it. Until grain/task-1568 the tool was on every
// run's roster with no grant, and the daemon asked nothing of the caller,
// so any run could broadcast into all of the running work.
//
// The daemon checks the grant again on the request
// (ui.Client.BroadcastTeamMessage, which is told which task is asking and
// checks that task's grants for itself). The tool keeps its old name,
// which the capability's tool allowlists list.
//
// It mints nothing and writes no Placement -- every method but Spec is
// model.BaseCapability's default -- and, like memory, it reaches a run
// only through `grain mcpserver -grant team-broadcast` with a route back
// to the daemon, since the comments it writes are rows in the daemon's
// store.
package teambroadcast

import "github.com/ai-on-gke/grain/pkg/model"

// CapabilityName is the grant name: "team-broadcast".
const CapabilityName = "team-broadcast"

// Description is the one sentence ui.OfferedCapabilities shows beside
// the checkbox.
const Description = "Let this task broadcast a message to every active task of its own team -- " +
	"un-parking tasks awaiting a reply and delivering the text into running sessions"

// Provider is the team-broadcast capability.
type Provider struct {
	model.BaseCapability
}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// Spec is the capability's identity: a GRANT with no credential of its
// own.
func (p Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Team broadcast",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: Description,
	}
}
