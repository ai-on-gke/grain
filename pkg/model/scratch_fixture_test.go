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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ScratchFixture is a trust gate, so each thing it checks is pinned here
// by the one case that differs from a fixture it accepts only in that.
func TestScratchFixtureIsOnlyTheProposersOwnScratchBranch(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	config := model.ConfigRepo()
	parent := model.Task{ID: "41", Target: &repo, Grants: []model.Grant{{Capability: "ci-read"}}}
	fixture := func(edit func(*model.Task)) model.Task {
		c := model.Task{Target: &repo, Base: "qualification/41", Grants: []model.Grant{{Capability: "ci-read"}}}
		if edit != nil {
			edit(&c)
		}
		return c
	}
	for _, tc := range []struct {
		name          string
		child, parent model.Task
		pushes        bool
		want          bool
	}{
		{name: "its own scratch branch", child: fixture(nil), parent: parent, pushes: true, want: true},
		{name: "a template that may not push it", child: fixture(nil), parent: parent},
		{name: "the default branch", child: fixture(func(c *model.Task) { c.Base = "" }), parent: parent, pushes: true},
		{name: "another task's scratch branch", child: fixture(func(c *model.Task) { c.Base = "qualification/42" }), parent: parent, pushes: true},
		{name: "a branch below it", child: fixture(func(c *model.Task) { c.Base = "qualification/41/x" }), parent: parent, pushes: true},
		{name: "another repository", child: fixture(func(c *model.Task) { c.Target = &other }), parent: parent, pushes: true},
		{name: "the config repository", child: fixture(func(c *model.Task) { c.Target = &config }),
			parent: model.Task{ID: "41", Target: &config}, pushes: true},
		{name: "a capability the proposer lacks", child: fixture(func(c *model.Task) {
			c.Grants = append(c.Grants, model.Grant{Capability: "self-debug"})
		}), parent: parent, pushes: true},
		{name: "a read the proposer lacks", child: fixture(func(c *model.Task) { c.Reads = []model.RepoRef{other} }),
			parent: parent, pushes: true},
		{name: "a write target of its own, even one the proposer holds", child: fixture(func(c *model.Task) {
			c.Writes = []model.WriteTarget{{Repo: repo}}
		}), parent: parent, pushes: true},
		{name: "a change target", child: fixture(func(c *model.Task) { c.ChangeTarget = &model.ChangeTarget{Kind: "cl"} }),
			parent: parent, pushes: true},
		{name: "no repository", child: fixture(func(c *model.Task) { c.Target = nil }), parent: parent, pushes: true},
	} {
		if got := model.ScratchFixture(tc.child, tc.parent, tc.pushes); got != tc.want {
			t.Errorf("%s: ScratchFixture = %v, want %v", tc.name, got, tc.want)
		}
	}
}
