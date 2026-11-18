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
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
	v1 "github.com/ai-on-gke/grain/pkg/qa/golden/v1"
	"gopkg.in/yaml.v3"
)

// FlowSchemaV2 identifies the v2 declarative QA flow package format.
const FlowSchemaV2 = "grain.dev/qa-flow/v2"

// Built-in semantic verifier checklist identifiers.
const ChecklistEscapeClassesV1 = "escape-classes-v1"

// Max caps enforced at flow validation and publish time.
const (
	MaxSkillBytesCap      = 4096
	MaxVerifierSecondsCap = 600
	MaxVerifierTokensCap  = 200000
)

// EscapeClassesV1ChecklistText is the built-in S4 checklist targeting empirically observed escape classes.
const EscapeClassesV1ChecklistText = `Escape-class verification checklist (escape-classes-v1):
1. State-machine transitions: verify every state transition updates persisted status, timestamps, and guards atomically without unreachable or stuck states.
2. Invariants and bijections: verify encode/decode, serialize/parse, and forward/reverse mappings preserve round-trip identity on edge inputs.
3. Arithmetic & slice boundaries: check integer overflow, uint64 wraparound, negative/zero lengths, and off-by-one slice bounds on sizes and offsets.
4. Nil, zero, and omitted config values: distinguish omitted optional fields from explicit zero values and verify unknown config fields are rejected.
5. Context cancellation & cleanup: verify goroutines, timers, file handles, and locks release cleanly on early error and context cancellation.`

// SkillDecl configures the coding-time skill prompt in a FlowDeclarationV2.
type SkillDecl struct {
	Mode     string `json:"mode" yaml:"mode"`
	Text     string `json:"text" yaml:"text"`
	MaxBytes int    `json:"maxBytes" yaml:"max-bytes"`
}

