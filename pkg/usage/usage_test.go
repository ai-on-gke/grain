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

package usage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// run is a one-hour run of conn/m that finished ago before now and spent
// tokens billable tokens.
func run(id, conn, m string, ago time.Duration, tokens int64) model.RunUsage {
	end := now.Add(-ago)
	return model.RunUsage{RunID: id, Connection: conn, Model: m, InputTokens: tokens, StartedAt: end.Add(-time.Hour), FinishedAt: end}
}

func budget(conn, m string, tokens int64, window string) model.TokenBudget {
	return model.TokenBudget{Connection: conn, Model: m, Tokens: tokens, Window: window}
}

func scope(t *testing.T, rep Report, conn, m string) Scope {
	t.Helper()
	for _, s := range rep.Scopes {
		if s.Connection == conn && s.Model == m {
			return s
		}
	}
	t.Fatalf("no scope %s/%s in %+v", conn, m, rep.Scopes)
	return Scope{}
}

func TestComputeBudgetBurnAndSustainableWorkers(t *testing.T) {
	// Each run spends 100k tokens in an hour: 100k tokens per worker-hour.
	// 1M over 10h; 400k used, 600k left over a 10h horizon = 0.6 workers...
	// so use a 2M budget: 1.6M left / (100k * 10h) = 1.6 workers.
	in := Input{
		Now:        now,
		MaxWorkers: 6,
		Runs: []model.RunUsage{
			run("a", "conn", "opus", 30*time.Minute, 100000),
			run("b", "conn", "opus", 2*time.Hour, 100000),
			run("c", "conn", "opus", 3*time.Hour, 100000),
			run("d", "conn", "opus", 4*time.Hour, 100000),
			run("old", "conn", "opus", 11*time.Hour, 100000), // outside the window
		},
		Settings:      model.UsageSettings{Budgets: []model.TokenBudget{budget("conn", "opus", 2000000, "10h")}},
		DefaultModels: []string{"opus", "gemini"},
	}
	rep := Compute(in)
	s := scope(t, rep, "conn", "opus")
	if s.Used != 400000 || s.Runs != 4 || s.Remaining != 1600000 || s.LimitSource != "configured" {
		t.Fatalf("scope = %+v", s)
	}
	if s.BurnPerHour != 100000 {
		t.Fatalf("burn = %v, want 100000/h", s.BurnPerHour)
	}
	if s.RunsOutAt == nil || !s.RunsOutAt.Equal(now.Add(16*time.Hour)) {
		t.Fatalf("runsOutAt = %v, want now+16h", s.RunsOutAt)
	}
	if s.SustainableWorkers == nil || *s.SustainableWorkers != 1.6 {
		t.Fatalf("sustainable = %v, want 1.6", s.SustainableWorkers)
	}
	if rep.SustainableWorkers == nil || *rep.SustainableWorkers != 1 {
		t.Fatalf("deployment sustainable = %v, want 1", rep.SustainableWorkers)
	}
	// Advisory mode changes nothing about dispatch.
	if rep.Mode != model.UsageModeAdvisory || rep.EffectiveWorkers != 6 {
		t.Fatalf("mode %s effective %d, want advisory and 6", rep.Mode, rep.EffectiveWorkers)
	}
	if len(rep.Advisories) == 0 || !strings.Contains(rep.Advisories[0], "conn's opus budget runs out in ~16h; sustainable ≈ 1.6 workers") {
		t.Fatalf("advisories = %q", rep.Advisories)
	}
}

func TestAutoCapStaysWithinBounds(t *testing.T) {
	base := Input{
		Now:  now,
		Runs: []model.RunUsage{run("a", "conn", "opus", 10*time.Minute, 100000)},
	}
	cases := []struct {
		name       string
		budget     int64
		maxWorkers int
		minWorkers int
		want       int
	}{
		// 100k tokens/worker-hour over a 5h window: 500k per worker.
		{"estimate inside the bounds", 100000 + 3*500000, 6, 1, 3},
		{"estimate above max_workers is held at max_workers", 100000 + 50*500000, 6, 1, 6},
		{"exhausted budget is held at the floor", 100000, 6, 2, 2},
		{"a floor above max_workers never exceeds max_workers", 100000, 4, 9, 4},
		{"zero floor reads as one", 100000, 6, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			in.MaxWorkers = c.maxWorkers
			in.Settings = model.UsageSettings{Mode: model.UsageModeAuto, MinWorkers: c.minWorkers,
				Budgets: []model.TokenBudget{budget("conn", "opus", c.budget, "5h")}}
			rep := Compute(in)
			if rep.EffectiveWorkers != c.want {
				t.Fatalf("effective = %d (sustainable %v), want %d", rep.EffectiveWorkers, rep.SustainableWorkers, c.want)
			}
			if rep.EffectiveWorkers > c.maxWorkers {
				t.Fatalf("effective %d exceeds max_workers %d", rep.EffectiveWorkers, c.maxWorkers)
			}
		})
	}
}

