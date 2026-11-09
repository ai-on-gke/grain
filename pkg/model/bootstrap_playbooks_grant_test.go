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
	"strings"
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
		"BOOTSTRAP-PLAYBOOKS":   true,
		BootstrapPlaybooksGrant: true,
		strings.ToUpper("https://") + strings.TrimPrefix(BootstrapPlaybooksGrant, "https://"): true,
		ConfigMapCapabilityServerID + `={"config_maps":["bootstrap"]}`:                        true,
		ConfigMapCapabilityServerID + `={"keys":["bootstrap/host-migration.md"]}`:             true,
		SkillCapabilityServerID + `={"skills":["bootstrap"]}`:                                 true,
		SkillCapabilityServerID + `={"config_maps":["bootstrap"]}`:                            true,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"]}`:                               true,
		ConfigMapRef + `={"config_maps":["bootstrap","tools"],"keys":["bootstrap/a.md"]}`:     true,
		ConfigMapRef + `={"keys":["bootstrap/host-migration.md"]}`:                            true,
		skill + `={"skills":["bootstrap"]}`:                                                   true,
		cmSkill + `={"skills":["bootstrap","ops"]}`:                                           true,
		ConfigMapRef:                              false,
		ConfigMapCapabilityServerID:               false,
		SkillCapabilityServerID:                   false,
		ConfigMapRef + `={}`:                      false,
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

func TestBootstrapSkillSetUnnamedAndCatalogueBinding(t *testing.T) {
	skill := SkillCapabilityServerID + "#skill"
	cmSkill := ConfigMapCapabilityServerID + "#skill"
	for grant, want := range map[string]bool{
		skill:                               true,
		skill + `={}`:                       true,
		cmSkill:                             true,
		cmSkill + `={}`:                     true,
		SkillCapabilityServerID:             true,
		SkillCapabilityServerID + `={}`:     true,
		skill + `={"skills":["ops"]}`:       false,
		skill + `={"skills":["bootstrap"]}`: false,
		SkillCapabilityServerID + `={"secrets":["prod"]}`: false,
		ConfigMapCapabilityServerID:                       false,
		ConfigMapRef:                                      false,
		BootstrapPlaybooksGrant:                           false,
	} {
		if got := BootstrapSkillSetUnnamed(grant); got != want {
			t.Errorf("BootstrapSkillSetUnnamed(%s) = %v, want %v", grant, got, want)
		}
	}

	disabled := false
	cat := &CapabilityCatalogue{
		InProcess: map[string]CatalogueInProcess{
			"bootstrap-playbooks": {Enabled: &disabled},
		},
	}
	for _, blocked := range []string{
		"bootstrap-playbooks",
		BootstrapPlaybooksGrant,
		ConfigMapCapabilityServerID + `={"config_maps":["bootstrap"]}`,
		SkillCapabilityServerID + `={"skills":["bootstrap"]}`,
		skill,
		cmSkill,
		SkillCapabilityServerID,
	} {
		if _, err := cat.ValidateGrants(GrantContext{Kind: CatalogueKindCI, Grants: []string{blocked}}); err == nil {
			t.Errorf("disabled in_process.bootstrap-playbooks admitted %q, want refusal", blocked)
		}
	}
	for _, allowed := range []string{
		skill + `={"skills":["ops"]}`,
		cmSkill + `={"skills":["ops"]}`,
		ConfigMapRef + `={"config_maps":["ops"]}`,
	} {
		if _, err := cat.ValidateGrants(GrantContext{Kind: CatalogueKindCI, Grants: []string{allowed}}); err != nil {
			t.Errorf("disabled in_process.bootstrap-playbooks refused unrelated %q: %v", allowed, err)
		}
	}
}

