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

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// DemoCIWaitTaskTitle is the title of the demo's task resting on a
// resume_later CI wait.
const DemoCIWaitTaskTitle = "Bump the widgets build to Go 1.27"

// seedDemoCIWait files a task whose run stopped with resume_later(when:
// {ci: "my-branch"}) two hours ago -- past ui.CIWaitHealthAge, short of
// the orchestrator's backstop -- so GET /api/sandboxes' ciWaits, `grain
// health` and /next/system's CI waits card have a wait to show. It is
// written the way a live run writes one (HoldResumeOn while the run is
// live, then its finish and requested suspension), and is seeded last, so
// every task id before it stays what the demo tests pin.
func seedDemoCIWait(ctx context.Context, store *model.Store,
	create func(time.Time, ui.CreateTaskRequest) (ui.Task, error), ago func(time.Duration) time.Time) error {
	task, err := create(ago(3*time.Hour), ui.CreateTaskRequest{
		Title:       DemoCIWaitTaskTitle,
		Description: "Move go.mod and the CI image to Go 1.27 and fix whatever the new vet checks find.",
		Repo:        "acme/widgets",
		Approved:    true,
	})
	if err != nil {
		return err
	}
	runID := "demo-run-" + task.ID
	started := ago(2*time.Hour + 20*time.Minute)
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: "demo-sandbox-ci-wait",
		Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		return fmt.Errorf("seeding a CI wait: %w", err)
	}
	deferredAt := ago(2 * time.Hour)
	when := model.ResumeOn{CI: model.ResumeOnMyBranch}
	if err := store.SetDeferred(ctx, task.ID, &deferredAt, nil, deferredAt); err != nil {
		return fmt.Errorf("seeding a CI wait: %w", err)
	}
	if err := store.HoldResumeOn(ctx, task.ID, when, deferredAt); err != nil {
		return fmt.Errorf("seeding a CI wait: %w", err)
	}
	finished := deferredAt.Add(10 * time.Second)
	if err := store.FinishRun(ctx, runID, finished, "succeeded", "the run made 31 tool call(s)"); err != nil {
		return fmt.Errorf("seeding a CI wait: %w", err)
	}
	if err := store.Suspend(ctx, model.Suspension{
		RunID: runID, Kind: model.SuspendRequested,
		Detail:   model.RequestedDetail("pushed the Go 1.27 bump; waiting for CI on it", when),
		ResumeOn: when.JSON(), CreatedAt: finished,
	}); err != nil {
		return fmt.Errorf("seeding a CI wait: %w", err)
	}
	return nil
}
