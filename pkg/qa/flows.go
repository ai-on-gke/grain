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
	"fmt"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	v1 "github.com/ai-on-gke/grain/pkg/qa/golden/v1"
	"gopkg.in/yaml.v3"
)

// Built-in deterministic pre-PR S3/S3.5 and semantic S4 rule identifiers.
const (
	RuleTestIntegrityGuard    = "golden:test-integrity-guard"
	RuleStrictConfigDecoder   = "golden:strict-config-decoder"
	RuleSiblingExportHazard   = "golden:sibling-export-hazard"
	RuleUIBudgetAndSpecFloor  = "golden:ui-budget-and-spec-floor"
	RuleExportedSurfaceDelta  = "golden:exported-surface-delta"
	RuleTestHygiene           = "golden:test-hygiene"
	RuleMutationHardening     = "golden:mutation-hardening"
	RuleBoundaryFuzz          = "golden:boundary-fuzz"
	RulePRDescriptionContract = "golden:pr-description-contract"
	RuleS4OrthogonalVerifier  = "golden:s4-orthogonal-verifier"
)

// RepoFlowCandidatePaths lists the in-tree YAML paths consulted when flowId is "repo-custom".
var RepoFlowCandidatePaths = []string{
	".grain/qa-flow.yml",
	".grain/qa/flow.yml",
}

// RuleSpec describes one deterministic (S3/S3.5) or fresh-context semantic (S4) rule in a QA flow.
type RuleSpec struct {
	ID          string         `json:"id" yaml:"id"`
	Label       string         `json:"label" yaml:"label"`
	Stage       string         `json:"stage" yaml:"stage"`
	Severity    string         `json:"severity" yaml:"severity"`
	Summary     string         `json:"summary" yaml:"summary"`
	TargetPaths []string       `json:"targetPaths" yaml:"target-paths"`
	Enabled     bool           `json:"enabled" yaml:"enabled"`
	Params      map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
}

// FlowSpec describes a quality control flow package executable by the Grain QA engine.
type FlowSpec struct {
	ID                  string                `json:"id" yaml:"id"`
	SchemaVer           string                `json:"schemaVer,omitempty" yaml:"-"`
	Label               string                `json:"label" yaml:"label"`
	Summary             string                `json:"summary" yaml:"summary"`
	InjectSkill         bool                  `json:"injectSkill" yaml:"inject-skill"`
	SkillText           string                `json:"skillText,omitempty" yaml:"-"`
	IntentSkillText     string                `json:"intentSkillText,omitempty" yaml:"-"`
	PlanCriticSkillText string                `json:"planCriticSkillText,omitempty" yaml:"-"`
	VerifierSkillText   string                `json:"verifierSkillText,omitempty" yaml:"-"`
	SecuritySkillText   string                `json:"securitySkillText,omitempty" yaml:"-"`
	SkillBytes          int                   `json:"skillBytes" yaml:"-"`
	StopNudge           bool                  `json:"stopNudge" yaml:"stop-nudge"`
	StagingIsolation    bool                  `json:"stagingIsolation" yaml:"staging-isolation"`
	EnableS4            bool                  `json:"enableS4" yaml:"enable-s4"`
	VerifierTiers       []string              `json:"verifierTiers,omitempty" yaml:"verifier-tiers,omitempty"`
	MaxS4RepairRounds   int                   `json:"maxS4RepairRounds" yaml:"max-s4-repair-rounds"`
	MaxS4Seconds        int                   `json:"maxS4Seconds,omitempty" yaml:"max-s4-seconds,omitempty"`
	MaxS4Tokens         int                   `json:"maxS4Tokens,omitempty" yaml:"max-s4-tokens,omitempty"`
	VerifierChecklist   string                `json:"verifierChecklist,omitempty" yaml:"verifier-checklist,omitempty"`
	StrictDecoderPaths  []string              `json:"strictDecoderPaths,omitempty" yaml:"strict-decoder-paths,omitempty"`
	Rules               []RuleSpec            `json:"rules" yaml:"rules"`
	S0Intent            bool                  `json:"s0Intent,omitempty" yaml:"s0-intent,omitempty"`
	S0IntentConfig      S0IntentDecl          `json:"s0IntentConfig,omitempty" yaml:"-"`
	S2PlanCritic        bool                  `json:"s2PlanCritic,omitempty" yaml:"s2-plan-critic,omitempty"`
	S2PlanCriticConfig  S2PlanCriticDecl      `json:"s2PlanCriticConfig,omitempty" yaml:"-"`
	RiskOverrides       []RiskOverride        `json:"riskOverrides,omitempty" yaml:"risk-overrides,omitempty"`
	CustomStages        []CustomStageDecl     `json:"customStages,omitempty" yaml:"custom-stages,omitempty"`
	S5MergeTriage       TriageConfig          `json:"s5MergeTriage,omitempty" yaml:"s5-merge-triage,omitempty"`
	CustomChecks        []CustomCheckDecl     `json:"customChecks,omitempty" yaml:"custom-checks,omitempty"`
	Decompose           DecomposeDecl         `json:"decompose,omitempty" yaml:"-"`
	Context             ContextDiscoveryDecl  `json:"context,omitempty" yaml:"-"`
	ReadabilityPacks    []ReadabilityPackDecl `json:"readabilityPacks,omitempty" yaml:"-"`
	SkillSteps          []SkillStepDecl       `json:"skillSteps,omitempty" yaml:"-"`
	PRContract          PRContractDecl        `json:"prContract,omitempty" yaml:"-"`
}

