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
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// The task's own example: a triage set composed from two built-ins, with
// the dangerous scope one of them carries denied, limited to one
// repository.
var opsTriage = APIScopeSet{Name: "ops/triage",
	Includes: []string{"grain/ci-read", "grain/task-admin", "ops/approve"},
	Deny:     []string{"tasks:admin"}, Repos: []string{"bwsalmon/grain"}, Version: 1}

var opsApprove = APIScopeSet{Name: "ops/approve", Scopes: []string{"tasks:*"}}

func TestScopeSetsComposeIncludesThenDeny(t *testing.T) {
	r, err := ResolveAPIScopeSets([]APIScopeSet{opsTriage, opsApprove})
	if err != nil {
		t.Fatal(err)
	}
	approve := r["ops/approve"]
	if want := []APIScope{"tasks:admin", "tasks:from-template", "tasks:read", "tasks:write"}; !slices.Equal(approve.Resolved, want) {
		t.Errorf("tasks:* = %v, want every tasks scope of the vocabulary %v", approve.Resolved, want)
	}
	if !approve.Person {
		t.Error("a set holding tasks:admin does not require a person")
	}
	triage := r["ops/triage"]
	want := []APIScope{"ci:read", "ci:write", "merge:write", "notifications:write", "tasks:from-template", "tasks:read", "tasks:write"}
	if !slices.Equal(triage.Resolved, want) {
		t.Errorf("ops/triage = %v, want %v", triage.Resolved, want)
	}
	if triage.Person {
		t.Error("ops/triage denies its only dangerous scope, but still requires a person")
	}
	for _, sc := range triage.Resolved {
		if lim := triage.RepoLimits[sc]; !slices.Equal(lim, []string{"bwsalmon/grain"}) {
			t.Errorf("%s reaches %v, want only bwsalmon/grain", sc, lim)
		}
	}
	if !r["grain/task-admin"].Builtin || r["ops/triage"].Builtin {
		t.Error("built-in flags are wrong")
	}
}

func TestScopeSetsRefuseCyclesAndUnknowns(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets []APIScopeSet
		want string
	}{
		{"cycle", []APIScopeSet{
			{Name: "a", Includes: []string{"b"}}, {Name: "b", Includes: []string{"c"}}, {Name: "c", Includes: []string{"a"}}},
			"cycle: a -> b -> c -> a"},
		{"self", []APIScopeSet{{Name: "a", Includes: []string{"a"}}}, "cycle: a -> a"},
		{"missing include", []APIScopeSet{{Name: "a", Includes: []string{"nope"}}}, `includes "nope"`},
		{"unknown scope", []APIScopeSet{{Name: "a", Scopes: []string{"tasks:everything"}}}, "not a scope"},
		{"unknown area", []APIScopeSet{{Name: "a", Scopes: []string{"bogus:*"}}}, "no area"},
		{"bare star", []APIScopeSet{{Name: "a", Scopes: []string{"*"}}}, "not a scope"},
		{"internal", []APIScopeSet{{Name: "a", Scopes: []string{"internal"}}}, "plumbing"},
		{"bad deny", []APIScopeSet{{Name: "a", Scopes: []string{"ci:read"}, Deny: []string{"ci:nope"}}}, "deny"},
		{"bad repo", []APIScopeSet{{Name: "a", Scopes: []string{"ci:read"}, Repos: []string{"nope"}}}, "repos"},
		{"empty", []APIScopeSet{{Name: "a"}}, "grants nothing"},
		{"duplicate", []APIScopeSet{{Name: "a", Scopes: []string{"ci:read"}}, {Name: "a", Scopes: []string{"ci:read"}}}, "twice"},
		{"bad name", []APIScopeSet{{Name: "Ops Triage", Scopes: []string{"ci:read"}}}, "lower-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveAPIScopeSets(tc.sets)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one saying %q", err, tc.want)
			}
		})
	}
}

