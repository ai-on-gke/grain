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
	"slices"
	"testing"
)

func TestBlameTrailersReadsBlamesAndFixesCommit(t *testing.T) {
	tasks, commits := BlameTrailers([]string{
		"fix: one\n\nBlames: task 4820\nFixes-commit: ABCDEF1\nOrigin: task 2923",
		"fix: two\n\nBlames: #4821\nBlames: 4820\nFixes-commit: abcdef1\n",
		"docs: mentions Blames: 99 mid-line, which is not a trailer",
	})
	if want := []string{"4820", "4821"}; !slices.Equal(tasks, want) {
		t.Errorf("tasks = %v, want %v", tasks, want)
	}
	if want := []string{"abcdef1"}; !slices.Equal(commits, want) {
		t.Errorf("commits = %v, want %v", commits, want)
	}
}

func TestRepairKindNamesEachRepair(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	base := Task{Target: &repo}
	SetFixesBaseBranch(&base, true)
	for _, tc := range []struct {
		task Task
		want string
	}{
		{base, "base-branch"},
		{Task{Tags: []string{Stage1RepairTagPrefix + "acme/widgets"}}, "stage1"},
		{Task{Title: RCBlockerMarker("v1.2.0-rc.1") + " fix the flake"}, "rc-blocker"},
		{Task{Title: "Add a widget"}, ""},
	} {
		if got := RepairKind(tc.task); got != tc.want {
			t.Errorf("RepairKind(%q, %v) = %q, want %q", tc.task.Title, tc.task.Tags, got, tc.want)
		}
	}
}
