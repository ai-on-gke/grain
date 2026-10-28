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

package grainci_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSpecIsAGrantThatMintsNothing(t *testing.T) {
	spec := grainci.New().Spec()
	if spec.Name != grainci.CapabilityName {
		t.Errorf("name = %q", spec.Name)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Errorf("provision = %q, want a grant: there is nothing here to mint", spec.Provision)
	}
	if spec.MaxLease != 0 {
		t.Errorf("MaxLease = %s, want none: nothing is minted, so nothing expires", spec.MaxLease)
	}
	if len(spec.Requires) != 0 {
		t.Errorf("Requires = %v, want none: this capability resolves no credential", spec.Requires)
	}
	if spec.Description == "" {
		t.Error("a capability with no description is a picker row nobody can read")
	}
}

func TestMaterializeMovesNothingIntoASandbox(t *testing.T) {
	p := grainci.New()
	ctx := context.Background()
	res, err := p.Resolve(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Refused {
		t.Errorf("resolve refused: %s", res.Reason)
	}
	m, err := p.Materialize(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(m.Placements) != 0 || m.Lease != nil || m.CredentialOverride != nil {
		t.Errorf("grain-ci must place nothing and mint nothing: %+v", m)
	}
}

// The refusal has to send an operator to the switch that works, not
// back to a capability picker (task 2015).
func TestRefusalNamesTheCIMode(t *testing.T) {
	msg := grainci.Refusal()
	for _, want := range []string{"grain-ci", "Use Grain CI", "CI Mode"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Refusal() = %q, want it to name %q", msg, want)
		}
	}
}

func TestConfigRepoRequiresCIWithoutAnOperatorDefault(t *testing.T) {
	if !grainci.RequiredFor(model.ConfigRepo()) {
		t.Fatal("grain/config must require CI")
	}
	if grainci.RequiredFor(model.RepoRef{Owner: "acme", Name: "widgets"}) {
		t.Fatal("ordinary repositories remain opt-in")
	}
}

func TestRepoUsesGrainCI(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Config repo always uses Grain CI
	if !grainci.RepoUsesGrainCI(nil, model.ConfigRepo()) {
		t.Error("config repo must use Grain CI")
	}

	// Ordinary repo with no settings does not use Grain CI
	if grainci.RepoUsesGrainCI(nil, repo) {
		t.Error("default repo should not use Grain CI")
	}

	// Repo with CIModeGrain
	if !grainci.RepoUsesGrainCI(&model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}, repo) {
		t.Error("repo with CIModeGrain should use Grain CI")
	}

	// Repo with CIModeGitHub
	if grainci.RepoUsesGrainCI(&model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}, repo) {
		t.Error("repo with CIModeGitHub should not use Grain CI")
	}

	// Repo with CIModeNone
	if grainci.RepoUsesGrainCI(&model.RepoConfig{Repo: repo, CIMode: model.CIModeNone}, repo) {
		t.Error("repo with CIModeNone should not use Grain CI")
	}

	// Repo with IgnoreGitHubCI
	if !grainci.RepoUsesGrainCI(&model.RepoConfig{Repo: repo, IgnoreGitHubCI: true}, repo) {
		t.Error("repo with IgnoreGitHubCI should use Grain CI")
	}
}

func TestGrantedReadsATasksOwnGrants(t *testing.T) {
	if grainci.Granted([]model.Grant{{Capability: "self-debug"}}) {
		t.Error("a task holding something else does not hold this")
	}
	if !grainci.Granted([]model.Grant{{Capability: "self-debug"}, {Capability: grainci.CapabilityName}}) {
		t.Error("a task holding grain-ci holds it")
	}
}
