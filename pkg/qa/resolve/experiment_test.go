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

package resolve_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
)

func TestExperimentExplicitPairedAndBaseBranchEnforcement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := model.New(db)
	if err := st.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := st.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.QA.Enabled = true
		cfg.QA.RolloutPct = 0
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	now := time.Date(2026, 11, 13, 18, 30, 0, 0, time.UTC)
	if err := st.CreateQAExperiment(ctx, model.QAExperiment{
		ID:    "exp-paired-test",
		Name:  "Paired Test",
		State: model.QAExpStateRunning,
		Arms: []model.QAExperimentArm{
			{ID: "control", Label: "Control", FlowHash: "control", Weight: 50, BaseBranch: "bench/exp/control"},
			{ID: "golden-v2", Label: "golden v2", FlowHash: "golden-v2@1", Weight: 50, BaseBranch: "bench/exp/golden-v2"},
		},
		Allocation: model.QAExperimentAllocation{Mode: "explicit", Design: "paired"},
		AllocationList: []model.QAExplicitAllocationEntry{
			{Key: "alloc-0001", ArmID: "control", PairID: "pair-001"},
			{Key: "alloc-0002", ArmID: "golden-v2", PairID: "pair-001"},
			{Key: "alloc-0003", ArmID: "golden-v2", PairID: "pair-002"},
		},
		PrimaryMetric:   "quality.panel_score",
		PrimaryContrast: "golden-v2:control",
		Plan:            model.QAExperimentPlan{PlannedOriginsPerArm: 10, MaxDays: 7, Alpha: 0.05},
		Options:         model.QAExperimentOptions{ShadowOnControl: true, Memory: "frozen"},
		StartedAt:       &now,
	}); err != nil {
		t.Fatalf("CreateQAExperiment: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "grain"}
	tCtrl := model.Task{ID: "101", Title: "Slot A", Target: &repo, Base: "bench/exp/control", Tags: []string{"qa-alloc:alloc-0001"}, WorkOrigin: "task:101"}
	tTreat := model.Task{ID: "102", Title: "Slot B", Target: &repo, Base: "bench/exp/golden-v2", Tags: []string{"qa-alloc:alloc-0002"}, WorkOrigin: "task:102"}
	tWrongBase := model.Task{ID: "103", Title: "Wrong base", Target: &repo, Base: "main", Tags: []string{"qa-alloc:alloc-0003"}, WorkOrigin: "task:103"}
	tReusedKey := model.Task{ID: "104", Title: "Reused key", Target: &repo, Base: "bench/exp/golden-v2", Tags: []string{"qa-alloc:alloc-0002"}, WorkOrigin: "task:104"}

	for _, tk := range []model.Task{tCtrl, tTreat, tWrongBase, tReusedKey} {
		if err := st.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask %s: %v", tk.ID, err)
		}
	}

	rCtrl, _, err := resolve.EnsureQAResolutionAtDispatch(ctx, st, tCtrl, "101-1", now)
	if err != nil || rCtrl.ArmID != "control" || !rCtrl.Shadow || rCtrl.IsEnforcedTreatment() {
		t.Fatalf("control resolution = (%+v, %v)", rCtrl, err)
	}

	rTreat, _, err := resolve.EnsureQAResolutionAtDispatch(ctx, st, tTreat, "102-1", now)
	if err != nil || rTreat.ArmID != "golden-v2" || !rTreat.IsEnforcedTreatment() || rTreat.Spec == nil {
		t.Fatalf("treatment resolution = (%+v, %v)", rTreat, err)
	}

	// Read-only resolution on child task inherits custom arm ID ("golden-v2") and IsEnforcedTreatment() == true.
	tChild := model.Task{ID: "102-child", Title: "Child", Target: &repo, Base: "bench/exp/golden-v2", WorkOrigin: "task:102"}
	if err := st.PutTask(ctx, tChild); err != nil {
		t.Fatalf("PutTask child: %v", err)
	}
	rChild, _, err := resolve.ActiveQAResolutionForTask(ctx, st, tChild.ID)
	if err != nil || rChild == nil || rChild.ArmID != "golden-v2" || !rChild.IsEnforcedTreatment() {
		t.Fatalf("child read-only resolution = (%+v, %v)", rChild, err)
	}

	// Wrong baseBranch -> excluded.
	rWrong, _, err := resolve.EnsureQAResolutionAtDispatch(ctx, st, tWrongBase, "103-1", now)
	if err != nil || rWrong.ArmID != "excluded" {
		t.Fatalf("wrong baseBranch should exclude, got (%+v, %v)", rWrong, err)
	}

	// Reused explicit key -> excluded.
	rReused, _, err := resolve.EnsureQAResolutionAtDispatch(ctx, st, tReusedKey, "104-1", now)
	if err != nil || rReused.ArmID != "excluded" {
		t.Fatalf("reused key should exclude, got (%+v, %v)", rReused, err)
	}
}
