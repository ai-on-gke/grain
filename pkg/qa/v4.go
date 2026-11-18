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
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FlowSchemaV4 identifies the v4 declarative 8-stage engineering workflow package format.
const FlowSchemaV4 = "grain.dev/qa-flow/v4"

// MaxActiveCodingGuidanceBytes caps the combined coding prompt, readability packs, skill steps, and PR contract guidance.
const MaxActiveCodingGuidanceBytes = 8192

// DecomposeDecl configures Stage 1 complexity-gated task decomposition in FlowDeclarationV4.
type DecomposeDecl struct {
	Enabled         bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Mode            string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	Complexities    []string `json:"complexities,omitempty" yaml:"complexities,omitempty"`
	MaxSubtasks     int      `json:"maxSubtasks,omitempty" yaml:"max-subtasks,omitempty"`
	TargetDiffLines int      `json:"targetDiffLines,omitempty" yaml:"target-diff-lines,omitempty"`
}

// ContextDiscoveryDecl configures Stage 2 caller & memory context discovery in FlowDeclarationV4.
type ContextDiscoveryDecl struct {
	Enabled        bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	IncludeCallers bool     `json:"includeCallers,omitempty" yaml:"include-callers,omitempty"`
	IncludeMemory  bool     `json:"includeMemory,omitempty" yaml:"include-memory,omitempty"`
	MaxTokens      int      `json:"maxTokens,omitempty" yaml:"max-tokens,omitempty"`
	ExtraPaths     []string `json:"extraPaths,omitempty" yaml:"extra-paths,omitempty"`
}

// ReadabilityPackDecl configures one portable language readability pack in FlowDeclarationV4.
type ReadabilityPackDecl struct {
	ID         string   `json:"id" yaml:"id"`
	Enabled    bool     `json:"enabled" yaml:"enabled"`
	Activation string   `json:"activation,omitempty" yaml:"activation,omitempty"`
	Globs      []string `json:"globs,omitempty" yaml:"globs,omitempty"`
	CustomText string   `json:"customText,omitempty" yaml:"custom-text,omitempty"`
}

// SkillStepDecl configures one modular stage-scoped skill step in FlowDeclarationV4.
type SkillStepDecl struct {
	ID       string   `json:"id" yaml:"id"`
	Label    string   `json:"label,omitempty" yaml:"label,omitempty"`
	Stage    string   `json:"stage,omitempty" yaml:"stage,omitempty"`
	Enabled  bool     `json:"enabled" yaml:"enabled"`
	Globs    []string `json:"globs,omitempty" yaml:"globs,omitempty"`
	MaxBytes int      `json:"maxBytes,omitempty" yaml:"max-bytes,omitempty"`
	Text     string   `json:"text,omitempty" yaml:"text,omitempty"`
}

// PRContractDecl configures Stage 6 pull request authoring & lint-suppression rules in FlowDeclarationV4.
type PRContractDecl struct {
	Enabled               bool     `json:"enabled" yaml:"enabled"`
	Mode                  string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxSubjectChars       int      `json:"maxSubjectChars,omitempty" yaml:"max-subject-chars,omitempty"`
	RequiredSections      []string `json:"requiredSections,omitempty" yaml:"required-sections,omitempty"`
	AuditLintSuppressions bool     `json:"auditLintSuppressions,omitempty" yaml:"audit-lint-suppressions,omitempty"`
	GuidanceText          string   `json:"guidanceText,omitempty" yaml:"guidance-text,omitempty"`
}

// FlowDeclarationV4 is the declarative grain.dev/qa-flow/v4 specification.
type FlowDeclarationV4 struct {
	Schema           string                `json:"schema" yaml:"schema"`
	Key              string                `json:"key" yaml:"key"`
	Extends          string                `json:"extends,omitempty" yaml:"extends,omitempty"`
	Label            string                `json:"label,omitempty" yaml:"label,omitempty"`
	Summary          string                `json:"summary,omitempty" yaml:"summary,omitempty"`
	Skill            SkillDecl             `json:"skill,omitempty" yaml:"skill,omitempty"`
	Skills           V3SkillsDecl          `json:"skills,omitempty" yaml:"skills,omitempty"`
	Stages           V3StagesDecl          `json:"stages,omitempty" yaml:"stages,omitempty"`
	Decompose        DecomposeDecl         `json:"decompose,omitempty" yaml:"decompose,omitempty"`
	Context          ContextDiscoveryDecl  `json:"context,omitempty" yaml:"context,omitempty"`
	ReadabilityPacks []ReadabilityPackDecl `json:"readabilityPacks,omitempty" yaml:"readability-packs,omitempty"`
	SkillSteps       []SkillStepDecl       `json:"skillSteps,omitempty" yaml:"skill-steps,omitempty"`
	PRContract       PRContractDecl        `json:"prContract,omitempty" yaml:"pr-contract,omitempty"`
	StopNudge        *bool                 `json:"stopNudge,omitempty" yaml:"stop-nudge,omitempty"`
	StagingIsolation *bool                 `json:"stagingIsolation,omitempty" yaml:"staging-isolation,omitempty"`
	Checks           map[string]CheckDecl  `json:"checks,omitempty" yaml:"checks,omitempty"`
	CustomChecks     []CustomCheckDecl     `json:"custom-checks,omitempty" yaml:"custom-checks,omitempty"`
	Verifier         VerifierDecl          `json:"verifier,omitempty" yaml:"verifier,omitempty"`
}

// WorkflowStageView represents one of the 8 canonical workflow lifecycle stages in UI/API projections.
type WorkflowStageView struct {
	ID       string             `json:"id"`
	Index    int                `json:"index"`
	Label    string             `json:"label"`
	Boundary string             `json:"boundary"`
	Enabled  bool               `json:"enabled"`
	Mode     string             `json:"mode,omitempty"`
	Summary  string             `json:"summary"`
	Steps    []WorkflowStepView `json:"steps"`
}

// WorkflowStepView represents one skill, pack, check, critic, or gate step inside a WorkflowStageView.
type WorkflowStepView struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	StageID     string `json:"stageId"`
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode,omitempty"`
	Scope       string `json:"scope,omitempty"`
	LockedFloor bool   `json:"lockedFloor,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Text        string `json:"text,omitempty"`
	Bytes       int    `json:"bytes,omitempty"`
}

const engineeringWorkflowV4YAML = `schema: grain.dev/qa-flow/v4
key: engineering-workflow-v4
extends: golden-v3@1
label: engineering workflow v4 (8-stage lifecycle + readability & PR packs)
summary: >
  End-to-end 8-stage engineering workflow combining complexity-gated task decomposition,
  caller & memory context discovery, fresh-context plan critique, portable language
  readability packs (go, ts-angular, python, cpp), deterministic S3 gates, PR authoring
  contracts, S4 adversarial verification, and ComposeTightenOnly S5 merge triage.

decompose:
  enabled: true
  mode: slice_plan
  complexities: [medium, high]
  max-subtasks: 4
  target-diff-lines: 400

context:
  enabled: true
  include-callers: true
  include-memory: true
  max-tokens: 12000
  extra-paths: ["design/design/**", "docs/**"]

readability-packs:
  - id: go
    enabled: true
    activation: auto
  - id: ts-angular
    enabled: true
    activation: auto
  - id: python
    enabled: true
    activation: auto
  - id: cpp
    enabled: true
    activation: auto

