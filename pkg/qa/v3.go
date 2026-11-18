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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const FlowSchemaV3 = "grain.dev/qa-flow/v3"

const goldenV3YAML = `schema: grain.dev/qa-flow/v3
key: golden-v3
extends: golden-v2@1
label: golden v3 (plan critic + custom checks + blast triage)
summary: >
  Adds Stage 0/2 complexity-gated intent & plan critique, fixture-verified custom checks,
  0..100 blast-radius scoring, and ComposeTightenOnly S5 human-review triage.

skills:
  coding:
    mode: inherit                       # inherit | append | replace (cap <= 4096 B)
  intent:
    mode: replace
    max-bytes: 1200
    text: |
      Extract 2 to 6 testable WHEN ... SHALL acceptance criteria and explicit non-goals
      strictly from the task title and body. If a load-bearing requirement is ambiguous,
      return status: needs_clarification with one concrete question.
  plan-critic:
    mode: replace
    max-bytes: 1600
    text: |
      Inspect the proposed file plan and sibling importers (by Go import path / TS module).
      Flag missing caller updates, missing schema/route registrations, or scope creep
      beyond the task specification. Do not invent unrequested architecture.

stages:
  s0-intent:
    enabled: true
    complexities: [medium, high]        # skips low-complexity tasks & tasks with Design-Item:
    max-seconds: 30
    max-tokens: 15000
  s1-risk:
    overrides:                          # tighten-only minTier overrides
      - paths: ["pkg/orchestrator/**", "pkg/model/migrations.go"]
        min-tier: T3
        reason: "Core state machine and schema migrations"
  s2-plan-critic:
    enabled: true
    complexities: [high]
    tiers: [T2, T3]
    max-seconds: 45
    max-tokens: 25000
    max-bounces: 1
  custom:
    - id: "custom:s3b-companion-checks"
      label: "Schema & route companion gate"
      insert-after: s3                  # allowed: s2 | s3 | s4 (never after s5)
      kind: deterministic_batch         # deterministic_batch | isolated_sandbox_check | isolated_critic | human_gate
      when-paths: ["pkg/model/**", "pkg/ui/**"]
      min-tier: T1
      timeout-seconds: 30
      check-ids: ["custom:schema-needs-migration", "custom:no-fmt-println-pkg"]
  s5-merge-triage:
    mode: shadow                        # off | shadow | enforced
    auto-merge-tiers: [T0, T1]          # clean green T0/T1 auto-merge when repo allows
    require-human-tiers: [T2, T3]       # T2/T3 withdraw auto-merge & route to human Submit

checks:
  golden:test-integrity-guard: {}       # locked floor
  golden:strict-config-decoder: {}      # locked floor
  golden:sibling-export-hazard: {severity: warn, params: {resolve: import-path}}
  golden:ui-budget-and-spec-floor: {enabled: true}
  golden:exported-surface-delta: {severity: warn}
  golden:test-hygiene: {severity: warn}
  golden:mutation-hardening: {severity: warn, params: {mutants: 25, max-seconds: 60}}
  golden:boundary-fuzz: {severity: warn, params: {max-seconds: 15}}

custom-checks:
  - id: "custom:schema-needs-migration"
    label: "Schema edit requires migration"
    kind: companion
    severity: blocking                  # blocking | warn | shadow
    when-changed: ["pkg/model/schema.go"]
    require-changed: ["pkg/model/migrations.go"]
    message: "Changing pkg/model/schema.go requires a matching migration in pkg/model/migrations.go."
    fixtures:
      fail:
        - name: "schema.go changed alone"
          changed-files: ["pkg/model/schema.go"]
      pass:
        - name: "schema.go and migrations.go changed together"
          changed-files: ["pkg/model/schema.go", "pkg/model/migrations.go"]
  - id: "custom:no-fmt-println-pkg"
    label: "No debug fmt.Println in pkg/"
    kind: pattern
    severity: warn
    paths: ["pkg/**/*.go"]
    exclude: ["**/*_test.go"]
    forbid-added: '\bfmt\.Println\('
    message: "Use structured logging instead of fmt.Println in production packages."
    fixtures:
      fail:
        - name: "added fmt.Println in handler"
          file: "pkg/ui/server.go"
          added-lines: ["\tfmt.Println(\"debug\", req.URL)"]
      pass:
        - name: "fmt.Println in unit test ignored"
          file: "pkg/ui/server_test.go"
          added-lines: ["\tfmt.Println(\"test output\")"]

verifier:
  enabled: true
  tiers: [T1, T2, T3]
  max-repair-rounds: 1
  max-seconds: 60
  max-tokens: 50000
  checklist: escape-classes-v1
`

