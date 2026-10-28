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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeTasks is a TaskReader over a fixed set of records -- the store
// stands in for nothing here, since what these tests are about is what a
// self-debug run reads back, not how the daemon found it.
type fakeTasks struct {
	summaries     []TaskSummary
	records       map[string]TaskRecord
	prompts       map[string]TaskPromptRecord
	transcripts   map[string]string // "<id>#<attempt>"
	broadcastFunc func(message string) (BroadcastResult, error)
	err           error
}

func (f *fakeTasks) ListTasks(context.Context) ([]TaskSummary, error) {
	return f.summaries, f.err
}

func (f *fakeTasks) Task(_ context.Context, id string) (TaskRecord, error) {
	if f.err != nil {
		return TaskRecord{}, f.err
	}
	record, ok := f.records[id]
	if ok {
		return record, nil
	}
	for _, s := range f.summaries {
		if s.ID == id {
			return TaskRecord{TaskSummary: s}, nil
		}
	}
	return TaskRecord{}, fmt.Errorf("no task %s", id)
}

func (f *fakeTasks) TaskPrompt(_ context.Context, id string) (TaskPromptRecord, error) {
	if f.err != nil {
		return TaskPromptRecord{}, f.err
	}
	return f.prompts[id], nil
}

func (f *fakeTasks) AttemptTranscript(_ context.Context, id string, attempt int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	transcript, ok := f.transcripts[fmt.Sprintf("%s#%d", id, attempt)]
	if !ok {
		return "", fmt.Errorf("no attempt %d for task %s", attempt, id)
	}
	return transcript, nil
}

func (f *fakeTasks) BroadcastTeamMessage(_ context.Context, message string) (BroadcastResult, error) {
	if f.err != nil {
		return BroadcastResult{}, f.err
	}
	if f.broadcastFunc != nil {
		return f.broadcastFunc(message)
	}
	return BroadcastResult{
		DeliveredCount: 2,
		TaskIDs:        []string{"task-2", "task-3"},
	}, nil
}

// callTask invokes one of the tools by name, the way a client
// would, so a rename of a tool breaks these tests rather than silently
// testing a function nothing serves.
func callTask(t *testing.T, reader TaskReader, name string, args map[string]any) Result {
	t.Helper()
	var allTools []Tool
	allTools = append(allTools, NewTaskTools(reader)...)
	allTools = append(allTools, NewBroadcastTools(reader)...)
	for _, tool := range allTools {
		if tool.Name == name {
			return tool.Handler(context.Background(), args)
		}
	}
	t.Fatalf("task tools registered no %s", name)
	return Result{}
}

func at(text string) time.Time {
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		panic(err)
	}
	return parsed
}

func worldOfTasks() *fakeTasks {
	filed := at("2026-01-02T03:04:05Z")
	finished := at("2026-01-02T03:30:00Z")
	return &fakeTasks{
		summaries: []TaskSummary{
			{ID: "task-1", Title: "Teach the merge queue to wait", State: "completed",
				Repo: "acme/widgets", CreatedAt: &filed},
			{ID: "task-2", Title: "Broken clone", State: "failed", Repo: "acme/widgets"},
			{ID: "task-3", Title: "Configuration agent", State: "running",
				Interactive: true, Configuration: true},
		},
		records: map[string]TaskRecord{
			"task-2": {
				TaskSummary: TaskSummary{ID: "task-2", Title: "Broken clone", State: "failed",
					Repo: "acme/widgets", CreatedAt: &filed},
				Description:       "The checkout fails before the agent gets a turn.",
				Author:            "operator",
				AuthorKind:        "human",
				Base:              "main",
				Capabilities:      []string{"github-sandbox"},
				FailedAttempts:    2,
				LastFailureReason: "cloning acme/widgets: exit status 128",
				Attempts: []TaskAttempt{
					{Number: 1, StartedAt: filed, FinishedAt: &finished, Outcome: "failed",
						Detail: "cloning acme/widgets: exit status 128", Model: "gemini-3.1-pro", AgentFramework: "antigravity"},
					{Number: 2, StartedAt: finished, Outcome: "running", Model: "gemini-3.1-flash"},
				},
				Comments: []TaskComment{
					{Author: "operator", AuthorKind: "human", CreatedAt: filed,
						Body: "This has failed twice now."},
				},
			},
		},
		prompts: map[string]TaskPromptRecord{
			"task-2": {Prompt: "You are working on task-2.", Attempt: 2},
		},
		transcripts: map[string]string{
			"task-2#1": "attempt one: git clone failed",
			"task-2#2": "attempt two: still going",
		},
	}
}

