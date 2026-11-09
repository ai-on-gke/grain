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
	"errors"
	"testing"
)

func TestGitCredentialOverrideFindsCanonicalAndLegacyGrant(t *testing.T) {
	grants := []Grant{
		{Capability: "gemini-key", Via: GrantByFolder},
		GitCredentialGrant("workflow"),
	}
	name, ok, err := GitCredentialOverride(grants)
	if err != nil {
		t.Fatalf("GitCredentialOverride canonical: %v", err)
	}
	if !ok || name != "workflow" {
		t.Errorf("name=%q ok=%v, want %q true", name, ok, "workflow")
	}

	legacyGrants := []Grant{
		{Capability: "github-credential:release-bot", Via: GrantByLabel},
	}
	name, ok, err = GitCredentialOverride(legacyGrants)
	if err != nil {
		t.Fatalf("GitCredentialOverride legacy: %v", err)
	}
	if !ok || name != "release-bot" {
		t.Errorf("legacy name=%q ok=%v, want %q true", name, ok, "release-bot")
	}
}

func TestGitCredentialOverrideIgnoresUnrelatedGrants(t *testing.T) {
	grants := []Grant{{Capability: "gemini-key", Via: GrantByFolder}}
	if _, ok, err := GitCredentialOverride(grants); err != nil || ok {
		t.Errorf("an unrelated capability should not be read as an override (ok=%v err=%v)", ok, err)
	}
}

func TestGitCredentialOverrideIsAbsentWithNoGrants(t *testing.T) {
	if _, ok, err := GitCredentialOverride(nil); err != nil || ok {
		t.Errorf("no grants at all should not be read as an override (ok=%v err=%v)", ok, err)
	}
}

func TestGitCredentialOverrideRefusesConflictingGrants(t *testing.T) {
	for _, grants := range [][]Grant{
		{GitCredentialGrant("bot-two"), GitCredentialGrant("release-bot")},
		{{Capability: "github-credential:bot-two", Via: GrantByLabel}, GitCredentialGrant("release-bot")},
		{{Capability: "github-credential:bot-two", Via: GrantByLabel}, {Capability: "github-credential:release-bot", Via: GrantByLabel}},
	} {
		_, _, err := GitCredentialOverride(grants)
		if !errors.Is(err, ErrConflictingGitCredentials) {
			t.Errorf("GitCredentialOverride(%v) = %v, want ErrConflictingGitCredentials", grants, err)
		}
	}
}

func TestGitCredentialCapabilityRoundTrips(t *testing.T) {
	id := GitCredentialCapability("release-bot")
	want := `https://grain.dev/capabilities/repo#credential={"credential":"release-bot"}`
	if id != want {
		t.Errorf("GitCredentialCapability = %q, want %q", id, want)
	}
	if name, ok := GitCredentialName(id); !ok || name != "release-bot" {
		t.Errorf("GitCredentialName(%q) = %q, %v, want %q true", id, name, ok, "release-bot")
	}
	if name, ok := GitCredentialName("github-credential:release-bot"); !ok || name != "release-bot" {
		t.Errorf("GitCredentialName(legacy) = %q, %v, want %q true", name, ok, "release-bot")
	}
	if got := CanonicalLegacyGrant("github-credential:release-bot"); got != want {
		t.Errorf("CanonicalLegacyGrant(github-credential:release-bot) = %q, want %q", got, want)
	}
	if _, ok := GitCredentialName("gemini-key"); ok {
		t.Error("an ordinary capability id should not read as a named GitHub token")
	}
	if _, ok := GitCredentialName(GitCredentialCapabilityPrefix); ok {
		t.Error("the bare prefix names no credential and should not read as one")
	}
}

func TestRepoReadCapabilityRoundTrips(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "tools"}
	id := RepoReadCapability(repo)
	if want := `https://grain.dev/capabilities/repo#read={"repo":"acme/tools"}`; id != want {
		t.Errorf("RepoReadCapability = %q, want %q", id, want)
	}
	// The retired bare spelling is read as the same grant indefinitely.
	for _, id := range []string{id, "repo-read:acme/tools", ` https://grain.dev/capabilities/repo#read={ "repo": "acme/tools" } `} {
		if got, ok := RepoReadTarget(id); !ok || !got.Same(repo) {
			t.Errorf("RepoReadTarget(%q) = %v, %v, want %v true", id, got, ok, repo)
		}
	}
	if _, ok := RepoReadTarget("gemini-key"); ok {
		t.Error("an ordinary capability id should not read as a repo-read capability")
	}
	if _, ok := RepoReadTarget(RepoReadCapabilityPrefix); ok {
		t.Error("the bare prefix names no repo and should not read as one")
	}
	if _, ok := RepoReadTarget("repo-read:invalid"); ok {
		t.Error("invalid repo name should not read as a repo-read target")
	}
}

