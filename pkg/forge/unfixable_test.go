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

package forge

import (
	"slices"
	"testing"
)

// The repository-wide finding covers every check it reports, named or
// not: the causes of it are settings on the repository and the account
// behind it, so there is no list to keep.
func TestChecksNeverStartCoversEveryCheck(t *testing.T) {
	u := Unfixable{ChecksNeverStart: true, Why: "not one recent run got a job as far as a step"}
	if !u.Any() {
		t.Fatal("a repository whose runs never start says something")
	}
	for _, name := range []string{"test", "image (amd64)", "cla/google", "check-changes"} {
		if !u.Covers(name) {
			t.Errorf("%q should be covered by a repository whose runs never start", name)
		}
	}
}

// grain CI is never covered, whatever the finding says: its verdict is
// about one exact commit on this branch, so it is the branch's own answer
// by construction -- and a deployment that could declare its own runner
// unfixable would have built a way to merge with no verdict at all.
func TestGrainCIIsNeverUnfixable(t *testing.T) {
	for _, u := range []Unfixable{
		{ChecksNeverStart: true},
		{Checks: []string{GrainCICheckName}},
	} {
		if u.Covers(GrainCICheckName) {
			t.Errorf("%+v covers grain CI's own verdict", u)
		}
	}
}

// A declared check is matched by name, not by substring: reading a
// check's name for a phrase is the mistake this package made once
// already, and a substring match would let one job's name take a
// repository's whole gate away.
func TestDeclaredChecksMatchByName(t *testing.T) {
	u := Unfixable{Checks: []string{"cla/google"}}
	if !u.Covers("cla/google") || !u.Covers("CLA/Google") {
		t.Error("a declared check should match its own name, case aside")
	}
	if u.Covers("cla/google-internal") || u.Covers("cla") {
		t.Error("a declared check should not match by substring")
	}
	if u.Covers("test") {
		t.Error("a declaration about one check says nothing about another")
	}
}

// The zero Unfixable -- what a caller that could not find out passes --
// leaves every check the branch's own, which is what the queue did before
// any of this existed.
func TestNoFindingLeavesEveryRedTheBranchOwn(t *testing.T) {
	r := SplitReds([]string{"test", "lint"}, false, Unfixable{})
	if !r.PathToGreen() {
		t.Error("with no finding there is always a path to green")
	}
	if !r.BranchOwns() || !slices.Equal(r.Own, []string{"test", "lint"}) {
		t.Errorf("own = %v, want both checks", r.Own)
	}
}

// Both kinds of red at once -- grain/task-219's state: seven checks a
// repository out of Actions minutes could not start, and one grain CI
// failure the branch really does own. There is no path to green, and
// there is still work for an agent.
func TestBothKindsOfRedAtOnce(t *testing.T) {
	r := SplitReds([]string{"test", "check-changes", "cla/google"}, true,
		Unfixable{ChecksNeverStart: true})
	if r.PathToGreen() {
		t.Error("nothing pushed here clears checks that never start")
	}
	if !r.BranchOwns() {
		t.Fatal("a failed grain CI run is the branch's own red")
	}
	if !slices.Equal(r.Own, []string{GrainCICheckName}) {
		t.Errorf("own = %v, want grain CI alone", r.Own)
	}
	if !slices.Equal(r.Inherited, []string{"test", "check-changes", "cla/google"}) {
		t.Errorf("inherited = %v, want the three GitHub checks", r.Inherited)
	}
}

// One declared check red among checks the branch does own: there is no
// path to green, because the declared one stays red however the rest go.
func TestOneUnfixableCheckRemovesThePathToGreen(t *testing.T) {
	r := SplitReds([]string{"test", "cla/google"}, false, Unfixable{Checks: []string{"cla/google"}})
	if r.PathToGreen() {
		t.Error("a check that never passes here is a check the merge waits on forever")
	}
	if !slices.Equal(r.Own, []string{"test"}) {
		t.Errorf("own = %v, want the test that is genuinely this branch's", r.Own)
	}
}

// A repository judged by grain CI alone passes no GitHub check names at
// all, so the finding about GitHub's runs changes nothing for it.
func TestGrainCIGatedRepositoryIsUnaffected(t *testing.T) {
	r := SplitReds(nil, true, Unfixable{ChecksNeverStart: true})
	if !r.PathToGreen() {
		t.Error("with GitHub's checks unread there is nothing inherited to wait on")
	}
	if !r.BranchOwns() {
		t.Error("grain CI's failure is still the branch's own")
	}
}
