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

package antigravity

import (
	"github.com/ai-on-gke/grain/pkg/agent/antigravity/hook"
	"github.com/ai-on-gke/grain/pkg/model"
)

// HostToolGrant is a capability grant that admits a fixed, read-only
// handful of the agy host's own native tools (hook.HostToolGrant). A
// package outside core registers one from init() with
// RegisterHostToolGrant, or hook.RegisterHostToolGrant -- the same
// registry, which a binary that only answers the hook (grain-agentd) can
// reach without linking this package.
type HostToolGrant = hook.HostToolGrant

func init() {
	// The daemon side matches a run's grants to registered host-tool
	// grants by what they grant, retired names included.
	hook.SetGrantCanonicalizer(model.CanonicalLegacyGrant)
}

// RegisterHostToolGrant makes g's tools admissible to a run holding
// g.Capability until the returned func is called
// (hook.RegisterHostToolGrant).
func RegisterHostToolGrant(g HostToolGrant) (unregister func()) {
	return hook.RegisterHostToolGrant(g)
}

// registeredHostToolGrants is every registered grant, ordered by
// Capability.
func registeredHostToolGrants() []HostToolGrant { return hook.RegisteredHostToolGrants() }

// heldHostToolGrants is the registered grants among grants, each matched
// by what it grants rather than by its spelling.
func heldHostToolGrants(grants []string) []HostToolGrant { return hook.HeldHostToolGrants(grants) }

// hostToolGrantFor is the registered grant whose allowlist names tool.
func hostToolGrantFor(tool string) (HostToolGrant, bool) { return hook.HostToolGrantFor(tool) }
