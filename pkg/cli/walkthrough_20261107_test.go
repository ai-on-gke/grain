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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// Regressions from the §8 walkthrough
// (docs/test-runs/2026-11-07-cli-contexts-gitlogin-liveness.md).

// TestShowSaysTheReasonSubCase (F3): `grain show` names the sub-case a
// task is held in -- which ask an awaiting_reply task is parked on
// (#782), the redacted dispatch-down condition a queued one waits behind
// (#712) -- where before only -json carried it.
func TestShowSaysTheReasonSubCase(t *testing.T) {
	for _, tc := range []struct {
		reason *ui.Reason
		want   string
	}{
		{&ui.Reason{Reason: "awaiting_reply/access_request", Class: "awaiting a person", Control: "Grant"},
			"reason:     awaiting_reply/access_request [Grant]\n"},
		{&ui.Reason{Reason: "queued/dispatch_blocked", Class: "awaiting an external answer",
			Resource: "dispatch blocked -- git proxy: listen tcp [redacted: secret bind-host/value]: bind: address already in use"},
			"reason:     queued/dispatch_blocked (dispatch blocked -- git proxy: listen tcp [redacted: secret bind-host/value]: bind: address already in use)\n"},
	} {
		got := taskBlock(ui.Task{ID: "7", Title: "t", State: model.StateQueued, Reason: tc.reason})
		if !strings.Contains(got, tc.want) {
			t.Errorf("taskBlock with %+v =\n%s\nwant a line %q", tc.reason, got, tc.want)
		}
	}
	if got := taskBlock(ui.Task{ID: "7", Title: "t", State: model.StateQueued}); strings.Contains(got, "reason:") {
		t.Errorf("a task with no classifier wired printed a reason line:\n%s", got)
	}
}

// TestWaitSaysAParkedQuestionOnce (F4): a task parked on a question has
// that question as its closing note too; `grain wait` printed it twice.
func TestWaitSaysAParkedQuestionOnce(t *testing.T) {
	const q = "Should the retry budget be 3 or 5?"
	out := captureStdout(t, func() {
		printWait(&printer{}, ui.TasksWaitResponse{Met: true, Tasks: []ui.TaskWaitState{{
			WaitTarget: model.WaitTarget{ID: "2", State: model.StateAwaitingReply, ClosingNote: q},
			Question:   q,
		}}})
	})
	if n := strings.Count(out, q); n != 1 {
		t.Errorf("grain wait printed the question %d times:\n%s", n, out)
	}
	out = captureStdout(t, func() {
		printWait(&printer{}, ui.TasksWaitResponse{Met: true, Tasks: []ui.TaskWaitState{{
			WaitTarget: model.WaitTarget{ID: "2", State: model.StateCompleted, Closed: true, ClosingNote: "done: PR opened"},
		}}})
	})
	if !strings.Contains(out, "done: PR opened") {
		t.Errorf("a closed task's closing note went missing:\n%s", out)
	}
}