// CheckDecl configures one built-in check inside FlowDeclarationV2.Checks.
type CheckDecl struct {
	Enabled  *bool          `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Severity string         `json:"severity,omitempty" yaml:"severity,omitempty"`
	Params   map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
}

// VerifierDecl configures the fresh-context S4 semantic verifier in FlowDeclarationV2.
type VerifierDecl struct {
	Enabled         *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Tiers           []string `json:"tiers,omitempty" yaml:"tiers,omitempty"`
	MaxRepairRounds *int     `json:"maxRepairRounds,omitempty" yaml:"max-repair-rounds,omitempty"`
	MaxSeconds      *int     `json:"maxSeconds,omitempty" yaml:"max-seconds,omitempty"`
	MaxTokens       *int     `json:"maxTokens,omitempty" yaml:"max-tokens,omitempty"`
	Checklist       string   `json:"checklist,omitempty" yaml:"checklist,omitempty"`
}

// FlowDeclarationV2 is the declarative grain.dev/qa-flow/v2 specification.
type FlowDeclarationV2 struct {
	Schema           string               `json:"schema" yaml:"schema"`
	Key              string               `json:"key" yaml:"key"`
	Extends          string               `json:"extends,omitempty" yaml:"extends,omitempty"`
	Label            string               `json:"label,omitempty" yaml:"label,omitempty"`
	Summary          string               `json:"summary,omitempty" yaml:"summary,omitempty"`
	Skill            SkillDecl            `json:"skill" yaml:"skill"`
	StopNudge        *bool                `json:"stopNudge,omitempty" yaml:"stop-nudge,omitempty"`
	StagingIsolation *bool                `json:"stagingIsolation,omitempty" yaml:"staging-isolation,omitempty"`
	Checks           map[string]CheckDecl `json:"checks,omitempty" yaml:"checks,omitempty"`
	Verifier         VerifierDecl         `json:"verifier" yaml:"verifier"`
}

type FlowDeclarationV3 struct {
	Schema       string               `json:"schema" yaml:"schema"`
	Key          string               `json:"key" yaml:"key"`
	Extends      string               `json:"extends,omitempty" yaml:"extends,omitempty"`
	Label        string               `json:"label,omitempty" yaml:"label,omitempty"`
	Summary      string               `json:"summary,omitempty" yaml:"summary,omitempty"`
	Skills       V3SkillsDecl         `json:"skills,omitempty" yaml:"skills,omitempty"`
	Stages       V3StagesDecl         `json:"stages,omitempty" yaml:"stages,omitempty"`
	Checks       map[string]CheckDecl `json:"checks,omitempty" yaml:"checks,omitempty"`
	CustomChecks []CustomCheckDecl    `json:"custom-checks,omitempty" yaml:"custom-checks,omitempty"`
	Verifier     VerifierDecl         `json:"verifier,omitempty" yaml:"verifier,omitempty"`
}

type V3SkillsDecl struct {
	Coding     SkillDecl `json:"coding,omitempty" yaml:"coding,omitempty"`
	Intent     SkillDecl `json:"intent,omitempty" yaml:"intent,omitempty"`
	PlanCritic SkillDecl `json:"plan-critic,omitempty" yaml:"plan-critic,omitempty"`
	Verifier   SkillDecl `json:"verifier,omitempty" yaml:"verifier,omitempty"`
	Security   SkillDecl `json:"security,omitempty" yaml:"security,omitempty"`
}

type V3StagesDecl struct {
	S0Intent      S0IntentDecl      `json:"s0-intent,omitempty" yaml:"s0-intent,omitempty"`
	S1Risk        S1RiskDecl        `json:"s1-risk,omitempty" yaml:"s1-risk,omitempty"`
	S2PlanCritic  S2PlanCriticDecl  `json:"s2-plan-critic,omitempty" yaml:"s2-plan-critic,omitempty"`
	Custom        []CustomStageDecl `json:"custom,omitempty" yaml:"custom,omitempty"`
	S5MergeTriage TriageConfig      `json:"s5-merge-triage,omitempty" yaml:"s5-merge-triage,omitempty"`
}

type S0IntentDecl struct {
	Enabled         bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Complexities    []string `json:"complexities,omitempty" yaml:"complexities,omitempty"`
	MaxSeconds      int      `json:"max-seconds,omitempty" yaml:"max-seconds,omitempty"`
	MaxTokens       int      `json:"max-tokens,omitempty" yaml:"max-tokens,omitempty"`
	OnClarification string   `json:"on-clarification,omitempty" yaml:"on-clarification,omitempty"`
}

type S1RiskDecl struct {
	Overrides []RiskOverride `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

type S2PlanCriticDecl struct {
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Complexities []string `json:"complexities,omitempty" yaml:"complexities,omitempty"`
	Tiers        []string `json:"tiers,omitempty" yaml:"tiers,omitempty"`
	MaxSeconds   int      `json:"max-seconds,omitempty" yaml:"max-seconds,omitempty"`
	MaxTokens    int      `json:"max-tokens,omitempty" yaml:"max-tokens,omitempty"`
	MaxBounces   int      `json:"max-bounces,omitempty" yaml:"max-bounces,omitempty"`
}

// FlowVersionBundle pairs a materialized FlowDeclarationV2 with its canonical hash, version metadata, and executable FlowSpec.
type FlowVersionBundle struct {
	Hash          string             `json:"hash"`
	Key           string             `json:"key"`
	Version       int                `json:"version"`
	ParentHash    string             `json:"parentHash,omitempty"`
	Label         string             `json:"label"`
	Summary       string             `json:"summary"`
	BuiltIn       bool               `json:"builtIn"`
	QAEngineRev   int                `json:"qaEngineRev"`
	SchemaVer     string             `json:"schemaVer,omitempty"`
	SourceYAML    string             `json:"sourceYaml,omitempty"`
	Declaration   FlowDeclarationV2  `json:"declaration"`
	DeclarationV3 *FlowDeclarationV3 `json:"declarationV3,omitempty"`
	DeclarationV4 *FlowDeclarationV4 `json:"declarationV4,omitempty"`
	SpecJSON      string             `json:"specJson"`
	Resolved      FlowSpec           `json:"resolved"`
}

const goldenV2YAML = `schema: grain.dev/qa-flow/v2
key: golden-v2
extends: golden-v1-minimal@1
label: golden v2
summary: >
  Semantic verifier on every non-trivial diff with an escape-class checklist, type-aware
  strict decoders, mutation-guided test hardening, boundary fuzzing, and a scope contract.
skill:
  mode: replace
  max-bytes: 1800
  text: |
    Before you open a pull request:
    1. Scope: implement what the task asks. No unrequested subsystems or options.
       List any deliberate extra in the PR description under "Extras".
    2. Callers: for every exported symbol you change, update its callers (found by
       import path) or say why not in caller_notes.
    3. Config: decoders of config or declarations reject unknown keys, keep exact key
       case, reject null where a value is required, use pointers where omitted and zero
       differ, and have a marshal/unmarshal round-trip test.
    4. Tests: each new test fails without your change; no sleeps for ordering; no
       always-true loops; assert outcomes, not just absence of errors.
    5. Boundaries: check overflow, wraparound and huge inputs on sizes and offsets.
    6. Verify on the final HEAD: build, vet and tests pass.
stop-nudge: true
staging-isolation: true
checks:
  golden:test-integrity-guard: {}
  golden:strict-config-decoder: {}
  golden:sibling-export-hazard: {severity: warn, params: {resolve: import-path}}
  golden:ui-budget-and-spec-floor: {enabled: true}
  golden:exported-surface-delta: {severity: warn}
  golden:test-hygiene: {severity: warn}
  golden:mutation-hardening: {severity: warn, params: {mutants: 25, max-seconds: 60}}
  golden:boundary-fuzz: {severity: warn, params: {max-seconds: 15}}
verifier:
  enabled: true
  tiers: [T1, T2, T3]
  max-repair-rounds: 1
  max-seconds: 60
  max-tokens: 50000
  checklist: escape-classes-v1
`

const goldenV2SkillOnlyYAML = `schema: grain.dev/qa-flow/v2
key: golden-v2-skill-only
extends: golden-v2@1
label: golden v2 (skill + floors only)
summary: Ablation flow keeping the golden-v2 coding skill and mandatory trust-boundary floors with non-floor checks and S4 verifier off.
checks:
  golden:test-integrity-guard: {}
  golden:strict-config-decoder: {}
  golden:sibling-export-hazard: {enabled: false}
  golden:ui-budget-and-spec-floor: {enabled: false}
  golden:exported-surface-delta: {enabled: false}
  golden:test-hygiene: {enabled: false}
  golden:mutation-hardening: {enabled: false}
  golden:boundary-fuzz: {enabled: false}
verifier:
  enabled: false
`

const goldenV2ChecksOnlyYAML = `schema: grain.dev/qa-flow/v2
key: golden-v2-checks-only
extends: golden-v2@1
label: golden v2 (checks + verifier only)
summary: Ablation flow running all golden-v2 checks and the S4 semantic verifier with coding skill injection and stop nudge turned off.
skill:
  mode: replace
  max-bytes: 1800
  text: ""
stop-nudge: false
`

var allBuiltInRuleCatalog = []RuleSpec{
	{
		ID:          RuleTestIntegrityGuard,
		Label:       "Test integrity & non-tautological assertion guard",
		Stage:       "S3",
		Severity:    "blocking",
		Summary:     "Blocks net test file deletion, unannotated skips or focused tests, net test-case drops alongside production edits, and newly added tests with zero failure or assertion calls.",
		TargetPaths: []string{"**/*_test.go", "**/*.spec.ts", "**/*.spec.js"},
		Enabled:     true,
	},
	{
		ID:          RuleStrictConfigDecoder,
		Label:       "Type-aware strict YAML/JSON configuration decoder",
		Stage:       "S3",
		Severity:    "blocking",
		Summary:     "Requires KnownFields(true) or DisallowUnknownFields() when decoding YAML/JSON into Go structs while ignoring structural yaml.Node, map, raw message, or interface targets.",
		TargetPaths: []string{"pkg/**/*.go", "internal/**/*.go", "cmd/**/*.go"},
		Enabled:     true,
	},
	{
		ID:          RuleSiblingExportHazard,
		Label:       "Sibling-package caller sweep hazard check",
		Stage:       "S3",
		Severity:    "blocking",
		Summary:     "Detects modified exported Go/TypeScript signatures and struct fields referenced by untouched sibling packages resolved by import path.",
		TargetPaths: []string{"**/*.go", "**/*.ts"},
		Enabled:     false,
	},
	{
		ID:          RuleUIBudgetAndSpecFloor,
		Label:       "UI style ratchet, lazy-route & spec floor",
		Stage:       "S3",
		Severity:    "blocking",
		Summary:     "Enforces angular.json component style budgets via a non-regressing size ratchet, blocks new eager feature imports, and requires unit specs for newly added Angular components.",
		TargetPaths: []string{"ui/**", "ui-ng/**"},
		Enabled:     false,
	},
	{
		ID:          RuleExportedSurfaceDelta,
		Label:       "Exported API surface delta vs task scope",
		Stage:       "S3",
		Severity:    "warn",
		Summary:     "Flags disproportionate additions of new exported types, functions, and methods relative to the requested task scope.",
		TargetPaths: []string{"**/*.go", "**/*.ts"},
		Enabled:     false,
	},
	{
		ID:          RuleTestHygiene,
		Label:       "Test timing & termination hygiene",
		Stage:       "S3",
		Severity:    "warn",
		Summary:     "Flags synchronization sleeps (time.Sleep) in tests and unbounded constant-true loops lacking a cancellation or iteration bound.",
		TargetPaths: []string{"**/*_test.go"},
		Enabled:     false,
	},
	{
		ID:          RuleMutationHardening,
		Label:       "Mutation-guided test hardening on added lines",
		Stage:       "S3.5",
		Severity:    "warn",
		Summary:     "Evaluates bounded single-site mutants on newly added Go production lines inside the task sandbox and reports surviving mutants lacking assertions.",
		TargetPaths: []string{"**/*.go"},
		Enabled:     false,
	},
	{
		ID:          RuleBoundaryFuzz,
		Label:       "Bounded slice/offset fuzz harness",
		Stage:       "S3.5",
		Severity:    "warn",
		Summary:     "Runs a short native Go fuzz pass against changed byte/string/offset functions to catch panics and wraparound bugs before pull request creation.",
		TargetPaths: []string{"**/*.go"},
		Enabled:     false,
	},
	{
		ID:          RulePRDescriptionContract,
		Label:       "PR description & subject-line contract",
		Stage:       "S3",
		Severity:    "warn",
		Summary:     "Enforces PR subject-line length caps, required description sections (Summary, Verification), and flags unannotated lint/type-check suppression directives in added lines.",
		TargetPaths: []string{"**/*"},
		Enabled:     false,
	},
}

var (
	flowRegMu       sync.RWMutex
	registeredFlows []FlowVersionBundle
)

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func normalizeSkillText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ParseFlowDeclarationV2 strictly decodes YAML or JSON into a FlowDeclarationV2 (KnownFields(true)).
func ParseFlowDeclarationV2(raw []byte) (FlowDeclarationV2, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return FlowDeclarationV2{}, fmt.Errorf("qa: empty flow declaration")
	}
	var decl FlowDeclarationV2
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	dec.KnownFields(true)
	if err := dec.Decode(&decl); err != nil {
		return FlowDeclarationV2{}, fmt.Errorf("qa: parsing flow declaration v2: %w", err)
	}
	if decl.Schema != FlowSchemaV2 {
		return FlowDeclarationV2{}, fmt.Errorf("qa: unsupported flow schema %q (want %q)", decl.Schema, FlowSchemaV2)
	}
	if strings.TrimSpace(decl.Key) == "" {
		return FlowDeclarationV2{}, fmt.Errorf("qa: flow key is required")
	}
	return decl, nil
}

