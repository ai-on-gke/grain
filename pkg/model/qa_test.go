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

package model_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestQAConfigDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	var zero model.QAConfig
	if !zero.IsZero() {
		t.Fatalf("zero QAConfig should report IsZero=true")
	}
	if zero.Enabled {
		t.Fatalf("zero QAConfig must ship disabled by default")
	}
	if got := zero.FlowIDOrDefault(); got != model.DefaultQAFlowID {
		t.Errorf("FlowIDOrDefault = %q, want %q", got, model.DefaultQAFlowID)
	}
	if !zero.ShadowOnControlEnabled() {
		t.Errorf("zero QAConfig should default ShadowOnControlEnabled=true for initial activation")
	}
	if got := model.EncodeQAConfig(zero); got != "" {
		t.Errorf("EncodeQAConfig(zero) = %q, want empty string", got)
	}

	if _, err := model.NormalizeQAConfig(model.QAConfig{Enabled: true, RolloutPct: -1}); err == nil {
		t.Errorf("expected negative rolloutPct error")
	}
	if _, err := model.NormalizeQAConfig(model.QAConfig{Enabled: true, RolloutPct: 101}); err == nil {
		t.Errorf("expected >100 rolloutPct error")
	}
	if _, err := model.NormalizeQAConfig(model.QAConfig{Enabled: true, FlowID: "unknown-flow", RolloutPct: 50}); err == nil {
		t.Errorf("expected unknown flowId error")
	}

	wantIDs := []string{
		model.QAFlowStandard,
		model.QAFlowFast,
		model.QAFlowGatesOnly,
	}
	if got := model.QAFlowIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("QAFlowIDs() = %v, want %v", got, wantIDs)
	}
	for _, validID := range []string{
		model.QAFlowStandard,
		model.QAFlowStandard + "@1",
		model.QAFlowFast,
		model.QAFlowFast + "@1",
		model.QAFlowGatesOnly,
		model.QAFlowGatesOnly + "@1",
		model.QAFlowGoldenV1Minimal,
		model.QAFlowGoldenV1Minimal + "@1",
		model.QAFlowEngineeringWorkflowV4,
		model.QAFlowEngineeringWorkflowV4 + "@1",
		model.QAFlowGoldenV3,
		model.QAFlowGoldenV3 + "@1",
		model.QAFlowGoldenV2,
		model.QAFlowGoldenV2 + "@1",
		model.QAFlowGoldenV2ChecksOnly,
		model.QAFlowGoldenV2ChecksOnly + "@1",
		model.QAFlowGoldenV2SkillOnly,
		model.QAFlowGoldenV2SkillOnly + "@1",
		model.QAFlowDeterministicS3Only,
		model.QAFlowRepoCustom,
	} {
		if !model.ValidQAFlowID(validID) {
			t.Errorf("ValidQAFlowID(%q) = false, want true", validID)
		}
		norm, err := model.NormalizeQAConfig(model.QAConfig{Enabled: true, FlowID: validID, RolloutPct: 25})
		if err != nil || norm.FlowID != validID {
			t.Errorf("NormalizeQAConfig(%q) = (%+v, %v), want FlowID=%q", validID, norm, err, validID)
		}
	}
	for _, invalidID := range []string{
		"",
		"nonexistent-flow",
		model.QAFlowGoldenV3Enforced,
		model.QAFlowDeterministicS3Only + "@1",
		model.QAFlowRepoCustom + "@1",
		"golden-v3@2",
		"golden-v3@1@1",
	} {
		if model.ValidQAFlowID(invalidID) {
			t.Errorf("ValidQAFlowID(%q) = true, want false", invalidID)
		}
	}

	// Custom validator support (plus nil fallback to ValidQAFlowID) and stored round-trip.
	customNorm, err := model.NormalizeQAConfigWithValidator(
		model.QAConfig{Enabled: true, FlowID: "team-custom-flow", RolloutPct: 40},
		func(id string) bool { return id == "team-custom-flow" },
	)
	if err != nil || customNorm.FlowID != "team-custom-flow" {
		t.Fatalf("NormalizeQAConfigWithValidator custom = (%+v, %v)", customNorm, err)
	}
	if _, err := model.NormalizeQAConfigWithValidator(
		model.QAConfig{Enabled: true, FlowID: "team-custom-flow", RolloutPct: 40},
		nil,
	); err == nil {
		t.Fatalf("expected nil validator to fall back to ValidQAFlowID")
	}
	customAdv, err := model.AdvanceQAConfigWithValidator(
		customNorm,
		model.QAConfig{Enabled: true, FlowID: "team-custom-flow@2", RolloutPct: 40},
		func(id string) bool { return id == "team-custom-flow@2" },
	)
	if err != nil || customAdv.FlowID != "team-custom-flow@2" || customAdv.RegimeID != 2 {
		t.Fatalf("AdvanceQAConfigWithValidator = (%+v, %v), want FlowID=team-custom-flow@2 RegimeID=2", customAdv, err)
	}
	encCustom := model.EncodeQAConfig(customAdv)
	decCustom, err := model.DecodeQAConfig(encCustom)
	if err != nil || !customAdv.Equal(decCustom) {
		t.Fatalf("DecodeQAConfig(%q) = (%+v, %v), want %+v", encCustom, decCustom, err, customAdv)
	}
	if _, err := model.DecodeQAConfig(`{"enabled":true,"rolloutPct":20,"unknownKey":1}`); err == nil {
		t.Fatalf("expected DecodeQAConfig to reject unknown JSON keys")
	}
}

