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

package mcp

// tool_budget.go is the per-run tool budget a durable judgement task's
// wake runs under (design/design/durable-tasks.design.md#DT-13): the target is a
// wake that fits in context with no compaction, at most 40 tool calls and
// 150 KB of tool output, against the 154 calls and 1.1 MB the pipeline
// steward's first wake spent.
//
// Past the soft line every result carries a sentence saying so. Past the
// hard line (60 calls or 300 KB) every result asks the run to write its
// handover and end the wake. The design has the hard line end in
// resume_later() -- the wake continuing as the next resumable step with a
// fresh context -- and that half lands with resume_later itself (DT-S4);
// until then the notice is the whole of it.
//
// A wake is counted from the last thing a person said to it, not from the
// start of the run: an interactive session that holds a conversation in one
// run (the chat tool) begins a new wake with every reply, which restarts
// the count (Result.HumanTurn). A session's continued turn after it
// answers (orchestrator's continueSessionTurns) forks a new mcpserver,
// and so a new budget, of its own.
//
// Only a run whose registry has been told to (AnnounceToolBudget, the
// mcpserver's -tool-budget) counts at all; every other run's results read
// exactly as their handlers wrote them.

import (
	"fmt"
	"sync"
)

// The two lines, in calls and in bytes of tool output.
const (
	ToolBudgetSoftCalls = 40
	ToolBudgetSoftBytes = 150 << 10
	ToolBudgetHardCalls = 60
	ToolBudgetHardBytes = 300 << 10
)

// toolBudget counts what one run has spent.
type toolBudget struct {
	mu      sync.Mutex
	calls   int
	bytes   int
	wording ToolBudgetWording
}

// ToolBudgetWording is how the notice names what it asks a run to do,
// which depends on what the run can call. The zero value names the full
// surface's tools and no pipeline cursor.
type ToolBudgetWording struct {
	// CLI is a run on the control surface, where the handover and the
	// pipeline feed are `grain` commands inside the sandbox rather than
	// tools, and every run_command result is charged at the size it
	// reaches the run.
	CLI bool
	// PipelineCursor is a pipeline steward wake, whose handover carries
	// the feed's cursor for the next wake to start from. A director's
	// chat session reads no feed and is not asked for one.
	PipelineCursor bool
}

// AnnounceToolBudget turns the budget on for this registry's run.
func (r *Registry) AnnounceToolBudget() {
	r.budget = &toolBudget{}
}

// WordToolBudget sets how the budget's notice is worded for this run; it
// does nothing for a registry whose budget was never announced.
func (r *Registry) WordToolBudget(w ToolBudgetWording) {
	if r.budget == nil {
		return
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	r.budget.wording = w
}

// spend records one result of n bytes and returns the notice the result
// should carry, or "" while the run is inside its budget.
func (b *toolBudget) spend(n int) string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.bytes += n
	return toolBudgetNotice(b.calls, b.bytes, b.wording)
}

// reset starts a new wake whose first result is n bytes -- a person's
// reply, which is in the run's context like any other result.
func (b *toolBudget) reset(n int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = 1
	b.bytes = n
}

// toolBudgetNotice is the sentence for a run that has made calls calls and
// read bytes bytes of tool output, worded for what the run can call.
func toolBudgetNotice(calls, bytes int, w ToolBudgetWording) string {
	spent := fmt.Sprintf("%d tool calls and %s of tool output", calls, humanBytes(bytes))
	switch {
	case calls >= ToolBudgetHardCalls || bytes >= ToolBudgetHardBytes:
		handover, cursor := "write_handover", "the read_pipeline_changes cursor to start from"
		if w.CLI {
			handover = "`grain handover write --file <f>`"
			cursor = "the cursor `grain read deploy://pipeline-changes` ended with, to start from"
		}
		settled := "what you settled and what is left"
		if w.PipelineCursor {
			settled = "what you settled, what is left, and " + cursor
		}
		return fmt.Sprintf("[grain] Tool budget exhausted: this wake has made %s, past its limit of %d calls "+
			"or %s. Write your handover now (%s) -- %s -- then end this wake. The next wake carries on "+
			"from the handover with a fresh context.", spent, ToolBudgetHardCalls, humanBytes(ToolBudgetHardBytes),
			handover, settled) + w.cliCost()
	case calls >= ToolBudgetSoftCalls || bytes >= ToolBudgetSoftBytes:
		return fmt.Sprintf("[grain] Tool budget: this wake has made %s, past its target of %d calls or %s. "+
			"Read nothing a change does not name, prefer summary views, and start wrapping up; at %d calls "+
			"or %s you will be asked to write your handover and stop.", spent, ToolBudgetSoftCalls,
			humanBytes(ToolBudgetSoftBytes), ToolBudgetHardCalls, humanBytes(ToolBudgetHardBytes)) + w.cliCost()
	}
	return ""
}

// cliCost is what a control-surface run is told about where its bytes go:
// `grain` itself has no size cap, but each run_command result is counted
// at the size it reaches the run, after elision.
func (w ToolBudgetWording) cliCost() string {
	if !w.CLI {
		return ""
	}
	return fmt.Sprintf(" Each run_command result counts towards the budget at the size it reaches you "+
		"(up to about %s after elision): send `grain` output to a file and grep it rather than printing it.",
		humanBytes(maxResultTextBytes()))
}
