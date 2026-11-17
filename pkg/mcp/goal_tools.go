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

// MockToolsOptions configures which escape-hatch and goal-mode tools are
// exposed by NewMockToolsWithOptions.
type MockToolsOptions struct {
	AutonomousGoal     bool
	MockMissingSecrets bool
}

// GoalCompletion records a validated complete_goal call made against a MockSink.
type GoalCompletion struct {
	Summary      string                  `json:"summary"`
	Deliverables []model.GoalDeliverable `json:"deliverables"`
}

// GoalAssumptions returns a copy of all record_assumption calls recorded on s.
func (s *MockSink) GoalAssumptions() []model.GoalAssumption {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.GoalAssumption, len(s.goalAssumptions))
	copy(out, s.goalAssumptions)
	return out
}

// GoalCompletion returns a copy of the latest complete_goal call recorded on s,
// or nil if none was recorded.
func (s *MockSink) GoalCompletion() *GoalCompletion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goalCompletion == nil {
		return nil
	}
	cp := *s.goalCompletion
	cp.Deliverables = make([]model.GoalDeliverable, len(s.goalCompletion.Deliverables))
	copy(cp.Deliverables, s.goalCompletion.Deliverables)
	return &cp
}

// NewGoalTools returns the Autonomous Goal Mode control tools
// (record_assumption and complete_goal).
func NewGoalTools(sink *MockSink) []Tool {
	return []Tool{
		recordAssumptionTool(sink),
		completeGoalTool(sink),
	}
}

// NewMockToolsWithOptions returns the escape-hatch tools tailored for opts.
// When opts.AutonomousGoal is true, ask_question and request_access are omitted
// (and request_secret is omitted when opts.MockMissingSecrets is true), while
// record_assumption and complete_goal are registered.
func NewMockToolsWithOptions(sink *MockSink, proposeRepos []string, opts MockToolsOptions) []Tool {
	var tools []Tool
	if !opts.AutonomousGoal {
		tools = append(tools, askQuestionTool(sink))
	}
	if !opts.AutonomousGoal || !opts.MockMissingSecrets {
		tools = append(tools, requestSecretTool(sink))
	}
	if !opts.AutonomousGoal {
		tools = append(tools, requestAccessTool(sink))
	}
	tools = append(tools,
		commentOnIssueTool(sink),
		nothingNeededTool(sink),
		writeHandoverTool(),
		compactContextTool(),
		updateBriefTool(),
		proposeTaskTool(sink, proposeRepos, "", nil),
		addReviewCommentTool(sink),
	)
	if opts.AutonomousGoal {
		tools = append(tools, NewGoalTools(sink)...)
	}
	return tools
}

func recordAssumptionTool(sink *MockSink) Tool {
	return Tool{
		Name: "record_assumption",
		Description: "Record an engineering or UX assumption made autonomously when " +
			"the task specification leaves a detail ambiguous. Use this instead of " +
			"stopping to ask a human so the decision, its rationale, and the " +
			"alternatives considered are surfaced in the task scorecard and pull request.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"topic": map[string]any{
					"type":        "string",
					"description": "Short name of the ambiguous area or decision point.",
				},
				"decision": map[string]any{
					"type":        "string",
					"description": "The concrete decision or assumption chosen.",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "Why this decision fits repository idioms and task goals.",
				},
				"alternatives": map[string]any{
					"type":        "string",
					"description": "Other options considered and why they were not chosen.",
				},
			},
			"required":             []string{"topic", "decision"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			allowed := map[string]bool{
				"topic":        true,
				"decision":     true,
				"rationale":    true,
				"alternatives": true,
			}
			for k := range args {
				if !allowed[k] {
					return Result{Text: fmt.Sprintf("record_assumption: unknown argument %q", k), IsError: true}
				}
			}
			topic, err := requiredNonEmptyStringArg(args, "topic")
			if err != nil {
				return Result{Text: "record_assumption: " + err.Error(), IsError: true}
			}
			decision, err := requiredNonEmptyStringArg(args, "decision")
			if err != nil {
				return Result{Text: "record_assumption: " + err.Error(), IsError: true}
			}
			rationale, err := optionalStringArg(args, "rationale")
			if err != nil {
				return Result{Text: "record_assumption: " + err.Error(), IsError: true}
			}
			alternatives, err := optionalStringArg(args, "alternatives")
			if err != nil {
				return Result{Text: "record_assumption: " + err.Error(), IsError: true}
			}

			if sink != nil {
				sink.mu.Lock()
				sink.goalAssumptions = append(sink.goalAssumptions, model.GoalAssumption{
					Topic:        topic,
					Decision:     decision,
					Rationale:    rationale,
					Alternatives: alternatives,
				})
				sink.mu.Unlock()
			}
			return Result{Text: fmt.Sprintf("Recorded assumption for %q: %s", topic, decision)}
		},
	}
}

