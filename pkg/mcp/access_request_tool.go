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

	"github.com/ai-on-gke/grain/pkg/model"
)

// AccessRequest is one request_access call: what a run asked a person
// for, and why.
type AccessRequest struct {
	Summary string
	Reason  string
	Items   []model.AccessRequestItem
}

// AccessRequestChecker judges what a request_access call names against
// the run's own task -- an unknown capability, arguments its schema
// refuses, a repository this deployment does not work with, something
// the task holds already -- the checks the task's grants editor makes.
// pkg/daemon's implementation asks the daemon (ui's POST
// /api/tasks/{id}/access-request/check).
type AccessRequestChecker interface {
	CheckAccessRequest(ctx context.Context, items []model.AccessRequestItem) error
}

// requestAccessTool is ask_question for the one answer a question used
// to need a person's own editing to act on: more access for this task.
// It parks the task the same way (orchestrator.relayParkingCalls), and
// what the person is offered is a card listing exactly what was asked
// for with Grant and Deny, rather than a reply box -- granting applies
// the items to the task and re-queues it, denying hands the next run the
// person's reason.
func requestAccessTool(sink *MockSink) Tool {
	return Tool{
		Name: "request_access",
		Description: "Ask a person to give this task access it does not hold and its work " +
			"needs: another repository to read or to write, a capability grant (an in-process " +
			"capability, an api:* scope, or a capability server's method with its with: " +
			"arguments, such as gcp#gcp-key {permission_set} or repo#write {refs, force}), or " +
			"open network access. This ends your turn the way ask_question does: when this " +
			"run finishes, grain parks the task on the request and shows the person a card " +
			"listing exactly what you asked for and why, with Grant and Deny. Granting applies " +
			"the items they choose to this task and queues it again, and the next run is told " +
			"what was granted and what was declined; denying queues it again with their " +
			"reason, and the next run must change its approach rather than ask again. Each " +
			"item is checked now, as the task's grants editor checks it -- an unknown " +
			"capability, arguments its schema refuses, a repository this deployment does not " +
			"work with, or something the task holds already is refused, so fix the request " +
			"and call again. Never ask for grain#ci: a repository's CI Mode grants it, " +
			"never a person, and `grain ci wait` and `grain pr open` need no grant; to read " +
			"CI runs and logs, ask for https://grain.dev/capabilities/api#ci-read with {repo}. " +
			"Dangerous scopes are a person's to grant, which is what this " +
			"asks for. Only the first request_access call in a run is relayed. Do not use " +
			"ask_question to ask for access: a reply there grants nothing. After calling " +
			"this, do not take any further actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"summary": questionSummarySchema("the access you need and why, such as " +
					"\"Grant write to acme/lib so the fix can land in both repos\""),
				"reason": map[string]any{
					"type": "string",
					"description": "Why the work needs it: what you will do with each item and " +
						"why the task cannot finish without it. The person deciding is shown " +
						"this verbatim beside the items.",
				},
				"requests": map[string]any{
					"type":     "array",
					"minItems": 1,
					"description": "What to grant, each one of: {kind: \"read\", repo: " +
						"\"owner/name\"}; {kind: \"write\", repo: \"owner/name\", base?: " +
						"\"branch\"}; {kind: \"grant\", capability: \"<id or ref>\", with?: " +
						"{...arguments}}; {kind: \"network\", access: \"open\"}.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"kind": map[string]any{
								"type": "string",
								"enum": []string{model.AccessRead, model.AccessWrite, model.AccessGrant, model.AccessNetwork},
							},
							"repo":       map[string]any{"type": "string", "description": "owner/name, for read and write."},
							"base":       map[string]any{"type": "string", "description": "For write: the branch to land work on; omit for the repository's default."},
							"capability": map[string]any{"type": "string", "description": "For grant: an in-process capability id, an api:<scope> grant, or a capability server method's <capability>#<method> reference."},
							"with":       map[string]any{"type": "object", "description": "For grant: the method's with: arguments."},
							"access":     map[string]any{"type": "string", "enum": []string{"open"}, "description": "For network: \"open\"."},
						},
						"required": []string{"kind"},
					},
				},
			},
			"required": []string{"summary", "reason", "requests"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			summary, err := questionSummaryArg(args)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if summary == "" {
				return Result{Text: "summary is required", IsError: true}
			}
			reason, _ := argString(args, "reason")
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return Result{Text: "reason is required: the person deciding is told nothing else " +
					"about why the work needs this", IsError: true}
			}
			items, err := model.ParseAccessRequestItems(args["requests"])
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			sink.mu.Lock()
			checker := sink.accessChecker
			sink.mu.Unlock()
			if checker != nil {
				if err := checker.CheckAccessRequest(ctx, items); err != nil {
					return Result{Text: "This request cannot be relayed as it stands: " + err.Error() +
						". Fix it and call request_access again.", IsError: true}
				}
			}
			sink.mu.Lock()
			// First call wins, matching what is relayed:
			// orchestrator.relayParkingCalls reads the first non-error
			// request_access call off agent.Result.ToolCalls.
			if sink.accessRequest == nil {
				sink.accessRequest = &AccessRequest{Summary: summary, Reason: reason, Items: items}
			}
			sink.mu.Unlock()
			labels := make([]string, 0, len(items))
			for _, it := range items {
				labels = append(labels, it.Label())
			}
			return Result{Text: fmt.Sprintf("Recorded. When this run finishes, grain parks the task on "+
				"this request and asks a person to grant or deny: %s. This ends your turn: stop here "+
				"and take no further actions.", strings.Join(labels, "; "))}
		},
	}
}