type DeclarationDiff struct {
	AddedStages         []string `json:"addedStages,omitempty"`
	RemovedStages       []string `json:"removedStages,omitempty"`
	ChangedSkills       []string `json:"changedSkills,omitempty"`
	AddedCustomChecks   []string `json:"addedCustomChecks,omitempty"`
	RemovedCustomChecks []string `json:"removedCustomChecks,omitempty"`
	ChangedChecks       []string `json:"changedChecks,omitempty"`
	ChangedVerifier     bool     `json:"changedVerifier,omitempty"`
	ChangedTriage       bool     `json:"changedTriage,omitempty"`
}

func ComputeDeclarationDiff(ref, cand *FlowVersionBundle) DeclarationDiff {
	var diff DeclarationDiff
	if ref == nil {
		if cand != nil {
			// Zero-QA control to some flow.
			diff.AddedStages = []string{"s3"} // implicitly added as base
			if cand.Resolved.EnableS4 {
				diff.AddedStages = append(diff.AddedStages, "s4")
			}
			if cand.Resolved.S0Intent {
				diff.AddedStages = append(diff.AddedStages, "s0-intent")
			}
			if cand.Resolved.Decompose.Enabled && cand.Resolved.Decompose.Mode != "off" {
				diff.AddedStages = append(diff.AddedStages, "s0-decompose")
			}
			if cand.Resolved.Context.Enabled {
				diff.AddedStages = append(diff.AddedStages, "s1-context")
			}
			if cand.Resolved.S2PlanCritic {
				diff.AddedStages = append(diff.AddedStages, "s2-plan-critic")
			}
			if cand.Resolved.PRContract.Enabled && cand.Resolved.PRContract.Mode != "off" {
				diff.AddedStages = append(diff.AddedStages, "s3-pr")
			}
			for _, stg := range cand.Resolved.CustomStages {
				diff.AddedStages = append(diff.AddedStages, stg.ID)
			}
			for _, chk := range cand.Resolved.CustomChecks {
				diff.AddedCustomChecks = append(diff.AddedCustomChecks, chk.ID)
			}
			for _, pack := range cand.Resolved.ReadabilityPacks {
				if pack.Enabled {
					diff.ChangedSkills = append(diff.ChangedSkills, "pack:"+pack.ID)
				}
			}
			for _, step := range cand.Resolved.SkillSteps {
				if step.Enabled {
					diff.ChangedSkills = append(diff.ChangedSkills, "step:"+step.ID)
				}
			}
		}
		return diff
	}
	if cand == nil {
		return diff
	}

	// Compare skills
	if ref.Resolved.SkillText != cand.Resolved.SkillText {
		diff.ChangedSkills = append(diff.ChangedSkills, "coding")
	}
	if ref.Resolved.IntentSkillText != cand.Resolved.IntentSkillText {
		diff.ChangedSkills = append(diff.ChangedSkills, "intent")
	}
	if ref.Resolved.PlanCriticSkillText != cand.Resolved.PlanCriticSkillText {
		diff.ChangedSkills = append(diff.ChangedSkills, "plan-critic")
	}
	if ref.Resolved.VerifierSkillText != cand.Resolved.VerifierSkillText {
		diff.ChangedSkills = append(diff.ChangedSkills, "verifier")
	}
	if ref.Resolved.SecuritySkillText != cand.Resolved.SecuritySkillText {
		diff.ChangedSkills = append(diff.ChangedSkills, "security")
	}

	// Compare Stages
	if !ref.Resolved.S0Intent && cand.Resolved.S0Intent {
		diff.AddedStages = append(diff.AddedStages, "s0-intent")
	} else if ref.Resolved.S0Intent && !cand.Resolved.S0Intent {
		diff.RemovedStages = append(diff.RemovedStages, "s0-intent")
	}
	refDecomp := ref.Resolved.Decompose.Enabled && ref.Resolved.Decompose.Mode != "off"
	candDecomp := cand.Resolved.Decompose.Enabled && cand.Resolved.Decompose.Mode != "off"
	if !refDecomp && candDecomp {
		diff.AddedStages = append(diff.AddedStages, "s0-decompose")
	} else if refDecomp && !candDecomp {
		diff.RemovedStages = append(diff.RemovedStages, "s0-decompose")
	}
	if !ref.Resolved.Context.Enabled && cand.Resolved.Context.Enabled {
		diff.AddedStages = append(diff.AddedStages, "s1-context")
	} else if ref.Resolved.Context.Enabled && !cand.Resolved.Context.Enabled {
		diff.RemovedStages = append(diff.RemovedStages, "s1-context")
	}
	if !ref.Resolved.S2PlanCritic && cand.Resolved.S2PlanCritic {
		diff.AddedStages = append(diff.AddedStages, "s2-plan-critic")
	} else if ref.Resolved.S2PlanCritic && !cand.Resolved.S2PlanCritic {
		diff.RemovedStages = append(diff.RemovedStages, "s2-plan-critic")
	}
	refPR := ref.Resolved.PRContract.Enabled && ref.Resolved.PRContract.Mode != "off"
	candPR := cand.Resolved.PRContract.Enabled && cand.Resolved.PRContract.Mode != "off"
	if !refPR && candPR {
		diff.AddedStages = append(diff.AddedStages, "s3-pr")
	} else if refPR && !candPR {
		diff.RemovedStages = append(diff.RemovedStages, "s3-pr")
	}
	diff.ChangedSkills = append(diff.ChangedSkills, diffV4PacksAndSteps(ref.Resolved, cand.Resolved)...)

	refCustomStages := make(map[string]bool)
	candCustomStages := make(map[string]bool)
	for _, stg := range ref.Resolved.CustomStages {
		refCustomStages[stg.ID] = true
	}
	for _, stg := range cand.Resolved.CustomStages {
		candCustomStages[stg.ID] = true
	}
	for id := range candCustomStages {
		if !refCustomStages[id] {
			diff.AddedStages = append(diff.AddedStages, id)
		}
	}
	for id := range refCustomStages {
		if !candCustomStages[id] {
			diff.RemovedStages = append(diff.RemovedStages, id)
		}
	}

	// Compare Checks
	refChecks := make(map[string]RuleSpec)
	candChecks := make(map[string]RuleSpec)
	for _, chk := range ref.Resolved.Rules {
		if chk.Enabled {
			refChecks[chk.ID] = chk
		}
	}
	for _, chk := range cand.Resolved.Rules {
		if chk.Enabled {
			candChecks[chk.ID] = chk
		}
	}
	for id, chk := range candChecks {
		refChk, ok := refChecks[id]
		if !ok || refChk.Severity != chk.Severity || fmt.Sprintf("%v", refChk.Params) != fmt.Sprintf("%v", chk.Params) {
			diff.ChangedChecks = append(diff.ChangedChecks, id)
		}
	}

	// Custom Checks
	refCustomChecks := make(map[string]CustomCheckDecl)
	candCustomChecks := make(map[string]CustomCheckDecl)
	for _, chk := range ref.Resolved.CustomChecks {
		refCustomChecks[chk.ID] = chk
	}
	for _, chk := range cand.Resolved.CustomChecks {
		candCustomChecks[chk.ID] = chk
	}
	for id := range candCustomChecks {
		if _, ok := refCustomChecks[id]; !ok {
			diff.AddedCustomChecks = append(diff.AddedCustomChecks, id)
		}
	}
	for id := range refCustomChecks {
		if _, ok := candCustomChecks[id]; !ok {
			diff.RemovedCustomChecks = append(diff.RemovedCustomChecks, id)
		}
	}

	if ref.Resolved.EnableS4 != cand.Resolved.EnableS4 ||
		fmt.Sprintf("%v", ref.Resolved.VerifierTiers) != fmt.Sprintf("%v", cand.Resolved.VerifierTiers) ||
		ref.Resolved.MaxS4RepairRounds != cand.Resolved.MaxS4RepairRounds ||
		ref.Resolved.VerifierChecklist != cand.Resolved.VerifierChecklist {
		diff.ChangedVerifier = true
	}

	if ref.Resolved.S5MergeTriage.Mode != cand.Resolved.S5MergeTriage.Mode {
		diff.ChangedTriage = true
	}

	return diff
}

