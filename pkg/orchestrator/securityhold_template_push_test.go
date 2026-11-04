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

package orchestrator_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A template's `push:` list is a grant (trustboundary.TemplatePush,
// releases playbook §0.3/§0.4): a change that makes, widens or narrows one
// waits for a person's Submit, and a clean security review does not give
// auto-merge back. A change to the rest of a template does not.
func TestATemplatesPushListChangeIsHeldForAPerson(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	const path = ".grain/templates/release.yml"
	plain := "title: Cut a release\nbody: Cut it.\n"
	reworded := "title: Cut a release\nbody: Cut it, carefully.\n"
	granted := "title: Cut a release\nbody: Cut it.\npush:\n  - refs/tags/v*-rc.*\n"
	grantedReworded := "title: Cut a release\nbody: Cut it, carefully.\npush:\n  - refs/tags/v*-rc.*\n"
	widened := "title: Cut a release\nbody: Cut it.\npush:\n  - refs/tags/v*-rc.*\n  - refs/tags/latest\n"
	forced := "title: Cut a release\nbody: Cut it.\npush:\n  - ref: refs/tags/v*-rc.*\n    force: true\n"

	for _, tc := range []struct {
		name       string
		base, head map[string]string
		held       bool
	}{
		{"a push list added", map[string]string{path: plain}, map[string]string{path: granted}, true},
		{"a new template with a push list", nil, map[string]string{path: granted}, true},
		{"a push list widened", map[string]string{path: granted}, map[string]string{path: widened}, true},
		{"a push entry given force", map[string]string{path: granted}, map[string]string{path: forced}, true},
		{"a push list removed", map[string]string{path: granted}, map[string]string{path: plain}, true},
		{"a push list that does not parse", map[string]string{path: plain},
			map[string]string{path: plain + "push:\n  - refs/heads/*\n"}, true},
		{"a new template with a push list and an upper-case extension", nil,
			map[string]string{".grain/templates/release.YML": granted}, true},
		{"a template without one reworded", map[string]string{path: plain}, map[string]string{path: reworded}, false},
		{"a template with one reworded, its list untouched", map[string]string{path: granted},
			map[string]string{path: grantedReworded}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			got := finishStateChangeWithBaseFiles(t, store, ctx, repo, tc.base, tc.head)
			humanOnly := linkOf(got, model.LinkHumanOnlyHold) != ""
			if tc.held && (got.AutoMerge || !humanOnly) {
				t.Fatalf("%s was not held for a person: automerge=%v links=%+v", tc.name, got.AutoMerge, got.Links)
			}
			if !tc.held && (!got.AutoMerge || humanOnly) {
				t.Fatalf("%s was held for a person though its push list is unchanged: automerge=%v links=%+v",
					tc.name, got.AutoMerge, got.Links)
			}
		})
	}
}
