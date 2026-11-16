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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

const weeklyReason = "claude: usage limit reached; You've hit your weekly limit · resets Nov 8, 3am (UTC)"

func TestPauseHorizon(t *testing.T) {
	for reason, want := range map[string]string{
		weeklyReason: HorizonWeekly,
		"claude: usage limit reached; You've hit your Opus weekly limit": HorizonWeekly,
		"gemini: quota exceeded for 7-day requests":                      HorizonWeekly,
		"claude: usage limit reached; You've hit your session limit":     HorizonInterval,
		"": HorizonInterval,
	} {
		if got := PauseHorizon(model.UsageLimitPause{Reason: reason}); got != want {
			t.Errorf("PauseHorizon(%q) = %q, want %q", reason, got, want)
		}
	}
}

// The task-5618 report: nothing ran in the last 5h, so the interval
// reads 0 used, while the week's quota is spent and the connection is
// paused on it. The scope must say so rather than show a fresh window.
func TestWeeklyPauseIsReportedAsWeeklyExhaustion(t *testing.T) {
	until := now.Add(3*24*time.Hour + 15*time.Hour)
	in := Input{
		Now: now, MaxWorkers: 6,
		Runs: []model.RunUsage{
			run("a", "conn", "opus", 2*24*time.Hour, 3000000),
			run("b", "conn", "opus", 3*24*time.Hour, 2000000),
			run("old", "conn", "opus", 8*24*time.Hour, 9000000), // outside the week
		},
		Pauses:        []model.UsageLimitPause{{Connection: "conn", Until: until, Reason: weeklyReason}},
		DefaultModels: []string{"opus"},
	}
	rep := Compute(in)
	s := scope(t, rep, "conn", "opus")
	if s.Used != 0 || s.Runs != 0 {
		t.Fatalf("interval = %d tokens over %d runs, want none in the last 5h", s.Used, s.Runs)
	}
	if s.WeeklyUsed != 5000000 || s.WeeklyRuns != 2 || !s.WeeklyWindowStart.Equal(now.Add(-WeeklyWindow)) {
		t.Fatalf("weekly = %d tokens over %d runs from %v, want 5M over 2 from a week ago", s.WeeklyUsed, s.WeeklyRuns, s.WeeklyWindowStart)
	}
	if s.PauseHorizon != HorizonWeekly || s.WeeklyPausedUntil == nil || !s.WeeklyPausedUntil.Equal(until) ||
		s.WeeklyResetAt == nil || !s.WeeklyResetAt.Equal(until) || s.Remaining != 0 || s.WeeklyRemaining != 0 {
		t.Fatalf("scope = %+v, want a weekly pause until %v", s, until)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "default model opus on conn hit its weekly limit and is paused until Nov 9 03:00 UTC") {
		t.Fatalf("warnings = %q", rep.Warnings)
	}
}

func TestIntervalPauseNamesTheIntervalLimit(t *testing.T) {
	in := Input{
		Now: now, MaxWorkers: 6,
		Runs:          []model.RunUsage{run("a", "conn", "opus", time.Hour, 100)},
		Pauses:        []model.UsageLimitPause{{Connection: "conn", Model: "opus", Until: now.Add(2 * time.Hour), Reason: "claude: usage limit reached; You've hit your session limit"}},
		DefaultModels: []string{"opus", "sonnet"},
	}
	rep := Compute(in)
	s := scope(t, rep, "conn", "opus")
	if s.PauseHorizon != HorizonInterval || s.WeeklyPausedUntil != nil || s.WeeklyUsed != 100 {
		t.Fatalf("scope = %+v, want an interval pause and the run in the week", s)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0] != "default model opus on conn hit its 5h interval limit and is paused until 14:00 UTC; work falls back to sonnet" {
		t.Fatalf("warnings = %q", rep.Warnings)
	}
}

