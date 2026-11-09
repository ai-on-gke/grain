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
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCanonicalGrantMapsEachSpelling(t *testing.T) {
	for _, tc := range []struct {
		in, capability, with string
	}{
		{"api:ci:write", APIRef("ci:write"), ""},
		{"api:ci:write@acme/widgets", APIRef("ci:write"), `{"repo":"acme/widgets"}`},
		{"api:task-admin", APIRef("task-admin"), ""},
		{"api:task-admin@Acme/Widgets", APIRef("task-admin"), `{"repo":"Acme/Widgets"}`},
		{"repo-read:acme/widgets", RepoReadRef, `{"repo":"acme/widgets"}`},
		{"repo-write:acme/widgets", RepoWriteRef, `{"repo":"acme/widgets"}`},
		{APIRef("ci:write"), APIRef("ci:write"), ""},
		{APIRef("ci:write") + `={"repo":"acme/widgets"}`, APIRef("ci:write"), `{"repo":"acme/widgets"}`},
		{GCPKeyServerGrant("ci"), GCPKeyRef, `{"permission_set":"ci","primary":true}`},
		{"github-sandbox", "github-sandbox", ""},
		{"self-debug", "self-debug", ""},
		{"api:", "api:", ""},
		{"api:ci:write@not a repo", "api:ci:write@not a repo", ""},
	} {
		g := CanonicalGrant(tc.in)
		if g.Capability != tc.capability || string(g.With) != tc.with {
			t.Errorf("CanonicalGrant(%q) = {%q, %s}, want {%q, %s}", tc.in, g.Capability, g.With, tc.capability, tc.with)
		}
	}
}

func TestLegacyGrantStringRefusesArgumentsNoSpellingCarries(t *testing.T) {
	if _, ok := LegacyGrantString(Grant{Capability: APIRef("ci:write"), With: json.RawMessage(`{"repo":"a/b","x":1}`)}); ok {
		t.Errorf("LegacyGrantString accepted an api grant with an argument besides repo")
	}
	if _, _, err := ParseAPIGrant(APIRef("ci:write") + `={"bogus":true}`); err == nil {
		t.Errorf("ParseAPIGrant accepted an api reference with an argument besides repo")
	}
	if _, err := NormalizeCapabilityNames([]string{APIRef("no-such-scope")}, nil); err == nil {
		t.Errorf("NormalizeCapabilityNames accepted an api reference naming no scope")
	}
}

