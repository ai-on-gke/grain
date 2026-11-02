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

// grain-ci retired for grain#ci: every writer records the grant, and
// every reader still takes the bare name nothing rewrote.
func TestGrainCIGrantReplacesBareGrainCI(t *testing.T) {
	if got := model.CanonicalLegacyGrant("grain-ci"); got != model.GrainCIGrant {
		t.Errorf("CanonicalLegacyGrant(grain-ci) = %q, want %q", got, model.GrainCIGrant)
	}
	for _, g := range []string{model.GrainCIGrant, model.GrainCIRef, "grain-ci", " " + model.GrainCIGrant + " "} {
		if !model.IsGrainCIGrant(g) {
			t.Errorf("IsGrainCIGrant(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"", "self-debug", "grain-ci-off", model.GrainJournalGrant, model.GrainHealthRef,
		"https://grain.dev/capabilities/grain#ci-read={}", "https://example.com/capabilities/grain#ci={}"} {
		if model.IsGrainCIGrant(g) {
			t.Errorf("IsGrainCIGrant(%q) = true, want false", g)
		}
	}
	if !model.HasGrainCI([]string{"self-debug", "grain-ci"}) || model.HasGrainCI([]string{"self-debug"}) {
		t.Error("HasGrainCI does not read a list of grants the way IsGrainCIGrant reads one")
	}
}
