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

package metrics_test

// The mid-run pull request loop: who was offered it, who took it, and
// whether taking it went with fewer red builds left behind.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// targeted is a task that had a repo to push to -- the only kind whose
// runs were ever told about open_pull_request at all.
func targeted(id string, fixTask bool) model.TaskTiming {
	return model.TaskTiming{
		TaskID:    id,
		CreatedAt: at(0), ApprovedAt: at(0),
		Targeted: true, FixTaskFiled: fixTask,
	}
}

// runFor is one finished attempt of task, so that a task can have more
// than one and the fix-task link on it be shared between them.
func runFor(id, task string, d time.Duration) model.RunTiming {
	return model.RunTiming{
		RunID: id, TaskID: task, Attempt: 1,
		StartedAt: since.Add(d - time.Hour), FinishedAt: at(d),
		Outcome: "succeeded",
	}
}

func TestPullRequestsAdoptionIsOverRunsThatWereOfferedTheTool(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			targeted("t1", false),
			targeted("t2", false),
			targeted("t3", false),
			// No repo to push to, so BuildPrompt never named the tool:
			// this run belongs in no denominator here.
			{TaskID: "t4", CreatedAt: at(0)},
			targeted("t5", false),
			targeted("t6", false),
		},
		Runs: []model.RunTiming{
			runFor("r1", "t1", 10*time.Hour),
			runFor("r2", "t2", 20*time.Hour),
			runFor("r3", "t3", 30*time.Hour),
			runFor("r4", "t4", 40*time.Hour),
			// Recorded a census, but finished outside the window.
			runFor("r5", "t5", 9*24*time.Hour),
			// Inside the window and recorded nothing at all: a run from
			// before the census, or one that called no tool. Neither
			// decided anything about a pull request.
			runFor("r6", "t6", 50*time.Hour),
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "run_command", Calls: 10},
			{RunID: "r1", Tool: "open_pull_request", Calls: 3},
			{RunID: "r2", Tool: "run_command", Calls: 10},
			{RunID: "r3", Tool: "open_pull_request", Calls: 1},
			{RunID: "r4", Tool: "open_pull_request", Calls: 9},
			{RunID: "r5", Tool: "open_pull_request", Calls: 9},
		},
	})

	pr := rep.PullRequests
	if pr.Runs != 3 {
		t.Errorf("Runs = %d, want 3 -- r1, r2 and r3, the runs in the window "+
			"that recorded a census for a task with a target", pr.Runs)
	}
	if pr.Opened != 2 || pr.Calls != 4 {
		t.Errorf("Opened, Calls = %d, %d; want 2 runs and 4 calls", pr.Opened, pr.Calls)
	}
	if got, want := pr.AdoptionRate, 2.0/3.0; !closeEnough(got, want) {
		t.Errorf("AdoptionRate = %v, want %v", got, want)
	}
}

func TestPullRequestsReportsFixTasksForBothCohorts(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			targeted("t1", true),
			targeted("t2", false),
			targeted("t3", true),
			targeted("t4", true),
		},
		Runs: []model.RunTiming{
			runFor("r1", "t1", 10*time.Hour),
			runFor("r2", "t2", 11*time.Hour),
			runFor("r3", "t3", 12*time.Hour),
			runFor("r4", "t4", 13*time.Hour),
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "open_pull_request", Calls: 2},
			{RunID: "r2", Tool: "open_pull_request", Calls: 1},
			{RunID: "r3", Tool: "run_command", Calls: 5},
			{RunID: "r4", Tool: "run_command", Calls: 5},
		},
	})

	pr := rep.PullRequests
	if pr.WithTool.Runs != 2 || pr.WithTool.FixTasks != 1 {
		t.Errorf("WithTool = %+v, want 2 runs and 1 fix task", pr.WithTool)
	}
	if got, want := pr.WithTool.Rate, 0.5; !closeEnough(got, want) {
		t.Errorf("WithTool.Rate = %v, want %v", got, want)
	}
	if pr.WithoutTool.Runs != 2 || pr.WithoutTool.FixTasks != 2 {
		t.Errorf("WithoutTool = %+v, want 2 runs and 2 fix tasks", pr.WithoutTool)
	}
	if got, want := pr.WithoutTool.Rate, 1.0; !closeEnough(got, want) {
		t.Errorf("WithoutTool.Rate = %v, want %v", got, want)
	}
	// The two cohorts partition the denominator: every run counted above
	// is in exactly one of them, which is what makes the pair a
	// comparison rather than two overlapping samples.
	if pr.WithTool.Runs+pr.WithoutTool.Runs != pr.Runs {
		t.Errorf("cohorts hold %d + %d runs against a denominator of %d",
			pr.WithTool.Runs, pr.WithoutTool.Runs, pr.Runs)
	}
}

// A task's fix-task link is shared by every attempt of it, which is the
// coarseness the section's own doc comment warns about, pinned here so
// that it stays a known property rather than becoming a surprise.
func TestPullRequestsCountsAFixTaskOncePerAttemptOfTheTask(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{targeted("t1", true)},
		Runs: []model.RunTiming{
			runFor("r1", "t1", 10*time.Hour),
			runFor("r2", "t1", 20*time.Hour),
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "open_pull_request", Calls: 1},
			{RunID: "r2", Tool: "open_pull_request", Calls: 1},
		},
	})

	if got := rep.PullRequests.WithTool; got.Runs != 2 || got.FixTasks != 2 {
		t.Errorf("WithTool = %+v, want both attempts of the one task counted", got)
	}
}

// The census stores whatever pkg/mcp called the tool, and this package
// matches that string rather than importing it. A rename on the far side
// leaves this section reading zero, silently -- so the name is pinned
// here, where the failure is a test rather than a metric that quietly
// flatlines.
func TestPullRequestsMatchesTheToolsStoredName(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:   win,
		Tasks:    []model.TaskTiming{targeted("t1", false)},
		Runs:     []model.RunTiming{runFor("r1", "t1", 10*time.Hour)},
		ToolUses: []model.RunToolUse{{RunID: "r1", Tool: "open_pull_request", Calls: 1}},
	})
	if rep.PullRequests.Opened != 1 {
		t.Fatalf("a census row naming open_pull_request was not counted: %+v", rep.PullRequests)
	}
}

// A caller with no census supplies no rows and gets an empty section --
// not a zero adoption rate, which would read as "no run ever calls it"
// where the truth is that nobody measured.
func TestPullRequestsIsEmptyWithoutACensus(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{targeted("t1", true)},
		Runs:   []model.RunTiming{runFor("r1", "t1", 10*time.Hour)},
	})
	if got := rep.PullRequests; got.Runs != 0 || got.Opened != 0 || got.AdoptionRate != 0 {
		t.Errorf("PullRequests = %+v, want an empty section", got)
	}
}
