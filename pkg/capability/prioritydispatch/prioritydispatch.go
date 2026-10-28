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

// Package prioritydispatch is the priority-dispatch capability: a task
// holding it is dispatched the way a console session is
// (model.Task.HasPriorityDispatch). It is admitted outside the worker and
// merger counts, ordered ahead of mergers and ordinary work, passes a
// pipeline pause other than an upgrade's own hold, is never parked behind
// a red base branch's repair, and is not held by a provider connection's
// concurrency limit. It still charges the sandbox budget, under the
// exception policy a session's does.
//
// The provider places nothing and mints nothing: the grant is read by
// dispatch, the store and the agent pool, which is where the whole of
// its effect is. It exists so the grant is offered, listed and audited
// like any other capability.
//
// Only a person may grant it (model.IsPersonOnlyGrant), since it lets a
// task jump the queue; grain's own pipeline steward template carries it
// by construction.
package prioritydispatch

import "github.com/ai-on-gke/grain/pkg/model"

// CapabilityName is the grant name: "priority-dispatch".
const CapabilityName = model.PriorityDispatchCapability

// Description is the sentence an operator reads before granting it, kept
// here so the picker (ui.OfferedCapabilities) and the provider's own Spec
// cannot drift apart.
const Description = "Dispatch this task the way a console session is: started whenever it is ready, even " +
	"with every worker busy, ahead of merge-queue repairs and ordinary work, past a pipeline pause (but never " +
	"an upgrade's hold), never parked behind a red base branch's repair, and not held by a model connection's " +
	"concurrency limit. It still needs room in the sandbox budget. Only a person may grant it"

// Provider implements model.CapabilityProvider for priority-dispatch.
// Every method but Spec is model.BaseCapability's default.
type Provider struct {
	model.BaseCapability
}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// Spec is the capability's identity.
func (Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Priority dispatch",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: Description,
	}
}
