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

package daemon

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/selfrepair"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// daemonHostExecConfirm is selfrepair.Confirm over the daemon's REST API, and
// what run_host_command asks with in a forked "grain mcpserver", which
// holds no store (selfrepair.ConfirmedHostCommandTools):
// it posts the question into the task's own conversation (POST
// /api/tasks/{id}/messages, which also parks the task on it, as Confirm
// marks PendingQuestionCommentID), then reads that conversation back
// (GET /api/tasks/{id}/messages?after=N) until a person answers or
// timeout passes -- daemonChat's two calls, asking a narrower question.
//
// Unlike daemonChat it reads only what was said *after* its own
// question. A chat turn is happy to be handed something a person said
// while the run was busy; a confirmation is not, because a stray
// "approve" typed in answer to something else must never run a command
// nobody has seen yet.
//
// Refusing is the answer to everything it cannot settle -- a timeout, a
// cancelled run, a reply selfrepair.Decide does not read as approval --
// exactly as Confirm refuses. A failed read is retried on the next poll
// rather than taken as a refusal, for daemonChat's reason: a daemon
// restart is a blip, not a "no".
type daemonHostExecConfirm struct {
	client  *ui.HTTPClient
	taskID  string
	timeout time.Duration
	// poll is how long to wait after a read that failed before the next
	// one; chatPollInterval when 0.
	poll time.Duration
	// delivered is told about the reply, so the addenda poll does not
	// hand the same words to the run again on its next tool result.
	delivered *deliveredCursor
}

func (d daemonHostExecConfirm) Confirm(ctx context.Context, question string) (bool, string, error) {
	posted, err := d.client.PostTaskMessage(ctx, d.taskID, question, selfrepair.ConfirmSummary, nil)
	if err != nil {
		return false, "", err
	}
	poll := d.poll
	if poll <= 0 {
		poll = chatPollInterval
	}
	deadline := time.Now().Add(d.timeout)
	for {
		if ctx.Err() != nil {
			return false, "the run was cancelled while waiting for a reply", nil
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return false, "no reply within the timeout", nil
		}
		start := time.Now()
		pollCtx, cancel := context.WithDeadline(ctx, deadline)
		messages, err := d.client.TaskMessagesWait(pollCtx, d.taskID, posted.ID, min(rem, 5*time.Second))
		cancel()
		if err == nil && len(messages) > 0 {
			// The first thing said after the question is the answer to
			// it: what comes after is a conversation the run hears about
			// the ordinary way, as addenda.
			reply := messages[0]
			d.delivered.deliveredByChat(ctx, messages[:1])
			return selfrepair.Decide(reply.Body), reply.Body, nil
		}
		// A failed read, or a daemon that answered at once rather than
		// holding the long poll, waits out the rest of one interval
		// rather than asking again in a tight loop.
		if sleep := min(poll-time.Since(start), time.Until(deadline)); sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}
