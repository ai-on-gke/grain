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
