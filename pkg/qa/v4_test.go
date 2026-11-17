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
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBuiltInEngineeringWorkflowV4AndPreservedV1ToV3Hashes(t *testing.T) {
	// Verify existing built-in hashes remain byte-for-byte unchanged.
	wantBuiltInHashes := map[string]string{
		"golden-v1-minimal@1":     "builtin:golden-v1-minimal@1#4",
		"golden-v2@1":             "builtin:golden-v2@1#4",
		"golden-v2-skill-only@1":  "builtin:golden-v2-skill-only@1#4",
		"golden-v2-checks-only@1": "builtin:golden-v2-checks-only@1#4",
		"golden-v3@1":             "builtin:golden-v3@1#4",
	}
	for ref, wantHash := range wantBuiltInHashes {
		b, ok := LookupBuiltInOrRegisteredVersion(ref)
		if !ok {
			t.Fatalf("LookupBuiltInOrRegisteredVersion(%q) not found", ref)
		}
		if b.Hash != wantHash {
			t.Errorf("%s Hash = %q, want %q", ref, b.Hash, wantHash)
		}
		if b.Resolved.HasRule(RulePRDescriptionContract) {
			t.Errorf("%s should have %s disabled by default", ref, RulePRDescriptionContract)
		}
		r, found := b.Resolved.Rule(RulePRDescriptionContract)
		if !found || r.Enabled || r.Severity != "warn" {
			t.Errorf("%s Rule(%s) = (%+v, %v), want Enabled=false Severity=warn", ref, RulePRDescriptionContract, r, found)
		}
		if strings.Contains(b.SpecJSON, RulePRDescriptionContract) {
			t.Errorf("%s SpecJSON must not change with %s when unmentioned: %s", ref, RulePRDescriptionContract, b.SpecJSON)
		}
	}

	// Verify defaultS3Rules includes RulePRDescriptionContract with Enabled=false, Severity="warn".
	s3Rules := defaultS3Rules()
	foundInDefaultS3 := false
	for _, r := range s3Rules {
		if r.ID == RulePRDescriptionContract {
			foundInDefaultS3 = true
			if r.Enabled || r.Severity != "warn" {
				t.Errorf("defaultS3Rules %s = %+v, want Enabled=false Severity=warn", RulePRDescriptionContract, r)
			}
		}
	}
	if !foundInDefaultS3 {
		t.Fatalf("defaultS3Rules missing %s", RulePRDescriptionContract)
	}

	// Verify engineering-workflow-v4@1 is registered and fully populated.
	v4, ok := LookupBuiltInOrRegisteredVersion("engineering-workflow-v4@1")
	if !ok {
		t.Fatal("LookupBuiltInOrRegisteredVersion(engineering-workflow-v4@1) not found")
	}
	if v4.SchemaVer != FlowSchemaV4 || v4.DeclarationV4 == nil {
		t.Fatalf("unexpected v4 SchemaVer=%q DeclarationV4=%v", v4.SchemaVer, v4.DeclarationV4)
	}
	if !v4.Resolved.HasRule(RulePRDescriptionContract) {
		t.Errorf("engineering-workflow-v4@1 must enable %s", RulePRDescriptionContract)
	}
	prRule, _ := v4.Resolved.Rule(RulePRDescriptionContract)
	if prRule.Severity != "warn" {
		t.Errorf("engineering-workflow-v4@1 %s severity = %q, want warn", RulePRDescriptionContract, prRule.Severity)
	}
	if !v4.Resolved.Decompose.Enabled || v4.Resolved.Decompose.Mode != "slice_plan" {
		t.Errorf("unexpected Decompose on engineering-workflow-v4@1: %+v", v4.Resolved.Decompose)
	}
	if !v4.Resolved.Context.Enabled || !v4.Resolved.Context.IncludeCallers || !v4.Resolved.Context.IncludeMemory {
		t.Errorf("unexpected Context on engineering-workflow-v4@1: %+v", v4.Resolved.Context)
	}
	if len(v4.Resolved.ReadabilityPacks) != 4 {
		t.Fatalf("len(ReadabilityPacks) = %d, want 4", len(v4.Resolved.ReadabilityPacks))
	}
	if !v4.Resolved.PRContract.Enabled || v4.Resolved.PRContract.MaxSubjectChars != 72 || !v4.Resolved.PRContract.AuditLintSuppressions {
		t.Errorf("unexpected PRContract on engineering-workflow-v4@1: %+v", v4.Resolved.PRContract)
	}
	if !slices.Equal(v4.Resolved.PRContract.RequiredSections, []string{"Summary", "Verification"}) {
		t.Errorf("RequiredSections = %v, want [Summary Verification]", v4.Resolved.PRContract.RequiredSections)
	}
	if v4.Resolved.S5MergeTriage.Mode != "shadow" {
		t.Errorf("S5MergeTriage.Mode = %q, want shadow", v4.Resolved.S5MergeTriage.Mode)
	}
}

