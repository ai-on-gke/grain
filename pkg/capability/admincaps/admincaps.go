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

// Package admincaps lists grain's host- and deployment-level
// capabilities -- the ones that reach past a sandbox into grain's host,
// its database, its deployment or its cloud project.
//
// They were once restricted to admin teams (grain/task-1557). Since the
// single-tenant reset (docs/single-tenant-reset.md, stage 4) every task
// may be granted any of them, and this package is only the one place
// cmd/grain's capabilityProviders and pkg/ui's catalog build them from.
package admincaps

import (
	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/dispatchcontrol"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/memory"
	"github.com/ai-on-gke/grain/pkg/capability/pipelinecontrol"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/capability/selfrepair"
	"github.com/ai-on-gke/grain/pkg/capability/selfupgrade"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamScoped is the capabilities that a team other than an admin team
// used to hold scoped to its own work (grain/task-2555). Every grant of
// them is now deployment-wide: self-debug reads every task and the whole
// metrics report, pipeline-control pauses the whole deployment, and
// memory writes the deployment layer.
var TeamScoped = []string{
	selfdebug.CapabilityName,
	pipelinecontrol.CapabilityName,
	memory.CapabilityName,
}

// IsTeamScoped reports whether id is one of TeamScoped.
func IsTeamScoped(id string) bool {
	for _, have := range TeamScoped {
		if have == id {
			return true
		}
	}
	return false
}

// Providers builds every host- or deployment-level provider that needs
// no deployment configuration. gcp-key and package-publish are built by
// the caller, which has their other settings. Every capability registered
// with capability.RegisterProvider follows core's.
func Providers() []model.CapabilityProvider {
	providers := []model.CapabilityProvider{
		selfdebug.NewWithConfig(selfdebug.Config{}),
		grainlog.NewWithConfig(grainlog.ProviderConfig{}),
		deploymenthealth.NewWithConfig(deploymenthealth.ProviderConfig{}),
		selfrepair.NewWithConfig(selfrepair.Config{}),
		selfupgrade.NewWithConfig(selfupgrade.ProviderConfig{}),
		pipelinecontrol.NewWithConfig(pipelinecontrol.ProviderConfig{}),
		dispatchcontrol.New(),
		dbaccess.NewReadProviderWithConfig(dbaccess.Config{}),
		dbaccess.NewWriteProviderWithConfig(dbaccess.Config{}),
		memory.NewWithConfig(memory.Config{}),
	}
	for _, r := range capability.Registered() {
		providers = append(providers, r.New())
	}
	return providers
}
