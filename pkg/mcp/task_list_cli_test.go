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
	"strings"
	"testing"
	"time"
)

func TestTaskListPageLinesAreOnePerTaskInFilingOrder(t *testing.T) {
	at := func(m int) *time.Time {
		v := time.Date(2026, 10, 1, 0, m, 0, 0, time.UTC)
		return &v
	}
	tasks := []TaskSummary{
		{ID: "1000", State: "queued", Repo: "acme/widgets", Title: "Newest\nfix", CreatedAt: at(3), BlockedBy: []string{"999", "998"}},
		{ID: "999", State: "running", Repo: "acme/widgets", Title: "Middle", CreatedAt: at(2), Stall: "running for 9h"},
		{ID: "10", State: "proposed", Title: "Oldest", CreatedAt: at(1)},
		{ID: "11", State: "closed", Repo: "acme/gadgets", Title: "Closed", CreatedAt: at(0)},
	}
	page := ListTaskPage(tasks, TaskListQuery{States: []string{"queued", "running", "proposed"}})
	want := "10  proposed  -  Oldest\n" +
		"999  running  acme/widgets  Middle  (stalled)\n" +
		"1000  queued  acme/widgets  Newest fix  blocked on 999, 998\n"
	if got := page.Lines(); got != want {
		t.Errorf("Lines() =\n%s\nwant\n%s", got, want)
	}
	if note := page.Note(); note != "3 task(s) shown of 3 matching, oldest first." {
		t.Errorf("Note() = %q", note)
	}

	page = ListTaskPage(tasks, TaskListQuery{Limit: 1, Repo: "ACME/widgets"})
	if got := page.Lines(); !strings.HasPrefix(got, "1000  ") || strings.Count(got, "\n") != 1 {
		t.Errorf("--limit 1 --repo kept %q, want only the newest", got)
	}
	if note := page.Note(); !strings.Contains(note, "1 older matching tasks are not shown") {
		t.Errorf("Note() = %q", note)
	}

	page = ListTaskPage(tasks, TaskListQuery{Search: "nothing like it"})
	if page.Lines() != "" || page.Note() != `No tasks with "nothing like it" in the title.` {
		t.Errorf("empty page: %q %q", page.Lines(), page.Note())
	}
	if err := CheckTaskKind("Port"); err != nil {
		t.Errorf("CheckTaskKind(Port) = %v", err)
	}
	if err := CheckTaskKind("nope"); err == nil {
		t.Error("CheckTaskKind(nope) = nil")
	}
}

func TestListGrainTasksTakesSeveralStates(t *testing.T) {
	tasks := []TaskSummary{
		{ID: "1", State: "queued", Title: "a"},
		{ID: "2", State: "proposed", Title: "b"},
		{ID: "3", State: "closed", Title: "c"},
	}
	got := renderFilteredTaskList(tasks, taskListFilter{State: "proposed, queued"}, 10)
	if !strings.Contains(got, "1  [queued]") || !strings.Contains(got, "2  [proposed]") || strings.Contains(got, "3  [closed]") {
		t.Errorf("state \"proposed, queued\" =\n%s", got)
	}
}

// A task parked on an operator's Stop waits on that person, not on
// anything grain is failing to do: it is marked as such, and --stalled
// leaves it out, whatever its merge-queue row says (task 2345).
func TestTaskListMarksAStoppedTurnRatherThanStalled(t *testing.T) {
	blocked := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tasks := []TaskSummary{
		{ID: "1977", State: "awaiting_reply", Repo: "acme/widgets", Title: "Stopped", TurnStopped: true,
			MergeQueue: TaskMergeQueue{State: "merge_failed", BlockedAt: &blocked}},
		{ID: "1978", State: "running", Repo: "acme/widgets", Title: "Slow", Stall: "running for 9h"},
	}
	got := ListTaskPage(tasks, TaskListQuery{}).Lines()
	want := "1977  awaiting_reply  acme/widgets  Stopped  (stopped by operator)\n" +
		"1978  running  acme/widgets  Slow  (stalled)\n"
	if got != want {
		t.Errorf("Lines() =\n%s\nwant\n%s", got, want)
	}
	if got := ListTaskPage(tasks, TaskListQuery{Stalled: true}).Lines(); strings.Contains(got, "1977") {
		t.Errorf("--stalled kept the stopped turn:\n%s", got)
	}
	entries := ListTaskPage(tasks, TaskListQuery{}).Entries()
	if !entries[0].StoppedByOperator || entries[0].Stalled {
		t.Errorf("JSON entry = %+v, want stoppedByOperator and not stalled", entries[0])
	}
}