func TestListGrainTasksNamesEveryTask(t *testing.T) {
	res := callTask(t, worldOfTasks(), "list_grain_tasks", nil)
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	for _, want := range []string{
		"task-1", "task-2", "task-3",
		"[completed]", "[failed]", "[running]",
		"acme/widgets", "Teach the merge queue to wait", "(configuration agent)",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// The state filter is what makes this tool usable on a deployment with a
// long history: "which of these failed" is the question a debugging
// session actually starts from.
func TestListGrainTasksFiltersByState(t *testing.T) {
	res := callTask(t, worldOfTasks(), "list_grain_tasks", map[string]any{"state": "failed"})
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "task-2") {
		t.Errorf("text = %q, want the failed task in it", res.Text)
	}
	if strings.Contains(res.Text, "task-1") || strings.Contains(res.Text, "task-3") {
		t.Errorf("text = %q, want only the failed task", res.Text)
	}
}

// A limit that cuts the list has to say so: a reader shown 1 of 3 tasks
// with no notice would conclude the deployment has one task.
func TestListGrainTasksSaysWhatALimitDropped(t *testing.T) {
	res := callTask(t, worldOfTasks(), "list_grain_tasks", map[string]any{"limit": float64(1)})
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "2 older matching tasks are not shown") {
		t.Errorf("text = %q, want it to say what was dropped", res.Text)
	}
	// The newest end is what is kept: a debugging question is nearly
	// always about something recent.
	if !strings.Contains(res.Text, "task-3") || strings.Contains(res.Text, "task-1") {
		t.Errorf("text = %q, want the newest task kept and the oldest dropped", res.Text)
	}
}

// The daemon hands tasks over in the store's backlog order, which on a
// real deployment is newest-first, and the list used to trim from the
// front of whatever it was given: a limit of 200 on 4485 tasks returned
// the oldest 200 under a notice calling the dropped newest ones "older"
// (grain task 4702). The limit keeps the newest by filing time whatever
// order the tasks arrive in.
func TestListGrainTasksLimitKeepsTheNewestWhenSuppliedNewestFirst(t *testing.T) {
	var summaries []TaskSummary
	for n := 12; n >= 1; n-- {
		filed := at("2026-09-01T00:00:00Z").Add(time.Duration(n) * time.Hour)
		summaries = append(summaries, TaskSummary{
			ID: fmt.Sprintf("grain/task-%d", n), Title: fmt.Sprintf("Task number %d", n),
			State: "closed", CreatedAt: &filed,
		})
	}
	res := callTask(t, &fakeTasks{summaries: summaries}, "list_grain_tasks",
		map[string]any{"limit": float64(3)})
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	for n := 1; n <= 12; n++ {
		id := fmt.Sprintf("grain/task-%d ", n)
		kept := n >= 10
		if got := strings.Contains(res.Text, id); got != kept {
			t.Errorf("task %d shown = %v, want %v; text = %q", n, got, kept, res.Text)
		}
	}
	// Newest last, so the most recent task is the one beside the count.
	if i, j := strings.Index(res.Text, "grain/task-10 "), strings.Index(res.Text, "grain/task-12 "); i < 0 || j < i {
		t.Errorf("text = %q, want the kept tasks oldest first and newest last", res.Text)
	}
	if !strings.Contains(res.Text, "9 older matching tasks are not shown") {
		t.Errorf("text = %q, want it to say the 9 older tasks were dropped", res.Text)
	}
}

// Two tasks reading `[queued]` can be a task about to start and a task
// nothing will ever dispatch, and the list said the same thing about
// both: the director filed eight tasks blocked on its own never-ending
// session and went on reporting them as queued for five hours
// (grain/task-487). The ids of the blockers are on the row, which is a
// thirty-second diagnosis instead of a state that lies.
func TestListGrainTasksSaysWhatABlockedTaskIsWaitingOn(t *testing.T) {
	world := worldOfTasks()
	world.summaries = append(world.summaries, TaskSummary{
		ID: "task-4", Title: "Split out of the chat", State: "queued",
		BlockedBy: []string{"task-450"},
	})
	res := callTask(t, world, "list_grain_tasks", nil)
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "blocked on task-450") {
		t.Errorf("text = %q, want the blocked task's own blocker named on its row", res.Text)
	}
	// And nothing on the rows of tasks that are waiting on nobody.
	if strings.Count(res.Text, "blocked on") != 1 {
		t.Errorf("text = %q, want the mark on the blocked row alone", res.Text)
	}
}

