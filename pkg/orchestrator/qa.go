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

package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
)

// resolveTaskQAAssignment resolves and persists the sticky work-tree QA arm for task
// when QA is enabled in grain_config. When QA is disabled (the default), it returns
// QAArmOff immediately without writing to the database.
func resolveTaskQAAssignment(
	ctx context.Context,
	store *model.Store,
	cfg Config,
	task model.Task,
	runID string,
	now time.Time,
) (model.QAAssignment, error) {
	qaCfg := cfg.QA
	if qaCfg.IsZero() && store != nil {
		mc, err := store.GetConfig(ctx)
		if err != nil {
			return model.QAAssignment{}, fmt.Errorf("orchestrator: reading deployment config for qa: %w", err)
		}
		if mc != nil {
			qaCfg = mc.QA
		}
	}
	rootOrigin := qa.ResolveRootOrigin(string(task.WorkOrigin), task.ID)
	tier := qa.ClassifyTaskComplexity(task.Title, task.Body)
	if !qaCfg.Enabled || store == nil {
		return qa.AssignOrLoadArm(ctx, store, rootOrigin, tier, qaCfg, now)
	}

	var assigned model.QAAssignment
	_, assignErr := cfg.QAProfiler.Measure(qa.PerfSample{
		TaskID:  task.ID,
		RunID:   runID,
		Stage:   "S0",
		CheckID: "arm-assign",
	}, func() (string, error) {
		a, err := qa.AssignOrLoadArm(ctx, store, rootOrigin, tier, qaCfg, now)
		if err != nil {
			return "error", err
		}
		assigned = a
		return a.Arm, nil
	})
	return assigned, assignErr
}
