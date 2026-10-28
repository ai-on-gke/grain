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

// Package memory is a GRANT model.CapabilityProvider: a task holding it
// may write down what grain has learned about working here
// (model.Memory, model/memory.go), through the update_memory tool its
// own MCP server exposes -- and read the long form of it
// (model.MemoryDocument) through the two tools beside it,
// list_memory_documents and read_memory_document.
//
// It mints nothing and writes no Placement -- Resolve, Materialize,
// PromptSection and Revoke are all model.BaseCapability's defaults --
// because what it grants is not material moved into a sandbox, it is
// three tools, exactly as pkg/capability/selfdebug grants two.
//
// A capability rather than something every run can do, because memory is
// read by every later run: a note written here is repeated in every
// prompt this deployment issues until somebody replaces it, which is a
// standing influence over work nobody is watching at the time. That is
// worth being a grant a human attaches deliberately -- to the nightly
// memory agent, which is the run it exists for, and to whatever else
// somebody decides should be able to teach this deployment something.
//
// Reading a document is on this same grant rather than open to every run
// (grain/task-270), which is what makes a document's staleness somebody's
// problem: a run that can find out a note no longer matches the tree can
// replace it in the same turn, with the same capability that let it look.
// It also decides who is told a document exists at all -- the index in a
// dispatch's prompt is rendered only for a task holding this grant
// (orchestrator.resolveMemory), since a line pointing at a tool a run does
// not have could only send it looking for one.
//
// How it reaches a run is `grain mcpserver -grant memory`, passed by a
// Framework only for a task holding this grant (agent.RunConfig.Grants,
// agent.GrantArgs), and only for a run that also has a route back to the
// daemon -- the write lands in the daemon's store, so there is nothing a
// sandbox with no daemon to ask could do with it.
package memory

import "github.com/ai-on-gke/grain/pkg/model"

// CapabilityName is the grant name ui.OfferedCapabilities lists as
// "memory".
const CapabilityName = "memory"

// Provider is the memory capability. Every method but Spec is
// model.BaseCapability's default: there is no credential to withhold, no
// lease to mint, nothing to place and nothing to revoke.
type Provider struct {
	model.BaseCapability
}

// Config holds configuration for memory Provider.
type Config struct{}

// New returns the provider, for cmd/grain's registry.
func New() Provider { return Provider{} }

// NewWithConfig returns the provider with config.
func NewWithConfig(_ Config) Provider { return Provider{} }

// Spec is the capability's identity. GRANT and no Requires: this
// capability holds no credential of its own, and the run it serves holds
// none either -- it asks the daemon, which is already the only thing
// that writes this deployment's store.
func (p Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:      CapabilityName,
		Label:     "Memory",
		Source:    model.GrantByLabel,
		Provision: model.ProvisionGrant,
		Description: "Let this task write down what grain has learned about working here, " +
			"deployment-wide or per repository -- notes that are added to the prompt " +
			"of every run dispatched afterwards, and longer notes that no prompt " +
			"carries but any run holding this may look up.",
	}
}