func TestListGrainTasksSaysWhenNothingMatches(t *testing.T) {
	res := callTask(t, worldOfTasks(), "list_grain_tasks", map[string]any{"state": "closed"})
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "No tasks in state") {
		t.Errorf("text = %q, want a plain \"nothing matched\"", res.Text)
	}
}

// The whole point of read_grain_task: every attempt, and the error grain
// recorded for the ones that failed.
func TestReadGrainTaskShowsAttemptsAndTheirErrors(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task", map[string]any{"task_id": "task-2", "view": "full"})
	if res.IsError {
		t.Fatalf("read_grain_task reported an error: %s", res.Text)
	}
	for _, want := range []string{
		"Task task-2: Broken clone",
		"State: failed",
		"Filed by: operator (human)",
		"Consecutive failed attempts: 2",
		"cloning acme/widgets: exit status 128",
		"#1  started 2026-01-02T03:04:05Z",
		"still running",
		"on gemini-3.1-pro (antigravity)",
		"on gemini-3.1-flash\n",
		"This has failed twice now.",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

func TestReadGrainTaskReportsAnUnknownTask(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task", map[string]any{"task_id": "task-99", "view": "full"})
	if !res.IsError || !strings.Contains(res.Text, "task-99") {
		t.Errorf("text = %q (isError=%v), want an error naming the task", res.Text, res.IsError)
	}
}

func TestReadGrainTaskNeedsATaskID(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task", map[string]any{})
	if !res.IsError || !strings.Contains(res.Text, "task_id is required") {
		t.Errorf("text = %q (isError=%v), want the missing-argument error", res.Text, res.IsError)
	}
}

func TestReadGrainTaskPromptReadsWhatTheAgentWasTold(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task_prompt", map[string]any{"task_id": "task-2"})
	if res.IsError {
		t.Fatalf("read_grain_task_prompt reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "attempt 2") || !strings.Contains(res.Text, "You are working on task-2.") {
		t.Errorf("text = %q, want the prompt and which attempt got it", res.Text)
	}
}

// A task nothing has been dispatched for is not an error: it is a task
// whose prompt does not exist yet, and saying so plainly is the answer.
func TestReadGrainTaskPromptSaysWhenThereIsNone(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task_prompt", map[string]any{"task_id": "task-1"})
	if res.IsError {
		t.Fatalf("read_grain_task_prompt reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "no recorded prompt") {
		t.Errorf("text = %q, want the no-prompt-yet sentence", res.Text)
	}
}

func TestReadGrainTaskTranscriptReadsOneAttempt(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task_transcript",
		map[string]any{"task_id": "task-2", "attempt": float64(1)})
	if res.IsError {
		t.Fatalf("read_grain_task_transcript reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "attempt one: git clone failed") {
		t.Errorf("text = %q, want attempt 1's own transcript", res.Text)
	}
}

// Omitting "attempt" means the most recent one, which the tool has to
// resolve off the task's own record -- a caller debugging a task usually
// wants the run that just happened, not the one it has to count to.
func TestReadGrainTaskTranscriptDefaultsToTheLatestAttempt(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task_transcript", map[string]any{"task_id": "task-2"})
	if res.IsError {
		t.Fatalf("read_grain_task_transcript reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "attempt two: still going") {
		t.Errorf("text = %q, want the newest attempt's transcript", res.Text)
	}
}

func TestReadGrainTaskTranscriptSaysWhenATaskHasNeverRun(t *testing.T) {
	world := worldOfTasks()
	world.records["task-4"] = TaskRecord{TaskSummary: TaskSummary{ID: "task-4", State: "queued"}}
	res := callTask(t, world, "read_grain_task_transcript", map[string]any{"task_id": "task-4"})
	if !res.IsError || !strings.Contains(res.Text, "no attempts") {
		t.Errorf("text = %q (isError=%v), want the never-ran explanation", res.Text, res.IsError)
	}
}

// A store that cannot be read is reported as such rather than as an
// empty task list, which would read as a deployment with no tasks.
func TestTaskToolsReportAReadFailure(t *testing.T) {
	world := worldOfTasks()
	world.err = errors.New("reaching grain server: connection refused")
	res := callTask(t, world, "list_grain_tasks", nil)
	if !res.IsError || !strings.Contains(res.Text, "connection refused") {
		t.Errorf("text = %q (isError=%v), want the read failure reported", res.Text, res.IsError)
	}
}

// nil reader: all four tools still register, and each says why it can do
// nothing -- what a `grain mcpserver -self-debug` with no -server serves,
// and what each framework's allowedTools enumerates.
func TestTaskToolsRegisterWithoutAReader(t *testing.T) {
	tools := NewTaskTools(nil)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		res := tool.Handler(context.Background(), map[string]any{"task_id": "task-1"})
		if !res.IsError || !strings.Contains(res.Text, "no route back to the grain daemon") {
			t.Errorf("%s answered %q (isError=%v), want the no-daemon explanation",
				tool.Name, res.Text, res.IsError)
		}
	}
	want := []string{"list_grain_tasks", "read_grain_task", "read_grain_task_prompt", "read_grain_task_transcript"}
	if len(names) != len(want) {
		t.Fatalf("NewTaskTools registered %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("NewTaskTools registered %v, want %v", names, want)
		}
	}
}

func TestBroadcastToolsRegisterWithoutAReader(t *testing.T) {
	tools := NewBroadcastTools(nil)
	if len(tools) != 1 || tools[0].Name != "broadcast_team_message" {
		t.Fatalf("NewBroadcastTools registered %v, want [broadcast_team_message]", tools)
	}
	res := tools[0].Handler(context.Background(), map[string]any{"message": "hello"})
	if !res.IsError || !strings.Contains(res.Text, "no route back to the grain daemon") {
		t.Errorf("broadcast_team_message answered %q (isError=%v), want no-daemon explanation", res.Text, res.IsError)
	}
}

// queuedWorld is a deployment with one task in it whose run is over and
// whose change the merge queue is still driving -- the shape an operator
// asks about, and the one a task record could say nothing about before
// grain/task-212.
//
// The timestamps are relative to a real now rather than fixed, since what
// these assert on is the age the record renders beside them, and the
// record ages them against the clock at the moment of the call.
func queuedWorld(q TaskMergeQueue) *fakeTasks {
	filed := at("2026-01-02T03:04:05Z")
	return &fakeTasks{
		summaries: []TaskSummary{
			{ID: "task-5", Title: "Teach the merge queue to wait", State: "completed",
				Repo: "acme/widgets", CreatedAt: &filed, MergeQueue: q},
		},
		records: map[string]TaskRecord{
			"task-5": {
				TaskSummary: TaskSummary{ID: "task-5", Title: "Teach the merge queue to wait",
					State: "completed", Repo: "acme/widgets", CreatedAt: &filed, MergeQueue: q},
				Description: "It merges before the checks finish.",
				PullRequest: "https://github.com/acme/widgets/pull/12",
			},
		},
	}
}

func ago(d time.Duration) *time.Time {
	when := time.Now().Add(-d)
	return &when
}

// Every state the queue writes comes back in the words the row shows and
// in the raw name grain's own logs are written in, with the queue's note
// under it. This is the answer to "why has this been sitting at completed
// for three hours", which used to be five possible categories and a
// request that the operator read their own screen.
func TestReadGrainTaskSaysWhatTheMergeQueueIsDoing(t *testing.T) {
	for state, phrase := range map[string]string{
		"waiting_ci":      "Waiting for CI",
		"waiting_queue":   "Waiting on Queue",
		"waiting_review":  "Waiting for its review",
		"merging":         "Merging",
		"updating_branch": "Bringing the branch up to date",
		"batching":        "Testing Batch",
		"repair_needed":   "Needs repairing",
		"repair_queued":   "Queued for repair",
		"repairing":       "Resolving Conflicts",
		"agent_working":   "An agent is on this branch",
		"merge_failed":    "Merge failed",
		"unreadable":      "Can't read the pull request",
	} {
		world := queuedWorld(TaskMergeQueue{
			State: state, Note: "behind task 149 in acme/widgets", At: ago(3 * time.Hour),
		})
		res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
		if res.IsError {
			t.Fatalf("read_grain_task reported an error: %s", res.Text)
		}
		for _, want := range []string{
			fmt.Sprintf("Merge queue: %s (%s)", phrase, state),
			"(3h ago)",
			"Note: behind task 149 in acme/widgets",
		} {
			if !strings.Contains(res.Text, want) {
				t.Errorf("state %s: text = %q, want it to contain %q", state, res.Text, want)
			}
		}
	}
}

// A state this build has no phrase for is still reported, by its raw
// name. The browser drops one so that an older build never shows anybody
// "waiting_thing"; an agent explaining a row to an operator needs the
// opposite, since silence here reads as "the queue is doing nothing".
func TestReadGrainTaskReportsAnUnknownMergeQueueState(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{State: "waiting_thing", At: ago(time.Minute)})
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if !strings.Contains(res.Text, "(waiting_thing)") {
		t.Errorf("text = %q, want the raw state named", res.Text)
	}
}

// A wait grain has flagged as one in name only says so outright, rather
// than leaving an agent to read the judgement out of the queue's own
// sentence (TaskMergeQueue.StalledAt, orchestrator's liveness.go). The
// row on screen is marked the same way, so quoting this at an operator
// describes what they are looking at.
func TestReadGrainTaskSaysAWaitHasStalled(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{
		State: "waiting_ci", Note: "waiting on `go`", At: ago(4 * time.Hour),
		StalledAt: ago(90 * time.Minute),
	})
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	for _, want := range []string{"Waiting for CI", "Stalled since ", "1h ago",
		"stuck rather than as waiting", "waiting on `go`"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}

	// And an ordinary wait is left alone. A flag on every row is a flag on
	// none of them.
	world = queuedWorld(TaskMergeQueue{State: "waiting_ci", Note: "waiting on `go`", At: ago(time.Minute)})
	res = callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if strings.Contains(res.Text, "Stalled") {
		t.Errorf("text = %q, want a wait inside its patience described as a wait", res.Text)
	}
}

// The list marks it too, in two words. A list is read to pick the row
// worth opening, and a wait nothing is getting anywhere with reads
// exactly like a healthy one without this.
func TestListGrainTasksMarksAStalledRow(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{
		State: "waiting_ci", Note: "waiting on `go`", At: ago(4 * time.Hour),
		StalledAt: ago(90 * time.Minute),
	})
	res := callTask(t, world, "list_grain_tasks", nil)
	if !strings.Contains(res.Text, "merge queue: Waiting for CI (stalled)") {
		t.Errorf("text = %q, want the stalled row marked as one", res.Text)
	}
}

// The section is absent, not empty, for the overwhelming majority of
// tasks: one the queue has never touched has no merge-queue facts, and a
// line saying so is a line to rule out on every record anybody reads.
func TestReadGrainTaskHasNoMergeSectionWithoutTheQueue(t *testing.T) {
	res := callTask(t, worldOfTasks(), "read_grain_task", map[string]any{"task_id": "task-2", "view": "full"})
	if res.IsError {
		t.Fatalf("read_grain_task reported an error: %s", res.Text)
	}
	if strings.Contains(res.Text, "Merge queue") {
		t.Errorf("text = %q, want no merge queue section for a task the queue never drove", res.Text)
	}
	if strings.Contains(res.Text, "Activity:") {
		t.Errorf("text = %q, want no activity line for a task that is saying nothing", res.Text)
	}
}

// An entry the queue has given up on says so outright, and says what that
// means: nothing further happens automatically until a person presses
// "Retry merge". A blocked entry that merely said "Merge failed" would
// read as a state the queue might still work its way out of.
func TestReadGrainTaskSaysAMergeQueueEntryIsBlocked(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{
		State: "merge_failed", Note: "GitHub refused the merge: not mergeable",
		At: ago(90 * time.Minute), BlockedAt: ago(time.Hour),
	})
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if res.IsError {
		t.Fatalf("read_grain_task reported an error: %s", res.Text)
	}
	for _, want := range []string{"Blocked since ", "(1h ago)", "Retry merge"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// A blocked stamp with no live state under it -- the pull request closed
// on the cycle that cleared the state -- still reports the stamp rather
// than rendering as a task the queue never touched.
func TestReadGrainTaskReportsABlockedStampWithNoState(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{BlockedAt: ago(2 * 24 * time.Hour)})
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	for _, want := range []string{"no live state", "Blocked since ", "(2d ago)"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// A merge-queue observation can outlive the pull request it describes,
// and a closed task's own row shows none of it. The record still reports
// what the queue last wrote -- it is evidence -- but says that the row
// does not show it, since an agent quoting "Merging" about a row showing
// nothing is the confusion this section is meant to end.
func TestReadGrainTaskMarksAClosedTasksMergeStateStale(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{State: "merging", At: ago(2 * time.Hour)})
	record := world.records["task-5"]
	record.State = "closed"
	world.records["task-5"] = record
	world.summaries[0].State = "closed"

	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if !strings.Contains(res.Text, "Merge queue: Merging (merging)") {
		t.Errorf("text = %q, want the queue's last word kept", res.Text)
	}
	if !strings.Contains(res.Text, "what the queue last wrote is stale") {
		t.Errorf("text = %q, want the closed task's row said to show none of it", res.Text)
	}

	list := callTask(t, world, "list_grain_tasks", nil)
	if strings.Contains(list.Text, "merge queue: ") {
		t.Errorf("list = %q, want a closed row to read as the UI's own does", list.Text)
	}
}

// A long note is cut where it is written rather than being left to push
// the description and the attempts out of the head of the answer -- and
// the cut says how much went, so a reader never takes the head of a note
// for the whole of it.
func TestReadGrainTaskBoundsALongMergeQueueNote(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{
		State: "waiting_ci", Note: strings.Repeat("checks are still running. ", 4000),
		At: ago(time.Minute),
	})
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if res.IsError {
		t.Fatalf("read_grain_task reported an error: %s", res.Text)
	}
	if len(res.Text) > maxMergeQueueNoteBytes*4 {
		t.Errorf("text is %d bytes, want the note bounded near %d", len(res.Text), maxMergeQueueNoteBytes)
	}
	if !strings.Contains(res.Text, "bytes cut") {
		t.Errorf("text = %q, want the cut stated", res.Text)
	}
	// The rest of the record is still there: the point of bounding the
	// note is that it does not crowd out what a record is read for.
	if !strings.Contains(res.Text, "It merges before the checks finish.") {
		t.Errorf("text = %q, want the description kept", res.Text)
	}
}

// The other thing an operator asks about a row: what the run says it is
// doing, and how long it has been saying it. Whose voice it is comes with
// it, since grain's own setup notes and an agent's own line read alike.
func TestReadGrainTaskShowsTheActivityLine(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{})
	record := world.records["task-5"]
	record.State = "running"
	record.Activity = TaskActivity{Note: "waiting for CI on the second push", At: ago(22 * time.Minute)}
	world.records["task-5"] = record
	res := callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	for _, want := range []string{
		"Activity: waiting for CI on the second push",
		"what this run says it is doing",
		"(22m ago)",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}

	record.Activity.BySetup = true
	world.records["task-5"] = record
	res = callTask(t, world, "read_grain_task", map[string]any{"task_id": "task-5", "view": "full"})
	if !strings.Contains(res.Text, "what grain is doing to get this run started") {
		t.Errorf("text = %q, want grain's own voice marked as its own", res.Text)
	}
}

