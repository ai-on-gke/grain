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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRefusalNamesTheCIMode(t *testing.T) {
	msg := grainci.Refusal("", "")
	for _, want := range []string{"grain-ci", model.GrainCIRef, "Use Grain CI", "CI Mode", "api#ci-read"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Refusal(\"\", \"\") = %q, want it to name %q", msg, want)
		}
	}
}

// On a repository that deliberately uses GitHub Actions the refusal
// offers the read grant that stands in for grain#ci, and the commands
// that need nothing -- not switching the repository to grain CI.
func TestRefusalOnAGitHubModeRepoOffersCIRead(t *testing.T) {
	for _, mode := range []model.CIMode{model.CIModeGitHub, model.CIModeNone, ""} {
		msg := grainci.Refusal(mode, "acme/widgets")
		for _, want := range []string{model.GrainCIRef, "api#ci-read", `{repo: "acme/widgets"}`,
			grainci.SubstituteGrant("acme/widgets"), "`grain ci wait`", "`grain pr open`"} {
			if !strings.Contains(msg, want) {
				t.Errorf("Refusal(%q, acme/widgets) = %q, want it to name %q", mode, msg, want)
			}
		}
		if strings.Contains(msg, "Set the CI Mode") || strings.Contains(msg, "set its CI Mode") {
			t.Errorf("Refusal(%q, acme/widgets) = %q, should not tell the person to switch the CI Mode", mode, msg)
		}
	}
}

func TestRefusalOnAGrainCIRepoSaysItIsHeld(t *testing.T) {
	msg := grainci.Refusal(model.CIModeGrain, "acme/widgets")
	if !strings.Contains(msg, "hold it already") || strings.Contains(msg, "api#ci-read") {
		t.Errorf("Refusal(grain, acme/widgets) = %q, want it to say the tasks hold it already", msg)
	}
}

func TestSubstituteGrantIsCIRead(t *testing.T) {
	got := grainci.SubstituteGrant("acme/widgets")
	if !model.APIGrantsOf([]string{got}).Allows("ci:read", "acme/widgets") {
		t.Errorf("SubstituteGrant = %q, does not allow ci:read on acme/widgets", got)
	}
	if model.APIGrantsOf([]string{got}).Allows("ci:read", "acme/other") {
		t.Errorf("SubstituteGrant = %q allows ci:read beyond acme/widgets", got)
	}
	capability, with := grainci.SubstituteRequest("acme/widgets")
	if capability != model.APIRef("ci-read") || with["repo"] != "acme/widgets" {
		t.Errorf("SubstituteRequest = %q %v", capability, with)
	}
}

func TestModeOf(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		cfg  *model.RepoConfig
		want model.CIMode
	}{
		{nil, model.CIModeGitHub},
		{&model.RepoConfig{CIMode: model.CIModeGrain}, model.CIModeGrain},
		{&model.RepoConfig{IgnoreGitHubCI: true}, model.CIModeGrain},
		{&model.RepoConfig{CIMode: model.CIModeNone}, model.CIModeNone},
	} {
		if got := grainci.ModeOf(tc.cfg, repo); got != tc.want {
			t.Errorf("ModeOf(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
	if got := grainci.ModeOf(nil, model.ConfigRepo()); got != model.CIModeGrain {
		t.Errorf("ModeOf(config repo) = %q, want grain", got)
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
	if !grainci.Granted([]model.Grant{{Capability: "self-debug"}, {Capability: grainci.Grant}}) {
		t.Error("a task holding grain#ci holds it")
	}
	if grainci.Granted([]model.Grant{{Capability: grainci.LegacyName}}) {
		t.Error("a task holding only the retired bare grain-ci does not hold it: the name is no alias any longer")
	}
	if !grainci.Granted([]model.Grant{{Capability: model.GrainCIRef}}) {
		t.Error("a task holding the bare grain#ci reference holds it")
	}
	if grainci.Granted([]model.Grant{{Capability: model.GrainJournalGrant}}) {
		t.Error("another grain server method is not grain#ci")
	}
}
