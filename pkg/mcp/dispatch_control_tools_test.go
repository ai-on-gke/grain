package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeDispatch struct {
	ran, paused string
	passes      bool
	pause       string
}

func (f *fakeDispatch) PausePipeline(_ context.Context, reason string) (bool, error) {
	f.paused = reason
	return true, nil
}
func (f *fakeDispatch) ResumePipeline(context.Context) (bool, error) { return true, nil }
func (f *fakeDispatch) RunTaskNow(_ context.Context, id string) (string, bool, error) {
	f.ran = id
	return f.pause, f.passes, nil
}
func (f *fakeDispatch) DeferTask(context.Context, string, *time.Time) error { return nil }

func dispatchTool(t *testing.T, c DispatchController, name string) Tool {
	t.Helper()
	for _, tool := range NewDispatchControlTools(c) {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no %s tool", name)
	return Tool{}
}

// run_task_now names the task it asked for, and tells the agent the one
// case where the request waits: an upgrade's hold.
func TestRunTaskNowSaysWhetherThePausePassesIt(t *testing.T) {
	fake := &fakeDispatch{pause: "manual mode", passes: true}
	res := dispatchTool(t, fake, "run_task_now").Handler(context.Background(), map[string]any{"task_id": "#12"})
	if res.IsError || fake.ran != "12" || !strings.Contains(res.Text, "passes that pause") {
		t.Fatalf("run_task_now under an operator's pause: ran %q, %+v", fake.ran, res)
	}
	fake.pause, fake.passes = "upgrading to main", false
	res = dispatchTool(t, fake, "run_task_now").Handler(context.Background(), map[string]any{"task_id": "12"})
	if res.IsError || !strings.Contains(res.Text, "upgrade holds the queue") {
		t.Fatalf("run_task_now under an upgrade's hold: %+v", res)
	}

	res = dispatchTool(t, fake, "pause_pipeline").Handler(context.Background(),
		map[string]any{"reason": "hand-picking", "repo": "acme/widgets"})
	if res.IsError || fake.paused != "[acme/widgets] hand-picking" {
		t.Fatalf("pause_pipeline: paused with %q, %+v", fake.paused, res)
	}

	if res := dispatchTool(t, nil, "run_task_now").Handler(context.Background(), map[string]any{"task_id": "12"}); !res.IsError {
		t.Fatalf("run_task_now with no daemon: %+v, want a refusal", res)
	}
}
