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

// DemoDurableTaskTitle is the title of the demo's standing durable task.
const DemoDurableTaskTitle = "Maintain the acme/widgets changelog"

// seedDemoDurableTask files a standing durable task whose context has
// something on every line of the task page's Context panel and brief
// (design/design/durable-tasks.design.md#DT-15): a conversation compacted
// once, so its early comments are summarised; a current brief edited
// twice, standing in for a description that describes finished work;
// and more runs since the handover than HandoverStaleRuns, so it reads
// as stale. It is seeded last, so every task id before it stays what the
// demo tests pin.
func seedDemoDurableTask(ctx context.Context, store *model.Store,
	create func(time.Time, ui.CreateTaskRequest) (ui.Task, error), ago func(time.Duration) time.Time) error {
	task, err := create(ago(20*24*time.Hour), ui.CreateTaskRequest{
		Title: DemoDurableTaskTitle,
		Description: "Stage 1: write `CHANGELOG.md` for acme/widgets from the last two releases' merged pull requests, " +
			"one section per release, grouped into Features, Fixes and Internal.",
		Approved: true,
	})
	if err != nil {
		return err
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Admission = model.AdmissionDurable
		return nil
	}); err != nil {
		return fmt.Errorf("seeding a durable task: %w", err)
	}
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "dana@example.com"}}
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "changelog-agent"}
	byAgent := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, OnBehalfOf: &agent}
	for _, c := range []struct {
		at     time.Duration
		author model.Attribution
		body   string
	}{
		{19 * 24 * time.Hour, byAgent, "Drafted `CHANGELOG.md` for v1.3 and v1.4: 41 pull requests, 6 left out as reverts."},
		{18 * 24 * time.Hour, human, "Looks good. Put the breaking change in v1.4 at the top of its section."},
		{18*24*time.Hour - time.Hour, byAgent, "Done, and opened #212 with it."},
		{17 * 24 * time.Hour, human, "Merged. From now on keep it current: add each release's entry when it is tagged."},
	} {
		if _, err := store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: c.author, Body: c.body, CreatedAt: ago(c.at)}); err != nil {
			return fmt.Errorf("seeding a durable task: %w", err)
		}
	}
	if _, err := store.CompactTask(ctx, task.ID,
		"Stage 1 is done (#212 merged). The task now keeps `CHANGELOG.md` current: on each tag, add the release's entry "+
			"from its merged pull requests, breaking changes first. Reverts are left out.",
		0, human, "dana@example.com", ago(16*24*time.Hour)); err != nil {
		return fmt.Errorf("seeding a durable task: %w", err)
	}
	if err := store.SetTaskBrief(ctx, task.ID,
		"Keep `CHANGELOG.md` current: add each release's entry when it is tagged.",
		"changelog-agent", "stage 1 merged in #212", ago(16*24*time.Hour)); err != nil {
		return fmt.Errorf("seeding a durable task: %w", err)
	}
	if err := store.SetTaskBrief(ctx, task.ID,
		"Keep `CHANGELOG.md` current: add each release's entry when it is tagged, breaking changes first, "+
			"and link every entry to its pull request.",
		"dana@example.com", "entries should link their pull requests", ago(9*24*time.Hour)); err != nil {
		return fmt.Errorf("seeding a durable task: %w", err)
	}
	// A run a day since the handover, one past HandoverStaleRuns.
	for i := model.HandoverStaleRuns + 1; i >= 1; i-- {
		id := fmt.Sprintf("demo-run-%s-%d", task.ID, model.HandoverStaleRuns+2-i)
		started := ago(time.Duration(i) * 24 * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: task.ID, Sandbox: "demo-sandbox-durable",
			Attempt: model.HandoverStaleRuns + 2 - i, StartedAt: started}, model.Limits{}); err != nil {
			return fmt.Errorf("seeding a durable task: %w", err)
		}
		if err := store.FinishRun(ctx, id, started.Add(4*time.Minute), "succeeded",
			"no new tag; nothing to add"); err != nil {
			return fmt.Errorf("seeding a durable task: %w", err)
		}
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: byAgent,
		Body: "v1.5.0 was tagged; added its entry (7 pull requests) in #231.", CreatedAt: ago(36 * time.Hour)}); err != nil {
		return fmt.Errorf("seeding a durable task: %w", err)
	}
	return nil
}