skill-steps:
  - id: "step:minimal-diff-discipline"
    label: "Minimal diff & caller sweep discipline"
    stage: coding
    enabled: true
    max-bytes: 600
    text: |
      Keep diffs focused on the requested change; update sibling callers when changing exported signatures and add regression tests that fail without your fix.
  - id: "step:pre-pr-self-audit"
    label: "Pre-PR self-review & verification log"
    stage: pr
    enabled: true
    max-bytes: 600
    text: |
      Before opening or updating a pull request, re-read your full git diff for unintended edits, verify build/vet/tests pass on HEAD, and document verification commands in the PR description.

pr-contract:
  enabled: true
  mode: warn
  max-subject-chars: 72
  required-sections: ["Summary", "Verification"]
  audit-lint-suppressions: true
  guidance-text: |
    Write a Conventional Commits subject line <= 72 characters and include Summary and Verification sections in the PR body without unannotated lint suppressions.

stages:
  s0-intent:
    enabled: true
    complexities: [medium, high]
    max-seconds: 30
    max-tokens: 15000
    on-clarification: warn
  s2-plan-critic:
    enabled: true
    complexities: [high]
    tiers: [T2, T3]
    max-seconds: 45
    max-tokens: 25000
    max-bounces: 1
  s5-merge-triage:
    mode: shadow
    auto-merge-tiers: [T0, T1]
    require-human-tiers: [T2, T3]

checks:
  golden:test-integrity-guard: {}
  golden:strict-config-decoder: {}
  golden:sibling-export-hazard: {severity: warn, params: {resolve: import-path}}
  golden:ui-budget-and-spec-floor: {enabled: true}
  golden:exported-surface-delta: {severity: warn}
  golden:test-hygiene: {severity: warn}
  golden:mutation-hardening: {severity: warn, params: {mutants: 25, max-seconds: 60}}
  golden:boundary-fuzz: {severity: warn, params: {max-seconds: 15}}
  golden:pr-description-contract: {enabled: true, severity: warn}
`

// BuiltInReadabilityPackText returns the portable, public-safe engineering readability guidance for id.
func BuiltInReadabilityPackText(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "go":
		return `Go Readability & Reliability Rules (pack:go):
- Error handling: wrap errors with fmt.Errorf("operation: %w", err); never discard errors silently on I/O, transactions, or close calls that flush state.
- Context & concurrency: accept context.Context as the first parameter; ensure every goroutine has a deterministic termination path on ctx.Done() and release locks/timers via defer.
- Config & zero values: make zero values useful; use pointers to distinguish omitted optional fields from explicit zero values; enable KnownFields(true) or DisallowUnknownFields() on config decoders.
- Slices & arithmetic: guard slice bounds, negative lengths, and integer conversions against overflow or wraparound.
- Testing: write deterministic table-driven subtests (t.Run) asserting concrete output values rather than error-only checks.`
	case "ts-angular":
		return `TypeScript & Angular Readability Rules (pack:ts-angular):
- Strict typing: avoid any and non-null ! assertions; model API contracts and component state with explicit interfaces and narrow union types.
- Signals & reactivity: prefer Angular signals (signal, computed, input) and immutable state updates; clean up subscriptions with takeUntilDestroyed.
- Templates & budgets: use native control flow (@if, @for, @switch) with explicit track expressions; keep feature routes lazy-loaded and respect component style budgets.
- Accessibility & hygiene: preserve semantic landmarks, ARIA labels, keyboard focus order, and test hooks; never add eslint-disable or @ts-ignore without an inline justification.
- Testing: maintain unit .spec.ts coverage for every new component, state transition, and error branch.`
	case "python":
		return `Python Readability & Type-Safety Rules (pack:python):
- Type annotations: annotate all public function parameters, return values, and structured data models (dataclasses or TypedDict); avoid untyped dict payloads across module boundaries.
- Defaults & resources: never use mutable default arguments; manage files, locks, subprocesses, and network sessions with context managers (with).
- Exceptions: catch specific exception subclasses instead of bare except or broad Exception; chain underlying causes with raise ... from err.
- Comprehensions & clarity: keep comprehensions concise (single loop/filter); prefer explicit loops or generators when side effects or multi-branch logic are involved.
- Testing: write deterministic pytest tests using tmp_path and isolated fixtures without time.sleep polling.`
	case "cpp":
		return `C++ Readability & Memory-Safety Rules (pack:cpp):
