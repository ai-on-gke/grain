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

package trustboundary

import (
	"reflect"
	"strings"
	"testing"
)

func TestClassifyStateHoldsEverythingByDefault(t *testing.T) {
	held := []string{
		"settings/config-policy.yml",
		"settings/capabilities.yml",
		"settings/new-task-form.yml",
		"settings/templates.yml",
		"settings/schedules.yml",
		"settings/suites.yml",
		"tables/grain_config.json",
		"tables/repo_config.json",
		"tables/api_scope_set.json",
		"tables/git_credential.json",
		"tables/repo_git_credential.json",
		"tables/agent_backend.json",
		"tables/console_session.json",
		"tables/repo_workflow.json",
		"tables/template_grant.json",
		"tables/standing_sandbox_secret.json",
		"tables/gcp_key.json",
		"tables/board.json",
		"tables/custom_field.json",
		"tables/task.json",
		"tables/some_table_added_later.json",
		"memory/deployment.md",
		"memory/documents/teams/default/notes.md",
		"release-notes/2026-W43.md",
		"README.md",
		"scripts/guest-customization.sh",
		"scripts/repos/acme/widgets/setup.sh",
		"workflows/acme/widgets/ci.yml",
		".github/workflows/grain-state-check.yml",
		".grain/workflows/ci.yml",
		"schema-version",
		"./tables/../tables/gcp_key.json",
	}
	got := ClassifyState(held)
	// The last path cleans to "tables/gcp_key.json", already listed above.
	if len(got) != 1 || got[0].Category.ID != StatePrivilege.ID || !got[0].Category.RequiresHuman || len(got[0].Paths) != len(held)-1 {
		t.Fatalf("ClassifyState(%v) = %+v, want every unique path under StatePrivilege (RequiresHuman)", held, got)
	}
	if !RequiresHuman(got) {
		t.Error("state-privilege must not be releasable by an automated review")
	}
}

func TestClassifyStateAutoMergePathsOptIn(t *testing.T) {
	exceptions := []string{
		"memory/**",
		"release-notes/**",
		"tables/board*.json",
		"README.md",
	}

	exempt := []string{
		"memory/deployment.md",
		"memory/documents/teams/default/notes.md",
		"release-notes/2026-W43.md",
		"tables/board.json",
		"tables/board_column.json",
		"README.md",
	}
	if got := ClassifyState(exempt, exceptions...); got != nil {
		t.Fatalf("ClassifyState(%v, %v) = %+v, want nil (all matched by auto_merge_paths)", exempt, exceptions, got)
	}

	// Mixed change: exempt paths are filtered out, non-exempt paths remain held.
	mixed := append(append([]string{}, exempt...), "tables/custom_field.json", "settings/boards.yml")
	got := ClassifyState(mixed, exceptions...)
	if len(got) != 1 || !got[0].Category.RequiresHuman {
		t.Fatalf("ClassifyState(mixed) = %+v, want 1 human-required match", got)
	}
	wantPaths := []string{"settings/boards.yml", "tables/custom_field.json"}
	if !reflect.DeepEqual(got[0].Paths, wantPaths) {
		t.Errorf("ClassifyState(mixed) paths = %v, want %v", got[0].Paths, wantPaths)
	}
}

func TestClassifyStateNeverAutoMergePathsCeiling(t *testing.T) {
	// Concrete sample paths covering every entry in NeverAutoMergePaths.
	neverConcrete := []string{
		"settings/config-policy.yml",
		"settings/capabilities.yml",
		"settings/new-task-form.yml",
		"settings/templates.yml",
		"settings/schedules.yml",
		"settings/suites.yml",
		"tables/grain_config.json",
		"tables/repo_config.json",
		"tables/api_scope_set.json",
		"tables/git_credential.json",
		"tables/repo_git_credential.json",
		"tables/agent_backend.json",
		"tables/console_session.json",
		"tables/repo_workflow.json",
		"scripts/guest-customization.sh",
		"scripts/repos/acme/widgets/setup.sh",
		"workflows/acme/widgets/ci.yml",
		".github/workflows/grain-state-check.yml",
		".grain/workflows/ci.yml",
		"schema-version",
	}

	// Even if autoMergePaths tries to opt in everything ("**") or names
	// the exact path, NeverAutoMergePaths is evaluated first and holds it.
	broadExceptions := append([]string{"**", "settings/**", "tables/**", "scripts/**", "workflows/**", ".github/**", ".grain/**"}, NeverAutoMergePaths...)
	for _, p := range neverConcrete {
		if !IsNeverAutoMergePath(p) {
			t.Errorf("IsNeverAutoMergePath(%q) = false, want true", p)
		}
		got := ClassifyState([]string{p}, broadExceptions...)
		if len(got) != 1 || !RequiresHuman(got) || len(got[0].Paths) != 1 || got[0].Paths[0] != p {
			t.Errorf("ClassifyState([%q], broadExceptions) = %+v, want held by NeverAutoMergePaths ceiling", p, got)
		}
	}
}

