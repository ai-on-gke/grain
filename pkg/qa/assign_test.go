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

package qa_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
)

type memAssignmentStore struct {
	mu   sync.Mutex
	rows map[string]model.QAAssignment
}

func newMemAssignmentStore() *memAssignmentStore {
	return &memAssignmentStore{rows: make(map[string]model.QAAssignment)}
}

func keyOf(origin string, epoch int) string {
	return fmt.Sprintf("%s#%d", origin, epoch)
}

func (m *memAssignmentStore) QAAssignment(_ context.Context, rootOrigin string, epoch int) (*model.QAAssignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.rows[keyOf(rootOrigin, epoch)]; ok {
		cp := a
		return &cp, nil
	}
	return nil, nil
}

func (m *memAssignmentStore) EnsureQAAssignment(_ context.Context, candidate model.QAAssignment) (model.QAAssignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := keyOf(candidate.RootOrigin, candidate.Epoch)
	if existing, ok := m.rows[k]; ok {
		return existing, nil
	}
	m.rows[k] = candidate
	return candidate, nil
}

func TestAssignOrLoadArm_OffWritesNothing(t *testing.T) {
	t.Parallel()
	st := newMemAssignmentStore()
	got, err := qa.AssignOrLoadArm(context.Background(), st, "task:1", model.QAComplexityLow, model.QAConfig{
		Enabled:    false,
		RolloutPct: 50,
	}, time.Now())
	if err != nil {
		t.Fatalf("AssignOrLoadArm disabled: %v", err)
	}
	if got.Arm != model.QAArmOff {
		t.Fatalf("Arm = %q, want %q", got.Arm, model.QAArmOff)
	}
	if len(st.rows) != 0 {
		t.Fatalf("disabled QA wrote %d assignment rows, want 0", len(st.rows))
	}
}

func TestAssignOrLoadArm_CalibrationAndBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	// 0% -> all control
	stZero := newMemAssignmentStore()
	for i := 0; i < 50; i++ {
		a, err := qa.AssignOrLoadArm(ctx, stZero, fmt.Sprintf("task:%d", i), model.QAComplexityMedium, model.QAConfig{
			Enabled:    true,
			RolloutPct: 0,
			Epoch:      1,
			RegimeID:   1,
		}, now)
		if err != nil || a.Arm != model.QAArmControl {
			t.Fatalf("0%% rollout gave (%+v, %v), want control", a, err)
		}
	}

	// 100% -> all treatment
	stFull := newMemAssignmentStore()
	for i := 0; i < 50; i++ {
		a, err := qa.AssignOrLoadArm(ctx, stFull, fmt.Sprintf("task:%d", i), model.QAComplexityMedium, model.QAConfig{
			Enabled:    true,
			RolloutPct: 100,
			Epoch:      1,
			RegimeID:   1,
		}, now)
		if err != nil || a.Arm != model.QAArmTreatment {
			t.Fatalf("100%% rollout gave (%+v, %v), want treatment", a, err)
		}
	}

	// 30% across 1000 distinct work-tree origins -> ~300 treatment within [255, 345].
	st30 := newMemAssignmentStore()
	treatmentCount := 0
	for i := 0; i < 1000; i++ {
		a, err := qa.AssignOrLoadArm(ctx, st30, fmt.Sprintf("acme/widgets#%d", i+1), model.QAComplexityMedium, model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowGoldenV1Minimal,
			RolloutPct: 30,
			Epoch:      1,
			RegimeID:   1,
		}, now)
		if err != nil {
			t.Fatalf("30%% rollout iter %d: %v", i, err)
		}
		if a.Arm == model.QAArmTreatment {
			treatmentCount++
		}
	}
	if treatmentCount < 255 || treatmentCount > 345 {
		t.Fatalf("30%% rollout assigned %d/1000 treatment, want within [255, 345]", treatmentCount)
	}
}

func TestAssignOrLoadArm_StickyWithinEpochAndResetsOnNewEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newMemAssignmentStore()
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	// Assign root at 0% (control) in Epoch 1, Regime 1.
	root, err := qa.AssignOrLoadArm(ctx, st, "task:500", model.QAComplexityHigh, model.QAConfig{
		Enabled:    true,
		RolloutPct: 0,
		Epoch:      1,
		RegimeID:   1,
	}, now)
	if err != nil || root.Arm != model.QAArmControl {
		t.Fatalf("initial root = (%+v, %v), want control", root, err)
	}

	// Operator raises rollout to 100% (Regime 2, still Epoch 1): child task in task:500 tree
	// MUST stay on control to prevent cross-arm workstream contamination.
	child, err := qa.AssignOrLoadArm(ctx, st, "task:500", model.QAComplexityLow, model.QAConfig{
		Enabled:    true,
		RolloutPct: 100,
		Epoch:      1,
		RegimeID:   2,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("child AssignOrLoadArm: %v", err)
	}
	if child.Arm != model.QAArmControl || child.RegimeID != 1 {
		t.Fatalf("sticky arm violated: got %+v, want Arm=control RegimeID=1", child)
	}

	// Bumping Epoch to 2 re-assigns cleanly at 100% -> treatment.
	epoch2, err := qa.AssignOrLoadArm(ctx, st, "task:500", model.QAComplexityHigh, model.QAConfig{
		Enabled:    true,
		RolloutPct: 100,
		Epoch:      2,
		RegimeID:   3,
	}, now.Add(2*time.Minute))
	if err != nil || epoch2.Arm != model.QAArmTreatment || epoch2.Epoch != 2 {
		t.Fatalf("epoch 2 assignment = (%+v, %v), want treatment in epoch 2", epoch2, err)
	}
}

func TestClassifyTaskComplexity(t *testing.T) {
	t.Parallel()
	if got := qa.ClassifyTaskComplexity("Fix typo in README", "Correct spelling of flag name."); got != model.QAComplexityLow {
		t.Errorf("typo task classified as %q, want low", got)
	}
	if got := qa.ClassifyTaskComplexity("Add schema migration", "Update SQLite table columns."); got != model.QAComplexityHigh {
		t.Errorf("schema+migration task classified as %q, want high", got)
	}
}