func TestQAConfigAdvanceBumpsRegimeAndPreservesExplicitShadowFalse(t *testing.T) {
	t.Parallel()

	r1, err := model.AdvanceQAConfig(model.QAConfig{}, model.QAConfig{
		Enabled:         true,
		RolloutPct:      30,
		ShadowOnControl: model.BoolPtr(false),
	})
	if err != nil {
		t.Fatalf("AdvanceQAConfig initial: %v", err)
	}
	if r1.RegimeID != 1 || r1.Epoch != 1 {
		t.Fatalf("initial RegimeID=%d Epoch=%d, want 1 and 1", r1.RegimeID, r1.Epoch)
	}
	if r1.ShadowOnControlEnabled() {
		t.Fatalf("explicit ShadowOnControl=false must survive normalization")
	}

	encoded := model.EncodeQAConfig(r1)
	decoded, err := model.DecodeQAConfig(encoded)
	if err != nil {
		t.Fatalf("DecodeQAConfig(%s): %v", encoded, err)
	}
	if !r1.Equal(decoded) || !reflect.DeepEqual(r1, decoded) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v (json=%s)", decoded, r1, encoded)
	}
	if _, err := model.DecodeQAConfig(`{"enabled":true,"rolloutPct":150}`); err == nil {
		t.Fatalf("expected DecodeQAConfig to reject out-of-range rolloutPct")
	}
	if _, err := model.DecodeQAConfig(`{not-json`); err == nil {
		t.Fatalf("expected DecodeQAConfig to reject malformed JSON")
	}

	// Saving identical config does not bump RegimeID.
	same, err := model.AdvanceQAConfig(r1, r1)
	if err != nil {
		t.Fatalf("AdvanceQAConfig no-op: %v", err)
	}
	if same.RegimeID != 1 {
		t.Fatalf("no-op save bumped RegimeID to %d, want 1", same.RegimeID)
	}

	// Raising rollout 30% -> 50% bumps RegimeID to 2 while preserving Epoch 1.
	r2, err := model.AdvanceQAConfig(r1, model.QAConfig{
		Enabled:         true,
		FlowID:          model.QAFlowGoldenV1Minimal,
		RolloutPct:      50,
		ShadowOnControl: model.BoolPtr(false),
	})
	if err != nil {
		t.Fatalf("AdvanceQAConfig 30->50: %v", err)
	}
	if r2.RegimeID != 2 || r2.Epoch != 1 {
		t.Fatalf("30->50 transition gave RegimeID=%d Epoch=%d, want 2 and 1", r2.RegimeID, r2.Epoch)
	}

	// Raising Epoch to 3 succeeds, and attempting to downgrade Epoch back to 1 fails.
	r3, err := model.AdvanceQAConfig(r2, model.QAConfig{Enabled: true, RolloutPct: 50, Epoch: 3})
	if err != nil || r3.Epoch != 3 {
		t.Fatalf("AdvanceQAConfig epoch 3 = (%+v, %v), want Epoch=3", r3, err)
	}
	if _, err := model.AdvanceQAConfig(r3, model.QAConfig{Enabled: true, RolloutPct: 50, Epoch: 1}); err == nil {
		t.Fatalf("expected AdvanceQAConfig to reject Epoch downgrade 3 -> 1")
	}
}