func TestReadabilityPacksAndActiveCodingGuidance(t *testing.T) {
	for _, id := range []string{"go", "ts-angular", "python", "cpp"} {
		txt := BuiltInReadabilityPackText(id)
		if len(txt) < 100 {
			t.Errorf("BuiltInReadabilityPackText(%q) too short: %q", id, txt)
		}
	}
	if got := BuiltInReadabilityPackText("unknown-lang"); got != "" {
		t.Errorf("BuiltInReadabilityPackText(unknown-lang) = %q, want empty", got)
	}

	v4, ok := LookupBuiltInOrRegisteredVersion("engineering-workflow-v4@1")
	if !ok {
		t.Fatal("engineering-workflow-v4@1 not found")
	}

	// Empty targetPaths includes default go and ts-angular auto packs, skill steps, and PR contract.
	defaultGuidance := v4.Resolved.ActiveCodingGuidance(nil)
	if !strings.Contains(defaultGuidance, "pack:go") || !strings.Contains(defaultGuidance, "pack:ts-angular") {
		t.Errorf("empty targetPaths should include go and ts-angular packs: %s", defaultGuidance)
	}
	if !strings.Contains(defaultGuidance, "PR Contract") || !strings.Contains(defaultGuidance, "Summary, Verification") {
		t.Errorf("guidance should include PR Contract: %s", defaultGuidance)
	}

	// Python target path activates python pack and omits go and cpp.
	pyGuidance := v4.Resolved.ActiveCodingGuidance([]string{"scripts/train.py"})
	if !strings.Contains(pyGuidance, "pack:python") {
		t.Errorf("expected python pack for .py target: %s", pyGuidance)
	}
	if strings.Contains(pyGuidance, "pack:cpp") {
		t.Errorf("did not expect cpp pack for .py target: %s", pyGuidance)
	}

	// C++ target path activates cpp pack.
	cppGuidance := v4.Resolved.ActiveCodingGuidance([]string{"runtime/engine.cc", "runtime/engine.h"})
	if !strings.Contains(cppGuidance, "pack:cpp") {
		t.Errorf("expected cpp pack for .cc/.h target: %s", cppGuidance)
	}

	// Verify 8192 byte cap on huge custom packs.
	hugeSpec := v4.Resolved
	hugeSpec.ReadabilityPacks = append(slices.Clone(hugeSpec.ReadabilityPacks), ReadabilityPackDecl{
		ID:         "custom-huge",
		Enabled:    true,
		Activation: "always",
		CustomText: strings.Repeat("Rule line for cap testing. ", 400),
	})
	capped := hugeSpec.ActiveCodingGuidance([]string{"pkg/qa/v4.go"})
	if len(capped) > MaxActiveCodingGuidanceBytes {
		t.Errorf("len(ActiveCodingGuidance) = %d, exceeds cap %d", len(capped), MaxActiveCodingGuidanceBytes)
	}
}

