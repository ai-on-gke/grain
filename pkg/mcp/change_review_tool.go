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
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/change"
)

// The review tools of a run whose task's change is a CL (changelists.md
// §5, G3-5): reply_to_pull_request_comment and resolve_review_thread
// answer one of the CL's threads, by the ID the run was told
// ("cl/<number>:<comment id>"), as a draft the provider publishes when
// the run ends. A provider with no write path to the CL's threads answers
// degraded, and the daemon posts the reply in the task's conversation
// instead. Registered in place of the pull-request tools of the same
// names, never beside them.

// ChangeReviewer is how these tools reach the task's change.Replier: the
// daemon, over its REST API (pkg/daemon's DaemonChangeReview). Which task
// is fixed at process start, never by a tool call.
type ChangeReviewer interface {
	ReplyToChangeThread(ctx context.Context, thread, body string) (change.ReplyResult, error)
	ResolveChangeThread(ctx context.Context, thread string) (change.ReplyResult, error)
}

// NewChangeReviewTools is the two review-thread tools of a changelist task.
func NewChangeReviewTools(reviewer ChangeReviewer) []Tool {
	return []Tool{changeReplyTool(reviewer), changeResolveTool(reviewer)}
}

var changeThreadSchema = map[string]any{
	"type":        "string",
	"description": "The CL review thread, as the note that sent this run or pull_request_status names it (cl/<number>:<comment id>).",
}

func changeReplyTool(reviewer ChangeReviewer) Tool {
	return Tool{
		Name: "reply_to_pull_request_comment",
		Description: "Answer one review thread on this task's CL. The reply is saved as a " +
			"draft on the CL and published, with this run's other replies, when the run " +
			"ends -- use it to answer a reviewer, not to report to grain (that is " +
			"comment_on_issue).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"thread_id": changeThreadSchema,
				"body":      map[string]any{"type": "string", "description": "The reply text."},
			},
			"required": []string{"thread_id", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			thread, _ := argString(args, "thread_id")
			body, _ := argString(args, "body")
			if strings.TrimSpace(body) == "" {
				return Result{Text: "body is required and must not be empty", IsError: true}
			}
			res, err := reviewer.ReplyToChangeThread(ctx, thread, body)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: renderChangeReply(res, thread, "Drafted a reply to")}
		},
	}
}

func changeResolveTool(reviewer ChangeReviewer) Tool {
	return Tool{
		Name: "resolve_review_thread",
		Description: "Mark one review thread on this task's CL resolved once it is dealt " +
			"with. Saved as a draft on the CL and published when this run ends.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"thread_id": changeThreadSchema},
			"required":             []string{"thread_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			thread, _ := argString(args, "thread_id")
			res, err := reviewer.ResolveChangeThread(ctx, thread)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: renderChangeReply(res, thread, "Drafted resolving")}
		},
	}
}

func renderChangeReply(res change.ReplyResult, thread, did string) string {
	if res.Thread != "" {
		thread = res.Thread
	}
	if res.Degraded {
		text := fmt.Sprintf("grain cannot write to the CL's threads on this deployment, so nothing was "+
			"saved on thread %s: it was posted in this task's conversation instead.", thread)
		if res.Note != "" {
			text += " " + res.Note
		}
		return text
	}
	text := fmt.Sprintf("%s thread %s", did, thread)
	if res.Draft != "" {
		text += fmt.Sprintf(" (draft %s)", res.Draft)
	}
	text += ". It is published when this run ends."
	if res.Note != "" {
		text += " " + res.Note
	}
	return text
}
