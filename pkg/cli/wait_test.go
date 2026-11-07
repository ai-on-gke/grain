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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// #XC-22: grain wait exits 0 on a met condition, 1 when until=merged
// meets a task that closed unmerged, wakes when a task parks on a
// question, and exits 5 with the command that carries on when its
// -timeout runs out first.
func TestTasksWaitEndpointWakesOnConditionsEarnsNoWaitCreditAndExits5OnTimeout(t *testing.T) {
	client, c := externalCLIServer(t)
	ctx := context.Background()
	file := func(title string) string {
		t.Helper()
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: title, NoRepo: true, Approved: true})
		if err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	open, closed := file("open"), file("closed")
	now := time.Now()
	if err := client.Store.Observe(ctx, model.Observation{TaskID: closed, ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}

	var err error
	text := captureStdout(t, func() {
		err = cmdWait(ctx, c, &printer{json: true}, []string{closed, "--until=closed"})
	})
	if err != nil {
		t.Fatalf("waiting on a closed task until closed: %v", err)
	}
	var resp ui.TasksWaitResponse
	if jerr := json.Unmarshal([]byte(text), &resp); jerr != nil || !resp.Met || resp.Tasks[0].ID != closed {
		t.Errorf("-json printed %q (%v)", text, jerr)
	}

	captureStdout(t, func() { err = cmdWait(ctx, c, &printer{}, []string{closed, "--until=merged"}) })
	if exit, _, _ := classifyError(err); exit != exitFailed {
		t.Errorf("until=merged on a task closed unmerged: %v (exit %d), want exit 1", err, exit)
	}
	captureStdout(t, func() { err = cmdWait(ctx, c, &printer{}, []string{closed, "--until=merged,closed"}) })
	if err != nil {
		t.Errorf("until=merged,closed on a closed task: %v, want exit 0", err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		parkOnQuestion(t, client, open, "")
	}()
	start := time.Now()
	text = captureStdout(t, func() {
		err = cmdWait(withWaitLimit(ctx, 20*time.Second), c, &printer{}, []string{open, "--until=awaiting-reply"})
	})
	if err != nil || time.Since(start) > 15*time.Second || !strings.Contains(text, "which branch?") {
		t.Errorf("until=awaiting-reply: %v after %s, printed %q", err, time.Since(start), text)
	}

	captureStdout(t, func() {
		err = cmdWait(withWaitLimit(ctx, 1500*time.Millisecond), c, &printer{}, []string{open, "--until=closed"})
	})
	exit, code, fix := classifyError(err)
	if exit != exitWaitTimedOut || code != "wait_timeout" || !strings.Contains(fix, "grain wait "+open+" --until=closed --timeout=") {
		t.Errorf("a wait that ran out: %v (exit %d, %s, fix %q), want exit 5 with a resumable command", err, exit, code, fix)
	}

	if err := cmdWait(ctx, c, &printer{}, []string{open, "--until=forever"}); err == nil {
		t.Error("an unknown -until was accepted")
	}
	credits, cerr := client.Store.RunWaitCredits(ctx)
	if cerr != nil || len(credits) != 0 {
		t.Errorf("grain wait accrued wait credit: %+v, %v", credits, cerr)
	}
}