func TestDangerousScopesForceRequiresPerson(t *testing.T) {
	for _, dangerous := range []string{"capabilities:admin", "secrets:write", "settings:write",
		"connections:write", "merge:force", "tasks:admin", "deployment:*", "deployment:reboot"} {
		r, err := ResolveAPIScopeSets([]APIScopeSet{{Name: "x", Scopes: []string{"ci:read", dangerous}, RequiresPerson: false}})
		if err != nil {
			t.Fatal(err)
		}
		if !r["x"].Person {
			t.Errorf("a set holding %s does not require a person", dangerous)
		}
		g, _, err := parseWith(t, r, "api:x")
		if err != nil || !g.Dangerous() {
			t.Errorf("api:x with %s is not a dangerous grant", dangerous)
		}
	}
	// Declared on without a dangerous scope: still only a person's.
	r, err := ResolveAPIScopeSets([]APIScopeSet{{Name: "x", Scopes: []string{"ci:read"}, RequiresPerson: true}})
	if err != nil || !r["x"].Person {
		t.Fatalf("declared requires_person was dropped: %v", err)
	}
}

func TestBuiltinScopeSetsCannotBeShadowedOrEdited(t *testing.T) {
	for _, name := range []string{"grain/task-admin", "grain/new-one", "task-admin", "release-manager"} {
		_, err := ResolveAPIScopeSets([]APIScopeSet{{Name: name, Scopes: []string{"secrets:write"}}})
		if err == nil {
			t.Errorf("a deployment set named %s was accepted", name)
		}
	}
	store := openScopeSetStore(t)
	if err := store.PutAPIScopeSet(context.Background(), APIScopeSet{Name: "grain/task-admin", Scopes: []string{"ci:read"}}); err == nil {
		t.Error("the store accepted an edit of grain/task-admin")
	}
	// Composable, though.
	if _, err := ResolveAPIScopeSets([]APIScopeSet{{Name: "mine", Includes: []string{"grain/task-admin"}}}); err != nil {
		t.Errorf("including a built-in: %v", err)
	}
}

func TestInvalidScopeSetsLeaveThePreviousInEffect(t *testing.T) {
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	if err := InstallAPIScopeSets([]APIScopeSet{opsApprove}); err != nil {
		t.Fatal(err)
	}
	broken := []APIScopeSet{{Name: "ops/approve", Scopes: []string{"ci:read"}}, {Name: "ops/other", Includes: []string{"missing"}}}
	if err := InstallAPIScopeSets(broken); err == nil {
		t.Fatal("an install with a missing include was accepted")
	}
	if APIScopeSetsError() == nil {
		t.Error("the refusal is not recorded for Settings to show")
	}
	r, ok := APIScopeSetNamed("ops/approve")
	if !ok || !slices.Contains(r.Resolved, "tasks:admin") {
		t.Errorf("ops/approve = %v, want the previous version (tasks:*) still in effect", r.Resolved)
	}
	if _, ok := APIScopeSetNamed("ops/other"); ok {
		t.Error("half of a refused install was applied")
	}
}

func TestGrantingBySetNameResolvesItsScopes(t *testing.T) {
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	if err := InstallAPIScopeSets([]APIScopeSet{opsTriage, opsApprove}); err != nil {
		t.Fatal(err)
	}
	set := APIGrantsOf([]string{"api:ops/triage"})
	if !set.Allows("tasks:write", "bwsalmon/grain") || set.Allows("tasks:write", "acme/other") {
		t.Error("ops/triage is not held to bwsalmon/grain")
	}
	if set.Has("tasks:admin") {
		t.Error("the denied scope was granted")
	}
	if IsDangerousAPIGrant("api:ops/triage") || !IsDangerousAPIGrant("api:ops/approve") {
		t.Error("the grant's dangerousness does not follow the set's requires_person")
	}
	// A grant's own @repo narrows the set's limit, and outside it the
	// grant reaches nothing.
	if s := APIGrantsOf([]string{"api:ops/triage@acme/other"}); !s.Empty() {
		t.Errorf("api:ops/triage@acme/other grants %v outside the set's repos", s.Scopes())
	}
	// mcpserver is handed the scopes, since it cannot read the sets.
	names := APIScopeSetGrantNames("api:ops/triage")
	if !slices.Contains(names, "api:ci:read@bwsalmon/grain") || slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "tasks:admin") }) {
		t.Errorf("expanded names = %v", names)
	}
	if got := APIScopeSetGrantNames("api:grain/task-admin"); !slices.Equal(got, []string{"api:grain/task-admin"}) {
		t.Errorf("a built-in was expanded: %v", got)
	}
	// And the {capability: api, with: {set: ...}} form names the same grant.
	var list CapabilityList
	if err := json.Unmarshal([]byte(`["self-debug", {"capability": "api", "with": {"set": ["ops/triage", "grain/ci-read"], "scopes": ["ci:read"]}}]`), &list); err != nil {
		t.Fatal(err)
	}
	if want := []string{"self-debug", "api:ci:read", "api:grain/ci-read", "api:ops/triage"}; !slices.Equal([]string(list), want) {
		t.Errorf("capabilities = %v, want %v", list, want)
	}
	if err := json.Unmarshal([]byte(`[{"capability": "api", "with": {"set": "nope"}}]`), &list); err == nil {
		t.Error("a with: set naming no set was accepted")
	}
}

