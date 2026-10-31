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
	"slices"
	"strings"
	"time"
)

// task_list_cli.go is list_grain_tasks for the sandbox's `grain task
// list` (pkg/gitproxy's /cli/tasks): the same filters, the same order and
// the same newest-kept limit, rendered one line per task so the answer
// pipes to grep and awk. Before it, `grain task list` was a listing of
// task:// -- every id the deployment had ever filed, sorted as strings,
// whatever flags it was given (grain task 1945).

// DefaultTaskListLimit is how many tasks `grain task list` answers with
// when it names no --limit: list_grain_tasks' own default.
const DefaultTaskListLimit = defaultTaskListLimit

// TaskSummaryLister is what a namespace.TaskProvider also implements
// when it can list tasks rather than only their ids: NamespaceTasks does.
type TaskSummaryLister interface {
	ListTaskSummaries(ctx context.Context) ([]TaskSummary, error)
}

var _ TaskSummaryLister = NamespaceTasks{}

// ListTaskSummaries is every task, as list_grain_tasks reads them.
func (n NamespaceTasks) ListTaskSummaries(ctx context.Context) ([]TaskSummary, error) {
	if n.Reader == nil {
		return nil, fmt.Errorf("task:// has no route to grain's task store")
	}
	return n.Reader.ListTasks(ctx)
}

// TaskListQuery is `grain task list`'s flags.
type TaskListQuery struct {
	// States is the states a task may be in to be listed; empty is any.
	States       []string
	Repo         string
	Search       string
	Kind         string
	ChangedSince time.Time
	Stalled      bool
	// Limit is how many of the newest matching tasks to list; zero or
	// less is DefaultTaskListLimit.
	Limit int
}

func (q TaskListQuery) filter() taskListFilter {
	return taskListFilter{
		State:        strings.Join(q.States, ","),
		ChangedSince: q.ChangedSince,
		Stalled:      q.Stalled,
		Kind:         strings.ToLower(strings.TrimSpace(q.Kind)),
		Repo:         strings.TrimSpace(q.Repo),
		Search:       strings.TrimSpace(q.Search),
	}
}

// CheckTaskKind is list_grain_tasks' refusal of a kind outside its
// vocabulary, or nil.
func CheckTaskKind(kind string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" || slices.Contains(taskKindVocabulary, kind) {
		return nil
	}
	return fmt.Errorf("kind %q is not one of %s", kind, strings.Join(taskKindVocabulary, ", "))
}

// TaskListPage is one answer to a TaskListQuery.
type TaskListPage struct {
	// Tasks is what is shown: oldest first by filing, newest last.
	Tasks []TaskSummary
	// Matched is how many tasks matched before Limit cut them.
	Matched int
	filter  taskListFilter
}

// ListTaskPage narrows tasks to q.
func ListTaskPage(tasks []TaskSummary, q TaskListQuery) TaskListPage {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultTaskListLimit
	}
	f := q.filter()
	shown, matched := selectTasks(tasks, f, limit)
	return TaskListPage{Tasks: shown, Matched: matched, filter: f}
}

// Lines is the page one line per task -- id, state, repo ("-" for none)
// and title, then "(stalled)" and "blocked on <ids>" where they apply --
// with nothing else in it, so that a count of lines is a count of tasks.
// What the list leaves out is Note's to say.
func (p TaskListPage) Lines() string {
	var b strings.Builder
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "%s  %s  %s  %s", t.ID, t.State, blankAsNone(t.Repo, "-"), singleLine(t.Title))
		if t.Priority > 0 && t.Priority != 5 {
			fmt.Fprintf(&b, "  (priority %d)", t.Priority)
		}
		if taskStalled(t) {
			b.WriteString("  (stalled)")
		}
		if len(t.BlockedBy) > 0 {
			fmt.Fprintf(&b, "  blocked on %s", strings.Join(t.BlockedBy, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Note is the page's count and elision in one sentence: a list that
// silently stopped at its limit would have the reader concluding that is
// every task there is.
func (p TaskListPage) Note() string {
	if p.Matched == 0 {
		if p.filter.narrowed() {
			return fmt.Sprintf("No tasks %s.", p.filter.describe())
		}
		return "No tasks."
	}
	note := fmt.Sprintf("%d task(s) shown of %d matching, oldest first.", len(p.Tasks), p.Matched)
	if dropped := p.Matched - len(p.Tasks); dropped > 0 {
		note += fmt.Sprintf(" %d older matching tasks are not shown -- raise --limit to see them.", dropped)
	}
	return note
}

// TaskListEntry is one task as `grain task list --json` prints it.
type TaskListEntry struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Repo       string     `json:"repo,omitempty"`
	Title      string     `json:"title"`
	Priority   int        `json:"priority,omitempty"`
	Kinds      []string   `json:"kinds,omitempty"`
	BlockedBy  []string   `json:"blockedBy,omitempty"`
	Stalled    bool       `json:"stalled,omitempty"`
	MergeQueue string     `json:"mergeQueue,omitempty"`
	CreatedAt  *time.Time `json:"createdAt,omitempty"`
	ChangedAt  *time.Time `json:"changedAt,omitempty"`
}

// Entries is the page as TaskListEntry values, never nil.
func (p TaskListPage) Entries() []TaskListEntry {
	out := make([]TaskListEntry, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		priority := t.Priority
		if priority <= 0 {
			priority = 5
		}
		e := TaskListEntry{
			ID: t.ID, State: t.State, Repo: t.Repo, Title: t.Title,
			Priority:  priority,
			Kinds:     t.Kinds, BlockedBy: t.BlockedBy, Stalled: taskStalled(t),
			CreatedAt: t.CreatedAt, ChangedAt: t.ChangedAt,
		}
		if !strings.EqualFold(t.State, "closed") {
			e.MergeQueue = t.MergeQueue.State
		}
		out = append(out, e)
	}
	return out
}
