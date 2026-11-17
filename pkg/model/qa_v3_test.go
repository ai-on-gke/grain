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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openQAV3TestStore(t *testing.T) *model.Store {
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

func TestQAFlowVersionV3Provenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAV3TestStore(t)

	fv := model.QAFlowVersion{
		Hash:         "hash123",
		Key:          "key123",
		Version:      1,
		ParentHash:   "",
		Label:        "Label",
		Summary:      "Summary",
		SpecJSON:     "{}",
		SourceYAML:   "key: key123",
		QAEngineRev:  1,
		CreatedBy:    "tester",
		CreatedAt:    time.Now().UTC(),
		SchemaVer:    "grain.dev/qa-flow/v3",
		SourceKind:   "repo",
		SourceRepo:   "owner/repo",
		SourcePath:   ".grain/qa/flow.yml",
		SourceCommit: "deadbeef",
	}

	if err := s.InsertQAFlowVersion(ctx, fv); err != nil {
		t.Fatalf("InsertQAFlowVersion: %v", err)
	}

	got, err := s.GetQAFlowVersion(ctx, "hash123")
	if err != nil {
		t.Fatalf("GetQAFlowVersion: %v", err)
	}
	if got.SchemaVer != "grain.dev/qa-flow/v3" {
		t.Errorf("got SchemaVer %q, want grain.dev/qa-flow/v3", got.SchemaVer)
	}
	if got.SourceKind != "repo" {
		t.Errorf("got SourceKind %q, want repo", got.SourceKind)
	}
	if got.SourceRepo != "owner/repo" {
		t.Errorf("got SourceRepo %q, want owner/repo", got.SourceRepo)
	}
	if got.SourcePath != ".grain/qa/flow.yml" {
		t.Errorf("got SourcePath %q, want .grain/qa/flow.yml", got.SourcePath)
	}
	if got.SourceCommit != "deadbeef" {
		t.Errorf("got SourceCommit %q, want deadbeef", got.SourceCommit)
	}
}

func TestQACheckRunRepaired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAV3TestStore(t)

	taskID := "task-1"
	if err := s.PutTask(ctx, model.Task{ID: taskID}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	run1 := model.QACheckRun{
		TaskID:         taskID,
		RunID:          "run-1",
		CommitSHA:      "sha1",
		Stage:          "s3",
		CheckID:        "check-1",
		Arm:            "treatment",
		Shadow:         false,
		Verdict:        "fail",
		BlastTier:      "T1",
		BlastScore:     10,
		ComplexityTier: "high",
		TriageDecision: "auto_merge",
	}
	if _, err := s.RecordQACheckRun(ctx, run1, nil); err != nil {
		t.Fatalf("RecordQACheckRun: %v", err)
	}

	run2 := model.QACheckRun{
		TaskID:    taskID,
		RunID:     "run-2",
		CommitSHA: "sha2",
		Stage:     "s3",
		CheckID:   "check-1",
		Arm:       "treatment",
		Shadow:    false,
		Verdict:   "pass",
	}
	if _, err := s.RecordQACheckRun(ctx, run2, nil); err != nil {
		t.Fatalf("RecordQACheckRun 2: %v", err)
	}

	if err := s.MarkPriorQACheckRunsRepaired(ctx, taskID, "check-1", "sha2"); err != nil {
		t.Fatalf("MarkPriorQACheckRunsRepaired: %v", err)
	}

	runs, err := s.ListQACheckRunsForTask(ctx, taskID)
	if err != nil {
		t.Fatalf("ListQACheckRunsForTask: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}

	if runs[0].RepairedInNextSha != 1 {
		t.Errorf("run1 RepairedInNextSha = %d, want 1", runs[0].RepairedInNextSha)
	}
	if runs[0].BlastTier != "T1" {
		t.Errorf("run1 BlastTier = %q, want T1", runs[0].BlastTier)
	}
	if runs[0].ComplexityTier != "high" {
		t.Errorf("run1 ComplexityTier = %q, want high", runs[0].ComplexityTier)
	}

	if runs[1].RepairedInNextSha != 0 {
		t.Errorf("run2 RepairedInNextSha = %d, want 0", runs[1].RepairedInNextSha)
	}
}

func TestQAExperimentReferenceArmID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAV3TestStore(t)

	exp := model.QAExperiment{
		ID:             "exp-1",
		Name:           "Exp 1",
		State:          model.QAExpStateDraft,
		ReferenceArmID: "golden-v2@1",
	}
	if err := s.CreateQAExperiment(ctx, exp); err != nil {
		t.Fatalf("CreateQAExperiment: %v", err)
	}

	got, err := s.GetQAExperiment(ctx, "exp-1")
	if err != nil {
		t.Fatalf("GetQAExperiment: %v", err)
	}
	if got.ReferenceArmID != "golden-v2@1" {
		t.Errorf("got ReferenceArmID %q, want golden-v2@1", got.ReferenceArmID)
	}
}

func TestQATriageHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openQAV3TestStore(t)

	taskID := "task-hold"
	if err := s.PutTask(ctx, model.Task{ID: taskID}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	task, _ := s.GetTask(ctx, taskID)

	reason := model.HumanReviewReason{
		Source:   model.HumanReviewQATriage,
		Category: "risk",
		Reason:   "test triage hold",
	}
	if _, _, err := model.AddHumanReviewReason(task, reason); err != nil {
		t.Fatalf("AddHumanReviewReason: %v", err)
	}

	agent := model.Principal{Kind: model.PrincipalAgent, ID: "agent-1"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "human-1"}

	// Release by agent should fail
	if _, err := s.ReleaseHumanReviewHold(ctx, taskID, agent, "released"); err == nil {
		t.Errorf("expected ReleaseHumanReviewHold to fail for PrincipalAgent")
	}

	// Wait, we need to save the task before we can release it in the DB!
	if err := s.UpdateTask(ctx, taskID, func(t *model.Task) error {
		t.Links = task.Links
		return nil
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// Release by agent should still fail
	if _, err := s.ReleaseHumanReviewHold(ctx, taskID, agent, "released"); err == nil {
		t.Errorf("expected ReleaseHumanReviewHold to fail for PrincipalAgent")
	}

	// Release by human should succeed
	released, err := s.ReleaseHumanReviewHold(ctx, taskID, human, "released")
	if err != nil {
		t.Fatalf("ReleaseHumanReviewHold (human): %v", err)
	}
	if len(released) != 1 {
		t.Errorf("got %d released reasons, want 1", len(released))
	}
}
