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

package resolve

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
)

// Resolution describes the effective QA arm and materialized FlowSpec for a task.
type Resolution struct {
	Source       string       `json:"source"`
	ExperimentID string       `json:"experimentId,omitempty"`
	ArmID        string       `json:"armId"`
	FlowID       string       `json:"flowId,omitempty"`
	FlowHash     string       `json:"flowHash,omitempty"`
	QAEngineRev  int          `json:"qaEngineRev,omitempty"`
	Spec         *qa.FlowSpec `json:"spec,omitempty"`
	Shadow       bool         `json:"shadow"`
	ExcludedWhy  string       `json:"excludedWhy,omitempty"`
}

// RegistryResolver resolves an already-enrolled work tree from qa_enrollment (read-only).
var RegistryResolver func(ctx context.Context, store *model.Store, task model.Task, rootOrigin string, qaCfg model.QAConfig) (*Resolution, error)

// RegistryDispatchResolver enrolls or resolves a work tree against a running qa_experiment at dispatch time.
var RegistryDispatchResolver func(ctx context.Context, store *model.Store, task model.Task, rootOrigin string, hasPriorRuns bool, qaCfg model.QAConfig, now time.Time) (*Resolution, error)

func init() {
	model.ActiveQAResolution = func(ctx context.Context, store *model.Store, taskID string) (string, error) {
		res, _, err := ActiveQAResolutionForTask(ctx, store, taskID)
		if err != nil {
			return "", err
		}
		if res != nil && res.IsEnforcedTreatment() {
			return model.QAArmTreatment, nil
		}
		return "", nil
	}
}

// IsEnforcedTreatment reports whether r requires treatment-arm staging and blocking gates.
func (r *Resolution) IsEnforcedTreatment() bool {
	if r == nil || r.Shadow {
		return false
	}
	switch r.ArmID {
	case "", "baseline", "excluded", model.QAArmControl:
		return false
	default:
		return r.Spec != nil
	}
}

// LookupFlowSpec resolves ref against built-in/registered v2/v3 flows, stored qa_flow_version rows, or v1 flows.
func LookupFlowSpec(ctx context.Context, store *model.Store, ref string) (*qa.FlowSpec, string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || trimmed == "control" || trimmed == "baseline" {
		return nil, "", nil
	}
	if b, ok := qa.LookupBuiltInOrRegisteredVersion(trimmed); ok {
		sp := b.Resolved
		return &sp, b.Hash, nil
	}
	if store != nil {
		findFV := func(q string) (model.QAFlowVersion, bool) {
			if before, after, hasAt := strings.Cut(q, "@"); hasAt {
				if vNum, aErr := strconv.Atoi(after); aErr == nil && vNum > 0 {
					if fv, gErr := store.GetQAFlowVersionByKeyVersion(ctx, before, vNum); gErr == nil {
						return fv, true
					}
				}
			}
			if fv, gErr := store.GetQAFlowVersion(ctx, q); gErr == nil {
				return fv, true
			}
			if list, lErr := store.ListQAFlowVersions(ctx, q, 1); lErr == nil && len(list) > 0 {
				return list[0], true
			}
			return model.QAFlowVersion{}, false
		}

		var resolveParent qa.ParentLookupFunc
		seen := map[string]bool{}
		resolveParent = func(pRef string) (qa.FlowVersionBundle, bool) {
			if b, ok := qa.LookupBuiltInOrRegisteredVersion(pRef); ok {
				return b, true
			}
			if seen[pRef] {
				return qa.FlowVersionBundle{}, false
			}
			seen[pRef] = true
			pfv, ok := findFV(pRef)
			if !ok || strings.TrimSpace(pfv.SourceYAML) == "" {
				return qa.FlowVersionBundle{}, false
			}
			b, rErr := qa.ResolveAnyFlowDeclaration([]byte(pfv.SourceYAML), pfv.Version, resolveParent)
			if rErr != nil {
				return qa.FlowVersionBundle{}, false
			}
			b.Hash = pfv.Hash
			return b, true
		}

		if fv, ok := findFV(trimmed); ok {
			if strings.TrimSpace(fv.SourceYAML) != "" {
				if b, rErr := qa.ResolveAnyFlowDeclaration([]byte(fv.SourceYAML), fv.Version, resolveParent); rErr == nil {
					sp := b.Resolved
					return &sp, fv.Hash, nil
				}
			}
			var sp qa.FlowSpec
			if jErr := json.Unmarshal([]byte(fv.SpecJSON), &sp); jErr == nil {
				return &sp, fv.Hash, nil
			}
		}
	}
	if sp, ok := qa.LookupFlow(trimmed); ok {
		return &sp, "", nil
	}
	return nil, "", nil
}