func TestBuildWorkflowStagesViewAcrossV1ToV4(t *testing.T) {
	wantStageIDs := []string{
		"s0-decompose",
		"s1-context",
		"s2-plan",
		"s2-coding",
		"s3-checks",
		"s3-pr",
		"s4-verify",
		"s5-triage",
	}
	wantLabels := []string{
		"1 · Goal & Subtask Breakdown",
		"2 · Context & Blast Radius",
		"3 · Plan & Fresh Critique",
		"4 · Coding & Readability Rules",
		"5 · Pre-PR Deterministic Gates",
		"6 · PR Authoring & Self-Review",
		"7 · Adversarial Verification",
		"8 · Scorecard & Merge Triage",
	}

	for _, ref := range []string{"golden-v1-minimal@1", "golden-v2@1", "golden-v3@1", "engineering-workflow-v4@1"} {
		b, ok := LookupBuiltInOrRegisteredVersion(ref)
		if !ok {
			t.Fatalf("%s not found", ref)
		}
		stages := BuildWorkflowStagesView(b)
		if len(stages) != 8 {
			t.Fatalf("%s len(stages) = %d, want 8", ref, len(stages))
		}
		for i, stg := range stages {
			if stg.Index != i+1 || stg.ID != wantStageIDs[i] || stg.Label != wantLabels[i] {
				t.Errorf("%s stage[%d] = (%d, %q, %q), want (%d, %q, %q)",
					ref, i, stg.Index, stg.ID, stg.Label, i+1, wantStageIDs[i], wantLabels[i])
			}
			if len(stg.Steps) == 0 {
				t.Errorf("%s stage %s has 0 steps", ref, stg.ID)
			}
			if ref == "engineering-workflow-v4@1" && !stg.Enabled {
				t.Errorf("engineering-workflow-v4@1 stage %s should be enabled", stg.ID)
			}
		}
	}
}

