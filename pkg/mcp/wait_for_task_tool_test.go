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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeTaskWaiter answers WaitOn from a script: the target stays open
// until closeAfter polls, and parks are recorded.
type fakeTaskWaiter struct {
	mu         sync.Mutex
	polls      int
	closeAfter int // <0 never
	refuse     error
	parked     [][]string
	parkedTill []time.Time
	resumed    []model.ResumeOn
}

func (f *fakeTaskWaiter) ParkUntil(_ context.Context, until time.Time) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return time.Time{}, f.refuse
	}
	f.parkedTill = append(f.parkedTill, until)
	return until, nil
}

// ResumeLater does what ui.Client.ResumeLater does with the two forms
// wait_for_task uses, and records every call.
func (f *fakeTaskWaiter) ResumeLater(ctx context.Context, when model.ResumeOn) (model.ResumeOn, error) {
	f.mu.Lock()
	f.resumed = append(f.resumed, when)
	f.mu.Unlock()
	switch {
	case len(when.Tasks) > 0:
		_, err := f.WaitOn(ctx, when.Tasks, true)
		return when, err
	case when.At != nil && when.CI == "" && !when.Message:
		until, err := f.ParkUntil(ctx, *when.At)
		when.At = &until
		return when, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return when, f.refuse
}

func (f *fakeTaskWaiter) WaitOn(_ context.Context, ids []string, park bool) ([]model.WaitTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return nil, f.refuse
	}
	if park {
		f.parked = append(f.parked, ids)
	}
	f.polls++
	closed := f.closeAfter >= 0 && f.polls > f.closeAfter
	var out []model.WaitTarget
	for _, id := range ids {
		t := model.WaitTarget{ID: id, Title: "publish the image", State: model.StateRunning}
		if closed {
			t.State, t.Closed, t.Merged, t.Outcome = model.StateClosed, true, true, model.WaitOutcomeMerged
			t.ClosingNote = "published"
		}
		out = append(out, t)
	}
	return out, nil
}

func callWaitForTask(t *testing.T, ctx context.Context, w TaskWaiter, unpushed UnpushedWorkReader, args map[string]any) Result {
	t.Helper()
	return waitForTaskTool(w, unpushed, 5*time.Millisecond).Handler(ctx, args)
}

func TestWaitForTaskReturnsWhenTheTargetCloses(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{closeAfter: 3}
	res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"task_id": "#4944"})
	if res.IsError || !strings.HasPrefix(res.Text, "Done waiting") ||
		!strings.Contains(res.Text, "outcome merged") || !strings.Contains(res.Text, "> published") {
		t.Fatalf("result = %+v, want a finished wait naming the outcome and closing note", res)
	}
	if len(w.parked) != 0 {
		t.Fatalf("a blocking wait parked: %v", w.parked)
	}
}

func TestWaitForTaskAlreadySettledReturnsAtOnce(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{closeAfter: 0}
	res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"task_id": "4944", "park": true})
	if res.IsError || !strings.HasPrefix(res.Text, "Already settled") || len(w.parked) != 0 {
		t.Fatalf("result = %+v, parked %v; want an immediate answer and no park", res, w.parked)
	}
	if WaitForTaskParked(map[string]any{"park": true}, res.Text) {
		t.Fatal("WaitForTaskParked read an already-settled answer as a park")
	}
}

func TestWaitForTaskTimesOut(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{closeAfter: -1}
	start := time.Now()
	res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"task_id": "4944", "timeout_seconds": 1.0})
	if res.IsError || !strings.HasPrefix(res.Text, "Timed out") || !strings.Contains(res.Text, "[still waiting]") {
		t.Fatalf("result = %+v, want a timeout still waiting", res)
	}
	if took := time.Since(start); took < time.Second || took > 10*time.Second {
		t.Fatalf("wait took %s, want about the 1s timeout", took)
	}
}

