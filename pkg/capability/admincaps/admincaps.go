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
	"github.com/bwsalmon/grain/pkg/capability"
	"github.com/bwsalmon/grain/pkg/capability/dbaccess"
	"github.com/bwsalmon/grain/pkg/capability/deploymenthealth"
	"github.com/bwsalmon/grain/pkg/capability/dispatchcontrol"
	"github.com/bwsalmon/grain/pkg/capability/grainlog"
	"github.com/bwsalmon/grain/pkg/capability/memory"
	"github.com/bwsalmon/grain/pkg/capability/pipelinecontrol"
	"github.com/bwsalmon/grain/pkg/capability/selfdebug"
	"github.com/bwsalmon/grain/pkg/capability/selfrepair"
	"github.com/bwsalmon/grain/pkg/capability/selfupgrade"
	"github.com/bwsalmon/grain/pkg/model"
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
