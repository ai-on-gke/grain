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

import (
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Agents groups per-wake tool use, re-reads, and steward report checks by
// template over the report's window (docs/design/durable-tasks.md#DT-14).
type Agents struct {
	Templates   []AgentTemplate
	ReportCheck AgentReportCheck
}

// AgentTemplate summarises the wakes of tasks filed from one template over the
// window.
type AgentTemplate struct {
	TemplateID      string
	Wakes           int
	Calls           int
	CallsP50        int
	CallsP95        int
	ResultBytes     int64
	BytesP50        int64
	BytesP95        int64
	Elisions        int
	ElisionsPerWake float64
	ReReads         int
	ReReadsPerWake  float64
	ReportCheck     AgentReportCheck
}

// AgentReportCheck summarises stewardReportCheck (and weekly sampled audit)
// results over the wakes in the window.
type AgentReportCheck struct {
	CheckedWakes int
	Claims       int
	Uncited      int
	Unread       int
	Mismatched   int
	UncitedRate  float64
	UnreadRate   float64
	MismatchRate float64
}

func (c *AgentReportCheck) add(row model.RunReportCheck) {
	c.CheckedWakes++
	c.Claims += row.Claims
	c.Uncited += row.Uncited
	c.Unread += row.Unread
	c.Mismatched += row.Mismatched
	if c.Claims > 0 {
		c.UncitedRate = float64(c.Uncited) / float64(c.Claims)
		c.UnreadRate = float64(c.Unread) / float64(c.Claims)
		c.MismatchRate = float64(c.Mismatched) / float64(c.Claims)
	}
}

func isReadToolForAgents(tool string) bool {
	switch tool {
	case "read_grain_task", "read_task", "read_grain_task_prompt", "read_grain_task_transcript",
		"list_grain_tasks", "read_pipeline_changes", "query_merge_queue",
		"list_ci_runs", "get_ci_log", "pull_request_status", "base_branch_status",
		"query_test_history", "grain_read", "grain_ls", "grain_grep",
		"api_get_task", "api_list_tasks", "api_get_ci_run", "api_list_ci_runs":
		return true
	}
	return false
}

func agentsOf(w Window, tasks []model.TaskTiming, runs []model.RunTiming, uses []model.RunToolUse,
	apiCalls []model.RunAPICall, reportChecks []model.RunReportCheck) Agents {

	taskTemplate := make(map[string]string, len(tasks))
	for _, t := range tasks {
		if t.TemplateID != "" {
			taskTemplate[t.TaskID] = t.TemplateID
		}
	}

	type wakeStats struct {
		calls       int
		resultBytes int64
		elisions    int
		reReads     int
	}
	byRun := map[string]*wakeStats{}
	for _, u := range uses {
		if u.Tool == "_stop" {
			continue
		}
		st := byRun[u.RunID]
		if st == nil {
			st = &wakeStats{}
			byRun[u.RunID] = st
		}
		st.calls += u.Calls
		st.resultBytes += u.ResultBytes
		st.elisions += u.Elided
	}

	// Count re-reads per run: the same target ID read more than twice (> 2) in one wake.
	readCountsByRun := map[string]map[string]int{}
	for _, call := range apiCalls {
		if call.Tool == "comment_on_issue" || call.Tool == "nothing_needed" {
			continue
		}
		if !isReadToolForAgents(call.Tool) || call.TargetIDs == "" {
			continue
		}
		counts := readCountsByRun[call.RunID]
		if counts == nil {
			counts = map[string]int{}
			readCountsByRun[call.RunID] = counts
		}
		seenInCall := map[string]bool{}
		for _, raw := range strings.Split(call.TargetIDs, ",") {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			if norm := model.NormalizeTaskID(id); norm != "" {
				id = norm
			}
			if !seenInCall[id] {
				seenInCall[id] = true
				counts[id]++
			}
		}
	}
	for runID, counts := range readCountsByRun {
		reReads := 0
		for _, n := range counts {
			if n > 2 {
				reReads++
			}
		}
		if reReads > 0 {
			st := byRun[runID]
			if st == nil {
				st = &wakeStats{}
				byRun[runID] = st
			}
			st.reReads = reReads
		}
	}

	checksByRun := make(map[string]model.RunReportCheck, len(reportChecks))
	for _, rc := range reportChecks {
		checksByRun[rc.RunID] = rc
	}

	type templateAccum struct {
		row        AgentTemplate
		callCounts []int
		byteCounts []int64
	}
	byTemplate := map[string]*templateAccum{}
	var out Agents

	for _, r := range runs {
		if !w.holds(r.FinishedAt) {
			continue
		}
		rc, hasCheck := checksByRun[r.RunID]
		if hasCheck {
			out.ReportCheck.add(rc)
		}
		tmplID := taskTemplate[r.TaskID]
		if tmplID == "" && hasCheck {
			tmplID = model.PipelineStewardTemplateID
		}
		if tmplID == "" {
			continue
		}
		acc := byTemplate[tmplID]
		if acc == nil {
			acc = &templateAccum{row: AgentTemplate{TemplateID: tmplID}}
			byTemplate[tmplID] = acc
		}
		acc.row.Wakes++
		var calls int
		var bytes int64
		if st := byRun[r.RunID]; st != nil {
			calls = st.calls
			bytes = st.resultBytes
			acc.row.Calls += st.calls
			acc.row.ResultBytes += st.resultBytes
			acc.row.Elisions += st.elisions
			acc.row.ReReads += st.reReads
		}
		acc.callCounts = append(acc.callCounts, calls)
		acc.byteCounts = append(acc.byteCounts, bytes)
		if hasCheck {
			acc.row.ReportCheck.add(rc)
		}
	}

	if len(byTemplate) == 0 {
		return out
	}

	templates := make([]AgentTemplate, 0, len(byTemplate))
	for _, acc := range byTemplate {
		row := acc.row
		if row.Wakes > 0 {
			row.CallsP50 = intPercentile(acc.callCounts, 50)
			row.CallsP95 = intPercentile(acc.callCounts, 95)
			row.BytesP50 = int64Percentile(acc.byteCounts, 50)
			row.BytesP95 = int64Percentile(acc.byteCounts, 95)
			row.ElisionsPerWake = float64(row.Elisions) / float64(row.Wakes)
			row.ReReadsPerWake = float64(row.ReReads) / float64(row.Wakes)
		}
		templates = append(templates, row)
	}
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].Wakes != templates[j].Wakes {
			return templates[i].Wakes > templates[j].Wakes
		}
		return templates[i].TemplateID < templates[j].TemplateID
	})
	out.Templates = templates
	return out
}

func intPercentile(samples []int, p int) int {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	idx := (p*len(sorted) + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

func int64Percentile(samples []int64, p int) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p*len(sorted) + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}
