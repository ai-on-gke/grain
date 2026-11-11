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

package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCapabilityCatalogueDisabledReason(t *testing.T) {
	t.Parallel()

	var nilCat *model.CapabilityCatalogue
	if reason, ok := nilCat.DisabledReason(model.GitHubReadIssuesGrant); ok || reason != "" {
		t.Fatalf("nil catalogue DisabledReason = (%q, %v), want empty/false", reason, ok)
	}

	cat := &model.CapabilityCatalogue{
		Servers: []model.CatalogueServer{
			{
				ID:      "https://capabilities.internal/off",
				Enabled: boolp(false),
				Aliases: []string{"https://capabilities.internal/off-alias"},
			},
			{
				ID: "https://capabilities.internal/restricted",
				Methods: []model.CatalogueMethod{{
					Name: "run",
					Args: map[string]model.CatalogueArg{"env": {Allowed: []string{"dev"}}},
					Restrictions: &model.CapabilityRestrictions{
						Repos: model.Patterns{"acme/*"},
						Kinds: model.Patterns{"task"},
					},
				}},
			},
		},
		InProcess: map[string]model.CatalogueInProcess{
			"read-issues":    {Enabled: boolp(false)},
			"image-signing":  {Enabled: boolp(false)},
			"self-debug":     {Enabled: boolp(false)},
			"active-off":     {Enabled: boolp(false)},
			"ci-only-active": {Restrictions: &model.CapabilityRestrictions{CIOnly: true}},
		},
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				"memory":     {Enabled: boolp(false)},
				"ops/triage": {Enabled: boolp(false)},
			},
		},
	}

	disabledCases := []struct {
		grant   string
		wantSub string
	}{
		{"read-issues", "capability read-issues is disabled in settings/capabilities.yml"},
		{model.GitHubReadIssuesGrant, "in_process.read-issues, the capability it replaced"},
		{"https://grain.dev/capabilities/release#image-signing={}", "in_process.image-signing, the capability it replaced"},
		{"api:self-debug", "in_process.self-debug, the capability it replaced"},
		{"https://grain.dev/capabilities/grain#source={}", "in_process.self-debug, the capability it replaced"},
		{"active-off", "capability active-off is disabled in settings/capabilities.yml"},
		{"active-off:sub", "capability active-off:sub is disabled in settings/capabilities.yml"},
		{"https://capabilities.internal/off#do={}", "server https://capabilities.internal/off is disabled"},
		{"https://capabilities.internal/off-alias#do={}", "server https://capabilities.internal/off is disabled"},
		{"api:memory", "capability api:memory is disabled in settings/capabilities.yml"},
		{"api:grain/memory", "capability api:grain/memory is disabled in settings/capabilities.yml"},
		{"memory", "capability api:memory is disabled in settings/capabilities.yml"},
		{"api:ops/triage", "capability api:ops/triage is disabled in settings/capabilities.yml"},
	}
	for _, tc := range disabledCases {
		reason, ok := cat.DisabledReason(tc.grant)
		if !ok {
			t.Errorf("DisabledReason(%q) = false, want disabled (%q)", tc.grant, tc.wantSub)
			continue
		}
		if !strings.Contains(reason, tc.wantSub) {
			t.Errorf("DisabledReason(%q) = %q, want substring %q", tc.grant, reason, tc.wantSub)
		}
	}

	// Non-disabled refusals (ci_only, repo mismatch, unlisted method, disallowed
	// arg) must NOT be classified as disabled.
	notDisabled := []string{
		"",
		"release-gate-read",
		"ci-only-active",
		`https://capabilities.internal/restricted#run={"env":"prod"}`,
		`https://capabilities.internal/restricted#unlisted={}`,
	}
	for _, g := range notDisabled {
		if reason, ok := cat.DisabledReason(g); ok {
			t.Errorf("DisabledReason(%q) = (%q, true), want false", g, reason)
		}
	}
}

func TestEffectiveCatalogueForDisabledCheckMergesMCPServers(t *testing.T) {
	t.Parallel()
	cat := &model.CapabilityCatalogue{
		InProcess: map[string]model.CatalogueInProcess{"read-issues": {Enabled: boolp(true)}},
	}
	mcpJSON := `{"https://capabilities.internal/ext":{"command":"grain","disabled":true,"aliases":["https://capabilities.internal/ext-alias"]}}`
	eff := model.EffectiveCatalogueForDisabledCheck(cat, mcpJSON)
	if reason, ok := eff.DisabledReason("https://capabilities.internal/ext-alias#run={}"); !ok || !strings.Contains(reason, "disabled") {
		t.Fatalf("DisabledReason via disabled mcp_servers entry = (%q, %v), want disabled", reason, ok)
	}
	if reason, ok := eff.DisabledReason(model.GitHubReadIssuesGrant); ok {
		t.Fatalf("enabled read-issues reported disabled: %q", reason)
	}
}
