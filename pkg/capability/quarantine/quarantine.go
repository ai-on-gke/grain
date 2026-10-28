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

// Package quarantine implements the test-quarantine capability, under which a run
// can quarantine failing or flaky tests in .grain/quarantine.yml via the quarantine_test MCP tool
// and file follow-up repair tasks.
package quarantine

import "github.com/ai-on-gke/grain/pkg/model"

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
