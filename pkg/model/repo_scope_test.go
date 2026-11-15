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

func TestConfigRepoIsAlwaysAllowed(t *testing.T) {
	allowed := []string{"acme/widgets"}
	if !RepoAllowed(allowed, ConfigRepo()) {
		t.Fatalf("%s should remain targetable when external repositories are restricted to %v", ConfigRepo(), allowed)
	}
	if RepoAllowed(allowed, RepoRef{Owner: "grain", Name: "config-default"}) {
		t.Fatal("grain/config-default is not a configuration repository, so the exception must not admit it")
	}
	if RepoAllowed(allowed, RepoRef{Owner: "acme", Name: "other"}) {
		t.Fatal("the config repository exception widened an ordinary repository")
	}
}

// grain/config is the one configuration repository. The per-team
// grain/config-<team> names are ordinary repo names now, in any case.
func TestIsConfigRepoIsOnlyGrainConfig(t *testing.T) {
	for _, tc := range []struct {
		repo RepoRef
		want bool
	}{
		{ConfigRepo(), true},
		{RepoRef{Owner: "Grain", Name: "Config"}, true},
		{RepoRef{Owner: "grain", Name: "config-default"}, false},
		{RepoRef{Owner: "grain", Name: "config-platform"}, false},
		{RepoRef{Owner: "acme", Name: "config"}, false},
		{RepoRef{Owner: "acme", Name: "widgets"}, false},
	} {
		if got := IsConfigRepo(tc.repo); got != tc.want {
			t.Errorf("IsConfigRepo(%v) = %v, want %v", tc.repo, got, tc.want)
		}
	}
}

func TestRepoAllowedWildcards(t *testing.T) {
	allowed := []string{"backend/*", "infra/terraform", "*"}

	if !RepoAllowed([]string{"backend/*"}, RepoRef{Owner: "backend", Name: "api"}) {
		t.Errorf("backend/* should match backend/api")
	}
	if !RepoAllowed([]string{"backend/*"}, RepoRef{Owner: "backend", Name: "worker"}) {
		t.Errorf("backend/* should match backend/worker")
	}
	if RepoAllowed([]string{"backend/*"}, RepoRef{Owner: "frontend", Name: "app"}) {
		t.Errorf("backend/* should not match frontend/app")
	}
	if !RepoAllowed([]string{"*"}, RepoRef{Owner: "anything", Name: "anything"}) {
		t.Errorf("* should match anything/anything")
	}
	if !RepoAllowed(allowed, RepoRef{Owner: "backend", Name: "core"}) {
		t.Errorf("allowed list should match backend/core")
	}
}

// One owner/name can exist on two git hosts as two repositories, so an
// owner wildcard on one host must not admit that owner on another, in
// either direction, while an explicit host still matches its own repos.
func TestRepoAllowedOwnerWildcardStaysOnItsHost(t *testing.T) {
	onDefault := RepoRef{Owner: "acme", Name: "widgets"}
	onOther := RepoRef{Host: "git.example.com", Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		allowed string
		target  RepoRef
		want    bool
	}{
		{"git.example.com/acme/*", onOther, true},
		{"git.example.com/acme/*", onDefault, false},
		{"acme/*", onDefault, true},
		{"acme/*", onOther, false},
		{"github.com/acme/*", onDefault, true},
		{"github.com/acme/*", onOther, false},
		{"git.example.com/acme/widgets", onDefault, false},
		{"acme/widgets", onOther, false},
		{"*", onOther, true},
		{"*/*", onOther, true},
	} {
		if got := RepoAllowed([]string{tc.allowed}, tc.target); got != tc.want {
			t.Errorf("RepoAllowed([%q], %s) = %v, want %v", tc.allowed, tc.target, got, tc.want)
		}
	}
}

// A capability catalogue entry's repos: globs follow RepoAllowed's host
// rule: `owner/*` is the default host's owner and `host/owner/*` that
// host's, never both.
func TestCatalogueRepoGlobsStayOnTheirHost(t *testing.T) {
	onDefault := RepoRef{Owner: "acme", Name: "widgets"}
	onOther := RepoRef{Host: "git.example.com", Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		pattern string
		target  RepoRef
		want    bool
	}{
		{"acme/*", onDefault, true},
		{"acme/*", onOther, false},
		{"git.example.com/acme/*", onOther, true},
		{"git.example.com/acme/*", onDefault, false},
		{"acme/widgets", onOther, false},
		{"git.example.com/acme/widgets", onOther, true},
		{"*", onOther, true},
		{"*/*", onOther, true},
	} {
		target := tc.target
		if got := matchesRepo([]string{tc.pattern}, &target); got != tc.want {
			t.Errorf("matchesRepo([%q], %s) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}
