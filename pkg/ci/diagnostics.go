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
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

	// Go test failure line: --- FAIL: TestName (0.01s) or --- FAIL: TestName/SubTest (0.01s)
	goTestFailRegex = regexp.MustCompile(`(?m)^\s*--- FAIL:\s+([^\s\(\)]+)(?:\s+\(([^)]+)\))?`)
	// Go test location inside failure block: file_test.go:123: message
	goLocRegex = regexp.MustCompile(`^\s*([a-zA-Z0-9_\-./]+\.go:\d+)(?::\s*(.*))?$`)

	// Playwright structured failure
	playwrightHeaderRegex = regexp.MustCompile(`(?m)^\s*(?:\d+\)\s+)?(?:(\[[^\]]+\])\s+›\s+)?([^\s:]+\.(?:spec|test)\.[tj]sx?:\d+:\d+)\s+›\s+(.*)`)
	playwrightNumRegex    = regexp.MustCompile(`(?m)^\s*\d+\)\s+(?:(\[[^\]]+\])\s+›\s+)?(.*)`)
	jestTestRegex         = regexp.MustCompile(`(?m)^\s*[●✕]\s+(.*)`)

	// JS/TS stack location
	jsLocationRegex = regexp.MustCompile(`^\s*at\s+(?:.*?\s+\()?([^\s():]+\.(?:ts|js|tsx|jsx|spec\.[tj]sx?|test\.[tj]sx?):\d+(?::\d+)?)\)?`)

	// Standard compiler / linter diagnostics: path/to/file.ext:line:col: [error:] message
	standardDiagnosticRegex = regexp.MustCompile(`(?m)^([a-zA-Z0-9_\-./]+\.(?:go|ts|js|tsx|jsx|rs|py|c|cpp|h|proto|yaml|yml|json)):(\d+)(?::(\d+))?:\s*(?:(error|warning|fatal|note):\s*)?(.*?)(?:\s+\(([a-zA-Z0-9_\-\.]+)\))?$`)
	// TypeScript compiler: path/file.ts(line,col): error TS1234: message
	tsDiagnosticRegex = regexp.MustCompile(`(?m)^([a-zA-Z0-9_\-./]+\.(?:ts|tsx))\(([0-9]+),([0-9]+)\):\s*(error|warning)\s*(TS[0-9]+):\s*(.*)$`)
	// ESLint line under file header
	eslintLineRegex = regexp.MustCompile(`^\s*(\d+):(\d+)\s+(error|warning)\s+(.*?)(?:\s{2,}(\S+))?$`)
	fileHeaderRegex = regexp.MustCompile(`^([a-zA-Z0-9_\-./]+\.(?:js|jsx|ts|tsx))$`)

	// Panic regex
	panicLineRegex = regexp.MustCompile(`(?m)^panic:\s*(.*)$`)
)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// ExtractFailureSummary parses raw build or test output to extract structured failures,
// diagnostics, and high-signal error lines.
func ExtractFailureSummary(rawOutput string) *FailureSummary {
	if strings.TrimSpace(rawOutput) == "" {
		return nil
	}
	clean := stripANSI(rawOutput)
	clean = strings.ReplaceAll(clean, "\r\n", "\n")

	summary := &FailureSummary{}
	usedLocations := make(map[string]bool)

	// 1. Parse Go test failures
	parseGoTests(clean, summary, usedLocations)

	// 2. Parse Playwright / Jest test failures
	parseNodeTests(clean, summary, usedLocations)

	// 3. Parse Compiler & Linter Diagnostics
	parseDiagnostics(clean, summary, usedLocations)

	// 4. Parse unhandled panics/fatals if not already in a test failure
	parseRawErrors(clean, summary)

	if summary.Empty() {
		return nil
	}
	return summary
}

