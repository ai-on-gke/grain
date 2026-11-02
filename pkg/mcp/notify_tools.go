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

// notify_operator and message_task (design/design/durable-tasks.design.md, DT-5
// and DT-10): "tell the operator" and "tell another task's agent", two
// separate tools, neither of which parks anybody. ask_question keeps its
// meaning -- this task cannot continue without an answer -- and these
// are for everything a run wants known while it carries on.
//
// Both act through the daemon (ui.Client.AgentNotify and PostTaskNote),
// which applies the rules -- the subject's namespace, the per-run and
// per-task limits, who may message whom -- for the task fixed at process
// start, so a run can only ever speak as itself.

import (
	"context"
	"fmt"
	"strings"
)

// OperatorNotification is notify_operator's arguments, as the daemon is
// handed them.
type OperatorNotification struct {
	Subject    string
	Title      string
	Body       string
	Severity   string
	Task       string
	ClearsWhen map[string]any
}

// RaisedNotification is the daemon's answer to a raise.
type RaisedNotification struct {
	ID       int64
	Subject  string
	Severity string
	Count    int
	New      bool
}

// OperatorNotifier is the daemon, as notify_operator and message_task
// see it.
type OperatorNotifier interface {
	Notify(ctx context.Context, n OperatorNotification) (RaisedNotification, error)
	MessageTask(ctx context.Context, task, body string) (int64, error)
}

const noNotifier = "This run has no grain daemon to ask, so it cannot reach the operator or another task. " +
	"Say it in your closing note instead."

// NewNotifyTools returns notify_operator and message_task. A nil
// notifier is a server with no daemon to ask, and both tools say so.
func NewNotifyTools(notifier OperatorNotifier) []Tool {
	return []Tool{notifyOperatorTool(notifier), messageTaskTool(notifier)}
}

func notifyOperatorTool(notifier OperatorNotifier) Tool {
	return Tool{
		Name: "notify_operator",
		Description: "Tell the operator something they should know or act on, and carry on: unlike " +
			"ask_question this never parks your task or waits for an answer. It shows in grain's Alerts " +
			"and on the Inbox, and a warning or critical one reaches the director chat. subject is a short " +
			"key for what it is about (\"flaky-e2e\", \"port-stale\"): raising the same subject again " +
			"bumps the open notification rather than adding another, and it is namespaced to this task " +
			"unless the task holds api:task-admin. severity is info, warning (the default) or critical; " +
			"critical needs api:task-admin. clears_when makes it clear itself: {\"kind\": \"task\", \"task\": " +
			"\"123\", \"until\": \"closed\"|\"merged\"}, {\"kind\": \"base-green\", \"repo\": \"owner/name\", " +
			"\"branch\": \"main\"}, or {\"kind\": \"ttl\", \"ttl\": \"6h\"}. A run may raise at most 10, and a " +
			"task have at most 20 open. Links in body must be root-relative (/tasks/12).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"subject":  map[string]any{"type": "string", "description": "A short key for what this is about; raising it again bumps it."},
				"title":    map[string]any{"type": "string", "description": "One line a person reads first."},
				"body":     map[string]any{"type": "string", "description": "Markdown: what happened and what to do about it."},
				"severity": map[string]any{"type": "string", "enum": []string{"info", "warning", "critical"}},
				"task":     map[string]any{"type": "string", "description": "The task it is about, if not this one."},
				"clears_when": map[string]any{
					"type":        "object",
					"description": "The condition that clears it: kind task, base-green or ttl (see the tool's description).",
				},
			},
			"required": []string{"subject", "title"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if notifier == nil {
				return Result{Text: noNotifier, IsError: true}
			}
			n := OperatorNotification{}
			n.Subject, _ = argString(args, "subject")
			n.Title, _ = argString(args, "title")
			n.Body, _ = argString(args, "body")
			n.Severity, _ = argString(args, "severity")
			n.Task, _ = argString(args, "task")
			n.ClearsWhen, _ = args["clears_when"].(map[string]any)
			if strings.TrimSpace(n.Subject) == "" || strings.TrimSpace(n.Title) == "" {
				return Result{Text: "notify_operator needs a subject and a title.", IsError: true}
			}
			raised, err := notifier.Notify(ctx, n)
			if err != nil {
				return Result{Text: "The notification was not raised: " + err.Error(), IsError: true}
			}
			if raised.New {
				return Result{Text: fmt.Sprintf("Raised notification %d (%s, %s). The operator will see it; carry on with your work.",
					raised.ID, raised.Subject, raised.Severity)}
			}
			return Result{Text: fmt.Sprintf("Notification %d (%s) was already open; raised again (%d times), now %s. Carry on with your work.",
				raised.ID, raised.Subject, raised.Count, raised.Severity)}
		},
	}
}

func messageTaskTool(notifier OperatorNotifier) Tool {
	return Tool{
		Name: "message_task",
		Description: "Leave a note for another task's agent. It is a comment on that task's conversation " +
			"from this task's agent; it never parks that task or changes its state. A run that task has " +
			"going is handed it with its next tool result, a resting durable task is woken by it, and " +
			"otherwise its next run reads it in its prompt. Only for this task's parent or its children, " +
			"unless this task holds api:task-admin. To tell a person something, use notify_operator.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task": map[string]any{"type": "string", "description": "The task to message (\"4944\" or \"#4944\")."},
				"body": map[string]any{"type": "string", "description": "What to tell its agent, in markdown."},
			},
			"required": []string{"task", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if notifier == nil {
				return Result{Text: noNotifier, IsError: true}
			}
			task, _ := argString(args, "task")
			body, _ := argString(args, "body")
			task = strings.TrimPrefix(strings.TrimSpace(task), "#")
			if task == "" || strings.TrimSpace(body) == "" {
				return Result{Text: "message_task needs a task and a body.", IsError: true}
			}
			id, err := notifier.MessageTask(ctx, task, body)
			if err != nil {
				return Result{Text: "The note was not delivered: " + err.Error(), IsError: true}
			}
			return Result{Text: fmt.Sprintf("Note %d left on task %s.", id, task)}
		},
	}
}
