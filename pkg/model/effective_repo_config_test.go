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
	"reflect"
	"testing"
)

func TestEffectiveRepoConfigEmpty(t *testing.T) {
	t.Parallel()

	var empty EffectiveRepoConfig
	if !empty.Empty() {
		t.Errorf("expected zero-valued EffectiveRepoConfig to be empty")
	}

	nonEmpty := EffectiveRepoConfig{CloneURL: "https://example.com"}
	if nonEmpty.Empty() {
		t.Errorf("expected EffectiveRepoConfig with CloneURL to not be empty")
	}
}

// The repository's own configuration resolves over the deployment's
// defaults, and holds the per-repository git credential the default
// team's team_repo_config held until schema 23.
func TestEffectiveRepoConfigResolution(t *testing.T) {
	t.Parallel()

	global := Config{
		DefaultCapabilities: []string{"global-cap"},
		PromptExtension:     "Global prompt",
	}
	repoCfg := &RepoConfig{
		Repo:                  RepoRef{Owner: "acme", Name: "widgets"},
		CloneURL:              "https://github.com/acme/widgets.git",
		DefaultCapabilities:   []string{"repo-cap"},
		CICapabilities:        []string{"repo-ci"},
		PromptExtension:       "Repo prompt",
		SetupCommand:          "make test",
		CIMode:                CIModeGrain,
		GitCredential:         "repo-cred",
		RepairTemplateID:      "repo-repair-tpl",
		RequireBranchUpToDate: true,
		WatchDefaultBranch:    true,
		ReleaseSecrets:        []string{"REPO_SECRET"},
	}

	got := EffectiveRepoConfigFor(global, repoCfg)
	want := EffectiveRepoConfig{
		Repo:                  repoCfg.Repo,
		CloneURL:              repoCfg.CloneURL,
		DefaultCapabilities:   []string{"repo-cap"},
		CICapabilities:        []string{"repo-ci"},
		PromptExtension:       "Repo prompt",
		SetupCommand:          "make test",
		CIMode:                CIModeGrain,
		GitCredential:         "repo-cred",
		RepairTemplateID:      "repo-repair-tpl",
		RequireBranchUpToDate: true,
		WatchDefaultBranch:    true,
		ReleaseSecrets:        []string{"REPO_SECRET"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveRepoConfigFor = %+v, want %+v", got, want)
	}

	// No repo row: the deployment's defaults.
	got = EffectiveRepoConfigFor(global, nil)
	if !reflect.DeepEqual(got.DefaultCapabilities, global.DefaultCapabilities) || got.PromptExtension != global.PromptExtension {
		t.Errorf("EffectiveRepoConfigFor(nil) = %+v, want the deployment's defaults", got)
	}

	// The legacy ignore-GitHub-CI switch still reads as grain CI.
	got = EffectiveRepoConfigFor(Config{}, &RepoConfig{IgnoreGitHubCI: true})
	if got.CIMode != CIModeGrain {
		t.Errorf("CIMode with IgnoreGitHubCI = %q, want %q", got.CIMode, CIModeGrain)
	}
}
