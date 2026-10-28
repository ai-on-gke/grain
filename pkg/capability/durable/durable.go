// Package durable is the durable capability
// (docs/design/durable-tasks.md#DT-1): a task holding it keeps going
// until its work is done rather than until a clock runs out
// (model.Task.IsDurable). A run of it has no wall-clock deadline and no
// wait-credit cap; an idle watchdog suspends it instead, and it is
// resumed on the next cycle. It is dispatched with priority (durable
// implies priority-dispatch) under the deployment's own ceiling on durable
// runs rather than max_workers, and a setup failure suspends it rather
// than counting against it.
//
// Like priority-dispatch, the provider places nothing and mints nothing:
// the grant is read by dispatch, the store and the orchestrator. It exists
// so the grant is offered, listed and audited like any other capability.
//
// Only a person may grant it (model.IsPersonOnlyGrant).
package durable

import "github.com/ai-on-gke/grain/pkg/model"

// CapabilityName is the grant name: "durable".
const CapabilityName = model.DurableCapability

// Description is the sentence an operator reads before granting it.
const Description = "Let this task's runs keep going until the work is done: no wall-clock deadline and no " +
	"wait-credit cap, suspended and resumed if a run goes idle for an hour, and never failed by a sandbox " +
	"setup error. Implies priority dispatch, under the deployment's own ceiling on durable runs. A durable " +
	"run holds a sandbox for as long as it lasts. Only a person may grant it"

// Provider implements model.CapabilityProvider for durable. Every method
// but Spec is model.BaseCapability's default.
type Provider struct {
	model.BaseCapability
}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// Spec is the capability's identity.
func (Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Durable",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: Description,
	}
}