func TestRepoWriteCapabilityRoundTrips(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "tools"}
	id := RepoWriteCapability(repo)
	if want := `https://grain.dev/capabilities/repo#write={"repo":"acme/tools"}`; id != want {
		t.Errorf("RepoWriteCapability = %q, want %q", id, want)
	}
	// The retired bare spelling is read as the same grant indefinitely.
	for _, id := range []string{id, "repo-write:acme/tools", ` https://grain.dev/capabilities/repo#write={ "repo": "acme/tools" } `} {
		if got, ok := RepoWriteTarget(id); !ok || !got.Same(repo) {
			t.Errorf("RepoWriteTarget(%q) = %v, %v, want %v true", id, got, ok, repo)
		}
	}
	if _, ok := RepoWriteTarget("gemini-key"); ok {
		t.Error("an ordinary capability id should not read as a repo-write capability")
	}
	if _, ok := RepoWriteTarget(RepoWriteCapabilityPrefix); ok {
		t.Error("the bare prefix names no repo and should not read as one")
	}
	if _, ok := RepoWriteTarget("repo-write:invalid"); ok {
		t.Error("invalid repo name should not read as a repo-write target")
	}
}

func TestGitCredentialGrantIsAppliedByLabel(t *testing.T) {
	g := GitCredentialGrant("workflow")
	if g.Via != GrantByLabel {
		t.Errorf("Via = %q, want %q -- a grain-github-<name> label is what produces this grant", g.Via, GrantByLabel)
	}
}

func TestGCPKeyCapabilityRoundTrips(t *testing.T) {
	if got := GCPKeyCapability(""); got != "gcp-key" {
		t.Errorf("GCPKeyCapability(\"\") = %q, want \"gcp-key\"", got)
	}
	if got := GCPKeyCapability("default"); got != "gcp-key" {
		t.Errorf("GCPKeyCapability(\"default\") = %q, want \"gcp-key\"", got)
	}
	if got := GCPKeyCapability("deploy"); got != "gcp-key:deploy" {
		t.Errorf("GCPKeyCapability(\"deploy\") = %q, want \"gcp-key:deploy\"", got)
	}
	if name, ok := GCPKeyName("gcp-key"); !ok || name != "" {
		t.Errorf("GCPKeyName(\"gcp-key\") = (%q, %v), want (\"\", true)", name, ok)
	}
	if name, ok := GCPKeyName("gcp-key:deploy"); !ok || name != "deploy" {
		t.Errorf("GCPKeyName(\"gcp-key:deploy\") = (%q, %v), want (\"deploy\", true)", name, ok)
	}
	if _, ok := GCPKeyName("gemini-key"); ok {
		t.Error("an ordinary capability id should not read as a GCP key capability")
	}
	if _, ok := GCPKeyName(GCPKeyCapabilityPrefix); ok {
		t.Error("bare prefix names no GCP key and should not read as one")
	}
	grant := GCPKeyGrant("deploy")
	if grant.Capability != "gcp-key:deploy" || grant.Via != GrantByLabel {
		t.Errorf("GCPKeyGrant(\"deploy\") = %+v, want Capability: \"gcp-key:deploy\", Via: %q", grant, GrantByLabel)
	}
}

func TestGrantsSubsetOfIsTrueWithNoGrantsAtAll(t *testing.T) {
	if !GrantsSubsetOf(nil, nil) {
		t.Error("no grants requested is a subset of no grants allowed")
	}
	if !GrantsSubsetOf(nil, []Grant{{Capability: "gemini-key", Via: GrantByLabel}}) {
		t.Error("no grants requested is a subset of any allowed set")
	}
}

func TestGrantsSubsetOfComparesByCapabilityNameOnly(t *testing.T) {
	grants := []Grant{{Capability: "gemini-key", Via: GrantByDirective}}
	allowed := []Grant{{Capability: "gemini-key", Via: GrantByLabel, Folder: ParseFolder("acme/widgets")}}
	if !GrantsSubsetOf(grants, allowed) {
		t.Error("same capability name should match regardless of Via or Folder")
	}
}

func TestGrantsSubsetOfIsFalseForAWiderRequest(t *testing.T) {
	grants := []Grant{{Capability: "gemini-key", Via: GrantByLabel}, {Capability: "self-repair", Via: GrantByLabel}}
	allowed := []Grant{{Capability: "gemini-key", Via: GrantByLabel}}
	if GrantsSubsetOf(grants, allowed) {
		t.Error("a capability not in allowed should make this false")
	}
}