// ActiveQAResolutionForTask performs a read-only resolution of taskID's QA exposure.
// It never mutates assignment or enrollment rows.
func ActiveQAResolutionForTask(ctx context.Context, store *model.Store, taskID string) (*Resolution, model.QAConfig, error) {
	if store == nil {
		return nil, model.QAConfig{}, nil
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return nil, model.QAConfig{}, err
	}
	if task == nil || !model.TaskEligibleForQAStaging(*task) {
		return nil, model.QAConfig{}, nil
	}

	qaCfg, err := store.ConfigQA(ctx)
	if err != nil {
		return nil, qaCfg, err
	}
	// Step 1: Master kill switch immediately disables QA enforcement across all sources.
	if !qaCfg.Enabled {
		return nil, qaCfg, nil
	}

	root := model.ResolveQARootOrigin(task.WorkOrigin, task.ID)

	// Step 2: Experiment registry enrollment takes precedence over legacy epoch assignments.
	if RegistryResolver != nil {
		res, err := RegistryResolver(ctx, store, *task, root, qaCfg)
		if err != nil {
			return nil, qaCfg, err
		}
		if res != nil {
			return res, qaCfg, nil
		}
	}

	if res, err := resolveOperatorFlowBinding(ctx, store, *task); err != nil {
		return nil, qaCfg, err
	} else if res != nil {
		return res, qaCfg, nil
	}

	// Step 3: Sticky legacy assignment from task_qa_assignment.
	assign, err := store.EarliestQAAssignment(ctx, root)
	if err != nil {
		return nil, qaCfg, err
	}
	if assign == nil {
		return nil, qaCfg, nil
	}
	return resolveLegacyAssignment(ctx, store, *assign, qaCfg), qaCfg, nil
}

// EnsureQAResolutionAtDispatch resolves or assigns QA exposure when a run is about to start.
// It is the sole entry point allowed to write new QA assignments/enrollments.
func EnsureQAResolutionAtDispatch(ctx context.Context, store *model.Store, task model.Task, runID string, now time.Time) (*Resolution, model.QAConfig, error) {
	return EnsureQAResolutionAtDispatchWithConfig(ctx, store, task, runID, nil, now)
}