func builtInV1Declaration() FlowDeclarationV2 {
	text := normalizeSkillText(v1.SkillMarkdown)
	return FlowDeclarationV2{
		Schema:           FlowSchemaV2,
		Key:              model.QAFlowGoldenV1Minimal,
		Label:            "golden-v1-minimal (Recommended)",
		Summary:          "270-token coding contract, staging-ref isolation, 4 sub-second deterministic AST/diff gates (S3), and 1-round fresh-context T2/T3 verifier (S4).",
		Skill:            SkillDecl{Mode: "replace", Text: text, MaxBytes: 1800},
		StopNudge:        boolPtr(true),
		StagingIsolation: boolPtr(true),
		Checks: map[string]CheckDecl{
			RuleTestIntegrityGuard:   {Enabled: boolPtr(true), Severity: "blocking"},
			RuleStrictConfigDecoder:  {Enabled: boolPtr(true), Severity: "blocking"},
			RuleSiblingExportHazard:  {Enabled: boolPtr(true), Severity: "blocking", Params: map[string]any{"resolve": "import-path"}},
			RuleUIBudgetAndSpecFloor: {Enabled: boolPtr(true), Severity: "blocking"},
			RuleExportedSurfaceDelta: {Enabled: boolPtr(false), Severity: "warn"},
			RuleTestHygiene:          {Enabled: boolPtr(false), Severity: "warn"},
			RuleMutationHardening:    {Enabled: boolPtr(false), Severity: "warn"},
			RuleBoundaryFuzz:         {Enabled: boolPtr(false), Severity: "warn"},
		},
		Verifier: VerifierDecl{
			Enabled:         boolPtr(true),
			Tiers:           []string{"T2", "T3"},
			MaxRepairRounds: intPtr(1),
			MaxSeconds:      intPtr(60),
			MaxTokens:       intPtr(50000),
			Checklist:       ChecklistEscapeClassesV1,
		},
	}
}

