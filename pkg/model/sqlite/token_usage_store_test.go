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

package sqlite_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRunUsageRoundTripsAndPrunes(t *testing.T) {
	store, ctx := openStoreIn(t, t.TempDir())
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	runs := []model.RunUsage{
		{RunID: "r1", TaskID: "7", Connection: "conn-a", Model: "Opus-5.5-High", Framework: "claude",
			InputTokens: 100, OutputTokens: 20, CacheReadTokens: 900, CacheWriteTokens: 5, CostUSD: 0.125, Turns: 3,
			StartedAt: t0, FinishedAt: t0.Add(10 * time.Minute)},
		{RunID: "r2", TaskID: "7", Connection: "conn-a", Model: "opus-5.5-high", Framework: "claude",
			InputTokens: 50, OutputTokens: 10, StartedAt: t0.Add(time.Hour), FinishedAt: t0.Add(2 * time.Hour)},
		{RunID: "r3", TaskID: "8", Connection: "conn-b", Model: "gemini", Framework: "antigravity", Hostd: "laptop",
			InputTokens: 1, StartedAt: t0.Add(3 * time.Hour), FinishedAt: t0.Add(4 * time.Hour)},
	}
	for _, r := range runs {
		if err := store.RecordRunUsage(ctx, r); err != nil {
			t.Fatalf("RecordRunUsage(%s): %v", r.RunID, err)
		}
	}
	// Recording the same run again replaces it rather than counting twice.
	if err := store.RecordRunUsage(ctx, runs[0]); err != nil {
		t.Fatal(err)
	}
	got, err := store.TaskRunUsages(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RunID != "r1" || got[0].Model != "opus-5.5-high" || got[0].CostUSD != 0.125 || got[0].Billable() != 125 {
		t.Fatalf("TaskRunUsages = %+v", got)
	}
	since, err := store.RunUsagesSince(ctx, t0.Add(90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 2 || since[1].Hostd != "laptop" {
		t.Fatalf("RunUsagesSince = %+v", since)
	}
	n, err := store.PruneRunUsage(ctx, t0.Add(3*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("PruneRunUsage = %d, %v; want 2", n, err)
	}
}

func TestUsageCapacityAndSettingsRoundTrip(t *testing.T) {
	store, ctx := openStoreIn(t, t.TempDir())
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := store.RecordUsageCapacity(ctx, model.UsageCapacity{Connection: "c", Model: "m", Tokens: 10, Window: 5 * time.Hour, ObservedAt: at, ResetAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsageCapacity(ctx, model.UsageCapacity{Connection: "c", Model: "m", Tokens: 20, Window: 5 * time.Hour, ObservedAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	caps, err := store.ListUsageCapacities(ctx)
	if err != nil || len(caps) != 1 || caps[0].Tokens != 20 || caps[0].Window != 5*time.Hour || !caps[0].ResetAt.IsZero() {
		t.Fatalf("ListUsageCapacities = %+v, %v", caps, err)
	}

	s, err := store.UsageSettings(ctx)
	if err != nil || s.EffectiveMode() != model.UsageModeAdvisory || s.Retention() != 30*24*time.Hour {
		t.Fatalf("default UsageSettings = %+v, %v", s, err)
	}
	want := model.UsageSettings{Mode: "auto", MinWorkers: 2, RetentionDays: 7,
		Budgets: []model.TokenBudget{{Connection: "c", Model: "m", Tokens: 1000, Window: "5h"}}}
	if err := store.PutUsageSettings(ctx, want, at); err != nil {
		t.Fatal(err)
	}
	s, err = store.UsageSettings(ctx)
	if err != nil || s.EffectiveMode() != model.UsageModeAuto || s.MinWorkers != 2 || len(s.Budgets) != 1 || s.Budgets[0].WindowDuration() != 5*time.Hour {
		t.Fatalf("UsageSettings = %+v, %v", s, err)
	}
	if err := store.PutUsageSettings(ctx, model.UsageSettings{Budgets: []model.TokenBudget{{Connection: "c", Tokens: 1, Window: "soon"}}}, at); err == nil {
		t.Fatal("PutUsageSettings accepted a window that is not a duration")
	}
}
