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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openQAExperimentTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := model.New(db)
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return s
}

func TestQAExperimentLifecycleOptimisticConcurrencyAndSingleRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAExperimentTestStore(t)

	exp1 := model.QAExperiment{
		ID:              "exp-alpha",
		Name:            "Alpha Experiment",
		Hypothesis:      "golden-v2 lowers escaped defects",
		State:           model.QAExpStateDraft,
		Arms:            []model.QAExperimentArm{{ID: "control", Label: "Control", FlowHash: "control", Weight: 50}, {ID: "treatment", Label: "Treatment", FlowHash: "golden-v2@1", Weight: 50}},
		Scope:           model.QAExperimentScope{Repos: []string{"acme/widgets"}},
		Allocation:      model.QAExperimentAllocation{Mode: "hash"},
		Salt:            "0123456789abcdef0123456789abcdef",
		PrimaryMetric:   "review.findings_sev.per_pr",
		PrimaryContrast: "treatment:control",
		Plan:            model.QAExperimentPlan{PlannedOriginsPerArm: 30, MaxDays: 14, Alpha: 0.05},
		Options:         model.QAExperimentOptions{ShadowOnControl: true, Memory: "frozen"},
		Engine:          model.QAExperimentEngine{QAEngineRev: 1},
		CreatedBy:       "operator@example.com",
	}
	if err := s.CreateQAExperiment(ctx, exp1); err != nil {
		t.Fatalf("CreateQAExperiment exp1: %v", err)
	}

	got, err := s.GetQAExperiment(ctx, "exp-alpha")
	if err != nil {
		t.Fatalf("GetQAExperiment: %v", err)
	}
	if got.Revision != 1 || got.State != model.QAExpStateDraft || len(got.Arms) != 2 {
		t.Fatalf("unexpected draft state: %+v", got)
	}

	// Wrong expected revision conflicts.
	if _, err := s.UpdateQAExperiment(ctx, "exp-alpha", 99, func(e *model.QAExperiment) error {
		e.Hypothesis = "stale"
		return nil
	}); !errors.Is(err, model.ErrQAExperimentConflict) {
		t.Fatalf("expected ErrQAExperimentConflict on stale revision, got %v", err)
	}

	now := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	started, err := s.UpdateQAExperiment(ctx, "exp-alpha", 1, func(e *model.QAExperiment) error {
		e.State = model.QAExpStateRunning
		e.StartedAt = &now
		return nil
	})
	if err != nil {
		t.Fatalf("start exp-alpha: %v", err)
	}
	if started.Revision != 2 || started.State != model.QAExpStateRunning {
		t.Fatalf("unexpected started state: %+v", started)
	}

	running, err := s.GetRunningQAExperiment(ctx)
	if err != nil || running == nil || running.ID != "exp-alpha" {
		t.Fatalf("GetRunningQAExperiment = (%+v, %v)", running, err)
	}

	// Second experiment cannot enter running while exp-alpha is running.
	exp2 := exp1
	exp2.ID = "exp-beta"
	exp2.Name = "Beta Experiment"
	if err := s.CreateQAExperiment(ctx, exp2); err != nil {
		t.Fatalf("CreateQAExperiment exp2: %v", err)
	}
	if _, err := s.UpdateQAExperiment(ctx, "exp-beta", 1, func(e *model.QAExperiment) error {
		e.State = model.QAExpStateRunning
		return nil
	}); !errors.Is(err, model.ErrQAExperimentConflict) {
		t.Fatalf("expected ErrQAExperimentConflict for second running experiment, got %v", err)
	}

	// Engine revision bump holds enrollment on running experiment.
	held, err := s.HoldRunningExperimentForEngineRev(ctx, 2)
	if err != nil || held == nil || held.EnrollmentHeld != "engine-change" {
		t.Fatalf("HoldRunningExperimentForEngineRev = (%+v, %v)", held, err)
	}
}

