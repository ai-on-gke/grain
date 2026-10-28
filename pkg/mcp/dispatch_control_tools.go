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
	"fmt"
	"strings"
	"time"
)

// DispatchController is what dispatch-control's tools call: the
// deployment's pipeline pause, and the task-level actions that decide
// what runs while it is in force (grain/task-399). A pause and a Run now
// are made as the run's own agent, {agent, <task id>}, so the pause row
// and the task's conversation name it rather than the operator; a resume
// and a deferral record nobody, for an agent as for a person.
type DispatchController interface {
	// PausePipeline pauses the whole deployment and reports whether this
	// call is what paused it.
	PausePipeline(ctx context.Context, reason string) (paused bool, err error)
	// ResumePipeline lifts the pause and reports whether there was one.
	ResumePipeline(ctx context.Context) (resumed bool, err error)
	// RunTaskNow asks for taskID to be started now, approving it and
	// picking it back up from deferral where needed, and reports the
	// pause in force afterwards: "" for none, otherwise its reason, with
	// runNowPasses false for the one kind a Run now waits behind (an
	// upgrade's hold).
	RunTaskNow(ctx context.Context, taskID string) (pause string, runNowPasses bool, err error)
	// DeferTask sets taskID aside, until until when it is not nil.
	DeferTask(ctx context.Context, taskID string, until *time.Time) error
}

const noDispatchController = "This run cannot control dispatch: it holds the dispatch-control grant " +
	"but was dispatched with no route back to the grain daemon that dispatches work. Say so if it " +
	"matters to what you were asked to do; nothing you can do from here will change it."

// NewDispatchControlTools returns dispatch-control's tools:
// pause_pipeline, resume_pipeline, run_task_now and defer_task. The
// first two share their names with pipeline-control's; a run holding
// both is served these (daemon.GrantedDaemonTools).
func NewDispatchControlTools(controller DispatchController) []Tool {
	return []Tool{
		dispatchPauseTool(controller),
		dispatchResumeTool(controller),
		runTaskNowTool(controller),
		deferTaskTool(controller),
	}
}

func dispatchPauseTool(controller DispatchController) Tool {
	return Tool{
		Name: "pause_pipeline",
		Description: "Pause this whole deployment: no new work is dispatched -- not the backlog, merge-queue " +
			"repairs, scheduled or suite tasks, ports -- except tasks somebody (you, with run_task_now, or a " +
			"person with Run now) explicitly starts. Runs already in flight finish. Protected by the " +
			"dispatch-control capability; recorded as paused by this run's agent.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"reason"},
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Why the deployment is paused -- shown on the banner every operator sees.",
				},
				// pipeline-control's own tool of this name takes a
				// repo, and a run that held that grant may well have
				// been told to pass one.
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional. A repository (owner/name) the reason is about; the pause covers the whole deployment either way.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noDispatchController, IsError: true}
			}
			reason, _ := args["reason"].(string)
			repo, _ := args["repo"].(string)
			reason, repo = strings.TrimSpace(reason), strings.TrimSpace(repo)
			if reason == "" {
				return Result{Text: "pause_pipeline failed: reason is required and cannot be empty", IsError: true}
			}
			if repo != "" {
				reason = fmt.Sprintf("[%s] %s", repo, reason)
			}
			paused, err := controller.PausePipeline(ctx, reason)
			if err != nil {
				return Result{Text: fmt.Sprintf("pause_pipeline failed: %v", err), IsError: true}
			}
			if !paused {
				return Result{Text: "The deployment was already paused; the pause now carries your reason: " + reason +
					". Only tasks started with run_task_now (or a person's Run now) will dispatch."}
			}
			return Result{Text: "The deployment is paused: " + reason + ". Nothing new dispatches except tasks " +
				"started with run_task_now (or a person's Run now). Call resume_pipeline to give the queue back."}
		},
	}
}