func completeGoalTool(sink *MockSink) Tool {
	return Tool{
		Name: "complete_goal",
		Description: "Declare that the autonomous goal has been completed and verified. " +
			"Call this at the end of your run after building, testing, committing, and " +
			"pushing your changes, listing every requirement and the concrete observed " +
			"command or test output that verified it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "Overall summary of how the goal was achieved and verified.",
				},
				"deliverables": map[string]any{
					"type":        "array",
					"description": "Each goal requirement paired with concrete verification evidence.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"requirement": map[string]any{
								"type":        "string",
								"description": "The requirement from the task specification.",
							},
							"evidence": map[string]any{
								"type":        "string",
								"description": "Concrete observed command or test output proving the requirement is met.",
							},
							"verified": map[string]any{
								"type":        "boolean",
								"description": "True when the requirement has been verified by observed evidence.",
							},
						},
						"required":             []string{"requirement", "evidence", "verified"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"summary", "deliverables"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			for k := range args {
				if k != "summary" && k != "deliverables" {
					return Result{Text: fmt.Sprintf("complete_goal: unknown argument %q", k), IsError: true}
				}
			}
			summary, err := requiredNonEmptyStringArg(args, "summary")
			if err != nil {
				return Result{Text: "complete_goal: " + err.Error(), IsError: true}
			}
			rawDeliverables, ok := args["deliverables"]
			if !ok || rawDeliverables == nil {
				return Result{Text: "complete_goal: deliverables is required", IsError: true}
			}
			items, ok := rawDeliverables.([]any)
			if !ok {
				return Result{Text: "complete_goal: deliverables must be an array", IsError: true}
			}
			if len(items) == 0 {
				return Result{Text: "complete_goal: deliverables must not be empty", IsError: true}
			}

			unverified := 0
			deliverables := make([]model.GoalDeliverable, 0, len(items))
			for i, item := range items {
				obj, ok := item.(map[string]any)
				if !ok {
					return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d] must be an object", i), IsError: true}
				}
				for k := range obj {
					if k != "requirement" && k != "evidence" && k != "verified" {
						return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d]: unknown field %q", i, k), IsError: true}
					}
				}
				reqStr, err := requiredNonEmptyStringArg(obj, "requirement")
				if err != nil {
					return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d]: %v", i, err), IsError: true}
				}
				evStr, err := requiredNonEmptyStringArg(obj, "evidence")
				if err != nil {
					return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d]: %v", i, err), IsError: true}
				}
				verRaw, hasVer := obj["verified"]
				if !hasVer {
					return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d]: verified is required", i), IsError: true}
				}
				verBool, ok := verRaw.(bool)
				if !ok {
					return Result{Text: fmt.Sprintf("complete_goal: deliverables[%d]: verified must be a boolean", i), IsError: true}
				}
				if !verBool {
					unverified++
				}
				deliverables = append(deliverables, model.GoalDeliverable{
					Requirement: reqStr,
					Evidence:    evStr,
					Verified:    verBool,
				})
			}

			if sink != nil {
				sink.mu.Lock()
				sink.goalCompletion = &GoalCompletion{
					Summary:      summary,
					Deliverables: deliverables,
				}
				sink.mu.Unlock()
			}
			if unverified > 0 {
				return Result{
					Text:    fmt.Sprintf("complete_goal: recorded completion but %d deliverable(s) have verified=false; complete and verify all deliverables before finishing", unverified),
					IsError: true,
				}
			}
			return Result{Text: fmt.Sprintf("Recorded goal completion with %d deliverable(s).", len(deliverables))}
		},
	}
}

func requiredNonEmptyStringArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("%s is required", key)
	}
	str, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	trimmed := strings.TrimSpace(str)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return trimmed, nil
}

func optionalStringArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", nil
	}
	str, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(str), nil
}
