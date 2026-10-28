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

package ci

import (
	"sort"
	"strings"
)

// ParseAttemptGoTests extracts per-test output logs from a job attempt's raw
// GoTestLog, its step outputs, and its FailureSummary.
//
// The returned map is keyed by normalized repository-relative package path
// (e.g. "pkg/ci", "tests/e2e", or "." for the module root) -> test name
// (e.g. "TestFoo" or "TestFoo/subtest") -> the captured test output log.
func ParseAttemptGoTests(attempt JobAttempt) map[string]map[string]string {
	out := make(map[string]map[string]string)
	addTestLog := func(pkg, testName, text string) {
		pkg = NormalizeGoTestPackage(pkg)
		testName = strings.TrimSpace(testName)
		if testName == "" || text == "" {
			return
		}
		if pkg == "" {
			pkg = "."
		}
		if out[pkg] == nil {
			out[pkg] = make(map[string]string)
		}
		if existing, ok := out[pkg][testName]; !ok || len(text) > len(existing) {
			out[pkg][testName] = text
		}
	}

	sources := make([]string, 0, 1+len(attempt.Steps))
	if strings.TrimSpace(attempt.GoTestLog) != "" {
		sources = append(sources, attempt.GoTestLog)
	}
	for _, step := range attempt.Steps {
		if strings.TrimSpace(step.Output) != "" {
			sources = append(sources, step.Output)
		}
	}

	for _, src := range sources {
		parseGoTestOutputInto(src, addTestLog)
	}

	if attempt.FailureSummary != nil {
		for _, tf := range attempt.FailureSummary.Tests {
			pkg, name := splitSummaryTestName(tf.Name)
			if name == "" {
				continue
			}
			if out[pkg] != nil && out[pkg][name] != "" {
				continue
			}
			var b strings.Builder
			b.WriteString("=== RUN   " + name + "\n")
			if tf.Location != "" {
				b.WriteString("    " + tf.Location + "\n")
			}
			if tf.Message != "" {
				for _, line := range strings.Split(strings.TrimRight(tf.Message, "\n"), "\n") {
					b.WriteString("    " + line + "\n")
				}
			}
			if tf.Diff != "" {
				for _, line := range strings.Split(strings.TrimRight(tf.Diff, "\n"), "\n") {
					b.WriteString("    " + line + "\n")
				}
			}
			b.WriteString("--- FAIL: " + name + "\n")
			addTestLog(pkg, name, b.String())
			if root, _, isSub := strings.Cut(name, "/"); isSub && (out[pkg] == nil || out[pkg][root] == "") {
				addTestLog(pkg, root, b.String())
			}
		}
	}

	return out
}

func splitSummaryTestName(raw string) (pkg, name string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ".", ""
	}
	if fields := strings.Fields(raw); len(fields) == 2 {
		return NormalizeGoTestPackage(fields[0]), fields[1]
	}
	if idx := strings.LastIndex(raw, "."); idx > 0 && strings.Contains(raw[:idx], "/") {
		return NormalizeGoTestPackage(raw[:idx]), raw[idx+1:]
	}
	return ".", raw
}

// LookupAttemptGoTest looks up a single test's log in attempt by package path
// and test name (with or without a trailing ".log").
func LookupAttemptGoTest(attempt JobAttempt, pkg, testName string) (string, bool) {
	testsByPkg := ParseAttemptGoTests(attempt)
	if len(testsByPkg) == 0 {
		return "", false
	}
	testName = strings.TrimSuffix(strings.TrimSpace(testName), ".log")
	normPkg := NormalizeGoTestPackage(pkg)
	for _, candidatePkg := range []string{normPkg, strings.Trim(pkg, "/"), "."} {
		if candidatePkg == "" {
			continue
		}
		if pkgMap, ok := testsByPkg[candidatePkg]; ok {
			if logText, ok := pkgMap[testName]; ok {
				return logText, true
			}
		}
	}
	return "", false
}

