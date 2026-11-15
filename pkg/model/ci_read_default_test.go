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

// A composition targeting a GitHub-CI repository is given
// api:ci-read@<target> by default, credited to the repository layer; one
// targeting a grain-CI repository is given grain#ci instead; and the
// opt-out leaves neither.
func TestApplyRepoCIGrantsDefaultsPerCIMode(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	sched := Schedule{ID: "s", Title: "t", Target: repo, Grants: []Grant{{Capability: "web-search", Via: GrantByLabel}}}

	github := ComposeScheduleTask(sched, nil)
	applyRepoCIGrants(&github, RepoCIDefaultsFor(false, CapabilityPolicy{}, repo))
	if len(github.Grants) != 2 || !github.Grants[1].Equal(DefaultCIReadGrant(repo)) || github.GrantSources[1] != LayerRepo {
		t.Errorf("github-mode grants = %+v sources %v, want web-search then the default ci-read from %q", github.Grants, github.GrantSources, LayerRepo)
	}
	if HasGrainCI([]string{github.Grants[1].Capability}) {
		t.Errorf("github-mode composition holds grain#ci")
	}
	if sched.Grants[0].Capability != "web-search" || len(sched.Grants) != 1 {
		t.Errorf("the schedule's own grants were rewritten: %+v", sched.Grants)
	}
	// Applying it again adds no duplicate.
	applyRepoCIGrants(&github, RepoCIDefaultsFor(false, CapabilityPolicy{}, repo))
	if len(github.Grants) != 2 {
		t.Errorf("second apply: %+v", github.Grants)
	}

	grain := ComposeScheduleTask(sched, nil)
	applyRepoCIGrants(&grain, RepoCIDefaultsFor(true, CapabilityPolicy{}, repo))
	if len(grain.Grants) != 2 || !IsGrainCIGrant(grain.Grants[1].Capability) {
		t.Errorf("grain-mode grants = %+v, want web-search then grain#ci", grain.Grants)
	}

	deny := CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: APIRef("ci-read"), Deny: true}}}
	off := ComposeScheduleTask(sched, nil)
	applyRepoCIGrants(&off, RepoCIDefaultsFor(false, deny, repo))
	if len(off.Grants) != 1 {
		t.Errorf("opted-out grants = %+v, want web-search alone", off.Grants)
	}
}

// The opt-out is a capability policy deny rule, deployment-wide or for the
// repositories it names; an unrelated rule leaves the default on.
func TestCIReadDefaultPermitted(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		name   string
		policy CapabilityPolicy
		want   bool
	}{
		{name: "no policy", want: true},
		{name: "deny everywhere", policy: CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: APIRef("ci-read"), Deny: true}}}},
		{name: "deny here", policy: CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: APIRef("ci-read"), Repos: []string{"acme/*"}, Deny: true}}}},
		{name: "deny elsewhere", want: true, policy: CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: APIRef("ci-read"), Repos: []string{"other/*"}, Deny: true}}}},
		{name: "unrelated deny", want: true, policy: CapabilityPolicy{Rules: []CapabilityPolicyRule{{Capability: APIRef("tasks:read"), Deny: true}}}},
	} {
		if got := CIReadDefaultPermitted(tc.policy, repo); got != tc.want {
			t.Errorf("%s: CIReadDefaultPermitted = %v, want %v", tc.name, got, tc.want)
		}
	}
	if CIReadDefaultPermitted(CapabilityPolicy{}, RepoRef{}) {
		t.Errorf("a task with no repository is given a CI read")
	}
}

// The default reaches its own repository's CI and nothing else, and is
// recognised only on that repository and only as a default.
func TestDefaultCIReadGrantIsReadOnlyAndRepoScoped(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	g := DefaultCIReadGrant(repo)
	set := APIGrantsOf([]string{g.Capability})
	if !set.Allows("ci:read", "acme/widgets") || set.Allows("ci:read", "acme/other") || set.Allows("ci:read", "") {
		t.Errorf("default ci-read reaches the wrong repositories")
	}
	for _, scope := range []APIScope{"ci:write", "tasks:read", "tasks:write", "merge:write", "memory:read", "deployment:read"} {
		if set.Has(scope) {
			t.Errorf("default ci-read opens %s", scope)
		}
	}
	if !IsDefaultCIReadGrant(g, repo) {
		t.Errorf("IsDefaultCIReadGrant(DefaultCIReadGrant(repo), repo) = false")
	}
	if IsDefaultCIReadGrant(g, RepoRef{Owner: "acme", Name: "other"}) {
		t.Errorf("the default on acme/widgets is recognised as acme/other's")
	}
	asked := g
	asked.Via = GrantByLabel
	if IsDefaultCIReadGrant(asked, repo) {
		t.Errorf("an asked-for ci-read is treated as the default")
	}
	// A proposal carrying it is not asking its proposer for anything.
	child := Task{Target: &repo, Grants: []Grant{g}}
	if got := GrantsBeyondCIReadDefault(child); len(got) != 0 {
		t.Errorf("GrantsBeyondCIReadDefault = %+v, want none", got)
	}
	if !GrantsSubsetOf(GrantsBeyondCIReadDefault(child), nil) || len(EscalatedCapabilities(child, Task{Target: &repo})) != 0 {
		t.Errorf("the default ci-read counts as an escalation")
	}
}