func TestWaitForTaskUntilMergedReportsAClosureThatDidNotMerge(t *testing.T) {
	t.Parallel()
	target := model.WaitTarget{ID: "7", State: model.StateClosed, Closed: true, Outcome: model.WaitOutcomeCancelled}
	if settled, met := target.Settled(true); !settled || met {
		t.Fatalf("Settled(merged) = %v, %v, want settled and unmet", settled, met)
	}
	if settled, met := target.Settled(false); !settled || !met {
		t.Fatalf("Settled(closed) = %v, %v, want met", settled, met)
	}
	if !strings.Contains(describeTargets([]model.WaitTarget{target}, true), "will not reach it") {
		t.Fatal("an unmerged closure was not called out")
	}
}

func TestWaitForTaskPassesRefusalsBack(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{refuse: errors.New("a task cannot depend on itself")}
	res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"task_id": "4945"})
	if !res.IsError || !strings.Contains(res.Text, "cannot depend on itself") {
		t.Fatalf("result = %+v, want the refusal", res)
	}
	if res := callWaitForTask(t, context.Background(), w, nil, map[string]any{}); !res.IsError {
		t.Fatal("a call naming no task was accepted")
	}
	if res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"task_id": "1", "until": "soon"}); !res.IsError {
		t.Fatal("an unknown until was accepted")
	}
	if res := callWaitForTask(t, context.Background(), nil, nil, map[string]any{"task_id": "1"}); !res.IsError {
		t.Fatal("a server with no daemon answered as if it could wait")
	}
}

func TestWaitForTaskParkRefusesUnpushedWorkUnlessForced(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{closeAfter: -1}
	unpushed := func(context.Context) (string, error) { return "unpushed commit: abc123 wip", nil }
	args := map[string]any{"task_id": "4944", "park": true}
	res := callWaitForTask(t, context.Background(), w, unpushed, args)
	if !res.IsError || !strings.Contains(res.Text, "abc123") || len(w.parked) != 0 {
		t.Fatalf("result = %+v, parked %v; want a refusal naming the commit", res, w.parked)
	}

	args["force"] = true
	res = callWaitForTask(t, context.Background(), w, unpushed, args)
	if res.IsError || !strings.HasPrefix(res.Text, parkedMarker) || len(w.parked) != 1 || w.parked[0][0] != "4944" {
		t.Fatalf("result = %+v, parked %v; want a forced park", res, w.parked)
	}
	if !WaitForTaskParked(args, res.Text) || !WaitForTaskParked(map[string]any{}, res.Text) {
		t.Fatal("WaitForTaskParked did not recognise the park")
	}
}

// A wait longer than the run has room for parks on its own.
func TestWaitForTaskParksAWaitTheRunCannotAfford(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.AnnounceDeadline(time.Now().Add(10*time.Minute), nil)
	ctx := r.withRunDeadline(context.Background())
	w := &fakeTaskWaiter{closeAfter: -1}
	clean := func(context.Context) (string, error) { return "", nil }
	res := callWaitForTask(t, ctx, w, clean, map[string]any{"task_id": "4944", "timeout_seconds": 3600.0})
	if res.IsError || !strings.HasPrefix(res.Text, parkedMarker) || !strings.Contains(res.Text, "parked rather than blocking") {
		t.Fatalf("result = %+v, want an automatic park", res)
	}
	if len(w.parked) != 1 {
		t.Fatalf("parked %v, want one park", w.parked)
	}
}

func TestWaitForTaskIDsNormalizes(t *testing.T) {
	t.Parallel()
	got := WaitForTaskIDs(map[string]any{"task_id": "#12", "task_ids": []any{"12", " 13 ", ""}})
	if strings.Join(got, ",") != "12,13" {
		t.Fatalf("ids = %v, want [12 13]", got)
	}
}

func TestUnpushedFromOutput(t *testing.T) {
	t.Parallel()
	out := "exit=0\nstdout:\nGRAIN-UNPUSHED-BEGIN\nunpushed commit: abc wip\nGRAIN-UNPUSHED-END\n\nstderr:\n"
	if got := unpushedFromOutput(out); got != "unpushed commit: abc wip" {
		t.Fatalf("unpushed = %q", got)
	}
	if got := unpushedFromOutput("exit=0\nstdout:\nGRAIN-UNPUSHED-BEGIN\nGRAIN-UNPUSHED-END\n"); got != "" {
		t.Fatalf("clean checkout read as %q", got)
	}
	if got := unpushedFromOutput("exit=0\nstdout:\n\nstderr:\n"); got != "" {
		t.Fatalf("no checkout read as %q", got)
	}
}