// A learned weekly capacity sits beside the interval's: each bounds its
// own horizon, and the tighter of the two bounds sustainable workers.
func TestLearnedWeeklyCapacityBoundsTheScope(t *testing.T) {
	in := Input{
		Now: now, MaxWorkers: 20,
		Runs: []model.RunUsage{
			run("a", "conn", "opus", 30*time.Minute, 100000),
			run("b", "conn", "opus", 2*24*time.Hour, 800000),
		},
		Settings: model.UsageSettings{Mode: model.UsageModeAuto},
		Capacities: []model.UsageCapacity{
			{Connection: "conn", Model: "opus", Tokens: 50000000, Window: DefaultWindow},
			{Connection: "conn", Model: "opus" + weeklyCapacitySuffix, Tokens: 2000000, Window: WeeklyWindow, ResetAt: now.Add(-6 * 24 * time.Hour)},
		},
		DefaultModels: []string{"opus"},
	}
	rep := Compute(in)
	if len(rep.Scopes) != 1 {
		t.Fatalf("scopes = %+v, want the weekly capacity on opus's own scope", rep.Scopes)
	}
	s := rep.Scopes[0]
	if s.Limit != 50000000 || s.WeeklyLimit != 2000000 || s.WeeklyLimitSource != "learned" || s.WeeklyUsed != 900000 || s.WeeklyRemaining != 1100000 {
		t.Fatalf("scope = %+v", s)
	}
	// The weekly reset 6 days ago puts the next one a day away. 1.1M left
	// of the week at 100k per worker-hour (the last day's run; the
	// average does not reach back a week) over that day is 0.5 workers,
	// far below the interval's estimate.
	if s.WeeklyResetAt == nil || !s.WeeklyResetAt.Equal(now.Add(24*time.Hour)) || !s.WeeklyWindowStart.Equal(now.Add(-6*24*time.Hour)) {
		t.Fatalf("weekly reset %v from %v, want now+1d from now-6d", s.WeeklyResetAt, s.WeeklyWindowStart)
	}
	if s.SustainableWorkers == nil || *s.SustainableWorkers != 0.5 {
		t.Fatalf("sustainable = %v, want the weekly bound 0.5", s.SustainableWorkers)
	}
	if s.AvgTokensPerRun != 100000 {
		t.Fatalf("avg tokens per run = %v, want the last day's 100k", s.AvgTokensPerRun)
	}
	if s.WeeklyRunsOutAt == nil || !s.WeeklyRunsOutAt.Equal(now.Add(11*time.Hour)) {
		t.Fatalf("weekly runs out at %v, want now+11h", s.WeeklyRunsOutAt)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "runs out of its weekly limit in ~11h, before its weekly reset") {
		t.Fatalf("warnings = %q", rep.Warnings)
	}
	// A weekly reset inside the week drops what came before it.
	in.Capacities[1].ResetAt = now.Add(-24 * time.Hour)
	if s := Compute(in).Scopes[0]; s.WeeklyUsed != 100000 || !s.WeeklyWindowStart.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("after a weekly reset: %+v", s)
	}
}

// The interval running out first does not silence the week when the
// interval resets before it runs out: only a horizon that warns competes.
func TestWeeklyRunOutWarnsWhenTheIntervalResetsFirst(t *testing.T) {
	in := Input{
		Now: now, MaxWorkers: 6,
		Runs: []model.RunUsage{run("a", "conn", "opus", 30*time.Minute, 1000000)},
		Capacities: []model.UsageCapacity{
			{Connection: "conn", Model: "opus", Tokens: 5000000, Window: DefaultWindow, ResetAt: now.Add(time.Hour)},
			{Connection: "conn", Model: "opus" + weeklyCapacitySuffix, Tokens: 21000000, Window: WeeklyWindow, ResetAt: now.Add(3 * 24 * time.Hour)},
		},
		DefaultModels: []string{"opus"},
	}
	rep := Compute(in)
	s := rep.Scopes[0]
	// Interval: 4M left at 1M/h runs out in 4h, after its reset in 1h.
	// Week: 20M left runs out in 20h, days before its reset.
	if s.RunsOutAt == nil || !s.RunsOutAt.Equal(now.Add(4*time.Hour)) || s.WeeklyRunsOutAt == nil || !s.WeeklyRunsOutAt.Equal(now.Add(20*time.Hour)) {
		t.Fatalf("runs out at %v, weekly %v", s.RunsOutAt, s.WeeklyRunsOutAt)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "runs out of its weekly limit in ~20h, before its weekly reset") {
		t.Fatalf("warnings = %q", rep.Warnings)
	}
}

// Raising the lookback to a week for the weekly horizon leaves the
// interval's per-run averages, and so its estimate, on the last day.
func TestWeekOldRunsDoNotMoveTheIntervalEstimate(t *testing.T) {
	recent := []model.RunUsage{run("a", "conn", "opus", 30*time.Minute, 100000)}
	in := Input{
		Now: now, MaxWorkers: 20, Runs: recent,
		Settings: model.UsageSettings{Budgets: []model.TokenBudget{budget("conn", "opus", 1000000, "5h")}},
	}
	before := Compute(in)
	in.Runs = append(append([]model.RunUsage{}, recent...), run("old", "conn", "opus", 3*24*time.Hour, 5000000))
	after := Compute(in)
	b, a := before.Scopes[0], after.Scopes[0]
	if *a.SustainableWorkers != *b.SustainableWorkers || a.AvgTokensPerRun != b.AvgTokensPerRun || after.Rollup.Runs != 1 {
		t.Fatalf("a 3-day-old run moved the interval: sustainable %v -> %v, avg %v -> %v, rollup %d runs",
			*b.SustainableWorkers, *a.SustainableWorkers, b.AvgTokensPerRun, a.AvgTokensPerRun, after.Rollup.Runs)
	}
	if a.WeeklyUsed != 5100000 {
		t.Fatalf("weekly used = %d, want the old run counted in the week", a.WeeklyUsed)
	}
}
