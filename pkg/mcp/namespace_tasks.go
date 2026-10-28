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

	"github.com/ai-on-gke/grain/pkg/namespace"
)

// namespace_tasks.go serves the sandbox namespace's task:// root
// (pkg/namespace's TaskProvider) out of the same TaskReader, and in the
// same words, as read_grain_task, read_grain_task_prompt and
// read_grain_task_transcript: a run reading task://<id>/record.md over
// the proxy reads what the tool would have answered, less the 16 KB cap
// a tool result is held to -- /fs/ has ranges and a tail of its own.

// AttemptPromptReader is what a TaskReader also implements when it can
// read the prompt of an attempt other than the latest (TaskReader.
// TaskPrompt answers only that one). Without it task://<id>/attempts/<n>/
// prompt.md is the latest attempt's prompt for that attempt and empty for
// every other.
type AttemptPromptReader interface {
	AttemptPrompt(ctx context.Context, id string, attempt int) (string, error)
}

// NamespaceTasks is a namespace.TaskProvider (and namespace.
// TaskAttemptReader) over a TaskReader.
type NamespaceTasks struct {
	Reader TaskReader
	// Now is the clock the record's "stalled for" lines are measured
	// against; nil is time.Now.
	Now func() time.Time
}

var (
	_ namespace.TaskProvider      = NamespaceTasks{}
	_ namespace.TaskAttemptReader = NamespaceTasks{}
)

func (n NamespaceTasks) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// ListTaskIDs is every task's id, as list_grain_tasks lists them.
func (n NamespaceTasks) ListTaskIDs(ctx context.Context) ([]string, error) {
	if n.Reader == nil {
		return nil, fmt.Errorf("%w: task:// has no route to grain's task store", namespace.ErrNotFound)
	}
	tasks, err := n.Reader.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	return ids, nil
}

// ReadTaskView reads one task's record and its latest prompt. Attempts
// are named but not read: their prompts and transcripts are read one at a
// time, by ReadTaskAttempt, when a path asks for them.
func (n NamespaceTasks) ReadTaskView(ctx context.Context, id string) (*namespace.TaskView, error) {
	if n.Reader == nil {
		return nil, fmt.Errorf("%w: task:// has no route to grain's task store", namespace.ErrNotFound)
	}
	task, err := n.Reader.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	now := n.now()
	view := &namespace.TaskView{
		ID:           task.ID,
		Record:       renderTaskRecord(task, now) + "\n",
		Summary:      renderTaskSummary(task, now) + "\n",
		Conversation: renderTaskConversationFile(task),
		Handover:     taskHandover(task),
	}
	prompt, err := n.Reader.TaskPrompt(ctx, id)
	if err != nil {
		return nil, err
	}
	view.Prompt = prompt.Prompt
	for _, a := range task.Attempts {
		view.Attempts = append(view.Attempts, namespace.TaskAttemptView{
			Number:  a.Number,
			Growing: a.FinishedAt == nil,
		})
	}
	return view, nil
}

// ReadTaskAttempt reads one attempt's prompt and transcript.
func (n NamespaceTasks) ReadTaskAttempt(ctx context.Context, id string, attempt namespace.TaskAttemptView) (*namespace.TaskAttemptView, error) {
	if n.Reader == nil {
		return nil, fmt.Errorf("%w: task:// has no route to grain's task store", namespace.ErrNotFound)
	}
	out := attempt
	transcript, err := n.Reader.AttemptTranscript(ctx, id, attempt.Number)
	if err != nil {
		return nil, err
	}
	out.Transcript = transcript
	if prompts, ok := n.Reader.(AttemptPromptReader); ok {
		if out.Prompt, err = prompts.AttemptPrompt(ctx, id, attempt.Number); err != nil {
			return nil, err
		}
		return &out, nil
	}
	latest, err := n.Reader.TaskPrompt(ctx, id)
	if err != nil {
		return nil, err
	}
	if latest.Attempt == attempt.Number {
		out.Prompt = latest.Prompt
	}
	return &out, nil
}

// taskHandover is the task's handover: the latest comment written as one
// (write_handover), or "" for a task that has none.
func taskHandover(t TaskRecord) string {
	for i := len(t.Comments) - 1; i >= 0; i-- {
		if t.Comments[i].Handover {
			return strings.TrimRight(t.Comments[i].Body, "\n") + "\n"
		}
	}
	return ""
}

// renderTaskConversationFile is renderTaskConversation's view of the whole
// thread: every comment, none of them clipped, since a file has ranges
// where a tool result has pages.
func renderTaskConversationFile(t TaskRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s conversation (%d comment(s)):\n", t.ID, len(t.Comments))
	for _, c := range t.Comments {
		kind := c.AuthorKind
		if c.Handover {
			kind += ", handover"
		}
		fmt.Fprintf(&b, "  #%d [%s] %s (%s):\n", c.ID, c.CreatedAt.UTC().Format(time.RFC3339), c.Author, kind)
		b.WriteString(indent(c.Body))
	}
	return b.String()
}
