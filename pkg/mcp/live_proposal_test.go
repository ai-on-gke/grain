package mcp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingProposalFiler struct {
	ids    map[string]string
	calls  int
	err    error
	queued bool
	tasks  []ProposedTask
}

func (f *recordingProposalFiler) FileProposal(ctx context.Context, task ProposedTask, key string) (FiledProposal, error) {
	f.tasks = append(f.tasks, task)
	if err := ctx.Err(); err != nil {
		return FiledProposal{}, err
	}
	if f.err != nil {
		return FiledProposal{}, f.err
	}
	if id := f.ids[key]; id != "" {
		return FiledProposal{ID: id, URL: "/tasks/" + id, Queued: f.queued}, nil
	}
	f.calls++
	id := string(rune('0' + f.calls))
	f.ids[key] = id
	return FiledProposal{ID: id, URL: "/tasks/" + id, Queued: f.queued}, nil
}

func TestLiveProposalsReturnNumbersImmediatelyAndRetryIdempotently(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	first := map[string]any{"id": "first", "title": "one", "body": "body"}
	got := tool.Handler(context.Background(), first)
	if got.IsError || !strings.Contains(got.Text, "#1") || !strings.Contains(got.Text, "/tasks/1") {
		t.Fatalf("first result = %+v", got)
	}
	retry := tool.Handler(context.Background(), first)
	if retry.IsError || !strings.Contains(retry.Text, "#1") || f.calls != 1 {
		t.Fatalf("retry = %+v, calls = %d", retry, f.calls)
	}
	second := tool.Handler(context.Background(), map[string]any{
		"id": "second", "title": "two", "body": "body", "depends_on": []any{"first"},
	})
	if second.IsError || !strings.Contains(second.Text, "#2") {
		t.Fatalf("second result = %+v", second)
	}
}

// A live proposal tells the run which of the two things just happened
// to it: it is waiting on a human, or it is already queued because the
// proposing task's own permission covered it (model.AutoSubmits). Saying
// "unapproved" about a task that is in fact dispatchable is the one
// answer that leaves a run reasoning about the wrong queue -- writing a
// body for somebody who will never read it, and skipping the depends_on
// that is now the only thing holding the work back.
func TestLiveProposalSaysWhetherItIsQueuedOrWaitingOnAHuman(t *testing.T) {
	waiting := &recordingProposalFiler{ids: map[string]string{}}
	got := NewLiveProposalTools(&MockSink{}, nil, "623", waiting)[0].
		Handler(context.Background(), map[string]any{"title": "one", "body": "body"})
	if got.IsError || !strings.Contains(got.Text, "unapproved") {
		t.Fatalf("proposal waiting on a human = %+v", got)
	}

	queued := &recordingProposalFiler{ids: map[string]string{}, queued: true}
	got = NewLiveProposalTools(&MockSink{}, nil, "623", queued)[0].
		Handler(context.Background(), map[string]any{"title": "one", "body": "body"})
	if got.IsError || strings.Contains(got.Text, "unapproved") ||
		!strings.Contains(got.Text, "queued") {
		t.Fatalf("proposal filed queued = %+v", got)
	}
}

func TestLiveProposalFailureAndCancellationReturnErrorsWithoutSuccess(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}, err: errors.New("write failed")}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	args := map[string]any{"title": "one", "body": "body"}
	if got := tool.Handler(context.Background(), args); !got.IsError {
		t.Fatalf("failure = %+v", got)
	}
	f.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := tool.Handler(ctx, args); !got.IsError || f.calls != 0 {
		t.Fatalf("cancel = %+v calls=%d", got, f.calls)
	}
}

func TestLiveProposalForwardsReadsWritesAndReviewTemplate(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	args := map[string]any{
		"title":              "cross repo task",
		"body":               "do work",
		"branch":             "feat/live",
		"reads":              []any{"owner/read1", "owner/read2"},
		"writes":             []any{map[string]any{"repo": "owner/write1", "base": "main"}},
		"review_template_id": "review-tmpl-1",
	}
	got := tool.Handler(context.Background(), args)
	if got.IsError {
		t.Fatalf("unexpected error: %+v", got)
	}
	if len(f.tasks) != 1 {
		t.Fatalf("recorded %d tasks, want 1", len(f.tasks))
	}
	task := f.tasks[0]
	if !reflect.DeepEqual(task.Reads, []string{"owner/read1", "owner/read2"}) {
		t.Errorf("task.Reads = %v, want [owner/read1 owner/read2]", task.Reads)
	}
	wantWrites := []WriteTarget{{Repo: "owner/write1", Base: "main"}}
	if !reflect.DeepEqual(task.Writes, wantWrites) {
		t.Errorf("task.Writes = %v, want %v", task.Writes, wantWrites)
	}
	if task.ReviewTemplateID != "review-tmpl-1" {
		t.Errorf("task.ReviewTemplateID = %q, want review-tmpl-1", task.ReviewTemplateID)
	}
	if task.Branch != "feat/live" {
		t.Errorf("task.Branch = %q, want feat/live", task.Branch)
	}
}

