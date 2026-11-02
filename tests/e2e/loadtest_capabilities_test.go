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

// loadtest_capabilities_test.go is the capability registry the load
// harness (loadtest_test.go) ticks RunCycle against, and the per-commit
// check that it still holds what a tick will actually ask of it.
//
// orchestrator.RunCycle maintains grain's shipped console sessions
// (model.DefaultConsoleSessions) and dispatches their tasks like any
// other, so the registry the harness hands its Deps has to hold a
// provider for every capability those sessions grant. When it does not,
// the director's task is parked on the first tick that reaches it ("no
// provider is registered for capability ...") and, since the harness
// treats a RunCycle error as a failure, the whole run dies in seconds.
// That is exactly what happened when DefaultConsoleSessions grew
// "deployment-health" (2b70de96c) and the harness's own hand-written
// list of four providers did not: every nightly load run lost its
// `sustained load` job in thirteen seconds for a week, because the only
// place that drift showed up was a job nobody gates a commit on.
//
// So the list is not hand-written per session any more. The harness
// registers the no-argument, grant-only providers cmd/grain's own
// capabilityProviders registers unconditionally, and takes the
// capabilities it must cover from model.DefaultConsoleSessions itself --
// a director granted self-repair or memory tomorrow needs no edit here.
// A capability that still has no provider fails loudly and by name,
// twice over: TestLoadHarnessCoversDefaultConsoleSessions below says so
// in seconds on the commit that causes it (it is an ordinary test, not
// one GRAIN_LOAD_TEST gates), and the harness itself refuses to start
// rather than letting the reader work the name back out of a parked
// task twelve hours later.
//
// Deliberately not shared with cmd/grain's capabilityProviders: that
// function is in package main, and it takes a whole parsed daemon
// config, a model-proxy endpoint map and a GitHub credential ladder to
// construct providers this harness has no deployment for. Duplicating
// the handful of no-argument constructors is cheaper than either
// dragging that wiring into tests/e2e or extracting a package for it --
// and what actually broke was never the constructors, it was nobody
// noticing a name had been added to the other list.
package e2e

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	consolecap "github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/prioritydispatch"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/capability/reporead"
	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// loadHarnessRegistry is the registry the harness ticks with: every
// provider a console session can hold without this harness inventing
// deployment configuration for it, plus whatever test doubles the caller
// passes (the harness's own loadCapability).
//
// The providers are pkg/cli/daemon.go's unconditional block minus the
// ones that need something a real daemon has and this harness does not
// -- github-sandbox (a GitHub host), the two GCP ones (a project),
// package-publish (a credential ladder), the model proxies (running
// endpoints) and the named GitHub tokens (an operator's secrets). None
// of those is grantable to a console session out of the box; if one ever
// is, the check below names it rather than leaving a tick to discover it.
//
// The dynamic resolvers are registered the way newCapabilityRegistry
// does, so a session granted a repo-read:owner/name capability resolves
// here exactly as it would on a deployment.
func loadHarnessRegistry(extra ...model.CapabilityProvider) *model.CapabilityRegistry {
	providers := append([]model.CapabilityProvider{}, extra...)
	providers = append(providers,
		selfdebug.New(),
		bootstrap.New(), grainci.New(), registrypush.New(),
		consolecap.New(), readissues.New(),
		prioritydispatch.New(), durable.New())
	for _, r := range capability.Registered() {
		providers = append(providers, r.New())
	}
	reg := model.NewCapabilityRegistry(providers...)
	reg.RegisterDynamic(reporead.DynamicProvider, repowrite.DynamicProvider, ui.AgentAPIDynamicProvider)
	return reg
}

// missingSessionCapabilities names every capability the given console
// sessions grant that reg has no provider for -- the registry's own
// Lookup, which is the call model.ResolveGrants makes when a tick
// prepares one of those sessions' tasks, plus grainserver's in-process
// capability-server check for https://grain.dev/capabilities/grain#*
// references, so this answers the question the dispatch will ask rather
// than a restatement of it.
func missingSessionCapabilities(reg *model.CapabilityRegistry, sessions []model.ConsoleSession) []string {
	var missing []string
	seen := map[string]bool{}
	lookup := grainserver.Lookup(nil)
	for _, session := range sessions {
		for _, name := range session.Capabilities {
			if seen[name] {
				continue
			}
			seen[name] = true
			if model.IsCapabilityRef(name) {
				if !grainserver.IsInProcessGrant(name) {
					missing = append(missing, name)
					continue
				}
				if _, err := model.NormalizeCapabilityNames([]string{name}, lookup); err != nil {
					missing = append(missing, name)
				}
				continue
			}
			if _, ok := reg.Lookup(name); !ok {
				missing = append(missing, name)
			}
		}
	}
	return missing
}

// TestLoadHarnessCoversDefaultConsoleSessions is the cheap standing
// assertion this file exists for: it runs on every commit (no
// GRAIN_LOAD_TEST gate, no store, no tick) and fails naming the
// capability that was granted to a shipped session without the harness
// gaining a provider for it.
func TestLoadHarnessCoversDefaultConsoleSessions(t *testing.T) {
	t.Parallel()
	reg := loadHarnessRegistry()
	if missing := missingSessionCapabilities(reg, model.DefaultConsoleSessions()); len(missing) != 0 {
		t.Fatalf("model.DefaultConsoleSessions grants %v, which the load harness has no capability provider for: "+
			"orchestrator.RunCycle parks the session's own task on its first dispatch (\"no provider is registered for "+
			"capability ...\"), which fails TestLoadSustainedConcurrency within seconds of starting. "+
			"Register a provider for each in loadHarnessRegistry (this file).", missing)
	}
}
