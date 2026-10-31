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

package staterepo

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestSettingsSectionLinksNameExportedTables holds the UI's "View in
// config repo" links (ui/src/settingsSections.js CONFIG_REPO_SECTION_PATHS)
// to files this package writes: every tables/<t>.json a section names is
// the dump of a settings table. Git connections and console sessions once
// pointed at tables that no longer exist (design/spec/settings/settings.spec.md §2).
func TestSettingsSectionLinksNameExportedTables(t *testing.T) {
	src, err := os.ReadFile("../../ui/src/settingsSections.js")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(src), "export const CONFIG_REPO_SECTION_PATHS = {")
	if !ok {
		t.Fatal("CONFIG_REPO_SECTION_PATHS not found in ui/src/settingsSections.js")
	}
	block, _, _ = strings.Cut(block, "\n};")
	paths := regexp.MustCompile(`"(` + TablesDir + `/[^"]+)"`).FindAllStringSubmatch(block, -1)
	if len(paths) == 0 {
		t.Fatal("no tables/ paths found in CONFIG_REPO_SECTION_PATHS")
	}
	for _, m := range paths {
		table := strings.TrimSuffix(strings.TrimPrefix(m[1], TablesDir+"/"), ".json")
		if !slices.Contains(SettingsTables, table) || DumpPath(table) != m[1] {
			t.Errorf("CONFIG_REPO_SECTION_PATHS links %s, which is not the dump of a settings table", m[1])
		}
	}
}