// A set merged into the table grants nothing to anybody: a task holding
// no grant of it reaches no scope of it.
func TestDeclaringASetGrantsNothing(t *testing.T) {
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	store := openScopeSetStore(t)
	ctx := context.Background()
	if err := store.PutAPIScopeSet(ctx, opsApprove); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadAPIScopeSets(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := APIScopeSetNamed("ops/approve"); !ok {
		t.Fatal("the stored set was not installed")
	}
	if s := APIGrantsOf([]string{"self-debug-not-api", "repo-read:acme/x"}); !s.Empty() {
		t.Errorf("a task with no grant of the set holds %v", s.Scopes())
	}
}

func TestStoreRefusesASetThatBreaksAnother(t *testing.T) {
	store := openScopeSetStore(t)
	ctx := context.Background()
	if err := store.PutAPIScopeSet(ctx, opsApprove); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAPIScopeSet(ctx, opsTriage); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAPIScopeSet(ctx, "ops/approve"); err == nil {
		t.Error("deleting a set another includes was accepted")
	}
	if err := store.PutAPIScopeSet(ctx, APIScopeSet{Name: "ops/approve", Includes: []string{"ops/triage"}}); err == nil {
		t.Error("an edit closing a cycle was accepted")
	}
	got, err := store.ListAPIScopeSets(ctx)
	if err != nil || len(got) != 2 || !slices.Equal(got[1].Deny, []string{"tasks:admin"}) {
		t.Fatalf("listed %+v, %v", got, err)
	}
}

func parseWith(t *testing.T, r map[string]ResolvedAPIScopeSet, name string) (APIGrant, bool, error) {
	t.Helper()
	var sets []APIScopeSet
	for _, s := range r {
		if !s.Builtin {
			sets = append(sets, s.APIScopeSet)
		}
	}
	if err := InstallAPIScopeSets(sets); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	return ParseAPIGrant(name)
}

func openScopeSetStore(t *testing.T) *Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
}

// An @repo none of a repo-limited set's scopes reach grants nothing,
// which OutsideRepoLimit names; one some of its scopes reach, or a set
// with no limit, does not.
func TestAPIGrantOutsideRepoLimit(t *testing.T) {
	if err := InstallAPIScopeSets([]APIScopeSet{
		{Name: "walk-readers", Scopes: []string{"tasks:read"}, Repos: []string{"acme/widgets"}},
		{Name: "walk-mixed", Scopes: []string{"ci:read"}, Includes: []string{"walk-readers"}},
		{Name: "walk-open", Scopes: []string{"tasks:read"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = InstallAPIScopeSets(nil) })
	for name, outside := range map[string]bool{
		"api:walk-readers@acme/gadgets": true,
		"api:walk-readers@ACME/widgets": false,
		"api:walk-readers":              false,
		"api:walk-mixed@acme/gadgets":   false,
		"api:walk-open@acme/gadgets":    false,
		"api:tasks:read@acme/gadgets":   false,
	} {
		g, ok, err := ParseAPIGrant(name)
		if !ok || err != nil {
			t.Fatalf("ParseAPIGrant(%q) = %v, %v", name, ok, err)
		}
		if got := g.OutsideRepoLimit(); (got != nil) != outside {
			t.Errorf("%s: OutsideRepoLimit() = %v, want outside %v", name, got, outside)
		}
	}
}
