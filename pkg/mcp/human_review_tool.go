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
)

// HumanReviewCategories is the category vocabulary request_human_review
// accepts (model.HumanReviewCategories, restated so this package's schema
// needs no import of the store's).
var HumanReviewCategories = []string{"product-decision", "risk", "security", "intent-unclear", "other"}

// HumanReviewReport is what one request_human_review call did: the task
// now held, whether the reason was new, and every reason it is held for.
type HumanReviewReport struct {
	Task    string
	Added   bool
	Reasons []string
}

// HumanReviewRequester holds the change this run reviews for a person's
// decision. Like StatusReporter it names no task: which review is asking
// is fixed at process start (`grain mcpserver -task`), so a run can only
// ever hold the change its own review was filed against. The real
// implementation asks the daemon over its REST API
// (POST /api/tasks/{id}/human-review, pkg/daemon's mcpserver.go).
type HumanReviewRequester interface {
	RequestHumanReview(ctx context.Context, reason, details, category string) (HumanReviewReport, error)
}

// NewHumanReviewTools returns request_human_review, offered to review
// runs only (security reviews and template reviews). requester nil
// returns the tool anyway, refusing every call -- what lets each agent
// framework's allowedTools enumerate it without a live requester.
func NewHumanReviewTools(requester HumanReviewRequester) []Tool {
	return []Tool{humanReviewTool(requester)}
}

func humanReviewTool(requester HumanReviewRequester) Tool {
	return Tool{
		Name: "request_human_review",
		Description: "Hold the change you are reviewing for a person's decision, when the code is fine " +
			"(or fixed) but something about it is not an agent's call: a product or design decision, a " +
			"risky migration, a security-sensitive behaviour change, or a brief whose intent is unclear. " +
			"The change's auto-merge is turned off and it waits in its owner's inbox, with your reason on a " +
			"banner, until a person presses Submit or Release hold -- a clean CI run, a later clean review " +
			"or a new push do not release it. Your review still finishes normally; this is not a failure. " +
			"Do not use it for code defects: fix those, or leave a finding (add_review_comment) for what " +
			"an agent can fix. Calling it again adds another reason to the same hold; repeating a reason " +
			"adds nothing.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "One short line: what a person has to decide. Shown in the inbox and on the task's banner.",
				},
				"details": map[string]any{
					"type":        "string",
					"description": "Markdown: the context a person needs to decide, and the answer you recommend.",
				},
				"category": map[string]any{
					"type":        "string",
					"enum":        HumanReviewCategories,
					"description": "What kind of decision it is. Defaults to other.",
				},
			},
			"required": []string{"reason"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if requester == nil {
				return Result{Text: "This run cannot ask for human review: it is not a review run, or it has no " +
					"route back to the grain daemon that dispatched it. Put the concern in your closing note.",
					IsError: true}
			}
			reason, _ := argString(args, "reason")
			details, _ := argString(args, "details")
			category, _ := argString(args, "category")
			if strings.TrimSpace(reason) == "" {
				return Result{Text: "reason is required: one short line saying what a person has to decide", IsError: true}
			}
			report, err := requester.RequestHumanReview(ctx, reason, details, category)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			did := "Held"
			if !report.Added {
				did = "Already held, for that same reason:"
			}
			return Result{Text: fmt.Sprintf("%s task %s for a person's review. It will not merge itself until a "+
				"person presses Submit or Release hold. Reasons on the hold now: %s. Finish your review as usual.",
				did, report.Task, strings.Join(report.Reasons, "; "))}
		},
	}
}
