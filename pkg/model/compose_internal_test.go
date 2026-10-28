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

import "testing"

// adjustGrainCIGrant is handed a template's or schedule's own Grants
// slice, which every composition made from that declaration shares. A
// slice with spare capacity must come back as a fresh array, not with the
// grain-ci grant written into the declaration's own backing store.
func TestAdjustGrainCIGrantDoesNotWriteIntoTheDeclaration(t *testing.T) {
	backing := make([]Grant, 1, 4)
	backing[0] = Grant{Capability: "self-debug", Via: GrantByLabel}
	declared := backing[:1]

	got := adjustGrainCIGrant(declared, true)
	if len(got) != 2 || got[1].Capability != "grain-ci" {
		t.Fatalf("adjustGrainCIGrant(..., true) = %+v, want self-debug then grain-ci", got)
	}
	if spare := backing[:2][1]; spare != (Grant{}) {
		t.Fatalf("adjustGrainCIGrant wrote %+v into the declaration's spare capacity", spare)
	}

	// A second composition from the same declaration, for a repo that
	// does not use grain CI, must not see the first one's grant.
	if again := adjustGrainCIGrant(declared, false); len(again) != 1 || again[0].Capability != "self-debug" {
		t.Fatalf("adjustGrainCIGrant(..., false) = %+v, want only self-debug", again)
	}
}

func TestApplyGrainCIGrantCreditsTheRepoNotTheDeclaration(t *testing.T) {
	sched := Schedule{
		ID:     "sched-2",
		Title:  "Weekly Dependency & Vulnerability Audit",
		Target: RepoRef{Owner: "acme", Name: "widgets"},
		Grants: []Grant{{Capability: "self-debug", Via: GrantByLabel}},
	}
	comp := ComposeScheduleTask(sched, nil)
	applyGrainCIGrant(&comp, true)

	if len(comp.Grants) != 2 || comp.Grants[0].Capability != "self-debug" || comp.Grants[1].Capability != "grain-ci" {
		t.Fatalf("Grants = %+v, want [self-debug, grain-ci]", comp.Grants)
	}
	if len(comp.GrantSources) != 2 || comp.GrantSources[0] != LayerSchedule || comp.GrantSources[1] != LayerRepo {
		t.Errorf("GrantSources = %v, want [%q, %q]", comp.GrantSources, LayerSchedule, LayerRepo)
	}
	if comp.Sources.Grants != LayerSchedule {
		t.Errorf("Sources.Grants = %q, want %q", comp.Sources.Grants, LayerSchedule)
	}

	// When the declaration itself asked for no capabilities, the repo is
	// the only layer that granted any.
	empty := ComposeScheduleTask(Schedule{
		ID:     "sched-1",
		Title:  "Sweep",
		Target: RepoRef{Owner: "acme", Name: "widgets"},
	}, nil)
	applyGrainCIGrant(&empty, true)
	if len(empty.Grants) != 1 || empty.Grants[0].Capability != "grain-ci" {
		t.Fatalf("empty.Grants = %+v, want [grain-ci]", empty.Grants)
	}
	if empty.Sources.Grants != LayerRepo {
		t.Errorf("empty.Sources.Grants = %q, want %q", empty.Sources.Grants, LayerRepo)
	}
	if len(empty.GrantSources) != 1 || empty.GrantSources[0] != LayerRepo {
		t.Errorf("empty.GrantSources = %v, want [%q]", empty.GrantSources, LayerRepo)
	}
}
