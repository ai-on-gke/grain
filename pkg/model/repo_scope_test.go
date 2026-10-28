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