// EnsureQAResolutionAtDispatchWithConfig resolves or assigns QA exposure at dispatch using an optional
// in-memory QAConfig override when non-nil.
func EnsureQAResolutionAtDispatchWithConfig(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	runID string,
	qaCfgOverride *model.QAConfig,
	now time.Time,
) (*Resolution, model.QAConfig, error) {
	if store == nil {
		return &Resolution{Source: "none", ArmID: "baseline"}, model.QAConfig{}, nil
	}
	if task.Target != nil && !model.TaskEligibleForQAStaging(task) {
		return &Resolution{Source: "none", ArmID: "baseline"}, model.QAConfig{}, nil
	}
	if task.Target == nil && qaCfgOverride == nil {
		return &Resolution{Source: "none", ArmID: "baseline"}, model.QAConfig{}, nil
	}

	var qaCfg model.QAConfig
	var err error
	if qaCfgOverride != nil {
		qaCfg = *qaCfgOverride
	} else {
		qaCfg, err = store.ConfigQA(ctx)
		if err != nil {
			return nil, model.QAConfig{}, err
		}
	}
	if !qaCfg.Enabled {
		return &Resolution{Source: "none", ArmID: "baseline"}, qaCfg, nil
	}

	root := model.ResolveQARootOrigin(task.WorkOrigin, task.ID)
	hasPrior, err := store.OriginHasPriorRuns(ctx, root, runID)
	if err != nil {
		return nil, qaCfg, err
	}

	// Step 1: Running or previously enrolled qa_experiment takes precedence over legacy rollout.
	if RegistryDispatchResolver != nil {
		res, err := RegistryDispatchResolver(ctx, store, task, root, hasPrior, qaCfg, now)
		if err != nil {
			return nil, qaCfg, err
		}
		if res != nil {
			return res, qaCfg, nil
		}
	}

	if res, err := resolveOperatorFlowBinding(ctx, store, task); err != nil {
		return nil, qaCfg, err
	} else if res != nil {
		return res, qaCfg, nil
	}

	// Step 2: Sticky legacy assignment; pre-existing work trees with prior runs are excluded from new rollout.
	assign, err := store.EarliestQAAssignment(ctx, root)
	if err != nil {
		return nil, qaCfg, err
	}
	if assign == nil {
		if hasPrior {
			return &Resolution{
				Source:      "none",
				ArmID:       "excluded",
				ExcludedWhy: "origin had runs before rollout epoch started",
			}, qaCfg, nil
		}
		if qaCfg.RolloutPct <= 0 {
			return &Resolution{Source: "none", ArmID: "baseline"}, qaCfg, nil
		}
		tier := qa.ClassifyTaskComplexity(task.Title, task.Body)
		created, err := qa.AssignOrLoadArm(ctx, store, root, tier, qaCfg, now)
		if err != nil {
			return nil, qaCfg, err
		}
		assign = &created
	}

	return resolveLegacyAssignment(ctx, store, *assign, qaCfg), qaCfg, nil
}

func resolveLegacyAssignment(ctx context.Context, store *model.Store, assign model.QAAssignment, qaCfg model.QAConfig) *Resolution {
	//Epoch stickiness: if the deployment's epoch moved past the origin's assigned epoch,
	// do not re-bucket mid-work-tree; treat the origin as baseline.
	if assign.Epoch != qaCfg.EpochOrDefault() {
		return &Resolution{
			Source: "legacy",
			ArmID:  "baseline",
		}
	}

	flowID := strings.TrimSpace(assign.FlowID)
	if flowID == "" {
		flowID = strings.TrimSpace(qaCfg.FlowID)
	}
	if flowID == "" {
		flowID = model.DefaultQAFlowID
	}

	if assign.Arm == model.QAArmTreatment {
		spec, hash, _ := LookupFlowSpec(ctx, store, flowID)
		return &Resolution{
			Source:      "legacy",
			ArmID:       model.QAArmTreatment,
			Spec:        spec,
			FlowID:      flowID,
			FlowHash:    hash,
			QAEngineRev: qa.QAEngineRev,
			Shadow:      false,
		}
	}

	if assign.Arm == model.QAArmControl {
		shadow := qaCfg.ShadowOnControl != nil && *qaCfg.ShadowOnControl
		var spec *qa.FlowSpec
		var hash string
		if shadow {
			spec, hash, _ = LookupFlowSpec(ctx, store, flowID)
		}
		return &Resolution{
			Source:      "legacy",
			ArmID:       model.QAArmControl,
			FlowID:      flowID,
			FlowHash:    hash,
			QAEngineRev: qa.QAEngineRev,
			Spec:        spec,
			Shadow:      shadow,
		}
	}

	return &Resolution{
		Source: "legacy",
		ArmID:  assign.Arm,
	}
}

func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocking":
		return 3
	case "warn":
		return 2
	case "shadow":
		return 1
	default:
		return 0
	}
}

func triageModeRank(m string) int {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "enforced":
		return 3
	case "shadow":
		return 2
	case "off":
		return 1
	default:
		return 0
	}
}

