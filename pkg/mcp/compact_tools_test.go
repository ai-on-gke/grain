package mcp

// Tests for the compact pipeline tools a judgement agent works from
// (docs/design/durable-tasks.md#DT-13): read_pipeline_changes, the
// summary and conversation views of read_grain_task, list_grain_tasks'
// filters, list_ci_runs' summary default, query_merge_queue's compact
// default and the per-run tool budget -- and the measurement that a
// steward wake's reads, made through them, fit that budget.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

type fakePipeline struct {
	page  PipelineChangesPage
	after string
	limit int
}

func (f *fakePipeline) PipelineChanges(_ context.Context, after string, limit int) (PipelineChangesPage, error) {
	f.after, f.limit = after, limit
	return f.page, nil
}

func TestReadPipelineChangesIsOneLinePerChangeEndingWithTheCursor(t *testing.T) {
	reader := &fakePipeline{page: PipelineChangesPage{
		Changes: []PipelineChange{
			{At: at("2026-10-26T08:00:00Z"), ID: "832", Kind: "task", From: "running", To: "completed"},
			{At: at("2026-10-26T08:01:00Z"), ID: "acme/widgets@main", Kind: "base", From: "running", To: "failure", Detail: "run ci-9"},
		},
		Next: "2026-10-26T08:01:00Z~0", More: true,
	}}
	res := NewPipelineTools(reader)[0].Handler(context.Background(), map[string]any{"after": "2026-10-26T07:00:00Z~3", "limit": float64(2)})
	if res.IsError {
		t.Fatal(res.Text)
	}
	want := "task 832 task running→completed 2026-10-26T08:00:00Z\n" +
		"acme/widgets@main base running→failure 2026-10-26T08:01:00Z (run ci-9)\n" +
		"(more changes follow: read again with this cursor)\n" +
		"next: 2026-10-26T08:01:00Z~0"
	if res.Text != want {
		t.Errorf("text =\n%s\nwant\n%s", res.Text, want)
	}
	if reader.after != "2026-10-26T07:00:00Z~3" || reader.limit != 2 {
		t.Errorf("asked after %q limit %d", reader.after, reader.limit)
	}
	if res := NewPipelineTools(nil)[0].Handler(context.Background(), nil); !res.IsError {
		t.Error("a run with no daemon was answered")
	}
}

// heavyTask is a record with the weight a real one carries: a long
// description, a long thread and a handover.
func heavyTask(id string, kinds ...string) TaskRecord {
	filed := at("2026-10-20T00:00:00Z")
	finished := filed.Add(time.Hour)
	r := TaskRecord{
		TaskSummary: TaskSummary{ID: id, Title: "Port the merge-queue fix " + id, State: "completed",
			Repo: "acme/widgets", CreatedAt: &filed, Kinds: kinds, DependsOn: []string{"831"}, ChangedAt: &finished},
		Description: strings.Repeat("What this task is for, at length. ", 120),
		Base:        "candidate",
		Attempts: []TaskAttempt{{Number: 1, StartedAt: filed, FinishedAt: &finished, Outcome: "success",
			Detail: "pushed a branch"}},
	}
	for i := range 12 {
		r.Comments = append(r.Comments, TaskComment{ID: int64(100 + i), Author: "grain", AuthorKind: "system",
			Body: strings.Repeat(fmt.Sprintf("comment %d says a good deal. ", i), 30), CreatedAt: filed.Add(time.Duration(i) * time.Minute)})
	}
	r.Comments = append(r.Comments, TaskComment{ID: 200, Author: "steward", AuthorKind: "agent", Handover: true,
		Body:      "cursor 2026-10-26T08:00:00Z~0\nwatching 832 and 833\nnothing else open\nline four\nline five\nline six",
		CreatedAt: filed.Add(2 * time.Hour)})
	return r
}