// resume_at parks the task until a time with no task to wait on, and its
// result is one WaitForTaskParkedUntil reads the wake time back out of --
// which is how orchestrator.processResult tells the run parked.
func TestWaitForTaskResumeAtParksUntilATime(t *testing.T) {
	t.Parallel()
	w := &fakeTaskWaiter{}
	before := time.Now()
	args := map[string]any{"resume_at": "1h"}
	res := callWaitForTask(t, context.Background(), w, func(context.Context) (string, error) { return "", nil }, args)
	if res.IsError || len(w.parkedTill) != 1 {
		t.Fatalf("result = %+v, parks = %v, want one timed park", res, w.parkedTill)
	}
	if d := w.parkedTill[0].Sub(before); d < time.Hour || d > time.Hour+time.Minute {
		t.Fatalf("parked until %v, want an hour from %v", w.parkedTill[0], before)
	}
	if !WaitForTaskParked(args, res.Text) {
		t.Fatalf("WaitForTaskParked does not read %q as a park", res.Text)
	}
	until, ok := WaitForTaskParkedUntil(args, res.Text)
	if !ok || !until.Equal(w.parkedTill[0].Truncate(time.Second)) {
		t.Fatalf("WaitForTaskParkedUntil = %v, %v, want %v", until, ok, w.parkedTill[0])
	}
	if _, ok := WaitForTaskParkedUntil(map[string]any{"task_id": "1", "park": true}, "Parked. This task now depends on #1"); ok {
		t.Fatal("a park on a task was read as a park until a time")
	}

	stamp := "2099-01-02T03:04:05Z"
	w = &fakeTaskWaiter{}
	if res := callWaitForTask(t, context.Background(), w, nil, map[string]any{"resume_at": stamp}); res.IsError ||
		len(w.parkedTill) != 1 || w.parkedTill[0].Format(time.RFC3339) != stamp {
		t.Fatalf("result = %+v, parks = %v, want a park until %s", res, w.parkedTill, stamp)
	}
}

// A timed park is refused for what a park on a task is refused for --
// unpushed work, a refusal from the daemon -- and for what it cannot mean:
// a task to wait on as well, or a time it cannot read.
func TestWaitForTaskResumeAtRefusals(t *testing.T) {
	t.Parallel()
	lost := func(context.Context) (string, error) { return "unpushed commit: abc fix", nil }
	for name, tc := range map[string]struct {
		w        *fakeTaskWaiter
		unpushed UnpushedWorkReader
		args     map[string]any
		want     string
	}{
		"unpushed":    {&fakeTaskWaiter{}, lost, map[string]any{"resume_at": "1h"}, "abc fix"},
		"refused":     {&fakeTaskWaiter{refuse: errors.New("too far off")}, nil, map[string]any{"resume_at": "1h"}, "too far off"},
		"with a task": {&fakeTaskWaiter{}, nil, map[string]any{"resume_at": "1h", "task_id": "4"}, "not both"},
		"unreadable":  {&fakeTaskWaiter{}, nil, map[string]any{"resume_at": "tomorrow-ish"}, "want a duration"},
		"negative":    {&fakeTaskWaiter{}, nil, map[string]any{"resume_at": "-1h"}, "positive"},
	} {
		res := callWaitForTask(t, context.Background(), tc.w, tc.unpushed, tc.args)
		if !res.IsError || !strings.Contains(res.Text, tc.want) {
			t.Errorf("%s: result = %+v, want an error saying %q", name, res, tc.want)
		}
		if len(tc.w.parkedTill) != 0 {
			t.Errorf("%s: parked until %v regardless", name, tc.w.parkedTill)
		}
	}
	// force parks over unpushed work, as it does for a park on a task.
	w := &fakeTaskWaiter{}
	if res := callWaitForTask(t, context.Background(), w, lost, map[string]any{"resume_at": "1h", "force": true}); res.IsError || len(w.parkedTill) != 1 {
		t.Fatalf("result = %+v, want force to park anyway", res)
	}
}
