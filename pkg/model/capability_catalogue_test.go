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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func boolp(b bool) *bool { return &b }

func testCatalogue() *model.CapabilityCatalogue {
	return &model.CapabilityCatalogue{
		Servers: []model.CatalogueServer{{
			ID: "https://capabilities.internal/gcp",
			Methods: []model.CatalogueMethod{{
				Name: "gcp-key",
				Args: map[string]model.CatalogueArg{"project": {Allowed: []string{"grain-dev", "grain-prod"}}},
				Restrictions: &model.CapabilityRestrictions{
					Repos:              model.Patterns{"ai-on-gke/*"},
					Kinds:              model.Patterns{"task", "ci"},
					RequiresPersonWhen: map[string][]string{"project": {"grain-prod"}},
				},
			}},
		}, {
			ID: "https://capabilities.internal/off", Enabled: boolp(false),
		}},
		InProcess: map[string]model.CatalogueInProcess{
			"self-debug": {Restrictions: &model.CapabilityRestrictions{
				Repos: model.Patterns{"ai-on-gke/grain"}, Kinds: model.Patterns{"task", "session", "director"},
			}},
			"gcp-key": {Restrictions: &model.CapabilityRestrictions{
				Kinds: model.Patterns{"ci"}, CIOnly: true,
			}},
			"release-tool": {Restrictions: &model.CapabilityRestrictions{
				Templates: model.Patterns{"release-*"}, RequiresPerson: true,
			}},
			"nowhere":  {Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{}}},
			"disabled": {Enabled: boolp(false)},
		},
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				"release-coord": {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
			},
			RawScopes: map[string]model.CapabilityRestrictions{
				"*:write":   {RequiresPerson: true},
				"secrets:*": {Kinds: model.Patterns{}},
			},
		},
	}
}

func repo(s string) *model.RepoRef {
	r, err := model.ParseRepo(s)
	if err != nil {
		panic(err)
	}
	return &r
}

func TestCatalogueValidateGrants(t *testing.T) {
	cat := testCatalogue()
	cases := []struct {
		name    string
		gc      model.GrantContext
		refused string
		person  bool
	}{
		{name: "unlisted capability is not the catalogue's to judge",
			gc: model.GrantContext{Kind: "task", Grants: []string{"memory"}}},
		{name: "repo admitted", gc: model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "task", Grants: []string{"self-debug"}}},
		{name: "repo refused", gc: model.GrantContext{Repo: repo("other/repo"), Kind: "task", Grants: []string{"self-debug"}},
			refused: "not granted on other/repo"},
		{name: "no-repo refused", gc: model.GrantContext{Kind: "task", Grants: []string{"self-debug"}},
			refused: "not granted on none"},
		{name: "kind refused", gc: model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "review", Grants: []string{"self-debug"}},
			refused: "not granted to a review"},
		{name: "ci_only refuses a task", gc: model.GrantContext{Kind: "task", Grants: []string{"gcp-key:prod"}},
			refused: "CI-only"},
		{name: "ci_only admits ci", gc: model.GrantContext{Kind: "ci", Grants: []string{"gcp-key:prod"}}},
		{name: "template refused for ad-hoc", gc: model.GrantContext{Kind: "task", Grants: []string{"release-tool"}},
			refused: "ad-hoc"},
		{name: "template admitted and needs a person", gc: model.GrantContext{Kind: "task", TemplateID: "template-9", TemplateName: "release-weekly", Grants: []string{"release-tool"}},
			person: true},
		{name: "empty repos admits nobody", gc: model.GrantContext{Repo: repo("a/b"), Kind: "task", Grants: []string{"nowhere"}},
			refused: "not granted on a/b"},
		{name: "disabled in-process", gc: model.GrantContext{Kind: "task", Grants: []string{"disabled"}}, refused: "disabled"},
		{name: "method admitted", gc: model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "task",
			Grants: []string{`https://capabilities.internal/gcp#gcp-key={"project":"grain-dev"}`}}},
		{name: "requires_person_when", gc: model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "task",
			Grants: []string{`https://capabilities.internal/gcp#gcp-key={"project":"grain-prod"}`}}, person: true},
		{name: "argument not allowed", gc: model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "task",
			Grants: []string{`https://capabilities.internal/gcp#gcp-key={"project":"elsewhere"}`}}, refused: "not one settings/capabilities.yml allows"},
		{name: "unlisted method", gc: model.GrantContext{Kind: "task",
			Grants: []string{"https://capabilities.internal/gcp#other"}}, refused: "lists no method"},
		{name: "disabled server", gc: model.GrantContext{Kind: "task",
			Grants: []string{"https://capabilities.internal/off#x"}}, refused: "disabled"},
		{name: "preset needs a person", gc: model.GrantContext{Kind: "task", Grants: []string{"api:release-coord@ai-on-gke/grain"}}, person: true},
		{name: "raw write scope needs a person", gc: model.GrantContext{Kind: "task", Grants: []string{"api:tasks:write"}}, person: true},
		{name: "raw read scope is free", gc: model.GrantContext{Kind: "task", Grants: []string{"api:tasks:read"}}},
		{name: "raw secrets scope refused", gc: model.GrantContext{Kind: "director", Grants: []string{"api:secrets:read"}},
			refused: "secrets:*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := cat.ValidateGrants(tc.gc)
			if tc.refused != "" {
				var refusal *model.GrantRefusal
				if !errors.As(err, &refusal) || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("ValidateGrants = %v, want a refusal saying %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateGrants refused: %v", err)
			}
			if got := len(v.RequiresPerson) > 0; got != tc.person {
				t.Fatalf("RequiresPerson = %v, want person %v", v.RequiresPerson, tc.person)
			}
		})
	}
}