func TestQAConfigAdvancePreservesCustomFlowOnDowngradeAndDisable(t *testing.T) {
	t.Parallel()

	for _, flow := range []string{model.QAFlowGoldenV3Enforced, "custom-team-flow@2"} {
		t.Run(flow, func(t *testing.T) {
			prev := model.QAConfig{
				Enabled:         true,
				FlowID:          flow,
				RolloutPct:      40,
				ShadowOnControl: model.BoolPtr(true),
				Epoch:           1,
				RegimeID:        1,
			}

			// Circuit-breaker auto-downgrade to 0% preserves prev.FlowID and bumps RegimeID.
			downgraded, err := model.AdvanceQAConfig(prev, model.QAConfig{
				Enabled:         true,
				FlowID:          flow,
				RolloutPct:      0,
				ShadowOnControl: model.BoolPtr(true),
				Epoch:           1,
			})
			if err != nil {
				t.Fatalf("AdvanceQAConfig downgrade(%q): %v", flow, err)
			}
			if !downgraded.Enabled || downgraded.FlowID != flow || downgraded.RolloutPct != 0 || downgraded.RegimeID != 2 {
				t.Fatalf("downgraded = %+v, want Enabled=true FlowID=%q RolloutPct=0 RegimeID=2", downgraded, flow)
			}

			// Disabling QA while keeping prev.FlowID succeeds without an explicit store validator.
			disabled, err := model.AdvanceQAConfig(prev, model.QAConfig{
				Enabled:         false,
				FlowID:          flow,
				RolloutPct:      40,
				ShadowOnControl: model.BoolPtr(true),
				Epoch:           1,
			})
			if err != nil {
				t.Fatalf("AdvanceQAConfig disable(%q): %v", flow, err)
			}
			if disabled.Enabled || disabled.FlowID != flow || disabled.RegimeID != 2 {
				t.Fatalf("disabled = %+v, want Enabled=false FlowID=%q RegimeID=2", disabled, flow)
			}

			// Without an explicit store validator, rollout increases, epoch increases, re-enables,
			// and flow switches must still be rejected.
			if _, err := model.AdvanceQAConfig(prev, model.QAConfig{
				Enabled:    true,
				FlowID:     flow,
				RolloutPct: 60,
				Epoch:      1,
			}); err == nil {
				t.Fatalf("expected AdvanceQAConfig(%q) rollout increase to fail without store validator", flow)
			}
			if _, err := model.AdvanceQAConfig(prev, model.QAConfig{
				Enabled:    true,
				FlowID:     flow,
				RolloutPct: 40,
				Epoch:      2,
			}); err == nil {
				t.Fatalf("expected AdvanceQAConfig(%q) epoch increase to fail without store validator", flow)
			}
			if _, err := model.AdvanceQAConfig(disabled, model.QAConfig{
				Enabled:    true,
				FlowID:     flow,
				RolloutPct: 10,
				Epoch:      1,
			}); err == nil {
				t.Fatalf("expected AdvanceQAConfig(%q) re-enable to fail without store validator", flow)
			}
			if _, err := model.AdvanceQAConfig(prev, model.QAConfig{
				Enabled:    true,
				FlowID:     "other-custom-flow@1",
				RolloutPct: 0,
				Epoch:      1,
			}); err == nil {
				t.Fatalf("expected AdvanceQAConfig(%q) flow switch to fail without store validator", flow)
			}
		})
	}
}

