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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The live real-model suites' shape: a .grain/workflows cron in a
// ci_mode: github repository comes due every night and is dropped, so its
// schedule's LastRunAt stops moving. StaleSchedules names it, with why,
// once it is more than twice its cadence old -- and leaves a schedule that
// is firing, a disabled one, and one still inside the window alone.
func TestStaleSchedulesNamesAScheduleThatStoppedRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	daily := model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 7*60 + 37}
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	tmpl := model.WorkflowReviewTemplateID("live-agent")
	for _, s := range []model.Schedule{
		{ID: "workflow-dark", Title: "live-agent", Target: repo, TemplateID: &tmpl, Recurrence: daily, Enabled: true,
			LastRunAt: at(14 * 24 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "never-ran", Title: "never ran", Target: repo, Recurrence: daily, Enabled: true,
			CreatedAt: now.Add(-5 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "firing", Title: "firing", Target: repo, Recurrence: daily, Enabled: true,
			LastRunAt: at(22 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "missed-one", Title: "missed one night", Target: repo, Recurrence: daily, Enabled: true,
			LastRunAt: at(40 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
		{ID: "disabled", Title: "disabled", Target: repo, Recurrence: daily, Enabled: false,
			LastRunAt: at(30 * 24 * time.Hour), CreatedAt: now.Add(-60 * 24 * time.Hour), NextRunAt: now.Add(time.Hour)},
	} {
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatalf("PutSchedule(%s): %v", s.ID, err)
		}
	}

	stale, err := store.StaleSchedules(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range stale {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "never-ran,workflow-dark" {
		t.Fatalf("StaleSchedules = %v, want never-ran (no run since it was created) then workflow-dark", ids)
	}
	dark := stale[1]
	if dark.Workflow != "live-agent" || dark.Cadence != 24*time.Hour {
		t.Errorf("workflow-dark = %+v, want workflow live-agent with a 24h cadence", dark)
	}
	if !strings.Contains(dark.Reason, "github") || !strings.Contains(dark.Reason, ".grain/workflows/live-agent.yml") {
		t.Errorf("workflow-dark's reason = %q, want it to say the repository's CI mode drops .grain/workflows/live-agent.yml", dark.Reason)
	}
}