// TestNeverAutoMergePathsCoverGrantAndSecretTables pins the tables the
// 2026-10-29 security review found missing from the ceiling: rows that
// bind a deployment secret into a sandbox, name a GCP key, or grant a
// template or schedule a capability or a write repository. Listing one
// in auto_merge_paths must be refused, and must not release it.
func TestNeverAutoMergePathsCoverGrantAndSecretTables(t *testing.T) {
	for _, p := range []string{
		"tables/standing_sandbox.json",
		"tables/standing_sandbox_secret.json",
		"tables/standing_sandbox_env.json",
		"tables/persistent_sandbox.json",
		"tables/gcp_key.json",
		"tables/template_grant.json",
		"tables/template_write.json",
		"tables/schedule_grant.json",
		"tables/schedule_write.json",
	} {
		if !IsNeverAutoMergePath(p) {
			t.Errorf("IsNeverAutoMergePath(%q) = false, want true", p)
		}
		got := ClassifyState([]string{p}, p)
		if len(got) != 1 || got[0].Category.ID != StatePrivilege.ID || !reflect.DeepEqual(got[0].Paths, []string{p}) {
			t.Errorf("ClassifyState([%q], %q) = %+v, want a StatePrivilege match holding it", p, p, got)
		}
		err := ValidateConfigPolicy(ConfigPolicy{AutoMergePaths: []string{p}})
		if err == nil || !strings.Contains(err.Error(), "NeverAutoMergePaths entry \""+p+"\"") {
			t.Errorf("ValidateConfigPolicy(%q) = %v, want an error naming the ceiling entry", p, err)
		}
	}
}

func TestParseAndCheckConfigPolicy(t *testing.T) {
	// Empty or comment-only policy is valid with zero exceptions.
	for _, empty := range []string{"", "   \n", "# comment only\n"} {
		pol, err := CheckConfigPolicy([]byte(empty))
		if err != nil {
			t.Fatalf("CheckConfigPolicy(%q) unexpected error: %v", empty, err)
		}
		if len(pol.AutoMergePaths) != 0 {
			t.Fatalf("CheckConfigPolicy(%q) = %+v, want empty AutoMergePaths", empty, pol)
		}
	}

	// Valid opt-in policy.
	validYAML := "auto_merge_paths:\n  - memory/**\n  - release-notes/**\n  - tables/board*.json\n"
	pol, err := CheckConfigPolicy([]byte(validYAML))
	if err != nil {
		t.Fatalf("CheckConfigPolicy(valid) unexpected error: %v", err)
	}
	want := []string{"memory/**", "release-notes/**", "tables/board*.json"}
	if !reflect.DeepEqual(pol.AutoMergePaths, want) {
		t.Errorf("AutoMergePaths = %v, want %v", pol.AutoMergePaths, want)
	}

	// Syntax errors, unknown fields, and invalid globs are rejected.
	invalidSyntaxes := []string{
		"auto_merge_paths: [unclosed",
		"unknown_field:\n  - memory/**\n",
		"auto_merge_paths:\n  - /leading/slash\n",
		"auto_merge_paths:\n  - trailing/slash/\n",
		"auto_merge_paths:\n  - ../escape\n",
		"auto_merge_paths:\n  - foo**bar/baz\n",
		"auto_merge_paths:\n  - \"\"\n",
	}
	for _, bad := range invalidSyntaxes {
		if _, err := ParseConfigPolicy([]byte(bad)); err == nil {
			t.Errorf("ParseConfigPolicy(%q) succeeded, want error", bad)
		}
	}

	// Every NeverAutoMergePaths entry, as well as wildcards overlapping it,
	// must be rejected by CheckConfigPolicy / ValidateConfigPolicy.
	rejectedPatterns := append([]string{}, NeverAutoMergePaths...)
	rejectedPatterns = append(rejectedPatterns,
		"**",
		"*",
		"settings/*",
		"settings/**",
		"tables/*.json",
		"tables/**",
		"scripts/setup.sh",
		"workflows/ci.yml",
		".github/workflows/ci.yml",
		".grain/workflows/ci.yml",
	)
	for _, pat := range rejectedPatterns {
		err := ValidateConfigPolicy(ConfigPolicy{AutoMergePaths: []string{pat}})
		if err == nil {
			t.Errorf("ValidateConfigPolicy(auto_merge_paths: [%q]) succeeded, want rejection against NeverAutoMergePaths", pat)
			continue
		}
		if !strings.Contains(err.Error(), "NeverAutoMergePaths") {
			t.Errorf("ValidateConfigPolicy(%q) error = %v, want mention of NeverAutoMergePaths", pat, err)
		}
	}
}

// The state repository's layout means nothing anywhere else: the same
// paths in an ordinary repository are judged by Classify alone.
func TestClassifyIgnoresStatePaths(t *testing.T) {
	if got := Classify([]string{"tables/template_grant.json", "settings/templates.yml"}); got != nil {
		t.Errorf("Classify = %v, want nothing", categoryIDs(got))
	}
}