func ParseFlowDeclaration(raw []byte) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("qa: empty flow declaration")
	}
	trimmed = normalizeJSONFlowDeclarationBytes(trimmed)

	var peek map[string]any
	if err := yaml.Unmarshal(trimmed, &peek); err != nil {
		return nil, fmt.Errorf("qa: checking flow schema: %w", err)
	}
	schemaStr, _ := peek["schema"].(string)

	if schemaStr == FlowSchemaV4 {
		return ParseFlowDeclarationV4(trimmed)
	}

	if schemaStr == FlowSchemaV3 {
		var decl FlowDeclarationV3
		dec := yaml.NewDecoder(bytes.NewReader(trimmed))
		dec.KnownFields(true)
		if err := dec.Decode(&decl); err != nil {
			return nil, fmt.Errorf("qa: parsing flow declaration v3: %w", err)
		}
		if strings.TrimSpace(decl.Key) == "" {
			return nil, fmt.Errorf("qa: flow key is required")
		}
		return &decl, nil
	}

	if schemaStr == "" || schemaStr == FlowSchemaV2 {
		var decl FlowDeclarationV2
		dec := yaml.NewDecoder(bytes.NewReader(trimmed))
		dec.KnownFields(true)
		if err := dec.Decode(&decl); err != nil {
			return nil, fmt.Errorf("qa: parsing flow declaration v2: %w", err)
		}
		decl.Schema = FlowSchemaV2
		if strings.TrimSpace(decl.Key) == "" {
			return nil, fmt.Errorf("qa: flow key is required")
		}
		return &decl, nil
	}

	return nil, fmt.Errorf("qa: unsupported flow schema %q", schemaStr)
}

