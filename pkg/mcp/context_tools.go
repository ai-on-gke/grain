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

import (
	"context"
	"strings"
)

// compactContextTool is write_handover's deliberate form: the handover it
// carries replaces the task's conversation up to now in every later
// prompt, rather than only the part of it a cut reached
// (design/design/durable-tasks.design.md#DT-15). It is relayed when the
// run ends, last call wins, like write_handover
// (orchestrator.relayCompaction). The comments it summarises are the
// ones posted before this run started, so nothing a person said while
// the run was working is folded away unread.
func compactContextTool() Tool {
	return Tool{
		Name: "compact_context",
		Description: "Compact this task's context: write a fresh handover that folds in the " +
			"current handover and everything the conversation so far has settled, and grain " +
			"leaves every message posted before this run started out of later prompts on this " +
			"task, carrying your handover in their place. The messages stay on the task and in " +
			"grain's UI. Use it when grain asks you to, or when the conversation in your prompt " +
			"is mostly history the work no longer needs. Write it for an agent that will see " +
			"none of those messages: decisions, invariants, names, what is done, what is left " +
			"and what was ruled out. Not a closing note; it does not complete the task. The " +
			"last call in a run is the one relayed.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"handover": map[string]any{
					"type":        "string",
					"description": "The new handover, in full: it replaces the current one and the messages it summarises.",
				},
			},
			"required": []string{"handover"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			handover, ok := argString(args, "handover")
			if !ok || strings.TrimSpace(handover) == "" {
				return Result{Text: "handover is required: the summary later prompts carry in place " +
					"of the messages this compacts", IsError: true}
			}
			return Result{Text: "Recorded. When this run finishes, grain posts this as the task's " +
				"handover and leaves every message posted before this run started out of later " +
				"prompts. It does not complete the task, so carry on with the work."}
		},
	}
}

// updateBriefTool rewrites a task's current brief: the description later
// prompts carry in place of the one the task was filed with. The original
// stays on the task, and every edit is recorded with its reason
// (model.Store.SetTaskBrief, orchestrator.relayBrief).
func updateBriefTool() Tool {
	return Tool{
		Name: "update_brief",
		Description: "Rewrite this task's current brief: what the task is for now, which later " +
			"prompts on this task carry in place of the description it was filed with. Use it " +
			"on a long-lived task whose description describes finished work or a stage it has " +
			"moved past. The original description stays on the task and in grain's UI, and every " +
			"edit is recorded with your reason. The last call in a run is the one relayed.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"brief": map[string]any{
					"type":        "string",
					"description": "The whole brief, as it should read from now on.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Why it changed, in a sentence: what the old brief no longer said right.",
				},
			},
			"required": []string{"brief", "reason"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			brief, ok := argString(args, "brief")
			if !ok || strings.TrimSpace(brief) == "" {
				return Result{Text: "brief is required: the task's brief as it should read from now on", IsError: true}
			}
			if reason, _ := argString(args, "reason"); strings.TrimSpace(reason) == "" {
				return Result{Text: "reason is required: say why the brief changed", IsError: true}
			}
			return Result{Text: "Recorded. When this run finishes, grain makes this the task's current " +
				"brief; the original description stays on the task."}
		},
	}
}