type CustomStageDecl struct {
	ID             string   `json:"id" yaml:"id"`
	Label          string   `json:"label,omitempty" yaml:"label,omitempty"`
	InsertAfter    string   `json:"insertAfter" yaml:"insert-after"`
	Kind           string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	WhenPaths      []string `json:"whenPaths,omitempty" yaml:"when-paths,omitempty"`
	MinTier        string   `json:"minTier,omitempty" yaml:"min-tier,omitempty"`
	Timeout        int      `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty" yaml:"timeout-seconds,omitempty"`
	CheckIDs       []string `json:"checkIds,omitempty" yaml:"check-ids,omitempty"`
	SkillRef       string   `json:"skillRef,omitempty" yaml:"skill-ref,omitempty"`
}

type TriageConfig struct {
	Mode              string   `json:"mode" yaml:"mode"`
	AutoMergeTiers    []string `json:"auto-merge-tiers,omitempty" yaml:"auto-merge-tiers,omitempty"`
	RequireHumanTiers []string `json:"require-human-tiers,omitempty" yaml:"require-human-tiers,omitempty"`
}

// SkillPrompt returns the coding-time skill text when InjectSkill is true.
func (f FlowSpec) SkillPrompt() string {
	if !f.InjectSkill {
		return ""
	}
	if strings.TrimSpace(f.SkillText) != "" {
		return strings.TrimSpace(f.SkillText)
	}
	return strings.TrimSpace(v1.SkillMarkdown)
}

// Rule looks up ruleID in f.Rules.
func (f FlowSpec) Rule(ruleID string) (RuleSpec, bool) {
	for _, r := range f.Rules {
		if r.ID == ruleID {
			return r, true
		}
	}
	return RuleSpec{}, false
}

// RequiresS4ForTier reports whether S4 semantic verification applies to tier under f.
func (f FlowSpec) RequiresS4ForTier(tier BlastTier) bool {
	if !f.EnableS4 {
		return false
	}
	if len(f.VerifierTiers) == 0 {
		return tier.RequiresS4()
	}
	return slices.Contains(f.VerifierTiers, string(tier))
}

// HasRule reports whether ruleID is enabled in f.
func (f FlowSpec) HasRule(ruleID string) bool {
	for _, r := range f.Rules {
		if r.ID == ruleID {
			return r.Enabled
		}
	}
	return false
}