func ResolveFlowDeclarationV3(decl FlowDeclarationV3, version int, sourceYAML string, explicitParentHash string, builtIn bool, lookup ParentLookupFunc) (FlowVersionBundle, error) {
	effectiveLookup := func(ref string) (FlowVersionBundle, bool) {
		if lookup != nil {
			if b, ok := lookup(ref); ok {
				return b, true
			}
		}
		return LookupBuiltInOrRegisteredVersion(ref)
	}

	// Inherit V3 stages, skills, risk overrides, and custom checks from V3 parent when extending one.
	if ext := strings.TrimSpace(decl.Extends); ext != "" {
		if parent, ok := effectiveLookup(ext); ok && parent.DeclarationV3 != nil {
			pv3 := parent.DeclarationV3
			if decl.Skills.Intent.Text == "" && decl.Skills.Intent.Mode != "replace" {
				decl.Skills.Intent = pv3.Skills.Intent
			}
			if decl.Skills.PlanCritic.Text == "" && decl.Skills.PlanCritic.Mode != "replace" {
				decl.Skills.PlanCritic = pv3.Skills.PlanCritic
			}
			if decl.Skills.Verifier.Text == "" && decl.Skills.Verifier.Mode != "replace" {
				decl.Skills.Verifier = pv3.Skills.Verifier
			}
			if decl.Skills.Security.Text == "" && decl.Skills.Security.Mode != "replace" {
				decl.Skills.Security = pv3.Skills.Security
			}
			if !decl.Stages.S0Intent.Enabled && len(decl.Stages.S0Intent.Complexities) == 0 && pv3.Stages.S0Intent.Enabled {
				decl.Stages.S0Intent = pv3.Stages.S0Intent
			} else if decl.Stages.S0Intent.OnClarification == "" && pv3.Stages.S0Intent.OnClarification != "" {
				decl.Stages.S0Intent.OnClarification = pv3.Stages.S0Intent.OnClarification
			}
			if len(decl.Stages.S1Risk.Overrides) == 0 && len(pv3.Stages.S1Risk.Overrides) > 0 {
				decl.Stages.S1Risk.Overrides = pv3.Stages.S1Risk.Overrides
			}
			if !decl.Stages.S2PlanCritic.Enabled && len(decl.Stages.S2PlanCritic.Complexities) == 0 && pv3.Stages.S2PlanCritic.Enabled {
				decl.Stages.S2PlanCritic = pv3.Stages.S2PlanCritic
			}
			if len(decl.Stages.Custom) == 0 && len(pv3.Stages.Custom) > 0 {
				decl.Stages.Custom = pv3.Stages.Custom
			}
			if decl.Stages.S5MergeTriage.Mode == "" {
				decl.Stages.S5MergeTriage = pv3.Stages.S5MergeTriage
			} else {
				if len(decl.Stages.S5MergeTriage.AutoMergeTiers) == 0 {
					decl.Stages.S5MergeTriage.AutoMergeTiers = pv3.Stages.S5MergeTriage.AutoMergeTiers
				}
				if len(decl.Stages.S5MergeTriage.RequireHumanTiers) == 0 {
					decl.Stages.S5MergeTriage.RequireHumanTiers = pv3.Stages.S5MergeTriage.RequireHumanTiers
				}
			}
			if len(decl.CustomChecks) == 0 && len(pv3.CustomChecks) > 0 {
				decl.CustomChecks = pv3.CustomChecks
			}
		}
	}

	// Custom Checks Validation (mandatory >=1 fail and >=1 pass inline fixtures).
	for _, cc := range decl.CustomChecks {
		if err := ValidateCustomCheck(cc); err != nil {
			return FlowVersionBundle{}, fmt.Errorf("qa: invalid custom check %q: %w", cc.ID, err)
		}
	}

	// Validate stage skill byte caps.
	for name, sk := range map[string]SkillDecl{
		"intent":      decl.Skills.Intent,
		"plan-critic": decl.Skills.PlanCritic,
		"verifier":    decl.Skills.Verifier,
		"security":    decl.Skills.Security,
	} {
		capB := sk.MaxBytes
		if capB <= 0 {
			capB = MaxSkillBytesCap
		}
		if capB > MaxSkillBytesCap {
			return FlowVersionBundle{}, fmt.Errorf("qa: skills.%s.max-bytes %d exceeds maximum %d", name, capB, MaxSkillBytesCap)
		}
		if len(normalizeSkillText(sk.Text)) > capB {
			return FlowVersionBundle{}, fmt.Errorf("qa: skills.%s exceeds max-bytes %d", name, capB)
		}
	}

	// Validate S0 intent on-clarification if specified.
	if oc := strings.TrimSpace(decl.Stages.S0Intent.OnClarification); oc != "" && oc != "warn" && oc != "ask_human" {
		return FlowVersionBundle{}, fmt.Errorf("qa: invalid s0-intent on-clarification %q (want warn or ask_human)", oc)
	}

	// Validate S5 triage mode if specified.
	if m := strings.TrimSpace(decl.Stages.S5MergeTriage.Mode); m != "" && m != "off" && m != "shadow" && m != "enforced" {
		return FlowVersionBundle{}, fmt.Errorf("qa: invalid s5-merge-triage mode %q (want off, shadow, or enforced)", m)
	}

	v2 := FlowDeclarationV2{
		Schema:   FlowSchemaV2,
		Key:      decl.Key,
		Extends:  decl.Extends,
		Label:    decl.Label,
		Summary:  decl.Summary,
		Skill:    decl.Skills.Coding,
		Checks:   decl.Checks,
		Verifier: decl.Verifier,
	}

	b, err := compileBundle(v2, version, sourceYAML, explicitParentHash, builtIn, effectiveLookup)
	if err != nil {
		return FlowVersionBundle{}, err
	}

	// Validate CustomStage insert-after and timeout bounds (10..120s).
	for i := range decl.Stages.Custom {
		stg := &decl.Stages.Custom[i]
		if stg.InsertAfter != "s2" && stg.InsertAfter != "s3" && stg.InsertAfter != "s4" {
			return FlowVersionBundle{}, fmt.Errorf("qa: custom stage %q insert-after %q is invalid (allowed: s2, s3, s4)", stg.ID, stg.InsertAfter)
		}
		tSec := stg.TimeoutSeconds
		if tSec == 0 {
			tSec = stg.Timeout
		}
		if tSec != 0 && (tSec < 10 || tSec > 120) {
			return FlowVersionBundle{}, fmt.Errorf("qa: custom stage %q timeout %d is out of bounds (10..120)", stg.ID, tSec)
		}
		if stg.TimeoutSeconds == 0 && tSec > 0 {
			stg.TimeoutSeconds = tSec
		}
	}

	matV3 := FlowDeclarationV3{
		Schema:       FlowSchemaV3,
		Key:          b.Key,
		Extends:      b.Declaration.Extends,
		Label:        b.Label,
		Summary:      b.Summary,
		Skills:       decl.Skills,
		Stages:       decl.Stages,
		Checks:       b.Declaration.Checks,
		CustomChecks: decl.CustomChecks,
		Verifier:     b.Declaration.Verifier,
	}
	matV3.Skills.Coding = b.Declaration.Skill

	b.SchemaVer = FlowSchemaV3
	b.DeclarationV3 = &matV3

	b.Resolved.SchemaVer = FlowSchemaV3
	b.Resolved.IntentSkillText = normalizeSkillText(decl.Skills.Intent.Text)
	b.Resolved.PlanCriticSkillText = normalizeSkillText(decl.Skills.PlanCritic.Text)
	b.Resolved.VerifierSkillText = normalizeSkillText(decl.Skills.Verifier.Text)
	b.Resolved.SecuritySkillText = normalizeSkillText(decl.Skills.Security.Text)
	b.Resolved.S0Intent = decl.Stages.S0Intent.Enabled
	b.Resolved.S0IntentConfig = decl.Stages.S0Intent
	b.Resolved.S2PlanCritic = decl.Stages.S2PlanCritic.Enabled
	b.Resolved.S2PlanCriticConfig = decl.Stages.S2PlanCritic
	b.Resolved.RiskOverrides = decl.Stages.S1Risk.Overrides
	b.Resolved.CustomStages = decl.Stages.Custom
	b.Resolved.S5MergeTriage = decl.Stages.S5MergeTriage
	b.Resolved.CustomChecks = decl.CustomChecks

	// Compute canonical JSON and SHA-256 over the full materialized V3 declaration!
	specBytes, hash, err := CanonicalAnyJSONAndHash(matV3)
	if err != nil {
		return FlowVersionBundle{}, err
	}
	b.SpecJSON = string(specBytes)
	b.Hash = hash

	return b, nil
}

// CanonicalAnyJSONAndHash serializes any materialized declaration struct to compact sorted-key JSON and returns its SHA-256 hex digest.
func CanonicalAnyJSONAndHash(v any) ([]byte, string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, "", fmt.Errorf("qa: marshaling declaration: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, "", fmt.Errorf("qa: canonicalizing declaration: %w", err)
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, "", fmt.Errorf("qa: encoding canonical json: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func ResolveAnyFlowDeclaration(raw []byte, version int, lookup ParentLookupFunc) (FlowVersionBundle, error) {
	anyDecl, err := ParseFlowDeclaration(raw)
	if err != nil {
		return FlowVersionBundle{}, err
	}
	switch d := anyDecl.(type) {
	case *FlowDeclarationV4:
		return ResolveFlowDeclarationV4(*d, version, string(raw), "", false, lookup)
	case *FlowDeclarationV3:
		return ResolveFlowDeclarationV3(*d, version, string(raw), "", false, lookup)
	case *FlowDeclarationV2:
		return compileBundle(*d, version, string(raw), "", false, lookup)
	default:
		return FlowVersionBundle{}, fmt.Errorf("qa: unknown declaration type")
	}
}
