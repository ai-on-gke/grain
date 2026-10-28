package statecheck

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// grain/task-442: an agent proposes a scope set by pull request against
// tables/api_scope_set.json, and the check that pull request runs says
// what merging it would mean -- the resolved scopes, whether only a person
// may grant it, and how its effective scopes move from the base.
func TestReportsWhatAProposedScopeSetResolvesTo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	store := model.New(db)
	for _, set := range []model.APIScopeSet{
		{Name: "ops/triage", Includes: []string{"grain/ci-read", "grain/task-admin"},
			Deny: []string{"tasks:admin"}, Repos: []string{"bwsalmon/grain"}, Version: 2},
		{Name: "ops/keys", Scopes: []string{"secrets:write"}},
	} {
		if err := store.PutAPIScopeSet(ctx, set); err != nil {
			t.Fatal(err)
		}
	}
	// The base held ops/triage without ci:write, and no ops/keys.
	previous := []byte(`[{"name": "ops/triage", "description": "", "scopes": "[\"ci:read\",\"tasks:read\",\"tasks:write\",\"merge:write\"]",
		"includes": "[]", "repos": "[\"bwsalmon/grain\"]", "deny": "[]", "requires_person": 0, "version": 1, "deprecated": 0}]`)
	var out bytes.Buffer
	if err := APIScopeSets(ctx, db, &out, previous); err != nil {
		t.Fatalf("check: %v", err)
	}
	report := out.String()
	for _, want := range []string{
		"ops/triage (version 2): ci:read@bwsalmon/grain, ci:write@bwsalmon/grain, merge:write@bwsalmon/grain, notifications:write@bwsalmon/grain, tasks:read@bwsalmon/grain, tasks:write@bwsalmon/grain",
		"ops/keys: secrets:write\n    requires_person: yes",
		"grants it to nobody",
		"ops/keys: new\n    + secrets:write",
		"ops/triage:\n    + ci:write@bwsalmon/grain\n    + notifications:write@bwsalmon/grain",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

// A set that does not resolve fails the check -- the daemon would refuse
// the whole file and keep the sets already in effect, so the pull request
// is the one place to say so.
func TestFailsAScopeSetGrainWouldRefuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// Written as a merged file would write it, past the store's own
	// validation.
	if _, err := db.ExecContext(ctx, "INSERT INTO `api_scope_set` (`name`,`description`,`scopes`,`includes`,`repos`,`deny`) "+
		"VALUES ('a','','[]','[\"b\"]','[]','[]'), ('b','','[]','[\"a\"]','[]','[]')"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = APIScopeSets(ctx, db, &out, nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "tables/api_scope_set.json") {
		t.Fatalf("err = %v, want the cycle named against the file", err)
	}
}

// Dropping a set's repos limit adds no scope but widens every grant of it
// from those repositories to all of them, and the diff a person reads
// before merging has to say so rather than report that nothing moved.
func TestReportsAScopeSetsRepositoriesWidening(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup, err := ScratchStore()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := model.New(db).PutAPIScopeSet(ctx, model.APIScopeSet{Name: "ops/read", Scopes: []string{"ci:read"}}); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`[{"name": "ops/read", "description": "", "scopes": "[\"ci:read\"]",
		"includes": "[]", "repos": "[\"bwsalmon/grain\"]", "deny": "[]", "requires_person": 0, "version": 0, "deprecated": 0}]`)
	var out bytes.Buffer
	if err := APIScopeSets(ctx, db, &out, previous); err != nil {
		t.Fatalf("check: %v", err)
	}
	report := out.String()
	if strings.Contains(report, "no set's effective scopes change") ||
		!strings.Contains(report, "ops/read:\n    ~ ci:read: bwsalmon/grain -> every repository") {
		t.Errorf("report does not show the widening:\n%s", report)
	}
}
