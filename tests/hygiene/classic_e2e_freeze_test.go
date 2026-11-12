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

package hygiene

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The classic Playwright suite (ui/e2e/, the React UI at /) is frozen:
// no new tests, flakes quarantined rather than fixed, and a spec stops
// gating once its Go and ui-ng halves land
// (docs/test-plans/classic-ui-e2e-migration, "Bucket 3";
// ui/e2e/README.md). These tests keep the freeze honest:
//
//   - TestNoNewClassicE2ESpecs: every classic spec under ui/e2e/ is in
//     ui/e2e/FROZEN_SPECS, and that list only shrinks. The next-*.spec.js
//     files drive the Angular UI at /next/ and are not classic; they are
//     exempt until they move to ui-ng/e2e/.
//   - TestClassicE2EFreezeLists: every entry in ui/e2e/QUARANTINE and
//     ui/e2e/NONGATING names a spec that exists and, for file:line, a
//     line that starts a test(...) with a literal title -- the same thing
//     ui/e2e/frozen.js needs to build playwright.config.js's grepInvert,
//     checked here so a stale line fails `go unit` rather than the whole
//     `ui e2e` job.

const classicE2EDir = "ui/e2e"

// frozenClassicSpecCount is how many classic specs FROZEN_SPECS held when
// the suite was frozen (task 5099). The list may get shorter as pages
// retire; it may not get longer. Lower this when you delete specs, never
// raise it.
const frozenClassicSpecCount = 343

// classicSpecName is what Playwright picks up as a spec under testDir
// (its default testMatch).
var classicSpecName = regexp.MustCompile(`\.(spec|test)\.[cm]?[jt]sx?$`)

func isNextSpec(rel string) bool {
	return !strings.Contains(rel, "/") && strings.HasPrefix(rel, "next-")
}

// readListFile returns a list file's non-comment, non-blank lines with
// their 1-based line numbers.
func readListFile(t *testing.T, path string) (lines []string, lineNos []int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range strings.Split(string(b), "\n") {
		if j := strings.IndexByte(raw, '#'); j >= 0 {
			raw = raw[:j]
		}
		if s := strings.TrimSpace(raw); s != "" {
			lines = append(lines, s)
			lineNos = append(lineNos, i+1)
		}
	}
	return lines, lineNos
}

func TestNoNewClassicE2ESpecs(t *testing.T) {
	dir := filepath.Join(repoRoot(t), classicE2EDir)
	listed, lineNos := readListFile(t, filepath.Join(dir, "FROZEN_SPECS"))

	allowed := map[string]bool{}
	for i, name := range listed {
		if allowed[name] {
			t.Errorf("ui/e2e/FROZEN_SPECS:%d: %s is listed twice", lineNos[i], name)
		}
		allowed[name] = true
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("ui/e2e/FROZEN_SPECS:%d: %s no longer exists; delete the line (and lower frozenClassicSpecCount)", lineNos[i], name)
		}
	}
	if len(allowed) > frozenClassicSpecCount {
		t.Errorf("ui/e2e/FROZEN_SPECS lists %d specs, more than the %d it was frozen with: the classic suite takes no new specs (ui/e2e/README.md)",
			len(allowed), frozenClassicSpecCount)
	}

	var added []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if classicSpecName.MatchString(rel) && !isNextSpec(rel) && !allowed[rel] {
			added = append(added, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(added)
	for _, rel := range added {
		t.Errorf("ui/e2e/%s is a new classic e2e spec. The classic suite is frozen: write it against /next/ "+
			"(ui/e2e/next-*.spec.js, ui-ng/e2e/ once it exists) or as a Go test under tests/uiapi instead, "+
			"and do not add it to ui/e2e/FROZEN_SPECS (ui/e2e/README.md)", rel)
	}
}

// freezeEntry is the shape ui/e2e/frozen.js accepts.
var freezeEntry = regexp.MustCompile(`^([^\s:]+\.spec\.js)(?::(\d+))?$`)

// testCallStart and testLiteralTitle mirror frozen.js's own checks.
var (
	testCallStart    = regexp.MustCompile(`\btest(?:\.\w+)?\(`)
	testLiteralTitle = regexp.MustCompile("\\btest(?:\\.(?:only|fixme|fail|slow|skip))?\\(\\s*(?:\"(?:\\\\.|[^\"\\\\])*\"|'(?:\\\\.|[^'\\\\])*'|`(?:\\\\.|[^`\\\\$])*`)")
)

func TestClassicE2EFreezeLists(t *testing.T) {
	dir := filepath.Join(repoRoot(t), classicE2EDir)
	seen := map[string]string{}    // entry -> where it was first listed
	wholeFile := map[string]bool{} // files listed without a line
	type lineEntry struct{ where, file string }
	var lineEntries []lineEntry

	for _, list := range []string{"QUARANTINE", "NONGATING"} {
		lines, lineNos := readListFile(t, filepath.Join(dir, list))
		for i, spec := range lines {
			where := fmt.Sprintf("ui/e2e/%s:%d", list, lineNos[i])
			m := freezeEntry.FindStringSubmatch(spec)
			if m == nil {
				t.Errorf("%s: %q is not <file>.spec.js or <file>.spec.js:<line>", where, spec)
				continue
			}
			if prev, ok := seen[spec]; ok {
				t.Errorf("%s: %s is already listed at %s", where, spec, prev)
				continue
			}
			seen[spec] = where
			file := m[1]
			src, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Errorf("%s: %s: no such spec in ui/e2e (deleted? drop the entry)", where, file)
				continue
			}
			if m[2] == "" {
				wholeFile[file] = true
				continue
			}
			lineEntries = append(lineEntries, lineEntry{where, file})
			n, _ := strconv.Atoi(m[2])
			srcLines := strings.Split(string(src), "\n")
			if n < 1 || n > len(srcLines) || !testCallStart.MatchString(srcLines[n-1]) {
				t.Errorf("%s: line %d of %s does not start a test(...) call (the test moved? point the entry at its new line)", where, n, file)
				continue
			}
			end := min(n+2, len(srcLines))
			if !testLiteralTitle.MatchString(strings.Join(srcLines[n-1:end], "\n")) {
				t.Errorf("%s: the test at %s:%d has no literal title, so grepInvert cannot name it; list the whole file instead", where, file, n)
			}
		}
	}
	for _, e := range lineEntries {
		if wholeFile[e.file] {
			t.Errorf("%s: %s is already listed as a whole file; drop the line entry", e.where, e.file)
		}
	}
}
