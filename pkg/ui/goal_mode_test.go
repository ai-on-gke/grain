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

package ui_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateUpdateAndProjectGoalMode(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Autonomous goal task",
		Description: "Build and verify end-to-end",
		Repo:        "acme/widgets",
		Interactive: true,
		Approved:    true,
		GoalMode: &model.GoalModeConfig{
			Enabled:            true,
			MockMissingSecrets: true,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Interactive {
		t.Fatal("expected Interactive to be false when GoalMode.Enabled is true on root task")
	}
	if created.GoalMode == nil || !created.GoalMode.Enabled {
		t.Fatalf("expected GoalMode enabled on created task, got %+v", created.GoalMode)
	}
	if created.GoalMode.MaxPasses != 8 || created.GoalMode.WallClockMinutes != 120 || !created.GoalMode.MockMissingSecrets {
		t.Fatalf("unexpected normalized GoalMode: %+v", created.GoalMode)
	}

	// Child proposal inherits GoalMode when req.GoalMode is nil.
	child, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Child task",
		Description: "Follow-up slice",
		Repo:        "acme/widgets",
		ProposedBy:  created.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask child: %v", err)
	}
	if child.GoalMode == nil || !child.GoalMode.Enabled || !child.GoalMode.MockMissingSecrets {
		t.Fatalf("expected child task to inherit GoalMode, got %+v", child.GoalMode)
	}

	// Persist assumptions and scorecard on parent and verify projection in GetTask.
	if err := store.UpdateTask(ctx, created.ID, func(mt *model.Task) error {
		if err := model.AppendGoalAssumption(mt, model.GoalAssumption{
			Topic:    "Storage format",
			Decision: "Task links JSON",
			Pass:     1,
		}); err != nil {
			return err
		}
		return model.SetGoalScorecard(mt, model.GoalScorecard{
			PassesUsed: 2,
			MaxPasses:  8,
			Verified:   true,
			Summary:    "Verified all requirements",
			Deliverables: []model.GoalDeliverable{
				{Requirement: "Unit tests", Evidence: "go test passed", Verified: true},
			},
		})
	}); err != nil {
		t.Fatalf("store.UpdateTask: %v", err)
	}

	detail, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Task.GoalAssumptions) != 1 || detail.Task.GoalAssumptions[0].Topic != "Storage format" {
		t.Fatalf("GoalAssumptions = %+v", detail.Task.GoalAssumptions)
	}
	if detail.Task.GoalScorecard == nil || !detail.Task.GoalScorecard.Verified || detail.Task.GoalScorecard.PassesUsed != 2 {
		t.Fatalf("GoalScorecard = %+v", detail.Task.GoalScorecard)
	}

	// Live update and disable GoalMode via UpdateTask.
	updated, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{
		GoalMode: &model.GoalModeConfig{
			Enabled:          true,
			MaxPasses:        4,
			WallClockMinutes: 45,
		},
	})
	if err != nil {
		t.Fatalf("UpdateTask modify: %v", err)
	}
	if updated.GoalMode == nil || updated.GoalMode.MaxPasses != 4 || updated.GoalMode.WallClockMinutes != 45 {
		t.Fatalf("updated.GoalMode = %+v", updated.GoalMode)
	}

	disabled, err := c.UpdateTask(ctx, created.ID, ui.UpdateTaskRequest{
		GoalMode: &model.GoalModeConfig{Enabled: false},
	})
	if err != nil {
		t.Fatalf("UpdateTask disable: %v", err)
	}
	if disabled.GoalMode != nil {
		t.Fatalf("expected GoalMode to be nil after disabling, got %+v", disabled.GoalMode)
	}
}
