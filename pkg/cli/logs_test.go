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
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// #XC-23: grain logs prints an attempt's transcript (-tail trims it),
// and -follow streams the lines written after it starts, once each,
// until the attempt finishes -- exit 1 for an attempt that errored,
// 0 for one that succeeded.
func TestGrainLogsStreamsAndExitsOnAttemptCompletion(t *testing.T) {
	client, c := externalCLIServer(t)
	ctx := context.Background()
	old := logsPollInterval
	logsPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { logsPollInterval = old })
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "running", NoRepo: true, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	start := func(run string, attempt int) {
		t.Helper()
		if err := client.Store.StartRun(ctx, model.Run{ID: run, TaskID: task.ID, Sandbox: "s-" + run,
			Attempt: attempt, StartedAt: time.Now()}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	start("r1", 1)
	if err := client.Store.SetRunTranscript(ctx, "r1", "one\ntwo"); err != nil {
		t.Fatal(err)
	}

	text := captureStdout(t, func() {
		if err := cmdLogs(ctx, c, &printer{}, []string{task.ID, "-tail", "1"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(text) != "two" {
		t.Errorf("-tail 1 printed %q, want the last line", text)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = client.Store.SetRunTranscript(ctx, "r1", "one\ntwo\nthree")
		time.Sleep(200 * time.Millisecond)
		_ = client.Store.FinishRun(ctx, "r1", time.Now(), "errored", "the agent crashed")
	}()
	var ferr error
	text = captureStdout(t, func() {
		ferr = cmdLogs(ctx, c, &printer{}, []string{task.ID, "-follow"})
	})
	if got := strings.Join(strings.Fields(text), ","); got != "one,two,three" {
		t.Errorf("-follow printed %q, want every line once", text)
	}
	if exit, _, _ := classifyError(ferr); exit != exitFailed {
		t.Errorf("-follow over an errored attempt: %v (exit %d), want exit 1", ferr, exit)
	}

	start("r2", 2)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = client.Store.SetRunTranscript(ctx, "r2", "done")
		_ = client.Store.FinishRun(ctx, "r2", time.Now(), "succeeded", "")
	}()
	text = captureStdout(t, func() {
		ferr = cmdLogs(ctx, c, &printer{}, []string{task.ID, "-follow"})
	})
	if ferr != nil || strings.TrimSpace(text) != "done" {
		t.Errorf("-follow over a succeeding attempt: %v, printed %q", ferr, text)
	}
	text = captureStdout(t, func() {
		if err := cmdLogs(ctx, c, &printer{}, []string{task.ID, "-attempt", "1"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Join(strings.Fields(text), ",") != "one,two,three" {
		t.Errorf("-attempt 1 printed %q", text)
	}
	if err := cmdLogs(ctx, c, &printer{}, []string{task.ID, "-attempt", "9"}); err == nil {
		t.Error("-attempt 9 of a task with two was answered")
	}
}