func TestAutoCapRecoversAfterReset(t *testing.T) {
	// Ten one-hour runs of 300k tokens each spent the window's 3M and the
	// provider refused it; the pause ends in 2h, and grain learned the
	// window's capacity from it.
	resetAt := now.Add(2 * time.Hour)
	var runs []model.RunUsage
	for i := 0; i < 10; i++ {
		runs = append(runs, run(fmt.Sprintf("r%d", i), "conn", "opus", time.Duration(i)*25*time.Minute, 300000))
	}
	in := Input{
		Now: now, MaxWorkers: 6, Runs: runs,
		Settings:   model.UsageSettings{Mode: model.UsageModeAuto, MinWorkers: 1},
		Capacities: []model.UsageCapacity{{Connection: "conn", Model: "opus", Tokens: 3000000, Window: 5 * time.Hour, ObservedAt: now, ResetAt: resetAt}},
		Pauses:     []model.UsageLimitPause{{Connection: "conn", Model: "opus", Until: resetAt}},
	}
	rep := Compute(in)
	s := scope(t, rep, "conn", "opus")
	if s.PausedUntil == nil || s.Remaining != 0 || s.SustainableWorkers == nil || *s.SustainableWorkers != 0 {
		t.Fatalf("paused scope = %+v", s)
	}
	if rep.EffectiveWorkers != 1 {
		t.Fatalf("effective while paused = %d, want the floor 1", rep.EffectiveWorkers)
	}

	// Past the reset: the pause has ended, the usage before the reset no
	// longer counts, and the cap comes back up to what a fresh window
	// sustains: 3M / (300k per worker-hour x 5h) = 2 workers.
	in.Now = resetAt.Add(time.Minute)
	in.Pauses = nil
	rep = Compute(in)
	s = scope(t, rep, "conn", "opus")
	if s.Used != 0 || s.Remaining != 3000000 || s.LimitSource != "learned" {
		t.Fatalf("scope after reset = %+v", s)
	}
	if rep.EffectiveWorkers != 2 {
		t.Fatalf("effective after reset = %d (sustainable %v), want 2", rep.EffectiveWorkers, rep.SustainableWorkers)
	}

	// And with a larger learned window it recovers all the way to
	// max_workers, never past it.
	in.Capacities[0].Tokens = 300000000
	rep = Compute(in)
	if rep.EffectiveWorkers != 6 {
		t.Fatalf("effective with a large window = %d, want max_workers 6", rep.EffectiveWorkers)
	}
}

func TestUnbudgetedConnectionLeavesTheCapAlone(t *testing.T) {
	in := Input{
		Now: now, MaxWorkers: 6,
		Runs: []model.RunUsage{
			run("a", "conn", "opus", 10*time.Minute, 100000),
			run("b", "other", "gemini", 10*time.Minute, 100000),
		},
		Settings: model.UsageSettings{Mode: model.UsageModeAuto, Budgets: []model.TokenBudget{budget("conn", "opus", 100000, "5h")}},
	}
	rep := Compute(in)
	if rep.SustainableWorkers != nil || rep.EffectiveWorkers != 6 {
		t.Fatalf("sustainable %v effective %d: an unbudgeted connection in use must leave the cap at max_workers", rep.SustainableWorkers, rep.EffectiveWorkers)
	}
}

func TestOffModeComputesNoEstimate(t *testing.T) {
	in := Input{Now: now, MaxWorkers: 6, Runs: []model.RunUsage{run("a", "conn", "opus", 10*time.Minute, 100)},
		Settings: model.UsageSettings{Mode: model.UsageModeOff, Budgets: []model.TokenBudget{budget("conn", "opus", 100, "5h")}}}
	rep := Compute(in)
	if len(rep.Scopes) != 0 || rep.EffectiveWorkers != 6 || rep.Rollup.Runs != 1 {
		t.Fatalf("off report = %+v", rep)
	}
}

func TestConnectionWideBudgetCoversItsModels(t *testing.T) {
	in := Input{
		Now: now, MaxWorkers: 10,
		Runs: []model.RunUsage{
			run("a", "conn", "opus", 10*time.Minute, 100000),
			run("b", "conn", "sonnet", 10*time.Minute, 100000),
		},
		Settings: model.UsageSettings{Mode: model.UsageModeAuto, Budgets: []model.TokenBudget{budget("conn", "", 200000+2*500000, "5h")}},
	}
	rep := Compute(in)
	s := scope(t, rep, "conn", "")
	if s.Used != 200000 || s.Runs != 2 {
		t.Fatalf("connection scope = %+v", s)
	}
	if rep.SustainableWorkers == nil || *rep.SustainableWorkers != 2 || rep.EffectiveWorkers != 2 {
		t.Fatalf("sustainable %v effective %d, want 2", rep.SustainableWorkers, rep.EffectiveWorkers)
	}
}

func TestWarnsBeforeADefaultModelRunsOut(t *testing.T) {
	// A learned window resetting in 4h, burning 100k/h with 150k left:
	// it runs out in 1.5h, before the reset.
	in := Input{
		Now: now, MaxWorkers: 6,
		Runs:          []model.RunUsage{run("a", "conn", "opus", 10*time.Minute, 100000)},
		Capacities:    []model.UsageCapacity{{Connection: "conn", Model: "opus", Tokens: 250000, Window: 5 * time.Hour, ResetAt: now.Add(4 * time.Hour)}},
		DefaultModels: []string{"Opus", "gemini"},
	}
	rep := Compute(in)
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "default model opus on conn runs out in ~2h, before its reset; next on the default list: gemini") {
		t.Fatalf("warnings = %q", rep.Warnings)
	}
}