func dispatchResumeTool(controller DispatchController) Tool {
	return Tool{
		Name: "resume_pipeline",
		Description: "Lift this deployment's pipeline pause, so the queue dispatches as usual again. Protected " +
			"by the dispatch-control capability. Refused while an upgrade holds the queue (running or " +
			"scheduled): that hold goes when the upgrade is over, or when a person resumes from the banner.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				// Accepted, and only logged by the caller's own
				// transcript, for the same reason pause_pipeline takes
				// repo: pipeline-control's tool of this name requires
				// both.
				"repo":   map[string]any{"type": "string", "description": "Optional; ignored."},
				"reason": map[string]any{"type": "string", "description": "Optional; why you are resuming."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noDispatchController, IsError: true}
			}
			resumed, err := controller.ResumePipeline(ctx)
			if err != nil {
				return Result{Text: fmt.Sprintf("resume_pipeline failed: %v", err), IsError: true}
			}
			if !resumed {
				return Result{Text: "The deployment was not paused; nothing changed."}
			}
			return Result{Text: "The pause is lifted: the queue dispatches as usual again."}
		},
	}
}

func runTaskNowTool(controller DispatchController) Tool {
	return Tool{
		Name: "run_task_now",
		Description: "Start a task on the next dispatch cycle, ahead of the queue -- Run now, as a person " +
			"presses it, recorded as asked for by this run's agent. Approves a proposed task and picks a " +
			"deferred one back up first, but refuses to approve one asking for a capability, write target or " +
			"read this run's task does not hold, filed in a repository it cannot read, or set to auto-merge where " +
			"this run's task could not have proposed that: a person approves those. It is started even while the deployment is paused (pause_pipeline), " +
			"subject to capacity, but not while an upgrade holds the queue: that hold is followed by a restart " +
			"that would discard the run, so the request waits for the upgrade instead. Refused for a closed task, " +
			"one already running, and one waiting on a person's reply (a question or a secret it asked for): " +
			"only that person may start it without an answer. Protected by the dispatch-control capability.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"task_id"},
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task to start (\"4944\" or \"#4944\")."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noDispatchController, IsError: true}
			}
			id := dispatchTaskID(args)
			if id == "" {
				return Result{Text: "run_task_now failed: task_id is required", IsError: true}
			}
			pause, passes, err := controller.RunTaskNow(ctx, id)
			if err != nil {
				return Result{Text: fmt.Sprintf("run_task_now failed for task %s: %v", id, err), IsError: true}
			}
			switch {
			case pause == "":
				return Result{Text: fmt.Sprintf("Task %s is asked to run now: it starts on the next cycle, capacity permitting.", id)}
			case passes:
				return Result{Text: fmt.Sprintf("Task %s is asked to run now. The deployment is paused (%s), and "+
					"this request passes that pause: it starts on the next cycle, capacity permitting.", id, pause)}
			default:
				return Result{Text: fmt.Sprintf("Task %s is asked to run now, but an upgrade holds the queue (%s) "+
					"and Run now does not pass that: the request stays outstanding and the task starts once the "+
					"upgrade is over.", id, pause)}
			}
		},
	}
}

func deferTaskTool(controller DispatchController) Tool {
	return Tool{
		Name: "defer_task",
		Description: "Set a task aside so the queue does not dispatch it -- until a given time, or until " +
			"somebody picks it back up (run_task_now does). Protected by the dispatch-control capability.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"task_id"},
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task to defer (\"4944\" or \"#4944\")."},
				"until": map[string]any{
					"type":        "string",
					"description": "Optional RFC 3339 time (2026-10-14T09:00:00Z) the task picks itself back up at.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if controller == nil {
				return Result{Text: noDispatchController, IsError: true}
			}
			id := dispatchTaskID(args)
			if id == "" {
				return Result{Text: "defer_task failed: task_id is required", IsError: true}
			}
			var until *time.Time
			if raw, _ := args["until"].(string); strings.TrimSpace(raw) != "" {
				t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
				if err != nil {
					return Result{Text: fmt.Sprintf("defer_task failed: until %q is not an RFC 3339 time", raw), IsError: true}
				}
				until = &t
			}
			if err := controller.DeferTask(ctx, id, until); err != nil {
				return Result{Text: fmt.Sprintf("defer_task failed for task %s: %v", id, err), IsError: true}
			}
			if until != nil {
				return Result{Text: fmt.Sprintf("Task %s is deferred until %s.", id, until.UTC().Format(time.RFC3339))}
			}
			return Result{Text: fmt.Sprintf("Task %s is deferred until somebody picks it back up.", id)}
		},
	}
}

// dispatchTaskID reads a task_id argument, tolerating the "#4944" a
// person would write.
func dispatchTaskID(args map[string]any) string {
	id, _ := args["task_id"].(string)
	return strings.TrimPrefix(strings.TrimSpace(id), "#")
}