func tierRank(t string) int {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "T3":
		return 3
	case "T2":
		return 2
	case "T1":
		return 1
	default:
		return 0
	}
}

func unionStrings(base, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := slices.Clone(base)
	for _, s := range extra {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func floorRuleTemplate(ruleID string) qa.RuleSpec {
	switch ruleID {
	case qa.RuleTestIntegrityGuard:
		return qa.RuleSpec{
			ID:          qa.RuleTestIntegrityGuard,
			Label:       "Test integrity & non-tautological assertion guard",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Blocks net test file deletion, unannotated skips or focused tests, net test-case drops alongside production edits, and newly added tests with zero failure or assertion calls.",
			TargetPaths: []string{"**/*_test.go", "**/*.spec.ts", "**/*.spec.js"},
			Enabled:     true,
		}
	case qa.RuleStrictConfigDecoder:
		return qa.RuleSpec{
			ID:          qa.RuleStrictConfigDecoder,
			Label:       "Type-aware strict YAML/JSON configuration decoder",
			Stage:       "S3",
			Severity:    "blocking",
			Summary:     "Requires KnownFields(true) or DisallowUnknownFields() when decoding YAML/JSON into Go structs while ignoring structural yaml.Node, map, raw message, or interface targets.",
			TargetPaths: []string{"pkg/**/*.go", "internal/**/*.go", "cmd/**/*.go"},
			Enabled:     true,
		}
	default:
		return qa.RuleSpec{
			ID:       ruleID,
			Stage:    "S3",
			Severity: "blocking",
			Enabled:  true,
		}
	}
}

// ComposeTightenOnly enforces non-negotiable QA floors on a resolved FlowSpec
// and optionally merges one or more overlay specs using tighten-only semantics
// (an overlay can enable stages/checks or raise severities/triage modes, but can
// never disable an enabled stage/check or lower a severity/triage floor).
// StagingIsolation is always true, and RuleTestIntegrityGuard and
// RuleStrictConfigDecoder are always Enabled: true with Severity: "blocking".
func ComposeTightenOnly(spec qa.FlowSpec, overlays ...qa.FlowSpec) qa.FlowSpec {
	rules := make([]qa.RuleSpec, len(spec.Rules))
	for i := range spec.Rules {
		rules[i] = spec.Rules[i]
		rules[i].TargetPaths = slices.Clone(spec.Rules[i].TargetPaths)
		if spec.Rules[i].Params != nil {
			paramsCopy := make(map[string]any, len(spec.Rules[i].Params))
			for k, v := range spec.Rules[i].Params {
				paramsCopy[k] = v
			}
			rules[i].Params = paramsCopy
		}
	}
	spec.Rules = rules
	spec.VerifierTiers = slices.Clone(spec.VerifierTiers)
	spec.StrictDecoderPaths = slices.Clone(spec.StrictDecoderPaths)
	spec.RiskOverrides = slices.Clone(spec.RiskOverrides)
	spec.CustomStages = slices.Clone(spec.CustomStages)
	spec.CustomChecks = slices.Clone(spec.CustomChecks)
	spec.ReadabilityPacks = slices.Clone(spec.ReadabilityPacks)
	spec.SkillSteps = slices.Clone(spec.SkillSteps)
	spec.S5MergeTriage.AutoMergeTiers = slices.Clone(spec.S5MergeTriage.AutoMergeTiers)
	spec.S5MergeTriage.RequireHumanTiers = slices.Clone(spec.S5MergeTriage.RequireHumanTiers)

	for _, ov := range overlays {
		spec.StopNudge = spec.StopNudge || ov.StopNudge
		spec.InjectSkill = spec.InjectSkill || ov.InjectSkill
		if strings.TrimSpace(spec.SkillText) == "" && strings.TrimSpace(ov.SkillText) != "" {
			spec.SkillText = ov.SkillText
		}
		if ov.SkillBytes > spec.SkillBytes {
			spec.SkillBytes = ov.SkillBytes
		}
		if strings.TrimSpace(spec.IntentSkillText) == "" && strings.TrimSpace(ov.IntentSkillText) != "" {
			spec.IntentSkillText = ov.IntentSkillText
		}
		if strings.TrimSpace(spec.PlanCriticSkillText) == "" && strings.TrimSpace(ov.PlanCriticSkillText) != "" {
			spec.PlanCriticSkillText = ov.PlanCriticSkillText
		}
		if strings.TrimSpace(spec.VerifierSkillText) == "" && strings.TrimSpace(ov.VerifierSkillText) != "" {
			spec.VerifierSkillText = ov.VerifierSkillText
		}
		if strings.TrimSpace(spec.SecuritySkillText) == "" && strings.TrimSpace(ov.SecuritySkillText) != "" {
			spec.SecuritySkillText = ov.SecuritySkillText
		}

		// S0 Intent tighten-only
		if ov.S0Intent || ov.S0IntentConfig.Enabled {
			spec.S0Intent = true
			spec.S0IntentConfig.Enabled = true
		}
		if spec.S0Intent {
			spec.S0IntentConfig.Enabled = true
		}
		spec.S0IntentConfig.Complexities = unionStrings(spec.S0IntentConfig.Complexities, ov.S0IntentConfig.Complexities)
		if ov.S0IntentConfig.MaxSeconds > spec.S0IntentConfig.MaxSeconds {
			spec.S0IntentConfig.MaxSeconds = ov.S0IntentConfig.MaxSeconds
		}
		if ov.S0IntentConfig.MaxTokens > spec.S0IntentConfig.MaxTokens {
			spec.S0IntentConfig.MaxTokens = ov.S0IntentConfig.MaxTokens
		}
		if ov.S0IntentConfig.OnClarification == "park" || (spec.S0IntentConfig.OnClarification == "" && ov.S0IntentConfig.OnClarification != "") {
			spec.S0IntentConfig.OnClarification = ov.S0IntentConfig.OnClarification
		}

		// S2 Plan Critic tighten-only
		if ov.S2PlanCritic || ov.S2PlanCriticConfig.Enabled {
			spec.S2PlanCritic = true
			spec.S2PlanCriticConfig.Enabled = true
		}
		if spec.S2PlanCritic {
			spec.S2PlanCriticConfig.Enabled = true
		}
		spec.S2PlanCriticConfig.Complexities = unionStrings(spec.S2PlanCriticConfig.Complexities, ov.S2PlanCriticConfig.Complexities)
		spec.S2PlanCriticConfig.Tiers = unionStrings(spec.S2PlanCriticConfig.Tiers, ov.S2PlanCriticConfig.Tiers)
		if ov.S2PlanCriticConfig.MaxSeconds > spec.S2PlanCriticConfig.MaxSeconds {
			spec.S2PlanCriticConfig.MaxSeconds = ov.S2PlanCriticConfig.MaxSeconds
		}
		if ov.S2PlanCriticConfig.MaxTokens > spec.S2PlanCriticConfig.MaxTokens {
			spec.S2PlanCriticConfig.MaxTokens = ov.S2PlanCriticConfig.MaxTokens
		}
		if ov.S2PlanCriticConfig.MaxBounces > spec.S2PlanCriticConfig.MaxBounces {
			spec.S2PlanCriticConfig.MaxBounces = ov.S2PlanCriticConfig.MaxBounces
		}

		// S4 Verifier tighten-only
		if ov.EnableS4 {
			spec.EnableS4 = true
		}
		spec.VerifierTiers = unionStrings(spec.VerifierTiers, ov.VerifierTiers)
		if ov.MaxS4RepairRounds > spec.MaxS4RepairRounds {
			spec.MaxS4RepairRounds = ov.MaxS4RepairRounds
		}
		if ov.MaxS4Seconds > spec.MaxS4Seconds {
			spec.MaxS4Seconds = ov.MaxS4Seconds
		}
		if ov.MaxS4Tokens > spec.MaxS4Tokens {
			spec.MaxS4Tokens = ov.MaxS4Tokens
		}
		if strings.TrimSpace(spec.VerifierChecklist) == "" && strings.TrimSpace(ov.VerifierChecklist) != "" {
			spec.VerifierChecklist = ov.VerifierChecklist
		}
		spec.StrictDecoderPaths = unionStrings(spec.StrictDecoderPaths, ov.StrictDecoderPaths)

		// V4 Decompose, Context, PRContract tighten-only
		if ov.Decompose.Enabled && ov.Decompose.Mode != "off" {
			spec.Decompose.Enabled = true
			if spec.Decompose.Mode == "" || spec.Decompose.Mode == "off" || ov.Decompose.Mode == "propose_tasks" {
				spec.Decompose.Mode = ov.Decompose.Mode
			}
			spec.Decompose.Complexities = unionStrings(spec.Decompose.Complexities, ov.Decompose.Complexities)
			if ov.Decompose.MaxSubtasks > spec.Decompose.MaxSubtasks {
				spec.Decompose.MaxSubtasks = ov.Decompose.MaxSubtasks
			}
			if spec.Decompose.TargetDiffLines == 0 || (ov.Decompose.TargetDiffLines > 0 && ov.Decompose.TargetDiffLines < spec.Decompose.TargetDiffLines) {
				spec.Decompose.TargetDiffLines = ov.Decompose.TargetDiffLines
			}
		}
		if ov.Context.Enabled {
			spec.Context.Enabled = true
		}
		spec.Context.IncludeCallers = spec.Context.IncludeCallers || ov.Context.IncludeCallers
		spec.Context.IncludeMemory = spec.Context.IncludeMemory || ov.Context.IncludeMemory
		if ov.Context.MaxTokens > spec.Context.MaxTokens {
			spec.Context.MaxTokens = ov.Context.MaxTokens
		}
		spec.Context.ExtraPaths = unionStrings(spec.Context.ExtraPaths, ov.Context.ExtraPaths)

		if ov.PRContract.Enabled && ov.PRContract.Mode != "off" {
			spec.PRContract.Enabled = true
			if severityRank(ov.PRContract.Mode) > severityRank(spec.PRContract.Mode) {
				spec.PRContract.Mode = ov.PRContract.Mode
			}
			spec.PRContract.RequiredSections = unionStrings(spec.PRContract.RequiredSections, ov.PRContract.RequiredSections)
			spec.PRContract.AuditLintSuppressions = spec.PRContract.AuditLintSuppressions || ov.PRContract.AuditLintSuppressions
			if spec.PRContract.MaxSubjectChars == 0 || (ov.PRContract.MaxSubjectChars > 0 && ov.PRContract.MaxSubjectChars < spec.PRContract.MaxSubjectChars) {
				spec.PRContract.MaxSubjectChars = ov.PRContract.MaxSubjectChars
			}
			if strings.TrimSpace(spec.PRContract.GuidanceText) == "" && strings.TrimSpace(ov.PRContract.GuidanceText) != "" {
				spec.PRContract.GuidanceText = ov.PRContract.GuidanceText
			}
		}

		// Rules tighten-only
		for _, orule := range ov.Rules {
			idx := -1
			for i := range spec.Rules {
				if spec.Rules[i].ID == orule.ID {
					idx = i
					break
				}
			}
			if idx < 0 {
				cloned := orule
				cloned.TargetPaths = slices.Clone(orule.TargetPaths)
				if orule.Params != nil {
					p := make(map[string]any, len(orule.Params))
					for k, v := range orule.Params {
						p[k] = v
					}
					cloned.Params = p
				}
				spec.Rules = append(spec.Rules, cloned)
				continue
			}
			r := &spec.Rules[idx]
			r.Enabled = r.Enabled || orule.Enabled
			if severityRank(orule.Severity) > severityRank(r.Severity) {
				r.Severity = orule.Severity
			}
			if strings.TrimSpace(r.Label) == "" {
				r.Label = orule.Label
			}
			if strings.TrimSpace(r.Stage) == "" {
				r.Stage = orule.Stage
			}
			if strings.TrimSpace(r.Summary) == "" {
				r.Summary = orule.Summary
			}
			r.TargetPaths = unionStrings(r.TargetPaths, orule.TargetPaths)
			if len(orule.Params) > 0 {
				if r.Params == nil {
					r.Params = make(map[string]any, len(orule.Params))
				}
				for k, v := range orule.Params {
					if _, exists := r.Params[k]; !exists {
						r.Params[k] = v
					}
				}
			}
		}

		// RiskOverrides tighten-only (append non-duplicate overrides)
		for _, oro := range ov.RiskOverrides {
			spec.RiskOverrides = append(spec.RiskOverrides, oro)
		}

		// CustomChecks tighten-only
		for _, occ := range ov.CustomChecks {
			found := false
			for i := range spec.CustomChecks {
				if spec.CustomChecks[i].ID == occ.ID {
					if severityRank(occ.Severity) > severityRank(spec.CustomChecks[i].Severity) {
						spec.CustomChecks[i].Severity = occ.Severity
					}
					found = true
					break
				}
			}
			if !found {
				spec.CustomChecks = append(spec.CustomChecks, occ)
			}
		}

		// CustomStages tighten-only
		for _, ocs := range ov.CustomStages {
			found := false
			for i := range spec.CustomStages {
				if spec.CustomStages[i].ID == ocs.ID {
					spec.CustomStages[i].CheckIDs = unionStrings(spec.CustomStages[i].CheckIDs, ocs.CheckIDs)
					spec.CustomStages[i].WhenPaths = unionStrings(spec.CustomStages[i].WhenPaths, ocs.WhenPaths)
					if tierRank(ocs.MinTier) < tierRank(spec.CustomStages[i].MinTier) && strings.TrimSpace(ocs.MinTier) != "" {
						// Lower minTier means the stage runs on more tiers (tighter coverage).
						spec.CustomStages[i].MinTier = ocs.MinTier
					}
					found = true
					break
				}
			}
			if !found {
				spec.CustomStages = append(spec.CustomStages, ocs)
			}
		}

		// ReadabilityPacks & SkillSteps tighten-only
		for _, op := range ov.ReadabilityPacks {
			found := false
			for i := range spec.ReadabilityPacks {
				if spec.ReadabilityPacks[i].ID == op.ID {
					spec.ReadabilityPacks[i].Enabled = spec.ReadabilityPacks[i].Enabled || op.Enabled
					found = true
					break
				}
			}
			if !found {
				spec.ReadabilityPacks = append(spec.ReadabilityPacks, op)
			}
		}
		for _, os := range ov.SkillSteps {
			found := false
			for i := range spec.SkillSteps {
				if spec.SkillSteps[i].ID == os.ID {
					spec.SkillSteps[i].Enabled = spec.SkillSteps[i].Enabled || os.Enabled
					found = true
					break
				}
			}
			if !found {
				spec.SkillSteps = append(spec.SkillSteps, os)
			}
		}

		// S5MergeTriage tighten-only
		if triageModeRank(ov.S5MergeTriage.Mode) > triageModeRank(spec.S5MergeTriage.Mode) {
			spec.S5MergeTriage.Mode = ov.S5MergeTriage.Mode
		}
		spec.S5MergeTriage.RequireHumanTiers = unionStrings(spec.S5MergeTriage.RequireHumanTiers, ov.S5MergeTriage.RequireHumanTiers)
		if len(spec.S5MergeTriage.AutoMergeTiers) == 0 {
			spec.S5MergeTriage.AutoMergeTiers = slices.Clone(ov.S5MergeTriage.AutoMergeTiers)
		} else if len(ov.S5MergeTriage.AutoMergeTiers) > 0 {
			var intersected []string
			for _, t := range spec.S5MergeTriage.AutoMergeTiers {
				if slices.Contains(ov.S5MergeTriage.AutoMergeTiers, t) {
					intersected = append(intersected, t)
				}
			}
			spec.S5MergeTriage.AutoMergeTiers = intersected
		}
	}

	// Ensure any tier in RequireHumanTiers is removed from AutoMergeTiers (tighten-only human gate).
	if len(spec.S5MergeTriage.RequireHumanTiers) > 0 && len(spec.S5MergeTriage.AutoMergeTiers) > 0 {
		var filteredAuto []string
		for _, t := range spec.S5MergeTriage.AutoMergeTiers {
			if !slices.Contains(spec.S5MergeTriage.RequireHumanTiers, t) {
				filteredAuto = append(filteredAuto, t)
			}
		}
		spec.S5MergeTriage.AutoMergeTiers = filteredAuto
	}

	// Enforce mandatory global floors unconditionally.
	spec.StagingIsolation = true
	for _, floorID := range []string{qa.RuleTestIntegrityGuard, qa.RuleStrictConfigDecoder} {
		tmpl := floorRuleTemplate(floorID)
		found := false
		for i := range spec.Rules {
			if spec.Rules[i].ID == floorID {
				spec.Rules[i].Enabled = true
				spec.Rules[i].Severity = "blocking"
				if strings.TrimSpace(spec.Rules[i].Stage) == "" {
					spec.Rules[i].Stage = tmpl.Stage
				}
				if strings.TrimSpace(spec.Rules[i].Label) == "" {
					spec.Rules[i].Label = tmpl.Label
				}
				if strings.TrimSpace(spec.Rules[i].Summary) == "" {
					spec.Rules[i].Summary = tmpl.Summary
				}
				if len(spec.Rules[i].TargetPaths) == 0 {
					spec.Rules[i].TargetPaths = slices.Clone(tmpl.TargetPaths)
				}
				found = true
			}
		}
		if !found {
			spec.Rules = append(spec.Rules, tmpl)
		}
	}
	if spec.EnableS4 {
		for i := range spec.Rules {
			if spec.Rules[i].ID == qa.RuleS4OrthogonalVerifier {
				spec.Rules[i].Enabled = true
				if strings.TrimSpace(spec.Rules[i].Severity) == "" {
					spec.Rules[i].Severity = "blocking"
				}
			}
		}
	}
	return spec
}

func resolveOperatorFlowBinding(ctx context.Context, store *model.Store, task model.Task) (*Resolution, error) {
	var repoSpec *qa.FlowSpec
	var repoRef, repoHash string
	if store != nil && task.Target != nil && *task.Target != (model.RepoRef{}) {
		repoCfg, err := store.RepoConfig(ctx, *task.Target)
		if err != nil {
			return nil, err
		}
		if repoCfg != nil {
			if ref := strings.TrimSpace(repoCfg.QAFlowRef); ref != "" {
				spec, hash, err := LookupFlowSpec(ctx, store, ref)
				if err != nil {
					return nil, err
				}
				if spec != nil {
					repoSpec = spec
					repoRef = ref
					repoHash = hash
				}
			}
		}
	}

	if ref := strings.TrimSpace(task.QAFlowRef); ref != "" {
		spec, hash, err := LookupFlowSpec(ctx, store, ref)
		if err != nil {
			return nil, err
		}
		if spec != nil {
			var enforced qa.FlowSpec
			if repoSpec != nil {
				enforced = ComposeTightenOnly(*spec, *repoSpec)
			} else {
				enforced = ComposeTightenOnly(*spec)
			}
			return &Resolution{
				Source:      "process",
				ArmID:       model.QAArmTreatment,
				FlowID:      ref,
				FlowHash:    hash,
				QAEngineRev: qa.QAEngineRev,
				Spec:        &enforced,
				Shadow:      false,
			}, nil
		}
	}
	if repoSpec != nil {
		enforced := ComposeTightenOnly(*repoSpec)
		return &Resolution{
			Source:      "repo",
			ArmID:       model.QAArmTreatment,
			FlowID:      repoRef,
			FlowHash:    repoHash,
			QAEngineRev: qa.QAEngineRev,
			Spec:        &enforced,
			Shadow:      false,
		}, nil
	}
	return nil, nil
}
