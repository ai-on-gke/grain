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
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The scopes design/spec/director/director-capabilities.spec.md §3.3 and
// G7 name, spelled out here rather than read back from the package, so
// dropping one from the floor fails this test.
var tier3Scopes = []model.APIScope{"merge:force", "settings:write", "secrets:write", "connections:write", "repos:write"}

func TestDirectorForbiddenAPIScopesAreTheSpecsTier3List(t *testing.T) {
	t.Parallel()
	got := model.DirectorForbiddenAPIScopes()
	if !slices.Equal(got, tier3Scopes) {
		t.Fatalf("DirectorForbiddenAPIScopes() = %v, want %v", got, tier3Scopes)
	}
	for _, s := range got {
		if _, ok := model.APIScopeInfoOf(s); !ok {
			t.Errorf("%s is not a scope of the vocabulary", s)
		}
	}
	// A caller mutating the copy does not widen the floor.
	got[0] = "tasks:read"
	if !model.IsDirectorForbiddenAPIScope("merge:force") {
		t.Fatal("mutating the returned slice changed the floor")
	}
}

// Every spelling that reaches a Tier 3 scope is caught: the bare scope,
// one narrowed to a repository, a built-in scope set expanding to it,
// and the canonical …/api# reference.
func TestGrantAPIScopesReadsEverySpelling(t *testing.T) {
	t.Parallel()
	for grant, want := range map[string]model.APIScope{
		"api:merge:force":                                     "merge:force",
		" api:merge:force ":                                   "merge:force",
		"api:repos:write@acme/widgets":                        "repos:write",
		"api:" + model.DefaultAccessRole:                      "merge:force",
		"https://grain.dev/capabilities/api#merge:force":      "merge:force",
		"https://grain.dev/capabilities/api#secrets:write={}": "secrets:write",
		"self-upgrade":                                        "deployment:upgrade",
	} {
		if got := model.GrantAPIScopes(grant); !slices.Contains(got, want) {
			t.Errorf("GrantAPIScopes(%q) = %v, want it to include %s", grant, got, want)
		}
	}
	for _, grant := range []string{"", "self-debug-not-a-thing", "api:no-such-scope", model.GrainHostExecGrant} {
		for _, s := range model.GrantAPIScopes(grant) {
			if model.IsDirectorForbiddenAPIScope(s) {
				t.Errorf("GrantAPIScopes(%q) reaches %s", grant, s)
			}
		}
	}
}

func directorGC(grants ...string) model.GrantContext {
	return model.GrantContext{Kind: model.CatalogueKindDirector, Grants: grants}
}

// The floor binds with no settings/capabilities.yml at all (a nil
// catalogue), with an empty one, and with one that tries to hand the
// director each scope explicitly -- by a raw_scopes rule naming the
// director kind, and by a presets entry with no restrictions.
func TestDirectorFloorBindsWhateverTheCatalogueSays(t *testing.T) {
	t.Parallel()
	widening := &model.CapabilityCatalogue{APIScopes: model.CatalogueAPIScopes{
		Presets:   map[string]model.CataloguePreset{},
		RawScopes: map[string]model.CapabilityRestrictions{},
	}}
	for _, s := range tier3Scopes {
		widening.APIScopes.RawScopes[string(s)] = model.CapabilityRestrictions{Kinds: model.Patterns{model.CatalogueKindDirector}}
	}
	widening.APIScopes.RawScopes["*:write"] = model.CapabilityRestrictions{Kinds: model.Patterns(slices.Clone(model.CatalogueKinds))}
	widening.APIScopes.Presets["operator"] = model.CataloguePreset{Enabled: boolp(true)}
	widening.APIScopes.Presets[model.DefaultAccessRole] = model.CataloguePreset{Enabled: boolp(true)}
	if err := widening.Validate(); err != nil {
		t.Fatalf("the widening catalogue does not validate: %v", err)
	}

	cats := map[string]*model.CapabilityCatalogue{
		"nil":      nil,
		"empty":    {},
		"widening": widening,
	}
	grants := []string{"api:" + model.DefaultAccessRole}
	for _, s := range tier3Scopes {
		grants = append(grants, "api:"+string(s), "api:"+string(s)+"@acme/widgets")
	}
	for name, cat := range cats {
		for _, g := range grants {
			_, err := cat.ValidateGrants(directorGC("self-debug", g))
			var refusal *model.GrantRefusal
			if !errors.As(err, &refusal) {
				t.Errorf("%s catalogue: ValidateGrants(director, %s) = %v, want a refusal", name, g, err)
				continue
			}
			if !refusal.Builtin || refusal.Grant != g {
				t.Errorf("%s catalogue: refusal = %+v, want a built-in refusal of %s", name, refusal, g)
			}
			if !strings.Contains(refusal.Reason, "director") || !strings.Contains(refusal.Reason, "§3.3") {
				t.Errorf("%s catalogue: reason %q does not say why", name, refusal.Reason)
			}
			refusals := cat.Refusals(directorGC("self-debug", g, "api:tasks:read"))
			if len(refusals) != 1 || refusals[0].Grant != g || !refusals[0].Builtin {
				t.Errorf("%s catalogue: Refusals(director, %s) = %+v, want exactly that grant", name, g, refusals)
			}
		}
		// The director keeps everything else, and every other kind keeps
		// these scopes: the floor is the director's alone.
		if _, err := cat.ValidateGrants(directorGC("self-debug", "api:tasks:read", "api:merge:write", "api:repos:read")); err != nil {
			t.Errorf("%s catalogue refuses the director an ordinary grant: %v", name, err)
		}
		for _, kind := range []string{model.CatalogueKindTask, model.CatalogueKindSession, model.CatalogueKindCI, model.CatalogueKindReview, ""} {
			if r := model.DirectorFloorRefusal("api:merge:force", kind); r != nil {
				t.Errorf("DirectorFloorRefusal(%q) = %+v, want nil", kind, r)
			}
			if name == "nil" || name == "empty" {
				if _, err := cat.ValidateGrants(model.GrantContext{Kind: kind, Grants: []string{"api:repos:write"}}); err != nil {
					t.Errorf("%s catalogue refuses a %q run api:repos:write: %v", name, kind, err)
				}
			}
		}
	}
}

