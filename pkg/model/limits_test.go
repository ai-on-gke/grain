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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The whole of the rule the three numbers make, as a table: a team's
// agent and CI limits are independent of each other.
func TestLimitsKeepAgentAndCIConcurrencyIndependent(t *testing.T) {
	for _, c := range []struct {
		name   string
		limits model.Limits
		agents int
		ci     int
	}{
		{
			name:   "no CI adds nothing",
			limits: model.Limits{Workers: 4, Mergers: 2},
			agents: 6,
			ci:     0,
		}, {
			name:   "CI is additional capacity",
			limits: model.Limits{Workers: 4, Mergers: 2, CIJobs: 2},
			agents: 6,
			ci:     2,
		}, {
			name:   "CI may exceed agent capacity",
			limits: model.Limits{Workers: 2, CIJobs: 5},
			agents: 2,
			ci:     5,
		}, {
			name:   "negative CI capacity is defensive zero",
			limits: model.Limits{Workers: 1, CIJobs: -1},
			agents: 1,
			ci:     0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.limits.AgentRuns(); got != c.agents {
				t.Errorf("AgentRuns = %d, want %d", got, c.agents)
			}
			if got := c.limits.CILimit(); got != c.ci {
				t.Errorf("CILimit = %d, want %d", got, c.ci)
			}
		})
	}
}

// CIJobs changes CI's gate and never silently reduces the worker and
// merger limits an operator configured.
func TestConsoleSessionsDoNotReduceOrExpandOrdinaryCapacity(t *testing.T) {
	limits := model.Limits{Workers: 2, Mergers: 1}
	full := model.RunCounts{Workers: 2, Mergers: 1}

	withSessions := full.AddDirector().AddDirector().AddDirector()
	if limits.Admits(withSessions, false) || limits.Admits(withSessions, true) {
		t.Error("live console sessions changed whether ordinary work fits the worker and merger limits")
	}
	if !limits.Admits(model.RunCounts{Workers: 1}.AddDirector().AddDirector(), false) {
		t.Error("live console sessions consumed an ordinary worker slot")
	}
	if withSessions.Total() != limits.AgentRuns()+3 {
		t.Errorf("live total = %d, ordinary ceiling plus three sessions = %d",
			withSessions.Total(), limits.AgentRuns()+3)
	}
}

func TestCILimitDoesNotReduceAgentCapacity(t *testing.T) {
	limits := model.Limits{Workers: 3, Mergers: 2, CIJobs: 2}

	if !limits.Admits(model.RunCounts{Workers: 2}, false) {
		t.Error("a third worker should be admitted")
	}
	if !limits.Admits(model.RunCounts{Workers: 2}, true) {
		t.Error("a merger should be admitted beside two workers")
	}
	if !limits.Admits(model.RunCounts{Workers: 3}, true) ||
		!limits.Admits(model.RunCounts{Workers: 3, Mergers: 1}, true) {
		t.Error("CI capacity reduced the five agent slots")
	}
	if limits.Admits(model.RunCounts{Workers: 3, Mergers: 2}, true) {
		t.Error("Workers+Mergers remains the agent ceiling")
	}
}

// Limits that bound nothing are the zero value a caller with no agent
// limit to enforce passes. A CI limit alone must not acquire an agent
// limit by the back door.
func TestUnlimitedAgentLimitsStayUnlimitedWithACILimit(t *testing.T) {
	var none model.Limits
	if !none.Unlimited() || none.CILimit() != 0 {
		t.Fatalf("the zero value limits nothing: %+v", none)
	}
	if !none.Admits(model.RunCounts{Workers: 99}, false) {
		t.Error("limits that bound nothing admit anything")
	}

	ci := model.Limits{CIJobs: 3}
	if !ci.Unlimited() {
		t.Error("a CI limit alone is not a bound on agent runs")
	}
	if got := ci.CILimit(); got != 3 {
		t.Errorf("CILimit = %d, want 3", got)
	}
	if !ci.Admits(model.RunCounts{Workers: 99}, false) {
		t.Error("a CI limit must not start bounding agent runs")
	}
}

func TestTaskShapeAppliesPerTaskOverridesIncludingDisk(t *testing.T) {
	limits := model.Limits{
		AgentShape: model.Commitment{CPUs: 4, MemoryMB: 8192, DiskGB: 30},
	}
	if got := limits.TaskShape(model.Task{}); got != limits.AgentShape {
		t.Errorf("TaskShape(default) = %+v, want %+v", got, limits.AgentShape)
	}
	override := model.Task{SandboxCPUs: 8, SandboxMemoryMB: 16384, SandboxDiskGB: 60}
	want := model.Commitment{CPUs: 8, MemoryMB: 16384, DiskGB: 60}
	if got := limits.TaskShape(override); got != want {
		t.Errorf("TaskShape(override) = %+v, want %+v", got, want)
	}
	if got := (model.Limits{}).TaskShape(override); !got.IsZero() {
		t.Errorf("TaskShape with zero AgentShape = %+v, want zero", got)
	}
}