// ParentLookupFunc resolves custom parent flows (by hash or key@version) when resolving extends.
type ParentLookupFunc func(ref string) (FlowVersionBundle, bool)

// BuiltInFlowVersions returns the built-in v2 flow bundles plus any packages registered via RegisterFlowPackage.
func BuiltInFlowVersions() []FlowVersionBundle {
	v1Decl := builtInV1Declaration()
	v1Bundle, err := compileBundle(v1Decl, 1, "", "", true, nil)
	if err != nil {
		panic(fmt.Sprintf("qa: compiling built-in golden-v1-minimal@1: %v", err))
	}
	v1Bundle.Hash = fmt.Sprintf("builtin:%s@1#%d", model.QAFlowGoldenV1Minimal, QAEngineRev)

	lookup := map[string]FlowVersionBundle{
		v1Bundle.Hash:                                   v1Bundle,
		model.QAFlowGoldenV1Minimal:                     v1Bundle,
		model.QAFlowGoldenV1Minimal + "@1":              v1Bundle,
		"builtin:" + model.QAFlowGoldenV1Minimal + "@1": v1Bundle,
	}

	out := []FlowVersionBundle{v1Bundle}
	for _, rawYAML := range []string{goldenV2YAML, goldenV2SkillOnlyYAML, goldenV2ChecksOnlyYAML, goldenV3YAML, engineeringWorkflowV4YAML} {
		anyDecl, pErr := ParseFlowDeclaration([]byte(rawYAML))
		if pErr != nil {
			panic(fmt.Sprintf("qa: invalid built-in yaml: %v", pErr))
		}

		var b FlowVersionBundle
		var cErr error
		var key string

		lfunc := func(ref string) (FlowVersionBundle, bool) {
			v, ok := lookup[ref]
			return v, ok
		}

		switch d := anyDecl.(type) {
		case *FlowDeclarationV4:
			key = d.Key
			b, cErr = ResolveFlowDeclarationV4(*d, 1, rawYAML, "", true, lfunc)
		case *FlowDeclarationV3:
			key = d.Key
			b, cErr = ResolveFlowDeclarationV3(*d, 1, rawYAML, "", true, lfunc)
		case *FlowDeclarationV2:
			key = d.Key
			b, cErr = compileBundle(*d, 1, rawYAML, "", true, lfunc)
		}
		if cErr != nil {
			panic(fmt.Sprintf("qa: compiling built-in %s: %v", key, cErr))
		}
		b.Hash = fmt.Sprintf("builtin:%s@1#%d", key, QAEngineRev)
		lookup[b.Hash] = b
		lookup[b.Key] = b
		lookup[fmt.Sprintf("%s@1", b.Key)] = b
		lookup[fmt.Sprintf("builtin:%s@1", b.Key)] = b
		out = append(out, b)
	}

	flowRegMu.RLock()
	extra := slices.Clone(registeredFlows)
	flowRegMu.RUnlock()
	out = append(out, extra...)
	return out
}

