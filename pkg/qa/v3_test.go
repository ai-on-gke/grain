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

package qa

import (
	"slices"
	"strings"
	"testing"
)

func TestBuiltInGoldenV3AndChildInheritance(t *testing.T) {
	b, ok := LookupBuiltInOrRegisteredVersion("golden-v3@1")
	if !ok {
		t.Fatal("expected golden-v3@1 to be registered in BuiltInFlowVersions")
	}
	if b.SchemaVer != FlowSchemaV3 {
		t.Fatalf("got SchemaVer %q, want %q", b.SchemaVer, FlowSchemaV3)
	}
	if !b.Resolved.S0Intent || !b.Resolved.S2PlanCritic {
		t.Fatalf("expected S0Intent and S2PlanCritic enabled on golden-v3@1: %+v", b.Resolved)
	}
	if b.Resolved.S5MergeTriage.Mode != "shadow" {
		t.Fatalf("got S5MergeTriage.Mode %q, want shadow", b.Resolved.S5MergeTriage.Mode)
	}
	if len(b.Resolved.CustomChecks) != 2 {
		t.Fatalf("got %d CustomChecks, want 2", len(b.Resolved.CustomChecks))
	}

	childYAML := `schema: grain.dev/qa-flow/v3
key: golden-v3-enforced
extends: golden-v3@1
stages:
  s5-merge-triage:
    mode: enforced
`
	child, err := ResolveAnyFlowDeclaration([]byte(childYAML), 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration(golden-v3-enforced): %v", err)
	}
	if child.Resolved.S5MergeTriage.Mode != "enforced" {
		t.Fatalf("got child mode %q, want enforced", child.Resolved.S5MergeTriage.Mode)
	}
	if len(child.Resolved.CustomChecks) != 2 || !child.Resolved.S0Intent {
		t.Fatalf("expected child to inherit parent custom checks and S0Intent: %+v", child.Resolved)
	}
	if child.Hash == b.Hash {
		t.Fatalf("expected distinct hash when S5 triage mode changes from shadow to enforced")
	}

	v2, _ := LookupBuiltInOrRegisteredVersion("golden-v2@1")
	diff := ComputeDeclarationDiff(&v2, &child)
	if !slices.Contains(diff.AddedStages, "s0-intent") || !slices.Contains(diff.AddedStages, "s2-plan-critic") {
		t.Errorf("expected s0-intent and s2-plan-critic in AddedStages, got %+v", diff.AddedStages)
	}
	if len(diff.AddedCustomChecks) != 2 || !diff.ChangedTriage {
		t.Errorf("unexpected DeclarationDiff: %+v", diff)
	}
}

func TestV3RejectsArbitraryShellCmdAndFloorWeakening(t *testing.T) {
	shellYAML := `schema: grain.dev/qa-flow/v3
key: unsafe-shell
custom-checks:
  - id: custom:bad
    label: Bad
    kind: pattern
    cmd: "rm -rf /"
`
	if _, err := ParseFlowDeclaration([]byte(shellYAML)); err == nil {
		t.Fatal("expected strict YAML decoder to reject unknown cmd: field")
	}

	floorYAML := `schema: grain.dev/qa-flow/v3
key: floor-weaken
extends: golden-v2@1
checks:
  golden:test-integrity-guard:
    enabled: false
`
	if _, err := ResolveAnyFlowDeclaration([]byte(floorYAML), 1, nil); err == nil {
		t.Fatal("expected ComposeTightenOnly floor check disabling to fail")
	}
}