func isStdlibGoLocation(loc string) bool {
	return loc == "testing.go" ||
		strings.HasPrefix(loc, "testing.go:") ||
		strings.Contains(loc, "/src/runtime/") ||
		strings.Contains(loc, "/src/testing/") ||
		strings.HasPrefix(loc, "runtime/") ||
		strings.HasPrefix(loc, "testing/")
}

func isSectionOrRunBoundary(trimmed string) bool {
	return strings.HasPrefix(trimmed, "=== ") ||
		strings.HasPrefix(trimmed, "--- PASS:") ||
		strings.HasPrefix(trimmed, "--- SKIP:") ||
		trimmed == "PASS" ||
		strings.HasPrefix(trimmed, "FAIL\t") ||
		strings.HasPrefix(trimmed, "ok  \t") ||
		strings.HasPrefix(trimmed, "ok \t")
}

func parseGoTests(output string, summary *FailureSummary, usedLocations map[string]bool) {
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		locMatch := goTestFailRegex.FindStringSubmatch(line)
		if len(locMatch) == 0 {
			continue
		}
		testName := locMatch[1]
		tf := TestFailure{Name: testName}

		var msgLines []string
		var diffLines []string
		var panicLines []string
		sawGoLocMsg := false

		// Find lookbehind start boundary before `--- FAIL:` (up to 25 lines back).
		startB := max(0, i-25)
		for b := i - 1; b >= startB; b-- {
			prevTrim := strings.TrimSpace(lines[b])
			if isSectionOrRunBoundary(prevTrim) {
				startB = b + 1
				break
			}
			if prevFail := goTestFailRegex.FindStringSubmatch(lines[b]); len(prevFail) > 0 {
				prevName := prevFail[1]
				if !strings.HasPrefix(testName, prevName+"/") {
					startB = i
					break
				}
			}
		}

		// Check lines before `--- FAIL:` for test error lines
		for b := startB; b < i; b++ {
			prevTrim := strings.TrimSpace(lines[b])
			if strings.HasPrefix(prevTrim, "--- FAIL:") {
				continue
			}
			if m := goLocRegex.FindStringSubmatch(lines[b]); len(m) > 0 {
				usedLocations[m[1]] = true
				if tf.Location == "" || (isStdlibGoLocation(tf.Location) && !isStdlibGoLocation(m[1])) {
					tf.Location = m[1]
				}
				if m[2] != "" {
					if !sawGoLocMsg {
						msgLines = nil
						sawGoLocMsg = true
					}
					if len(msgLines) < 5 {
						msgLines = append(msgLines, m[2])
					}
				}
			} else if strings.HasPrefix(prevTrim, "- ") || strings.HasPrefix(prevTrim, "+ ") ||
				strings.HasPrefix(prevTrim, "diff:") || strings.HasPrefix(prevTrim, "Diff:") ||
				strings.HasPrefix(prevTrim, "got:") || strings.HasPrefix(prevTrim, "want:") {
				diffLines = append(diffLines, prevTrim)
			} else if prevTrim != "" && !sawGoLocMsg && len(msgLines) < 4 && !isSectionOrRunBoundary(prevTrim) {
				msgLines = append(msgLines, prevTrim)
			}
		}

		// Check lines after `--- FAIL:` (up to 80 lines to cover run-go-tests.sh diag+tail buffers)
		inPanic := false
		inRace := false
		panicSawStack := false
		j := i + 1
		for j < len(lines) && j < i+80 {
			subLine := lines[j]
			trimmed := strings.TrimSpace(subLine)
			if strings.HasPrefix(trimmed, "--- FAIL:") || isSectionOrRunBoundary(trimmed) ||
				(trimmed == "FAIL" && !inPanic) {
				break
			}
			if strings.HasPrefix(trimmed, "==================") {
				if inRace {
					inPanic = false
					inRace = false
				}
				j++
				continue
			}
			if !inPanic && strings.HasPrefix(trimmed, "WARNING: DATA RACE") {
				inPanic = true
				inRace = true
				panicSawStack = false
				panicLines = append(panicLines, trimmed)
				j++
				continue
			}
			if panicMatch := panicLineRegex.FindStringSubmatch(trimmed); len(panicMatch) > 0 && !inPanic {
				inPanic = true
				inRace = false
				panicSawStack = false
				panicLines = append(panicLines, "panic: "+panicMatch[1])
				j++
				continue
			}
			if inPanic {
				if trimmed == "" {
					if !inRace && panicSawStack {
						inPanic = false
					}
					j++
					continue
				}
				if strings.HasPrefix(trimmed, "FAIL\t") || trimmed == "FAIL" {
					break
				}
				if strings.HasPrefix(trimmed, "goroutine ") || strings.HasPrefix(subLine, "\t") || strings.HasPrefix(subLine, "  ") {
					panicSawStack = true
				}
				panicLines = append(panicLines, subLine)
				if strings.Contains(subLine, ".go:") {
					for _, p := range strings.Fields(trimmed) {
						if strings.Contains(p, ".go:") {
							usedLocations[p] = true
							if tf.Location == "" || (isStdlibGoLocation(tf.Location) && !isStdlibGoLocation(p)) {
								tf.Location = p
							}
							break
						}
					}
				}
				j++
				continue
			}

			if m := goLocRegex.FindStringSubmatch(subLine); len(m) > 0 {
				usedLocations[m[1]] = true
				if tf.Location == "" || (isStdlibGoLocation(tf.Location) && !isStdlibGoLocation(m[1])) {
					tf.Location = m[1]
				}
				if m[2] != "" {
					if !sawGoLocMsg {
						msgLines = nil
						sawGoLocMsg = true
					}
					if len(msgLines) < 5 {
						msgLines = append(msgLines, m[2])
					}
				}
			} else if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "+ ") ||
				strings.HasPrefix(trimmed, "diff:") || strings.HasPrefix(trimmed, "Diff:") ||
				strings.HasPrefix(trimmed, "got:") || strings.HasPrefix(trimmed, "want:") {
				diffLines = append(diffLines, trimmed)
			} else if trimmed != "" && !sawGoLocMsg && len(msgLines) < 5 {
				msgLines = append(msgLines, trimmed)
			}
			j++
		}

		if len(panicLines) > 0 {
			tf.Message = strings.Join(panicLines, "\n")
		} else if len(msgLines) > 0 {
			tf.Message = strings.Join(msgLines, "\n")
		}
		if len(diffLines) > 0 {
			tf.Diff = strings.Join(diffLines, "\n")
		}

		merged := false
		for idx := range summary.Tests {
			if summary.Tests[idx].Name == tf.Name {
				if (summary.Tests[idx].Location == "" || isStdlibGoLocation(summary.Tests[idx].Location)) &&
					tf.Location != "" && !isStdlibGoLocation(tf.Location) {
					summary.Tests[idx].Location = tf.Location
				} else if summary.Tests[idx].Location == "" && tf.Location != "" {
					summary.Tests[idx].Location = tf.Location
				}
				if summary.Tests[idx].Message == "" && tf.Message != "" {
					summary.Tests[idx].Message = tf.Message
				}
				if summary.Tests[idx].Diff == "" && tf.Diff != "" {
					summary.Tests[idx].Diff = tf.Diff
				}
				merged = true
				break
			}
		}
		if !merged {
			summary.Tests = append(summary.Tests, tf)
			if len(summary.Tests) >= 20 {
				break
			}
		}
	}
}

