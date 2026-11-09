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
)

// seedDemoTokenUsage gives Settings → Models' token usage card and the
// task timeline something to show (docs/connections-and-models.md "Token
// usage and budgets"): usage for every finished demo run, on the demo's
// one connection, split between its two models; a configured budget for
// gemini-2.5-pro and a capacity learned for gemini-2.5-flash.
func seedDemoTokenUsage(ctx context.Context, store *model.Store, now time.Time) error {
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return err
	}
	i := 0
	for _, t := range tasks {
		runs, err := store.Runs(ctx, t.ID)
		if err != nil {
			return err
		}
		for _, r := range runs {
			if r.FinishedAt == nil {
				continue
			}
			i++
			m := "gemini-2.5-pro"
			if i%3 == 0 {
				m = "gemini-2.5-flash"
			}
			// Spread across the last few hours so the window and the
			// burn rate both have something in them.
			end := now.Add(-time.Duration(i%10) * 25 * time.Minute)
			if err := store.RecordRunUsage(ctx, model.RunUsage{
				RunID: r.ID, TaskID: t.ID, Connection: "gemini-pro", Team: model.DefaultTeamID, Model: m,
				Framework:   model.AgentFrameworkAntigravity,
				InputTokens: int64(40000 + 9000*(i%7)), OutputTokens: int64(6000 + 1500*(i%5)),
				CacheReadTokens: int64(300000 + 50000*(i%4)), Turns: 10 + i%9,
				StartedAt: end.Add(-35 * time.Minute), FinishedAt: end,
			}); err != nil {
				return err
			}
		}
	}
	// And a morning's worth of runs no demo task kept, so the window,
	// the burn rate and the estimate have something to work from.
	if len(tasks) > 0 {
		for j := 0; j < 14; j++ {
			m := "gemini-2.5-pro"
			if j%3 == 0 {
				m = "gemini-2.5-flash"
			}
			end := now.Add(-time.Duration(j) * 20 * time.Minute)
			if err := store.RecordRunUsage(ctx, model.RunUsage{
				RunID: fmt.Sprintf("demo-usage-%d", j), TaskID: tasks[j%len(tasks)].ID, Connection: "gemini-pro", Team: model.DefaultTeamID,
				Model: m, Framework: model.AgentFrameworkAntigravity,
				InputTokens: int64(60000 + 11000*(j%6)), OutputTokens: int64(8000 + 2000*(j%4)),
				CacheReadTokens: int64(400000 + 60000*(j%5)), Turns: 12 + j%7,
				StartedAt: end.Add(-40 * time.Minute), FinishedAt: end,
			}); err != nil {
				return err
			}
		}
	}
	if err := store.RecordUsageCapacity(ctx, model.UsageCapacity{
		Connection: "gemini-pro", Model: "gemini-2.5-flash", Tokens: 900000, Window: 5 * time.Hour,
		ObservedAt: now.Add(-20 * time.Hour), ResetAt: now.Add(-15 * time.Hour),
	}); err != nil {
		return err
	}
	return store.PutUsageSettings(ctx, model.UsageSettings{
		Budgets: []model.TokenBudget{{Connection: "gemini-pro", Model: "gemini-2.5-pro", Tokens: 4000000, Window: "5h"}},
	}, now)
}