- Ownership & RAII: manage memory, file descriptors, and locks strictly through RAII (std::unique_ptr, std::vector, scoped locks); avoid raw new/delete and ambiguous pointer ownership.
- Views & lifetimes: use std::string_view and std::span<const T> only when the backing buffer is guaranteed to outlive the consumer; never return views to temporaries.
- Bounds & integer safety: validate container indices and emptiness before access; guard signed/unsigned conversions and size arithmetic against overflow.
- Const correctness & headers: mark non-mutating methods and local values const; keep headers self-contained with minimal includes.
- Testing: cover boundary sizes (0, 1, max) and error paths cleanly under address and undefined-behavior sanitizers.`
	default:
		return ""
	}
}

// ParseFlowDeclarationV4 strictly decodes YAML or JSON into a FlowDeclarationV4 (KnownFields(true)).
func ParseFlowDeclarationV4(raw []byte) (*FlowDeclarationV4, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("qa: empty flow declaration")
	}
	trimmed = normalizeJSONFlowDeclarationBytes(trimmed)

	var decl FlowDeclarationV4
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	dec.KnownFields(true)
	if err := dec.Decode(&decl); err != nil {
		return nil, fmt.Errorf("qa: parsing flow declaration v4: %w", err)
	}
	if decl.Schema == "" {
		decl.Schema = FlowSchemaV4
	}
	if decl.Schema != FlowSchemaV4 {
		return nil, fmt.Errorf("qa: unsupported flow schema %q (want %q)", decl.Schema, FlowSchemaV4)
	}
	if strings.TrimSpace(decl.Key) == "" {
		return nil, fmt.Errorf("qa: flow key is required")
	}
	return &decl, nil
}

// ResolveFlowDeclarationV4 resolves inheritance, validates all V4/V3/V2 stages, packs, skill steps, and PR contracts, and computes the canonical SHA-256 hash.
func ResolveFlowDeclarationV4(decl FlowDeclarationV4, version int, sourceYAML string, explicitParentHash string, builtIn bool, lookup ParentLookupFunc) (FlowVersionBundle, error) {
	effectiveLookup := func(ref string) (FlowVersionBundle, bool) {
		if lookup != nil {
			if b, ok := lookup(ref); ok {
				return b, true
			}
		}
		return LookupBuiltInOrRegisteredVersion(ref)
	}

	if decl.Skills.Coding.Mode == "" && decl.Skills.Coding.Text == "" && decl.Skills.Coding.MaxBytes == 0 {
		if decl.Skill.Mode != "" || decl.Skill.Text != "" || decl.Skill.MaxBytes != 0 {
			decl.Skills.Coding = decl.Skill
		}
	}

	// Inherit V4 fields from V4 parent when extending one.
	if ext := strings.TrimSpace(decl.Extends); ext != "" {
		if parent, ok := effectiveLookup(ext); ok && parent.DeclarationV4 != nil {
			pv4 := parent.DeclarationV4
			if !decl.Decompose.Enabled && decl.Decompose.Mode == "" && len(decl.Decompose.Complexities) == 0 && decl.Decompose.MaxSubtasks == 0 && decl.Decompose.TargetDiffLines == 0 {
				decl.Decompose = cloneDecomposeDecl(pv4.Decompose)
			}
			if !decl.Context.Enabled && !decl.Context.IncludeCallers && !decl.Context.IncludeMemory && decl.Context.MaxTokens == 0 && len(decl.Context.ExtraPaths) == 0 {
				decl.Context = cloneContextDiscoveryDecl(pv4.Context)
			}
			if len(decl.ReadabilityPacks) == 0 && len(pv4.ReadabilityPacks) > 0 {
				decl.ReadabilityPacks = cloneReadabilityPacks(pv4.ReadabilityPacks)
			} else if len(decl.ReadabilityPacks) > 0 && len(pv4.ReadabilityPacks) > 0 {
				decl.ReadabilityPacks = mergeReadabilityPacks(pv4.ReadabilityPacks, decl.ReadabilityPacks)
			}
			if len(decl.SkillSteps) == 0 && len(pv4.SkillSteps) > 0 {
				decl.SkillSteps = cloneSkillSteps(pv4.SkillSteps)
			} else if len(decl.SkillSteps) > 0 && len(pv4.SkillSteps) > 0 {
				decl.SkillSteps = mergeSkillSteps(pv4.SkillSteps, decl.SkillSteps)
			}
			if !decl.PRContract.Enabled && decl.PRContract.Mode == "" && decl.PRContract.MaxSubjectChars == 0 && len(decl.PRContract.RequiredSections) == 0 && !decl.PRContract.AuditLintSuppressions && decl.PRContract.GuidanceText == "" {
				decl.PRContract = clonePRContractDecl(pv4.PRContract)
			} else if decl.PRContract.Mode != "off" {
				if decl.PRContract.MaxSubjectChars == 0 {
					decl.PRContract.MaxSubjectChars = pv4.PRContract.MaxSubjectChars
				}
				if len(decl.PRContract.RequiredSections) == 0 && len(pv4.PRContract.RequiredSections) > 0 {
					decl.PRContract.RequiredSections = slices.Clone(pv4.PRContract.RequiredSections)
				}
				if decl.PRContract.GuidanceText == "" {
					decl.PRContract.GuidanceText = pv4.PRContract.GuidanceText
				}
			}
		}
	}

	// Validate DecomposeDecl.
	decompMode := strings.TrimSpace(decl.Decompose.Mode)
	if decl.Decompose.Enabled && decompMode == "" {
		decompMode = "slice_plan"
	}
	if decompMode != "" && decompMode != "off" && decompMode != "slice_plan" && decompMode != "propose_dag" {
		return FlowVersionBundle{}, fmt.Errorf("qa: invalid decompose.mode %q (want off, slice_plan, or propose_dag)", decompMode)
	}
	decl.Decompose.Mode = decompMode
	if decl.Decompose.MaxSubtasks < 0 || decl.Decompose.MaxSubtasks > 32 {
		return FlowVersionBundle{}, fmt.Errorf("qa: decompose.max-subtasks %d out of range [0,32]", decl.Decompose.MaxSubtasks)
	}
	if decl.Decompose.TargetDiffLines < 0 {
		return FlowVersionBundle{}, fmt.Errorf("qa: decompose.target-diff-lines %d cannot be negative", decl.Decompose.TargetDiffLines)
	}
	for _, c := range decl.Decompose.Complexities {
		if c != string(ComplexityLow) && c != string(ComplexityMedium) && c != string(ComplexityHigh) {
			return FlowVersionBundle{}, fmt.Errorf("qa: invalid decompose complexity %q", c)
		}
	}

	// Validate ContextDiscoveryDecl.
	if decl.Context.MaxTokens < 0 || decl.Context.MaxTokens > MaxVerifierTokensCap {
		return FlowVersionBundle{}, fmt.Errorf("qa: context.max-tokens %d out of range [0,%d]", decl.Context.MaxTokens, MaxVerifierTokensCap)
	}

	// Validate ReadabilityPacks.
	for i := range decl.ReadabilityPacks {
		rp := &decl.ReadabilityPacks[i]
		rp.ID = strings.TrimSpace(rp.ID)
		if rp.ID == "" {
			return FlowVersionBundle{}, fmt.Errorf("qa: readability-packs[%d] id is required", i)
		}
		act := strings.TrimSpace(rp.Activation)
		if act == "" {
			act = "auto"
		}
		if act != "auto" && act != "always" && act != "glob" {
			return FlowVersionBundle{}, fmt.Errorf("qa: invalid readability pack %q activation %q (want auto, always, or glob)", rp.ID, act)
		}
		if act == "glob" && len(rp.Globs) == 0 {
			return FlowVersionBundle{}, fmt.Errorf("qa: readability pack %q with glob activation requires non-empty globs", rp.ID)
		}
		rp.Activation = act
		rp.CustomText = normalizeSkillText(rp.CustomText)
		if BuiltInReadabilityPackText(rp.ID) == "" && rp.CustomText == "" {
			return FlowVersionBundle{}, fmt.Errorf("qa: unknown built-in readability pack %q (provide custom-text for custom packs)", rp.ID)
		}
	}

	// Validate SkillSteps.
	for i := range decl.SkillSteps {
		ss := &decl.SkillSteps[i]
		ss.ID = strings.TrimSpace(ss.ID)
		if ss.ID == "" {
			return FlowVersionBundle{}, fmt.Errorf("qa: skill-steps[%d] id is required", i)
		}
		stg := strings.ToLower(strings.TrimSpace(ss.Stage))
		if stg == "" {
			stg = "coding"
		}
		if stg != "s0" && stg != "s2" && stg != "coding" && stg != "pr" && stg != "s4" {
			return FlowVersionBundle{}, fmt.Errorf("qa: invalid skill step %q stage %q (want s0, s2, coding, pr, or s4)", ss.ID, stg)
		}
		ss.Stage = stg
		ss.Text = normalizeSkillText(ss.Text)
		capB := ss.MaxBytes
		if capB <= 0 {
			capB = MaxSkillBytesCap
		}
		if capB > MaxSkillBytesCap {
			return FlowVersionBundle{}, fmt.Errorf("qa: skill step %q max-bytes %d exceeds maximum %d", ss.ID, capB, MaxSkillBytesCap)
		}
		if len(ss.Text) > capB {
			return FlowVersionBundle{}, fmt.Errorf("qa: skill step %q text (%d bytes) exceeds max-bytes %d", ss.ID, len(ss.Text), capB)
		}
	}

	// Validate PRContractDecl.
	prMode := strings.TrimSpace(decl.PRContract.Mode)
	if decl.PRContract.Enabled && prMode == "" {
		prMode = "warn"
	}
	if prMode != "" && prMode != "off" && prMode != "warn" && prMode != "blocking" {
		return FlowVersionBundle{}, fmt.Errorf("qa: invalid pr-contract.mode %q (want off, warn, or blocking)", prMode)
	}
	decl.PRContract.Mode = prMode
	decl.PRContract.GuidanceText = normalizeSkillText(decl.PRContract.GuidanceText)
	if decl.PRContract.MaxSubjectChars < 0 || decl.PRContract.MaxSubjectChars > 500 {
		return FlowVersionBundle{}, fmt.Errorf("qa: pr-contract.max-subject-chars %d out of range [0,500]", decl.PRContract.MaxSubjectChars)
	}

	if decl.PRContract.Enabled && decl.PRContract.Mode != "off" {
		if decl.Checks == nil {
			decl.Checks = make(map[string]CheckDecl)
		}
		if _, ok := decl.Checks[RulePRDescriptionContract]; !ok {
			sev := decl.PRContract.Mode
			if sev != "blocking" && sev != "warn" {
				sev = "warn"
			}
			decl.Checks[RulePRDescriptionContract] = CheckDecl{
				Enabled:  boolPtr(true),
				Severity: sev,
			}
		}
	}

	v3Input := FlowDeclarationV3{
		Schema:       FlowSchemaV3,
		Key:          decl.Key,
		Extends:      decl.Extends,
		Label:        decl.Label,
		Summary:      decl.Summary,
		Skills:       decl.Skills,
		Stages:       decl.Stages,
		Checks:       decl.Checks,
		CustomChecks: decl.CustomChecks,
		Verifier:     decl.Verifier,
	}
	b, err := ResolveFlowDeclarationV3(v3Input, version, sourceYAML, explicitParentHash, builtIn, effectiveLookup)
	if err != nil {
		return FlowVersionBundle{}, err
	}

	if decl.StopNudge != nil {
		b.Declaration.StopNudge = boolPtr(*decl.StopNudge)
		b.Resolved.StopNudge = *decl.StopNudge
	}
	if decl.StagingIsolation != nil {
		if !*decl.StagingIsolation {
			for _, c := range b.Declaration.Checks {
				if c.Enabled != nil && *c.Enabled && c.Severity == "blocking" {
					return FlowVersionBundle{}, fmt.Errorf("qa: staging-isolation cannot be false when blocking checks are enabled")
				}
			}
		}
		b.Declaration.StagingIsolation = boolPtr(*decl.StagingIsolation)
		b.Resolved.StagingIsolation = *decl.StagingIsolation
	}

	matV4 := FlowDeclarationV4{
		Schema:           FlowSchemaV4,
		Key:              b.Key,
		Extends:          b.Declaration.Extends,
		Label:            b.Label,
		Summary:          b.Summary,
		Skills:           b.DeclarationV3.Skills,
		Stages:           b.DeclarationV3.Stages,
		Decompose:        decl.Decompose,
		Context:          decl.Context,
		ReadabilityPacks: slices.Clone(decl.ReadabilityPacks),
		SkillSteps:       slices.Clone(decl.SkillSteps),
		PRContract:       decl.PRContract,
		StopNudge:        b.Declaration.StopNudge,
		StagingIsolation: b.Declaration.StagingIsolation,
		Checks:           b.Declaration.Checks,
		CustomChecks:     b.DeclarationV3.CustomChecks,
		Verifier:         b.Declaration.Verifier,
	}

	b.SchemaVer = FlowSchemaV4
	b.DeclarationV4 = &matV4
	b.Resolved.SchemaVer = FlowSchemaV4
	b.Resolved.Decompose = cloneDecomposeDecl(matV4.Decompose)
	b.Resolved.Context = cloneContextDiscoveryDecl(matV4.Context)
	b.Resolved.ReadabilityPacks = cloneReadabilityPacks(matV4.ReadabilityPacks)
	b.Resolved.SkillSteps = cloneSkillSteps(matV4.SkillSteps)
	b.Resolved.PRContract = clonePRContractDecl(matV4.PRContract)

	specBytes, hash, err := CanonicalAnyJSONAndHash(matV4)
	if err != nil {
		return FlowVersionBundle{}, err
	}
	b.SpecJSON = string(specBytes)
	b.Hash = hash

	return b, nil
}

// ActiveCodingGuidance returns SkillPrompt() combined with matching enabled ReadabilityPacks,
// enabled "coding"/"pr" SkillSteps, and PRContract guidance, capped to 8192 bytes.
func (f FlowSpec) ActiveCodingGuidance(targetPaths []string) string {
	var sections []string
	if base := strings.TrimSpace(f.SkillPrompt()); base != "" {
		sections = append(sections, base)
	}

	for _, pack := range f.ReadabilityPacks {
		if !pack.Enabled {
			continue
		}
		if !readabilityPackMatches(pack, targetPaths) {
			continue
		}
		txt := BuiltInReadabilityPackText(pack.ID)
		if custom := strings.TrimSpace(pack.CustomText); custom != "" {
			if txt != "" {
				txt = txt + "\n" + custom
			} else {
				txt = custom
			}
		}
		if txt != "" {
			sections = append(sections, txt)
		}
	}

	for _, step := range f.SkillSteps {
		if !step.Enabled {
			continue
		}
		stg := strings.ToLower(strings.TrimSpace(step.Stage))
		if stg == "" {
			stg = "coding"
		}
		if stg != "coding" && stg != "pr" {
			continue
		}
		if len(step.Globs) > 0 && len(targetPaths) > 0 && !MatchesAnyGlob(step.Globs, targetPaths) {
			continue
		}
		if txt := strings.TrimSpace(step.Text); txt != "" {
			sections = append(sections, txt)
		}
	}

	if f.PRContract.Enabled && f.PRContract.Mode != "off" {
		var lines []string
		lines = append(lines, fmt.Sprintf("PR Contract (mode: %s):", defaultNonEmpty(f.PRContract.Mode, "warn")))
		if f.PRContract.MaxSubjectChars > 0 {
			lines = append(lines, fmt.Sprintf("- Subject line must be <= %d characters.", f.PRContract.MaxSubjectChars))
		}
		if len(f.PRContract.RequiredSections) > 0 {
			lines = append(lines, fmt.Sprintf("- Required PR description sections: %s.", strings.Join(f.PRContract.RequiredSections, ", ")))
		}
		if f.PRContract.AuditLintSuppressions {
			lines = append(lines, "- Audit lint/type suppressions: do not add unannotated nolint, eslint-disable, or type-ignore directives.")
		}
		if g := strings.TrimSpace(f.PRContract.GuidanceText); g != "" {
			lines = append(lines, g)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}

	combined := strings.TrimSpace(strings.Join(sections, "\n\n"))
	if len(combined) > MaxActiveCodingGuidanceBytes {
		combined = strings.TrimSpace(combined[:MaxActiveCodingGuidanceBytes])
	}
	return combined
}

func readabilityPackMatches(pack ReadabilityPackDecl, targetPaths []string) bool {
	act := strings.ToLower(strings.TrimSpace(pack.Activation))
	if act == "" {
		act = "auto"
	}
	switch act {
	case "always":
		return true
	case "glob":
		if len(targetPaths) == 0 {
			return len(pack.Globs) == 0
		}
		return MatchesAnyGlob(pack.Globs, targetPaths)
	case "auto":
		id := strings.ToLower(strings.TrimSpace(pack.ID))
		if len(targetPaths) == 0 {
			// When targetPaths is empty, auto packs include go and ts-angular (or custom packs) within the 8192B cap.
			return id == "go" || id == "ts-angular" || (id != "python" && id != "cpp")
		}
		if len(pack.Globs) > 0 && MatchesAnyGlob(pack.Globs, targetPaths) {
			return true
		}
		for _, p := range targetPaths {
			lower := strings.ToLower(strings.TrimSpace(p))
			switch id {
			case "go":
				if strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, "go.mod") || strings.HasSuffix(lower, "go.sum") {
					return true
				}
			case "ts-angular":
				if strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".css") || strings.HasSuffix(lower, ".scss") || strings.HasSuffix(lower, ".mjs") || strings.HasSuffix(lower, ".js") {
					return true
				}
			case "python":
				if strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".pyi") {
					return true
				}
			case "cpp":
				if strings.HasSuffix(lower, ".cc") || strings.HasSuffix(lower, ".cpp") || strings.HasSuffix(lower, ".cxx") || strings.HasSuffix(lower, ".c") || strings.HasSuffix(lower, ".h") || strings.HasSuffix(lower, ".hpp") || strings.HasSuffix(lower, ".hh") {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}

func readabilityPackScope(pack ReadabilityPackDecl) string {
	if len(pack.Globs) > 0 {
		return strings.Join(pack.Globs, ", ")
	}
	switch strings.ToLower(strings.TrimSpace(pack.ID)) {
	case "go":
		return "*.go"
	case "ts-angular":
		return "*.ts, *.html, *.css"
	case "python":
		return "*.py"
	case "cpp":
		return "*.cc, *.cpp, *.h, *.hpp"
	default:
		return "*.*"
	}
}

// BuildWorkflowStagesView projects any v1, v2, v3, or v4 FlowVersionBundle onto the 8 canonical lifecycle stages.
func BuildWorkflowStagesView(b FlowVersionBundle) []WorkflowStageView {
	f := b.Resolved
	rules := f.Rules
	if len(rules) == 0 {
		rules = append(defaultS3Rules(), s4Rule(f.EnableS4))
	}

	// Stage 1: s0-decompose
	decompEnabled := f.Decompose.Enabled && f.Decompose.Mode != "off"
	s0Enabled := f.S0Intent || decompEnabled || hasEnabledSkillStepForStage(f.SkillSteps, "s0")
	s0Mode := "off"
	if decompEnabled {
		s0Mode = defaultNonEmpty(f.Decompose.Mode, "slice_plan")
	} else if f.S0Intent {
		s0Mode = defaultNonEmpty(f.S0IntentConfig.OnClarification, "warn")
	}
	s0Steps := []WorkflowStepView{
		{
			ID:      "s0:intent",
			Label:   "Intent & WHEN/SHALL Acceptance Criteria",
			Kind:    "llm_critic",
			StageID: "s0-decompose",
			Enabled: f.S0Intent,
			Mode:    boolMode(f.S0Intent, defaultNonEmpty(f.S0IntentConfig.OnClarification, "warn")),
			Scope:   joinOrDefault(f.S0IntentConfig.Complexities, "medium, high"),
			Summary: "Extracts testable WHEN ... SHALL criteria and explicit non-goals from the task brief before edits begin.",
			Text:    f.IntentSkillText,
			Bytes:   len(f.IntentSkillText),
		},
		{
			ID:      "s0:decompose",
			Label:   "Complexity-Gated Subtask Decomposition",
			Kind:    "decomposition",
			StageID: "s0-decompose",
			Enabled: decompEnabled,
			Mode:    boolMode(decompEnabled, defaultNonEmpty(f.Decompose.Mode, "slice_plan")),
			Scope:   joinOrDefault(f.Decompose.Complexities, "medium, high"),
			Summary: fmt.Sprintf("Splits complex work into bounded slices (maxSubtasks=%d, targetDiffLines=%d).", f.Decompose.MaxSubtasks, f.Decompose.TargetDiffLines),
		},
	}
	s0Steps = append(s0Steps, buildSkillStepViews(f.SkillSteps, "s0", "s0-decompose")...)

	// Stage 2: s1-context
	s1Mode := "blast_only"
	if f.Context.Enabled {
		s1Mode = "context+blast"
	}
	s1Steps := []WorkflowStepView{
		{
			ID:          "s1:blast-radius",
			Label:       "Deterministic 0..100 Blast-Radius Classifier",
			Kind:        "classifier",
			StageID:     "s1-context",
			Enabled:     true,
			Mode:        "enforced",
			Scope:       "T0..T3",
			LockedFloor: true,
			Summary:     fmt.Sprintf("Scores diff blast radius (T0..T3) with %d tighten-only path override(s).", len(f.RiskOverrides)),
		},
		{
			ID:      "s1:context-discovery",
			Label:   "Sibling Caller & Memory Context Discovery",
			Kind:    "context_discovery",
			StageID: "s1-context",
			Enabled: f.Context.Enabled,
			Mode:    boolMode(f.Context.Enabled, "active"),
			Scope:   joinOrDefault(f.Context.ExtraPaths, "import-graph + memory"),
			Summary: fmt.Sprintf("includeCallers=%v includeMemory=%v maxTokens=%d", f.Context.IncludeCallers, f.Context.IncludeMemory, f.Context.MaxTokens),
		},
	}

	// Stage 3: s2-plan
	s2CustomStages := buildCustomStageViews(f.CustomStages, "s2", "s2-plan")
	s2SkillSteps := buildSkillStepViews(f.SkillSteps, "s2", "s2-plan")
	s2PlanEnabled := f.S2PlanCritic || len(s2CustomStages) > 0 || hasEnabledSkillStepForStage(f.SkillSteps, "s2")
	s2PlanSteps := []WorkflowStepView{
		{
			ID:      "s2:plan-critic",
			Label:   "Fresh-Context Plan & Blast Critic",
			Kind:    "llm_critic",
			StageID: "s2-plan",
			Enabled: f.S2PlanCritic,
			Mode:    boolMode(f.S2PlanCritic, "blocking"),
			Scope:   joinOrDefault(append(slices.Clone(f.S2PlanCriticConfig.Complexities), f.S2PlanCriticConfig.Tiers...), "high, T2, T3"),
			Summary: fmt.Sprintf("Critiques file plan and sibling caller coverage (maxBounces=%d, maxSeconds=%d).", f.S2PlanCriticConfig.MaxBounces, f.S2PlanCriticConfig.MaxSeconds),
			Text:    f.PlanCriticSkillText,
			Bytes:   len(f.PlanCriticSkillText),
		},
	}
	s2PlanSteps = append(s2PlanSteps, s2SkillSteps...)
	s2PlanSteps = append(s2PlanSteps, s2CustomStages...)

	// Stage 4: s2-coding
	codingPrompt := f.SkillPrompt()
	codingSteps := []WorkflowStepView{
		{
			ID:      "coding:skill",
			Label:   "Core Engineering Coding Contract",
			Kind:    "skill",
			StageID: "s2-coding",
			Enabled: f.InjectSkill && len(codingPrompt) > 0,
			Mode:    boolMode(f.InjectSkill && len(codingPrompt) > 0, defaultNonEmpty(b.Declaration.Skill.Mode, "replace")),
			Scope:   "**/*",
			Summary: fmt.Sprintf("Primary coding skill contract (%d bytes).", len(codingPrompt)),
			Text:    codingPrompt,
			Bytes:   len(codingPrompt),
		},
	}
	hasEnabledPack := false
	for _, pack := range f.ReadabilityPacks {
		if pack.Enabled {
			hasEnabledPack = true
		}
		packText := BuiltInReadabilityPackText(pack.ID)
		if custom := strings.TrimSpace(pack.CustomText); custom != "" {
			if packText != "" {
				packText = packText + "\n" + custom
			} else {
				packText = custom
			}
		}
		codingSteps = append(codingSteps, WorkflowStepView{
			ID:      "pack:" + pack.ID,
			Label:   fmt.Sprintf("Readability Pack (%s)", pack.ID),
			Kind:    "readability_pack",
			StageID: "s2-coding",
			Enabled: pack.Enabled,
			Mode:    defaultNonEmpty(pack.Activation, "auto"),
			Scope:   readabilityPackScope(pack),
			Summary: fmt.Sprintf("Portable %s readability guidance (%s activation, %d bytes).", pack.ID, defaultNonEmpty(pack.Activation, "auto"), len(packText)),
			Text:    packText,
			Bytes:   len(packText),
		})
	}
	codingSteps = append(codingSteps, buildSkillStepViews(f.SkillSteps, "coding", "s2-coding")...)
	s2CodingEnabled := (f.InjectSkill && len(codingPrompt) > 0) || hasEnabledPack || hasEnabledSkillStepForStage(f.SkillSteps, "coding")

	// Stage 5: s3-checks
	var s3Steps []WorkflowStepView
	for _, r := range rules {
		if r.Stage == "S4" || r.ID == RuleS4OrthogonalVerifier {
			continue
		}
		locked := r.ID == RuleTestIntegrityGuard || r.ID == RuleStrictConfigDecoder
		s3Steps = append(s3Steps, WorkflowStepView{
			ID:          r.ID,
			Label:       r.Label,
			Kind:        "builtin_check",
			StageID:     "s3-checks",
			Enabled:     r.Enabled,
			Mode:        boolMode(r.Enabled, defaultNonEmpty(r.Severity, "blocking")),
			Scope:       strings.Join(r.TargetPaths, ", "),
			LockedFloor: locked,
			Summary:     r.Summary,
		})
	}
	for _, cc := range f.CustomChecks {
		kind := defaultNonEmpty(cc.Kind, cc.Engine)
		scope := append(slices.Clone(cc.Paths), cc.WhenChanged...)
		s3Steps = append(s3Steps, WorkflowStepView{
			ID:      cc.ID,
			Label:   defaultNonEmpty(cc.Label, cc.ID),
			Kind:    "custom_check:" + kind,
			StageID: "s3-checks",
			Enabled: true,
			Mode:    defaultNonEmpty(cc.Severity, "warn"),
			Scope:   joinOrDefault(scope, "**/*"),
			Summary: cc.Message,
		})
	}
	s3Steps = append(s3Steps, buildCustomStageViews(f.CustomStages, "s3", "s3-checks")...)

	// Stage 6: s3-pr
	prContractActive := (f.PRContract.Enabled && f.PRContract.Mode != "off") || f.HasRule(RulePRDescriptionContract)
	prContractMode := "off"
	if prContractActive {
		prContractMode = defaultNonEmpty(f.PRContract.Mode, "warn")
	}
	prSteps := []WorkflowStepView{
		{
			ID:      "pr:contract",
			Label:   "PR Subject, Required Sections & Lint-Suppression Contract",
			Kind:    "pr_contract",
			StageID: "s3-pr",
			Enabled: prContractActive,
			Mode:    prContractMode,
			Scope:   joinOrDefault(f.PRContract.RequiredSections, "Summary, Verification"),
			Summary: fmt.Sprintf("maxSubjectChars=%d requiredSections=%v auditLintSuppressions=%v", f.PRContract.MaxSubjectChars, f.PRContract.RequiredSections, f.PRContract.AuditLintSuppressions),
			Text:    f.PRContract.GuidanceText,
			Bytes:   len(f.PRContract.GuidanceText),
		},
		{
			ID:      "pr:stop-nudge",
			Label:   "Pre-Completion Self-Review Nudge",
			Kind:    "stop_nudge",
			StageID: "s3-pr",
			Enabled: f.StopNudge,
			Mode:    boolMode(f.StopNudge, "active"),
			Scope:   "turn completion",
			Summary: "Prompts the agent to verify build, tests, and PR description completeness before ending its turn.",
		},
	}
	prSteps = append(prSteps, buildSkillStepViews(f.SkillSteps, "pr", "s3-pr")...)
	s3PREnabled := prContractActive || f.StopNudge || hasEnabledSkillStepForStage(f.SkillSteps, "pr")

	// Stage 7: s4-verify
	vText := verifierStageText(f)
	s4Steps := []WorkflowStepView{
		{
			ID:      RuleS4OrthogonalVerifier,
			Label:   "Fresh-Context Orthogonal Diff Verifier + 6-Filter Inquisitor",
			Kind:    "llm_verifier",
			StageID: "s4-verify",
			Enabled: f.EnableS4,
			Mode:    boolMode(f.EnableS4, "blocking"),
			Scope:   joinOrDefault(f.VerifierTiers, "T2, T3"),
			Summary: fmt.Sprintf("maxRepairRounds=%d maxSeconds=%d maxTokens=%d checklist=%s", f.MaxS4RepairRounds, f.MaxS4Seconds, f.MaxS4Tokens, defaultNonEmpty(f.VerifierChecklist, ChecklistEscapeClassesV1)),
			Text:    vText,
			Bytes:   len(vText),
		},
	}
	if f.SecuritySkillText != "" {
		s4Steps = append(s4Steps, WorkflowStepView{
			ID:      "s4:security-skill",
			Label:   "Security Verifier Skill",
			Kind:    "skill",
			StageID: "s4-verify",
			Enabled: f.EnableS4,
			Mode:    boolMode(f.EnableS4, "blocking"),
			Scope:   joinOrDefault(f.VerifierTiers, "T2, T3"),
			Summary: "Additional security review instructions for Stage 4 verification.",
			Text:    f.SecuritySkillText,
			Bytes:   len(f.SecuritySkillText),
		})
	}
	s4Steps = append(s4Steps, buildSkillStepViews(f.SkillSteps, "s4", "s4-verify")...)
	s4Steps = append(s4Steps, buildCustomStageViews(f.CustomStages, "s4", "s4-verify")...)

	// Stage 8: s5-triage
	triageMode := defaultNonEmpty(f.S5MergeTriage.Mode, "off")
	triageEnabled := triageMode != "off"
	s5Steps := []WorkflowStepView{
		{
			ID:          "s5:merge-triage",
			Label:       "ComposeTightenOnly Blast-Tier Merge Triage",
			Kind:        "merge_triage",
			StageID:     "s5-triage",
			Enabled:     triageEnabled,
			Mode:        triageMode,
			Scope:       fmt.Sprintf("auto=%v human=%v", f.S5MergeTriage.AutoMergeTiers, f.S5MergeTriage.RequireHumanTiers),
			LockedFloor: true,
			Summary:     "Routes clean green low-blast PRs to auto-merge when allowed and holds high-blast or trust-boundary changes for human review.",
		},
	}

	return []WorkflowStageView{
		{
			ID:       "s0-decompose",
			Index:    1,
			Label:    "1 · Goal & Subtask Breakdown",
			Boundary: "controller_fresh_context",
			Enabled:  s0Enabled,
			Mode:     s0Mode,
			Summary:  "Extracts testable acceptance criteria and decomposes medium/high-complexity work into reviewable slices.",
			Steps:    s0Steps,
		},
		{
			ID:       "s1-context",
			Index:    2,
			Label:    "2 · Context & Blast Radius",
			Boundary: "deterministic_ast_graph",
			Enabled:  true,
			Mode:     s1Mode,
			Summary:  "Computes 0..100 diff blast-radius tiers (T0..T3) and discovers sibling callers and relevant repository memory.",
			Steps:    s1Steps,
		},
		{
			ID:       "s2-plan",
			Index:    3,
			Label:    "3 · Plan & Fresh Critique",
			Boundary: "isolated_critic",
			Enabled:  s2PlanEnabled,
			Mode:     boolMode(s2PlanEnabled, "active"),
			Summary:  "Audits the proposed file plan and sibling importers in a fresh context before heavy code edits.",
			Steps:    s2PlanSteps,
		},
		{
			ID:       "s2-coding",
			Index:    4,
			Label:    "4 · Coding & Readability Rules",
			Boundary: "sandbox_agent_prompt",
			Enabled:  s2CodingEnabled,
			Mode:     boolMode(s2CodingEnabled, "active"),
			Summary:  "Injects the core coding contract, portable language readability packs, and stage-scoped coding skill steps.",
			Steps:    codingSteps,
		},
		{
			ID:       "s3-checks",
			Index:    5,
			Label:    "5 · Pre-PR Deterministic Gates",
			Boundary: "staging_ref_ast_gate",
			Enabled:  true,
			Mode:     "enforced",
			Summary:  "Runs sub-second AST/diff integrity floors, sibling caller sweeps, mutation/fuzz hardening, and fixture-verified custom checks.",
			Steps:    s3Steps,
		},
		{
			ID:       "s3-pr",
			Index:    6,
			Label:    "6 · PR Authoring & Self-Review",
			Boundary: "pre_pr_contract",
			Enabled:  s3PREnabled,
			Mode:     boolMode(s3PREnabled, defaultNonEmpty(prContractMode, "active")),
			Summary:  "Enforces commit/PR subject length, required Summary & Verification sections, lint-suppression audits, and self-review nudges.",
			Steps:    prSteps,
		},
		{
			ID:       "s4-verify",
			Index:    7,
			Label:    "7 · Adversarial Verification",
			Boundary: "fresh_context_readonly_verifier",
			Enabled:  f.EnableS4,
			Mode:     boolMode(f.EnableS4, "blocking"),
			Summary:  "Executes a fresh-context read-only verification pass with zero author CoT and 6-filter Inquisitor grounding.",
			Steps:    s4Steps,
		},
		{
			ID:       "s5-triage",
			Index:    8,
			Label:    "8 · Scorecard & Merge Triage",
			Boundary: "controller_merge_gate",
			Enabled:  triageEnabled,
			Mode:     triageMode,
			Summary:  "Computes the quality scorecard and applies fail-closed blast-tier merge triage.",
			Steps:    s5Steps,
		},
	}
}

func buildSkillStepViews(steps []SkillStepDecl, stageFilter, stageViewID string) []WorkflowStepView {
	var out []WorkflowStepView
	for _, s := range steps {
		stg := strings.ToLower(strings.TrimSpace(s.Stage))
		if stg == "" {
			stg = "coding"
		}
		if stg != stageFilter {
			continue
		}
		out = append(out, WorkflowStepView{
			ID:      s.ID,
			Label:   defaultNonEmpty(s.Label, s.ID),
			Kind:    "skill_step",
			StageID: stageViewID,
			Enabled: s.Enabled,
			Mode:    boolMode(s.Enabled, stageFilter),
			Scope:   joinOrDefault(s.Globs, "**/*"),
			Summary: fmt.Sprintf("Custom %s skill step (%d bytes).", stageFilter, len(s.Text)),
			Text:    s.Text,
			Bytes:   len(s.Text),
		})
	}
	return out
}

func buildCustomStageViews(stages []CustomStageDecl, insertAfterFilter, stageViewID string) []WorkflowStepView {
	var out []WorkflowStepView
	for _, stg := range stages {
		ia := strings.ToLower(strings.TrimSpace(stg.InsertAfter))
		if ia == "" {
			ia = "s3"
		}
		if ia != insertAfterFilter {
			continue
		}
		tSec := stg.TimeoutSeconds
		if tSec == 0 {
			tSec = stg.Timeout
		}
		out = append(out, WorkflowStepView{
			ID:      stg.ID,
			Label:   defaultNonEmpty(stg.Label, stg.ID),
			Kind:    "custom_stage:" + defaultNonEmpty(stg.Kind, "deterministic_batch"),
			StageID: stageViewID,
			Enabled: true,
			Mode:    defaultNonEmpty(stg.MinTier, "T0"),
			Scope:   joinOrDefault(stg.WhenPaths, "**/*"),
			Summary: fmt.Sprintf("insert-after=%s kind=%s timeout=%ds checks=%v", ia, defaultNonEmpty(stg.Kind, "deterministic_batch"), tSec, stg.CheckIDs),
		})
	}
	return out
}

func hasEnabledSkillStepForStage(steps []SkillStepDecl, stageFilter string) bool {
	for _, s := range steps {
		if !s.Enabled {
			continue
		}
		stg := strings.ToLower(strings.TrimSpace(s.Stage))
		if stg == "" {
			stg = "coding"
		}
		if stg == stageFilter {
			return true
		}
	}
	return false
}

func verifierStageText(f FlowSpec) string {
	var parts []string
	if f.VerifierChecklist == "" || f.VerifierChecklist == ChecklistEscapeClassesV1 {
		parts = append(parts, EscapeClassesV1ChecklistText)
	}
	if strings.TrimSpace(f.VerifierSkillText) != "" {
		parts = append(parts, strings.TrimSpace(f.VerifierSkillText))
	}
	return strings.Join(parts, "\n\n")
}

func defaultNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fallback
}

func boolMode(enabled bool, activeMode string) string {
	if !enabled {
		return "off"
	}
	return activeMode
}

func joinOrDefault(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return strings.Join(items, ", ")
}

func cloneDecomposeDecl(d DecomposeDecl) DecomposeDecl {
	d.Complexities = slices.Clone(d.Complexities)
	return d
}

func cloneContextDiscoveryDecl(c ContextDiscoveryDecl) ContextDiscoveryDecl {
	c.ExtraPaths = slices.Clone(c.ExtraPaths)
	return c
}

func cloneReadabilityPacks(in []ReadabilityPackDecl) []ReadabilityPackDecl {
	if len(in) == 0 {
		return nil
	}
	out := make([]ReadabilityPackDecl, len(in))
	for i, p := range in {
		p.Globs = slices.Clone(p.Globs)
		out[i] = p
	}
	return out
}

func mergeReadabilityPacks(base, overrides []ReadabilityPackDecl) []ReadabilityPackDecl {
	out := cloneReadabilityPacks(base)
	for _, ov := range overrides {
		ovCopy := ov
		ovCopy.Globs = slices.Clone(ov.Globs)
		found := false
		for i := range out {
			if out[i].ID == ovCopy.ID {
				if ovCopy.Activation == "" {
					ovCopy.Activation = out[i].Activation
				}
				if len(ovCopy.Globs) == 0 && len(out[i].Globs) > 0 {
					ovCopy.Globs = slices.Clone(out[i].Globs)
				}
				if ovCopy.CustomText == "" {
					ovCopy.CustomText = out[i].CustomText
				}
				out[i] = ovCopy
				found = true
				break
			}
		}
		if !found {
			out = append(out, ovCopy)
		}
	}
	return out
}

func cloneSkillSteps(in []SkillStepDecl) []SkillStepDecl {
	if len(in) == 0 {
		return nil
	}
	out := make([]SkillStepDecl, len(in))
	for i, s := range in {
		s.Globs = slices.Clone(s.Globs)
		out[i] = s
	}
	return out
}

func mergeSkillSteps(base, overrides []SkillStepDecl) []SkillStepDecl {
	out := cloneSkillSteps(base)
	for _, ov := range overrides {
		ovCopy := ov
		ovCopy.Globs = slices.Clone(ov.Globs)
		found := false
		for i := range out {
			if out[i].ID == ovCopy.ID {
				if ovCopy.Label == "" {
					ovCopy.Label = out[i].Label
				}
				if ovCopy.Stage == "" {
					ovCopy.Stage = out[i].Stage
				}
				if len(ovCopy.Globs) == 0 && len(out[i].Globs) > 0 {
					ovCopy.Globs = slices.Clone(out[i].Globs)
				}
				if ovCopy.MaxBytes == 0 {
					ovCopy.MaxBytes = out[i].MaxBytes
				}
				if ovCopy.Text == "" {
					ovCopy.Text = out[i].Text
				}
				out[i] = ovCopy
				found = true
				break
			}
		}
		if !found {
			out = append(out, ovCopy)
		}
	}
	return out
}

func clonePRContractDecl(p PRContractDecl) PRContractDecl {
	p.RequiredSections = slices.Clone(p.RequiredSections)
	return p
}

func diffV4PacksAndSteps(ref, cand FlowSpec) []string {
	var changed []string
	refPacks := make(map[string]ReadabilityPackDecl, len(ref.ReadabilityPacks))
	for _, p := range ref.ReadabilityPacks {
		refPacks[p.ID] = p
	}
	candPacks := make(map[string]ReadabilityPackDecl, len(cand.ReadabilityPacks))
	for _, p := range cand.ReadabilityPacks {
		candPacks[p.ID] = p
	}
	for _, p := range cand.ReadabilityPacks {
		rp, ok := refPacks[p.ID]
		if !ok || rp.Enabled != p.Enabled || rp.Activation != p.Activation || rp.CustomText != p.CustomText || !slices.Equal(rp.Globs, p.Globs) {
			changed = append(changed, "pack:"+p.ID)
		}
	}
	for _, p := range ref.ReadabilityPacks {
		if _, ok := candPacks[p.ID]; !ok {
			changed = append(changed, "pack:"+p.ID)
		}
	}

	refSteps := make(map[string]SkillStepDecl, len(ref.SkillSteps))
	for _, s := range ref.SkillSteps {
		refSteps[s.ID] = s
	}
	candSteps := make(map[string]SkillStepDecl, len(cand.SkillSteps))
	for _, s := range cand.SkillSteps {
		candSteps[s.ID] = s
	}
	for _, s := range cand.SkillSteps {
		rs, ok := refSteps[s.ID]
		if !ok || rs.Enabled != s.Enabled || rs.Stage != s.Stage || rs.Text != s.Text || !slices.Equal(rs.Globs, s.Globs) {
			changed = append(changed, "step:"+s.ID)
		}
	}
	for _, s := range ref.SkillSteps {
		if _, ok := candSteps[s.ID]; !ok {
			changed = append(changed, "step:"+s.ID)
		}
	}
	return changed
}

var jsonFlowCamelToYAMLKey = map[string]string{
	"readabilityPacks":      "readability-packs",
	"skillSteps":            "skill-steps",
	"prContract":            "pr-contract",
	"customChecks":          "custom-checks",
	"stopNudge":             "stop-nudge",
	"stagingIsolation":      "staging-isolation",
	"maxBytes":              "max-bytes",
	"maxRepairRounds":       "max-repair-rounds",
	"maxSeconds":            "max-seconds",
	"maxTokens":             "max-tokens",
	"maxBounces":            "max-bounces",
	"planCritic":            "plan-critic",
	"s0Intent":              "s0-intent",
	"s1Risk":                "s1-risk",
	"s2PlanCritic":          "s2-plan-critic",
	"s5MergeTriage":         "s5-merge-triage",
	"onClarification":       "on-clarification",
	"minTier":               "min-tier",
	"insertAfter":           "insert-after",
	"whenPaths":             "when-paths",
	"timeoutSeconds":        "timeout-seconds",
	"checkIds":              "check-ids",
	"skillRef":              "skill-ref",
	"autoMergeTiers":        "auto-merge-tiers",
	"requireHumanTiers":     "require-human-tiers",
	"maxSubtasks":           "max-subtasks",
	"targetDiffLines":       "target-diff-lines",
	"includeCallers":        "include-callers",
	"includeMemory":         "include-memory",
	"extraPaths":            "extra-paths",
	"customText":            "custom-text",
	"maxSubjectChars":       "max-subject-chars",
	"requiredSections":      "required-sections",
	"auditLintSuppressions": "audit-lint-suppressions",
	"guidanceText":          "guidance-text",
	"forbidAdded":           "forbid-added",
	"requireAdded":          "require-added",
	"maxChangedLines":       "max-changed-lines",
	"maxFiles":              "max-files",
	"whenChanged":           "when-changed",
	"requireChanged":        "require-changed",
	"allowWaiver":           "allow-waiver",
	"schemaJson":            "schema-json",
	"addedLines":            "added-lines",
	"changedFiles":          "changed-files",
}

func normalizeJSONFlowDeclarationBytes(trimmed []byte) []byte {
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return trimmed
	}
	var rawMap map[string]any
	if err := json.Unmarshal(trimmed, &rawMap); err != nil {
		return trimmed
	}
	normalized := normalizeJSONFlowValue(rawMap, "")
	out, err := json.Marshal(normalized)
	if err != nil {
		return trimmed
	}
	return out
}

func normalizeJSONFlowValue(v any, parentKey string) any {
	if parentKey == "params" || parentKey == "schema-json" || parentKey == "schemaJson" {
		return v
	}
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			if parentKey == "" && (k == "version" || k == "declarationV3" || k == "declarationV4") {
				continue
			}
			mapped := k
			if parentKey != "checks" {
				if yamlKey, ok := jsonFlowCamelToYAMLKey[k]; ok {
					mapped = yamlKey
				}
			}
			out[mapped] = normalizeJSONFlowValue(item, mapped)
		}
		if parentKey == "" {
			if s, _ := out["schema"].(string); strings.TrimSpace(s) == "" {
				ext, _ := out["extends"].(string)
				if _, hasDecomp := out["decompose"]; hasDecomp ||
					out["context"] != nil ||
					out["readability-packs"] != nil ||
					out["skill-steps"] != nil ||
					out["pr-contract"] != nil ||
					strings.HasPrefix(ext, "golden-v4") ||
					strings.HasPrefix(ext, "engineering-workflow-v4") {
					out["schema"] = FlowSchemaV4
				} else if out["skills"] != nil ||
					out["stages"] != nil ||
					out["custom-checks"] != nil ||
					strings.HasPrefix(ext, "golden-v3") {
					out["schema"] = FlowSchemaV3
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = normalizeJSONFlowValue(item, parentKey)
		}
		return out
	default:
		return v
	}
}
