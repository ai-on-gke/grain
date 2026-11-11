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

package orchestrator

// The run's own account of its tool use, turned into rows.
//
// toolCallSummary has always counted this -- every tool a run called, and
// how many of those calls came back as errors -- and has always rendered
// it into English for task_run.detail, where a human reads one run at a
// time and nothing can aggregate it. That sentence stays exactly as it
// is. This is the same census as data, so that "is edit_file's error rate
// climbing?", "how big does a run_command answer actually get?" and "how
// many pushes does a run make before its checks go green?" have answers
// over a window rather than per anecdote (docs/agent-ergonomics.md,
// findings 11 to 13).
//
// It is computed here, at the one moment it can be: agent.Result carries
// every call the run made and what each returned, and is discarded when
// RunDispatch returns. Nothing downstream can recover it -- the
// transcript is per-framework prose that a tool's own output can forge
// (outcomeOf's own doc comment on why counting calls out of it is
// unsound), and the store holds nothing else.
//
// Counts and sizes only. No argument, no result and no fragment of
// output crosses into the census: what a run's commands said is the
// task's business and stays in the transcript, and a measurement table
// that quoted them would be a second copy of it with none of its
// bounds.

import (
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// runTelemetry builds the census for one finished run.
//
// A run with no calls produces nothing at all (RunTelemetry.Empty), not a
// row of zeroes: "this run called nothing" is already recorded in its
// outcome and its detail, and a zero row here would only dilute every
// per-tool rate it was averaged into.
func runTelemetry(result *agent.Result) model.RunTelemetry {
	return runTelemetryAt(result, time.Time{})
}

func runTelemetryAt(result *agent.Result, at time.Time) model.RunTelemetry {
	if result == nil || (len(result.ToolCalls) == 0 && len(result.StopBlocks) == 0) {
		return model.RunTelemetry{}
	}
	var out model.RunTelemetry
	// The census keeps the order the run first reached for each tool,
	// which is stable and reproducible; the store's own read orders by
	// name for a report.
	var order []string
	byTool := map[string]*model.RunToolUse{}
	pushes := 0
	// The red-CI procedure's own steps, in the order they happened
	// (model.RunCIHabit): red is set by the first failed wait, and
	// narrowSinceRed by a narrowed test after it.
	var habit model.RunCIHabit
	red, narrowSinceRed := false, false

	for i, call := range result.ToolCalls {
		call = unwrapControllerToolCall(call)
		use, seen := byTool[call.Name]
		if !seen {
			use = &model.RunToolUse{
				Tool:    call.Name,
				Sizes:   model.SizeHistogram{},
				Dropped: model.SizeHistogram{},
			}
			byTool[call.Name] = use
			order = append(order, call.Name)
		}
		use.Calls++
		if call.IsError {
			use.Errored++
		}
		size := int64(len(call.Text))
		use.ResultBytes += size
		if size > use.MaxResultBytes {
			use.MaxResultBytes = size
		}
		use.Sizes.Add(size)
		if isAgentAPITool(call.Name) {
			out.APICalls = append(out.APICalls, model.RunAPICall{
				Seq:         i + 1,
				Tool:        call.Name,
				TargetIDs:   extractAPICallTargetIDs(call),
				ResultBytes: size,
				At:          at,
			})
		}
		// Every tool, not just the two below: the result cap applies to
		// all of them, and the size a call is recorded at is the size
		// *after* any cut, so without this the census would report a
		// deployment whose answers all fit comfortably under a cap that
		// was in fact trimming them to it (mcp.ResultElision).
		if dropped, cut := mcp.ResultElision(call.Text); cut {
			use.Elided++
			// A cut whose own notice this build cannot read a size out of
			// is still a cut, but it is not a nought-byte one: it counts
			// in Elided and stays out of the distribution rather than
			// pulling its median down to a number nothing measured.
			if dropped > 0 {
				use.Dropped.Add(dropped)
			}
		}

		switch call.Name {
		case runCommandToolName:
			if mcp.RunCommandTimedOut(call.Text) {
				use.TimedOut++
			}
			// Counted before the wait below, so that a wait_for_checks
			// call records the pushes that preceded it -- the point of
			// PushesBefore is what this run had already pushed by the
			// time it asked.
			if isNarrowTest(call) {
				if pushes == 0 {
					habit.NarrowTestBeforePush = true
				}
				if red {
					narrowSinceRed = true
				}
			}
			if isPush(call) {
				// The first push after the first red is the fix push,
				// whether the red was this run's own change or a branch
				// it was handed red.
				if red && !habit.PushedAfterRed {
					habit.PushedAfterRed = true
					habit.NarrowTestBeforeFixPush = narrowSinceRed
				}
				pushes++
			}
		case runCIJobToolName, retriggerCIToolName:
			habit.Selective = true
		case waitForChecksToolName:
			if selectsJobs(call) {
				habit.Selective = true
			}
			if wait, ok := mcp.ReadCheckWait(call.Text); ok {
				if wait.Verdict == mcp.WaitVerdictFailed {
					red = true
				}
				out.CheckWaits = append(out.CheckWaits, model.RunCheckWait{
					Seq:          len(out.CheckWaits),
					Verdict:      wait.Verdict,
					Provider:     wait.Provider,
					Waited:       wait.Waited,
					PushesBefore: pushes,
				})
			}
		}
	}

	for _, name := range order {
		out.Tools = append(out.Tools, *byTool[name])
	}
	// Every time the stop rule sent the run back (agent.StopBlock),
	// counted as calls of a tool no agent can call, so the census says
	// how often runs try to end mid-promise without a column of its own.
	if n := len(result.StopBlocks); n > 0 {
		out.Tools = append(out.Tools, model.RunToolUse{
			Tool: agent.StopCensusTool, Calls: n,
			Sizes: model.SizeHistogram{}, Dropped: model.SizeHistogram{},
		})
	}
	if !habit.Empty() {
		out.CIHabit = &habit
	}
	return out
}

// execTransportFailures is how many of result's tool calls the
// cluster's exec tunnel failed (mcp.ExecTransportFailed) -- counted
// apart from the per-tool census, which keeps no time, because these
// feed the deployment-wide exec-tunnel alert (noteExecTunnelRate).
func execTransportFailures(result *agent.Result) int {
	if result == nil {
		return 0
	}
	n := 0
	for _, call := range result.ToolCalls {
		if call.IsError && mcp.ExecTransportFailed(call.Text) {
			n++
		}
	}
	return n
}

// The two tools the census reads more than a count off. Named here rather
// than spelled inline for the reason agent.ToolCall.Name's own doc
// comment gives: these are pkg/mcp's bare names, matched exactly, and a
// mismatch is silent.
const (
	runCommandToolName     = "run_command"
	waitForChecksToolName  = "wait_for_checks"
	runCIJobToolName       = "run_ci_job"
	retriggerCIToolName    = "retrigger_ci"
	controllerMCPNamespace = mcp.ControllerToolNamespace
)

// unwrapControllerToolCall normalises tool calls made against grain-controller
// (/api/mcp): CLI server prefixes (mcp__grain-controller__, mcp_grain-controller_,
// grain-controller/, and agy's call_mcp_tool wrapper) are stripped, and
// grain_invoke(tool, arguments) is unwrapped to the underlying api_* route name
// and arguments so run telemetry logs which api_* routes were called.
func unwrapControllerToolCall(call agent.ToolCall) agent.ToolCall {
	if call.Name == "call_mcp_tool" && call.Arguments != nil {
		if server, _ := call.Arguments["ServerName"].(string); server == controllerMCPNamespace {
			if tool, _ := call.Arguments["ToolName"].(string); tool != "" {
				call.Name = tool
				inner, _ := call.Arguments["Arguments"].(map[string]any)
				call.Arguments = inner
			}
		}
	}
	for _, prefix := range []string{
		"mcp__" + controllerMCPNamespace + "__",
		"mcp_" + controllerMCPNamespace + "_",
		controllerMCPNamespace + "/",
		controllerMCPNamespace + "__",
	} {
		if after, ok := strings.CutPrefix(call.Name, prefix); ok {
			call.Name = after
			break
		}
	}
	if call.Name != "grain_invoke" || call.Arguments == nil {
		return call
	}
	rawTool, _ := call.Arguments["tool"].(string)
	rawTool = strings.TrimSpace(rawTool)
	if rawTool == "" {
		return call
	}
	if !strings.HasPrefix(rawTool, "api_") {
		rawTool = "api_" + rawTool
	}
	call.Name = rawTool
	merged := map[string]any{}
	if inner, ok := call.Arguments["arguments"].(map[string]any); ok {
		for k, v := range inner {
			merged[k] = v
		}
		for _, subKey := range []string{"body", "query"} {
			if sub, ok := inner[subKey].(map[string]any); ok {
				for k, v := range sub {
					if _, exists := merged[k]; !exists {
						merged[k] = v
					}
				}
			}
		}
	}
	if reason, ok := call.Arguments["reason"].(string); ok && reason != "" {
		if _, exists := merged["reason"]; !exists {
			merged["reason"] = reason
		}
	}
	call.Arguments = merged
	return call
}

// selectsJobs reports whether a wait_for_checks call named the jobs it
// wanted rather than asking for the whole suite.
func selectsJobs(call agent.ToolCall) bool {
	switch jobs := call.Arguments["jobs"].(type) {
	case []any:
		return len(jobs) > 0
	case []string:
		return len(jobs) > 0
	}
	return false
}

// isNarrowTest reports whether a run_command call ran a narrowed test --
// one named test or file rather than a package's or a repository's
// whole suite, which is step 4 of the red-CI procedure.
//
// A heuristic, narrow on purpose for the reason isPush gives: it
// recognises the runners' own selection flags (`go test -run`, pytest's
// `-k` or a `file::test` node id, a Playwright or Jest `--grep`/`-g`/`-t`)
// and nothing else, so a run that reproduced a failure some other way is
// missed rather than a whole-suite run counted. Unlike isPush it counts a
// call that came back an error: run_command reports a non-zero exit as
// one, and a reproduction of a failing test is supposed to fail.
func isNarrowTest(call agent.ToolCall) bool {
	command, ok := call.Arguments["command"].(string)
	if !ok {
		return false
	}
	fields := strings.Fields(command)
	has := func(flags ...string) bool {
		for _, f := range fields {
			for _, flag := range flags {
				if f == flag || strings.HasPrefix(f, flag+"=") {
					return true
				}
			}
		}
		return false
	}
	switch {
	case strings.Contains(command, "go test"):
		return has("-run", "--run")
	case strings.Contains(command, "pytest"):
		return has("-k") || strings.Contains(command, "::")
	case strings.Contains(command, "playwright test"), strings.Contains(command, "jest"),
		strings.Contains(command, "vitest"):
		return has("-g", "--grep", "-t", "--testNamePattern")
	}
	return false
}

// isPush reports whether a run_command call was the run pushing its
// branch.
//
// A heuristic, and the only one available: grain does not do the pushing,
// git does, inside the sandbox, at the agent's own hand. Reading the
// command it asked for is the one place a push is visible at all. It is
// deliberately narrow -- the literal `git push`, and only a call that
// came back without an error, since a rejected push is not a push -- so
// that it under-counts rather than over-counts. What it feeds is
// "how many pushes before the checks went green", where an invented push
// would be worse than a missed one.
func isPush(call agent.ToolCall) bool {
	if call.IsError {
		return false
	}
	command, ok := call.Arguments["command"].(string)
	if !ok {
		return false
	}
	return strings.Contains(command, "git push")
}

// isAgentAPITool reports whether tool is a grain / agent-API tool rather than
// a sandbox shell or file tool.
func isAgentAPITool(name string) bool {
	switch name {
	case "", "run_command", "read_file", "write_file", "edit_file", "recreate_sandbox", agent.StopCensusTool:
		return false
	}
	return true
}

func isTaskIDAPIRoute(name string) bool {
	switch name {
	case "api_reorder", "api_approve", "api_close", "api_reopen", "api_hold", "api_release_hold",
		"api_retry_merge", "api_force_merge", "api_stop", "api_retry", "api_defer", "api_run_now":
		return true
	}
	return false
}

// extractAPICallTargetIDs extracts comma-separated task or CI run IDs targeted
// or returned by call. Errored calls return "" so a failed read cannot count as
// having read its requested target.
func extractAPICallTargetIDs(call agent.ToolCall) string {
	if call.IsError {
		return ""
	}
	var ids []string
	seen := map[string]bool{}
	addTask := func(raw string) {
		id := model.NormalizeTaskID(strings.TrimSpace(raw))
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	addRaw := func(raw string) {
		id := strings.TrimSpace(raw)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	addArgAny := func(v any, asTask bool) {
		switch val := v.(type) {
		case string:
			for _, part := range strings.Split(val, ",") {
				if asTask {
					addTask(part)
				} else {
					addRaw(part)
				}
			}
		case []string:
			for _, s := range val {
				if asTask {
					addTask(s)
				} else {
					addRaw(s)
				}
			}
		case []any:
			for _, elem := range val {
				if s, ok := elem.(string); ok {
					if asTask {
						addTask(s)
					} else {
						addRaw(s)
					}
				}
			}
		}
	}

	if call.Arguments != nil {
		for _, key := range []string{"task_id", "task", "task_ids", "depends_on", "supersedes"} {
			if v, ok := call.Arguments[key]; ok {
				addArgAny(v, true)
			}
		}
		if call.Name == "grain_get_task" {
			if v, ok := call.Arguments["id"]; ok {
				addArgAny(v, true)
			}
		}
		if strings.HasPrefix(call.Name, "api_") {
			if v, ok := call.Arguments["id"]; ok {
				if strings.Contains(call.Name, "task") || isTaskIDAPIRoute(call.Name) {
					addArgAny(v, true)
				} else if strings.Contains(call.Name, "ci_run") {
					addArgAny(v, false)
				}
			}
		}
		if when, ok := call.Arguments["when"].(map[string]any); ok {
			if v, ok := when["task"]; ok {
				addArgAny(v, true)
			}
		}
		if v, ok := call.Arguments["run_id"]; ok {
			addArgAny(v, false)
		}
	}

	if model.ReadsIDsFromOutput(call.Name) {
		for _, id := range model.ReadOutputIDs(call.Name, call.Text) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}

	return strings.Join(ids, ",")
}
