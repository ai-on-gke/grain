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

package model

import (
	"testing"
	"time"
)

func TestGoalModeLinkKindsDoNotBlock(t *testing.T) {
	for _, k := range []LinkKind{LinkGoalMode, LinkGoalAssumption, LinkGoalScorecard} {
		if k.Blocks() {
			t.Errorf("LinkKind(%q).Blocks() = true, want false", k)
		}
		if k.IsChange() {
			t.Errorf("LinkKind(%q).IsChange() = true, want false", k)
		}
	}
}

func TestNormalizeGoalModeConfig(t *testing.T) {
	disabled := NormalizeGoalModeConfig(GoalModeConfig{
		Enabled:            false,
		MaxPasses:          10,
		WallClockMinutes:   60,
		MockMissingSecrets: true,
	})
	if disabled != (GoalModeConfig{Enabled: false}) {
		t.Fatalf("disabled config = %+v, want zeroed GoalModeConfig", disabled)
	}

	defaults := NormalizeGoalModeConfig(GoalModeConfig{Enabled: true})
	if defaults.MaxPasses != 8 || defaults.WallClockMinutes != 120 || defaults.MockMissingSecrets {
		t.Fatalf("defaults = %+v, want MaxPasses=8 WallClockMinutes=120 MockMissingSecrets=false", defaults)
	}

	clampedLow := NormalizeGoalModeConfig(GoalModeConfig{
		Enabled:          true,
		MaxPasses:        -3,
		WallClockMinutes: 2,
	})
	if clampedLow.MaxPasses != 1 || clampedLow.WallClockMinutes != 5 {
		t.Fatalf("clampedLow = %+v, want MaxPasses=1 WallClockMinutes=5", clampedLow)
	}

	clampedHigh := NormalizeGoalModeConfig(GoalModeConfig{
		Enabled:            true,
		MaxPasses:          99,
		WallClockMinutes:   1440,
		MockMissingSecrets: true,
	})
	if clampedHigh.MaxPasses != 20 || clampedHigh.WallClockMinutes != 720 || !clampedHigh.MockMissingSecrets {
		t.Fatalf("clampedHigh = %+v, want MaxPasses=20 WallClockMinutes=720 MockMissingSecrets=true", clampedHigh)
	}
}

func TestGoalModeRoundTripAndOverwrite(t *testing.T) {
	var task Task
	if _, ok := GoalModeOf(task); ok {
		t.Fatal("expected GoalModeOf on empty task to return false")
	}

	if err := SetGoalMode(&task, &GoalModeConfig{
		Enabled:            true,
		MaxPasses:          5,
		WallClockMinutes:   90,
		MockMissingSecrets: true,
	}); err != nil {
		t.Fatalf("SetGoalMode: %v", err)
	}
	got, ok := GoalModeOf(task)
	if !ok {
		t.Fatal("expected GoalModeOf to return true")
	}
	if !got.Enabled || got.MaxPasses != 5 || got.WallClockMinutes != 90 || !got.MockMissingSecrets {
		t.Fatalf("GoalModeOf = %+v", got)
	}

	// Overwrite replaces existing LinkGoalMode rather than adding a second link.
	if err := SetGoalMode(&task, &GoalModeConfig{
		Enabled:          true,
		MaxPasses:        12,
		WallClockMinutes: 180,
	}); err != nil {
		t.Fatalf("SetGoalMode overwrite: %v", err)
	}
	count := 0
	for _, l := range task.Links {
		if l.Kind == LinkGoalMode {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("LinkGoalMode count = %d, want 1", count)
	}
	got, ok = GoalModeOf(task)
	if !ok || got.MaxPasses != 12 || got.WallClockMinutes != 180 || got.MockMissingSecrets {
		t.Fatalf("GoalModeOf after overwrite = %+v, ok=%v", got, ok)
	}

	// Disabling removes LinkGoalMode.
	if err := SetGoalMode(&task, &GoalModeConfig{Enabled: false}); err != nil {
		t.Fatalf("SetGoalMode disable: %v", err)
	}
	if _, ok := GoalModeOf(task); ok {
		t.Fatal("expected GoalModeOf to return false after disable")
	}
	for _, l := range task.Links {
		if l.Kind == LinkGoalMode {
			t.Fatal("expected LinkGoalMode link to be removed when disabled")
		}
	}
}

func TestGoalAssumptionsAndScorecardRoundTrip(t *testing.T) {
	var task Task
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	if err := AppendGoalAssumption(&task, GoalAssumption{
		Topic:        "Pagination format",
		Decision:     "Use cursor pagination",
		Rationale:    "Matches existing list endpoints",
		Alternatives: "Offset pagination",
		Pass:         1,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("AppendGoalAssumption 1: %v", err)
	}
	// Duplicate topic+decision should be deduplicated.
	if err := AppendGoalAssumption(&task, GoalAssumption{
		Topic:    "Pagination format",
		Decision: "Use cursor pagination",
		Pass:     2,
	}); err != nil {
		t.Fatalf("AppendGoalAssumption duplicate: %v", err)
	}
	if err := AppendGoalAssumption(&task, GoalAssumption{
		Topic:    "Secret handling",
		Decision: "Use local fake client when secret is absent",
		Pass:     2,
	}); err != nil {
		t.Fatalf("AppendGoalAssumption 2: %v", err)
	}

	assumptions := GoalAssumptionsOf(task)
	if len(assumptions) != 2 {
		t.Fatalf("len(GoalAssumptionsOf) = %d, want 2", len(assumptions))
	}
	if assumptions[0].ID != "assumption-1" || assumptions[0].Topic != "Pagination format" {
		t.Fatalf("assumptions[0] = %+v", assumptions[0])
	}
	if assumptions[1].ID != "assumption-2" || assumptions[1].Pass != 2 {
		t.Fatalf("assumptions[1] = %+v", assumptions[1])
	}

	if _, ok := GoalScorecardOf(task); ok {
		t.Fatal("expected no scorecard initially")
	}
	sc1 := GoalScorecard{
		PassesUsed: 1,
		MaxPasses:  8,
		Verified:   false,
		StopReason: "missing complete_goal",
		UpdatedAt:  now,
	}
	if err := SetGoalScorecard(&task, sc1); err != nil {
		t.Fatalf("SetGoalScorecard 1: %v", err)
	}
	sc2 := GoalScorecard{
		PassesUsed: 2,
		MaxPasses:  8,
		Verified:   true,
		Summary:    "Implemented and verified all requirements",
		Deliverables: []GoalDeliverable{
			{Requirement: "Add unit tests", Evidence: "go test ./pkg/model passed", Verified: true},
		},
		UpdatedAt: now.Add(time.Minute),
	}
	if err := SetGoalScorecard(&task, sc2); err != nil {
		t.Fatalf("SetGoalScorecard 2: %v", err)
	}

	gotSC, ok := GoalScorecardOf(task)
	if !ok || gotSC == nil {
		t.Fatal("expected GoalScorecardOf to return true")
	}
	if gotSC.PassesUsed != 2 || !gotSC.Verified || len(gotSC.Deliverables) != 1 {
		t.Fatalf("GoalScorecardOf = %+v", gotSC)
	}
	scLinks := 0
	for _, l := range task.Links {
		if l.Kind == LinkGoalScorecard {
			scLinks++
		}
	}
	if scLinks != 1 {
		t.Fatalf("LinkGoalScorecard count = %d, want 1", scLinks)
	}
}
