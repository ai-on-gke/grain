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
)

func TestQAFlowVersionCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	now := time.Now().UTC().Round(time.Second)

	fv := model.QAFlowVersion{
		Hash:        "hash1",
		Key:         "my-flow",
		Version:     1,
		ParentHash:  "",
		Label:       "My Flow",
		Summary:     "Test",
		SpecJSON:    "{}",
		SourceYAML:  "test: 1",
		QAEngineRev: 123,
		CreatedBy:   "user@example.com",
		CreatedAt:   now,
	}

	err := s.InsertQAFlowVersion(ctx, fv)
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	got, err := s.GetQAFlowVersion(ctx, "hash1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Hash != fv.Hash || got.Key != fv.Key {
		t.Errorf("Get mismatch: %+v", got)
	}

	next, err := s.NextQAFlowVersionNumber(ctx, "my-flow")
	if err != nil || next != 2 {
		t.Errorf("Next: got %d, %v", next, err)
	}

	// Unique constraint
	err = s.InsertQAFlowVersion(ctx, fv)
	if err == nil {
		t.Errorf("Expected unique constraint error")
	}

	fv2 := fv
	fv2.Hash = "hash2"
	fv2.Version = 2
	fv2.CreatedAt = now.Add(time.Hour)
	err = s.InsertQAFlowVersion(ctx, fv2)
	if err != nil {
		t.Fatalf("Insert 2 failed: %v", err)
	}

	list, err := s.ListQAFlowVersions(ctx, "my-flow", 10)
	if err != nil || len(list) != 2 {
		t.Errorf("List: %v %v", list, err)
	}
	if len(list) > 0 && list[0].Hash != "hash2" {
		t.Errorf("Expected descending order")
	}

	got2, err := s.GetQAFlowVersionByKeyVersion(ctx, "my-flow", 2)
	if err != nil || got2.Hash != "hash2" {
		t.Errorf("GetByKeyVersion: %v", err)
	}

	err = s.RetireQAFlowVersion(ctx, "hash1", now)
	if err != nil {
		t.Errorf("Retire: %v", err)
	}
	got3, _ := s.GetQAFlowVersion(ctx, "hash1")
	if got3.RetiredAt == nil {
		t.Errorf("Expected retired at")
	}
}

func TestEarliestQAAssignmentStickiness(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	now := time.Now().UTC().Round(time.Second)

	a1 := model.QAAssignment{
		RootOrigin:         "test-origin",
		Epoch:              1,
		RegimeID:           1,
		Arm:                "treatment",
		FlowID:             "golden-v2",
		RolloutPctAtAssign: 50,
		ComplexityTier:     "medium",
		AssignedAt:         now,
	}

	_, err := s.EnsureQAAssignment(ctx, a1)
	if err != nil {
		t.Fatalf("Ensure 1 failed: %v", err)
	}

	a2 := a1
	a2.Epoch = 2
	a2.Arm = "control"
	a2.AssignedAt = now.Add(time.Hour)
	_, err = s.EnsureQAAssignment(ctx, a2)
	if err != nil {
		t.Fatalf("Ensure 2 failed: %v", err)
	}

	got, err := s.EarliestQAAssignment(ctx, "test-origin")
	if err != nil || got == nil {
		t.Fatalf("Earliest: %v", err)
	}
	if got.Epoch != 1 || got.Arm != "treatment" {
		t.Errorf("Expected first assignment, got epoch %d arm %s", got.Epoch, got.Arm)
	}
}