func TestConfigStorePersistsQAConfigAndRuntimeTelemetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)

	// Fresh config starts with QA disabled.
	if err := st.PutConfig(ctx, model.Config{PollInterval: 30 * time.Second, GitHubHost: "github.com"}); err != nil {
		t.Fatalf("PutConfig default: %v", err)
	}
	cfg, err := st.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig default: %v", err)
	}
	if cfg.QA.Enabled || !cfg.QA.IsZero() {
		t.Fatalf("expected default QA off/zero, got %+v", cfg.QA)
	}

	// Save enabled 50% QA rollout and reload.
	wantQA, err := model.NormalizeQAConfig(model.QAConfig{
		Enabled:         true,
		FlowID:          model.QAFlowGoldenV1Minimal,
		RolloutPct:      50,
		ShadowOnControl: model.BoolPtr(true),
		Epoch:           2,
		RegimeID:        3,
	})
	if err != nil {
		t.Fatalf("NormalizeQAConfig: %v", err)
	}
	cfg.QA = wantQA
	if err := st.PutConfig(ctx, *cfg); err != nil {
		t.Fatalf("PutConfig QA: %v", err)
	}
	reloaded, err := st.GetConfig(ctx)
	if err != nil || reloaded == nil {
		t.Fatalf("GetConfig reloaded: %v", err)
	}
	if !reloaded.QA.Equal(wantQA) {
		t.Fatalf("reloaded QA = %+v, want %+v", reloaded.QA, wantQA)
	}

	// Sticky assignment is idempotent per (root_origin, epoch).
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	first, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:101",
		Epoch:              2,
		RegimeID:           3,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGoldenV1Minimal,
		RolloutPctAtAssign: 50,
		ComplexityTier:     model.QAComplexityHigh,
		AssignedAt:         now,
	})
	if err != nil {
		t.Fatalf("EnsureQAAssignment first: %v", err)
	}
	second, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:101",
		Epoch:              2,
		RegimeID:           4,
		Arm:                model.QAArmControl,
		FlowID:             model.QAFlowDeterministicS3Only,
		RolloutPctAtAssign: 10,
		ComplexityTier:     model.QAComplexityLow,
		AssignedAt:         now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("EnsureQAAssignment second: %v", err)
	}
	if second.Arm != model.QAArmTreatment || second.RegimeID != 3 || second.ComplexityTier != model.QAComplexityHigh {
		t.Fatalf("sticky assignment overwritten on second call: got %+v, want %+v", second, first)
	}

	// Record check run + finding + operator vote.
	runID, err := st.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:     "101",
		RunID:      "run-1",
		CommitSHA:  "deadbeef",
		Stage:      "S3",
		CheckID:    "hollow-test-ast",
		Arm:        model.QAArmTreatment,
		Shadow:     false,
		Verdict:    "fail",
		DurationMS: 142,
		AllocBytes: 65536,
		DetailJSON: `{"files":1}`,
		CreatedAt:  now,
	}, []model.QAFinding{
		{
			RuleID:   "go-test-zero-assertions",
			Severity: "blocker",
			FilePath: "calc/calc_test.go",
			Line:     18,
			Summary:  "TestAdd executes Add(2, 2) without checking the returned value or failing t.",
			Evidence: "func TestAdd(t *testing.T) { _ = Add(2, 2) }",
		},
	})
	if err != nil || runID <= 0 {
		t.Fatalf("RecordQACheckRun = (%d, %v), want >0", runID, err)
	}

	runs, err := st.ListQACheckRunsForTask(ctx, "101")
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListQACheckRunsForTask = (%v, %v), want 1 run", runs, err)
	}
	if runs[0].CheckID != "hollow-test-ast" || runs[0].DurationMS != 142 {
		t.Errorf("unexpected check run: %+v", runs[0])
	}

	findings, err := st.ListQAFindingsForTask(ctx, "101")
	if err != nil || len(findings) != 1 {
		t.Fatalf("ListQAFindingsForTask = (%v, %v), want 1 finding", findings, err)
	}
	voteTime := now.Add(2 * time.Minute)
	if err := st.VoteQAFinding(ctx, findings[0].ID, 1, "operator@example.com", voteTime); err != nil {
		t.Fatalf("VoteQAFinding: %v", err)
	}
	voted, err := st.ListQAFindingsForTask(ctx, "101")
	if err != nil || len(voted) != 1 {
		t.Fatalf("ListQAFindingsForTask after vote: %v", err)
	}
	if voted[0].HumanVote != 1 || voted[0].VotedBy != "operator@example.com" || voted[0].VotedAt == nil {
		t.Fatalf("expected +1 vote recorded, got %+v", voted[0])
	}

	// Vote -1 (false positive), then clear back to 0 (VotedAt nil, VotedBy empty), plus error bounds.
	if err := st.VoteQAFinding(ctx, findings[0].ID, -1, "reviewer@example.com", voteTime.Add(time.Minute)); err != nil {
		t.Fatalf("VoteQAFinding -1: %v", err)
	}
	negVoted, err := st.ListQAFindingsForTask(ctx, "101")
	if err != nil || negVoted[0].HumanVote != -1 || negVoted[0].VotedBy != "reviewer@example.com" {
		t.Fatalf("expected -1 vote recorded, got %+v (err=%v)", negVoted, err)
	}
	if err := st.VoteQAFinding(ctx, findings[0].ID, 0, "reviewer@example.com", voteTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("VoteQAFinding 0 clear: %v", err)
	}
	cleared, err := st.ListQAFindingsForTask(ctx, "101")
	if err != nil || cleared[0].HumanVote != 0 || cleared[0].VotedBy != "" || cleared[0].VotedAt != nil {
		t.Fatalf("expected cleared vote (0, empty voter, nil VotedAt), got %+v (err=%v)", cleared, err)
	}
	if err := st.VoteQAFinding(ctx, findings[0].ID, 2, "operator@example.com", voteTime); err == nil {
		t.Fatalf("expected out-of-range vote=2 error")
	}
	if err := st.VoteQAFinding(ctx, 999999, 1, "operator@example.com", voteTime); err == nil {
		t.Fatalf("expected sql.ErrNoRows on nonexistent finding ID")
	}
}

