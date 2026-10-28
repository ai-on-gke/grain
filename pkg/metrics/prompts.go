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

package metrics

// What a run was given before its first turn, over a window.
//
// tools.go is what a run spends its own context on; this is what grain
// spent for it in advance. A dispatch's prompt is measured on the way
// out (model.PromptSize, grain/task-268) and reported per attempt for
// one task (Store.RunPromptSizes, read_grain_task's attempt list, the
// UI's prompt pane), which answers "is this task's prompt growing?" --
// one task at a time, by somebody who already suspected that task. The
// deployment-wide question it could not answer is the census question:
// are this deployment's prompts growing at all, and which tasks are the
// heavy ones.
//
// The package's rules hold unchanged. Nothing here is stored -- a prompt
// size is `length(CAST(prompt AS BLOB))` over rows that already exist,
// so a task edited, retried or closed changes what every past report
// would have said, which is correct. A row belongs to a window when its
// *run* finished inside it, a prompt having no moment of its own. And a
// run that recorded no prompt contributes to nothing rather than to a
// zero, which is why Prompts.Runs is reported beside every rate.

import (
	"sort"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Prompts is how big the prompts this deployment handed its runs were.
//
// # What the distribution measures, and what it cannot
//
// The same censoring the tool-result cap has, from the other end. Since
// grain/task-268 the conversation half of a prompt is bounded at
// orchestrator's maxConversationBytes, so every prompt recorded after it
// is at most that bound plus grain's own sections -- the attempt
// history, the capability paragraphs, the task's own title and body. A
// distribution over those prompts therefore measures what runs *were
// given*, not what they would have been given: a task whose thread is
// ten times the bound and one whose thread just fits are the same number
// here.
//
// # Which window to ask for
//
// grain/task-268 landed on 18 Sep 2026, and prompts have been recorded
// only since. A window reaching back past that date is not wrong so much
// as unreadable: the runs before it recorded no prompt, so they are
// absent from Runs and from every rate taken over it rather than sitting
// in the denominators (asserted in prompts_test.go, because counting
// them would be the natural way to write this and would understate every
// rate here). What such a window costs instead is comparability -- it
// reports a share of a smaller population than its own dates suggest,
// and a reader who takes Runs for "the runs in this window" will read it
// as a deployment that cuts far less than it does. Start the window
// after that date and the numbers mean what they look like.
//
// ThreadCut is what survives that, and is the number to read the
// percentiles against. It counts the prompts carrying one of the notices
// grain writes where it cut the conversation
// (model.PromptThreadCutNotice), which is the same measurement
// Tools.Cut is for a capped tool answer and is read the same way: a
// bound nothing reaches is one that could come down, and a deployment
// cutting most of its threads is one paying the bound on every dispatch.
//
// # Why the percentiles are bounds
//
// For Sizes' reason, and deliberately not because the samples are
// unavailable: unlike the tool census, which keeps a histogram per run
// because a row per call would be hundreds a run, every prompt here is
// its own exact sample. They are bucketed anyway so that a size in this
// report means the same thing wherever it is read -- `grain metrics` and
// the Metrics pane draw prompt sizes and tool result sizes through the
// same "at most" columns, and two kinds of percentile under one heading
// would be a distinction nobody reading a report can see. Max and Mean
// are exact, as they are there.
type Prompts struct {
	// Runs is how many finished runs in the window recorded a prompt at
	// all, and is the first thing to read: a run that never reached its
	// agent, and one that finished before prompts were recorded, each
	// contribute nothing rather than a zero.
	Runs int
	// Bytes is the distribution of those prompts' sizes.
	Bytes Sizes
	// Large is how many of them crossed model.PromptWarnTokens -- a
	// quarter of the window grain measures against, which is where
	// RunDispatch logs `condition=prompt_size`. LargeShare is that over
	// Runs. This is the count that says whether that warning is a rare
	// event or this deployment's ordinary weather.
	Large      int
	LargeShare float64
	// ThreadCut is how many prompts had a conversation cut out of them to
	// fit, and ThreadCutShare is that over Runs. See the type's own note:
	// it is the only number here the bound does not censor.
	ThreadCut      int
	ThreadCutShare float64
	// Heaviest is the few tasks with the largest prompts, largest first
	// -- the part of this section that names where to look. A
	// distribution says prompts are growing; only this says whose.
	Heaviest []TaskPrompts
}

// TaskPrompts is one task's prompts across the window: how many of its
// attempts were measured, the largest of them and which attempt that
// was, and how many of them grain had to cut a conversation out of.
//
// Measured on the largest rather than the mean, because that is the
// attempt that was closest to the ceiling, and because a task's prompt
// grows with its thread: the last attempt of a long-running task is
// normally its biggest, and averaging it against its own first dispatch
// would hide exactly the growth this exists to show.
type TaskPrompts struct {
	TaskID string
	// Runs is how many of this task's attempts inside the window
	// recorded a prompt -- the denominator of ThreadCut below, and the
	// difference between a task dispatched once with a huge prompt and
	// one dispatched thirty times.
	Runs      int
	MaxBytes  int64
	MaxTokens int
	// Attempt is which attempt MaxBytes was, so a reader has somewhere to
	// go: the task's prompt pane holds that attempt's whole text.
	Attempt   int
	ThreadCut int
}

// maxHeaviestTasks is how many tasks Heaviest names: five.
//
// Enough to see whether one task is an outlier or a deployment has a
// habit, and few enough to print as lines under a distribution rather
// than as a page of its own. A report is read for where to look next,
// and a reader who wants the sixth-heaviest task has already learned
// what this section had to tell them.
const maxHeaviestTasks = 5

// promptsOf measures the prompts of the runs that finished in the
// window. It joins on RunTiming rather than reading a task id off the
// prompt rows themselves, for the reason finishedInWindow exists: the
// run's own row is where a prompt's moment, its task and its attempt
// number all come from.
func promptsOf(w Window, runs []model.RunTiming, prompts []model.RunPrompt) Prompts {
	finished := make(map[string]model.RunTiming, len(runs))
	for _, r := range runs {
		if r.RunID != "" && w.holds(r.FinishedAt) {
			finished[r.RunID] = r
		}
	}

	out := Prompts{}
	sizes := model.SizeHistogram{}
	var total, max int64
	byTask := map[string]*TaskPrompts{}
	var taskIDs []string

	for _, p := range prompts {
		run, ok := finished[p.RunID]
		if !ok {
			continue
		}
		size := int64(p.Bytes)
		out.Runs++
		sizes.Add(size)
		total += size
		if size > max {
			max = size
		}
		if model.MeasurePromptBytes(p.Bytes).Large() {
			out.Large++
		}
		if p.ThreadCut {
			out.ThreadCut++
		}

		task, ok := byTask[run.TaskID]
		if !ok {
			task = &TaskPrompts{TaskID: run.TaskID}
			byTask[run.TaskID] = task
			taskIDs = append(taskIDs, run.TaskID)
		}
		task.Runs++
		if p.ThreadCut {
			task.ThreadCut++
		}
		if size > task.MaxBytes {
			task.MaxBytes = size
			task.MaxTokens = model.MeasurePromptBytes(p.Bytes).EstimatedTokens
			task.Attempt = run.Attempt
		}
	}

	if out.Runs == 0 {
		return out
	}
	out.LargeShare = float64(out.Large) / float64(out.Runs)
	out.ThreadCutShare = float64(out.ThreadCut) / float64(out.Runs)
	out.Bytes = Sizes{
		N:         sizes.Total(),
		Mean:      total / int64(out.Runs),
		Max:       max,
		P50AtMost: sizeQuantile(sizes, 0.50),
		P95AtMost: sizeQuantile(sizes, 0.95),
		P99AtMost: sizeQuantile(sizes, 0.99),
	}

	heaviest := make([]TaskPrompts, 0, len(taskIDs))
	for _, id := range taskIDs {
		heaviest = append(heaviest, *byTask[id])
	}
	// Largest first, and by task id where two tie, so the order is stable
	// across reports of the same window (ByTool's own rule).
	sort.SliceStable(heaviest, func(i, j int) bool {
		if heaviest[i].MaxBytes == heaviest[j].MaxBytes {
			return heaviest[i].TaskID < heaviest[j].TaskID
		}
		return heaviest[i].MaxBytes > heaviest[j].MaxBytes
	})
	if len(heaviest) > maxHeaviestTasks {
		heaviest = heaviest[:maxHeaviestTasks]
	}
	out.Heaviest = heaviest
	return out
}