func TestLiveProposalPassesModelAndAgentFramework(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	got := tool.Handler(context.Background(), map[string]any{
		"title":           "specialized task",
		"body":            "needs high reasoning model",
		"model":           "claude-sonnet-5",
		"agent_framework": "claude",
	})
	if got.IsError {
		t.Fatalf("unexpected error: %v", got)
	}
	if len(f.tasks) != 1 {
		t.Fatalf("recorded %d tasks, want 1", len(f.tasks))
	}
	if f.tasks[0].Model != "claude-sonnet-5" || f.tasks[0].AgentFramework != "claude" {
		t.Fatalf("task model/framework = %q/%q, want claude-sonnet-5/claude",
			f.tasks[0].Model, f.tasks[0].AgentFramework)
	}
}

func TestLiveProposalCarriesNotBefore(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	got := tool.Handler(context.Background(), map[string]any{
		"title": "read the first week", "body": "after it has run", "not_before": "2099-10-14",
	})
	if got.IsError {
		t.Fatalf("unexpected error: %v", got)
	}
	want := time.Date(2099, 10, 14, 0, 0, 0, 0, time.UTC)
	if len(f.tasks) != 1 || f.tasks[0].NotBefore == nil || !f.tasks[0].NotBefore.Equal(want) {
		t.Fatalf("recorded %+v, want one task not before %v", f.tasks, want)
	}
	if !strings.Contains(got.Text, "2099-10-14T00:00:00Z") {
		t.Errorf("result %q does not say when the task may start", got.Text)
	}
}

func TestLiveProposalRefusesAnUnreadableNotBefore(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	got := tool.Handler(context.Background(), map[string]any{
		"title": "t", "body": "b", "not_before": "next tuesday",
	})
	if !got.IsError || len(f.tasks) != 0 {
		t.Fatalf("got %+v with %d filed, want a refusal and nothing filed", got, len(f.tasks))
	}
}

func TestLiveProposalIgnoresAPastNotBefore(t *testing.T) {
	f := &recordingProposalFiler{ids: map[string]string{}}
	tool := NewLiveProposalTools(&MockSink{}, nil, "623", f)[0]
	got := tool.Handler(context.Background(), map[string]any{
		"title": "t", "body": "b", "not_before": "2000-01-01T00:00:00Z",
	})
	if got.IsError || len(f.tasks) != 1 || f.tasks[0].NotBefore != nil {
		t.Fatalf("got %+v, %+v; want filed with no wake time", got, f.tasks)
	}
}

type staticURLProposalFiler struct {
	id  string
	url string
}

func (f staticURLProposalFiler) FileProposal(context.Context, ProposedTask, string) (FiledProposal, error) {
	return FiledProposal{ID: f.id, URL: f.url}, nil
}

func TestLiveProposalEmitsRelativeTaskLink(t *testing.T) {
	for _, rawURL := range []string{
		"/tasks/36",
		"http://127.0.0.1:4242/tasks/36",
		"http://localhost:8080/tasks/36",
		"http://[::1]:4242/tasks/36",
		"",
	} {
		tool := NewLiveProposalTools(&MockSink{}, nil, "623", staticURLProposalFiler{id: "36", url: rawURL})[0]
		got := tool.Handler(context.Background(), map[string]any{"title": "one", "body": "body"})
		if got.IsError {
			t.Fatalf("url=%q: unexpected error: %+v", rawURL, got)
		}
		want := "Proposed task #36 is visible now: /tasks/36."
		if !strings.Contains(got.Text, want) {
			t.Errorf("url=%q: result = %q, want it to contain %q", rawURL, got.Text, want)
		}
		if strings.Contains(got.Text, "127.0.0.1") || strings.Contains(got.Text, "localhost") {
			t.Errorf("url=%q: result still contains loopback origin: %q", rawURL, got.Text)
		}
	}
}
