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
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// uiRetiredGrantTable is the /next/ UI's copy of the retired in-process
// names: Settings → Capabilities names each one a deployment's
// settings/capabilities.yml still configures, with what replaced it.
const uiRetiredGrantTable = "../../ui-ng/src/app/settings/capability-retired.ts"

var uiRetiredKey = regexp.MustCompile(`(?m)^  "?([a-z][a-z0-9-]*)"?: `)

// TestUIRetiredGrantTableNamesEveryRetiredCapability keeps the UI's
// RETIRED_IN_PROCESS naming every bare capability retiredGrantReplacements
// and retiredGrantMultiReplacements retire: one left out (git-push-any-ref,
// the 2026-11-15 review of #1614) gets no deprecation notice on Settings.
func TestUIRetiredGrantTableNamesEveryRetiredCapability(t *testing.T) {
	src, err := os.ReadFile(uiRetiredGrantTable)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is not in this tree", uiRetiredGrantTable)
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "export const RETIRED_IN_PROCESS")
	if start < 0 {
		t.Fatalf("%s declares no RETIRED_IN_PROCESS", uiRetiredGrantTable)
	}
	body := text[start:]
	if end := strings.Index(body, "\n};"); end >= 0 {
		body = body[:end]
	}
	listed := map[string]bool{}
	for _, m := range uiRetiredKey.FindAllStringSubmatch(body, -1) {
		listed[m[1]] = true
	}
	var missing []string
	for name := range retiredGrantReplacements {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	for name := range retiredGrantMultiReplacements {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s's RETIRED_IN_PROCESS leaves out retired capabilities %v: add each with the grant it was retired onto", uiRetiredGrantTable, missing)
	}
}
