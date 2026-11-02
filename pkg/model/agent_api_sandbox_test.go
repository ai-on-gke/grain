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

// TestSandboxDeniedAPIScopes pins which scopes the sandbox channel
// (X-Grain-Channel: sandbox-cli) refuses even though they are not
// dangerous: the model-only ones, and every deployment:* scope but
// deployment:read, including one the vocabulary does not name yet.
func TestSandboxDeniedAPIScopes(t *testing.T) {
	for _, tc := range []struct {
		scope  APIScope
		denied bool
	}{
		// Model-only, though not dangerous.
		{"dispatch:write", true},
		{"repos:write", true},
		{"sessions:write", true},
		{"teams:broadcast", true},
		{"deployment:upgrade", true},
		{"deployment:restart", true},
		{"deployment:reboot", true},
		{"deployment:not-yet-named", true},
		// Dangerous, and grain's own plumbing.
		{"tasks:admin", true},
		{"settings:write", true},
		{APIScopeInternal, true},
		{"", true},
		// Reachable from the sandbox.
		{"deployment:read", false},
		{"sandboxes:read", false},
		{"dispatch:read", false},
		{"repos:read", false},
		{"sessions:read", false},
		{"tasks:read", false},
		{"tasks:write", false},
		{"ci:read", false},
		{"memory:write", false},
		{"notifications:write", false},
	} {
		if got := IsSandboxDeniedAPIScope(tc.scope); got != tc.denied {
			t.Errorf("IsSandboxDeniedAPIScope(%q) = %v, want %v", tc.scope, got, tc.denied)
		}
		if tc.scope == "" || tc.scope == APIScopeInternal || tc.scope == "deployment:not-yet-named" {
			continue
		}
		grant := []string{APIGrantPrefix + string(tc.scope)}
		if !APIGrantsOf(grant).Has(tc.scope) {
			t.Errorf("APIGrantsOf(%v) does not hold %s", grant, tc.scope)
		}
		if got := SandboxAPIGrantsOf(grant).Has(tc.scope); got == tc.denied {
			t.Errorf("SandboxAPIGrantsOf(%v).Has(%s) = %v, want %v", grant, tc.scope, got, !tc.denied)
		}
	}
}

// TestSandboxChannelDropsPersonOnlyScopeSets pins that a scope set only a
// person may grant reaches nothing over the sandbox channel, even when
// every scope in it is one the sandbox may otherwise call, while the same
// scopes granted by a set without requires_person still reach it.
func TestSandboxChannelDropsPersonOnlyScopeSets(t *testing.T) {
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	if err := InstallAPIScopeSets([]APIScopeSet{
		{Name: "ops/person-reads", Scopes: []string{"ci:read", "tasks:read"}, RequiresPerson: true},
		{Name: "ops/agent-reads", Scopes: []string{"ci:read", "tasks:read"}},
	}); err != nil {
		t.Fatal(err)
	}
	person := []string{"api:ops/person-reads"}
	for _, scope := range []APIScope{"ci:read", "tasks:read"} {
		if !APIGrantsOf(person).Has(scope) {
			t.Errorf("APIGrantsOf(%v) does not hold %s", person, scope)
		}
	}
	if s := SandboxAPIGrantsOf(person); !s.Empty() {
		t.Errorf("SandboxAPIGrantsOf(%v) = %v, want nothing: the set requires a person", person, s.Scopes())
	}
	agent := []string{"api:ops/agent-reads"}
	if s := SandboxAPIGrantsOf(agent); !s.Has("ci:read") || !s.Has("tasks:read") {
		t.Errorf("SandboxAPIGrantsOf(%v) = %v, want ci:read and tasks:read", agent, s.Scopes())
	}
}