func defaultS3Rules() []RuleSpec {
	return []RuleSpec{
		{
			ID:          RuleTestIntegrityGuard,
			Label:       "Test integrity & non-tautological assertion guard",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Blocks net test file deletion, unannotated skips or focused tests, net test-case drops alongside production edits, and newly added tests with zero failure or assertion calls across recursive subtests and helpers.",
			TargetPaths: []string{"**/*_test.go", "**/*.spec.ts", "**/*.spec.js"},
			Enabled:     true,
		},
		{
			ID:          RuleStrictConfigDecoder,
			Label:       "Strict YAML/JSON configuration decoder",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Requires KnownFields(true) or DisallowUnknownFields() on YAML/JSON config and declaration decoders so misspelled or unknown keys never drop silently.",
			TargetPaths: []string{"pkg/**/*.go", "internal/**/*.go", "cmd/**/*.go"},
			Enabled:     true,
		},
		{
			ID:          RuleSiblingExportHazard,
			Label:       "Sibling-package caller sweep hazard check",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Detects modified exported Go/TypeScript signatures and struct fields referenced by untouched sibling packages, surfacing exact file:line call sites once before PR creation unless updated or explained via caller_notes.",
			TargetPaths: []string{"**/*.go", "**/*.ts"},
			Enabled:     true,
		},
		{
			ID:          RuleUIBudgetAndSpecFloor,
			Label:       "UI style ratchet, lazy-route & spec floor",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Enforces angular.json component style budgets via a non-regressing size ratchet, blocks new eager feature imports in app.component.ts, and requires unit spec files for newly added Angular components.",
			TargetPaths: []string{"ui/**", "ui-ng/**"},
			Enabled:     true,
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
}

func s4Rule(enabled bool) RuleSpec {
	return RuleSpec{
		ID:          RuleS4OrthogonalVerifier,
		Label:       "Fresh-context orthogonal diff verifier + 6-filter Inquisitor",
		Stage:       "S4",
		Severity:    "blocking",
		Summary:     "Runs at most 1 fresh-context read-only verification pass on T2/T3 cross-package, schema, UI, or trust-boundary diffs with zero author CoT, filtering findings through deterministic diff-hunk grounding and 6 anti-theater rules.",
		TargetPaths: []string{"T2/T3 high-blast diffs"},
		Enabled:     enabled,
	}
}

// BuiltInFlows returns the 3 canonical quality control flow packages shipped with Grain.
func BuiltInFlows() []FlowSpec {
	versions := BuiltInFlowVersions()
	out := make([]FlowSpec, 0, len(versions))
	for _, v := range versions {
		spec := v.Resolved
		spec.ID = v.Key
		out = append(out, spec)
	}
	return out
}

func legacyV1Flows() []FlowSpec {
	skillLen := len(strings.TrimSpace(v1.SkillMarkdown))
	goldenRules := append(defaultS3Rules(), s4Rule(true))
	s3OnlyRules := append(defaultS3Rules(), s4Rule(false))
	customRules := append(defaultS3Rules(), s4Rule(true))

	return []FlowSpec{
		{
			ID:                model.QAFlowGoldenV1Minimal,
			Label:             "golden-v1-minimal (Recommended)",
			Summary:           "270-token coding contract, staging-ref isolation, 4 sub-second deterministic AST/diff gates (S3), and 1-round fresh-context T2/T3 verifier (S4).",
			InjectSkill:       true,
			SkillBytes:        skillLen,
			StopNudge:         true,
			StagingIsolation:  true,
			EnableS4:          true,
			MaxS4RepairRounds: 1,
			Rules:             goldenRules,
		},
		{
			ID:                model.QAFlowDeterministicS3Only,
			Label:             "deterministic-s3-only (Zero-LLM Ablation)",
			Summary:           "Runs only the 4 pure-Go deterministic S3 AST/diff checks and staging-ref isolation with zero prompt injection and zero LLM verifier tokens.",
			InjectSkill:       false,
			SkillBytes:        0,
			StopNudge:         true,
			StagingIsolation:  true,
			EnableS4:          false,
			MaxS4RepairRounds: 0,
			Rules:             s3OnlyRules,
		},
		{
			ID:                model.QAFlowRepoCustom,
			Label:             "repo-custom (.grain/qa-flow.yml)",
			Summary:           "Applies tighten-only repository overrides from .grain/qa-flow.yml on top of golden-v1-minimal without weakening trust-boundary floors.",
			InjectSkill:       true,
			SkillBytes:        skillLen,
			StopNudge:         true,
			StagingIsolation:  true,
			EnableS4:          true,
			MaxS4RepairRounds: 1,
			Rules:             customRules,
		},
	}
}

// LookupFlow returns the FlowSpec for id, falling back to DefaultQAFlowID when id is empty.
func LookupFlow(id string) (FlowSpec, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = model.DefaultQAFlowID
	}
	for _, f := range BuiltInFlows() {
		if f.ID == id {
			return f, true
		}
	}
	for _, f := range legacyV1Flows() {
		if f.ID == id {
			return f, true
		}
	}
	if v, ok := LookupBuiltInOrRegisteredVersion(id); ok {
		return v.Resolved, true
	}
	return FlowSpec{}, false
}

// repoFlowOverlay is the strict YAML schema accepted in .grain/qa-flow.yml.
// Overlay rules are tighten-only on mandatory S3 integrity gates and T3 trust boundaries.
type repoFlowOverlay struct {
	Base               string   `yaml:"base"`
	InjectSkill        *bool    `yaml:"inject-skill"`
	EnableS4           *bool    `yaml:"enable-s4"`
	StrictDecoderPaths []string `yaml:"strict-decoder-paths"`
	DisableRules       []string `yaml:"disable-rules"`
}

// ParseRepoFlowYAML parses a repository .grain/qa-flow.yml overlay strictly (KnownFields(true)),
// rejecting unknown keys and refusing to disable core test-integrity or strict-decoder floors.
func ParseRepoFlowYAML(raw []byte) (FlowSpec, error) {
	base, _ := LookupFlow(model.QAFlowGoldenV1Minimal)
	base.ID = model.QAFlowRepoCustom
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return base, nil
	}

	var overlay repoFlowOverlay
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	dec.KnownFields(true)
	if err := dec.Decode(&overlay); err != nil {
		return FlowSpec{}, fmt.Errorf("qa: parsing .grain/qa-flow.yml: %w", err)
	}
	if b := strings.TrimSpace(overlay.Base); b != "" && b != model.QAFlowGoldenV1Minimal {
		return FlowSpec{}, fmt.Errorf("qa: unsupported or weaker base flow %q in .grain/qa-flow.yml (only %q allowed)", b, model.QAFlowGoldenV1Minimal)
	}
	if overlay.InjectSkill != nil {
		if !*overlay.InjectSkill {
			return FlowSpec{}, fmt.Errorf("qa: inject-skill cannot be disabled in .grain/qa-flow.yml")
		}
		base.InjectSkill = *overlay.InjectSkill
	}
	if overlay.EnableS4 != nil {
		if !*overlay.EnableS4 {
			return FlowSpec{}, fmt.Errorf("qa: enable-s4 cannot be disabled in .grain/qa-flow.yml (tighten-only floor)")
		}
		base.EnableS4 = true
		if base.MaxS4RepairRounds < 1 {
			base.MaxS4RepairRounds = 1
		}
	}
	for _, p := range overlay.StrictDecoderPaths {
		p = strings.TrimSpace(p)
		if p != "" && !slices.Contains(base.StrictDecoderPaths, p) {
			base.StrictDecoderPaths = append(base.StrictDecoderPaths, p)
		}
	}
	for _, dis := range overlay.DisableRules {
		dis = strings.TrimSpace(dis)
		if dis == RuleTestIntegrityGuard {
			return FlowSpec{}, fmt.Errorf("qa: rule %q is a mandatory floor and cannot be disabled in .grain/qa-flow.yml", dis)
		}
		for i := range base.Rules {
			if base.Rules[i].ID == dis {
				base.Rules[i].Enabled = false
			}
		}
	}
	return base, nil
}
