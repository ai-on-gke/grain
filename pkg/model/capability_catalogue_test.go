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
			refused: "not granted on a task with no repository (none)"},
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

// TestCatalogueRetiredInProcessBindsItsPreset is the upgraded store from
// docs/test-runs/2026-11-01-capability-api-presets.md: a catalogue saved
// before memory, release-notes and test-quarantine were retired keeps
// their restrictions under in_process, and the migrations rewrite every
// grant to the api: preset. The restriction must keep binding the preset
// rather than being dropped because nothing is called "memory" any more.
func TestCatalogueRetiredInProcessBindsItsPreset(t *testing.T) {
	cat := &model.CapabilityCatalogue{InProcess: map[string]model.CatalogueInProcess{
		"memory": {Enabled: boolp(false)},
		"release-notes": {Restrictions: &model.CapabilityRestrictions{
			Repos: model.Patterns{"acme/*"}, RequiresPerson: true,
		}},
		"test-quarantine":   {Restrictions: &model.CapabilityRestrictions{Kinds: model.Patterns{"task"}}},
		"deployment-health": {Restrictions: &model.CapabilityRestrictions{Kinds: model.Patterns{"director"}}},
	}}
	cases := []struct {
		name    string
		gc      model.GrantContext
		refused string
		person  bool
	}{
		{name: "disabled memory refuses api:memory", gc: model.GrantContext{Kind: "task", Grants: []string{"api:memory"}},
			refused: "in_process.memory"},
		{name: "disabled memory refuses a repo-limited api:memory", gc: model.GrantContext{Kind: "task", Grants: []string{"api:memory@acme/widgets"}},
			refused: "disabled"},
		{name: "release-notes repo restriction binds the preset", gc: model.GrantContext{Repo: repo("other/x"), Kind: "task", Grants: []string{"api:release-notes"}},
			refused: "not granted on other/x"},
		{name: "release-notes requires a person on an admitted repo", gc: model.GrantContext{Repo: repo("acme/widgets"), Kind: "task", Grants: []string{"api:release-notes"}},
			person: true},
		{name: "test-quarantine kind restriction binds the preset", gc: model.GrantContext{Kind: "ci", Grants: []string{"api:test-quarantine"}},
			refused: "not granted to"},
		{name: "test-quarantine admitted for its kind", gc: model.GrantContext{Kind: "task", Grants: []string{"api:test-quarantine"}}},
		{name: "a split retirement binds its api half", gc: model.GrantContext{Kind: "task", Grants: []string{"api:deployment:read"}},
			refused: "in_process.deployment-health"},
		{name: "an unrelated preset is not judged", gc: model.GrantContext{Kind: "task", Grants: []string{"api:team-broadcast"}}},
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
	director := model.Task{Interactive: true, Tags: []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Origin: model.Origin{Reason: model.ReasonConsoleSession}}
	if got := model.CatalogueKindOf(director); got != model.CatalogueKindDirector {
		t.Fatalf("director session is %q", got)
	}
	session := model.Task{Interactive: true, Tags: []string{model.ConsoleSessionTag("ops")},
		Origin: model.Origin{Reason: model.ReasonConsoleSession}}
	if got := model.CatalogueKindOf(session); got != model.CatalogueKindSession {
		t.Fatalf("console session is %q", got)
	}
	// The tag on an ordinary interactive task does not make it the
	// director: only grain files a session.
	forged := model.Task{Interactive: true, Tags: []string{model.ConsoleSessionTag(model.DirectorSessionID)}}
	if got := model.CatalogueKindOf(forged); got != model.CatalogueKindTask {
		t.Fatalf("an interactive task tagged as the director is %q, want task", got)
	}
}

func TestFoldRetiredInProcessMergesConservatively(t *testing.T) {
	cat := &model.CapabilityCatalogue{
		InProcess: map[string]model.CatalogueInProcess{
			"memory": {Enabled: boolp(false)},
			"release-notes": {Restrictions: &model.CapabilityRestrictions{
				Repos:          model.Patterns{"acme/*"},
				Kinds:          model.Patterns{"task", "session"},
				RequiresPerson: true,
			}},
			"test-quarantine": {Restrictions: &model.CapabilityRestrictions{
				Repos:     model.Patterns{"acme/widgets", "other/*"},
				Kinds:     model.Patterns{"task", "review"},
				Templates: model.Patterns{"quarantine-*"},
			}},
			"pipeline-control": {Restrictions: &model.CapabilityRestrictions{
				CIOnly: true,
			}},
			"self-debug": {Restrictions: &model.CapabilityRestrictions{
				Repos: model.Patterns{"ai-on-gke/grain"},
			}},
			"deployment-health": {Restrictions: &model.CapabilityRestrictions{
				Kinds: model.Patterns{"director"},
			}},
		},
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				"test-quarantine": {
					Enabled: boolp(true),
					Restrictions: &model.CapabilityRestrictions{
						Repos:     model.Patterns{"acme/*"},
						Kinds:     model.Patterns{"task", "director"},
						Templates: model.Patterns{"quarantine-fix"},
					},
				},
				"pipeline-control": {
					Restrictions: &model.CapabilityRestrictions{
						Kinds: model.Patterns{"task"},
					},
				},
			},
		},
	}

	if !cat.FoldRetiredInProcess() {
		t.Fatal("FoldRetiredInProcess = false, want true")
	}
	if cat.FoldRetiredInProcess() {
		t.Fatal("second FoldRetiredInProcess = true, want idempotent false")
	}
	if err := cat.Validate(); err != nil {
		t.Fatalf("Validate after fold: %v", err)
	}

	for _, retired := range []string{"memory", "release-notes", "test-quarantine", "pipeline-control"} {
		if _, ok := cat.InProcess[retired]; ok {
			t.Errorf("retired %q still in InProcess: %+v", retired, cat.InProcess)
		}
	}
	if _, ok := cat.InProcess["self-debug"]; !ok {
		t.Error("active self-debug was dropped from InProcess")
	}
	if _, ok := cat.InProcess["deployment-health"]; !ok {
		t.Error("split deployment-health was dropped from InProcess")
	}

	mem := cat.APIScopes.Presets["memory"]
	if mem.Enabled == nil || *mem.Enabled {
		t.Fatalf("Presets[memory] = %+v, want disabled", mem)
	}
	for _, g := range []string{"api:memory", "api:grain/memory", "memory"} {
		if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{g}}); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("ValidateGrants(%s) = %v, want disabled refusal", g, err)
		}
	}

	tq := cat.APIScopes.Presets["test-quarantine"]
	if tq.Restrictions == nil {
		t.Fatal("Presets[test-quarantine] has nil restrictions")
	}
	if got := []string(tq.Restrictions.Repos); len(got) != 1 || got[0] != "acme/widgets" {
		t.Errorf("merged test-quarantine repos = %v, want [acme/widgets]", got)
	}
	if got := []string(tq.Restrictions.Kinds); len(got) != 1 || got[0] != "task" {
		t.Errorf("merged test-quarantine kinds = %v, want [task]", got)
	}
	if got := []string(tq.Restrictions.Templates); len(got) != 1 || got[0] != "quarantine-fix" {
		t.Errorf("merged test-quarantine templates = %v, want [quarantine-fix]", got)
	}

	// pipeline-control had CIOnly: true in inProcess and Kinds: [task] in
	// presets -> disjoint kinds, so nobody may hold it and Validate passes.
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "ci", Grants: []string{"api:pipeline-control"}}); err == nil {
		t.Error("merged pipeline-control admitted ci, want refused because preset allowed only task")
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{"api:pipeline-control"}}); err == nil {
		t.Error("merged pipeline-control admitted task, want refused because in_process was ci_only")
	}
}

