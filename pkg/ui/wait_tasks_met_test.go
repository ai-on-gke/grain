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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// #XC-22: until=awaiting-reply is met by a task that has parked -- the
// derived awaiting_reply with no attempt live -- and not by a question
// or secret request read beside a run still finishing or a state the
// park has not reached yet (the 2026-11-07 walkthrough's F6, which woke
// on "queued" with the access request as its question).
func TestTasksWaitAwaitingReplyNeedsTheParkSettled(t *testing.T) {
	waiting := func(st model.State, question, secret string, live bool) TaskWaitState {
		return TaskWaitState{WaitTarget: model.WaitTarget{ID: "7", State: st},
			Question: question, PendingSecret: secret, liveAttempt: live}
	}
	for _, tc := range []struct {
		name string
		task TaskWaitState
		want bool
	}{
		{"parked on a question", waiting(model.StateAwaitingReply, "which branch?", "", false), true},
		{"parked on a secret request", waiting(model.StateAwaitingReply, "set it", "api-key", false), true},
		{"awaiting an acknowledgement", waiting(model.StateAwaitingReply, "", "", false), true},
		{"question pending while the run is live", waiting(model.StateAwaitingReply, "which branch?", "", true), false},
		{"question read beside a state still queued", waiting(model.StateQueued, "access request", "", false), false},
		{"question read beside a state still running", waiting(model.StateRunning, "which branch?", "", true), false},
		{"secret read beside a state still queued", waiting(model.StateQueued, "set it", "api-key", false), false},
		{"nothing pending", waiting(model.StateQueued, "", "", false), false},
	} {
		if met, failed := tasksWaitMet(WaitUntilAwaitingReply, []TaskWaitState{tc.task}, "", ""); met != tc.want || failed {
			t.Errorf("%s: met=%t failed=%t, want met=%t failed=false", tc.name, met, failed, tc.want)
		}
	}
}
