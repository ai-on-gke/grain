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

package demo

// The stalled-tasks demo is the fixture behind the Inbox's "Look at a
// stuck task" group (InboxPage.jsx's "stalled"): the one group the
// ordinary demo can never show, because a task's stall note
// (ui.Task.Stall) is only written when the UI is handed the
// orchestrator's deadlines (ui.Config.Stall), and `grain demo` is not.
// With -stalled-tasks it is, as cmd/grain's daemon is, and two tasks are
// seeded past their patience: a queued task nothing has dispatched for
// days, whose row offers Run now, and a run no process has finished in
// two days, whose row offers Cancel. Opt-in, so the ordinary demo's
// Inbox, which other specs count, is unchanged.

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const (
	demoStalledQueuedTitle  = "Backfill the audit log retention job"
	demoStalledRunningTitle = "Regenerate the protobuf bindings"
)

// stalledTasksConfig hands cfg the orchestrator's own task-lane
// deadlines, which is all a stall note is read from.
func stalledTasksConfig(cfg ui.Config) ui.Config {
	cfg.Stall = orchestrator.TaskStallNote
	return cfg
}

func seedStalledTasksDemo(ctx context.Context, store *model.Store, cfg ui.Config) error {
	client := ui.NewClient(cfg, store)
	now := time.Now().UTC()
	create := func(at time.Time, title string) (ui.Task, error) {
		client.Now = func() time.Time { return at }
		created, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title:       title,
			Description: "Seeded by grain demo -stalled-tasks.",
			Approved:    true,
		})
		if err != nil {
			return ui.Task{}, fmt.Errorf("seeding %q: %w", title, err)
		}
		return created, nil
	}
	// Approved four days ago and never dispatched: past the 72h a queued
	// task is given (orchestrator's dispatchPatience).
	if _, err := create(now.Add(-96*time.Hour), demoStalledQueuedTitle); err != nil {
		return err
	}
	// A live run started two days ago: past the longest clock a run gets
	// (orchestrator's runPatience), so its process is taken to be gone.
	running, err := create(now.Add(-50*time.Hour), demoStalledRunningTitle)
	if err != nil {
		return err
	}
	runID := "demo-run-" + running.ID
	if err := store.StartRun(ctx, model.Run{
		ID:        runID,
		TaskID:    running.ID,
		Sandbox:   "demo-sandbox-stalled",
		Attempt:   1,
		StartedAt: now.Add(-48 * time.Hour),
	}, model.Limits{}); err != nil {
		return fmt.Errorf("seeding %q: %w", demoStalledRunningTitle, err)
	}
	return nil
}