func parseNodeTests(output string, summary *FailureSummary, usedLocations map[string]bool) {
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		var testName string
		var location string

		if m := playwrightHeaderRegex.FindStringSubmatch(line); len(m) > 0 {
			location = m[2]
			testName = m[3]
			if m[1] != "" {
				testName = m[1] + " " + testName
			}
		} else if m := playwrightNumRegex.FindStringSubmatch(line); len(m) > 0 {
			testName = m[2]
			if m[1] != "" {
				testName = m[1] + " " + testName
			}
		} else if m := jestTestRegex.FindStringSubmatch(line); len(m) > 0 {
			testName = m[1]
		}

		testName = strings.Trim(testName, " ─\t\r\n")

		if testName == "" {
			continue
		}

		// Avoid duplicates if already recorded
		alreadyHave := false
		for _, existing := range summary.Tests {
			if strings.Trim(existing.Name, " ─\t\r\n") == testName {
				alreadyHave = true
				break
			}
		}
		if alreadyHave {
			continue
		}

		tf := TestFailure{Name: testName, Location: location}
		if location != "" {
			usedLocations[location] = true
		}

		var msgLines []string
		var diffLines []string

		j := i + 1
		for j < len(lines) && j < i+35 {
			subLine := lines[j]
			subTrimmed := strings.TrimSpace(subLine)
			if playwrightHeaderRegex.MatchString(subLine) || playwrightNumRegex.MatchString(subLine) ||
				jestTestRegex.MatchString(subLine) || strings.HasPrefix(subTrimmed, "Test Suites:") {
				break
			}

			if locMatch := jsLocationRegex.FindStringSubmatch(subLine); len(locMatch) > 0 && tf.Location == "" {
				tf.Location = locMatch[1]
				usedLocations[tf.Location] = true
			} else if strings.HasPrefix(subTrimmed, "- Expected") || strings.HasPrefix(subTrimmed, "+ Received") ||
				strings.HasPrefix(subTrimmed, "Expected:") || strings.HasPrefix(subTrimmed, "Received:") ||
				strings.HasPrefix(subTrimmed, "- ") || strings.HasPrefix(subTrimmed, "+ ") {
				diffLines = append(diffLines, subTrimmed)
			} else if strings.HasPrefix(subTrimmed, "Error:") || strings.HasPrefix(subTrimmed, "TypeError:") ||
				strings.HasPrefix(subTrimmed, "expect(") {
				msgLines = append(msgLines, subTrimmed)
			} else if subTrimmed != "" && len(msgLines) < 4 && !strings.HasPrefix(subTrimmed, "at ") {
				msgLines = append(msgLines, subTrimmed)
			}
			j++
		}

		if len(msgLines) > 0 {
			tf.Message = strings.Join(msgLines, "\n")
		}
		if len(diffLines) > 0 {
			tf.Diff = strings.Join(diffLines, "\n")
		}

		summary.Tests = append(summary.Tests, tf)
		if len(summary.Tests) >= 20 {
			break
		}
	}
}

