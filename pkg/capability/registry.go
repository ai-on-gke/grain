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

// Package capability is the registry through which a package outside core
// grain contributes a capability provider of its own (google/README.md).
//
// Core's own providers are listed where they are built -- admincaps,
// cmd/grain's capabilityProviders, pkg/ui's catalog -- and need nothing
// here. A provider registered here is added to every one of those
// listings, from the one Registration, so a contributed capability is
// offered, granted and served exactly as a core one would be without any
// of them naming it.
//
// A Registration can also say it is on by default (DefaultOn): a fresh
// deployment of a build that registers it starts with it among its
// default capabilities (NewDeploymentConfig), and an existing one is only
// told so in Settings. Core grain declares no defaults; this is a seam for
// a build such as google/cmd/grain to use.
package capability

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Registration is one contributed capability.
type Registration struct {
	// ID is the grant name tasks carry, and what Spec().Name of the
	// provider New builds must say.
	ID string
	// Name is the short display name Settings and the per-task picker
	// show, and Description the one sentence beside it.
	Name, Description string
	// New builds the provider.
	New func() model.CapabilityProvider
	// RunGrant says the grant's effect is on the run itself -- the tools
	// an agent framework or `grain mcpserver` gives it -- rather than
	// anything placed in its sandbox, so the orchestrator forwards it to
	// the run in agent.RunConfig.Grants and `grain mcpserver -grant`
	// accepts it without a warning.
	RunGrant bool
	// DefaultOn says a brand-new deployment of a build that registers
	// this capability starts with it in model.Config.DefaultCapabilities,
	// so every task filed there is seeded holding it (NewDeploymentConfig
	// below), and in the shipped director console session's default
	// capabilities (model.DefaultConsoleSessions). It is a seed and
	// nothing more: the grant is still an
	// ordinary GrantByDefault row a task can drop, every policy and
	// approval rule that applies to the capability still applies, and a
	// deployment that already has a stored configuration is never
	// changed by it -- an operator who took it out of the defaults or
	// unticked it on the director keeps it out. Core grain registers
	// nothing with it set.
	DefaultOn bool
}

var (
	mu         sync.RWMutex
	registered = map[string]Registration{}
)

// RegisterProvider adds r to every listing of grain's capabilities until
// the returned func is called. It is meant to be called from init(), and
// panics on a Registration with no ID or no New, or on an ID already
// registered, since either is a programming error no deployment could
// work around.
func RegisterProvider(r Registration) (unregister func()) {
	if r.ID == "" || r.New == nil {
		panic(fmt.Sprintf("capability: RegisterProvider(%q): a registration needs an ID and a New", r.ID))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := registered[r.ID]; ok {
		panic(fmt.Sprintf("capability: RegisterProvider(%q): already registered", r.ID))
	}
	registered[r.ID] = r
	var unregisterSession func()
	if r.DefaultOn {
		unregisterSession = model.RegisterDefaultConsoleSessionCapability(r.ID)
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(registered, r.ID)
		if unregisterSession != nil {
			unregisterSession()
		}
	}
}

// Registered returns every registered capability, ordered by ID so that
// each listing reads the same way on every call.
func Registered() []Registration {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Registration, 0, len(registered))
	for _, r := range registered {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup returns the registration for id, if there is one.
func Lookup(id string) (Registration, bool) {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registered[id]
	return r, ok
}

// RunGrants returns the IDs of the registered capabilities with RunGrant
// set, in Registered's order.
func RunGrants() []string {
	var ids []string
	for _, r := range Registered() {
		if r.RunGrant {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// DefaultOn returns the IDs of the registered capabilities with DefaultOn
// set, in Registered's order.
func DefaultOn() []string {
	var ids []string
	for _, r := range Registered() {
		if r.DefaultOn {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// NewDeploymentConfig is model.NewDeploymentConfig with DefaultOn as its
// DefaultCapabilities: the configuration a deployment of this build
// stores in its first grain_config row. It lives here rather than in
// pkg/model because only this package knows what the build registered.
//
// Only the paths that write or describe a deployment with nothing stored
// call it (the daemon's seed for a fresh database, ui.UpdateSettings'
// first save, ui.GetSettings before either); a stored row is read back as
// it is, so the defaults of an existing deployment are its operator's.
func NewDeploymentConfig() model.Config {
	c := model.NewDeploymentConfig()
	c.DefaultCapabilities = DefaultOn()
	return c
}