func TestTaskQASummariesBatchAndPendingPreview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)

	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	repo := &model.RepoRef{Owner: "acme", Name: "widgets"}

	qaCfg, err := model.NormalizeQAConfig(model.QAConfig{
		Enabled:    true,
		FlowID:     model.QAFlowStandard,
		RolloutPct: 100,
		Epoch:      1,
		RegimeID:   1,
	})
	if err != nil {
		t.Fatalf("NormalizeQAConfig: %v", err)
	}
	if err := st.PutConfig(ctx, model.Config{
		PollInterval: 30 * time.Second,
		GitHubHost:   "github.com",
		QA:           qaCfg,
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// 1. Assigned treatment task with S1 + S3 + S5 check runs.
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:t-treat",
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowEngineeringWorkflowV4,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityMedium,
		AssignedAt:         now,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment treatment: %v", err)
	}
	for _, cr := range []model.QACheckRun{
		{TaskID: "t-treat", RunID: "r1", Stage: "S1", CheckID: "qa:context-discovery", Arm: model.QAArmTreatment, Verdict: "pass", CreatedAt: now},
		{TaskID: "t-treat", RunID: "r1", Stage: "S3", CheckID: "golden:hollow-test-ast", Arm: model.QAArmTreatment, Verdict: "pass", BlastTier: "T1", BlastScore: 18, CreatedAt: now.Add(time.Second)},
		{TaskID: "t-treat", RunID: "r1", Stage: "S5", CheckID: "qa:merge-triage", Arm: model.QAArmTreatment, Verdict: "pass", BlastTier: "T1", BlastScore: 18, TriageDecision: "auto_merge", CreatedAt: now.Add(2 * time.Second)},
	} {
		if _, err := st.RecordQACheckRun(ctx, cr, nil); err != nil {
			t.Fatalf("RecordQACheckRun(%s): %v", cr.Stage, err)
		}
	}

	// 2. Assigned control-arm task.
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:t-ctrl",
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmControl,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 50,
		ComplexityTier:     model.QAComplexityLow,
		AssignedAt:         now,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment control: %v", err)
	}

	tasks := []model.Task{
		{ID: "t-treat", Title: "Add API filter", Target: repo},
		{ID: "t-ctrl", Title: "Fix typo", Target: repo},
		{ID: "t-queued", Title: "Implement multi-service billing workflow", Target: repo},
		{ID: "t-closed-old", Title: "Legacy closed task before QA", Target: repo},
		{ID: "t-interactive", Title: "Console session", Target: repo, Interactive: true},
	}
	states := map[string]model.State{
		"t-treat":       model.StateClosed,
		"t-ctrl":        model.StateRunning,
		"t-queued":      model.StateQueued,
		"t-closed-old":  model.StateClosed,
		"t-interactive": model.StateQueued,
	}

	summaries, err := st.TaskQASummaries(ctx, tasks, states, func(task model.Task, _ model.TaskSpecAttachments) string {
		if task.ID == "t-queued" {
			return model.QAComplexityHigh
		}
		return model.QAComplexityLow
	})
	if err != nil {
		t.Fatalf("TaskQASummaries: %v", err)
	}

	treat := summaries["t-treat"]
	if treat == nil || treat.FlowID != model.QAFlowStandard || treat.FlowLabel != "Standard" || treat.BlastTier != "T1" || treat.TriageDecision != "auto_merge" || treat.StageCount != 3 || treat.Pending {
		t.Fatalf("unexpected treatment summary: %+v", treat)
	}
	ctrl := summaries["t-ctrl"]
	if ctrl == nil || ctrl.Arm != model.QAArmControl || ctrl.FlowID != "control" || ctrl.FlowLabel != "Control" {
		t.Fatalf("unexpected control summary: %+v", ctrl)
	}
	queued := summaries["t-queued"]
	if queued == nil || !queued.Pending || queued.FlowID != model.QAFlowStandard || queued.FlowLabel != "Standard" || queued.ComplexityTier != model.QAComplexityHigh {
		t.Fatalf("expected pending Standard/high preview on open eligible task, got %+v", queued)
	}
	if got := summaries["t-closed-old"]; got != nil {
		t.Fatalf("closed historical task without QA runs must stay untagged, got %+v", got)
	}
	if got := summaries["t-interactive"]; got != nil {
		t.Fatalf("interactive session must stay untagged, got %+v", got)
	}

	// 3. Multi-rule S3 check run where rule 1 fails and rule 2 passes on the same SHA must keep verdict="fail".
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:t-multirule",
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowGatesOnly,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityMedium,
		AssignedAt:         now,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment multirule: %v", err)
	}
	if _, err := st.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:    "t-multirule",
		RunID:     "r-mr",
		Stage:     "S3",
		CheckID:   "golden:rule-a",
		Arm:       model.QAArmTreatment,
		Verdict:   "fail",
		CommitSHA: "deadbeef1",
		BlastTier: "T2",
	}, nil); err != nil {
		t.Fatalf("RecordQACheckRun fail: %v", err)
	}
	if _, err := st.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:    "t-multirule",
		RunID:     "r-mr",
		Stage:     "S3",
		CheckID:   "golden:rule-b",
		Arm:       model.QAArmTreatment,
		Verdict:   "pass",
		CommitSHA: "deadbeef1",
		BlastTier: "T2",
	}, nil); err != nil {
		t.Fatalf("RecordQACheckRun pass: %v", err)
	}

	// 4. Control-arm shadow S3 run without task_qa_assignment must still classify as Control, not Treatment.
	if _, err := st.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:    "t-exp-ctrl",
		RunID:     "r-ec",
		Stage:     "S3",
		CheckID:   "golden:rule-b",
		Arm:       model.QAArmControl,
		Shadow:    true,
		Verdict:   "pass",
		CommitSHA: "cafebabe1",
	}, nil); err != nil {
		t.Fatalf("RecordQACheckRun control shadow: %v", err)
	}

	extraSummaries, err := st.TaskQASummaries(ctx, []model.Task{
		{ID: "t-multirule", Title: "Broken multi-rule commit", Target: repo},
		{ID: "t-exp-ctrl", Title: "Experiment control shadow run", Target: repo},
	}, map[string]model.State{
		"t-multirule": model.StateAwaitingSubmit,
		"t-exp-ctrl":  model.StateClosed,
	}, nil)
	if err != nil {
		t.Fatalf("TaskQASummaries extra: %v", err)
	}
	if mr := extraSummaries["t-multirule"]; mr == nil || mr.Verdict != "fail" || mr.FlowID != model.QAFlowGatesOnly {
		t.Fatalf("expected worst verdict=fail across multi-rule S3 run on same SHA, got %+v", mr)
	}
	if ec := extraSummaries["t-exp-ctrl"]; ec == nil || ec.Arm != model.QAArmControl || ec.FlowID != "control" {
		t.Fatalf("expected control-arm shadow run without legacy assignment to stay Control, got %+v", ec)
	}
}

