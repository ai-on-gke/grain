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
// wake runs under (docs/design/durable-tasks.md#DT-13): the target is a
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
	mu    sync.Mutex
	calls int
	bytes int
}

// AnnounceToolBudget turns the budget on for this registry's run.
func (r *Registry) AnnounceToolBudget() {
	r.budget = &toolBudget{}
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
	return toolBudgetNotice(b.calls, b.bytes)
}

// toolBudgetNotice is the sentence for a run that has made calls calls and
// read bytes bytes of tool output.
func toolBudgetNotice(calls, bytes int) string {
	spent := fmt.Sprintf("%d tool calls and %s of tool output", calls, humanBytes(bytes))
	switch {
	case calls >= ToolBudgetHardCalls || bytes >= ToolBudgetHardBytes:
		return fmt.Sprintf("[grain] Tool budget exhausted: this wake has made %s, past its limit of %d calls "+
			"or %s. Write your handover now (write_handover) -- what you settled, what is left, and the "+
			"read_pipeline_changes cursor to start from -- then end this wake. The next wake carries on "+
			"from the handover with a fresh context.", spent, ToolBudgetHardCalls, humanBytes(ToolBudgetHardBytes))
	case calls >= ToolBudgetSoftCalls || bytes >= ToolBudgetSoftBytes:
		return fmt.Sprintf("[grain] Tool budget: this wake has made %s, past its target of %d calls or %s. "+
			"Read nothing a change does not name, prefer summary views, and start wrapping up; at %d calls "+
			"or %s you will be asked to write your handover and stop.", spent, ToolBudgetSoftCalls,
			humanBytes(ToolBudgetSoftBytes), ToolBudgetHardCalls, humanBytes(ToolBudgetHardBytes))
	}
	return ""
}