// LookupBuiltInOrRegisteredVersion looks up a built-in or registered flow bundle by hash, key@version, or key.
func LookupBuiltInOrRegisteredVersion(ref string) (FlowVersionBundle, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return FlowVersionBundle{}, false
	}
	if ref == "golden-v4-composable" || ref == "golden-v4-composable@1" {
		ref = "engineering-workflow-v4@1"
	}
	all := BuiltInFlowVersions()
	for _, b := range all {
		if b.Hash == ref || fmt.Sprintf("%s@%d", b.Key, b.Version) == ref || fmt.Sprintf("builtin:%s@%d", b.Key, b.Version) == ref {
			return b, true
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Key == ref {
			return all[i], true
		}
	}
	return FlowVersionBundle{}, false
}

// RegisterFlowPackage validates and registers a compiled v2 flow package (for example from an enterprise wrapper module).
func RegisterFlowPackage(decl FlowDeclarationV2) error {
	bundle, err := ResolveFlowDeclarationV2(decl, 1, "", nil)
	if err != nil {
		return err
	}
	bundle.BuiltIn = true
	flowRegMu.Lock()
	defer flowRegMu.Unlock()
	for i, existing := range registeredFlows {
		if existing.Key == bundle.Key && existing.Version == bundle.Version {
			registeredFlows[i] = bundle
			return nil
		}
	}
	registeredFlows = append(registeredFlows, bundle)
	return nil
}