func TestTaskSpecAttachmentsAndSummariesHydration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.PutConfig(ctx, model.Config{
		PollInterval: 30 * time.Second,
		GitHubHost:   "github.com",
		QA: model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowStandard,
			RolloutPct: 100,
			Epoch:      1,
			RegimeID:   1,
		},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	repo := &model.RepoRef{Owner: "acme", Name: "widgets"}
	taskSpec := model.Task{
		ID:     "t-attached-spec",
		Title:  "[ACME-13] UX polish",
		Body:   "Dispatched from **Issue Tracker** (`ACME-13`).\n\n## Capability Summary\nQuick wrapper.\n",
		Target: repo,
		Writes: []model.WriteTarget{{Repo: *repo, Base: "main"}},
	}
	if err := st.PutTask(ctx, taskSpec); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	mdContent := []byte("# ACME-13 Multi-Area Spec\n\n" +
		"## 1. Overview\nCross-surface refactor across `internal/server/server.go`, `internal/store/sqlite.go`, and `ui-ng/src/app/inspector.component.ts`.\n\n" +
		"## 2. Backend Schema\n- Add table columns and migration.\n- Validate optimistic locking.\n\n" +
		"## 3. Frontend Inspector\n- Pin footer bar.\n- Fix tab scroll reset.\n- Restore focus on Escape.\n- Add Vitest coverage.\n\n" +
		"## 4. Phased Implementation\n- Phase 1: Store and API.\n- Phase 2: UI components.\n")
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      taskSpec.ID,
		Filename:    "1-concept-spec.md",
		ContentType: "text/markdown; charset=utf-8",
		Size:        int64(len(mdContent)),
		Content:     mdContent,
		CreatedAt:   time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AddAttachment md: %v", err)
	}
	htmlMock := []byte("<!doctype html><html><body><h1>Mock pkg/orchestrator/schema.go</h1></body></html>")
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      taskSpec.ID,
		Filename:    "2-html_mock.html",
		ContentType: "text/html",
		Size:        int64(len(htmlMock)),
		Content:     htmlMock,
		CreatedAt:   time.Date(2026, 11, 18, 12, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AddAttachment html: %v", err)
	}
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02}
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      taskSpec.ID,
		Filename:    "3-screenshot.png",
		ContentType: "image/png",
		Size:        int64(len(pngBytes)),
		Content:     pngBytes,
		CreatedAt:   time.Date(2026, 11, 18, 12, 0, 2, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AddAttachment png: %v", err)
	}

	gotAtt, err := st.TaskSpecAttachments(ctx, taskSpec.ID, 4096)
	if err != nil {
		t.Fatalf("TaskSpecAttachments: %v", err)
	}
	if gotAtt.MarkdownCount != 1 || !strings.Contains(gotAtt.Text, "## 4. Phased Implementation") || strings.Contains(gotAtt.Text, "<!doctype html>") {
		t.Fatalf("unexpected TaskSpecAttachments: count=%d text=%q", gotAtt.MarkdownCount, gotAtt.Text)
	}

	// Verify SQLite BLOB SUBSTR mid-rune boundary slicing preserves valid UTF-8 attachments.
	midRuneAtt, err := st.TaskSpecAttachments(ctx, taskSpec.ID, 20)
	if err != nil {
		t.Fatalf("TaskSpecAttachments(maxBytes=20): %v", err)
	}
	if midRuneAtt.MarkdownCount != 1 || midRuneAtt.Text == "" {
		t.Fatalf("expected mid-rune cap to retain attachment, got %+v", midRuneAtt)
	}
	utf8BlobTask := model.Task{ID: "t-utf8-blob", Title: "UTF8 boundary", Target: repo}
	if err := st.PutTask(ctx, utf8BlobTask); err != nil {
		t.Fatalf("PutTask utf8BlobTask: %v", err)
	}
	utf8Payload := []byte("alpha — beta — gamma")
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      utf8BlobTask.ID,
		Filename:    "1-utf8.md",
		ContentType: "text/markdown",
		Size:        int64(len(utf8Payload)),
		Content:     utf8Payload,
		CreatedAt:   time.Date(2026, 11, 18, 12, 0, 3, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AddAttachment utf8: %v", err)
	}
	// Byte 8 lands inside the 3-byte em-dash ('\xe2\x80\x94'); SUBSTR + TrimIncompleteTrailingUTF8Bytes must keep "alpha".
	splitAtt, err := st.TaskSpecAttachments(ctx, utf8BlobTask.ID, 8)
	if err != nil {
		t.Fatalf("TaskSpecAttachments split: %v", err)
	}
	if splitAtt.MarkdownCount != 1 || splitAtt.Text != "alpha" {
		t.Fatalf("expected mid-rune BLOB slice to trim cleanly to \"alpha\", got %+v", splitAtt)
	}

	// Pending proposed task hydrates markdown attachment without mutating task.Body.
	summaries, err := st.TaskQASummaries(ctx, []model.Task{taskSpec}, map[string]model.State{
		taskSpec.ID: model.StateProposed,
	}, func(_ model.Task, specAtt model.TaskSpecAttachments) string {
		if specAtt.MarkdownCount == 1 && strings.Contains(specAtt.Text, "## 4. Phased Implementation") {
			return model.QAComplexityHigh
		}
		return model.QAComplexityLow
	})
	if err != nil {
		t.Fatalf("TaskQASummaries pending: %v", err)
	}
	if s := summaries[taskSpec.ID]; s == nil || s.ComplexityTier != model.QAComplexityHigh || !s.Pending {
		t.Fatalf("expected pending proposed task to hydrate attachment complexity=high, got %+v", s)
	}

	// Sticky assignment is epoch-scoped and monotonic-upward only (never downgrades high -> low or mutates prior epochs).
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:" + taskSpec.ID,
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityLow,
		AssignedAt:         time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("EnsureQAAssignment epoch 1: %v", err)
	}
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:" + taskSpec.ID,
		Epoch:              2,
		RegimeID:           2,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityMedium,
		AssignedAt:         time.Date(2026, 11, 18, 12, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("EnsureQAAssignment epoch 2: %v", err)
	}
	if err := st.UpdateQAAssignmentComplexityTier(ctx, "task:"+taskSpec.ID, 2, model.QAComplexityHigh); err != nil {
		t.Fatalf("UpdateQAAssignmentComplexityTier upward: %v", err)
	}
	// Attempting to downgrade high -> low (e.g. from a child subtask) must be a no-op.
	if err := st.UpdateQAAssignmentComplexityTier(ctx, "task:"+taskSpec.ID, 2, model.QAComplexityLow); err != nil {
		t.Fatalf("UpdateQAAssignmentComplexityTier downward: %v", err)
	}
	ep1, err := st.QAAssignment(ctx, "task:"+taskSpec.ID, 1)
	if err != nil || ep1 == nil || ep1.ComplexityTier != model.QAComplexityLow {
		t.Fatalf("expected historical epoch 1 assignment to remain untouched (low), got %+v (err=%v)", ep1, err)
	}
	updatedAssign, err := st.QAAssignment(ctx, "task:"+taskSpec.ID, 2)
	if err != nil || updatedAssign == nil || updatedAssign.ComplexityTier != model.QAComplexityHigh {
		t.Fatalf("expected epoch 2 assignment complexity=high after monotonic guard, got %+v (err=%v)", updatedAssign, err)
	}
}
