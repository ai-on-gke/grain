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

package staterepo_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// benignSettingsTables are the SettingsTables an operator may list in
// settings/config-policy.yml's auto_merge_paths: they grant no
// capability, repository or secret and set nothing about what a sandbox
// runs. Every other settings table is live on the next tick once merged
// (staterepo.Apply), so it must sit under the hardcoded ceiling.
var benignSettingsTables = map[string]bool{
	"custom_field":                  true,
	"qualification_config":          true,
	"qualification_item":            true,
	"qualification_item_depends_on": true,
	"suite":                         true,
	"suite_item":                    true,
	"suite_sequence":                true,
	"board":                         true,
	"board_column":                  true,
	"board_panel":                   true,
	"board_section":                 true,
	"board_sequence":                true,
}

// TestSettingsTablesAreHeldUnlessBenign keeps NeverAutoMergePaths in step
// with SettingsTables: a table added to the apply list is held for a
// human Submit until somebody decides here that it is benign.
func TestSettingsTablesAreHeldUnlessBenign(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range staterepo.SettingsTables {
		seen[name] = true
		if benignSettingsTables[name] {
			continue
		}
		if p := "tables/" + name + ".json"; !trustboundary.IsNeverAutoMergePath(p) {
			t.Errorf("settings table %q is applied on the next tick but %s is not in trustboundary.NeverAutoMergePaths; add it there, or to benignSettingsTables if it grants nothing", name, p)
		}
	}
	for name := range benignSettingsTables {
		if !seen[name] {
			t.Errorf("benignSettingsTables lists %q, which is not in staterepo.SettingsTables", name)
		}
	}
}
