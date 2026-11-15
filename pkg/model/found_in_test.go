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

func TestNormalizeFoundIn(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"  ":            "",
		"v2.5.0":        "v2.5.0",
		" v2.5.0-rc.2 ": "v2.5.0-rc.2",
		"2.5.0":         "v2.5.0",
		"2.5.0-rc.10":   "v2.5.0-rc.10",
	} {
		got, err := NormalizeFoundIn(in)
		if err != nil || got != want {
			t.Errorf("NormalizeFoundIn(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"latest", "v2.5", "v2.5.0-beta", "release/v2.5.0", "v2.5.0-rc.1-discarded"} {
		if got, err := NormalizeFoundIn(in); err == nil {
			t.Errorf("NormalizeFoundIn(%q) = %q, want it refused", in, got)
		}
	}
}

func TestDerivedFoundIn(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	other := RepoRef{Owner: "acme", Name: "docs"}
	for name, tc := range map[string]struct {
		task Task
		want string
	}{
		"rc blocker":    {Task{Title: RCBlockerMarker("v2.5.0-rc.2") + " Revert x", Base: "release/v2.4.0"}, "v2.5.0-rc.2"},
		"backport base": {Task{Title: "Port x", Target: &repo, Base: "release/v2.4.0"}, "v2.4.0"},
		"backport writes": {Task{Title: "Backport x", Target: &repo,
			Writes: []WriteTarget{{Repo: repo, Base: "release/v2.4.1"}}}, "v2.4.1"},
		"another repo's release": {Task{Title: "Docs", Target: &repo,
			Writes: []WriteTarget{{Repo: other, Base: "release/v2.4.1"}}}, ""},
		"main":         {Task{Title: "Fix x", Target: &repo, Base: "main"}, ""},
		"not a marker": {Task{Title: "[rc-blocker <rc_tag>] x"}, ""},
	} {
		if got := DerivedFoundIn(tc.task); got != tc.want {
			t.Errorf("%s: DerivedFoundIn = %q, want %q", name, got, tc.want)
		}
	}
}

func TestSetFoundInReplacesAndApplyKeepsWhatWasStated(t *testing.T) {
	task := Task{
		Title: RCBlockerMarker("v2.5.0-rc.2") + " Fix x",
		Links: []Link{{Kind: LinkProposedBy, Target: "9"}},
	}
	SetFoundIn(&task, "v2.5.0-rc.1")
	ApplyDerivedFoundIn(&task)
	if got := FoundIn(task); got != "v2.5.0-rc.1" {
		t.Fatalf("FoundIn = %q, want the stated v2.5.0-rc.1 kept over the title's", got)
	}
	SetFoundIn(&task, "v2.5.0")
	if len(task.Links) != 2 || FoundIn(task) != "v2.5.0" {
		t.Fatalf("links = %+v, want proposed-by and one found-in v2.5.0", task.Links)
	}
	SetFoundIn(&task, "")
	ApplyDerivedFoundIn(&task)
	if got := FoundIn(task); got != "v2.5.0-rc.2" {
		t.Fatalf("FoundIn = %q, want the title's v2.5.0-rc.2 once nothing is stated", got)
	}
}
