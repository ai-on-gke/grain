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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// grain-ci retired for grain#ci: the store migration and an import
// rewrite the bare name, and no reader takes it as an alias any longer.
func TestGrainCIGrantReplacesBareGrainCI(t *testing.T) {
	if got := model.CanonicalLegacyGrant("grain-ci"); got != "grain-ci" {
		t.Errorf("CanonicalLegacyGrant(grain-ci) = %q, want it unchanged", got)
	}
	if got := model.RetiredGrantsBecame("grain-ci"); len(got) != 1 || got[0] != model.GrainCIGrant {
		t.Errorf("RetiredGrantsBecame(grain-ci) = %q, want [%q]", got, model.GrainCIGrant)
	}
	for _, g := range []string{model.GrainCIGrant, model.GrainCIRef, " " + model.GrainCIGrant + " "} {
		if !model.IsGrainCIGrant(g) {
			t.Errorf("IsGrainCIGrant(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"", "self-debug", "grain-ci", "grain-ci-off", model.GrainJournalGrant, model.GrainHealthRef,
		"https://grain.dev/capabilities/grain#ci-read={}", "https://example.com/capabilities/grain#ci={}"} {
		if model.IsGrainCIGrant(g) {
			t.Errorf("IsGrainCIGrant(%q) = true, want false", g)
		}
	}
	if !model.HasGrainCI([]string{"self-debug", model.GrainCIGrant}) || model.HasGrainCI([]string{"self-debug", "grain-ci"}) {
		t.Error("HasGrainCI does not read a list of grants the way IsGrainCIGrant reads one")
	}
}

// A repository's CI Mode attaches grain#ci with GrantByDefault, while a
// standing session or a task on a GitHub-mode repository cannot hold
// grain#ci at all: that default must not count as an uncovered parent
// escalation, and a parent holding the retired bare grain-ci no longer
// covers a child's canonical grain#ci grant.
func TestDefaultGrainCIGrantIsNotAParentEscalation(t *testing.T) {
	director := model.Task{ID: "9", Grants: []model.Grant{{Capability: "self-debug", Via: model.GrantByGrain}}}
	child := model.Task{Grants: []model.Grant{
		{Capability: model.GrainCIGrant, Via: model.GrantByDefault},
	}}
	if got := model.EscalatedCapabilities(child, director); len(got) != 0 {
		t.Errorf("EscalatedCapabilities with default grain#ci = %v, want none", got)
	}
	if refusal := model.AutoApprovalRefusal(child, director, true, nil); refusal != "" {
		t.Errorf("AutoApprovalRefusal with default grain#ci = %q, want none", refusal)
	}

	labeled := model.Task{Grants: []model.Grant{
		{Capability: model.GrainCIGrant, Via: model.GrantByLabel},
	}}
	if got := model.EscalatedCapabilities(labeled, director); len(got) != 1 || got[0] != model.GrainCIGrant {
		t.Errorf("EscalatedCapabilities with labeled grain#ci and bare parent = %v, want [%s]", got, model.GrainCIGrant)
	}

	legacyParent := model.Task{ID: "10", Grants: []model.Grant{{Capability: "grain-ci", Via: model.GrantByDefault}}}
	if model.GrantsSubsetOf(labeled.Grants, legacyParent.Grants) {
		t.Error("GrantsSubsetOf treated retired bare grain-ci on parent as covering canonical grain#ci")
	}
	if got := model.EscalatedCapabilities(labeled, legacyParent); len(got) != 1 || got[0] != model.GrainCIGrant {
		t.Errorf("EscalatedCapabilities against parent holding retired grain-ci = %v, want [%s]", got, model.GrainCIGrant)
	}
}