func TestQAEnrollmentStickinessMemoryFreezeAndCheckRunProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAExperimentTestStore(t)

	now := time.Date(2026, 11, 13, 18, 10, 0, 0, time.UTC)
	if err := s.CreateQAExperiment(ctx, model.QAExperiment{
		ID:              "exp-paired",
		Name:            "Paired Bench",
		State:           model.QAExpStateRunning,
		Arms:            []model.QAExperimentArm{{ID: "control", Label: "Control", FlowHash: "control", Weight: 50}, {ID: "treatment", Label: "Treatment", FlowHash: "golden-v2@1", Weight: 50}},
		Allocation:      model.QAExperimentAllocation{Mode: "explicit", Design: "paired"},
		PrimaryMetric:   "quality.panel_score",
		PrimaryContrast: "treatment:control",
		Plan:            model.QAExperimentPlan{PlannedOriginsPerArm: 30, MaxDays: 7, Alpha: 0.05},
		Options:         model.QAExperimentOptions{ShadowOnControl: true, Memory: "frozen"},
		StartedAt:       &now,
	}); err != nil {
		t.Fatalf("CreateQAExperiment: %v", err)
	}

	bucket := 420
	enr1, created, err := s.InsertQAEnrollment(ctx, model.QAEnrollment{
		RootOrigin:    "task:t1",
		ExperimentID:  "exp-paired",
		Segment:       1,
		ArmID:         "treatment",
		FlowHash:      "golden-v2@1",
		QAEngineRev:   1,
		Model:         "gemini-pro",
		GrainBuild:    "v3.6.0-dev",
		AllocationKey: "alloc-0001",
		Bucket:        &bucket,
		AssignedAt:    now,
	})
	if err != nil || !created || enr1.ArmID != "treatment" {
		t.Fatalf("first InsertQAEnrollment = (%+v, %v, %v)", enr1, created, err)
	}

	// Second attempt on same rootOrigin is idempotent and never flips arm or experiment.
	enr2, created2, err := s.InsertQAEnrollment(ctx, model.QAEnrollment{
		RootOrigin:    "task:t1",
		ExperimentID:  "other-exp",
		Segment:       2,
		ArmID:         "control",
		AllocationKey: "alloc-9999",
	})
	if err != nil || created2 || enr2.ExperimentID != "exp-paired" || enr2.ArmID != "treatment" {
		t.Fatalf("sticky InsertQAEnrollment failed: (%+v, %v, %v)", enr2, created2, err)
	}

	used, err := s.IsAllocationKeyUsed(ctx, "exp-paired", "alloc-0001")
	if err != nil || !used {
		t.Fatalf("IsAllocationKeyUsed(alloc-0001) = (%v, %v), want (true, nil)", used, err)
	}

	// Create enrolled task t1 and non-enrolled task t2 to test memory: frozen enforcement.
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := s.PutTask(ctx, model.Task{
		ID:         "t1",
		Title:      "Enrolled task",
		Target:     &repo,
		WorkOrigin: "task:t1",
	}); err != nil {
		t.Fatalf("PutTask t1: %v", err)
	}
	if err := s.PutTask(ctx, model.Task{
		ID:         "t2",
		Title:      "Unenrolled task",
		Target:     &repo,
		WorkOrigin: "task:t2",
	}); err != nil {
		t.Fatalf("PutTask t2: %v", err)
	}

	errFrozen := s.PutMemory(ctx, model.Memory{
		Repo:      &repo,
		Text:      "attempted leak",
		UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "t1"},
	})
	if errFrozen == nil || !strings.Contains(errFrozen.Error(), "Repo memory is read-only during experiment exp-paired") {
		t.Fatalf("expected frozen memory error for enrolled agent t1, got %v", errFrozen)
	}

	errDocFrozen := s.PutMemoryDocument(ctx, model.MemoryDocument{
		Repo:      &repo,
		Name:      "notes",
		Summary:   "summary",
		Text:      "attempted doc leak",
		UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "t1"},
	})
	if errDocFrozen == nil || !strings.Contains(errDocFrozen.Error(), "Repo memory is read-only during experiment exp-paired") {
		t.Fatalf("expected frozen memory document error for enrolled agent t1, got %v", errDocFrozen)
	}

	// Human operator and unenrolled agent t2 can still write memory.
	if err := s.PutMemory(ctx, model.Memory{
		Repo:      &repo,
		Text:      "operator note",
		UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"},
	}); err != nil {
		t.Fatalf("human PutMemory failed: %v", err)
	}
	if err := s.PutMemory(ctx, model.Memory{
		Repo:      &repo,
		Text:      "unenrolled agent note",
		UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "t2"},
	}); err != nil {
		t.Fatalf("unenrolled agent PutMemory failed: %v", err)
	}

	// Verify QACheckRun provenance columns round-trip.
	if _, err := s.RecordQACheckRun(ctx, model.QACheckRun{
		TaskID:       "t1",
		RunID:        "t1-1",
		CommitSHA:    "deadbeef",
		Stage:        "S3",
		CheckID:      "golden:test-integrity-guard",
		Arm:          "treatment",
		Verdict:      "pass",
		ExperimentID: "exp-paired",
		ArmID:        "treatment",
		FlowHash:     "golden-v2@1",
		QAEngineRev:  1,
		Model:        "gemini-pro",
		BaseSHA:      "6b560ee97",
	}, nil); err != nil {
		t.Fatalf("RecordQACheckRun: %v", err)
	}
	runs, err := s.ListQACheckRunsForTask(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListQACheckRunsForTask = (%+v, %v)", runs, err)
	}
	if runs[0].ExperimentID != "exp-paired" || runs[0].ArmID != "treatment" || runs[0].FlowHash != "golden-v2@1" || runs[0].QAEngineRev != 1 || runs[0].Model != "gemini-pro" || runs[0].BaseSHA != "6b560ee97" {
		t.Fatalf("check run provenance mismatch: %+v", runs[0])
	}
}
