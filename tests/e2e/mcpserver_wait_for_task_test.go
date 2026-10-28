package e2e

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// Task A's run parks on task B through the real `grain mcpserver` and the
// daemon's own API; A sits out the queue while B is open; B merges and
// closes; A is dispatchable again, its next prompt says it resumed and
// how B ended, and its second run completes it.
func TestWaitForTaskParksThenResumesWhenTheTargetMerges(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	// No target repository on either: what is under test is the wait,
	// and a task with no repo has no branch for the finish step to look
	// for on GitHub.
	actor := human("operator")
	for _, id := range []string{"a1", "b1"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentAnalyze, Title: "task " + id, Body: "please",
			Origin:   model.Origin{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval: &model.Attribution{Actor: actor},
			Binding:  model.BindingDirective,
		}); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
	}
	start := time.Now().UTC()
	if err := store.StartRun(ctx, model.Run{ID: "a1-1", TaskID: "a1", Sandbox: "a1-1", Attempt: 1, StartedAt: start}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities(),
	}), store))
	p := startMCPServer(t, bin, "-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", "a1")
	if names := toolNames(t, p); !names["wait_for_task"] {
		t.Fatalf("tools/list is missing wait_for_task; got %v", names)
	}

	// The refusals arrive over the wire as tool errors.
	for _, id := range []string{"a1", "nope"} {
		res, err := p.CallTool(ctx, "wait_for_task", map[string]any{"task_id": id})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("waiting on %q was accepted: %s", id, res.Text())
		}
	}

	args := map[string]any{"task_id": "b1", "park": true}
	res, err := p.CallTool(ctx, "wait_for_task", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.HasPrefix(res.Text(), "Parked.") {
		t.Fatalf("park answered %q, want a park\nstderr:\n%s", res.Text(), p.stderr.String())
	}

	// The run ends; the finish step reads the call back out of its result.
	if err := store.FinishRun(ctx, "a1-1", start, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetTask(ctx, "a1")
	if err != nil || a == nil {
		t.Fatalf("reading a1: %v", err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "wait_for_task", Arguments: args, Text: res.Text()}}}
	if err := orchestrator.ProcessResult(ctx, store, nil, *a, result, "a1-1", start); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st, _ := store.State(ctx, "a1"); st != model.StateQueued {
		t.Fatalf("a1 state = %q, want queued", st)
	}
	if ready, _ := store.Ready(ctx); slices.Contains(ready, "a1") {
		t.Fatal("a1 was ready while b1 was still open")
	}

	// B's change merges, and B closes.
	done := start.Add(time.Minute)
	if err := store.ObserveField(ctx, "b1", done, func(o *model.Observation) {
		o.CompletedAt, o.PrMergedAt, o.ClosedAt = &done, &done, &done
	}); err != nil {
		t.Fatal(err)
	}
	if ready, _ := store.Ready(ctx); !slices.Contains(ready, "a1") {
		t.Fatalf("ready = %v, want a1 once b1 closed", ready)
	}

	runs, err := store.Runs(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.ParkedOutcome {
		t.Fatalf("runs = %+v, want one parked", runs)
	}
	prompt := orchestrator.BuildPrompt(*a, "", "", false, 0, orchestrator.History{
		Attempt: 2, Attempts: runs, Resumed: orchestrator.ResumedFrom(ctx, store, runs),
	}, nil)
	for _, want := range []string{"resumes from a park", "Task b1", "outcome merged"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resumed prompt lacks %q:\n%s", want, prompt)
		}
	}

	// The resumed run finishes the task.
	if err := store.StartRun(ctx, model.Run{ID: "a1-2", TaskID: "a1", Sandbox: "a1-2", Attempt: 2, StartedAt: done}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "a1-2", done, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	closing := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "comment_on_issue",
		Arguments: map[string]any{"comment": "b1 merged; promoted the release."}}}}
	if err := orchestrator.ProcessResult(ctx, store, nil, *a, closing, "a1-2", done); err != nil {
		t.Fatalf("ProcessResult (resumed): %v", err)
	}
	if st, _ := store.State(ctx, "a1"); st != model.StateAwaitingReply && st != model.StateCompleted {
		t.Fatalf("a1 state after its resumed run = %q, want it finished", st)
	}
}
