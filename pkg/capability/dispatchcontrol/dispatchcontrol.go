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

// Package dispatchcontrol is the dispatch-control capability: manual
// mode for an agent (grain/task-399). A run holding it may pause this
// deployment's pipeline, start exactly the tasks it chooses while the
// pause is in force, defer tasks, and resume -- the same things an
// operator does with the pause banner and the Run now button.
//
// The risk is the point of it: an agent holding this decides what this
// deployment spends its runs on, for as long as it holds the pause.
// Nothing it can do passes an upgrade's hold (a Run now waits behind
// one), the provider usage-limit pause, or a task's own grant and
// credential holds, and a person can lift its pause from the banner at
// any time. A pause and a Run now are attributed to {agent, <task id>}
// -- in the pause row, and in the task's conversation -- so who decided
// is never a guess; a resume and a deferral record nobody, as they do
// for a person. A Run now never approves a grant this task lacks, and
// never starts a task parked on a person's reply in that person's place.
package dispatchcontrol

import (
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilityName is the grant name: "dispatch-control".
const CapabilityName = "dispatch-control"

// Description is the sentence an operator reads before granting it, kept
// here so the picker (ui.OfferedCapabilities) and the provider's own
// Spec cannot drift apart.
const Description = "Let this task decide what this deployment runs: pause the pipeline, start chosen " +
	"tasks with Run now while it is paused (approving them where needed, but never one asking for a grant " +
	"or a repository this task does not hold, or for auto-merge this task could not propose, and never one " +
	"waiting on a person's reply), defer tasks, and resume. " +
	"Its pauses and Run nows are recorded as this task's agent. Run now never passes an upgrade's hold."

// Controller is what the tools call -- mcp.DispatchController.
type Controller = mcp.DispatchController

// Config configures the tools dispatch-control provides.
type Config struct {
	TaskID     string
	Controller Controller
}

// Provider implements model.CapabilityProvider for dispatch-control.
type Provider struct {
	model.BaseCapability
}

// New returns a dispatch-control Provider ready to register.
func New() *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Dispatch control",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
		Description: Description,
	}
}

// Tools returns pause_pipeline, resume_pipeline, run_task_now and
// defer_task. A nil Controller is a run with no daemon to ask: every
// tool is on the roster and refuses each call with a sentence saying so.
func Tools(cfg Config) []mcp.Tool {
	return mcp.NewDispatchControlTools(cfg.Controller)
}