// The list carries the state so that "which rows are stuck?" is one call
// rather than one per row -- and carries neither the note nor the age,
// which are the questions asked once a row has been picked out.
func TestListGrainTasksCarriesTheMergeQueueState(t *testing.T) {
	world := queuedWorld(TaskMergeQueue{
		State: "unreadable", Note: "GitHub said 404 reading acme/widgets#12", At: ago(time.Hour),
	})
	res := callTask(t, world, "list_grain_tasks", nil)
	if res.IsError {
		t.Fatalf("list_grain_tasks reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "merge queue: Can't read the pull request") {
		t.Errorf("text = %q, want the queue's own phrase on the row", res.Text)
	}
	if strings.Contains(res.Text, "404") || strings.Contains(res.Text, "1h ago") {
		t.Errorf("text = %q, want the note and the age kept off the list", res.Text)
	}
}

// A task the queue is not driving takes no width on the row at all: the
// list is read by a run with a context to spend, and most tasks in a
// store have nothing to say here.
func TestListGrainTasksSaysNothingForTasksOffTheQueue(t *testing.T) {
	res := callTask(t, worldOfTasks(), "list_grain_tasks", nil)
	if strings.Contains(res.Text, "merge queue: ") {
		t.Errorf("text = %q, want no merge queue column for tasks the queue never drove", res.Text)
	}
}

// A transcript longer than one result may carry comes back head-and-tail
// with a notice, the same cap run_command and read_file answer under: a
// run's whole session can be far larger than its own context.
func TestReadGrainTaskTranscriptIsCapped(t *testing.T) {
	world := worldOfTasks()
	world.transcripts["task-2#1"] = strings.Repeat("a line of transcript\n", 10000)
	res := callTask(t, world, "read_grain_task_transcript",
		map[string]any{"task_id": "task-2", "attempt": float64(1)})
	if res.IsError {
		t.Fatalf("read_grain_task_transcript reported an error: %s", res.Text)
	}
	limit := int(maxToolResultBytes.Load())
	if len(res.Text) > 2*limit {
		t.Errorf("text is %d bytes, want it capped near %d", len(res.Text), limit)
	}
	if !strings.Contains(res.Text, "grain's own UI") {
		t.Errorf("text = %q, want the elision notice saying where the rest is", res.Text)
	}
}

func TestBroadcastTeamMessageDeliversToTasks(t *testing.T) {
	world := worldOfTasks()
	var calledMsg string
	world.broadcastFunc = func(message string) (BroadcastResult, error) {
		calledMsg = message
		return BroadcastResult{
			DeliveredCount: 2,
			TaskIDs:        []string{"task-2", "task-3"},
		}, nil
	}

	res := callTask(t, world, "broadcast_team_message", map[string]any{
		"message": "Deployment paused for maintenance.",
	})
	if res.IsError {
		t.Fatalf("broadcast_team_message reported error: %s", res.Text)
	}
	if calledMsg != "Deployment paused for maintenance." {
		t.Errorf("calledMsg=%q, want the message", calledMsg)
	}
	if !strings.Contains(res.Text, "Broadcast delivered to 2 active task(s): task-2, task-3") {
		t.Errorf("unexpected output text: %q", res.Text)
	}
	if strings.Contains(res.Text, "team") {
		t.Errorf("output text %q names a team; grain has only the deployment", res.Text)
	}
}

// The tool no longer takes a team, and one an older prompt still passes
// is ignored rather than refused: there is only the deployment to reach.
func TestBroadcastTeamMessageIgnoresARetiredTeamArgument(t *testing.T) {
	world := worldOfTasks()
	called := false
	world.broadcastFunc = func(message string) (BroadcastResult, error) {
		called = true
		return BroadcastResult{DeliveredCount: 1, TaskIDs: []string{"task-1"}}, nil
	}

	res := callTask(t, world, "broadcast_team_message", map[string]any{
		"team":    "infra",
		"message": "deployment-wide notice",
	})
	if res.IsError || !called {
		t.Errorf("a broadcast naming a team was refused: %q (called=%v)", res.Text, called)
	}
	if _, ok := broadcastTeamMessageInputSchema["properties"].(map[string]any)["team"]; ok {
		t.Error("broadcast_team_message still offers a team argument")
	}
}

func TestBroadcastTeamMessageRequiresMessage(t *testing.T) {
	world := worldOfTasks()
	res := callTask(t, world, "broadcast_team_message", map[string]any{})
	if !res.IsError || !strings.Contains(res.Text, "A non-empty \"message\" is required") {
		t.Errorf("text = %q (isError=%v), want message required error", res.Text, res.IsError)
	}
}

func TestBroadcastTeamMessageReportsNoActiveTasks(t *testing.T) {
	world := worldOfTasks()
	world.broadcastFunc = func(message string) (BroadcastResult, error) {
		return BroadcastResult{DeliveredCount: 0, TaskIDs: []string{}}, nil
	}

	res := callTask(t, world, "broadcast_team_message", map[string]any{
		"message": "Hello",
	})
	if res.IsError {
		t.Fatalf("broadcast_team_message reported error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Broadcast sent: this deployment has no active tasks.") {
		t.Errorf("unexpected output text: %q", res.Text)
	}
}

func TestBroadcastTeamMessageReportsError(t *testing.T) {
	world := worldOfTasks()
	world.broadcastFunc = func(message string) (BroadcastResult, error) {
		return BroadcastResult{}, errors.New("daemon unreachable")
	}

	res := callTask(t, world, "broadcast_team_message", map[string]any{
		"message": "Hello",
	})
	if !res.IsError || !strings.Contains(res.Text, "Error broadcasting the message: daemon unreachable") {
		t.Errorf("text = %q (isError=%v), want error text", res.Text, res.IsError)
	}
}