func TestRepoRefSameFoldsCase(t *testing.T) {
	widgets := RepoRef{Owner: "acme", Name: "widgets"}
	for _, tc := range []struct {
		a, b RepoRef
		want bool
	}{
		{widgets, widgets, true},
		{RepoRef{Owner: "ACME", Name: "Widgets"}, widgets, true},
		{RepoRef{Owner: "acme", Name: "gadgets"}, widgets, false},
		{RepoRef{Owner: "other", Name: "widgets"}, widgets, false},
	} {
		if got := tc.a.Same(tc.b); got != tc.want {
			t.Errorf("%s.Same(%s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSameBranchComparesTheTargetAndTheBase(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := &RepoRef{Owner: "acme", Name: "gadgets"}
	for _, tc := range []struct {
		name string
		a, b Task
		want bool
	}{
		{"both on the default branch", Task{Target: widgets}, Task{Target: widgets}, true},
		{"both on the same named base",
			Task{Target: widgets, Base: "release/2.0"},
			Task{Target: widgets, Base: "release/2.0"}, true},
		{"a named base against the default branch",
			Task{Target: widgets, Base: "release/2.0"},
			Task{Target: widgets}, false},
		{"different bases", Task{Target: widgets, Base: "release/2.0"},
			Task{Target: widgets, Base: "release/1.0"}, false},
		{"different repos", Task{Target: widgets}, Task{Target: gadgets}, false},
		// GitHub resolves owner and name case-insensitively, so these
		// are one branch of one repo however they were typed.
		{"the same repo written differently",
			Task{Target: &RepoRef{Owner: "ACME", Name: "Widgets"}},
			Task{Target: widgets}, true},
		{"neither writes anywhere", Task{}, Task{}, true},
		{"one writes nowhere", Task{}, Task{Target: widgets}, false},
	} {
		if got := SameBranch(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameBranch = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// LandsWhereGranted is SameBranch widened by the extra write targets a
// task declares, and by nothing else: a child lands where its parent was
// given permission to land work, or it inherits nothing.
func TestLandsWhereGrantedReadsTheDeclaredWriteTargets(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	state := &RepoRef{Owner: "acme", Name: "widgets-state"}
	docs := &RepoRef{Owner: "acme", Name: "docs"}
	parent := Task{
		Target: widgets, Base: "release/2.0",
		Writes: []WriteTarget{
			{Repo: *widgets, Base: "release/1.0"},
			{Repo: *state, Base: "deploy"},
			{Repo: *state, Base: "staging"},
			{Repo: *docs},
		},
	}
	for _, tc := range []struct {
		name  string
		child Task
		want  bool
	}{
		{"the parent's own branch", Task{Target: widgets, Base: "release/2.0"}, true},
		{"the parent's repo, second declared branch", Task{Target: widgets, Base: "release/1.0"}, true},
		{"the parent's repo, other undeclared branch", Task{Target: widgets}, false},
		{"a declared write target, on its first declared branch",
			Task{Target: state, Base: "deploy"}, true},
		{"a declared write target, on its second declared branch",
			Task{Target: state, Base: "staging"}, true},
		{"a declared write target, on some other branch",
			Task{Target: state, Base: "main"}, false},
		{"a declared write target whose branch is the repo's default",
			Task{Target: docs}, true},
		// "" is left unresolved here the way SameBranch leaves it: this
		// package cannot ask the remote which branch that is, so naming
		// it explicitly reads as a different branch and withholds.
		{"a declared default branch, named explicitly",
			Task{Target: docs, Base: "main"}, false},
		{"a repo nobody declared", Task{Target: &RepoRef{Owner: "acme", Name: "elsewhere"}}, false},
		{"no repo at all", Task{}, false},
		{"a declared write target written in another case",
			Task{Target: &RepoRef{Owner: "ACME", Name: "Widgets-State"}, Base: "deploy"}, true},
	} {
		if got := LandsWhereGranted(tc.child, parent); got != tc.want {
			t.Errorf("%s: LandsWhereGranted = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A task with no extra write targets is exactly SameBranch, which is
	// what keeps every single-repo deployment reading as it always did.
	plain := Task{Target: widgets}
	if LandsWhereGranted(Task{Target: state}, plain) {
		t.Error("a task with no declared write targets passed something on to another repo")
	}
	if !LandsWhereGranted(Task{Target: widgets}, plain) {
		t.Error("a task with no declared write targets stopped recognising its own branch")
	}
}

// A child's own Writes are permission it hands on to the proposals it
// makes, so a child naming a write target its parent was never given
// lands nowhere granted -- however ordinary its own branch is (#2378).
func TestLandsWhereGrantedRefusesWritesTheParentNeverHeld(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	state := RepoRef{Owner: "acme", Name: "widgets-state"}
	grants := []Grant{{Capability: "self-repair", Via: GrantByLabel}}
	parent := Task{
		Target: widgets, Base: "main", AutoMerge: true, Grants: grants,
		Writes: []WriteTarget{{Repo: state, Base: "deploy"}},
	}
	for _, tc := range []struct {
		name   string
		writes []WriteTarget
		want   bool
	}{
		{"no writes of its own", nil, true},
		{"the parent's declared write target", []WriteTarget{{Repo: state, Base: "deploy"}}, true},
		{"the parent's own target, as a write", []WriteTarget{{Repo: *widgets, Base: "main"}}, true},
		{"a declared repo on another branch", []WriteTarget{{Repo: state, Base: "main"}}, false},
		{"the configuration repo", []WriteTarget{{Repo: ConfigRepo(), Base: "main"}}, false},
		{"one granted and one not", []WriteTarget{{Repo: state, Base: "deploy"}, {Repo: ConfigRepo(), Base: "main"}}, false},
	} {
		child := Task{Target: widgets, Base: "main", Grants: grants, Writes: tc.writes}
		if got := LandsWhereGranted(child, parent); got != tc.want {
			t.Errorf("%s: LandsWhereGranted = %v, want %v", tc.name, got, tc.want)
		}
		if got := AutoSubmits(child, parent); got != tc.want {
			t.Errorf("%s: AutoSubmits = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A child's own Reads are repositories its sandbox may fetch, so a child
// asking to read one its parent could not is a widening of read access
// and is not queued on the parent's permission -- though where it lands,
// and so its auto-merge, is unaffected.
func TestAutoSubmitsRefusesReadsTheParentNeverHeld(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	docs := RepoRef{Owner: "acme", Name: "docs"}
	state := RepoRef{Owner: "acme", Name: "widgets-state"}
	private := RepoRef{Owner: "acme", Name: "private"}
	parent := Task{
		Target: widgets, Base: "main", AutoMerge: true,
		Reads:  []RepoRef{docs},
		Writes: []WriteTarget{{Repo: state, Base: "deploy"}},
	}
	for _, tc := range []struct {
		name  string
		reads []RepoRef
		want  []RepoRef
	}{
		{"no reads of its own", nil, nil},
		{"one of the parent's reads", []RepoRef{docs}, nil},
		{"the parent's reads, spelled in another case", []RepoRef{{Owner: "ACME", Name: "Docs"}}, nil},
		{"the parent's own target", []RepoRef{*widgets}, nil},
		{"the parent's declared write target", []RepoRef{state}, nil},
		{"a repo the parent could not read", []RepoRef{private}, []RepoRef{private}},
		{"the configuration repo", []RepoRef{ConfigRepo()}, []RepoRef{ConfigRepo()}},
		{"one granted and one not", []RepoRef{docs, private}, []RepoRef{private}},
	} {
		child := Task{Target: widgets, Base: "main", Reads: tc.reads}
		got := UngrantedReads(child, parent)
		if len(got) != len(tc.want) {
			t.Errorf("%s: UngrantedReads = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if !got[i].Same(tc.want[i]) {
				t.Errorf("%s: UngrantedReads = %v, want %v", tc.name, got, tc.want)
			}
		}
		if want := len(tc.want) == 0; AutoSubmits(child, parent) != want {
			t.Errorf("%s: AutoSubmits = %v, want %v", tc.name, !want, want)
		}
		if !LandsWhereGranted(child, parent) {
			t.Errorf("%s: LandsWhereGranted = false; reads say nothing about where a task lands", tc.name)
		}
	}

	// An auto-approve-proposals parent is held to the same bound.
	approver := Task{Target: widgets, Base: "main", AutoApproveProposals: true}
	if AutoSubmits(Task{Target: widgets, Base: "main", Reads: []RepoRef{private}}, approver) {
		t.Error("an auto-approve-proposals parent queued a proposal reading a repo it never held")
	}
}

// The issue's two-hop chain: even a task that somehow holds a
// configuration repo among its declared Writes does not pass unreviewed
// landing on to a proposal targeting it -- only a task already working
// on that repo's own branch does.
func TestLandsWhereGrantedNeverWidensIntoAConfigRepo(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	config := ConfigRepo()
	grants := []Grant{{Capability: "self-repair", Via: GrantByLabel}}
	hop1 := Task{
		Target: widgets, Base: "main", AutoMerge: true, Grants: grants,
		Writes: []WriteTarget{{Repo: config, Base: "main"}},
	}
	hop2 := Task{Target: &config, Base: "main", Grants: grants}
	if LandsWhereGranted(hop2, hop1) {
		t.Error("a declared Writes entry passed unreviewed landing on to the configuration repo")
	}
	if AutoSubmits(hop2, hop1) {
		t.Error("a proposal targeting the configuration repo auto-submitted through a Writes entry")
	}

	// A task a human set up on the configuration repo itself still
	// passes its permission to a follow-up on that same branch.
	onConfig := Task{Target: &config, Base: "main", AutoMerge: true, Grants: grants}
	if !AutoSubmits(Task{Target: &config, Base: "main", Grants: grants}, onConfig) {
		t.Error("a follow-up on the configuration repo task's own branch stopped auto-submitting")
	}
}

// AutoSubmits is the whole trust gate at once: the parent's permission
// to land unreviewed work, and both limits on how far it reaches. A task
// that holds none of it passes on none of it, and a task that holds it
// passes it only to a child that stays inside it.
func TestAutoSubmitsNeedsThePermissionAndBothItsLimits(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	docs := &RepoRef{Owner: "acme", Name: "docs"}
	repair := []Grant{{Capability: "self-repair", Via: GrantByLabel}}
	parent := Task{
		Target: widgets, AutoMerge: true, Grants: repair,
		Writes: []WriteTarget{{Repo: *docs, Base: "deploy"}},
	}
	for _, tc := range []struct {
		name   string
		child  Task
		parent Task
		want   bool
	}{
		{"the parent's own branch", Task{Target: widgets}, parent, true},
		{"a subset of the parent's capabilities",
			Task{Target: widgets, Grants: repair}, parent, true},
		{"a declared write target on its declared branch",
			Task{Target: docs, Base: "deploy"}, parent, true},
		{"a branch nobody granted", Task{Target: widgets, Base: "release/2.0"}, parent, false},
		{"a repo nobody granted", Task{Target: &RepoRef{Owner: "acme", Name: "other"}}, parent, false},
		{"a capability the parent never held",
			Task{Target: widgets, Grants: []Grant{{Capability: "gemini-key", Via: GrantByDefault}}},
			parent, false},
		{"a parent that does not auto-merge", Task{Target: widgets},
			Task{Target: widgets, Grants: repair}, false},
		{"a parent with auto-approve-proposals", Task{Target: widgets},
			Task{Target: widgets, AutoApproveProposals: true, Grants: repair}, true},
		{"a parent with auto-approve-proposals proposing ungranted capability",
			Task{Target: widgets, Grants: []Grant{{Capability: "gemini-key", Via: GrantByDefault}}},
			Task{Target: widgets, AutoApproveProposals: true, Grants: repair}, false},
		{"a parent with auto-approve-proposals proposing ungranted branch",
			Task{Target: widgets, Base: "release/2.0"},
			Task{Target: widgets, AutoApproveProposals: true, Grants: repair}, false},
	} {
		if got := AutoSubmits(tc.child, tc.parent); got != tc.want {
			t.Errorf("%s: AutoSubmits = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// WritesBesides is what template and schedule content goes through on
// its way onto a task (grain/task-86): the repo and base branch the task
// is filed against comes off the list, whatever case it was written in,
// while other branches of that repo and other repos are kept.
func TestWritesBesidesDropsTheRepoTheTaskIsFiledAgainst(t *testing.T) {
	widgets := RepoRef{Owner: "acme", Name: "widgets"}
	writes := []WriteTarget{
		{Repo: RepoRef{Owner: "acme", Name: "widgets-state"}, Base: "deploy"},
		{Repo: RepoRef{Owner: "ACME", Name: "Widgets"}, Base: "main"},
		{Repo: RepoRef{Owner: "ACME", Name: "Widgets"}, Base: "release/1.0"},
		{Repo: RepoRef{Owner: "acme", Name: "docs"}},
	}
	got := WritesBesides(writes, widgets, "main")
	if len(got) != 3 || got[0].Repo.Name != "widgets-state" || got[1].Base != "release/1.0" || got[2].Repo.Name != "docs" {
		t.Fatalf("WritesBesides = %+v, want the entries besides widgets on main", got)
	}
	// Nothing to drop leaves the list alone, and a list that is entirely
	// the target comes back empty rather than as a slice of nothing.
	if got := WritesBesides(writes, RepoRef{Owner: "acme", Name: "elsewhere"}, "main"); len(got) != 4 {
		t.Errorf("WritesBesides = %+v, want all four left alone", got)
	}
	only := []WriteTarget{{Repo: widgets, Base: "main"}}
	if got := WritesBesides(only, widgets, "main"); got != nil {
		t.Errorf("WritesBesides = %+v, want nil", got)
	}
}

func TestReadsBesidesDropsTheRepoTheTaskIsFiledAgainst(t *testing.T) {
	widgets := RepoRef{Owner: "acme", Name: "widgets"}
	reads := []RepoRef{
		{Owner: "ACME", Name: "Widgets"},
		{Owner: "acme", Name: "gadgets"},
	}
	got := ReadsBesides(reads, widgets)
	if len(got) != 1 || got[0].Name != "gadgets" {
		t.Fatalf("ReadsBesides = %+v, want only acme/gadgets", got)
	}
	// An unbound preview (zero RepoRef) keeps every read intact.
	if got := ReadsBesides(reads, RepoRef{}); len(got) != 2 {
		t.Errorf("ReadsBesides(unbound) = %+v, want both entries", got)
	}
	if got := ReadsBesides(reads, RepoRef{Owner: "acme", Name: "elsewhere"}); len(got) != 2 {
		t.Errorf("ReadsBesides(elsewhere) = %+v, want both entries", got)
	}
	if got := ReadsBesides([]RepoRef{widgets}, widgets); got != nil {
		t.Errorf("ReadsBesides(only target) = %+v, want nil", got)
	}
}

// ParseRepo's own gate: a paste that is not owner/name is either
// normalised to it or refused here, because nothing downstream of this
// function looks at a RepoRef again before building a clone URL out of
// it. Found by hand (task 244): `grain create -repo
// https://github.com/bwsalmon/grain` used to file a task whose owner
// was "https:".
func TestParseRepoNormalisesTheFormsAnOperatorPastes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"acme/widgets", "acme/widgets"},
		{"  acme/widgets  ", "acme/widgets"},
		{"https://github.com/acme/widgets", "acme/widgets"},
		{"https://github.com/acme/widgets/", "acme/widgets"},
		{"https://github.com/acme/widgets.git", "acme/widgets"},
		{"http://github.example.com/acme/widgets", "github.example.com/acme/widgets"},
		{"git@github.com:acme/widgets.git", "acme/widgets"},
		{"ssh://git@github.com/acme/widgets.git", "acme/widgets"},
		{"git@ghe.example.com:acme/widgets.git", "ghe.example.com/acme/widgets"},
		{"https://GHE.example.com:443/acme/widgets.git", "ghe.example.com/acme/widgets"},
		{"ghe.example.com/acme/widgets", "ghe.example.com/acme/widgets"},
		{"git.example.com:8443/acme/widgets", "git.example.com:8443/acme/widgets"},
		{"github.com/acme/widgets", "acme/widgets"},
	} {
		got, err := ParseRepo(tc.in)
		if err != nil {
			t.Errorf("ParseRepo(%q): %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseRepoRefusesWhatWouldNotSurviveBeingPutInAURL(t *testing.T) {
	for _, in := range []string{
		"",
		"not-a-repo",
		"acme/",
		"/widgets",
		"acme/widgets/extra",
		"https://github.com/acme",
		"acme/wid gets",
		"acme/../etc",
		"../acme/widgets",
		"acme/widgets#fragment",
		"acme/widgets?query",
		"acme/wid\ngets",
		"ghe.example.com/acme",
		"ghe.example.com/acme/widgets/extra",
		"../widgets",
		"a..b/acme/widgets",
	} {
		if got, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) = %q, want an error", in, got)
		}
	}
}

// A repository on a git host other than the deployment's default keeps
// its host through every form it is written in, and one on the default
// host never spells it out -- which is what keeps every row and file
// written before hosts existed reading back unchanged (docs/git-hosts.md).
func TestRepoRefHostRoundTrips(t *testing.T) {
	ghe, err := ParseRepo("ghe.example.com/acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if ghe.Host != "ghe.example.com" || ghe.Owner != "acme" || ghe.Name != "widgets" {
		t.Fatalf("ParseRepo = %+v", ghe)
	}
	if got := RepoFromStored(ghe.StoredOwner(), ghe.Name); got != ghe {
		t.Errorf("stored round trip = %+v, want %+v", got, ghe)
	}
	legacy := RepoRef{Owner: "acme", Name: "widgets"}
	if legacy.StoredOwner() != "acme" || legacy.String() != "acme/widgets" || legacy.GitHost() != DefaultGitHost {
		t.Errorf("legacy repo = %q, %q, %q", legacy.StoredOwner(), legacy.String(), legacy.GitHost())
	}
	if ghe.Same(legacy) || !ghe.Same(RepoRef{Host: "GHE.example.com", Owner: "ACME", Name: "widgets"}) {
		t.Errorf("Same does not compare hosts")
	}
	if !legacy.Same(RepoRef{Host: "github.com", Owner: "acme", Name: "widgets"}) {
		t.Errorf("an explicit default host is not the default host")
	}
}

func TestParseRepoDropsTheDeploymentsOwnDefaultHost(t *testing.T) {
	SetDefaultGitHost("ghe.example.com")
	defer SetDefaultGitHost("")
	for in, want := range map[string]string{
		"ghe.example.com/acme/widgets":         "acme/widgets",
		"https://ghe.example.com/acme/widgets": "acme/widgets",
		"https://github.com/acme/widgets":      "github.com/acme/widgets",
		"acme/widgets":                         "acme/widgets",
	} {
		got, err := ParseRepo(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeTaskID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"123", "123"},
		{"  123  ", "123"},
		{"#123", "123"},
		{"# 123", "123"},
		{"  #123  ", "123"},
		{"grain/task-123", "123"},
		{"grain/0a1b2c3d/task-123", "123"},
		{"grain/prod-eu/task-123", "123"},
		{"#grain/prod/task-123", "123"},
		{"https://grain.example.com/tasks/grain/prod/task-123", "123"},
		{"task-123", "123"},
		{"task/123", "123"},
		{"task:123", "123"},
		{"grain/task/123", "123"},
		{"grain/123", "123"},
		{"#grain/task-123", "123"},
		{"grain/task-#123", "123"},
		{"http://localhost:8080/tasks/123", "123"},
		{"http://localhost:8080/tasks/123/", "123"},
		{"http://localhost:8080/#/tasks/123", "123"},
		{"https://grain.example.com/task/123", "123"},
		{"https://grain.example.com/tasks/grain/task-123", "123"},
		{"https://example.com/tasks/123?tab=comments", "123"},
		{"http://localhost:3000/t/123", "123"},
		{"t1", "t1"},
		{"#t1", "t1"},
		{"grain/task-t1", "t1"},
		{"task-custom-name", "custom-name"},
		{"http://localhost:8080/tasks/123/runs", "123"},
		{"https://grain.example.com/tasks/123#comment-4", "123"},
		// A URL that is not a task page names no task: these are left
		// whole so they resolve to nothing, rather than to whichever
		// task happens to share the number at the end of them.
		{"https://github.com/acme/widgets/pull/12", "https://github.com/acme/widgets/pull/12"},
		{"https://github.com/acme/widgets/issues/7", "https://github.com/acme/widgets/issues/7"},
		{"https://grain.example.com/123", "https://grain.example.com/123"},
	} {
		if got := NormalizeTaskID(tc.in); got != tc.want {
			t.Errorf("NormalizeTaskID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Repository access carries what the retired reporead / repowrite
// providers decided: it is never the any-ref grant, read is admitted on
// every event, and write only on a release, a workflow_dispatch or
// grain's own trusted schedule.
func TestRepoAccessGrantCarriesItsEventGate(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "tools"}
	if IsRepoAccessGrant(GitPushAnyRefGrant) || IsRepoAccessGrant(RepoWriteRef) ||
		IsRepoAccessGrant(RepoWriteRef+`={"repo":"acme/tools","force":true}`) {
		t.Error("a repo#write that names no repository alone read as repository access")
	}
	if IsGitPushAnyRefGrant(RepoWriteGrant(repo)) {
		t.Error("repo#write {repo} read as the any-ref grant")
	}
	for _, tc := range []struct {
		event   string
		trusted bool
		write   bool
	}{
		{"release", false, true},
		{"workflow_dispatch", false, true},
		{"schedule", true, true},
		{"schedule", false, false},
		{"push", false, false},
		{"pull_request", false, false},
	} {
		for _, read := range []string{RepoReadGrant(repo), "repo-read:acme/tools"} {
			if !RepoAccessAdmitsCIEvent(read, tc.event, tc.trusted) {
				t.Errorf("%s refused on %s (trusted %v)", read, tc.event, tc.trusted)
			}
		}
		for _, write := range []string{RepoWriteGrant(repo), "repo-write:acme/tools"} {
			if got := RepoAccessAdmitsCIEvent(write, tc.event, tc.trusted); got != tc.write {
				t.Errorf("RepoAccessAdmitsCIEvent(%s, %s, %v) = %v, want %v", write, tc.event, tc.trusted, got, tc.write)
			}
		}
	}
	if RepoAccessAdmitsCIEvent("package-publish", "release", false) {
		t.Error("a capability that is not repository access was admitted")
	}
}

// AlignDefaultBases respells a base only where it and the parent's write
// target for the same repository name one branch -- one of them "" and
// the other that repository's default -- so a parent filed with an
// explicit `base: main` reaches a proposal that named no base, and
// nothing else changes (task 3411's live walk).
func TestAlignDefaultBasesReadsEmptyAndTheDefaultBranchAsOne(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	docs := &RepoRef{Owner: "acme", Name: "docs"}
	state := &RepoRef{Owner: "acme", Name: "widgets-state"}
	defaults := map[string]string{"acme/widgets": "main", "acme/docs": "trunk", "acme/widgets-state": "main"}
	asked := map[string]int{}
	defaultBranch := func(r RepoRef) string {
		asked[r.String()]++
		return defaults[r.String()]
	}
	parent := Task{
		Target: widgets, Base: "main", AutoApproveProposals: true,
		Writes: []WriteTarget{{Repo: *docs}, {Repo: *state, Base: "deploy"}},
	}

	for _, tc := range []struct {
		name     string
		child    Task
		wantBase string
		want     bool
	}{
		{"no base on the parent's repo, whose default is the parent's base",
			Task{Target: widgets}, "main", true},
		{"the parent's base named explicitly", Task{Target: widgets, Base: "main"}, "main", true},
		{"another branch of the parent's repo", Task{Target: widgets, Base: "dev"}, "dev", false},
		{"the default branch named explicitly where the write target left it empty",
			Task{Target: docs, Base: "trunk"}, "", true},
		{"a branch that is not the default where the write target left it empty",
			Task{Target: docs, Base: "main"}, "main", false},
		{"no base where the write target named a branch that is not the default",
			Task{Target: state}, "", false},
	} {
		got := AlignDefaultBases(tc.child, parent, defaultBranch)
		if got.Base != tc.wantBase {
			t.Errorf("%s: Base = %q, want %q", tc.name, got.Base, tc.wantBase)
		}
		if lands := LandsWhereGranted(got, parent); lands != tc.want {
			t.Errorf("%s: LandsWhereGranted after aligning = %v, want %v", tc.name, lands, tc.want)
		}
		if submits := AutoSubmits(got, parent); submits != tc.want {
			t.Errorf("%s: AutoSubmits after aligning = %v, want %v", tc.name, submits, tc.want)
		}
	}

	// The child's own Writes are aligned the same way, so a proposal
	// handing on the parent's default-branch write target is not read as
	// minting a new one -- and the slice it came with is left alone.
	writes := []WriteTarget{{Repo: *widgets}}
	child := Task{Target: widgets, Writes: writes}
	got := AlignDefaultBases(child, parent, defaultBranch)
	if len(UngrantedWrites(got, parent)) != 0 || !AutoSubmits(got, parent) {
		t.Errorf("aligned writes = %+v, ungranted %+v", got.Writes, UngrantedWrites(got, parent))
	}
	if writes[0].Base != "" {
		t.Errorf("AlignDefaultBases rewrote the caller's Writes slice: %+v", writes)
	}

	// Unresolvable -- no resolver, or a repository it cannot answer
	// for -- compares as written and withholds, as before.
	if got := AlignDefaultBases(Task{Target: widgets}, parent, nil); got.Base != "" || AutoSubmits(got, parent) {
		t.Errorf("with no resolver: Base %q, AutoSubmits %v", got.Base, AutoSubmits(got, parent))
	}
	unknown := func(RepoRef) string { return "" }
	if got := AlignDefaultBases(Task{Target: widgets}, parent, unknown); got.Base != "" {
		t.Errorf("with an unresolvable default: Base %q, want \"\"", got.Base)
	}

	// Nothing to resolve asks nothing: a base already matching, or a
	// repository the parent was never given.
	asked = map[string]int{}
	AlignDefaultBases(Task{Target: widgets, Base: "main"}, parent, defaultBranch)
	AlignDefaultBases(Task{Target: &RepoRef{Owner: "acme", Name: "elsewhere"}}, parent, defaultBranch)
	if len(asked) != 0 {
		t.Errorf("asked for default branches it did not need: %v", asked)
	}
}

// AutoSubmitRefusedOnLanding is the one refusal no other note explains:
// the parent may queue its proposals, the child asks for nothing more,
// and only the branch it lands on is wrong.
func TestAutoSubmitRefusedOnLanding(t *testing.T) {
	widgets := &RepoRef{Owner: "acme", Name: "widgets"}
	parent := Task{Target: widgets, Base: "release/1.0", AutoApproveProposals: true}
	if !AutoSubmitRefusedOnLanding(Task{Target: widgets}, parent) {
		t.Error("a proposal on another branch was not reported as refused on landing")
	}
	if AutoSubmitRefusedOnLanding(Task{Target: widgets, Base: "release/1.0"}, parent) {
		t.Error("a proposal on the parent's own branch was reported as refused")
	}
	if AutoSubmitRefusedOnLanding(Task{Target: widgets}, Task{Target: widgets, Base: "release/1.0"}) {
		t.Error("a parent with no permission to queue proposals was reported as refusing on landing")
	}
	if AutoSubmitRefusedOnLanding(Task{Target: widgets, Grants: []Grant{{Capability: "x"}}}, parent) {
		t.Error("a proposal escalating a capability was reported as refused on landing alone")
	}
}
