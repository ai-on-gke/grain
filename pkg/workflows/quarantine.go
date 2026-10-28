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

package workflows

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// QuarantineConfig contains a list of quarantined test definitions.
type QuarantineConfig struct {
	Tests            []QuarantineEntry `yaml:"tests,omitempty" json:"tests,omitempty"`
	QuarantinedTests []QuarantineEntry `yaml:"quarantined_tests,omitempty" json:"quarantined_tests,omitempty"`
}

// QuarantineEntry defines a single quarantined test rule and its tracking metadata.
type QuarantineEntry struct {
	TestName       string     `yaml:"test_name" json:"test_name"`
	Reason         string     `yaml:"reason" json:"reason"`
	TaskID         string     `yaml:"task_id,omitempty" json:"task_id,omitempty"`
	FollowUpTaskID string     `yaml:"follow_up_task_id,omitempty" json:"follow_up_task_id,omitempty"`
	CreatedAt      *time.Time `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	ExpiresAt      *time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	TTL            string     `yaml:"ttl,omitempty" json:"ttl,omitempty"`
}

// AllTests returns the combined list of quarantine entries across both yaml field synonyms.
func (c *QuarantineConfig) AllTests() []QuarantineEntry {
	if c == nil {
		return nil
	}
	var res []QuarantineEntry
	res = append(res, c.Tests...)
	res = append(res, c.QuarantinedTests...)
	return res
}

// IsActive reports whether the quarantine entry is still active at reference time now.
func (e *QuarantineEntry) IsActive(now time.Time) bool {
	if e == nil {
		return false
	}
	if e.ExpiresAt != nil && !e.ExpiresAt.IsZero() {
		if now.After(*e.ExpiresAt) {
			return false
		}
	}
	if e.TTL != "" && e.CreatedAt != nil && !e.CreatedAt.IsZero() {
		dur, err := parseDuration(e.TTL)
		if err == nil && dur > 0 {
			if now.After(e.CreatedAt.Add(dur)) {
				return false
			}
		}
	}
	return true
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(daysStr, "%d", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

// Matches reports whether the quarantined test rule matches testName.
// Matching supports:
// 1. Exact match (e.g. "TestFlakyReconcile" or "pkg/orchestrator_test.TestFlakyReconcile")
// 2. Qualified test name or package suffix (e.g. "pkg/orchestrator_test.TestFlakyReconcile" matches "TestFlakyReconcile")
// 3. Subtest prefix (e.g. rule "TestFlakyReconcile" matches subtest "TestFlakyReconcile/SubTest1")
// 4. Glob pattern match (e.g. "*Flaky*")
//
// test_name is deliberately never read as a regular expression: the file
// is committed to the repository whose tests it quarantines, and a value
// as innocent-looking as ".*" (or a test name with an unescaped ".") would
// otherwise match every test in the suite and take it out of the gate.
func (e *QuarantineEntry) Matches(testName string) bool {
	if e == nil || strings.TrimSpace(e.TestName) == "" || strings.TrimSpace(testName) == "" {
		return false
	}
	pattern := strings.TrimSpace(e.TestName)
	candidate := strings.TrimSpace(testName)

	if pattern == candidate {
		return true
	}

	// Subtest match: rule "TestFoo" matches "TestFoo/Sub1"
	if strings.HasPrefix(candidate, pattern+"/") {
		return true
	}

	// Suffix / package qualified: candidate is "pkg/foo.TestBar", pattern is "TestBar"
	if strings.HasSuffix(candidate, "."+pattern) || strings.HasSuffix(candidate, "/"+pattern) {
		return true
	}

	// Suffix / package qualified reverse: pattern is "pkg/foo.TestBar", candidate is "TestBar"
	if strings.HasSuffix(pattern, "."+candidate) || strings.HasSuffix(pattern, "/"+candidate) {
		return true
	}

	// Glob match
	if matched, err := filepath.Match(pattern, candidate); err == nil && matched {
		return true
	}

	return false
}

// FindMatch finds the first active QuarantineEntry matching testName at reference time now.
func (c *QuarantineConfig) FindMatch(testName string, now time.Time) *QuarantineEntry {
	if c == nil {
		return nil
	}
	for _, entry := range c.AllTests() {
		if entry.IsActive(now) && entry.Matches(testName) {
			cp := entry
			return &cp
		}
	}
	return nil
}

// LoadQuarantine parses raw YAML bytes into a QuarantineConfig.
func LoadQuarantine(data []byte) (*QuarantineConfig, error) {
	if len(data) == 0 {
		return &QuarantineConfig{}, nil
	}
	var cfg QuarantineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// Also try unmarshaling as a bare list of entries
		var list []QuarantineEntry
		if err2 := yaml.Unmarshal(data, &list); err2 == nil {
			return &QuarantineConfig{Tests: list}, nil
		}
		return nil, fmt.Errorf("parsing quarantine yaml: %w", err)
	}
	return &cfg, nil
}

// EvaluateQuarantine inspects a FailureSummary against QuarantineConfig.
// If there are test failures and EVERY failed test matches an active quarantine rule,
// and there are no compilation/linter diagnostics or raw unparseable errors,
// allQuarantined is true, allowing non-blocking CI passage.
func EvaluateQuarantine(summary *FailureSummary, q *QuarantineConfig, now time.Time) (allQuarantined bool, quarantinedNames []string, unquarantinedTests []TestFailure) {
	if summary == nil || summary.Empty() {
		return false, nil, nil
	}
	if q == nil || len(q.AllTests()) == 0 {
		return false, nil, summary.Tests
	}

	// If there are compiler/linter diagnostics or raw errors, it's not purely a quarantined test failure.
	if len(summary.Diagnostics) > 0 || len(summary.RawErrors) > 0 {
		return false, nil, summary.Tests
	}

	if len(summary.Tests) == 0 {
		return false, nil, nil
	}

	for i := range summary.Tests {
		t := &summary.Tests[i]
		if match := q.FindMatch(t.Name, now); match != nil {
			t.Quarantined = true
			quarantinedNames = append(quarantinedNames, t.Name)
		} else {
			unquarantinedTests = append(unquarantinedTests, *t)
		}
	}

	if len(unquarantinedTests) == 0 && len(quarantinedNames) > 0 {
		allQuarantined = true
	}

	return allQuarantined, quarantinedNames, unquarantinedTests
}