func TestSkillAndConfigMapImplicitServerAliasBindsPolicyAndCatalogue(t *testing.T) {
	skillSecret := SkillCapabilityServerID + `#config-map={"secrets":["prod"]}`
	cmSecret := ConfigMapRef + `={"secrets":["prod"]}`
	cmShorthand := ConfigMapCapabilityServerID + `={"secrets":["prod"]}`
	skillShorthand := SkillCapabilityServerID + `={"secrets":["prod"]}`

	// CapabilityCatalogue restrictions on config-map bind grants spelled via
	// the implicit skill server ID alias even when Aliases is unset.
	disabled := false
	disabledCat := &CapabilityCatalogue{
		Servers: []CatalogueServer{{ID: ConfigMapCapabilityServerID, Enabled: &disabled}},
	}
	for _, g := range []string{skillSecret, cmShorthand, skillShorthand} {
		if _, r := disabledCat.ValidateGrants(GrantContext{Kind: CatalogueKindTask, Grants: []string{g}}); r == nil {
			t.Errorf("disabled config-map catalogue admitted %s", g)
		}
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

	// CapabilityPolicy.Permits binds unnormalized skill#config-map and
	// methodless shorthand grants to config-map#config-map rules (and vice
	// versa) when EnsureBuiltins creates config-map with empty Aliases, unless
	// skill is configured as a separate server.
	builtins := map[string]protocol.ServerConfig{
		ConfigMapCapabilityServerID: {Command: "grain", Args: []string{"capability", "config-map"}, Builtin: "config-map"},
	}
	denyCM := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: ConfigMapRef, Deny: true}}}
	for _, g := range []string{skillSecret, cmShorthand, skillShorthand} {
		if err := denyCM.Permits(g, CapabilitySubject{Kind: CapabilityRunTask}, builtins); err == nil {
			t.Errorf("deny %s permitted %s under implicit alias", ConfigMapRef, g)
		}
		if err := denyCM.Permits(g, CapabilitySubject{Kind: CapabilityRunTask}); err == nil {
			t.Errorf("deny %s permitted %s with no server map", ConfigMapRef, g)
		}
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

func TestCapabilityPolicyDenyConfigMapCoversBootstrapSkillAndUnensuredServers(t *testing.T) {
	skill := SkillCapabilityServerID + "#skill"
	cmSkill := ConfigMapCapabilityServerID + "#skill"
	skillCM := SkillCapabilityServerID + "#" + ConfigMapMethod
	subj := CapabilitySubject{Repo: &RepoRef{Owner: "acme", Name: "widgets"}, Kind: CapabilityRunTask}

	// Pre-EnsureBuiltins MCPServers map holding an unrelated server entry:
	// implicit skill/config-map alias and #skill bootstrap deny must still bind.
	unensured := map[string]protocol.ServerConfig{
		GCPCapabilityServerID: {Command: "grain", Args: []string{"capability", "gcp"}, Builtin: "gcp"},
	}
	pol := CapabilityPolicy{
		Rules: []CapabilityPolicyRule{{Capability: ConfigMapRef, Deny: true}},
	}
	for _, blocked := range []string{
		BootstrapPlaybooksGrant,
		skillCM + `={"config_maps":["bootstrap"]}`,
		skillCM + `={"secrets":["prod"]}`,
		cmSkill + `={"skills":["bootstrap"]}`,
		skill + `={"skills":["bootstrap"]}`,
		cmSkill,
		skill,
	} {
		if err := pol.Permits(blocked, subj, unensured); err == nil {
			t.Errorf("Permits(%q, unensured) = nil under ConfigMapRef deny, want refusal", blocked)
		}
	}

	// Unrelated explicit #skill grants remain permitted under a migrated
	// ConfigMapRef deny rule.
	for _, allowed := range []string{
		cmSkill + `={"skills":["ops"]}`,
		skill + `={"skills":["ops"]}`,
	} {
		if err := pol.Permits(allowed, subj, unensured); err != nil {
			t.Errorf("Permits(%q, unensured) = %v under ConfigMapRef deny, want nil", allowed, err)
		}
	}

	// Widening is strictly deny-only: AllowUntrusted on ConfigMapRef never
	// admits untrusted #skill grants.
	allowUntrustedCM := CapabilityPolicy{
		Rules: []CapabilityPolicyRule{{Capability: ConfigMapRef, AllowUntrusted: true}},
	}
	untrusted := CapabilitySubject{Repo: subj.Repo, Kind: CapabilityRunCI, Event: "pull_request", Untrusted: true}
	if err := allowUntrustedCM.Permits(cmSkill+`={"skills":["bootstrap"]}`, untrusted); err == nil {
		t.Error("AllowUntrusted on ConfigMapRef admitted untrusted #skill grant")
	}
}

func TestDangerousAndTemplateGrantsExpandLegacyCapabilities(t *testing.T) {
	const testSrv = "https://acme.example/capabilities/host-script"
	protocol.NotePersonGrantedArgs(testSrv, map[string]*protocol.MethodInfo{
		"host-script": {
			ArgsSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"script": map[string]any{
						"type":                       "string",
						protocol.PersonGrantedArgKey: []any{"guarded-wipe"},
					},
				},
			},
		},
	})
	defer protocol.NotePersonGrantedArgs(testSrv, map[string]*protocol.MethodInfo{"host-script": {}})

	restore := SetRuntimeGrantExpansions(map[string][]string{
		"local-scripts:guarded": {testSrv + `#host-script={"script":"guarded-wipe"}`},
		"local-scripts:safe":    {testSrv + `#host-script={"script":"lint"}`},
	})
	defer restore()

	if !IsDangerousGrant("local-scripts:guarded") || !IsPersonOnlyGrant("local-scripts:guarded") {
		t.Error("runtime-expanded person-granted capability was not classified as dangerous/person-only")
	}
	if IsDangerousGrant("local-scripts:safe") {
		t.Error("non-person-granted runtime expansion was classified as dangerous")
	}

	acting := Task{Grants: []Grant{{Capability: BootstrapPlaybooksGrant}}}
	bootTmpl := Template{ID: "t-boot", Grants: []Grant{{Capability: "bootstrap-playbooks"}}}
	if err := TemplateWithinTask(acting, bootTmpl); err != nil {
		t.Errorf("TemplateWithinTask(acting with canonical bootstrap, template with legacy alias) = %v, want nil", err)
	}
	if err := TemplateWithinTask(Task{}, bootTmpl); err == nil {
		t.Error("TemplateWithinTask(empty acting, template with bootstrap-playbooks) = nil, want refusal")
	}
	guardedTmpl := Template{ID: "t-guarded", Grants: []Grant{{Capability: "local-scripts:guarded"}}}
	actingWithGuarded := Task{Grants: []Grant{{Capability: testSrv + `#host-script={"script":"guarded-wipe"}`}}}
	if err := TemplateWithinTask(actingWithGuarded, guardedTmpl); err == nil {
		t.Error("TemplateWithinTask admitted person-only runtime-expanded grant")
	}
}