// ResolveFlowDeclarationV2 resolves extends, enforces floors/caps, materializes defaults, and computes the canonical SHA-256 hash.
func ResolveFlowDeclarationV2(decl FlowDeclarationV2, version int, sourceYAML string, extraLookup ParentLookupFunc) (FlowVersionBundle, error) {
	return compileBundle(decl, version, sourceYAML, "", false, func(ref string) (FlowVersionBundle, bool) {
		if b, ok := LookupBuiltInOrRegisteredVersion(ref); ok {
			return b, true
		}
		if extraLookup != nil {
			return extraLookup(ref)
		}
		return FlowVersionBundle{}, false
	})
}

var allowedCheckParams = map[string][]string{
	RuleSiblingExportHazard:   {"resolve"},
	RuleMutationHardening:     {"mutants", "max-seconds"},
	RuleBoundaryFuzz:          {"max-seconds"},
	RulePRDescriptionContract: {"max-subject-chars", "required-sections", "audit-lint-suppressions"},
}

func compileBundle(decl FlowDeclarationV2, version int, sourceYAML string, explicitParentHash string, builtIn bool, lookup ParentLookupFunc) (FlowVersionBundle, error) {
	if decl.Schema == "" {
		decl.Schema = FlowSchemaV2
	}
	if decl.Schema != FlowSchemaV2 {
		return FlowVersionBundle{}, fmt.Errorf("qa: unsupported schema %q", decl.Schema)
	}
	key := strings.TrimSpace(decl.Key)
	if key == "" {
		return FlowVersionBundle{}, fmt.Errorf("qa: key is required")
	}

	base := builtInV1Declaration()
	parentHash := explicitParentHash
	if ext := strings.TrimSpace(decl.Extends); ext != "" {
		if lookup == nil {
			return FlowVersionBundle{}, fmt.Errorf("qa: unknown parent flow %q", ext)
		}
		parent, ok := lookup(ext)
		if !ok {
			return FlowVersionBundle{}, fmt.Errorf("qa: unknown parent flow %q", ext)
		}
		base = parent.Declaration
		if parentHash == "" {
			parentHash = parent.Hash
		}
	}

	mat := FlowDeclarationV2{
		Schema:           FlowSchemaV2,
		Key:              key,
		Extends:          strings.TrimSpace(decl.Extends),
		Label:            strings.TrimSpace(decl.Label),
		Summary:          strings.TrimSpace(decl.Summary),
		StopNudge:        base.StopNudge,
		StagingIsolation: base.StagingIsolation,
		Checks:           make(map[string]CheckDecl, len(allBuiltInRuleCatalog)),
		Verifier: VerifierDecl{
			Enabled:         base.Verifier.Enabled,
			Tiers:           slices.Clone(base.Verifier.Tiers),
			MaxRepairRounds: base.Verifier.MaxRepairRounds,
			MaxSeconds:      base.Verifier.MaxSeconds,
			MaxTokens:       base.Verifier.MaxTokens,
			Checklist:       base.Verifier.Checklist,
		},
	}
	if mat.Label == "" {
		mat.Label = key
	}
	if mat.Summary == "" {
		mat.Summary = base.Summary
	}
	if decl.StopNudge != nil {
		mat.StopNudge = boolPtr(*decl.StopNudge)
	}
	if decl.StagingIsolation != nil {
		mat.StagingIsolation = boolPtr(*decl.StagingIsolation)
	}

	// Skill resolution (inherit | append | replace).
	mode := strings.TrimSpace(decl.Skill.Mode)
	if mode == "" {
		mode = "inherit"
	}
	maxBytes := decl.Skill.MaxBytes
	if maxBytes <= 0 {
		maxBytes = base.Skill.MaxBytes
	}
	if maxBytes <= 0 {
		maxBytes = 1800
	}
	if maxBytes > MaxSkillBytesCap {
		return FlowVersionBundle{}, fmt.Errorf("qa: skill.max-bytes %d exceeds maximum %d", maxBytes, MaxSkillBytesCap)
	}
	var resolvedSkill string
	switch mode {
	case "inherit":
		resolvedSkill = base.Skill.Text
	case "append":
		extra := normalizeSkillText(decl.Skill.Text)
		if base.Skill.Text != "" && extra != "" {
			resolvedSkill = base.Skill.Text + "\n\n" + extra
		} else if extra != "" {
			resolvedSkill = extra
		} else {
			resolvedSkill = base.Skill.Text
		}
	case "replace":
		resolvedSkill = normalizeSkillText(decl.Skill.Text)
	default:
		return FlowVersionBundle{}, fmt.Errorf("qa: invalid skill.mode %q (want inherit, append, or replace)", mode)
	}
	if len(resolvedSkill) > maxBytes {
		return FlowVersionBundle{}, fmt.Errorf("qa: resolved skill is %d bytes, exceeding skill.max-bytes %d", len(resolvedSkill), maxBytes)
	}
	mat.Skill = SkillDecl{
		Mode:     "replace",
		Text:     resolvedSkill,
		MaxBytes: maxBytes,
	}

	// Checks resolution + strict validation.
	knownRules := make(map[string]RuleSpec, len(allBuiltInRuleCatalog))
	for _, r := range allBuiltInRuleCatalog {
		knownRules[r.ID] = r
		bc, inBase := base.Checks[r.ID]
		_, inDecl := decl.Checks[r.ID]
		if r.ID == RulePRDescriptionContract && !inBase && !inDecl {
			continue
		}
		en := r.Enabled
		if bc.Enabled != nil {
			en = *bc.Enabled
		}
		sev := bc.Severity
		if sev == "" {
			sev = r.Severity
		}
		var params map[string]any
		if len(bc.Params) > 0 {
			params = cloneParams(bc.Params)
		}
		mat.Checks[r.ID] = CheckDecl{Enabled: boolPtr(en), Severity: sev, Params: params}
	}

	for ruleID, override := range decl.Checks {
		if _, ok := knownRules[ruleID]; !ok {
			return FlowVersionBundle{}, fmt.Errorf("qa: unknown check id %q", ruleID)
		}
		cur := mat.Checks[ruleID]
		isFloor := ruleID == RuleTestIntegrityGuard || ruleID == RuleStrictConfigDecoder
		if override.Enabled != nil {
			if isFloor && !*override.Enabled {
				return FlowVersionBundle{}, fmt.Errorf("qa: floor check %q cannot be disabled", ruleID)
			}
			cur.Enabled = boolPtr(*override.Enabled)
		} else {
			// Mentioning a check block in YAML (`golden:exported-surface-delta: {severity: warn}`) enables it by default.
			cur.Enabled = boolPtr(true)
		}
		if override.Severity != "" {
			if override.Severity != "blocking" && override.Severity != "warn" && override.Severity != "shadow" {
				return FlowVersionBundle{}, fmt.Errorf("qa: invalid severity %q on %q", override.Severity, ruleID)
			}
			if isFloor && override.Severity != "blocking" {
				return FlowVersionBundle{}, fmt.Errorf("qa: floor check %q cannot be downgraded from blocking to %q", ruleID, override.Severity)
			}
			cur.Severity = override.Severity
		}
		if len(override.Params) > 0 {
			allowed := allowedCheckParams[ruleID]
			if cur.Params == nil {
				cur.Params = make(map[string]any, len(override.Params))
			}
			for pk, pv := range override.Params {
				if !slices.Contains(allowed, pk) {
					return FlowVersionBundle{}, fmt.Errorf("qa: unknown param %q for check %q", pk, ruleID)
				}
				cur.Params[pk] = pv
			}
		}
		mat.Checks[ruleID] = cur
	}

	// Enforce mandatory floors regardless of inheritance.
	for _, floorID := range []string{RuleTestIntegrityGuard, RuleStrictConfigDecoder} {
		fc := mat.Checks[floorID]
		if fc.Enabled == nil || !*fc.Enabled || fc.Severity != "blocking" {
			return FlowVersionBundle{}, fmt.Errorf("qa: mandatory floor %q must remain enabled and blocking", floorID)
		}
	}

	// Staging isolation cannot be disabled when any blocking check is enabled.
	hasBlocking := false
	for _, c := range mat.Checks {
		if c.Enabled != nil && *c.Enabled && c.Severity == "blocking" {
			hasBlocking = true
			break
		}
	}
	if (mat.StagingIsolation == nil || !*mat.StagingIsolation) && hasBlocking {
		return FlowVersionBundle{}, fmt.Errorf("qa: staging-isolation cannot be false when blocking checks are enabled")
	}

	// Verifier resolution & caps.
	if decl.Verifier.Enabled != nil {
		mat.Verifier.Enabled = boolPtr(*decl.Verifier.Enabled)
	}
	if len(decl.Verifier.Tiers) > 0 {
		for _, t := range decl.Verifier.Tiers {
			if t != string(BlastT0) && t != string(BlastT1) && t != string(BlastT2) && t != string(BlastT3) {
				return FlowVersionBundle{}, fmt.Errorf("qa: invalid verifier blast tier %q", t)
			}
		}
		tiers := slices.Clone(decl.Verifier.Tiers)
		sort.Strings(tiers)
		mat.Verifier.Tiers = slices.Compact(tiers)
	}
	if decl.Verifier.MaxRepairRounds != nil {
		if *decl.Verifier.MaxRepairRounds < 0 || *decl.Verifier.MaxRepairRounds > 3 {
			return FlowVersionBundle{}, fmt.Errorf("qa: verifier.max-repair-rounds %d out of range [0,3]", *decl.Verifier.MaxRepairRounds)
		}
		mat.Verifier.MaxRepairRounds = intPtr(*decl.Verifier.MaxRepairRounds)
	}
	if decl.Verifier.MaxSeconds != nil {
		if *decl.Verifier.MaxSeconds <= 0 || *decl.Verifier.MaxSeconds > MaxVerifierSecondsCap {
			return FlowVersionBundle{}, fmt.Errorf("qa: verifier.max-seconds %d exceeds cap %d", *decl.Verifier.MaxSeconds, MaxVerifierSecondsCap)
		}
		mat.Verifier.MaxSeconds = intPtr(*decl.Verifier.MaxSeconds)
	}
	if decl.Verifier.MaxTokens != nil {
		if *decl.Verifier.MaxTokens <= 0 || *decl.Verifier.MaxTokens > MaxVerifierTokensCap {
			return FlowVersionBundle{}, fmt.Errorf("qa: verifier.max-tokens %d exceeds cap %d", *decl.Verifier.MaxTokens, MaxVerifierTokensCap)
		}
		mat.Verifier.MaxTokens = intPtr(*decl.Verifier.MaxTokens)
	}
	if strings.TrimSpace(decl.Verifier.Checklist) != "" {
		if decl.Verifier.Checklist != ChecklistEscapeClassesV1 {
			return FlowVersionBundle{}, fmt.Errorf("qa: unknown verifier checklist %q", decl.Verifier.Checklist)
		}
		mat.Verifier.Checklist = decl.Verifier.Checklist
	}

	specJSONBytes, hash, err := CanonicalFlowJSONAndHash(mat)
	if err != nil {
		return FlowVersionBundle{}, err
	}

	// Build executable FlowSpec.
	rules := make([]RuleSpec, 0, len(allBuiltInRuleCatalog)+1)
	for _, cat := range allBuiltInRuleCatalog {
		r := cat
		if mc, ok := mat.Checks[cat.ID]; ok {
			r.Enabled = mc.Enabled != nil && *mc.Enabled
			if mc.Severity != "" {
				r.Severity = mc.Severity
			}
			if len(mc.Params) > 0 {
				r.Params = cloneParams(mc.Params)
			}
		}
		rules = append(rules, r)
	}
	s4Enabled := mat.Verifier.Enabled != nil && *mat.Verifier.Enabled
	rules = append(rules, s4Rule(s4Enabled))

	flowSpec := FlowSpec{
		ID:                fmt.Sprintf("%s@%d", mat.Key, version),
		Label:             mat.Label,
		Summary:           mat.Summary,
		InjectSkill:       len(resolvedSkill) > 0,
		SkillText:         resolvedSkill,
		SkillBytes:        len(resolvedSkill),
		StopNudge:         mat.StopNudge != nil && *mat.StopNudge,
		StagingIsolation:  mat.StagingIsolation != nil && *mat.StagingIsolation,
		EnableS4:          s4Enabled,
		VerifierTiers:     slices.Clone(mat.Verifier.Tiers),
		MaxS4RepairRounds: derefInt(mat.Verifier.MaxRepairRounds, 1),
		MaxS4Seconds:      derefInt(mat.Verifier.MaxSeconds, 60),
		MaxS4Tokens:       derefInt(mat.Verifier.MaxTokens, 50000),
		VerifierChecklist: mat.Verifier.Checklist,
		Rules:             rules,
	}

	if version <= 0 {
		version = 1
	}
	return FlowVersionBundle{
		Hash:        hash,
		Key:         mat.Key,
		Version:     version,
		ParentHash:  parentHash,
		Label:       mat.Label,
		Summary:     mat.Summary,
		BuiltIn:     builtIn,
		QAEngineRev: QAEngineRev,
		SourceYAML:  sourceYAML,
		Declaration: mat,
		SpecJSON:    string(specJSONBytes),
		Resolved:    flowSpec,
	}, nil
}

// CanonicalFlowJSONAndHash serializes a materialized FlowDeclarationV2 to compact sorted-key JSON and returns its SHA-256 hex digest.
func CanonicalFlowJSONAndHash(mat FlowDeclarationV2) ([]byte, string, error) {
	raw, err := json.Marshal(mat)
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

func cloneParams(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func derefInt(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}