func TestCustomCheckEnginesAndMandatoryFixtures(t *testing.T) {
	// 1. Missing pass/fail fixture rejected
	noFixtureYAML := `schema: grain.dev/qa-flow/v3
key: missing-fixtures
custom-checks:
  - id: custom:pat
    label: Pat
    kind: pattern
    forbid-added: 'TODO_FORBIDDEN'
    message: no forbidden todo
    fixtures:
      fail: []
      pass: []
`
	if _, err := ResolveAnyFlowDeclaration([]byte(noFixtureYAML), 1, nil); err == nil {
		t.Fatal("expected empty fixtures to be rejected")
	}

	// 2. All 4 engines (pattern, diff-budget, companion, jsonschema) pass self-test
	allFourYAML := `schema: grain.dev/qa-flow/v3
key: all-four-engines
extends: golden-v2@1
custom-checks:
  - id: custom:pat
    label: Forbid debug print
    kind: pattern
    severity: blocking
    paths: ["pkg/**/*.go"]
    exclude: ["**/*_test.go"]
    forbid-added: 'DEBUG_PRINT\('
    message: Remove DEBUG_PRINT
    fixtures:
      fail:
        - name: hits prod go
          file: pkg/foo/bar.go
          added-lines: ["DEBUG_PRINT(1)"]
      pass:
        - name: ignores test go
          file: pkg/foo/bar_test.go
          added-lines: ["DEBUG_PRINT(1)"]
  - id: custom:budget
    label: Small config diff budget
    kind: diff-budget
    severity: warn
    paths: ["configs/**"]
    max-files: 2
    max-changed-lines: 3
    message: Keep config diffs small
    fixtures:
      fail:
        - name: too many files
          changed-files: ["configs/a.yml", "configs/b.yml", "configs/c.yml"]
      pass:
        - name: two small files
          changed-files: ["configs/a.yml", "configs/b.yml"]
  - id: custom:comp
    label: Schema requires migration
    kind: companion
    severity: blocking
    when-changed: ["pkg/model/schema.go"]
    require-changed: ["pkg/model/migrations.go"]
    message: Add migration alongside schema edit
    fixtures:
      fail:
        - name: schema alone
          changed-files: ["pkg/model/schema.go"]
      pass:
        - name: schema with migration
          changed-files: ["pkg/model/schema.go", "pkg/model/migrations.go"]
  - id: custom:schema
    label: Manifest JSON schema
    kind: jsonschema
    severity: blocking
    paths: ["manifests/**/*.json"]
    message: Manifest must declare version and kind
    schema-json:
      type: object
      required: ["version", "kind"]
      additionalProperties: false
      properties:
        version:
          type: integer
        kind:
          type: string
          enum: ["service", "worker"]
    fixtures:
      fail:
        - name: missing kind
          file: manifests/app.json
          content: '{"version": 1}'
      pass:
        - name: valid manifest
          file: manifests/app.json
          content: '{"version": 1, "kind": "service"}'
`
	b, err := ResolveAnyFlowDeclaration([]byte(allFourYAML), 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration(allFourYAML): %v", err)
	}
	if len(b.Resolved.CustomChecks) != 4 {
		t.Fatalf("got %d custom checks, want 4", len(b.Resolved.CustomChecks))
	}

	// Verify stage insert-after after s5 is rejected
	badStageYAML := strings.Replace(allFourYAML, "key: all-four-engines", "key: bad-stage\nstages:\n  custom:\n    - id: custom:bad\n      insert-after: s5", 1)
	if _, err := ResolveAnyFlowDeclaration([]byte(badStageYAML), 1, nil); err == nil {
		t.Fatal("expected custom stage insert-after: s5 to be rejected")
	}

	// Verify cyclic YAML-alias jsonschema is rejected without stack overflow
	cyclicSchemaYAML := `schema: grain.dev/qa-flow/v3
key: cyclic-schema
extends: golden-v2@1
custom-checks:
  - id: custom:cyclic
    label: Cyclic schema
    kind: jsonschema
    severity: blocking
    paths: ["manifests/**/*.json"]
    message: Cyclic
    schema-json: &self
      type: object
      properties:
        next: *self
    fixtures:
      fail:
        - name: bad
          file: manifests/a.json
          content: "123"
      pass:
        - name: ok
          file: manifests/a.json
          content: "{}"
`
	if _, err := ResolveAnyFlowDeclaration([]byte(cyclicSchemaYAML), 1, nil); err == nil {
		t.Fatal("expected cyclic YAML alias jsonschema to be rejected by depth limit")
	}
}