func TestFlowDeclarationV4InheritanceJSONDecodeAndValidation(t *testing.T) {
	// 1. Child V4 flow in JSON format extending engineering-workflow-v4@1.
	rawJSON := map[string]any{
		"schema":  FlowSchemaV4,
		"key":     "v4-child-json",
		"extends": "engineering-workflow-v4@1",
		"label":   "V4 Child via JSON",
		"decompose": map[string]any{
			"enabled":         true,
			"mode":            "propose_dag",
			"maxSubtasks":     6,
			"targetDiffLines": 300,
		},
		"prContract": map[string]any{
			"enabled":               true,
			"mode":                  "blocking",
			"maxSubjectChars":       68,
			"requiredSections":      []string{"Summary", "Verification", "Risks"},
			"auditLintSuppressions": true,
		},
		"stages": map[string]any{
			"s0-intent": map[string]any{
				"enabled":          true,
				"on-clarification": "ask_human",
			},
			"s5-merge-triage": map[string]any{
				"mode": "enforced",
			},
		},
	}
	jsonBytes, err := json.Marshal(rawJSON)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	child, err := ResolveAnyFlowDeclaration(jsonBytes, 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration(jsonBytes): %v", err)
	}
	if child.SchemaVer != FlowSchemaV4 || child.DeclarationV4 == nil {
		t.Fatalf("expected FlowSchemaV4 and non-nil DeclarationV4, got %q %v", child.SchemaVer, child.DeclarationV4)
	}
	if child.Resolved.Decompose.Mode != "propose_dag" || child.Resolved.Decompose.MaxSubtasks != 6 {
		t.Errorf("unexpected Decompose on child: %+v", child.Resolved.Decompose)
	}
	if child.Resolved.S0IntentConfig.OnClarification != "ask_human" {
		t.Errorf("OnClarification = %q, want ask_human", child.Resolved.S0IntentConfig.OnClarification)
	}
	if len(child.Resolved.ReadabilityPacks) != 4 {
		t.Errorf("expected child to inherit 4 ReadabilityPacks, got %d", len(child.Resolved.ReadabilityPacks))
	}
	if len(child.Resolved.CustomChecks) != 2 {
		t.Errorf("expected child to inherit 2 CustomChecks from golden-v3@1, got %d", len(child.Resolved.CustomChecks))
	}

	v2, _ := LookupBuiltInOrRegisteredVersion("golden-v2@1")
	diff := ComputeDeclarationDiff(&v2, &child)
	if !slices.Contains(diff.AddedStages, "s0-decompose") || !slices.Contains(diff.AddedStages, "s1-context") || !slices.Contains(diff.AddedStages, "s3-pr") {
		t.Errorf("expected s0-decompose, s1-context, s3-pr in AddedStages, got %+v", diff.AddedStages)
	}
	if !slices.Contains(diff.ChangedChecks, RulePRDescriptionContract) {
		t.Errorf("expected %s in ChangedChecks, got %+v", RulePRDescriptionContract, diff.ChangedChecks)
	}

	// 2. Reject invalid configurations and unknown keys.
	badCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unknown field in v4 yaml",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\nnot-a-field: true\n",
			want: "not-a-field",
		},
		{
			name: "invalid on-clarification",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\nstages:\n  s0-intent:\n    enabled: true\n    on-clarification: invalid_mode\n",
			want: "on-clarification",
		},
		{
			name: "invalid decompose mode",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\ndecompose:\n  enabled: true\n  mode: invalid_decomp\n",
			want: "decompose.mode",
		},
		{
			name: "glob pack without globs",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\nreadability-packs:\n  - id: go\n    enabled: true\n    activation: glob\n",
			want: "requires non-empty globs",
		},
		{
			name: "invalid skill step stage",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\nskill-steps:\n  - id: s1\n    stage: s99\n    enabled: true\n    text: hi\n",
			want: "stage",
		},
		{
			name: "invalid pr-contract mode",
			raw:  "schema: grain.dev/qa-flow/v4\nkey: bad\npr-contract:\n  enabled: true\n  mode: invalid_pr_mode\n",
			want: "pr-contract.mode",
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveAnyFlowDeclaration([]byte(tc.raw), 1, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ResolveAnyFlowDeclaration(%s) err = %v, want substring %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestResolveStudioExportedV4JSONWithDiffFixtures(t *testing.T) {
	studioJSON := `{
		"version": 1,
		"key": "studio-v4-custom",
		"extends": "golden-v4-composable",
		"label": "Studio V4 Custom",
		"decompose": {
			"enabled": true,
			"mode": "slice_plan",
			"maxSubtasks": 4,
			"targetDiffLines": 250
		},
		"customChecks": [
			{
				"id": "no-console-log",
				"label": "No console.log",
				"kind": "pattern",
				"severity": "blocking",
				"message": "Do not commit console.log",
				"paths": ["src/**/*.ts"],
				"forbidAdded": "console\\.log\\(",
				"fixtures": {
					"fail": [{"diff": "+console.log(x)"}],
					"pass": [{"diff": "+logger.info(x)"}]
				}
			}
		]
	}`
	bundle, err := ResolveAnyFlowDeclaration([]byte(studioJSON), 1, nil)
	if err != nil {
		t.Fatalf("ResolveAnyFlowDeclaration failed on Studio V4 JSON: %v", err)
	}
	if bundle.Key != "studio-v4-custom" {
		t.Errorf("got key %q, want studio-v4-custom", bundle.Key)
	}
	if bundle.DeclarationV4 == nil || len(bundle.DeclarationV4.CustomChecks) != 1 {
		t.Fatalf("expected 1 custom check on DeclarationV4, got %+v", bundle.DeclarationV4)
	}
	if got := bundle.DeclarationV4.CustomChecks[0].Paths; len(got) != 1 || got[0] != "src/**/*.ts" {
		t.Errorf("got custom check paths %v, want [src/**/*.ts]", got)
	}
}