// An operator file still narrows the director: a catalogue rule refusing
// it a scope the floor leaves alone is applied as before.
func TestCatalogueStillNarrowsTheDirector(t *testing.T) {
	t.Parallel()
	cat := &model.CapabilityCatalogue{APIScopes: model.CatalogueAPIScopes{RawScopes: map[string]model.CapabilityRestrictions{
		"memory:write": {Kinds: model.Patterns{model.CatalogueKindTask}},
	}}}
	_, err := cat.ValidateGrants(directorGC("api:memory:write"))
	var refusal *model.GrantRefusal
	if !errors.As(err, &refusal) || refusal.Builtin {
		t.Fatalf("ValidateGrants = %v, want the catalogue's own refusal", err)
	}
}

func TestWithinDirectorFloorStripsTier3Scopes(t *testing.T) {
	t.Parallel()
	director := model.Task{
		ID:          "d",
		Interactive: true,
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
	if model.CatalogueKindOf(director) != model.CatalogueKindDirector {
		t.Fatalf("fixture is a %s, not the director", model.CatalogueKindOf(director))
	}
	held := []string{"api:" + model.DefaultAccessRole}
	set := model.WithinDirectorFloor(director, model.APIGrantsOf(held))
	for _, s := range tier3Scopes {
		if set.Has(s) {
			t.Errorf("the director's set still holds %s", s)
		}
	}
	if !set.Has("tasks:read") || !set.Has("merge:write") {
		t.Errorf("the director's set lost an ordinary scope: %v", set.Scopes())
	}
	plain := model.Task{ID: "p"}
	if !model.WithinDirectorFloor(plain, model.APIGrantsOf(held)).Has("merge:force") {
		t.Error("an ordinary task lost merge:force")
	}
	// The tag alone does not make a task the director.
	tagged := plain
	tagged.Tags = director.Tags
	if !model.WithinDirectorFloor(tagged, model.APIGrantsOf(held)).Has("merge:force") {
		t.Error("a task carrying only the director's tag was judged as the director")
	}
}

func TestValidateConsoleSessionsRefusesTier3OnTheDirector(t *testing.T) {
	t.Parallel()
	for _, s := range tier3Scopes {
		err := model.ValidateConsoleSessions([]model.ConsoleSession{
			{ID: model.DirectorSessionID, Capabilities: []string{"self-debug", "api:" + string(s)}},
		})
		if err == nil || !strings.Contains(err.Error(), string(s)) {
			t.Errorf("saving the director with api:%s = %v, want a refusal naming it", s, err)
		}
	}
	if err := model.ValidateConsoleSessions(model.DefaultConsoleSessions()); err != nil {
		t.Errorf("the shipped director does not validate: %v", err)
	}
	if err := model.ValidateConsoleSessions([]model.ConsoleSession{
		{ID: model.DirectorSessionID},
		{ID: "release", Capabilities: []string{"api:repos:write"}},
	}); err != nil {
		t.Errorf("another session holding api:repos:write was refused: %v", err)
	}
}
