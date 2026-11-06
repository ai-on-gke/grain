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

package model

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// IsEmbeddedBootstrapOnlyGrant is what lets a grant through on a sandbox
// that is a directory on grain's own host (hasExternalCapabilityRef,
// onlyEmbeddedBootstrapPlacements), so it answers true only for arguments
// that provably select nothing but the embedded bootstrap map: no operator
// config map or secret, and nothing malformed the mint would read past.
func TestIsEmbeddedBootstrapOnlyGrant(t *testing.T) {
	skill := SkillCapabilityServerID + "#skill"
	cmSkill := ConfigMapCapabilityServerID + "#skill"
	for grant, want := range map[string]bool{
		BootstrapPlaybooksGrant: true,
		ConfigMapRef + `={"keys":["bootstrap/host-migration.md"]}`:      true,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":[]}`:    true,
		skill + `={"skills":["bootstrap"]}`:                             true,
		cmSkill + `={"skills":["bootstrap"]}`:                           true,
		"bootstrap-playbooks":                                           false,
		ConfigMapRef:                                                    false,
		ConfigMapRef + `={}`:                                            false,
		ConfigMapRef + `={"config_maps":[]}`:                            false,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"]}`:         false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":["t"]}`: false,
		ConfigMapRef + `={"keys":["bootstrap/a.md","tools/b"]}`:         false,
		// A non-string beside a string: the mint skips the non-string
		// and places the string's map, so the list is not "empty".
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":["t",1]}`: false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"secrets":"t"}`:     false,
		ConfigMapRef + `={"config_maps":["bootstrap",1]}`:                 false,
		ConfigMapRef + `={"config_maps":["bootstrap"],"other":["x"]}`:     false,
		skill:   false,
		cmSkill: false,
		skill + `={"skills":["bootstrap","ops"]}`:    false,
		cmSkill + `={"skills":["bootstrap","ops"]}`:  false,
		skill + `={"skills":["bootstrap"],"x":1}`:    false,
		skill + `={"skills":["bootstrap",1]}`:        false,
		ModelProxyRef + `={"vendors":["anthropic"]}`: false,
	} {
		if got := IsEmbeddedBootstrapOnlyGrant(grant); got != want {
			t.Errorf("IsEmbeddedBootstrapOnlyGrant(%s) = %v, want %v", grant, got, want)
		}
	}
}

func TestIsBootstrapPlaybooksGrant(t *testing.T) {
	skill := SkillCapabilityServerID + "#skill"
	cmSkill := ConfigMapCapabilityServerID + "#skill"
	for grant, want := range map[string]bool{
		"bootstrap-playbooks":   true,
		BootstrapPlaybooksGrant: true,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"]}`:                           true,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"],"keys":["bootstrap/a.md"]}`: true,
		ConfigMapRef + `={"keys":["bootstrap/host-migration.md"]}`:                        true,
		skill + `={"skills":["bootstrap"]}`:                                               true,
		cmSkill + `={"skills":["bootstrap","ops"]}`:                                       true,
		ConfigMapRef:         false,
		ConfigMapRef + `={}`: false,
		ConfigMapRef + `={"config_maps":["ops"]}`: false,
		// Keys subset excludes bootstrap/* even though bootstrap is in config_maps.
		ConfigMapRef + `={"config_maps":["bootstrap","tools"],"keys":["tools/a"]}`: false,
		// Explicit config_maps/secrets excludes bootstrap even though keys names bootstrap/*.
		ConfigMapRef + `={"config_maps":["tools"],"keys":["bootstrap/a.md"]}`: false,
		ConfigMapRef + `={"config_maps":[],"keys":["bootstrap/a.md"]}`:        false,
		ConfigMapRef + `={"secrets":["s"],"keys":["bootstrap/a.md"]}`:         false,
		ConfigMapRef + `={"config_maps":["bootstrap",1]}`:                     false,
		// Bare/empty skill grants on unrelated skill directories do not open src://playbooks.
		skill:                         false,
		skill + `={}`:                 false,
		cmSkill:                       false,
		cmSkill + `={}`:               false,
		skill + `={"skills":["ops"]}`: false,
	} {
		if got := IsBootstrapPlaybooksGrant(grant); got != want {
			t.Errorf("IsBootstrapPlaybooksGrant(%s) = %v, want %v", grant, got, want)
		}
	}
}

func TestSkillAndConfigMapImplicitServerAliasBindsPolicyAndCatalogue(t *testing.T) {
	skillSecret := SkillCapabilityServerID + `#config-map={"secrets":["prod"]}`
	cmSecret := ConfigMapRef + `={"secrets":["prod"]}`

	// CapabilityCatalogue restrictions on config-map bind grants spelled via
	// the implicit skill server ID alias even when Aliases is unset.
	disabled := false
	disabledCat := &CapabilityCatalogue{
		Servers: []CatalogueServer{{ID: ConfigMapCapabilityServerID, Enabled: &disabled}},
	}
	if _, r := disabledCat.ValidateGrants(GrantContext{Kind: CatalogueKindTask, Grants: []string{skillSecret}}); r == nil {
		t.Errorf("disabled config-map catalogue admitted %s", skillSecret)
	}
	personCat := &CapabilityCatalogue{
		Servers: []CatalogueServer{{
			ID:           ConfigMapCapabilityServerID,
			Restrictions: &CapabilityRestrictions{RequiresPerson: true},
		}},
	}
	verdict, r := personCat.ValidateGrants(GrantContext{Kind: CatalogueKindTask, Grants: []string{skillSecret}})
	if r != nil || !slices.Contains(verdict.RequiresPerson, skillSecret) {
		t.Errorf("RequiresPerson config-map catalogue on %s = (%+v, %v), want RequiresPerson", skillSecret, verdict, r)
	}

	// CapabilityPolicy.Permits binds unnormalized skill#config-map grants to
	// config-map#config-map rules (and vice versa) when EnsureBuiltins creates
	// config-map with empty Aliases, unless skill is configured as a separate server.
	builtins := map[string]protocol.ServerConfig{
		ConfigMapCapabilityServerID: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map"},
	}
	denyCM := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: ConfigMapRef, Deny: true}}}
	if err := denyCM.Permits(skillSecret, CapabilitySubject{Kind: CapabilityRunTask}, builtins); err == nil {
		t.Errorf("deny %s permitted %s under implicit alias", ConfigMapRef, skillSecret)
	}
	if err := denyCM.Permits(skillSecret, CapabilitySubject{Kind: CapabilityRunTask}); err == nil {
		t.Errorf("deny %s permitted %s with no server map", ConfigMapRef, skillSecret)
	}
	denySkill := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: SkillCapabilityServerID + "#*", Deny: true}}}
	if err := denySkill.Permits(cmSecret, CapabilitySubject{Kind: CapabilityRunTask}, builtins); err == nil {
		t.Errorf("deny skill#* permitted %s under implicit alias", cmSecret)
	}
	independent := map[string]protocol.ServerConfig{
		ConfigMapCapabilityServerID: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map"},
		SkillCapabilityServerID:     {Command: "custom-skill", Args: []string{"serve"}},
	}
	if err := denyCM.Permits(skillSecret, CapabilitySubject{Kind: CapabilityRunTask}, independent); err != nil {
		t.Errorf("independent skill server was denied by config-map rule: %v", err)
	}
}
