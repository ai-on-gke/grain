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
	"strings"
	"testing"
)

func TestBuiltInFlowVersionsIncludeGoldenV2AndAblations(t *testing.T) {
	versions := BuiltInFlowVersions()
	if len(versions) < 4 {
		t.Fatalf("len(BuiltInFlowVersions()) = %d, want >= 4", len(versions))
	}

	v2, ok := LookupBuiltInOrRegisteredVersion("golden-v2@1")
	if !ok {
		t.Fatal("LookupBuiltInOrRegisteredVersion(golden-v2@1) not found")
	}
	if !v2.BuiltIn {
		t.Error("golden-v2@1 should be marked BuiltIn")
	}
	if !v2.Resolved.EnableS4 || !v2.Resolved.RequiresS4ForTier(BlastT1) {
		t.Errorf("golden-v2@1 should run S4 on T1+, got tiers=%v", v2.Resolved.VerifierTiers)
	}
	if v2.Resolved.SkillBytes == 0 || v2.Resolved.SkillBytes > 1800 {
		t.Errorf("golden-v2@1 skillBytes = %d, want (0, 1800]", v2.Resolved.SkillBytes)
	}
	for _, checkID := range []string{
		RuleTestIntegrityGuard,
		RuleStrictConfigDecoder,
		RuleSiblingExportHazard,
		RuleUIBudgetAndSpecFloor,
		RuleExportedSurfaceDelta,
		RuleTestHygiene,
		RuleMutationHardening,
		RuleBoundaryFuzz,
	} {
		if !v2.Resolved.HasRule(checkID) {
			t.Errorf("golden-v2@1 expected check %s enabled", checkID)
		}
	}

	checksOnly, ok := LookupBuiltInOrRegisteredVersion("golden-v2-checks-only@1")
	if !ok {
		t.Fatal("golden-v2-checks-only@1 not found")
	}
	if checksOnly.Resolved.InjectSkill || checksOnly.Resolved.StopNudge {
		t.Errorf("checks-only ablation should disable skill and stopNudge: %+v", checksOnly.Resolved)
	}

	skillOnly, ok := LookupBuiltInOrRegisteredVersion("golden-v2-skill-only@1")
	if !ok {
		t.Fatal("golden-v2-skill-only@1 not found")
	}
	if skillOnly.Resolved.EnableS4 || skillOnly.Resolved.HasRule(RuleMutationHardening) {
		t.Errorf("skill-only ablation should disable S4 and non-floor checks: %+v", skillOnly.Resolved)
	}
	if !skillOnly.Resolved.HasRule(RuleTestIntegrityGuard) || !skillOnly.Resolved.HasRule(RuleStrictConfigDecoder) {
		t.Error("skill-only ablation must retain mandatory floors")
	}
}

func TestCanonicalFlowHashIsDeterministicAcrossFormattingAndCRLF(t *testing.T) {
	yamlA := "schema: grain.dev/qa-flow/v2\nkey: custom-a\nextends: golden-v2@1\nskill:\n  mode: replace\n  max-bytes: 1800\n  text: \"line one   \\nline two\\n\"\nchecks:\n  golden:test-hygiene: {severity: blocking}\n  golden:exported-surface-delta: {severity: warn}\n"
	yamlB := "schema: grain.dev/qa-flow/v2\r\nkey: custom-a\r\nextends: golden-v2@1\r\nchecks:\r\n  golden:exported-surface-delta: {severity: warn}\r\n  golden:test-hygiene: {severity: blocking}\r\nskill:\r\n  mode: replace\r\n  max-bytes: 1800\r\n  text: |\r\n    line one\r\n    line two\r\n"

	declA, err := ParseFlowDeclarationV2([]byte(yamlA))
	if err != nil {
		t.Fatalf("ParseFlowDeclarationV2(A): %v", err)
	}
	declB, err := ParseFlowDeclarationV2([]byte(yamlB))
	if err != nil {
		t.Fatalf("ParseFlowDeclarationV2(B): %v", err)
	}
	bunA, err := ResolveFlowDeclarationV2(declA, 1, yamlA, nil)
	if err != nil {
		t.Fatalf("Resolve(A): %v", err)
	}
	bunB, err := ResolveFlowDeclarationV2(declB, 1, yamlB, nil)
	if err != nil {
		t.Fatalf("Resolve(B): %v", err)
	}
	if bunA.Hash != bunB.Hash {
		t.Fatalf("canonical hash mismatch: %s vs %s\nA=%s\nB=%s", bunA.Hash, bunB.Hash, bunA.SpecJSON, bunB.SpecJSON)
	}
}

func TestResolveFlowDeclarationV2RejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "downgrade floor",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nchecks:\n  golden:test-integrity-guard: {severity: warn}\n",
			want: "cannot be downgraded",
		},
		{
			name: "disable floor",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nchecks:\n  golden:strict-config-decoder: {enabled: false}\n",
			want: "cannot be disabled",
		},
		{
			name: "staging off with blocking",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nstaging-isolation: false\n",
			want: "staging-isolation cannot be false",
		},
		{
			name: "skill over cap",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nskill:\n  mode: replace\n  max-bytes: 5000\n  text: hi\n",
			want: "exceeds maximum 4096",
		},
		{
			name: "unknown check",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nchecks:\n  golden:not-a-check: {enabled: true}\n",
			want: "unknown check id",
		},
		{
			name: "verifier seconds over cap",
			yaml: "schema: grain.dev/qa-flow/v2\nkey: bad\nextends: golden-v2@1\nverifier:\n  max-seconds: 900\n",
			want: "exceeds cap 600",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decl, err := ParseFlowDeclarationV2([]byte(tc.yaml))
			if err != nil {
				if strings.Contains(err.Error(), tc.want) {
					return
				}
				t.Fatalf("unexpected parse error: %v", err)
			}
			_, err = ResolveFlowDeclarationV2(decl, 1, tc.yaml, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ResolveFlowDeclarationV2 err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRegisterFlowPackageExposesCustomBuiltInPackage(t *testing.T) {
	raw := `schema: grain.dev/qa-flow/v2
key: enterprise-style-pack-test
extends: golden-v2@1
label: Enterprise Style Pack
skill:
  mode: append
  max-bytes: 2400
  text: "Prefer explicit error wrapping with actionable context."
`
	decl, err := ParseFlowDeclarationV2([]byte(raw))
	if err != nil {
		t.Fatalf("ParseFlowDeclarationV2: %v", err)
	}
	if err := RegisterFlowPackage(decl); err != nil {
		t.Fatalf("RegisterFlowPackage: %v", err)
	}
	t.Cleanup(func() {
		flowRegMu.Lock()
		registeredFlows = nil
		flowRegMu.Unlock()
	})

	got, ok := LookupFlow("enterprise-style-pack-test@1")
	if !ok {
		t.Fatal("LookupFlow(enterprise-style-pack-test@1) not found")
	}
	if !strings.Contains(got.SkillPrompt(), "actionable context") {
		t.Errorf("SkillPrompt missing appended text: %q", got.SkillPrompt())
	}
}