func TestRetiredGrainServerCapabilitiesAndPresetAliases(t *testing.T) {
	cat := &model.CapabilityCatalogue{
		InProcess: map[string]model.CatalogueInProcess{
			"grain-log":         {Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{"ai-on-gke/grain"}}},
			"deployment-health": {Restrictions: &model.CapabilityRestrictions{Kinds: model.Patterns{"director"}}},
			"db-read":           {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
			"db-write":          {Enabled: boolp(false)},
		},
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				model.ReleaseGateReadCapability: {Enabled: boolp(false)},
				"self-debug": {Restrictions: &model.CapabilityRestrictions{
					Repos:          model.Patterns{"ai-on-gke/grain"},
					RequiresPerson: true,
				}},
			},
		},
	}

	if _, err := cat.ValidateGrants(model.GrantContext{Repo: repo("other/repo"), Kind: "task", Grants: []string{model.GrainJournalGrant}}); err == nil || !strings.Contains(err.Error(), "in_process.grain-log") {
		t.Errorf("GrainJournalGrant on other/repo = %v, want in_process.grain-log refusal", err)
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{model.GrainHealthGrant}}); err == nil || !strings.Contains(err.Error(), "in_process.deployment-health") {
		t.Errorf("GrainHealthGrant for task = %v, want in_process.deployment-health refusal", err)
	}
	v, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{model.GrainSQLReadGrant}})
	if err != nil || len(v.RequiresPerson) != 1 {
		t.Errorf("GrainSQLReadGrant = (%+v, %v), want admitted with RequiresPerson from in_process.db-read", v, err)
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{model.GrainSQLWriteGrant}}); err == nil || !strings.Contains(err.Error(), "in_process.db-write") {
		t.Errorf("GrainSQLWriteGrant = %v, want disabled refusal from in_process.db-write", err)
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Kind: "ci", Grants: []string{model.ReleaseGateReadCapability}}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("bare release-gate-read = %v, want disabled refusal from api_scopes.presets.release-gate-read", err)
	}
	if _, err := cat.ValidateGrants(model.GrantContext{Repo: repo("other/repo"), Kind: "task", Grants: []string{"self-debug"}}); err == nil || !strings.Contains(err.Error(), "other/repo") {
		t.Errorf("bare self-debug on other/repo = %v, want repo refusal from api_scopes.presets.self-debug", err)
	}
	sdVerdict, err := cat.ValidateGrants(model.GrantContext{Repo: repo("ai-on-gke/grain"), Kind: "task", Grants: []string{"self-debug"}})
	if err != nil || len(sdVerdict.RequiresPerson) != 1 || sdVerdict.RequiresPerson[0] != "self-debug" {
		t.Errorf("bare self-debug on ai-on-gke/grain = (%+v, %v), want RequiresPerson [self-debug] from api_scopes.presets.self-debug", sdVerdict, err)
	}
	disabledSD := &model.CapabilityCatalogue{
		APIScopes: model.CatalogueAPIScopes{
			Presets: map[string]model.CataloguePreset{
				"self-debug": {Enabled: boolp(false)},
			},
		},
	}
	if _, err := disabledSD.ValidateGrants(model.GrantContext{Kind: "task", Grants: []string{"self-debug"}}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("bare self-debug with disabled api_scopes.presets.self-debug = %v, want disabled refusal", err)
	}

	merged := model.MergeCapabilityRestrictions(
		&model.CapabilityRestrictions{Templates: model.Patterns{"ops*"}},
		&model.CapabilityRestrictions{Templates: model.Patterns{"ops/nightly*"}},
	)
	if len(merged.Templates) != 0 {
		t.Errorf("MergeCapabilityRestrictions(ops*, ops/nightly*).Templates = %v, want [] because path.Match '*' does not cross '/'", merged.Templates)
	}

	for _, tc := range []struct {
		name string
		a, b *model.CapabilityRestrictions
	}{
		{
			name: "template bracket class vs literal bracket suffix",
			a:    &model.CapabilityRestrictions{Templates: model.Patterns{"a*]"}},
			b:    &model.CapabilityRestrictions{Templates: model.Patterns{"a[bc]"}},
		},
		{
			name: "repo bracket class vs literal bracket suffix",
			a:    &model.CapabilityRestrictions{Repos: model.Patterns{"acme/a*]"}},
			b:    &model.CapabilityRestrictions{Repos: model.Patterns{"acme/a[bc]"}},
		},
		{
			name: "template escaped star vs escaped literal",
			a:    &model.CapabilityRestrictions{Templates: model.Patterns{`a\*`}},
			b:    &model.CapabilityRestrictions{Templates: model.Patterns{`a\b`}},
		},
		{
			name: "template escaped backslash glob vs escaped hyphen",
			a:    &model.CapabilityRestrictions{Templates: model.Patterns{`tpl\\*`}},
			b:    &model.CapabilityRestrictions{Templates: model.Patterns{`tpl\-1`}},
		},
	} {
		got := model.MergeCapabilityRestrictions(tc.a, tc.b)
		if len(got.Repos) != 0 || len(got.Templates) != 0 {
			t.Errorf("%s: MergeCapabilityRestrictions = %+v, want empty intersection", tc.name, got)
		}
	}
}