func parseDiagnostics(output string, summary *FailureSummary, usedLocations map[string]bool) {
	lines := strings.Split(output, "\n")
	seenDiag := make(map[string]bool)

	var currentFileHeader string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check if line is a file header (for ESLint)
		if fileHeaderRegex.MatchString(trimmed) {
			currentFileHeader = trimmed
			continue
		}

		// 1. Standard diagnostics: path/to/file.ext:12:34: [error:] message [(rule)]
		if m := standardDiagnosticRegex.FindStringSubmatch(trimmed); len(m) > 0 {
			file := m[1]
			lineNum, _ := strconv.Atoi(m[2])
			colNum, _ := strconv.Atoi(m[3])
			sev := m[4]
			msg := m[5]
			rule := m[6]

			locKey := fmt.Sprintf("%s:%d", file, lineNum)
			if usedLocations[locKey] && len(summary.Tests) > 0 {
				continue
			}
			// Indented `file.go:line: msg` without a column is `go test` t.Logf/t.Errorf output,
			// not a compiler or linter diagnostic.
			if m[3] == "" && strings.HasSuffix(file, ".go") && (strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")) {
				continue
			}

			key := fmt.Sprintf("%s:%d:%d:%s", file, lineNum, colNum, msg)
			if seenDiag[key] {
				continue
			}
			seenDiag[key] = true

			summary.Diagnostics = append(summary.Diagnostics, Diagnostic{
				File:     file,
				Line:     lineNum,
				Column:   colNum,
				Severity: sev,
				Message:  msg,
				Rule:     rule,
			})
			if len(summary.Diagnostics) >= 20 {
				return
			}
			continue
		}

		// 2. TypeScript diagnostics: path/to/file.ts(12,34): error TS1234: message
		if m := tsDiagnosticRegex.FindStringSubmatch(trimmed); len(m) > 0 {
			file := m[1]
			lineNum, _ := strconv.Atoi(m[2])
			colNum, _ := strconv.Atoi(m[3])
			sev := m[4]
			rule := m[5]
			msg := m[6]

			key := fmt.Sprintf("%s:%d:%d:%s", file, lineNum, colNum, msg)
			if seenDiag[key] {
				continue
			}
			seenDiag[key] = true

			summary.Diagnostics = append(summary.Diagnostics, Diagnostic{
				File:     file,
				Line:     lineNum,
				Column:   colNum,
				Severity: sev,
				Rule:     rule,
				Message:  msg,
			})
			if len(summary.Diagnostics) >= 20 {
				return
			}
			continue
		}

		// 3. ESLint style: 12:34 error message rule
		if currentFileHeader != "" {
			if m := eslintLineRegex.FindStringSubmatch(trimmed); len(m) > 0 {
				lineNum, _ := strconv.Atoi(m[1])
				colNum, _ := strconv.Atoi(m[2])
				sev := m[3]
				msg := m[4]
				rule := m[5]

				key := fmt.Sprintf("%s:%d:%d:%s", currentFileHeader, lineNum, colNum, msg)
				if seenDiag[key] {
					continue
				}
				seenDiag[key] = true

				summary.Diagnostics = append(summary.Diagnostics, Diagnostic{
					File:     currentFileHeader,
					Line:     lineNum,
					Column:   colNum,
					Severity: sev,
					Rule:     rule,
					Message:  msg,
				})
				if len(summary.Diagnostics) >= 20 {
					return
				}
				continue
			}
		}
	}
}