// SortedAttemptTestPackages returns the sorted package keys from ParseAttemptGoTests.
func SortedAttemptTestPackages(testsByPkg map[string]map[string]string) []string {
	pkgs := make([]string, 0, len(testsByPkg))
	for pkg := range testsByPkg {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	return pkgs
}

// SortedPackageTestNames returns the sorted test names within a package map.
func SortedPackageTestNames(pkgTests map[string]string) []string {
	names := make([]string, 0, len(pkgTests))
	for name := range pkgTests {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NormalizeGoTestPackage converts a full Go module package import path (such as
// "github.com/ai-on-gke/grain/pkg/ci") into a repository-relative package path
// ("pkg/ci"), or "." for the module root.
func NormalizeGoTestPackage(pkg string) string {
	pkg = strings.TrimSpace(pkg)
	pkg = strings.TrimPrefix(pkg, "./")
	pkg = strings.Trim(pkg, "/")
	if pkg == "" || pkg == "." {
		return "."
	}
	const grainModulePrefix = "github.com/ai-on-gke/grain"
	if pkg == grainModulePrefix {
		return "."
	}
	if rest, ok := strings.CutPrefix(pkg, grainModulePrefix+"/"); ok {
		return rest
	}
	parts := strings.Split(pkg, "/")
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		if len(parts) == 3 {
			return "."
		}
		return strings.Join(parts[3:], "/")
	}
	return pkg
}

func parseGoTestOutputInto(src string, add func(pkg, testName, text string)) {
	var (
		explicitPkg string
		activeTest  string
		summaryPkg  string
		summaryTest string
		summaryBuf  strings.Builder
		buffers     = make(map[string]*strings.Builder)
		unassigned  []string
	)

	flushSummaryBlock := func() {
		if summaryTest != "" && summaryBuf.Len() > 0 {
			add(summaryPkg, summaryTest, summaryBuf.String())
			if root, _, isSub := strings.Cut(summaryTest, "/"); isSub {
				add(summaryPkg, root, summaryBuf.String())
			}
		}
		summaryPkg = ""
		summaryTest = ""
		summaryBuf.Reset()
	}

	appendToActive := func(name, line string) {
		if name == "" {
			return
		}
		b, ok := buffers[name]
		if !ok {
			b = &strings.Builder{}
			buffers[name] = b
			unassigned = append(unassigned, name)
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if root, _, isSub := strings.Cut(name, "/"); isSub {
			rb, ok := buffers[root]
			if !ok {
				rb = &strings.Builder{}
				buffers[root] = rb
				unassigned = append(unassigned, root)
			}
			rb.WriteString(line)
			rb.WriteByte('\n')
		}
	}

	flushPackage := func(pkg string) {
		flushSummaryBlock()
		if pkg == "" {
			return
		}
		for _, name := range unassigned {
			if b := buffers[name]; b != nil && b.Len() > 0 {
				add(pkg, name, b.String())
			}
		}
		buffers = make(map[string]*strings.Builder)
		unassigned = nil
		activeTest = ""
	}

	for _, rawLine := range strings.Split(src, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)

		if rest, ok := strings.CutPrefix(trimmed, "=== PACKAGE "); ok {
			flushSummaryBlock()
			newPkg := NormalizeGoTestPackage(rest)
			if explicitPkg != "" && explicitPkg != newPkg {
				flushPackage(explicitPkg)
			}
			explicitPkg = newPkg
			continue
		}

		if strings.HasPrefix(trimmed, "=== ") && !strings.HasPrefix(trimmed, "=== RUN ") && !strings.HasPrefix(trimmed, "=== PAUSE ") && !strings.HasPrefix(trimmed, "=== CONT ") {
			flushSummaryBlock()
			continue
		}

		if pkg, ok := parseGoTestPackageSummaryLine(trimmed); ok {
			flushSummaryBlock()
			targetPkg := pkg
			if explicitPkg != "" {
				targetPkg = explicitPkg
			}
			flushPackage(targetPkg)
			explicitPkg = ""
			continue
		}

		if testName, ok := parseGoTestDirective(trimmed); ok {
			flushSummaryBlock()
			activeTest = testName
			appendToActive(activeTest, line)
			continue
		}

		if pkg, testName, isSummaryFail := parseRunGoTestsSummaryFailLine(trimmed); isSummaryFail {
			flushSummaryBlock()
			summaryPkg = pkg
			summaryTest = testName
			summaryBuf.WriteString(line + "\n")
			continue
		}

		if testName, ok := parseGoTestResultDirective(trimmed); ok {
			flushSummaryBlock()
			appendToActive(testName, line)
			if activeTest == testName {
				if root, _, isSub := strings.Cut(testName, "/"); isSub {
					activeTest = root
				} else {
					activeTest = ""
				}
			}
			continue
		}

		if summaryTest != "" {
			if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
				summaryBuf.WriteString(line + "\n")
				continue
			}
			flushSummaryBlock()
		}

		if activeTest != "" {
			appendToActive(activeTest, line)
		}
	}

	flushSummaryBlock()
	if len(unassigned) > 0 {
		pkg := explicitPkg
		if pkg == "" {
			pkg = "."
		}
		flushPackage(pkg)
	}
}

func parseRunGoTestsSummaryFailLine(trimmed string) (pkg, testName string, ok bool) {
	rest, hasPrefix := strings.CutPrefix(trimmed, "--- FAIL: ")
	if !hasPrefix {
		return "", "", false
	}
	fields := strings.Fields(rest)
	if len(fields) == 2 && !strings.HasPrefix(fields[1], "(") {
		return NormalizeGoTestPackage(fields[0]), fields[1], true
	}
	return "", "", false
}

func parseGoTestDirective(trimmed string) (string, bool) {
	for _, prefix := range []string{"=== RUN   ", "=== PAUSE ", "=== CONT  "} {
		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			name := strings.TrimSpace(rest)
			if name != "" {
				return name, true
			}
		}
	}
	return "", false
}

func parseGoTestResultDirective(trimmed string) (string, bool) {
	for _, prefix := range []string{"--- FAIL: ", "--- PASS: ", "--- SKIP: "} {
		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			name, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
			if name != "" {
				return name, true
			}
		}
	}
	return "", false
}

func parseGoTestPackageSummaryLine(trimmed string) (string, bool) {
	for _, prefix := range []string{"ok  \t", "ok\t", "FAIL\t", "?   \t"} {
		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return NormalizeGoTestPackage(fields[0]), true
			}
		}
	}
	return "", false
}