// TestWritersKeepTodaysSpelling is slice 2's "write one": a reference
// sent in is recorded as the api:<scope> an older binary reads.
func TestWritersKeepTodaysSpelling(t *testing.T) {
	names, err := NormalizeCapabilityNames([]string{
		APIRef("ci:write") + `={"repo":"acme/widgets"}`, "api:ci:write@acme/widgets", APIRef("task-admin"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api:ci:write@acme/widgets", "api:task-admin"}; !slices.Equal(names, want) {
		t.Errorf("NormalizeCapabilityNames = %v, want %v", names, want)
	}
	grants, err := NormalizeCapabilityGrants([]Grant{
		{Capability: APIRef("merge:force"), Via: GrantByLabel},
		{Capability: APIRef("ci:read"), With: json.RawMessage(`{"repo":"acme/widgets"}`), Via: GrantByDefault},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Grant{{Capability: "api:merge:force", Via: GrantByLabel}, {Capability: "api:ci:read@acme/widgets", Via: GrantByDefault}}
	if !slices.EqualFunc(grants, want, Grant.Equal) {
		t.Errorf("NormalizeCapabilityGrants = %+v, want %+v", grants, want)
	}
}

func TestSplitRequestGrants(t *testing.T) {
	got, err := SplitRequestGrants([]Grant{
		{Capability: APIRef("ci:write"), With: json.RawMessage(`{"repo":"acme/widgets"}`)},
		{Capability: RepoReadRef, With: json.RawMessage(`{"repo":"acme/docs"}`)},
		{Capability: RepoWriteRef, With: json.RawMessage(`{"repo":"acme/site","base":"release"}`)},
		{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"acme/widgets","base":""}`)},
		{Capability: RepoWriteRef, With: json.RawMessage(`{"force":true,"refs":["refs/heads/*","refs/tags/*"]}`)},
		{Capability: "self-debug"},
	}, GrantSpelling{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api:ci:write@acme/widgets", GitPushAnyRefGrant, "self-debug"}; !slices.Equal(got.Capabilities, want) {
		t.Errorf("Capabilities = %v, want %v", got.Capabilities, want)
	}
	if len(got.Reads) != 1 || got.Reads[0].String() != "acme/docs" {
		t.Errorf("Reads = %v", got.Reads)
	}
	if len(got.Writes) != 1 || got.Writes[0].Repo.String() != "acme/site" || got.Writes[0].Base != "release" {
		t.Errorf("Writes = %v", got.Writes)
	}
	if got.Target == nil || got.Target.Repo.String() != "acme/widgets" || got.Target.Base != "" {
		t.Errorf("Target = %v", got.Target)
	}

	// Both spellings of one kind are refused rather than merged, and target
	// refusals name the field(s) the request actually sent.
	for _, tc := range []struct {
		g      Grant
		legacy GrantSpelling
		want   string
	}{
		{Grant{Capability: "api:ci:read"}, GrantSpelling{Capabilities: true}, "and capabilities both name capability grants"},
		{Grant{Capability: RepoReadRef, With: json.RawMessage(`{"repo":"a/b"}`)}, GrantSpelling{Reads: true}, "and reads both name read access"},
		{Grant{Capability: RepoWriteRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{Writes: true}, "and writes both name write targets"},
		{Grant{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{Target: true}, "the request's repo field both name the task's repository"},
		{Grant{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{TargetFields: []string{"branch"}}, "the request's branch field both name the task's repository"},
		{Grant{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{TargetFields: []string{"base"}}, "the request's base field both name the task's repository"},
		{Grant{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{TargetFields: []string{"no_repo"}}, "the request's no_repo field both name the task's repository"},
		{Grant{Capability: RepoTargetRef, With: json.RawMessage(`{"repo":"a/b","base":""}`)}, GrantSpelling{TargetFields: []string{"repo", "branch"}}, "the request's repo and branch fields both name the task's repository"},
	} {
		_, err := SplitRequestGrants([]Grant{tc.g}, tc.legacy)
		if err == nil {
			t.Errorf("SplitRequestGrants(%s, %+v) merged both spellings", tc.g.Key(), tc.legacy)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("SplitRequestGrants(%s, %+v) = %q, want substring %q", tc.g.Key(), tc.legacy, err.Error(), tc.want)
		}
	}
	// A kind sent only once is fine beside another kind's old spelling.
	if _, err := SplitRequestGrants([]Grant{{Capability: "api:ci:read"}}, GrantSpelling{Reads: true, Writes: true, Target: true}); err != nil {
		t.Errorf("SplitRequestGrants refused a capability beside reads/writes/repo: %v", err)
	}
}

// fidelityStore is the fixture store of unified-grants §3's fidelity
// test: every spelling of grant a task_grant row (and the deployment's
// and repositories' default and CI capability lists) holds today.
var fidelityStore = []string{
	"api:ci:read",
	"api:ci:write",
	"api:ci:write@acme/widgets",
	"api:ci-read@acme/widgets",
	"api:tasks:read",
	"api:tasks:write",
	"api:task-admin",
	"api:self-debug",
	"api:merge:force",
	"api:secrets:write",
	"api:settings:write@acme/widgets",
	"api:deployment-ops",
	"api:memory",
	"self-debug",
	"self-upgrade",
	"pipeline-control",
	"team-broadcast",
	"github-sandbox",
	"read-issues",
	"db-write",
	"self-repair",
	"git-push-any-ref",
	GitPushAnyRefGrant,
	GrainJournalGrant,
	GrainSourceGrant,
	GrainSQLWriteGrant,
	GrainHostExecGrant,
	GCPKeyServerGrant("ci"),
	RepoCredentialGrant("ops"),
	RepoReadGrant(RepoRef{Owner: "acme", Name: "docs"}),
	RepoWriteGrant(RepoRef{Owner: "acme", Name: "site"}),
}

// referenceSpelling is s as a slice 3 store will hold it: the canonical
// reference, with its {repo} if any. withEmpty spells an argument-less
// one as a normalized grant records it (={}).
func referenceSpelling(s string, withEmpty bool) string {
	g := CanonicalGrant(s)
	if withEmpty && len(g.With) == 0 && strings.HasPrefix(g.Capability, APICapabilityServerID+"#") {
		return g.Capability + "={}"
	}
	return g.Key()
}

// TestFidelityRoundTrip: old -> CanonicalGrant -> old again is
// byte-identical for every row of the fixture store.
func TestFidelityRoundTrip(t *testing.T) {
	for _, s := range fidelityStore {
		back, ok := LegacyGrantString(CanonicalGrant(s))
		if !ok || back != s {
			t.Errorf("LegacyGrantString(CanonicalGrant(%q)) = %q, %v; want it back unchanged", s, back, ok)
		}
		for _, withEmpty := range []bool{false, true} {
			if got := StoredGrantSpelling(referenceSpelling(s, withEmpty)); got != s {
				t.Errorf("StoredGrantSpelling(%q) = %q, want %q", referenceSpelling(s, withEmpty), got, s)
			}
		}
	}
	// The retired repo-read:/repo-write: spellings read as the reference
	// schema migration 88 stores.
	for legacy, stored := range map[string]string{
		"repo-read:acme/docs":  RepoReadGrant(RepoRef{Owner: "acme", Name: "docs"}),
		"repo-write:acme/site": RepoWriteGrant(RepoRef{Owner: "acme", Name: "site"}),
	} {
		if CanonicalGrantKey(legacy) != CanonicalGrantKey(stored) {
			t.Errorf("CanonicalGrantKey(%q) = %q, want %q's", legacy, CanonicalGrantKey(legacy), stored)
		}
	}
}

// fidelityTasks are the fixture's tasks, each a proposer and a child of
// every other: grant sets drawn from fidelityStore and repository access
// in each shape a task holds it.
func fidelityTasks() []Task {
	widgets := RepoRef{Owner: "acme", Name: "widgets"}
	site := RepoRef{Owner: "acme", Name: "site"}
	docs := RepoRef{Owner: "acme", Name: "docs"}
	cfg := RepoRef{Owner: "acme", Name: "grain-config"}
	label := func(caps ...string) []Grant {
		out := make([]Grant, len(caps))
		for i, c := range caps {
			out[i] = Grant{Capability: c, Via: GrantByLabel}
		}
		return out
	}
	return []Task{
		{ID: "none"},
		{ID: "ci", Target: &widgets, Grants: label("api:ci:read", "api:ci:write")},
		{ID: "ci-limited", Target: &widgets, Grants: label("api:ci:write@acme/widgets")},
		{ID: "admin", Target: &widgets, Grants: label("api:task-admin", "api:merge:force", "self-debug")},
		{ID: "dangerous", Target: &widgets, Grants: label("api:secrets:write", "db-write", GrainHostExecGrant)},
		{ID: "mixed", Target: &widgets, Base: "release", Grants: append(label("api:memory", GCPKeyServerGrant("ci"),
			GrainJournalGrant, "github-sandbox"), Grant{Capability: "api:ci-read@acme/widgets", Via: GrantByDefault})},
		{ID: "writes", Target: &widgets, Reads: []RepoRef{docs},
			Writes: []WriteTarget{{Repo: site, Base: ""}, {Repo: docs, Base: "main"}}, Grants: label("api:tasks:read")},
		{ID: "site", Target: &site, Grants: label("api:tasks:read")},
		{ID: "site-writes", Target: &site, Writes: []WriteTarget{{Repo: widgets}}, Grants: label("api:tasks:read")},
		{ID: "config", Target: &cfg, Grants: label("api:settings:write@acme/widgets", "api:deployment-ops")},
		{ID: "push-any", Target: &widgets, Grants: label(GitPushAnyRefGrant, RepoCredentialGrant("ops"), "git-push-any-ref")},
	}
}

// respelled is t with every grant in its reference spelling, and its
// repository access read back from the grants it projects to
// (TaskRepoGrants, SplitRequestGrants).
func respelled(t *testing.T, task Task, withEmpty bool) Task {
	t.Helper()
	out := task
	out.Grants = make([]Grant, len(task.Grants))
	for i, g := range task.Grants {
		g.Capability = referenceSpelling(g.Capability, withEmpty)
		out.Grants[i] = g
	}
	split, err := SplitRequestGrants(TaskRepoGrants(task), GrantSpelling{})
	if err != nil {
		t.Fatalf("SplitRequestGrants(TaskRepoGrants(%s)): %v", task.ID, err)
	}
	out.Reads, out.Writes, out.Target, out.Base = split.Reads, split.Writes, nil, ""
	if split.Target != nil {
		repo := split.Target.Repo
		out.Target, out.Base = &repo, split.Target.Base
	}
	return out
}

// fidelityPolicy names the api server's references, so the policy
// predicate has something to match either spelling against.
var fidelityPolicy = CapabilityPolicy{Rules: []CapabilityPolicyRule{
	{Capability: APIRef("merge:force"), Deny: true},
	{Capability: APIRef("ci:*"), Repos: []string{"acme/widgets"}},
}}

// predicates is every approval and policy answer for one (proposer,
// child) pair, as a printable value.
func predicates(child, parent Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "subset=%v ", GrantsSubsetOf(child.Grants, parent.Grants))
	fmt.Fprintf(&b, "escalated=%q ", EscalatedCapabilities(child, parent))
	fmt.Fprintf(&b, "lands=%v ", LandsWhereGranted(child, parent))
	fmt.Fprintf(&b, "ungrantedWrites=%v ", UngrantedWrites(child, parent))
	fmt.Fprintf(&b, "ungrantedReads=%v ", UngrantedReads(child, parent))
	fmt.Fprintf(&b, "sameBranch=%v ", SameBranch(child, parent))
	for _, g := range child.Grants {
		fmt.Fprintf(&b, "[dangerous=%v personOnly=%v api=%v",
			IsDangerousGrant(g.Capability), IsPersonOnlyGrant(g.Capability), IsDangerousAPIGrant(g.Capability))
		for _, repo := range []RepoRef{{Owner: "acme", Name: "widgets"}, {Owner: "acme", Name: "site"}} {
			err := fidelityPolicy.Permits(g.Capability, CapabilitySubject{Kind: CapabilityRunTask, Repo: &repo})
			fmt.Fprintf(&b, " policy(%s)=%v", repo, err != nil)
		}
		b.WriteString("] ")
	}
	set := APIGrantsOf(grantNames(child.Grants))
	fmt.Fprintf(&b, "scopes=%v", set.Scopes())
	for _, sc := range set.Scopes() {
		fmt.Fprintf(&b, " %s:%v", sc, set.Repos(sc))
	}
	return b.String()
}

func grantNames(grants []Grant) []string {
	out := make([]string, len(grants))
	for i, g := range grants {
		out[i] = g.Capability
	}
	return out
}

// TestFidelityPredicatesAgreeOnBothSpellings is unified-grants §3's
// fidelity matrix: for every (proposer, child) pair of the fixture, each
// approval and policy predicate gives the same answer whether the two
// tasks' grants are spelled the way slice 2 stores them or as canonical
// references -- child, proposer, or both respelled.
func TestFidelityPredicatesAgreeOnBothSpellings(t *testing.T) {
	tasks := fidelityTasks()
	var all strings.Builder
	for _, parent := range tasks {
		for _, child := range tasks {
			want := predicates(child, parent)
			all.WriteString(want + "\n")
			for _, withEmpty := range []bool{false, true} {
				rc, rp := respelled(t, child, withEmpty), respelled(t, parent, withEmpty)
				for name, pair := range map[string][2]Task{
					"child":  {rc, parent},
					"parent": {child, rp},
					"both":   {rc, rp},
				} {
					got := predicates(pair[0], pair[1])
					if got != want {
						t.Errorf("(%s proposes %s), %s respelled (={} %v):\n got %s\nwant %s",
							parent.ID, child.ID, name, withEmpty, got, want)
					}
				}
			}
		}
	}
	// The matrix is only worth having if each predicate answers both ways
	// somewhere in it.
	for _, both := range [][2]string{
		{"subset=true", "subset=false"}, {"escalated=[]", `escalated=["`}, {"lands=true", "lands=false"},
		{"ungrantedWrites=[]", "ungrantedWrites=[{"}, {"dangerous=true", "dangerous=false"},
		{"policy(acme/widgets)=true", "policy(acme/widgets)=false"}, {"policy(acme/site)=true", "policy(acme/site)=false"},
	} {
		for _, answer := range both {
			if !strings.Contains(all.String(), answer) {
				t.Errorf("no pair in the fixture answers %s", answer)
			}
		}
	}
}

// TestFidelityRepoGrantsRoundTrip: a task's repository access projected
// to grants and sorted back is the access it started with.
func TestFidelityRepoGrantsRoundTrip(t *testing.T) {
	for _, task := range fidelityTasks() {
		back := respelled(t, task, false)
		if !reflect.DeepEqual(back.Target, task.Target) || back.Base != task.Base ||
			!slices.EqualFunc(back.Reads, task.Reads, RepoRef.Same) ||
			!reflect.DeepEqual(back.Writes, task.Writes) {
			t.Errorf("%s: repo access round-tripped to target=%v base=%q reads=%v writes=%v, want %v %q %v %v",
				task.ID, back.Target, back.Base, back.Reads, back.Writes, task.Target, task.Base, task.Reads, task.Writes)
		}
		if got, want := WriteTargetsOfGrants(TaskRepoGrants(task)), WriteTargets(task); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Errorf("%s: WriteTargetsOfGrants = %v, want WriteTargets' %v", task.ID, got, want)
		}
	}
}