func normalizePanicHeadline(line string) string {
	s := strings.TrimSpace(line)
	if strings.HasSuffix(s, "]") {
		if idx := strings.LastIndex(s, " ["); idx > 0 {
			tag := s[idx+2 : len(s)-1]
			if !strings.HasPrefix(tag, "recovered") {
				s = strings.TrimSpace(s[:idx])
			}
		}
	}
	return s
}

func parseRawErrors(output string, summary *FailureSummary) {
	lines := strings.Split(output, "\n")
	seenRaw := make(map[string]bool)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
			norm := normalizePanicHeadline(line)
			alreadyInTest := false
			for _, t := range summary.Tests {
				if strings.Contains(t.Message, line) || (norm != "" && strings.Contains(t.Message, norm)) {
					alreadyInTest = true
					break
				}
			}
			if alreadyInTest || seenRaw[norm] {
				continue
			}
			seenRaw[norm] = true

			var panicBlock []string
			panicBlock = append(panicBlock, line)
			j := i + 1
			for j < len(lines) && j < i+12 {
				sub := lines[j]
				subTrim := strings.TrimSpace(sub)
				if subTrim == "" && len(panicBlock) == 1 {
					j++
					continue
				}
				if strings.HasPrefix(subTrim, "goroutine ") || strings.HasPrefix(sub, "\t") || strings.HasPrefix(sub, "    ") {
					panicBlock = append(panicBlock, subTrim)
					j++
				} else {
					break
				}
			}
			summary.RawErrors = append(summary.RawErrors, strings.Join(panicBlock, "\n"))
			if len(summary.RawErrors) >= 5 {
				break
			}
		}
	}
}
