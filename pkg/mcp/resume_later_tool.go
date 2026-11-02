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

// resume_later: the agent's own exit (docs/design/durable-tasks.md#DT-9).
//
// A run that is not done but has to stop -- it is waiting on CI, on a
// person's message, on another task, on the clock, or is a durable task
// splitting long work into steps -- ends as a requested suspension
// (model.SuspendRequested) and its task is queued again when any
// condition in `when` is met, or on the next cycle with none.
// wait_for_task's park and resume_at are forms of it, and go through the
// same TaskWaiter.ResumeLater call (ui.Client.ResumeLater).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// resumeLaterTakenPrefix opens the last line of a resume_later result:
// the condition as the daemon took it, in JSON, which
// orchestrator.processResult reads back (ResumeLaterTaken) -- an `at`
// given as a duration is only a time once the daemon has resolved it.
const resumeLaterTakenPrefix = "resume_on: "

// NewResumeLaterTools returns resume_later. A nil waiter is a server with
// no daemon to ask, and the tool says so rather than vanishing.
func NewResumeLaterTools(waiter TaskWaiter, unpushed UnpushedWorkReader) []Tool {
	return []Tool{resumeLaterTool(waiter, unpushed)}
}

func resumeLaterTool(waiter TaskWaiter, unpushed UnpushedWorkReader) Tool {
	return Tool{
		Name: "resume_later",
		Description: "Stop this run without completing the task, and have the task queued again later: " +
			"this is how to stop when the work is not done (comment_on_issue or nothing_needed is how " +
			"to finish). The task is queued again when any condition in `when` is met -- a grain CI " +
			"verdict (ci: a run id, or \"my-branch\" for the next run on this task's branch to " +
			"conclude), other tasks closing (task, with until \"closed\" or \"merged\"; takes no other " +
			"condition), a time (at: a duration like \"1h\" or an RFC 3339 time, at most a week away), " +
			"or anybody posting to this task (message: true). With no `when` it is queued again on " +
			"grain's next cycle, which suits splitting long work into steps. The next run's prompt " +
			"says this run was suspended and why. THE SANDBOX DOES NOT SURVIVE: commit and push your " +
			"branch first; a call with unpushed commits or uncommitted changes is refused unless you " +
			"set force: true. After calling this, take no further actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"reason"},
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Why the run is stopping and what the next one is to do: recorded on the run and shown to the next one.",
				},
				"when": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"ci": map[string]any{
							"type":        "string",
							"description": "A grain CI run id, or \"my-branch\": resume once it has a verdict.",
						},
						"task": map[string]any{
							"anyOf": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
							"description": "A task id, or several: resume once every one has closed (or merged).",
						},
						"until": map[string]any{
							"type":        "string",
							"enum":        []string{"closed", "merged"},
							"description": "With task: \"closed\" (default) or \"merged\".",
						},
						"at": map[string]any{
							"type":        "string",
							"description": "A duration from now (\"1h\", \"90m\") or an RFC 3339 time.",
						},
						"message": map[string]any{
							"type":        "boolean",
							"description": "Also resume when anybody posts to this task.",
						},
					},
				},
				"force": map[string]any{
					"type":        "boolean",
					"description": "Stop even though the checkout has unpushed commits or uncommitted changes, which are lost with the sandbox.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			return resumeLater(ctx, waiter, unpushed, args, time.Now())
		},
	}
}

func resumeLater(ctx context.Context, waiter TaskWaiter, unpushed UnpushedWorkReader,
	args map[string]any, now time.Time) Result {
	if waiter == nil {
		return Result{Text: "resume_later is not available here: this server has no grain daemon to ask.",
			IsError: true}
	}
	reason, _ := args["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return Result{Text: "reason is required: say why this run is stopping and what the next one is to do.",
			IsError: true}
	}
	when, err := ResumeLaterWhen(args, now)
	if err != nil {
		return Result{Text: err.Error(), IsError: true}
	}
	force, _ := args["force"].(bool)
	if refusal := unpushedRefusal(ctx, unpushed, force, ""); refusal != nil {
		refusal.Text = strings.Replace(refusal.Text, "Not parked", "Not stopped", 1)
		return *refusal
	}
	taken, err := waiter.ResumeLater(ctx, when)
	if err != nil {
		return Result{Text: "Not stopped: " + err.Error(), IsError: true}
	}
	encoded, _ := json.Marshal(taken)
	queued := "once " + taken.Describe()
	if taken.Empty() {
		queued = "on the next cycle"
	}
	return Result{Text: parkedMarker + " This task is queued again " + queued +
		"; the next run's prompt says this one was suspended, and why. This ends your run: stop " +
		"here and take no further actions.\n\n" + resumeLaterTakenPrefix + string(encoded)}
}

// ResumeLaterWhen reads resume_later's `when` out of its arguments.
func ResumeLaterWhen(args map[string]any, now time.Time) (model.ResumeOn, error) {
	var when model.ResumeOn
	raw, _ := args["when"].(map[string]any)
	if raw == nil {
		return when, nil
	}
	when.CI = strings.TrimSpace(stringArg(raw, "ci"))
	taskArgs := map[string]any{}
	switch v := raw["task"].(type) {
	case string:
		taskArgs["task_id"] = v
	case []any:
		taskArgs["task_ids"] = v
	}
	when.Tasks = WaitForTaskIDs(taskArgs)
	switch until := stringArg(raw, "until"); until {
	case "", "closed":
	case "merged":
		when.UntilMerged = true
	default:
		return when, fmt.Errorf("until must be \"closed\" or \"merged\", not %q", until)
	}
	if at := strings.TrimSpace(stringArg(raw, "at")); at != "" {
		t, err := ParseResumeAt(at, now)
		if err != nil {
			return when, errors.New(strings.Replace(err.Error(), "resume_at", "at", 1))
		}
		when.At = &t
	}
	when.Message, _ = raw["message"].(bool)
	if len(when.Tasks) > 0 && (when.At != nil || when.CI != "" || when.Message) {
		return when, errors.New("when.task takes no other condition: wait on tasks, or on ci, at and message, not both")
	}
	return when, nil
}

// ResumeLaterTaken is the condition a successful resume_later call's
// result says the daemon took, or false for any other text.
//
// The result a run sees is not only the tool's own: the registry appends
// grain's notices after it (the deadline, the tool budget, a person's
// words posted mid-run), so only the one JSON value after the first
// resumeLaterTakenPrefix is read, and whatever follows it is left alone.
// The first, not the last: a person's message relayed in a notice could
// quote the prefix itself.
func ResumeLaterTaken(text string) (model.ResumeOn, bool) {
	if !strings.HasPrefix(text, parkedMarker) {
		return model.ResumeOn{}, false
	}
	i := strings.Index(text, "\n"+resumeLaterTakenPrefix)
	if i < 0 {
		return model.ResumeOn{}, false
	}
	var when model.ResumeOn
	dec := json.NewDecoder(strings.NewReader(text[i+1+len(resumeLaterTakenPrefix):]))
	if err := dec.Decode(&when); err != nil {
		return model.ResumeOn{}, false
	}
	return when, true
}

func stringArg(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}