func TestReadGrainTaskSummaryIsAboutAKilobyte(t *testing.T) {
	reader := &fakeTasks{records: map[string]TaskRecord{"832": heavyTask("832", "port")}}
	res := callTask(t, reader, "read_grain_task", map[string]any{"task_id": "832"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if len(res.Text) > 1300 {
		t.Errorf("summary is %d bytes, want about 1 KB:\n%s", len(res.Text), res.Text)
	}
	for _, want := range []string{"State: completed", "Kind: port", "base candidate", "Depends on: 831",
		"Last events:", "handover #200", "Handover (head):", "cursor 2026-10-26T08:00:00Z~0", "..."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("summary lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "line six") || strings.Contains(res.Text, "What this task is for") {
		t.Errorf("summary carries what only the full view should:\n%s", res.Text)
	}
	if full := callTask(t, reader, "read_grain_task", map[string]any{"task_id": "832", "view": "full"}); !strings.Contains(full.Text, "What this task is for") {
		t.Error("view full lost the description")
	}
	if bad := callTask(t, reader, "read_grain_task", map[string]any{"task_id": "832", "view": "everything"}); !bad.IsError {
		t.Error("an unknown view was answered")
	}
}

func TestReadGrainTaskConversationPagesByCommentID(t *testing.T) {
	reader := &fakeTasks{records: map[string]TaskRecord{"832": heavyTask("832")}}
	res := callTask(t, reader, "read_grain_task", map[string]any{"task_id": "832", "view": "conversation", "after": float64(109)})
	if res.IsError {
		t.Fatal(res.Text)
	}
	for _, want := range []string{"#110 ", "#111 ", "#200 ", "That is the end of the thread."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("page lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "#109 [") {
		t.Errorf("page repeats the comment it was asked to start after:\n%s", res.Text)
	}
}

func TestListGrainTasksFilters(t *testing.T) {
	old, recent := at("2026-10-01T00:00:00Z"), time.Now().Add(-10*time.Minute)
	stalledAt := recent
	reader := &fakeTasks{summaries: []TaskSummary{
		{ID: "831", Title: "the fix", State: "closed", ChangedAt: &old},
		{ID: "832", Title: "its port", State: "completed", Kinds: []string{"port"}, ChangedAt: &recent},
		{ID: "833", Title: "a repair", State: "queued", Kinds: []string{"repair"}, Stall: "queued too long", ChangedAt: &old},
		{ID: "834", Title: "stuck merge", State: "completed", ChangedAt: &old,
			MergeQueue: TaskMergeQueue{State: "waiting_ci", StalledAt: &stalledAt}},
	}}
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"changed_since": "1h"}, []string{"832"}},
		{map[string]any{"stalled": true}, []string{"833", "834"}},
		{map[string]any{"kind": "port"}, []string{"832"}},
		{map[string]any{"kind": "repair", "stalled": true}, []string{"833"}},
	} {
		res := callTask(t, reader, "list_grain_tasks", tc.args)
		if res.IsError {
			t.Fatalf("%v: %s", tc.args, res.Text)
		}
		for _, s := range reader.summaries {
			listed := strings.Contains(res.Text, s.ID+"  [")
			if listed != containsString(tc.want, s.ID) {
				t.Errorf("%v: task %s listed=%v, want %v:\n%s", tc.args, s.ID, listed, !listed, res.Text)
			}
		}
	}
	if res := callTask(t, reader, "list_grain_tasks", map[string]any{"kind": "porcupine"}); !res.IsError {
		t.Error("an unknown kind was answered")
	}
	if res := callTask(t, reader, "list_grain_tasks", map[string]any{"kind": "session"}); !strings.Contains(res.Text, "No tasks of kind session") {
		t.Errorf("empty answer = %q", res.Text)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// pagedCIRunner is a deployment with n runs, one a minute, newest first.
type pagedCIRunner struct {
	fakeGrainCIRunner
	runs []ci.Run
}

func newPagedCIRunner(n int) *pagedCIRunner {
	r := &pagedCIRunner{}
	start := at("2026-10-26T00:00:00Z")
	for i := n; i >= 1; i-- {
		started := start.Add(time.Duration(i) * time.Minute)
		run := ci.Run{ID: fmt.Sprintf("ci-%d", i), Repo: "acme/widgets", Ref: fmt.Sprintf("grain/task-%d", i),
			StartedAt: &started, Conclusion: ci.ConclusionSuccess,
			Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess, Error: strings.Repeat("log ", 200)}}}
		if i%3 == 0 {
			run.Conclusion = ci.ConclusionFailure
			run.Jobs = append(run.Jobs, ci.JobRun{Name: "ui e2e", Conclusion: ci.ConclusionFailure, Error: strings.Repeat("dump ", 300)})
		}
		r.runs = append(r.runs, run)
	}
	return r
}

func (r *pagedCIRunner) GrainCIRun(_ context.Context, id string) (ci.Run, error) {
	for _, run := range r.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return ci.Run{}, fmt.Errorf("no run %s", id)
}

func (r *pagedCIRunner) FindGrainCIRuns(_ context.Context, f ci.RunFilter, limit int) (ci.RunPage, error) {
	var matched []ci.Run
	for _, run := range r.runs {
		if !f.Until.IsZero() && !run.StartedAt.Before(f.Until) {
			continue
		}
		if len(f.States) > 0 && !containsString(f.States, string(run.Conclusion)) {
			continue
		}
		matched = append(matched, run)
	}
	page := ci.RunPage{Matched: len(matched), Stored: len(r.runs)}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	page.Runs = matched
	return page, nil
}

func (r *pagedCIRunner) GrainCISharedFaults(context.Context) ([]CIFault, error) { return nil, nil }

func TestListCIRunsSummaryIsOneLinePerRunAndPagesWithAfter(t *testing.T) {
	list := NewGrainCIWaitTools(newPagedCIRunner(30), CIRepoScope{AllRepos: true})[1]
	res := list.Handler(context.Background(), map[string]any{"limit": float64(5)})
	if res.IsError {
		t.Fatal(res.Text)
	}
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 7 {
		t.Fatalf("summary is %d lines, want a heading, five runs and a footer:\n%s", len(lines), res.Text)
	}
	if lines[1] != "ci-30 acme/widgets grain/task-30 failure 2026-10-26T00:30:00Z failing: ui e2e" {
		t.Errorf("first run = %q", lines[1])
	}
	if !strings.Contains(res.Text, `read on with after "ci-26"`) {
		t.Errorf("footer does not say where to read on from:\n%s", res.Text)
	}
	next := list.Handler(context.Background(), map[string]any{"limit": float64(5), "after": "ci-26"})
	if next.IsError || !strings.Contains(next.Text, "\nci-25 ") || strings.Contains(next.Text, "ci-26 ") {
		t.Errorf("the page after ci-26 =\n%s", next.Text)
	}
	if full := list.Handler(context.Background(), map[string]any{"limit": float64(5), "view": "full"}); !strings.Contains(full.Text, "Job ui e2e: failure") {
		t.Errorf("view full lost the jobs:\n%s", full.Text)
	}
}

type fakeMergeQueue struct{ report MergeQueueReport }

func (f fakeMergeQueue) QueryMergeQueue(context.Context, string, string) (MergeQueueReport, error) {
	return f.report, nil
}

func TestQueryMergeQueueCompactAndChangedSince(t *testing.T) {
	recent, old := time.Now().Add(-5*time.Minute), time.Now().Add(-5*time.Hour)
	reader := fakeMergeQueue{report: MergeQueueReport{QueuedPRs: []QueuedPRReport{
		{TaskID: "832", Number: 12, Repo: "acme/widgets", Branch: "main", State: "waiting_ci", At: &recent, Stalled: true,
			Note: strings.Repeat("a long note ", 40), MergePolicy: "rule one\nrule two"},
		{TaskID: "833", Number: 13, Repo: "acme/widgets", Branch: "main", State: "waiting_queue", At: &old},
	}}}
	tool := NewMergeQueueTools(reader)[0]
	res := tool.Handler(context.Background(), map[string]any{"changed_since": "1h"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	if !strings.Contains(res.Text, "1. task 832 PR #12 acme/widgets@main waiting_ci stalled since ") {
		t.Errorf("compact line missing:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "833") || !strings.Contains(res.Text, "1 batch(es) and queued change(s) unchanged") {
		t.Errorf("changed_since did not hide and count the unmoved change:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "a long note") {
		t.Errorf("compact carries the note:\n%s", res.Text)
	}
	if full := tool.Handler(context.Background(), map[string]any{"view": "full"}); !strings.Contains(full.Text, "a long note") {
		t.Error("view full lost the note")
	}
}

func TestToolBudgetNoticesPastEachLine(t *testing.T) {
	for _, tc := range []struct {
		calls, bytes int
		want         string
	}{
		{ToolBudgetSoftCalls - 1, 0, ""},
		{ToolBudgetSoftCalls, 0, "past its target of 40 calls"},
		{1, ToolBudgetSoftBytes, "past its target of 40 calls"},
		{ToolBudgetHardCalls, 0, "Write your handover now"},
		{1, ToolBudgetHardBytes, "Write your handover now"},
	} {
		got := toolBudgetNotice(tc.calls, tc.bytes)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("notice(%d calls, %d bytes) = %q, want %q", tc.calls, tc.bytes, got, tc.want)
		}
	}
	// And through a registry: counted only once announced.
	registry := NewRegistry()
	registry.Register(Tool{Name: "big", Handler: func(context.Context, map[string]any) Result {
		return Result{Text: strings.Repeat("x", 4<<10)}
	}})
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	call := func() string {
		res, err := client.CallTool(context.Background(), "big", nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Text()
	}
	for range ToolBudgetSoftCalls + 1 {
		if strings.Contains(call(), "Tool budget") {
			t.Fatal("a registry with no budget announced counted")
		}
	}
	registry.AnnounceToolBudget()
	var last string
	for range ToolBudgetSoftCalls {
		last = call()
	}
	if !strings.Contains(last, "Tool budget: this wake has made 40 tool calls and 160 KB") {
		t.Errorf("the 40th call carries no notice: %q", last[len(last)-200:])
	}
}

// stewardFixture is a deployment the size the pipeline steward's first
// wake read: 300 tasks with real-sized records, 200 CI runs, and a merge
// queue with a dozen changes in it -- of which, since the last wake, the
// handful the design's account of that wake names (831-836) moved.
type stewardFixture struct {
	tasks    *fakeTasks
	pipeline *fakePipeline
	ci       *pagedCIRunner
	queue    fakeMergeQueue
}

func newStewardFixture() stewardFixture {
	f := stewardFixture{tasks: &fakeTasks{records: map[string]TaskRecord{}}, ci: newPagedCIRunner(200)}
	now := time.Now()
	for i := 600; i < 900; i++ {
		id := fmt.Sprint(i)
		var kinds []string
		if i == 832 || i == 836 {
			kinds = []string{"port"}
		}
		if i == 833 {
			kinds = []string{"repair"}
		}
		r := heavyTask(id, kinds...)
		changed := now.Add(-time.Duration(900-i) * time.Hour)
		r.ChangedAt = &changed
		if i == 834 {
			r.Stall = "queued for 3h, past its 1h patience: dispatch should have taken it"
			r.State = "queued"
		}
		f.tasks.records[id] = r
		f.tasks.summaries = append(f.tasks.summaries, r.TaskSummary)
	}
	ats := now.Add(-20 * time.Minute)
	f.pipeline = &fakePipeline{page: PipelineChangesPage{Next: "2026-10-26T08:30:00Z~0", Changes: []PipelineChange{
		{At: ats, ID: "831", Kind: "task", From: "completed", To: "merged"},
		{At: ats, ID: "832", Kind: "task", From: "filed", To: "running"},
		{At: ats, ID: "832", Kind: "task", From: "running", To: "completed"},
		{At: ats, ID: "832", Kind: "queue", To: "waiting_ci"},
		{At: ats, ID: "acme/widgets@main", Kind: "base", From: "running", To: "failure", Detail: "run ci-198"},
		{At: ats, ID: "833", Kind: "task", To: "filed"},
		{At: ats, ID: "834", Kind: "stall", To: "queued stalled"},
		{At: ats, ID: "batch 41", Kind: "batch", From: "running", To: "failed", Detail: "acme/widgets@main merged 0 of 3, run ci-199"},
		{At: ats, ID: "836", Kind: "task", To: "filed"},
	}}}
	for i := 830; i < 842; i++ {
		f.queue.report.QueuedPRs = append(f.queue.report.QueuedPRs, QueuedPRReport{TaskID: fmt.Sprint(i), Number: i,
			Repo: "acme/widgets", Branch: "main", State: "waiting_ci", At: &ats, Note: strings.Repeat("note ", 60)})
	}
	return f
}

func (f stewardFixture) registry() *Registry {
	r := NewRegistry()
	r.Register(NewTaskTools(f.tasks)...)
	r.Register(NewPipelineTools(f.pipeline)...)
	r.Register(NewGrainCIWaitTools(f.ci, CIRepoScope{AllRepos: true})...)
	r.Register(NewMergeQueueTools(f.queue)...)
	return r
}

type replayedCall struct {
	tool string
	args map[string]any
}

// stewardWakeReads is what the steward's first wake had to find out --
// what changed, what is stuck, whose CI is red, what the merge queue is
// doing, and the record of each task it acted on or named -- asked the
// way DT-12's working rules ask it: start from the delta feed, read
// nothing a change does not name, and re-read each task in summary
// immediately before acting on it or naming it.
func stewardWakeReads() []replayedCall {
	calls := []replayedCall{
		{"read_pipeline_changes", map[string]any{"after": "2026-10-26T08:00:00Z~0"}},
		{"list_grain_tasks", map[string]any{"stalled": true}},
		{"list_grain_tasks", map[string]any{"changed_since": "1h"}},
		{"list_ci_runs", map[string]any{"conclusion": "failure", "limit": float64(10)}},
		{"query_merge_queue", map[string]any{"changed_since": "1h"}},
	}
	// Each task a change named, read once to decide and once more before
	// acting on it or citing it.
	for _, id := range []string{"831", "832", "833", "834", "836"} {
		calls = append(calls,
			replayedCall{"read_grain_task", map[string]any{"task_id": id}},
			replayedCall{"read_grain_task", map[string]any{"task_id": id}})
	}
	// One red run read in full, and one conversation paged.
	calls = append(calls,
		replayedCall{"list_ci_runs", map[string]any{"ref": "grain/task-198", "view": "full", "limit": float64(1)}},
		replayedCall{"read_grain_task", map[string]any{"task_id": "834", "view": "conversation", "after": float64(110)}},
		replayedCall{"read_pipeline_changes", map[string]any{"after": "2026-10-26T08:30:00Z~0"}},
	)
	return calls
}

// TestAStewardWakeFitsItsToolBudget is the DT-S8 measurement: a steward
// wake's reads replayed through the compact tools fit in the 40-call,
// 150 KB budget, with no budget notice on any result -- where the same
// questions asked of the full views (a whole record per task, a full run
// list, the full merge-queue report), the way wake 1 asked them, do not.
func TestAStewardWakeFitsItsToolBudget(t *testing.T) {
	fixture := newStewardFixture()
	registry := fixture.registry()
	registry.AnnounceToolBudget()
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	reads := stewardWakeReads()
	total := 0
	for _, c := range reads {
		res, err := client.CallTool(context.Background(), c.tool, c.args)
		if err != nil {
			t.Fatalf("%s(%v): %v", c.tool, c.args, err)
		}
		if res.IsError {
			t.Fatalf("%s(%v) failed: %s", c.tool, c.args, res.Text())
		}
		if strings.Contains(res.Text(), "Tool budget") {
			t.Errorf("%s(%v) carried a budget notice: the wake ran over", c.tool, c.args)
		}
		total += len(res.Text())
	}
	t.Logf("compact wake: %d calls, %s", len(reads), humanBytes(total))
	if len(reads) > ToolBudgetSoftCalls || total > ToolBudgetSoftBytes {
		t.Errorf("the wake took %d calls and %s, over the %d-call, %s budget",
			len(reads), humanBytes(total), ToolBudgetSoftCalls, humanBytes(ToolBudgetSoftBytes))
	}

	// The same questions the way wake 1 asked them: every task's full
	// record to find what moved, and every run in full.
	sweep := 0
	tools := fixture.registry()
	for _, s := range fixture.tasks.summaries {
		sweep += len(tools.tools["read_grain_task"].Handler(context.Background(), map[string]any{"task_id": s.ID, "view": "full"}).Text)
	}
	sweep += len(tools.tools["list_ci_runs"].Handler(context.Background(), map[string]any{"view": "full", "limit": float64(200)}).Text)
	sweep += len(tools.tools["query_merge_queue"].Handler(context.Background(), map[string]any{"view": "full"}).Text)
	t.Logf("sweeping wake: %d calls, %s", len(fixture.tasks.summaries)+2, humanBytes(sweep))
	if sweep <= ToolBudgetHardBytes {
		t.Errorf("the sweep came to %s, which would fit the budget: the fixture is too light to measure anything", humanBytes(sweep))
	}
}