func TestNilCatalogueAdmitsEverything(t *testing.T) {
	var cat *model.CapabilityCatalogue
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{"anything"}}); err != nil {
		t.Fatal(err)
	}
	if err := cat.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogueValidateConsistency(t *testing.T) {
	cat := testCatalogue()
	if err := cat.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	bad := testCatalogue()
	bad.Defaults.Task = []string{"gcp-key:prod"}
	bad.Defaults.Repos = map[string]model.CatalogueRepoDefaults{"other/repo": {Task: []string{"self-debug"}}}
	bad.InProcess["x"] = model.CatalogueInProcess{Secret: "plaintext", Restrictions: &model.CapabilityRestrictions{
		CIOnly: true, Kinds: model.Patterns{"task"},
	}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate admitted an inconsistent catalogue")
	}
	for _, want := range []string{"defaults.task", "defaults.repos[other/repo].task", "in_process.x: secret", "ci_only is set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want it to mention %q", err, want)
		}
	}
	// A deployment-wide default is not refused for a repository rule it
	// cannot be measured against.
	ok := testCatalogue()
	ok.Defaults.Task = []string{"self-debug"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate refused a repo-restricted deployment default: %v", err)
	}
}

// The catalogue is stored inside capability_policy as JSON, and a
// `repos: []` (nobody) must not come back as an unwritten list (everybody).
func TestCatalogueSurvivesThePolicyColumn(t *testing.T) {
	p := model.CapabilityPolicy{Catalogue: testCatalogue()}
	text, err := model.EncodeCapabilityPolicy(p)
	if err != nil || text == "" {
		t.Fatalf("EncodeCapabilityPolicy = %q, %v", text, err)
	}
	back, err := model.DecodeCapabilityPolicy(text)
	if err != nil {
		t.Fatal(err)
	}
	r := back.Catalogue.InProcess["nowhere"].Restrictions
	if r.Repos == nil || len(r.Repos) != 0 {
		t.Fatalf("repos: [] came back as %#v", r.Repos)
	}
	if back.Catalogue.InProcess["self-debug"].Restrictions.Templates != nil {
		t.Fatal("an unwritten templates list came back written")
	}
	a, _ := json.Marshal(p.Catalogue)
	b, _ := json.Marshal(back.Catalogue)
	if string(a) != string(b) {
		t.Fatalf("round trip changed the catalogue:\n%s\n%s", a, b)
	}
}

func TestCatalogueKindOf(t *testing.T) {
	if got := model.CatalogueKindOf(model.Task{}); got != model.CatalogueKindTask {
		t.Fatalf("plain task is %q", got)
	}
	if got := model.CatalogueKindOf(model.Task{Origin: model.Origin{Reason: model.ReasonReview}}); got != model.CatalogueKindReview {
		t.Fatalf("review task is %q", got)
	}
}
